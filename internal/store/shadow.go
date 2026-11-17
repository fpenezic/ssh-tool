package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// Profile snapshots (sync push and local backups) must stay readable by an
// app that predates the catalog schema, running at the same time, without
// it ever seeing a catalog row. Such an app mirrors a pulled snapshot
// through a fixed table list with a column intersection: anything in
// folders / connections / port_forwards reaches it, anything in a table it
// does not know is ignored.
//
// So the snapshot copy (never the live store) is "shadowed" before it is
// sealed: every catalog-owned folder, connection and forward, and the
// user's own forwards on catalog connections, move whole into
// catalog_shadow_<table> tables and leave the three tree tables; workspace
// panes that open a catalog connection are stripped from the copy's
// layouts, with the full layout kept in catalog_shadow_workspaces. A
// catalog_shadow_meta row carries a nonce that sync also writes into its
// meta.json.
//
// A new app applying a snapshot moves the shadow rows back (MirrorFromWith
// live, unshadowOnOpen after a restart-time swap). It trusts them only
// when the nonce matches the meta.json of the pull, or for a local backup:
// an old app that whole-file-restored a shadowed snapshot keeps its shadow
// tables forever without refreshing them, and its later pushes must not
// bring that catalog state back. An untrusted or missing shadow means the
// legacy mirror (legacyCatalogMirror): this machine's catalog data stays.

const shadowPrefix = "catalog_shadow_"

var shadowTables = []struct{ table, where string }{
	// Order matters for the move out: children first.
	// Snippets attached to a catalog connection: an older app mirrors
	// with foreign keys on and would fail on the dangling reference.
	{"snippets", `connection_id IN (SELECT id FROM connections WHERE catalog_id IS NOT NULL)`},
	{"port_forwards", `remote_uuid IS NOT NULL OR connection_id IN (SELECT id FROM connections WHERE catalog_id IS NOT NULL)`},
	{"connections", `catalog_id IS NOT NULL`},
	{"folders", `catalog_id IS NOT NULL`},
	// Credentials made for external references, and their folder.
	{"credential_refs", catalogCredWhere},
	{"credential_folders", catalogCredWhere},
}

// NewShadowNonce returns a fresh random nonce for one snapshot.
func NewShadowNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tableExists(q interface {
	QueryRow(string, ...any) *sql.Row
}, name string) bool {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// ShadowSnapshot shadows the store file at path (a fresh VACUUM INTO copy)
// in place, with nonce. A file without the catalog schema (an older
// layout, a test fixture) is left alone.
func ShadowSnapshot(path, nonce string) error {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if !sourceHasCatalogSchema(db) || !tableExists(db, "catalog_writer") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := beginCatalogWrite(tx); err != nil {
		return err
	}
	for _, t := range shadowTables {
		sh := shadowPrefix + t.table
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + sh); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE ` + sh + ` AS SELECT * FROM ` + t.table + ` WHERE ` + t.where); err != nil {
			return fmt.Errorf("shadow %s: %w", t.table, err)
		}
	}
	// Workspaces next, while the catalog connection ids are still known.
	if err := shadowWorkspaces(tx); err != nil {
		return err
	}
	for _, t := range shadowTables {
		if _, err := tx.Exec(`DELETE FROM ` + t.table + ` WHERE id IN (SELECT id FROM ` + shadowPrefix + t.table + `)`); err != nil {
			return fmt.Errorf("shadow %s: %w", t.table, err)
		}
	}
	if err := shadowCatalogImages(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + shadowPrefix + `meta`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE ` + shadowPrefix + `meta (nonce TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO `+shadowPrefix+`meta (nonce) VALUES (?)`, nonce); err != nil {
		return err
	}
	if err := endCatalogWrite(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// shadowCatalogImages leaves cached catalog images (catimg-*) out of the
// snapshot: every machine fetches them from the catalog again. The
// shadowed rows that point at one lose the pointer (the foreign key would
// fail on the receiving side); materialise there points them at its own
// copy. An image a local row uses stays.
func shadowCatalogImages(tx *sql.Tx) error {
	if !tableExists(tx, "images") {
		return nil
	}
	for _, t := range []string{"connections", "folders", "credential_refs", "credential_folders"} {
		if _, err := tx.Exec(`UPDATE ` + shadowPrefix + t + ` SET icon_image_id = NULL WHERE icon_image_id LIKE 'catimg-%'`); err != nil {
			return fmt.Errorf("shadow images: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM images WHERE id LIKE 'catimg-%'` + imageUnusedSQL); err != nil {
		return fmt.Errorf("shadow images: %w", err)
	}
	return nil
}

func shadowWorkspaces(tx *sql.Tx) error {
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + shadowPrefix + `workspaces`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE ` + shadowPrefix + `workspaces (id TEXT PRIMARY KEY, layout_json TEXT NOT NULL)`); err != nil {
		return err
	}
	if !tableExists(tx, "workspaces") {
		return nil
	}
	catalogConns, err := idSet(tx, `SELECT id FROM connections WHERE catalog_id IS NOT NULL`)
	if err != nil || len(catalogConns) == 0 {
		return err
	}
	rows, err := tx.Query(`SELECT id, layout_json FROM workspaces`)
	if err != nil {
		return err
	}
	type ws struct{ id, full, stripped string }
	var changed []ws
	for rows.Next() {
		var w ws
		if err := rows.Scan(&w.id, &w.full); err != nil {
			rows.Close()
			return err
		}
		if s, ok := StripWorkspaceLayout(w.full, catalogConns); ok {
			w.stripped = s
			changed = append(changed, w)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, w := range changed {
		if _, err := tx.Exec(`INSERT INTO `+shadowPrefix+`workspaces (id, layout_json) VALUES (?, ?)`, w.id, w.full); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE workspaces SET layout_json = ? WHERE id = ?`, w.stripped, w.id); err != nil {
			return err
		}
	}
	return nil
}

// ShadowTrust says which shadow tables a pull may take back.
type ShadowTrust struct {
	// FromSync: the snapshot came from profile sync, so its shadow is
	// trusted only when its nonce equals Nonce (meta.json of that pull).
	// Unset (a local backup): the file is the unit, its shadow is trusted.
	FromSync bool
	Nonce    string
}

func shadowNonceOf(db *sql.DB) string {
	if !tableExists(db, shadowPrefix+"meta") {
		return ""
	}
	var n string
	_ = db.QueryRow(`SELECT nonce FROM ` + shadowPrefix + `meta LIMIT 1`).Scan(&n)
	return n
}

// trustedShadow reports whether src is a snapshot from a catalog-aware app
// whose shadow tables may be taken back. Anything else is mirrored the
// legacy way.
func trustedShadow(src *sql.DB, trust ShadowTrust) bool {
	if !sourceHasCatalogSchema(src) {
		return false
	}
	n := shadowNonceOf(src)
	if n == "" {
		return false
	}
	return !trust.FromSync || n == trust.Nonce
}

// SnapshotIsTrusted opens a snapshot store file and applies trustedShadow.
func SnapshotIsTrusted(path string, trust ShadowTrust) (bool, error) {
	src, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return false, err
	}
	defer src.Close()
	return trustedShadow(src, trust), nil
}

// unshadowFrom moves src's shadow rows into the live tables (inside the
// mirror's writer transaction) and restores the workspace layouts whose
// copy still equals the stripped form.
func unshadowFrom(tx *sql.Tx, src *sql.DB) error {
	for i := len(shadowTables) - 1; i >= 0; i-- { // parents first
		t := shadowTables[i]
		if err := copyRows(tx, src, shadowPrefix+t.table, t.table, nil); err != nil {
			return fmt.Errorf("unshadow %s: %w", t.table, err)
		}
	}
	full, err := readLayouts(src, `SELECT id, layout_json FROM `+shadowPrefix+`workspaces`)
	if err != nil {
		return err
	}
	return restoreWorkspaceLayouts(tx, full)
}

// unshadowOnOpen takes back the shadow rows a restart-time swap left in
// the live file. The staging path already decided the file is to be
// trusted (PrepareStagedStore). No-op when there is nothing to take back.
func unshadowOnOpen(db *sql.DB) error {
	if !tableExists(db, shadowPrefix+"meta") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := beginCatalogWrite(tx); err != nil {
		return err
	}
	for i := len(shadowTables) - 1; i >= 0; i-- {
		t := shadowTables[i]
		sh := shadowPrefix + t.table
		if !tableExists(tx, sh) {
			continue
		}
		cols, err := commonColumns(tx, sh, t.table)
		if err != nil {
			return err
		}
		if len(cols) > 0 {
			list := strings.Join(cols, ",")
			if _, err := tx.Exec(`INSERT OR REPLACE INTO ` + t.table + ` (` + list + `) SELECT ` + list + ` FROM ` + sh); err != nil {
				return fmt.Errorf("unshadow %s: %w", t.table, err)
			}
		}
	}
	if tableExists(tx, shadowPrefix+"workspaces") {
		full, err := readLayouts(tx, `SELECT id, layout_json FROM `+shadowPrefix+`workspaces`)
		if err != nil {
			return err
		}
		if err := restoreWorkspaceLayouts(tx, full); err != nil {
			return err
		}
	}
	if err := dropShadowTables(tx); err != nil {
		return err
	}
	if err := endCatalogWrite(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("store: took catalog rows back from a restored snapshot")
	return nil
}

func dropShadowTables(x interface {
	Exec(string, ...any) (sql.Result, error)
}) error {
	for _, t := range []string{"snippets", "port_forwards", "connections", "folders", "credential_refs", "credential_folders", "workspaces", "meta"} {
		if _, err := x.Exec(`DROP TABLE IF EXISTS ` + shadowPrefix + t); err != nil {
			return err
		}
	}
	return nil
}

// DropShadowTables removes shadow tables from a snapshot file that is about
// to be restored the legacy way, so unshadowOnOpen never sees them.
func DropShadowTables(path string) error {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	return dropShadowTables(db)
}

func commonColumns(tx *sql.Tx, a, b string) ([]string, error) {
	ac, err := txTableColumns(tx, a)
	if err != nil {
		return nil, err
	}
	bc, err := txTableColumns(tx, b)
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, c := range bc {
		in[c] = true
	}
	var out []string
	for _, c := range ac {
		if in[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// copyRows inserts every row of src table from into dst table to, over the
// columns both have.
func copyRows(tx *sql.Tx, src *sql.DB, from, to string, hook rowHook) error {
	if !tableExists(src, from) {
		return nil
	}
	return copyTableAs(tx, src, from, to, hook)
}

func readLayouts(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string) (map[string]string, error) {
	rows, err := q.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, layout string
		if err := rows.Scan(&id, &layout); err != nil {
			return nil, err
		}
		out[id] = layout
	}
	return out, rows.Err()
}

// restoreWorkspaceLayouts puts each full layout back where the workspace
// now holds exactly its stripped form. A workspace that an older app saved
// again (it differs) keeps that version: the user saw it without the
// catalog panes and chose it.
func restoreWorkspaceLayouts(tx *sql.Tx, full map[string]string) error {
	if len(full) == 0 || !tableExists(tx, "workspaces") {
		return nil
	}
	catalogConns, err := idSet(tx, `SELECT id FROM connections WHERE catalog_id IS NOT NULL`)
	if err != nil {
		return err
	}
	current, err := readLayouts(tx, `SELECT id, layout_json FROM workspaces`)
	if err != nil {
		return err
	}
	for id, f := range full {
		cur, ok := current[id]
		if !ok {
			continue // deleted there
		}
		stripped, changed := StripWorkspaceLayout(f, catalogConns)
		if !changed {
			continue
		}
		if canonicalJSON(cur) != canonicalJSON(stripped) {
			continue
		}
		if _, err := tx.Exec(`UPDATE workspaces SET layout_json = ? WHERE id = ?`, f, id); err != nil {
			return err
		}
	}
	return nil
}

func canonicalJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// StripWorkspaceLayout removes every pane that opens one of the given
// connections from a workspace layout (both stored shapes: version 2 tabs
// with sessions + a pane tree, version 1 tabs with one connectionId). A
// split that loses one side collapses into the other, sessions are
// renumbered, a tab with nothing left goes. changed is false when nothing
// referenced those connections; then the input is returned as is. Unknown
// fields are kept.
func StripWorkspaceLayout(layout string, conns map[string]bool) (string, bool) {
	var root map[string]any
	if err := json.Unmarshal([]byte(layout), &root); err != nil {
		return layout, false
	}
	tabs, _ := root["tabs"].([]any)
	changed := false
	var out []any
	for _, t := range tabs {
		tab, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		if id, ok := tab["connectionId"].(string); ok && tab["root"] == nil {
			if conns[id] {
				changed = true
				continue
			}
			out = append(out, tab)
			continue
		}
		sessions, _ := tab["sessions"].([]any)
		drop := map[int]bool{}
		for i, s := range sessions {
			if m, ok := s.(map[string]any); ok {
				if id, ok := m["connectionId"].(string); ok && conns[id] {
					drop[i] = true
				}
			}
		}
		if len(drop) == 0 {
			out = append(out, tab)
			continue
		}
		changed = true
		node := pruneSpec(tab["root"], drop)
		if node == nil {
			continue
		}
		// Renumber the sessions the remaining leaves use.
		remap := map[int]int{}
		var kept []any
		collectRefs(node, func(ref int) {
			if _, ok := remap[ref]; !ok && ref >= 0 && ref < len(sessions) {
				remap[ref] = len(kept)
				kept = append(kept, sessions[ref])
			}
		})
		renumber(node, remap)
		tab["root"] = node
		tab["sessions"] = kept
		out = append(out, tab)
	}
	if !changed {
		return layout, false
	}
	if out == nil {
		out = []any{}
	}
	root["tabs"] = out
	b, err := json.Marshal(root)
	if err != nil {
		return layout, false
	}
	return string(b), true
}

func refOf(m map[string]any) int {
	if f, ok := m["ref"].(float64); ok {
		return int(f)
	}
	return -1
}

func pruneSpec(n any, drop map[int]bool) any {
	m, ok := n.(map[string]any)
	if !ok {
		return nil
	}
	if m["kind"] == "split" {
		a := pruneSpec(m["a"], drop)
		b := pruneSpec(m["b"], drop)
		switch {
		case a == nil:
			return b
		case b == nil:
			return a
		}
		m["a"], m["b"] = a, b
		return m
	}
	if drop[refOf(m)] {
		return nil
	}
	return m
}

func collectRefs(n any, f func(int)) {
	m, ok := n.(map[string]any)
	if !ok {
		return
	}
	if m["kind"] == "split" {
		collectRefs(m["a"], f)
		collectRefs(m["b"], f)
		return
	}
	f(refOf(m))
}

func renumber(n any, remap map[int]int) {
	m, ok := n.(map[string]any)
	if !ok {
		return
	}
	if m["kind"] == "split" {
		renumber(m["a"], remap)
		renumber(m["b"], remap)
		return
	}
	if r, ok := remap[refOf(m)]; ok {
		m["ref"] = float64(r)
	}
}
