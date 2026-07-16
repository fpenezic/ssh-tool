package main

// Vaultwarden / Bitwarden integration wiring: the manager, its vault-secret and
// cache-seal adapters, the API-key credential lookup, the resolver hook, and the
// IPC surface the frontend drives. The read-only decryption lives in
// internal/bitwarden; this file is the app-level glue (mirrors app_keepass.go).

import (
	"fmt"
	"path/filepath"
	"strings"

	"ssh-tool/internal/bitwarden"
	"ssh-tool/internal/creds"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// bitwardenSecrets adapts the vault to the manager's SecretReader.
type bitwardenSecrets struct{ a *App }

func (s bitwardenSecrets) Get(account string) (string, bool, error) {
	return s.a.vault.Get(account)
}
func (s bitwardenSecrets) Unlocked() bool {
	return s.a.vault.Status().Kind == "unlocked"
}

// bitwardenSealer seals the on-disk sync cache with the app vault, so a stolen
// cache file is worthless without an unlock.
type bitwardenSealer struct{ a *App }

func (s bitwardenSealer) Seal(plaintext []byte) ([]byte, error) { return s.a.vault.Seal(plaintext) }
func (s bitwardenSealer) Open(sealed []byte) ([]byte, error)    { return s.a.vault.Open(sealed) }

// initBitwarden builds the manager. Called from initialise() after db + vault
// are set. Idempotent-safe.
func (a *App) initBitwarden() {
	if a.bitwarden != nil {
		return
	}
	cacheDir := filepath.Join(store.DataDir(), "bitwarden-cache")
	a.bitwarden = bitwarden.NewManager(a.db, bitwardenSecrets{a}, bitwardenSealer{a}, a.bitwardenAPIKeyLookup, cacheDir)
	// Route SSH auth resolution through the manager for any credential carrying a
	// bitwarden_ref. handled=false means "not a Bitwarden credential".
	sshlayer.BitwardenResolveHook = func(cred *store.CredentialRef) (string, bool, error) {
		ref := store.ParseBitwardenRef(cred.Config)
		if ref == nil {
			return "", false, nil
		}
		secret, _, err := a.bitwarden.Resolve(*ref)
		if err != nil {
			return "", true, err
		}
		return secret, true, nil
	}
}

// bitwardenAPIKeyLookup resolves a server's API-key credential ref to its
// client_id / client_secret. The API key is a normal api_token credential:
// token_id in config holds the client_id, the vault secret holds the
// client_secret. apiKeyRef is the credential id.
func (a *App) bitwardenAPIKeyLookup(apiKeyRef string) (bitwarden.Credentials, error) {
	if apiKeyRef == "" {
		return bitwarden.Credentials{}, fmt.Errorf("bitwarden: no API key configured for this server")
	}
	cred, err := a.db.GetCredential(apiKeyRef)
	if err != nil {
		return bitwarden.Credentials{}, fmt.Errorf("bitwarden: API key credential not found: %w", err)
	}
	clientID, _ := cred.Config["token_id"].(string)
	if clientID == "" {
		return bitwarden.Credentials{}, fmt.Errorf("bitwarden: API key credential %s has no client id", cred.Name)
	}
	if cred.VaultKey == nil {
		return bitwarden.Credentials{}, fmt.Errorf("bitwarden: API key credential %s has no secret", cred.Name)
	}
	secret, ok, err := a.vault.Get(*cred.VaultKey)
	if err != nil {
		return bitwarden.Credentials{}, err
	}
	if !ok {
		return bitwarden.Credentials{}, bitwarden.ErrVaultLocked
	}
	return bitwarden.Credentials{ClientID: clientID, ClientSecret: secret}, nil
}

// forgetBitwarden drops decrypted vaults. Called from VaultLock so Bitwarden
// secrets never outlive the vault.
func (a *App) forgetBitwarden() {
	if a.bitwarden != nil {
		a.bitwarden.Forget()
	}
}

// ---------- IPC ----------

// BitwardenList returns the registered servers (no secrets).
func (a *App) BitwardenList() ([]store.BitwardenServer, error) {
	return a.db.ListBitwardenServers()
}

// BitwardenSaveInput is the create/update payload from the settings UI. The
// master password is passed inline here and sealed into the vault by this method
// - it is never stored in the row and never returned. The API key is a normal
// credential referenced by id (APIKeyCredID).
type BitwardenSaveInput struct {
	ID           string `json:"id"` // empty => create
	Name         string `json:"name"`
	ServerURL    string `json:"server_url"`
	APIKeyCredID string `json:"api_key_cred_id"` // credential id (api_token) holding client_id/secret
	Master       string `json:"master"`          // "" leaves an existing master unchanged on update
	SetMaster    bool   `json:"set_master"`      // true when Master should be written (incl. clearing)
}

// BitwardenSave creates or updates a server registration, sealing the master
// password into the vault. Requires the vault unlocked.
func (a *App) BitwardenSave(in BitwardenSaveInput) (*store.BitwardenServer, error) {
	if a.vault.Status().Kind != "unlocked" {
		return nil, fmt.Errorf("unlock the vault before saving a Bitwarden server")
	}
	if in.ID == "" {
		return a.bitwardenCreate(in)
	}
	return a.bitwardenUpdate(in)
}

func (a *App) bitwardenCreate(in BitwardenSaveInput) (*store.BitwardenServer, error) {
	srv := store.BitwardenServer{
		Name:      in.Name,
		ServerURL: strings.TrimRight(strings.TrimSpace(in.ServerURL), "/"),
		APIKeyRef: in.APIKeyCredID,
	}
	created, err := a.db.CreateBitwardenServer(srv)
	if err != nil {
		return nil, err
	}
	if in.Master != "" {
		acct := a.bitwardenMasterAccount(created.ID)
		if err := a.vault.Put(acct, in.Master); err != nil {
			_ = a.db.DeleteBitwardenServer(created.ID)
			return nil, err
		}
		created.MasterRef = acct
	}
	updated, err := a.db.UpdateBitwardenServer(*created)
	if err != nil {
		return nil, err
	}
	a.recordAudit("bitwarden.add", updated.ID, map[string]string{"name": updated.Name, "url": updated.ServerURL})
	EventsEmit("bitwarden_servers_changed", nil)
	return updated, nil
}

func (a *App) bitwardenUpdate(in BitwardenSaveInput) (*store.BitwardenServer, error) {
	existing, err := a.db.GetBitwardenServer(in.ID)
	if err != nil {
		return nil, err
	}
	existing.Name = in.Name
	existing.ServerURL = strings.TrimRight(strings.TrimSpace(in.ServerURL), "/")
	existing.APIKeyRef = in.APIKeyCredID
	if in.SetMaster {
		acct := a.bitwardenMasterAccount(in.ID)
		if in.Master == "" {
			_ = a.vault.Delete(acct)
			existing.MasterRef = ""
		} else {
			if err := a.vault.Put(acct, in.Master); err != nil {
				return nil, err
			}
			existing.MasterRef = acct
		}
	}
	updated, err := a.db.UpdateBitwardenServer(*existing)
	if err != nil {
		return nil, err
	}
	// Drop any cached open so the next resolve re-reads with new settings.
	a.bitwarden.DeleteCache(in.ID)
	a.recordAudit("bitwarden.update", updated.ID, map[string]string{"name": updated.Name})
	EventsEmit("bitwarden_servers_changed", nil)
	return updated, nil
}

// BitwardenDelete removes a server registration, its master secret, and its
// cache. Refuses when credentials still reference it.
func (a *App) BitwardenDelete(id string) error {
	users, err := a.db.BitwardenUsage(id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("still referenced by %d credential(s): %s", len(users), strings.Join(users, ", "))
	}
	srv, err := a.db.GetBitwardenServer(id)
	if err != nil {
		return err
	}
	if srv.MasterRef != "" {
		_ = a.vault.Delete(srv.MasterRef)
	}
	if a.bitwarden != nil {
		a.bitwarden.DeleteCache(id)
	}
	if err := a.db.DeleteBitwardenServer(id); err != nil {
		return err
	}
	a.recordAudit("bitwarden.delete", id, map[string]string{"name": srv.Name})
	EventsEmit("bitwarden_servers_changed", nil)
	return nil
}

// BitwardenSync forces a re-login + re-sync and returns the resulting freshness.
func (a *App) BitwardenSync(id string) (string, error) {
	if a.bitwarden == nil {
		return "", fmt.Errorf("bitwarden not initialised")
	}
	fresh, err := a.bitwarden.Sync(id)
	if err != nil {
		return "", err
	}
	a.recordAudit("bitwarden.sync", id, map[string]string{"freshness": string(fresh)})
	return string(fresh), nil
}

// BitwardenBrowse returns the org/collection/item tree for the credential picker.
func (a *App) BitwardenBrowse(id string) ([]bitwarden.GroupInfo, error) {
	if a.bitwarden == nil {
		return nil, fmt.Errorf("bitwarden not initialised")
	}
	return a.bitwarden.Browse(id)
}

// BitwardenEnsureCredentialInput picks a Bitwarden item+field straight from the
// connection auth picker.
type BitwardenEnsureCredentialInput struct {
	ServerID string  `json:"server_id"`
	CipherID string  `json:"cipher_id"`
	Field    string  `json:"field"`
	IsKey    bool    `json:"is_key"`
	Name     string  `json:"name"`      // suggested name (item title); deduped if taken
	Username string  `json:"username"`  // item username -> credential default_username
	FolderID *string `json:"folder_id"` // credential folder to file it under
}

// BitwardenEnsureCredential returns a credential referencing the given Bitwarden
// item+field, creating one if none exists yet.
func (a *App) BitwardenEnsureCredential(in BitwardenEnsureCredentialInput) (*store.CredentialRef, error) {
	if in.ServerID == "" || in.CipherID == "" {
		return nil, fmt.Errorf("bitwarden reference is incomplete")
	}
	field := in.Field
	if field == "" {
		field = "password"
	}
	existing, err := a.db.ListCredentials()
	if err != nil {
		return nil, err
	}
	for i := range existing {
		ref := store.ParseBitwardenRef(existing[i].Config)
		if ref != nil && ref.ServerID == in.ServerID && ref.CipherID == in.CipherID && ref.Field == field {
			return &existing[i], nil
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Bitwarden item"
	}
	name = a.uniqueCredentialName(name, existing)

	// Auto-created Bitwarden credentials collect under a single "Bitwarden"
	// credential folder. The caller's FolderID, when set, wins; otherwise ensure
	// the default folder. A connection's folder_id must NEVER be passed here - it
	// lives in a different tree (see onBitwardenPick).
	folderID := in.FolderID
	if folderID == nil {
		if fid, err := a.ensureBitwardenCredFolder(); err == nil {
			folderID = fid
		}
	}

	var defUser *string
	if u := strings.TrimSpace(in.Username); u != "" {
		defUser = &u
	}
	res, err := a.credSvc.Create(creds.CreateInput{
		Kind:              "bitwarden",
		Name:              name,
		FolderID:          folderID,
		DefaultUsername:   defUser,
		BitwardenServerID: in.ServerID,
		BitwardenCipherID: in.CipherID,
		BitwardenField:    field,
		BitwardenIsKey:    in.IsKey,
	})
	if err != nil {
		return nil, err
	}
	a.recordAudit("bitwarden.credential.create", res.Credential.ID, map[string]string{"name": name})
	return res.Credential, nil
}

// bitwardenCredFolderName is the credential folder auto-created Bitwarden
// references collect under.
const bitwardenCredFolderName = "Bitwarden"

// ensureBitwardenCredFolder returns the id of the "Bitwarden" credential folder
// at the root, creating it on first use. Best-effort.
func (a *App) ensureBitwardenCredFolder() (*string, error) {
	folders, err := a.db.ListCredentialFolders()
	if err != nil {
		return nil, err
	}
	for i := range folders {
		if folders[i].ParentID == nil && folders[i].Name == bitwardenCredFolderName {
			id := folders[i].ID
			return &id, nil
		}
	}
	f, err := a.db.CreateCredentialFolder(bitwardenCredFolderName, nil)
	if err != nil {
		return nil, err
	}
	return &f.ID, nil
}

// ---------- vault account naming ----------

func (a *App) bitwardenMasterAccount(id string) string { return "bitwarden:" + id + ":master" }
