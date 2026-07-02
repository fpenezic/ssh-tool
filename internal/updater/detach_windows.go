//go:build windows

package updater

import "syscall"

// detachedAttr breaks the helper script away from the ssh-tool console
// so the .cmd survives the parent process exit.
//
// Flag choice:
//   - CREATE_NO_WINDOW (0x08000000): suppress the console window the
//     child would normally allocate. DETACHED_PROCESS only severs the
//     parent's console; when we exec cmd.exe (a console subsystem
//     binary), cmd.exe still allocates its own console with no parent
//     to attach to. CREATE_NO_WINDOW is what actually keeps the
//     window from appearing.
//   - CREATE_NEW_PROCESS_GROUP (0x00000200): own process group so the
//     child survives the parent's exit (parent's Ctrl+C / WM_CLOSE
//     don't cascade).
//   - HideWindow:true: belt-and-suspenders for the cmd window in case
//     a future Windows behaves differently with the flags above.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: 0x08000000 | 0x00000200, // CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP
		HideWindow:    true,
	}
}
