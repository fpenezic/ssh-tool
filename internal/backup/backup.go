// Package backup creates and restores encrypted snapshots of the user's
// data directory. A backup bundles store.db + vault.enc + a manifest
// into a single XChaCha20-Poly1305 payload keyed by the user's vault
// master passphrase. The machine-bound sidecar is intentionally NOT
// included - restores on a different machine fall through to the
// passphrase prompt, and the sidecar regenerates after the first
// successful unlock there.
//
// Restore takes a safety snapshot of the current store.db + vault.enc
// before overwriting them, so a mistaken restore leaves an undo path
// next to the data files.
package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	_ "modernc.org/sqlite"

	"ssh-tool/internal/creds"
)

var base64Std = base64.StdEncoding

// File layout (little-endian where applicable):
//
//	magic           "SSHTOOL\x01"        8 bytes
//	salt_len        1 byte = 16
//	salt            16 bytes (Argon2id)
//	kdf_time        4 bytes (uint32, big-endian)
//	kdf_memory_kib  4 bytes (uint32, big-endian)
//	kdf_threads     1 byte
//	nonce_len       1 byte = 24
//	nonce           24 bytes (XChaCha20 nonce)
//	ciphertext      remainder (AEAD-sealed tar payload + auth tag)
//
// Plaintext payload is a JSON envelope:
//
//	{
//	  "version": 1,
//	  "created_at": "...",
//	  "app_version": "...",
//	  "store_db": "<base64>",
//	  "vault_enc": "<base64>",
//	  "store_sha256": "...",
//	  "vault_sha256": "..."
//	}

const (
	// magic identifies the on-disk format and version. v1 sealed
	// the JSON envelope with nil AAD - leaving the header (kdf
	// params, salt, nonce) unauthenticated. v2 keeps the same layout
	// but binds the entire header as AEAD additional data, so any
	// tamper of those bytes fails the Poly1305 tag at Open(). v1
	// files still decrypt via decodeLegacyV1 for backward compat.
	magic       = "SSHTOOL\x02"
	legacyMagic = "SSHTOOL\x01"
	currentVer  = 1
	kdfTime     = 2
	kdfMemKiB   = 64 * 1024 // 64 MiB - backups can afford it (one-shot, not interactive)
	kdfThreads  = 1
	saltLen     = 16
	nonceLen    = 24 // XChaCha20
	keyLen      = 32
	backupExt   = ".sshtool-backup"
	backupDir   = "backups"
	snapshotDir = "pre-restore"
)

var ErrWrongPassphrase = errors.New("backup: wrong passphrase or corrupted file")

// Envelope is the JSON payload sealed inside the AEAD ciphertext.
type Envelope struct {
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	AppVersion  string `json:"app_version"`
	StoreDBB64  string `json:"store_db"`
	VaultEncB64 string `json:"vault_enc"`
	StoreSHA256 string `json:"store_sha256"`
	VaultSHA256 string `json:"vault_sha256"`
}

// Info describes a backup file on disk without decrypting it.
type Info struct {
	Path      string    `json:"path"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Create writes an encrypted backup of the data directory to destPath.
// `storeDBPath` and `vaultEncPath` are the live data files; storeDBPath
// is snapshotted via SQLite's VACUUM INTO to dodge the WAL.
// `passphrase` must match the live vault passphrase (caller verifies
// that separately by attempting to unlock the vault first).
func Create(destPath, storeDBPath, vaultEncPath, passphrase, appVersion string) error {
	if passphrase == "" {
		return errors.New("backup: empty passphrase")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return fmt.Errorf("backup: mkdir dest: %w", err)
	}

	// Atomic SQLite snapshot via VACUUM INTO.
	tmpDB, err := os.CreateTemp(filepath.Dir(destPath), "store-snap-*.db")
	if err != nil {
		return fmt.Errorf("backup: temp store: %w", err)
	}
	tmpDBPath := tmpDB.Name()
	_ = tmpDB.Close()
	_ = os.Remove(tmpDBPath) // VACUUM INTO refuses to overwrite
	defer os.Remove(tmpDBPath)

	if err := sqliteSnapshot(storeDBPath, tmpDBPath); err != nil {
		return fmt.Errorf("backup: store snapshot: %w", err)
	}

	storeBytes, err := os.ReadFile(tmpDBPath)
	if err != nil {
		return fmt.Errorf("backup: read store snap: %w", err)
	}
	vaultBytes, err := os.ReadFile(vaultEncPath)
	if err != nil {
		return fmt.Errorf("backup: read vault: %w", err)
	}

	env := Envelope{
		Version:     currentVer,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		AppVersion:  appVersion,
		StoreDBB64:  b64(storeBytes),
		VaultEncB64: b64(vaultBytes),
		StoreSHA256: sha256hex(storeBytes),
		VaultSHA256: sha256hex(vaultBytes),
	}
	plaintext, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("backup: marshal: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	key := argon2.IDKey([]byte(passphrase), salt, kdfTime, kdfMemKiB, kdfThreads, keyLen)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	// Build the on-disk header up front and pass it as AAD to the
	// AEAD. This binds magic + KDF params + salt + nonce to the
	// ciphertext: an attacker who flips m=64MiB to m=1<<31 (DoS)
	// or downgrades the version byte will fail the Poly1305 tag at
	// Open() time instead of triggering an Argon2 OOM. See the
	// security audit H1 (vault) finding.
	header := buildHeader(salt, nonce, kdfTime, kdfMemKiB, kdfThreads)
	ct := aead.Seal(nil, nonce, plaintext, header)

	// Write to a sibling temp file then rename for atomicity.
	tmpOut, err := os.CreateTemp(filepath.Dir(destPath), "backup-*.tmp")
	if err != nil {
		return err
	}
	tmpOutPath := tmpOut.Name()
	if err := writeBackupRaw(tmpOut, header, ct); err != nil {
		_ = tmpOut.Close()
		_ = os.Remove(tmpOutPath)
		return err
	}
	if err := tmpOut.Close(); err != nil {
		_ = os.Remove(tmpOutPath)
		return err
	}
	if err := os.Rename(tmpOutPath, destPath); err != nil {
		_ = os.Remove(tmpOutPath)
		return err
	}
	_ = os.Chmod(destPath, 0o600)
	return nil
}

// PendingDir is the staging directory where a Restore parks the new
// store.db + vault.enc to be swapped in at next start. Live process
// handles on Windows hold store.db open, so in-place rename fails with
// "access is denied"; we defer the swap to ApplyPending before the
// next sql.Open.
const PendingDir = "pending-restore"

// Restore decrypts srcPath with passphrase, verifies checksums, snapshots
// the current live store.db + vault.enc into <dataDir>/backups/pre-restore-
// <timestamp>/, and stages the new files in <dataDir>/pending-restore/.
// The actual swap happens in ApplyPending at the next app start.
func Restore(srcPath, passphrase, storeDBPath, vaultEncPath string) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	env, err := decodeAndDecrypt(raw, passphrase)
	if err != nil {
		return err
	}

	storeBytes, err := unb64(env.StoreDBB64)
	if err != nil {
		return fmt.Errorf("backup: store b64: %w", err)
	}
	vaultBytes, err := unb64(env.VaultEncB64)
	if err != nil {
		return fmt.Errorf("backup: vault b64: %w", err)
	}
	if sha256hex(storeBytes) != env.StoreSHA256 {
		return errors.New("backup: store checksum mismatch")
	}
	if sha256hex(vaultBytes) != env.VaultSHA256 {
		return errors.New("backup: vault checksum mismatch")
	}

	dataDir := filepath.Dir(storeDBPath)

	// Safety snapshot of current live files before we even stage.
	// We bundle them into the SAME encrypted-backup envelope as a
	// regular Create() snapshot, sealed with the live vault
	// passphrase that the caller already proved they hold. Earlier
	// versions wrote raw store.db + vault.enc into a pre-restore
	// directory - that worked but left a plaintext copy of every
	// stored connection / secret on disk under 0700 dir perms only.
	// Encrypting closes that gap (audit M-finding "pre-restore
	// plaintext"); recovery is now identical to restoring any
	// other backup file.
	snapFilename := fmt.Sprintf("%s-%s%s", snapshotDir, time.Now().UTC().Format("20060102-150405"), backupExt)
	snapPath := filepath.Join(dataDir, backupDir, snapFilename)
	if err := os.MkdirAll(filepath.Dir(snapPath), 0o700); err != nil {
		return fmt.Errorf("backup: snap dir: %w", err)
	}
	if err := Create(snapPath, storeDBPath, vaultEncPath, passphrase, "pre-restore"); err != nil {
		return fmt.Errorf("backup: pre-restore snapshot: %w", err)
	}

	pending := filepath.Join(dataDir, PendingDir)
	if err := os.MkdirAll(pending, 0o700); err != nil {
		return fmt.Errorf("backup: pending dir: %w", err)
	}

	// Stage encrypted copies of the new files. The passphrase the
	// caller provided is verified against the source backup, so we
	// can safely turn it into a fresh AEAD key for the per-stage
	// blobs. This closes the v0.16 audit M-finding "pending-restore
	// staging is plaintext until next start".
	stagedStoreBlob, err := sealStaged(storeBytes, passphrase)
	if err != nil {
		return fmt.Errorf("backup: seal staged store: %w", err)
	}
	stagedVaultBlob, err := sealStaged(vaultBytes, passphrase)
	if err != nil {
		return fmt.Errorf("backup: seal staged vault: %w", err)
	}
	if err := atomicWrite(filepath.Join(pending, "store.db.enc"), stagedStoreBlob); err != nil {
		return fmt.Errorf("backup: stage store: %w", err)
	}
	if err := atomicWrite(filepath.Join(pending, "vault.enc.enc"), stagedVaultBlob); err != nil {
		return fmt.Errorf("backup: stage vault: %w", err)
	}
	// Wipe the cleartext copies - sealed bytes live on disk now.
	for i := range storeBytes {
		storeBytes[i] = 0
	}
	for i := range vaultBytes {
		vaultBytes[i] = 0
	}

	// Seal the passphrase under the machine+user key so ApplyPending
	// can decrypt the staged files at next start without prompting.
	// A stolen pending-restore/ directory alone is useless: without
	// the host machine, OpenForMachine() fails AEAD verify and the
	// staged files stay sealed.
	sealedPass, err := creds.SealForMachine([]byte(passphrase))
	if err != nil {
		return fmt.Errorf("backup: seal pending passphrase: %w", err)
	}
	readyJSON, err := json.Marshal(struct {
		Version             int    `json:"version"`
		StagedAt            string `json:"staged_at"`
		Source              string `json:"source"`
		SealedPassphraseB64 string `json:"sealed_passphrase_b64"`
	}{
		Version:             2,
		StagedAt:            time.Now().UTC().Format(time.RFC3339),
		Source:              filepath.Base(srcPath),
		SealedPassphraseB64: base64Std.EncodeToString(sealedPass),
	})
	if err != nil {
		return fmt.Errorf("backup: marshal ready: %w", err)
	}
	if err := os.WriteFile(filepath.Join(pending, "READY"), readyJSON, 0o600); err != nil {
		return fmt.Errorf("backup: ready flag: %w", err)
	}
	return nil
}

// sealStaged encrypts staged bytes with a per-stage key derived from
// the passphrase via a small Argon2 (we already verified the
// passphrase against the source backup, so this just turns it into a
// proper AEAD key). Returns the magic-prefixed nonce||salt||ct blob
// readStagedDecrypt expects.
func sealStaged(plaintext []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(passphrase), salt, kdfTime, kdfMemKiB, kdfThreads, keyLen)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, []byte("ssh-tool-pending-v1"))
	out := make([]byte, 0, len(salt)+len(nonce)+len(ct))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// openStaged is sealStaged's inverse.
func openStaged(blob []byte, passphrase string) ([]byte, error) {
	const minLen = saltLen + 24 // saltLen + chacha20 nonce
	if len(blob) < minLen {
		return nil, errors.New("staged: blob too short")
	}
	salt := blob[:saltLen]
	rest := blob[saltLen:]
	key := argon2.IDKey([]byte(passphrase), salt, kdfTime, kdfMemKiB, kdfThreads, keyLen)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	nonce := rest[:ns]
	ct := rest[ns:]
	return aead.Open(nil, nonce, ct, []byte("ssh-tool-pending-v1"))
}

// ApplyPending swaps in staged files from <dataDir>/pending-restore/ if
// the READY flag is present. Call this before opening the SQLite store
// on each app startup. Returns true if a restore was applied.
//
// Both target files are required to be writable when called: the swap
// will happen before sql.Open, so no process is holding them yet.
//
// Staged files sit on disk encrypted with the same passphrase the
// user typed at Restore time. The passphrase itself is held in the
// READY flag, sealed under the machine+user key (creds.SealForMachine).
// A thief who steals just `pending-restore/` away from this machine
// can't decrypt anything; on this machine the startup decrypts and
// swaps without re-prompting.
func ApplyPending(storeDBPath, vaultEncPath string) (bool, error) {
	dataDir := filepath.Dir(storeDBPath)
	pending := filepath.Join(dataDir, PendingDir)
	flag := filepath.Join(pending, "READY")
	readyBytes, err := os.ReadFile(flag)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	stagedStore := filepath.Join(pending, "store.db.enc")
	stagedVault := filepath.Join(pending, "vault.enc.enc")

	storeBlob, err := os.ReadFile(stagedStore)
	if err != nil {
		return false, fmt.Errorf("backup: staged store missing: %w", err)
	}
	vaultBlob, err := os.ReadFile(stagedVault)
	if err != nil {
		return false, fmt.Errorf("backup: staged vault missing: %w", err)
	}

	// READY is JSON now: {sealed_passphrase, source, staged_at}.
	// Tolerate the legacy plaintext-info READY shape (no sealed
	// passphrase, staged files at the old names) just long enough
	// that a v0.24 → v0.25 upgrade with a pending restore in
	// flight still applies - fall back to reading store.db /
	// vault.enc as raw bytes.
	sealed, sourceMissing := parseReadyFlag(readyBytes)
	if sourceMissing {
		// Legacy path: try old filenames + raw read.
		legacyStore := filepath.Join(pending, "store.db")
		legacyVault := filepath.Join(pending, "vault.enc")
		sb, sErr := os.ReadFile(legacyStore)
		vb, vErr := os.ReadFile(legacyVault)
		if sErr != nil || vErr != nil {
			return false, fmt.Errorf("backup: staged files unreadable (legacy READY)")
		}
		storeBytes := sb
		vaultBytes := vb
		if err := writePendingSwap(storeDBPath, vaultEncPath, storeBytes, vaultBytes); err != nil {
			return false, err
		}
		_ = os.Remove(legacyStore)
		_ = os.Remove(legacyVault)
	} else {
		passBytes, err := creds.OpenForMachine(sealed)
		if err != nil {
			return false, fmt.Errorf("backup: unseal pending passphrase (different machine?): %w", err)
		}
		passphrase := string(passBytes)
		// Wipe the decrypted passphrase after we're done.
		defer func() {
			for i := range passBytes {
				passBytes[i] = 0
			}
		}()

		storeBytes, err := openStaged(storeBlob, passphrase)
		if err != nil {
			return false, fmt.Errorf("backup: decrypt staged store: %w", err)
		}
		vaultBytes, err := openStaged(vaultBlob, passphrase)
		if err != nil {
			return false, fmt.Errorf("backup: decrypt staged vault: %w", err)
		}
		if err := writePendingSwap(storeDBPath, vaultEncPath, storeBytes, vaultBytes); err != nil {
			return false, err
		}
		_ = os.Remove(stagedStore)
		_ = os.Remove(stagedVault)
	}

	// Sidecar is invalidated by the swap (different passphrase scope).
	_ = os.Remove(vaultEncPath + ".local.key")

	// Clean up staging dir.
	_ = os.Remove(flag)
	_ = os.Remove(pending) // succeeds only if empty
	return true, nil
}

// writePendingSwap is the file-system half of ApplyPending: clear
// existing live files (removing WAL/SHM siblings of the SQLite store
// first) and write the new bytes in their place.
func writePendingSwap(storeDBPath, vaultEncPath string, storeBytes, vaultBytes []byte) error {
	_ = os.Remove(storeDBPath + "-wal")
	_ = os.Remove(storeDBPath + "-shm")
	if err := os.Remove(storeDBPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("backup: clear live store: %w", err)
	}
	if err := atomicWrite(storeDBPath, storeBytes); err != nil {
		return fmt.Errorf("backup: write store: %w", err)
	}
	if err := os.Remove(vaultEncPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("backup: clear live vault: %w", err)
	}
	if err := atomicWrite(vaultEncPath, vaultBytes); err != nil {
		return fmt.Errorf("backup: write vault: %w", err)
	}
	return nil
}

// parseReadyFlag returns (sealedPassphrase, sourceMissing). The new
// READY format is JSON {sealed_passphrase_b64, source, staged_at};
// the legacy v0.24 format was a plain "key=value" text block with
// no sealed passphrase. legacy=true tells the caller to fall back
// to raw-bytes staging.
func parseReadyFlag(raw []byte) ([]byte, bool) {
	type readyV2 struct {
		SealedPassphraseB64 string `json:"sealed_passphrase_b64"`
	}
	var v2 readyV2
	if err := json.Unmarshal(raw, &v2); err == nil && v2.SealedPassphraseB64 != "" {
		sealed, err := base64.StdEncoding.DecodeString(v2.SealedPassphraseB64)
		if err == nil {
			return sealed, false
		}
	}
	return nil, true
}

// List returns the backups found in <dataDir>/backups/ sorted newest
// first. Pre-restore snapshot dirs are skipped.
func List(dataDir string) ([]Info, error) {
	dir := filepath.Join(dataDir, backupDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), backupExt) {
			continue
		}
		// Pre-restore safety snapshots use the same envelope format
		// but live under a reserved name prefix; hide them from the
		// regular "your backups" list so users don't confuse them
		// with intentional backups.
		if strings.HasPrefix(e.Name(), snapshotDir+"-") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			Path:      filepath.Join(dir, e.Name()),
			Filename:  e.Name(),
			Size:      fi.Size(),
			CreatedAt: fi.ModTime().UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// DefaultDir returns the conventional backups subdirectory inside dataDir.
func DefaultDir(dataDir string) string { return filepath.Join(dataDir, backupDir) }

// SuggestedFilename builds the canonical "ssh-tool-backup-<ts>.sshtool-backup" name.
func SuggestedFilename(now time.Time) string {
	return fmt.Sprintf("ssh-tool-backup-%s%s", now.UTC().Format("20060102-150405"), backupExt)
}

// ---------- internals ----------

// buildHeader serialises the per-file metadata (magic, kdf params,
// salt, nonce lengths and values) into a single byte slice that is
// then used both as the on-disk prefix AND as AEAD additional data
// at Seal/Open time. Keeping a single canonical encoding for both
// purposes means a tamper of any byte breaks the AEAD tag - there
// is no "header-only" parse path that touches Argon2 before we've
// authenticated the params.
func buildHeader(salt, nonce []byte, t, mem uint32, threads uint8) []byte {
	out := make([]byte, 0, len(magic)+1+len(salt)+4+4+1+1+len(nonce))
	out = append(out, []byte(magic)...)
	out = append(out, byte(len(salt)))
	out = append(out, salt...)
	out = append(out, u32be(t)...)
	out = append(out, u32be(mem)...)
	out = append(out, threads)
	out = append(out, byte(len(nonce)))
	out = append(out, nonce...)
	return out
}

func writeBackupRaw(w io.Writer, header, ct []byte) error {
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(ct); err != nil {
		return err
	}
	return nil
}

func decodeAndDecrypt(raw []byte, passphrase string) (*Envelope, error) {
	if len(raw) < len(magic)+1+saltLen+4+4+1+1+nonceLen+chacha20poly1305.Overhead {
		return nil, errors.New("backup: file too short")
	}
	off := 0
	// Allow v1 (legacy nil-AAD) or v2 (header-as-AAD). Both share
	// the same layout from this point on; only the AAD differs.
	useAAD := true
	switch {
	case len(raw) >= len(magic) && string(raw[off:off+len(magic)]) == magic:
		// v2
	case len(raw) >= len(legacyMagic) && string(raw[off:off+len(legacyMagic)]) == legacyMagic:
		useAAD = false
	default:
		return nil, errors.New("backup: bad magic")
	}
	off += len(magic)
	if int(raw[off]) != saltLen {
		return nil, errors.New("backup: bad salt len")
	}
	off++
	salt := raw[off : off+saltLen]
	off += saltLen
	t := u32beDecode(raw[off : off+4])
	off += 4
	mem := u32beDecode(raw[off : off+4])
	off += 4
	threads := raw[off]
	off++
	if int(raw[off]) != nonceLen {
		return nil, errors.New("backup: bad nonce len")
	}
	off++
	nonce := raw[off : off+nonceLen]
	off += nonceLen
	ct := raw[off:]
	// The header on disk is raw[0:off]. For v2 files use it
	// verbatim as AAD - any tampering (param flip, nonce swap,
	// magic bump) will fail the AEAD tag check below. v1 files
	// were sealed with nil AAD so we keep that for backwards
	// compatibility while urging users to re-create those backups
	// under v2.
	var header []byte
	if useAAD {
		header = raw[0:off]
	}

	// Sanity-bound the KDF params BEFORE deriving - even though the
	// AAD check will reject a tampered file, that check happens
	// after Argon2 has already run, so a hostile m=1<<31 could OOM
	// us first. These bounds are well above what Create writes and
	// well below anything dangerous on a desktop.
	if t == 0 || t > 16 {
		return nil, errors.New("backup: kdf time out of range")
	}
	if mem < 8 || mem > 1024*1024 { // 8 KiB .. 1 GiB
		return nil, errors.New("backup: kdf memory out of range")
	}
	if threads == 0 || threads > 16 {
		return nil, errors.New("backup: kdf threads out of range")
	}

	key := argon2.IDKey([]byte(passphrase), salt, t, mem, threads, keyLen)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, header)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	var env Envelope
	if err := json.Unmarshal(pt, &env); err != nil {
		return nil, fmt.Errorf("backup: envelope parse: %w", err)
	}
	if env.Version != currentVer {
		return nil, fmt.Errorf("backup: unsupported envelope version %d", env.Version)
	}
	return &env, nil
}

func sqliteSnapshot(srcPath, destPath string) error {
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return err
	}
	defer db.Close()
	// VACUUM INTO requires the target to NOT exist; the caller already
	// removed it but be defensive.
	_, err = db.Exec(fmt.Sprintf("VACUUM INTO %s", quoteSQLString(destPath)))
	return err
}

func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func b64(b []byte) string {
	return base64Std.EncodeToString(b)
}

func unb64(s string) ([]byte, error) {
	return base64Std.DecodeString(s)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func u32be(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

func u32beDecode(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// Extract decrypts an encrypted backup/sync envelope into two temp
// files (store.db, vault.enc) WITHOUT staging or swapping anything.
// Used by the live sync-pull path, which mirrors the store and merges
// the vault into the running app instead of replacing files on disk.
// Caller owns the returned temp files and must remove them.
//
// Checksums are verified exactly as in Restore - a corrupt or tampered
// envelope is rejected before the caller touches its contents.
func Extract(srcPath, passphrase string) (storeTmp, vaultTmp string, err error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return "", "", err
	}
	env, err := decodeAndDecrypt(raw, passphrase)
	if err != nil {
		return "", "", err
	}
	storeBytes, err := unb64(env.StoreDBB64)
	if err != nil {
		return "", "", fmt.Errorf("backup: store b64: %w", err)
	}
	vaultBytes, err := unb64(env.VaultEncB64)
	if err != nil {
		return "", "", fmt.Errorf("backup: vault b64: %w", err)
	}
	if sha256hex(storeBytes) != env.StoreSHA256 {
		return "", "", errors.New("backup: store checksum mismatch")
	}
	if sha256hex(vaultBytes) != env.VaultSHA256 {
		return "", "", errors.New("backup: vault checksum mismatch")
	}

	sf, err := os.CreateTemp("", "ssh-tool-pull-store-*.db")
	if err != nil {
		return "", "", err
	}
	storeTmp = sf.Name()
	_, werr := sf.Write(storeBytes)
	_ = sf.Close()
	if werr != nil {
		os.Remove(storeTmp)
		return "", "", werr
	}

	vf, err := os.CreateTemp("", "ssh-tool-pull-vault-*.enc")
	if err != nil {
		os.Remove(storeTmp)
		return "", "", err
	}
	vaultTmp = vf.Name()
	_, werr = vf.Write(vaultBytes)
	_ = vf.Close()
	if werr != nil {
		os.Remove(storeTmp)
		os.Remove(vaultTmp)
		return "", "", werr
	}
	return storeTmp, vaultTmp, nil
}
