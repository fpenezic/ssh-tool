package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// InfisicalServer is a registered Infisical instance ssh-tool reads secrets from
// at connect time (the per-request sibling of bitwarden_servers). There is no
// master password - Infisical decrypts server-side, so the only secret is the
// machine-identity API key, held behind a normal api_token credential pointed at
// by APIKeyRef. A credential references a secret via
// config_json.infisical_ref {server_id, project_id, environment, secret_path, key}.
type InfisicalServer struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ServerURL        string `json:"server_url"`
	APIKeyRef        string `json:"api_key_ref"`        // credential id (api_token) holding client_id/secret
	NetworkProfileID string `json:"network_profile_id"` // WireGuard profile to dial through, empty = direct
	LastUsedAt       *int64 `json:"last_used_at"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

// InfisicalRef is the reference a credential's config_json carries to point at a
// specific secret inside a registered server. Stored under the key
// "infisical_ref" in credential_refs.config_json.
type InfisicalRef struct {
	ServerID    string `json:"server_id"`
	ProjectID   string `json:"project_id"`  // Infisical workspace/project id
	Environment string `json:"environment"` // environment slug (dev / prod ...)
	SecretPath  string `json:"secret_path"` // folder path, "/" for root
	Key         string `json:"key"`         // bare secret key
}

// ParseInfisicalRef extracts an InfisicalRef from a credential's config map, or
// nil if the credential has no (valid) infisical_ref.
func ParseInfisicalRef(config map[string]any) *InfisicalRef {
	raw, ok := config["infisical_ref"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ref InfisicalRef
	if err := json.Unmarshal(b, &ref); err != nil {
		return nil
	}
	if ref.ServerID == "" || ref.ProjectID == "" || ref.Environment == "" || ref.Key == "" {
		return nil
	}
	if ref.SecretPath == "" {
		ref.SecretPath = "/"
	}
	return &ref
}

func (d *DB) CreateInfisicalServer(s InfisicalServer) (*InfisicalServer, error) {
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("validation: infisical server name is empty")
	}
	if strings.TrimSpace(s.ServerURL) == "" {
		return nil, fmt.Errorf("validation: infisical server URL is empty")
	}
	s.ID = newID()
	ts := now()
	s.CreatedAt, s.UpdatedAt = ts, ts
	_, err := d.conn.Exec(
		`INSERT INTO infisical_servers
		 (id, name, server_url, api_key_ref, network_profile_id,
		  last_used_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.ServerURL, s.APIKeyRef, s.NetworkProfileID,
		s.LastUsedAt, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: infisical server '%s' already exists", s.Name)
		}
		return nil, err
	}
	return &s, nil
}

func (d *DB) GetInfisicalServer(id string) (*InfisicalServer, error) {
	row := d.conn.QueryRow(
		`SELECT id, name, server_url, api_key_ref, network_profile_id,
		        last_used_at, created_at, updated_at
		 FROM infisical_servers WHERE id = ?`, id,
	)
	return scanInfisical(row)
}

func (d *DB) ListInfisicalServers() ([]InfisicalServer, error) {
	rows, err := d.conn.Query(
		`SELECT id, name, server_url, api_key_ref, network_profile_id,
		        last_used_at, created_at, updated_at
		 FROM infisical_servers ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InfisicalServer
	for rows.Next() {
		s, err := scanInfisical(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// UpdateInfisicalServer updates the mutable metadata. It does NOT touch
// last_used_at (that moves via TouchInfisicalUsed).
func (d *DB) UpdateInfisicalServer(s InfisicalServer) (*InfisicalServer, error) {
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("validation: infisical server name is empty")
	}
	_, err := d.conn.Exec(
		`UPDATE infisical_servers SET
		   name=?, server_url=?, api_key_ref=?, network_profile_id=?, updated_at=?
		 WHERE id=?`,
		s.Name, s.ServerURL, s.APIKeyRef, s.NetworkProfileID, now(), s.ID,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: infisical server name already exists")
		}
		return nil, err
	}
	return d.GetInfisicalServer(s.ID)
}

// TouchInfisicalUsed records the last time a secret was read from this server.
func (d *DB) TouchInfisicalUsed(id string, usedAt int64) error {
	_, err := d.conn.Exec(
		`UPDATE infisical_servers SET last_used_at=? WHERE id=?`, usedAt, id,
	)
	return err
}

func (d *DB) DeleteInfisicalServer(id string) error {
	res, err := d.conn.Exec("DELETE FROM infisical_servers WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// InfisicalUsage returns credentials that reference the given server, so the UI
// can warn before deleting a server still in use.
func (d *DB) InfisicalUsage(serverID string) ([]string, error) {
	creds, err := d.ListCredentials()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range creds {
		if ref := ParseInfisicalRef(c.Config); ref != nil && ref.ServerID == serverID {
			out = append(out, c.Name)
		}
	}
	return out, nil
}

func scanInfisical(s scanner) (*InfisicalServer, error) {
	var (
		v        InfisicalServer
		lastUsed sql.NullInt64
	)
	err := s.Scan(
		&v.ID, &v.Name, &v.ServerURL, &v.APIKeyRef, &v.NetworkProfileID,
		&lastUsed, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastUsed.Valid {
		u := lastUsed.Int64
		v.LastUsedAt = &u
	}
	return &v, nil
}
