package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// KeepassSource is where a KeePass database file lives.
type KeepassSource string

const (
	KeepassLocal  KeepassSource = "local"
	KeepassWebDAV KeepassSource = "webdav"
	KeepassSFTP   KeepassSource = "sftp"
)

// KeepassDatabase is a registered .kdbx ssh-tool reads secrets from. The master
// password and key file are NOT stored here - MasterRef / KeyfileRef point at
// vault accounts. For remote sources the file is fetched and cached under
// DataDir; Path then points at the cache and URL at the origin.
type KeepassDatabase struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Source        KeepassSource     `json:"source"`
	Path          string            `json:"path"`
	URL           string            `json:"url"`
	MasterRef     string            `json:"master_ref"`
	KeyfileRef    string            `json:"keyfile_ref"`
	RemoteConfig  map[string]string `json:"remote_config"`
	LastFetchedAt *int64            `json:"last_fetched_at"`
	LastETag      string            `json:"last_etag"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
}

// KeepassRef is the reference a credential's config_json carries to point at a
// specific entry+field inside a registered database. Stored under the key
// "keepass_ref" in credential_refs.config_json.
type KeepassRef struct {
	DBID      string `json:"db_id"`
	EntryUUID string `json:"entry_uuid"`
	Field     string `json:"field"` // "password" | a custom field | an attachment key
}

// ParseKeepassRef extracts a KeepassRef from a credential's config map, or nil
// if the credential has no keepass_ref. Used by the resolver to decide whether
// to route through the KeePass layer instead of the vault.
func ParseKeepassRef(config map[string]any) *KeepassRef {
	raw, ok := config["keepass_ref"]
	if !ok {
		return nil
	}
	// config_json round-trips through map[string]any, so the ref is a nested
	// map, not a typed struct.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ref KeepassRef
	if err := json.Unmarshal(b, &ref); err != nil {
		return nil
	}
	if ref.DBID == "" || ref.EntryUUID == "" {
		return nil
	}
	if ref.Field == "" {
		ref.Field = "password"
	}
	return &ref
}

func (d *DB) CreateKeepassDatabase(k KeepassDatabase) (*KeepassDatabase, error) {
	if strings.TrimSpace(k.Name) == "" {
		return nil, fmt.Errorf("validation: keepass database name is empty")
	}
	if k.Source == "" {
		return nil, fmt.Errorf("validation: keepass source is empty")
	}
	k.ID = newID()
	ts := now()
	k.CreatedAt, k.UpdatedAt = ts, ts
	if k.RemoteConfig == nil {
		k.RemoteConfig = map[string]string{}
	}
	cfgJSON, err := json.Marshal(k.RemoteConfig)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(
		`INSERT INTO keepass_databases
		 (id, name, source, path, url, master_ref, keyfile_ref, remote_cfg_json,
		  last_fetched_at, last_etag, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Name, string(k.Source), k.Path, k.URL, k.MasterRef, k.KeyfileRef,
		string(cfgJSON), k.LastFetchedAt, k.LastETag, k.CreatedAt, k.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: keepass database '%s' already exists", k.Name)
		}
		return nil, err
	}
	return &k, nil
}

func (d *DB) GetKeepassDatabase(id string) (*KeepassDatabase, error) {
	row := d.conn.QueryRow(
		`SELECT id, name, source, path, url, master_ref, keyfile_ref, remote_cfg_json,
		        last_fetched_at, last_etag, created_at, updated_at
		 FROM keepass_databases WHERE id = ?`, id,
	)
	return scanKeepass(row)
}

func (d *DB) ListKeepassDatabases() ([]KeepassDatabase, error) {
	rows, err := d.conn.Query(
		`SELECT id, name, source, path, url, master_ref, keyfile_ref, remote_cfg_json,
		        last_fetched_at, last_etag, created_at, updated_at
		 FROM keepass_databases ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeepassDatabase
	for rows.Next() {
		k, err := scanKeepass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// UpdateKeepassDatabase updates the mutable metadata fields. It intentionally
// does NOT touch last_fetched_at / last_etag (those move via TouchKeepassFetch).
func (d *DB) UpdateKeepassDatabase(k KeepassDatabase) (*KeepassDatabase, error) {
	if strings.TrimSpace(k.Name) == "" {
		return nil, fmt.Errorf("validation: keepass database name is empty")
	}
	if k.RemoteConfig == nil {
		k.RemoteConfig = map[string]string{}
	}
	cfgJSON, err := json.Marshal(k.RemoteConfig)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(
		`UPDATE keepass_databases SET
		   name=?, source=?, path=?, url=?, master_ref=?, keyfile_ref=?,
		   remote_cfg_json=?, updated_at=?
		 WHERE id=?`,
		k.Name, string(k.Source), k.Path, k.URL, k.MasterRef, k.KeyfileRef,
		string(cfgJSON), now(), k.ID,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("conflict: keepass database name already exists")
		}
		return nil, err
	}
	return d.GetKeepassDatabase(k.ID)
}

// TouchKeepassFetch records a successful remote fetch: when it happened and the
// validator (ETag) the cache was written from.
func (d *DB) TouchKeepassFetch(id string, fetchedAt int64, etag string) error {
	_, err := d.conn.Exec(
		`UPDATE keepass_databases SET last_fetched_at=?, last_etag=?, updated_at=? WHERE id=?`,
		fetchedAt, etag, now(), id,
	)
	return err
}

func (d *DB) DeleteKeepassDatabase(id string) error {
	res, err := d.conn.Exec("DELETE FROM keepass_databases WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// KeepassUsage returns credentials that reference the given KeePass database,
// so the UI can warn before deleting a database still in use.
func (d *DB) KeepassUsage(dbID string) ([]string, error) {
	creds, err := d.ListCredentials()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range creds {
		if ref := ParseKeepassRef(c.Config); ref != nil && ref.DBID == dbID {
			out = append(out, c.Name)
		}
	}
	return out, nil
}

func scanKeepass(s scanner) (*KeepassDatabase, error) {
	var (
		k             KeepassDatabase
		source        string
		cfgRaw        string
		lastFetched   sql.NullInt64
	)
	err := s.Scan(
		&k.ID, &k.Name, &source, &k.Path, &k.URL, &k.MasterRef, &k.KeyfileRef,
		&cfgRaw, &lastFetched, &k.LastETag, &k.CreatedAt, &k.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	k.Source = KeepassSource(source)
	if err := json.Unmarshal([]byte(cfgRaw), &k.RemoteConfig); err != nil {
		return nil, err
	}
	if lastFetched.Valid {
		v := lastFetched.Int64
		k.LastFetchedAt = &v
	}
	return &k, nil
}
