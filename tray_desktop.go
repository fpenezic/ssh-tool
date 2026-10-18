//go:build !android && !ios

package main

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// trayController owns the tray icon's menu: Show window, the VPN
// profiles and tunnels with their state (a click starts or stops one),
// favorite and recent connections, Lock vault and Quit.
//
// The menu is rebuilt from scratch whenever what it shows changes: a
// poll every trayRefreshEvery compares a signature of the state, and
// every tray action refreshes right after itself. A fresh menu goes in
// through SetMenu, which every platform implements as a full rebuild
// (on Linux it re-exports the dbusmenu tree); mutating the live menu
// in place is not reliably reflected there.
type trayController struct {
	a    *App
	app  *application.App
	tray *application.SystemTray

	mu   sync.Mutex
	menu *application.Menu
	sig  string
	// busy holds the profiles and tunnels a tray click is starting or
	// stopping right now (a VPN login can wait on a prompt for minutes).
	busy map[string]bool
}

const (
	trayRefreshEvery = 3 * time.Second
	// trayMaxFavorites keeps the submenu usable for someone who stars
	// half the tree; the rest are one click away in the window.
	trayMaxFavorites = 20
	// trayRecent matches the sidebar's Quick access: the most recently
	// connected, favorites left out since they have their own submenu.
	trayRecent = 10
)

type trayProfile struct {
	id, name      string
	running, busy bool
}

type trayTunnel struct {
	id, name, state string
	busy            bool
}

type trayFavorite struct {
	id, name string
}

type trayState struct {
	profiles  []trayProfile
	tunnels   []trayTunnel
	favorites []trayFavorite
	recent    []trayFavorite
	locked    bool
}

func newTrayController(a *App, app *application.App, tray *application.SystemTray) *trayController {
	t := &trayController{a: a, app: app, tray: tray, busy: map[string]bool{}}
	t.refresh(true)
	go func() {
		for range time.Tick(trayRefreshEvery) {
			start := time.Now()
			t.refresh(false)
			if d := time.Since(start); d > time.Second {
				log.Printf("tray: refresh took %s", d.Round(time.Millisecond))
			}
		}
	}()
	return t
}

// state reads what the menu shows. Before the store is open (vault
// still locked on first start) it is just the fixed items.
func (t *trayController) state() trayState {
	a := t.a
	t.mu.Lock()
	busy := make(map[string]bool, len(t.busy))
	for id := range t.busy {
		busy[id] = true
	}
	t.mu.Unlock()
	st := trayState{locked: a.vault == nil || a.vault.Status().Kind != creds.StatusUnlocked}
	if a.db == nil {
		return st
	}
	if rows, err := a.db.ListNetworkProfiles(); err == nil {
		for _, r := range rows {
			running := (a.wgman != nil && a.wgman.Get(r.ID) != nil) || (a.nbman != nil && a.nbman.Get(r.ID) != nil)
			st.profiles = append(st.profiles, trayProfile{id: r.ID, name: r.Name, running: running, busy: busy[r.ID]})
		}
		sort.Slice(st.profiles, func(i, j int) bool {
			return strings.ToLower(st.profiles[i].name) < strings.ToLower(st.profiles[j].name)
		})
	}
	if list, err := a.TunnelsList(); err == nil {
		for _, tn := range list {
			st.tunnels = append(st.tunnels, trayTunnel{id: tn.ConnectionID, name: tn.Name, state: tn.State, busy: busy[tn.ConnectionID]})
		}
	}
	if favs, err := a.ConnectionsFavorites(); err == nil {
		for _, c := range favs {
			st.favorites = append(st.favorites, trayFavorite{id: c.ID, name: c.Name})
		}
		sort.Slice(st.favorites, func(i, j int) bool {
			return strings.ToLower(st.favorites[i].name) < strings.ToLower(st.favorites[j].name)
		})
		if len(st.favorites) > trayMaxFavorites {
			st.favorites = st.favorites[:trayMaxFavorites]
		}
	}
	if conns, err := a.db.ListConnections(nil); err == nil {
		times := a.recentTimes()
		var recent []store.Connection
		for _, c := range conns {
			if _, ok := times[c.ID]; ok && !c.Favorite {
				recent = append(recent, c)
			}
		}
		sort.Slice(recent, func(i, j int) bool { return times[recent[i].ID] > times[recent[j].ID] })
		if len(recent) > trayRecent {
			recent = recent[:trayRecent]
		}
		for _, c := range recent {
			st.recent = append(st.recent, trayFavorite{id: c.ID, name: c.Name})
		}
	}
	return st
}

func (s trayState) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "L%v|", s.locked)
	for _, p := range s.profiles {
		fmt.Fprintf(&b, "p%s=%s:%v:%v|", p.id, p.name, p.running, p.busy)
	}
	for _, tn := range s.tunnels {
		fmt.Fprintf(&b, "t%s=%s:%s:%v|", tn.id, tn.name, tn.state, tn.busy)
	}
	for _, f := range s.favorites {
		fmt.Fprintf(&b, "f%s=%s|", f.id, f.name)
	}
	for _, f := range s.recent {
		fmt.Fprintf(&b, "r%s=%s|", f.id, f.name)
	}
	return b.String()
}

// refresh rebuilds the menu when the state changed since the last
// build (always, with force).
func (t *trayController) refresh(force bool) {
	st := t.state()
	sig := st.signature()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !force && sig == t.sig {
		return
	}
	t.sig = sig
	up := 0
	for _, p := range st.profiles {
		if p.running {
			up++
		}
	}
	log.Printf("tray: menu rebuilt (profiles %d, up %d, tunnels %d, favorites %d, recent %d, locked %v)",
		len(st.profiles), up, len(st.tunnels), len(st.favorites), len(st.recent), st.locked)
	old := t.menu
	t.menu = t.build(st)
	t.tray.SetMenu(t.menu)
	if old != nil {
		old.Clear()
	}
}

// tunnelRunning is a tunnel state that a click should stop rather than
// start: up, or still trying to come up.
func tunnelRunning(state string) bool {
	switch state {
	case tunnelConnected, tunnelConnecting, tunnelReconnecting:
		return true
	}
	return false
}

func (t *trayController) build(st trayState) *application.Menu {
	m := t.app.Menu.New()
	m.Add("Show window").OnClick(func(_ *application.Context) { t.a.ShowAndFocusMainWindow() })

	if len(st.profiles) > 0 || len(st.tunnels) > 0 || len(st.favorites) > 0 || len(st.recent) > 0 {
		m.AddSeparator()
	}
	if len(st.profiles) > 0 {
		sub := m.AddSubmenu("VPN profiles")
		for _, p := range st.profiles {
			p := p
			if p.busy {
				sub.Add(p.name + " (working...)").SetEnabled(false)
				continue
			}
			sub.AddCheckbox(p.name, p.running).OnClick(func(_ *application.Context) {
				go t.toggleProfile(p)
			})
		}
	}
	if len(st.tunnels) > 0 {
		sub := m.AddSubmenu("Tunnels")
		for _, tn := range st.tunnels {
			tn := tn
			if tn.busy {
				sub.Add(tn.name + " (working...)").SetEnabled(false)
				continue
			}
			label := tn.name
			if tn.state == tunnelError {
				label += " (failed)"
			}
			sub.AddCheckbox(label, tunnelRunning(tn.state)).OnClick(func(_ *application.Context) {
				go t.toggleTunnel(tn)
			})
		}
	}
	t.addConnections(m, "Favorites", st.favorites)
	t.addConnections(m, "Recent", st.recent)

	m.AddSeparator()
	lock := m.Add("Lock vault")
	lock.SetEnabled(!st.locked)
	lock.OnClick(func(_ *application.Context) {
		// The frontend locks, so its unlock gate and status bar follow.
		EventsEmit("tray_lock_vault", nil)
		go func() {
			time.Sleep(500 * time.Millisecond)
			t.refresh(false)
		}()
	})
	m.AddSeparator()
	m.Add("Quit").OnClick(func(_ *application.Context) {
		// Same path as closing the window: ShouldQuit asks first when
		// sessions are open (it shows the window for the prompt).
		t.app.Quit()
	})
	return m
}

// begin marks id busy and rebuilds the menu; the returned func clears
// it and rebuilds again. Both rebuilds are forced: a native checkbox
// flips its own tick when clicked, so after a failed start the state
// is unchanged while the menu is not - a signature check would keep
// the wrong tick.
func (t *trayController) begin(id string) func() {
	t.mu.Lock()
	t.busy[id] = true
	t.mu.Unlock()
	t.refresh(true)
	return func() {
		t.mu.Lock()
		delete(t.busy, id)
		t.mu.Unlock()
		t.refresh(true)
	}
}

// addConnections adds a submenu of connections (none: no submenu); a
// click brings the window up and connects like a double-click in the
// tree - that flow (tabs, prompts, errors) lives in the frontend.
func (t *trayController) addConnections(m *application.Menu, title string, list []trayFavorite) {
	if len(list) == 0 {
		return
	}
	sub := m.AddSubmenu(title)
	for _, f := range list {
		f := f
		sub.Add(f.name).OnClick(func(_ *application.Context) {
			t.a.ShowAndFocusMainWindow()
			EventsEmit("tray_connect", f.id)
		})
	}
}

func (t *trayController) toggleProfile(p trayProfile) {
	defer t.begin(p.id)()
	if p.running {
		t.a.NetworkProfileStop(p.id)
		return
	}
	if _, err := t.a.NetworkProfileTest(p.id); err != nil {
		log.Printf("tray: start profile %s: %v", p.name, err)
		t.notify("Could not start "+p.name, err.Error())
	}
	EventsEmit("network_tunnel_changed", p.id)
}

func (t *trayController) toggleTunnel(tn trayTunnel) {
	defer t.begin(tn.id)()
	var err error
	if tunnelRunning(tn.state) {
		err = t.a.TunnelStop(tn.id)
	} else {
		err = t.a.TunnelStart(tn.id)
	}
	if err != nil {
		log.Printf("tray: tunnel %s: %v", tn.name, err)
		t.notify("Tunnel "+tn.name, err.Error())
	}
}

// notify reports a failed tray action. The window is usually hidden
// then, so unlike prompt notifications it does not check focus.
func (t *trayController) notify(title, body string) {
	if notifier == nil || !t.a.notificationsEnabled() {
		return
	}
	if err := notifier.SendNotification(notifications.NotificationOptions{
		ID:    "ssh-tool-tray-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Title: title,
		Body:  body,
	}); err != nil {
		log.Printf("notification: send failed: %v", err)
	}
}
