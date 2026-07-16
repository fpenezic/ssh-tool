// Package creds implements credential storage: the encrypted file vault,
// a machine-bound auto-unlock sidecar, and the memory mirror that ties
// them together for the running process.
package creds

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// FileVault errors.
var (
	ErrLocked          = errors.New("vault locked")
	ErrWrongPassphrase = errors.New("wrong passphrase")
	ErrCorrupt         = errors.New("vault file corrupt")
)

const fileVersion = 1

const sentinelKey = "__sentinel__"

var sentinelPlaintext = []byte("ssh-tool vault sentinel v1")

type vaultFile struct {
	Version int                  `json:"version"`
	KDF     kdfParams            `json:"kdf"`
	Entries map[string]vaultItem `json:"entries"`
}

type kdfParams struct {
	Algo    string `json:"algo"`
	Time    uint32 `json:"t"` // iterations
	Memory  uint32 `json:"m"` // KiB
	Threads uint8  `json:"p"`
	SaltB64 string `json:"salt"`
}

type vaultItem struct {
	NonceB64 string `json:"nonce"`
	CTB64    string `json:"ct"`
}

// UnlockedVault holds the derived key for the process lifetime. Persisted to
// disk on every Put / Delete via atomic write.
type UnlockedVault struct {
	mu   sync.Mutex
	path string
	key  []byte // 32 bytes
	file vaultFile
}

// freshKDFParams returns interactive-grade Argon2id params. We deliberately
// pick lower memory than OWASP "recommended" (19 MiB vs 64 MiB) because on
// WSL2 the 64 MiB tier takes ~10 seconds - disproportionate to our threat
// model where the vault is in the user's home with 0600 perms.
func freshKDFParams() (kdfParams, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return kdfParams{}, err
	}
	return kdfParams{
		Algo:    "argon2id",
		Time:    2,
		Memory:  19 * 1024, // 19 MiB
		Threads: 1,
		SaltB64: b64encode(salt),
	}, nil
}

func deriveKey(passphrase []byte, p kdfParams) ([]byte, error) {
	if p.Algo != "argon2id" {
		return nil, fmt.Errorf("unsupported KDF: %s", p.Algo)
	}
	salt, err := b64decode(p.SaltB64)
	if err != nil {
		return nil, fmt.Errorf("salt b64: %w", err)
	}
	return argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, 32), nil
}

// padSizeFor rounds the plaintext length up to the next bucket so
// on-disk ciphertext sizes don't leak the original secret length to
// an attacker who can read vault.enc but not decrypt it. Buckets
// stop at 16 KiB; rarely larger secrets (private keys) pad to the
// next 16 KiB multiple.
func padSizeFor(n int) int {
	switch {
	case n <= 60:
		return 60
	case n <= 1020:
		return 1020
	case n <= 4092:
		return 4092
	case n <= 16380:
		return 16380
	}
	// Bigger than 16 KiB: round up to the next 16 KiB so a 17 KiB
	// secret and a 30 KiB secret bucket together.
	const big = 16380
	return ((n + big - 1) / big) * big
}

// padPlaintext prefixes the original length (uint32 big-endian) and
// pads the buffer to a bucket size. decrypt() reverses this.
func padPlaintext(plaintext []byte) []byte {
	target := padSizeFor(len(plaintext))
	// 4 length bytes + plaintext + zero padding
	out := make([]byte, 4+target)
	out[0] = byte(len(plaintext) >> 24)
	out[1] = byte(len(plaintext) >> 16)
	out[2] = byte(len(plaintext) >> 8)
	out[3] = byte(len(plaintext))
	copy(out[4:], plaintext)
	return out
}

// unpadPlaintext is the inverse: read the 4-byte length and slice
// off any padding. Tolerates legacy entries written before padding
// was added (no length prefix) by sniffing whether the leading
// uint32 plausibly addresses something inside the buffer.
func unpadPlaintext(padded []byte) []byte {
	if len(padded) < 4 {
		return padded
	}
	declared := uint32(padded[0])<<24 | uint32(padded[1])<<16 | uint32(padded[2])<<8 | uint32(padded[3])
	if int(declared) <= len(padded)-4 {
		return padded[4 : 4+declared]
	}
	// Legacy entry written before padding shipped. Return raw so
	// existing vaults keep working until the next Put rewrites
	// them under the new format.
	return padded
}

// SealBlob encrypts arbitrary bytes with the vault's data key, returning a
// self-contained nonce||ciphertext blob. Used for large at-rest caches (e.g.
// the Bitwarden sync payload) that shouldn't bloat the JSON account store.
func (v *UnlockedVault) SealBlob(plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ct...), nil
}

// OpenBlob reverses SealBlob.
func (v *UnlockedVault) OpenBlob(blob []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("sealed blob too short")
	}
	pt, err := aead.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return pt, nil
}

func encrypt(key, plaintext []byte) (vaultItem, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return vaultItem{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return vaultItem{}, err
	}
	ct := aead.Seal(nil, nonce, padPlaintext(plaintext), nil)
	return vaultItem{NonceB64: b64encode(nonce), CTB64: b64encode(ct)}, nil
}

func decrypt(key []byte, item vaultItem) ([]byte, error) {
	nonce, err := b64decode(item.NonceB64)
	if err != nil {
		return nil, fmt.Errorf("nonce b64: %w", err)
	}
	ct, err := b64decode(item.CTB64)
	if err != nil {
		return nil, fmt.Errorf("ct b64: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	pt = unpadPlaintext(pt)
	return pt, nil
}

// FileExists is a cheap check used by the vault status query.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// InitVault creates a new file vault under `path` with the given passphrase.
// Returns the unlocked vault. Errors if a file already exists at `path`.
func InitVault(path, passphrase string) (*UnlockedVault, error) {
	if FileExists(path) {
		return nil, fmt.Errorf("vault already initialized: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	kdf, err := freshKDFParams()
	if err != nil {
		return nil, err
	}
	key, err := deriveKey([]byte(passphrase), kdf)
	if err != nil {
		return nil, err
	}
	sentinel, err := encrypt(key, sentinelPlaintext)
	if err != nil {
		return nil, err
	}
	v := &UnlockedVault{
		path: path,
		key:  key,
		file: vaultFile{
			Version: fileVersion,
			KDF:     kdf,
			Entries: map[string]vaultItem{sentinelKey: sentinel},
		},
	}
	if err := v.flush(); err != nil {
		return nil, err
	}
	return v, nil
}

// UnlockVault opens an existing file vault. Validates passphrase via sentinel
// entry before returning.
func UnlockVault(path, passphrase string) (*UnlockedVault, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f vaultFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrCorrupt, f.Version)
	}
	key, err := deriveKey([]byte(passphrase), f.KDF)
	if err != nil {
		return nil, err
	}
	sentinel, ok := f.Entries[sentinelKey]
	if !ok {
		return nil, fmt.Errorf("%w: missing sentinel", ErrCorrupt)
	}
	pt, err := decrypt(key, sentinel)
	if err != nil {
		return nil, err // already ErrWrongPassphrase on AEAD failure
	}
	if string(pt) != string(sentinelPlaintext) {
		return nil, ErrWrongPassphrase
	}
	return &UnlockedVault{path: path, key: key, file: f}, nil
}

func (v *UnlockedVault) Get(account string) (string, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	item, ok := v.file.Entries[account]
	if !ok {
		return "", false, nil
	}
	pt, err := decrypt(v.key, item)
	if err != nil {
		return "", false, err
	}
	return string(pt), true, nil
}

func (v *UnlockedVault) Put(account, secret string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	item, err := encrypt(v.key, []byte(secret))
	if err != nil {
		return err
	}
	v.file.Entries[account] = item
	return v.flush()
}

func (v *UnlockedVault) Delete(account string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.file.Entries[account]; ok {
		delete(v.file.Entries, account)
		return v.flush()
	}
	return nil
}

// ChangePassphrase re-derives the file key from a new passphrase, re-
// encrypts every entry under the new key, and atomically writes the
// result. The current in-memory key is replaced only after a successful
// flush so a mid-rotation failure leaves the live vault usable.
//
// Caller is responsible for verifying the OLD passphrase first (typically
// via Unlock) so a bug doesn't silently wipe the user's secrets.
func (v *UnlockedVault) ChangePassphrase(newPassphrase string) error {
	if newPassphrase == "" {
		return errors.New("vault: empty new passphrase")
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	newKDF, err := freshKDFParams()
	if err != nil {
		return err
	}
	newKey, err := deriveKey([]byte(newPassphrase), newKDF)
	if err != nil {
		return err
	}

	reEnc := make(map[string]vaultItem, len(v.file.Entries))
	for account, item := range v.file.Entries {
		var pt []byte
		if account == sentinelKey {
			pt = sentinelPlaintext
		} else {
			p, err := decrypt(v.key, item)
			if err != nil {
				return fmt.Errorf("vault: re-decrypt %s: %w", account, err)
			}
			pt = p
		}
		newItem, err := encrypt(newKey, pt)
		if err != nil {
			return fmt.Errorf("vault: re-encrypt %s: %w", account, err)
		}
		reEnc[account] = newItem
	}

	prevKDF := v.file.KDF
	prevEntries := v.file.Entries
	prevKey := v.key

	v.file.KDF = newKDF
	v.file.Entries = reEnc
	v.key = newKey

	if err := v.flush(); err != nil {
		v.file.KDF = prevKDF
		v.file.Entries = prevEntries
		v.key = prevKey
		return err
	}
	return nil
}

func (v *UnlockedVault) flush() error {
	data, err := json.MarshalIndent(v.file, "", "  ")
	if err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.path)
}

// b64* helpers - standard base64, padded.

func b64encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
func b64decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// Accounts returns every stored account name (excluding the internal
// sentinel). Used by the live sync-pull merge to enumerate the source
// vault's secrets.
func (v *UnlockedVault) Accounts() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.file.Entries))
	for k := range v.file.Entries {
		if k == sentinelKey {
			continue
		}
		out = append(out, k)
	}
	return out
}
