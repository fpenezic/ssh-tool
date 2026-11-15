package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"

	"ssh-tool/internal/store"
)

// Writing catalog records from the app (PROTOCOL 6.2 - 6.4): creates with
// client uuids, RFC 7396 merge patches with If-Match, deletes. The
// server's answer is applied through the same path as a feed item, so the
// local rows look exactly as if the next sync had brought them.

// WriteResult is a create or update answer.
type WriteResult struct {
	Record   json.RawMessage `json:"record"`
	Warnings []RecordReport  `json:"warnings"`
}

// Deleted is one record a delete removed.
type Deleted struct {
	Kind string `json:"kind"`
	UUID string `json:"uuid"`
	Rev  int64  `json:"rev"`
}

// RevMismatch returns the current record of a 412 rev_mismatch.
func RevMismatch(err error) (json.RawMessage, bool) {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusPreconditionFailed {
		return nil, false
	}
	var d struct {
		Current json.RawMessage `json:"current"`
	}
	_ = json.Unmarshal(ae.Details, &d)
	return d.Current, true
}

// InUse returns the referrers of a slot delete refused with 409 in_use.
func InUse(err error) ([]Deleted, bool) {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "in_use" {
		return nil, false
	}
	var d struct {
		Referrers []Deleted `json:"referrers"`
	}
	_ = json.Unmarshal(ae.Details, &d)
	return d.Referrers, true
}

// FieldErrors reads details.fields of a 422.
func FieldErrors(err error) []RecordReport {
	var ae *APIError
	if !errors.As(err, &ae) || len(ae.Details) == 0 {
		return nil
	}
	var d struct {
		Fields []RecordReport `json:"fields"`
	}
	_ = json.Unmarshal(ae.Details, &d)
	return d.Fields
}

func ifMatch(rev int64) map[string]string {
	return map[string]string{"If-Match": `"` + strconv.FormatInt(rev, 10) + `"`}
}

// Create posts a new record (body carries its uuid).
func (c *Client) Create(ctx context.Context, kind string, body map[string]any) (*WriteResult, error) {
	if !KnownKind(kind) {
		return nil, fmt.Errorf("catalog: unknown kind %q", kind)
	}
	var out WriteResult
	if err := c.send(ctx, http.MethodPost, "/api/v1/"+kind, body, &out, true, nil); err != nil {
		return nil, err
	}
	return &out, nil
}

// Patch applies a merge patch to a record at rev.
func (c *Client) Patch(ctx context.Context, kind, uuid string, rev int64, patch map[string]any) (*WriteResult, error) {
	if !KnownKind(kind) {
		return nil, fmt.Errorf("catalog: unknown kind %q", kind)
	}
	h := ifMatch(rev)
	h["Content-Type"] = "application/merge-patch+json"
	var out WriteResult
	if err := c.send(ctx, http.MethodPatch, "/api/v1/"+kind+"/"+uuid, patch, &out, true, h); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a record at rev; recursive for a folder with content.
func (c *Client) Delete(ctx context.Context, kind, uuid string, rev int64, recursive bool) ([]Deleted, error) {
	if !KnownKind(kind) {
		return nil, fmt.Errorf("catalog: unknown kind %q", kind)
	}
	path := "/api/v1/" + kind + "/" + uuid
	if recursive {
		path += "?recursive=true"
	}
	var out struct {
		Deleted []Deleted `json:"deleted"`
	}
	if err := c.send(ctx, http.MethodDelete, path, nil, &out, true, ifMatch(rev)); err != nil {
		return nil, err
	}
	return out.Deleted, nil
}

// MergePatch is the RFC 7396 diff from old to new: changed and new keys
// with their new value, removed keys as null, objects recursively, arrays
// whole. Equal inputs give an empty patch.
func MergePatch(old, new map[string]any) map[string]any {
	out := map[string]any{}
	for k, nv := range new {
		ov, had := old[k]
		if had && reflect.DeepEqual(ov, nv) {
			continue
		}
		om, oIsMap := ov.(map[string]any)
		nm, nIsMap := nv.(map[string]any)
		if had && oIsMap && nIsMap {
			if sub := MergePatch(om, nm); len(sub) > 0 {
				out[k] = sub
			}
			continue
		}
		out[k] = nv
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

// ApplyRecords applies records the server answered with (create, update)
// and deletions, in one catalog writer transaction, then materialises.
func ApplyRecords(db *store.DB, cat *store.Catalog, items []Item, external ExternalResolver) error {
	return db.WithCatalogWriter(func(tx *sql.Tx) error {
		a, err := newApplier(db, tx, cat, nil)
		if err != nil {
			return err
		}
		if err := a.page(items); err != nil {
			return err
		}
		// A forward whose connection came later in the same list.
		pending := a.pendingFwd
		a.pendingFwd = nil
		for _, it := range pending {
			if err := a.upsertForward(it); err != nil {
				return err
			}
		}
		if _, err := db.CatalogParentsTx(tx, cat.ID, cat.RootFolderID); err != nil {
			return err
		}
		return materialiseTx(db, tx, cat.ID, external)
	})
}

// UpsertItem wraps a record for ApplyRecords.
func UpsertItem(kind string, raw json.RawMessage) Item {
	return Item{Kind: kind, Op: "upsert", Record: raw}
}

// DeleteItems wraps a delete answer for ApplyRecords.
func DeleteItems(ds []Deleted) []Item {
	out := make([]Item, 0, len(ds))
	for _, d := range ds {
		out = append(out, Item{Kind: d.Kind, Op: "delete", UUID: d.UUID, Rev: d.Rev, Reason: "deleted"})
	}
	return out
}
