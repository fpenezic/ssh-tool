//go:build !windows && !android

package creds

import (
	"crypto/sha256"
	"errors"
	"os"
	"runtime"
	"strings"

	"crypto/rand"
	"golang.org/x/crypto/chacha20poly1305"
)

// platformHasStrongSidecar reports whether this build can write
// sidecar v2.
//
// Linux: yes when /etc/machine-id is present and non-empty. We do
//
//	NOT fall back to hostname for v2 - a hostname change
//	shouldn't unlock the vault, and a container without
//	machine-id genuinely has no host-bound identity to derive
//	from.
//
// macOS: NO. Keychain Services integration is the right answer
//
//	there (kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly)
//	and requires CGO + signing surface; tracked in TODO.md.
//	Until then macOS keeps v1 behaviour: weaker derivation,
//	sufficient for the typical single-user desktop where the
//	attacker would need both file access and the user account.
func platformHasStrongSidecar() bool {
	if runtime.GOOS == "linux" {
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if b, err := os.ReadFile(p); err == nil {
				if strings.TrimSpace(string(b)) != "" {
					return true
				}
			}
		}
	}
	return false
}

// sealStrong / openStrong derive a key from machine-id alone (no
// hostname, no user env) and wrap with XChaCha20-Poly1305. Used by
// the sidecar v2 writer when platformHasStrongSidecar() is true.
func sealStrong(plaintext []byte) ([]byte, error) {
	key, err := strongKey()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

func openStrong(blob []byte) ([]byte, error) {
	key, err := strongKey()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("sealed v2 blob too short")
	}
	return aead.Open(nil, blob[:ns], blob[ns:], nil)
}

// strongKey derives the v2 key from machine-id only. SHA256 over
// app salt + the machine id; no user component because Unix file
// permissions already enforce the user binding (0600 sidecar file
// under the user's data dir).
func strongKey() ([]byte, error) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			id := strings.TrimSpace(string(b))
			if id != "" {
				h := sha256.New()
				h.Write([]byte("ssh-tool/v2/strong-sidecar"))
				h.Write([]byte("|machine-id="))
				h.Write([]byte(id))
				return h.Sum(nil), nil
			}
		}
	}
	return nil, errors.New("strong sidecar: no machine-id")
}
