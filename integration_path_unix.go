//go:build !windows && !android && !ios

package main

import "path/filepath"

// samePath compares two executable paths. Unix paths are
// case-sensitive; symlinks are resolved so a registration pointing at a
// link to this binary is not reported as stale.
func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
