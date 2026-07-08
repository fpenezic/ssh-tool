//go:build !android

package creds

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// MachineSidecar is the optional file written next to vault.enc that allows
// auto-unlock on the same machine + user without re-prompting for the
// master passphrase. Encrypted with a key derived from machine_id + USER +
// app salt.
//
// Threat model: protects against disk theft or moving the file to another
// machine/user. Does NOT protect against another process on the same
// machine running as the same user.

const sidecarSuffix = ".local.key"

// Sidecar file version 1: weak - derives key from
//
//	SHA256(app_salt | machineID-or-hostname | user)
//
// on every platform. Hostname fallback on Linux + Windows means an
// attacker who steals the file plus spoofs %COMPUTERNAME% / $HOSTNAME
// decrypts. Audit M-finding.
//
// Sidecar file version 2: platform-strong.
//   - Windows: DPAPI CryptProtectData (user-scoped, machine-bound by
//     OS). No app-side key material.
//   - Linux: SHA256 of app salt + /etc/machine-id only. No hostname
//     fallback. Refused on systems without machine-id.
//   - macOS: NOT WRITTEN. Falls back to v1 until the Keychain
//     integration lands (see TODO.md).
const sidecarVersion = 1
const sidecarVersionStrong = 2

var appSalt = []byte("ssh-tool/v1/machine-bound-autounlock")

type sidecarFile struct {
	Version  int    `json:"version"`
	NonceB64 string `json:"nonce"`
	CTB64    string `json:"ct"`
}

// SidecarPath returns the path next to the given vault file.
func SidecarPath(vaultPath string) string {
	return vaultPath + sidecarSuffix
}

// SidecarExists is a cheap check.
func SidecarExists(vaultPath string) bool {
	_, err := os.Stat(SidecarPath(vaultPath))
	return err == nil
}

// WriteSidecar encrypts `passphrase` with a machine-bound key and
// writes it to disk with 0600 perms. Prefers the v2 (platform-
// strong) format when the build/platform supports it
// (Windows DPAPI, Linux + /etc/machine-id). Falls back to v1
// otherwise so macOS and bare-bones containers still get
// auto-unlock - just with the old weaker derivation.
func WriteSidecar(vaultPath, passphrase string) error {
	if platformHasStrongSidecar() {
		return writeSidecarV2(vaultPath, passphrase)
	}
	return writeSidecarV1(vaultPath, passphrase)
}

func writeSidecarV1(vaultPath, passphrase string) error {
	key, err := deriveMachineKey()
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := aead.Seal(nil, nonce, []byte(passphrase), nil)
	sc := sidecarFile{
		Version:  sidecarVersion,
		NonceB64: b64encode(nonce),
		CTB64:    b64encode(ct),
	}
	return writeSidecarFile(vaultPath, sc)
}

func writeSidecarV2(vaultPath, passphrase string) error {
	sealed, err := sealStrong([]byte(passphrase))
	if err != nil {
		return err
	}
	sc := sidecarFile{
		Version:  sidecarVersionStrong,
		NonceB64: "",
		CTB64:    b64encode(sealed),
	}
	return writeSidecarFile(vaultPath, sc)
}

func writeSidecarFile(vaultPath string, sc sidecarFile) error {
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	path := SidecarPath(vaultPath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadSidecar attempts to decrypt the sidecar; returns the passphrase or
// (empty, nil) if the sidecar isn't present. Recognises both v1
// (legacy weak derivation) and v2 (platform-strong) formats so an
// upgrade across this change keeps working without re-prompting.
func ReadSidecar(vaultPath string) (string, error) {
	path := SidecarPath(vaultPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var sc sidecarFile
	if err := json.Unmarshal(raw, &sc); err != nil {
		return "", fmt.Errorf("sidecar parse: %w", err)
	}
	switch sc.Version {
	case sidecarVersionStrong:
		blob, err := b64decode(sc.CTB64)
		if err != nil {
			return "", err
		}
		pt, err := openStrong(blob)
		if err != nil {
			return "", fmt.Errorf("sidecar v2 decrypt: %w", err)
		}
		return string(pt), nil
	case sidecarVersion:
		nonce, err := b64decode(sc.NonceB64)
		if err != nil {
			return "", err
		}
		ct, err := b64decode(sc.CTB64)
		if err != nil {
			return "", err
		}
		key, err := deriveMachineKey()
		if err != nil {
			return "", err
		}
		aead, err := chacha20poly1305.NewX(key)
		if err != nil {
			return "", err
		}
		pt, err := aead.Open(nil, nonce, ct, nil)
		if err != nil {
			return "", fmt.Errorf("sidecar v1 decrypt: %w", err)
		}
		return string(pt), nil
	default:
		return "", fmt.Errorf("sidecar: unsupported version %d", sc.Version)
	}
}

// SealForMachine encrypts arbitrary plaintext with the same
// machine+user-bound key the vault sidecar uses, returning a
// self-describing nonce||ciphertext blob. Used by the backup
// package to seal the staged-restore passphrase so the next
// startup can decrypt and apply the pending restore without
// re-prompting the user - but a stolen file alone, without the
// machine binding, won't decrypt.
func SealForMachine(plaintext []byte) ([]byte, error) {
	key, err := deriveMachineKey()
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

// OpenForMachine reverses SealForMachine. Returns ErrCorrupt-style
// error when the blob is shorter than the nonce or when the AEAD
// tag fails (wrong machine, file tampering, etc).
func OpenForMachine(blob []byte) ([]byte, error) {
	key, err := deriveMachineKey()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("sealed blob too short")
	}
	nonce := blob[:ns]
	ct := blob[ns:]
	return aead.Open(nil, nonce, ct, nil)
}

// SidecarStrengthKind classifies how strongly an existing auto-unlock sidecar
// is bound to this machine.
type SidecarStrengthKind string

const (
	SidecarStrengthNone   SidecarStrengthKind = "none"   // no sidecar present
	SidecarStrengthWeak   SidecarStrengthKind = "weak"   // v1: derivation may fall back to hostname
	SidecarStrengthStrong SidecarStrengthKind = "strong" // v2: DPAPI / machine-id-only
)

// SidecarStrength reports the binding strength of the sidecar next to
// vaultPath. "weak" means the v1 format is in use, whose key derivation can
// fall back to the hostname - an attacker who steals the vault file plus the
// sidecar and can guess/spoof the hostname could auto-unlock. The UI surfaces
// this so a user on macOS or a container without /etc/machine-id knows their
// auto-unlock is not machine-strong. Reads only the version field; never
// decrypts.
func SidecarStrength(vaultPath string) SidecarStrengthKind {
	raw, err := os.ReadFile(SidecarPath(vaultPath))
	if err != nil {
		return SidecarStrengthNone
	}
	var sc sidecarFile
	if err := json.Unmarshal(raw, &sc); err != nil {
		return SidecarStrengthNone
	}
	if sc.Version == sidecarVersionStrong {
		return SidecarStrengthStrong
	}
	return SidecarStrengthWeak
}

// DeleteSidecar removes the auto-unlock file. Used when the user wants to
// require the passphrase on next launch.
func DeleteSidecar(vaultPath string) error {
	err := os.Remove(SidecarPath(vaultPath))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// deriveMachineKey: SHA-256(app_salt | "|machine=" | machine_id | "|user=" | user).
// Same shape as Rust implementation so a vault.local.key file from the Rust
// build remains decryptable here.
func deriveMachineKey() ([]byte, error) {
	mid, err := machineID()
	if err != nil {
		return nil, err
	}
	user, err := userID()
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(appSalt)
	h.Write([]byte("|machine="))
	h.Write([]byte(mid))
	h.Write([]byte("|user="))
	h.Write([]byte(user))
	return h.Sum(nil), nil
}

// StableMachineID exposes the hardware/OS-derived machine identifier
// (the same value the sidecar key is bound to) for callers that need a
// per-machine identity that does NOT travel through the synced store -
// e.g. tunnel presence, where two machines sharing a profile must have
// DISTINCT ids. A store-persisted UUID is unsuitable there: it rides the
// sync snapshot and both machines end up with the same id, so each reads
// the other's presence record as its own. This is derived fresh from the
// local machine every call and never written to the store. Empty string
// only in the impossible case that even the hostname is unavailable.
func StableMachineID() string {
	id, err := machineID()
	if err != nil {
		return ""
	}
	return id
}

func machineID() (string, error) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p)
		if err == nil {
			s := strings.TrimSpace(string(b))
			if s != "" {
				return s, nil
			}
		}
	}
	// macOS has no machine-id file; the stable equivalent is the
	// IOPlatformUUID (hardware UUID). Checked before the env
	// fallbacks because those are both weak AND absent in
	// Finder-launched apps (no HOSTNAME in the GUI environment, which
	// is why mac auto-unlock never worked before this branch).
	if runtime.GOOS == "darwin" {
		if uuid := darwinPlatformUUID(); uuid != "" {
			return uuid, nil
		}
	}
	// Fallbacks. Hostname alone is weak but better than a static string.
	for _, env := range []string{"HOSTNAME", "COMPUTERNAME"} {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
	}
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/sys/kernel/hostname"); err == nil {
			s := strings.TrimSpace(string(b))
			if s != "" {
				return s, nil
			}
		}
	}
	// Last resort on any platform: the kernel hostname via syscall,
	// which doesn't depend on the launch environment.
	if h, err := os.Hostname(); err == nil && h != "" {
		return h, nil
	}
	return "", errors.New("no machine_id and no hostname")
}

// darwinPlatformUUID reads the IOPlatformUUID from the IOKit registry
// via ioreg - stable across reboots and renames, tied to the hardware.
// Returns "" on any failure (caller falls through to weaker sources).
func darwinPlatformUUID() string {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		// Line shape: `"IOPlatformUUID" = "XXXX-..."`. The UUID is the
		// second quoted token.
		parts := strings.Split(line, "\"")
		if len(parts) >= 4 && parts[3] != "" {
			return parts[3]
		}
	}
	return ""
}

// userID returns a stable per-user identifier for the sidecar key
// derivation. It must be deterministic on the same machine/account so an
// auto-unlock sidecar written today still opens tomorrow. Env first (USER /
// USERNAME); then os/user, which reads the OS account (passwd / token)
// rather than the environment, so it works in containers / cron / CI where
// the env vars are unset; finally a constant fallback so seal/open never
// fail outright - in that degenerate case the binding leans on the
// machine-id component alone, which is acceptable defence-in-depth (the
// threat model is a stolen vault file, not a same-host attacker).
func userID() (string, error) {
	for _, env := range []string{"USER", "USERNAME"} {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
	}
	if u, err := user.Current(); err == nil {
		if u.Username != "" {
			return u.Username, nil
		}
		if u.Uid != "" {
			return "uid:" + u.Uid, nil
		}
	}
	return "ssh-tool-default-user", nil
}
