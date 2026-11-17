package main

// Shared connection catalogs: the app side of the catalog protocol (see
// internal/catalog). This file wires a catalog into the app: adding one,
// signing in, the background sync, slot bindings, a record's local layer,
// the "removed from catalog" actions, and the states the frontend shows.
//
// Two rules hold throughout:
//   - A browser sign-in starts only from an explicit user action (Add,
//     Sign in). A background sync that finds itself signed out records the
//     state and stops; it never opens a browser.
//   - Catalog data alone never starts anything on this machine: auto_start
//     and tunnel_autostart from a catalog are suggestions (materialise drops
//     them), the user enables them in their own layer.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/bitwarden"
	"ssh-tool/internal/catalog"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// Catalog states, as the frontend shows them on the catalog's root row.
const (
	catStateOK       = "ok"         // synced
	catStateSyncing  = "syncing"    // a pass is running
	catStateNoLogin  = "no_login"   // never signed in on this machine (rows came through profile sync)
	catStateSignedOU = "signed_out" // the server ended the session; sign in again
	catStateError    = "error"      // the last pass failed (network, server); retried
	catStateLocked   = "locked"     // the vault is locked: no token, no sync
	catStateSignIn   = "signing_in" // a browser sign-in is waiting
	catStateWaiting  = "waiting"    // waits for a newer profile to be pulled first
)

const catWaitingMsg = "another machine changed the profile; the catalog syncs after it is pulled"

// catalogRuntime is the in-memory side of all catalogs.
type catalogRuntime struct {
	mu      sync.Mutex
	clients map[string]*catalog.Client
	state   map[string]catalogStatus
	next    map[string]time.Time // earliest next background pass
	signIn  map[string]*signInJob
	// apply serialises catalog writes with a profile-sync pull (MirrorFrom
	// replaces the same rows).
	apply   sync.Mutex
	started bool
	kick    chan struct{}
}

// signInJob is a browser sign-in in progress (cancelled by a newer one, a
// sign-out or a removal).
type signInJob struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the browser sign-in returned
	once   sync.Once
}

func (j *signInJob) finished() { j.once.Do(func() { close(j.done) }) }

type catalogStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

const catSignedOutMsg = "the catalog ended this machine's session; sign in again"

// catalogOpenBrowser opens the sign-in page. Only CatalogSignIn calls it
// (a test swaps it to prove nothing else does).
var catalogOpenBrowser = func(u string) {
	// Logged so a headless (server-mode) run, or a machine whose browser
	// did not open, can still be signed in by hand. The URL carries no
	// secret: the PKCE verifier never leaves this process, and the
	// redirect goes to this machine's loopback listener, so only a
	// browser on this machine can complete the sign-in.
	log.Printf("catalog: sign in at %s", u)
	_ = BrowserOpenURL(u)
}

// errCatalogOwned is what a write to a catalog record answers in the app.
var errCatalogOwned = errors.New("this record comes from a shared catalog and is read-only here; local settings can be changed in its editor")

func (a *App) catRT() *catalogRuntime {
	a.catalogs.mu.Lock()
	defer a.catalogs.mu.Unlock()
	if a.catalogs.clients == nil {
		a.catalogs.clients = map[string]*catalog.Client{}
		a.catalogs.state = map[string]catalogStatus{}
		a.catalogs.next = map[string]time.Time{}
		a.catalogs.signIn = map[string]*signInJob{}
		a.catalogs.kick = make(chan struct{}, 1)
	}
	return &a.catalogs
}

// vaultTokens keeps one catalog's refresh token in the vault under the
// machine-scoped key (creds.CatalogTokenAccount).
type vaultTokens struct {
	a  *App
	id string
}

func (t vaultTokens) Load() (string, error) {
	v, ok, err := t.a.vault.Get(creds.CatalogTokenAccount(t.id))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return v, nil
}

func (t vaultTokens) Save(rt string) error {
	if rt == "" {
		return t.a.vault.Delete(creds.CatalogTokenAccount(t.id))
	}
	return t.a.vault.Put(creds.CatalogTokenAccount(t.id), rt)
}

func (a *App) catalogClient(cat *store.Catalog) (*catalog.Client, error) {
	rt := a.catRT()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if c, ok := rt.clients[cat.ID]; ok && c.BaseURL == strings.TrimRight(cat.BaseURL, "/") {
		return c, nil
	}
	c, err := catalog.NewClient(cat.BaseURL, "ssh-tool/"+appVersion, vaultTokens{a: a, id: cat.ID})
	if err != nil {
		return nil, err
	}
	c.DeviceName = catalogDeviceName()
	rt.clients[cat.ID] = c
	return c, nil
}

func (a *App) setCatalogState(id, state, msg string) {
	rt := a.catRT()
	rt.mu.Lock()
	prev := rt.state[id]
	rt.state[id] = catalogStatus{State: state, Error: msg}
	rt.mu.Unlock()
	if prev.State != state || prev.Error != msg {
		EventsEmit("catalog_status", map[string]any{"catalog_id": id, "state": state, "error": msg})
	}
}

// hasCatalogToken: is there a refresh token on this machine? An error is
// the locked vault.
func (a *App) hasCatalogToken(id string) (bool, error) {
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return false, bitwarden.ErrVaultLocked
	}
	v, err := vaultTokens{a: a, id: id}.Load()
	return v != "", err
}

// CatalogInfo is one catalog as the frontend lists it.
type CatalogInfo struct {
	store.Catalog
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Slots   int    `json:"slots"`
	Unbound int    `json:"unbound"`
	Removed int    `json:"removed"`
}

func (a *App) catalogInfo(c store.Catalog) CatalogInfo {
	info := CatalogInfo{Catalog: c}
	rt := a.catRT()
	rt.mu.Lock()
	st, known := rt.state[c.ID]
	rt.mu.Unlock()
	// A locked vault hides every token: that is what the user has to fix
	// first, whatever the last pass said.
	vaultLocked := a.vault == nil || a.vault.Status().Kind != creds.StatusUnlocked
	switch {
	case vaultLocked && st.State != catStateSignIn:
		info.State = catStateLocked
	case known && st.State != catStateOK && st.State != catStateError:
		info.State, info.Error = st.State, st.Error
	default:
		has, err := a.hasCatalogToken(c.ID)
		switch {
		case err != nil:
			info.State = catStateLocked
		case !has && (known && st.State == catStateSignedOU || c.LastError == catSignedOutMsg):
			info.State, info.Error = catStateSignedOU, catSignedOutMsg
		case !has:
			info.State = catStateNoLogin
		case known:
			info.State, info.Error = st.State, st.Error
		case c.LastError != "":
			info.State, info.Error = catStateError, c.LastError
		default:
			info.State = catStateOK
		}
	}
	slots, _ := a.db.ListCatalogSlots(c.ID)
	info.Slots = len(slots)
	for _, s := range a.catalogSlotInfos(c.ID, slots) {
		if s.State == "unbound" {
			info.Unbound++
		}
	}
	_ = a.db.Conn().QueryRow(`SELECT (SELECT COUNT(*) FROM connections WHERE catalog_id = ? AND catalog_state = 'removed')
		+ (SELECT COUNT(*) FROM folders WHERE catalog_id = ? AND catalog_state = 'removed')`, c.ID, c.ID).Scan(&info.Removed)
	return info
}

// CatalogList returns every attached catalog with its state.
func (a *App) CatalogList() ([]CatalogInfo, error) {
	cats, err := a.db.ListCatalogs()
	if err != nil {
		return nil, err
	}
	out := make([]CatalogInfo, 0, len(cats))
	for _, c := range cats {
		out = append(out, a.catalogInfo(c))
	}
	return out, nil
}

// CatalogAdd attaches a catalog by its base URL and starts the browser
// sign-in (an explicit user action). The catalog stays attached when the
// sign-in is cancelled or fails; Sign in tries again.
func (a *App) CatalogAdd(baseURL string) (*CatalogInfo, error) {
	u, err := catalog.ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	probe, err := catalog.NewClient(u, "ssh-tool/"+appVersion, vaultTokens{a: a, id: ""})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	meta, err := probe.Meta(ctx)
	cancel()
	if err != nil {
		return nil, err
	}
	// One attachment per server: a second one would hold the same records
	// twice (two roots, slots bound twice, two sessions), with nothing to
	// gain - another account on the same server is a sign-out and sign-in.
	cats, _ := a.db.ListCatalogs()
	for _, c := range cats {
		if catalog.SameServer(c.BaseURL, u) {
			return nil, fmt.Errorf("this catalog server is already attached as %q; to use another account there, sign out and sign in again", c.Name)
		}
	}
	name := strings.TrimSpace(meta.Server.Name)
	if name == "" {
		name = "Catalog"
	}
	cat, err := a.db.CreateCatalog(store.NewCatalog{Name: name, BaseURL: u, ServerName: meta.Server.Name, ProtocolRevision: int64(meta.Protocol.Revision)})
	if err != nil {
		return nil, err
	}
	a.recordAudit("catalog.add", cat.ID, map[string]string{"base_url": u})
	EventsEmit("catalog_changed", map[string]any{"catalog_id": cat.ID})
	info := a.catalogInfo(*cat)
	if err := a.CatalogSignIn(cat.ID); err != nil {
		info = a.catalogInfo(*cat)
		return &info, err
	}
	info = a.catalogInfo(*cat)
	return &info, nil
}

// CatalogSignIn opens the browser sign-in for a catalog and, once it
// completes, syncs. Explicit user action only. Desktop only in this
// version: a phone gets the catalog's rows through profile sync.
func (a *App) CatalogSignIn(id string) error {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return fmt.Errorf("sign in to the catalog on a desktop; this device gets its connections through profile sync")
	}
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return fmt.Errorf("unlock the vault first: the catalog sign-in is kept there")
	}
	cat, err := a.db.GetCatalog(id)
	if err != nil {
		return err
	}
	c, err := a.catalogClient(cat)
	if err != nil {
		return err
	}
	rt := a.catRT()
	ctx, cancel := context.WithCancel(context.Background())
	job := &signInJob{cancel: cancel, done: make(chan struct{})}
	rt.mu.Lock()
	if prev := rt.signIn[id]; prev != nil {
		prev.cancel()
	}
	rt.signIn[id] = job
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		if rt.signIn[id] == job { // a newer sign-in owns the entry otherwise
			delete(rt.signIn, id)
		}
		rt.mu.Unlock()
		cancel()
		job.finished()
	}()
	a.setCatalogState(id, catStateSignIn, "")
	err = c.SignIn(ctx, catalogOpenBrowser)
	job.finished()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err() // signed out or removed while it finished
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			a.setCatalogState(id, catStateNoLogin, "")
			return fmt.Errorf("sign-in cancelled")
		}
		a.setCatalogState(id, catStateNoLogin, err.Error())
		return err
	}
	return a.catalogSignedIn(cat, c, "browser")
}

// catalogSignedIn finishes a sign-in (browser or enrollment code): records
// who is signed in, starts from a full snapshot when that is someone else
// than before, and syncs.
func (a *App) catalogSignedIn(cat *store.Catalog, c *catalog.Client, via string) error {
	id := cat.ID
	w, err := c.WhoAmI(context.Background())
	if err != nil {
		a.setCatalogState(id, catStateError, err.Error())
		return err
	}
	// Someone else signed in on this profile: their view differs, start
	// from a full snapshot.
	if cat.Subject != "" && cat.Subject != w.Subject {
		_ = a.db.ResetCatalogCursor(id)
	}
	meta, _ := c.Meta(context.Background())
	server, rev := cat.ServerName, cat.ProtocolRevision
	if meta != nil {
		server, rev = meta.Server.Name, int64(meta.Protocol.Revision)
	}
	_ = a.db.UpdateCatalogAccount(id, w.Subject, w.Name, server, rev)
	_ = a.db.SetCatalogSyncState(id, "", nil, "")
	a.recordAudit("catalog.sign_in", id, map[string]string{"account": w.Name, "via": via})
	a.setCatalogState(id, catStateOK, "")
	go a.catalogSyncNow(id, true)
	return nil
}

// CatalogCancelSignIn gives up a sign-in that is waiting in the browser.
func (a *App) CatalogCancelSignIn(id string) {
	rt := a.catRT()
	rt.mu.Lock()
	j := rt.signIn[id]
	rt.mu.Unlock()
	if j != nil {
		j.cancel()
	}
}

// CatalogSignOut ends the session at the server and forgets this
// machine's token. The rows stay.
func (a *App) CatalogSignOut(id string) error {
	stopIconUploads(id)
	// A browser sign-in still waiting must not store a token afterwards.
	rt := a.catRT()
	rt.mu.Lock()
	j := rt.signIn[id]
	if j != nil {
		j.cancel()
		delete(rt.signIn, id)
	}
	rt.mu.Unlock()
	if j != nil {
		// Let it return before the tokens are cleared, so its last write
		// cannot land after the sign-out.
		select {
		case <-j.done:
		case <-time.After(10 * time.Second):
		}
	}
	cat, err := a.db.GetCatalog(id)
	if err != nil {
		return err
	}
	if c, err := a.catalogClient(cat); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := c.Revoke(ctx); err != nil {
			log.Printf("catalog %s: revoke: %v", cat.Name, err)
		}
		cancel()
	}
	_ = vaultTokens{a: a, id: id}.Save("")
	a.recordAudit("catalog.sign_out", id, nil)
	a.setCatalogState(id, catStateNoLogin, "")
	return nil
}

// CatalogRemove detaches a catalog: signs out and deletes its rows, slots,
// bindings, local layers and the credentials made for it.
func (a *App) CatalogRemove(id string) error {
	_ = a.CatalogSignOut(id)
	rt := a.catRT()
	rt.apply.Lock()
	err := a.db.WithCatalogWriter(func(tx *sql.Tx) error { return a.db.DeleteCatalogTx(tx, id) })
	rt.apply.Unlock()
	if err != nil {
		return err
	}
	a.forgetCatalog(id)
	a.recordAudit("catalog.remove", id, nil)
	EventsEmit("catalog_changed", map[string]any{"catalog_id": id})
	return nil
}

// CatalogSetConfig renames a catalog or changes its sync interval.
func (a *App) CatalogSetConfig(id, name string, intervalSec int64) error {
	if err := a.db.UpdateCatalogConfig(id, name, intervalSec); err != nil {
		return err
	}
	EventsEmit("catalog_changed", map[string]any{"catalog_id": id})
	return nil
}

// CatalogSyncNow runs a pass now (the user's "Sync now").
func (a *App) CatalogSyncNow(id string) error {
	return a.catalogSyncNow(id, false)
}

// catalogBitwardenProbe asks a registered Bitwarden server's vault whether
// it holds an item.
func (a *App) catalogBitwardenProbe(serverID, cipherID string) (bool, error) {
	if a.bitwarden == nil {
		return false, fmt.Errorf("bitwarden not ready")
	}
	_, _, err := a.bitwarden.Resolve(store.BitwardenRef{ServerID: serverID, CipherID: cipherID, Field: "password"})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, bitwarden.ErrCipherNotFound) {
		return false, nil
	}
	if strings.Contains(err.Error(), "field") {
		return true, nil // the item is there, it just has no password
	}
	return false, err
}

// catalogSyncNow runs one pass for a catalog. background=true is the
// scheduler: it never starts a sign-in, it records the state and stops.
func (a *App) catalogSyncNow(id string, background bool) error {
	cat, err := a.db.GetCatalog(id)
	if err != nil {
		return err
	}
	has, err := a.hasCatalogToken(id)
	if err != nil {
		a.setCatalogState(id, catStateLocked, "")
		return fmt.Errorf("unlock the vault first")
	}
	if !has {
		rt := a.catRT()
		rt.mu.Lock()
		st := rt.state[id].State
		rt.mu.Unlock()
		if st != catStateSignedOU && cat.LastError != catSignedOutMsg {
			a.setCatalogState(id, catStateNoLogin, "")
		}
		return catalog.ErrSignedOut
	}
	if a.profileRemoteAhead() {
		// A machine of this user may have published a folder this
		// machine still holds locally: the profile pull converts it,
		// a catalog pass first would add the records as copies.
		a.setCatalogState(id, catStateWaiting, catWaitingMsg)
		rt := a.catRT()
		rt.mu.Lock()
		rt.next[id] = time.Now().Add(time.Minute)
		rt.mu.Unlock()
		return errors.New(catWaitingMsg)
	}
	c, err := a.catalogClient(cat)
	if err != nil {
		return err
	}
	a.setCatalogState(id, catStateSyncing, "")
	rt := a.catRT()
	rt.apply.Lock()
	ext, perr := catalog.PrepareExternal(a.db, cat, a.catalogBitwardenProbe)
	if perr != nil {
		log.Printf("catalog %s: external references: %v", cat.Name, perr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	res, err := catalog.Sync(ctx, a.db, cat, c, ext)
	cancel()
	if err == nil && res.Upserted+res.Removed > 0 {
		// The slots may have changed: prepare external references again
		// for what arrived, and materialise with them.
		if ext2, perr := catalog.PrepareExternal(a.db, cat, a.catalogBitwardenProbe); perr == nil {
			err = catalog.Materialise(a.db, id, ext2)
		}
	}
	rt.apply.Unlock()
	if err == nil {
		a.catalogImagesPass(cat, c)
	}
	interval := time.Duration(cat.SyncIntervalSec) * time.Second
	if interval < time.Minute {
		interval = time.Minute
	}
	next := time.Now().Add(interval)
	var ra *catalog.RetryAfterError
	switch {
	case err == nil:
		_ = a.db.SetCatalogSyncState(id, "", nil, "")
		a.setCatalogState(id, catStateOK, "")
		if res.Upserted+res.Removed+res.Kept > 0 || res.Reset {
			EventsEmit("catalog_changed", map[string]any{"catalog_id": id, "result": res})
		}
	case errors.Is(err, catalog.ErrSignedOut):
		// Kept in last_error too, so "signed out" survives a restart
		// instead of turning into "never signed in".
		_ = a.db.SetCatalogSyncState(id, "", nil, catSignedOutMsg)
		a.setCatalogState(id, catStateSignedOU, catSignedOutMsg)
		a.recordAudit("catalog.signed_out", id, nil)
	case errors.As(err, &ra):
		next = time.Now().Add(ra.Wait)
		a.setCatalogState(id, catStateError, catalogShortError(err))
	default:
		short := catalogShortError(err)
		_ = a.db.SetCatalogSyncState(id, "", nil, short)
		a.setCatalogState(id, catStateError, short)
	}
	rt.mu.Lock()
	rt.next[id] = next
	rt.mu.Unlock()
	if err != nil {
		log.Printf("catalog %s: sync: %v", cat.Name, err)
	}
	return err
}

// catalogImagesPass fetches the icon images the catalog's records name and
// this machine lacks, materialises again when any arrived, and drops
// cached images nothing uses any more. Failures only log: the rows show
// the default icon and the next pass tries again.
func (a *App) catalogImagesPass(cat *store.Catalog, c *catalog.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	n, err := catalog.FetchImages(ctx, a.db, cat.ID, c)
	if err != nil {
		log.Printf("catalog %s: images: %v", cat.Name, err)
	}
	if n > 0 {
		rt := a.catRT()
		rt.apply.Lock()
		ext, _ := catalog.PrepareExternal(a.db, cat, nil)
		if err := catalog.Materialise(a.db, cat.ID, ext); err != nil {
			log.Printf("catalog %s: materialise: %v", cat.Name, err)
		}
		rt.apply.Unlock()
		EventsEmit("catalog_changed", map[string]any{"catalog_id": cat.ID})
	}
	if _, err := catalog.PruneImages(a.db); err != nil {
		log.Printf("catalog: prune images: %v", err)
	}
	a.startIconUploads(cat, c)
}

// startCatalogScheduler runs the background passes. A catalog is synced
// when its interval is up, the vault is open and this machine holds a
// token; a signed-out catalog waits for the user.
func (a *App) startCatalogScheduler() {
	rt := a.catRT()
	rt.mu.Lock()
	if rt.started {
		rt.mu.Unlock()
		return
	}
	rt.started = true
	kick := rt.kick
	rt.mu.Unlock()
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		first := time.After(15 * time.Second)
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-first:
			case <-tick.C:
			case <-kick:
			}
			a.catalogSchedulerPass()
		}
	}()
}

func (a *App) catalogSchedulerPass() {
	if a.db == nil || a.vault == nil || a.vault.Status().Kind != creds.StatusUnlocked {
		return
	}
	cats, err := a.db.ListCatalogs()
	if err != nil {
		return
	}
	rt := a.catRT()
	for _, c := range cats {
		rt.mu.Lock()
		next, st := rt.next[c.ID], rt.state[c.ID].State
		signing := rt.signIn[c.ID] != nil
		rt.mu.Unlock()
		if signing || st == catStateSignedOU || st == catStateSyncing || time.Now().Before(next) {
			continue
		}
		if has, err := a.hasCatalogToken(c.ID); err != nil || !has {
			continue
		}
		_ = a.catalogSyncNow(c.ID, true)
	}
}

// catalogVaultLocked: every catalog shows "vault locked" until unlock.
func (a *App) catalogVaultLocked() {
	cats, err := a.db.ListCatalogs()
	if err != nil {
		return
	}
	for _, c := range cats {
		a.setCatalogState(c.ID, catStateLocked, "")
	}
}

// catalogAfterProfilePull: a catalog that waited for the profile syncs
// now.
func (a *App) catalogAfterProfilePull() {
	rt := a.catRT()
	rt.mu.Lock()
	woke := false
	for id, st := range rt.state {
		if st.State == catStateWaiting {
			delete(rt.state, id)
			delete(rt.next, id)
			woke = true
		}
	}
	rt.mu.Unlock()
	if woke {
		select {
		case rt.kick <- struct{}{}:
		default:
		}
	}
}

// catalogVaultUnlocked: the tokens are readable now; sync without waiting
// for the next tick.
func (a *App) catalogVaultUnlocked() {
	rt := a.catRT()
	rt.mu.Lock()
	for id, st := range rt.state {
		if st.State == catStateLocked {
			delete(rt.state, id)
		}
	}
	for id := range rt.next {
		delete(rt.next, id)
	}
	rt.mu.Unlock()
	EventsEmit("catalog_changed", map[string]any{})
	select {
	case rt.kick <- struct{}{}:
	default:
	}
}

// rematerialiseCatalogs re-translates every catalog after something its
// references point at changed (a credential, network profile or connection
// deleted, a binding or layer set).
func (a *App) rematerialiseCatalogs() {
	cats, err := a.db.ListCatalogs()
	if err != nil || len(cats) == 0 {
		return
	}
	rt := a.catRT()
	rt.apply.Lock()
	defer rt.apply.Unlock()
	for _, c := range cats {
		c := c
		ext, err := catalog.PrepareExternal(a.db, &c, nil)
		if err != nil {
			log.Printf("catalog %s: external references: %v", c.Name, err)
		}
		if err := catalog.Materialise(a.db, c.ID, ext); err != nil {
			log.Printf("catalog %s: materialise: %v", c.Name, err)
		}
	}
	EventsEmit("catalog_changed", map[string]any{})
}

// Slots ------------------------------------------------------------------

// CatalogSlotInfo is a slot with what it is bound to on this machine.
type CatalogSlotInfo struct {
	store.CatalogSlot
	// State: "bound" (to TargetID), "external" (resolved through a
	// Bitwarden server), "unbound".
	State      string `json:"state"`
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
	// TargetGone: bound to something since deleted (counts as unbound).
	TargetGone bool `json:"target_gone,omitempty"`
	// UsedBy: how many catalog records name the slot here.
	UsedBy int `json:"used_by"`
	// CanWrite: this user may rename or delete it; FolderName: where it
	// is defined.
	CanWrite   bool   `json:"can_write"`
	FolderName string `json:"folder_name"`
	// Skipped: an unbound slot whose one-time code the user types
	// themselves on this machine (the connect dialog's skip).
	Skipped bool `json:"skipped,omitempty"`
}

func (a *App) catalogSlotInfos(catalogID string, slots []store.CatalogSlot) []CatalogSlotInfo {
	bindings, _ := a.db.ListCatalogBindings(catalogID)
	bound := map[string]store.CatalogBinding{}
	for _, b := range bindings {
		bound[b.SlotUUID] = b
	}
	skipped := a.otpSkipped()
	out := make([]CatalogSlotInfo, 0, len(slots))
	for _, s := range slots {
		info := CatalogSlotInfo{CatalogSlot: s, State: "unbound", Skipped: skipped[catalogID+"/"+s.UUID]}
		if ok, lid := a.slotFolderWritable(catalogID, s.FolderUUID); lid != "" {
			info.CanWrite = ok && runtime.GOOS != "android" && runtime.GOOS != "ios"
			if f, err := a.db.GetFolder(lid); err == nil {
				info.FolderName = f.Name
			}
		}
		if b, ok := bound[s.UUID]; ok {
			info.TargetID = b.TargetID
			name, ok := a.catalogTargetName(b.Kind, b.TargetID)
			if ok {
				info.State, info.TargetName = "bound", name
			} else {
				info.TargetGone = true
			}
		} else if s.Kind == "external" {
			id := store.CatalogCredentialID(catalogID, s.UUID)
			if cred, err := a.db.GetCredential(id); err == nil {
				info.State, info.TargetID, info.TargetName = "external", id, cred.Name
			}
		}
		_ = a.db.Conn().QueryRow(`SELECT (SELECT COUNT(*) FROM connections WHERE catalog_id = ? AND instr(catalog_json, ?) > 0)
			+ (SELECT COUNT(*) FROM folders WHERE catalog_id = ? AND instr(catalog_json, ?) > 0)`,
			catalogID, s.UUID, catalogID, s.UUID).Scan(&info.UsedBy)
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (a *App) catalogTargetName(kind, id string) (string, bool) {
	switch kind {
	case "cred":
		if c, err := a.db.GetCredential(id); err == nil {
			return c.Name, true
		}
	case "net":
		if p, err := a.db.GetNetworkProfile(id); err == nil {
			return p.Name, true
		}
	case "jump":
		if c, err := a.db.GetConnection(id); err == nil {
			return c.Name, true
		}
		if strings.HasPrefix(id, "dyn:") {
			return id, true
		}
	}
	return "", false
}

// CatalogSlots lists a catalog's slots with their bindings.
func (a *App) CatalogSlots(catalogID string) ([]CatalogSlotInfo, error) {
	slots, err := a.db.ListCatalogSlots(catalogID)
	if err != nil {
		return nil, err
	}
	return a.catalogSlotInfos(catalogID, slots), nil
}

// CatalogBind binds a slot to a credential, network profile or connection
// of the user's own (the slot's type decides which).
func (a *App) CatalogBind(catalogID, slotUUID, targetID string) error {
	slots, err := a.db.ListCatalogSlots(catalogID)
	if err != nil {
		return err
	}
	var slot *store.CatalogSlot
	for i := range slots {
		if slots[i].UUID == slotUUID {
			slot = &slots[i]
		}
	}
	if slot == nil {
		return fmt.Errorf("no such slot in this catalog")
	}
	if _, ok := a.catalogTargetName(slot.Type, targetID); !ok {
		return fmt.Errorf("nothing of that kind with id %q", targetID)
	}
	// A jump slot may name a bastion from the same catalog; a loop is
	// caught by the jump expansion at connect time.
	if err := a.db.SetCatalogBinding(catalogID, slotUUID, slot.Type, targetID); err != nil {
		return err
	}
	_ = a.setOTPSkipped(catalogID, slotUUID, false)
	a.recordAudit("catalog.bind", catalogID, map[string]string{"slot": slot.Name})
	a.rematerialiseCatalogs()
	return nil
}

// CatalogUnbind removes a slot's binding.
func (a *App) CatalogUnbind(catalogID, slotUUID string) error {
	if err := a.db.DeleteCatalogBinding(catalogID, slotUUID); err != nil {
		return err
	}
	a.rematerialiseCatalogs()
	return nil
}

// Records and their local layer ------------------------------------------

// CatalogRecordInfo is what the editor needs about a catalog row.
type CatalogRecordInfo struct {
	CatalogID   string `json:"catalog_id"`
	CatalogName string `json:"catalog_name"`
	RemoteUUID  string `json:"remote_uuid"`
	Removed     bool   `json:"removed"`
	Skeleton    bool   `json:"skeleton"`
	DocMD       string `json:"doc_md"`
	UpdatedBy   string `json:"updated_by"`
	UpdatedAt   string `json:"updated_at"`
	// Catalog: the record's settings translated to local ids, without
	// the user's layer (the editor's placeholders and "reset to catalog").
	Catalog store.InheritableSettings `json:"catalog"`
	// Layer: the user's local layer (fields set here override Catalog).
	Layer json.RawMessage `json:"layer"`
	// SuggestedAutoStart: a catalog forward the catalog suggests starting
	// automatically (shown, never acted on).
	SuggestedAutoStart bool `json:"suggested_auto_start,omitempty"`
	// SuggestedTunnelAutostart: the same for a tunnel connection.
	SuggestedTunnelAutostart bool `json:"suggested_tunnel_autostart,omitempty"`
	// Record mode (writers): whether this user may change the record,
	// the folder it reaches readers of, the revision and the record as
	// the editor edits it.
	CanWrite   bool              `json:"can_write"`
	FolderName string            `json:"folder_name"`
	Rev        int64             `json:"rev"`
	Form       *catalog.EditForm `json:"form,omitempty"`
	// Images: the catalog takes icon images (record mode offers the
	// image library).
	Images bool `json:"images,omitempty"`
}

func (a *App) catalogRow(kind, id string) (catID, uuid, state, raw string, err error) {
	table := map[string]string{"folders": "folders", "connections": "connections", "forwards": "port_forwards"}[kind]
	if table == "" {
		return "", "", "", "", fmt.Errorf("unknown record kind %q", kind)
	}
	var c, u sql.NullString
	err = a.db.Conn().QueryRow(`SELECT catalog_id, remote_uuid, catalog_state, catalog_json FROM `+table+` WHERE id = ?`, id).Scan(&c, &u, &state, &raw)
	if err != nil {
		return "", "", "", "", err
	}
	if !c.Valid || !u.Valid {
		return "", "", "", "", fmt.Errorf("not a catalog record")
	}
	return c.String, u.String, state, raw, nil
}

// CatalogRecord describes a catalog row for its editor.
func (a *App) CatalogRecord(kind, id string) (*CatalogRecordInfo, error) {
	catID, uuid, state, raw, err := a.catalogRow(kind, id)
	if err != nil {
		return nil, err
	}
	cat, err := a.db.GetCatalog(catID)
	if err != nil {
		return nil, err
	}
	info := &CatalogRecordInfo{CatalogID: catID, CatalogName: cat.Name, RemoteUUID: uuid, Removed: state == "removed", Layer: json.RawMessage("{}")}
	var rec struct {
		Settings  json.RawMessage `json:"settings"`
		Access    string          `json:"access"`
		DocMD     *string         `json:"doc_md"`
		UpdatedBy string          `json:"updated_by"`
		UpdatedAt string          `json:"updated_at"`
		AutoStart bool            `json:"auto_start"`
	}
	_ = json.Unmarshal([]byte(raw), &rec)
	info.Skeleton = rec.Access == "skeleton"
	if rec.DocMD != nil {
		info.DocMD = *rec.DocMD
	}
	info.UpdatedBy, info.UpdatedAt = rec.UpdatedBy, rec.UpdatedAt
	info.SuggestedAutoStart = kind == "forwards" && rec.AutoStart
	var s map[string]any
	if json.Unmarshal(rec.Settings, &s) == nil {
		if v, ok := s["tunnel_autostart"].(bool); ok && v {
			info.SuggestedTunnelAutostart = true
		}
	}
	if kind != "forwards" {
		base, err := catalog.BaseSettings(a.db, catID, rec.Settings)
		if err == nil {
			info.Catalog = base
		}
	}
	overrides, _ := a.db.ListCatalogOverrides(catID)
	for _, o := range overrides {
		if o.RemoteUUID == uuid {
			info.Layer = json.RawMessage(o.JSON)
		}
	}
	info.Rev = recordRev(raw)
	if fid, _, fraw, err := a.db.CatalogFolderOf(kind, id); err == nil {
		info.CanWrite = state == "" && catalogWritable(fraw) && runtime.GOOS != "android" && runtime.GOOS != "ios"
		if f, err := a.db.GetFolder(fid); err == nil {
			info.FolderName = f.Name
		}
	}
	if info.CanWrite {
		if f, err := catalog.FormOf(a.db, catID, kind, json.RawMessage(raw)); err == nil {
			info.Form = &f
		}
		if c, err := a.catalogClient(cat); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			info.Images = catalogImageLimits(ctx, c) != nil
			cancel()
		}
	}
	return info, nil
}

// CatalogSetLayer replaces a catalog record's local layer (sparse
// InheritableSettings JSON for a folder or connection, {"local_port": n}
// for a forward; "{}" clears it) and materialises.
func (a *App) CatalogSetLayer(kind, id, layerJSON string) error {
	catID, uuid, _, raw, err := a.catalogRow(kind, id)
	if err != nil {
		return err
	}
	if kind == "folders" {
		var rec struct {
			Access string `json:"access"`
		}
		_ = json.Unmarshal([]byte(raw), &rec)
		if rec.Access == "skeleton" {
			return fmt.Errorf("this folder is only an outline of a folder you cannot open; it takes no local settings")
		}
	}
	t := strings.TrimSpace(layerJSON)
	if t == "" {
		t = "{}"
	}
	if kind == "forwards" {
		var l struct {
			LocalPort *uint16 `json:"local_port"`
		}
		if err := json.Unmarshal([]byte(t), &l); err != nil {
			return fmt.Errorf("forward layer: %w", err)
		}
		if l.LocalPort != nil {
			t = fmt.Sprintf(`{"local_port":%d}`, *l.LocalPort)
		} else {
			t = "{}"
		}
	} else {
		var s store.InheritableSettings
		if err := json.Unmarshal([]byte(t), &s); err != nil {
			return fmt.Errorf("local settings: %w", err)
		}
		b, _ := json.Marshal(s)
		t = string(b)
	}
	if kind != "forwards" {
		t = a.keepRecordLayer(catID, uuid, t)
	}
	if err := a.db.SetCatalogOverride(catID, uuid, kind, t); err != nil {
		return err
	}
	a.rematerialiseCatalogs()
	return nil
}

// saveCatalogConnectionEdit routes an edit of a catalog connection's
// settings into its local layer: the fields that differ from the catalog's
// value become the layer. Anything else the edit changes that the catalog
// owns is refused.
func (a *App) saveCatalogConnectionEdit(existing *store.Connection, in store.UpdateConnection) (*store.Connection, error) {
	owned := in.Name != nil && *in.Name != existing.Name ||
		in.Hostname != nil && strings.TrimSpace(*in.Hostname) != existing.Hostname ||
		in.Notes != nil && *in.Notes != existing.Notes ||
		in.Tags != nil && strings.Join(*in.Tags, "\x00") != strings.Join(existing.Tags, "\x00") ||
		in.FolderID != nil && (existing.FolderID == nil || *in.FolderID != *existing.FolderID) || in.ClearFolder ||
		in.Protocol != nil && *in.Protocol != "" && *in.Protocol != existing.Protocol ||
		in.SortOrder != nil && *in.SortOrder != existing.SortOrder
	if owned {
		return nil, errCatalogOwned
	}
	// Local columns go straight through.
	local := store.UpdateConnection{ID: existing.ID, Favorite: in.Favorite, OpenHidden: in.OpenHidden}
	if in.Favorite != nil || in.OpenHidden != nil {
		if _, err := a.db.UpdateConnection(local); err != nil {
			return nil, err
		}
	}
	if in.Overrides != nil {
		catID, uuid, _, raw, err := a.catalogRow("connections", existing.ID)
		if err != nil {
			return nil, err
		}
		var rec struct {
			Settings json.RawMessage `json:"settings"`
		}
		_ = json.Unmarshal([]byte(raw), &rec)
		base, err := catalog.BaseSettings(a.db, catID, rec.Settings)
		if err != nil {
			return nil, err
		}
		layer := catalog.LayerFromEdit(base, *in.Overrides)
		b, _ := json.Marshal(layer)
		if err := a.db.SetCatalogOverride(catID, uuid, "connections", a.keepRecordLayer(catID, uuid, string(b))); err != nil {
			return nil, err
		}
		a.rematerialiseCatalogs()
	}
	return a.db.GetConnection(existing.ID)
}

// updateConnection is every app path's UpdateConnection: a catalog row's
// settings go to its local layer, its owned fields are refused.
func (a *App) updateConnection(in store.UpdateConnection) (*store.Connection, error) {
	existing, err := a.db.GetConnection(in.ID)
	if err != nil {
		return nil, err
	}
	if existing.CatalogID == nil {
		if in.FolderID != nil && a.isCatalogFolder(*in.FolderID) {
			return nil, errCatalogFolder
		}
		return a.db.UpdateConnection(in)
	}
	if existing.CatalogState == "removed" && (in.Overrides != nil) {
		return nil, fmt.Errorf("this connection left its catalog; keep it as a local connection to change it")
	}
	return a.saveCatalogConnectionEdit(existing, in)
}

// errCatalogFolder: nothing local goes inside a catalog folder.
var errCatalogFolder = errors.New("a catalog folder holds only the catalog's records; put your own connections and folders elsewhere")

func (a *App) isCatalogFolder(id string) bool {
	if id == "" {
		return false
	}
	f, err := a.db.GetFolder(id)
	return err == nil && f.CatalogID != nil
}

func (a *App) isCatalogConnection(id string) bool {
	c, err := a.db.GetConnection(id)
	return err == nil && c.CatalogID != nil
}

// CatalogKeepLocal turns a connection, or a folder with everything below
// it, that left its catalog into local rows.
func (a *App) CatalogKeepLocal(kind, id string) error {
	rt := a.catRT()
	rt.apply.Lock()
	var err error
	switch kind {
	case "connections":
		err = catalog.KeepConnectionLocal(a.db, id)
	case "folders":
		err = catalog.KeepFolderLocal(a.db, id)
	default:
		err = fmt.Errorf("unknown record kind %q", kind)
	}
	rt.apply.Unlock()
	if err == nil {
		EventsEmit("catalog_changed", map[string]any{})
	}
	return err
}

// CatalogDeleteRemoved deletes a connection or folder that left its
// catalog.
func (a *App) CatalogDeleteRemoved(kind, id string) error {
	rt := a.catRT()
	rt.apply.Lock()
	err := a.db.DeleteRemovedCatalogRow(kind, id)
	rt.apply.Unlock()
	if err == nil {
		EventsEmit("catalog_changed", map[string]any{})
	}
	return err
}

// ForwardsSetDisabled switches a forward off or on (catalog forwards
// cannot be deleted, only disabled).
func (a *App) ForwardsSetDisabled(id string, disabled bool) error {
	return a.db.SetPortForwardDisabled(id, disabled)
}

// updateCatalogForward applies an edit of a catalog forward: auto start and
// browser mode are the user's own columns, a local port goes to the
// forward's local layer (the published one may be taken here), anything
// else the catalog owns is refused.
func (a *App) updateCatalogForward(cur *store.PortForward, in ForwardUpdateInput) (*store.PortForward, error) {
	sameStr := func(p *string, v *string) bool { return p == nil || (v != nil && *p == *v) }
	sameU16 := func(p *uint16, v *uint16) bool { return p == nil || (v != nil && *p == *v) }
	if !sameStr(in.LocalAddr, cur.LocalAddr) || in.ClearLocalAddr ||
		!sameStr(in.RemoteHost, cur.RemoteHost) || in.ClearRemoteHost ||
		!sameU16(in.RemotePort, cur.RemotePort) || in.ClearRemotePort ||
		(in.Description != nil && *in.Description != cur.Description) {
		return nil, errCatalogOwned
	}
	if in.AutoStart != nil || in.BrowserMode != nil {
		if _, err := a.db.UpdatePortForward(store.UpdatePortForward{ID: cur.ID, AutoStart: in.AutoStart, BrowserMode: in.BrowserMode}); err != nil {
			return nil, err
		}
	}
	if (in.LocalPort != nil && !sameU16(in.LocalPort, cur.LocalPort)) || in.ClearLocalPort {
		layer := "{}"
		if in.LocalPort != nil && !in.ClearLocalPort {
			layer = fmt.Sprintf(`{"local_port":%d}`, *in.LocalPort)
		}
		if err := a.CatalogSetLayer("forwards", cur.ID, layer); err != nil {
			return nil, err
		}
	}
	return a.db.GetPortForward(cur.ID)
}

// errForwardDisabled: a disabled forward starts neither automatically nor
// by hand.
var errForwardDisabled = errors.New("this forward is disabled; enable it first")

// catalogShortError is the one line the frontend shows for a failed pass;
// the full error (with the request URL) stays in the log.
func catalogShortError(err error) string {
	var ae *catalog.APIError
	var ra *catalog.RetryAfterError
	var ue *url.Error
	switch {
	case errors.As(err, &ra):
		return fmt.Sprintf("the catalog server is busy; retrying in %s", ra.Wait.Round(time.Second))
	case errors.As(err, &ae):
		if ae.Message != "" {
			return "the catalog server refused the request: " + ae.Message
		}
		return fmt.Sprintf("the catalog server answered HTTP %d", ae.Status)
	case errors.As(err, &ue):
		if ue.Timeout() {
			return "the catalog server did not answer in time"
		}
		return "cannot reach the catalog server"
	case errors.Is(err, context.DeadlineExceeded):
		return "the sync took too long and was stopped; it will be retried"
	}
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160] + "..."
	}
	return msg
}

// saveCatalogFolderEdit routes an edit of a catalog folder: its settings
// become its local layer (the fields that differ from the catalog's), its
// name, place and order stay the catalog's. A skeleton folder and a
// catalog's root take no settings.
func (a *App) saveCatalogFolderEdit(in FoldersUpdateInput) (*store.Folder, error) {
	f, err := a.db.GetFolder(in.ID)
	if err != nil {
		return nil, err
	}
	if f.RemoteUUID == nil || f.CatalogSkeleton {
		return nil, errCatalogOwned
	}
	if (in.Name != nil && *in.Name != f.Name) || in.ParentID != nil || in.ClearParent ||
		(in.SortOrder != nil && *in.SortOrder != f.SortOrder) {
		return nil, errCatalogOwned
	}
	if in.Settings != nil {
		catID, uuid, state, raw, err := a.catalogRow("folders", f.ID)
		if err != nil {
			return nil, err
		}
		if state == "removed" {
			return nil, fmt.Errorf("this folder left its catalog; keep it as a local folder to change it")
		}
		var rec struct {
			Settings json.RawMessage `json:"settings"`
		}
		_ = json.Unmarshal([]byte(raw), &rec)
		base, err := catalog.BaseSettings(a.db, catID, rec.Settings)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(catalog.LayerFromEdit(base, *in.Settings))
		if err := a.db.SetCatalogOverride(catID, uuid, "folders", a.keepRecordLayer(catID, uuid, string(b))); err != nil {
			return nil, err
		}
		a.rematerialiseCatalogs()
	}
	return a.db.GetFolder(f.ID)
}
