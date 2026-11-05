package main

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

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
// in the app. Only the proxies check the hold; whatever the user starts
// goes ahead and, once the tunnel is up, lifts it.
var loginHolds = struct {
	sync.Mutex
	m map[string]*loginHold
}{m: map[string]*loginHold{}}

type loginHold struct {
	since    time.Time
	refused  int
	lastNote time.Time
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
// finish, and tells the user once.
func (a *App) holdAfterLogin(id, name string, err error) {
	var le *tunnelhelper.LoginError
	if !errors.As(err, &le) {
		return
	}
	loginHolds.Lock()
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

// clearLoginHold lifts the hold once the tunnel is up.
func clearLoginHold(id string) {
	loginHolds.Lock()
	h := loginHolds.m[id]
	delete(loginHolds.m, id)
	loginHolds.Unlock()
	if h != nil {
		log.Printf("network: profile %s is up again, proxy login hold lifted (%d connection(s) refused while held)", id, h.refused)
	}
}
