//go:build windows

package main

import (
	"path/filepath"
	"strings"
)

// samePath compares two executable paths, case-insensitively: Windows
// treats C:\Users\x\App.exe and c:\users\x\app.exe as the same file, and
// a registry value can carry either.
func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
