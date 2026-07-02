package syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ssh-tool/internal/backup"
)

const (
	metaName     = "meta.json"
	snapshotName = "snapshot.stb"
	snapshotTmp  = "snapshot.stb.uploading"
)

// Transport is the small storage surface sync needs from a backend. The
// server only ever sees ciphertext (the sealed snapshot) plus the tiny
// plaintext meta.json, so a transport is just a named blob store. WebDAV
// and SFTP both implement it. Get returns ErrNotFound for a missing name;
// Move overwrites the destination (atomic snapshot replacement); EnsureDir
// creates the sync directory if missing.
type Transport interface {
	EnsureDir() error
	Get(name string) ([]byte, error)
	Put(name string, data []byte) error
	Move(from, to string) error
	// Close releases any held connection. No-op for the stateless WebDAV
	// transport; SFTP closes its SSH session. Callers defer it after
	// building a transport so the SFTP connection doesn't leak.
	Close()
}

// Meta is the small plaintext commit record next to the snapshot.
// Generation is the optimistic-concurrency token: push requires the
// remote generation to equal the one this machine last saw. Carries
// no sensitive data - device is a user-chosen label / hostname.
type Meta struct {
	Format       int    `json:"format"`
	Generation   int64  `json:"generation"`
	UpdatedAt    string `json:"updated_at"`
	Device       string `json:"device"`
	AppVersion   string `json:"app_version"`
	SnapshotSize int64  `json:"snapshot_size"`
}

// FetchMeta reads the remote commit record. ErrNotFound when the
// directory has never been pushed to.
func FetchMeta(dav Transport) (*Meta, error) {
	raw, err := dav.Get(metaName)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("remote meta.json is corrupt: %w", err)
	}
	return &m, nil
}

// PushResult reports what landed.
type PushResult struct {
	Generation   int64 `json:"generation"`
	SnapshotSize int64 `json:"snapshot_size"`
}

// Push seals the live profile into a backup envelope and uploads it.
// knownGen is the remote generation this machine last observed (0 =
// never synced); unless force is set, a differing remote generation
// aborts with a pull-first error so a second machine's changes can't
// be silently overwritten.
//
// onGeneration (optional) runs with the NEW generation after the
// guard passes but BEFORE the snapshot is sealed. The caller persists
// the generation into the store there, so the uploaded snapshot
// already carries it - a machine that later pulls this snapshot comes
// up in-sync instead of perpetually one generation behind its own
// data. On any later push error the caller must roll the value back.
func Push(dav Transport, storeDBPath, vaultEncPath, passphrase, appVersion, device string, knownGen int64, force bool, onGeneration func(int64) error) (*PushResult, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("sync passphrase is not set")
	}
	if err := dav.EnsureDir(); err != nil {
		return nil, fmt.Errorf("sync dir: %w", err)
	}

	remote, err := FetchMeta(dav)
	switch {
	case err == ErrNotFound:
		remote = nil // first push into an empty dir
	case err != nil:
		return nil, err
	}
	if remote != nil && remote.Generation != knownGen && !force {
		return nil, fmt.Errorf(
			"remote has generation %d (last seen here: %d) - pull first, or force-push to overwrite",
			remote.Generation, knownGen)
	}

	gen := knownGen + 1
	if remote != nil && remote.Generation >= gen {
		gen = remote.Generation + 1 // force-push over a diverged remote
	}
	if onGeneration != nil {
		if err := onGeneration(gen); err != nil {
			return nil, fmt.Errorf("persist generation: %w", err)
		}
	}

	// Seal locally: same envelope as Backup (store.db via VACUUM INTO
	// + vault.enc, argon2id + XChaCha20-Poly1305). The server only
	// ever receives this ciphertext.
	tmp, err := os.CreateTemp("", "ssh-tool-sync-*.stb")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	if err := backup.Create(tmpPath, storeDBPath, vaultEncPath, passphrase, appVersion); err != nil {
		return nil, fmt.Errorf("seal snapshot: %w", err)
	}
	blob, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}

	// Upload snapshot under a temp name, MOVE over the live one
	// (atomic on real WebDAV servers), then commit by writing meta
	// LAST - a reader that sees the new meta is guaranteed the new
	// snapshot is fully in place.
	if err := dav.Put(snapshotTmp, blob); err != nil {
		return nil, err
	}
	if err := dav.Move(snapshotTmp, snapshotName); err != nil {
		// Some minimal servers lack MOVE - fall back to a direct PUT.
		if err2 := dav.Put(snapshotName, blob); err2 != nil {
			return nil, fmt.Errorf("snapshot upload: %v (MOVE: %v)", err2, err)
		}
	}

	meta := Meta{
		Format:       1,
		Generation:   gen,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		Device:       device,
		AppVersion:   appVersion,
		SnapshotSize: int64(len(blob)),
	}
	mraw, _ := json.Marshal(meta)
	if err := dav.Put(metaName, mraw); err != nil {
		return nil, fmt.Errorf("commit meta: %w", err)
	}
	return &PushResult{Generation: gen, SnapshotSize: int64(len(blob))}, nil
}

// PullResult reports what was staged.
type PullResult struct {
	Generation int64  `json:"generation"`
	Device     string `json:"device"`
	UpdatedAt  string `json:"updated_at"`
}

// Pull downloads the remote snapshot and stages it through the
// backup-restore path (pending-restore dir + READY flag, applied on
// next start - live SQLite files can't be swapped in-process on
// Windows). The caller tells the user to restart.
func Pull(dav Transport, passphrase, storeDBPath, vaultEncPath string) (*PullResult, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("sync passphrase is not set")
	}
	meta, err := FetchMeta(dav)
	if err == ErrNotFound {
		return nil, fmt.Errorf("nothing to pull - the sync folder is empty")
	}
	if err != nil {
		return nil, err
	}
	blob, err := dav.Get(snapshotName)
	if err == ErrNotFound {
		return nil, fmt.Errorf("remote meta exists but the snapshot is missing - re-push from the other machine")
	}
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "ssh-tool-sync-*.stb")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		os.Remove(tmpPath)
		return nil, err
	}
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	// First pull on a brand-new machine: the vault may never have
	// been written. Restore() seals a pre-restore safety snapshot of
	// the LIVE files and errors on a missing vault.enc - an empty
	// placeholder keeps that path working (it's immediately replaced
	// by the restored vault anyway).
	if _, err := os.Stat(vaultEncPath); os.IsNotExist(err) {
		if err := os.WriteFile(vaultEncPath, []byte{}, 0o600); err != nil {
			return nil, fmt.Errorf("placeholder vault: %w", err)
		}
	}

	if err := backup.Restore(tmpPath, passphrase, storeDBPath, vaultEncPath); err != nil {
		return nil, err
	}
	return &PullResult{Generation: meta.Generation, Device: meta.Device, UpdatedAt: meta.UpdatedAt}, nil
}

// DefaultDevice is the label written into meta.json - hostname with a
// sensible fallback.
func DefaultDevice() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return filepath.Base(os.Args[0])
}
