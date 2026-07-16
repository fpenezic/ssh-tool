package bitwarden

// Manager owns the runtime lifecycle of every registered Bitwarden server: it
// logs in + syncs (pulling the encrypted vault over HTTP), caches the sync blob
// SEALED with the app vault on disk, holds the DECRYPTED vault in memory, and
// wipes that memory when the app vault locks. It is the single seam the app and
// resolver reach Bitwarden through - the HTTP sibling of keepass.Manager.
//
// Freshness policy (identical to keepass):
//   - fetch-on-unlock: the first resolve after an unlock pulls fresh.
//   - fetch-on-connect-if-stale: an open older than staleAfter re-syncs before
//     returning a secret, so a teammate's just-added org item is seen without a
//     manual step. If the server is unreachable the cached copy is used and the
//     caller is told (FreshStale).
//   - manual Sync: forces a re-fetch regardless of age.
//
// No timer polling - checks hang off real events (unlock, connect, explicit
// sync).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ssh-tool/internal/store"
)

// staleAfter is how old an in-memory open may be before a connect triggers a
// re-sync. Matches keepass.
const staleAfter = 5 * time.Minute

// SecretReader returns a vault secret by account and whether the vault is
// unlocked. Injected so this package doesn't import creds. Also used to read the
// per-server master password and the API-key credential's secret.
type SecretReader interface {
	Get(account string) (string, bool, error)
	Unlocked() bool
}

// Sealer encrypts/decrypts the on-disk sync cache with the app vault, so a
// stolen cache file is worthless without an unlock. Injected for the same
// reason as SecretReader.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// Freshness describes where a resolved secret came from, for the UI.
type Freshness string

const (
	FreshFetched Freshness = "fetched" // just synced from the server
	FreshCached  Freshness = "cached"  // cache within staleAfter, no re-sync
	FreshStale   Freshness = "stale"   // cache used because the server was unreachable
)

// ErrVaultLocked signals the caller to run the unlock gate before retrying.
var ErrVaultLocked = errors.New("bitwarden: vault is locked")

// CredentialLookup resolves a server's API-key credential ref to its client id
// and secret. Injected so the manager can log in without importing creds.
type CredentialLookup func(apiKeyRef string) (Credentials, error)

// Syncer logs in and pulls a server's vault, returning the raw sync JSON.
// Injected so tests can supply a fake without a live server; production wires
// the HTTP Client.
type Syncer interface {
	Sync(serverURL string, creds Credentials) ([]byte, error)
}

// httpSyncer is the production Syncer backed by the HTTP Client.
type httpSyncer struct{}

func (httpSyncer) Sync(serverURL string, creds Credentials) ([]byte, error) {
	return NewClient(serverURL).LoginAndSync(creds)
}

type openState struct {
	vault    *Vault
	openedAt time.Time
	fresh    Freshness
}

// Manager is concurrency-safe.
type Manager struct {
	store    *store.DB
	secrets  SecretReader
	sealer   Sealer
	lookup   CredentialLookup
	syncer   Syncer
	cacheDir string

	mu   sync.Mutex
	open map[string]*openState // serverID -> decrypted state
}

// NewManager wires the manager with the production HTTP syncer. cacheDir holds
// the sealed sync blobs.
func NewManager(db *store.DB, secrets SecretReader, sealer Sealer, lookup CredentialLookup, cacheDir string) *Manager {
	return newManager(db, secrets, sealer, lookup, httpSyncer{}, cacheDir)
}

// newManager is the injectable constructor (tests supply a fake Syncer).
func newManager(db *store.DB, secrets SecretReader, sealer Sealer, lookup CredentialLookup, syncer Syncer, cacheDir string) *Manager {
	return &Manager{
		store:    db,
		secrets:  secrets,
		sealer:   sealer,
		lookup:   lookup,
		syncer:   syncer,
		cacheDir: cacheDir,
		open:     make(map[string]*openState),
	}
}

// Forget drops every decrypted vault (called when the app vault locks) and
// zeroes key material.
func (m *Manager) Forget() {
	m.mu.Lock()
	for _, st := range m.open {
		st.vault.Forget()
	}
	m.open = make(map[string]*openState)
	m.mu.Unlock()
}

// Resolve returns the secret for a credential reference, syncing/refreshing as
// needed. The returned Freshness lets the caller surface where it came from.
func (m *Manager) Resolve(ref store.BitwardenRef) (secret string, fresh Freshness, err error) {
	if !m.secrets.Unlocked() {
		return "", "", ErrVaultLocked
	}
	srv, err := m.store.GetBitwardenServer(ref.ServerID)
	if err != nil {
		return "", "", fmt.Errorf("bitwarden: server %s not found: %w", ref.ServerID, err)
	}
	st, err := m.ensureOpen(*srv, false)
	if err != nil {
		return "", "", err
	}
	sec, err := st.vault.Resolve(ref.CipherID, ref.Field)
	if err != nil {
		return "", st.fresh, err
	}
	return sec, st.fresh, nil
}

// Sync forces a re-login + re-sync of one server, ignoring staleness.
func (m *Manager) Sync(serverID string) (Freshness, error) {
	if !m.secrets.Unlocked() {
		return "", ErrVaultLocked
	}
	srv, err := m.store.GetBitwardenServer(serverID)
	if err != nil {
		return "", err
	}
	st, err := m.ensureOpen(*srv, true)
	if err != nil {
		return "", err
	}
	return st.fresh, nil
}

// Browse opens (if needed) and returns the org/collection/item tree for the
// picker.
func (m *Manager) Browse(serverID string) ([]GroupInfo, error) {
	if !m.secrets.Unlocked() {
		return nil, ErrVaultLocked
	}
	srv, err := m.store.GetBitwardenServer(serverID)
	if err != nil {
		return nil, err
	}
	st, err := m.ensureOpen(*srv, false)
	if err != nil {
		return nil, err
	}
	return st.vault.Browse(), nil
}

// DeleteCache removes a server's sealed cache and drops any open state.
func (m *Manager) DeleteCache(serverID string) {
	_ = os.Remove(m.cachePath(serverID))
	m.mu.Lock()
	if st, ok := m.open[serverID]; ok {
		st.vault.Forget()
		delete(m.open, serverID)
	}
	m.mu.Unlock()
}

// ensureOpen returns a decrypted vault, syncing per the freshness policy.
// force=true always re-syncs (manual sync).
func (m *Manager) ensureOpen(srv store.BitwardenServer, force bool) (*openState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, have := m.open[srv.ID]
	if have && !force && time.Since(cur.openedAt) < staleAfter {
		cur.fresh = FreshCached
		return cur, nil
	}

	raw, fresh, err := m.loadSync(srv)
	if err != nil {
		// Re-sync failed but we still hold a decrypted copy: keep serving it
		// marked stale rather than breaking an in-flight connect.
		if have {
			cur.fresh = FreshStale
			return cur, nil
		}
		return nil, err
	}

	master, ok, err := m.secrets.Get(srv.MasterRef)
	if err != nil {
		return nil, fmt.Errorf("bitwarden: read master from vault: %w", err)
	}
	if !ok {
		return nil, ErrVaultLocked
	}

	v, err := OpenVault(raw, master)
	if err != nil {
		return nil, err
	}
	if have {
		cur.vault.Forget()
	}
	st := &openState{vault: v, openedAt: time.Now(), fresh: fresh}
	m.open[srv.ID] = st
	return st, nil
}

// loadSync returns the sync JSON and its freshness. It syncs from the server
// (comparing a content hash to skip re-decrypt when unchanged), falling back to
// the sealed on-disk cache when the server is unreachable.
func (m *Manager) loadSync(srv store.BitwardenServer) ([]byte, Freshness, error) {
	creds, err := m.lookup(srv.APIKeyRef)
	if err != nil {
		if cached, cerr := m.readCache(srv.ID); cerr == nil {
			return cached, FreshStale, nil
		}
		return nil, "", fmt.Errorf("bitwarden: api key unavailable and no cache: %w", err)
	}

	raw, err := m.syncer.Sync(srv.ServerURL, creds)
	if err != nil {
		if cached, cerr := m.readCache(srv.ID); cerr == nil {
			return cached, FreshStale, nil
		}
		return nil, "", fmt.Errorf("bitwarden: sync %s and no cache: %w", srv.Name, err)
	}

	hash := hashHex(raw)
	if hash == srv.LastHash {
		// Unchanged: reuse the cache (already decrypted-cheaply upstream by the
		// staleness check; here we just touch the timestamp).
		if cached, cerr := m.readCache(srv.ID); cerr == nil {
			_ = m.store.TouchBitwardenSync(srv.ID, time.Now().Unix(), hash)
			return cached, FreshCached, nil
		}
	}
	if err := m.writeCache(srv.ID, raw); err != nil {
		return nil, "", err
	}
	_ = m.store.TouchBitwardenSync(srv.ID, time.Now().Unix(), hash)
	return raw, FreshFetched, nil
}

func (m *Manager) cachePath(serverID string) string {
	return filepath.Join(m.cacheDir, serverID+".json.sealed")
}

func (m *Manager) readCache(serverID string) ([]byte, error) {
	sealed, err := os.ReadFile(m.cachePath(serverID))
	if err != nil {
		return nil, err
	}
	return m.sealer.Open(sealed)
}

func (m *Manager) writeCache(serverID string, plaintext []byte) error {
	if err := os.MkdirAll(m.cacheDir, 0o700); err != nil {
		return fmt.Errorf("bitwarden: cache dir: %w", err)
	}
	sealed, err := m.sealer.Seal(plaintext)
	if err != nil {
		return fmt.Errorf("bitwarden: seal cache: %w", err)
	}
	return os.WriteFile(m.cachePath(serverID), sealed, 0o600)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
