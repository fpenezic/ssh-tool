package main

// Infisical integration wiring: the manager, its vault-secret and cache-seal
// adapters, the API-key credential lookup, the resolver hook, and the IPC
// surface the frontend drives. The read-only reads live in internal/infisical;
// this file is the app-level glue (mirrors app_bitwarden.go, minus the master
// password - Infisical decrypts server-side, so there is no master).

import (
	"fmt"
	"path/filepath"
	"strings"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/infisical"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// infisicalSecrets adapts the vault to the manager's SecretReader (used only to
// read the API-key credential's client secret - there is no master).
type infisicalSecrets struct{ a *App }

func (s infisicalSecrets) Get(account string) (string, bool, error) {
	return s.a.vault.Get(account)
}
func (s infisicalSecrets) Unlocked() bool {
	return s.a.vault.Status().Kind == "unlocked"
}

// infisicalSealer seals the on-disk last-known-value cache with the app vault,
// so a stolen cache file is worthless without an unlock.
type infisicalSealer struct{ a *App }

func (s infisicalSealer) Seal(plaintext []byte) ([]byte, error) { return s.a.vault.Seal(plaintext) }
func (s infisicalSealer) Open(sealed []byte) ([]byte, error)    { return s.a.vault.Open(sealed) }

// infisicalClientFor builds an Infisical client for a server, routing through the
// server's WireGuard profile when one is set. Netbird / Tailscale profiles are
// sidecar-SOCKS only and are not applied to this HTTP path (a direct dial is
// used); the settings UI therefore only offers WireGuard profiles - same
// constraint as Bitwarden.
func (a *App) infisicalClientFor(srv store.InfisicalServer) *infisical.Client {
	if srv.NetworkProfileID == "" {
		return infisical.NewClient(srv.ServerURL)
	}
	d, err := a.wgDialerFor(srv.NetworkProfileID)
	if err != nil {
		// Fall back to a direct dial; the read will surface the real error if the
		// server is genuinely only reachable through the tunnel.
		return infisical.NewClient(srv.ServerURL)
	}
	return infisical.NewClientWithDialer(srv.ServerURL, infisical.DialContext(d))
}

// initInfisical builds the manager. Called from initialise() after db + vault
// are set. Idempotent-safe.
func (a *App) initInfisical() {
	if a.infisical != nil {
		return
	}
	cacheDir := filepath.Join(store.DataDir(), "infisical-cache")
	a.infisical = infisical.NewManagerWithClient(a.db, infisicalSecrets{a}, infisicalSealer{a}, a.infisicalAPIKeyLookup, a.infisicalClientFor, cacheDir)
	// Route SSH auth resolution through the manager for any credential carrying an
	// infisical_ref. handled=false means "not an Infisical credential".
	sshlayer.InfisicalResolveHook = func(cred *store.CredentialRef) (string, bool, error) {
		ref := store.ParseInfisicalRef(cred.Config)
		if ref == nil {
			return "", false, nil
		}
		secret, _, err := a.infisical.Resolve(*ref)
		if err != nil {
			return "", true, err
		}
		return secret, true, nil
	}
}

// infisicalAPIKeyLookup resolves a server's API-key credential ref to its
// client_id / client_secret. The API key is a normal api_token credential:
// token_id in config holds the client_id, the vault secret holds the
// client_secret. apiKeyRef is the credential id. Identical to
// bitwardenAPIKeyLookup.
func (a *App) infisicalAPIKeyLookup(apiKeyRef string) (infisical.Credentials, error) {
	if apiKeyRef == "" {
		return infisical.Credentials{}, fmt.Errorf("infisical: no API key configured for this server")
	}
	cred, err := a.db.GetCredential(apiKeyRef)
	if err != nil {
		return infisical.Credentials{}, fmt.Errorf("infisical: API key credential not found: %w", err)
	}
	clientID, _ := cred.Config["token_id"].(string)
	if clientID == "" {
		return infisical.Credentials{}, fmt.Errorf("infisical: API key credential %s has no client id", cred.Name)
	}
	if cred.VaultKey == nil {
		return infisical.Credentials{}, fmt.Errorf("infisical: API key credential %s has no secret", cred.Name)
	}
	secret, ok, err := a.vault.Get(*cred.VaultKey)
	if err != nil {
		return infisical.Credentials{}, err
	}
	if !ok {
		return infisical.Credentials{}, infisical.ErrVaultLocked
	}
	return infisical.Credentials{ClientID: clientID, ClientSecret: secret}, nil
}

// forgetInfisical drops cached access tokens. Called from VaultLock so Infisical
// tokens never outlive the vault.
func (a *App) forgetInfisical() {
	if a.infisical != nil {
		a.infisical.Forget()
	}
}

// ---------- IPC ----------

// InfisicalList returns the registered servers (no secrets).
func (a *App) InfisicalList() ([]store.InfisicalServer, error) {
	return a.db.ListInfisicalServers()
}

// InfisicalSaveInput is the create/update payload from the settings UI. There is
// no master password - the only secret is the API key, referenced by id.
type InfisicalSaveInput struct {
	ID               string `json:"id"` // empty => create
	Name             string `json:"name"`
	ServerURL        string `json:"server_url"`
	APIKeyCredID     string `json:"api_key_cred_id"`    // credential id (api_token) holding client_id/secret
	NetworkProfileID string `json:"network_profile_id"` // WireGuard profile to dial through, "" = direct
}

// InfisicalSave creates or updates a server registration. Requires the vault
// unlocked (the API-key lookup needs it).
func (a *App) InfisicalSave(in InfisicalSaveInput) (*store.InfisicalServer, error) {
	if a.vault.Status().Kind != "unlocked" {
		return nil, fmt.Errorf("unlock the vault before saving an Infisical server")
	}
	if in.ID == "" {
		return a.infisicalCreate(in)
	}
	return a.infisicalUpdate(in)
}

func (a *App) infisicalCreate(in InfisicalSaveInput) (*store.InfisicalServer, error) {
	srv := store.InfisicalServer{
		Name:             in.Name,
		ServerURL:        strings.TrimRight(strings.TrimSpace(in.ServerURL), "/"),
		APIKeyRef:        in.APIKeyCredID,
		NetworkProfileID: in.NetworkProfileID,
	}
	created, err := a.db.CreateInfisicalServer(srv)
	if err != nil {
		return nil, err
	}
	a.recordAudit("infisical.add", created.ID, map[string]string{"name": created.Name, "url": created.ServerURL})
	EventsEmit("infisical_servers_changed", nil)
	return created, nil
}

func (a *App) infisicalUpdate(in InfisicalSaveInput) (*store.InfisicalServer, error) {
	existing, err := a.db.GetInfisicalServer(in.ID)
	if err != nil {
		return nil, err
	}
	existing.Name = in.Name
	existing.ServerURL = strings.TrimRight(strings.TrimSpace(in.ServerURL), "/")
	existing.APIKeyRef = in.APIKeyCredID
	existing.NetworkProfileID = in.NetworkProfileID
	updated, err := a.db.UpdateInfisicalServer(*existing)
	if err != nil {
		return nil, err
	}
	// Drop any cached token so the next read re-logs in with new settings.
	a.infisical.DeleteCache(in.ID)
	a.recordAudit("infisical.update", updated.ID, map[string]string{"name": updated.Name})
	EventsEmit("infisical_servers_changed", nil)
	return updated, nil
}

// InfisicalDelete removes a server registration and its cache. Refuses when
// credentials still reference it.
func (a *App) InfisicalDelete(id string) error {
	users, err := a.db.InfisicalUsage(id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("still referenced by %d credential(s): %s", len(users), strings.Join(users, ", "))
	}
	srv, err := a.db.GetInfisicalServer(id)
	if err != nil {
		return err
	}
	if a.infisical != nil {
		a.infisical.DeleteCache(id)
	}
	if err := a.db.DeleteInfisicalServer(id); err != nil {
		return err
	}
	a.recordAudit("infisical.delete", id, map[string]string{"name": srv.Name})
	EventsEmit("infisical_servers_changed", nil)
	return nil
}

// InfisicalTestLogin verifies a server's API key logs in (the Settings button).
func (a *App) InfisicalTestLogin(id string) error {
	if a.infisical == nil {
		return fmt.Errorf("infisical not initialised")
	}
	if err := a.infisical.TestLogin(id); err != nil {
		return err
	}
	a.recordAudit("infisical.test_login", id, nil)
	return nil
}

// InfisicalBrowse returns the project/environment/secret tree for the picker.
func (a *App) InfisicalBrowse(id string) ([]infisical.GroupInfo, error) {
	if a.infisical == nil {
		return nil, fmt.Errorf("infisical not initialised")
	}
	return a.infisical.Browse(id)
}

// InfisicalEnsureCredentialInput picks an Infisical secret straight from the
// connection auth picker.
type InfisicalEnsureCredentialInput struct {
	ServerID    string  `json:"server_id"`
	ProjectID   string  `json:"project_id"`
	Environment string  `json:"environment"`
	SecretPath  string  `json:"secret_path"`
	Key         string  `json:"key"`
	IsKey       bool    `json:"is_key"`
	Name        string  `json:"name"`      // suggested name; deduped if taken
	Username    string  `json:"username"`  // -> credential default_username
	FolderID    *string `json:"folder_id"` // credential folder to file it under
}

// InfisicalEnsureCredential returns a credential referencing the given Infisical
// secret, creating one if none exists yet.
func (a *App) InfisicalEnsureCredential(in InfisicalEnsureCredentialInput) (*store.CredentialRef, error) {
	if in.ServerID == "" || in.ProjectID == "" || in.Environment == "" || in.Key == "" {
		return nil, fmt.Errorf("infisical reference is incomplete")
	}
	path := in.SecretPath
	if path == "" {
		path = "/"
	}
	existing, err := a.db.ListCredentials()
	if err != nil {
		return nil, err
	}
	for i := range existing {
		ref := store.ParseInfisicalRef(existing[i].Config)
		if ref != nil && ref.ServerID == in.ServerID && ref.ProjectID == in.ProjectID &&
			ref.Environment == in.Environment && ref.SecretPath == path && ref.Key == in.Key {
			return &existing[i], nil
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = in.Key
	}
	if name == "" {
		name = "Infisical secret"
	}
	name = a.uniqueCredentialName(name, existing)

	// Auto-created Infisical credentials collect under a single "Infisical"
	// credential folder. The caller's FolderID, when set, wins; otherwise ensure
	// the default folder. A connection's folder_id must NEVER be passed here - it
	// lives in a different tree (see onInfisicalPick).
	folderID := in.FolderID
	if folderID == nil {
		if fid, err := a.ensureInfisicalCredFolder(); err == nil {
			folderID = fid
		}
	}

	var defUser *string
	if u := strings.TrimSpace(in.Username); u != "" {
		defUser = &u
	}
	res, err := a.credSvc.Create(creds.CreateInput{
		Kind:                 "infisical",
		Name:                 name,
		FolderID:             folderID,
		DefaultUsername:      defUser,
		InfisicalServerID:    in.ServerID,
		InfisicalProjectID:   in.ProjectID,
		InfisicalEnvironment: in.Environment,
		InfisicalSecretPath:  path,
		InfisicalKey:         in.Key,
		InfisicalIsKey:       in.IsKey,
	})
	if err != nil {
		return nil, err
	}
	a.recordAudit("infisical.credential.create", res.Credential.ID, map[string]string{"name": name})
	return res.Credential, nil
}

// infisicalCredFolderName is the credential folder auto-created Infisical
// references collect under.
const infisicalCredFolderName = "Infisical"

// ensureInfisicalCredFolder returns the id of the "Infisical" credential folder
// at the root, creating it on first use. Best-effort.
func (a *App) ensureInfisicalCredFolder() (*string, error) {
	folders, err := a.db.ListCredentialFolders()
	if err != nil {
		return nil, err
	}
	for i := range folders {
		if folders[i].ParentID == nil && folders[i].Name == infisicalCredFolderName {
			id := folders[i].ID
			return &id, nil
		}
	}
	f, err := a.db.CreateCredentialFolder(infisicalCredFolderName, nil)
	if err != nil {
		return nil, err
	}
	return &f.ID, nil
}
