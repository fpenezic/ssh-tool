package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// NetworkProfile is a stored userspace-WireGuard profile. ConfigJSON
// is the secretless wg.Profile serialization (addresses, DNS, MTU,
// peers with public keys / endpoints / allowed IPs); the interface
// private key and per-peer preshared keys live in the vault. Kept as
// an opaque JSON column here so the store doesn't import internal/wg.
type NetworkProfile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ConfigJSON string `json:"config_json"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

func (d *DB) CreateNetworkProfile(name, configJSON string) (*NetworkProfile, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	now := time.Now().Unix()
	p := &NetworkProfile{
		ID:         newID(),
		Name:       name,
		ConfigJSON: configJSON,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	_, err := d.conn.Exec(
		`INSERT INTO network_profiles (id, name, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.ConfigJSON, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (d *DB) UpdateNetworkProfile(id, name, configJSON string) (*NetworkProfile, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	now := time.Now().Unix()
	res, err := d.conn.Exec(
		`UPDATE network_profiles SET name = ?, config_json = ?, updated_at = ? WHERE id = ?`,
		name, configJSON, now, id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("network profile %s not found", id)
	}
	return d.GetNetworkProfile(id)
}

func (d *DB) GetNetworkProfile(id string) (*NetworkProfile, error) {
	row := d.conn.QueryRow(
		`SELECT id, name, config_json, created_at, updated_at
		 FROM network_profiles WHERE id = ?`, id)
	var p NetworkProfile
	if err := row.Scan(&p.ID, &p.Name, &p.ConfigJSON, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("network profile %s not found", id)
		}
		return nil, err
	}
	return &p, nil
}

func (d *DB) ListNetworkProfiles() ([]NetworkProfile, error) {
	rows, err := d.conn.Query(
		`SELECT id, name, config_json, created_at, updated_at
		 FROM network_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkProfile
	for rows.Next() {
		var p NetworkProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.ConfigJSON, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) DeleteNetworkProfile(id string) error {
	_, err := d.conn.Exec(`DELETE FROM network_profiles WHERE id = ?`, id)
	return err
}
