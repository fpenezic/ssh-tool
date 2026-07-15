package keepass

// Manager owns the runtime lifecycle of every registered KeePass database: it
// fetches remote .kdbx files (with conditional GET so an unchanged file isn't
// re-downloaded), caches the ENCRYPTED bytes on disk, holds the DECRYPTED tree
// in memory, and wipes that memory when the vault locks. It is the single seam
// the app and resolver reach KeePass through.
//
// Freshness policy (the whole point of the remote-fetch design):
//   - fetch-on-unlock: the first open after an unlock pulls fresh.
//   - fetch-on-connect-if-stale: an open older than staleAfter re-checks the
//     remote before returning a secret, so a colleague's just-added entry is
//     seen without a manual step. If the remote is unreachable the cached copy
//     is used and the caller is told (Stale/Offline on the returned status).
//   - manual Refresh: forces a re-fetch regardless of age.
//
// It deliberately does NOT poll on a timer - checks hang off real events
// (unlock, connect, explicit refresh), never a background clock.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ssh-tool/internal/store"
)

// staleAfter is how old an in-memory open may be before a connect triggers a
// remote freshness re-check. Local databases are never stale (the file IS the
// source). Chosen to be long enough that a burst of connects doesn't hammer the
// remote, short enough that a change made minutes ago is picked up.
const staleAfter = 5 * time.Minute

// Fetcher pulls a remote .kdbx. Implementations wrap the WebDAV / SFTP
// transports. etagIn is the validator from the last successful fetch (may be
// empty); notModified=true means the remote confirmed the cached copy is
// current and data is nil.
type Fetcher interface {
	Fetch(db store.KeepassDatabase, etagIn string) (data []byte, etagOut string, notModified bool, err error)
}

// SecretReader returns a vault secret by account, and whether the vault is
// currently unlocked. Injected so this package doesn't import creds.
type SecretReader interface {
	Get(account string) (string, bool, error)
	Unlocked() bool
}

// Freshness describes where the secret behind a resolve came from, for the UI.
type Freshness string

const (
	FreshLocal   Freshness = "local"   // local file, always current
	FreshFetched Freshness = "fetched" // just pulled from remote
	FreshCached  Freshness = "cached"  // cache within staleAfter, no re-check
	FreshStale   Freshness = "stale"   // cache used because remote was unreachable
)

// openState is one decrypted database plus when it was opened and the freshness
// of the underlying file.
type openState struct {
	db       *DB
	openedAt time.Time
	fresh    Freshness
}

// Manager is concurrency-safe. resolve/open take the lock only around map
// access and the (serialized) open of a given database.
type Manager struct {
	store    *store.DB
	secrets  SecretReader
	fetcher  Fetcher
	cacheDir string

	mu   sync.Mutex
	open map[string]*openState // dbID -> decrypted state
}

// NewManager wires the manager. cacheDir is where remote .kdbx bytes are cached
// (created on demand, 0700).
func NewManager(db *store.DB, secrets SecretReader, fetcher Fetcher, cacheDir string) *Manager {
	return &Manager{
		store:    db,
		secrets:  secrets,
		fetcher:  fetcher,
		cacheDir: cacheDir,
		open:     make(map[string]*openState),
	}
}

// Forget drops every decrypted database. Called when the vault locks so KeePass
// secrets never outlive the vault - same lifecycle guarantee.
func (m *Manager) Forget() {
	m.mu.Lock()
	m.open = make(map[string]*openState)
	m.mu.Unlock()
}

// Resolve returns the secret for a credential reference, opening/refreshing the
// database as needed. The returned Freshness lets the caller surface where the
// secret came from (and warn when it's stale-because-offline).
func (m *Manager) Resolve(ref store.KeepassRef) (secret string, fresh Freshness, err error) {
	if !m.secrets.Unlocked() {
		return "", "", ErrVaultLocked
	}
	kdb, err := m.store.GetKeepassDatabase(ref.DBID)
	if err != nil {
		return "", "", fmt.Errorf("keepass: database %s not found: %w", ref.DBID, err)
	}
	st, err := m.ensureOpen(*kdb, false)
	if err != nil {
		return "", "", err
	}
	sec, err := st.db.Resolve(ref.EntryUUID, ref.Field)
	if err != nil {
		return "", st.fresh, err
	}
	return sec, st.fresh, nil
}

// Refresh forces a re-fetch + re-open of one database, ignoring staleness.
// Returns the resulting freshness (FreshFetched on success).
func (m *Manager) Refresh(dbID string) (Freshness, error) {
	if !m.secrets.Unlocked() {
		return "", ErrVaultLocked
	}
	kdb, err := m.store.GetKeepassDatabase(dbID)
	if err != nil {
		return "", err
	}
	st, err := m.ensureOpen(*kdb, true)
	if err != nil {
		return "", err
	}
	return st.fresh, nil
}

// Browse opens (if needed) and returns the entry tree for the picker.
func (m *Manager) Browse(dbID string) ([]GroupInfo, error) {
	if !m.secrets.Unlocked() {
		return nil, ErrVaultLocked
	}
	kdb, err := m.store.GetKeepassDatabase(dbID)
	if err != nil {
		return nil, err
	}
	st, err := m.ensureOpen(*kdb, false)
	if err != nil {
		return nil, err
	}
	return st.db.Browse(), nil
}

// ErrVaultLocked signals the caller to run the unlock gate before retrying,
// mirroring the SSH layer's ErrVaultLockedT.
var ErrVaultLocked = errors.New("keepass: vault is locked")

// ensureOpen returns a decrypted database, fetching/re-opening per the
// freshness policy. force=true always re-fetches (manual refresh).
func (m *Manager) ensureOpen(kdb store.KeepassDatabase, force bool) (*openState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, have := m.open[kdb.ID]
	if have && !force {
		if kdb.Source == store.KeepassLocal {
			return cur, nil // local file: opened == current
		}
		if time.Since(cur.openedAt) < staleAfter {
			cur.fresh = FreshCached
			return cur, nil
		}
		// Stale: fall through to a freshness re-check.
	}

	raw, fresh, err := m.loadBytes(kdb, force || have)
	if err != nil {
		// If a re-check failed but we still hold a decrypted copy, keep serving
		// it and mark it stale rather than breaking an in-flight connect.
		if have {
			cur.fresh = FreshStale
			return cur, nil
		}
		return nil, err
	}

	master, keyFile, err := m.unlockMaterial(kdb)
	if err != nil {
		return nil, err
	}
	db, err := Open(raw, keyFile, master)
	if err != nil {
		return nil, err
	}
	st := &openState{db: db, openedAt: time.Now(), fresh: fresh}
	m.open[kdb.ID] = st
	return st, nil
}

// loadBytes returns the encrypted .kdbx bytes and their freshness. For local
// sources it reads the file. For remote sources it fetches (conditional GET
// when recheck is set and an ETag exists), falling back to the on-disk cache.
func (m *Manager) loadBytes(kdb store.KeepassDatabase, recheck bool) ([]byte, Freshness, error) {
	if kdb.Source == store.KeepassLocal {
		b, err := os.ReadFile(kdb.Path)
		if err != nil {
			return nil, "", fmt.Errorf("keepass: read %s: %w", kdb.Path, err)
		}
		return b, FreshLocal, nil
	}

	etagIn := ""
	if recheck {
		etagIn = kdb.LastETag
	}
	data, etagOut, notModified, err := m.fetcher.Fetch(kdb, etagIn)
	if err != nil {
		// Remote unreachable: use the cache if we have one.
		if cached, cerr := m.readCache(kdb.ID); cerr == nil {
			return cached, FreshStale, nil
		}
		return nil, "", fmt.Errorf("keepass: fetch %s and no cache: %w", kdb.Name, err)
	}
	if notModified {
		cached, cerr := m.readCache(kdb.ID)
		if cerr != nil {
			return nil, "", fmt.Errorf("keepass: remote unchanged but cache missing: %w", cerr)
		}
		_ = m.store.TouchKeepassFetch(kdb.ID, time.Now().Unix(), etagOut)
		return cached, FreshCached, nil
	}
	if err := m.writeCache(kdb.ID, data); err != nil {
		return nil, "", err
	}
	_ = m.store.TouchKeepassFetch(kdb.ID, time.Now().Unix(), etagOut)
	return data, FreshFetched, nil
}

func (m *Manager) unlockMaterial(kdb store.KeepassDatabase) (master string, keyFile []byte, err error) {
	if kdb.MasterRef != "" {
		pw, ok, gerr := m.secrets.Get(kdb.MasterRef)
		if gerr != nil {
			return "", nil, fmt.Errorf("keepass: read master from vault: %w", gerr)
		}
		if !ok {
			return "", nil, ErrVaultLocked
		}
		master = pw
	}
	if kdb.KeyfileRef != "" {
		kf, ok, gerr := m.secrets.Get(kdb.KeyfileRef)
		if gerr != nil {
			return "", nil, fmt.Errorf("keepass: read key file from vault: %w", gerr)
		}
		if !ok {
			return "", nil, ErrVaultLocked
		}
		keyFile = []byte(kf)
	}
	if master == "" && len(keyFile) == 0 {
		return "", nil, errors.New("keepass: no master password or key file configured")
	}
	return master, keyFile, nil
}

func (m *Manager) cachePath(dbID string) string {
	return filepath.Join(m.cacheDir, dbID+".kdbx")
}

func (m *Manager) readCache(dbID string) ([]byte, error) {
	return os.ReadFile(m.cachePath(dbID))
}

func (m *Manager) writeCache(dbID string, data []byte) error {
	if err := os.MkdirAll(m.cacheDir, 0o700); err != nil {
		return fmt.Errorf("keepass: cache dir: %w", err)
	}
	// The cached blob is the encrypted .kdbx; 0600 anyway (defence in depth -
	// it is worthless without the vault-held master).
	return os.WriteFile(m.cachePath(dbID), data, 0o600)
}

// DeleteCache removes a database's cached bytes (called when the database is
// deregistered).
func (m *Manager) DeleteCache(dbID string) {
	_ = os.Remove(m.cachePath(dbID))
	m.mu.Lock()
	delete(m.open, dbID)
	m.mu.Unlock()
}
