// Package store is the persistence layer: SQLite (via modernc.org/sqlite
// pure-Go driver), schema migrations, and CRUD repositories for folders,
// connections, credentials, and credential history.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps the underlying *sql.DB with our application-specific helpers.
type DB struct {
	conn *sql.DB
	// audit is a separate database file (audit.db). The audit log is
	// machine-local forensics: a synced/pulled profile must not
	// overwrite this machine's history, and audit appends fire on
	// every connect - kept in store.db they made the auto-sync dirty
	// signal permanently hot. Falls back to conn if the side file
	// can't open.
	audit *sql.DB
}

// Open opens (and migrates) the SQLite database at the given path. The
// directory is created if it doesn't exist.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// modernc driver doesn't honor PRAGMAs in DSN reliably across all builds;
	// re-issue here. busy_timeout makes concurrent writers wait up to 5s
	// instead of failing instantly with "database is locked" - saw that
	// hit when the connection editor saved a jump-chain edit while the
	// dynamic refresher was committing entries in the background.
	if _, err := db.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragmas: %w", err)
	}
	// modernc/sqlite is a single-writer engine; allowing the connection
	// pool to spawn N writers in parallel is what triggers SQLITE_BUSY
	// even with WAL. Cap writers at 1 so SQLite's own queue serialises
	// transactions. Readers can still go wide via WAL.
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	audit, aerr := openAuditDB(filepath.Join(filepath.Dir(path), "audit.db"))
	if aerr != nil {
		audit = db // degrade gracefully - audit must never break the app
	} else {
		migrateAuditRows(db, audit)
	}
	return &DB{conn: db, audit: audit}, nil
}

// openAuditDB opens/creates the side audit database with the same
// pragma profile as the main store.
func openAuditDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS audit_events (
	    id            INTEGER PRIMARY KEY AUTOINCREMENT,
	    ts            INTEGER NOT NULL,
	    action        TEXT NOT NULL,
	    target        TEXT NOT NULL DEFAULT '',
	    metadata_json TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX IF NOT EXISTS idx_audit_events_ts ON audit_events(ts DESC);
	CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events(action, ts DESC);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// migrateAuditRows moves legacy audit rows out of the main store into
// the side file, once. Idempotent: runs only while the side file is
// empty and the main table still has rows. The main table stays in
// place (emptied) so the versioned migration history is untouched -
// an empty table that's never written again doesn't dirty the sync
// signal.
func migrateAuditRows(main, audit *sql.DB) {
	var sideCount int
	if err := audit.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&sideCount); err != nil || sideCount > 0 {
		return
	}
	rows, err := main.Query(`SELECT ts, action, target, metadata_json FROM audit_events ORDER BY id`)
	if err != nil {
		return // table absent - nothing to migrate
	}
	defer rows.Close()
	moved := 0
	for rows.Next() {
		var ts int64
		var action, target, meta string
		if rows.Scan(&ts, &action, &target, &meta) != nil {
			continue
		}
		if _, err := audit.Exec(
			`INSERT INTO audit_events (ts, action, target, metadata_json) VALUES (?, ?, ?, ?)`,
			ts, action, target, meta); err == nil {
			moved++
		}
	}
	_ = rows.Close()
	if moved > 0 {
		_, _ = main.Exec(`DELETE FROM audit_events`)
	}
}

// Close releases the underlying connections.
func (d *DB) Close() error {
	if d.audit != nil && d.audit != d.conn {
		_ = d.audit.Close()
	}
	return d.conn.Close()
}

// Conn returns the raw *sql.DB; callers in the same package use it directly.
func (d *DB) Conn() *sql.DB { return d.conn }

// ContentFingerprint returns a signature of the profile tables that
// changes only on a real, committed mutation and - crucially - is
// STABLE ACROSS RESTARTS and identical between machines holding the
// same data. It is the sync change signal.
//
// Two earlier attempts failed for the same underlying reason - they
// keyed off process/file state instead of content:
//   - file mtime moved on every WAL checkpoint (push's VACUUM INTO
//     triggered one), so the loop never settled;
//   - PRAGMA data_version is a per-connection counter that resets
//     each launch, so the stamp from the previous session never
//     matched and every startup pushed.
//
// This is content: per-table row count joined with MAX(updated_at)
// (or last_pulled_at for dynamic folders). A row added, removed or
// touched moves it; a checkpoint, a snapshot, a read, or a fresh
// process does not.
func (d *DB) ContentFingerprint() string {
	tables := []struct {
		name       string
		updatedCol string
	}{
		{"folders", "updated_at"},
		{"connections", "updated_at"},
		{"credential_refs", "updated_at"},
		{"credential_folders", ""},
		{"port_forwards", ""},
		{"snippets", "updated_at"},
		{"workspaces", "updated_at"},
		// dynamic_folders: row count only, NOT last_pulled_at. That column
		// is bumped to now on every successful inventory refresh
		// (ReplaceDynamicEntries), which is pure local cache state, not a
		// user edit - including it made auto-sync push a new generation on
		// every provider poll (confirmed: "dynamic_folders: 2/T1->2/T2",
		// only the timestamp moved). The folder CONFIG (provider, token
		// ref, refresh interval) lives in config_json; a real config change
		// also touches connections/folders, and add/remove moves the count.
		{"dynamic_folders", ""},
		{"dynamic_entries", ""},
		{"images", ""},
		{"pinned_dynamic_entries", ""},
		// app_settings deliberately excluded: sync writes
		// sync_generation / sync_last_at into it on every push, which
		// would re-dirty the profile immediately. None of the
		// genuinely syncable settings change without an explicit user
		// action that also touches another table.
	}
	var b strings.Builder
	for _, t := range tables {
		var rows int64
		if err := d.conn.QueryRow("SELECT COUNT(*) FROM " + t.name).Scan(&rows); err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s=%d", t.name, rows)
		if t.updatedCol != "" {
			var mx sql.NullInt64
			if err := d.conn.QueryRow("SELECT MAX(" + t.updatedCol + ") FROM " + t.name).Scan(&mx); err == nil && mx.Valid {
				fmt.Fprintf(&b, "/%d", mx.Int64)
			}
		}
		b.WriteByte(';')
	}
	// dynamic_folders config signature: the stable, user-set columns only
	// (provider, config_json, refresh_seconds), ordered by folder_id - NOT
	// last_pulled_at / last_error, which are refresh bookkeeping. The row
	// count above catches add/remove; this catches an in-place config edit
	// (e.g. a changed token ref or refresh interval) that leaves the count
	// unchanged. dynamic_folders has no updated_at column, so we sign the
	// content directly.
	if rows, err := d.conn.Query(
		`SELECT folder_id, provider, config_json, refresh_seconds
		   FROM dynamic_folders ORDER BY folder_id`); err == nil {
		b.WriteString("dynfcfg=")
		for rows.Next() {
			var id, prov, cfg string
			var refr int64
			if err := rows.Scan(&id, &prov, &cfg, &refr); err != nil {
				continue
			}
			fmt.Fprintf(&b, "%s:%s:%d:%s|", id, prov, refr, cfg)
		}
		rows.Close()
		b.WriteByte(';')
	}
	return b.String()
}

// DefaultPath returns the platform-appropriate location for the store file.
//
//	Linux:   $XDG_DATA_HOME/ssh-tool/store.db  (fallback $HOME/.local/share/...)
//	macOS:   $HOME/Library/Application Support/ssh-tool/store.db
//	Windows: %APPDATA%/ssh-tool/store.db
func DefaultPath() string {
	return filepath.Join(DataDir(), "store.db")
}

// DataDir returns the app's per-user data directory.
func DataDir() string {
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "ssh-tool")
		}
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "ssh-tool")
		}
	case "android":
		// The Android host (MainActivity) sets HOME to getFilesDir()
		// before nativeInit so the app-private dir is reachable from Go.
		// Fall back to the known app-private path if it didn't.
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, "ssh-tool")
		}
		return "/data/data/app.sshtool/files/ssh-tool"
	}
	// Linux / fallback
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "ssh-tool")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "ssh-tool")
	}
	return ".ssh-tool"
}
