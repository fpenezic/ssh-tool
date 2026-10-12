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

// DeleteNetworkProfile removes the profile and every reference to it, so
// what pointed at it falls back to its inherited network: connections,
// folders and inventory folders drop the setting (an inheritable
// override), the Bitwarden / Infisical servers, which have nothing to
// inherit, go direct. Left in place, a reference fails every dial with
// "network profile <id> not found".
func (d *DB) DeleteNetworkProfile(id string) error {
	return d.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM network_profiles WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := clearNetworkRefs(tx, "%s = ?", id)
		return err
	})
}

// SweepOrphanNetworkRefs clears references to profiles that no longer
// exist - left by a delete before DeleteNetworkProfile cleaned up, or by
// a sync that brought references without their profile. Returns how many
// rows changed.
func (d *DB) SweepOrphanNetworkRefs() (int64, error) {
	var n int64
	err := d.WithTx(func(tx *sql.Tx) error {
		var err error
		n, err = clearNetworkRefs(tx, "%[1]s NOT IN ('', '"+networkDirectSentinel+"') AND %[1]s NOT IN (SELECT id FROM network_profiles)")
		return err
	})
	return n, err
}

// networkDirectSentinel is the inventory editor's explicit "Direct"
// choice (internal/inventory), not a profile id.
const networkDirectSentinel = "__direct__"

// clearNetworkRefs drops the network profile references matching cond,
// a WHERE fragment with %[1]s for the referenced value.
func clearNetworkRefs(tx *sql.Tx, cond string, args ...any) (int64, error) {
	type ref struct{ table, col, stamp string }
	jsonRefs := []ref{
		{"connections", "overrides_json", "updated_at"},
		{"folders", "settings_json", "updated_at"},
		{"dynamic_folders", "config_json", ""},
	}
	var total int64
	for _, r := range jsonRefs {
		val := fmt.Sprintf(`(CASE WHEN json_valid(%s) THEN json_extract(%s, '$.network_profile_id') END)`, r.col, r.col)
		set := fmt.Sprintf(`%s = json_remove(%s, '$.network_profile_id')`, r.col, r.col)
		q := fmt.Sprintf(`UPDATE %s SET %s`, r.table, set)
		a := append([]any{}, args...)
		if r.stamp != "" {
			q += ", " + r.stamp + " = ?"
			a = append([]any{now()}, a...)
		}
		q += " WHERE " + fmt.Sprintf(cond, val)
		res, err := tx.Exec(q, a...)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", r.table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	for _, table := range []string{"bitwarden_servers", "infisical_servers"} {
		q := fmt.Sprintf(`UPDATE %s SET network_profile_id = '', updated_at = ? WHERE `, table) + fmt.Sprintf(cond, "network_profile_id")
		res, err := tx.Exec(q, append([]any{now()}, args...)...)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
