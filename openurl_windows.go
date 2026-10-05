//go:build windows

// Opening a URL in the user's default browser, via ShellExecuteW.
//
// Wails' own Browser.OpenURL shells out to
//
//	rundll32 url.dll,FileProtocolHandler <url>
//
// which is the legacy route and mishandles long query strings: the URL
// becomes one argv entry of a process launched without a shell, and
// FileProtocolHandler re-parses it with its own (much older) rules. A
// 450-character OAuth URL carrying "&", "+" and percent-escapes - the
// shape every OAuth authorize endpoint produces - did not reach the
// browser at all. The click then fell through to the WebView, which
// offered to navigate the app window itself to claude.com. Short URLs
// survive the same path, which is why this only showed up on a login
// link.
//
// ShellExecuteW is what Explorer itself uses for "open with the default
// handler". It takes the URL as a single UTF-16 string rather than as a
// command line, so there is no second round of argument parsing and
// nothing to escape.
//
// Runs on its own OS thread with a fresh apartment: ShellExecuteW may
// initialise COM, and the callers here are IPC goroutines, not the Wails
// main thread.

package main

import (
	"fmt"
	"net/url"
	"strings"
	"syscall"
	"unsafe"

	sshlayer "ssh-tool/internal/ssh"
)

// The opkssh OIDC login opens its authorize URL through openpubkey's own
// launcher unless a hook is set. Since openpubkey v0.25 that launcher runs
// powershell.exe on Windows - a console program, so a terminal window
// flashed up before the browser on every login. Route it through
// ShellExecuteW like every other link the app opens.
func init() {
	sshlayer.BrowserOpenHook = openLoginURL
}

// openLoginURL opens an OIDC login URL, accepting only http(s): the URL
// comes from the provider's discovery document, and ShellExecuteW would
// run anything else it was handed (a file path, a custom scheme).
func openLoginURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("login url: %w", err)
	}
	if s := strings.ToLower(u.Scheme); (s != "http" && s != "https") || u.Host == "" {
		return fmt.Errorf("login url: refusing to open %q", u.Scheme+"://"+u.Host)
	}
	return openURLPlatform(raw)
}

var (
	shell32           = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

// Return values below 33 are error codes; anything higher is a (legacy,
// meaningless) instance handle. Documented on ShellExecuteW itself.
const shellExecuteMinSuccess = 32

// openURLPlatform hands the URL to the default protocol handler.
func openURLPlatform(url string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	// UTF16PtrFromString rejects a string containing a NUL, which is the
	// only way the file/URL argument could terminate early.
	target, err := syscall.UTF16PtrFromString(url)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	ret, _, callErr := procShellExecuteW.Call(
		0, // hwnd: no owner window, so a handler error box is not modal to us
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0, // parameters: none, the target carries everything
		0, // working directory: inherit
		uintptr(swShowNormal),
	)
	if ret < shellExecuteMinSuccess {
		// Call's error is only meaningful when the return value says the
		// call failed; otherwise it is a stale "operation completed
		// successfully".
		return fmt.Errorf("ShellExecuteW: %d (%v)", ret, callErr)
	}
	return nil
}
