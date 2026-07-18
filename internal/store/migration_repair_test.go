package store

import (
	"database/sql"
	"fmt"
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
		);
		-- Later migrations (v22) touch connections/folders/credential_refs/
		-- credential_folders; a real DB at v20 always has them. Minimal stubs
		-- so those ALTERs have a target.
		CREATE TABLE connections (id TEXT PRIMARY KEY);
		CREATE TABLE folders (id TEXT PRIMARY KEY);
		CREATE TABLE credential_refs (id TEXT PRIMARY KEY);
		CREATE TABLE credential_folders (id TEXT PRIMARY KEY);`)
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
	// Assert we landed on the head, not a hard-coded number, so adding a
	// later migration doesn't spuriously fail this idempotency test.
	want := fmt.Sprintf("%d", LatestSchemaVersion())
	if v != want {
		t.Fatalf("schema version = %q, want %q", v, want)
	}
}

// The reported bug: a DB that ran the ORIGINAL 2-table form of migration
// 22 (icon columns on connections + folders only) is recorded at v22 and
// never re-runs it, so credential_refs / credential_folders lack
// icon_name and every credential read fails. Migration 23 must add the
// missing credential icon columns while skipping the ones v22 already
// created.
func TestMigration23RepairsCredentialIconColumns(t *testing.T) {
	path := t.TempDir() + "/legacy22.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Simulate a DB pinned at v22 with the OLD 2-table icon shape:
	// connections + folders already have icon_name/icon_color;
	// credential_refs + credential_folders do NOT.
	_, err = db.Exec(`
		CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO schema_meta (key, value) VALUES ('version', '22');
		CREATE TABLE connections (id TEXT PRIMARY KEY, icon_name TEXT, icon_color TEXT);
		CREATE TABLE folders (id TEXT PRIMARY KEY, icon_name TEXT, icon_color TEXT);
		CREATE TABLE credential_refs (id TEXT PRIMARY KEY);
		CREATE TABLE credential_folders (id TEXT PRIMARY KEY);`)
	if err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	// The failing reads from the field report must now work.
	if _, err := db.Query(`SELECT icon_name, icon_color FROM credential_refs`); err != nil {
		t.Fatalf("credential_refs icon columns still missing: %v", err)
	}
	if _, err := db.Query(`SELECT icon_name, icon_color FROM credential_folders`); err != nil {
		t.Fatalf("credential_folders icon columns still missing: %v", err)
	}
	// And the columns v22 already had must be untouched (no duplicate error
	// aborted the migration).
	if _, err := db.Query(`SELECT icon_name FROM connections`); err != nil {
		t.Fatalf("connections icon_name lost: %v", err)
	}
}
