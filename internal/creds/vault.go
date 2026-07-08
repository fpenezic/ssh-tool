package creds

import (
	"errors"
	"path/filepath"
	"sync"

	"ssh-tool/internal/store"
)

// Vault facade orchestrates two storage layers consulted in order:
//
//	1. memory mirror - process-lifetime cache, populated on every Put;
//	                   cleared on Lock so a locked vault has no plaintext
//	2. file vault    - XChaCha20-Poly1305 encrypted disk store; persistent
//	                   across restarts. Requires the master passphrase
//	                   (typed by user or recovered from the machine-bound
//	                   sidecar) to be unlocked.
//
// Reads cascade memory -> file vault on miss. Writes hit memory + file.
// The OS keychain is intentionally NOT used as a per-secret store: it
// would silently bypass Lock() because Windows / macOS keep their stores
// unlocked for the duration of the user's login session.

// Vault is a process-wide singleton (constructed once in app.go).
type Vault struct {
	mu sync.Mutex
	// memory mirrors decrypted secrets as []byte so Lock() can
	// scrub them in place before GC. string values would be
	// immutable on the Go heap with no wipe path; bytes give us
	// an actual zero step that defeats opportunistic heap reads
	// from a crash dump or coredump.
	memory map[string][]byte
	file   *UnlockedVault // nil when locked
	path   string         // file vault path on disk
}

// NewVault constructs an empty vault facade. Call SetPath then optionally
// AutoUnlock / Init / Unlock before storing secrets.
func NewVault() *Vault {
	return &Vault{memory: map[string][]byte{}}
}

// wipeBytes overwrites a buffer in place with zeros. Callers
// hold the only reference at the call site; after this returns the
// underlying memory contains no plaintext until the GC reclaims it.
func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// SetPath sets the file vault location. Idempotent.
func (v *Vault) SetPath(path string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.path = path
}

// VaultPath returns the configured path.
func (v *Vault) VaultPath() string { return v.path }

// DefaultPath returns the platform path next to the store.
func DefaultPath() string {
	return filepath.Join(store.DataDir(), "vault.enc")
}

// Status reports whether a vault file exists and whether we have it unlocked.
type StatusKind string

const (
	StatusNotInitialized StatusKind = "not_initialized"
	StatusLocked         StatusKind = "locked"
	StatusUnlocked       StatusKind = "unlocked"
)

type Status struct {
	Kind                StatusKind `json:"state"`
	AutoUnlockAvailable bool       `json:"auto_unlock_available,omitempty"`
	// SidecarStrength reports how strongly an existing auto-unlock sidecar is
	// bound to this machine ("strong" | "weak" | "none"). "weak" means the v1
	// format whose key derivation can fall back to the hostname; the UI warns
	// on it. Empty when no sidecar / not applicable.
	SidecarStrength string `json:"sidecar_strength,omitempty"`
}

func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.file != nil {
		return Status{
			Kind:            StatusUnlocked,
			SidecarStrength: string(SidecarStrength(v.path)),
		}
	}
	if !FileExists(v.path) {
		return Status{Kind: StatusNotInitialized}
	}
	return Status{
		Kind:                StatusLocked,
		AutoUnlockAvailable: SidecarExists(v.path),
		SidecarStrength:     string(SidecarStrength(v.path)),
	}
}

// Init creates a new file vault, optionally writing the auto-unlock sidecar.
func (v *Vault) Init(passphrase string, rememberOnMachine bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	uv, err := InitVault(v.path, passphrase)
	if err != nil {
		return err
	}
	v.file = uv
	if rememberOnMachine {
		// Best-effort; failure logged via caller's tracing.
		_ = WriteSidecar(v.path, passphrase)
	}
	return nil
}

// Unlock opens an existing vault file.
func (v *Vault) Unlock(passphrase string, rememberOnMachine bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	uv, err := UnlockVault(v.path, passphrase)
	if err != nil {
		return err
	}
	v.file = uv
	if rememberOnMachine {
		_ = WriteSidecar(v.path, passphrase)
	}
	return nil
}

// AutoUnlock attempts to read the sidecar and unlock the vault with the
// stored passphrase. Returns (true, nil) on success, (false, nil) if the
// sidecar is missing, or (false, err) on read/decrypt errors.
func (v *Vault) AutoUnlock() (bool, error) {
	v.mu.Lock()
	if v.file != nil {
		v.mu.Unlock()
		return true, nil
	}
	if !FileExists(v.path) {
		v.mu.Unlock()
		return false, nil
	}
	v.mu.Unlock()

	pass, err := ReadSidecar(v.path)
	if err != nil {
		return false, err
	}
	if pass == "" {
		return false, nil
	}
	uv, err := UnlockVault(v.path, pass)
	if err != nil {
		return false, err
	}
	v.mu.Lock()
	v.file = uv
	v.mu.Unlock()
	return true, nil
}

// ChangePassphrase rotates the master passphrase of the file vault.
// The vault must be currently unlocked. The OLD passphrase is verified
// by attempting a full UnlockVault before mutating anything, defending
// against a stale in-memory key (e.g. machine sidecar drift). On
// success the sidecar (if present) is refreshed with the new
// passphrase so AutoUnlock keeps working.
func (v *Vault) ChangePassphrase(oldPassphrase, newPassphrase string) error {
	v.mu.Lock()
	file := v.file
	path := v.path
	v.mu.Unlock()
	if file == nil {
		return errors.New("vault is locked")
	}
	if newPassphrase == "" {
		return errors.New("new passphrase is empty")
	}
	// Verify old passphrase against the on-disk file independently of
	// the in-memory key.
	if _, err := UnlockVault(path, oldPassphrase); err != nil {
		return err
	}
	if err := file.ChangePassphrase(newPassphrase); err != nil {
		return err
	}
	if SidecarExists(path) {
		_ = WriteSidecar(path, newPassphrase)
	}
	return nil
}

// Lock forgets the derived file key and wipes the in-memory plaintext
// cache, so subsequent Get() calls must come through a fresh unlock.
// Pass forgetSidecar=true to also delete the auto-unlock file.
//
// Each cached secret is zeroed in place before the map entry is
// dropped. The GC will still eventually reclaim the buffer, but until
// then a crash dump / coredump from an attacker with file access will
// see zeros, not the plaintext credential. The file vault wrapper is
// nilled out so any held reference can't keep the derived AEAD key
// alive past Lock().
func (v *Vault) Lock(forgetSidecar bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.file = nil
	for k, b := range v.memory {
		wipeBytes(b)
		delete(v.memory, k)
	}
	if forgetSidecar {
		_ = DeleteSidecar(v.path)
	}
}

// Put writes the secret to the file vault and mirrors it in memory.
// Refuses to write when the vault is locked: otherwise a secret typed
// into a credential form would live only in process memory, vanish on
// the next restart, and silently leak the user's expectation that
// "saved" means "persisted on disk".
func (v *Vault) Put(account, secret string) error {
	v.mu.Lock()
	file := v.file
	v.mu.Unlock()

	if file == nil {
		return errors.New("vault is locked; unlock it before saving secrets")
	}
	if err := file.Put(account, secret); err != nil {
		return err
	}
	v.mu.Lock()
	if prev, ok := v.memory[account]; ok {
		wipeBytes(prev)
	}
	v.memory[account] = []byte(secret)
	v.mu.Unlock()
	return nil
}

// Get consults memory then the file vault. Returns ("", false, nil) when
// the secret isn't stored or when the vault is locked and the value isn't
// in the memory mirror.
func (v *Vault) Get(account string) (string, bool, error) {
	v.mu.Lock()
	if b, ok := v.memory[account]; ok {
		// Copy to a fresh string so a later wipeBytes on the
		// cached buffer can't reach into the caller's hand.
		s := string(b)
		v.mu.Unlock()
		return s, true, nil
	}
	file := v.file
	v.mu.Unlock()

	if file == nil {
		return "", false, nil
	}
	s, ok, err := file.Get(account)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	v.mu.Lock()
	v.memory[account] = []byte(s)
	v.mu.Unlock()
	return s, true, nil
}

// Delete removes from memory and the file vault (if unlocked).
func (v *Vault) Delete(account string) error {
	v.mu.Lock()
	delete(v.memory, account)
	file := v.file
	v.mu.Unlock()

	if file != nil {
		_ = file.Delete(account)
	}
	return nil
}

// VaultAccountKey is the canonical "current value" account name for a
// credential. Mirrors the Rust vault::current_key.
func VaultAccountKey(credentialID string) string {
	return "cred:" + credentialID
}

// PassphraseAccountKey is the account name for an encrypted-key's passphrase
// (stored separately so the key file and passphrase don't share an entry).
func PassphraseAccountKey(credentialID string) string {
	return "cred:" + credentialID + ":passphrase"
}

// MergeSnapshotVault adopts the secrets from a pulled snapshot's
// vault file into THIS (live, unlocked) vault, re-encrypting each
// secret under the local key. This is the live sync-pull path: no
// file swap, no restart, the running vault just gains/updates the
// other machine's entries.
//
// srcVaultPath is the snapshot's vault.enc, sealed under the SOURCE
// machine's vault passphrase. For a single-user setup that's the same
// passphrase, so the caller tries the local passphrase first; if it
// doesn't open the source (different passphrase across machines), this
// returns ErrWrongPassphrase and the caller falls back to a staged
// restart instead of prompting.
//
// Mirror semantics: every source account is Put (re-encrypted local);
// local accounts absent from the source are deleted - EXCEPT the sync
// machine-local keys, which identify this machine and must survive a
// pull. The live vault must be unlocked.
func (v *Vault) MergeSnapshotVault(srcVaultPath, srcPassphrase string) error {
	v.mu.Lock()
	live := v.file
	v.mu.Unlock()
	if live == nil {
		return errors.New("vault is locked; unlock before merging a pulled profile")
	}

	src, err := UnlockVault(srcVaultPath, srcPassphrase)
	if err != nil {
		return err // ErrWrongPassphrase -> caller falls back to restart
	}

	srcAccounts := src.Accounts()
	want := make(map[string]bool, len(srcAccounts))
	for _, acct := range srcAccounts {
		want[acct] = true
		secret, ok, gerr := src.Get(acct)
		if gerr != nil || !ok {
			continue
		}
		if err := v.Put(acct, secret); err != nil {
			return err
		}
	}

	// Delete local secrets the source doesn't have, except machine-local
	// sync keys (never stored as vault accounts today, but guard anyway).
	for _, acct := range live.Accounts() {
		if want[acct] || isVaultMachineLocalAccount(acct) {
			continue
		}
		_ = v.Delete(acct)
	}
	return nil
}

// isVaultMachineLocalAccount guards accounts that must not be wiped by
// a pulled profile. The sync WebDAV password and passphrase live here.
func isVaultMachineLocalAccount(account string) bool {
	switch account {
	case "sync:webdav_password", "sync:passphrase":
		return true
	}
	return false
}
