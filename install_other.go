//go:build !linux || android

package main

// InstallState mirrors the Linux type so the frontend can call
// GetInstallState unconditionally.
//
// Nowhere else needs it: Windows runs the .exe from wherever the user
// keeps it and has no desktop-entry concept to register, macOS installs
// by dragging a bundle, and on Android the APK is the install.
type InstallState struct {
	Kind         string `json:"kind"`
	ExePath      string `json:"exe_path"`
	TargetPath   string `json:"target_path"`
	CanOffer     bool   `json:"can_offer"`
	DesktopEntry bool   `json:"desktop_entry"`
}

// GetInstallState always reports "native": nothing to offer.
func (a *App) GetInstallState() InstallState {
	return InstallState{Kind: "native"}
}

// InstallToUserPrefix is a no-op off Linux.
func (a *App) InstallToUserPrefix() (string, error) {
	return "", nil
}
