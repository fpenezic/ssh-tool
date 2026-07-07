//go:build android || ios

package main

// Toast notifications for blocking desktop prompts don't apply on mobile (no
// MCP bridge, no desktop-style modals). Stub keeps the shared IPC surface.
func (a *App) SendPromptNotification(_ , _ string) {}

func requestNotificationAuth() {}
