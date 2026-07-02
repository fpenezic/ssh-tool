//go:build darwin

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// macOS binds URL schemes through the .app bundle's Info.plist
// (CFBundleURLTypes - declared in build/darwin/Info.plist). Launch
// Services picks it up when it indexes the bundle, which normally
// happens on first launch / first Finder sighting. "Register" here
// just forces a re-index with lsregister - useful after moving the
// bundle or when LS is being stale.

const lsregisterPath = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// bundlePath returns the enclosing .app bundle, or "" when running a
// bare binary (go run, task output before bundling).
func bundlePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	idx := strings.Index(exe, ".app/Contents/MacOS/")
	if idx == -1 {
		return ""
	}
	return exe[:idx+len(".app")]
}

func registerURLScheme() error {
	bundle := bundlePath()
	if bundle == "" {
		return fmt.Errorf("not running from an .app bundle - the ssh-tool:// handler is part of ssh-tool.app (Info.plist); launch the bundled app and register from there")
	}
	out, err := exec.Command(lsregisterPath, "-f", bundle).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Launch Services re-registration failed: %v (%s)", err, bytes.TrimSpace(out))
	}
	return nil
}

func urlSchemeStatus() string {
	// Declared in the bundle's Info.plist, so running from the bundle
	// means Launch Services has (or will have) the registration.
	if bundle := bundlePath(); bundle != "" {
		return bundle + " (Info.plist)"
	}
	return ""
}
