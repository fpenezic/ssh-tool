package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// SyncResult reports one pass of the change feed.
type SyncResult struct {
	Pages    int  `json:"pages"`
	Reset    bool `json:"reset"`
	Upserted int  `json:"upserted"`
	Removed  int  `json:"removed"`
	Kept     int  `json:"kept"`    // left the catalog, kept as "removed" for its local data
	Skipped  int  `json:"skipped"` // refused (protocol local) or unknown kind
	Fallback int  `json:"fallback"`
}

// pageLimit is the feed page size asked for (the server caps it at 2000).
const pageLimit = 500

// Sync pulls the change feed of one catalog and applies it to the store
// (PROTOCOL 7.4). Pages are applied as they arrive, each in its own catalog
// writer transaction; the cursor is stored only after the last page, in the
// same transaction as the end-of-pass work, so a pass that breaks off
// starts again from the previous cursor (re-applying a page is harmless:
// upserts go by rev, tombstones are idempotent).
//
// A reset (first sync, grants changed, an old cursor) is a full snapshot:
// afterwards every held record that did not appear in it is removed, under
// the rule that a record carrying local data is kept as "removed from
// catalog" instead.
func Sync(ctx context.Context, db *store.DB, cat *store.Catalog, c *Client) (SyncResult, error) {
	var res SyncResult
	cursor := cat.Cursor
	var snapshot map[string]bool // kind/uuid seen in the current snapshot
	var pendingFwd []Item
	for {
		feed, err := c.Changes(ctx, cursor, pageLimit)
		if err != nil {
			return res, err
		}
		res.Pages++
		if feed.Reset && snapshot == nil {
			snapshot = map[string]bool{}
			res.Reset = true
		}
		err = db.WithCatalogWriter(func(tx *sql.Tx) error {
			a, err := newApplier(db, tx, cat, snapshot)
			if err != nil {
				return err
			}
			if err := a.page(feed.Items); err != nil {
				return err
			}
			pendingFwd = append(pendingFwd, a.pendingFwd...)
			res.add(a.res)
			return nil
		})
		if err != nil {
			return res, err
		}
		cursor = feed.Cursor
		if !feed.More {
			break
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
	}
	err := db.WithCatalogWriter(func(tx *sql.Tx) error {
		a, err := newApplier(db, tx, cat, snapshot)
		if err != nil {
			return err
		}
		// Forwards that arrived before their connection (across pages).
		for _, it := range pendingFwd {
			if err := a.upsertForward(it); err != nil {
				return err
			}
		}
		for _, it := range a.pendingFwd {
			log.Printf("catalog %s: forward %s names a connection this catalog does not hold; skipped", cat.Name, it.UUID)
			a.res.Skipped++
		}
		if snapshot != nil {
			if err := a.removeUnseen(); err != nil {
				return err
			}
		}
		n, err := db.CatalogParentsTx(tx, cat.ID, cat.RootFolderID)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Printf("catalog %s: %d record(s) arrived without their parent folder; shown at the top of the catalog", cat.Name, n)
		}
		a.res.Fallback = n
		res.add(a.res)
		return db.SetCatalogCursorTx(tx, cat.ID, cursor, time.Now().Unix())
	})
	return res, err
}

func (r *SyncResult) add(o SyncResult) {
	r.Upserted += o.Upserted
	r.Removed += o.Removed
	r.Kept += o.Kept
	r.Skipped += o.Skipped
	r.Fallback += o.Fallback
}

// applier applies feed items inside one writer transaction.
type applier struct {
	db         *store.DB
	tx         *sql.Tx
	cat        *store.Catalog
	held       *store.CatalogHeld
	slots      map[string]int64
	snapshot   map[string]bool
	pendingFwd []Item
	res        SyncResult
}

func newApplier(db *store.DB, tx *sql.Tx, cat *store.Catalog, snapshot map[string]bool) (*applier, error) {
	held, err := db.CatalogHeldTx(tx, cat.ID)
	if err != nil {
		return nil, err
	}
	slots, err := db.CatalogSlotRevsTx(tx, cat.ID)
	if err != nil {
		return nil, err
	}
	return &applier{db: db, tx: tx, cat: cat, held: held, slots: slots, snapshot: snapshot}, nil
}

func key(kind, uuid string) string { return kind + "/" + uuid }

// wants: apply an upsert when the record is new here, newer than what is
// held, held as "removed" (it came back), or during a snapshot.
func (a *applier) wants(kind, uuid string, rev int64) bool {
	if a.snapshot != nil {
		return true
	}
	if kind == KindSlots {
		have, ok := a.slots[uuid]
		return !ok || rev > have
	}
	have, ok := a.held.Revs[kind][uuid]
	return !ok || rev > have || a.held.State[kind][uuid] == "removed"
}

// page applies one page: upserts in feed order (a folder comes before what
// is inside it within a page), tombstones afterwards, children before
// parents, so a folder's removal sees what is left below it.
func (a *applier) page(items []Item) error {
	var tombs []Item
	for _, it := range items {
		if !KnownKind(it.Kind) {
			a.res.Skipped++
			continue
		}
		switch it.Op {
		case "upsert":
			if err := a.upsert(it); err != nil {
				return err
			}
		case "delete":
			if a.snapshot != nil {
				delete(a.snapshot, key(it.Kind, it.UUID))
			}
			tombs = append(tombs, it)
		}
	}
	for _, kind := range []string{KindForwards, KindConnections, KindFolders, KindSlots} {
		var uuids []string
		for _, it := range tombs {
			if it.Kind == kind {
				uuids = append(uuids, it.UUID)
			}
		}
		if kind == KindFolders {
			uuids = a.byDepth(uuids)
		}
		for _, u := range uuids {
			if err := a.remove(kind, u); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *applier) upsert(it Item) error {
	var c Common
	if err := json.Unmarshal(it.Record, &c); err != nil || c.UUID == "" {
		a.res.Skipped++
		return nil
	}
	if a.snapshot != nil {
		a.snapshot[key(it.Kind, c.UUID)] = true
	}
	if !a.wants(it.Kind, c.UUID, c.Rev) {
		return nil
	}
	switch it.Kind {
	case KindFolders:
		return a.upsertFolder(it)
	case KindConnections:
		return a.upsertConnection(it)
	case KindForwards:
		return a.upsertForward(it)
	case KindSlots:
		return a.upsertSlot(it)
	}
	return nil
}

func (a *applier) folderID(uuid *string) string {
	if uuid == nil || *uuid == "" {
		return a.cat.RootFolderID
	}
	if id, ok := a.held.IDs[KindFolders][*uuid]; ok {
		return id
	}
	// Not held yet (a later page); CatalogParentsTx fixes it at the end.
	return a.cat.RootFolderID
}

func (a *applier) upsertFolder(it Item) error {
	var f Folder
	if err := json.Unmarshal(it.Record, &f); err != nil {
		a.res.Skipped++
		return nil
	}
	id, err := a.db.UpsertCatalogFolderTx(a.tx, a.cat.ID, store.CatalogFolderRow{
		RemoteUUID: f.UUID, Rev: f.Rev, ParentLocalID: a.folderID(f.ParentUUID), Name: f.Name,
		SortOrder: f.SortOrder, IconName: f.IconName, IconColor: f.IconColor, RawJSON: string(it.Record),
	}, a.held.IDs[KindFolders][f.UUID])
	if err != nil {
		return fmt.Errorf("catalog folder %s: %w", f.UUID, err)
	}
	a.hold(KindFolders, f.UUID, id, f.Rev)
	a.res.Upserted++
	return nil
}

func (a *applier) upsertConnection(it Item) error {
	var c Connection
	if err := json.Unmarshal(it.Record, &c); err != nil {
		a.res.Skipped++
		return nil
	}
	// The server refuses local connections; refuse them here as well. A
	// local connection runs its initial command on this machine.
	if c.ProtocolOrDefault() == "local" {
		log.Printf("catalog %s: connection %s has protocol local; refused", a.cat.Name, c.UUID)
		a.res.Skipped++
		if _, held := a.held.IDs[KindConnections][c.UUID]; held {
			return a.remove(KindConnections, c.UUID)
		}
		return nil
	}
	fu := c.FolderUUID
	id, err := a.db.UpsertCatalogConnectionTx(a.tx, a.cat.ID, store.CatalogConnectionRow{
		RemoteUUID: c.UUID, Rev: c.Rev, FolderLocalID: a.folderID(&fu), Name: c.Name, Hostname: c.Hostname,
		Protocol: c.ProtocolOrDefault(), SortOrder: c.SortOrder, Tags: c.Tags, Notes: c.Notes,
		IconName: c.IconName, IconColor: c.IconColor, RawJSON: string(it.Record),
	}, a.held.IDs[KindConnections][c.UUID])
	if err != nil {
		return fmt.Errorf("catalog connection %s: %w", c.UUID, err)
	}
	a.hold(KindConnections, c.UUID, id, c.Rev)
	a.res.Upserted++
	return nil
}

// upsertForward applies a forward; one whose connection is not held yet is
// set aside and retried after the last page.
func (a *applier) upsertForward(it Item) error {
	var f Forward
	if err := json.Unmarshal(it.Record, &f); err != nil {
		a.res.Skipped++
		return nil
	}
	conn, ok := a.held.IDs[KindConnections][f.ConnectionUUID]
	if !ok {
		it.UUID = f.UUID
		a.pendingFwd = append(a.pendingFwd, it)
		return nil
	}
	id, err := a.db.UpsertCatalogForwardTx(a.tx, a.cat.ID, store.CatalogForwardRow{
		RemoteUUID: f.UUID, Rev: f.Rev, ConnectionLocalID: conn, Kind: f.Kind, LocalAddr: f.LocalAddr,
		LocalPort: f.LocalPort, RemoteHost: f.RemoteHost, RemotePort: f.RemotePort, Description: f.Description,
		RawJSON: string(it.Record),
	}, a.held.IDs[KindForwards][f.UUID])
	if err != nil {
		log.Printf("catalog %s: forward %s: %v; skipped", a.cat.Name, f.UUID, err)
		a.res.Skipped++
		return nil
	}
	a.hold(KindForwards, f.UUID, id, f.Rev)
	a.res.Upserted++
	return nil
}

func (a *applier) upsertSlot(it Item) error {
	var s Slot
	if err := json.Unmarshal(it.Record, &s); err != nil {
		a.res.Skipped++
		return nil
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	ref := ""
	if len(s.ExternalRef) > 0 && string(s.ExternalRef) != "null" {
		ref = string(s.ExternalRef)
	}
	if err := a.db.UpsertCatalogSlotTx(a.tx, store.CatalogSlot{
		CatalogID: a.cat.ID, UUID: s.UUID, Rev: s.Rev, FolderUUID: s.FolderUUID, Type: s.Type, Name: s.Name,
		Kind: str(s.Kind), Backend: str(s.Backend), ExternalRefJSON: ref, DocMD: str(s.DocMD),
	}); err != nil {
		return fmt.Errorf("catalog slot %s: %w", s.UUID, err)
	}
	a.slots[s.UUID] = s.Rev
	a.res.Upserted++
	return nil
}

func (a *applier) hold(kind, uuid, id string, rev int64) {
	a.held.IDs[kind][uuid] = id
	a.held.Revs[kind][uuid] = rev
	a.held.State[kind][uuid] = ""
}

func (a *applier) remove(kind, uuid string) error {
	if kind == KindSlots {
		if _, ok := a.slots[uuid]; !ok {
			return nil
		}
		delete(a.slots, uuid)
		a.res.Removed++
		// The binding stays: it is the user's, and the slot may return.
		return a.db.DeleteCatalogSlotTx(a.tx, a.cat.ID, uuid)
	}
	if _, ok := a.held.IDs[kind][uuid]; !ok {
		return nil
	}
	kept, err := a.db.RemoveCatalogRecordTx(a.tx, a.cat.ID, kind, uuid)
	if err != nil {
		return err
	}
	if kept {
		a.held.State[kind][uuid] = "removed"
		a.res.Kept++
	} else {
		delete(a.held.IDs[kind], uuid)
		delete(a.held.Revs[kind], uuid)
		delete(a.held.State[kind], uuid)
		a.res.Removed++
	}
	return nil
}

// removeUnseen ends a snapshot: every held record it did not contain left
// the catalog for this user (deleted, or access lost). Rows already
// "removed" stay as they are.
func (a *applier) removeUnseen() error {
	for _, kind := range []string{KindForwards, KindConnections, KindFolders} {
		var gone []string
		for uuid := range a.held.IDs[kind] {
			if !a.snapshot[key(kind, uuid)] && a.held.State[kind][uuid] != "removed" {
				gone = append(gone, uuid)
			}
		}
		// Deeper folders first, so a parent sees its children gone.
		if kind == KindFolders {
			gone = a.byDepth(gone)
		}
		for _, uuid := range gone {
			if err := a.remove(kind, uuid); err != nil {
				return err
			}
		}
	}
	for uuid := range a.slots {
		if !a.snapshot[key(KindSlots, uuid)] {
			if err := a.remove(KindSlots, uuid); err != nil {
				return err
			}
		}
	}
	return nil
}

// byDepth orders folder uuids deepest first by their local parent chain.
func (a *applier) byDepth(uuids []string) []string {
	depth := map[string]int{}
	for _, u := range uuids {
		depth[u], _ = a.db.CatalogFolderDepthTx(a.tx, a.held.IDs[KindFolders][u])
	}
	out := append([]string(nil), uuids...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && depth[out[j]] > depth[out[j-1]]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// KeepConnectionLocal turns a connection that left its catalog ("removed")
// into an ordinary local connection at the top of the tree, with what it
// inherited from its catalog folders folded into its own overrides, so it
// keeps connecting the same way.
func KeepConnectionLocal(db *store.DB, id string) error {
	conn, err := db.GetConnection(id)
	if err != nil {
		return err
	}
	folders, err := db.ListFolders()
	if err != nil {
		return err
	}
	return db.KeepRemovedConnectionLocal(id, resolver.EffectiveOverrides(*conn, folders))
}
