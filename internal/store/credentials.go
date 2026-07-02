package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type NewCredential struct {
	Name                 string
	Kind                 CredentialKind
	StorageMode          StorageMode
	FolderID             *string
	Hint                 string
	Tags                 []string
	Config               map[string]any
	PublicKey            *string
	VaultKey             *string
	DefaultUsername      *string
	ExpiresAt            *int64
	RotationReminderDays *int64
	RetainHistory        bool
}

func (d *DB) CreateCredential(in NewCredential) (*CredentialRef, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("validation: credential name is empty")
	}
	id := newID()
	ts := now()
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, err
	}
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(
		`INSERT INTO credential_refs
		 (id, folder_id, name, kind, storage_mode, hint, tags_json, config_json, public_key,
		  vault_key, default_username, last_rotated_at, expires_at,
		  rotation_reminder_days, retain_history, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.FolderID, in.Name, string(in.Kind), string(in.StorageMode), in.Hint,
		string(tagsJSON), string(cfgJSON), in.PublicKey,
		in.VaultKey, in.DefaultUsername, ts, in.ExpiresAt,
		in.RotationReminderDays, boolToInt(in.RetainHistory), ts, ts,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: credential name '%s' already exists", in.Name)
		}
		return nil, err
	}
	return d.GetCredential(id)
}

func (d *DB) GetCredential(id string) (*CredentialRef, error) {
	row := d.conn.QueryRow(
		`SELECT id, folder_id, name, kind, storage_mode, hint, tags_json, config_json,
		        public_key, vault_key, default_username, last_rotated_at,
		        expires_at, rotation_reminder_days, retain_history, icon_image_id,
		        created_at, updated_at
		 FROM credential_refs WHERE id = ?`, id,
	)
	return scanCredential(row)
}

func (d *DB) ListCredentials() ([]CredentialRef, error) {
	rows, err := d.conn.Query(
		`SELECT id, folder_id, name, kind, storage_mode, hint, tags_json, config_json,
		        public_key, vault_key, default_username, last_rotated_at,
		        expires_at, rotation_reminder_days, retain_history, icon_image_id,
		        created_at, updated_at
		 FROM credential_refs ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialRef
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

type UpdateCredential struct {
	ID                       string
	Kind                     *CredentialKind
	FolderID                 *string
	SetFolderToNull          bool
	Name                     *string
	Hint                     *string
	Tags                     *[]string
	Config                   *map[string]any
	PublicKey                *string // nil = leave existing; use SetPublicKeyToNull to clear
	SetPublicKeyToNull       bool
	DefaultUsername          *string
	SetDefaultUsernameToNull bool
	ExpiresAt                *int64
	SetExpiresAtToNull       bool
	RotationReminderDays     *int64
	SetReminderToNull        bool
	RetainHistory            *bool
}

func (d *DB) UpdateCredential(in UpdateCredential) (*CredentialRef, error) {
	existing, err := d.GetCredential(in.ID)
	if err != nil {
		return nil, err
	}
	newKind := existing.Kind
	if in.Kind != nil {
		newKind = *in.Kind
	}
	newFolder := existing.FolderID
	if in.SetFolderToNull {
		newFolder = nil
	} else if in.FolderID != nil {
		newFolder = in.FolderID
	}
	newName := existing.Name
	if in.Name != nil {
		newName = *in.Name
	}
	if strings.TrimSpace(newName) == "" {
		return nil, fmt.Errorf("validation: credential name is empty")
	}
	newHint := existing.Hint
	if in.Hint != nil {
		newHint = *in.Hint
	}
	newTags := existing.Tags
	if in.Tags != nil {
		newTags = *in.Tags
	}
	newCfg := existing.Config
	if in.Config != nil {
		newCfg = *in.Config
	}
	newPublic := existing.PublicKey
	if in.SetPublicKeyToNull {
		newPublic = nil
	} else if in.PublicKey != nil {
		newPublic = in.PublicKey
	}
	newDefUser := existing.DefaultUsername
	if in.SetDefaultUsernameToNull {
		newDefUser = nil
	} else if in.DefaultUsername != nil {
		newDefUser = in.DefaultUsername
	}
	newExpires := existing.ExpiresAt
	if in.SetExpiresAtToNull {
		newExpires = nil
	} else if in.ExpiresAt != nil {
		newExpires = in.ExpiresAt
	}
	newReminder := existing.RotationReminderDays
	if in.SetReminderToNull {
		newReminder = nil
	} else if in.RotationReminderDays != nil {
		newReminder = in.RotationReminderDays
	}
	newRetain := existing.RetainHistory
	if in.RetainHistory != nil {
		newRetain = *in.RetainHistory
	}

	tagsJSON, err := json.Marshal(newTags)
	if err != nil {
		return nil, err
	}
	cfgJSON, err := json.Marshal(newCfg)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(
		`UPDATE credential_refs SET
		   folder_id=?, name=?, kind=?, hint=?, tags_json=?, config_json=?, public_key=?,
		   default_username=?, expires_at=?, rotation_reminder_days=?,
		   retain_history=?, updated_at=?
		 WHERE id=?`,
		newFolder, newName, string(newKind), newHint, string(tagsJSON), string(cfgJSON), newPublic,
		newDefUser, newExpires, newReminder,
		boolToInt(newRetain), now(), in.ID,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: credential name already exists")
		}
		return nil, err
	}
	return d.GetCredential(in.ID)
}

func (d *DB) SetCredentialIcon(id, imageID string) error {
	_, err := d.conn.Exec(
		"UPDATE credential_refs SET icon_image_id=?, updated_at=? WHERE id=?",
		imageID, now(), id,
	)
	return err
}

func (d *DB) SetCredentialVaultKey(id, vaultKey string) error {
	_, err := d.conn.Exec(
		"UPDATE credential_refs SET vault_key=?, updated_at=? WHERE id=?",
		vaultKey, now(), id,
	)
	return err
}

func (d *DB) TouchCredentialRotated(id string) error {
	_, err := d.conn.Exec(
		"UPDATE credential_refs SET last_rotated_at=?, updated_at=? WHERE id=?",
		now(), now(), id,
	)
	return err
}

func (d *DB) DeleteCredential(id string) error {
	res, err := d.conn.Exec("DELETE FROM credential_refs WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UsageRef is a reverse-lookup hit: a folder or connection that references
// the given credential anywhere in its settings (auth_ref or jump chain).
type UsageRef struct {
	Kind     string `json:"kind"` // "folder" | "connection"
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
}

func (d *DB) CredentialUsage(credentialID string) ([]UsageRef, error) {
	folders, err := d.ListFolders()
	if err != nil {
		return nil, err
	}
	connections, err := d.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	var out []UsageRef
	for _, f := range folders {
		if settingsUsesCred(&f.Settings, credentialID) {
			out = append(out, UsageRef{Kind: "folder", ID: f.ID, Name: f.Name})
		}
	}
	for _, c := range connections {
		if settingsUsesCred(&c.Overrides, credentialID) {
			out = append(out, UsageRef{
				Kind: "connection", ID: c.ID, Name: c.Name, Hostname: c.Hostname,
			})
		}
	}
	return out, nil
}

func settingsUsesCred(s *InheritableSettings, credID string) bool {
	if s.AuthRef != nil && *s.AuthRef == credID {
		return true
	}
	if s.JumpHost != nil && s.JumpHost.Kind == "chain" && s.JumpHost.Chain != nil {
		if jumpUsesCred(s.JumpHost.Chain, credID) {
			return true
		}
	}
	return false
}

func jumpUsesCred(spec *JumpHostSpec, credID string) bool {
	if spec.AuthRef != nil && *spec.AuthRef == credID {
		return true
	}
	if spec.Via != nil {
		return jumpUsesCred(spec.Via, credID)
	}
	return false
}

// ---------- credential history ----------

func (d *DB) AppendHistory(credentialID, note, rotatedBy string, hasValue bool) (*CredentialHistoryEntry, error) {
	id := newID()
	ts := now()
	_, err := d.conn.Exec(
		`INSERT INTO credential_history
		 (id, credential_id, changed_at, note, rotated_by, has_value)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, credentialID, ts, note, rotatedBy, boolToInt(hasValue),
	)
	if err != nil {
		return nil, err
	}
	return &CredentialHistoryEntry{
		ID: id, CredentialID: credentialID, ChangedAt: ts,
		Note: note, RotatedBy: rotatedBy, HasValue: hasValue,
	}, nil
}

// ---------- credential secret history (sealed previous values) ----------

// AppendSecretHistory records that a previous secret has been sealed
// in the vault under vaultAccount. The caller is responsible for
// having actually written the value to the vault BEFORE this row is
// inserted - partial state (row without corresponding vault entry)
// is harmless (Reveal returns "vault entry missing") but cleanup
// becomes ambiguous.
func (d *DB) AppendSecretHistory(credentialID, vaultAccount, note, rotatedBy string) (*CredentialSecretHistoryEntry, error) {
	id := newID()
	ts := now()
	_, err := d.conn.Exec(
		`INSERT INTO credential_secret_history
		 (id, credential_id, rotated_at, vault_account, note, rotated_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, credentialID, ts, vaultAccount, note, rotatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &CredentialSecretHistoryEntry{
		ID: id, CredentialID: credentialID, RotatedAt: ts,
		VaultAccount: vaultAccount, Note: note, RotatedBy: rotatedBy,
	}, nil
}

// ListSecretHistory returns newest-first snapshots for a credential.
// Plaintexts stay in the vault; the UI lazy-reveals via the dedicated
// IPC.
func (d *DB) ListSecretHistory(credentialID string) ([]CredentialSecretHistoryEntry, error) {
	rows, err := d.conn.Query(
		`SELECT id, credential_id, rotated_at, vault_account, note, rotated_by
		 FROM credential_secret_history WHERE credential_id = ?
		 ORDER BY rotated_at DESC`,
		credentialID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialSecretHistoryEntry
	for rows.Next() {
		var e CredentialSecretHistoryEntry
		if err := rows.Scan(&e.ID, &e.CredentialID, &e.RotatedAt, &e.VaultAccount, &e.Note, &e.RotatedBy); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetSecretHistory loads one history row by id. The caller uses this
// during reveal so it can look up the vault account in one query and
// confirm the row still belongs to the credential the user requested
// (defence against UI-side id confusion).
func (d *DB) GetSecretHistory(id string) (*CredentialSecretHistoryEntry, error) {
	row := d.conn.QueryRow(
		`SELECT id, credential_id, rotated_at, vault_account, note, rotated_by
		 FROM credential_secret_history WHERE id = ?`,
		id,
	)
	var e CredentialSecretHistoryEntry
	if err := row.Scan(&e.ID, &e.CredentialID, &e.RotatedAt, &e.VaultAccount, &e.Note, &e.RotatedBy); err != nil {
		return nil, err
	}
	return &e, nil
}

// DeleteSecretHistory drops a single history row. Returns the
// vault_account so the caller can purge the matching vault entry.
func (d *DB) DeleteSecretHistory(id string) (string, error) {
	row := d.conn.QueryRow(
		`SELECT vault_account FROM credential_secret_history WHERE id = ?`,
		id,
	)
	var acct string
	if err := row.Scan(&acct); err != nil {
		return "", err
	}
	if _, err := d.conn.Exec(`DELETE FROM credential_secret_history WHERE id = ?`, id); err != nil {
		return "", err
	}
	return acct, nil
}

// SecretHistoryAccountsToPrune returns the vault_accounts of history
// rows older than the keep-last-N cutoff for a credential. Callers
// use this to enforce retention after appending a new snapshot: take
// the returned accounts, delete them from the vault, then delete the
// rows themselves via DeleteSecretHistoriesByAccount.
func (d *DB) SecretHistoryAccountsToPrune(credentialID string, keepN int) ([]string, error) {
	if keepN <= 0 {
		keepN = 5
	}
	rows, err := d.conn.Query(
		`SELECT vault_account FROM credential_secret_history
		 WHERE credential_id = ?
		 ORDER BY rotated_at DESC
		 LIMIT -1 OFFSET ?`,
		credentialID, keepN,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteSecretHistoryRowsByAccount drops history rows whose
// vault_account matches one of the given accounts. Used together
// with SecretHistoryAccountsToPrune.
func (d *DB) DeleteSecretHistoryRowsByAccount(accounts []string) error {
	if len(accounts) == 0 {
		return nil
	}
	q := `DELETE FROM credential_secret_history WHERE vault_account IN (`
	args := make([]any, 0, len(accounts))
	for i, a := range accounts {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, a)
	}
	q += ")"
	_, err := d.conn.Exec(q, args...)
	return err
}

func (d *DB) ListHistory(credentialID string) ([]CredentialHistoryEntry, error) {
	rows, err := d.conn.Query(
		`SELECT id, credential_id, changed_at, note, rotated_by, has_value
		 FROM credential_history WHERE credential_id = ?
		 ORDER BY changed_at DESC`,
		credentialID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialHistoryEntry
	for rows.Next() {
		var (
			e  CredentialHistoryEntry
			hv int64
		)
		if err := rows.Scan(&e.ID, &e.CredentialID, &e.ChangedAt, &e.Note, &e.RotatedBy, &hv); err != nil {
			return nil, err
		}
		e.HasValue = hv != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- scanning ----------

func scanCredential(s scanner) (*CredentialRef, error) {
	var (
		c               CredentialRef
		folderID        sql.NullString
		kind, mode      string
		tagsRaw, cfgRaw string
		pubkey          sql.NullString
		vaultKey        sql.NullString
		defUser         sql.NullString
		lastRot, expAt  sql.NullInt64
		remDays         sql.NullInt64
		retain          int64
		iconImageID     sql.NullString
	)
	err := s.Scan(
		&c.ID, &folderID, &c.Name, &kind, &mode, &c.Hint, &tagsRaw, &cfgRaw,
		&pubkey, &vaultKey, &defUser, &lastRot, &expAt, &remDays, &retain, &iconImageID,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Kind = CredentialKind(kind)
	c.StorageMode = StorageMode(mode)
	if folderID.Valid {
		c.FolderID = &folderID.String
	}
	if err := json.Unmarshal([]byte(tagsRaw), &c.Tags); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(cfgRaw), &c.Config); err != nil {
		return nil, err
	}
	if pubkey.Valid {
		c.PublicKey = &pubkey.String
	}
	if vaultKey.Valid {
		c.VaultKey = &vaultKey.String
	}
	if defUser.Valid {
		c.DefaultUsername = &defUser.String
	}
	if lastRot.Valid {
		v := lastRot.Int64
		c.LastRotatedAt = &v
	}
	if expAt.Valid {
		v := expAt.Int64
		c.ExpiresAt = &v
	}
	if remDays.Valid {
		v := remDays.Int64
		c.RotationReminderDays = &v
	}
	c.RetainHistory = retain != 0
	if iconImageID.Valid {
		c.IconImageID = &iconImageID.String
	}
	return &c, nil
}
