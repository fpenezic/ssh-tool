//go:build !windows

package updater

import "syscall"

func detachedAttr() *syscall.SysProcAttr { return nil }
