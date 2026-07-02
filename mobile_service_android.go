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

// syncForegroundService reconciles the keep-alive service with the current
// live-session count. Idempotent and cheap; safe to call on every connect /
// disconnect.
func (a *App) syncForegroundService() {
	if a.pool == nil {
		return
	}
	n := len(a.pool.IDs())

	fgServiceMu.Lock()
	defer fgServiceMu.Unlock()

	if n > 0 {
		text := "1 SSH session running"
		if n > 1 {
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
