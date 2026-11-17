package main

// The record part of a catalog row's local layer ("_record"): notes,
// tags and icons kept for this user over the catalog record's own. A
// publish writes it for what it did not send (and for an uploaded icon
// image, which no catalog carries yet); setting an icon on a catalog row
// writes it too. Materialise applies it to the row's columns. The layer
// writers that rebuild the settings part keep it.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"ssh-tool/internal/catalog"
)

// catalogLayerJSON is a catalog row's stored local layer ("" for none).
func (a *App) catalogLayerJSON(catID, remoteUUID string) string {
	var j string
	err := a.db.Conn().QueryRow(`SELECT json FROM catalog_overrides WHERE catalog_id = ? AND remote_uuid = ?`, catID, remoteUUID).Scan(&j)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	return j
}

// keepRecordLayer returns newJSON (a settings layer) with the stored
// layer's record part carried over, unless newJSON sets its own.
func (a *App) keepRecordLayer(catID, remoteUUID, newJSON string) string {
	var old map[string]json.RawMessage
	if json.Unmarshal([]byte(a.catalogLayerJSON(catID, remoteUUID)), &old) != nil {
		return newJSON
	}
	rec, ok := old[catalog.RecordLayerKey]
	if !ok {
		return newJSON
	}
	m := map[string]json.RawMessage{}
	if newJSON != "" {
		if json.Unmarshal([]byte(newJSON), &m) != nil {
			return newJSON
		}
	}
	if _, has := m[catalog.RecordLayerKey]; has {
		return newJSON
	}
	m[catalog.RecordLayerKey] = rec
	b, _ := json.Marshal(m)
	return string(b)
}

// layerMu serialises read-modify-write of catalog override layers (record
// saves, icon uploads, resets), so two writers do not drop each other's
// change.
var layerMu sync.Mutex

// updateRecordLayer changes a catalog row's record part and
// materialises. Not a catalog row: handled is false.
func (a *App) updateRecordLayer(kind, id string, mut func(rec map[string]any)) (handled bool, err error) {
	catID, uuid, _, raw, err := a.catalogRow(kind, id)
	if err != nil || catID == "" || uuid == "" {
		return false, nil
	}
	if kind == "folders" {
		var r struct {
			Access string `json:"access"`
		}
		_ = json.Unmarshal([]byte(raw), &r)
		if r.Access == "skeleton" {
			return true, errors.New("this folder is only an outline of a folder you cannot open; it takes no local settings")
		}
	}
	layerMu.Lock()
	layer := map[string]any{}
	if j := a.catalogLayerJSON(catID, uuid); j != "" {
		_ = json.Unmarshal([]byte(j), &layer)
	}
	rec, _ := layer[catalog.RecordLayerKey].(map[string]any)
	if rec == nil {
		rec = map[string]any{}
	}
	mut(rec)
	if len(rec) == 0 {
		delete(layer, catalog.RecordLayerKey)
	} else {
		layer[catalog.RecordLayerKey] = rec
	}
	b, _ := json.Marshal(layer)
	err = a.db.SetCatalogOverride(catID, uuid, kind, string(b))
	layerMu.Unlock()
	if err != nil {
		return true, err
	}
	a.rematerialiseCatalogs()
	return true, nil
}

// setCatalogIcon puts an icon of the user's own on a catalog row (an
// uploaded image, or a built-in name and colour; all empty goes back to
// the catalog's icon).
func (a *App) setCatalogIcon(kind, id, imageID, name, color string) (bool, error) {
	return a.updateRecordLayer(kind, id, func(rec map[string]any) {
		delete(rec, "icon_image_id")
		delete(rec, "icon_name")
		delete(rec, "icon_color")
		delete(rec, catalog.IconPendingKey)
		switch {
		case imageID != "":
			rec["icon_image_id"] = imageID
		case name != "":
			rec["icon_name"] = name
			if color != "" {
				rec["icon_color"] = color
			}
		}
	})
}

// CatalogResetRecordField drops one field ("notes", "tags" or "icon")
// from a catalog row's record part: the row follows the catalog again.
func (a *App) CatalogResetRecordField(kind, id, field string) error {
	_, err := a.updateRecordLayer(kind, id, func(rec map[string]any) {
		if field == "icon" {
			delete(rec, catalog.IconPendingKey)
			delete(rec, "icon_image_id")
			delete(rec, "icon_name")
			delete(rec, "icon_color")
			return
		}
		delete(rec, field)
	})
	return err
}

// isCatalogRecordFolder: a catalog folder with a record (not a catalog's
// root, which is the user's own row).
func (a *App) isCatalogRecordFolder(id string) bool {
	f, err := a.db.GetFolder(id)
	return err == nil && f != nil && f.CatalogID != nil && f.RemoteUUID != nil
}
