package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// BitwardenServer is a registered Vaultwarden / Bitwarden server ssh-tool reads
// secrets from at connect time. Neither the master password nor the API key is
// stored here: MasterRef points at a hidden sealed vault account
// (bitwarden:<id>:master), APIKeyRef points at the vault account behind a normal
// API-key credential. A credential references an item via
// config_json.bitwarden_ref {server_id, cipher_id, field}.
type BitwardenServer struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ServerURL    string `json:"server_url"`
	APIKeyRef    string `json:"api_key_ref"`
	MasterRef    string `json:"master_ref"`
	LastSyncedAt *int64 `json:"last_synced_at"`
	LastHash     string `json:"last_hash"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// BitwardenRef is the reference a credential's config_json carries to point at a
// specific item+field inside a registered server. Stored under the key
// "bitwarden_ref" in credential_refs.config_json.
type BitwardenRef struct {
	ServerID string `json:"server_id"`
	CipherID string `json:"cipher_id"`
	Field    string `json:"field"` // "password" | "username" | "privatekey" | custom field
}

// ParseBitwardenRef extracts a BitwardenRef from a credential's config map, or
// nil if the credential has no bitwarden_ref.
func ParseBitwardenRef(config map[string]any) *BitwardenRef {
	raw, ok := config["bitwarden_ref"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ref BitwardenRef
	if err := json.Unmarshal(b, &ref); err != nil {
		return nil
	}
	if ref.ServerID == "" || ref.CipherID == "" {
		return nil
	}
	if ref.Field == "" {
		ref.Field = "password"
	}
	return &ref
}

func (d *DB) CreateBitwardenServer(s BitwardenServer) (*BitwardenServer, error) {
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("validation: bitwarden server name is empty")
	}
	if strings.TrimSpace(s.ServerURL) == "" {
		return nil, fmt.Errorf("validation: bitwarden server URL is empty")
	}
	s.ID = newID()
	ts := now()
	s.CreatedAt, s.UpdatedAt = ts, ts
	_, err := d.conn.Exec(
		`INSERT INTO bitwarden_servers
		 (id, name, server_url, api_key_ref, master_ref, last_synced_at, last_hash,
		  created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.ServerURL, s.APIKeyRef, s.MasterRef, s.LastSyncedAt,
		s.LastHash, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: bitwarden server '%s' already exists", s.Name)
		}
		return nil, err
	}
	return &s, nil
}

func (d *DB) GetBitwardenServer(id string) (*BitwardenServer, error) {
	row := d.conn.QueryRow(
		`SELECT id, name, server_url, api_key_ref, master_ref, last_synced_at,
		        last_hash, created_at, updated_at
		 FROM bitwarden_servers WHERE id = ?`, id,
	)
	return scanBitwarden(row)
}

func (d *DB) ListBitwardenServers() ([]BitwardenServer, error) {
	rows, err := d.conn.Query(
		`SELECT id, name, server_url, api_key_ref, master_ref, last_synced_at,
		        last_hash, created_at, updated_at
		 FROM bitwarden_servers ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BitwardenServer
	for rows.Next() {
		s, err := scanBitwarden(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// UpdateBitwardenServer updates the mutable metadata. It does NOT touch
// last_synced_at / last_hash (those move via TouchBitwardenSync).
func (d *DB) UpdateBitwardenServer(s BitwardenServer) (*BitwardenServer, error) {
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("validation: bitwarden server name is empty")
	}
	_, err := d.conn.Exec(
		`UPDATE bitwarden_servers SET
		   name=?, server_url=?, api_key_ref=?, master_ref=?, updated_at=?
		 WHERE id=?`,
		s.Name, s.ServerURL, s.APIKeyRef, s.MasterRef, now(), s.ID,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: bitwarden server name already exists")
		}
		return nil, err
	}
	return d.GetBitwardenServer(s.ID)
}

// TouchBitwardenSync records a successful sync: when it happened and the content
// hash the cache was written from.
func (d *DB) TouchBitwardenSync(id string, syncedAt int64, hash string) error {
	_, err := d.conn.Exec(
		`UPDATE bitwarden_servers SET last_synced_at=?, last_hash=?, updated_at=? WHERE id=?`,
		syncedAt, hash, now(), id,
	)
	return err
}

func (d *DB) DeleteBitwardenServer(id string) error {
	res, err := d.conn.Exec("DELETE FROM bitwarden_servers WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BitwardenUsage returns credentials that reference the given server, so the UI
// can warn before deleting a server still in use.
func (d *DB) BitwardenUsage(serverID string) ([]string, error) {
	creds, err := d.ListCredentials()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range creds {
		if ref := ParseBitwardenRef(c.Config); ref != nil && ref.ServerID == serverID {
			out = append(out, c.Name)
		}
	}
	return out, nil
}

func scanBitwarden(s scanner) (*BitwardenServer, error) {
	var (
		b          BitwardenServer
		lastSynced sql.NullInt64
	)
	err := s.Scan(
		&b.ID, &b.Name, &b.ServerURL, &b.APIKeyRef, &b.MasterRef, &lastSynced,
		&b.LastHash, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastSynced.Valid {
		v := lastSynced.Int64
		b.LastSyncedAt = &v
	}
	return &b, nil
}
