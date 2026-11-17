package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/store"
)

// Editing catalog records from the app (writers): record mode in the
// editor, create / move / delete in the tree, slot management. Every
// write goes to the server first; the local rows change only with the
// server's answer, applied as a feed item would be. No offline queue:
// when the server cannot be reached, nothing changes. Desktop only; MCP
// has no catalog write tool.

// catalogWritable reports the writer's access from a folder record.
func catalogWritable(raw string) bool {
	var r struct {
		Access string `json:"access"`
	}
	_ = json.Unmarshal([]byte(raw), &r)
	return r.Access == "write" || r.Access == "manage"
}

func recordRev(raw string) int64 {
	var r struct {
		Rev int64 `json:"rev"`
	}
	_ = json.Unmarshal([]byte(raw), &r)
	return r.Rev
}

// catalogWriteTarget is a catalog row a writer acts on.
type catalogWriteTarget struct {
	cat    *store.Catalog
	client *catalog.Client
	uuid   string
	raw    string
	rev    int64
	scope  catalog.RecordScope
}

func (a *App) catalogWriteGuard(catalogID string) (*store.Catalog, *catalog.Client, error) {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return nil, nil, fmt.Errorf("catalog records are edited on a desktop")
	}
	if err := a.catalogPublishGuard(catalogID); err != nil {
		return nil, nil, err
	}
	cat, err := a.db.GetCatalog(catalogID)
	if err != nil {
		return nil, nil, err
	}
	c, err := a.catalogClient(cat)
	if err != nil {
		return nil, nil, err
	}
	return cat, c, nil
}

func (a *App) catalogWriteTarget(kind, id string) (*catalogWriteTarget, error) {
	catID, u, state, raw, err := a.catalogRow(kind, id)
	if err != nil {
		return nil, err
	}
	if state == "removed" {
		return nil, fmt.Errorf("this record left the catalog")
	}
	cat, c, err := a.catalogWriteGuard(catID)
	if err != nil {
		return nil, err
	}
	fid, fu, fraw, err := a.db.CatalogFolderOf(kind, id)
	if err != nil {
		return nil, err
	}
	if !catalogWritable(fraw) {
		return nil, fmt.Errorf("you can read this catalog folder but not change it")
	}
	return &catalogWriteTarget{cat: cat, client: c, uuid: u, raw: raw, rev: recordRev(raw),
		scope: catalog.RecordScope{FolderUUID: fu, FolderLocalID: fid}}, nil
}

func (a *App) catalogApply(cat *store.Catalog, items []catalog.Item) error {
	rt := a.catRT()
	rt.apply.Lock()
	defer rt.apply.Unlock()
	ext, err := catalog.PrepareExternal(a.db, cat, a.catalogBitwardenProbe)
	if err != nil {
		log.Printf("catalog %s: external references: %v", cat.Name, err)
	}
	err = catalog.ApplyRecords(a.db, cat, items, ext)
	if err == nil {
		EventsEmit("catalog_changed", map[string]any{"catalog_id": cat.ID})
	}
	return err
}

func catalogWriteCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// CatalogSaveResult is what a record save answers.
type CatalogSaveResult struct {
	Saved   bool              `json:"saved"`
	Message string            `json:"message,omitempty"`
	Plan    *catalog.EditPlan `json:"plan,omitempty"`
	// Per-field problems the server reported (422).
	FieldErrors []catalog.RecordReport `json:"field_errors,omitempty"`
	Warnings    []catalog.RecordReport `json:"warnings,omitempty"`
	// Changed fields the writer's own local layer still overrides: the
	// edit reaches everyone else, not their own view.
	StillLocal []string `json:"still_local,omitempty"`
}

// CatalogRecordSave writes the writer's changes to the catalog record.
func (a *App) CatalogRecordSave(in catalog.EditInput) (*CatalogSaveResult, error) {
	t, err := a.catalogWriteTarget(in.Kind, in.LocalID)
	if err != nil {
		return nil, err
	}
	img, err := catalog.ResolveIconImage(a.db, &in)
	if err != nil {
		return &CatalogSaveResult{Message: "not saved: " + err.Error()}, nil
	}
	plan, err := catalog.PlanEdit(a.db, t.cat, t.scope, in, json.RawMessage(t.raw), t.rev)
	if err != nil {
		return nil, err
	}
	res := &CatalogSaveResult{Plan: plan}
	switch {
	case len(plan.Refusals) > 0:
		res.Message = "not saved: " + strings.Join(plan.Refusals, "; ")
		return res, nil
	case len(plan.Conflicts) > 0:
		res.Message = "the record changed in the catalog since you opened it"
		return res, nil
	case len(plan.NeedSlots) > 0:
		res.Message = "name the new slot(s) first"
		return res, nil
	case len(plan.Changed) == 0:
		res.Message = "nothing changed"
		return res, nil
	}
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	if _, sends := plan.Patch["icon_image"]; sends && img != nil {
		if msg := a.uploadRecordImage(ctx, t.client, img); msg != "" {
			res.Message = "not saved: " + msg
			return res, nil
		}
	}
	// New slots first (the record references them), each bound to the
	// writer's own object.
	var items []catalog.Item
	for _, s := range plan.NewSlots {
		body := map[string]any{"uuid": s.UUID, "folder_uuid": s.FolderUUID, "type": s.Type, "name": s.Name, "kind": s.Kind,
			"backend": s.Backend, "external_ref": s.ExternalRef, "doc_md": ""}
		w, err := t.client.Create(ctx, catalog.KindSlots, body)
		if err != nil {
			res.Message = "not saved: the new slot " + s.Name + ": " + catalogShortError(err)
			return res, nil
		}
		items = append(items, catalog.UpsertItem(catalog.KindSlots, w.Record))
	}
	if len(items) > 0 {
		if err := a.catalogApply(t.cat, items); err != nil {
			return nil, err
		}
	}
	for _, b := range plan.Bindings {
		if err := a.db.SetCatalogBinding(t.cat.ID, b.SlotUUID, b.Kind, b.TargetID); err != nil {
			return nil, err
		}
	}
	w, err := t.client.Patch(ctx, in.Kind, t.uuid, t.rev, plan.Patch)
	if err != nil {
		if cur, ok := catalog.RevMismatch(err); ok {
			// Someone saved first: keep their version and show both.
			if aerr := a.catalogApply(t.cat, []catalog.Item{catalog.UpsertItem(in.Kind, cur)}); aerr != nil {
				return nil, aerr
			}
			in.AcceptRev = 0
			plan, perr := catalog.PlanEdit(a.db, t.cat, t.scope, in, cur, recordRev(string(cur)))
			if perr != nil {
				return nil, perr
			}
			return &CatalogSaveResult{Plan: plan, Message: "the record changed in the catalog since you opened it"}, nil
		}
		res.Message = "not saved: " + catalogShortError(err)
		res.FieldErrors = catalog.FieldErrors(err)
		return res, nil
	}
	if err := a.catalogApply(t.cat, []catalog.Item{catalog.UpsertItem(in.Kind, w.Record)}); err != nil {
		return nil, err
	}
	res.Saved, res.Message, res.Warnings = true, "saved to the catalog", w.Warnings
	if in.Kind != catalog.KindForwards {
		if ovs, err := a.db.ListCatalogOverrides(t.cat.ID); err == nil {
			for _, o := range ovs {
				if o.RemoteUUID != t.uuid {
					continue
				}
				var layer map[string]any
				_ = json.Unmarshal([]byte(o.JSON), &layer)
				for _, f := range plan.Changed {
					if k, ok := strings.CutPrefix(f, "settings."); ok {
						if _, has := layer[k]; has {
							res.StillLocal = append(res.StillLocal, f)
						}
					}
				}
			}
		}
	}
	a.recordAudit("catalog.record_save", t.cat.ID, map[string]string{"kind": in.Kind, "uuid": t.uuid, "fields": strings.Join(plan.Changed, ",")})
	return res, nil
}

// CatalogCreateInput creates a record under a catalog folder (or, for a
// forward, a catalog connection). ParentID "" with kind folders on a
// catalog's root makes a space (admin).
type CatalogCreateInput struct {
	CatalogID string           `json:"catalog_id"`
	Kind      string           `json:"kind"`
	ParentID  string           `json:"parent_id"`
	Form      catalog.EditForm `json:"form"`
}

// CatalogCreate creates a record on the server, then shows it locally.
// Returns the new local id.
func (a *App) CatalogCreate(in CatalogCreateInput) (string, error) {
	cat, c, err := a.catalogWriteGuard(in.CatalogID)
	if err != nil {
		return "", err
	}
	u := uuid.NewString()
	body := map[string]any{"uuid": u}
	parentUUID := func(kind, id string) (string, error) {
		pc, pu, _, _, err := a.catalogRow(kind, id)
		if err != nil {
			return "", err
		}
		if pc != cat.ID {
			return "", fmt.Errorf("that is in another catalog")
		}
		return pu, nil
	}
	switch in.Kind {
	case catalog.KindFolders:
		name := strings.TrimSpace(in.Form.Name)
		if name == "" {
			return "", fmt.Errorf("a name is needed")
		}
		body["name"] = name
		if in.ParentID == "" || in.ParentID == cat.RootFolderID {
			body["parent_uuid"] = nil
		} else {
			pu, err := parentUUID(catalog.KindFolders, in.ParentID)
			if err != nil {
				return "", err
			}
			body["parent_uuid"] = pu
		}
	case catalog.KindConnections:
		if strings.TrimSpace(in.Form.Name) == "" || strings.TrimSpace(in.Form.Hostname) == "" {
			return "", fmt.Errorf("a name and a hostname are needed")
		}
		if strings.EqualFold(strings.TrimSpace(in.Form.Protocol), "local") {
			return "", fmt.Errorf("a local shell connection cannot be in a catalog")
		}
		pu, err := parentUUID(catalog.KindFolders, in.ParentID)
		if err != nil {
			return "", err
		}
		proto := in.Form.Protocol
		if proto == "" {
			proto = "ssh"
		}
		body["folder_uuid"], body["name"], body["hostname"], body["protocol"] = pu, strings.TrimSpace(in.Form.Name), strings.TrimSpace(in.Form.Hostname), proto
		body["settings"], body["tags"], body["notes"] = map[string]any{}, []string{}, ""
	case catalog.KindForwards:
		pu, err := parentUUID(catalog.KindConnections, in.ParentID)
		if err != nil {
			return "", err
		}
		f := in.Form
		body["connection_uuid"], body["kind"], body["local_addr"], body["local_port"] = pu, f.Kind, f.LocalAddr, f.LocalPort
		body["remote_host"], body["remote_port"], body["auto_start"], body["description"] = f.RemoteHost, f.RemotePort, f.AutoStart, f.Description
	default:
		return "", fmt.Errorf("unknown kind %q", in.Kind)
	}
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	w, err := c.Create(ctx, in.Kind, body)
	if err != nil {
		return "", fmt.Errorf("not created: %s", catalogShortError(err))
	}
	if err := a.catalogApply(cat, []catalog.Item{catalog.UpsertItem(in.Kind, w.Record)}); err != nil {
		return "", err
	}
	a.recordAudit("catalog.record_create", cat.ID, map[string]string{"kind": in.Kind, "uuid": u})
	return a.db.CatalogLocalID(cat.ID, in.Kind, u)
}

// CatalogMove moves a record to another folder (a forward to another
// connection) of the same catalog.
func (a *App) CatalogMove(kind, id, newParentID string) error {
	t, err := a.catalogWriteTarget(kind, id)
	if err != nil {
		return err
	}
	field, parentKind := "folder_uuid", catalog.KindFolders
	switch kind {
	case catalog.KindFolders:
		field = "parent_uuid"
	case catalog.KindForwards:
		field, parentKind = "connection_uuid", catalog.KindConnections
	}
	var target any
	if kind == catalog.KindFolders && newParentID == t.cat.RootFolderID {
		target = nil // a space (admin)
	} else {
		pc, pu, state, praw, err := a.catalogRow(parentKind, newParentID)
		if err != nil {
			return err
		}
		if pc != t.cat.ID {
			return fmt.Errorf("records move only inside their catalog; use Keep as local to take a copy out")
		}
		if state == "removed" {
			return fmt.Errorf("that folder left the catalog")
		}
		if parentKind == catalog.KindFolders && !catalogWritable(praw) {
			return fmt.Errorf("you cannot add to that folder")
		}
		target = pu
	}
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	w, err := t.client.Patch(ctx, kind, t.uuid, t.rev, map[string]any{field: target})
	if err != nil {
		if cur, ok := catalog.RevMismatch(err); ok {
			_ = a.catalogApply(t.cat, []catalog.Item{catalog.UpsertItem(kind, cur)})
			return fmt.Errorf("the record changed in the catalog meanwhile; it is updated here, try the move again")
		}
		return fmt.Errorf("not moved: %s", catalogShortError(err))
	}
	if err := a.catalogApply(t.cat, []catalog.Item{catalog.UpsertItem(kind, w.Record)}); err != nil {
		return err
	}
	a.recordAudit("catalog.record_move", t.cat.ID, map[string]string{"kind": kind, "uuid": t.uuid})
	return nil
}

// CatalogDelete deletes a record from the catalog for everyone; the
// writer's own local data on it goes too (the confirm says so).
func (a *App) CatalogDelete(kind, id string, recursive bool) error {
	t, err := a.catalogWriteTarget(kind, id)
	if err != nil {
		return err
	}
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	ds, err := t.client.Delete(ctx, kind, t.uuid, t.rev, recursive)
	if err != nil {
		if cur, ok := catalog.RevMismatch(err); ok {
			_ = a.catalogApply(t.cat, []catalog.Item{catalog.UpsertItem(kind, cur)})
			return fmt.Errorf("the record changed in the catalog meanwhile; look at it again before deleting")
		}
		var ae *catalog.APIError
		if errors.As(err, &ae) && ae.Code == "not_empty" {
			return fmt.Errorf("the folder is not empty")
		}
		return fmt.Errorf("not deleted: %s", catalogShortError(err))
	}
	if err := a.catalogApply(t.cat, catalog.DeleteItems(ds)); err != nil {
		return err
	}
	// Rows kept as "removed" because of the writer's own local data: the
	// writer deleted the record on purpose, so they go as well.
	if lid, _ := a.db.CatalogLocalID(t.cat.ID, kind, t.uuid); lid != "" {
		_ = a.db.DeleteRemovedCatalogRow(kind, lid)
	}
	for _, d := range ds {
		if d.Kind == catalog.KindSlots {
			continue
		}
		if lid, _ := a.db.CatalogLocalID(t.cat.ID, d.Kind, d.UUID); lid != "" {
			_ = a.db.DeleteRemovedCatalogRow(d.Kind, lid)
		}
	}
	EventsEmit("catalog_changed", map[string]any{"catalog_id": t.cat.ID})
	a.recordAudit("catalog.record_delete", t.cat.ID, map[string]string{"kind": kind, "uuid": t.uuid, "count": fmt.Sprint(len(ds))})
	return nil
}

// Slots ------------------------------------------------------------------

// CatalogSlotInput creates or changes a slot.
type CatalogSlotInput struct {
	CatalogID string `json:"catalog_id"`
	// Create: the catalog folder (local id) the slot is defined on.
	FolderID string `json:"folder_id"`
	// Update: the slot.
	UUID        string          `json:"uuid"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Backend     string          `json:"backend"`
	ExternalRef json.RawMessage `json:"external_ref"`
	DocMD       string          `json:"doc_md"`
}

func (a *App) slotFolderWritable(catalogID, folderUUID string) (bool, string) {
	lid, err := a.db.CatalogLocalID(catalogID, catalog.KindFolders, folderUUID)
	if err != nil || lid == "" {
		return false, ""
	}
	_, _, raw, err := a.db.CatalogFolderOf(catalog.KindFolders, lid)
	if err != nil {
		return false, ""
	}
	return catalogWritable(raw), lid
}

func slotBody(in CatalogSlotInput) map[string]any {
	b := map[string]any{"name": strings.TrimSpace(in.Name), "doc_md": in.DocMD}
	if in.Type == "cred" {
		k := in.Kind
		if k == "" {
			k = "slot"
		}
		b["kind"] = k
		if k == "external" {
			b["backend"], b["external_ref"] = in.Backend, in.ExternalRef
		} else {
			b["backend"], b["external_ref"] = nil, nil
		}
	}
	return b
}

// CatalogSlotCreate defines a new slot on a catalog folder.
func (a *App) CatalogSlotCreate(in CatalogSlotInput) error {
	cat, c, err := a.catalogWriteGuard(in.CatalogID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("a slot needs a name")
	}
	if in.Type != "cred" && in.Type != "net" && in.Type != "jump" {
		return fmt.Errorf("a slot is a credential, a network or a bastion")
	}
	fc, fu, _, fraw, err := a.catalogRow(catalog.KindFolders, in.FolderID)
	if err != nil || fc != cat.ID {
		return fmt.Errorf("choose a folder of this catalog")
	}
	if !catalogWritable(fraw) {
		return fmt.Errorf("you cannot add to that folder")
	}
	body := slotBody(in)
	body["uuid"], body["folder_uuid"], body["type"] = uuid.NewString(), fu, in.Type
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	w, err := c.Create(ctx, catalog.KindSlots, body)
	if err != nil {
		return fmt.Errorf("not created: %s", catalogShortError(err))
	}
	a.recordAudit("catalog.slot_create", cat.ID, map[string]string{"name": in.Name})
	return a.catalogApply(cat, []catalog.Item{catalog.UpsertItem(catalog.KindSlots, w.Record)})
}

func (a *App) catalogSlot(catalogID, slotUUID string) (*store.CatalogSlot, error) {
	slots, err := a.db.ListCatalogSlots(catalogID)
	if err != nil {
		return nil, err
	}
	for i := range slots {
		if slots[i].UUID == slotUUID {
			return &slots[i], nil
		}
	}
	return nil, fmt.Errorf("no such slot in this catalog")
}

// CatalogSlotUpdate renames a slot, changes its documentation or, for a
// credential slot, whether it names an external item.
func (a *App) CatalogSlotUpdate(in CatalogSlotInput) error {
	cat, c, err := a.catalogWriteGuard(in.CatalogID)
	if err != nil {
		return err
	}
	s, err := a.catalogSlot(cat.ID, in.UUID)
	if err != nil {
		return err
	}
	if ok, _ := a.slotFolderWritable(cat.ID, s.FolderUUID); !ok {
		return fmt.Errorf("you can read this slot but not change it")
	}
	in.Type = s.Type
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	w, err := c.Patch(ctx, catalog.KindSlots, s.UUID, s.Rev, slotBody(in))
	if err != nil {
		if cur, ok := catalog.RevMismatch(err); ok {
			_ = a.catalogApply(cat, []catalog.Item{catalog.UpsertItem(catalog.KindSlots, cur)})
			return fmt.Errorf("the slot changed in the catalog meanwhile; it is updated here, change it again")
		}
		return fmt.Errorf("not saved: %s", catalogShortError(err))
	}
	return a.catalogApply(cat, []catalog.Item{catalog.UpsertItem(catalog.KindSlots, w.Record)})
}

// CatalogSlotDelete deletes a slot; one still in use is refused with the
// records that use it.
func (a *App) CatalogSlotDelete(catalogID, slotUUID string) error {
	cat, c, err := a.catalogWriteGuard(catalogID)
	if err != nil {
		return err
	}
	s, err := a.catalogSlot(cat.ID, slotUUID)
	if err != nil {
		return err
	}
	if ok, _ := a.slotFolderWritable(cat.ID, s.FolderUUID); !ok {
		return fmt.Errorf("you can read this slot but not change it")
	}
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	ds, err := c.Delete(ctx, catalog.KindSlots, s.UUID, s.Rev, false)
	if err != nil {
		if refs, ok := catalog.InUse(err); ok {
			var names []string
			for _, r := range refs {
				names = append(names, a.catalogRecordName(cat.ID, r.Kind, r.UUID))
			}
			return fmt.Errorf("still used by %s; change those first", strings.Join(names, ", "))
		}
		return fmt.Errorf("not deleted: %s", catalogShortError(err))
	}
	a.recordAudit("catalog.slot_delete", cat.ID, map[string]string{"name": s.Name})
	return a.catalogApply(cat, catalog.DeleteItems(ds))
}

// catalogRecordName names a record for a message ("web-1", or "a record
// you cannot see").
func (a *App) catalogRecordName(catalogID, kind, uuid string) string {
	lid, _ := a.db.CatalogLocalID(catalogID, kind, uuid)
	if lid == "" {
		return "a record you cannot see"
	}
	var name string
	switch kind {
	case catalog.KindFolders:
		if f, err := a.db.GetFolder(lid); err == nil {
			name = f.Name
		}
	case catalog.KindConnections:
		if c, err := a.db.GetConnection(lid); err == nil {
			name = c.Name
		}
	default:
		name = "a forward"
	}
	if name == "" {
		return "a record"
	}
	return `"` + name + `"`
}

// CatalogDuplicate makes a new catalog connection next to a catalog
// connection, with the same settings (slots included), protocol, tags,
// notes, icon, documentation and forwards, under a new name and address.
// Writers of the folder only. Returns the new local id.
func (a *App) CatalogDuplicate(id, name, hostname string) (string, error) {
	name, hostname = strings.TrimSpace(name), strings.TrimSpace(hostname)
	if name == "" || hostname == "" {
		return "", fmt.Errorf("a name and a hostname are needed")
	}
	t, err := a.catalogWriteTarget(catalog.KindConnections, id)
	if err != nil {
		return "", err
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(t.raw), &rec); err != nil {
		return "", fmt.Errorf("catalog record: %w", err)
	}
	u := uuid.NewString()
	body := map[string]any{"uuid": u, "name": name, "hostname": hostname}
	// The record's own fields, in catalog terms already (slot references
	// stay valid in the same folder).
	for _, k := range []string{"folder_uuid", "protocol", "sort_order", "settings", "tags", "notes", "icon_name", "icon_color", "icon_image", "doc_md"} {
		if v, ok := rec[k]; ok && v != nil {
			body[k] = v
		}
	}
	ctx, cancel := catalogWriteCtx()
	defer cancel()
	w, err := t.client.Create(ctx, catalog.KindConnections, body)
	if err != nil {
		return "", fmt.Errorf("not created: %s", catalogShortError(err))
	}
	items := []catalog.Item{catalog.UpsertItem(catalog.KindConnections, w.Record)}
	// Its catalog forwards, as new records on the copy.
	rows, err := a.db.Conn().Query(`SELECT catalog_json FROM port_forwards WHERE connection_id = ? AND remote_uuid IS NOT NULL AND catalog_state <> 'removed'`, id)
	if err == nil {
		var fwds []string
		for rows.Next() {
			var j string
			if rows.Scan(&j) == nil {
				fwds = append(fwds, j)
			}
		}
		rows.Close()
		for _, j := range fwds {
			var f map[string]any
			if json.Unmarshal([]byte(j), &f) != nil {
				continue
			}
			fb := map[string]any{"uuid": uuid.NewString(), "connection_uuid": u}
			for _, k := range []string{"kind", "local_addr", "local_port", "remote_host", "remote_port", "auto_start", "description"} {
				if v, ok := f[k]; ok {
					fb[k] = v
				}
			}
			fw, err := t.client.Create(ctx, catalog.KindForwards, fb)
			if err != nil {
				log.Printf("catalog %s: duplicate forward: %v", t.cat.Name, err)
				continue
			}
			items = append(items, catalog.UpsertItem(catalog.KindForwards, fw.Record))
		}
	}
	if err := a.catalogApply(t.cat, items); err != nil {
		return "", err
	}
	a.recordAudit("catalog.record_duplicate", t.cat.ID, map[string]string{"from": t.uuid, "uuid": u})
	return a.db.CatalogLocalID(t.cat.ID, catalog.KindConnections, u)
}
