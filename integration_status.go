//go:build !android && !ios

package main

// IntegrationStatus describes one desktop registration - the
// ssh-tool:// handler, the file-manager context menu - and whether it
// still points at this binary.
//
// Stale is the part that matters. Every registration stores an absolute
// path to the executable, and installing to ~/.local/bin (or
// %LOCALAPPDATA%\Programs) moves it. The registration keeps working
// against the OLD path, so clicking an ssh-tool:// link launches
// whatever is still sitting in ~/Downloads - or nothing at all, once
// that is deleted. Neither failure says anything about why.
type IntegrationStatus struct {
	// Registered is true when a registration exists at all.
	Registered bool `json:"registered"`
	// Detail is the short OS-specific identifier shown to the user
	// (a .desktop name, a registry command).
	Detail string `json:"detail"`
	// Target is the executable path the registration points at, when
	// it can be read back. Empty when the OS does not expose it.
	Target string `json:"target"`
	// Stale is true when Target is readable and is not this binary.
	Stale bool `json:"stale"`
}

// integrationStatusFor builds the struct from a raw status string and
// the path the registration was found to point at.
//
// A registration whose target cannot be read is reported as
// not-stale rather than guessed at: "we cannot tell" and "it is wrong"
// deserve different words in the UI, and only one of them should push
// the user to re-register.
func integrationStatusFor(detail, target, self string) IntegrationStatus {
	st := IntegrationStatus{
		Registered: detail != "",
		Detail:     detail,
		Target:     target,
	}
	if target != "" && self != "" && !samePath(target, self) {
		st.Stale = true
	}
	return st
}
