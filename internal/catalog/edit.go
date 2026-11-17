package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"ssh-tool/internal/store"
)

// Editing a catalog record from the app ("record mode", writers only).
//
// The editor works in the same local terms as for the user's own rows: a
// credential is one of the user's credentials (or a marker for a slot
// they have not bound), a jump host is a connection of theirs. PlanEdit
// turns the fields the user changed back into catalog values with the
// publish translator: a credential becomes the slot in scope already
// bound to it (or naming the same external item), else a new slot the
// user names before it is created; a marker becomes its slot again; a
// catalog connection becomes its uuid. Only changed fields are sent, as
// an RFC 7396 merge patch, so keys this app does not know survive.
//
// Conflicts are judged per field: the form remembers the values it was
// loaded with; when the record changed on the server meanwhile, every
// changed field is listed with its value then, the user's value and the
// server's current one, and "both changed" where the server moved too.

// EditForm is a record as the editor shows and edits it, in local terms.
// Which fields apply depends on the kind.
type EditForm struct {
	Name      string   `json:"name"`
	Hostname  string   `json:"hostname"`
	Protocol  string   `json:"protocol"`
	Tags      []string `json:"tags"`
	Notes     string   `json:"notes"`
	IconName  *string  `json:"icon_name"`
	IconColor *string  `json:"icon_color"`
	// IconImage is the record's "sha256:<hex>"; IconImageID the local
	// image row with those bytes (for showing it, and what the editor
	// sets; ResolveIconImage turns it into IconImage before a save).
	IconImage   *string                   `json:"icon_image"`
	IconImageID string                    `json:"icon_image_id,omitempty"`
	DocMD       string                    `json:"doc_md"`
	Settings    store.InheritableSettings `json:"settings"`
	// Forwards.
	Kind        string  `json:"kind"`
	LocalAddr   *string `json:"local_addr"`
	LocalPort   *uint16 `json:"local_port"`
	RemoteHost  *string `json:"remote_host"`
	RemotePort  *uint16 `json:"remote_port"`
	AutoStart   bool    `json:"auto_start"`
	Description string  `json:"description"`
}

// FormOf reads a stored record into the editor's terms.
func FormOf(db *store.DB, catalogID, kind string, raw json.RawMessage) (EditForm, error) {
	var rec struct {
		EditForm
		Settings json.RawMessage `json:"settings"`
		DocMD    *string         `json:"doc_md"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return EditForm{}, fmt.Errorf("catalog record: %w", err)
	}
	f := rec.EditForm
	if rec.DocMD != nil {
		f.DocMD = *rec.DocMD
	}
	if kind != KindForwards {
		s, err := BaseSettings(db, catalogID, rec.Settings)
		if err != nil {
			return f, err
		}
		f.Settings = s
	}
	if f.Tags == nil {
		f.Tags = []string{}
	}
	f.IconImageID = ""
	if f.IconImage != nil {
		if h, ok := ImageHash(*f.IconImage); ok {
			f.IconImageID, _ = db.ImageIDBySHA256(h)
		}
	}
	return f, nil
}

// ResolveIconImage sets the edited form's IconImage from the local image
// the editor picked (IconImageID), when that differs from the loaded one:
// the image's address, the built-in icon fields cleared (the record holds
// one icon source). Returns the image bytes when the record gets a new
// image, for the upload before the patch.
func ResolveIconImage(db *store.DB, in *EditInput) ([]byte, error) {
	if in.Edited.IconImageID == in.Loaded.IconImageID {
		in.Edited.IconImage = in.Loaded.IconImage
		return nil, nil
	}
	if in.Edited.IconImageID == "" {
		in.Edited.IconImage = nil
		return nil, nil
	}
	_, data, ok, err := db.GetImage(in.Edited.IconImageID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("the picked image is no longer on this machine")
	}
	ref := ImageRef(data)
	in.Edited.IconImage, in.Edited.IconName, in.Edited.IconColor = &ref, nil, nil
	return data, nil
}

// fieldsOf flattens a form into the fields a patch can carry, settings
// keys as "settings.<key>".
func fieldsOf(kind string, f EditForm) map[string]any {
	m := map[string]any{}
	var names []string
	switch kind {
	case KindFolders:
		names = []string{"name", "icon_name", "icon_color", "icon_image", "doc_md"}
	case KindConnections:
		names = []string{"name", "hostname", "protocol", "tags", "notes", "icon_name", "icon_color", "icon_image", "doc_md"}
	case KindForwards:
		names = []string{"kind", "local_addr", "local_port", "remote_host", "remote_port", "auto_start", "description"}
	}
	all := map[string]any{}
	b, _ := json.Marshal(f)
	_ = json.Unmarshal(b, &all)
	for _, n := range names {
		m[n] = all[n]
	}
	if kind != KindForwards {
		for k, v := range toMap(f.Settings) {
			m["settings."+k] = v
		}
	}
	return m
}

// EditInput is one save from the editor.
type EditInput struct {
	Kind    string `json:"kind"`
	LocalID string `json:"local_id"`
	// Rev the form was loaded at, and the values it showed then.
	Rev    int64    `json:"rev"`
	Loaded EditForm `json:"loaded"`
	Edited EditForm `json:"edited"`
	// After a conflict: re-apply onto this rev, and for each field both
	// sides changed, whose value wins ("mine" or "theirs").
	AcceptRev int64             `json:"accept_rev"`
	Resolve   map[string]string `json:"resolve"`
	// Names for slots the edit needs (by PlanSlot.Key).
	SlotNames map[string]string `json:"slot_names"`
}

// FieldChange is one changed field of a conflict.
type FieldChange struct {
	Field  string `json:"field"`
	Base   string `json:"base"`
	Mine   string `json:"mine"`
	Theirs string `json:"theirs"`
	Both   bool   `json:"both"`
}

// EditPlan is what a save does, or why it cannot yet.
type EditPlan struct {
	Patch    map[string]any         `json:"-"`
	NewSlots []PublishSlot          `json:"-"`
	Bindings []store.CatalogBinding `json:"-"`
	// The user has to answer first:
	NeedSlots   []PlanSlot    `json:"need_slots,omitempty"`
	Conflicts   []FieldChange `json:"conflicts,omitempty"`
	ConflictRev int64         `json:"conflict_rev,omitempty"`
	Refusals    []string      `json:"refusals,omitempty"`
	Changed     []string      `json:"changed"`
}

// RecordScope is where a record sits: the folder whose slots it may use
// (its own folder; a folder's is itself) and that folder's local row.
type RecordScope struct {
	FolderUUID    string
	FolderLocalID string
}

// PlanEdit compares the form with the record now stored (current at
// currentRev) and builds the patch.
func PlanEdit(db *store.DB, cat *store.Catalog, scope RecordScope, in EditInput, current json.RawMessage, currentRev int64) (*EditPlan, error) {
	loaded, edited := fieldsOf(in.Kind, in.Loaded), fieldsOf(in.Kind, in.Edited)
	var changed []string
	for k := range union(loaded, edited) {
		if !reflect.DeepEqual(loaded[k], edited[k]) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	plan := &EditPlan{Changed: changed}
	if len(changed) == 0 {
		return plan, nil
	}
	if in.Kind == KindConnections && strings.EqualFold(strings.TrimSpace(in.Edited.Protocol), "local") {
		plan.Refusals = append(plan.Refusals, "a local shell connection cannot be in a catalog (it would run on colleagues' machines)")
		return plan, nil
	}

	if currentRev != in.Rev {
		cf, err := FormOf(db, cat.ID, in.Kind, current)
		if err != nil {
			return nil, err
		}
		theirs := fieldsOf(in.Kind, cf)
		if in.AcceptRev != currentRev {
			plan.ConflictRev = currentRev
			for _, k := range changed {
				plan.Conflicts = append(plan.Conflicts, FieldChange{Field: k, Base: display(loaded[k]), Mine: display(edited[k]),
					Theirs: display(theirs[k]), Both: !reflect.DeepEqual(theirs[k], loaded[k])})
			}
			return plan, nil
		}
		var keep []string
		for _, k := range changed {
			both := !reflect.DeepEqual(theirs[k], loaded[k])
			if both && in.Resolve[k] == "theirs" {
				continue
			}
			keep = append(keep, k)
		}
		changed = keep
		plan.Changed = keep
		if len(changed) == 0 {
			return plan, nil
		}
	}

	patch := map[string]any{}
	settings := map[string]any{} // changed settings keys, local values
	var removed []string
	for _, k := range changed {
		if sk, ok := strings.CutPrefix(k, "settings."); ok {
			if v, present := edited[k]; present && v != nil {
				settings[sk] = v
			} else {
				removed = append(removed, sk)
			}
			continue
		}
		patch[k] = edited[k]
	}
	if len(settings) > 0 || len(removed) > 0 {
		sp := map[string]any{}
		if len(settings) > 0 {
			catVals, err := reverseSettings(db, cat, scope, in, settings, plan)
			if err != nil {
				return nil, err
			}
			// An object value (env_vars, jump_host) is patched member by
			// member: a merge patch keeps members it does not name, so a
			// removed one needs its null (RFC 7396).
			var cur struct {
				Settings map[string]any `json:"settings"`
			}
			_ = json.Unmarshal(current, &cur)
			for k, v := range catVals {
				nm, nIsMap := v.(map[string]any)
				om, oIsMap := cur.Settings[k].(map[string]any)
				if nIsMap && oIsMap {
					sp[k] = MergePatch(om, nm)
					continue
				}
				sp[k] = v
			}
		}
		for _, k := range removed {
			sp[k] = nil
		}
		patch["settings"] = sp
	}
	plan.Patch = patch
	return plan, nil
}

func union(a, b map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// reverseSettings turns changed settings values from local ids into
// catalog references, collecting the slots that must be created.
func reverseSettings(db *store.DB, cat *store.Catalog, scope RecordScope, in EditInput, vals map[string]any, plan *EditPlan) (map[string]any, error) {
	c := &pubCtx{db: db, strict: true, opts: PublishOptions{CatalogID: cat.ID, RootID: scope.FolderLocalID, TargetUUID: scope.FolderUUID, SlotNames: in.SlotNames,
		SendLocalCreds: true, SendNetworks: true},
		plan:    &PublishPlan{Layers: map[string]PlanLayer{}, catalogID: cat.ID},
		folders: map[string]store.Folder{}, children: map[string][]string{}, conns: map[string]store.Connection{},
		byFolder: map[string][]store.Connection{}, fwds: map[string][]store.PortForward{},
		creds: map[string]store.CredentialRef{}, profiles: map[string]store.NetworkProfile{},
		inside: map[string]string{}, slots: map[string]*PlanSlot{}, slotUse: map[string]map[string]bool{},
		bindings: map[string]store.CatalogBinding{}, outside: map[string]*OutsideJump{},
		passthrough: map[string]passRef{}, inScope: map[string]bool{}, rootUUID: scope.FolderUUID}
	if err := c.load(); err != nil {
		return nil, err
	}
	if err := c.target(cat); err != nil {
		return nil, err
	}
	for _, s := range c.scope {
		c.inScope[s.UUID] = true
	}
	all, err := db.ListCatalogSlots(cat.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		ref := passRef{UUID: s.UUID, Slot: true}
		c.passthrough[store.CatalogUnboundRef(cat.ID, s.UUID)] = ref
		c.passthrough[store.CatalogMissingRef(cat.ID, s.UUID)] = ref
		c.passthrough[store.CatalogCredentialID(cat.ID, s.UUID)] = ref
	}
	err = db.WithTx(func(tx *sql.Tx) error {
		rows, err := db.CatalogRowsRawTx(tx, cat.ID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Kind == KindConnections {
				// A jump to a catalog connection is its uuid; so is a
				// missing-reference marker naming one.
				c.passthrough[r.ID] = passRef{UUID: r.RemoteUUID}
				c.passthrough[store.CatalogMissingRef(cat.ID, r.RemoteUUID)] = passRef{UUID: r.RemoteUUID}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var s store.InheritableSettings
	b, _ := json.Marshal(vals)
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	raw, _, err := c.settings(s, in.Kind, "edit", in.Edited.Name, in.Kind == KindFolders)
	if err != nil {
		return nil, err
	}
	for _, r := range c.plan.Refusals {
		plan.Refusals = append(plan.Refusals, r.Reason)
	}
	c.finish()
	for _, ps := range c.plan.Slots {
		plan.Bindings = append(plan.Bindings, store.CatalogBinding{SlotUUID: ps.UUID, Kind: ps.Type, TargetID: ps.BindTarget})
		if ps.New {
			if _, named := in.SlotNames[ps.Key]; !named {
				plan.NeedSlots = append(plan.NeedSlots, ps)
			}
		}
	}
	plan.NewSlots = c.plan.Request.Slots
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	// Only the changed keys (the translator copies what it was given).
	for k := range out {
		if _, ok := vals[k]; !ok {
			delete(out, k)
		}
	}
	return out, nil
}
