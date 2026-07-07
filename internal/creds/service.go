package creds

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ssh-tool/internal/store"
)

// randomHex returns 2*n hex characters of crypto/rand entropy. Used
// to mint unique vault account names for history snapshots.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read on a healthy OS is infallible; if it fails we'd
		// rather crash loudly than silently produce predictable
		// vault account names.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(buf)
}

// Service combines the DB credential repo with the Vault facade. The IPC
// layer in app.go calls into here so commands don't have to thread the two
// together.
type Service struct {
	DB    *store.DB
	Vault *Vault
}

// CreateInput is a tagged union; the same shape we used in Rust IPC.
type CreateInput struct {
	Kind                 string   `json:"kind"`
	Name                 string   `json:"name"`
	FolderID             *string  `json:"folder_id"`
	Hint                 *string  `json:"hint"`
	Tags                 []string `json:"tags"`
	DefaultUsername      *string  `json:"default_username"`
	RotationReminderDays *int64   `json:"rotation_reminder_days"`
	// ExpiresAt is the token/key expiry as a unix timestamp (nil = no
	// expiry). Set by the user for time-limited secrets - API tokens,
	// setup / auth keys - so the UI can warn before they lapse.
	ExpiresAt *int64 `json:"expires_at"`

	// password
	Password string `json:"password"`

	// key_generate
	Params *GenerateParams `json:"params"`

	// key_import_paste
	PrivateOpenSSH string  `json:"private_openssh"`
	Passphrase     *string `json:"passphrase"`

	// key_file_ref
	KeyPath string `json:"key_path"`

	// agent
	SocketPath  *string `json:"socket_path"`
	Fingerprint *string `json:"fingerprint"`

	// opkssh
	KeyBasename                      string  `json:"key_basename"`
	OpksshConfigYAML                 string  `json:"opkssh_config_yaml"`
	ProviderHint                     *string `json:"provider_hint"`
	MaxCertAgeHours                  *uint32 `json:"max_cert_age_hours"`
	MinRemainingBeforeRefreshMinutes *uint32 `json:"min_remaining_before_refresh_minutes"`

	// api_token
	APITokenID     string `json:"api_token_id"`
	APITokenSecret string `json:"api_token_secret"`
}

// CreateResult mirrors the Rust struct; public_key + fingerprint surfaced to
// UI for key-generate / key-import flows.
type CreateResult struct {
	Credential  *store.CredentialRef `json:"credential"`
	PublicKey   *string              `json:"public_key"`
	Fingerprint *string              `json:"fingerprint"`
}

func hintStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Service) Create(in CreateInput) (*CreateResult, error) {
	switch in.Kind {
	case "password":
		return s.createPassword(in)
	case "key_generate":
		return s.createKeyGenerate(in)
	case "key_import_paste":
		return s.createKeyImportPaste(in)
	case "key_file_ref":
		return s.createKeyFileRef(in)
	case "agent":
		return s.createAgent(in)
	case "opkssh":
		return s.createOpkssh(in)
	case "api_token":
		return s.createAPIToken(in)
	default:
		return nil, fmt.Errorf("unknown credential kind: %s", in.Kind)
	}
}

// createAPIToken stores an opaque token: identifier (e.g.
// "user@realm!tokenid" for proxmox) in the credential's config map,
// secret in the vault. Used by the dynamic-inventory layer; not SSH
// auth material.
func (s *Service) createAPIToken(in CreateInput) (*CreateResult, error) {
	if in.APITokenSecret == "" {
		return nil, fmt.Errorf("validation: api_token_secret is empty")
	}
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:             in.FolderID,
		Name:                 in.Name,
		Kind:                 store.CredAPIToken,
		StorageMode:          store.StorageManaged,
		Hint:                 hintStr(in.Hint),
		Tags:                 in.Tags,
		Config:               map[string]any{"token_id": in.APITokenID},
		DefaultUsername:      in.DefaultUsername,
		RotationReminderDays: in.RotationReminderDays,
		ExpiresAt:            in.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	vk := VaultAccountKey(cred.ID)
	if err := s.Vault.Put(vk, in.APITokenSecret); err != nil {
		_ = s.DB.DeleteCredential(cred.ID)
		return nil, err
	}
	if err := s.DB.SetCredentialVaultKey(cred.ID, vk); err != nil {
		return nil, err
	}
	_, _ = s.DB.AppendHistory(cred.ID, "created", "user", false)
	cred, err = s.DB.GetCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	return &CreateResult{Credential: cred}, nil
}

// Row-then-vault atomic creation: insert metadata, write secret, patch
// vault_key. If vault write fails the row is deleted.
func (s *Service) createPassword(in CreateInput) (*CreateResult, error) {
	if in.Password == "" {
		return nil, fmt.Errorf("validation: password is empty")
	}
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:             in.FolderID,
		Name:                 in.Name,
		Kind:                 store.CredPassword,
		StorageMode:          store.StorageManaged,
		Hint:                 hintStr(in.Hint),
		Tags:                 in.Tags,
		Config:               map[string]any{},
		DefaultUsername:      in.DefaultUsername,
		RotationReminderDays: in.RotationReminderDays,
		ExpiresAt:            in.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	vk := VaultAccountKey(cred.ID)
	if err := s.Vault.Put(vk, in.Password); err != nil {
		_ = s.DB.DeleteCredential(cred.ID)
		return nil, err
	}
	if err := s.DB.SetCredentialVaultKey(cred.ID, vk); err != nil {
		return nil, err
	}
	_, _ = s.DB.AppendHistory(cred.ID, "created", "user", false)
	cred, err = s.DB.GetCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	return &CreateResult{Credential: cred}, nil
}

func (s *Service) createKeyGenerate(in CreateInput) (*CreateResult, error) {
	if in.Params == nil {
		return nil, fmt.Errorf("validation: missing params")
	}
	gen, err := Generate(*in.Params)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"key_type":           string(in.Params.KeyType),
		"comment":            in.Params.Comment,
		"algorithm":          gen.Algorithm,
		"fingerprint_sha256": gen.FingerprintSha256,
		"has_passphrase":     in.Params.Passphrase != nil && *in.Params.Passphrase != "",
	}
	if in.Params.Bits != nil {
		cfg["key_bits"] = *in.Params.Bits
	}
	pubCopy := gen.PublicOpenSSH
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:        in.FolderID,
		Name:            in.Name,
		Kind:            store.CredKey,
		StorageMode:     store.StorageManaged,
		Hint:            hintStr(in.Hint),
		Tags:            in.Tags,
		Config:          cfg,
		PublicKey:       &pubCopy,
		DefaultUsername: in.DefaultUsername,
		RotationReminderDays: in.RotationReminderDays,
		ExpiresAt:            in.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	vk := VaultAccountKey(cred.ID)
	if err := s.Vault.Put(vk, gen.PrivateOpenSSH); err != nil {
		_ = s.DB.DeleteCredential(cred.ID)
		return nil, err
	}
	if err := s.DB.SetCredentialVaultKey(cred.ID, vk); err != nil {
		return nil, err
	}
	_, _ = s.DB.AppendHistory(cred.ID, "generated", "user", false)
	cred, err = s.DB.GetCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	fp := gen.FingerprintSha256
	return &CreateResult{Credential: cred, PublicKey: &pubCopy, Fingerprint: &fp}, nil
}

func (s *Service) createKeyImportPaste(in CreateInput) (*CreateResult, error) {
	if in.PrivateOpenSSH == "" {
		return nil, fmt.Errorf("validation: empty key text")
	}
	parsed, err := ParsePrivate(in.PrivateOpenSSH, in.Passphrase)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"algorithm":          parsed.Algorithm,
		"fingerprint_sha256": parsed.FingerprintSha256,
		"has_passphrase":     in.Passphrase != nil && *in.Passphrase != "",
	}
	pubCopy := parsed.PublicOpenSSH
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:        in.FolderID,
		Name:            in.Name,
		Kind:            store.CredKey,
		StorageMode:     store.StorageManaged,
		Hint:            hintStr(in.Hint),
		Tags:            in.Tags,
		Config:          cfg,
		PublicKey:       &pubCopy,
		DefaultUsername: in.DefaultUsername,
		RotationReminderDays: in.RotationReminderDays,
		ExpiresAt:            in.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	vk := VaultAccountKey(cred.ID)
	if err := s.Vault.Put(vk, in.PrivateOpenSSH); err != nil {
		_ = s.DB.DeleteCredential(cred.ID)
		return nil, err
	}
	if err := s.DB.SetCredentialVaultKey(cred.ID, vk); err != nil {
		return nil, err
	}
	_, _ = s.DB.AppendHistory(cred.ID, "imported", "import", false)
	cred, err = s.DB.GetCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	fp := parsed.FingerprintSha256
	return &CreateResult{Credential: cred, PublicKey: &pubCopy, Fingerprint: &fp}, nil
}

func (s *Service) createKeyFileRef(in CreateInput) (*CreateResult, error) {
	path := expandHome(in.KeyPath)
	cfg := map[string]any{
		"key_path":       path,
		"has_passphrase": in.Passphrase != nil && *in.Passphrase != "",
	}
	var pubKey *string
	if pubBytes, err := os.ReadFile(path + ".pub"); err == nil {
		s := strings.TrimSpace(string(pubBytes))
		if s != "" {
			pubKey = &s
		}
	}
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:        in.FolderID,
		Name:            in.Name,
		Kind:            store.CredKey,
		StorageMode:     store.StorageFileRef,
		Hint:            hintStr(in.Hint),
		Tags:            in.Tags,
		Config:          cfg,
		PublicKey:       pubKey,
		DefaultUsername: in.DefaultUsername,
		RotationReminderDays: in.RotationReminderDays,
		ExpiresAt:            in.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	if in.Passphrase != nil && *in.Passphrase != "" {
		pk := PassphraseAccountKey(cred.ID)
		if err := s.Vault.Put(pk, *in.Passphrase); err != nil {
			_ = s.DB.DeleteCredential(cred.ID)
			return nil, err
		}
		if err := s.DB.SetCredentialVaultKey(cred.ID, pk); err != nil {
			return nil, err
		}
	}
	_, _ = s.DB.AppendHistory(cred.ID, "linked to file", "import", false)
	cred, err = s.DB.GetCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	return &CreateResult{Credential: cred, PublicKey: pubKey}, nil
}

func (s *Service) createAgent(in CreateInput) (*CreateResult, error) {
	cfg := map[string]any{}
	if in.SocketPath != nil {
		cfg["socket_path"] = *in.SocketPath
	}
	if in.Fingerprint != nil {
		cfg["key_fingerprint"] = *in.Fingerprint
	}
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:        in.FolderID,
		Name:            in.Name,
		Kind:            store.CredAgent,
		StorageMode:     store.StorageExternal,
		Hint:            hintStr(in.Hint),
		Tags:            in.Tags,
		Config:          cfg,
		DefaultUsername: in.DefaultUsername,
	})
	if err != nil {
		return nil, err
	}
	return &CreateResult{Credential: cred}, nil
}

func (s *Service) createOpkssh(in CreateInput) (*CreateResult, error) {
	basename := in.KeyBasename
	if basename == "" {
		basename = "id_ecdsa"
	}
	cfg := map[string]any{
		"key_basename": basename,
	}
	if in.OpksshConfigYAML != "" {
		cfg["opkssh_config_yaml"] = in.OpksshConfigYAML
	}
	if in.ProviderHint != nil {
		cfg["provider_hint"] = *in.ProviderHint
	}
	maxAge := uint32(168)
	if in.MaxCertAgeHours != nil {
		maxAge = *in.MaxCertAgeHours
	}
	cfg["max_cert_age_hours"] = maxAge
	refresh := uint32(60)
	if in.MinRemainingBeforeRefreshMinutes != nil {
		refresh = *in.MinRemainingBeforeRefreshMinutes
	}
	cfg["min_remaining_before_refresh_minutes"] = refresh

	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:        in.FolderID,
		Name:            in.Name,
		Kind:            store.CredOpkssh,
		StorageMode:     store.StorageExternal,
		Hint:            hintStr(in.Hint),
		Tags:            in.Tags,
		Config:          cfg,
		DefaultUsername: in.DefaultUsername,
	})
	if err != nil {
		return nil, err
	}
	return &CreateResult{Credential: cred}, nil
}

// Delete refuses to remove a credential still referenced anywhere.
func (s *Service) Delete(id string) error {
	cred, err := s.DB.GetCredential(id)
	if err != nil {
		return err
	}
	uses, err := s.DB.CredentialUsage(id)
	if err != nil {
		return err
	}
	if len(uses) > 0 {
		return fmt.Errorf("in use by %d entities; reassign or remove them first", len(uses))
	}
	// Wipe vault material if managed; file_ref untouched (just metadata).
	if cred.StorageMode == store.StorageManaged ||
		(cred.StorageMode == store.StorageFileRef && cred.VaultKey != nil) {
		if cred.VaultKey != nil {
			_ = s.Vault.Delete(*cred.VaultKey)
		}
	}
	// Purge sealed secret history vault entries. The DB rows
	// disappear via ON DELETE CASCADE; the vault entries are
	// disjoint storage and we delete them explicitly so we don't
	// leak old plaintext under credhist:* accounts.
	if hist, err := s.DB.ListSecretHistory(id); err == nil {
		for _, h := range hist {
			_ = s.Vault.Delete(h.VaultAccount)
		}
	}
	return s.DB.DeleteCredential(id)
}

// RevealSecret returns the raw vault secret for a credential.
// For password: the password string.
// For managed key: the private key PEM.
// For file_ref: the passphrase (if stored in vault).
func (s *Service) RevealSecret(id string) (string, error) {
	cred, err := s.DB.GetCredential(id)
	if err != nil {
		return "", err
	}
	if cred.VaultKey == nil {
		return "", fmt.Errorf("no vault secret for this credential")
	}
	secret, ok, err := s.Vault.Get(*cred.VaultKey)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("secret not found in vault (vault may be locked)")
	}
	return secret, nil
}

// RotateKey replaces the SSH key material for a managed key credential.
// generateNew=true: fresh keypair (preserves key type from config).
// generateNew=false: import provided privateOpenSSH PEM.
func (s *Service) RotateKey(id string, generateNew bool, privateOpenSSH string, passphrase *string) (*store.CredentialRef, error) {
	cred, err := s.DB.GetCredential(id)
	if err != nil {
		return nil, err
	}
	if cred.Kind != store.CredKey {
		return nil, fmt.Errorf("rotate_key only works on key credentials")
	}
	if cred.StorageMode != store.StorageManaged {
		return nil, fmt.Errorf("rotate_key requires storage_mode=managed")
	}

	var newPrivate, newPublic, newAlgorithm, newFingerprint string
	if generateNew {
		kt := KeyEd25519
		if ktype, ok := cred.Config["key_type"].(string); ok {
			kt = KeyType(ktype)
		}
		gen, err := Generate(GenerateParams{KeyType: kt})
		if err != nil {
			return nil, err
		}
		newPrivate = gen.PrivateOpenSSH
		newPublic = gen.PublicOpenSSH
		newAlgorithm = gen.Algorithm
		newFingerprint = gen.FingerprintSha256
	} else {
		if privateOpenSSH == "" {
			return nil, fmt.Errorf("validation: no key provided")
		}
		parsed, err := ParsePrivate(privateOpenSSH, passphrase)
		if err != nil {
			return nil, err
		}
		newPrivate = privateOpenSSH
		newPublic = parsed.PublicOpenSSH
		newAlgorithm = parsed.Algorithm
		newFingerprint = parsed.FingerprintSha256
	}

	vk := VaultAccountKey(cred.ID)
	if cred.VaultKey != nil {
		vk = *cred.VaultKey
	}
	if err := s.Vault.Put(vk, newPrivate); err != nil {
		return nil, err
	}
	if cred.VaultKey == nil {
		if err := s.DB.SetCredentialVaultKey(id, vk); err != nil {
			return nil, err
		}
	}

	newCfg := make(map[string]any)
	for k, v := range cred.Config {
		newCfg[k] = v
	}
	newCfg["algorithm"] = newAlgorithm
	newCfg["fingerprint_sha256"] = newFingerprint
	newCfg["has_passphrase"] = passphrase != nil && *passphrase != ""

	if err := s.DB.TouchCredentialRotated(id); err != nil {
		return nil, err
	}
	note := "key rotated (generated)"
	if !generateNew {
		note = "key rotated (imported)"
	}
	_, _ = s.DB.AppendHistory(id, note, "user", false)

	return s.DB.UpdateCredential(store.UpdateCredential{
		ID:        id,
		Config:    &newCfg,
		PublicKey: &newPublic,
	})
}

// secretHistoryRetention is how many previous secrets we keep
// sealed per credential. Older snapshots are dropped from both the
// DB and the vault on every successful rotation. Five matches the
// "Documents → Files older than 5 versions are deleted" pattern most
// users already understand from cloud sync tools; adjust here when
// the configurable retention slider lands.
const secretHistoryRetention = 5

// snapshotPreviousSecret seals the current value of vaultAccount as
// a new history entry and prunes old entries past secretHistoryRetention.
// Best-effort: the snapshot failure is logged but does NOT block the
// rotation itself - a missing previous value (cred never set, vault
// locked) shouldn't deny the user their rotation. The retention prune
// is also best-effort for the same reason.
func (s *Service) snapshotPreviousSecret(credID, liveVaultAccount, note string) {
	prev, ok, err := s.Vault.Get(liveVaultAccount)
	if err != nil || !ok || prev == "" {
		// Nothing to snapshot (first-time set, locked vault, etc.).
		return
	}
	histAccount := HistoryVaultAccountKey()
	if err := s.Vault.Put(histAccount, prev); err != nil {
		return
	}
	row, err := s.DB.AppendSecretHistory(credID, histAccount, note, "user")
	if err != nil {
		// Vault entry orphaned but harmless; the next rotation will
		// produce a fresh row and this one will never be referenced.
		return
	}
	_ = row
	// Retention: keep last N. Anything older is dropped from both
	// DB and vault. If pruning fails, we leave the rows alone - a
	// stale entry is a smaller bug than a partially-pruned state.
	prune, err := s.DB.SecretHistoryAccountsToPrune(credID, secretHistoryRetention)
	if err != nil || len(prune) == 0 {
		return
	}
	for _, acct := range prune {
		_ = s.Vault.Delete(acct)
	}
	_ = s.DB.DeleteSecretHistoryRowsByAccount(prune)
}

// HistoryVaultAccountKey returns a vault account name for a new
// history snapshot. Random suffix so two rotations within the same
// second can't collide.
func HistoryVaultAccountKey() string {
	return "credhist:" + randomHex(16)
}

// ListSecretHistory returns the sealed history metadata for a
// credential. Plaintext is intentionally not included - UI fetches
// it lazily via RevealSecretHistory.
func (s *Service) ListSecretHistory(credID string) ([]store.CredentialSecretHistoryEntry, error) {
	return s.DB.ListSecretHistory(credID)
}

// RevealSecretHistory unseals one history snapshot. Same caveats as
// RevealSecret: caller drives clipboard auto-clear.
func (s *Service) RevealSecretHistory(historyID string) (string, error) {
	h, err := s.DB.GetSecretHistory(historyID)
	if err != nil {
		return "", err
	}
	v, ok, err := s.Vault.Get(h.VaultAccount)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("history entry has no sealed value (vault entry missing)")
	}
	return v, nil
}

// DeleteSecretHistoryEntry purges one snapshot from both DB + vault.
// Useful for explicit "forget this rotation" UX.
func (s *Service) DeleteSecretHistoryEntry(historyID string) error {
	acct, err := s.DB.DeleteSecretHistory(historyID)
	if err != nil {
		return err
	}
	_ = s.Vault.Delete(acct)
	return nil
}

// RotatePassword writes the new value to the vault and bumps last_rotated_at.
func (s *Service) RotatePassword(id, newPassword string) (*store.CredentialRef, error) {
	if newPassword == "" {
		return nil, fmt.Errorf("validation: password is empty")
	}
	cred, err := s.DB.GetCredential(id)
	if err != nil {
		return nil, err
	}
	if cred.Kind != store.CredPassword {
		return nil, fmt.Errorf("rotate_password only works on password credentials")
	}
	vk := VaultAccountKey(cred.ID)
	if cred.VaultKey != nil {
		vk = *cred.VaultKey
	}
	// Snapshot the OLD value (if any) before we overwrite it. Must
	// run BEFORE Put so the value we read is still the previous one.
	s.snapshotPreviousSecret(id, vk, "password rotated")
	if err := s.Vault.Put(vk, newPassword); err != nil {
		return nil, err
	}
	if cred.VaultKey == nil {
		if err := s.DB.SetCredentialVaultKey(id, vk); err != nil {
			return nil, err
		}
	}
	if err := s.DB.TouchCredentialRotated(id); err != nil {
		return nil, err
	}
	_, _ = s.DB.AppendHistory(id, "password rotated", "user", false)
	return s.DB.GetCredential(id)
}

// RotateAPIToken replaces the token id (config) and/or the token
// secret (vault) for an api_token credential. Pass tokenID=nil to
// leave the id unchanged; pass an empty newSecret to leave the
// vault secret unchanged. At least one must be set.
func (s *Service) RotateAPIToken(id string, tokenID *string, newSecret string) (*store.CredentialRef, error) {
	cred, err := s.DB.GetCredential(id)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, fmt.Errorf("credential %s not found", id)
	}
	if cred.Kind != store.CredAPIToken {
		return nil, fmt.Errorf("rotate_api_token only works on api_token credentials")
	}
	if tokenID == nil && newSecret == "" {
		return nil, fmt.Errorf("validation: nothing to rotate")
	}

	if tokenID != nil {
		cfg := map[string]any{}
		for k, v := range cred.Config {
			cfg[k] = v
		}
		cfg["token_id"] = *tokenID
		if _, err := s.DB.UpdateCredential(store.UpdateCredential{
			ID:     id,
			Config: &cfg,
		}); err != nil {
			return nil, err
		}
	}

	if newSecret != "" {
		vk := VaultAccountKey(cred.ID)
		if cred.VaultKey != nil {
			vk = *cred.VaultKey
		}
		s.snapshotPreviousSecret(id, vk, "api token secret rotated")
		if err := s.Vault.Put(vk, newSecret); err != nil {
			return nil, err
		}
		if cred.VaultKey == nil {
			if err := s.DB.SetCredentialVaultKey(id, vk); err != nil {
				return nil, err
			}
		}
		if err := s.DB.TouchCredentialRotated(id); err != nil {
			return nil, err
		}
		_, _ = s.DB.AppendHistory(id, "api token secret rotated", "user", false)
	} else if tokenID != nil {
		_, _ = s.DB.AppendHistory(id, "api token id updated", "user", false)
	}

	return s.DB.GetCredential(id)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	return p
}
