package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// An app older than schema 29 mirrors a pulled profile through its own
// table list and column intersection: this machine's catalog rows reach it
// as plain local rows (same local ids, catalog columns dropped) and the
// catalog tables not at all. When that app pushes, its snapshot would, on
// a normal mirror here, demote every catalog row to a local one and wipe
// the attachment, the bindings and the local layers. Re-adding the catalog
// would then create duplicates next to the demoted rows.
//
// legacyCatalogMirror mirrors such a snapshot without that damage:
//   - the catalog tables and every catalog row (catalog_id set) stay as
//     they are here; only local rows are replaced;
//   - a source row with the id of a catalog row here is that row's demoted
//     copy: it is not inserted (an edit made on the old machine to a
//     catalog record is lost, which is what read-only means), but the
//     local columns the user owns on it (favourite, open hidden, stored
//     password keys; a forward's auto start, bookmarks, browser mode) are
//     adopted, since the old machine was allowed to change those;
//   - a local row the old machine created inside a catalog folder (it saw
//     a plain folder) moves to the top level instead of landing in the
//     catalog; nothing is duplicated and nothing is dropped;
//   - the user's own forwards on catalog connections are kept as well:
//     shadowing keeps them out of every snapshot an older app sees, so it
//     cannot have changed or deleted them;
//   - any other local forward is inserted only if its connection exists
//     after the mirror, so a connection deleted there leaves no forward
//     behind.
//
// Local layers and bindings are keyed by remote uuid and the catalog rows
// they belong to are kept, so nothing is left dangling either way.
type legacyCatalogMirror struct {
	folders, conns, fwds map[string]bool // catalog rows kept here
	newConns             map[string]bool // local connections inserted from the source

	adoptConns map[string][]any // id -> favorite, open_hidden, password_vault_key, vnc_password_vault_key
	adoptFwds  map[string][]any // id -> auto_start, bookmarks, browser_mode

	// Workspace layouts here that open catalog connections, so the
	// stripped copy an older app sends back can be recognised (shadow.go).
	fullLayouts map[string]string

	demoted, rehomed, droppedFwds int
}

// catalogTables are the catalog client's own tables; a legacy source has
// none of them, and the local ones are kept.
var catalogTables = map[string]bool{
	"catalogs": true, "catalog_slots": true, "catalog_bindings": true, "catalog_overrides": true,
}

// sourceHasCatalogSchema reports whether a snapshot was written by an app
// that knows catalogs (schema 29 or later).
func sourceHasCatalogSchema(src *sql.DB) bool {
	cats, err := tableColumns(src, "catalogs")
	if err != nil || len(cats) == 0 {
		return false
	}
	cols, err := tableColumns(src, "connections")
	if err != nil {
		return false
	}
	for _, c := range cols {
		if c == "catalog_id" {
			return true
		}
	}
	return false
}

func newLegacyCatalogMirror(tx *sql.Tx) (*legacyCatalogMirror, error) {
	m := &legacyCatalogMirror{
		newConns:   map[string]bool{},
		adoptConns: map[string][]any{},
		adoptFwds:  map[string][]any{},
	}
	var err error
	if m.folders, err = idSet(tx, `SELECT id FROM folders WHERE catalog_id IS NOT NULL`); err != nil {
		return nil, err
	}
	if m.conns, err = idSet(tx, `SELECT id FROM connections WHERE catalog_id IS NOT NULL`); err != nil {
		return nil, err
	}
	// The user's own forwards on catalog connections count as kept too:
	// shadowing keeps them out of every snapshot an older app sees, so
	// that app can neither change nor delete them, and a snapshot from it
	// that lacks them says nothing about them.
	if m.fwds, err = idSet(tx, `SELECT id FROM port_forwards WHERE remote_uuid IS NOT NULL
		OR connection_id IN (SELECT id FROM connections WHERE catalog_id IS NOT NULL)`); err != nil {
		return nil, err
	}
	m.fullLayouts = map[string]string{}
	if len(m.conns) > 0 && tableExists(tx, "workspaces") {
		all, err := readLayouts(tx, `SELECT id, layout_json FROM workspaces`)
		if err != nil {
			return nil, err
		}
		for id, l := range all {
			if _, changed := StripWorkspaceLayout(l, m.conns); changed {
				m.fullLayouts[id] = l
			}
		}
	}
	return m, nil
}

// hasCatalogData: anything of a catalog here that the legacy mirror keeps.
func (m *legacyCatalogMirror) hasCatalogData(tx *sql.Tx) bool {
	if len(m.folders) > 0 {
		return true
	}
	for t := range catalogTables {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + t).Scan(&n); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// HasCatalogData reports whether this store holds any catalog: an attached
// catalog, its rows, bindings or local layers.
func (d *DB) HasCatalogData() bool {
	for _, q := range []string{
		`SELECT COUNT(*) FROM catalogs`, `SELECT COUNT(*) FROM catalog_bindings`,
		`SELECT COUNT(*) FROM catalog_overrides`, `SELECT COUNT(*) FROM folders WHERE catalog_id IS NOT NULL`,
	} {
		var n int
		if err := d.conn.QueryRow(q).Scan(&n); err == nil && n > 0 {
			return true
		}
	}
	return false
}

func idSet(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string) (map[string]bool, error) {
	rows, err := q.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// clear empties table t the legacy way; done=false leaves it to the normal
// delete-all.
func (m *legacyCatalogMirror) clear(tx *sql.Tx, t string) (bool, error) {
	var q string
	switch {
	case catalogTables[t]:
		return true, nil
	case t == "folders":
		q = `DELETE FROM folders WHERE catalog_id IS NULL`
	case t == "connections":
		q = `DELETE FROM connections WHERE catalog_id IS NULL`
	case t == "port_forwards":
		q = `DELETE FROM port_forwards WHERE remote_uuid IS NULL
			AND connection_id NOT IN (SELECT id FROM connections WHERE catalog_id IS NOT NULL)`
	case t == "credential_refs" || t == "credential_folders":
		// Credentials made for catalog external references never reach an
		// older app (shadowed), so its snapshot says nothing about them.
		q = `DELETE FROM ` + t + ` WHERE NOT (` + catalogCredWhere + `)`
	default:
		return false, nil
	}
	_, err := tx.Exec(q)
	return true, err
}

func (m *legacyCatalogMirror) keepsTable(t string) bool { return catalogTables[t] }

func colIndex(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

// pick copies the named columns' values out of a row (nil where the source
// lacks the column, in which case adopt leaves that column alone).
func pick(cols []string, vals []any, names ...string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		if j := colIndex(cols, n); j >= 0 {
			out[i] = vals[j]
		} else {
			out[i] = missingCol{}
		}
	}
	return out
}

type missingCol struct{}

var adoptConnCols = []string{"favorite", "open_hidden", "password_vault_key", "vnc_password_vault_key"}
var adoptFwdCols = []string{"auto_start", "bookmarks", "browser_mode"}

func (m *legacyCatalogMirror) hook(t string) rowHook {
	switch t {
	case "credential_refs", "credential_folders":
		return func(cols []string, vals []any) bool {
			return !IsCatalogCredential(asString(vals[colIndex(cols, "id")]))
		}
	case "folders":
		return func(cols []string, vals []any) bool {
			id := asString(vals[colIndex(cols, "id")])
			if m.folders[id] {
				m.demoted++
				return false
			}
			if p := colIndex(cols, "parent_id"); p >= 0 && m.folders[asString(vals[p])] {
				vals[p] = nil
				m.rehomed++
			}
			return true
		}
	case "connections":
		return func(cols []string, vals []any) bool {
			id := asString(vals[colIndex(cols, "id")])
			if m.conns[id] {
				m.adoptConns[id] = pick(cols, vals, adoptConnCols...)
				m.demoted++
				return false
			}
			if p := colIndex(cols, "folder_id"); p >= 0 && m.folders[asString(vals[p])] {
				vals[p] = nil
				m.rehomed++
			}
			m.newConns[id] = true
			return true
		}
	case "port_forwards":
		return func(cols []string, vals []any) bool {
			id := asString(vals[colIndex(cols, "id")])
			if m.fwds[id] {
				m.adoptFwds[id] = pick(cols, vals, adoptFwdCols...)
				m.demoted++
				return false
			}
			conn := asString(vals[colIndex(cols, "connection_id")])
			if !m.conns[conn] && !m.newConns[conn] {
				m.droppedFwds++
				return false
			}
			return true
		}
	}
	return nil
}

// finish puts back workspace layouts that came back stripped and
// unchanged, adopts the local columns of the demoted copies and logs what
// the legacy mirror did.
func (m *legacyCatalogMirror) finish(tx *sql.Tx) error {
	if err := restoreWorkspaceLayouts(tx, m.fullLayouts); err != nil {
		return err
	}
	if err := adopt(tx, "connections", adoptConnCols, m.adoptConns); err != nil {
		return err
	}
	if err := adopt(tx, "port_forwards", adoptFwdCols, m.adoptFwds); err != nil {
		return err
	}
	log.Printf("mirror: snapshot from an app without catalog support; kept %d catalog folders, %d connections, %d forwards; skipped %d demoted copies, moved %d local rows out of catalog folders, dropped %d orphaned forwards",
		len(m.folders), len(m.conns), len(m.fwds), m.demoted, m.rehomed, m.droppedFwds)
	return nil
}

func adopt(tx *sql.Tx, table string, cols []string, rows map[string][]any) error {
	for id, vals := range rows {
		for i, c := range cols {
			if _, missing := vals[i].(missingCol); missing {
				continue
			}
			if _, err := tx.Exec(`UPDATE `+table+` SET `+c+` = ? WHERE id = ?`, vals[i], id); err != nil {
				return fmt.Errorf("adopt %s.%s: %w", table, c, err)
			}
		}
	}
	return nil
}

// PrepareStagedStore decides what a restart-time restore stages for the
// snapshot store at path (a temporary copy the caller owns):
//   - a trusted shadow: the file as is; Open takes the catalog rows back;
//   - not trusted, and this machine holds no catalog data: the file as is,
//     with any (stale) shadow tables dropped so Open does not take them -
//     for everyone who never used a catalog that is today's path exactly;
//   - not trusted, and this machine holds catalog data: a merged copy,
//     built the way a live pull builds it (a copy of this store with the
//     snapshot mirrored over it by the legacy rules), so the swap keeps the
//     catalog. On this path alone machine-local settings stay this
//     machine's rather than the file's, as on a live pull.
func (d *DB) PrepareStagedStore(path string, trust ShadowTrust) (string, func(), error) {
	trusted, err := SnapshotIsTrusted(path, trust)
	if err != nil {
		return "", nil, err
	}
	if trusted {
		return path, nil, nil
	}
	if !d.HasCatalogData() {
		probe, err := sql.Open("sqlite", path+"?mode=ro")
		if err != nil {
			return "", nil, err
		}
		has := tableExists(probe, shadowPrefix+"meta")
		probe.Close()
		if has {
			if err := DropShadowTables(path); err != nil {
				return "", nil, err
			}
		}
		return path, nil, nil
	}
	dir, err := os.MkdirTemp("", "ssh-tool-stage-merge-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	merged := filepath.Join(dir, "store.db")
	if _, err := d.conn.Exec(`VACUUM INTO ?`, merged); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("stage: copy live store: %w", err)
	}
	m, err := Open(merged)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	_, merr := m.MirrorFromWith(path, trust)
	cerr := m.Close()
	if merr != nil {
		cleanup()
		return "", nil, merr
	}
	if cerr != nil {
		cleanup()
		return "", nil, cerr
	}
	log.Printf("restore: staged a merged store so this machine's catalog data survives the restart")
	return merged, cleanup, nil
}
