//go:build android || ios

// Foreground-service control: keep the process alive while SSH sessions are
// connected. Android suspends a backgrounded app's process, which drops the
// SSH sockets; a foreground service (with an ongoing notification) prevents
// that. syncForegroundService starts the service when there is at least one
// live session and stops it when the last one closes. Called after every
// pool add/remove.

package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var fgServiceMu sync.Mutex
var fgServiceRunning bool
var fgLoginKeepAlive bool

// setLoginKeepAlive holds the foreground service up for the duration of an
// interactive opkssh OIDC login, independent of the live-session count.
// Wired to ssh.LoginKeepAliveHook in configurePlatform. Without it the
// login usually runs with zero sessions -> no service -> android freezes
// the backgrounded process's network while the user is in the browser, and
// the token exchange dies on its first DNS lookup.
func (a *App) setLoginKeepAlive(active bool) {
	fgServiceMu.Lock()
	fgLoginKeepAlive = active
	fgServiceMu.Unlock()
	a.syncForegroundService()
}

// syncForegroundService reconciles the keep-alive service with the current
// desired state (live sessions, or an OIDC login in flight). Idempotent and
// cheap; safe to call on every connect / disconnect.
func (a *App) syncForegroundService() {
	if a.pool == nil {
		return
	}
	n := len(a.pool.IDs())

	fgServiceMu.Lock()
	defer fgServiceMu.Unlock()

	if n > 0 || fgLoginKeepAlive {
		text := "Signing in..."
		if n == 1 {
			text = "1 SSH session running"
		} else if n > 1 {
			text = fmt.Sprintf("%d SSH sessions running", n)
		}
		payload, _ := json.Marshal(map[string]string{"title": "ssh-tool", "text": text})
		application.Android.StartForegroundService(string(payload))
		fgServiceRunning = true
		return
	}
	if fgServiceRunning {
		application.Android.StopForegroundService()
		fgServiceRunning = false
	}
}
