package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// A DB that ran the ORIGINAL migration 19 (bitwarden_servers WITHOUT
// network_profile_id, before the column was amended into the CREATE
// TABLE) must be repaired by migration 21's ADD COLUMN, not left
// throwing "no such column: network_profile_id".
func TestMigration21AddsBitwardenNetworkProfileID(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Simulate the legacy shape: bitwarden_servers as migration 19
	// originally created it (no network_profile_id), and the schema
	// pinned at version 20 so migration 21 is the only one that runs.
	_, err = db.Exec(`
		CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO schema_meta (key, value) VALUES ('version', '20');
		CREATE TABLE bitwarden_servers (
		    id             TEXT PRIMARY KEY,
		    name           TEXT NOT NULL UNIQUE,
		    server_url     TEXT NOT NULL,
		    api_key_ref    TEXT NOT NULL DEFAULT '',
		    master_ref     TEXT NOT NULL DEFAULT '',
		    last_synced_at INTEGER,
		    last_hash      TEXT NOT NULL DEFAULT '',
		    created_at     INTEGER NOT NULL,
		    updated_at     INTEGER NOT NULL
		);`)
	if err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	// The failing query shape from the field report must now work.
	if _, err := db.Query(`SELECT network_profile_id FROM bitwarden_servers`); err != nil {
		t.Fatalf("network_profile_id still missing after repair: %v", err)
	}
}

// Running migration 21 against a DB that ALREADY has the column (created
// by the amended migration 19) must be a no-op, not a duplicate-column
// failure.
func TestMigration21IdempotentWhenColumnPresent(t *testing.T) {
	path := t.TempDir() + "/fresh.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Fresh DB: run every migration from scratch (bitwarden_servers is
	// created with network_profile_id by migration 19). Migration 21's
	// ADD COLUMN then hits an existing column and must be tolerated.
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations (fresh): %v", err)
	}
	// Re-running is also a no-op.
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations (rerun): %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM schema_meta WHERE key='version'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "21" {
		t.Fatalf("schema version = %q, want 21", v)
	}
}
