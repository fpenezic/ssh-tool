//go:build windows

// Windows taskbar progress via ITaskbarList3, the same COM interface Explorer
// and Chrome use to fill the taskbar button during a copy or a download.
//
// We bind the interface ourselves rather than using wails' pkg/w32: that
// package declares the full ITaskbarList3 vtable (SetProgressValue and
// SetProgressState included) but keeps the vtable pointer unexported and only
// wraps SetOverlayIcon, so there is no way to reach the progress methods from
// outside. The binding below is ~60 lines of syscall and adds no dependency.
//
// Threading: COM objects created in a single-threaded apartment may only be
// used from the thread that created them. Both creation and every call go
// through application.InvokeAsync, which runs on the Wails main thread, so
// "the thread that created it" is always the same one. Async rather than sync
// because the caller is an SFTP goroutine streaming bytes - it must not block
// waiting for the UI thread to drain.

package main

import (
	"log"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

var (
	clsidTaskbarList = syscall.GUID{
		Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11D0,
		Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90},
	}
	iidTaskbarList3 = syscall.GUID{
		Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86,
		Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF},
	}
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	// CoInitializeEx returns S_FALSE when the apartment already exists and
	// RPC_E_CHANGED_MODE when this thread joined a different apartment model.
	// Neither stops the taskbar object from working, so neither is fatal.
	sFalse          = 0x1
	rpcEChangedMode = 0x80010106
)

// iTaskbarList3Vtbl is truncated on purpose: the real vtable continues past
// SetProgressState, but Go only needs the entries it dereferences and the
// offsets of the ones before them.
type iTaskbarList3Vtbl struct {
	QueryInterface       uintptr
	AddRef               uintptr
	Release              uintptr
	HrInit               uintptr
	AddTab               uintptr
	DeleteTab            uintptr
	ActivateTab          uintptr
	SetActiveAlt         uintptr
	MarkFullscreenWindow uintptr
	SetProgressValue     uintptr
	SetProgressState     uintptr
}

type iTaskbarList3 struct {
	vtbl *iTaskbarList3Vtbl
}

var (
	taskbarOnce sync.Once
	taskbarObj  *iTaskbarList3
)

// taskbarList3 returns the shared COM object, creating it on first use. Must
// run on the Wails main thread. Returns nil if COM said no, in which case every
// caller quietly does nothing - a missing progress bar is not worth an error
// path in the transfer code.
func taskbarList3() *iTaskbarList3 {
	taskbarOnce.Do(func() {
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
		if hr != 0 && hr != sFalse && hr != rpcEChangedMode {
			log.Printf("taskbar: CoInitializeEx: 0x%x", hr)
			return
		}
		var obj *iTaskbarList3
		hr, _, _ = procCoCreateInstance.Call(
			uintptr(unsafe.Pointer(&clsidTaskbarList)),
			0,
			clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidTaskbarList3)),
			uintptr(unsafe.Pointer(&obj)),
		)
		if hr != 0 || obj == nil {
			log.Printf("taskbar: CoCreateInstance: 0x%x", hr)
			return
		}
		if hr, _, _ := syscall.SyscallN(obj.vtbl.HrInit, uintptr(unsafe.Pointer(obj))); hr != 0 {
			log.Printf("taskbar: HrInit: 0x%x", hr)
			syscall.SyscallN(obj.vtbl.Release, uintptr(unsafe.Pointer(obj)))
			return
		}
		taskbarObj = obj
	})
	return taskbarObj
}

// applyTaskbarProgress paints the aggregate progress onto the main window's
// taskbar button.
//
// The handle comes from a.mainWindow and NOT from Window.Current(): "current"
// follows focus, so with several windows open the bar would hop to whichever
// one the user last clicked. It belongs to the app, so it lives on the app's
// primary window.
func (a *App) applyTaskbarProgress(pct int, state taskbarState) {
	win := a.mainWindow
	if win == nil {
		return
	}
	application.InvokeAsync(func() {
		obj := taskbarList3()
		if obj == nil {
			return
		}
		native := win.NativeWindow()
		if native == nil {
			return
		}
		hwnd := uintptr(native)

		// SetProgressState first: switching to NOPROGRESS after a value would
		// briefly show the old fill, and NORMAL must be in effect before a
		// value means anything.
		syscall.SyscallN(obj.vtbl.SetProgressState,
			uintptr(unsafe.Pointer(obj)), hwnd, uintptr(state))
		if state != taskbarNormal {
			return
		}
		// ULONGLONG completed, ULONGLONG total. One uintptr each - we only
		// build 64-bit Windows targets (amd64, arm64); a 32-bit build would
		// need these split into low/high pairs.
		syscall.SyscallN(obj.vtbl.SetProgressValue,
			uintptr(unsafe.Pointer(obj)), hwnd, uintptr(pct), uintptr(100))
	})
}
