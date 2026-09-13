//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Direct COM creation of a .lnk via IShellLink + IPersistFile.
//
// This replaces an earlier implementation that shelled out to
// `powershell.exe -ExecutionPolicy Bypass -File <temp>.ps1` driving
// WScript.Shell. That worked, but the combination it baked into the
// binary - the literal "powershell.exe", "-ExecutionPolicy", "Bypass",
// dropping a script into %TEMP%, and writing into the Start Menu - is
// the textbook static signature of a dropper establishing persistence.
// Windows Defender started flagging the v0.95.0 release build as
// Trojan:Win32/Sabsik.FL.A!ml (a machine-learning verdict, not a
// signature match) while v0.94.0, built before that code landed, passed.
// Nothing here was ever malicious; an unsigned 33 MB Go binary that
// spawns an interpreter to write a Start Menu entry simply looks exactly
// like one to a classifier.
//
// Calling the shell API directly is what a normal installer does and
// carries none of that profile. It is also better code on its own
// terms: no temp file, no child process, no console-window hiding, no
// parsing an interpreter's error text, and no argument-quoting concerns.
//
// COM specifics worth knowing before editing:
//
//   - All calls must happen on one OS thread, because CoInitializeEx
//     applies per thread and the interface pointer is only valid on the
//     apartment that created it. Hence runtime.LockOSThread in the
//     caller.
//   - CoInitializeEx returns S_FALSE (1) when the thread is already
//     initialised. That is success, not an error, and we still owe a
//     CoUninitialize.
//   - RPC_E_CHANGED_MODE (0x80010106) means some other code already put
//     this thread in a different apartment model. Also survivable: we
//     simply must not uninitialise what we did not initialise.
//   - IPersistFile::Save takes a UTF-16 path; IShellLink's ANSI vs wide
//     split means we must use the W interface (CLSID is shared, IID is
//     not).

// ole32, procCoInitializeEx, procCoCreateInstance, coinitApartmentThreaded,
// clsctxInprocServer, sFalse and rpcEChangedMode are declared once in
// taskbar_windows.go, which binds ITaskbarList3 the same way. Only what is
// specific to shortcut creation is added here.
var procCoUninitialize = ole32.NewProc("CoUninitialize")

const (
	// Skips the OLE1 DDE layer, which the shell link object never needs
	// and which is the slow part of apartment setup.
	coinitDisableOLE1DDE = 0x4

	errNoInterface      = 0x80004002 // E_NOINTERFACE
	swShowNormal        = 1
	persistSaveRemember = 1 // IPersistFile::Save fRemember
)

// CLSID_ShellLink {00021401-0000-0000-C000-000000000046}
var clsidShellLink = syscall.GUID{
	Data1: 0x00021401,
	Data2: 0x0000,
	Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IShellLinkW {000214F9-0000-0000-C000-000000000046}
var iidShellLinkW = syscall.GUID{
	Data1: 0x000214F9,
	Data2: 0x0000,
	Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IPersistFile {0000010B-0000-0000-C000-000000000046}
var iidPersistFile = syscall.GUID{
	Data1: 0x0000010B,
	Data2: 0x0000,
	Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// iShellLinkWVtbl mirrors the vtable layout of IShellLinkW. The order is
// fixed by the interface definition in shobjidl_core.h and must not be
// reordered - each entry is called by its index in memory, not by name.
type iShellLinkWVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr

	GetPath             uintptr
	GetIDList           uintptr
	SetIDList           uintptr
	GetDescription      uintptr
	SetDescription      uintptr
	GetWorkingDirectory uintptr
	SetWorkingDirectory uintptr
	GetArguments        uintptr
	SetArguments        uintptr
	GetHotkey           uintptr
	SetHotkey           uintptr
	GetShowCmd          uintptr
	SetShowCmd          uintptr
	GetIconLocation     uintptr
	SetIconLocation     uintptr
	SetRelativePath     uintptr
	Resolve             uintptr
	SetPath             uintptr
}

type iShellLinkW struct {
	vtbl *iShellLinkWVtbl
}

// iPersistFileVtbl mirrors IPersistFile. It derives from IPersist, which
// derives from IUnknown, so the first four entries are inherited before
// IPersistFile's own methods begin.
type iPersistFileVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr

	GetClassID uintptr // IPersist

	IsDirty       uintptr
	Load          uintptr
	Save          uintptr
	SaveCompleted uintptr
	GetCurFile    uintptr
}

type iPersistFile struct {
	vtbl *iPersistFileVtbl
}

// hresultError turns a failing HRESULT into an error. COM reports
// failure by the sign bit, so anything with the top bit set is an error
// and everything else (including S_FALSE) is success.
func hresultError(op string, hr uintptr) error {
	if int32(hr) >= 0 {
		return nil
	}
	return fmt.Errorf("%s: HRESULT 0x%08X", op, uint32(hr))
}

// comInitialize sets up the calling thread's apartment. The bool reports
// whether the caller owes a matching CoUninitialize: it is false when the
// thread was already in a different apartment model, where uninitialising
// would unbalance whoever set it up.
func comInitialize() (needsUninit bool, err error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
	switch uint32(hr) {
	case 0, sFalse:
		return true, nil
	case rpcEChangedMode:
		// Already initialised as MTA by something else in this process.
		// The shell link object is in-proc and works either way.
		return false, nil
	}
	return false, hresultError("CoInitializeEx", hr)
}

// createShortcutCOM writes a .lnk at lnkPath pointing at target.
//
// The caller must have called runtime.LockOSThread, and must not release
// it until this returns: every pointer here belongs to this thread's
// apartment.
func createShortcutCOM(lnkPath, target, workDir, description string) error {
	needsUninit, err := comInitialize()
	if err != nil {
		return err
	}
	if needsUninit {
		defer procCoUninitialize.Call()
	}

	var linkPtr *iShellLinkW
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidShellLinkW)),
		uintptr(unsafe.Pointer(&linkPtr)),
	)
	if err := hresultError("CoCreateInstance(ShellLink)", hr); err != nil {
		return err
	}
	if linkPtr == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink): returned a nil interface")
	}
	defer syscall.SyscallN(linkPtr.vtbl.Release, uintptr(unsafe.Pointer(linkPtr)))

	targetW, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("target path: %w", err)
	}
	hr, _, _ = syscall.SyscallN(linkPtr.vtbl.SetPath,
		uintptr(unsafe.Pointer(linkPtr)), uintptr(unsafe.Pointer(targetW)))
	if err := hresultError("IShellLink::SetPath", hr); err != nil {
		return err
	}

	if workDir != "" {
		workDirW, err := windows.UTF16PtrFromString(workDir)
		if err != nil {
			return fmt.Errorf("working directory: %w", err)
		}
		hr, _, _ = syscall.SyscallN(linkPtr.vtbl.SetWorkingDirectory,
			uintptr(unsafe.Pointer(linkPtr)), uintptr(unsafe.Pointer(workDirW)))
		if err := hresultError("IShellLink::SetWorkingDirectory", hr); err != nil {
			return err
		}
	}

	if description != "" {
		descW, err := windows.UTF16PtrFromString(description)
		if err != nil {
			return fmt.Errorf("description: %w", err)
		}
		hr, _, _ = syscall.SyscallN(linkPtr.vtbl.SetDescription,
			uintptr(unsafe.Pointer(linkPtr)), uintptr(unsafe.Pointer(descW)))
		if err := hresultError("IShellLink::SetDescription", hr); err != nil {
			return err
		}
	}

	hr, _, _ = syscall.SyscallN(linkPtr.vtbl.SetShowCmd,
		uintptr(unsafe.Pointer(linkPtr)), swShowNormal)
	if err := hresultError("IShellLink::SetShowCmd", hr); err != nil {
		return err
	}

	// The saved .lnk carries its own icon reference. Point it at the exe
	// itself (index 0 = the first embedded icon resource) so the Start
	// Menu entry keeps the app icon even before the shell has looked
	// inside the target.
	if iconW, err := windows.UTF16PtrFromString(target); err == nil {
		hr, _, _ = syscall.SyscallN(linkPtr.vtbl.SetIconLocation,
			uintptr(unsafe.Pointer(linkPtr)), uintptr(unsafe.Pointer(iconW)), 0)
		// A failure here costs the icon, not the shortcut. Ignore it.
		_ = hr
	}

	// IPersistFile is what actually writes the file; IShellLink only
	// holds the state in memory.
	var persistPtr *iPersistFile
	hr, _, _ = syscall.SyscallN(linkPtr.vtbl.QueryInterface,
		uintptr(unsafe.Pointer(linkPtr)),
		uintptr(unsafe.Pointer(&iidPersistFile)),
		uintptr(unsafe.Pointer(&persistPtr)),
	)
	if uint32(hr) == errNoInterface {
		return fmt.Errorf("shell link does not implement IPersistFile")
	}
	if err := hresultError("IShellLink::QueryInterface(IPersistFile)", hr); err != nil {
		return err
	}
	if persistPtr == nil {
		return fmt.Errorf("QueryInterface(IPersistFile): returned a nil interface")
	}
	defer syscall.SyscallN(persistPtr.vtbl.Release, uintptr(unsafe.Pointer(persistPtr)))

	lnkW, err := windows.UTF16PtrFromString(lnkPath)
	if err != nil {
		return fmt.Errorf("shortcut path: %w", err)
	}
	// fRemember=TRUE (1): the object keeps the path as its current file,
	// matching what WScript.Shell did.
	hr, _, _ = syscall.SyscallN(persistPtr.vtbl.Save,
		uintptr(unsafe.Pointer(persistPtr)), uintptr(unsafe.Pointer(lnkW)), persistSaveRemember)
	if err := hresultError("IPersistFile::Save", hr); err != nil {
		return err
	}
	return nil
}
