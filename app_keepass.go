package main

// KeePass integration wiring: the manager, its vault/secret and remote-fetch
// adapters, the resolver hook, and the IPC surface the frontend drives. The
// read-only .kdbx parsing lives in internal/keepass; this file is the app-level
// glue (mirrors app_share.go / the network-profile wiring).

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/keepass"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/syncer"
)

// keepassSecrets adapts the vault to the manager's SecretReader.
type keepassSecrets struct{ a *App }

func (s keepassSecrets) Get(account string) (string, bool, error) {
	return s.a.vault.Get(account)
}
func (s keepassSecrets) Unlocked() bool {
	return s.a.vault.Status().Kind == "unlocked"
}

// keepassFetcher pulls remote .kdbx files. WebDAV uses a conditional GET
// (If-None-Match) so an unchanged file isn't re-downloaded; SFTP has no cheap
// validator, so it always transfers (the file is small and remote SFTP fetches
// are rare).
type keepassFetcher struct{ a *App }

func (f keepassFetcher) Fetch(kdb store.KeepassDatabase, etagIn string) (data []byte, etagOut string, notModified bool, err error) {
	switch kdb.Source {
	case store.KeepassWebDAV:
		return f.fetchWebDAV(kdb, etagIn)
	case store.KeepassSFTP:
		return f.fetchSFTP(kdb)
	default:
		return nil, "", false, fmt.Errorf("keepass: source %q is not remote", kdb.Source)
	}
}

func (f keepassFetcher) fetchWebDAV(kdb store.KeepassDatabase, etagIn string) ([]byte, string, bool, error) {
	pass := ""
	if ref := kdb.RemoteConfig["password_ref"]; ref != "" {
		if pw, ok, _ := f.a.vault.Get(ref); ok {
			pass = pw
		}
	}
	req, err := http.NewRequest("GET", kdb.URL, nil)
	if err != nil {
		return nil, "", false, err
	}
	if user := kdb.RemoteConfig["username"]; user != "" || pass != "" {
		req.SetBasicAuth(kdb.RemoteConfig["username"], pass)
	}
	if etagIn != "" {
		req.Header.Set("If-None-Match", etagIn)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etagIn, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", false, fmt.Errorf("keepass webdav GET: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", false, err
	}
	return body, resp.Header.Get("ETag"), false, nil
}

func (f keepassFetcher) fetchSFTP(kdb store.KeepassDatabase) ([]byte, string, bool, error) {
	pass, keyPEM, keyPass := "", "", ""
	if ref := kdb.RemoteConfig["password_ref"]; ref != "" {
		pass, _, _ = f.a.vault.Get(ref)
	}
	if ref := kdb.RemoteConfig["key_ref"]; ref != "" {
		keyPEM, _, _ = f.a.vault.Get(ref)
	}
	if ref := kdb.RemoteConfig["key_pass_ref"]; ref != "" {
		keyPass, _, _ = f.a.vault.Get(ref)
	}
	methods, err := sshlayer.InlineAuthMethods(pass, keyPEM, keyPass)
	if err != nil {
		return nil, "", false, fmt.Errorf("keepass sftp auth: %w", err)
	}
	port := 22
	if p := kdb.RemoteConfig["port"]; p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	host := kdb.RemoteConfig["host"]
	remoteDir := path.Dir(kdb.URL)
	fileName := path.Base(kdb.URL)
	var algos []string
	if lk := f.a.makeAlgoLookup(); lk != nil {
		algos = lk(host, port)
	}
	tr := &syncer.SFTP{
		Host:              host,
		Port:              port,
		User:              kdb.RemoteConfig["user"],
		Dir:               remoteDir,
		Auth:              methods,
		HostKey:           f.a.makeHostKeyCallback(),
		HostKeyAlgorithms: algos,
		Timeout:           f.a.connectTimeout(),
	}
	defer tr.Close()
	body, err := tr.Get(fileName)
	if err != nil {
		return nil, "", false, err
	}
	return body, "", false, nil
}

// initKeepass builds the manager. Called from initialise() after db + vault are
// set. Idempotent-safe (only builds once).
func (a *App) initKeepass() {
	if a.keepass != nil {
		return
	}
	cacheDir := filepath.Join(store.DataDir(), "keepass-cache")
	a.keepass = keepass.NewManager(a.db, keepassSecrets{a}, keepassFetcher{a}, cacheDir)
	// Route SSH auth resolution through the manager for any credential carrying
	// a keepass_ref. handled=false means "not a KeePass credential".
	sshlayer.KeepassResolveHook = func(cred *store.CredentialRef) (string, bool, error) {
		ref := store.ParseKeepassRef(cred.Config)
		if ref == nil {
			return "", false, nil
		}
		secret, _, err := a.keepass.Resolve(*ref)
		if err != nil {
			return "", true, err
		}
		return secret, true, nil
	}
}

// forgetKeepass drops decrypted databases. Called from VaultLock so KeePass
// secrets never outlive the vault.
func (a *App) forgetKeepass() {
	if a.keepass != nil {
		a.keepass.Forget()
	}
}

// ---------- IPC ----------

// KeepassList returns the registered databases (no secrets).
func (a *App) KeepassList() ([]store.KeepassDatabase, error) {
	return a.db.ListKeepassDatabases()
}

// KeepassSaveInput is the create/update payload from the settings UI. Secrets
// (master, key file, remote password) are passed inline here and sealed into
// the vault by this method - they are never stored in the row.
type KeepassSaveInput struct {
	ID           string            `json:"id"` // empty => create
	Name         string            `json:"name"`
	Source       string            `json:"source"`
	Path         string            `json:"path"`
	URL          string            `json:"url"`
	Master       string            `json:"master"`        // "" leaves an existing master unchanged on update
	SetMaster    bool              `json:"set_master"`    // true when Master should be written (incl. clearing)
	KeyFile      string            `json:"key_file"`      // raw key-file bytes as string
	SetKeyFile   bool              `json:"set_key_file"`  // true when KeyFile should be written
	RemoteConfig map[string]string `json:"remote_config"` // host/user/port/username; NOT secrets
	RemotePass   string            `json:"remote_pass"`   // remote transport password, sealed to vault
	SetRemote    bool              `json:"set_remote"`    // true when RemotePass should be written
}

// KeepassSave creates or updates a database registration, sealing any provided
// secrets into the vault. Requires the vault to be unlocked (it must Put).
func (a *App) KeepassSave(in KeepassSaveInput) (*store.KeepassDatabase, error) {
	if a.vault.Status().Kind != "unlocked" {
		return nil, fmt.Errorf("unlock the vault before saving a KeePass database")
	}
	if in.ID == "" {
		return a.keepassCreate(in)
	}
	return a.keepassUpdate(in)
}

func (a *App) keepassCreate(in KeepassSaveInput) (*store.KeepassDatabase, error) {
	kdb := store.KeepassDatabase{
		Name:         in.Name,
		Source:       store.KeepassSource(in.Source),
		Path:         in.Path,
		URL:          in.URL,
		RemoteConfig: in.RemoteConfig,
	}
	// Reserve vault accounts keyed off a fresh id by creating the row first,
	// then sealing secrets under keepass:<id>:<kind> and updating the refs.
	created, err := a.db.CreateKeepassDatabase(kdb)
	if err != nil {
		return nil, err
	}
	created.MasterRef, created.KeyfileRef = a.keepassSecretAccounts(created.ID)
	if in.Master != "" {
		if err := a.vault.Put(created.MasterRef, in.Master); err != nil {
			_ = a.db.DeleteKeepassDatabase(created.ID)
			return nil, err
		}
	} else {
		created.MasterRef = ""
	}
	if in.KeyFile != "" {
		kfAcct := a.keepassKeyfileAccount(created.ID)
		if err := a.vault.Put(kfAcct, in.KeyFile); err != nil {
			_ = a.db.DeleteKeepassDatabase(created.ID)
			return nil, err
		}
		created.KeyfileRef = kfAcct
	} else {
		created.KeyfileRef = ""
	}
	if in.RemotePass != "" {
		acct := a.keepassRemotePassAccount(created.ID)
		if err := a.vault.Put(acct, in.RemotePass); err != nil {
			_ = a.db.DeleteKeepassDatabase(created.ID)
			return nil, err
		}
		if created.RemoteConfig == nil {
			created.RemoteConfig = map[string]string{}
		}
		created.RemoteConfig["password_ref"] = acct
	}
	updated, err := a.db.UpdateKeepassDatabase(*created)
	if err != nil {
		return nil, err
	}
	a.recordAudit("keepass.add", updated.ID, map[string]string{"name": updated.Name, "source": string(updated.Source)})
	EventsEmit("keepass_dbs_changed", nil)
	return updated, nil
}

func (a *App) keepassUpdate(in KeepassSaveInput) (*store.KeepassDatabase, error) {
	existing, err := a.db.GetKeepassDatabase(in.ID)
	if err != nil {
		return nil, err
	}
	existing.Name = in.Name
	existing.Source = store.KeepassSource(in.Source)
	existing.Path = in.Path
	existing.URL = in.URL
	if in.RemoteConfig != nil {
		// Preserve the sealed password_ref pointer across an edit.
		if pref := existing.RemoteConfig["password_ref"]; pref != "" {
			in.RemoteConfig["password_ref"] = pref
		}
		existing.RemoteConfig = in.RemoteConfig
	}
	if in.SetMaster {
		acct := a.keepassSecretAccountMaster(in.ID)
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
	if in.SetKeyFile {
		acct := a.keepassKeyfileAccount(in.ID)
		if in.KeyFile == "" {
			_ = a.vault.Delete(acct)
			existing.KeyfileRef = ""
		} else {
			if err := a.vault.Put(acct, in.KeyFile); err != nil {
				return nil, err
			}
			existing.KeyfileRef = acct
		}
	}
	if in.SetRemote {
		acct := a.keepassRemotePassAccount(in.ID)
		if in.RemotePass == "" {
			_ = a.vault.Delete(acct)
			delete(existing.RemoteConfig, "password_ref")
		} else {
			if err := a.vault.Put(acct, in.RemotePass); err != nil {
				return nil, err
			}
			if existing.RemoteConfig == nil {
				existing.RemoteConfig = map[string]string{}
			}
			existing.RemoteConfig["password_ref"] = acct
		}
	}
	updated, err := a.db.UpdateKeepassDatabase(*existing)
	if err != nil {
		return nil, err
	}
	// Drop any cached open so the next resolve re-reads with new settings.
	a.keepass.DeleteCache(in.ID)
	a.recordAudit("keepass.update", updated.ID, map[string]string{"name": updated.Name})
	EventsEmit("keepass_dbs_changed", nil)
	return updated, nil
}

// KeepassDelete removes a database registration, its vault secrets, and its
// cache. Refuses when credentials still reference it.
func (a *App) KeepassDelete(id string) error {
	users, err := a.db.KeepassUsage(id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("still referenced by %d credential(s): %s", len(users), strings.Join(users, ", "))
	}
	kdb, err := a.db.GetKeepassDatabase(id)
	if err != nil {
		return err
	}
	if kdb.MasterRef != "" {
		_ = a.vault.Delete(kdb.MasterRef)
	}
	if kdb.KeyfileRef != "" {
		_ = a.vault.Delete(kdb.KeyfileRef)
	}
	if pref := kdb.RemoteConfig["password_ref"]; pref != "" {
		_ = a.vault.Delete(pref)
	}
	if a.keepass != nil {
		a.keepass.DeleteCache(id)
	}
	if err := a.db.DeleteKeepassDatabase(id); err != nil {
		return err
	}
	a.recordAudit("keepass.delete", id, map[string]string{"name": kdb.Name})
	EventsEmit("keepass_dbs_changed", nil)
	return nil
}

// KeepassRefresh forces a re-fetch and returns the resulting freshness.
func (a *App) KeepassRefresh(id string) (string, error) {
	if a.keepass == nil {
		return "", fmt.Errorf("keepass not initialised")
	}
	fresh, err := a.keepass.Refresh(id)
	if err != nil {
		return "", err
	}
	a.recordAudit("keepass.refresh", id, map[string]string{"freshness": string(fresh)})
	return string(fresh), nil
}

// KeepassPickFile opens a native Open File dialog for choosing a local .kdbx,
// so the settings form doesn't make the user type a Windows path by hand.
// Empty result = cancelled. Desktop only (OpenFileDialog is a no-op on mobile).
func (a *App) KeepassPickFile() (string, error) {
	return OpenFileDialog(OpenFileDialogOptions{
		Title: "Choose a KeePass .kdbx database",
	})
}

// KeepassBrowse returns the entry tree for the credential picker.
func (a *App) KeepassBrowse(id string) ([]keepass.GroupInfo, error) {
	if a.keepass == nil {
		return nil, fmt.Errorf("keepass not initialised")
	}
	return a.keepass.Browse(id)
}

// KeepassEnsureCredentialInput picks a KeePass entry+field straight from the
// connection auth picker. The app finds an existing credential that already
// references the exact same entry+field (so choosing it twice reuses one) or
// creates a fresh one, and returns it ready to assign as auth_ref.
type KeepassEnsureCredentialInput struct {
	DBID      string  `json:"db_id"`
	EntryUUID string  `json:"entry_uuid"`
	Field     string  `json:"field"`
	IsKey     bool    `json:"is_key"`
	Name      string  `json:"name"`      // suggested name (entry title); deduped if taken
	Username  string  `json:"username"`  // entry username -> credential default_username
	FolderID  *string `json:"folder_id"` // credential folder to file it under
}

// KeepassEnsureCredential returns a credential referencing the given KeePass
// entry+field, creating one if none exists yet.
func (a *App) KeepassEnsureCredential(in KeepassEnsureCredentialInput) (*store.CredentialRef, error) {
	if in.DBID == "" || in.EntryUUID == "" {
		return nil, fmt.Errorf("keepass reference is incomplete")
	}
	field := in.Field
	if field == "" {
		field = "password"
	}
	// Dedup: reuse an existing credential pointing at the same entry+field.
	existing, err := a.db.ListCredentials()
	if err != nil {
		return nil, err
	}
	for i := range existing {
		ref := store.ParseKeepassRef(existing[i].Config)
		if ref != nil && ref.DBID == in.DBID && ref.EntryUUID == in.EntryUUID && ref.Field == field {
			return &existing[i], nil
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "KeePass entry"
	}
	name = a.uniqueCredentialName(name, existing)

	// Auto-created KeePass credentials collect under a single "KeePass"
	// credential folder so they don't clutter the root. The caller's FolderID,
	// when set, wins (an explicit choice); otherwise we ensure the default
	// folder. Note: a connection's folder_id must NEVER be passed here - it
	// lives in a different tree (see onKeepassPick).
	folderID := in.FolderID
	if folderID == nil {
		if fid, err := a.ensureKeepassCredFolder(); err == nil {
			folderID = fid
		}
	}

	var defUser *string
	if u := strings.TrimSpace(in.Username); u != "" {
		defUser = &u
	}
	res, err := a.credSvc.Create(creds.CreateInput{
		Kind:             "keepass",
		Name:             name,
		FolderID:         folderID,
		DefaultUsername:  defUser,
		KeepassDBID:      in.DBID,
		KeepassEntryUUID: in.EntryUUID,
		KeepassField:     field,
		KeepassIsKey:     in.IsKey,
	})
	if err != nil {
		return nil, err
	}
	a.recordAudit("keepass.credential.create", res.Credential.ID, map[string]string{"name": name})
	return res.Credential, nil
}

// keepassCredFolderName is the credential folder auto-created KeePass
// references collect under.
const keepassCredFolderName = "KeePass"

// ensureKeepassCredFolder returns the id of the "KeePass" credential folder at
// the root, creating it on first use. Best-effort: on any error the caller
// falls back to the root (folder_id nil).
func (a *App) ensureKeepassCredFolder() (*string, error) {
	folders, err := a.db.ListCredentialFolders()
	if err != nil {
		return nil, err
	}
	for i := range folders {
		if folders[i].ParentID == nil && folders[i].Name == keepassCredFolderName {
			id := folders[i].ID
			return &id, nil
		}
	}
	f, err := a.db.CreateCredentialFolder(keepassCredFolderName, nil)
	if err != nil {
		return nil, err
	}
	return &f.ID, nil
}

// uniqueCredentialName appends " (2)", " (3)", ... until the name is free,
// since credential names are unique.
func (a *App) uniqueCredentialName(base string, existing []store.CredentialRef) string {
	taken := make(map[string]bool, len(existing))
	for i := range existing {
		taken[existing[i].Name] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s (%d)", base, n)
		if !taken[cand] {
			return cand
		}
	}
}

// ---------- vault account naming ----------

func (a *App) keepassSecretAccounts(id string) (master, keyfile string) {
	return a.keepassSecretAccountMaster(id), a.keepassKeyfileAccount(id)
}
func (a *App) keepassSecretAccountMaster(id string) string { return "keepass:" + id + ":master" }
func (a *App) keepassKeyfileAccount(id string) string      { return "keepass:" + id + ":keyfile" }
func (a *App) keepassRemotePassAccount(id string) string   { return "keepass:" + id + ":remotepass" }
