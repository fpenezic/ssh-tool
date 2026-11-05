package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
)

// Login hold: a VPN login that a proxy client started on its own (DBeaver
// retrying through the profile's proxy after the machine woke up) and that
// did not finish is not tried again by the next client. Each try is a new
// login, and with a push second factor (Duo) a new push on the user's
// phone: unanswered, the gateway asks again, the prompt here times out, the
// helper exits and the client's next retry starts it all over, a push every
// couple of minutes for as long as the client keeps retrying. Held, proxy
// connections through the profile fail at once until the tunnel comes up
// some other way: Connect on the profile, or a connection the user opens
// in the app. Only proxy-started tunnels check the hold; whatever the user
// starts goes ahead and, once the tunnel is up, lifts it.
var loginHolds = struct {
	sync.Mutex
	m map[string]*loginHold
	// ups counts successful starts per profile: a proxy start that
	// failed while a newer one succeeded must not hold the profile.
	ups   map[string]uint64
	start map[string]*sync.Mutex
}{m: map[string]*loginHold{}, ups: map[string]uint64{}, start: map[string]*sync.Mutex{}}

type loginHold struct {
	since    time.Time
	refused  int
	lastNote time.Time
}

var errLoginHeld = errors.New("VPN login needed: the last login started through a proxy did not finish; Connect the profile to log in")

type proxyStartKey struct{}

// withProxyStart marks a dial made for a proxy client (a jump-host route
// of the app proxy), so a tunnel it needs is started under the hold.
func withProxyStart(ctx context.Context) context.Context {
	return context.WithValue(ctx, proxyStartKey{}, true)
}

// ensureTunnelCtx is ensureTunnel, held when ctx comes from a proxy client.
func (a *App) ensureTunnelCtx(ctx context.Context, row *store.NetworkProfile) (tunnelDialer, error) {
	if on, _ := ctx.Value(proxyStartKey{}).(bool); on {
		return a.ensureTunnelFromProxy(row)
	}
	return a.ensureTunnel(row)
}

// ensureTunnelFromProxy starts a profile's tunnel for a proxy client. One
// proxy start per profile at a time, so the hold a failed login sets is in
// place before the next client looks (concurrent clients wait here, as
// they would on the pending start).
func (a *App) ensureTunnelFromProxy(row *store.NetworkProfile) (tunnelDialer, error) {
	if a.tunnelStatus(row.ID).Running {
		return a.ensureTunnel(row)
	}
	loginHolds.Lock()
	mu := loginHolds.start[row.ID]
	if mu == nil {
		mu = &sync.Mutex{}
		loginHolds.start[row.ID] = mu
	}
	loginHolds.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if !a.tunnelStatus(row.ID).Running && a.loginHeld(row.ID, row.Name) {
		return nil, errLoginHeld
	}
	gen := loginUps(row.ID)
	t, err := a.ensureTunnel(row)
	if err != nil {
		a.holdAfterLogin(row.ID, row.Name, err, gen)
	}
	return t, err
}

func loginUps(id string) uint64 {
	loginHolds.Lock()
	defer loginHolds.Unlock()
	return loginHolds.ups[id]
}

// loginHeld reports whether proxy starts of the profile's tunnel are held,
// counting the refusal for the log.
func (a *App) loginHeld(id, name string) bool {
	loginHolds.Lock()
	defer loginHolds.Unlock()
	h := loginHolds.m[id]
	if h == nil {
		return false
	}
	h.refused++
	if time.Since(h.lastNote) >= time.Minute {
		h.lastNote = time.Now()
		log.Printf("network: proxy %q: VPN login needed, %d connection(s) refused since %s; Connect the profile to log in", name, h.refused, h.since.Format("15:04:05"))
	}
	return true
}

// holdAfterLogin puts the profile on hold when err is a login that did not
// finish and no start succeeded since gen, and tells the user once.
func (a *App) holdAfterLogin(id, name string, err error, gen uint64) {
	var le *tunnelhelper.LoginError
	if !errors.As(err, &le) {
		return
	}
	loginHolds.Lock()
	if loginHolds.ups[id] != gen {
		loginHolds.Unlock()
		return
	}
	_, already := loginHolds.m[id]
	if !already {
		loginHolds.m[id] = &loginHold{since: time.Now(), lastNote: time.Now()}
	}
	loginHolds.Unlock()
	if already {
		return
	}
	log.Printf("network: proxy %q: the VPN login did not finish (%v); no new login until the profile is connected again", name, err)
	msg := fmt.Sprintf("VPN %s: the login did not finish. Connections through its proxy wait until you press Connect on the profile.", name)
	EventsEmit("forward_notice", map[string]string{"level": "err", "message": msg})
	a.SendPromptNotification("VPN login needed", msg)
	EventsEmit("network_tunnel_changed", id)
}

// tunnelStarted records a successful start and lifts the hold.
func tunnelStarted(id string) {
	loginHolds.Lock()
	loginHolds.ups[id]++
	h := loginHolds.m[id]
	delete(loginHolds.m, id)
	loginHolds.Unlock()
	if h != nil {
		log.Printf("network: profile %s is up again, proxy login hold lifted (%d connection(s) refused while held)", id, h.refused)
	}
}
