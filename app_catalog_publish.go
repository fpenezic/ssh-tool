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
	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
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
	CatalogID string `json:"catalog_id"`
	FolderID  string `json:"folder_id"`
	// ConnID publishes one connection instead of FolderID, straight into
	// TargetUUID (catalog servers from protocol revision 3).
	ConnID     string            `json:"conn_id,omitempty"`
	TargetUUID string            `json:"target_uuid"`
	SlotNames  map[string]string `json:"slot_names"`
	// Unsent: the "What to send" categories left unticked
	// (catalog.PublishCategories ids).
	Unsent []string `json:"unsent"`
	// NoSharedCreds: Bitwarden / Vaultwarden and opkssh credentials stay
	// local too. SkipCreds: credentials (local ids) not to send.
	NoSharedCreds bool     `json:"no_shared_creds"`
	SkipCreds     []string `json:"skip_creds"`
	// JumpSkip: outside bastions (connection or inventory ids) not to send
	// as a bastion record.
	JumpSkip []string `json:"jump_skip"`
	// ReviewHash: the review_hash of the plan the user reviewed; a publish
	// whose rebuilt plan differs is refused.
	ReviewHash string `json:"review_hash"`
	// Catalog-level slots (admin, protocol revision 12): SlotsAtCatalog
	// makes new slots for the whole catalog instead of the published
	// root; NoSlotMove names slot keys (cred:<id> ...) of another space
	// not to move to the catalog level and reuse.
	SlotsAtCatalog bool     `json:"slots_at_catalog"`
	NoSlotMove     []string `json:"no_slot_move"`
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
	// Stale: the folder changed after the preview; nothing was sent.
	Stale bool `json:"stale,omitempty"`
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

// catalogRevision is the server's protocol revision from a fresh meta. A
// failed read is an error, not revision 0: revision 0 would drop the
// revision-gated parts the user selected (forward bookmarks) from both the
// preview and the publish, and the matching review hashes would let that
// publish through without a word.
func catalogRevision(ctx context.Context, c *catalog.Client) (int, error) {
	r, err := c.FreshRevision(ctx)
	if err != nil {
		return 0, fmt.Errorf("catalog: read the server's protocol revision: %v", err)
	}
	return r, nil
}

// catalogIsAdmin: the signed-in user is a catalog admin, asked only where
// it matters (catalog-level slots, revision 12); false when unknown.
func catalogIsAdmin(ctx context.Context, c *catalog.Client, rev int) (bool, error) {
	if rev < catalog.CatalogSlotsRevision {
		return false, nil
	}
	w, err := c.WhoAmI(ctx)
	if err != nil {
		// Not "not an admin": the plan would quietly put slots elsewhere
		// than the dialog says.
		return false, fmt.Errorf("catalog: check whether you are an admin: %v", err)
	}
	return w.Admin, nil
}

// catalogSingleConnSupported: a single-connection publish needs a server
// from protocol revision 3; an older one refuses the request.
func catalogSingleConnSupported(ctx context.Context, c *catalog.Client, in CatalogPublishInput) error {
	if in.ConnID == "" {
		return nil
	}
	m, err := c.Meta(ctx)
	if err != nil {
		return fmt.Errorf("catalog: %v", err)
	}
	if m.Protocol.Revision < 3 {
		return fmt.Errorf("this catalog server cannot take a single connection (its protocol revision is below 3)")
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
				a.setCatalogAdmin(catalogID, w.Admin)
			}
			cancel()
		}
	}
	return out, nil
}

func (in CatalogPublishInput) options(account string) catalog.PublishOptions {
	unsent := map[string]bool{}
	for _, k := range in.Unsent {
		unsent[k] = true
	}
	skip := map[string]bool{}
	for _, id := range in.JumpSkip {
		skip[id] = true
	}
	skipCreds := map[string]bool{}
	for _, id := range in.SkipCreds {
		skipCreds[id] = true
	}
	noMove := map[string]bool{}
	for _, k := range in.NoSlotMove {
		noMove[k] = true
	}
	return catalog.PublishOptions{CatalogID: in.CatalogID, RootID: in.FolderID, ConnID: in.ConnID, TargetUUID: in.TargetUUID,
		SlotNames: in.SlotNames, AccountName: account, Unsent: unsent, JumpSkip: skip,
		NoSharedCreds: in.NoSharedCreds, SkipCreds: skipCreds,
		SlotsAtCatalog: in.SlotsAtCatalog, NoSlotMove: noMove}
}

// publishResolveDyn is a publish's view of an inventory host used as a
// bastion: what it resolves to now, as a connection. The id is a jump
// chain's reference, "dyn:<folderId>/<externalId>" (resolver.DynRef), not
// the "dyn:<entryId>" a direct connect uses: reading it as the latter found
// nothing, and every inventory bastion stayed with the publisher.
func (a *App) publishResolveDyn(id string) (store.Connection, bool) {
	syn, err := a.dynamicHostForRef(id)
	if err != nil || syn == nil {
		return store.Connection{}, false
	}
	folders, err := a.db.ListFolders()
	if err != nil {
		return store.Connection{}, false
	}
	eff := resolver.EffectiveOverrides(*syn, folders)
	return store.Connection{ID: id, Name: syn.Name, Hostname: syn.Hostname, Protocol: "ssh", Overrides: eff}, true
}

// CatalogPublishPreview builds the plan the dialog shows. Nothing is sent.
func (a *App) CatalogPublishPreview(in CatalogPublishInput) (*catalog.PublishPlan, error) {
	if err := a.catalogPublishGuard(in.CatalogID); err != nil {
		return nil, err
	}
	if in.ConnID == "" && a.isCatalogFolder(in.FolderID) {
		return nil, fmt.Errorf("this folder already belongs to a catalog")
	}
	opts := in.options("")
	opts.ResolveDyn = a.publishResolveDyn
	if cat, err := a.db.GetCatalog(in.CatalogID); err == nil {
		if c, err := a.catalogClient(cat); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			opts.Images = catalogImageLimits(ctx, c)
			rev, err := catalogRevision(ctx, c)
			if err == nil {
				opts.Revision = rev
				if opts.Admin, err = catalogIsAdmin(ctx, c, rev); err == nil {
					err = catalogSingleConnSupported(ctx, c, in)
				}
			}
			cancel()
			if err != nil {
				return nil, err
			}
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
	opts.ResolveDyn = a.publishResolveDyn
	mctx, mcancel := context.WithTimeout(context.Background(), 20*time.Second)
	opts.Images = catalogImageLimits(mctx, c)
	rev, serr := catalogRevision(mctx, c)
	if serr == nil {
		opts.Revision = rev
		if opts.Admin, serr = catalogIsAdmin(mctx, c, rev); serr == nil {
			serr = catalogSingleConnSupported(mctx, c, in)
		}
	}
	mcancel()
	if serr != nil {
		return nil, serr
	}
	plan, err := catalog.BuildPublish(a.db, opts)
	if err != nil {
		return nil, err
	}
	if len(plan.Refusals) > 0 {
		return &CatalogPublishResult{Message: "some rows cannot be published", Refusals: plan.Refusals}, nil
	}
	if in.ReviewHash == "" || in.ReviewHash != plan.ReviewHash {
		return &CatalogPublishResult{Stale: true,
			Message: "it changed since the preview; look at the review again before publishing"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute+publishImageWait)
	if err := a.uploadPublishImages(ctx, cat.ID, c, plan); err != nil {
		cancel()
		return &CatalogPublishResult{Message: "not published: " + catalogShortError(err) + ". Nothing was created and nothing changed here."}, nil
	}
	// Slots of another space this subtree reuses move to the catalog
	// level first; the publish below then refers to them in scope.
	// Each remote slot moves once (two local credentials can stand for
	// the same item), and a move that went through is applied here at
	// once, under the apply lock held above: a later failure must not
	// leave the old folder and revision to trip the next attempt.
	moved := map[string]bool{}
	for _, ps := range plan.Slots {
		if !ps.MoveToCatalog || moved[ps.UUID] {
			continue
		}
		w, err := c.Patch(ctx, catalog.KindSlots, ps.UUID, ps.Rev, map[string]any{"folder_uuid": nil})
		if err != nil {
			cancel()
			return &CatalogPublishResult{Message: "not published: moving the placeholder \"" + ps.Name + "\" to all spaces failed (" + strings.TrimRight(catalogShortError(err), ". ") + "). Nothing was created."}, nil
		}
		moved[ps.UUID] = true
		// Only the slot row: its folder and revision change no reference,
		// so nothing is rematerialised (and no external pick is touched).
		if serr := catalog.StoreSlotRecord(a.db, cat.ID, w.Record); serr != nil {
			log.Printf("catalog %s: record moved slot: %v", cat.Name, serr)
		}
	}
	// What stays done when the publish below fails.
	unchanged, movedNote := " Nothing was created and nothing changed here.", ""
	if len(moved) > 0 {
		movedNote = fmt.Sprintf(" %d placeholder(s) moved to all spaces before the publish stay there.", len(moved))
		unchanged = " Nothing was created." + movedNote
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
				Message: "no answer from the catalog (" + catalogShortError(err) + "); it stays as it is here until the next sync shows whether the publish arrived." + movedNote}, nil
		}
		if derr := a.db.DropPublishPending(cat.ID, plan.UUIDs()); derr != nil {
			log.Printf("catalog %s: publish: drop pending: %v", cat.Name, derr)
		}
		if errors.Is(err, catalog.ErrSignedOut) {
			_ = a.db.SetCatalogSyncState(cat.ID, "", nil, catSignedOutMsg)
			a.setCatalogState(cat.ID, catStateSignedOU, catSignedOutMsg)
		}
		problems := mapPublishReports(plan, catalog.PublishRecordErrors(err))
		msg := "The catalog refused the publish (" + strings.TrimRight(catalogShortError(err), ". ") + ")." + unchanged
		if len(problems) > 0 {
			msg = fmt.Sprintf("The catalog refused %d record(s), marked below.", len(problems)) + unchanged
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
			p.Name = "placeholder " + n
		}
		out = append(out, p)
	}
	return out
}
