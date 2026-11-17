package main

// Icon images and the catalog (PROTOCOL 3.6).
//
// A publish uploads every distinct image it sends before the POST (HEAD
// first, PUT when the store lacks it). The server takes at most 100 new
// images per caller per hour; the publish waits for its Retry-After for
// about a minute at most, then goes without the images still waiting.
// Those records keep their image in the publisher's "_record" layer,
// marked icon_pending, and a background job per catalog uploads them with
// the server's pacing and sets icon_image on each record as its image
// lands. The mark lives in the store, so a restart picks the job up again
// after the next sync pass. Signing out or removing the catalog stops it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/store"
)

// publishImageWait bounds how long a publish waits on the upload limit.
var publishImageWait = 60 * time.Second

const imageLimitReason = "the catalog's upload limit - sent later"

// catalogImageLimits returns the server's image limits, nil when it has
// no image store (or meta is unreachable).
func catalogImageLimits(ctx context.Context, c *catalog.Client) *catalog.ImageLimits {
	m, err := c.Meta(ctx)
	if err != nil || !m.ImagesSupported() {
		return nil
	}
	l := m.ImageLimitsOf()
	return &l
}

// uploadImage puts one image in the store unless it is there. A 429
// waits for Retry-After when that ends before deadline; otherwise the
// RetryAfterError comes back.
func uploadImage(ctx context.Context, c *catalog.Client, hash string, data []byte, deadline time.Time) error {
	for {
		has, err := c.HeadImage(ctx, hash)
		if err == nil && has {
			return nil
		}
		if err == nil {
			err = c.PutImage(ctx, hash, data)
			if err == nil {
				return nil
			}
		}
		var ra *catalog.RetryAfterError
		if !errors.As(err, &ra) {
			return err
		}
		wait := ra.Wait
		if wait <= 0 {
			wait = time.Second
		}
		if time.Now().Add(wait).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// uploadPublishImages uploads the plan's images before the POST. An image
// the server refuses, or still held back by the upload limit after
// publishImageWait, is taken out of the request (plan.KeepImage). The
// dialog sees progress as catalog_publish_progress events.
func (a *App) uploadPublishImages(ctx context.Context, catID string, c *catalog.Client, plan *catalog.PublishPlan) error {
	imgs := plan.SentImages()
	if len(imgs) == 0 {
		return nil
	}
	deadline := time.Now().Add(publishImageWait)
	limited := false
	for i, pi := range imgs {
		EventsEmit("catalog_publish_progress", map[string]any{"catalog_id": catID, "images_done": i, "images_total": len(imgs)})
		if limited {
			plan.KeepImage(pi.SHA256, imageLimitReason, true)
			continue
		}
		_, data, ok, err := a.db.GetImage(pi.ImageID)
		if err != nil {
			return err
		}
		if !ok {
			plan.KeepImage(pi.SHA256, "the image is no longer on this machine", false)
			continue
		}
		err = uploadImage(ctx, c, pi.SHA256, data, deadline)
		var ra *catalog.RetryAfterError
		switch {
		case err == nil:
		case errors.As(err, &ra):
			limited = true
			plan.KeepImage(pi.SHA256, imageLimitReason, true)
		case catalog.ServerImageRefusal(err) != nil:
			plan.KeepImage(pi.SHA256, catalog.ServerImageRefusal(err).Message, false)
		default:
			return fmt.Errorf("upload icon image: %w", err)
		}
	}
	EventsEmit("catalog_publish_progress", map[string]any{"catalog_id": catID, "images_done": len(imgs), "images_total": len(imgs)})
	return nil
}

// Background upload of held-back images -----------------------------------

type iconJob struct{ cancel context.CancelFunc }

type iconJobs struct {
	mu   sync.Mutex
	stop map[string]*iconJob
}

var catalogIconJobs = &iconJobs{stop: map[string]*iconJob{}}

// pendingIcon is a record whose image waits for the background upload.
type pendingIcon struct {
	kind, uuid, imageID string
}

func (a *App) pendingIcons(catID string) ([]pendingIcon, error) {
	ovs, err := a.db.ListCatalogOverrides(catID)
	if err != nil {
		return nil, err
	}
	var out []pendingIcon
	for _, o := range ovs {
		var layer map[string]json.RawMessage
		if json.Unmarshal([]byte(o.JSON), &layer) != nil {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(layer[catalog.RecordLayerKey], &rec) != nil {
			continue
		}
		id, _ := rec["icon_image_id"].(string)
		if p, _ := rec[catalog.IconPendingKey].(bool); p && id != "" {
			out = append(out, pendingIcon{kind: o.Kind, uuid: o.RemoteUUID, imageID: id})
		}
	}
	return out, nil
}

// startIconUploads starts the background upload for a catalog when it
// has held-back images and none is running.
func (a *App) startIconUploads(cat *store.Catalog, c *catalog.Client) {
	pend, err := a.pendingIcons(cat.ID)
	if err != nil || len(pend) == 0 {
		return
	}
	j := catalogIconJobs
	j.mu.Lock()
	if _, running := j.stop[cat.ID]; running {
		j.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &iconJob{cancel: cancel}
	j.stop[cat.ID] = job
	j.mu.Unlock()
	go func() {
		defer func() {
			j.mu.Lock()
			if j.stop[cat.ID] == job { // a newer job owns the entry otherwise
				delete(j.stop, cat.ID)
			}
			j.mu.Unlock()
			cancel()
		}()
		a.runIconUploads(ctx, cat, c)
	}()
}

// stopIconUploads ends a catalog's background upload (sign out, remove).
func stopIconUploads(catID string) {
	j := catalogIconJobs
	j.mu.Lock()
	if job, ok := j.stop[catID]; ok {
		job.cancel()
		delete(j.stop, catID)
	}
	j.mu.Unlock()
}

func (a *App) runIconUploads(ctx context.Context, cat *store.Catalog, c *catalog.Client) {
	emit := func(left, total int, waitUntil time.Time, msg string) {
		ev := map[string]any{"catalog_id": cat.ID, "catalog_name": cat.Name, "left": left, "total": total}
		if !waitUntil.IsZero() {
			ev["waiting_until"] = waitUntil.Unix()
		}
		if msg != "" {
			ev["message"] = msg
		}
		EventsEmit("catalog_icon_uploads", ev)
	}
	pend, err := a.pendingIcons(cat.ID)
	if err != nil || len(pend) == 0 {
		return
	}
	total := len(pend)
	done := 0
	// The layers lost their marks: materialise once at the end.
	defer a.rematerialiseCatalogs()
	for len(pend) > 0 {
		p := pend[0]
		emit(total-done, total, time.Time{}, "")
		_, data, ok, err := a.db.GetImage(p.imageID)
		if err != nil || !ok {
			a.clearIconPending(cat.ID, p, false)
			pend, done = pend[1:], done+1
			continue
		}
		hash := catalog.ImageSHA256(data)
		err = uploadImage(ctx, c, hash, data, time.Now().Add(time.Hour+time.Minute))
		var ra *catalog.RetryAfterError
		switch {
		case err == nil:
			// The user may have set or reset the icon meanwhile, or a
			// colleague given the record an icon: then nothing is patched
			// and the user's own icon stays theirs.
			if !a.stillPending(cat.ID, p) {
				break
			}
			if a.recordHasIcon(cat.ID, p) {
				a.clearIconPending(cat.ID, p, false)
				break
			}
			if err := a.patchRecordIcon(ctx, cat, p, hash); err != nil {
				log.Printf("catalog %s: icon for %s: %v", cat.Name, p.uuid, err)
				emit(total-done, total, time.Time{}, "an icon could not be set: "+catalogShortError(err))
				return // the next sync pass tries again
			}
			a.clearIconPending(cat.ID, p, true)
		case errors.As(err, &ra):
			until := time.Now().Add(ra.Wait)
			emit(total-done, total, until, "")
			select {
			case <-ctx.Done():
				emit(0, 0, time.Time{}, "")
				return
			case <-time.After(ra.Wait):
			}
			continue
		case catalog.ServerImageRefusal(err) != nil:
			// Stays the publisher's own icon.
			a.clearIconPending(cat.ID, p, false)
		case ctx.Err() != nil:
			emit(0, 0, time.Time{}, "")
			return
		default:
			log.Printf("catalog %s: icon upload: %v", cat.Name, err)
			emit(total-done, total, time.Time{}, "icon upload paused: "+catalogShortError(err))
			return
		}
		pend, done = pend[1:], done+1
	}
	emit(0, total, time.Time{}, "")
}

// patchRecordIcon sets icon_image on a catalog record (the built-in icon
// fields cleared in the same patch, PROTOCOL 3.6). A record someone
// changed meanwhile is refreshed and patched once more.
func (a *App) patchRecordIcon(ctx context.Context, cat *store.Catalog, p pendingIcon, hash string) error {
	patch := map[string]any{"icon_image": "sha256:" + hash, "icon_name": nil, "icon_color": nil}
	for attempt := 0; attempt < 2; attempt++ {
		rev, ok := a.catalogRecordRev(cat.ID, p.kind, p.uuid)
		if !ok {
			return nil // the record left the catalog: nothing to set
		}
		w, err := a.catalogClientPatch(ctx, cat, p.kind, p.uuid, rev, patch)
		if err == nil {
			return a.catalogApply(cat, []catalog.Item{catalog.UpsertItem(p.kind, w.Record)})
		}
		cur, mismatch := catalog.RevMismatch(err)
		if !mismatch {
			return err
		}
		if err := a.catalogApply(cat, []catalog.Item{catalog.UpsertItem(p.kind, cur)}); err != nil {
			return err
		}
	}
	return errors.New("the record keeps changing in the catalog")
}

func (a *App) catalogClientPatch(ctx context.Context, cat *store.Catalog, kind, u string, rev int64, patch map[string]any) (*catalog.WriteResult, error) {
	c, err := a.catalogClient(cat)
	if err != nil {
		return nil, err
	}
	return c.Patch(ctx, kind, u, rev, patch)
}

// catalogRecordRev is the rev of a catalog record as this machine holds it.
func (a *App) catalogRecordRev(catID, kind, u string) (int64, bool) {
	table := map[string]string{catalog.KindFolders: "folders", catalog.KindConnections: "connections"}[kind]
	if table == "" {
		return 0, false
	}
	var raw string
	err := a.db.Conn().QueryRow(`SELECT catalog_json FROM `+table+` WHERE catalog_id = ? AND remote_uuid = ? AND catalog_state <> 'removed'`, catID, u).Scan(&raw)
	if err != nil {
		return 0, false
	}
	return recordRev(raw), true
}

// clearIconPending drops the pending mark; sent also drops the layer's
// image (the record carries it now), unless the user set another icon
// meanwhile.
func (a *App) clearIconPending(catID string, p pendingIcon, sent bool) {
	layerMu.Lock()
	defer layerMu.Unlock()
	layer := map[string]any{}
	j, err := a.catalogLayerRead(catID, p.uuid)
	if err != nil {
		log.Printf("catalog: read layer: %v", err)
		return
	}
	if j != "" {
		_ = json.Unmarshal([]byte(j), &layer)
	}
	rec, _ := layer[catalog.RecordLayerKey].(map[string]any)
	if rec == nil {
		return
	}
	if id, _ := rec["icon_image_id"].(string); id != p.imageID {
		return
	}
	delete(rec, catalog.IconPendingKey)
	if sent {
		delete(rec, "icon_image_id")
	}
	if len(rec) == 0 {
		delete(layer, catalog.RecordLayerKey)
	}
	b, _ := json.Marshal(layer)
	if err := a.db.SetCatalogOverride(catID, p.uuid, p.kind, string(b)); err != nil {
		log.Printf("catalog: clear icon mark: %v", err)
	}
}

// uploadRecordImage puts a record-mode icon in the catalog's store before
// the patch that names it. Returns why not, "" when it is there.
func (a *App) uploadRecordImage(ctx context.Context, c *catalog.Client, data []byte) string {
	l := catalogImageLimits(ctx, c)
	if l == nil {
		return "this catalog does not take icon images yet"
	}
	if _, err := catalog.CheckImage(data, *l); err != nil {
		return err.Error()
	}
	err := uploadImage(ctx, c, catalog.ImageSHA256(data), data, time.Now().Add(15*time.Second))
	var ra *catalog.RetryAfterError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ra):
		return fmt.Sprintf("the catalog's upload limit; try again in %d min", int(ra.Wait.Minutes())+1)
	case catalog.ServerImageRefusal(err) != nil:
		return catalog.ServerImageRefusal(err).Message
	}
	return "icon image: " + catalogShortError(err)
}

// stillPending: the layer still holds this image, marked pending.
func (a *App) stillPending(catID string, p pendingIcon) bool {
	pend, err := a.pendingIcons(catID)
	if err != nil {
		return false
	}
	for _, x := range pend {
		if x.uuid == p.uuid && x.imageID == p.imageID {
			return true
		}
	}
	return false
}

// recordHasIcon: the catalog record carries an icon now (someone set one
// after the publish that held this image back).
func (a *App) recordHasIcon(catID string, p pendingIcon) bool {
	table := map[string]string{catalog.KindFolders: "folders", catalog.KindConnections: "connections"}[p.kind]
	if table == "" {
		return false
	}
	var raw string
	if a.db.Conn().QueryRow(`SELECT catalog_json FROM `+table+` WHERE catalog_id = ? AND remote_uuid = ?`, catID, p.uuid).Scan(&raw) != nil {
		return false
	}
	var r struct {
		IconImage *string `json:"icon_image"`
		IconName  *string `json:"icon_name"`
	}
	_ = json.Unmarshal([]byte(raw), &r)
	return (r.IconImage != nil && *r.IconImage != "") || (r.IconName != nil && *r.IconName != "")
}
