package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// MirrorFrom replaces the contents of this store's profile tables with
// the contents of srcPath (a pulled snapshot's store.db), in a single
// transaction, WITHOUT swapping files - so the app keeps running, SSH
// sessions survive, and no restart is needed.
//
// This is the live-apply path for sync pull. The whole-file restore
// path (backup.ApplyPending) still exists for the cases a live apply
// can't cover (a brand-new machine with no open DB, schema migration
// across versions); MirrorFrom is the common case: same schema, app
// already running, just adopt the other machine's data.
//
// What is mirrored: every profile table, parent-before-child for FK
// integrity. What is NOT: the audit log (separate machine-local DB)
// and a small set of machine-local app_settings keys (sync config and
// generation, window/session state) - those identify THIS machine and
// must survive a pull, exactly as they're kept out of the synced
// snapshot's meaning elsewhere.
func (d *DB) MirrorFrom(srcPath string) error {
	src, err := sql.Open("sqlite", srcPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("mirror: open source: %w", err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)

	// Insert order: a table may only be filled after the tables it
	// references by FK. Delete runs in reverse.
	order := []string{
		"images",
		"credential_folders",
		"folders",
		"credential_refs",
		"credential_history",
		"credential_secret_history",
		// network_profiles has no FK dependency; it must be listed or a
		// live pull mirrors every table EXCEPT the VPN profiles, so a
		// second machine gets the connections that inherit a profile but
		// never the profile itself. (Cold whole-file restore is fine -
		// this list is only the live-apply path.)
		"network_profiles",
		"connections",
		"port_forwards",
		"broadcast_groups",
		"dynamic_folders",
		"dynamic_entries",
		"pinned_dynamic_entries",
		"snippets",
		"workspaces",
		"known_hosts",
		"app_settings",
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("mirror: begin: %w", err)
	}
	defer tx.Rollback()

	// FKs off for the duration: we delete-all then insert-all, and a
	// mid-transaction child insert would otherwise fail against a
	// parent not yet repopulated. Integrity is guaranteed by mirroring
	// a self-consistent source snapshot wholesale.
	if _, err := tx.Exec("PRAGMA defer_foreign_keys = ON"); err != nil {
		return fmt.Errorf("mirror: defer fk: %w", err)
	}

	// Delete children first.
	for i := len(order) - 1; i >= 0; i-- {
		t := order[i]
		if t == "app_settings" {
			// Preserve machine-local keys; clear the rest.
			if _, err := tx.Exec(
				`DELETE FROM app_settings WHERE key NOT LIKE 'sync_%' AND key NOT IN (`+machineLocalSettingsPlaceholders()+`)`,
				machineLocalSettingsArgs()...); err != nil {
				return fmt.Errorf("mirror: clear app_settings: %w", err)
			}
			continue
		}
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("mirror: clear %s: %w", t, err)
		}
	}

	// Copy each table from the source.
	for _, t := range order {
		if err := copyTable(tx, src, t); err != nil {
			return fmt.Errorf("mirror: copy %s: %w", t, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mirror: commit: %w", err)
	}
	return nil
}

// machineLocalSettings are app_settings keys that describe THIS
// machine and must not be overwritten by a pulled profile. sync_*
// keys are handled by a LIKE clause separately.
var machineLocalSettings = []string{
	"window_state_v1",
	"last_session_tabs_v1",
	"settings_active_section",
	"recent_connections_count",
	"keyring_legacy_purged_v1",
	"app_log_tail_enabled",
}

func machineLocalSettingsPlaceholders() string {
	ph := make([]string, len(machineLocalSettings))
	for i := range ph {
		ph[i] = "?"
	}
	return strings.Join(ph, ",")
}

func machineLocalSettingsArgs() []any {
	args := make([]any, len(machineLocalSettings))
	for i, k := range machineLocalSettings {
		args[i] = k
	}
	return args
}

// copyTable reads every row of one table from src and inserts it into
// the live transaction. Column list is read from the source so the
// copy is schema-shape agnostic. app_settings skips the machine-local
// keys so a pulled profile can't clobber this machine's sync config.
func copyTable(tx *sql.Tx, src *sql.DB, table string) error {
	srcCols, err := tableColumns(src, table)
	if err != nil {
		return err
	}
	// Intersect with the destination's columns so the mirror tolerates a
	// schema skew between the pushing and pulling app versions: a newer
	// snapshot's extra columns (e.g. vnc_password_vault_key added in a
	// later schema) are simply dropped when an older app pulls, and a
	// newer app pulling an older snapshot leaves its extra columns at
	// their defaults. Only columns present on BOTH sides are copied.
	dstCols, err := txTableColumns(tx, table)
	if err != nil {
		return err
	}
	dstSet := make(map[string]bool, len(dstCols))
	for _, c := range dstCols {
		dstSet[c] = true
	}
	cols := make([]string, 0, len(srcCols))
	for _, c := range srcCols {
		if dstSet[c] {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	rows, err := src.Query("SELECT " + strings.Join(cols, ",") + " FROM " + table)
	if err != nil {
		return err
	}
	defer rows.Close()

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	insert := "INSERT INTO " + table + " (" + strings.Join(cols, ",") + ") VALUES (" + placeholders + ")"
	keyIdx := -1
	if table == "app_settings" {
		for i, c := range cols {
			if c == "key" {
				keyIdx = i
			}
		}
	}

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if keyIdx >= 0 {
			if k, _ := vals[keyIdx].(string); isMachineLocalSetting(k) {
				continue // don't import another machine's local setting
			}
		}
		if _, err := tx.Exec(insert, vals...); err != nil {
			return err
		}
	}
	return rows.Err()
}

func isMachineLocalSetting(key string) bool {
	if strings.HasPrefix(key, "sync_") {
		return true
	}
	for _, k := range machineLocalSettings {
		if k == key {
			return true
		}
	}
	return false
}

// tableColumns returns the column names of a table in definition order.
func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	return scanTableInfo(rows)
}

// txTableColumns is tableColumns against an open transaction (the
// destination DB during a mirror).
func txTableColumns(tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	return scanTableInfo(rows)
}

func scanTableInfo(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}
