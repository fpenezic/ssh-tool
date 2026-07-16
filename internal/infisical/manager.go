package infisical

// Manager owns the runtime lifecycle of every registered Infisical server: it
// logs in (caching the access token in memory), reads a single secret per
// resolve straight from the server, and seals the last-known value of each ref
// on disk so an in-flight connect survives the server being briefly unreachable.
// It is the single seam the app and resolver reach Infisical through - the
// per-request sibling of bitwarden.Manager.
//
// Unlike Bitwarden there is NO client-side crypto and NO full-vault sync:
// Infisical decrypts server-side and returns plaintext, so a resolve is one HTTP
// read. There is also no master password - the only secret is the machine-
// identity API key.
//
// Freshness policy:
//   - every resolve fetches the one secret fresh (fast: a single GET).
//   - if the fetch fails, the sealed per-ref cache is served marked FreshStale
//     so a connect that was working keeps working through a brief outage.
//   - the access token is cached in memory (TTL-aware) and re-fetched on expiry
//     or a 401; Forget() (called on vault lock) drops every token.
//
// No timer polling - reads hang off real events (connect, browse, test login).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/store"
)

// tokenRefreshMargin re-logs in this long before a cached token actually
// expires, so a resolve never races the expiry.
const tokenRefreshMargin = 60 * time.Second

// SecretReader returns a vault secret by account and whether the vault is
// unlocked. Injected so this package doesn't import creds; used to read the
// API-key credential's client secret.
type SecretReader interface {
	Get(account string) (string, bool, error)
	Unlocked() bool
}

// Sealer encrypts/decrypts the on-disk last-known-value cache with the app
// vault, so a stolen cache file is worthless without an unlock.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// CredentialLookup resolves a server's API-key credential ref to its client id
// and secret. Injected so the manager can log in without importing creds.
type CredentialLookup func(apiKeyRef string) (Credentials, error)

// Freshness describes where a resolved secret came from, for the UI.
type Freshness string

const (
	FreshFetched Freshness = "fetched" // read live from the server
	FreshStale   Freshness = "stale"   // sealed cache used because the server was unreachable
)

// ErrVaultLocked signals the caller to run the unlock gate before retrying.
var ErrVaultLocked = errors.New("infisical: vault is locked")

// errUnauthorized is returned by the client on a 401 so the manager re-logs in.
var errUnauthorized = errors.New("infisical: unauthorized")

type tokenState struct {
	token   string
	expires time.Time
}

// Manager is concurrency-safe.
type Manager struct {
	store   *store.DB
	secrets SecretReader
	sealer  Sealer
	lookup  CredentialLookup
	// newClient builds a Client for a server URL, optionally tunnelled.
	newClient func(serverURL string) *Client
	cacheDir  string

	mu     sync.Mutex
	tokens map[string]tokenState // serverID -> cached access token
}

// NewManager wires the manager with a direct-dial client factory. cacheDir holds
// the sealed last-known-value blobs.
func NewManager(db *store.DB, secrets SecretReader, sealer Sealer, lookup CredentialLookup, cacheDir string) *Manager {
	return NewManagerWithClient(db, secrets, sealer, lookup, func(url string) *Client {
		return NewClient(url)
	}, cacheDir)
}

// NewManagerWithClient is NewManager with a caller-supplied client factory, so
// the app can route traffic through a server's WireGuard profile (and tests can
// point at an httptest server).
func NewManagerWithClient(db *store.DB, secrets SecretReader, sealer Sealer, lookup CredentialLookup, newClient func(serverURL string) *Client, cacheDir string) *Manager {
	return &Manager{
		store:     db,
		secrets:   secrets,
		sealer:    sealer,
		lookup:    lookup,
		newClient: newClient,
		cacheDir:  cacheDir,
		tokens:    make(map[string]tokenState),
	}
}

// Forget drops every cached access token (called when the app vault locks).
func (m *Manager) Forget() {
	m.mu.Lock()
	m.tokens = make(map[string]tokenState)
	m.mu.Unlock()
}

// Resolve reads the secret for a credential reference straight from the server,
// falling back to the sealed last-known value if the server is unreachable.
func (m *Manager) Resolve(ref store.InfisicalRef) (secret string, fresh Freshness, err error) {
	if !m.secrets.Unlocked() {
		return "", "", ErrVaultLocked
	}
	srv, err := m.store.GetInfisicalServer(ref.ServerID)
	if err != nil {
		return "", "", fmt.Errorf("infisical: server %s not found: %w", ref.ServerID, err)
	}
	path := ref.SecretPath
	if path == "" {
		path = "/"
	}

	val, ferr := m.fetchSecret(*srv, ref.ProjectID, ref.Environment, path, ref.Key)
	if ferr != nil {
		if cached, cerr := m.readCache(ref); cerr == nil {
			return cached, FreshStale, nil
		}
		return "", "", ferr
	}
	// Best-effort seal for the next outage.
	_ = m.writeCache(ref, val)
	_ = m.store.TouchInfisicalUsed(srv.ID, time.Now().Unix())
	return val, FreshFetched, nil
}

// TestLogin verifies a server's API key logs in (used by the Settings button).
func (m *Manager) TestLogin(serverID string) error {
	if !m.secrets.Unlocked() {
		return ErrVaultLocked
	}
	srv, err := m.store.GetInfisicalServer(serverID)
	if err != nil {
		return err
	}
	// Force a fresh login (ignore any cached token).
	m.mu.Lock()
	delete(m.tokens, serverID)
	m.mu.Unlock()
	_, err = m.token(*srv)
	return err
}

// Browse returns the project/environment/secret tree for the picker. It lists
// each environment's secrets recursively so nested folders appear (subject to
// server-version quirks; see ListSecrets).
func (m *Manager) Browse(serverID string) ([]GroupInfo, error) {
	if !m.secrets.Unlocked() {
		return nil, ErrVaultLocked
	}
	srv, err := m.store.GetInfisicalServer(serverID)
	if err != nil {
		return nil, err
	}
	token, err := m.token(*srv)
	if err != nil {
		return nil, err
	}
	cl := m.newClient(srv.ServerURL)
	projects, err := m.listProjectsRetry(cl, *srv, token)
	if err != nil {
		return nil, err
	}
	out := make([]GroupInfo, 0, len(projects))
	for _, p := range projects {
		g := GroupInfo{ProjectID: p.ID, Name: p.Name}
		for _, env := range p.Environments {
			secrets, serr := m.listSecretsRetry(cl, *srv, token, p.ID, env.Slug, "/")
			if serr != nil {
				// A project the identity can list but not read secrets in: show
				// the environment empty rather than failing the whole browse.
				g.Environments = append(g.Environments, EnvInfo{Name: env.Name, Slug: env.Slug})
				continue
			}
			g.Environments = append(g.Environments, EnvInfo{
				Name:    env.Name,
				Slug:    env.Slug,
				Entries: buildEnvEntries("/", secrets),
			})
		}
		out = append(out, g)
	}
	return out, nil
}

// DeleteCache removes a server's sealed last-known-value blobs and drops its
// token.
func (m *Manager) DeleteCache(serverID string) {
	m.mu.Lock()
	delete(m.tokens, serverID)
	m.mu.Unlock()
	// Remove every cache file for this server (prefix serverID+"-").
	entries, err := os.ReadDir(m.cacheDir)
	if err != nil {
		return
	}
	prefix := serverID + "-"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(m.cacheDir, e.Name()))
		}
	}
}

// fetchSecret reads one secret, retrying once after a re-login on 401.
func (m *Manager) fetchSecret(srv store.InfisicalServer, projectID, env, path, key string) (string, error) {
	token, err := m.token(srv)
	if err != nil {
		return "", err
	}
	cl := m.newClient(srv.ServerURL)
	val, err := cl.ReadSecret(token, projectID, env, path, key)
	if errors.Is(err, errUnauthorized) {
		if token, err = m.relogin(srv); err != nil {
			return "", err
		}
		val, err = cl.ReadSecret(token, projectID, env, path, key)
	}
	return val, err
}

func (m *Manager) listProjectsRetry(cl *Client, srv store.InfisicalServer, token string) ([]project, error) {
	projects, err := cl.ListProjects(token)
	if errors.Is(err, errUnauthorized) {
		if token, err = m.relogin(srv); err != nil {
			return nil, err
		}
		projects, err = cl.ListProjects(token)
	}
	return projects, err
}

func (m *Manager) listSecretsRetry(cl *Client, srv store.InfisicalServer, token, projectID, env, path string) ([]rawSecret, error) {
	secrets, err := cl.ListSecrets(token, projectID, env, path, true)
	if errors.Is(err, errUnauthorized) {
		if token, err = m.relogin(srv); err != nil {
			return nil, err
		}
		secrets, err = cl.ListSecrets(token, projectID, env, path, true)
	}
	return secrets, err
}

// token returns a valid access token for a server, logging in (and caching) when
// none is held or the cached one is near expiry.
func (m *Manager) token(srv store.InfisicalServer) (string, error) {
	m.mu.Lock()
	if ts, ok := m.tokens[srv.ID]; ok && time.Until(ts.expires) > tokenRefreshMargin {
		tok := ts.token
		m.mu.Unlock()
		return tok, nil
	}
	m.mu.Unlock()
	return m.relogin(srv)
}

// relogin performs a fresh login and caches the token with its expiry.
func (m *Manager) relogin(srv store.InfisicalServer) (string, error) {
	creds, err := m.lookup(srv.APIKeyRef)
	if err != nil {
		return "", err
	}
	tok, expiresIn, err := m.newClient(srv.ServerURL).Login(creds)
	if err != nil {
		return "", err
	}
	ttl := time.Duration(expiresIn) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute // defensive default when the server omits expiresIn
	}
	m.mu.Lock()
	m.tokens[srv.ID] = tokenState{token: tok, expires: time.Now().Add(ttl)}
	m.mu.Unlock()
	return tok, nil
}

// ---- last-known-value cache (sealed) ----

func (m *Manager) cachePath(ref store.InfisicalRef) string {
	h := sha256.Sum256([]byte(ref.ProjectID + "\x00" + ref.Environment + "\x00" + normPath(ref.SecretPath) + "\x00" + ref.Key))
	return filepath.Join(m.cacheDir, ref.ServerID+"-"+hex.EncodeToString(h[:8])+".sealed")
}

func (m *Manager) readCache(ref store.InfisicalRef) (string, error) {
	sealed, err := os.ReadFile(m.cachePath(ref))
	if err != nil {
		return "", err
	}
	plain, err := m.sealer.Open(sealed)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (m *Manager) writeCache(ref store.InfisicalRef, value string) error {
	if err := os.MkdirAll(m.cacheDir, 0o700); err != nil {
		return err
	}
	sealed, err := m.sealer.Seal([]byte(value))
	if err != nil {
		return err
	}
	return os.WriteFile(m.cachePath(ref), sealed, 0o600)
}
