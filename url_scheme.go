// OS-level registration of the `ssh-tool://` URL scheme so the
// catalog's "Open in ssh-tool" deep links actually launch the app.
//
// Per-platform behaviour:
//   - Windows: writes HKCU\Software\Classes\ssh-tool registry keys
//     pointing at the current executable. No admin elevation
//     needed (per-user scope). Implemented in url_scheme_windows.go.
//   - Linux: writes ~/.local/share/applications/ssh-tool-url.desktop
//     + runs `xdg-mime default` to associate the scheme.
//     Implemented in url_scheme_linux.go.
//   - macOS: bundling the .app with the proper Info.plist
//     CFBundleURLTypes is the right way - no runtime hook needed.
//     This stub returns ErrNotSupported on darwin.
//
// The IPC method App.RegisterURLScheme is one-shot - call it once
// (from a Settings button) and the registration sticks. Idempotent;
// re-running overwrites the existing entries.

package main

import "fmt"

// ErrURLSchemeNotSupported is returned when there's no registration
// path for the current platform (currently darwin - bundling
// handles it).
var ErrURLSchemeNotSupported = fmt.Errorf("automatic URL scheme registration not supported on this platform - see docs for manual setup")
