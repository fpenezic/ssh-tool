//go:build android || ios

// Ensure a writable temp dir on mobile.
//
// Go's os.TempDir() reads $TMPDIR and falls back to /data/local/tmp on
// Android - a path the app sandbox cannot write, so every os.CreateTemp("")
// (sync pull, backup staging) fails with "permission denied". The native
// Activity does set TMPDIR via Os.setenv, but System.loadLibrary("wails")
// runs in a static initializer BEFORE onCreate, so the Go runtime may have
// already snapshotted the environment without it.
//
// Fix it from the Go side at package init, before any temp file is created:
// if TMPDIR is unset or points at the unwritable default, derive a cache
// dir next to HOME (which the Activity sets reliably and store.DataDir()
// already depends on) and point TMPDIR there.

package main

import (
	"log"
	"os"
	"path/filepath"
)

// ensureMobileTempDir points TMPDIR at a writable app-private directory.
//
// Must be called AFTER the native Activity has set HOME (it runs in
// configurePlatform, i.e. from main() via RegisterAndroidMain, which fires
// from nativeInit in onCreate - after the Activity's Os.setenv("HOME",...)).
// A package init() would be too early: it runs when libwails.so loads (the
// System.loadLibrary static block), before onCreate, so HOME is still empty
// and Go's os.TempDir() falls back to the unwritable /data/local/tmp.
// androidAppFilesDir is the app-private files directory. android.system.Os
// .setenv from the Activity does NOT reach Go's os.Getenv (the Go runtime
// snapshots the environment at .so load, before onCreate), so HOME/TMPDIR
// arrive empty. The path is deterministic from the package id, so derive it
// directly rather than relying on the env. The path follows the gradle
// applicationId (app.sshtool), not the namespace (com.wails.app, kept for
// the JNI bridge) - getFilesDir() resolves under /data/data/<applicationId>/.
const androidAppFilesDir = "/data/data/app.sshtool/files"

// ensureMobileTempDir points TMPDIR at a writable app-private dir so
// os.CreateTemp("") (sync pull, backup staging) works. Also exports HOME so
// store.DataDir() resolves to the same files dir without its hardcoded
// fallback.
func ensureMobileTempDir() {
	// Prefer an env that's already a real app dir (future Wails versions may
	// set one); otherwise use the deterministic files dir.
	base := os.Getenv("HOME")
	if base == "" || base == "/" {
		base = androidAppFilesDir
		_ = os.Setenv("HOME", base)
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" || tmp == "/data/local/tmp" || tmp == "/tmp" {
		tmp = filepath.Join(base, "tmp")
		_ = os.Setenv("TMPDIR", tmp)
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		log.Printf("[mobile-env] mkdir TMPDIR %q failed: %v", tmp, err)
	}
}
