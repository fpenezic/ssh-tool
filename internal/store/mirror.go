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
	_, err := d.MirrorFromWith(srcPath, ShadowTrust{})
	return err
}

// MirrorInfo says how MirrorFromWith treated the catalog part of a pull.
type MirrorInfo struct {
	// Legacy: the snapshot had no trusted catalog shadow (an app without
	// catalog support pushed it, or a stale shadow), so this machine's
	// catalog data was kept rather than replaced.
	Legacy bool
	// KeptCatalog: Legacy, and there was catalog data here to keep - the
	// remote now lacks it, and pushing once puts it back for this user's
	// other up-to-date machines.
	KeptCatalog bool
}

// MirrorFromWith is MirrorFrom with the shadow-trust rule of the pull (see
// shadow.go): a trusted shadow is taken back into the tree tables, anything
// else leaves this machine's catalog data alone.
func (d *DB) MirrorFromWith(srcPath string, trust ShadowTrust) (MirrorInfo, error) {
	var info MirrorInfo
	src, err := sql.Open("sqlite", srcPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return info, fmt.Errorf("mirror: open source: %w", err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)

	// Insert order: a table may only be filled after the tables it
	// references by FK. Delete runs in reverse.
	order := []string{
		"images",
		"credential_folders",
		// catalogs before folders: a catalog's root folder names it.
		"catalogs",
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
		// The catalog client's local state: slots as last synced,
		// the user's bindings and local layers. The cursor rides in
		// catalogs, so rows and cursor arrive as a consistent pair.
		"catalog_slots",
		"catalog_bindings",
		"catalog_overrides",
		"catalog_publish_pending",
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
		return info, fmt.Errorf("mirror: begin: %w", err)
	}
	defer tx.Rollback()

	// FKs off for the duration: we delete-all then insert-all, and a
	// mid-transaction child insert would otherwise fail against a
	// parent not yet repopulated. Integrity is guaranteed by mirroring
	// a self-consistent source snapshot wholesale.
	if _, err := tx.Exec("PRAGMA defer_foreign_keys = ON"); err != nil {
		return info, fmt.Errorf("mirror: defer fk: %w", err)
	}
	// The mirror replaces catalog rows wholesale, which the catalog
	// guard triggers refuse to anyone but the catalog writer.
	if err := beginCatalogWrite(tx); err != nil {
		return info, fmt.Errorf("mirror: %w", err)
	}

	// A snapshot from an app that predates the catalog schema carries
	// this machine's catalog rows as plain local copies (its mirror
	// dropped the catalog columns) and none of the catalog tables.
	// Mirroring it as usual would demote every catalog row and wipe the
	// attachment, bindings and local layers. See legacyCatalogMirror.
	var legacy *legacyCatalogMirror
	trusted := trustedShadow(src, trust)
	if !trusted {
		if legacy, err = newLegacyCatalogMirror(tx); err != nil {
			return info, fmt.Errorf("mirror: %w", err)
		}
		info.Legacy = true
		info.KeptCatalog = legacy.hasCatalogData(tx)
	}

	// Delete children first.
	for i := len(order) - 1; i >= 0; i-- {
		t := order[i]
		if legacy != nil {
			if done, err := legacy.clear(tx, t); err != nil {
				return info, fmt.Errorf("mirror: clear %s: %w", t, err)
			} else if done {
				continue
			}
		}
		if t == "images" {
			// Cached catalog images stay: snapshots leave them out
			// (shadowCatalogImages), so clearing them would mean fetching
			// every catalog icon again after each pull.
			if err := clearImagesKeepingCatalogCache(tx, src); err != nil {
				return info, fmt.Errorf("mirror: clear images: %w", err)
			}
			continue
		}
		if t == "app_settings" {
			// Preserve machine-local keys; clear the rest.
			if _, err := tx.Exec(
				`DELETE FROM app_settings WHERE key NOT LIKE 'sync_%' AND key NOT IN (`+machineLocalSettingsPlaceholders()+`)`,
				machineLocalSettingsArgs()...); err != nil {
				return info, fmt.Errorf("mirror: clear app_settings: %w", err)
			}
			continue
		}
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return info, fmt.Errorf("mirror: clear %s: %w", t, err)
		}
	}

	// Copy each table from the source.
	for _, t := range order {
		var hook rowHook
		if legacy != nil {
			if legacy.keepsTable(t) {
				continue
			}
			hook = legacy.hook(t)
		}
		if err := copyTable(tx, src, t, hook); err != nil {
			return info, fmt.Errorf("mirror: copy %s: %w", t, err)
		}
	}
	if legacy != nil {
		if err := legacy.finish(tx); err != nil {
			return info, fmt.Errorf("mirror: %w", err)
		}
	} else if err := unshadowFrom(tx, src); err != nil {
		return info, fmt.Errorf("mirror: %w", err)
	}
	// A snapshot from a version before schema 28 marks sensitive
	// connections in the old column, which nothing reads any more.
	if _, err := tx.Exec(foldLegacySensitiveSQL); err != nil {
		return info, fmt.Errorf("mirror: fold sensitive: %w", err)
	}

	if err := endCatalogWrite(tx); err != nil {
		return info, fmt.Errorf("mirror: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return info, fmt.Errorf("mirror: commit: %w", err)
	}
	return info, nil
}

// clearImagesKeepingCatalogCache deletes every image row except cached
// catalog images (catimg-*), and those too when the source brings the
// same id or the same bytes (md5 is unique), so the copy that follows
// meets no conflict. A catalog image dropped that way is either in the
// source now or fetched again.
func clearImagesKeepingCatalogCache(tx *sql.Tx, src *sql.DB) error {
	if _, err := tx.Exec(`DELETE FROM images WHERE id NOT LIKE 'catimg-%'`); err != nil {
		return err
	}
	if !tableExists(src, "images") {
		return nil
	}
	rows, err := src.Query(`SELECT id, md5 FROM images`)
	if err != nil {
		return err
	}
	var ids, sums []string
	for rows.Next() {
		var id, m string
		if err := rows.Scan(&id, &m); err != nil {
			rows.Close()
			return err
		}
		ids, sums = append(ids, id), append(sums, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range ids {
		if _, err := tx.Exec(`DELETE FROM images WHERE id LIKE 'catimg-%' AND (id = ? OR md5 = ?)`, ids[i], sums[i]); err != nil {
			return err
		}
	}
	return nil
}

// machineLocalSettings are app_settings keys that describe THIS
// machine and must not be overwritten by a pulled profile. sync_*
// keys are handled by a LIKE clause separately.
var machineLocalSettings = []string{
	"window_state_v1",
	"last_session_tabs_v1",
	"last_session_tabs_v2",
	"last_session_detached_v1",
	"settings_active_section",
	"recent_connections_count",
	"keyring_legacy_purged_v1",
	"app_log_tail_enabled",
	"local_shell_dir",
	"local_shell_kind",
	"ci_runner",
	"ci_runner_machine",
	"network_shared_proxy_listen",
	"catalog_otp_skipped", // a catalog one-time code slot the user types on this machine
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
// rowHook sees each source row before it is inserted; it may change vals
// in place, and returning false skips the row.
type rowHook func(cols []string, vals []any) bool

func copyTable(tx *sql.Tx, src *sql.DB, table string, hook rowHook) error {
	return copyTableAs(tx, src, table, table, hook)
}

// copyTableAs copies src table from into dst table table (they differ for
// shadow tables).
func copyTableAs(tx *sql.Tx, src *sql.DB, from, table string, hook rowHook) error {
	srcCols, err := tableColumns(src, from)
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
	rows, err := src.Query("SELECT " + strings.Join(cols, ",") + " FROM " + from)
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
		if hook != nil && !hook(cols, vals) {
			continue
		}
		if _, err := tx.Exec(insert, vals...); err != nil {
			return err
		}
	}
	return rows.Err()
}

// IsMachineLocalSetting reports whether a settings key describes THIS
// machine and must never arrive from a pulled profile. Exported so the
// app's own routing table can be tested against this list: the two have
// to agree, and a versioned key that is in one but not the other syncs
// something that should have stayed put.
func IsMachineLocalSetting(key string) bool { return isMachineLocalSetting(key) }

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
