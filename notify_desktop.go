//go:build !android && !ios

package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// notifier is the shared Windows/Linux/macOS toast notification service,
// registered in buildApp. Used to surface blocking prompts (MCP approval,
// host-key TOFU) as an OS notification when the app is backgrounded, so a
// prompt you're waiting on in another window doesn't sit unseen. The taskbar
// flash (RequestAttention) is the always-on complement; the toast is opt-out
// via the notifications_enabled setting (default on).
var notifier = notifications.New()

// SendPromptNotification posts an OS toast for a blocking prompt. No-op when
// the window is focused (you're already looking at ssh-tool), when the toast
// setting is off, or when the platform/authorization refuses. Called from the
// frontend when an approval / host-key modal appears; the taskbar flash is
// handled separately by RequestAttention.
func (a *App) SendPromptNotification(title, body string) {
	if a.windowFocused.Load() {
		log.Printf("notification: skipped (window focused): %s", title)
		return
	}
	if !a.notificationsEnabled() {
		log.Printf("notification: skipped (disabled): %s", title)
		return
	}
	if notifier == nil {
		log.Printf("notification: skipped (no notifier): %s", title)
		return
	}
	if err := notifier.SendNotification(notifications.NotificationOptions{
		Title: title,
		Body:  body,
	}); err != nil {
		log.Printf("notification: send failed: %v", err)
		return
	}
	log.Printf("notification: sent %q", title)
}

// notificationsEnabled reads the toggle (default true when unset).
func (a *App) notificationsEnabled() bool {
	if a.db == nil {
		return false
	}
	v, ok, err := a.db.GetSetting("notifications_enabled")
	if err != nil || !ok || v == "" {
		return true // default on
	}
	return v == "1" || v == "true"
}

// requestNotificationAuth asks the OS for notification permission once at
// startup (macOS needs it; Windows/Linux are no-ops that return true). Best
// effort - a denial just means SendNotification later fails quietly.
func requestNotificationAuth() {
	if notifier == nil {
		return
	}
	ok, err := notifier.RequestNotificationAuthorization()
	if err != nil {
		log.Printf("notification auth: error: %v", err)
		return
	}
	log.Printf("notification auth: granted=%v", ok)
}
