package store

import (
	"database/sql"
	"fmt"
)

type KnownHost struct {
	ID          string
	Hostname    string
	Port        int
	KeyType     string
	KeyB64      string
	Fingerprint string
	AddedAt     int64
}

// GetKnownHost returns the pinned record for (hostname, port), or
// nil if none. There is at most one row per host:port - the
// algorithm is pinned alongside the key blob so the caller can
// require the server to serve exactly that algo (see migration 12
// for the security rationale).
func (db *DB) GetKnownHost(hostname string, port int) (*KnownHost, error) {
	row := db.conn.QueryRow(`
		SELECT id, hostname, port, key_type, key_b64, fingerprint, added_at
		FROM known_hosts WHERE hostname=? AND port=?
	`, hostname, port)
	var h KnownHost
	err := row.Scan(&h.ID, &h.Hostname, &h.Port, &h.KeyType, &h.KeyB64, &h.Fingerprint, &h.AddedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("known_hosts get: %w", err)
	}
	return &h, nil
}

// UpsertKnownHost overwrites whatever row exists for (hostname,
// port) - the user has just confirmed a new fingerprint, so a
// previous algo/key is being intentionally replaced.
func (db *DB) UpsertKnownHost(hostname string, port int, keyType, keyB64, fingerprint string) error {
	_, err := db.conn.Exec(`
		INSERT INTO known_hosts (hostname, port, key_type, key_b64, fingerprint)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(hostname, port) DO UPDATE SET
		    key_type    = excluded.key_type,
		    key_b64     = excluded.key_b64,
		    fingerprint = excluded.fingerprint,
		    added_at    = unixepoch()
	`, hostname, port, keyType, keyB64, fingerprint)
	if err != nil {
		return fmt.Errorf("known_hosts upsert: %w", err)
	}
	return nil
}
