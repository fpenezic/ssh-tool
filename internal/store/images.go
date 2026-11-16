package store

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// PutImage stores an image blob, content-addressed by MD5. If the same image
// already exists, returns its existing id (no duplicate row). Returns the
// row id either way.
//
// RDM exports tend to repeat the same PNG hundreds of times across many
// connections sharing a customer logo. Hashing on insert avoids bloating
// the DB.
func (d *DB) PutImage(data []byte, mimeType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty image")
	}
	sum := md5.Sum(data)
	hash := hex.EncodeToString(sum[:])

	var existingID string
	err := d.conn.QueryRow("SELECT id FROM images WHERE md5 = ?", hash).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if mimeType == "" {
		mimeType = "image/png"
	}
	id := newID()
	_, err = d.conn.Exec(
		`INSERT INTO images (id, md5, sha256, mime_type, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, hash, sha256Hex(data), mimeType, data, time.Now().Unix(),
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetImage returns (mime, data, true) for the given image id, or
// (_, _, false) if not found.
func (d *DB) GetImage(id string) (string, []byte, bool, error) {
	var mime string
	var data []byte
	err := d.conn.QueryRow("SELECT mime_type, data FROM images WHERE id = ?", id).
		Scan(&mime, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return mime, data, true, nil
}

// ListImageIDs returns every image id in the store paired with a
// usage count (how many folders + connections reference it). Used
// by the icon picker so the user can pick from images already in
// the DB (e.g. RDM-imported logos) instead of re-uploading them.
type ImageSummary struct {
	ID       string `json:"id"`
	MIME     string `json:"mime"`
	UseCount int    `json:"use_count"`
}

func (d *DB) ListImageIDs() ([]ImageSummary, error) {
	rows, err := d.conn.Query(`
		SELECT i.id, i.mime_type,
		       (SELECT COUNT(*) FROM folders f WHERE f.icon_image_id = i.id)
		     + (SELECT COUNT(*) FROM connections c WHERE c.icon_image_id = i.id)
		     + (SELECT COUNT(*) FROM credential_refs cr WHERE cr.icon_image_id = i.id)
		     + (SELECT COUNT(*) FROM credential_folders cf WHERE cf.icon_image_id = i.id)
		  FROM images i
		 ORDER BY i.created_at DESC, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageSummary
	for rows.Next() {
		var s ImageSummary
		if err := rows.Scan(&s.ID, &s.MIME, &s.UseCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetFolderIcon updates the icon_image_id on a folder. Pass empty string to
// clear. Setting an uploaded image clears any built-in icon (they are
// mutually exclusive - one icon source at a time).
func (d *DB) SetFolderIcon(folderID, imageID string) error {
	var v interface{}
	if imageID != "" {
		v = imageID
	}
	_, err := d.conn.Exec("UPDATE folders SET icon_image_id = ?, icon_name = NULL, icon_color = NULL, updated_at = ? WHERE id = ?",
		v, time.Now().Unix(), folderID)
	return err
}

// SetConnectionIcon updates the icon_image_id on a connection. Pass empty
// string to clear. Setting an uploaded image clears any built-in icon.
func (d *DB) SetConnectionIcon(connID, imageID string) error {
	var v interface{}
	if imageID != "" {
		v = imageID
	}
	_, err := d.conn.Exec("UPDATE connections SET icon_image_id = ?, icon_name = NULL, icon_color = NULL, updated_at = ? WHERE id = ?",
		v, time.Now().Unix(), connID)
	return err
}

// SetFolderNamedIcon sets a built-in (lucide) icon + palette colour on a
// folder, clearing any uploaded image. Pass an empty name to clear the
// built-in icon (falls back to the default). color may be empty.
func (d *DB) SetFolderNamedIcon(folderID, name, color string) error {
	var n, c interface{}
	if name != "" {
		n = name
		if color != "" {
			c = color
		}
	}
	_, err := d.conn.Exec("UPDATE folders SET icon_name = ?, icon_color = ?, icon_image_id = NULL, updated_at = ? WHERE id = ?",
		n, c, time.Now().Unix(), folderID)
	return err
}

// SetConnectionNamedIcon sets a built-in icon + palette colour on a
// connection, clearing any uploaded image. Empty name clears it.
func (d *DB) SetConnectionNamedIcon(connID, name, color string) error {
	var n, c interface{}
	if name != "" {
		n = name
		if color != "" {
			c = color
		}
	}
	_, err := d.conn.Exec("UPDATE connections SET icon_name = ?, icon_color = ?, icon_image_id = NULL, updated_at = ? WHERE id = ?",
		n, c, time.Now().Unix(), connID)
	return err
}

// ImageUsage describes an uploaded icon by what already wears it. Uploaded
// images have no name of their own - only an id and an md5 - so the only
// human-meaningful handle is "the icon web-01 uses". The MCP bridge lists them
// this way so a model can reuse one without guessing at a UUID.
type ImageUsage struct {
	ID       string   `json:"id"`
	MIME     string   `json:"mime"`
	UseCount int      `json:"use_count"`
	Examples []string `json:"examples"` // names of rows carrying this icon
}

// ListImageUsage returns every uploaded icon along with up to `limit` names of
// the connections and folders using it, most recently updated first.
// Rows in hidden (connection and folder ids) are not named: the only caller
// is the MCP bridge, which must not learn sensitive ones exist. Sensitivity
// is resolved (overrides, inherited from folders), so the caller decides.
func (d *DB) ListImageUsage(limit int, hidden map[string]bool) ([]ImageUsage, error) {
	if limit <= 0 {
		limit = 3
	}
	imgs, err := d.ListImageIDs()
	if err != nil {
		return nil, err
	}
	out := make([]ImageUsage, 0, len(imgs))
	for _, im := range imgs {
		u := ImageUsage{ID: im.ID, MIME: im.MIME, UseCount: im.UseCount}
		rows, err := d.conn.Query(`
			SELECT id, name FROM (
			    SELECT id, name, updated_at FROM connections WHERE icon_image_id = ?
			    UNION ALL
			    SELECT id, name, updated_at FROM folders WHERE icon_image_id = ?
			) ORDER BY updated_at DESC`, im.ID, im.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, n string
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if hidden[id] {
				u.UseCount-- // a "+N more" must not count it either
				continue
			}
			if len(u.Examples) < limit {
				u.Examples = append(u.Examples, n)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		out = append(out, u)
	}
	return out, nil
}

// FolderIconHint describes the icon a folder inherits VISUALLY from the
// nearest ancestor that carries one.
//
// This exists for the MCP bridge. In practice an icon here is a customer's
// logo, set on the CUSTOMER folder: the subsystem folders below it keep the
// default folder glyph, and connections carry icons of their own. A model
// staging a connection into Work/<customer>/<subsystem> therefore has to look
// UPWARD to find the logo - the connection's siblings say nothing about it.
//
// Nothing is inherited in the data model. This is advice for a model deciding
// what to set explicitly, and the user still approves it in the plan modal.
type FolderIconHint struct {
	IconName    string // built-in icon name on the ancestor, or ""
	IconColor   string // its colour, when set
	IconImageID string // uploaded image on the ancestor, or ""
	FolderPath  string // where the icon was found, so the hint can cite it
	Depth       int    // 0 = this folder itself, 1 = its parent, ...
}

// ConnIconUsage summarises what the connections inside one folder actually
// use, so a model can follow the folder's existing convention instead of
// inventing one.
//
// Two shapes matter and they call for different behaviour:
//   - every connection wears the SAME icon (usually an uploaded customer
//     logo): a new connection there should wear it too.
//   - connections wear DIFFERENT built-in icons that track their role
//     (db-01 -> database, nfs-01 -> hard-drive): the convention is per-role,
//     so the model should pick by role rather than copy any one of them.
//
// Examples carries "connection name -> icon" pairs as evidence for the second
// case; without them a model cannot see that the mapping is role-based.
type ConnIconUsage struct {
	Total     int      // connections in this folder
	WithIcon  int      // how many carry any icon at all
	Unanimous string   // the icon every one of them shares, or ""
	IsImage   bool     // whether Unanimous is an uploaded image id
	Examples  []string // "name -> icon" samples, when they differ
}

// FolderConnIcons maps folder id -> what its OWN connections use. Folders
// whose connections carry no icons at all are absent. Connections in hidden
// are left out (MCP-only caller, see ListImageUsage).
func (d *DB) FolderConnIcons(examples int, hidden map[string]bool) (map[string]ConnIconUsage, error) {
	if examples <= 0 {
		examples = 6
	}
	rows, err := d.conn.Query(`
		SELECT id, folder_id, name,
		       IFNULL(icon_name, ''), IFNULL(icon_color, ''), IFNULL(icon_image_id, '')
		FROM connections
		WHERE folder_id IS NOT NULL
		ORDER BY folder_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		total, withIcon int
		icons           map[string]bool
		isImage         map[string]bool
		samples         []string
	}
	byFolder := map[string]*acc{}
	for rows.Next() {
		var id, fid, name, iname, icolor, image string
		if err := rows.Scan(&id, &fid, &name, &iname, &icolor, &image); err != nil {
			return nil, err
		}
		if hidden[id] {
			continue
		}
		a := byFolder[fid]
		if a == nil {
			a = &acc{icons: map[string]bool{}, isImage: map[string]bool{}}
			byFolder[fid] = a
		}
		a.total++
		// A built-in icon is its name plus colour: "database (mauve)" and a
		// plain "database" are different conventions, and an LLM copying
		// the convention needs both halves.
		icon := iname
		if icon != "" && icolor != "" {
			icon += "/" + icolor
		}
		if image != "" {
			icon = image
		}
		if icon == "" {
			continue
		}
		a.withIcon++
		a.icons[icon] = true
		a.isImage[icon] = image != ""
		if len(a.samples) < examples {
			label := icon
			if image != "" {
				label = "uploaded " + icon
			}
			a.samples = append(a.samples, name+" -> "+label)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]ConnIconUsage{}
	for fid, a := range byFolder {
		if a.withIcon == 0 {
			continue
		}
		u := ConnIconUsage{Total: a.total, WithIcon: a.withIcon}
		// Unanimity requires that every connection carries an icon, not just
		// that the ones which do happen to agree: a folder where half are
		// unset has no settled convention to copy.
		if len(a.icons) == 1 && a.withIcon == a.total {
			for k := range a.icons {
				u.Unanimous = k
				u.IsImage = a.isImage[k]
			}
		} else {
			u.Examples = a.samples
		}
		out[fid] = u
	}
	return out, nil
}

type folderIconRow struct {
	parent string
	name   string
	iname  string
	icolor string
	image  string
}

// FolderIconHints maps folder id -> the icon it or its nearest iconed ancestor
// carries. Folders with no iconed ancestor are absent.
func (d *DB) FolderIconHints() (map[string]FolderIconHint, error) {
	rows, err := d.conn.Query(`
		SELECT id, IFNULL(parent_id, ''), name,
		       IFNULL(icon_name, ''), IFNULL(icon_color, ''), IFNULL(icon_image_id, '')
		FROM folders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all := map[string]folderIconRow{}
	for rows.Next() {
		var id string
		var r folderIconRow
		if err := rows.Scan(&id, &r.parent, &r.name, &r.iname, &r.icolor, &r.image); err != nil {
			return nil, err
		}
		all[id] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The guard counters below bound the walk: a corrupted parent chain that
	// formed a cycle would otherwise hang the bridge rather than fail.
	var path func(id string, guard int) string
	path = func(id string, guard int) string {
		r, ok := all[id]
		if !ok || guard > 32 {
			return ""
		}
		if r.parent == "" {
			return r.name
		}
		if up := path(r.parent, guard+1); up != "" {
			return up + "/" + r.name
		}
		return r.name
	}

	out := map[string]FolderIconHint{}
	for id := range all {
		cur, depth := id, 0
		for depth <= 32 {
			r, ok := all[cur]
			if !ok {
				break
			}
			if r.image != "" || r.iname != "" {
				out[id] = FolderIconHint{
					IconName:    r.iname,
					IconColor:   r.icolor,
					IconImageID: r.image,
					FolderPath:  path(cur, 0),
					Depth:       depth,
				}
				break
			}
			if r.parent == "" {
				break
			}
			cur, depth = r.parent, depth+1
		}
	}
	return out, nil
}

// imageRefTables lists every column that can point at an uploaded image.
// Deleting an image has to clear all of them first: foreign keys are on,
// so a delete with a reference left fails outright.
var imageRefTables = []string{"folders", "connections", "credential_refs", "credential_folders"}

// DeleteImage removes an uploaded icon. Rows still using it fall back to
// their default icon (their icon_image_id is cleared and updated_at bumped,
// so the change syncs like any other icon edit). Returns how many rows
// lost the icon.
func (d *DB) DeleteImage(id string) (int, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	cleared := 0
	for _, t := range imageRefTables {
		res, err := tx.Exec("UPDATE "+t+" SET icon_image_id = NULL, updated_at = ? WHERE icon_image_id = ?", now, id)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		cleared += int(n)
	}
	res, err := tx.Exec("DELETE FROM images WHERE id = ?", id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("image %s not found", id)
	}
	return cleared, tx.Commit()
}

// DeleteUnusedImages removes every uploaded icon that no folder,
// connection or credential uses, and returns how many went.
func (d *DB) DeleteUnusedImages() (int, error) {
	q := "DELETE FROM images WHERE id NOT IN (SELECT icon_image_id FROM folders WHERE icon_image_id IS NOT NULL)" +
		" AND id NOT IN (SELECT icon_image_id FROM connections WHERE icon_image_id IS NOT NULL)" +
		" AND id NOT IN (SELECT icon_image_id FROM credential_refs WHERE icon_image_id IS NOT NULL)" +
		" AND id NOT IN (SELECT icon_image_id FROM credential_folders WHERE icon_image_id IS NOT NULL)"
	res, err := d.conn.Exec(q)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CatalogImagePrefix marks an image row cached from a catalog's image
// store (PROTOCOL 3.6): catimg-<sha256>. Such rows can be fetched again at
// any time, so they stay out of profile snapshots (ShadowSnapshot) and are
// pruned when no catalog record uses them any more.
const CatalogImagePrefix = "catimg-"

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ImageIDBySHA256 returns the local image row holding the bytes with this
// SHA-256 (lowercase hex), or "" when there is none. Rows without the
// hash yet (written before migration 31, or by an older app's mirror) get
// it filled here, once per miss.
func (d *DB) ImageIDBySHA256(hexHash string) (string, error) {
	q := func() (string, error) {
		var id string
		err := d.conn.QueryRow(`SELECT id FROM images WHERE sha256 = ? ORDER BY id LIKE 'catimg-%', created_at LIMIT 1`, hexHash).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return id, err
	}
	id, err := q()
	if id != "" || err != nil {
		return id, err
	}
	n, err := d.backfillImageSHA256()
	if err != nil || n == 0 {
		return "", err
	}
	return q()
}

// backfillImageSHA256 fills the hash of every row that lacks it.
func (d *DB) backfillImageSHA256() (int, error) {
	rows, err := d.conn.Query(`SELECT id, data FROM images WHERE sha256 IS NULL`)
	if err != nil {
		return 0, err
	}
	type row struct{ id, sum string }
	var todo []row
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, row{id, sha256Hex(data)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range todo {
		if _, err := d.conn.Exec(`UPDATE images SET sha256 = ? WHERE id = ?`, r.sum, r.id); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// PutCatalogImage stores bytes fetched from a catalog under
// catimg-<sha256>, or returns the row that already holds the same bytes
// (the user's own upload of the file included). The caller has checked
// the bytes and that they hash to hexHash.
func (d *DB) PutCatalogImage(hexHash, mimeType string, data []byte) (string, error) {
	if id, err := d.ImageIDBySHA256(hexHash); id != "" || err != nil {
		return id, err
	}
	sum := md5.Sum(data)
	m := hex.EncodeToString(sum[:])
	var existing string
	err := d.conn.QueryRow(`SELECT id FROM images WHERE md5 = ?`, m).Scan(&existing)
	if err == nil {
		_, err = d.conn.Exec(`UPDATE images SET sha256 = ? WHERE id = ?`, hexHash, existing)
		return existing, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id := CatalogImagePrefix + hexHash
	_, err = d.conn.Exec(`INSERT INTO images (id, md5, sha256, mime_type, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, m, hexHash, mimeType, data, time.Now().Unix())
	if err != nil {
		return "", err
	}
	return id, nil
}

// PruneCatalogImages deletes cached catalog images (catimg-*) that no row
// uses and whose hash is not in keep (the images current catalog records
// name). Returns how many went.
func (d *DB) PruneCatalogImages(keep map[string]bool) (int, error) {
	rows, err := d.conn.Query(`SELECT id, IFNULL(sha256, '') FROM images WHERE id LIKE 'catimg-%'` + imageUnusedSQL)
	if err != nil {
		return 0, err
	}
	var drop []string
	for rows.Next() {
		var id, sum string
		if err := rows.Scan(&id, &sum); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[sum] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range drop {
		if _, err := d.conn.Exec(`DELETE FROM images WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	return len(drop), nil
}

// imageUnusedSQL narrows an images query to rows no icon column uses.
const imageUnusedSQL = " AND id NOT IN (SELECT icon_image_id FROM folders WHERE icon_image_id IS NOT NULL)" +
	" AND id NOT IN (SELECT icon_image_id FROM connections WHERE icon_image_id IS NOT NULL)" +
	" AND id NOT IN (SELECT icon_image_id FROM credential_refs WHERE icon_image_id IS NOT NULL)" +
	" AND id NOT IN (SELECT icon_image_id FROM credential_folders WHERE icon_image_id IS NOT NULL)"

// ImageIDsBySHA256Tx maps the hash of every image row that has one to its
// id (a user's own upload before a cached catalog copy of the same bytes).
func ImageIDsBySHA256Tx(tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.Query(`SELECT sha256, id FROM images WHERE sha256 IS NOT NULL ORDER BY id LIKE 'catimg-%' DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, id string
		if err := rows.Scan(&h, &id); err != nil {
			return nil, err
		}
		out[h] = id // later rows win: own uploads, oldest first
	}
	return out, rows.Err()
}

// CatalogImageRefs returns the distinct icon_image values ("sha256:...")
// the current catalog records of a catalog name; catalogID "" means every
// catalog.
func (d *DB) CatalogImageRefs(catalogID string) ([]string, error) {
	q := `SELECT DISTINCT v FROM (
		SELECT CASE WHEN json_valid(catalog_json) THEN json_extract(catalog_json, '$.icon_image') END AS v, catalog_id FROM folders WHERE catalog_id IS NOT NULL AND catalog_state <> 'removed'
		UNION ALL
		SELECT CASE WHEN json_valid(catalog_json) THEN json_extract(catalog_json, '$.icon_image') END AS v, catalog_id FROM connections WHERE catalog_id IS NOT NULL AND catalog_state <> 'removed'
	) WHERE v IS NOT NULL AND (? = '' OR catalog_id = ?)`
	rows, err := d.conn.Query(q, catalogID, catalogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
