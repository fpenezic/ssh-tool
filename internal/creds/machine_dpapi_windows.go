//go:build windows

package creds

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// blobToBytes copies the bytes pointed at by a DataBlob into a
// fresh Go slice so we can LocalFree the original Windows-allocated
// buffer safely.
func blobToBytes(b windows.DataBlob) []byte {
	if b.Size == 0 || b.Data == nil {
		return nil
	}
	src := unsafe.Slice(b.Data, int(b.Size))
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

// sealWithDPAPI wraps plaintext using Windows DPAPI scoped to the
// current user (LOCAL_MACHINE flag intentionally NOT set so the key
// is bound to the user profile, not just the machine - matches the
// macOS Keychain afterFirstUnlockThisDeviceOnly story).
//
// Replaces the v1 sidecar's SHA256(salt|machineID|user) derived key,
// which fell back to %COMPUTERNAME% when the kernel had no machine
// ID - and would happily decrypt for any user on the same host.
// DPAPI handles both gaps natively.
func sealWithDPAPI(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("dpapi: empty plaintext")
	}
	in := windows.DataBlob{Size: uint32(len(plaintext)), Data: &plaintext[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
	return blobToBytes(out), nil
}

func openWithDPAPI(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errors.New("dpapi: empty blob")
	}
	in := windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.Data))))
	return blobToBytes(out), nil
}

// platformHasStrongSidecar reports whether this build can write
// sidecar v2. Windows always can (DPAPI ships with the OS); other
// platforms have their own predicate.
func platformHasStrongSidecar() bool { return true }

// sealStrong / openStrong are the platform-native v2 sealers used
// when platformHasStrongSidecar() returns true.
func sealStrong(plaintext []byte) ([]byte, error) { return sealWithDPAPI(plaintext) }
func openStrong(blob []byte) ([]byte, error)      { return openWithDPAPI(blob) }
