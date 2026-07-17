//go:build android || ios

package main

// Toast notifications for blocking desktop prompts don't apply on mobile (no
// MCP bridge, no desktop-style modals). Stub keeps the shared IPC surface.
func (a *App) SendPromptNotification(_, _ string) {}

// SendUpdateNotification is a no-op on mobile: app updates go through the
// platform store, not this in-app updater.
func (a *App) SendUpdateNotification(_, _ string) {}

func requestNotificationAuth() {}
