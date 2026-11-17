package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// Materialisation turns the stored catalog records into what the rest of
// the app reads: settings_json on catalog folders, overrides_json on
// catalog connections, local_port on catalog forwards. Each is
//
//	overlay(translate(catalog settings), the user's local layer)
//
// translate replaces every catalog reference with a local id (slot ->
// binding, catalog connection uuid -> local row id) or, where there is
// none, with a marker (store.CatalogUnboundRef / CatalogMissingRef) that
// every lookup refuses with a typed error - never nil, which would inherit
// something else in its place. The resolver then runs on ordinary rows.
//
// It runs at the end of every sync pass and after any binding, local layer
// or referenced local object changes (Materialise).

// ExternalResolver maps an external-reference slot (a Bitwarden item) to a
// local credential id, or "" when it cannot. Wired by the app (commit for
// external references); nil leaves such slots to manual binding.
type ExternalResolver func(slot store.CatalogSlot) string

// slotTypes: which slot type each reference field needs.
const (
	slotCred = "cred"
	slotNet  = "net"
	slotJump = "jump"
)

// translator holds what one catalog's references resolve against.
type translator struct {
	catID    string
	in       *store.CatalogInputsTx
	external ExternalResolver
}

func (t *translator) slot(v, typ string) string {
	if v == "" {
		return "" // explicitly none, as in the app
	}
	s, ok := t.in.Slots[v]
	if !ok {
		return store.CatalogMissingRef(t.catID, v)
	}
	if s.Type != typ {
		return store.CatalogUnboundRef(t.catID, v)
	}
	if b, ok := t.in.Bindings[v]; ok && b.Kind == typ && t.exists(typ, b.TargetID) {
		return b.TargetID
	}
	if typ == slotCred && s.Kind == "external" && t.external != nil {
		if id := t.external(s); id != "" && t.in.Credentials[id] {
			return id
		}
	}
	return store.CatalogUnboundRef(t.catID, v)
}

func (t *translator) exists(typ, id string) bool {
	switch typ {
	case slotCred:
		return t.in.Credentials[id]
	case slotNet:
		return t.in.Profiles[id]
	case slotJump:
		// A connection of the user's own, or an inventory host reference
		// (checked when it is dialled, like any jump reference).
		return t.in.Connections[id] || strings.HasPrefix(id, resolver.DynRefPrefix)
	}
	return false
}

func (t *translator) conn(uuid string) string {
	if id, ok := t.in.ConnIDs[uuid]; ok {
		return id
	}
	return store.CatalogMissingRef(t.catID, uuid)
}

// keys removed from catalog settings before use: broadcast_group_id is a
// local concept; tunnel_autostart is a suggestion that must not start
// anything until the user enables it in their own layer (PROTOCOL 3.4).
var droppedKeys = []string{"broadcast_group_id", "tunnel_autostart"}

var credFields = []string{"auth_ref", "otp_ref", "kafka_credential_ref", "db_credential_ref"}

// translate turns a catalog settings object into InheritableSettings with
// local ids. Keys this build does not know are dropped here (they stay in
// the stored record for later writes).
func (t *translator) translate(raw json.RawMessage) (store.InheritableSettings, error) {
	var out store.InheritableSettings
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out, fmt.Errorf("catalog settings: %w", err)
	}
	for _, k := range droppedKeys {
		delete(m, k)
	}
	for _, k := range credFields {
		if v, ok := m[k].(string); ok {
			m[k] = t.slot(v, slotCred)
		}
	}
	if v, ok := m["network_profile_id"].(string); ok {
		m["network_profile_id"] = t.slot(v, slotNet)
	}
	if targets, ok := m["db_targets"].([]any); ok {
		for _, x := range targets {
			if tg, ok := x.(map[string]any); ok {
				if v, ok := tg["credential_ref"].(string); ok {
					tg["credential_ref"] = t.slot(v, slotCred)
				}
			}
		}
	}
	if jh, ok := m["jump_host"].(map[string]any); ok {
		if jh["kind"] == "chain" {
			hop, nested := jh["chain"].(map[string]any)
			if !nested {
				// The legacy flat shape: the hop's fields beside "kind".
				hop = map[string]any{}
				for k, v := range jh {
					if k != "kind" {
						hop[k] = v
					}
				}
			}
			t.hop(hop)
			m["jump_host"] = map[string]any{"kind": "chain", "chain": hop}
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("catalog settings: %w", err)
	}
	return out, nil
}

func (t *translator) hop(h map[string]any) {
	for depth := 0; h != nil && depth < 32; depth++ {
		delete(h, "name") // runtime-only in the app
		if slot, ok := h["jump_slot"].(string); ok {
			delete(h, "jump_slot")
			h["connection_id"] = t.slot(slot, slotJump)
		} else if id, ok := h["connection_id"].(string); ok {
			if id == "" {
				h["connection_id"] = ""
			} else {
				h["connection_id"] = t.conn(id)
			}
		}
		if v, ok := h["auth_ref"].(string); ok {
			h["auth_ref"] = t.slot(v, slotCred)
		}
		next, _ := h["via"].(map[string]any)
		h = next
	}
}

// Overlay lays the user's local layer over the translated catalog value:
// a field the layer sets replaces the catalog's (explicit "" and false
// included), maps merge key by key, database targets merge like folder
// inheritance (the user's own added, an entry with a catalog id adjusts
// it), the jump chain is replaced whole. Absent from the layer means the
// catalog's value ("reset to catalog" deletes the key).
func Overlay(base, layer store.InheritableSettings) store.InheritableSettings {
	out := base
	ov := reflect.ValueOf(&out).Elem()
	lv := reflect.ValueOf(layer)
	for i := 0; i < lv.NumField(); i++ {
		f := lv.Field(i)
		switch f.Kind() {
		case reflect.Ptr:
			if !f.IsNil() {
				ov.Field(i).Set(f)
			}
		case reflect.Map:
			if f.Len() == 0 {
				continue
			}
			dst := reflect.MakeMap(f.Type())
			if b := ov.Field(i); !b.IsNil() {
				for _, k := range b.MapKeys() {
					dst.SetMapIndex(k, b.MapIndex(k))
				}
			}
			for _, k := range f.MapKeys() {
				dst.SetMapIndex(k, f.MapIndex(k))
			}
			ov.Field(i).Set(dst)
		case reflect.Slice:
			if f.Len() == 0 {
				continue
			}
			if f.Type() == reflect.TypeOf([]store.DbTarget(nil)) {
				ov.Field(i).Set(reflect.ValueOf(resolver.MergeDbTargets(base.DbTargets, layer.DbTargets)))
			} else {
				ov.Field(i).Set(f)
			}
		}
	}
	return out
}

// forwardLayer is a catalog forward's local layer.
type forwardLayer struct {
	LocalPort *uint16 `json:"local_port,omitempty"`
}

// Materialise rewrites the materialised values of one catalog in its own
// writer transaction.
func Materialise(db *store.DB, catalogID string, external ExternalResolver) error {
	return db.WithCatalogWriter(func(tx *sql.Tx) error {
		return materialiseTx(db, tx, catalogID, external)
	})
}

func materialiseTx(db *store.DB, tx *sql.Tx, catalogID string, external ExternalResolver) error {
	in, err := db.CatalogInputs(tx, catalogID)
	if err != nil {
		return err
	}
	t := &translator{catID: catalogID, in: in, external: external}
	imgs, err := store.ImageIDsBySHA256Tx(tx)
	if err != nil {
		return err
	}
	rows, err := db.CatalogRowsRawTx(tx, catalogID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		layerJSON := in.Overrides[r.RemoteUUID].JSON
		switch r.Kind {
		case "folders", "connections":
			var rec struct {
				Settings json.RawMessage `json:"settings"`
				Access   string          `json:"access"`
			}
			if err := json.Unmarshal([]byte(r.RawJSON), &rec); err != nil {
				return fmt.Errorf("catalog %s %s: %w", r.Kind, r.RemoteUUID, err)
			}
			base, err := t.translate(rec.Settings)
			if err != nil {
				return fmt.Errorf("catalog %s %s: %w", r.Kind, r.RemoteUUID, err)
			}
			// No local layer on a skeleton folder: it is not the user's
			// to adjust.
			if layerJSON != "" && rec.Access != "skeleton" {
				var layer store.InheritableSettings
				if err := json.Unmarshal([]byte(layerJSON), &layer); err == nil {
					base = Overlay(base, layer)
				}
			}
			// The same encoding UpdateConnection / UpdateFolder write, so
			// a full-row rewrite of a catalog row compares equal and the
			// guard lets the user's local columns through.
			b, err := json.Marshal(base)
			if err != nil {
				return err
			}
			if string(b) != r.Current {
				if err := db.SetCatalogMaterialTx(tx, r.Kind, r.ID, string(b), nil); err != nil {
					return err
				}
			}
			// Record fields: the record's, unless the local layer's
			// record part keeps the user's own.
			want := recordCols(r.RawJSON, imgs)
			if layerJSON != "" && rec.Access != "skeleton" {
				overlayRecordCols(&want, layerJSON)
			}
			if r.Kind == "folders" {
				want.Notes, want.Tags = "", nil
			}
			if !sameRecordCols(want, r.Cols) {
				if err := db.SetCatalogRecordColsTx(tx, r.Kind, r.ID, want); err != nil {
					return err
				}
			}
		case "forwards":
			var rec struct {
				LocalPort *uint16 `json:"local_port"`
			}
			_ = json.Unmarshal([]byte(r.RawJSON), &rec)
			port := rec.LocalPort
			if layerJSON != "" {
				var l forwardLayer
				if json.Unmarshal([]byte(layerJSON), &l) == nil && l.LocalPort != nil {
					port = l.LocalPort
				}
			}
			var cur *uint16
			if r.LocalPort != nil {
				v := uint16(*r.LocalPort)
				cur = &v
			}
			if !sameU16(cur, port) {
				if err := db.SetCatalogMaterialTx(tx, r.Kind, r.ID, "", port); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func sameU16(a, b *uint16) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// BaseSettings is a catalog record's settings translated to local ids,
// without the user's layer: what the editor shows as the catalog's value
// and what an edit is compared with.
func BaseSettings(db *store.DB, catalogID string, settings json.RawMessage) (store.InheritableSettings, error) {
	var out store.InheritableSettings
	err := db.WithTx(func(tx *sql.Tx) error {
		in, err := db.CatalogInputs(tx, catalogID)
		if err != nil {
			return err
		}
		t := &translator{catID: catalogID, in: in, external: func(s store.CatalogSlot) string {
			return store.CatalogCredentialID(catalogID, s.UUID)
		}}
		out, err = t.translate(settings)
		return err
	})
	return out, err
}

// LayerFromEdit turns an edit of a catalog record's settings into its local
// layer: every field whose edited value differs from the catalog's. Maps
// keep the keys that differ or are new; database targets keep the entries
// that are new or differ (an entry with a catalog id adjusts that one).
// Clearing a field the catalog sets cannot be expressed as a layer (absent
// means "the catalog's value"); such a field keeps the catalog's value.
func LayerFromEdit(base, edited store.InheritableSettings) store.InheritableSettings {
	var layer store.InheritableSettings
	lv := reflect.ValueOf(&layer).Elem()
	bv, ev := reflect.ValueOf(base), reflect.ValueOf(edited)
	for i := 0; i < ev.NumField(); i++ {
		e, b := ev.Field(i), bv.Field(i)
		switch e.Kind() {
		case reflect.Ptr:
			if !e.IsNil() && !reflect.DeepEqual(e.Interface(), b.Interface()) {
				lv.Field(i).Set(e)
			}
		case reflect.Map:
			if e.Len() == 0 {
				continue
			}
			m := reflect.MakeMap(e.Type())
			for _, k := range e.MapKeys() {
				bvv := b.MapIndex(k)
				if !bvv.IsValid() || !reflect.DeepEqual(bvv.Interface(), e.MapIndex(k).Interface()) {
					m.SetMapIndex(k, e.MapIndex(k))
				}
			}
			if m.Len() > 0 {
				lv.Field(i).Set(m)
			}
		case reflect.Slice:
			if e.Type() != reflect.TypeOf([]store.DbTarget(nil)) {
				if e.Len() > 0 && !reflect.DeepEqual(e.Interface(), b.Interface()) {
					lv.Field(i).Set(e)
				}
				continue
			}
			byID := map[string]store.DbTarget{}
			for _, t := range base.DbTargets {
				byID[t.ID] = t
			}
			var keep []store.DbTarget
			for _, t := range edited.DbTargets {
				bt, ok := byID[t.ID]
				if ok && reflect.DeepEqual(bt, t) {
					continue
				}
				if ok {
					t.Override = true
				}
				keep = append(keep, t)
			}
			layer.DbTargets = keep
		}
	}
	return layer
}

// recordCols reads a record's notes, tags and icon. An icon image
// points at the local row holding its bytes (imgs: sha256 -> image id);
// one not fetched yet shows the default icon until the fetch after the
// sync pass (FetchImages) brings it.
func recordCols(raw string, imgs map[string]string) store.RecordCols {
	var rec struct {
		Notes     string   `json:"notes"`
		Tags      []string `json:"tags"`
		IconName  *string  `json:"icon_name"`
		IconColor *string  `json:"icon_color"`
		IconImage *string  `json:"icon_image"`
	}
	_ = json.Unmarshal([]byte(raw), &rec)
	c := store.RecordCols{Notes: rec.Notes, Tags: rec.Tags, IconName: rec.IconName, IconColor: rec.IconColor}
	if rec.IconImage != nil {
		if h, ok := ImageHash(*rec.IconImage); ok {
			if id := imgs[h]; id != "" {
				c.IconImageID, c.IconName, c.IconColor = &id, nil, nil
			}
		}
	}
	return c
}

// overlayRecordCols applies the local layer's record part: a present
// field replaces the record's. An icon of the user's own (built-in or an
// image) replaces the record's icon as a whole.
func overlayRecordCols(c *store.RecordCols, layerJSON string) {
	var l map[string]json.RawMessage
	if json.Unmarshal([]byte(layerJSON), &l) != nil {
		return
	}
	raw, ok := l[RecordLayerKey]
	if !ok {
		return
	}
	var r struct {
		Notes       *string   `json:"notes"`
		Tags        *[]string `json:"tags"`
		IconName    *string   `json:"icon_name"`
		IconColor   *string   `json:"icon_color"`
		IconImageID *string   `json:"icon_image_id"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	if r.Notes != nil {
		c.Notes = *r.Notes
	}
	if r.Tags != nil {
		c.Tags = *r.Tags
	}
	if r.IconImageID != nil && *r.IconImageID != "" {
		c.IconImageID, c.IconName, c.IconColor = r.IconImageID, nil, nil
	} else if r.IconName != nil {
		// A built-in icon of the user's own replaces the record's image too.
		c.IconName, c.IconColor, c.IconImageID = r.IconName, r.IconColor, nil
	}
}

func sameRecordCols(a, b store.RecordCols) bool {
	eq := func(x, y *string) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
	if a.Notes != b.Notes || !eq(a.IconName, b.IconName) || !eq(a.IconColor, b.IconColor) || !eq(a.IconImageID, b.IconImageID) {
		return false
	}
	if len(a.Tags) != len(b.Tags) {
		return false
	}
	for i := range a.Tags {
		if a.Tags[i] != b.Tags[i] {
			return false
		}
	}
	return true
}
