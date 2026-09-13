//go:build (!linux && !windows) || android

package main

// InstallState mirrors the Linux type so the frontend can call
// GetInstallState unconditionally.
//
// Linux and Windows have real implementations. This covers macOS, where
// an app is installed by dragging the bundle to /Applications and there
// is nothing for us to add, and Android, where the APK is the install.
type InstallState struct {
	Kind             string `json:"kind"`
	ExePath          string `json:"exe_path"`
	TargetPath       string `json:"target_path"`
	CanOffer         bool   `json:"can_offer"`
	DesktopEntry     bool   `json:"desktop_entry"`
	InstalledVersion string `json:"installed_version"`
	Replaces         bool   `json:"replaces"`
}

// GetInstallState always reports "native": nothing to offer.
func (a *App) GetInstallState() InstallState {
	return InstallState{Kind: "native"}
}

// RelaunchFromInstall is a no-op off Linux; nothing offers an install
// there, so nothing ever asks for this restart.
func (a *App) RelaunchFromInstall(path string) error { return nil }

// UninstallUserPrefix is a no-op where there is no desktop integration
// to remove.
func (a *App) UninstallUserPrefix() (bool, error) { return false, nil }

// InstallToUserPrefix is a no-op off Linux.
func (a *App) InstallToUserPrefix() (string, error) {
	return "", nil
}
