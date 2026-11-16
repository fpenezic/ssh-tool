package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/creds"
)

// Publishing a local folder to a catalog. Started only by the user's
// click in the publish dialog (no MCP tool, no automation): the dialog
// shows the preview first, the user reviews the free text, then Publish.

// CatalogPublishTarget is a catalog folder the user may publish into.
type CatalogPublishTarget struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// CatalogPublishTargets lists them: catalog folders this user can write
// to, and whether they may create a new space (admin).
type CatalogPublishTargets struct {
	Targets        []CatalogPublishTarget `json:"targets"`
	CanCreateSpace bool                   `json:"can_create_space"`
}

// CatalogPublishInput are the dialog's choices.
type CatalogPublishInput struct {
	CatalogID   string            `json:"catalog_id"`
	FolderID    string            `json:"folder_id"`
	TargetUUID  string            `json:"target_uuid"`
	JumpInclude []string          `json:"jump_include"`
	SlotNames   map[string]string `json:"slot_names"`
	// Unsent: the "What to send" categories left unticked
	// (catalog.PublishCategories ids).
	Unsent []string `json:"unsent"`
}

// CatalogPublishProblem is one per-record error of a refused publish,
// mapped back to the local row.
type CatalogPublishProblem struct {
	Kind    string `json:"kind"`
	LocalID string `json:"local_id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CatalogPublishResult is what Publish answers. Exactly one of: Published,
// Unknown (no answer; settles on the next sync), or a refusal (Message,
// Problems; nothing was created, everything stays local).
type CatalogPublishResult struct {
	Published bool                    `json:"published"`
	Unknown   bool                    `json:"unknown"`
	Message   string                  `json:"message"`
	Problems  []CatalogPublishProblem `json:"problems,omitempty"`
	Warnings  []CatalogPublishProblem `json:"warnings,omitempty"`
	// Refusals: the local check stopped it before sending.
	Refusals []catalog.Refusal `json:"refusals,omitempty"`
	// Images: what happened to each uploaded icon (kept ones with why).
	Images []catalog.PlanImage `json:"images,omitempty"`
}

// catalogPublishAfterPost runs between the server's answer and the local
// conversion. A crash-test build (tag catalogcrashtest) exits the process
// there; nothing else sets it.
var catalogPublishAfterPost func()

func (a *App) catalogPublishGuard(catalogID string) error {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return fmt.Errorf("publish to the catalog on a desktop")
	}
	if a.vault == nil || a.vault.Status().Kind != creds.StatusUnlocked {
		return fmt.Errorf("unlock the vault first")
	}
	has, err := a.hasCatalogToken(catalogID)
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("sign in to the catalog first")
	}
	return nil
}

// CatalogPublishTargets lists where the user can publish.
func (a *App) CatalogPublishTargets(catalogID string) (*CatalogPublishTargets, error) {
	if err := a.catalogPublishGuard(catalogID); err != nil {
		return nil, err
	}
	type rec struct {
		ParentUUID *string `json:"parent_uuid"`
		Name       string  `json:"name"`
		Access     string  `json:"access"`
	}
	recs := map[string]rec{}
	err := a.db.WithTx(func(tx *sql.Tx) error {
		rows, err := a.db.CatalogRowsRawTx(tx, catalogID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Kind != catalog.KindFolders || r.State != "" {
				continue
			}
			var x rec
			if json.Unmarshal([]byte(r.RawJSON), &x) == nil {
				recs[r.RemoteUUID] = x
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := &CatalogPublishTargets{Targets: []CatalogPublishTarget{}}
	for u, r := range recs {
		if r.Access != "write" && r.Access != "manage" {
			continue
		}
		var parts []string
		for p, depth := u, 0; p != "" && depth < 64; depth++ {
			x, ok := recs[p]
			if !ok {
				break
			}
			parts = append([]string{x.Name}, parts...)
			if x.ParentUUID == nil {
				break
			}
			p = *x.ParentUUID
		}
		out.Targets = append(out.Targets, CatalogPublishTarget{UUID: u, Name: r.Name, Path: strings.Join(parts, " / ")})
	}
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].Path < out.Targets[j].Path })
	if cat, err := a.db.GetCatalog(catalogID); err == nil {
		if c, err := a.catalogClient(cat); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if w, err := c.WhoAmI(ctx); err == nil {
				out.CanCreateSpace = w.Admin
			}
			cancel()
		}
	}
	return out, nil
}

func (in CatalogPublishInput) options(account string) catalog.PublishOptions {
	inc := map[string]bool{}
	for _, id := range in.JumpInclude {
		inc[id] = true
	}
	unsent := map[string]bool{}
	for _, k := range in.Unsent {
		unsent[k] = true
	}
	return catalog.PublishOptions{CatalogID: in.CatalogID, RootID: in.FolderID, TargetUUID: in.TargetUUID,
		JumpInclude: inc, SlotNames: in.SlotNames, AccountName: account, Unsent: unsent}
}

// CatalogPublishPreview builds the plan the dialog shows. Nothing is sent.
func (a *App) CatalogPublishPreview(in CatalogPublishInput) (*catalog.PublishPlan, error) {
	if err := a.catalogPublishGuard(in.CatalogID); err != nil {
		return nil, err
	}
	if a.isCatalogFolder(in.FolderID) {
		return nil, fmt.Errorf("this folder already belongs to a catalog")
	}
	opts := in.options("")
	if cat, err := a.db.GetCatalog(in.CatalogID); err == nil {
		if c, err := a.catalogClient(cat); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			opts.Images = catalogImageLimits(ctx, c)
			cancel()
		}
	}
	return catalog.BuildPublish(a.db, opts)
}

// CatalogPublish publishes a local folder: the plan is built again from
// the same choices, recorded as pending, sent, and on success the local
// rows become the catalog rows they were published as.
func (a *App) CatalogPublish(in CatalogPublishInput) (*CatalogPublishResult, error) {
	if err := a.catalogPublishGuard(in.CatalogID); err != nil {
		return nil, err
	}
	cat, err := a.db.GetCatalog(in.CatalogID)
	if err != nil {
		return nil, err
	}
	c, err := a.catalogClient(cat)
	if err != nil {
		return nil, err
	}
	rt := a.catRT()
	rt.apply.Lock()
	defer rt.apply.Unlock()

	opts := in.options(cat.AccountName)
	mctx, mcancel := context.WithTimeout(context.Background(), 20*time.Second)
	opts.Images = catalogImageLimits(mctx, c)
	mcancel()
	plan, err := catalog.BuildPublish(a.db, opts)
	if err != nil {
		return nil, err
	}
	if len(plan.Refusals) > 0 {
		return &CatalogPublishResult{Message: "some rows cannot be published", Refusals: plan.Refusals}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute+publishImageWait)
	if err := a.uploadPublishImages(ctx, cat.ID, c, plan); err != nil {
		cancel()
		return &CatalogPublishResult{Message: "not published: " + catalogShortError(err) + ". Nothing was created and nothing changed here."}, nil
	}
	if err := a.db.RecordPublishPending(cat.ID, plan.Pending()); err != nil {
		cancel()
		return nil, err
	}
	resp, err := c.Publish(ctx, &plan.Request)
	cancel()
	if err != nil {
		log.Printf("catalog %s: publish: %v", cat.Name, err)
		if !catalog.PublishNotCreated(err) {
			return &CatalogPublishResult{Unknown: true,
				Message: "no answer from the catalog (" + catalogShortError(err) + "); the folder stays as it is until the next sync shows whether the publish arrived"}, nil
		}
		if derr := a.db.DropPublishPending(cat.ID, plan.UUIDs()); derr != nil {
			log.Printf("catalog %s: publish: drop pending: %v", cat.Name, derr)
		}
		if errors.Is(err, catalog.ErrSignedOut) {
			_ = a.db.SetCatalogSyncState(cat.ID, "", nil, catSignedOutMsg)
			a.setCatalogState(cat.ID, catStateSignedOU, catSignedOutMsg)
		}
		problems := mapPublishReports(plan, catalog.PublishRecordErrors(err))
		msg := "The catalog refused the publish (" + strings.TrimRight(catalogShortError(err), ". ") + "). Nothing was created and nothing changed here."
		if len(problems) > 0 {
			msg = fmt.Sprintf("The catalog refused %d record(s), marked below. Nothing was created and nothing changed here.", len(problems))
		}
		return &CatalogPublishResult{Message: msg, Problems: problems}, nil
	}
	if catalogPublishAfterPost != nil {
		catalogPublishAfterPost()
	}
	ext, perr := catalog.PrepareExternal(a.db, cat, a.catalogBitwardenProbe)
	if perr != nil {
		log.Printf("catalog %s: external references: %v", cat.Name, perr)
	}
	res := &CatalogPublishResult{Published: true, Warnings: mapPublishReports(plan, resp.Warnings)}
	if err := catalog.CompletePublish(a.db, plan, resp.Records, cat.AccountName, ext); err != nil {
		// The pending rows are still there (the conversion is one
		// transaction): the next sync finishes it in place.
		log.Printf("catalog %s: publish accepted, local conversion failed: %v", cat.Name, err)
		res.Message = "published; this machine finishes converting the folder on the next sync"
	} else {
		res.Message = "published"
	}
	a.recordAudit("catalog.publish", cat.ID, map[string]string{
		"records": strconv.Itoa(len(plan.Records)), "slots": strconv.Itoa(len(plan.Request.Slots)),
		"root": plan.RootUUID,
	})
	EventsEmit("catalog_changed", map[string]any{"catalog_id": cat.ID})
	res.Images = plan.Images
	// Bastion copies and the server's own fields arrive with the feed;
	// images held back by the upload limit follow after that pass.
	go func() { _ = a.catalogSyncNow(cat.ID, true) }()
	return res, nil
}

// mapPublishReports points the server's per-record reports at the local
// rows by the uuids the plan gave them.
func mapPublishReports(plan *catalog.PublishPlan, reps []catalog.RecordReport) []CatalogPublishProblem {
	if len(reps) == 0 {
		return nil
	}
	byUUID := map[string]catalog.PlanRecord{}
	for _, r := range plan.Records {
		byUUID[r.UUID] = r
	}
	slotName := map[string]string{}
	for _, s := range plan.Slots {
		slotName[s.UUID] = s.Name
	}
	out := make([]CatalogPublishProblem, 0, len(reps))
	for _, r := range reps {
		p := CatalogPublishProblem{Kind: r.Kind, Field: r.Path, Code: r.Code, Message: r.Message}
		if rec, ok := byUUID[r.UUID]; ok {
			p.LocalID, p.Name, p.Path = rec.LocalID, rec.Name, rec.Path
		} else if n, ok := slotName[r.UUID]; ok {
			p.Name = "slot " + n
		}
		out = append(out, p)
	}
	return out
}
