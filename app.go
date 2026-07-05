package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/backup"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/exporter"
	"ssh-tool/internal/httpc"
	"ssh-tool/internal/importer/mobaxterm"
	"ssh-tool/internal/importer/puttyreg"
	"ssh-tool/internal/importer/rdm"
	"ssh-tool/internal/importer/sshconfig"
	"ssh-tool/internal/inventory"
	"ssh-tool/internal/local"
	"ssh-tool/internal/recorder"
	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/syncer"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/updater"
	"ssh-tool/internal/wg"
)

// App is the root service exposed to the frontend.
type App struct {
	// ctx is preserved for code that still expects a context (e.g.
	// graceful background goroutine shutdown). v3 hands us one in
	// ServiceStartup.
	ctx context.Context
	// app is the v3 application handle. main.go writes this immediately
	// after constructing the App so multi-window calls can reach it.
	app         *application.App
	db          *store.DB
	vault       *creds.Vault
	credSvc     *creds.Service
	pool        *sshlayer.Pool
	forwards    *sshlayer.ForwardPool
	localPool   *local.Pool
	inventory   *inventory.Manager
	backupSched *backup.Scheduler
	wgman       *wg.Manager
	// nbman runs NetBird (and future helper-backed) tunnels via the
	// ssh-tool-netbird sidecar. Kept beside wgman; app_network.go
	// dispatches by profile kind.
	nbman *tunnelhelper.Manager

	// Tunnel idle-stop accounting: which sessions dialed through
	// which network profile, and the pending linger timers. See
	// wgAcquire / wgRelease in app_network.go.
	wgSessMu      sync.Mutex
	wgSessProfile map[string]string      // sessionID -> profileID
	wgStopTimers  map[string]*time.Timer // profileID -> linger stop

	metaMu      sync.Mutex
	sessionMeta map[string]sessionMetaEntry

	pendingHostKeysMu sync.Mutex
	pendingHostKeys   map[string]chan bool

	transfersMu sync.Mutex
	transfers   map[string]chan struct{} // transferID -> cancel channel

	reconnectMu sync.Mutex
	// reconnects: maps the OLD sessionID of a dropped session to its
	// cancel channel. Closing the channel aborts the retry loop.
	reconnects map[string]chan struct{}

	// connectCancels maps a connect key (connectionID, or a dynamic
	// entry key) to the in-flight connect's cancel handle. SshCancelConnect
	// calls it to abort a connect that's hung on opkssh OIDC login (browser
	// closed / wrong config) without restarting the app. The value is a
	// pointer so a superseding attempt can be told apart from the one it
	// replaced (CancelFuncs aren't comparable). Guarded by connectCancelsMu.
	connectCancelsMu sync.Mutex
	connectCancels   map[string]*connectCancel

	pendingTabDragMu sync.Mutex
	pendingTabDrag   *TabDragPayload

	// updateMu guards the update pipeline state. The asset (URL +
	// manifest sha256) is captured by CheckForUpdate and the apply
	// script path by DownloadUpdate, so the frontend never feeds us
	// a URL to fetch or a script to execute - the webview only says
	// "go" and the backend acts on what it derived itself.
	updateMu          sync.Mutex
	updateAssetURL    string
	updateAssetSHA256 string
	updateApplyScript string

	// debugBufMu guards the per-connect debug ring buffer. Every
	// EmitDebug call is appended under the connectionID. Dropped when
	// the next attempt against the same connectionID starts (so the
	// buffer is "the last failed attempt's diagnostics"), and on
	// successful connect after a short delay (Terminal has time to
	// mount and subscribe live).
	debugBufMu sync.Mutex
	debugBuf   map[string][]string // connectionID -> lines (capped)

	// logBuf is the in-app log ring (wired from main.go after
	// log.SetOutput). Frontend reads it via AppGetLogs and listens
	// for "app_log" events for the live tail.
	logBuf *logBuffer

	// broadcastMu guards the broadcast groups. Groups live on the
	// backend (not the frontend) so all windows see the same state -
	// detached windows, multi-window detach/redock, and fan-out from
	// any pane all share one source of truth. A "broadcast_changed"
	// event with the full group->members map is emitted after every
	// mutation.
	//
	// broadcastGroups[""] is the legacy default group, kept so the
	// existing single-set frontend store keeps working without
	// changes. Multi-group additions live under named keys; FanOut
	// walks every group the origin belongs to.
	broadcastMu     sync.Mutex
	broadcastGroups map[string]map[string]bool

	// detachedSessions maps a detached window's Name -> sessionIDs
	// that landed in it. WindowClosing fires SshDisconnect for the
	// listed sessions so the main UI doesn't leave them as ghost
	// green entries after the window's gone. Redock clears the
	// slot before close so the disconnect is a no-op there.
	detachedMu       sync.Mutex
	detachedSessions map[string][]string

	// Auto-sync loop state (sync_auto.go). lastRemoteCheck paces the
	// periodic "is the remote ahead" poll; notifiedGen dedupes the
	// remote-ahead notification to once per remote generation.
	syncLastRemoteCheck time.Time
	syncNotifiedGen     int64
	// syncDirtyFP / syncDirtySince implement the quiet-period batch:
	// the fingerprint seen when the profile first went dirty, and when
	// it last changed. Touched only from the single auto-sync
	// goroutine, so no lock.
	syncDirtyFP    string
	syncDirtySince time.Time

	// recorder owns per-session asciicast files. Output is tapped at
	// the EmitOutput sink (SSH) / output-sink closure (local PTY) -
	// the single point every chunk already flows through. Keystrokes
	// are never recorded (typed passwords would leak).
	recorder *recorder.Manager

	// termSizeMu guards termSizes: last cols/rows reported per session
	// via SshResize / LocalShellResize. RecordingStart needs the size
	// for the asciicast header; sessions themselves don't retain it
	// (Resize just forwards a window-change to the PTY).
	termSizeMu sync.Mutex
	termSizes  map[string][2]uint16

	tcpdumpMu sync.Mutex
	tcpdumps  map[string]*sshlayer.TcpdumpHandle
	// tcpdumpBySession maps a sessionID to the dumpID of its active
	// capture, so a window that didn't start the capture (e.g. after a
	// tab detach moves the session to a new window) can re-attach to the
	// already-running capture instead of starting a second one. The
	// capture's lifetime is the session's, not the originating window's.
	tcpdumpBySession map[string]string

	// quitConfirmed gates the main-window WindowClosing handler. While
	// false and at least one SSH session is alive, the handler cancels
	// the close and emits "quit_request" so the frontend can prompt
	// the user. ConfirmQuit() flips it true and re-closes.
	quitConfirmed atomic.Bool

	// mainWindow is the primary application window. main.go writes
	// it after construction so the tray + IPCs can address it.
	mainWindow application.Window

	// windowHidden tracks the tray-hide state for click-to-toggle.
	windowHidden atomic.Bool

	// onDBReady runs once at the end of initialise(), after the store is
	// open. main.go uses it to restore the saved window geometry, which
	// needs both the window (set before Run) and the db (opened during
	// Run, in initialise).
	onDBReady func()

	// vncBridge is the loopback websocket server that relays RFB between
	// noVNC in the webview and a VNC upstream (Proxmox vncwebsocket, or a
	// raw/SSH-tunnelled TCP port). Started lazily on the first VncOpen*.
	vncBridge *sshlayer.VncBridge
	// vncMu guards vncSessions. Each open console keeps the metadata
	// needed to re-mint a ws token when a detached window asks for it
	// (VncSessionList), so the console survives a tab tear-off the same
	// way local PTYs do.
	vncMu       sync.Mutex
	vncSessions map[string]*vncSessionMeta
}

// vncSessionMeta is one live VNC console. The upstream factory is
// re-runnable so VncSessionList can mint a fresh token for a detached
// window without re-querying Proxmox / re-resolving the connection.
type vncSessionMeta struct {
	title    string
	username string
	password string
	// connectionID ties this console to its saved connection so the lazy
	// upstream factory (which runs after the session id is minted) can
	// attach its owned SSH session / chain cleanup via setVncOwned. Empty
	// for Proxmox consoles, which don't use the lazy-owned mechanism.
	connectionID string
	// transport labels how the RFB upstream is reached (direct / jump:<host>
	// / tunnel / proxmox) so VncSessionList can repopulate it on a re-mint.
	transport string
	// lastErr holds the most recent upstream-open failure (e.g. a jump-host
	// auth failure) so the frontend can show WHY a console disconnected -
	// noVNC only reports a bare "clean: false" on the websocket close, the
	// reason itself never reaches the RFB layer. Set by setVncError from the
	// lazy open factory; read + cleared by VncLastError. Guarded by vncMu.
	lastErr string
	open    func(ctx context.Context) (sshlayer.VncUpstream, error)
	// ownedSession is the dedicated SSH session a tunnelled console
	// opened for itself, if any. Disconnected on VncClose. nil for
	// direct-TCP and Proxmox consoles.
	ownedSession *sshlayer.Session
	// ownedCleanup tears down a jump chain opened to reach a direct
	// (non-loopback) RFB port behind a bastion. nil when there's no jump.
	ownedCleanup func()
}

// TabDragPayload is returned by WindowAcceptTabDrag.
//
// Layout is an opaque base64-encoded JSON blob describing the pane
// tree, title, and group metadata of the dragged tab. The backend
// treats it as a string so the frontend owns the schema. Empty when
// the drag originated before pane layouts were tracked.
type TabDragPayload struct {
	TabID    string `json:"tab_id"`
	Sessions string `json:"sessions"`
	Layout   string `json:"layout"`
}

// sessionMetaEntry holds the per-session display info we hand back from
// SshActiveSessions for UI recovery.
type sessionMetaEntry struct {
	connectionID string
	name         string
	hostname     string
}

// wailsSink emits session events as Wails events. Implements
// sshlayer.EventSink. connectionID is the originating connection so
// we can buffer debug lines under a stable key (a failed Connect
// never hands a sessionID back to the UI; the connectionID we know
// before the call).
type wailsSink struct {
	app          *App
	connectionID string
}

func (s wailsSink) EmitState(sessionID string, state sshlayer.SessionState) {
	EventsEmit("session_state:"+sessionID, state)
}
func (s wailsSink) EmitOutput(sessionID string, data []byte, cum uint64) {
	if s.app != nil && s.app.recorder != nil {
		s.app.recorder.Write(sessionID, data)
	}
	EventsEmit("pty_output:"+sessionID, sshlayer.OutputPayload{
		B64: sshlayer.EncodeBase64(data),
		Cum: cum,
	})
}
func (s wailsSink) EmitExitStatus(sessionID string, code uint32) {
	EventsEmit("session_exit:"+sessionID, code)
}
func (s wailsSink) EmitDebug(sessionID string, line string) {
	EventsEmit("session_debug:"+sessionID, line)
	// Also buffer the line under the connectionID so a failed connect
	// (where Terminal never mounts and the live subscription never
	// fires) can be surfaced by the frontend via SshGetConnectDebug.
	if s.connectionID != "" {
		s.app.appendDebug(s.connectionID, line)
	}
}

func NewApp() *App {
	return &App{}
}

// ServiceStartup is the v3 service lifecycle hook. Replaces v2's
// OnStartup. Wails calls this once before any frontend bind methods.
func (a *App) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	a.ctx = ctx
	a.initialise()
	return nil
}

// initialise contains what used to be the body of startup(). Kept
// separate so we don't blow up the public lifecycle hook with detail.
func (a *App) initialise() {
	// Clean up the `<exe>.old` an earlier Windows update may have left
	// behind (the apply script's own delete can lose a race with the
	// exiting process releasing the file lock).
	updater.CleanupOldBinary()

	a.pendingHostKeys = make(map[string]chan bool)
	a.transfers = make(map[string]chan struct{})
	a.reconnects = make(map[string]chan struct{})
	a.connectCancels = make(map[string]*connectCancel)
	a.debugBuf = make(map[string][]string)
	a.broadcastGroups = map[string]map[string]bool{
		"": make(map[string]bool),
	}
	dbPath := store.DefaultPath()
	vaultPath := creds.DefaultPath()
	appliedRestore := false
	if applied, err := backup.ApplyPending(dbPath, vaultPath); err != nil {
		log.Fatalf("apply pending restore: %v", err)
	} else if applied {
		log.Printf("restore: applied pending backup; live files swapped")
		// A freshly applied profile is the new clean baseline - stamp
		// it once the store is open (below) so the auto-sync dirty
		// check doesn't immediately push the just-pulled snapshot back
		// out as a "change".
		appliedRestore = true
	}
	log.Printf("opening store at %s", dbPath)
	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	if err := db.SeedDefaults(); err != nil {
		log.Fatalf("seed defaults: %v", err)
	}
	vault := creds.NewVault()
	vault.SetPath(creds.DefaultPath())

	a.db = db
	a.vault = vault
	if appliedRestore {
		// Mark the just-applied profile as the synced baseline so the
		// auto-sync loop doesn't push it straight back out.
		a.recordSyncPushedFingerprint()
	}
	a.credSvc = &creds.Service{DB: db, Vault: vault}
	a.pool = sshlayer.NewPool()
	a.localPool = local.NewPool()
	a.vncBridge = sshlayer.NewVncBridge()
	a.vncSessions = map[string]*vncSessionMeta{}
	a.recorder = recorder.NewManager()
	a.termSizes = map[string][2]uint16{}
	a.forwards = sshlayer.NewForwardPool()
	a.wgman = wg.NewManager()
	a.nbman = tunnelhelper.NewManager(func(profileID string) {
		// A helper process died (crash, network, kill). Sessions that
		// dialed through it lose their transport and drop on their own;
		// just refresh the UI's tunnel state (status pill, VPN badge).
		EventsEmit("network_tunnel_changed", profileID)
	})
	// First-hop dialer for connections resolved to a network profile:
	// vault-resolve the WG secrets, lazily start (or reuse) the
	// userspace tunnel, hand its netstack DialContext to the SSH layer.
	// Errors abort the connect - a profile-pinned connection must never
	// fall back to a direct dial.
	sshlayer.FirstHopDialerHook = func(s *store.ResolvedSettings) (sshlayer.ContextDialer, error) {
		if s.NetworkProfileID == nil {
			return nil, fmt.Errorf("no network profile on settings")
		}
		// wgDialerFor applies the profile's connect policy (always /
		// auto-with-direct-probe / paused). See app_network.go.
		return a.wgDialerFor(*s.NetworkProfileID)
	}
	// Same tunnels for dynamic-inventory API calls (a Proxmox that is
	// only reachable over VPN). Manual refreshes may start the tunnel;
	// timer refreshes only ride one that's already up
	// (wgBackgroundDialerFor) so a passive app never holds a VPN path
	// open just to poll inventory.
	inventory.TunnelDialContext = func(profileID string, background bool) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
		if background {
			return a.wgBackgroundDialerFor(profileID)
		}
		return a.wgDialerFor(profileID)
	}
	a.inventory = inventory.NewManager(db, vault, func(folderID string) {
		EventsEmit("dynamic_folder_refreshed", folderID)
	})
	a.inventory.Start()
	a.backupSched = backup.New()
	a.applyBackupSchedulerConfig()
	a.backupSched.Start()
	a.startAutoSync()
	a.sessionMeta = map[string]sessionMetaEntry{}
	// Restore log-tail toggle from settings. Default = on (matches
	// initial constructor state). Stored as "0"/"1".
	if v, ok, _ := a.db.GetSetting("app_log_tail_enabled"); ok && v == "0" && a.logBuf != nil {
		a.logBuf.SetEnabled(false)
	}
	log.Printf("store + vault ready")

	// One-shot cleanup: earlier versions mirrored every per-secret Put
	// into the OS keychain, which silently bypassed Lock(). The fallback
	// is gone now; remove any leftover entries on this machine so a
	// locked vault really is locked.
	if v, ok, _ := a.db.GetSetting("keyring_legacy_purged_v1"); !ok || v != "1" {
		if refs, err := a.db.ListCredentials(); err == nil {
			ids := make([]string, 0, len(refs))
			for _, r := range refs {
				ids = append(ids, r.ID)
			}
			creds.PurgeLegacyKeyringEntries(ids)
		}
		_ = a.db.SetSetting("keyring_legacy_purged_v1", "1")
	}

	// Store is open now; let main.go restore the saved window geometry.
	if a.onDBReady != nil {
		a.onDBReady()
	}
}

// Ping smoke command.
func (a *App) Ping(name string) string {
	return fmt.Sprintf("pong: hello %s, backend alive (go 1.26.3)", name)
}

// ----- Folders -----

func (a *App) FoldersList() ([]store.Folder, error) {
	return a.db.ListFolders()
}

func (a *App) FoldersGet(id string) (*store.Folder, error) {
	return a.db.GetFolder(id)
}

// FoldersCreateInput keeps the IPC signature flat (Wails generator handles
// nested types via JSON but flat shape is simpler to call from TS).
type FoldersCreateInput struct {
	ParentID  *string                   `json:"parent_id"`
	Name      string                    `json:"name"`
	SortOrder int64                     `json:"sort_order"`
	Settings  store.InheritableSettings `json:"settings"`
}

func (a *App) FoldersCreate(in FoldersCreateInput) (*store.Folder, error) {
	return a.db.CreateFolder(store.NewFolder{
		ParentID:  in.ParentID,
		Name:      in.Name,
		SortOrder: in.SortOrder,
		Settings:  in.Settings,
	})
}

type FoldersUpdateInput struct {
	ID          string                     `json:"id"`
	ParentID    *string                    `json:"parent_id"`
	ClearParent bool                       `json:"clear_parent"`
	Name        *string                    `json:"name"`
	SortOrder   *int64                     `json:"sort_order"`
	Settings    *store.InheritableSettings `json:"settings"`
}

func (a *App) FoldersUpdate(in FoldersUpdateInput) (*store.Folder, error) {
	return a.db.UpdateFolder(store.UpdateFolder{
		ID:          in.ID,
		ParentID:    in.ParentID,
		ClearParent: in.ClearParent,
		Name:        in.Name,
		SortOrder:   in.SortOrder,
		Settings:    in.Settings,
	})
}

// ----- Dynamic inventory (proxmox / hetzner / …) -----

// DynamicFolderCreateInput bundles a `folders` row + the side
// `dynamic_folders` row in a single call so the UI doesn't have to
// orchestrate two creates.
type DynamicFolderCreateInput struct {
	ParentID       *string                   `json:"parent_id"`
	Name           string                    `json:"name"`
	Settings       store.InheritableSettings `json:"settings"`
	Provider       string                    `json:"provider"`
	Config         map[string]any            `json:"config"`
	RefreshSeconds int                       `json:"refresh_seconds"`
}

func (a *App) DynamicFolderCreate(in DynamicFolderCreateInput) (*store.Folder, error) {
	folder, err := a.db.CreateFolder(store.NewFolder{
		ParentID: in.ParentID,
		Name:     in.Name,
		Settings: in.Settings,
	})
	if err != nil {
		return nil, err
	}
	refresh := in.RefreshSeconds
	if refresh < 0 {
		refresh = 0
	}
	if err := a.db.CreateDynamicFolder(store.DynamicFolder{
		FolderID:       folder.ID,
		Provider:       in.Provider,
		Config:         in.Config,
		RefreshSeconds: refresh,
	}); err != nil {
		_ = a.db.DeleteFolder(folder.ID)
		return nil, err
	}
	a.inventory.Restart(folder.ID)
	// Kick a first refresh so the UI lands with entries already
	// populated - non-blocking; errors surface in the folder's
	// last_error field.
	go func() {
		_ = a.inventory.Refresh(context.Background(), folder.ID, true)
	}()
	return folder, nil
}

type DynamicFolderUpdateInput struct {
	FolderID       string         `json:"folder_id"`
	Provider       string         `json:"provider"`
	Config         map[string]any `json:"config"`
	RefreshSeconds int            `json:"refresh_seconds"`
}

func (a *App) DynamicFolderUpdate(in DynamicFolderUpdateInput) error {
	if err := a.db.UpdateDynamicFolder(store.DynamicFolder{
		FolderID:       in.FolderID,
		Provider:       in.Provider,
		Config:         in.Config,
		RefreshSeconds: in.RefreshSeconds,
	}); err != nil {
		return err
	}
	a.inventory.Restart(in.FolderID)
	return nil
}

func (a *App) DynamicFolderGet(folderID string) (*store.DynamicFolder, error) {
	return a.db.GetDynamicFolder(folderID)
}

func (a *App) DynamicFoldersList() ([]store.DynamicFolder, error) {
	return a.db.ListDynamicFolders()
}

func (a *App) DynamicFolderRefreshNow(folderID string) error {
	return a.inventory.Refresh(context.Background(), folderID, true)
}

func (a *App) DynamicEntriesList(folderID string) ([]store.DynamicEntry, error) {
	return a.db.ListDynamicEntries(folderID)
}

// PinDynamicEntryInput captures the user's choice when promoting a
// dynamic inventory entry into a permanent connection. TargetFolderID
// defaults to the dynamic folder itself when empty; OverrideCredentialID
// (if non-empty) is applied to the new connection's overrides so the
// host doesn't have to lean on the dynamic folder's inherited cred.
type PinDynamicEntryInput struct {
	FolderID             string   `json:"folder_id"`
	EntryID              string   `json:"entry_id"`
	TargetFolderID       string   `json:"target_folder_id"`
	Name                 string   `json:"name"`
	OverrideCredentialID string   `json:"override_credential_id"`
	Tags                 []string `json:"tags"`
}

// PinDynamicEntry promotes a single dynamic entry into a real connection.
// Reads the dynamic_entry, lifts provider-specific vars (Ansible) into
// the new connection's overrides, records the pin so the next refresh
// skips this external_id. Returns the created connection.
func (a *App) PinDynamicEntry(in PinDynamicEntryInput) (*store.Connection, error) {
	entry, err := a.db.GetDynamicEntry(in.EntryID)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.FolderID != in.FolderID {
		return nil, fmt.Errorf("dynamic entry not found")
	}

	// Carry over any per-folder jump credential so the resulting
	// connection behaves the same as the dynamic ghost did.
	jumpCred := ""
	if df, err := a.db.GetDynamicFolder(in.FolderID); err == nil && df != nil {
		if s, ok := df.Config["jump_credential_id"].(string); ok {
			jumpCred = s
		}
	}

	target := in.TargetFolderID
	if target == "" {
		target = in.FolderID
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = entry.Name
	}

	syntheticOverrides := store.InheritableSettings{}
	tmp := store.Connection{Overrides: syntheticOverrides}
	applyAnsibleVarsToConnection(&tmp, entry.Raw, jumpCred)
	overrides := tmp.Overrides

	if in.OverrideCredentialID != "" {
		oc := in.OverrideCredentialID
		overrides.AuthRef = &oc
	}

	// A pinned Proxmox guest keeps its console: enable VNC so the "Open
	// VNC console" action shows, and the open path routes back through
	// the Proxmox API (the pin remembers the folder + vmid).
	if df, err := a.db.GetDynamicFolder(in.FolderID); err == nil && df != nil && df.Provider == "proxmox" {
		if entry.Kind == "guest_vm" || entry.Kind == "guest_lxc" {
			vncOn := true
			overrides.VncEnabled = &vncOn
		}
	}

	tags := in.Tags
	if tags == nil {
		tags = entry.Tags
	}
	if tags == nil {
		tags = []string{}
	}

	folderRef := target
	conn, err := a.db.CreateConnection(store.NewConnection{
		FolderID:  &folderRef,
		Name:      name,
		Hostname:  entry.Hostname,
		Overrides: overrides,
		Tags:      tags,
		Notes:     "Pinned from dynamic inventory (" + entry.ExternalID + ")",
	})
	if err != nil {
		return nil, err
	}
	if err := a.db.AddPinnedDynamicEntry(store.PinnedDynamicEntry{
		FolderID:     in.FolderID,
		ExternalID:   entry.ExternalID,
		ConnectionID: conn.ID,
	}); err != nil {
		// Roll back the connection so the pin set never falls out of
		// sync with the connections table.
		_ = a.db.DeleteConnection(conn.ID)
		return nil, err
	}
	// Drop the dynamic ghost from the cached entries immediately so the
	// UI doesn't show the host twice until the next scheduled refresh.
	if remaining, lerr := a.db.ListDynamicEntries(in.FolderID); lerr == nil {
		filtered := remaining[:0]
		for _, e := range remaining {
			if e.ExternalID == entry.ExternalID {
				continue
			}
			filtered = append(filtered, e)
		}
		_ = a.db.ReplaceDynamicEntries(in.FolderID, filtered)
		EventsEmit("dynamic_folder_refreshed", in.FolderID)
	}
	a.recordAudit("dynamic.pin", conn.ID, map[string]string{
		"folder_id":   in.FolderID,
		"external_id": entry.ExternalID,
		"name":        conn.Name,
	})
	return conn, nil
}

// UnpinConnection deletes the pin mapping AND the underlying
// connection. The next inventory refresh re-includes the original
// external_id as a dynamic ghost. Returns the dynamic folder id so the
// frontend knows which folder to refresh-now.
func (a *App) UnpinConnection(connectionID string) (string, error) {
	pin, err := a.db.GetPinForConnection(connectionID)
	if err != nil {
		return "", err
	}
	if pin == nil {
		return "", fmt.Errorf("connection is not pinned")
	}
	folderID := pin.FolderID
	if err := a.db.DeleteConnection(connectionID); err != nil {
		return "", err
	}
	// FK cascade already nuked the pin row; explicit cleanup is a
	// belt-and-braces no-op in case the cascade ever moves.
	_ = a.db.DeletePinByConnection(connectionID)
	a.recordAudit("dynamic.unpin", connectionID, map[string]string{
		"folder_id":   folderID,
		"external_id": pin.ExternalID,
	})
	return folderID, nil
}

// ConvertDynamicFolderToStatic snapshots every cached dynamic entry
// into a real connection inside the same folder, drops the
// dynamic_folders side-table row, and clears the cached entries. The
// base folders row stays in place so connection inheritance for the
// new rows works as expected. Operation is irreversible from the UI.
func (a *App) ConvertDynamicFolderToStatic(folderID string) (int, error) {
	df, err := a.db.GetDynamicFolder(folderID)
	if err != nil {
		return 0, err
	}
	if df == nil {
		return 0, fmt.Errorf("not a dynamic folder")
	}
	entries, err := a.db.ListDynamicEntries(folderID)
	if err != nil {
		return 0, err
	}

	jumpCred := ""
	if s, ok := df.Config["jump_credential_id"].(string); ok {
		jumpCred = s
	}

	// Find external IDs that are already pinned - those connections
	// exist; skip them so we don't create duplicates.
	pinned, err := a.db.ListPinnedExternalIDs(folderID)
	if err != nil {
		return 0, err
	}

	created := 0
	for i, e := range entries {
		if _, isPinned := pinned[e.ExternalID]; isPinned {
			continue
		}
		tmp := store.Connection{Overrides: store.InheritableSettings{}}
		applyAnsibleVarsToConnection(&tmp, e.Raw, jumpCred)
		tags := e.Tags
		if tags == nil {
			tags = []string{}
		}
		folderRef := folderID
		if _, err := a.db.CreateConnection(store.NewConnection{
			FolderID:  &folderRef,
			Name:      e.Name,
			Hostname:  e.Hostname,
			SortOrder: int64(i),
			Overrides: tmp.Overrides,
			Tags:      tags,
			Notes:     "Converted from dynamic inventory (" + e.ExternalID + ")",
		}); err != nil {
			return created, err
		}
		created++
	}
	// Drop the dynamic side-table row + cached entries. The pin table
	// would normally outlive the dynamic folder, but at this point the
	// concept of "the dynamic source" is gone, so clear those too -
	// otherwise UnpinConnection would refresh-now a folder that no
	// longer pulls anything.
	if err := a.db.DeletePinsByFolder(folderID); err != nil {
		return created, err
	}
	if err := a.db.DeleteDynamicFolder(folderID); err != nil {
		return created, err
	}
	// Stop the refresh timer.
	if a.inventory != nil {
		a.inventory.Stop(folderID)
	}
	a.recordAudit("dynamic.convert", folderID, map[string]string{
		"created": strconv.Itoa(created),
		"total":   strconv.Itoa(len(entries)),
	})
	EventsEmit("dynamic_folder_refreshed", folderID)
	return created, nil
}

// applyAnsibleVarsToConnection reads the Ansible per-host vars from
// raw (the JSON payload stashed on the dynamic_entry at refresh
// time) and lifts the recognised ones into the synthetic connection's
// overrides. Non-Ansible providers have a different Raw shape; we
// detect the Ansible shape by presence of "groups" + "vars" and
// silently no-op for everything else.
func applyAnsibleVarsToConnection(c *store.Connection, raw []byte, jumpCredentialID string) {
	if len(raw) == 0 {
		return
	}
	var probe struct {
		Vars   map[string]string `json:"vars"`
		Groups []string          `json:"groups"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return
	}
	if probe.Vars == nil && probe.Groups == nil {
		return
	}
	if u := strings.TrimSpace(probe.Vars["ansible_user"]); u != "" {
		c.Overrides.Username = &u
	}
	if p := strings.TrimSpace(probe.Vars["ansible_port"]); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			port := uint16(n)
			c.Overrides.Port = &port
		}
	}
	// Jump host: ansible_ssh_common_args first (canonical), then
	// extra_args as fallback for inventories that drift between
	// the two.
	for _, key := range []string{"ansible_ssh_common_args", "ansible_ssh_extra_args"} {
		args := probe.Vars[key]
		if args == "" {
			continue
		}
		hops := inventory.AnsibleParseJumpHosts(args)
		if len(hops) == 0 {
			continue
		}
		c.Overrides.JumpHost = buildJumpChainFromHops(hops, jumpCredentialID)
		break
	}
}

// buildJumpChainFromHops turns a list of "[user@]host[:port]" hop
// strings into a JumpHostOverride with Kind="chain" and a nested
// JumpHostSpec linked-list via .Via. Hops travel ssh-config order
// (first hop is the one you connect to first); JumpHostSpec.Via is
// the *next* hop from this one's perspective, so we build
// right-to-left.
func buildJumpChainFromHops(hops []string, jumpCredentialID string) *store.JumpHostOverride {
	if len(hops) == 0 {
		return nil
	}
	var chain *store.JumpHostSpec
	for i := len(hops) - 1; i >= 0; i-- {
		user, host, port := parseHopString(hops[i])
		node := &store.JumpHostSpec{
			Hostname: host,
			Via:      chain,
		}
		if user != "" {
			node.Username = &user
		}
		if port != 0 {
			p := uint16(port)
			node.Port = &p
		}
		// Apply the per-folder jump credential to every hop. Without
		// it the SSH layer has no way to authenticate to the
		// bastion: target-host credentials (Ansible inventory)
		// rarely also work on the jump host. Empty string skips -
		// caller will hit "no credential for jump host" later.
		if jumpCredentialID != "" {
			cred := jumpCredentialID
			node.AuthRef = &cred
		}
		chain = node
	}
	return &store.JumpHostOverride{Kind: "chain", Chain: chain}
}

// parseHopString splits "user@host:port" into its parts. Missing
// pieces return empty / zero, leaving the caller to decide whether
// to inherit a default.
func parseHopString(s string) (user, host string, port int) {
	s = strings.TrimSpace(s)
	if at := strings.Index(s, "@"); at >= 0 {
		user = s[:at]
		s = s[at+1:]
	}
	if colon := strings.LastIndex(s, ":"); colon >= 0 {
		if n, err := strconv.Atoi(s[colon+1:]); err == nil && n > 0 && n < 65536 {
			port = n
			s = s[:colon]
		}
	}
	host = s
	return
}

// SshConnectDynamic spins up an SSH session against a cached
// dynamic_entry. The entry is read-only and has no `connections` row,
// so we construct a synthetic store.Connection in memory carrying the
// hostname + folder lineage and feed it through the same resolver +
// SSH layer the persistent-connection path uses.
func (a *App) SshConnectDynamic(folderID, entryID string) (*SshConnectResult, error) {
	return a.sshConnectDynamicInternal(folderID, entryID, "", "", "", "", "")
}

// SshConnectDynamicWithOverride mirrors SshConnectWithOverride for
// dynamic-inventory entries - same one-shot credential override that
// doesn't touch any persisted state.
func (a *App) SshConnectDynamicWithOverride(folderID, entryID, overrideCredentialID string) (*SshConnectResult, error) {
	return a.sshConnectDynamicInternal(folderID, entryID, overrideCredentialID, "", "", "", "")
}

// SshConnectDynamicAdvanced is the dynamic-inventory twin of
// SshConnectAdvanced. Same semantics: each override is independent,
// empty string = fall through to normal resolution.
func (a *App) SshConnectDynamicAdvanced(folderID, entryID, overrideCredentialID, overrideUsername, overridePassword string) (*SshConnectResult, error) {
	return a.sshConnectDynamicInternal(folderID, entryID, overrideCredentialID, overrideUsername, overridePassword, "", "")
}

// SshConnectDynamicWithJumpOverride adds jump host overrides to the
// advanced flow. jumpHostOverride is "[user@]host[:port]" (empty =
// keep the host parsed from Ansible vars / inherited from folder).
// jumpCredentialOverride is a credential id that replaces the
// per-folder jump credential for this connect only.
func (a *App) SshConnectDynamicWithJumpOverride(folderID, entryID, overrideCredentialID, overrideUsername, overridePassword, jumpHostOverride, jumpCredentialOverride string) (*SshConnectResult, error) {
	return a.sshConnectDynamicInternal(folderID, entryID, overrideCredentialID, overrideUsername, overridePassword, jumpHostOverride, jumpCredentialOverride)
}

func (a *App) sshConnectDynamicInternal(folderID, entryID, overrideCredentialID, overrideUsername, overridePassword, jumpHostOverride, jumpCredentialOverride string) (*SshConnectResult, error) {
	entry, err := a.db.GetDynamicEntry(entryID)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.FolderID != folderID {
		return nil, fmt.Errorf("dynamic entry not found")
	}
	if entry.Status == "stopped" {
		// Backend won't second-guess the user; the frontend prompts
		// before this call. Surfaced here as a log line so the
		// debug stream notes the deliberate connect-despite-stopped.
		log.Printf("connecting to stopped dynamic entry %s (%s)", entry.Name, entry.Hostname)
	}

	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	folderRef := folderID
	syntheticConn := store.Connection{
		ID:        "dyn:" + entryID,
		FolderID:  &folderRef,
		Name:      entry.Name,
		Hostname:  entry.Hostname,
		Overrides: store.InheritableSettings{},
	}
	// Ansible-provider entries carry per-host vars in Raw - lift
	// ansible_user / ansible_port / ansible_ssh_common_args into
	// the synthetic connection's overrides BEFORE we resolve so
	// the inherit cascade still wins where overrides are unset.
	// jumpCred is the per-folder credential the user picked for
	// every parsed jump hop (Ansible vars only carry the host, not
	// credentials); empty string = inherit normally.
	jumpCred := ""
	if df, err := a.db.GetDynamicFolder(folderID); err == nil && df != nil {
		if s, ok := df.Config["jump_credential_id"].(string); ok {
			jumpCred = s
		}
	}
	// Per-connect jump-credential override wins over the folder
	// default. Lets the user A/B a different bastion credential
	// without editing the folder config.
	effectiveJumpCred := jumpCred
	if jumpCredentialOverride != "" {
		effectiveJumpCred = jumpCredentialOverride
	}
	applyAnsibleVarsToConnection(&syntheticConn, entry.Raw, effectiveJumpCred)

	// Per-connect jump-host override: replace the chain built from
	// Ansible vars (or add one if none was parsed) with a single
	// hop the user picked. Targets the "Ansible says bastionA but
	// I want bastionB this time" workflow.
	if jumpHostOverride != "" {
		syntheticConn.Overrides.JumpHost = buildJumpChainFromHops([]string{jumpHostOverride}, effectiveJumpCred)
	}

	settings := resolver.ResolveWith(syntheticConn, folders)

	// Per-attempt credential override for dynamic entries.
	if overrideCredentialID != "" {
		oc := overrideCredentialID
		settings.AuthRef = &oc
		settings.PasswordOverride = nil
	}

	if overrideUsername != "" {
		ou := overrideUsername
		settings.Username = &ou
	}
	if overridePassword != "" {
		op := overridePassword
		settings.PasswordOverride = &op
	}

	// Per-connection password override doesn't apply (no row). Inherit
	// cascade still resolves credentials from the folder chain.
	if settings.AuthRef == nil && settings.PasswordOverride == nil {
		return nil, fmt.Errorf("dynamic folder has no credential to inherit; set one on the folder")
	}
	if settings.Username == nil && settings.AuthRef != nil {
		if cred, err2 := a.db.GetCredential(*settings.AuthRef); err2 == nil && cred.DefaultUsername != nil {
			settings.Username = cred.DefaultUsername
		}
	}

	connectionID := syntheticConn.ID
	a.resetDebug(connectionID)
	sink := wailsSink{app: a, connectionID: connectionID}
	var ct time.Duration
	if raw := a.SettingsGet("connect_timeout_seconds"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			ct = time.Duration(n) * time.Second
		}
	}
	progress := func(stage string) {
		EventsEmit("connect_progress:"+connectionID, stage)
	}
	// Cancelable connect context (opkssh login hang). Keyed on the synthetic
	// connectionID, which the frontend learns from the connect_progress
	// event, so SshCancelConnect can abort this dynamic connect too.
	ctx, cancel := context.WithCancel(context.Background())
	deregister := a.registerConnectCancel(connectionID, cancel)
	defer deregister()
	defer cancel()
	sess, err := sshlayer.Connect(ctx, a.db, a.vault, &settings, sink, a.makeHostKeyCallback(), a.makeAlgoLookup(), ct, progress)
	if err != nil {
		log.Printf("ssh connect dynamic %s (%s): %v", entry.Name, settings.Hostname, err)
		return nil, err
	}
	a.pool.Add(sess)
	a.metaMu.Lock()
	a.sessionMeta[sess.ID] = sessionMetaEntry{
		connectionID: connectionID,
		name:         entry.Name,
		hostname:     entry.Hostname,
	}
	a.metaMu.Unlock()
	a.syncForegroundService()
	sess.SetOnClose(func(sessionID string) {
		a.forwards.StopAllForSession(sessionID)
		a.sessionRecordingCleanup(sessionID)
		a.pool.Remove(sessionID)
		a.wgRelease(sessionID)
		a.metaMu.Lock()
		delete(a.sessionMeta, sessionID)
		a.metaMu.Unlock()
		a.syncForegroundService()
		EventsEmit("session_state:"+sessionID, sshlayer.SessionState{State: "disconnected"})
	})
	dynUser := ""
	if settings.Username != nil {
		dynUser = *settings.Username
	}
	a.recordAudit("ssh.connect.dynamic", "dyn:"+entryID, map[string]string{
		"session_id": sess.ID,
		"folder_id":  folderID,
		"host":       settings.Hostname,
		"port":       strconv.Itoa(int(settings.Port)),
		"user":       dynUser,
		"name":       entry.Name,
	})
	return &SshConnectResult{SessionID: sess.ID, NetworkVia: a.wgTrackSession(sess, &settings)}, nil
}

func (a *App) FoldersDelete(id string) error {
	// Cascade cleanup: stop any inventory timer associated with this
	// folder before the row is gone (the side dynamic_folders row
	// will be cascade-deleted by the FK).
	if isDyn, _ := a.db.IsDynamicFolder(id); isDyn && a.inventory != nil {
		a.inventory.Stop(id)
	}
	return a.db.DeleteFolder(id)
}

// ----- Connections -----

func (a *App) ConnectionsList(folderID *string) ([]store.Connection, error) {
	return a.db.ListConnections(folderID)
}

// ConnectionsRecent returns the N most-recently connected entries.
// Sidebar mounts these above the tree under "Recent". N comes from
// settings (recent_connections_count) and defaults to 5.
func (a *App) ConnectionsRecent() ([]store.Connection, error) {
	n := 5
	if v, ok, _ := a.db.GetSetting("recent_connections_count"); ok && v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	// Recency lives in machine-local state now (legacy column values
	// merged in for pre-split data); sort + cap here instead of SQL.
	times := a.recentTimes()
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	recent := make([]store.Connection, 0, len(times))
	for _, c := range conns {
		if _, ok := times[c.ID]; ok {
			recent = append(recent, c)
		}
	}
	sort.Slice(recent, func(i, j int) bool { return times[recent[i].ID] > times[recent[j].ID] })
	if len(recent) > n {
		recent = recent[:n]
	}
	return recent, nil
}

// ConnectionsFavorites returns every connection flagged favorite.
func (a *App) ConnectionsFavorites() ([]store.Connection, error) {
	return a.db.FavoriteConnections()
}

// ConnectionsSetFavorite toggles the favorite flag without touching
// any other field on the connection (UpdateConnection is heavier).
func (a *App) ConnectionsSetFavorite(id string, fav bool) error {
	_, err := a.db.UpdateConnection(store.UpdateConnection{ID: id, Favorite: &fav})
	return err
}

func (a *App) ConnectionsGet(id string) (*store.Connection, error) {
	return a.db.GetConnection(id)
}

type ConnectionsCreateInput struct {
	FolderID  *string                   `json:"folder_id"`
	Name      string                    `json:"name"`
	Hostname  string                    `json:"hostname"`
	SortOrder int64                     `json:"sort_order"`
	Overrides store.InheritableSettings `json:"overrides"`
	Tags      []string                  `json:"tags"`
	Notes     string                    `json:"notes"`
}

func (a *App) ConnectionsCreate(in ConnectionsCreateInput) (*store.Connection, error) {
	return a.db.CreateConnection(store.NewConnection{
		FolderID:  in.FolderID,
		Name:      in.Name,
		Hostname:  in.Hostname,
		SortOrder: in.SortOrder,
		Overrides: in.Overrides,
		Tags:      in.Tags,
		Notes:     in.Notes,
	})
}

type ConnectionsUpdateInput struct {
	ID          string                     `json:"id"`
	FolderID    *string                    `json:"folder_id"`
	ClearFolder bool                       `json:"clear_folder"`
	Name        *string                    `json:"name"`
	Hostname    *string                    `json:"hostname"`
	SortOrder   *int64                     `json:"sort_order"`
	Overrides   *store.InheritableSettings `json:"overrides"`
	Tags        *[]string                  `json:"tags"`
	Notes       *string                    `json:"notes"`
	Favorite    *bool                      `json:"favorite"`
	Sensitive   *bool                      `json:"sensitive"`
}

func (a *App) ConnectionsUpdate(in ConnectionsUpdateInput) (*store.Connection, error) {
	return a.db.UpdateConnection(store.UpdateConnection{
		ID:          in.ID,
		FolderID:    in.FolderID,
		ClearFolder: in.ClearFolder,
		Name:        in.Name,
		Hostname:    in.Hostname,
		SortOrder:   in.SortOrder,
		Overrides:   in.Overrides,
		Tags:        in.Tags,
		Notes:       in.Notes,
		Favorite:    in.Favorite,
		Sensitive:   in.Sensitive,
	})
}

func (a *App) ConnectionsDelete(id string) error {
	return a.db.DeleteConnection(id)
}

func (a *App) ConnectionsClone(id string) (*store.Connection, error) {
	src, err := a.db.GetConnection(id)
	if err != nil {
		return nil, err
	}
	return a.db.CreateConnection(store.NewConnection{
		FolderID:  src.FolderID,
		Name:      "Copy of " + src.Name,
		Hostname:  src.Hostname,
		SortOrder: src.SortOrder,
		Overrides: src.Overrides,
		Tags:      src.Tags,
		Notes:     src.Notes,
	})
}

// ConnectionsBatchUpdate applies the same overrides patch to many
// connections in a single transaction. Used by the tree's multi-select
// batch-edit panel. ClearFields lists which inheritable settings to set
// back to nil (i.e. inherit from folder). Patch.* lists which to write.
// Fields not in either bucket are left unchanged on each row.
type ConnectionsBatchUpdateInput struct {
	IDs         []string                  `json:"ids"`
	Patch       store.InheritableSettings `json:"patch"`
	ClearFields []string                  `json:"clear_fields"`
	AddTags     []string                  `json:"add_tags"`
	RemoveTags  []string                  `json:"remove_tags"`
}

type ConnectionsBatchUpdateResult struct {
	Updated int `json:"updated"`
}

func (a *App) ConnectionsBatchUpdate(in ConnectionsBatchUpdateInput) (*ConnectionsBatchUpdateResult, error) {
	n, err := a.db.BatchUpdateConnectionOverrides(in.IDs, store.BatchOverridePatch{
		Patch:       in.Patch,
		ClearFields: in.ClearFields,
		AddTags:     in.AddTags,
		RemoveTags:  in.RemoveTags,
	})
	if err != nil {
		return nil, err
	}
	return &ConnectionsBatchUpdateResult{Updated: n}, nil
}

func (a *App) ConnectionsTouch(id string) error {
	a.touchRecent(id)
	return nil
}

func (a *App) ConnectionsResolve(id string) (*store.ResolvedSettings, error) {
	return resolver.ResolveConnection(a.db, id)
}

// ----- Credentials -----

func (a *App) CredentialsList() ([]store.CredentialRef, error) {
	return a.db.ListCredentials()
}

func (a *App) CredentialsGet(id string) (*store.CredentialRef, error) {
	return a.db.GetCredential(id)
}

func (a *App) CredentialsCreate(in creds.CreateInput) (*creds.CreateResult, error) {
	// Reject hostile opkssh provider YAML at save time so the user
	// gets feedback immediately rather than at next OIDC refresh.
	// Validation is also enforced at login time (defence in depth)
	// against legacy configs saved before this check existed.
	if in.OpksshConfigYAML != "" {
		if err := sshlayer.ValidateOpksshYAML(in.OpksshConfigYAML); err != nil {
			return nil, fmt.Errorf("opkssh config rejected: %w", err)
		}
	}
	return a.credSvc.Create(in)
}

type CredentialsUpdateInput struct {
	ID                       string          `json:"id"`
	Kind                     *string         `json:"kind"`
	FolderID                 *string         `json:"folder_id"`
	SetFolderToNull          bool            `json:"set_folder_to_null"`
	Name                     *string         `json:"name"`
	Hint                     *string         `json:"hint"`
	Tags                     *[]string       `json:"tags"`
	Config                   *map[string]any `json:"config"`
	PublicKey                *string         `json:"public_key"`
	SetPublicKeyToNull       bool            `json:"set_public_key_to_null"`
	DefaultUsername          *string         `json:"default_username"`
	SetDefaultUsernameToNull bool            `json:"set_default_username_to_null"`
	RotationReminderDays     *int64          `json:"rotation_reminder_days"`
	SetReminderToNull        bool            `json:"set_reminder_to_null"`
}

func (a *App) CredentialsUpdate(in CredentialsUpdateInput) (*store.CredentialRef, error) {
	// Same save-time gate as Create: a Config map that carries an
	// opkssh_config_yaml entry must pass the redirect/issuer
	// validator before we persist it. Other config keys are
	// untouched here; ssh-agent socket_path is checked at dial
	// time in resolveAgent, password fields aren't shaped by us.
	if in.Config != nil {
		if y, ok := (*in.Config)["opkssh_config_yaml"].(string); ok && y != "" {
			if err := sshlayer.ValidateOpksshYAML(y); err != nil {
				return nil, fmt.Errorf("opkssh config rejected: %w", err)
			}
		}
	}
	var kind *store.CredentialKind
	if in.Kind != nil {
		k := store.CredentialKind(*in.Kind)
		kind = &k
	}
	return a.db.UpdateCredential(store.UpdateCredential{
		ID:                       in.ID,
		Kind:                     kind,
		FolderID:                 in.FolderID,
		SetFolderToNull:          in.SetFolderToNull,
		Name:                     in.Name,
		Hint:                     in.Hint,
		Tags:                     in.Tags,
		Config:                   in.Config,
		PublicKey:                in.PublicKey,
		SetPublicKeyToNull:       in.SetPublicKeyToNull,
		DefaultUsername:          in.DefaultUsername,
		SetDefaultUsernameToNull: in.SetDefaultUsernameToNull,
		RotationReminderDays:     in.RotationReminderDays,
		SetReminderToNull:        in.SetReminderToNull,
	})
}

func (a *App) CredentialsDelete(id string) error {
	return a.credSvc.Delete(id)
}

func (a *App) CredentialsRotatePassword(id, newPassword string) (*store.CredentialRef, error) {
	return a.credSvc.RotatePassword(id, newPassword)
}

func (a *App) CredentialsRevealSecret(id string) (string, error) {
	return a.credSvc.RevealSecret(id)
}

type CredentialsRotateKeyInput struct {
	ID             string  `json:"id"`
	GenerateNew    bool    `json:"generate_new"`
	PrivateOpenSSH string  `json:"private_openssh"`
	Passphrase     *string `json:"passphrase"`
}

func (a *App) CredentialsRotateKey(in CredentialsRotateKeyInput) (*store.CredentialRef, error) {
	return a.credSvc.RotateKey(in.ID, in.GenerateNew, in.PrivateOpenSSH, in.Passphrase)
}

type CredentialsRotateAPITokenInput struct {
	ID        string  `json:"id"`
	TokenID   *string `json:"token_id"`   // nil = leave unchanged
	NewSecret string  `json:"new_secret"` // "" = leave unchanged
}

func (a *App) CredentialsRotateAPIToken(in CredentialsRotateAPITokenInput) (*store.CredentialRef, error) {
	return a.credSvc.RotateAPIToken(in.ID, in.TokenID, in.NewSecret)
}

// ----- Credential folders -----

func (a *App) CredentialFoldersList() ([]store.CredentialFolder, error) {
	return a.db.ListCredentialFolders()
}

func (a *App) CredentialFoldersCreate(name string, parentID *string) (*store.CredentialFolder, error) {
	return a.db.CreateCredentialFolder(name, parentID)
}

func (a *App) CredentialFoldersUpdate(id string, name *string, parentID *string, clearParent bool) (*store.CredentialFolder, error) {
	return a.db.UpdateCredentialFolder(id, name, parentID, clearParent)
}

// CredentialFoldersDelete removes the folder AND everything inside it:
// subfolders via the FK cascade, credentials explicitly through
// credSvc.Delete so vault secrets and sealed history follow the normal
// deletion path. The credential_refs FK is ON DELETE SET NULL - left
// alone it silently dumped the folder's credentials flat at the root,
// which matched neither the connections tree nor user expectation.
func (a *App) CredentialFoldersDelete(id string) error {
	folders, err := a.db.ListCredentialFolders()
	if err != nil {
		return err
	}
	children := map[string][]string{}
	for _, f := range folders {
		if f.ParentID != nil {
			children[*f.ParentID] = append(children[*f.ParentID], f.ID)
		}
	}
	subtree := map[string]bool{id: true}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if !subtree[c] {
				subtree[c] = true
				queue = append(queue, c)
			}
		}
	}
	creds, err := a.db.ListCredentials()
	if err != nil {
		return err
	}
	for _, c := range creds {
		if c.FolderID != nil && subtree[*c.FolderID] {
			if err := a.credSvc.Delete(c.ID); err != nil {
				return fmt.Errorf("delete credential %s: %w", c.Name, err)
			}
		}
	}
	return a.db.DeleteCredentialFolder(id)
}

func (a *App) CredentialsUsage(id string) ([]store.UsageRef, error) {
	return a.db.CredentialUsage(id)
}

func (a *App) CredentialsHistory(id string) ([]store.CredentialHistoryEntry, error) {
	return a.db.ListHistory(id)
}

// CredentialsSecretHistory lists sealed previous-value snapshots for a
// credential. Metadata only - call CredentialsRevealSecretHistory to
// unseal one.
func (a *App) CredentialsSecretHistory(id string) ([]store.CredentialSecretHistoryEntry, error) {
	return a.credSvc.ListSecretHistory(id)
}

// CredentialsRevealSecretHistory unseals one history snapshot. UI
// drives the 30s clipboard auto-clear the same way it does for
// CredentialsRevealSecret. We log a "history revealed" audit event
// so a reveal that produces a real plaintext is auditable.
func (a *App) CredentialsRevealSecretHistory(historyID string) (string, error) {
	v, err := a.credSvc.RevealSecretHistory(historyID)
	if err == nil {
		a.recordAudit("credential.history.reveal", historyID, nil)
	}
	return v, err
}

// CredentialsDeleteSecretHistory drops one history snapshot from both
// the DB and the vault. No-op-safe: missing IDs return an error
// without state changes.
func (a *App) CredentialsDeleteSecretHistory(historyID string) error {
	err := a.credSvc.DeleteSecretHistoryEntry(historyID)
	if err == nil {
		a.recordAudit("credential.history.delete", historyID, nil)
	}
	return err
}

// ----- Audit log -----
//
// recordAudit is the single fan-in for security-relevant operations.
// Failures are logged but never propagated: the audit log is best-
// effort observability, not load-bearing on the underlying op.
func (a *App) recordAudit(action, target string, metadata map[string]string) {
	if a == nil || a.db == nil {
		return
	}
	if err := a.db.AppendAudit(action, target, metadata); err != nil {
		log.Printf("audit: append %s failed: %v", action, err)
	}
}

type AuditListInput struct {
	Action string `json:"action"`
	Limit  int    `json:"limit"`
	Before int64  `json:"before"`
}

func (a *App) AuditList(in AuditListInput) ([]store.AuditEvent, error) {
	return a.db.ListAudit(store.AuditFilter{
		Action: in.Action,
		Limit:  in.Limit,
		Before: in.Before,
	})
}

// AuditPurge drops all rows older than `olderThanDays`. Returns the
// number of rows removed.
func (a *App) AuditPurge(olderThanDays int) (int64, error) {
	if olderThanDays <= 0 {
		return 0, fmt.Errorf("retention must be positive")
	}
	cutoff := time.Now().AddDate(0, 0, -olderThanDays).Unix()
	n, err := a.db.PurgeAuditBefore(cutoff)
	if err == nil {
		a.recordAudit("audit.purge", "", map[string]string{
			"older_than_days": strconv.Itoa(olderThanDays),
			"rows":            strconv.FormatInt(n, 10),
		})
	}
	return n, err
}

// OpksshCertStatusResult wraps the vault cert state for the credential
// editor. VaultLocked distinguishes "no cert" from "can't look" - a
// locked vault returns ok=false on every Get and would otherwise
// masquerade as a missing cert.
type OpksshCertStatusResult struct {
	VaultLocked bool  `json:"vault_locked"`
	HasCert     bool  `json:"has_cert"`
	IssuedAt    int64 `json:"issued_at"`
	ValidBefore int64 `json:"valid_before"`
	RenewAt     int64 `json:"renew_at"`
}

// OpksshCertStatus reports when the opkssh credential's cert was
// issued and when the connect path will force a browser re-login, so
// the editor can render "re-login in ~6d23h" instead of raw config
// numbers. Read-only; never triggers a refresh.
func (a *App) OpksshCertStatus(credentialID string) (*OpksshCertStatusResult, error) {
	cred, err := a.db.GetCredential(credentialID)
	if err != nil {
		return nil, err
	}
	if cred.Kind != store.CredOpkssh {
		return nil, fmt.Errorf("credential is not opkssh")
	}
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return &OpksshCertStatusResult{VaultLocked: true}, nil
	}
	cfg, err := sshlayer.ParseOpksshConfig(cred)
	if err != nil {
		return nil, err
	}
	st := sshlayer.GetCertStatus(cfg, a.vault)
	return &OpksshCertStatusResult{
		HasCert:     st.HasCert,
		IssuedAt:    st.IssuedAt,
		ValidBefore: st.ValidBefore,
		RenewAt:     st.RenewAt,
	}, nil
}

// ----- Vault -----

func (a *App) VaultStatus() creds.Status {
	return a.vault.Status()
}

func (a *App) VaultInit(passphrase string, rememberOnMachine bool) error {
	err := a.vault.Init(passphrase, rememberOnMachine)
	if err != nil {
		a.recordAudit("vault.init.failed", "", map[string]string{"error": err.Error()})
		return err
	}
	a.recordAudit("vault.init", "", map[string]string{"sidecar": boolStr(rememberOnMachine)})
	return nil
}

func (a *App) VaultUnlock(passphrase string, rememberOnMachine bool) error {
	err := a.vault.Unlock(passphrase, rememberOnMachine)
	if err != nil {
		a.recordAudit("vault.unlock.failed", "", map[string]string{"error": err.Error()})
		return err
	}
	a.recordAudit("vault.unlock", "", map[string]string{"sidecar": boolStr(rememberOnMachine)})
	return nil
}

func (a *App) VaultAutoUnlock() (bool, error) {
	ok, err := a.vault.AutoUnlock()
	if err != nil {
		a.recordAudit("vault.auto_unlock.failed", "", map[string]string{"error": err.Error()})
		return ok, err
	}
	if ok {
		a.recordAudit("vault.auto_unlock", "", nil)
	}
	return ok, nil
}

func (a *App) VaultLock(forgetSidecar bool) {
	a.vault.Lock(forgetSidecar)
	a.recordAudit("vault.lock", "", map[string]string{"forget_sidecar": boolStr(forgetSidecar)})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// VaultChangePassphrase rotates the master passphrase. Vault must be
// unlocked. The old passphrase is independently re-verified against
// the on-disk file before any mutation.
func (a *App) VaultChangePassphrase(oldPassphrase, newPassphrase string) error {
	if err := a.vault.ChangePassphrase(oldPassphrase, newPassphrase); err != nil {
		a.recordAudit("vault.rotate.failed", "", map[string]string{"error": err.Error()})
		return err
	}
	a.recordAudit("vault.rotate", "", nil)
	return nil
}

// ----- Backup -----

type BackupCreateInput struct {
	// DestPath is optional. Empty -> default <DataDir>/backups/<auto>.
	DestPath   string `json:"dest_path"`
	Passphrase string `json:"passphrase"`
}

type BackupCreateResult struct {
	Path string `json:"path"`
}

// BackupsCreate writes an encrypted snapshot of store.db + vault.enc.
// The passphrase must match the live vault passphrase; we verify by
// re-unlocking the vault file (cheap because it does not touch process
// state).
func (a *App) BackupsCreate(in BackupCreateInput) (*BackupCreateResult, error) {
	if in.Passphrase == "" {
		return nil, fmt.Errorf("passphrase required")
	}
	if _, err := creds.UnlockVault(a.vault.VaultPath(), in.Passphrase); err != nil {
		return nil, fmt.Errorf("verify passphrase: %w", err)
	}
	dest := in.DestPath
	if dest == "" {
		dest = filepath.Join(backup.DefaultDir(store.DataDir()), backup.SuggestedFilename(time.Now()))
	}
	if err := backup.Create(dest, store.DefaultPath(), creds.DefaultPath(), in.Passphrase, appVersion); err != nil {
		a.recordAudit("backup.create.failed", filepath.Base(dest), map[string]string{"error": err.Error()})
		return nil, err
	}
	a.recordAudit("backup.create", filepath.Base(dest), nil)
	return &BackupCreateResult{Path: dest}, nil
}

type BackupRestoreInput struct {
	SrcPath    string `json:"src_path"`
	Passphrase string `json:"passphrase"`
}

// BackupsRestore overwrites the live store.db + vault.enc with the
// contents of the backup at SrcPath. The caller must restart the app
// for the new files to take effect (in-process SQLite handle keeps
// pointing at the old inode otherwise).
func (a *App) BackupsRestore(in BackupRestoreInput) error {
	if in.Passphrase == "" {
		return fmt.Errorf("passphrase required")
	}
	err := backup.Restore(in.SrcPath, in.Passphrase, store.DefaultPath(), creds.DefaultPath())
	if err != nil {
		a.recordAudit("backup.restore.failed", filepath.Base(in.SrcPath), map[string]string{"error": err.Error()})
		return err
	}
	a.recordAudit("backup.restore", filepath.Base(in.SrcPath), nil)
	return nil
}

func (a *App) BackupsList() ([]backup.Info, error) {
	return backup.List(store.DataDir())
}

// AutoBackupPrefs is the IPC-visible shape of the scheduler config the
// user can edit. KeepLast clamps at the backend; UI shows a number
// input.
type AutoBackupPrefs struct {
	Enabled  bool `json:"enabled"`
	KeepLast int  `json:"keep_last"`
}

func (a *App) AutoBackupPrefsGet() AutoBackupPrefs {
	enabled := false
	if v, ok, _ := a.db.GetSetting("auto_backup_enabled"); ok && v == "1" {
		enabled = true
	}
	keep := 7
	if v, ok, _ := a.db.GetSetting("auto_backup_keep_last"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			keep = n
		}
	}
	return AutoBackupPrefs{Enabled: enabled, KeepLast: keep}
}

func (a *App) AutoBackupPrefsSet(p AutoBackupPrefs) error {
	if p.KeepLast <= 0 {
		p.KeepLast = 7
	}
	if p.KeepLast > 365 {
		p.KeepLast = 365
	}
	enabledStr := "0"
	if p.Enabled {
		enabledStr = "1"
	}
	if err := a.db.SetSetting("auto_backup_enabled", enabledStr); err != nil {
		return err
	}
	if err := a.db.SetSetting("auto_backup_keep_last", strconv.Itoa(p.KeepLast)); err != nil {
		return err
	}
	a.applyBackupSchedulerConfig()
	return nil
}

// applyBackupSchedulerConfig pushes the current settings into the live
// scheduler. Safe to call from any goroutine - Scheduler.SetConfig swaps
// the pointer atomically.
func (a *App) applyBackupSchedulerConfig() {
	if a.backupSched == nil {
		return
	}
	prefs := a.AutoBackupPrefsGet()
	a.backupSched.SetConfig(backup.SchedulerConfig{
		Enabled:      prefs.Enabled,
		KeepLast:     prefs.KeepLast,
		StoreDBPath:  store.DefaultPath(),
		VaultEncPath: creds.DefaultPath(),
		AppVersion:   appVersion,
		GetPassphrase: func() (string, error) {
			return creds.ReadSidecar(creds.DefaultPath())
		},
		OnEvent: func(kind, msg string) {
			log.Printf("backup scheduler %s: %s", kind, msg)
		},
	})
}

// ----- Sync (encrypted WebDAV snapshots) -----
//
// Free-tier personal sync: the whole profile travels as the same
// sealed envelope backups use. URL + username live in settings;
// the WebDAV password and the sync passphrase live in the VAULT
// (sync therefore requires an unlocked vault). The sync passphrase
// is deliberately independent of the vault passphrase - a fresh
// machine types it once for the first pull.

const (
	syncVaultPasswordKey   = "sync:webdav_password"
	syncVaultPassphraseKey = "sync:passphrase"
	// Inline SFTP auth secrets (mode=inline). Live in the vault like the
	// WebDAV password, so a fresh machine types them once and bootstraps.
	syncSftpPasswordKey      = "sync:sftp_password"
	syncSftpKeyKey           = "sync:sftp_key"            // private key PEM
	syncSftpKeyPassphraseKey = "sync:sftp_key_passphrase" // for an encrypted key
)

type SyncConfig struct {
	URL           string `json:"url"`
	Username      string `json:"username"`
	HasPassword   bool   `json:"has_password"`
	HasPassphrase bool   `json:"has_passphrase"`
	Generation    int64  `json:"generation"`
	LastSyncAt    int64  `json:"last_sync_at"`
	Device        string `json:"device"`
	Auto          bool   `json:"auto"`
	AutoApply     bool   `json:"auto_apply"`
	CheckMinutes  int    `json:"check_minutes"`

	// Transport selects the backend: "webdav" (default) or "sftp". The
	// WebDAV fields above (URL/Username/HasPassword) drive webdav; the
	// SFTP fields below drive sftp. The passphrase + generation are
	// transport-independent.
	Transport    string `json:"transport"`
	SftpHost     string `json:"sftp_host"`
	SftpPort     int    `json:"sftp_port"`
	SftpUser     string `json:"sftp_user"`
	SftpDir      string `json:"sftp_dir"`
	SftpAuthMode string `json:"sftp_auth_mode"` // "credential" | "inline"
	SftpCredID   string `json:"sftp_cred_id"`   // credential ref from the vault tree (mode=credential)
	SftpCredName string `json:"sftp_cred_name"` // resolved name for display
	// Inline auth (mode=inline) so a fresh machine can bootstrap without
	// the tree credential it would otherwise need (which only arrives WITH
	// the first pull). Secrets live in the vault; these flags just report
	// presence to the UI.
	SftpHasPassword bool `json:"sftp_has_password"`
	SftpHasKey      bool `json:"sftp_has_key"`
}

func (a *App) syncGeneration() int64 {
	if v, ok, _ := a.db.GetSetting("sync_generation"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func (a *App) SyncConfigGet() SyncConfig {
	cfg := SyncConfig{Generation: a.syncGeneration(), Device: syncer.DefaultDevice()}
	if v, ok, _ := a.db.GetSetting("sync_webdav_url"); ok {
		cfg.URL = v
	}
	if v, ok, _ := a.db.GetSetting("sync_webdav_username"); ok {
		cfg.Username = v
	}
	if v, ok, _ := a.db.GetSetting("sync_last_at"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.LastSyncAt = n
		}
	}
	if _, ok, _ := a.vault.Get(syncVaultPasswordKey); ok {
		cfg.HasPassword = true
	}
	if _, ok, _ := a.vault.Get(syncVaultPassphraseKey); ok {
		cfg.HasPassphrase = true
	}
	cfg.Auto = a.syncAutoEnabled()
	cfg.AutoApply = a.syncAutoApplyEnabled()
	cfg.CheckMinutes = a.syncCheckMinutes()

	cfg.Transport = "webdav"
	if v, ok, _ := a.db.GetSetting("sync_transport"); ok && v != "" {
		cfg.Transport = v
	}
	if v, ok, _ := a.db.GetSetting("sync_sftp_host"); ok {
		cfg.SftpHost = v
	}
	cfg.SftpPort = 22
	if v, ok, _ := a.db.GetSetting("sync_sftp_port"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SftpPort = n
		}
	}
	if v, ok, _ := a.db.GetSetting("sync_sftp_user"); ok {
		cfg.SftpUser = v
	}
	if v, ok, _ := a.db.GetSetting("sync_sftp_dir"); ok {
		cfg.SftpDir = v
	}
	if v, ok, _ := a.db.GetSetting("sync_sftp_cred_id"); ok && v != "" {
		cfg.SftpCredID = v
		if cred, err := a.db.GetCredential(v); err == nil && cred != nil {
			cfg.SftpCredName = cred.Name
		}
	}
	cfg.SftpAuthMode = "credential"
	if v, ok, _ := a.db.GetSetting("sync_sftp_auth_mode"); ok && v != "" {
		cfg.SftpAuthMode = v
	}
	if _, ok, _ := a.vault.Get(syncSftpPasswordKey); ok {
		cfg.SftpHasPassword = true
	}
	if _, ok, _ := a.vault.Get(syncSftpKeyKey); ok {
		cfg.SftpHasKey = true
	}
	return cfg
}

// SyncConfigSet stores the sync settings. Empty password/passphrase
// keeps the existing vault value (the UI sends blanks unless changed).
func (a *App) SyncConfigSet(url, username, webdavPassword, passphrase string) error {
	if strings.TrimSpace(url) != "" {
		if err := syncer.ValidateURL(url); err != nil {
			return err
		}
	}
	if err := a.db.SetSetting("sync_webdav_url", strings.TrimSpace(url)); err != nil {
		return err
	}
	if err := a.db.SetSetting("sync_webdav_username", strings.TrimSpace(username)); err != nil {
		return err
	}
	if webdavPassword != "" {
		if err := a.vault.Put(syncVaultPasswordKey, webdavPassword); err != nil {
			return fmt.Errorf("vault: %w (unlock the vault first)", err)
		}
	}
	if passphrase != "" {
		if err := a.vault.Put(syncVaultPassphraseKey, passphrase); err != nil {
			return fmt.Errorf("vault: %w (unlock the vault first)", err)
		}
	}
	return nil
}

// SyncTransportSet selects the sync backend ("webdav" or "sftp").
func (a *App) SyncTransportSet(transport string) error {
	switch transport {
	case "webdav", "sftp":
	default:
		return fmt.Errorf("unknown sync transport %q", transport)
	}
	return a.db.SetSetting("sync_transport", transport)
}

// SyncSftpConfigInput carries the SFTP sync settings. Auth is one of two
// modes:
//   - "credential": reuse a vault credential from the connection tree
//     (CredID). Convenient on a machine that already has the credential.
//   - "inline": type the auth in directly (InlinePassword OR InlineKeyPEM,
//     with InlineKeyPassphrase for an encrypted key). Stored in the vault
//     like the WebDAV password - the only mode that lets a FRESH machine
//     bootstrap, since a tree credential doesn't exist until the first pull
//     brings it in.
//
// Blank inline secrets / passphrase keep the existing saved values (the UI
// sends blanks unless changed). The snapshot-sealing passphrase is
// transport-independent.
type SyncSftpConfigInput struct {
	Host                string `json:"host"`
	Port                int    `json:"port"`
	User                string `json:"user"`
	Dir                 string `json:"dir"`
	AuthMode            string `json:"auth_mode"` // "credential" | "inline"
	CredID              string `json:"cred_id"`
	InlinePassword      string `json:"inline_password"`
	InlineKeyPEM        string `json:"inline_key_pem"`
	InlineKeyPassphrase string `json:"inline_key_passphrase"`
	Passphrase          string `json:"passphrase"` // snapshot-sealing passphrase
}

func (a *App) SyncSftpConfigSet(in SyncSftpConfigInput) error {
	host := strings.TrimSpace(in.Host)
	if host == "" {
		return fmt.Errorf("SFTP host is required")
	}
	port := in.Port
	if port <= 0 {
		port = 22
	}
	if strings.TrimSpace(in.User) == "" {
		return fmt.Errorf("SFTP username is required")
	}
	if strings.TrimSpace(in.Dir) == "" {
		return fmt.Errorf("SFTP remote directory is required")
	}
	mode := in.AuthMode
	if mode == "" {
		mode = "credential"
	}
	switch mode {
	case "credential":
		if strings.TrimSpace(in.CredID) == "" {
			return fmt.Errorf("pick a credential, or switch to inline auth")
		}
		if _, err := a.db.GetCredential(in.CredID); err != nil {
			return fmt.Errorf("SFTP credential: %w", err)
		}
	case "inline":
		// Stored secrets count too - a blank input keeps the saved one.
		_, hasPw, _ := a.vault.Get(syncSftpPasswordKey)
		_, hasKey, _ := a.vault.Get(syncSftpKeyKey)
		if in.InlinePassword == "" && in.InlineKeyPEM == "" && !hasPw && !hasKey {
			return fmt.Errorf("inline auth needs a password or a private key")
		}
		// Validate a freshly pasted key before saving it.
		if in.InlineKeyPEM != "" {
			if _, err := sshlayer.InlineAuthMethods("", in.InlineKeyPEM, in.InlineKeyPassphrase); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown auth mode %q", mode)
	}
	for k, v := range map[string]string{
		"sync_sftp_host":      host,
		"sync_sftp_port":      strconv.Itoa(port),
		"sync_sftp_user":      strings.TrimSpace(in.User),
		"sync_sftp_dir":       strings.TrimSpace(in.Dir),
		"sync_sftp_auth_mode": mode,
		"sync_sftp_cred_id":   strings.TrimSpace(in.CredID),
	} {
		if err := a.db.SetSetting(k, v); err != nil {
			return err
		}
	}
	if in.InlinePassword != "" {
		if err := a.vault.Put(syncSftpPasswordKey, in.InlinePassword); err != nil {
			return fmt.Errorf("vault: %w (unlock the vault first)", err)
		}
	}
	if in.InlineKeyPEM != "" {
		if err := a.vault.Put(syncSftpKeyKey, in.InlineKeyPEM); err != nil {
			return fmt.Errorf("vault: %w (unlock the vault first)", err)
		}
		// Key passphrase only matters alongside a key; store/clear it with it.
		if err := a.vault.Put(syncSftpKeyPassphraseKey, in.InlineKeyPassphrase); err != nil {
			return fmt.Errorf("vault: %w", err)
		}
	}
	if in.Passphrase != "" {
		if err := a.vault.Put(syncVaultPassphraseKey, in.Passphrase); err != nil {
			return fmt.Errorf("vault: %w (unlock the vault first)", err)
		}
	}
	return nil
}

// syncClient builds the configured sync Transport + the snapshot passphrase.
// Returns a syncer.Transport interface so Push/Pull/FetchMeta are
// transport-agnostic. Errors are user-actionable ("set X first").
func (a *App) syncClient() (syncer.Transport, string, error) {
	cfg := a.SyncConfigGet()
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return nil, "", fmt.Errorf("unlock the vault first - sync credentials live in it")
	}
	phrase, _, _ := a.vault.Get(syncVaultPassphraseKey)

	if cfg.Transport == "sftp" {
		t, err := a.sftpSyncTransport(cfg)
		if err != nil {
			return nil, "", err
		}
		return t, phrase, nil
	}

	// Default: WebDAV.
	if cfg.URL == "" {
		return nil, "", fmt.Errorf("sync URL is not configured")
	}
	// Defence in depth: the setting may predate the https policy or
	// have been edited in the DB by hand.
	if err := syncer.ValidateURL(cfg.URL); err != nil {
		return nil, "", err
	}
	pass, _, _ := a.vault.Get(syncVaultPasswordKey)
	dav := &syncer.WebDAV{BaseURL: cfg.URL, Username: cfg.Username, Password: pass}
	return dav, phrase, nil
}

// sftpSyncTransport builds the SFTP transport from the saved config,
// resolving the chosen vault credential into SSH auth methods and reusing
// the connection layer's known-hosts callback + algo pinning so a sync host
// is trusted exactly like any other host in the tree.
func (a *App) sftpSyncTransport(cfg SyncConfig) (*syncer.SFTP, error) {
	if cfg.SftpHost == "" {
		return nil, fmt.Errorf("SFTP host is not configured")
	}
	if cfg.SftpUser == "" {
		return nil, fmt.Errorf("SFTP username is not configured")
	}
	if cfg.SftpDir == "" {
		return nil, fmt.Errorf("SFTP remote directory is not configured")
	}

	var methods []gossh.AuthMethod
	switch cfg.SftpAuthMode {
	case "inline":
		// Bootstrap-friendly: auth typed in directly, secrets from the vault.
		pw, _, _ := a.vault.Get(syncSftpPasswordKey)
		keyPEM, _, _ := a.vault.Get(syncSftpKeyKey)
		keyPass, _, _ := a.vault.Get(syncSftpKeyPassphraseKey)
		m, err := sshlayer.InlineAuthMethods(pw, keyPEM, keyPass)
		if err != nil {
			return nil, fmt.Errorf("SFTP inline auth: %w", err)
		}
		methods = m
	default: // "credential"
		if cfg.SftpCredID == "" {
			return nil, fmt.Errorf("SFTP sync needs a credential - pick one from the vault, or use inline auth")
		}
		cred, err := a.db.GetCredential(cfg.SftpCredID)
		if err != nil {
			return nil, fmt.Errorf("SFTP credential: %w", err)
		}
		auth, err := sshlayer.ResolveAuth(context.Background(), cred, a.vault)
		if err != nil {
			return nil, fmt.Errorf("SFTP credential: %w", err)
		}
		methods = auth.ToAuthMethods()
		if len(methods) == 0 {
			return nil, fmt.Errorf("SFTP credential has no usable auth method")
		}
	}
	port := cfg.SftpPort
	if port <= 0 {
		port = 22
	}
	var algos []string
	if lk := a.makeAlgoLookup(); lk != nil {
		algos = lk(cfg.SftpHost, port)
	}
	return &syncer.SFTP{
		Host:              cfg.SftpHost,
		Port:              port,
		User:              cfg.SftpUser,
		Dir:               cfg.SftpDir,
		Auth:              methods,
		HostKey:           a.makeHostKeyCallback(),
		HostKeyAlgorithms: algos,
		Timeout:           a.connectTimeout(),
	}, nil
}

// SyncStatusResult compares local vs remote generation.
type SyncStatusResult struct {
	State            string `json:"state"` // empty | in_sync | remote_ahead | remote_behind
	LocalGeneration  int64  `json:"local_generation"`
	RemoteGeneration int64  `json:"remote_generation"`
	RemoteDevice     string `json:"remote_device"`
	RemoteUpdatedAt  string `json:"remote_updated_at"`
	SnapshotSize     int64  `json:"snapshot_size"`
	// LocalDirty: this machine has changes since its last push. When
	// the remote is ahead AND local is clean, a pull is lossless -
	// the quick-pull path uses that to skip the Settings detour.
	LocalDirty bool `json:"local_dirty"`
}

func (a *App) SyncStatus() (*SyncStatusResult, error) {
	dav, _, err := a.syncClient()
	if err != nil {
		return nil, err
	}
	defer dav.Close()
	res := &SyncStatusResult{
		LocalGeneration: a.syncGeneration(),
		LocalDirty:      a.syncDirty(),
	}
	meta, err := syncer.FetchMeta(dav)
	if err == syncer.ErrNotFound {
		res.State = "empty"
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	res.RemoteGeneration = meta.Generation
	res.RemoteDevice = meta.Device
	res.RemoteUpdatedAt = meta.UpdatedAt
	res.SnapshotSize = meta.SnapshotSize
	switch {
	case meta.Generation == res.LocalGeneration:
		res.State = "in_sync"
	case meta.Generation > res.LocalGeneration:
		res.State = "remote_ahead"
	default:
		res.State = "remote_behind"
	}
	return res, nil
}

// SyncPush seals + uploads the profile. The new generation is
// persisted BEFORE sealing so the uploaded snapshot carries it (a
// machine that pulls it comes up in-sync); on failure it's rolled
// back so the next attempt's guard math stays right.
func (a *App) SyncPush(force bool) (*syncer.PushResult, error) {
	dav, phrase, err := a.syncClient()
	if err != nil {
		return nil, err
	}
	defer dav.Close()
	prevGen := a.syncGeneration()
	res, err := syncer.Push(dav, store.DefaultPath(), creds.DefaultPath(), phrase,
		appVersion, syncer.DefaultDevice(), prevGen, force,
		func(gen int64) error {
			return a.db.SetSetting("sync_generation", strconv.FormatInt(gen, 10))
		})
	if err != nil {
		_ = a.db.SetSetting("sync_generation", strconv.FormatInt(prevGen, 10))
		return nil, err
	}
	_ = a.db.SetSetting("sync_last_at", strconv.FormatInt(time.Now().Unix(), 10))
	a.recordSyncPushedFingerprint()
	a.recordAudit("sync.push", "", map[string]string{
		"generation": strconv.FormatInt(res.Generation, 10),
		"force":      strconv.FormatBool(force),
	})
	return res, nil
}

// SyncAutoSet flips the auto-sync loop (push-on-change + periodic
// remote check) and its check interval.
func (a *App) SyncAutoSet(enabled bool, checkMinutes int) error {
	v := "0"
	if enabled {
		v = "1"
	}
	if err := a.db.SetSetting("sync_auto", v); err != nil {
		return err
	}
	if checkMinutes < 1 {
		checkMinutes = 5
	}
	return a.db.SetSetting("sync_check_minutes", strconv.Itoa(checkMinutes))
}

// SyncAutoApplySet toggles automatic background pull (apply incoming
// changes without asking). Independent of auto-push; meaningful only
// when auto sync is on.
func (a *App) SyncAutoApplySet(enabled bool) error {
	v := "0"
	if enabled {
		v = "1"
	}
	return a.db.SetSetting("sync_auto_apply", v)
}

// SyncPull downloads the remote snapshot and stages it through the
// backup-restore path. The user must quit + reopen to apply; the
// restored profile carries its own sync_generation, so no local
// bookkeeping here.
func (a *App) SyncPull() (*syncer.PullResult, error) {
	dav, phrase, err := a.syncClient()
	if err != nil {
		return nil, err
	}
	defer dav.Close()
	res, err := syncer.Pull(dav, phrase, store.DefaultPath(), creds.DefaultPath())
	if err != nil {
		return nil, err
	}
	a.recordAudit("sync.pull", "", map[string]string{
		"generation": strconv.FormatInt(res.Generation, 10),
		"device":     res.Device,
	})
	return res, nil
}

// SyncPullLiveResult reports how a live pull resolved.
type SyncPullLiveResult struct {
	Generation int64  `json:"generation"`
	Device     string `json:"device"`
	UpdatedAt  string `json:"updated_at"`
	// VaultRestartNeeded is true when the store was mirrored live but
	// the vault couldn't be merged in-place (the snapshot's secrets are
	// under a different vault passphrase than this machine's). The
	// store changes are already applied; the secrets need a staged
	// restart. False = fully applied, no restart.
	VaultRestartNeeded bool `json:"vault_restart_needed"`
}

// SyncPullLive applies a pulled profile WITHOUT a restart: the store is
// mirrored into the running database (SSH sessions survive) and the
// vault's secrets are re-encrypted under the local key and merged into
// the unlocked vault. The frontend reloads its stores afterward.
//
// The vault merge needs the snapshot's vault passphrase. For a single
// user with the same passphrase everywhere that's the local one (read
// from the machine sidecar), so it's silent. If that doesn't open the
// snapshot's vault (different passphrase across machines), the store is
// still applied and VaultRestartNeeded is returned so the caller can
// stage a restart for the secrets - never a passphrase prompt.
func (a *App) SyncPullLive() (*SyncPullLiveResult, error) {
	dav, phrase, err := a.syncClient()
	if err != nil {
		return nil, err
	}
	defer dav.Close()
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return nil, fmt.Errorf("unlock the vault first")
	}

	meta, err := syncer.FetchMeta(dav)
	if err == syncer.ErrNotFound {
		return nil, fmt.Errorf("nothing to pull - the sync folder is empty")
	}
	if err != nil {
		return nil, err
	}
	blob, err := dav.Get("snapshot.stb")
	if err != nil {
		return nil, err
	}
	tmpEnv, err := os.CreateTemp("", "ssh-tool-pull-*.stb")
	if err != nil {
		return nil, err
	}
	tmpEnvPath := tmpEnv.Name()
	_, _ = tmpEnv.Write(blob)
	_ = tmpEnv.Close()
	defer os.Remove(tmpEnvPath)

	storeTmp, vaultTmp, err := backup.Extract(tmpEnvPath, phrase)
	if err != nil {
		return nil, err
	}
	defer os.Remove(storeTmp)
	defer os.Remove(vaultTmp)

	// Mirror the store into the live DB (sessions unaffected).
	if err := a.db.MirrorFrom(storeTmp); err != nil {
		return nil, fmt.Errorf("apply profile: %w", err)
	}

	// Merge the vault in place using the local passphrase (same-user,
	// same-passphrase case). A wrong-passphrase error means the
	// snapshot's vault is under a different passphrase - leave the
	// store applied and signal a restart for the secrets.
	restartNeeded := false
	// Desktop reads the machine-bound sidecar; android has no sidecar but
	// may hold the passphrase in the Keystore-backed secure store (set when
	// the user opted into biometric auto-unlock). localAutoUnlockPass
	// abstracts both so a same-passphrase pull applies live everywhere.
	localPass := a.localAutoUnlockPass()
	if localPass == "" {
		// No machine-bound auto-unlock passphrase available: can't
		// decrypt the snapshot vault in place, fall back to a staged
		// swap on restart.
		restartNeeded = true
	} else if err := a.vault.MergeSnapshotVault(vaultTmp, localPass); err != nil {
		// Snapshot vault is under a different passphrase than this
		// machine's - stage the secrets for restart instead.
		log.Printf("sync live pull: vault merge needs restart: %v", err)
		restartNeeded = true
	}

	if restartNeeded {
		// Stage the vault for a restart-time swap (store is already
		// applied, so stage only needs to carry the new secrets; the
		// existing pending-restore path swaps both files but the
		// re-mirrored store is identical, so it's a no-op for store).
		if err := backup.Restore(tmpEnvPath, phrase, store.DefaultPath(), creds.DefaultPath()); err != nil {
			return nil, fmt.Errorf("stage vault for restart: %w", err)
		}
	}

	// New clean baseline - the live store now equals the snapshot.
	a.recordSyncPushedFingerprint()
	_ = a.db.SetSetting("sync_generation", strconv.FormatInt(meta.Generation, 10))
	a.syncNotifiedGen = 0
	a.recordAudit("sync.pull", "", map[string]string{
		"generation": strconv.FormatInt(meta.Generation, 10),
		"device":     meta.Device,
		"mode":       "live",
	})
	EventsEmit("profile_reloaded", meta.Generation)
	return &SyncPullLiveResult{
		Generation:         meta.Generation,
		Device:             meta.Device,
		UpdatedAt:          meta.UpdatedAt,
		VaultRestartNeeded: restartNeeded,
	}, nil
}

// AppRelaunch spawns a fresh instance of this binary and quits the
// current one - the "restart to apply" step after a sync pull or
// backup restore, without making the user find the icon again. The
// child gets SSH_TOOL_WAIT_PID so its startup waits for this process
// to release store.db (and so it doesn't hand itself off to us via
// the single-instance socket and exit).
func (a *App) AppRelaunch() error {
	return a.relaunchApp()
}

func (a *App) BackupsDelete(path string) error {
	// Defence-in-depth: only allow paths under the backups subdir.
	dir := backup.DefaultDir(store.DataDir())
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(abs, dir+string(filepath.Separator)) {
		return fmt.Errorf("refusing to delete outside backups dir")
	}
	return os.Remove(abs)
}

// ----- SSH -----

type SshConnectResult struct {
	SessionID string `json:"session_id"`
	// NetworkVia is the network profile NAME when the first hop
	// dialed through its WireGuard tunnel; empty for plain dials and
	// when an auto/paused policy went direct. Drives the pane VPN
	// badge - only truthful tunnel use shows it.
	NetworkVia string `json:"network_via,omitempty"`
}

// HostKeyChallengeEvent is emitted as a Wails event when a server presents
// an unknown or changed host key. The frontend must respond via SshRespondHostKey.
type HostKeyChallengeEvent struct {
	ChallengeID    string `json:"challenge_id"`
	Hostname       string `json:"hostname"`
	Port           int    `json:"port"`
	KeyType        string `json:"key_type"`
	Fingerprint    string `json:"fingerprint"`
	KeyB64         string `json:"key_b64"`
	Status         string `json:"status"` // "unknown" | "changed"
	OldFingerprint string `json:"old_fingerprint,omitempty"`
}

func (a *App) SshConnect(connectionID string) (*SshConnectResult, error) {
	return a.sshConnectInternal(connectionID, "", "", "")
}

// SshConnectWithOverride is identical to SshConnect except it forces
// the credential used for the target hop to overrideCredentialID for
// this one attempt. The connection's persisted auth_ref is NOT
// modified - the override is in-memory only and never survives the
// call. Empty overrideCredentialID falls through to the regular
// resolution path (same as SshConnect).
func (a *App) SshConnectWithOverride(connectionID, overrideCredentialID string) (*SshConnectResult, error) {
	return a.sshConnectInternal(connectionID, overrideCredentialID, "", "")
}

// SshConnectAdvanced lets the caller override credentialID, username,
// and / or password for one attempt. Empty strings on each field
// mean "use whatever resolution would normally pick"; a non-empty
// value overrides. The three knobs are independent - e.g. override
// just the username while keeping the persisted credential, or
// supply a one-shot password without a credential. Returned session
// uses the overridden values; persisted connection is unchanged.
func (a *App) SshConnectAdvanced(connectionID, overrideCredentialID, overrideUsername, overridePassword string) (*SshConnectResult, error) {
	return a.sshConnectInternal(connectionID, overrideCredentialID, overrideUsername, overridePassword)
}

// connectCancel is a uniquely-identifiable cancel handle so a deregister
// can tell its own entry apart from a superseding attempt's.
type connectCancel struct {
	cancel context.CancelFunc
}

// registerConnectCancel stores a cancel handle under key and returns a
// teardown safe to defer. If a connect for the same key is already in flight
// its handle is replaced (the new attempt supersedes the old); the old
// attempt's deregister then no-ops because the stored pointer differs.
func (a *App) registerConnectCancel(key string, cancel context.CancelFunc) func() {
	h := &connectCancel{cancel: cancel}
	a.connectCancelsMu.Lock()
	a.connectCancels[key] = h
	a.connectCancelsMu.Unlock()
	return func() {
		a.connectCancelsMu.Lock()
		if a.connectCancels[key] == h {
			delete(a.connectCancels, key)
		}
		a.connectCancelsMu.Unlock()
	}
}

// SshCancelConnect aborts an in-flight connect for connectionID (or a
// dynamic entry key) that's blocked on opkssh OIDC login. Safe to call when
// nothing is in flight - it's a no-op then. Returns true if a connect was
// actually cancelled.
func (a *App) SshCancelConnect(key string) bool {
	a.connectCancelsMu.Lock()
	h := a.connectCancels[key]
	a.connectCancelsMu.Unlock()
	if h == nil {
		return false
	}
	log.Printf("ssh: cancelling in-flight connect %s", key)
	h.cancel()
	return true
}

func (a *App) sshConnectInternal(connectionID, overrideCredentialID, overrideUsername, overridePassword string) (*SshConnectResult, error) {
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, err
	}
	if settings.Hostname == "" {
		return nil, fmt.Errorf("connection has no hostname")
	}

	// Per-attempt credential override. We swap settings.AuthRef
	// before any of the downstream code reads it. The override
	// applies to the target hop only - jump hosts keep their
	// inherited credentials so the user doesn't accidentally
	// expose a temp credential to bastions in the chain.
	if overrideCredentialID != "" {
		oc := overrideCredentialID
		settings.AuthRef = &oc
		// Clear PasswordOverride so the override credential's
		// auth methods run cleanly - mixing the two would
		// double-attempt and could harvest the password against
		// a honeypot.
		settings.PasswordOverride = nil
	}

	// Load the raw connection to access fields that live outside the
	// inheritable settings (password_vault_key, icon, etc.).
	rawConn, connErr := a.db.GetConnection(connectionID)

	// Resolve per-connection password override from the vault.
	// Skip when a credential override is in effect (see above).
	if overrideCredentialID == "" && connErr == nil && rawConn.PasswordVaultKey != nil {
		if pass, ok, _ := a.vault.Get(*rawConn.PasswordVaultKey); ok && pass != "" {
			settings.PasswordOverride = &pass
		}
	}

	// One-shot username override (advanced UI). Empty value means
	// "leave whatever is resolved".
	if overrideUsername != "" {
		ou := overrideUsername
		settings.Username = &ou
	}
	// One-shot password override. Plain text from the UI - never
	// persisted. Wins over any credential password methods (the
	// SSH layer appends password override last in auth method
	// list, so key auth still runs first if a credential is set).
	if overridePassword != "" {
		op := overridePassword
		settings.PasswordOverride = &op
	}

	// If no auth method is available at all, fail fast with a clear message.
	if settings.AuthRef == nil && settings.PasswordOverride == nil {
		return nil, fmt.Errorf("no credential and no password set for this connection")
	}

	// If no username resolved from folder/connection tree, fall back to the
	// credential's default_username (useful for imported credentials).
	if settings.Username == nil && settings.AuthRef != nil {
		if cred, err2 := a.db.GetCredential(*settings.AuthRef); err2 == nil && cred.DefaultUsername != nil {
			settings.Username = cred.DefaultUsername
		}
	}
	a.resetDebug(connectionID)
	sink := wailsSink{app: a, connectionID: connectionID}
	// Resolve the user-configurable connect timeout (app-wide setting,
	// stored as seconds). Falls back to the layer's default if missing
	// or invalid.
	var ct time.Duration
	if raw := a.SettingsGet("connect_timeout_seconds"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			ct = time.Duration(n) * time.Second
		}
	}
	// Live progress hint for the DetailPane spinner. Keyed on
	// connectionID because the frontend doesn't have a sessionID yet
	// (SshConnect blocks until the session is built).
	progress := func(stage string) {
		EventsEmit("connect_progress:"+connectionID, stage)
	}
	// Cancelable connect context so SshCancelConnect(connectionID) can abort
	// a hang on opkssh OIDC login (closed browser / bad config). Keyed on
	// connectionID. Deregistered when this connect returns.
	ctx, cancel := context.WithCancel(context.Background())
	deregister := a.registerConnectCancel(connectionID, cancel)
	defer deregister()
	defer cancel()
	sess, err := sshlayer.Connect(ctx, a.db, a.vault, settings, sink, a.makeHostKeyCallback(), a.makeAlgoLookup(), ct, progress)
	if err != nil {
		log.Printf("ssh connect %s (%s): %v", connectionID, settings.Hostname, err)
		// Vault-locked is a recoverable failure class: surface it
		// as a typed event so the frontend can pop VaultGate
		// instead of leaving the user staring at "password missing"
		// with no obvious next step.
		if sshlayer.ContainsVaultLocked(err) {
			EventsEmit("vault_locked_during_connect", map[string]string{
				"connection_id": connectionID,
				"hostname":      settings.Hostname,
				"message":       err.Error(),
			})
		}
		// Keep the debug buffer around - frontend will pull it via
		// SshGetConnectDebug to render the failure context.
		return nil, err
	}
	a.pool.Add(sess)
	a.syncForegroundService()
	// Recents are machine-local state - a store.db write here dirtied
	// the sync profile on every single connect.
	a.touchRecent(connectionID)

	// Stash meta for SshActiveSessions recovery.
	conn, _ := a.db.GetConnection(connectionID)
	a.metaMu.Lock()
	a.sessionMeta[sess.ID] = sessionMetaEntry{
		connectionID: connectionID,
		name:         conn.Name,
		hostname:     settings.Hostname,
	}
	a.metaMu.Unlock()

	// On session end (user disconnect OR server killed OR network drop):
	// tear down any forwards we registered, drop the pool entry. If the
	// resolved settings have auto_reconnect=true AND this wasn't a user-
	// initiated Disconnect, kick off a retry loop.
	connID := connectionID
	autoReconnect := settings.AutoReconnect
	sess.SetOnClose(func(id string) {
		a.forwards.StopAllForSession(id)
		a.sessionRecordingCleanup(id)
		userInit := false
		if s, ok := a.pool.Get(id); ok {
			userInit = s.WasUserInitiated()
		}
		a.pool.Remove(id)
		a.wgRelease(id)
		a.syncForegroundService()
		a.metaMu.Lock()
		delete(a.sessionMeta, id)
		a.metaMu.Unlock()
		// Evict from every broadcast group - a dead session can't
		// accept fan-out and we don't want the manager to show
		// ghost members in any group.
		a.broadcastMu.Lock()
		evicted := false
		for _, g := range a.broadcastGroups {
			if g[id] {
				delete(g, id)
				evicted = true
			}
		}
		a.broadcastMu.Unlock()
		if evicted {
			a.emitBroadcastChanged()
		}
		log.Printf("session %s cleaned up (user_initiated=%v)", id, userInit)

		if autoReconnect && !userInit {
			a.spawnReconnect(id, connID)
		}
	})

	// Auto-start any forwards marked auto_start=1 against this connection.
	a.forwardsAutoStartFor(connectionID, sess.ID)

	user := ""
	if settings.Username != nil {
		user = *settings.Username
	}
	a.recordAudit("ssh.connect", connectionID, map[string]string{
		"session_id": sess.ID,
		"host":       settings.Hostname,
		"port":       strconv.Itoa(int(settings.Port)),
		"user":       user,
	})

	return &SshConnectResult{SessionID: sess.ID, NetworkVia: a.wgTrackSession(sess, settings)}, nil
}

// makeAlgoLookup returns a HostKeyAlgoLookup that pins the SSH
// handshake to the algorithm stored in known_hosts for (host, port).
// First-time connects (no row yet) return nil - the lib falls back
// to its default algo preference list, the HostKeyCallback emits a
// TOFU prompt, and the user's choice stores both the bytes and the
// algorithm together (see UpsertKnownHost). Subsequent connects then
// pin. This is what closes the algorithm-downgrade gap (Critical C1
// in the audit): without it an attacker could swap ed25519 for RSA
// and the callback would see the "wrong" row.
func (a *App) makeAlgoLookup() sshlayer.HostKeyAlgoLookup {
	return func(host string, port int) []string {
		stored, err := a.db.GetKnownHost(host, port)
		if err != nil || stored == nil {
			return nil
		}
		return []string{stored.KeyType}
	}
}

// connectTimeout returns the configured SSH connect timeout (0 when unset,
// which callers treat as their own default). Mirrors the inline parsing used
// on the interactive connect paths.
func (a *App) connectTimeout() time.Duration {
	if raw := a.SettingsGet("connect_timeout_seconds"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

func (a *App) makeHostKeyCallback() gossh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		host, portStr, err := net.SplitHostPort(hostname)
		if err != nil {
			host = hostname
			portStr = "22"
		}
		port, _ := strconv.Atoi(portStr)
		if port == 0 {
			port = 22
		}

		keyType := key.Type()
		keyB64 := base64.StdEncoding.EncodeToString(key.Marshal())
		fp := gossh.FingerprintSHA256(key)

		stored, err := a.db.GetKnownHost(host, port)
		if err != nil {
			// Fail CLOSED on DB errors. Returning nil here would
			// silently accept any host key whenever SQLite hiccups
			// (busy lock, disk full, transient driver error), which
			// is a MITM-on-network-poison window. Refuse the connect
			// instead - the user can retry; a persistent DB error is
			// itself a serious bug worth surfacing.
			log.Printf("known_hosts lookup error: %v", err)
			return fmt.Errorf("known_hosts lookup failed: %w", err)
		}

		// Match requires BOTH key_type and key_b64 - pin the algorithm,
		// not just the bytes under the algorithm. Without the type
		// check, an active MITM serving a new RSA host key after the
		// user first trusted ed25519 would land here as an "unknown"
		// (no row for the new algo), which on the legacy schema was
		// silently re-prompted as a new host. With the per-(host,port)
		// row this can't happen - a mismatch on type OR bytes triggers
		// the "changed" branch and a CHANGED-key prompt.
		if stored != nil && stored.KeyType == keyType && stored.KeyB64 == keyB64 {
			return nil // known, matches
		}

		status := "unknown"
		oldFP := ""
		if stored != nil {
			status = "changed"
			oldFP = stored.Fingerprint
		}

		challengeID := uuid.New().String()
		ch := make(chan bool, 1)
		a.pendingHostKeysMu.Lock()
		a.pendingHostKeys[challengeID] = ch
		a.pendingHostKeysMu.Unlock()

		EventsEmit("host_key_challenge", HostKeyChallengeEvent{
			ChallengeID:    challengeID,
			Hostname:       host,
			Port:           port,
			KeyType:        keyType,
			Fingerprint:    fp,
			KeyB64:         keyB64,
			Status:         status,
			OldFingerprint: oldFP,
		})

		log.Printf("host key challenge emitted: %s status=%s fp=%s", challengeID, status, fp)

		// Three exit paths:
		//   - user responded (accept/reject) → use the channel value
		//   - app shutting down → ctx.Done(); treat as reject
		//   - 2-minute timeout → reject + log. Without the timeout a
		//     user who closed the modal without responding (frontend
		//     bug or hard crash with the channel still registered)
		//     leaves this goroutine + the half-open SSH socket
		//     pinned for the rest of the process lifetime.
		const hostKeyTimeout = 2 * time.Minute
		var accepted bool
		select {
		case accepted = <-ch:
		case <-a.ctx.Done():
		case <-time.After(hostKeyTimeout):
			log.Printf("host key challenge %s timed out after %s; rejecting", challengeID, hostKeyTimeout)
		}

		a.pendingHostKeysMu.Lock()
		delete(a.pendingHostKeys, challengeID)
		a.pendingHostKeysMu.Unlock()

		if !accepted {
			return fmt.Errorf("host key rejected by user")
		}
		return nil
	}
}

// SshRespondHostKey is called by the frontend after the user decides on a
// host key challenge. remember=true persists the key to known_hosts.
func (a *App) SshRespondHostKey(challengeID string, accept bool, remember bool, hostname string, port int, keyType string, keyB64 string, fingerprint string) error {
	a.pendingHostKeysMu.Lock()
	ch, ok := a.pendingHostKeys[challengeID]
	a.pendingHostKeysMu.Unlock()
	if !ok {
		return fmt.Errorf("unknown challenge %s", challengeID)
	}

	if accept && remember {
		if err := a.db.UpsertKnownHost(hostname, port, keyType, keyB64, fingerprint); err != nil {
			log.Printf("known_hosts upsert failed: %v", err)
		}
	}

	ch <- accept
	return nil
}

func (a *App) SshWrite(sessionID, dataB64 string) error {
	data, err := sshlayer.DecodeBase64(dataB64)
	if err != nil {
		return fmt.Errorf("invalid base64: %w", err)
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found")
	}
	return sess.Write(data)
}

// SshServerStats runs a one-shot read-only health probe (load / memory /
// disk / logged-in users) on a side channel of the session's SSH client and
// returns the parsed snapshot. Used by the optional status-bar readout for
// the focused session; the frontend only calls this when the feature is on
// and a terminal pane is focused, so no probing happens otherwise. Returns
// OK=false stats (not an error) for a host that answered but yielded no
// parseable metrics; errors only when the session/channel is unavailable.
func (a *App) SshServerStats(sessionID string) (*sshlayer.ServerStats, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	client := sess.TargetClient()
	if client == nil {
		return nil, fmt.Errorf("no live ssh client")
	}
	return sshlayer.FetchServerStats(client)
}

// ----- Broadcast group -----
//
// Single global set of session ids that share keystroke input. State
// lives on the backend so every window sees the same group - a
// detached window still sees the main window's selection, fan-out
// works across windows, and the manager modal in any window shows
// every live session in the pool.

// BroadcastList returns the legacy default-group member ids
// (unordered). Frontend code that hasn't migrated to the
// multi-group API keeps working.
func (a *App) BroadcastList() []string {
	return a.BroadcastListGroup("")
}

// BroadcastListGroup returns the members of one named group.
func (a *App) BroadcastListGroup(groupID string) []string {
	a.broadcastMu.Lock()
	defer a.broadcastMu.Unlock()
	g := a.broadcastGroups[groupID]
	out := make([]string, 0, len(g))
	for id := range g {
		out = append(out, id)
	}
	return out
}

// BroadcastListGroups returns the full group->members snapshot so a
// new client can render every group's chips at startup. Map order
// is unspecified.
func (a *App) BroadcastListGroups() map[string][]string {
	a.broadcastMu.Lock()
	defer a.broadcastMu.Unlock()
	out := make(map[string][]string, len(a.broadcastGroups))
	for g, members := range a.broadcastGroups {
		ids := make([]string, 0, len(members))
		for id := range members {
			ids = append(ids, id)
		}
		out[g] = ids
	}
	return out
}

func (a *App) BroadcastAdd(sessionID string) {
	a.BroadcastAddTo("", sessionID)
}

// BroadcastAddTo adds a session to the named group. Empty groupID
// is the legacy default group.
func (a *App) BroadcastAddTo(groupID, sessionID string) {
	a.broadcastMu.Lock()
	g, ok := a.broadcastGroups[groupID]
	if !ok {
		g = make(map[string]bool)
		a.broadcastGroups[groupID] = g
	}
	g[sessionID] = true
	a.broadcastMu.Unlock()
	a.emitBroadcastChanged()
}

func (a *App) BroadcastRemove(sessionID string) {
	a.BroadcastRemoveFrom("", sessionID)
}

// BroadcastRemoveFrom drops a session from the named group. The
// group itself is left in place even when it goes empty so the user
// can re-add without re-creating it; explicit deletion uses
// BroadcastGroupDelete.
func (a *App) BroadcastRemoveFrom(groupID, sessionID string) {
	a.broadcastMu.Lock()
	if g, ok := a.broadcastGroups[groupID]; ok {
		delete(g, sessionID)
	}
	a.broadcastMu.Unlock()
	a.emitBroadcastChanged()
}

func (a *App) BroadcastClear() {
	a.BroadcastClearGroup("")
}

// BroadcastClearGroup empties one group but keeps it defined.
func (a *App) BroadcastClearGroup(groupID string) {
	a.broadcastMu.Lock()
	a.broadcastGroups[groupID] = make(map[string]bool)
	a.broadcastMu.Unlock()
	a.emitBroadcastChanged()
}

// BroadcastGroupDelete removes the named group entirely. No-op for
// the default group ("") - clear it instead.
func (a *App) BroadcastGroupDelete(groupID string) {
	if groupID == "" {
		return
	}
	a.broadcastMu.Lock()
	delete(a.broadcastGroups, groupID)
	a.broadcastMu.Unlock()
	a.emitBroadcastChanged()
}

// BroadcastSetAll replaces the entire default-group membership in
// one call. Useful for the manager's select-all / invert actions so
// we don't emit N events.
func (a *App) BroadcastSetAll(sessionIDs []string) {
	a.BroadcastSetAllInGroup("", sessionIDs)
}

// BroadcastSetAllInGroup replaces membership of one named group.
func (a *App) BroadcastSetAllInGroup(groupID string, sessionIDs []string) {
	a.broadcastMu.Lock()
	next := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		next[id] = true
	}
	a.broadcastGroups[groupID] = next
	a.broadcastMu.Unlock()
	a.emitBroadcastChanged()
}

// BroadcastFanOut writes dataB64 to every member except originID. Server-
// side fan-out means a keystroke from any window reaches every other
// session even when the originating window is detached and the target
// session lives in a different window's pane tree. Returns an error
// summary string per failing member (empty on full success).
func (a *App) BroadcastFanOut(originID, dataB64 string) string {
	a.broadcastMu.Lock()
	// Union of every group the origin belongs to. A session in two
	// groups broadcasts to both unions; targets de-duplicated.
	targetSet := make(map[string]bool)
	for _, g := range a.broadcastGroups {
		if !g[originID] {
			continue
		}
		for id := range g {
			if id == originID {
				continue
			}
			targetSet[id] = true
		}
	}
	if len(targetSet) == 0 {
		a.broadcastMu.Unlock()
		return ""
	}
	targets := make([]string, 0, len(targetSet))
	for id := range targetSet {
		targets = append(targets, id)
	}
	a.broadcastMu.Unlock()

	data, err := sshlayer.DecodeBase64(dataB64)
	if err != nil {
		return fmt.Sprintf("decode: %v", err)
	}
	var sb strings.Builder
	for _, id := range targets {
		// Look up in the SSH pool first, then fall through to the
		// local PTY pool. Broadcast members can mix the two - a
		// user might want to type into three SSH boxes AND tail
		// the local journal at the same time.
		if sess, ok := a.pool.Get(id); ok {
			if err := sess.Write(data); err != nil {
				sb.WriteString(fmt.Sprintf("%s: %v\n", id, err))
			}
			continue
		}
		if sess, ok := a.localPool.Get(id); ok {
			if err := sess.Write(data); err != nil {
				sb.WriteString(fmt.Sprintf("%s: %v\n", id, err))
			}
			continue
		}
		sb.WriteString(fmt.Sprintf("%s: session not found\n", id))
	}
	return sb.String()
}

func (a *App) emitBroadcastChanged() {
	a.broadcastMu.Lock()
	// Legacy payload: just the default group as a flat list, so
	// existing frontend code (single-group BroadcastStore) keeps
	// working without changes.
	legacy := make([]string, 0)
	if g, ok := a.broadcastGroups[""]; ok {
		for id := range g {
			legacy = append(legacy, id)
		}
	}
	// New payload: full group->members snapshot for clients that
	// have moved to the multi-group view. Separate event so old
	// listeners don't break on shape change.
	groups := make(map[string][]string, len(a.broadcastGroups))
	for gid, members := range a.broadcastGroups {
		ids := make([]string, 0, len(members))
		for id := range members {
			ids = append(ids, id)
		}
		groups[gid] = ids
	}
	a.broadcastMu.Unlock()
	EventsEmit("broadcast_changed", legacy)
	EventsEmit("broadcast_groups_changed", groups)
}

// ScrollbackSnapshot pairs a base64 chunk with the cumulative-byte watermark
// at snapshot time, so the frontend can subscribe-then-snapshot atomically
// and discard re-delivered bytes without losing or duplicating any.
type ScrollbackSnapshot struct {
	B64 string `json:"b64"`
	Cum uint64 `json:"cum"`
}

// SshGetScrollback returns the buffered PTY output for a session plus the
// cumulative-byte watermark. Called by Terminal.svelte on mount: subscribe
// to pty_output first, then call this - events with cum ≤ watermark are
// already in the snapshot, partials straddling the watermark are trimmed.
func (a *App) SshGetScrollback(sessionID string) (ScrollbackSnapshot, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return ScrollbackSnapshot{}, nil // session gone - empty
	}
	data, cum := sess.Scrollback()
	if len(data) == 0 {
		return ScrollbackSnapshot{Cum: cum}, nil
	}
	return ScrollbackSnapshot{B64: sshlayer.EncodeBase64(data), Cum: cum}, nil
}

func (a *App) SshResize(sessionID string, cols, rows uint16) error {
	a.noteTermSize(sessionID, cols, rows)
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil // session gone - no-op
	}
	a.recorder.Resize(sessionID, cols, rows)
	return sess.Resize(cols, rows)
}

// ----- Local shell (in-app PTY) -----

// LocalShellOpenResult mirrors SshConnectResult so the frontend code
// can use the same shape for both kinds of sessions.
type LocalShellOpenResult struct {
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Display   string `json:"display"`
}

// LocalShellOpen spawns a new local PTY session. kind is one of the
// platform-supported shells; "" or "auto" picks a sensible default
// (see internal/local.resolveShell). Output events use the same
// pty_output:<sessionID> channel as SSH so the xterm component
// doesn't need to know which kind it is.
func (a *App) LocalShellOpen(kind, dir string, cols, rows uint16) (*LocalShellOpenResult, error) {
	sess, err := local.Spawn(local.SpawnRequest{
		Kind: kind,
		Cols: cols,
		Rows: rows,
		Dir:  dir,
	})
	if err != nil {
		return nil, err
	}
	sess.ID = uuid.NewString()

	// Wire output sink before Start so the first chunk of shell
	// banner / prompt isn't lost. Mirrors the SSH pumpOutput path.
	sess.SetOutputSink(func(data []byte, cum uint64) {
		a.recorder.Write(sess.ID, data)
		EventsEmit("pty_output:"+sess.ID, sshlayer.OutputPayload{
			B64: sshlayer.EncodeBase64(data),
			Cum: cum,
		})
	})

	// Cleanup on shell exit: drop from pool + tell the frontend the
	// session is gone so the tab can disappear. Also evict from any
	// broadcast group so fan-out from the survivors doesn't hit a
	// ghost session id (SSH cleanup does the same dance).
	sess.SetOnClose(func(id string) {
		a.sessionRecordingCleanup(id)
		a.localPool.Remove(id)
		a.broadcastMu.Lock()
		evicted := false
		for _, g := range a.broadcastGroups {
			if g[id] {
				delete(g, id)
				evicted = true
			}
		}
		a.broadcastMu.Unlock()
		if evicted {
			a.emitBroadcastChanged()
		}
		EventsEmit("session_exit:"+id, 0)
		EventsEmit("local_session_closed:"+id, true)
	})

	a.localPool.Add(sess)
	sess.Start()

	return &LocalShellOpenResult{
		SessionID: sess.ID,
		Kind:      sess.Kind,
		Display:   sess.Display,
	}, nil
}

func (a *App) LocalShellWrite(sessionID, dataB64 string) error {
	sess, ok := a.localPool.Get(sessionID)
	if !ok {
		return nil
	}
	data, err := sshlayer.DecodeBase64(dataB64)
	if err != nil {
		return err
	}
	return sess.Write(data)
}

func (a *App) LocalShellResize(sessionID string, cols, rows uint16) error {
	a.noteTermSize(sessionID, cols, rows)
	sess, ok := a.localPool.Get(sessionID)
	if !ok {
		return nil
	}
	a.recorder.Resize(sessionID, cols, rows)
	return sess.Resize(cols, rows)
}

func (a *App) LocalShellDisconnect(sessionID string) error {
	sess, ok := a.localPool.Get(sessionID)
	if !ok {
		return nil
	}
	sess.Disconnect()
	return nil
}

// ----- Session recording (asciicast v2) -----

// RecordingState is the payload of the "recording_changed" event and
// the return of RecordingStart/Stop. Path is where the .cast file
// lives (final, even after stop).
type RecordingState struct {
	SessionID string `json:"session_id"`
	Recording bool   `json:"recording"`
	Path      string `json:"path"`
}

// noteTermSize remembers the last cols/rows the frontend reported for
// a session. RecordingStart needs them for the asciicast header.
func (a *App) noteTermSize(sessionID string, cols, rows uint16) {
	a.termSizeMu.Lock()
	a.termSizes[sessionID] = [2]uint16{cols, rows}
	a.termSizeMu.Unlock()
}

func (a *App) termSize(sessionID string) (cols, rows uint16) {
	a.termSizeMu.Lock()
	defer a.termSizeMu.Unlock()
	if s, ok := a.termSizes[sessionID]; ok && s[0] > 0 && s[1] > 0 {
		return s[0], s[1]
	}
	return 80, 24
}

// sessionRecordingCleanup runs from every session onClose path: stop
// a live recording (file is finalised, not discarded) and drop the
// cached terminal size. Safe to call for sessions that never recorded.
func (a *App) sessionRecordingCleanup(sessionID string) {
	a.termSizeMu.Lock()
	delete(a.termSizes, sessionID)
	a.termSizeMu.Unlock()
	if path, ok := a.recorder.Stop(sessionID); ok {
		a.recordAudit("recording.stop", sessionID, map[string]string{
			"path": path, "reason": "session_closed",
		})
		EventsEmit("recording_changed", RecordingState{SessionID: sessionID, Recording: false, Path: path})
	}
}

// RecordingsDir returns where new .cast files land. Overridable via
// the "recordings_dir" setting; default <DataDir>/recordings.
func (a *App) RecordingsDir() string {
	if a.db != nil {
		if v, ok, _ := a.db.GetSetting("recordings_dir"); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return filepath.Join(store.DataDir(), "recordings")
}

// RecordingsPickDir opens a native directory picker for the Settings
// "recordings folder" field. Empty result = cancelled.
func (a *App) RecordingsPickDir() (string, error) {
	return OpenDirectoryDialog(OpenFileDialogOptions{
		Title: "Choose where session recordings are saved",
	})
}

// recordingNameFor resolves a human name for the session: connection
// name for SSH (incl. dynamic), shell display for local PTYs.
func (a *App) recordingNameFor(sessionID string) (name, title string) {
	a.metaMu.Lock()
	meta, ok := a.sessionMeta[sessionID]
	a.metaMu.Unlock()
	if ok {
		title = meta.name
		if meta.hostname != "" && meta.hostname != meta.name {
			title = meta.name + " (" + meta.hostname + ")"
		}
		return meta.name, title
	}
	if sess, ok := a.localPool.Get(sessionID); ok {
		return sess.Display, sess.Display
	}
	return "session", ""
}

// RecordingsOpenDir reveals the recordings folder in the OS file
// manager. Creates it first so the button works before the first
// recording ever lands. Not BrowserOpenURL("file://...") - file URLs
// with Windows backslash paths are unreliable through ShellExecute.
func (a *App) RecordingsOpenDir() error {
	dir := a.RecordingsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

// RecordingStart begins capturing the session's PTY output to a new
// asciicast v2 file in RecordingsDir. Output-only: keystrokes are
// never written, so typed secrets can't end up in the file.
func (a *App) RecordingStart(sessionID string) (*RecordingState, error) {
	_, sshOK := a.pool.Get(sessionID)
	_, localOK := a.localPool.Get(sessionID)
	if !sshOK && !localOK {
		return nil, fmt.Errorf("session not found")
	}
	name, title := a.recordingNameFor(sessionID)
	cols, rows := a.termSize(sessionID)
	dir := a.RecordingsDir()
	base := recorder.SuggestedFilename(name, time.Now())
	path, err := a.recorder.Start(sessionID, filepath.Join(dir, base), cols, rows, title)
	if os.IsExist(err) {
		// Same connection recorded twice within one second (split
		// panes) - disambiguate with the session id prefix.
		base = sessionID[:8] + "-" + base
		path, err = a.recorder.Start(sessionID, filepath.Join(dir, base), cols, rows, title)
	}
	if err != nil {
		return nil, err
	}
	a.recordAudit("recording.start", sessionID, map[string]string{"path": path})
	st := RecordingState{SessionID: sessionID, Recording: true, Path: path}
	EventsEmit("recording_changed", st)
	return &st, nil
}

// RecordingStop finalises the session's recording and returns where
// the file landed.
func (a *App) RecordingStop(sessionID string) (*RecordingState, error) {
	path, ok := a.recorder.Stop(sessionID)
	if !ok {
		return nil, fmt.Errorf("session is not being recorded")
	}
	a.recordAudit("recording.stop", sessionID, map[string]string{
		"path": path, "reason": "user",
	})
	st := RecordingState{SessionID: sessionID, Recording: false, Path: path}
	EventsEmit("recording_changed", st)
	return &st, nil
}

// RecordingActive snapshots every live recording so a reloaded /
// detached window can restore its indicators.
func (a *App) RecordingActive() []RecordingState {
	m := a.recorder.ActivePaths()
	out := make([]RecordingState, 0, len(m))
	for id, p := range m {
		out = append(out, RecordingState{SessionID: id, Recording: true, Path: p})
	}
	return out
}

// recordingPathOK rejects paths outside the recordings folder. The
// read/delete IPCs take a path from the frontend list, but the webview
// must not become a generic file reader/deleter.
func (a *App) recordingPathOK(p string) error {
	absDir, err := filepath.Abs(a.RecordingsDir())
	if err != nil {
		return err
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(absP, absDir+string(filepath.Separator)) {
		return fmt.Errorf("path is outside the recordings folder")
	}
	return nil
}

// RecordingsList returns every .cast file in the recordings folder,
// newest first, with parsed header + duration for the browser UI.
func (a *App) RecordingsList() ([]recorder.FileInfo, error) {
	return recorder.ListDir(a.RecordingsDir())
}

// RecordingRead returns the raw asciicast text for the in-app player.
// Capped: a recording bigger than the cap should be played with an
// external tool rather than shipped through the IPC bridge in one
// string.
func (a *App) RecordingRead(path string) (string, error) {
	if err := a.recordingPathOK(path); err != nil {
		return "", err
	}
	const maxPlayable = 32 << 20 // 32 MiB
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() > maxPlayable {
		return "", fmt.Errorf("recording is too large for the in-app player (%d MiB); play it with asciinema instead", st.Size()>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RecordingDelete removes a .cast file. Refuses while the file is
// still being written by a live recording.
func (a *App) RecordingDelete(path string) error {
	if err := a.recordingPathOK(path); err != nil {
		return err
	}
	for _, p := range a.recorder.ActivePaths() {
		if p == path {
			return fmt.Errorf("recording is still in progress - stop it first")
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	a.recordAudit("recording.delete", "", map[string]string{"path": path})
	return nil
}

func (a *App) LocalShellGetScrollback(sessionID string) (ScrollbackSnapshot, error) {
	sess, ok := a.localPool.Get(sessionID)
	if !ok {
		return ScrollbackSnapshot{}, nil
	}
	data, cum := sess.Scrollback()
	if len(data) == 0 {
		return ScrollbackSnapshot{Cum: cum}, nil
	}
	return ScrollbackSnapshot{B64: sshlayer.EncodeBase64(data), Cum: cum}, nil
}

// LocalShellList returns metadata for every live local session so the
// frontend can resurrect tabs after a reload.
type LocalShellInfo struct {
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Display   string `json:"display"`
}

func (a *App) LocalShellList() []LocalShellInfo {
	out := []LocalShellInfo{}
	for _, id := range a.localPool.IDs() {
		s, ok := a.localPool.Get(id)
		if !ok {
			continue
		}
		out = append(out, LocalShellInfo{
			SessionID: s.ID,
			Kind:      s.Kind,
			Display:   s.Display,
		})
	}
	return out
}

// SetConnectionPassword stores a per-connection password in the vault and
// records the vault-key reference on the connection row.
func (a *App) SetConnectionPassword(connectionID, password string) error {
	vaultKey := "conn_pass:" + connectionID
	if err := a.vault.Put(vaultKey, password); err != nil {
		return fmt.Errorf("vault put: %w", err)
	}
	return a.db.SetConnectionPasswordKey(connectionID, vaultKey)
}

// ClearConnectionPassword removes the per-connection password from the vault
// and clears the vault-key reference.
func (a *App) ClearConnectionPassword(connectionID string) error {
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return err
	}
	if conn.PasswordVaultKey != nil {
		_ = a.vault.Delete(*conn.PasswordVaultKey)
	}
	return a.db.ClearConnectionPasswordKey(connectionID)
}

// GetConnectionHasPassword returns true if a per-connection password is stored.
func (a *App) GetConnectionHasPassword(connectionID string) bool {
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return false
	}
	return conn.PasswordVaultKey != nil
}

func (a *App) SshDisconnect(sessionID string) error {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil
	}
	a.metaMu.Lock()
	meta, hasMeta := a.sessionMeta[sessionID]
	a.metaMu.Unlock()
	a.forwards.StopAllForSession(sessionID)
	sess.Disconnect()
	a.pool.Remove(sessionID)
	auditMeta := map[string]string{"session_id": sessionID}
	target := ""
	if hasMeta {
		target = meta.connectionID
		auditMeta["host"] = meta.hostname
		auditMeta["name"] = meta.name
	}
	a.recordAudit("ssh.disconnect", target, auditMeta)
	return nil
}

// ----- Port forwards (persisted spec) -----

func (a *App) ForwardsList(connectionID string) ([]store.PortForward, error) {
	return a.db.ListPortForwards(connectionID)
}

// ForwardsListAll returns every port-forward across all connections.
// The global quick palette uses this to build its tunnel / bookmark
// rows in a single IPC instead of fanning out per-connection.
func (a *App) ForwardsListAll() ([]store.PortForward, error) {
	return a.db.ListAllPortForwards()
}

type ForwardCreateInput struct {
	ConnectionID string  `json:"connection_id"`
	Kind         string  `json:"kind"`
	LocalAddr    *string `json:"local_addr"`
	LocalPort    *uint16 `json:"local_port"`
	RemoteHost   *string `json:"remote_host"`
	RemotePort   *uint16 `json:"remote_port"`
	AutoStart    bool    `json:"auto_start"`
	Description  string  `json:"description"`
}

func (a *App) ForwardsCreate(in ForwardCreateInput) (*store.PortForward, error) {
	return a.db.CreatePortForward(store.NewPortForward{
		ConnectionID: in.ConnectionID,
		Kind:         in.Kind,
		LocalAddr:    in.LocalAddr,
		LocalPort:    in.LocalPort,
		RemoteHost:   in.RemoteHost,
		RemotePort:   in.RemotePort,
		AutoStart:    in.AutoStart,
		Description:  in.Description,
	})
}

type ForwardUpdateInput struct {
	ID              string  `json:"id"`
	LocalAddr       *string `json:"local_addr"`
	ClearLocalAddr  bool    `json:"clear_local_addr"`
	LocalPort       *uint16 `json:"local_port"`
	ClearLocalPort  bool    `json:"clear_local_port"`
	RemoteHost      *string `json:"remote_host"`
	ClearRemoteHost bool    `json:"clear_remote_host"`
	RemotePort      *uint16 `json:"remote_port"`
	ClearRemotePort bool    `json:"clear_remote_port"`
	AutoStart       *bool   `json:"auto_start"`
	Description     *string `json:"description"`
}

func (a *App) ForwardsUpdate(in ForwardUpdateInput) (*store.PortForward, error) {
	return a.db.UpdatePortForward(store.UpdatePortForward{
		ID:              in.ID,
		LocalAddr:       in.LocalAddr,
		ClearLocalAddr:  in.ClearLocalAddr,
		LocalPort:       in.LocalPort,
		ClearLocalPort:  in.ClearLocalPort,
		RemoteHost:      in.RemoteHost,
		ClearRemoteHost: in.ClearRemoteHost,
		RemotePort:      in.RemotePort,
		ClearRemotePort: in.ClearRemotePort,
		AutoStart:       in.AutoStart,
		Description:     in.Description,
	})
}

func (a *App) ForwardsSetBookmarks(forwardID string, bookmarks []store.ProxyBookmark) error {
	return a.db.SetPortForwardBookmarks(forwardID, bookmarks)
}

func (a *App) ForwardsDelete(id string) error {
	// Stop any active instances of this spec first.
	for _, s := range a.forwards.List("") {
		if s.ID == id {
			_ = a.forwards.Stop(id)
		}
	}
	return a.db.DeletePortForward(id)
}

// ----- Port forwards (live state) -----

// ForwardsActive returns running forwards (optionally filtered by session).
func (a *App) ForwardsActive(sessionID string) []sshlayer.ForwardStatus {
	return a.forwards.List(sessionID)
}

// ActiveSessionInfo describes one live SSH session for frontend recovery
// after a UI reload.
type ActiveSessionInfo struct {
	SessionID    string `json:"session_id"`
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
}

// SshActiveSessions returns metadata for every session still in the pool.
// Frontend uses this on mount to re-populate the SessionStore so a Ctrl+R
// (or a vite HMR reload) doesn't lose visibility of running sessions and
// forwards. The backend state was never lost - only the UI view of it.
// shouldCloseToTray reads the `close_to_tray` setting. Bad reads
// (DB closed during shutdown, missing key) fall back to false.
func (a *App) shouldCloseToTray() bool {
	return a.boolSetting("close_to_tray")
}

// shouldMinimiseToTray reads the `minimize_to_tray` setting.
func (a *App) shouldMinimiseToTray() bool {
	return a.boolSetting("minimize_to_tray")
}

func (a *App) boolSetting(key string) bool {
	if a.db == nil {
		return false
	}
	v, _, err := a.db.GetSetting(key)
	if err != nil || v == "" {
		return false
	}
	return v == "1" || v == "true"
}

// OpenNativeTerminal spawns the user's preferred OS terminal with
// nothing attached - fresh shell, no SSH, no command. Useful for
// running a quick local command without leaving the app.
//
// kind on Windows: "powershell" | "cmd" | "windowsterminal".
// On Linux: ignored - uses $TERMINAL, then x-terminal-emulator,
// then a small fallback list (gnome-terminal, konsole, xterm).
// On macOS: ignored - opens Terminal.app via `open -a Terminal`.
func (a *App) OpenNativeTerminal(kind string) error {
	switch runtime.GOOS {
	case "windows":
		switch kind {
		case "", "windowsterminal":
			if err := exec.Command("wt.exe").Start(); err == nil {
				return nil
			}
			fallthrough
		case "powershell":
			// Spawning powershell.exe / cmd.exe directly from a GUI
			// (windows subsystem) process gives them no console host
			// so the shell exits immediately. `cmd /c start ""
			// powershell.exe` detaches it into its own console.
			return exec.Command("cmd.exe", "/c", "start", "", "powershell.exe").Start()
		case "cmd":
			return exec.Command("cmd.exe", "/c", "start", "", "cmd.exe").Start()
		case "wsl":
			// Prefer wt.exe so WSL gets a real terminal emulator; falls
			// back to a bare console window via cmd /c start if wt
			// isn't installed.
			if _, err := exec.LookPath("wt.exe"); err == nil {
				return exec.Command("wt.exe", "wsl.exe").Start()
			}
			return exec.Command("cmd.exe", "/c", "start", "", "wsl.exe").Start()
		}
		return fmt.Errorf("unknown terminal kind %q", kind)
	case "darwin":
		return exec.Command("open", "-a", "Terminal").Start()
	case "linux":
		if t := os.Getenv("TERMINAL"); t != "" {
			if err := exec.Command(t).Start(); err == nil {
				return nil
			}
		}
		candidates := []string{
			"x-terminal-emulator",
			"gnome-terminal",
			"konsole",
			"xfce4-terminal",
			"alacritty",
			"kitty",
			"foot",
			"xterm",
		}
		for _, c := range candidates {
			if _, err := exec.LookPath(c); err == nil {
				return exec.Command(c).Start()
			}
		}
		return fmt.Errorf("no terminal emulator found on PATH; set $TERMINAL")
	}
	return fmt.Errorf("unsupported platform %q", runtime.GOOS)
}

// LaunchExternalTerminal spawns the requested OS terminal with an
// `ssh ...` command pointed at the resolved settings of the given
// connection. Returns once the spawn is dispatched (the terminal
// runs detached from the app).
//
// kind: "powershell" | "cmd" | "windowsterminal" (Windows only for
// now). On a non-Windows host returns an error so the caller can
// show a helpful message.
func (a *App) LaunchExternalTerminal(connectionID, kind string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("external terminal launch is only implemented on Windows for now")
	}
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}
	args := buildSSHArgs(settings)
	switch kind {
	case "", "windowsterminal":
		// `wt.exe new-tab -- ssh ...` opens a new tab in Windows
		// Terminal. If wt isn't installed the spawn fails and we
		// fall through to PowerShell.
		full := append([]string{"new-tab", "--"}, append([]string{"ssh"}, args...)...)
		cmd := exec.Command("wt.exe", full...)
		if err := cmd.Start(); err == nil {
			return nil
		}
		fallthrough
	case "powershell":
		// Avoid `cmd /c start` here - `start` runs the rest of its
		// line through cmd.exe's parser, which would treat `&`, `^`,
		// `"` etc. inside a user-supplied hostname as metacharacters.
		// Spawning powershell.exe directly with a real argv lets
		// Go's syscall.EscapeArg do the escaping; powershell.exe
		// receives each token as a single argument. The lack of
		// `start` means no detached console - for a GUI-subsystem
		// parent that means stdio is inherited; powershell still
		// opens its own window because it's a console subsystem
		// binary launched with no stdin redirected.
		full := append([]string{"-NoExit", "-Command", "ssh"}, args...)
		return exec.Command("powershell.exe", full...).Start()
	case "cmd":
		// Same reasoning: skip `start`. cmd.exe /k ssh ... receives
		// argv through CreateProcess + EscapeArg, no metacharacter
		// expansion on the hostname.
		full := append([]string{"/k", "ssh"}, args...)
		return exec.Command("cmd.exe", full...).Start()
	case "wsl":
		// Run ssh inside WSL - uses the Linux side's OpenSSH client,
		// which respects ~/.ssh/config and known_hosts on the WSL
		// distro. wt.exe wsl.exe -e bash -c "ssh …" gives the WSL
		// shell a TTY; the inner shell stays alive so the user can
		// see banner output even if ssh exits.
		inner := append([]string{"ssh"}, args...)
		sshCmd := shellQuote(inner)
		if _, err := exec.LookPath("wt.exe"); err == nil {
			return exec.Command("wt.exe", "wsl.exe", "-e", "bash", "-lc",
				sshCmd+"; exec bash").Start()
		}
		return exec.Command("cmd.exe", "/c", "start", "", "wsl.exe", "-e", "bash", "-lc",
			sshCmd+"; exec bash").Start()
	}
	return fmt.Errorf("unknown terminal kind %q", kind)
}

// shellQuote single-quotes each arg for bash -c, escaping any inner
// single quotes. Good enough for ssh hostname / port / -J chains
// which won't contain shell metacharacters in our case but defensive
// against user-supplied usernames.
func shellQuote(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return out
}

// buildSSHArgs translates ResolvedSettings into the same flags the
// in-app SSH layer would use, but suitable for an external ssh
// client (assumed to be OpenSSH on the path - Win10+ has it; older
// Win or stripped images may not).
func buildSSHArgs(s *store.ResolvedSettings) []string {
	args := []string{}
	if s.Port != 0 && s.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", s.Port))
	}
	if s.JumpHost != nil && s.JumpHost.Hostname != "" {
		args = append(args, "-J", formatJumpChain(s.JumpHost))
	}
	target := s.Hostname
	if s.Username != nil && *s.Username != "" {
		target = *s.Username + "@" + target
	}
	args = append(args, target)
	return args
}

// formatJumpChain serialises a JumpHostSpec chain to OpenSSH -J
// format: "user@host:port,user@host:port,...". OpenSSH's -J accepts
// a comma-separated list where each entry is a hop in order.
func formatJumpChain(j *store.JumpHostSpec) string {
	parts := []string{}
	for cur := j; cur != nil; cur = cur.Via {
		piece := cur.Hostname
		if cur.Username != nil && *cur.Username != "" {
			piece = *cur.Username + "@" + piece
		}
		if cur.Port != nil && *cur.Port != 0 && *cur.Port != 22 {
			piece = fmt.Sprintf("%s:%d", piece, *cur.Port)
		}
		parts = append(parts, piece)
	}
	return strings.Join(parts, ",")
}

// HideToTray hides the main window without quitting. Used by the
// frontend "Minimize to tray" button.
func (a *App) HideToTray() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Hide()
	a.windowHidden.Store(true)
}

// SetWindowTitle updates the OS window (and taskbar) title. The
// frontend calls this to reflect the active connection/section; Wails
// v3 alpha doesn't propagate document.title to the native title on its
// own. No-op when the window isn't wired yet (early startup).
func (a *App) SetWindowTitle(title string) {
	if a.mainWindow == nil || title == "" {
		return
	}
	a.mainWindow.SetTitle(title)
}

// ShowFromTray restores the main window.
func (a *App) ShowFromTray() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Show()
	a.mainWindow.Focus()
	a.windowHidden.Store(false)
}

// ConfirmQuit is called by the frontend after the user accepts the
// "active sessions will be disconnected" prompt. Sets the flag so the
// next WindowClosing event proceeds, then asks the app to quit.
func (a *App) ConfirmQuit() {
	a.quitConfirmed.Store(true)
	if a.app != nil {
		a.app.Quit()
	}
}

// SshActiveSessionCount returns how many live SSH sessions are in the
// pool. Used by the quit-confirm path before deciding whether to prompt.
func (a *App) SshActiveSessionCount() int {
	return len(a.pool.IDs())
}

func (a *App) SshActiveSessions() []ActiveSessionInfo {
	out := []ActiveSessionInfo{}
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	for _, id := range a.pool.IDs() {
		sess, ok := a.pool.Get(id)
		if !ok {
			continue
		}
		info := ActiveSessionInfo{SessionID: sess.ID}
		if meta, ok := a.sessionMeta[id]; ok {
			info.ConnectionID = meta.connectionID
			info.Name = meta.name
			info.Hostname = meta.hostname
		}
		out = append(out, info)
	}
	return out
}

// ForwardsStart instantiates a persisted forward against an active session.
func (a *App) ForwardsStart(forwardID, sessionID string) (*sshlayer.ForwardStatus, error) {
	spec, err := a.db.GetPortForward(forwardID)
	if err != nil {
		return nil, err
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not connected")
	}
	return startForward(a.forwards, sess, spec)
}

// ForwardsStop tears down a running forward by spec id.
func (a *App) ForwardsStop(forwardID string) error {
	return a.forwards.Stop(forwardID)
}

// ForwardsAutoStartFor instantiates every spec with auto_start=1 that's
// registered against the given connection. Called by SshConnect right after
// the session lands.
func (a *App) forwardsAutoStartFor(connectionID, sessionID string) {
	specs, err := a.db.ListPortForwards(connectionID)
	if err != nil {
		log.Printf("auto-start forwards: list: %v", err)
		return
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return
	}
	for _, spec := range specs {
		if !spec.AutoStart {
			continue
		}
		if _, err := startForward(a.forwards, sess, &spec); err != nil {
			log.Printf("auto-start forward %s: %v", spec.ID, err)
		}
	}
}

func startForward(pool *sshlayer.ForwardPool, sess *sshlayer.Session, spec *store.PortForward) (*sshlayer.ForwardStatus, error) {
	switch spec.Kind {
	case "local":
		if spec.RemoteHost == nil || spec.RemotePort == nil {
			return nil, fmt.Errorf("local forward needs remote_host + remote_port")
		}
		var la string
		if spec.LocalAddr != nil {
			la = *spec.LocalAddr
		}
		var lp uint16
		if spec.LocalPort != nil {
			lp = *spec.LocalPort
		}
		return pool.StartLocal(sess, spec.ID, la, lp, *spec.RemoteHost, *spec.RemotePort)
	case "dynamic":
		var la string
		if spec.LocalAddr != nil {
			la = *spec.LocalAddr
		}
		var lp uint16
		if spec.LocalPort != nil {
			lp = *spec.LocalPort
		}
		return pool.StartDynamic(sess, spec.ID, la, lp)
	case "remote":
		if spec.RemoteHost == nil || spec.RemotePort == nil {
			return nil, fmt.Errorf("remote forward needs local_host (in remote_host field) + local_port (in remote_port field)")
		}
		var la string
		if spec.LocalAddr != nil {
			la = *spec.LocalAddr
		}
		var lp uint16
		if spec.LocalPort != nil {
			lp = *spec.LocalPort
		}
		return pool.StartRemote(sess, spec.ID, la, lp, *spec.RemoteHost, *spec.RemotePort)
	default:
		return nil, fmt.Errorf("unknown kind: %s", spec.Kind)
	}
}

// ----- Isolated browser -----

type BrowserLaunchResult struct {
	PID int `json:"pid"`
}

// SshLaunchBrowser opens a browser pointed at the given SOCKS5 forward.
// Respects the user's `preferred_browser_path` setting if present;
// otherwise platform default detection (see internal/ssh/browser.go).
func (a *App) SshLaunchBrowser(forwardID, url string) (*BrowserLaunchResult, error) {
	for _, s := range a.forwards.List("") {
		if s.ID != forwardID {
			continue
		}
		if s.Kind != sshlayer.ForwardDynamic {
			return nil, fmt.Errorf("forward %s is not a dynamic (SOCKS) forward", forwardID)
		}
		preferred, _, _ := a.db.GetSetting("preferred_browser_path")
		pid, err := sshlayer.LaunchIsolatedBrowser(s.LocalAddr, s.LocalPort, url, sshlayer.LaunchOptions{
			PreferredPath: preferred,
		})
		if err != nil {
			return nil, err
		}
		return &BrowserLaunchResult{PID: pid}, nil
	}
	return nil, fmt.Errorf("forward %s is not active", forwardID)
}

// ----- Settings -----

// SettingsGet returns the value of an app-wide setting, or "" if unset.
func (a *App) SettingsGet(key string) string {
	if localStateKeys[key] {
		v, _ := a.localSettingGet(key)
		return v
	}
	v, _, _ := a.db.GetSetting(key)
	return v
}

func (a *App) SettingsSet(key, value string) error {
	if localStateKeys[key] {
		return a.localSettingSet(key, value)
	}
	return a.db.SetSetting(key, value)
}

func (a *App) SettingsDelete(key string) error {
	if localStateKeys[key] {
		return a.localSettingDelete(key)
	}
	return a.db.DeleteSetting(key)
}

// ----- Images -----

// ImagesGet returns the bytes + mime type for a stored image. The frontend
// turns this into a data URI for inline display.
type ImagePayload struct {
	MIME string `json:"mime"`
	B64  string `json:"b64"`
}

func (a *App) ImagesGet(id string) (*ImagePayload, error) {
	mime, data, ok, err := a.db.GetImage(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("image %s not found", id)
	}
	return &ImagePayload{
		MIME: mime,
		B64:  base64.StdEncoding.EncodeToString(data),
	}, nil
}

// ImagesList returns metadata for every image already in the DB so
// the IconPicker can offer "choose from existing" alongside upload.
// Bytes are NOT included - the frontend hydrates each thumbnail
// lazily via ImagesGet through the existing imageCache.
func (a *App) ImagesList() ([]store.ImageSummary, error) {
	return a.db.ListImageIDs()
}

// ImagesUpload stores a base64-encoded image and returns the image id.
// MIME is required so the frontend can render it back as a data URI
// later. Dedup happens inside store.PutImage (MD5 content addressing).
func (a *App) ImagesUpload(b64Data, mime string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	return a.db.PutImage(data, mime)
}

// ImagesSetFolder assigns an image to a folder. Pass empty imageID to
// clear the icon (folder reverts to the default emoji).
func (a *App) ImagesSetFolder(folderID, imageID string) error {
	return a.db.SetFolderIcon(folderID, imageID)
}

// ImagesSetConnection assigns an image to a connection. Empty clears.
func (a *App) ImagesSetConnection(connID, imageID string) error {
	return a.db.SetConnectionIcon(connID, imageID)
}

// ImagesSetCredential assigns an image to a credential. Empty clears.
func (a *App) ImagesSetCredential(credID, imageID string) error {
	return a.db.SetCredentialIcon(credID, imageID)
}

// ----- RDM import -----

// RdmImport parses a Devolutions RDM JSON export and merges it into the
// store. Frontend uploads the raw JSON string; we return a summary so the
// UI can show what landed and what needs follow-up.
// RdmImport parses a Devolutions RDM JSON export and merges it into the store.
// rootFolderID optionally places all imported top-level folders under an
// existing connection-tree folder; empty string means the DB root.
func (a *App) RdmImport(jsonText string, rootFolderID string) (*rdm.Summary, error) {
	if jsonText == "" {
		return nil, fmt.Errorf("empty import payload")
	}
	file, err := rdm.Parse([]byte(jsonText))
	if err != nil {
		return nil, err
	}
	im := rdm.NewImporter(a.db, file, rootFolderID)
	summary, err := im.Import()
	if err != nil {
		return nil, err
	}
	log.Printf("rdm import: %d folders, %d connections, %d credentials (%d need secret), %d images, %d jumps resolved, %d unresolved, %d warnings",
		summary.FoldersCreated, summary.ConnectionsCreated,
		summary.CredentialsCreated, summary.CredentialsNeedSecret,
		summary.ImagesStored, summary.JumpResolved, summary.JumpUnresolved,
		len(summary.Warnings))
	return &summary, nil
}

// ----- ssh_config import -----

// SshConfigImport parses an OpenSSH client config and writes one
// connection per non-wildcard Host block. rootFolderID groups the
// imports under an existing folder (empty = DB root).
func (a *App) SshConfigImport(text string, rootFolderID string) (*sshconfig.Summary, error) {
	if text == "" {
		return nil, fmt.Errorf("empty config text")
	}
	entries, err := sshconfig.Parse(text)
	if err != nil {
		return nil, err
	}
	return sshconfig.Apply(a.db, entries, rootFolderID)
}

// MobaXtermImport parses a MobaXterm .mxtsessions export and creates
// one connection per SSH session, rebuilding the bookmark folder tree
// under rootFolderID ("" = DB root). Passwords aren't in the export;
// non-SSH session types (RDP, telnet, ...) are counted and skipped.
func (a *App) MobaXtermImport(text string, rootFolderID string) (*mobaxterm.Summary, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty import payload")
	}
	entries, sum, err := mobaxterm.Parse(text)
	if err != nil {
		return nil, err
	}
	return mobaxterm.Apply(a.db, entries, sum, rootFolderID)
}

// PuttyRegImport parses a PuTTY registry export (.reg, also KiTTY)
// and creates one connection per Protocol=ssh session, flat under
// rootFolderID. PuTTY stores no passwords, so nothing is lost.
func (a *App) PuttyRegImport(text string, rootFolderID string) (*puttyreg.Summary, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty import payload")
	}
	entries, sum, err := puttyreg.Parse(text)
	if err != nil {
		return nil, err
	}
	return puttyreg.Apply(a.db, entries, sum, rootFolderID)
}

// PathIsDir reports whether path is a directory. Used by the SFTP
// drag-and-drop upload flow - when the OS drops file paths into a
// pane, we need to know whether to call SftpStartUpload (file) or
// SftpStartUploadDir (directory) per entry.
func (a *App) PathIsDir(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// ----- Export / Import (TOML / JSON) -----

// ExportSubtreeRequest selects what to export and how. Roots is a list
// of folder ids whose entire subtree (and contained connections) should
// be included. Extra is a flat list of connection ids selected outside
// of any included folder. Empty Roots + empty Extra = export everything.
type ExportSubtreeRequest struct {
	Roots                   []string `json:"roots"`
	Extra                   []string `json:"extra"`
	Format                  string   `json:"format"` // "toml" | "json"
	IncludeCredentials      bool     `json:"include_credentials"`
	Passphrase              string   `json:"passphrase"`
	StripNotes              bool     `json:"strip_notes"`
	StripTags               bool     `json:"strip_tags"`
	StripColor              bool     `json:"strip_color"`
	StripIcon               bool     `json:"strip_icon"`
	ConvertAuthRefToInherit bool     `json:"convert_auth_ref_to_inherit"`
}

// ExportSubtreeResult carries the serialised archive ready to drop on
// disk or paste into a sync tool.
type ExportSubtreeResult struct {
	Format string `json:"format"`
	Body   string `json:"body"`
	Bytes  int    `json:"bytes"`
}

func (a *App) ExportSubtree(req ExportSubtreeRequest) (*ExportSubtreeResult, error) {
	format := exporter.Format(req.Format)
	if format == "" {
		format = exporter.FormatTOML
	}
	arc, err := exporter.Build(a.db, req.Roots, req.Extra, exporter.Options{
		IncludeCredentials:      req.IncludeCredentials,
		Passphrase:              req.Passphrase,
		StripNotes:              req.StripNotes,
		StripTags:               req.StripTags,
		StripColor:              req.StripColor,
		StripIcon:               req.StripIcon,
		ConvertAuthRefToInherit: req.ConvertAuthRefToInherit,
	}, func(credID, vaultKey string) ([]byte, bool, error) {
		s, ok, err := a.vault.Get(vaultKey)
		if err != nil || !ok {
			return nil, ok, err
		}
		return []byte(s), true, nil
	})
	if err != nil {
		return nil, err
	}
	body, err := exporter.Encode(arc, format)
	if err != nil {
		return nil, err
	}
	return &ExportSubtreeResult{
		Format: string(format),
		Body:   body,
		Bytes:  len(body),
	}, nil
}

// RegisterURLScheme writes the OS-level registration that binds
// `ssh-tool://` URIs to this executable. Per-user / no admin.
// Idempotent. See url_scheme_*.go for platform specifics.
func (a *App) RegisterURLScheme() error {
	return registerURLScheme()
}

// URLSchemeStatus returns a short status string describing the
// currently-registered handler ("" = not registered, otherwise an
// OS-specific identifier like the launch command or
// `ssh-tool-url.desktop`).
func (a *App) URLSchemeStatus() string {
	return urlSchemeStatus()
}

// ExplorerMenuRegister adds "Open in ssh-tool" to the OS file
// manager's right-click menu for directories (Explorer on Windows,
// Dolphin/Nautilus on Linux). Per-user / no admin. Idempotent.
func (a *App) ExplorerMenuRegister() error {
	return registerExplorerMenu()
}

// ExplorerMenuUnregister removes the file-manager integration.
func (a *App) ExplorerMenuUnregister() error {
	return unregisterExplorerMenu()
}

// ExplorerMenuStatus returns a short description of the installed
// integration ("" = not installed).
func (a *App) ExplorerMenuStatus() string {
	return explorerMenuStatus()
}

// ssrfDialControl is a net.Dialer.Control hook that blocks dials to
// the cloud-metadata service IPs (AWS/GCP/Azure 169.254.169.254 and
// the IPv6 fd00:ec2::254). It runs after name resolution against the
// connected peer IP, so DNS rebinding can't slip a metadata IP past
// a resolution that initially returned something else.
//
// Earlier versions of this guard also rejected loopback and RFC1918,
// but in a single-user desktop app the SSRF threat model is narrow:
// the attacker would need to talk the user into pasting a URL or
// clicking an ssh-tool:// link, and most "internal" targets (LAN
// catalog at 192.168.x.y, localhost dev server) are legitimate use
// cases that the user is actively asking for. Cloud metadata is the
// one target the user would never knowingly fetch and that an
// attacker genuinely can't reach without the app's help - keep that
// blocked, let the rest through.
func ssrfDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ssrf: bad address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ssrf: non-IP host %q", host)
	}
	if ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return fmt.Errorf("ssrf: refusing to connect to cloud metadata IP %s", ip)
	}
	return nil
}

// FetchArchiveURL retrieves an archive payload from a remote URL
// (typically the ssh-tool-catalog /api/bundle endpoint). Returns
// the raw text so the frontend can hand it to ImportArchive with
// the user-picked conflict mode + dry-run flag.
//
// Done server-side so the request inherits ssh-tool's network +
// any future proxy/cert config, and so the user doesn't bump into
// CORS in the WebView for cross-origin reads.
func (a *App) FetchArchiveURL(rawURL string) (string, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http(s) URLs allowed, got %q", u.Scheme)
	}
	// SSRF guard: reject loopback, link-local, RFC1918, ULA, and the
	// cloud-metadata IPs. The check runs on the actually-connected
	// peer (Dialer.Control) so DNS rebinding can't slip a public name
	// past resolution and then hand us a private IP at connect time.
	// See the security audit's H1 IPC finding.
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				Control: ssrfDialControl,
			}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch %s: %s", rawURL, resp.Status)
	}
	// Cap at 10 MiB - archive payloads are tens to hundreds of KB
	// in practice; anything bigger is suspect.
	const maxBytes = 10 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return string(body), nil
}

// ImportArchiveRequest carries the raw text + dry-run / conflict mode.
type ImportArchiveRequest struct {
	Text           string `json:"text"`
	Conflict       string `json:"conflict"` // "skip" | "rename" | "overwrite"
	DryRun         bool   `json:"dry_run"`
	Passphrase     string `json:"passphrase"` // required if encrypted_secrets present
	TargetFolderID string `json:"target_folder_id,omitempty"`
}

func (a *App) ImportArchive(req ImportArchiveRequest) (*exporter.ImportSummary, error) {
	if req.Text == "" {
		return nil, fmt.Errorf("empty archive")
	}
	arc, err := exporter.Decode(req.Text)
	if err != nil {
		return nil, err
	}
	return exporter.Apply(a.db, arc, exporter.ImportOptions{
		Conflict:         exporter.ConflictMode(req.Conflict),
		DryRun:           req.DryRun,
		SecretPassphrase: req.Passphrase,
		TargetFolderID:   req.TargetFolderID,
	}, func(credID string, plain []byte) (string, error) {
		// Imported secrets get a stable vault key under imp:<credID>.
		// Mirrors how the rest of the app namespaces vault entries
		// (e.g. conn_pass:<connID> for per-connection passwords).
		key := "imp:" + credID
		if err := a.vault.Put(key, string(plain)); err != nil {
			return "", err
		}
		return key, nil
	})
}

// ----- SFTP -----

// SftpListResult mirrors what we hand to the frontend on SftpList. The
// resolved path is returned because the caller may pass "" to mean "home
// directory" and the UI wants to render the actual path that was listed.
type SftpListResult struct {
	Path    string               `json:"path"`
	Entries []sshlayer.SftpEntry `json:"entries"`
}

func (a *App) SftpList(sessionID, remotePath string) (*SftpListResult, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	resolved, entries, err := sess.SftpList(remotePath)
	if err != nil {
		return nil, err
	}
	return &SftpListResult{Path: resolved, Entries: entries}, nil
}

func (a *App) SftpStat(sessionID, remotePath string) (*sshlayer.SftpEntry, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return sess.SftpStat(remotePath)
}

func (a *App) SftpMkdir(sessionID, remotePath string) error {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return sess.SftpMkdir(remotePath)
}

func (a *App) SftpRemove(sessionID, remotePath string) error {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return sess.SftpRemove(remotePath)
}

func (a *App) SftpRename(sessionID, oldPath, newPath string) error {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return sess.SftpRename(oldPath, newPath)
}

// SftpReadPreview returns up to maxBytes of a remote file as a base64
// string. Used for inline preview of small text files in the SFTP
// browser. Frontend should clamp maxBytes to something sensible (e.g.
// 256KB) to avoid pulling a multi-GB file by accident.
type SftpPreview struct {
	B64       string `json:"b64"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
}

func (a *App) SftpReadPreview(sessionID, remotePath string, maxBytes int64) (*SftpPreview, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	stat, err := sess.SftpStat(remotePath)
	if err != nil {
		return nil, err
	}
	data, err := sess.SftpReadAll(remotePath, maxBytes+1)
	if err != nil {
		return nil, err
	}
	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	return &SftpPreview{
		B64:       base64.StdEncoding.EncodeToString(data),
		Truncated: truncated,
		Size:      stat.Size,
	}, nil
}

// LoadTextFileResult carries the picked archive path + its UTF-8
// contents. Empty path = user cancelled the picker.
type LoadTextFileResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// LoadTextFile pops a native Open dialog and returns the chosen
// file's contents. Used by the import-archive flow so the user
// doesn't have to copy-paste an archive into the textarea. Refuses
// files larger than 32 MiB to keep a stray binary pick from
// freezing the renderer.
func (a *App) LoadTextFile(title string) (*LoadTextFileResult, error) {
	if title == "" {
		title = "Choose a file"
	}
	path, err := OpenFileDialog(OpenFileDialogOptions{Title: title})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &LoadTextFileResult{}, nil
	}
	const maxBytes = 32 << 20
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file too large (%d bytes; limit %d)", info.Size(), maxBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return &LoadTextFileResult{Path: path, Content: string(b)}, nil
}

// SaveTextFile pops a native Save dialog and writes content there.
// Used by the connection-export flow so the user can drop a TOML
// archive on disk without leaving the app. Empty path returned =
// user cancelled. Returns the actual path written on success.
func (a *App) SaveTextFile(suggestedName, content string) (string, error) {
	path, err := SaveFileDialog(SaveFileDialogOptions{
		DefaultFilename: suggestedName,
		Title:           "Save as…",
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return path, nil
}

// SftpPickDownloadDest opens a native Save File dialog and returns the
// chosen path. The frontend calls this before SftpDownload so it doesn't
// have to know the local filesystem layout. Empty result = user
// cancelled.
func (a *App) SftpPickDownloadDest(suggestedName string) (string, error) {
	return SaveFileDialog(SaveFileDialogOptions{
		DefaultFilename: suggestedName,
		Title:           "Save remote file as…",
	})
}

// SftpPickUploadDirSource opens a native directory picker. Returns
// the chosen local directory path (empty on cancel). Used by the
// recursive upload flow.
func (a *App) SftpPickUploadDirSource() (string, error) {
	return OpenDirectoryDialog(OpenFileDialogOptions{
		Title: "Choose a folder to upload",
	})
}

// SftpPickDownloadDirDest opens a native directory picker for the
// "where should the downloaded folder land" question.
func (a *App) SftpPickDownloadDirDest() (string, error) {
	return OpenDirectoryDialog(OpenFileDialogOptions{
		Title: "Choose a destination folder",
	})
}

// SftpPickUploadSource opens a native Open File dialog and returns the
// chosen local path. Empty result = user cancelled.
func (a *App) SftpPickUploadSource() (string, error) {
	return OpenFileDialog(OpenFileDialogOptions{
		Title: "Choose a file to upload",
	})
}

// PickAnsibleInventoryFile opens a native Open File dialog so the
// dynamic-folder editor doesn't make the user copy the inventory
// path manually. No extension filter - the v3 dialog shim only
// carries Title for now; the parser dispatches on extension at
// read time anyway, so picking the wrong file just fails loudly.
func (a *App) PickAnsibleInventoryFile() (string, error) {
	return OpenFileDialog(OpenFileDialogOptions{
		Title: "Choose an Ansible inventory file",
	})
}

// SftpStartDownload begins streaming a remote file to localPath, emitting
// progress as "sftp_progress:<transferId>" events. Returns the
// transferId so the frontend can correlate the events and call
// SftpCancelTransfer if needed.
//
// Returns once the transfer goroutine is spawned. The final
// success/failure is signalled via the same progress channel with
// Done=true (and Err set on failure).
func (a *App) SftpStartDownload(sessionID, remotePath, localPath string) (string, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	transferID := uuid.NewString()
	cancel := make(chan struct{})
	a.transfersMu.Lock()
	a.transfers[transferID] = cancel
	a.transfersMu.Unlock()

	go func() {
		eventName := "sftp_progress:" + transferID
		onProgress := func(written, total int64) {
			EventsEmit(eventName, sshlayer.TransferProgress{
				TransferID: transferID, Bytes: written, Total: total,
			})
		}
		_, err := sess.SftpDownload(remotePath, localPath, onProgress, cancel)
		a.releaseTransfer(transferID)
		p := sshlayer.TransferProgress{TransferID: transferID, Done: true}
		if err != nil {
			p.Err = err.Error()
		} else {
			// Final 100% emit so the bar lands neat.
			if st, e := sess.SftpStat(remotePath); e == nil {
				p.Bytes = st.Size
				p.Total = st.Size
			}
		}
		EventsEmit(eventName, p)
	}()
	return transferID, nil
}

func (a *App) SftpStartUpload(sessionID, localPath, remotePath string) (string, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	transferID := uuid.NewString()
	cancel := make(chan struct{})
	a.transfersMu.Lock()
	a.transfers[transferID] = cancel
	a.transfersMu.Unlock()

	go func() {
		eventName := "sftp_progress:" + transferID
		onProgress := func(written, total int64) {
			EventsEmit(eventName, sshlayer.TransferProgress{
				TransferID: transferID, Bytes: written, Total: total,
			})
		}
		_, err := sess.SftpUpload(localPath, remotePath, onProgress, cancel)
		a.releaseTransfer(transferID)
		p := sshlayer.TransferProgress{TransferID: transferID, Done: true}
		if err != nil {
			p.Err = err.Error()
		} else if st, e := sess.SftpStat(remotePath); e == nil {
			p.Bytes = st.Size
			p.Total = st.Size
		}
		EventsEmit(eventName, p)
	}()
	return transferID, nil
}

// SftpStartDownloadDir mirrors a remote directory tree locally. The
// frontend uses the same sftp_progress event stream as single-file
// downloads; the progress payload carries DirProgress's files_done /
// files_total / bytes_done / bytes_total plus the current relative
// path so the UI can show "Downloading subdir/file.log".
func (a *App) SftpStartDownloadDir(sessionID, remoteRoot, localRoot string) (string, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	transferID := uuid.NewString()
	cancel := make(chan struct{})
	a.transfersMu.Lock()
	a.transfers[transferID] = cancel
	a.transfersMu.Unlock()

	go func() {
		eventName := "sftp_progress:" + transferID
		onProgress := func(p sshlayer.DirProgress) {
			EventsEmit(eventName, sshlayer.TransferProgress{
				TransferID:  transferID,
				Bytes:       p.BytesDone,
				Total:       p.BytesTotal,
				FilesDone:   p.FilesDone,
				FilesTotal:  p.FilesTotal,
				CurrentPath: p.CurrentPath,
			})
		}
		err := sess.SftpDownloadDir(remoteRoot, localRoot, onProgress, cancel)
		a.releaseTransfer(transferID)
		p := sshlayer.TransferProgress{TransferID: transferID, Done: true}
		if err != nil {
			p.Err = err.Error()
		}
		EventsEmit(eventName, p)
	}()
	return transferID, nil
}

// SftpStartUploadDir is the upload mirror of SftpStartDownloadDir.
func (a *App) SftpStartUploadDir(sessionID, localRoot, remoteRoot string) (string, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	transferID := uuid.NewString()
	cancel := make(chan struct{})
	a.transfersMu.Lock()
	a.transfers[transferID] = cancel
	a.transfersMu.Unlock()

	go func() {
		eventName := "sftp_progress:" + transferID
		onProgress := func(p sshlayer.DirProgress) {
			EventsEmit(eventName, sshlayer.TransferProgress{
				TransferID:  transferID,
				Bytes:       p.BytesDone,
				Total:       p.BytesTotal,
				FilesDone:   p.FilesDone,
				FilesTotal:  p.FilesTotal,
				CurrentPath: p.CurrentPath,
			})
		}
		err := sess.SftpUploadDir(localRoot, remoteRoot, onProgress, cancel)
		a.releaseTransfer(transferID)
		p := sshlayer.TransferProgress{TransferID: transferID, Done: true}
		if err != nil {
			p.Err = err.Error()
		}
		EventsEmit(eventName, p)
	}()
	return transferID, nil
}

func (a *App) SftpCancelTransfer(transferID string) {
	a.transfersMu.Lock()
	ch, ok := a.transfers[transferID]
	if ok {
		delete(a.transfers, transferID)
	}
	a.transfersMu.Unlock()
	if ok {
		close(ch)
	}
}

func (a *App) releaseTransfer(transferID string) {
	a.transfersMu.Lock()
	delete(a.transfers, transferID)
	a.transfersMu.Unlock()
}

// ----- Auto-reconnect -----

// ReconnectAttempt is the payload of session_reconnect_attempt.
type ReconnectAttempt struct {
	Attempt      int   `json:"attempt"`
	DelaySeconds int64 `json:"delay_seconds"`
	MaxAttempts  int   `json:"max_attempts"`
}

// ReconnectSuccess is the payload of session_reconnect_success.
type ReconnectSuccess struct {
	NewSessionID string `json:"new_session_id"`
	// NetworkVia mirrors SshConnectResult.NetworkVia for the fresh
	// session so the pane's VPN badge survives an auto-reconnect.
	NetworkVia string `json:"network_via,omitempty"`
}

// ReconnectFailed is the payload of session_reconnect_failed.
type ReconnectFailed struct {
	Reason string `json:"reason"`
}

// spawnReconnect runs an exponential-backoff retry loop on a background
// goroutine. Emits session_reconnect_attempt/success/failed events
// keyed on the OLD session id so the frontend can route them to the
// right pane and then swap in the new session id when we succeed.
func (a *App) spawnReconnect(oldSessionID, connectionID string) {
	cancel := make(chan struct{})
	a.reconnectMu.Lock()
	a.reconnects[oldSessionID] = cancel
	a.reconnectMu.Unlock()

	go a.runReconnect(oldSessionID, connectionID, cancel)
}

func (a *App) runReconnect(oldID, connID string, cancel <-chan struct{}) {
	const maxAttempts = 5
	defer func() {
		a.reconnectMu.Lock()
		delete(a.reconnects, oldID)
		a.reconnectMu.Unlock()
	}()

	delaysSec := []int64{1, 2, 4, 8, 16}
	attemptEvent := "session_reconnect_attempt:" + oldID
	successEvent := "session_reconnect_success:" + oldID
	failedEvent := "session_reconnect_failed:" + oldID

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		delay := delaysSec[attempt-1]
		EventsEmit(attemptEvent, ReconnectAttempt{
			Attempt: attempt, DelaySeconds: delay, MaxAttempts: maxAttempts,
		})
		select {
		case <-cancel:
			EventsEmit(failedEvent, ReconnectFailed{Reason: "cancelled"})
			return
		case <-time.After(time.Duration(delay) * time.Second):
		}

		// Try connecting. SshConnect will register a new session in the
		// pool with a fresh id; the frontend swaps the id on success.
		res, err := a.SshConnect(connID)
		if err == nil {
			EventsEmit(successEvent, ReconnectSuccess{NewSessionID: res.SessionID, NetworkVia: res.NetworkVia})
			return
		}
		log.Printf("reconnect attempt %d for conn %s failed: %v", attempt, connID, err)
	}
	EventsEmit(failedEvent, ReconnectFailed{Reason: "max attempts exceeded"})
}

// SshCancelReconnect aborts a pending retry loop. No-op if there isn't
// one running for this session id.
func (a *App) SshCancelReconnect(oldSessionID string) {
	a.reconnectMu.Lock()
	ch, ok := a.reconnects[oldSessionID]
	if ok {
		delete(a.reconnects, oldSessionID)
	}
	a.reconnectMu.Unlock()
	if ok {
		close(ch)
	}
}

// ----- Misc IPC -----

// sshSystemArgv returns the equivalent OpenSSH invocation for a
// connection as a real argv slice ({"ssh", "-p", "2222", "user@host"}).
// This is the form used for exec - hostname/username never go through
// a shell parser. Callers that just need a display string for copy
// / paste use SshSystemCommand which joins the same argv with spaces.
//
// We don't include -i KEYFILE because credentials live in our vault,
// not as filesystem keys; opkssh certs likewise.
func (a *App) sshSystemArgv(connectionID string) ([]string, error) {
	s, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, err
	}
	if s.Hostname == "" {
		return nil, fmt.Errorf("connection has no hostname")
	}

	argv := []string{"ssh"}

	if s.Port != 0 && s.Port != 22 {
		argv = append(argv, "-p", strconv.Itoa(int(s.Port)))
	}

	if s.JumpHost != nil {
		var hops []string
		cur := s.JumpHost
		for cur != nil {
			hop := cur.Hostname
			if cur.Username != nil && *cur.Username != "" {
				hop = *cur.Username + "@" + hop
			}
			if cur.Port != nil && *cur.Port != 0 && *cur.Port != 22 {
				hop = hop + ":" + strconv.Itoa(int(*cur.Port))
			}
			hops = append(hops, hop)
			cur = cur.Via
		}
		if len(hops) > 0 {
			argv = append(argv, "-J", strings.Join(hops, ","))
		}
	}

	target := s.Hostname
	if s.Username != nil && *s.Username != "" {
		target = *s.Username + "@" + target
	}
	argv = append(argv, target)
	return argv, nil
}

// SshSystemCommand returns the same invocation as sshSystemArgv but
// as a single space-joined string for display / copy purposes. Do
// NOT pass the returned value to a shell or to exec - hostname and
// username are unquoted here. Use sshSystemArgv for any exec path.
func (a *App) SshSystemCommand(connectionID string) (string, error) {
	argv, err := a.sshSystemArgv(connectionID)
	if err != nil {
		return "", err
	}
	return strings.Join(argv, " "), nil
}

// OpenURL routes a URL to the system browser via Wails runtime. Used by
// the xterm web-links addon when the user clicks a URL in the terminal.
func (a *App) OpenURL(url string) {
	BrowserOpenURL(url)
}

// SshLaunchInSystemTerminal opens the OS terminal with the equivalent
// `ssh ...` command preloaded so the user can run it with their own
// agent / key material (useful for opkssh-cli-based scripts, or when
// the user wants to keep a shell open after this app exits).
//
// Best-effort by platform:
//
//	Windows: prefer Windows Terminal (`wt new-tab -- cmd /k ...`);
//	         fall back to plain cmd if wt isn't installed.
//	macOS:   `osascript` -> `Terminal` -> do script.
//	Linux:   tries xterm / gnome-terminal / konsole in order. If
//	         none is found, returns an error and the UI falls back
//	         to the existing "copy to clipboard" path.
func (a *App) SshLaunchInSystemTerminal(connectionID string) error {
	argv, err := a.sshSystemArgv(connectionID)
	if err != nil {
		return err
	}
	return launchInSystemTerminal(argv)
}

// LogDir returns the path to the rotating log file directory so the
// Settings page can show + open it.
func (a *App) LogDir() string {
	return filepath.Join(store.DataDir(), "logs")
}

// AppVersionInfo bundles the build-time injected version metadata so
// the Settings → About panel can display it. Version defaults to
// "dev" for go-run / un-tagged builds; commit defaults to "unknown".
type AppVersionInfo struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	SchemaVersion int64  `json:"schema_version"`
}

// AppVersion returns the build-time injected name / version / commit
// plus the runtime-resolved schema version (from store/schema_meta).
func (a *App) AppVersion() AppVersionInfo {
	info := AppVersionInfo{
		Name:    appName,
		Version: appVersion,
		Commit:  appCommit,
	}
	if a.db != nil {
		if v, err := a.db.SchemaVersion(); err == nil {
			info.SchemaVersion = v
		}
	}
	if info.SchemaVersion == 0 {
		// Fall back to the latest known when the DB hasn't been opened
		// yet (e.g. very early UI poll during startup). Better than 0.
		info.SchemaVersion = store.LatestSchemaVersion()
	}
	return info
}

// ProfileStats bundles profile-wide counts for the Settings -> About
// "Profile statistics" block. Everything comes from the store in one
// pass; VNC is the resolved (inheritance-applied) value, not just the
// per-connection override, so a folder-level "VNC on" counts all its
// children.
type ProfileStats struct {
	Connections    int `json:"connections"`
	VncEnabled     int `json:"vnc_enabled"`
	Folders        int `json:"folders"`
	DynamicFolders int `json:"dynamic_folders"`
	Forwards       int `json:"forwards"`
	Bookmarks      int `json:"bookmarks"`
	Credentials    int `json:"credentials"`
	DynamicHosts   int `json:"dynamic_hosts"`
	DynamicVMs     int `json:"dynamic_vms"`
	DynamicLXC     int `json:"dynamic_lxc"`
	DynamicServers int `json:"dynamic_servers"`
}

// ProfileStats counts connections (total + resolved VNC-enabled),
// folders (total + dynamic), configured port forwards + their proxy
// bookmarks, credentials, and cached dynamic-inventory entries
// bucketed by kind (hosts / VMs / LXC / cloud servers).
func (a *App) ProfileStats() (*ProfileStats, error) {
	out := &ProfileStats{}
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	out.Connections = len(conns)
	out.Folders = len(folders)
	for _, c := range conns {
		if resolver.ResolveWith(c, folders).VncEnabled {
			out.VncEnabled++
		}
	}
	fwds, err := a.db.ListAllPortForwards()
	if err != nil {
		return nil, err
	}
	out.Forwards = len(fwds)
	for _, f := range fwds {
		out.Bookmarks += len(f.Bookmarks)
	}
	creds, err := a.db.ListCredentials()
	if err != nil {
		return nil, err
	}
	out.Credentials = len(creds)
	dyn, err := a.db.ListDynamicFolders()
	if err != nil {
		return nil, err
	}
	out.DynamicFolders = len(dyn)
	for _, df := range dyn {
		entries, err := a.db.ListDynamicEntries(df.FolderID)
		if err != nil {
			continue // one broken folder shouldn't sink the whole panel
		}
		for _, e := range entries {
			switch inventory.EntryKind(e.Kind) {
			case inventory.KindHost:
				out.DynamicHosts++
			case inventory.KindGuestVM:
				out.DynamicVMs++
			case inventory.KindGuestLXC:
				out.DynamicLXC++
			default:
				// Cloud providers (Hetzner, DO, ...) and Ansible all
				// report flat "server" entries.
				out.DynamicServers++
			}
		}
	}
	return out, nil
}

// UpdateCheckResult is what CheckForUpdate hands back to the frontend.
// IsNewer signals "a strictly higher version than the running build is
// available". Errors that aren't user-actionable (no network, server
// down) are surfaced as a non-nil Error string and IsNewer=false so the
// UI can stay quiet by default but still show a tooltip on click.
type UpdateCheckResult struct {
	Current      string `json:"current"`
	Latest       string `json:"latest"`
	IsNewer      bool   `json:"is_newer"`
	ChangelogURL string `json:"changelog_url"`
	DownloadURL  string `json:"download_url"`
	DownloadSize int64  `json:"download_size,omitempty"` // bytes, 0 when the manifest omits it
	ReleasedAt   string `json:"released_at"`
	Error        string `json:"error,omitempty"`
}

// updateGitHubRepo is the GitHub project whose Releases are the
// primary update source. The legacy release server (sshtool.app)
// stays as the fallback, and as the only source when the user has
// pointed `update_check_base_url` at their own server.
const updateGitHubRepo = "fpenezic/ssh-tool"

// CheckForUpdate resolves the newest release and compares against
// the build-time injected version. Primary source is GitHub
// Releases; on any error there it falls back to the legacy release
// server's /api/latest. A user-set `update_check_base_url` skips
// GitHub entirely. Honours the `update_check_disabled` setting
// (default false = check enabled). Network/server failures return a
// result with Error set rather than a Go error so the frontend
// doesn't have to special-case them - the UI can fall silent if
// Error != "" and IsNewer == false.
func (a *App) CheckForUpdate() UpdateCheckResult {
	res := UpdateCheckResult{Current: appVersion}

	if a.boolSetting("update_check_disabled") {
		res.Error = "update check disabled in settings"
		return res
	}

	customBase, _, _ := a.db.GetSetting("update_check_base_url")
	if customBase == "" {
		gh, err := updater.FetchGitHubLatest(updateGitHubRepo,
			fmt.Sprintf("ssh-tool/%s", appVersion))
		if err == nil {
			res.Latest = gh.Version
			res.ReleasedAt = gh.ReleasedAt
			res.ChangelogURL = gh.ChangelogURL
			if asset, ok := gh.Assets[platformAssetKey()]; ok {
				res.DownloadURL = asset.URL
				res.DownloadSize = asset.Size
				// Stash URL + digest backend-side; DownloadUpdate acts
				// on these instead of trusting frontend parameters.
				a.updateMu.Lock()
				a.updateAssetURL = asset.URL
				a.updateAssetSHA256 = asset.SHA256
				a.updateApplyScript = ""
				a.updateMu.Unlock()
			}
			res.IsNewer = semverGreater(gh.Version, appVersion)
			return res
		}
		// GitHub unreachable / rate-limited: fall through to the
		// legacy release server.
	}

	base := "https://sshtool.app"
	if customBase != "" {
		base = strings.TrimRight(customBase, "/")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", base+"/api/latest", nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", fmt.Sprintf("ssh-tool/%s", appVersion))
	resp, err := client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		res.Error = fmt.Sprintf("release server returned %d", resp.StatusCode)
		return res
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	var payload struct {
		Version      string `json:"version"`
		ReleasedAt   string `json:"released_at"`
		ChangelogURL string `json:"changelog_url"`
		Assets       map[string]struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		res.Error = "malformed /api/latest response: " + err.Error()
		return res
	}

	res.Latest = payload.Version
	res.ReleasedAt = payload.ReleasedAt
	res.ChangelogURL = payload.ChangelogURL
	if asset, ok := payload.Assets[platformAssetKey()]; ok {
		res.DownloadURL = asset.URL
		res.DownloadSize = asset.Size
		// Stash URL + manifest hash backend-side; DownloadUpdate acts
		// on these instead of trusting frontend-supplied parameters.
		a.updateMu.Lock()
		a.updateAssetURL = asset.URL
		a.updateAssetSHA256 = asset.SHA256
		a.updateApplyScript = "" // a fresh check invalidates any older staged script
		a.updateMu.Unlock()
	}
	res.IsNewer = semverGreater(payload.Version, appVersion)
	return res
}

// ReleaseNotes carries the markdown changelog for a single version.
// Returned by FetchReleaseNotes. ErrorMsg surfaces transient network
// or 404-style failures without turning the IPC call into a hard
// error so the UI can fall back gracefully (open the /releases page
// in a browser, for instance).
type ReleaseNotes struct {
	Version    string `json:"version"`
	ReleasedAt string `json:"released_at"`
	NotesMD    string `json:"notes_md"`
	ErrorMsg   string `json:"error,omitempty"`
}

// FetchReleaseNotes pulls the markdown changelog for `version` from
// the release server. Used by the in-app update modal to render
// release notes inline. Network errors and 404s come back as a
// populated ErrorMsg with the other fields blank.
func (a *App) FetchReleaseNotes(version string) ReleaseNotes {
	out := ReleaseNotes{Version: version}
	if !looksLikeSemver(version) {
		out.ErrorMsg = "invalid version"
		return out
	}
	customBase, _, _ := a.db.GetSetting("update_check_base_url")
	if customBase == "" {
		// Primary: the GitHub release body (the tag's CHANGELOG block,
		// uploaded by the release workflow). Fall back to the legacy
		// server's /api/notes on any error.
		tag := version
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		gh, err := updater.FetchGitHubByTag(updateGitHubRepo, tag,
			fmt.Sprintf("ssh-tool/%s", appVersion))
		if err == nil && gh.NotesMD != "" {
			out.Version = gh.Version
			out.ReleasedAt = gh.ReleasedAt
			out.NotesMD = gh.NotesMD
			return out
		}
	}
	base := "https://sshtool.app"
	if customBase != "" {
		base = strings.TrimRight(customBase, "/")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", base+"/api/notes/"+version, nil)
	if err != nil {
		out.ErrorMsg = err.Error()
		return out
	}
	req.Header.Set("User-Agent", fmt.Sprintf("ssh-tool/%s", appVersion))
	resp, err := client.Do(req)
	if err != nil {
		out.ErrorMsg = err.Error()
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		out.ErrorMsg = fmt.Sprintf("release server returned %d", resp.StatusCode)
		return out
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		out.ErrorMsg = err.Error()
		return out
	}
	var payload struct {
		Version    string `json:"version"`
		ReleasedAt string `json:"released_at"`
		NotesMD    string `json:"notes_md"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		out.ErrorMsg = "malformed /api/notes response: " + err.Error()
		return out
	}
	out.Version = payload.Version
	out.ReleasedAt = payload.ReleasedAt
	out.NotesMD = payload.NotesMD
	return out
}

// UpdateDownloadProgress is streamed on the `update_download_progress`
// event while DownloadUpdate runs. Total is -1 when the server sent no
// Content-Length; the UI falls back to an indeterminate bar.
type UpdateDownloadProgress struct {
	Read  int64 `json:"read"`
	Total int64 `json:"total"`
}

// DownloadUpdate streams the asset captured by the last CheckForUpdate
// into a staging slot next to the running binary, verifying its sha256
// against the manifest value before any swap. It deliberately takes no
// URL parameter - the backend only downloads what it derived from its
// own update check, never what the webview asks for. On Unix the swap
// happens during Download itself (renames are safe over a running
// binary). On Windows the swap is deferred to an apply script that
// ApplyUpdate spawns just before the app exits.
func (a *App) DownloadUpdate() (*updater.DownloadResult, error) {
	a.updateMu.Lock()
	url, wantSHA := a.updateAssetURL, a.updateAssetSHA256
	a.updateMu.Unlock()
	if url == "" {
		return nil, fmt.Errorf("no update available - run a check for updates first")
	}

	// Throttle progress events: at most one per 150ms, plus the final
	// chunk so the bar lands on 100%.
	var lastEmit time.Time
	onProgress := func(read, total int64) {
		now := time.Now()
		if now.Sub(lastEmit) < 150*time.Millisecond && (total <= 0 || read < total) {
			return
		}
		lastEmit = now
		EventsEmit("update_download_progress", UpdateDownloadProgress{Read: read, Total: total})
	}

	res, err := updater.Download(url, wantSHA, onProgress)
	if err != nil {
		a.recordAudit("update.download.failed", "", map[string]string{"error": err.Error(), "url": url})
		return nil, err
	}
	a.updateMu.Lock()
	a.updateApplyScript = res.ApplyScript
	a.updateMu.Unlock()
	a.recordAudit("update.download", "", map[string]string{
		"size":     strconv.FormatInt(res.Size, 10),
		"sha256":   res.SHA256,
		"verified": strconv.FormatBool(res.Verified),
	})
	return res, nil
}

// ApplyUpdate triggers the swap-and-restart helper (Windows only) and
// quits the app. On Unix the rename already happened during Download
// and we just exit so the user's next launch picks up the new binary.
// Like DownloadUpdate it takes no parameters - the script path comes
// from the backend's own Download, never from the webview.
//
// Caller (frontend) is expected to have already closed user-visible
// state; we still allow ~250 ms for in-flight IPC to flush before
// calling os.Exit.
func (a *App) ApplyUpdate() error {
	if runtime.GOOS == "windows" {
		a.updateMu.Lock()
		script := a.updateApplyScript
		a.updateMu.Unlock()
		if script == "" {
			return fmt.Errorf("no staged update - download it first")
		}
		if err := updater.Apply(script); err != nil {
			a.recordAudit("update.apply.failed", "", map[string]string{"error": err.Error()})
			return err
		}
	}
	a.recordAudit("update.apply", "", nil)
	go func() {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}()
	return nil
}

func looksLikeSemver(s string) bool {
	_, ok := parseSemver(s)
	return ok
}

// platformAssetKey maps GOOS/GOARCH to the asset key the release
// server uses (windows-amd64, linux-amd64, darwin-arm64, …).
func platformAssetKey() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

// semverGreater reports whether `a` parses to a strictly higher
// semantic version than `b`. Both are expected to look like
// "v1.2.3" or "1.2.3"; pre-release / build metadata are ignored
// for the comparison. Unparseable inputs return false so a malformed
// server response never claims an update is available.
func semverGreater(a, b string) bool {
	pa, ok1 := parseSemver(a)
	pb, ok2 := parseSemver(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	// Drop pre-release / build metadata.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return [3]int{}, false
	}
	out := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// ConnectionRevealPassword resolves a connection -> credential -> vault
// and returns the plaintext password for the resolved credential. Only
// works on kind=password creds; other kinds return an error so the UI
// can disable the button accordingly.
//
// Use cases: pasting a sudo password into an open terminal without
// flipping to the credentials tab.
func (a *App) ConnectionRevealPassword(connectionID string) (string, error) {
	s, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return "", err
	}
	if s.AuthRef == nil || *s.AuthRef == "" {
		return "", fmt.Errorf("connection has no credential")
	}
	c, err := a.db.GetCredential(*s.AuthRef)
	if err != nil {
		return "", err
	}
	if c.Kind != store.CredPassword {
		return "", fmt.Errorf("credential is not a password (kind=%s)", c.Kind)
	}
	return a.credSvc.RevealSecret(*s.AuthRef)
}

// ConnectionCopyInfo bundles the bits the UI needs for quick-copy
// buttons (username, hostname, port, ssh command). One round-trip
// instead of N.
type ConnectionCopyInfo struct {
	Username    string `json:"username"`
	Hostname    string `json:"hostname"`
	Port        int    `json:"port"`
	HasPassword bool   `json:"has_password"`
	SSHCommand  string `json:"ssh_command"`
}

func (a *App) ConnectionCopyInfo(connectionID string) (*ConnectionCopyInfo, error) {
	s, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, err
	}
	out := &ConnectionCopyInfo{
		Hostname: s.Hostname,
		Port:     int(s.Port),
	}
	if s.Username != nil {
		out.Username = *s.Username
	}
	if s.AuthRef != nil {
		if c, err := a.db.GetCredential(*s.AuthRef); err == nil {
			out.HasPassword = c.Kind == store.CredPassword
		}
	}
	if cmd, err := a.SshSystemCommand(connectionID); err == nil {
		out.SSHCommand = cmd
	}
	return out, nil
}

// ----- Multi-window (Wails v3) -----

// WindowDetachTab opens a new top-level window carrying just the given
// tabId. The Svelte app reads ?detached=<tabId> from the URL and
// renders only that tab's TerminalArea. SSH sessions stay in the
// shared backend pool, so both windows can keep observing pty_output /
// session_state events for sessions that belonged to the detached tab.
//
// Returns the new window's name so the frontend can address it.
func (a *App) WindowDetachTab(tabID string, sessions string, layout string) (string, error) {
	return a.WindowDetachTabAt(tabID, 0, 0, sessions, layout)
}

// WindowDetachTabAt opens a new window for tabID positioned at (screenX, screenY).
// sessions is a comma-separated list of session IDs that belong to the tab;
// the detached window uses it to restore only its own sessions.
// layout is an opaque base64 blob (see TabDragPayload) carrying the
// full pane tree so splits / titles / group metadata survive the move.
// Pass (0,0) for centered placement, "" for sessions to recover all.
func (a *App) WindowDetachTabAt(tabID string, screenX, screenY int, sessions string, layout string) (string, error) {
	if a.app == nil {
		return "", fmt.Errorf("application not initialised")
	}
	name := fmt.Sprintf("detached-%s", tabID)
	url := "/?detached=" + tabID
	if sessions != "" {
		url += "&sessions=" + sessions
	}
	if layout != "" {
		url += "&layout=" + layout
	}
	opts := application.WebviewWindowOptions{
		Name:             name,
		Title:            "ssh-tool",
		Width:            1000,
		Height:           700,
		MinWidth:         600,
		MinHeight:        400,
		URL:              url,
		BackgroundColour: application.NewRGB(30, 30, 46),
		EnableFileDrop:   true,
		DevToolsEnabled:  true,
	}
	if screenX != 0 || screenY != 0 {
		opts.InitialPosition = application.WindowXY
		opts.X = screenX - 500
		opts.Y = screenY - 16
	}
	w := a.app.Window.NewWithOptions(opts)
	registerFileDropForwarding(w)

	// Wire close handler: disconnect every session this detached
	// window owns so the main UI doesn't leave them dangling green
	// in the connections list. Redock goes through WindowRedockTab
	// which clears the slot first, so by the time WindowClosing
	// fires the list is empty (no-op).
	sessionIDs := parseCSVList(sessions)
	if len(sessionIDs) > 0 {
		a.detachedMu.Lock()
		if a.detachedSessions == nil {
			a.detachedSessions = map[string][]string{}
		}
		a.detachedSessions[name] = sessionIDs
		a.detachedMu.Unlock()
	}
	// Defer the WindowClosing wiring slightly: Wails v3 alpha emits
	// a WindowClosing event during window creation on some builds,
	// which would tear down the sessions we just transferred into
	// the new window. Half a second is plenty for the open-event
	// noise to settle.
	go func() {
		time.Sleep(500 * time.Millisecond)
		w.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
			a.detachedMu.Lock()
			ids := a.detachedSessions[name]
			delete(a.detachedSessions, name)
			a.detachedMu.Unlock()
			for _, sid := range ids {
				// SshDisconnect cleans up forwards + emits state events
				// + evicts from broadcast group via the OnClose hook
				// already wired in connectAndStart.
				if err := a.SshDisconnect(sid); err != nil {
					log.Printf("detached window %s: disconnect %s: %v", name, sid, err)
				}
			}
		})
	}()
	return name, nil
}

func parseCSVList(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, ",")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	// drop empties
	filtered := out[:0]
	for _, v := range out {
		if v != "" {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// WindowRedockTab tells the main window that a detached tab should come back.
// sessions is the comma-separated list of session IDs the detached window owns.
// layout is an opaque base64 blob with the pane tree so the main
// window can restore splits / titles instead of reflattening sessions
// into separate tabs. The main window uses them to reconstruct its pane tabs.
func (a *App) WindowRedockTab(tabID string, sessions string, layout string) {
	EventsEmit("window_redock", map[string]string{
		"tabId":    tabID,
		"sessions": sessions,
		"layout":   layout,
	})
}

// WindowCloseSelf closes the calling window WITHOUT touching its
// sessions. Used by the redock path - the main window has just
// claimed ownership, so we clear the detached slot before closing
// and the WindowClosing handler finds nothing to disconnect.
//
// Plain user-close (clicking the X on a detached window) skips
// this path and goes through WindowClosing directly, which disconnects.
func (a *App) WindowCloseSelf(windowName string) {
	if a.app == nil {
		return
	}
	a.detachedMu.Lock()
	delete(a.detachedSessions, windowName)
	a.detachedMu.Unlock()
	if w, ok := a.app.Window.GetByName(windowName); ok {
		w.Close()
	}
}

// WindowStartTabDrag registers an in-flight tab drag originating from a
// detached window. The main window polls this on drop to reclaim the tab.
// layout is an opaque base64 blob carrying the pane tree so splits and
// titles survive the redock.
func (a *App) WindowStartTabDrag(tabID string, sessions string, layout string) {
	a.pendingTabDragMu.Lock()
	a.pendingTabDrag = &TabDragPayload{TabID: tabID, Sessions: sessions, Layout: layout}
	a.pendingTabDragMu.Unlock()
}

// WindowAcceptTabDrag is called by the main window when a tab is dropped onto
// its tab bar. Returns the pending drag payload and clears it.
func (a *App) WindowAcceptTabDrag() (*TabDragPayload, error) {
	a.pendingTabDragMu.Lock()
	defer a.pendingTabDragMu.Unlock()
	if a.pendingTabDrag == nil {
		return nil, fmt.Errorf("no pending tab drag")
	}
	p := a.pendingTabDrag
	a.pendingTabDrag = nil
	return p, nil
}

// WindowCancelTabDrag clears a pending drag without accepting it (drag ended
// without being dropped on the main window).
func (a *App) WindowCancelTabDrag() {
	a.pendingTabDragMu.Lock()
	a.pendingTabDrag = nil
	a.pendingTabDragMu.Unlock()
}

// ----- Verbose connect debug buffer -----

const debugBufCap = 256

// appendDebug records one debug line under connectionID. Caps the
// buffer at debugBufCap entries - older lines drop off the front.
func (a *App) appendDebug(connectionID, line string) {
	a.debugBufMu.Lock()
	defer a.debugBufMu.Unlock()
	buf := a.debugBuf[connectionID]
	buf = append(buf, line)
	if len(buf) > debugBufCap {
		buf = buf[len(buf)-debugBufCap:]
	}
	a.debugBuf[connectionID] = buf
}

// resetDebug clears the buffer for connectionID. Called at the start
// of each SshConnect so a fresh attempt's diagnostics aren't
// shadowed by a previous attempt's lines.
func (a *App) resetDebug(connectionID string) {
	a.debugBufMu.Lock()
	delete(a.debugBuf, connectionID)
	a.debugBufMu.Unlock()
}

// ----- App log viewer -----

// AppGetLogs returns a snapshot of the in-app log ring (oldest first).
// Frontend mounts it once and then appends live entries via the
// "app_log" Wails event.
func (a *App) AppGetLogs() []string {
	if a.logBuf == nil {
		return nil
	}
	return a.logBuf.Snapshot()
}

// FrontendLog writes a line from the frontend into the same log sink the
// in-app Log viewer reads (the Go logger). Lets the webview surface diag
// output to a user who can't open the browser console - e.g. someone
// running the app on a remote desktop. Prefixed so frontend lines are
// distinguishable from backend ones.
func (a *App) FrontendLog(line string) {
	log.Printf("[fe] %s", line)
}

// AppClearLogs empties the ring. Live emit stops mid-stream so the
// frontend just calls AppGetLogs() to confirm the buffer is empty.
func (a *App) AppClearLogs() {
	if a.logBuf == nil {
		return
	}
	a.logBuf.Clear()
}

// AppGetLogTailEnabled / AppSetLogTailEnabled control whether the log
// buffer collects lines + emits live events. When off, log.Printf still
// writes to stdout but the ring stays frozen and the UI gets no
// notifications.
func (a *App) AppGetLogTailEnabled() bool {
	if a.logBuf == nil {
		return false
	}
	return a.logBuf.Enabled()
}

func (a *App) AppSetLogTailEnabled(on bool) error {
	if a.logBuf != nil {
		a.logBuf.SetEnabled(on)
	}
	v := "0"
	if on {
		v = "1"
	}
	return a.db.SetSetting("app_log_tail_enabled", v)
}

// SshGetConnectDebug returns the buffered debug lines for the most
// recent connect attempt against this connectionID. Empty slice if
// nothing buffered (verbose off, or no recent attempt).
func (a *App) SshGetConnectDebug(connectionID string) []string {
	a.debugBufMu.Lock()
	defer a.debugBufMu.Unlock()
	lines := a.debugBuf[connectionID]
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

// --- Workspaces -------------------------------------------------------

// WorkspaceListResult mirrors store.Workspace but with the layout
// pre-parsed by the frontend; backend stays opaque.
func (a *App) WorkspacesList() ([]store.Workspace, error) {
	return a.db.ListWorkspaces()
}

func (a *App) WorkspaceCreate(name, layoutJSON string) (*store.Workspace, error) {
	return a.db.CreateWorkspace(name, layoutJSON)
}

func (a *App) WorkspaceUpdate(id, name, layoutJSON string) (*store.Workspace, error) {
	return a.db.UpdateWorkspace(id, name, layoutJSON)
}

func (a *App) WorkspaceDelete(id string) error {
	return a.db.DeleteWorkspace(id)
}

// WorkspaceTouchLastOpened bumps last_opened_at after the frontend
// successfully restores a workspace. Best-effort.
func (a *App) WorkspaceTouchLastOpened(id string) error {
	return a.db.TouchWorkspaceLastOpened(id)
}

// --- Snippets ---------------------------------------------------------

// SnippetsList returns snippets visible for the optional connection.
// When connectionID is "" only global snippets are returned; otherwise
// both global + per-connection snippets are returned, ordered by recent
// use first.
func (a *App) SnippetsList(connectionID string) ([]store.Snippet, error) {
	var cp *string
	if connectionID != "" {
		cp = &connectionID
	}
	return a.db.ListSnippets(cp)
}

func (a *App) SnippetCreate(in store.SnippetInput) (*store.Snippet, error) {
	if in.ConnectionID != nil && *in.ConnectionID == "" {
		in.ConnectionID = nil
	}
	return a.db.CreateSnippet(in)
}

func (a *App) SnippetUpdate(id string, in store.SnippetInput) (*store.Snippet, error) {
	if in.ConnectionID != nil && *in.ConnectionID == "" {
		in.ConnectionID = nil
	}
	return a.db.UpdateSnippet(id, in)
}

func (a *App) SnippetDelete(id string) error {
	return a.db.DeleteSnippet(id)
}

// SnippetSendToSession writes the snippet body into the named session's
// stdin (after a RecordSnippetUse bump). Trailing newline is appended
// when missing so single-line snippets execute as the user would expect.
func (a *App) SnippetSendToSession(snippetID, sessionID string) error {
	s, err := a.db.GetSnippet(snippetID)
	if err != nil {
		return err
	}
	body := s.Body
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body += "\n"
	}
	data := []byte(body)

	// If the origin session is part of an active broadcast group,
	// fan the snippet out to every member (SSH or local PTY,
	// matching the keystroke fan-out path). Otherwise just write
	// to the single session. We always write to the origin first
	// so the user sees the snippet land in the foreground tab.
	writeOne := func(sid string) error {
		if sess, ok := a.pool.Get(sid); ok {
			return sess.Write(data)
		}
		if sess, ok := a.localPool.Get(sid); ok {
			return sess.Write(data)
		}
		return fmt.Errorf("session %s not found", sid)
	}
	if err := writeOne(sessionID); err != nil {
		return err
	}
	a.broadcastMu.Lock()
	// Union of every group containing the origin (matches FanOut).
	otherSet := make(map[string]bool)
	for _, g := range a.broadcastGroups {
		if !g[sessionID] {
			continue
		}
		for id := range g {
			if id == sessionID {
				continue
			}
			otherSet[id] = true
		}
	}
	others := make([]string, 0, len(otherSet))
	for id := range otherSet {
		others = append(others, id)
	}
	a.broadcastMu.Unlock()
	for _, id := range others {
		if err := writeOne(id); err != nil {
			log.Printf("snippet broadcast write %s: %v", id, err)
		}
	}
	_ = a.db.RecordSnippetUse(snippetID)
	return nil
}

// --- Tcpdump ----------------------------------------------------------

// TcpdumpProbeResult tells the frontend what auth path the next capture
// will take. RootUser=true means tcpdump runs directly; SudoNoPwd=true
// means a cached/NOPASSWD sudo ticket is good; HasCandidatePassword
// means we have a stored password (per-connection override or password
// credential) that's likely the sudo password too - the frontend can
// offer "Try the saved password first" instead of prompting blind.
type TcpdumpProbeResult struct {
	RootUser             bool `json:"root_user"`
	SudoNoPwd            bool `json:"sudo_no_pwd"`
	HasCandidatePassword bool `json:"has_candidate_password"`
}

// resolveSudoCandidate looks up a plausible sudo password for the
// session's connection. Tries the per-connection password override
// first, then the resolved password-kind credential. Returns "" if
// neither is set.
func (a *App) resolveSudoCandidate(sessionID string) string {
	meta, ok := a.sessionMeta[sessionID]
	if !ok || meta.connectionID == "" {
		return ""
	}
	connID := meta.connectionID
	if rawConn, err := a.db.GetConnection(connID); err == nil && rawConn.PasswordVaultKey != nil {
		if pass, ok, _ := a.vault.Get(*rawConn.PasswordVaultKey); ok && pass != "" {
			return pass
		}
	}
	if pass, err := a.ConnectionRevealPassword(connID); err == nil && pass != "" {
		return pass
	}
	return ""
}

func (a *App) TcpdumpProbe(sessionID string) (*TcpdumpProbeResult, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	target := sess.TargetClient()
	if target == nil {
		return nil, fmt.Errorf("session target unavailable")
	}
	r, sn, err := sshlayer.CheckRootOrSudo(target)
	if err != nil {
		return nil, err
	}
	hasCand := !r && !sn && a.resolveSudoCandidate(sessionID) != ""
	return &TcpdumpProbeResult{
		RootUser:             r,
		SudoNoPwd:            sn,
		HasCandidatePassword: hasCand,
	}, nil
}

func (a *App) TcpdumpListInterfaces(sessionID string) ([]string, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	target := sess.TargetClient()
	if target == nil {
		return nil, fmt.Errorf("session target unavailable")
	}
	return sshlayer.ListInterfaces(target)
}

// TcpdumpStartInput carries the frontend's request. RootUser + SudoNoPwd
// come back from TcpdumpProbe - passing them as separate fields lets the
// frontend display the auth state in the dialog before the user clicks
// Start, and avoids a second probe round-trip.
type TcpdumpStartInput struct {
	SessionID        string `json:"session_id"`
	Iface            string `json:"iface"`
	BPFFilter        string `json:"bpf_filter"`
	MaxCount         int    `json:"max_count"`
	RootUser         bool   `json:"root_user"`
	SudoNoPwd        bool   `json:"sudo_no_pwd"`
	UseSavedPassword bool   `json:"use_saved_password"` // auto-feed the connection's password to sudo
	Verbose          bool   `json:"verbose"`            // -v with per-protocol decode (DHCP, DNS, ARP)
	Insights         bool   `json:"insights"`           // live network-health analyzer (routing/interface anomalies)
	IncludeSSH       bool   `json:"include_ssh"`        // capture the SSH control connection too (default: exclude it)
	// PortOverrides maps a non-standard port to a protocol name so
	// the decoder treats that port as the named proto. Useful for
	// HTTP on 9000, MQTT bridge on 1885, etc. Keys are ports as
	// strings (JSON object keys); values are lowercase proto names
	// recognised by the decoder.
	PortOverrides map[string]string `json:"port_overrides,omitempty"`
}

// TcpdumpStart launches a capture and returns its dumpID. Live lines
// arrive as `tcpdump_line:<dumpID>` events; lifecycle transitions
// (needs_password / started / password_rejected / error / ended) as
// `tcpdump_event:<dumpID>` events with payload {event, msg}.
func (a *App) TcpdumpStart(in TcpdumpStartInput) (string, error) {
	sess, ok := a.pool.Get(in.SessionID)
	if !ok {
		return "", fmt.Errorf("session %s not found", in.SessionID)
	}
	target := sess.TargetClient()
	if target == nil {
		return "", fmt.Errorf("session target unavailable")
	}
	opts := sshlayer.TcpdumpOptions{
		Iface:     in.Iface,
		BPFFilter: in.BPFFilter,
		MaxCount:  in.MaxCount,
		Verbose:   in.Verbose,
		Insights:  in.Insights,
		// Exclude the SSH control connection by default - capturing it
		// over the same session is a feedback loop (see TcpdumpOptions).
		// The user can opt back in (e.g. to debug SSH itself).
		ExcludeSSH: !in.IncludeSSH,
	}
	// For the ARP off-subnet check the analyzer needs the host's own
	// interface subnets. Cheap one-shot probe; failure just disables
	// that single check (LocalCIDRs stays empty).
	if in.Insights {
		opts.LocalCIDRs, _ = sshlayer.ListLocalCIDRs(target)
	}
	if len(in.PortOverrides) > 0 {
		opts.PortOverrides = make(map[int]string, len(in.PortOverrides))
		for k, v := range in.PortOverrides {
			port, err := strconv.Atoi(k)
			if err != nil || port <= 0 || port > 65535 {
				continue
			}
			opts.PortOverrides[port] = v
		}
	}
	// Pre-allocate the handle ID so the line/lifecycle handlers can
	// route events even before StartTcpdump returns.
	dumpID := uuid.New().String()

	// Batch + tail-cap the live packet stream instead of emitting one
	// Wails event per packet. On a busy host a continuous capture
	// produces hundreds to thousands of packets per second; emitting them
	// all (even batched) floods the WebKit IPC queue and burns CPU on
	// both sides serializing/deserializing packets the UI never shows -
	// the live view only renders the most recent tail. So we keep only
	// the last `batchTailCap` packets seen since the previous flush in a
	// fixed-size CIRCULAR buffer (O(1) per packet, no per-packet reslice),
	// count the rest as skipped, and emit one small batch every
	// `batchFlushEvery`. Total carries the cumulative count so the UI
	// shows the true number. Full history stays on the backend ring
	// (Snapshot), so a re-attach still recovers everything.
	const (
		batchFlushEvery = 120 * time.Millisecond
		batchTailCap    = 250 // a bit above the frontend's render tail
	)
	var (
		batchMu      sync.Mutex
		batchRing    = make([]sshlayer.ParsedPacket, batchTailCap)
		batchHead    int   // next write index
		batchFilled  int   // how many valid entries (<= cap)
		batchSkipped int64 // overwritten (older) since last flush
		batchTotal   int64 // cumulative for the whole capture
	)
	batchDone := make(chan struct{})
	onLine := func(pkt sshlayer.ParsedPacket) {
		batchMu.Lock()
		batchTotal++
		if batchFilled == batchTailCap {
			batchSkipped++ // overwriting an unsent older packet
		} else {
			batchFilled++
		}
		batchRing[batchHead] = pkt
		batchHead = (batchHead + 1) % batchTailCap
		batchMu.Unlock()
	}
	flushBatch := func() {
		batchMu.Lock()
		if batchFilled == 0 {
			batchMu.Unlock()
			return
		}
		// Read the ring back in chronological order (oldest kept -> newest).
		out := make([]sshlayer.ParsedPacket, batchFilled)
		start := (batchHead - batchFilled + batchTailCap) % batchTailCap
		for i := 0; i < batchFilled; i++ {
			out[i] = batchRing[(start+i)%batchTailCap]
		}
		skipped := batchSkipped
		total := batchTotal
		batchFilled = 0
		batchHead = 0
		batchSkipped = 0
		batchMu.Unlock()
		EventsEmit("tcpdump_line_batch:"+dumpID, sshlayer.TcpdumpLineBatch{
			Packets: out,
			Skipped: skipped,
			Total:   total,
		})
	}
	go func() {
		t := time.NewTicker(batchFlushEvery)
		defer t.Stop()
		for {
			select {
			case <-batchDone:
				flushBatch() // final drain
				return
			case <-t.C:
				flushBatch()
			}
		}
	}()
	sid := in.SessionID
	var batchStopOnce sync.Once
	stopBatch := func() { batchStopOnce.Do(func() { close(batchDone) }) }
	onLifecycle := func(event, msg string) {
		// Capture ended on its own (hit the packet cap, process died) -
		// drop the session→dump index so a later window doesn't try to
		// re-attach to a dead capture. Guard on the index still pointing
		// at THIS dump so a restart doesn't clobber a newer one.
		if event == "ended" || event == "error" {
			a.tcpdumpMu.Lock()
			if a.tcpdumpBySession[sid] == dumpID {
				delete(a.tcpdumpBySession, sid)
			}
			a.tcpdumpMu.Unlock()
			stopBatch()
		}
		EventsEmit("tcpdump_event:"+dumpID, map[string]string{"event": event, "msg": msg})
	}
	onInsight := func(ins sshlayer.Insight) {
		EventsEmit("tcpdump_insight:"+dumpID, ins)
	}
	h, err := sshlayer.StartTcpdump(target, in.RootUser, in.SudoNoPwd, opts, onLine, onLifecycle, onInsight)
	if err != nil {
		return "", err
	}
	h.ID = dumpID
	a.tcpdumpMu.Lock()
	if a.tcpdumps == nil {
		a.tcpdumps = map[string]*sshlayer.TcpdumpHandle{}
	}
	if a.tcpdumpBySession == nil {
		a.tcpdumpBySession = map[string]string{}
	}
	a.tcpdumps[dumpID] = h
	a.tcpdumpBySession[in.SessionID] = dumpID
	a.tcpdumpMu.Unlock()
	// Auto-feed the cached password to the sudo prompt, if requested.
	// The lifecycle handler already emitted "needs_password"; sending
	// here unblocks the awaitPwd channel inside StartTcpdump before the
	// user ever sees the modal prompt. If the password turns out to be
	// wrong, lifecycle "password_rejected" fires and the modal asks
	// the user for the real one.
	if in.UseSavedPassword && !in.RootUser && !in.SudoNoPwd {
		if pass := a.resolveSudoCandidate(in.SessionID); pass != "" {
			h.ProvidePassword(pass)
		}
	}
	return dumpID, nil
}

func (a *App) TcpdumpProvidePassword(dumpID, password string) error {
	a.tcpdumpMu.Lock()
	h := a.tcpdumps[dumpID]
	a.tcpdumpMu.Unlock()
	if h == nil {
		return fmt.Errorf("tcpdump %s not found", dumpID)
	}
	h.ProvidePassword(password)
	return nil
}

func (a *App) TcpdumpStop(dumpID string) error {
	a.tcpdumpMu.Lock()
	h := a.tcpdumps[dumpID]
	delete(a.tcpdumps, dumpID)
	// Clear any session→dump index entry pointing at this dump.
	for sid, did := range a.tcpdumpBySession {
		if did == dumpID {
			delete(a.tcpdumpBySession, sid)
		}
	}
	a.tcpdumpMu.Unlock()
	if h == nil {
		return nil
	}
	h.Stop()
	return nil
}

// TcpdumpActiveForSession returns the dumpID of a capture already
// running for this session, or "" if none. A window that didn't start
// the capture (after a tab detach / redock moved the session here) calls
// this on mount and, when it gets a dumpID back, subscribes to the
// existing tcpdump_*:<dumpID> event streams instead of starting a second
// capture. The capture's lifetime is the session's, so it survives the
// window that launched it going away.
// TcpdumpActiveInfo describes a capture already running for a session,
// so a window attaching after a detach can show what it's doing instead
// of an empty/unknown state.
type TcpdumpActiveInfo struct {
	DumpID     string `json:"dump_id"`
	Iface      string `json:"iface"`
	BPFFilter  string `json:"bpf_filter"`
	Verbose    bool   `json:"verbose"`
	Insights   bool   `json:"insights"`
	Continuous bool   `json:"continuous"`
	MaxCount   int    `json:"max_count"`
}

func (a *App) TcpdumpActiveForSession(sessionID string) *TcpdumpActiveInfo {
	a.tcpdumpMu.Lock()
	defer a.tcpdumpMu.Unlock()
	dumpID := a.tcpdumpBySession[sessionID]
	if dumpID == "" {
		return &TcpdumpActiveInfo{}
	}
	h := a.tcpdumps[dumpID]
	if h == nil {
		return &TcpdumpActiveInfo{}
	}
	return &TcpdumpActiveInfo{
		DumpID:     dumpID,
		Iface:      h.Opts.Iface,
		BPFFilter:  h.Opts.BPFFilter,
		Verbose:    h.Opts.Verbose,
		Insights:   h.Opts.Insights,
		Continuous: h.Opts.MaxCount < 0,
		MaxCount:   h.Opts.MaxCount,
	}
}

// TcpdumpSnapshotResult carries the retained packet history for a
// capture plus the cumulative watermark the frontend dedupes against.
type TcpdumpSnapshotResult struct {
	Packets []sshlayer.ParsedPacket `json:"packets"`
	Cum     int64                   `json:"cum"`
}

// TcpdumpSnapshot returns the server-side packet history for a running
// capture. A window attaching mid-capture (after a detach moved the
// session here) calls this to recover the packets it missed, then
// dedupes the live stream against Cum. Empty result if the dump is gone.
func (a *App) TcpdumpSnapshot(dumpID string) (*TcpdumpSnapshotResult, error) {
	a.tcpdumpMu.Lock()
	h := a.tcpdumps[dumpID]
	a.tcpdumpMu.Unlock()
	if h == nil {
		return &TcpdumpSnapshotResult{}, nil
	}
	pkts, cum := h.Snapshot()
	return &TcpdumpSnapshotResult{Packets: pkts, Cum: cum}, nil
}

// TcpdumpRouteQuery mirrors sshlayer.RouteQuery for the IPC boundary.
type TcpdumpRouteQuery struct {
	Dst  string `json:"dst"`
	From string `json:"from"`
}

// TcpdumpCheckRoute runs `ip route get` on the session's host for each
// query and returns the egress interface + source address the kernel
// would pick. This is the active confirmation behind an insight's
// "Check route" button: it shows whether traffic to a peer actually
// leaves the expected interface with the expected source IP, which is
// the ground truth for the wrong-interface / 0.0.0.0-bind class of
// problems the passive analyzer flags. No sudo required.
func (a *App) TcpdumpCheckRoute(sessionID string, queries []TcpdumpRouteQuery) ([]sshlayer.RouteResult, error) {
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	target := sess.TargetClient()
	if target == nil {
		return nil, fmt.Errorf("session target unavailable")
	}
	qs := make([]sshlayer.RouteQuery, 0, len(queries))
	for _, q := range queries {
		if q.Dst == "" {
			continue
		}
		qs = append(qs, sshlayer.RouteQuery{Dst: q.Dst, From: q.From})
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("no valid route queries")
	}
	return sshlayer.CheckRoutes(target, qs)
}

// BatchExecInput is the IPC payload the multi-select panel sends.
type BatchExecInput struct {
	ConnectionIDs  []string `json:"connection_ids"`
	Command        string   `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// BatchExec runs one command across multiple connections in parallel.
// Each host opens its own quiet SSH chain (no PTY, no scrollback) and
// captures stdout/stderr/exit. Concurrency capped at 8. Returns a
// per-host result slice in the same order as the input ids.
func (a *App) BatchExec(in BatchExecInput) ([]sshlayer.BatchHostResult, error) {
	if in.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if len(in.ConnectionIDs) == 0 {
		return nil, fmt.Errorf("no connections selected")
	}
	hosts := make([]sshlayer.BatchHostInput, 0, len(in.ConnectionIDs))
	for _, cid := range in.ConnectionIDs {
		// Dynamic-inventory entries arrive as "dyn:<entryId>" - they
		// don't exist in the connections table; we build a synthetic
		// connection on the fly that inherits from the dynamic
		// folder. Mirrors SshConnectDynamic's resolver path.
		if strings.HasPrefix(cid, "dyn:") {
			entryID := strings.TrimPrefix(cid, "dyn:")
			entry, err := a.db.GetDynamicEntry(entryID)
			if err != nil || entry == nil {
				hosts = append(hosts, sshlayer.BatchHostInput{
					ConnectionID: cid, Settings: nil,
					Name: "(unknown dynamic entry)", Hostname: "",
				})
				continue
			}
			folders, err := a.db.ListFolders()
			if err != nil {
				hosts = append(hosts, sshlayer.BatchHostInput{
					ConnectionID: cid, Settings: nil,
					Name: entry.Name, Hostname: entry.Hostname,
				})
				continue
			}
			folderRef := entry.FolderID
			synthetic := store.Connection{
				ID: cid, FolderID: &folderRef,
				Name: entry.Name, Hostname: entry.Hostname,
				Overrides: store.InheritableSettings{},
			}
			s := resolver.ResolveWith(synthetic, folders)
			if s.Username == nil && s.AuthRef != nil {
				if cred, err2 := a.db.GetCredential(*s.AuthRef); err2 == nil && cred.DefaultUsername != nil {
					s.Username = cred.DefaultUsername
				}
			}
			hosts = append(hosts, sshlayer.BatchHostInput{
				ConnectionID: cid, Settings: &s,
				Name: entry.Name, Hostname: entry.Hostname,
			})
			continue
		}

		s, err := resolver.ResolveConnection(a.db, cid)
		if err != nil {
			// Skip with a sentinel result rather than failing the whole batch
			hosts = append(hosts, sshlayer.BatchHostInput{
				ConnectionID: cid,
				Settings:     nil,
				Name:         "(unknown)",
				Hostname:     "",
			})
			continue
		}
		// Resolve per-connection password override (same as SshConnect).
		if rawConn, connErr := a.db.GetConnection(cid); connErr == nil && rawConn.PasswordVaultKey != nil {
			if pass, ok, _ := a.vault.Get(*rawConn.PasswordVaultKey); ok && pass != "" {
				s.PasswordOverride = &pass
			}
		}
		// Username fallback from the credential's default.
		if s.Username == nil && s.AuthRef != nil {
			if cred, err2 := a.db.GetCredential(*s.AuthRef); err2 == nil && cred.DefaultUsername != nil {
				s.Username = cred.DefaultUsername
			}
		}
		name := "(unknown)"
		if c, err := a.db.GetConnection(cid); err == nil {
			name = c.Name
		}
		hosts = append(hosts, sshlayer.BatchHostInput{
			ConnectionID: cid,
			Settings:     s,
			Name:         name,
			Hostname:     s.Hostname,
		})
	}
	// Resolve the user-configurable connect timeout.
	var ct time.Duration
	if raw := a.SettingsGet("connect_timeout_seconds"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			ct = time.Duration(n) * time.Second
		}
	}
	results := sshlayer.BatchExec(a.db, a.vault, a.makeHostKeyCallback(), a.makeAlgoLookup(), ct, hosts, in.Command, in.TimeoutSeconds)
	return results, nil
}

// HttpDo issues a one-shot HTTP request. Routes through SocksAddr if
// set - typically the local bind of an active dynamic forward
// ("127.0.0.1:1080" etc.) - so requests can reach endpoints inside
// the remote network without curl gymnastics. Body is sent verbatim;
// the caller decides Content-Type via the headers list. Response
// body is capped at 4 MiB; the rest is dropped with truncated=true.
func (a *App) HttpDo(req httpc.Request) (*httpc.Response, error) {
	return httpc.Do(req)
}
