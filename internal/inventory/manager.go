package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// Manager coordinates dynamic-folder refreshes: per-folder timer
// goroutines that periodically call the provider and replace the
// cached entries. Mutations go through the same flow so a manual
// refresh button and the timer share one code path.
//
// Lifecycle: caller (App) constructs once at startup, calls Start()
// to spin up timers for every existing dynamic folder, and Refresh(id)
// on demand. OnRefresh callback fires after every successful refresh
// so the frontend can re-render the tree without polling.
type Manager struct {
	db        *store.DB
	vault     *creds.Vault
	providers map[string]Provider
	onRefresh func(folderID string)

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // folderID -> ctx cancel for its timer
}

func NewManager(db *store.DB, vault *creds.Vault, onRefresh func(folderID string)) *Manager {
	return &Manager{
		db:        db,
		vault:     vault,
		providers: defaultProviders(),
		onRefresh: onRefresh,
		cancels:   map[string]context.CancelFunc{},
	}
}

func defaultProviders() map[string]Provider {
	return map[string]Provider{
		Proxmox{}.Name():      Proxmox{},
		Hetzner{}.Name():      Hetzner{},
		DigitalOcean{}.Name(): DigitalOcean{},
		Linode{}.Name():       Linode{},
		Vultr{}.Name():        Vultr{},
		Scaleway{}.Name():     Scaleway{},
		AWSEC2{}.Name():       AWSEC2{},
		Ansible{}.Name():      Ansible{},
	}
}

// Start spawns the timer goroutine for every existing dynamic folder.
// Call once after the DB is open and migrations have run. Safe to
// call multiple times; existing timers get cancelled and respawned.
func (m *Manager) Start() {
	folders, err := m.db.ListDynamicFolders()
	if err != nil {
		log.Printf("inventory: ListDynamicFolders: %v", err)
		return
	}
	for _, f := range folders {
		m.startTimer(f)
	}
}

func (m *Manager) startTimer(f store.DynamicFolder) {
	m.mu.Lock()
	if cancel, ok := m.cancels[f.FolderID]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[f.FolderID] = cancel
	m.mu.Unlock()

	if f.RefreshSeconds <= 0 {
		return // 0 disables the timer; user can still hit manual refresh
	}
	go m.runTimer(ctx, f.FolderID, time.Duration(f.RefreshSeconds)*time.Second)
}

func (m *Manager) runTimer(ctx context.Context, folderID string, interval time.Duration) {
	// First tick after a short delay so the boot wave doesn't all
	// hit proxmox at once.
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Refresh(ctx, folderID, false); err != nil {
				log.Printf("inventory: refresh %s: %v", folderID, err)
			}
			t.Reset(interval)
		}
	}
}

// Refresh runs the provider fetch + filter pipeline once and writes
// the result to the cache. Errors are recorded on the dynamic_folders
// row but don't clear the existing entries - last good state stays
// visible during transient outages.
//
// force=true (user-initiated refresh, config change) always writes
// the fetched result so the cached Raw JSON is current for the
// detail pane. The TIMER passes force=false: providers embed
// volatile metrics in Raw (Proxmox uptime/cpu/mem change every
// poll), and writing an unchanged-in-substance entry set every
// cycle kept the sync profile permanently dirty.
func (m *Manager) Refresh(ctx context.Context, folderID string, force bool) error {
	f, err := m.db.GetDynamicFolder(folderID)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("dynamic folder %s not found", folderID)
	}
	// setError suppresses no-op writes: a provider that's down keeps
	// failing with the SAME error every cycle, and rewriting it each
	// time dirties store.db - which the auto-sync mtime signal reads
	// as "profile changed" and pushes a snapshot of nothing.
	setError := func(msg string) {
		if f.LastError == msg {
			return
		}
		_ = m.db.SetDynamicFolderError(folderID, msg)
	}
	prov, ok := m.providers[f.Provider]
	if !ok {
		setError("unknown provider: " + f.Provider)
		return fmt.Errorf("unknown provider %q", f.Provider)
	}
	cfg, err := m.resolveSecrets(f.Config)
	if err != nil {
		setError(err.Error())
		return err
	}
	entries, err := prov.Fetch(ctx, cfg)
	if err != nil {
		setError(err.Error())
		return err
	}

	// Apply per-folder filter (read from the same config blob).
	filter := filterFromConfig(f.Config)
	entries = filter.Apply(entries)

	// Drop entries that have been pinned into permanent connections.
	// Otherwise the host would appear twice (once as the real
	// connection, once as a dynamic ghost) after every refresh.
	pinned, perr := m.db.ListPinnedExternalIDs(folderID)
	if perr != nil {
		return perr
	}
	if len(pinned) > 0 {
		filtered := entries[:0]
		for _, e := range entries {
			if _, isPinned := pinned[e.ExternalID]; isPinned {
				continue
			}
			filtered = append(filtered, e)
		}
		entries = filtered
	}

	rows := make([]store.DynamicEntry, 0, len(entries))
	for _, e := range entries {
		raw, _ := json.Marshal(json.RawMessage(e.Raw))
		_ = raw // raw is already json; keep as-is
		rows = append(rows, store.DynamicEntry{
			ID:         uuid.NewString(),
			FolderID:   folderID,
			ExternalID: e.ExternalID,
			Name:       e.Name,
			Hostname:   e.Hostname,
			Kind:       string(e.Kind),
			Status:     e.Status,
			Tags:       e.Tags,
			Raw:        e.Raw,
		})
	}
	// Skip the destructive replace when nothing changed. Same sync
	// rationale as setError above: a stable inventory re-written every
	// RefreshSeconds kept the profile permanently dirty. Identity is
	// the FUNCTIONAL fields only - row ids regenerate per refresh and
	// Raw carries volatile provider metrics; both deliberately
	// excluded. The write still happens when forced or when an error
	// state needs clearing (last_error -> '').
	if !force && f.LastError == "" && sameDynamicEntries(m.db, folderID, rows) {
		if m.onRefresh != nil {
			m.onRefresh(folderID) // UI may still want the "checked" signal
		}
		return nil
	}
	if err := m.db.ReplaceDynamicEntries(folderID, rows); err != nil {
		return err
	}
	if m.onRefresh != nil {
		m.onRefresh(folderID)
	}
	return nil
}

// sameDynamicEntries reports whether the freshly fetched rows carry
// the same functional content as the cache (order-insensitive; row
// ids and Raw excluded - Raw embeds per-poll metrics like uptime).
func sameDynamicEntries(db *store.DB, folderID string, fresh []store.DynamicEntry) bool {
	current, err := db.ListDynamicEntries(folderID)
	if err != nil || len(current) != len(fresh) {
		return false
	}
	key := func(e store.DynamicEntry) string {
		// Tags compared order-insensitively: some providers build them
		// from a Go map (randomised iteration), so a stable host can
		// emit the same tags in a different order each refresh.
		tags := append([]string(nil), e.Tags...)
		sort.Strings(tags)
		return strings.Join([]string{
			e.ExternalID, e.Name, e.Hostname, e.Kind, e.Status,
			strings.Join(tags, "\x1f"),
		}, "\x1e")
	}
	seen := make(map[string]int, len(current))
	for _, e := range current {
		seen[key(e)]++
	}
	for _, e := range fresh {
		k := key(e)
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}

// Restart re-spins the timer for a folder (after a config / interval
// update). Safe to call when the folder has no timer yet.
func (m *Manager) Restart(folderID string) {
	f, err := m.db.GetDynamicFolder(folderID)
	if err != nil || f == nil {
		return
	}
	m.startTimer(*f)
}

// Stop cancels the timer for a folder. Called from the delete path.
func (m *Manager) Stop(folderID string) {
	m.mu.Lock()
	if cancel, ok := m.cancels[folderID]; ok {
		cancel()
		delete(m.cancels, folderID)
	}
	m.mu.Unlock()
}

// resolveSecrets pulls token-style credential references out of the
// folder's config and inlines the actual secret into a copy of the
// map before handing it to the provider. The on-disk config keeps
// only the credential id, never the secret.
//
// For proxmox: `api_token_credential_id` → resolves to the credential's
// token_id (config) + vault-stored secret, which are written back as
// `api_token_id` + `api_token_secret` for the provider to consume.
func (m *Manager) resolveSecrets(cfg map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	credID, _ := cfg["api_token_credential_id"].(string)
	if credID == "" || m.vault == nil {
		return out, nil
	}
	cred, err := m.db.GetCredential(credID)
	if err != nil {
		return nil, fmt.Errorf("resolve api token credential: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf("api token credential %s not found", credID)
	}
	if cred.Kind != store.CredAPIToken {
		return nil, fmt.Errorf("credential %s is %s, expected api_token", credID, cred.Kind)
	}
	if tokenID, ok := cred.Config["token_id"].(string); ok && tokenID != "" {
		out["api_token_id"] = tokenID
	}
	if cred.VaultKey != nil {
		secret, ok, err := m.vault.Get(*cred.VaultKey)
		if err != nil {
			return nil, fmt.Errorf("vault get token secret: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("token secret not in vault for credential %s", credID)
		}
		out["api_token_secret"] = secret
	}
	return out, nil
}

// ResolveConfig returns the folder's provider config with token-style
// credential references inlined (api_token_id + api_token_secret), the
// same map the provider Fetch sees. Exposed so features outside the
// refresh loop (e.g. the VNC console, which needs the Proxmox base_url +
// API token to call vncproxy) can reuse one secret-resolution path
// instead of re-reading credentials. The vault must be unlocked.
func (m *Manager) ResolveConfig(folderID string) (map[string]any, error) {
	df, err := m.db.GetDynamicFolder(folderID)
	if err != nil {
		return nil, err
	}
	if df == nil {
		return nil, fmt.Errorf("dynamic folder %s not found", folderID)
	}
	return m.resolveSecrets(df.Config)
}

// filterFromConfig pulls the include/exclude lists out of the same
// config blob the provider reads. Keeps everything in one map for
// the UI; the provider ignores filter keys, the filter ignores
// provider keys.
func filterFromConfig(cfg map[string]any) Filter {
	f := Filter{}
	if v, ok := cfg["include_hosts"].(bool); ok && v {
		f.IncludeKinds = append(f.IncludeKinds, KindHost)
	}
	if v, ok := cfg["include_guests"].(bool); ok && v {
		f.IncludeKinds = append(f.IncludeKinds,
			KindGuestVM, KindGuestLXC)
	}
	if v, ok := cfg["tag_whitelist"].([]any); ok {
		for _, t := range v {
			if s, ok := t.(string); ok {
				f.TagWhitelist = append(f.TagWhitelist, s)
			}
		}
	}
	if v, ok := cfg["tag_blacklist"].([]any); ok {
		for _, t := range v {
			if s, ok := t.(string); ok {
				f.TagBlacklist = append(f.TagBlacklist, s)
			}
		}
	}
	if v, ok := cfg["hide_stopped"].(bool); ok {
		f.HideStopped = v
	}
	return f
}
