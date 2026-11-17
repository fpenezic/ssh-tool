package main

// "Set it on the folder instead" (record mode, connections).
//
// A writer who gives one catalog connection a credential, one-time code,
// network or jump host often means the whole folder: the colleague with
// fifty connections under "Customer E" should not set the same slot fifty
// times. CatalogFolderOffers says, for each such field the save changes,
// what setting it on the connection's folder would do to the other
// connections there (inherit it now, drop an own value that becomes the
// same, keep a different own value), so the user sees every change before
// saving. CatalogRecordSaveOnFolder then writes the folder record first,
// and clears the value on the connection and on the siblings whose own
// value equals it. Every write is an ordinary record save (If-Match, slot
// creation, conflicts); a clear that fails after the folder succeeded
// leaves a redundant own value, which is still correct, and is reported.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/store"
)

// The settings a folder can carry for its connections and that name a
// slot when they reach the catalog.
var folderOfferFields = []string{"auth_ref", "otp_ref", "network_profile_id", "jump_host"}

// CatalogFolderOffer is what setting one field on the folder would do.
type CatalogFolderOffer struct {
	Field      string `json:"field"`
	FolderID   string `json:"folder_id"`
	FolderName string `json:"folder_name"`
	// Connections other than the edited one, by what happens to them:
	// Gain had no value and now inherit it; Switch inherited the folder's
	// current value and get the new one; Clear held the same value as
	// their own, which is removed; Keep hold a different own value, which
	// stays and wins.
	Gain   []string `json:"gain"`
	Switch []string `json:"switch"`
	Clear  []string `json:"clear"`
	Keep   []string `json:"keep"`
	// SelfOwn: the edited connection held a value of its own before.
	SelfOwn bool `json:"self_own"`
}

func settingsMap(s store.InheritableSettings) map[string]any {
	b, _ := json.Marshal(s)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func withoutSetting(s store.InheritableSettings, key string) store.InheritableSettings {
	m := settingsMap(s)
	delete(m, key)
	b, _ := json.Marshal(m)
	var out store.InheritableSettings
	_ = json.Unmarshal(b, &out)
	return out
}

func withSetting(s store.InheritableSettings, key string, v any) store.InheritableSettings {
	m := settingsMap(s)
	m[key] = v
	b, _ := json.Marshal(m)
	var out store.InheritableSettings
	_ = json.Unmarshal(b, &out)
	return out
}

// catalogSibling is another catalog connection in the folder, with its
// record in the editor's terms.
type catalogSibling struct {
	id, name, raw string
	form          catalog.EditForm
}

func (a *App) catalogSiblings(catID, folderID, exceptID string) ([]catalogSibling, error) {
	rows, err := a.db.Conn().Query(`SELECT id, name, catalog_json FROM connections
		WHERE catalog_id = ? AND folder_id = ? AND id <> ? AND catalog_state <> 'removed' ORDER BY name`, catID, folderID, exceptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catalogSibling
	for rows.Next() {
		var s catalogSibling
		if err := rows.Scan(&s.id, &s.name, &s.raw); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		f, err := catalog.FormOf(a.db, catID, catalog.KindConnections, json.RawMessage(out[i].raw))
		if err != nil {
			return nil, err
		}
		out[i].form = f
	}
	return out, nil
}

// CatalogFolderOffers lists, for a connection's record save, the fields
// that could go on its folder instead and what that would change. Empty
// when there is nothing to offer: not a connection, the folder already
// says the same, or no other connection there would be affected.
func (a *App) CatalogFolderOffers(in catalog.EditInput) ([]CatalogFolderOffer, error) {
	out := []CatalogFolderOffer{}
	if in.Kind != catalog.KindConnections {
		return out, nil
	}
	t, err := a.catalogWriteTarget(in.Kind, in.LocalID)
	if err != nil {
		return nil, err
	}
	fid, fu, fraw, err := a.db.CatalogFolderOf(in.Kind, in.LocalID)
	if err != nil || fu == "" || !catalogWritable(fraw) {
		return out, err
	}
	folderForm, err := catalog.FormOf(a.db, t.cat.ID, catalog.KindFolders, json.RawMessage(fraw))
	if err != nil {
		return nil, err
	}
	sibs, err := a.catalogSiblings(t.cat.ID, fid, in.LocalID)
	if err != nil {
		return nil, err
	}
	loaded, edited, inFolder := settingsMap(in.Loaded.Settings), settingsMap(in.Edited.Settings), settingsMap(folderForm.Settings)
	for _, k := range folderOfferFields {
		v, ok := edited[k]
		if !ok || v == nil || reflect.DeepEqual(v, loaded[k]) || reflect.DeepEqual(v, inFolder[k]) {
			continue
		}
		o := CatalogFolderOffer{Field: k, FolderID: fid, FolderName: folderForm.Name, Gain: []string{}, Switch: []string{}, Clear: []string{}, Keep: []string{},
			SelfOwn: loaded[k] != nil}
		for _, s := range sibs {
			own, has := settingsMap(s.form.Settings)[k]
			switch {
			case !has || own == nil:
				if inFolder[k] == nil {
					o.Gain = append(o.Gain, s.name)
				} else {
					o.Switch = append(o.Switch, s.name)
				}
			case reflect.DeepEqual(own, v):
				o.Clear = append(o.Clear, s.name)
			default:
				o.Keep = append(o.Keep, s.name)
			}
		}
		if len(o.Gain)+len(o.Switch)+len(o.Clear) > 0 {
			out = append(out, o)
		}
	}
	return out, nil
}

// CatalogFolderSaveResult is what a save on the folder answers.
type CatalogFolderSaveResult struct {
	Saved   bool   `json:"saved"`
	Message string `json:"message,omitempty"`
	// Folder: the folder record's save when it could not complete (new
	// slots to name, a conflict, a refusal); the editor shows it as it
	// shows a record save's.
	Folder *CatalogSaveResult `json:"folder,omitempty"`
	// Record: the edited connection's own save (its other changes, and
	// the cleared fields).
	Record *CatalogSaveResult `json:"record,omitempty"`
	// Left: connections whose redundant own value could not be cleared.
	Left []string `json:"left,omitempty"`
}

// CatalogRecordSaveOnFolder saves a connection's record with the given
// fields set on its folder instead: the folder record gets the values,
// the connection and the siblings holding the same own value lose theirs.
func (a *App) CatalogRecordSaveOnFolder(in catalog.EditInput, fields []string) (*CatalogFolderSaveResult, error) {
	if in.Kind != catalog.KindConnections || len(fields) == 0 {
		r, err := a.CatalogRecordSave(in)
		if err != nil {
			return nil, err
		}
		return &CatalogFolderSaveResult{Saved: r.Saved, Message: r.Message, Record: r}, nil
	}
	allowed := map[string]bool{}
	for _, k := range folderOfferFields {
		allowed[k] = true
	}
	t, err := a.catalogWriteTarget(in.Kind, in.LocalID)
	if err != nil {
		return nil, err
	}
	fid, fu, fraw, err := a.db.CatalogFolderOf(in.Kind, in.LocalID)
	if err != nil {
		return nil, err
	}
	if fu == "" || !catalogWritable(fraw) {
		return &CatalogFolderSaveResult{Message: "not saved: you cannot change this folder"}, nil
	}
	folderForm, err := catalog.FormOf(a.db, t.cat.ID, catalog.KindFolders, json.RawMessage(fraw))
	if err != nil {
		return nil, err
	}
	edited := settingsMap(in.Edited.Settings)
	fEdited := folderForm
	conn := in
	var keys []string
	for _, k := range fields {
		if !allowed[k] || edited[k] == nil {
			continue
		}
		keys = append(keys, k)
		fEdited.Settings = withSetting(fEdited.Settings, k, edited[k])
		conn.Edited.Settings = withoutSetting(conn.Edited.Settings, k)
	}
	if len(keys) == 0 {
		return &CatalogFolderSaveResult{Message: "nothing to set on the folder"}, nil
	}
	sort.Strings(keys)

	// The folder first: it is what the other connections inherit.
	fr, err := a.CatalogRecordSave(catalog.EditInput{Kind: catalog.KindFolders, LocalID: fid, Rev: recordRev(fraw),
		Loaded: folderForm, Edited: fEdited, SlotNames: in.SlotNames})
	if err != nil {
		return nil, err
	}
	if !fr.Saved {
		return &CatalogFolderSaveResult{Message: fr.Message, Folder: fr}, nil
	}

	// Then the connection itself (its other changes, the fields cleared).
	res := &CatalogFolderSaveResult{}
	rr, err := a.CatalogRecordSave(conn)
	if err != nil {
		// The folder is saved already: say so instead of a bare error.
		res.Message = "set on the folder, but this connection was not saved: " + err.Error()
		return res, nil
	}
	res.Record = rr
	if !rr.Saved && rr.Message != "nothing changed" {
		res.Message = "set on the folder, but this connection was not saved: " + rr.Message
		return res, nil
	}

	// Siblings holding the same value as their own: now redundant.
	sibs, err := a.catalogSiblings(t.cat.ID, fid, in.LocalID)
	if err != nil {
		res.Saved = true
		res.Message = "set on the folder; the other connections were not checked: " + err.Error()
		return res, nil
	}
	for _, s := range sibs {
		own := settingsMap(s.form.Settings)
		cleared := s.form
		changed := false
		for _, k := range keys {
			if v, ok := own[k]; ok && v != nil && reflect.DeepEqual(v, edited[k]) {
				cleared.Settings = withoutSetting(cleared.Settings, k)
				changed = true
			}
		}
		if !changed {
			continue
		}
		sr, err := a.CatalogRecordSave(catalog.EditInput{Kind: catalog.KindConnections, LocalID: s.id, Rev: recordRev(s.raw),
			Loaded: s.form, Edited: cleared})
		if err != nil || !sr.Saved {
			res.Left = append(res.Left, s.name)
		}
	}
	res.Saved = true
	res.Message = "set on the folder"
	if len(res.Left) > 0 {
		res.Message += "; these still carry the same value of their own: " + strings.Join(res.Left, ", ")
	}
	return res, nil
}
