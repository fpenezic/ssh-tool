package main

// Network profiles: userspace WireGuard tunnels (internal/wg) that a
// connection or folder can select via the inheritable
// network_profile_id setting. The SSH layer's FirstHopDialerHook
// (wired in initialise) calls ensureWgTunnel on connect.
//
// Secret handling mirrors credentials: the DB row carries the
// secretless profile JSON, the vault holds the interface private key
// under wg_private_key:<id> and per-peer preshared keys under
// wg_psk:<id>:<peer_public_key>.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/inventory"
	"ssh-tool/internal/presence"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/wg"
)

// directProbeTimeout is how long ModeAuto waits for a direct TCP dial
// before deciding the host is only reachable through the tunnel.
const directProbeTimeout = 3 * time.Second

// Profile kinds. The stored config JSON carries "kind"; absent means
// wireguard (pre-netbird profiles have no field).
const (
	kindWireguard = "wireguard"
	kindNetbird   = "netbird"
	kindTailscale = "tailscale"
)

// NetbirdConfig is the stored (secretless) NetBird profile: the setup
// key lives in the referenced api_token credential, registration
// state under DataDir/netbird/<profileID>/.
type NetbirdConfig struct {
	Kind                 string `json:"kind"` // "netbird"
	ManagementURL        string `json:"management_url"`
	DeviceName           string `json:"device_name"`
	SetupKeyCredentialID string `json:"setup_key_credential_id"`
	Mode                 string `json:"mode,omitempty"`
	Paused               bool   `json:"paused,omitempty"`
}

// TailscaleConfig is the stored (secretless) Tailscale profile: the
// auth key lives in the referenced api_token credential, node state
// under DataDir/tailscale/<profileID>/. ControlURL is blank for
// Tailscale's own control plane, set for a self-hosted Headscale.
type TailscaleConfig struct {
	Kind              string `json:"kind"` // "tailscale"
	ControlURL        string `json:"control_url"`
	Hostname          string `json:"hostname"`
	AuthKeyCredentialID string `json:"auth_key_credential_id"`
	Mode              string `json:"mode,omitempty"`
	Paused            bool   `json:"paused,omitempty"`
}

// profilePolicy is the kind/mode/paused triple every profile kind
// shares; parsed from the config JSON without caring about the rest.
type profilePolicy struct {
	Kind   string `json:"kind"`
	Mode   string `json:"mode"`
	Paused bool   `json:"paused"`
}

func parsePolicy(configJSON string) profilePolicy {
	var p profilePolicy
	_ = json.Unmarshal([]byte(configJSON), &p)
	if p.Kind == "" {
		p.Kind = kindWireguard
	}
	return p
}

// patchPolicyJSON sets the mode/paused fields on a stored profile's
// config JSON without touching anything else. It decodes into a
// generic map so it is agnostic to the profile kind - a NetBird or
// Tailscale config keeps its "kind" and provider fields intact, where
// decoding through a typed wg.Profile would drop every field that
// struct doesn't declare. Used by the policy toggle (always/auto,
// pause), which applies to all three kinds.
func patchPolicyJSON(configJSON, mode string, paused bool) (string, error) {
	m := map[string]json.RawMessage{}
	if strings.TrimSpace(configJSON) != "" {
		if err := json.Unmarshal([]byte(configJSON), &m); err != nil {
			return "", err
		}
	}
	modeJSON, _ := json.Marshal(mode)
	pausedJSON, _ := json.Marshal(paused)
	m["mode"] = modeJSON
	m["paused"] = pausedJSON
	out, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// tunnelDialer is what both tunnel kinds hand the SSH layer.
type tunnelDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// ensureTunnel starts (or reuses) the profile's tunnel regardless of
// kind and leaves a wgTouch hold - see ensureWgTunnel for why.
func (a *App) ensureTunnel(row *store.NetworkProfile) (tunnelDialer, error) {
	switch parsePolicy(row.ConfigJSON).Kind {
	case kindNetbird:
		return a.ensureNetbirdTunnel(row)
	case kindTailscale:
		return a.ensureTailscaleTunnel(row)
	default:
		return a.ensureWgTunnel(row.ID)
	}
}

// tunnelStop stops whichever manager runs the profile's tunnel and
// releases its presence claim so other synced machines see it free.
func (a *App) tunnelStop(profileID string) {
	a.wgman.Stop(profileID)
	a.nbman.Stop(profileID)
	go a.presenceClear(profileID)
}

// tunnelStatus merges both managers' views into the wg.Status shape
// the UI renders.
func (a *App) tunnelStatus(profileID string) wg.Status {
	if st := a.wgman.Status(profileID); st.Running {
		return st
	}
	if hs := a.nbman.Status(profileID); hs.Running {
		return wg.Status{ProfileID: profileID, Running: true, StartedAt: hs.StartedAt, Peers: hs.Peers}
	}
	return wg.Status{ProfileID: profileID}
}

// netbirdStateDir is where the helper keeps a profile's registration
// state (device keys). Removed on profile delete.
func netbirdStateDir(profileID string) string {
	return filepath.Join(store.DataDir(), "netbird", profileID)
}

func tailscaleStateDir(profileID string) string {
	return filepath.Join(store.DataDir(), "tailscale", profileID)
}

// resolveHelperSecret reads the secret (NetBird setup key / Tailscale
// auth key) from a profile's referenced api_token credential and the
// vault. Shared by both helper kinds - the vault-state error messages
// are identical. profileName only feeds the error text. credID "" is a
// hard error (no credential assigned).
func (a *App) resolveHelperSecret(profileName, credID, secretLabel string) (string, error) {
	if credID == "" {
		return "", fmt.Errorf("profile %s: no %s credential assigned", profileName, secretLabel)
	}
	cred, err := a.db.GetCredential(credID)
	if err != nil {
		return "", fmt.Errorf("profile %s: %s credential: %w", profileName, secretLabel, err)
	}
	if cred == nil {
		return "", fmt.Errorf("profile %s: %s credential not found (was it deleted?)", profileName, secretLabel)
	}
	if cred.VaultKey == nil {
		return "", fmt.Errorf("profile %s: credential %q holds no secret in the vault - recreate it with the %s as the secret", profileName, cred.Name, secretLabel)
	}
	secret, ok, verr := a.vault.Get(*cred.VaultKey)
	if verr != nil {
		return "", fmt.Errorf("profile %s: %s: %w", profileName, secretLabel, verr)
	}
	if !ok {
		switch a.vault.Status().Kind {
		case creds.StatusLocked:
			return "", fmt.Errorf("profile %s: vault is locked - unlock it, then retry", profileName)
		case creds.StatusNotInitialized:
			return "", fmt.Errorf("profile %s: the %s was stored in a memory-only vault and lost on restart - set a master passphrase (Settings -> Vault), then recreate the credential", profileName, secretLabel)
		default:
			return "", fmt.Errorf("profile %s: %s not found in the vault - recreate the credential %q", profileName, secretLabel, cred.Name)
		}
	}
	if secret == "" {
		return "", fmt.Errorf("profile %s: %s credential holds an empty secret", profileName, secretLabel)
	}
	return secret, nil
}

// ensureTailscaleTunnel spawns (or reuses) the ssh-tool-tailscale helper
// for the profile. The auth key is resolved from the referenced
// api_token credential and passed via env; once registered, the state
// dir carries node credentials and a missing key is fine.
func (a *App) ensureTailscaleTunnel(row *store.NetworkProfile) (tunnelDialer, error) {
	if p := a.nbman.Get(row.ID); p != nil {
		a.wgTouch(row.ID)
		return p, nil
	}
	var cfg TailscaleConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("profile %s: bad config: %w", row.Name, err)
	}
	exe, ok := pluginPath("tailscale")
	if !ok {
		return nil, fmt.Errorf("Tailscale plugin is not installed - download it in Settings -> Network profiles")
	}
	authKey, err := a.resolveHelperSecret(row.Name, cfg.AuthKeyCredentialID, "auth-key")
	if err != nil {
		return nil, err
	}
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = defaultTailscaleHostname()
	}
	args := []string{"--state-dir", tailscaleStateDir(row.ID), "--hostname", hostname}
	if cfg.ControlURL != "" {
		args = append(args, "--control", cfg.ControlURL)
	}
	env := []string{"SSHTOOL_TS_AUTHKEY=" + authKey}
	p, err := a.nbman.Ensure(row.ID, row.Name, exe, args, env)
	if err != nil {
		return nil, err
	}
	a.wgTouch(row.ID)
	a.recordAudit("network.tunnel.start", row.ID, map[string]string{"name": row.Name, "kind": kindTailscale})
	go a.presencePublish(row.ID, kindTailscale)
	EventsEmit("network_tunnel_changed", row.ID)
	return p, nil
}

// normalizeMgmtURL tolerates a management URL typed without a scheme
// ("vpn.example.com" -> "https://vpn.example.com"). Empty stays empty
// (means the netbird.io cloud default). An explicit http:// is left
// as-is for local/dev setups.
func normalizeMgmtURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "https://" + u
	}
	return u
}

// ensureNetbirdTunnel spawns (or reuses) the ssh-tool-netbird helper
// for the profile. The setup key is resolved from the referenced
// api_token credential and passed via env; once registered, the state
// dir carries device credentials and a missing key is fine.
func (a *App) ensureNetbirdTunnel(row *store.NetworkProfile) (tunnelDialer, error) {
	if p := a.nbman.Get(row.ID); p != nil {
		a.wgTouch(row.ID)
		return p, nil
	}
	var cfg NetbirdConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("profile %s: bad config: %w", row.Name, err)
	}
	exe, ok := pluginPath("netbird")
	if !ok {
		return nil, fmt.Errorf("NetBird plugin is not installed - download it in Settings -> Network profiles")
	}
	// Resolve the setup key from the referenced api_token credential.
	// The helper runs a pure userspace peer (no persisted file config),
	// so the setup key is needed on every start; NetBird dedupes the
	// device by name + state, so re-registering with a reusable key is
	// the normal path.
	if cfg.SetupKeyCredentialID == "" {
		return nil, fmt.Errorf("profile %s: no setup-key credential assigned", row.Name)
	}
	cred, err := a.db.GetCredential(cfg.SetupKeyCredentialID)
	if err != nil {
		return nil, fmt.Errorf("profile %s: setup key credential: %w", row.Name, err)
	}
	if cred == nil {
		return nil, fmt.Errorf("profile %s: setup key credential not found (was it deleted?)", row.Name)
	}
	if cred.VaultKey == nil {
		return nil, fmt.Errorf("profile %s: credential %q holds no secret in the vault - recreate it with the setup key as the secret", row.Name, cred.Name)
	}
	setupKey, ok, verr := a.vault.Get(*cred.VaultKey)
	if verr != nil {
		return nil, fmt.Errorf("profile %s: setup key: %w", row.Name, verr)
	}
	if !ok {
		switch a.vault.Status().Kind {
		case creds.StatusLocked:
			return nil, fmt.Errorf("profile %s: vault is locked - unlock it, then retry", row.Name)
		case creds.StatusNotInitialized:
			return nil, fmt.Errorf("profile %s: the setup key was stored in a memory-only vault and lost on restart - set a master passphrase (Settings -> Vault), then recreate the credential", row.Name)
		default:
			return nil, fmt.Errorf("profile %s: setup key not found in the vault - recreate the credential %q", row.Name, cred.Name)
		}
	}
	if setupKey == "" {
		return nil, fmt.Errorf("profile %s: setup key credential holds an empty secret", row.Name)
	}
	device := cfg.DeviceName
	if device == "" {
		device = defaultNetbirdDeviceName()
	}
	args := []string{"--state-dir", netbirdStateDir(row.ID), "--device", device}
	if cfg.ManagementURL != "" {
		args = append(args, "--management", cfg.ManagementURL)
	}
	env := []string{"SSHTOOL_NB_SETUP_KEY=" + setupKey}
	p, err := a.nbman.Ensure(row.ID, row.Name, exe, args, env)
	if err != nil {
		return nil, err
	}
	a.wgTouch(row.ID)
	a.recordAudit("network.tunnel.start", row.ID, map[string]string{"name": row.Name, "kind": kindNetbird})
	go a.presencePublish(row.ID, kindNetbird)
	EventsEmit("network_tunnel_changed", row.ID)
	return p, nil
}

// wgDialerFor implements the per-profile connect policy and returns
// the dialer the SSH layer should use for the first hop:
//
//	paused      -> plain direct dial (tunnel never starts)
//	mode auto   -> direct probe first, tunnel fallback
//	mode always -> tunnel, errors surface
func (a *App) wgDialerFor(profileID string) (sshlayer.ContextDialer, error) {
	row, err := a.db.GetNetworkProfile(profileID)
	if err != nil {
		return nil, err
	}
	pol := parsePolicy(row.ConfigJSON)

	// The SSH layer passes a *string under DialPathKey so the UI can
	// show which transport the session ACTUALLY used - "direct" when
	// an auto/paused policy skipped the tunnel.
	report := func(ctx context.Context, path string) {
		if h, ok := ctx.Value(sshlayer.DialPathKey{}).(*string); ok {
			*h = path
		}
	}
	direct := func(ctx context.Context, network, addr string) (net.Conn, error) {
		report(ctx, "direct")
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	if pol.Paused {
		return direct, nil
	}
	if pol.Mode == wg.ModeAuto {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			pctx, cancel := context.WithTimeout(ctx, directProbeTimeout)
			c, derr := direct(pctx, network, addr)
			cancel()
			if derr == nil {
				log.Printf("net: %s reachable directly, skipping tunnel %s", addr, row.Name)
				return c, nil
			}
			t, terr := a.ensureTunnel(row)
			if terr != nil {
				return nil, fmt.Errorf("direct dial failed (%v) and tunnel failed: %w", derr, terr)
			}
			log.Printf("net: %s not reachable directly, dialing via tunnel %s", addr, row.Name)
			report(ctx, "tunnel")
			return t.DialContext(ctx, network, addr)
		}, nil
	}
	t, err := a.ensureTunnel(row)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		report(ctx, "tunnel")
		return t.DialContext(ctx, network, addr)
	}, nil
}

// wgBackgroundDialerFor is the timer-refresh variant: it NEVER starts
// a tunnel. Paused -> direct; tunnel already up (someone is working
// through it) -> use it; auto mode -> direct only, no tunnel
// fallback; otherwise inventory.ErrTunnelWaiting so the refresh is
// skipped as an expected idle state.
func (a *App) wgBackgroundDialerFor(profileID string) (sshlayer.ContextDialer, error) {
	row, err := a.db.GetNetworkProfile(profileID)
	if err != nil {
		return nil, err
	}
	pol := parsePolicy(row.ConfigJSON)

	direct := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	if pol.Paused {
		return direct, nil
	}
	// Ride an already-running tunnel of either kind, but never START
	// one for a background poll.
	if t := a.wgman.Get(profileID); t != nil {
		a.wgTouch(profileID)
		return t.DialContext, nil
	}
	if p := a.nbman.Get(profileID); p != nil {
		a.wgTouch(profileID)
		return p.DialContext, nil
	}
	if pol.Mode == wg.ModeAuto {
		return direct, nil
	}
	return nil, inventory.ErrTunnelWaiting
}

func wgPrivateKeyVaultKey(profileID string) string {
	return "wg_private_key:" + profileID
}

func wgPSKVaultKey(profileID, peerPublicKey string) string {
	return "wg_psk:" + profileID + ":" + peerPublicKey
}

// loadWgProfile reads the stored profile and re-attaches its secrets
// from the vault. Fails when the vault is locked - same UX as a
// stored password: unlock first.
func (a *App) loadWgProfile(profileID string) (*wg.Profile, error) {
	row, err := a.db.GetNetworkProfile(profileID)
	if err != nil {
		return nil, err
	}
	var p wg.Profile
	if err := json.Unmarshal([]byte(row.ConfigJSON), &p); err != nil {
		return nil, fmt.Errorf("profile %s: bad config: %w", row.Name, err)
	}
	p.ID = row.ID
	p.Name = row.Name
	priv, ok, err := a.vault.Get(wgPrivateKeyVaultKey(row.ID))
	if err != nil {
		return nil, fmt.Errorf("profile %s: private key: %w (vault locked?)", row.Name, err)
	}
	if !ok {
		return nil, fmt.Errorf("profile %s: private key missing from vault", row.Name)
	}
	p.PrivateKey = priv
	for i := range p.Peers {
		if !p.Peers[i].HasPSK {
			continue
		}
		psk, ok, err := a.vault.Get(wgPSKVaultKey(row.ID, p.Peers[i].PublicKey))
		if err != nil {
			return nil, fmt.Errorf("profile %s: preshared key: %w", row.Name, err)
		}
		if ok {
			p.Peers[i].PresharedKey = psk
		}
	}
	return &p, nil
}

// ensureWgTunnel returns the running tunnel for the profile, starting
// it (vault secrets + userspace device) when needed. Every caller
// leaves a short wgTouch hold so tunnels whose user never registers a
// session (inventory fetch, connect that fails auth, manual test)
// still flow into the idle-stop accounting instead of running forever.
func (a *App) ensureWgTunnel(profileID string) (*wg.Tunnel, error) {
	if t := a.wgman.Get(profileID); t != nil {
		a.wgTouch(profileID)
		return t, nil
	}
	// Guard against flapping a synced WG identity: if another machine
	// owns this tunnel and the user hasn't authorised a take-over,
	// refuse with a recognisable error so the UI can offer one. The
	// error carries "<profileID>|<ownerName>" after the prefix so the
	// frontend take-over dialog has both without a resolve round-trip.
	if owner, blocked := a.presenceBlocksBringUp(profileID); blocked {
		return nil, fmt.Errorf("%s%s|%s", errProfileBusyPrefix, profileID, owner)
	}
	p, err := a.loadWgProfile(profileID)
	if err != nil {
		return nil, err
	}
	t, err := a.wgman.Ensure(p)
	if err != nil {
		return nil, err
	}
	a.wgTouch(profileID)
	a.recordAudit("network.tunnel.start", profileID, map[string]string{"name": p.Name})
	// Claim presence for this WG profile across synced machines.
	go a.presencePublish(profileID, kindWireguard)
	EventsEmit("network_tunnel_changed", profileID)
	return t, nil
}

// storeWgProfile strips secrets into the vault and persists the rest.
// Used by both create and update.
func (a *App) storeWgSecrets(profileID string, p *wg.Profile) error {
	if p.PrivateKey != "" {
		if err := a.vault.Put(wgPrivateKeyVaultKey(profileID), p.PrivateKey); err != nil {
			return fmt.Errorf("store private key: %w (vault locked?)", err)
		}
	}
	for _, peer := range p.Peers {
		if peer.PresharedKey == "" {
			continue
		}
		if err := a.vault.Put(wgPSKVaultKey(profileID, peer.PublicKey), peer.PresharedKey); err != nil {
			return fmt.Errorf("store preshared key: %w", err)
		}
	}
	return nil
}

// secretlessJSON serializes the profile for the DB row. PrivateKey /
// PresharedKey have `json:"-"` on the struct, so a plain Marshal is
// already secretless; this keeps that guarantee in one named place.
func secretlessJSON(p *wg.Profile) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NetworkProfileInfo is the list shape for the UI: the stored row plus
// the parsed secretless config for display. Kind selects which of
// Profile (WireGuard) / Netbird is populated.
type NetworkProfileInfo struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string           `json:"kind"` // "wireguard" | "netbird" | "tailscale"
	Mode      string           `json:"mode"`
	Paused    bool             `json:"paused"`
	Profile   wg.Profile       `json:"profile"`             // WG only
	Netbird   *NetbirdConfig   `json:"netbird,omitempty"`   // NetBird only
	Tailscale *TailscaleConfig `json:"tailscale,omitempty"` // Tailscale only
	Status    wg.Status        `json:"status"`
	CreatedAt int64          `json:"created_at"`
	UpdatedAt int64          `json:"updated_at"`
}

func (a *App) infoFor(row store.NetworkProfile) NetworkProfileInfo {
	pol := parsePolicy(row.ConfigJSON)
	info := NetworkProfileInfo{
		ID: row.ID, Name: row.Name,
		Kind: pol.Kind, Mode: pol.Mode, Paused: pol.Paused,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Status: a.tunnelStatus(row.ID),
	}
	switch pol.Kind {
	case kindNetbird:
		var nb NetbirdConfig
		_ = json.Unmarshal([]byte(row.ConfigJSON), &nb)
		info.Netbird = &nb
	case kindTailscale:
		var ts TailscaleConfig
		_ = json.Unmarshal([]byte(row.ConfigJSON), &ts)
		info.Tailscale = &ts
	default:
		_ = json.Unmarshal([]byte(row.ConfigJSON), &info.Profile)
		info.Profile.ID = row.ID
		info.Profile.Name = row.Name
	}
	return info
}

// NetworkProfilesList returns every stored profile with live status.
func (a *App) NetworkProfilesList() ([]NetworkProfileInfo, error) {
	rows, err := a.db.ListNetworkProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]NetworkProfileInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.infoFor(r))
	}
	return out, nil
}

// NetworkProfileCreate parses a wg-quick conf, stores its secrets in
// the vault and the rest in the DB.
func (a *App) NetworkProfileCreate(name, confText string) (*NetworkProfileInfo, error) {
	p, err := wg.ParseConf(confText)
	if err != nil {
		return nil, err
	}
	if p.PrivateKey == "" || p.PrivateKey == wg.KeepSecret {
		return nil, fmt.Errorf("config has no PrivateKey")
	}
	cfg, err := secretlessJSON(p)
	if err != nil {
		return nil, err
	}
	row, err := a.db.CreateNetworkProfile(name, cfg)
	if err != nil {
		return nil, err
	}
	if err := a.storeWgSecrets(row.ID, p); err != nil {
		// Don't keep a profile whose secrets never made it to the vault.
		_ = a.db.DeleteNetworkProfile(row.ID)
		return nil, err
	}
	a.recordAudit("network.profile.create", row.ID, map[string]string{"name": name})
	info := a.infoFor(*row)
	return &info, nil
}

// NetworkProfileCreateNetbird stores a NetBird profile: management
// URL, device name and a reference to the api_token credential that
// holds the setup key. Nothing secret lives on the row. Requires the
// NetBird plugin so the config can't be created and then fail to run
// with no explanation.
// defaultNetbirdDeviceName derives a NetBird peer name from this
// machine's hostname plus a ".ssh-tool" suffix, so a peer registered by
// this app is recognisable in the NetBird dashboard and distinct per
// machine. Sanitised to the character set NetBird accepts for a device
// name (alphanumerics, hyphen, dot): whitespace and anything else
// collapses to a hyphen. Falls back to a bare "ssh-tool" if the
// hostname is empty or sanitises to nothing.
func defaultNetbirdDeviceName() string {
	return netbirdDeviceNameFrom(machineName())
}

func netbirdDeviceNameFrom(host string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(host) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-.")
	if base == "" {
		return "ssh-tool"
	}
	return base + ".ssh-tool"
}

// SuggestNetbirdDeviceName is the default the create form pre-fills:
// "<hostname>.ssh-tool". The user can still edit it before saving.
func (a *App) SuggestNetbirdDeviceName() string {
	return defaultNetbirdDeviceName()
}

// defaultTailscaleHostname derives a tailnet node name from this
// machine's hostname. Sanitised to the Tailscale host label charset
// (alphanumerics + hyphen only, max 63; NO dot - Tailscale reserves it
// for the MagicDNS FQDN and would rewrite it to a hyphen anyway). No
// ".ssh-tool" suffix for the same reason. Falls back to "ssh-tool".
func defaultTailscaleHostname() string {
	return tailscaleHostnameFrom(machineName())
}

func tailscaleHostnameFrom(host string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(host) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		return "ssh-tool"
	}
	if len(base) > 63 {
		base = strings.Trim(base[:63], "-")
	}
	return base
}

// SuggestTailscaleHostname is the default the create form pre-fills.
func (a *App) SuggestTailscaleHostname() string {
	return defaultTailscaleHostname()
}

func (a *App) NetworkProfileCreateNetbird(name, managementURL, deviceName, setupKeyCredentialID string) (*NetworkProfileInfo, error) {
	if _, ok := pluginPath("netbird"); !ok {
		return nil, fmt.Errorf("NetBird plugin is not installed - download it first")
	}
	if setupKeyCredentialID == "" {
		return nil, fmt.Errorf("a setup-key credential is required")
	}
	cfg := NetbirdConfig{
		Kind:                 kindNetbird,
		ManagementURL:        normalizeMgmtURL(managementURL),
		DeviceName:           deviceName,
		SetupKeyCredentialID: setupKeyCredentialID,
		Mode:                 wg.ModeAlways,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	row, err := a.db.CreateNetworkProfile(name, string(b))
	if err != nil {
		return nil, err
	}
	a.recordAudit("network.profile.create", row.ID, map[string]string{"name": name, "kind": kindNetbird})
	info := a.infoFor(*row)
	return &info, nil
}

// NetworkProfileUpdateNetbird edits a NetBird profile's fields,
// preserving its policy (mode/paused). Restarts the helper so the new
// config takes effect.
func (a *App) NetworkProfileUpdateNetbird(id, name, managementURL, deviceName, setupKeyCredentialID string) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	var cfg NetbirdConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	cfg.Kind = kindNetbird
	cfg.ManagementURL = normalizeMgmtURL(managementURL)
	cfg.DeviceName = deviceName
	if setupKeyCredentialID != "" {
		cfg.SetupKeyCredentialID = setupKeyCredentialID
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	updated, err := a.db.UpdateNetworkProfile(id, name, string(b))
	if err != nil {
		return nil, err
	}
	a.tunnelStop(id)
	a.recordAudit("network.profile.update", id, map[string]string{"name": name, "kind": kindNetbird})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// NetworkProfileCreateTailscale stores a Tailscale profile: control URL
// (blank for Tailscale's own), hostname, and a reference to the
// api_token credential that holds the auth key. Nothing secret lives on
// the row. Requires the Tailscale plugin so the config can't be created
// and then fail to run with no explanation.
func (a *App) NetworkProfileCreateTailscale(name, controlURL, hostname, authKeyCredentialID string) (*NetworkProfileInfo, error) {
	if _, ok := pluginPath("tailscale"); !ok {
		return nil, fmt.Errorf("Tailscale plugin is not installed - download it first")
	}
	if authKeyCredentialID == "" {
		return nil, fmt.Errorf("an auth-key credential is required")
	}
	cfg := TailscaleConfig{
		Kind:                kindTailscale,
		ControlURL:          normalizeMgmtURL(controlURL),
		Hostname:            hostname,
		AuthKeyCredentialID: authKeyCredentialID,
		Mode:                wg.ModeAlways,
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	row, err := a.db.CreateNetworkProfile(name, string(b))
	if err != nil {
		return nil, err
	}
	a.recordAudit("network.profile.create", row.ID, map[string]string{"name": name, "kind": kindTailscale})
	info := a.infoFor(*row)
	return &info, nil
}

// NetworkProfileUpdateTailscale edits a Tailscale profile's fields,
// preserving its policy (mode/paused). Restarts the helper so the new
// config takes effect.
func (a *App) NetworkProfileUpdateTailscale(id, name, controlURL, hostname, authKeyCredentialID string) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	var cfg TailscaleConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	cfg.Kind = kindTailscale
	cfg.ControlURL = normalizeMgmtURL(controlURL)
	cfg.Hostname = hostname
	if authKeyCredentialID != "" {
		cfg.AuthKeyCredentialID = authKeyCredentialID
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	updated, err := a.db.UpdateNetworkProfile(id, name, string(b))
	if err != nil {
		return nil, err
	}
	a.tunnelStop(id)
	a.recordAudit("network.profile.update", id, map[string]string{"name": name, "kind": kindTailscale})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// NetworkProfileUpdate renames a profile and, when confText is
// non-empty, replaces its whole configuration (and vault secrets).
// A running tunnel is stopped so the next connect uses the new config.
func (a *App) NetworkProfileUpdate(id, name, confText string) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	cfg := row.ConfigJSON
	if confText != "" {
		p, err := wg.ParseConf(confText)
		if err != nil {
			return nil, err
		}
		if p.PrivateKey == "" {
			return nil, fmt.Errorf("config has no PrivateKey")
		}
		// The edit UI renders vault-held secrets as the KeepSecret
		// placeholder - translate that back to "leave the stored
		// value alone" (storeWgSecrets skips empties).
		if p.PrivateKey == wg.KeepSecret {
			p.PrivateKey = ""
		}
		for i := range p.Peers {
			if p.Peers[i].PresharedKey == wg.KeepSecret {
				p.Peers[i].PresharedKey = ""
				p.Peers[i].HasPSK = true
			}
		}
		// The conf text carries no policy fields - keep the ones the
		// user already set on this profile.
		var old wg.Profile
		_ = json.Unmarshal([]byte(row.ConfigJSON), &old)
		p.Mode = old.Mode
		p.Paused = old.Paused
		if cfg, err = secretlessJSON(p); err != nil {
			return nil, err
		}
		if err := a.storeWgSecrets(id, p); err != nil {
			return nil, err
		}
	}
	updated, err := a.db.UpdateNetworkProfile(id, name, cfg)
	if err != nil {
		return nil, err
	}
	a.tunnelStop(id)
	a.recordAudit("network.profile.update", id, map[string]string{"name": name, "config_replaced": fmt.Sprintf("%t", confText != "")})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// NetworkProfileRenderConf renders the stored profile back to
// wg-quick text for the edit form. Secrets come out as the
// wg.KeepSecret placeholder; NetworkProfileUpdate translates that
// back to "keep the vault value".
func (a *App) NetworkProfileRenderConf(id string) (string, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return "", err
	}
	var p wg.Profile
	if err := json.Unmarshal([]byte(row.ConfigJSON), &p); err != nil {
		return "", fmt.Errorf("bad config: %w", err)
	}
	return p.RenderConf(), nil
}

// NetworkProfileDelete stops the tunnel and removes the row + vault
// secrets. Connections still referencing the id fail to connect with
// "not found" - visible, not silent.
func (a *App) NetworkProfileDelete(id string) error {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return err
	}
	a.tunnelStop(id)
	// WG secrets live in the vault keyed by profile; NetBird/Tailscale
	// keep their registration state on disk. Clean up whichever this
	// profile has.
	switch parsePolicy(row.ConfigJSON).Kind {
	case kindNetbird:
		_ = os.RemoveAll(netbirdStateDir(id))
	case kindTailscale:
		_ = os.RemoveAll(tailscaleStateDir(id))
	default:
		var p wg.Profile
		_ = json.Unmarshal([]byte(row.ConfigJSON), &p)
		_ = a.vault.Delete(wgPrivateKeyVaultKey(id))
		for _, peer := range p.Peers {
			if peer.HasPSK {
				_ = a.vault.Delete(wgPSKVaultKey(id, peer.PublicKey))
			}
		}
	}
	if err := a.db.DeleteNetworkProfile(id); err != nil {
		return err
	}
	a.recordAudit("network.profile.delete", id, map[string]string{"name": row.Name})
	EventsEmit("network_tunnel_changed", id)
	return nil
}

// NetworkProfileStop tears down a running tunnel (connections dialed
// through it drop, like killing a session).
func (a *App) NetworkProfileStop(id string) {
	a.tunnelStop(id)
	EventsEmit("network_tunnel_changed", id)
}

// NetworkProfileSetPolicy updates the connect policy: mode is
// "always" or "auto"; paused is the per-profile kill switch (dial
// direct, don't start the tunnel). Pausing also stops a running
// tunnel so the switch takes effect immediately.
func (a *App) NetworkProfileSetPolicy(id, mode string, paused bool) (*NetworkProfileInfo, error) {
	if mode != wg.ModeAlways && mode != wg.ModeAuto {
		return nil, fmt.Errorf("mode must be %q or %q", wg.ModeAlways, wg.ModeAuto)
	}
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	// Patch only mode/paused in the raw config JSON, preserving the
	// kind field and every provider-specific field. Unmarshalling into
	// a concrete wg.Profile (which has no Kind field) and re-marshalling
	// would SILENTLY DROP the "kind" - turning a NetBird / Tailscale
	// profile into a broken WireGuard one on every policy toggle. That
	// is the bug this map-patch avoids; it is kind-agnostic by design.
	cfg, err := patchPolicyJSON(row.ConfigJSON, mode, paused)
	if err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	updated, err := a.db.UpdateNetworkProfile(id, row.Name, cfg)
	if err != nil {
		return nil, err
	}
	if paused {
		a.tunnelStop(id)
	}
	a.recordAudit("network.profile.policy", id, map[string]string{"mode": mode, "paused": fmt.Sprintf("%t", paused)})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// ----- Tunnel lifecycle: stop when the last session using it closes -----

// wgLinger is how long an idle tunnel stays up after its last SSH
// session closes. Long enough to cover a quick disconnect/reconnect
// cycle without paying the tunnel setup again; short enough that the
// network path doesn't sit open all day unused.
const wgLinger = 2 * time.Minute

// wgTouch places a short-lived hold on a profile's tunnel: an
// anonymous acquire that self-releases after 30s. Every tunnel dial
// that has no session lifecycle of its own (inventory fetch, failed
// SSH attempt, manual test) goes through this, so such tunnels still
// reach wgRelease's idle-stop path instead of staying up forever.
func (a *App) wgTouch(profileID string) {
	key := fmt.Sprintf("touch:%s:%d", profileID, time.Now().UnixNano())
	a.wgAcquire(profileID, key)
	time.AfterFunc(30*time.Second, func() { a.wgRelease(key) })
}

// wgAcquire marks a session as using a profile's tunnel and cancels
// any pending idle stop.
func (a *App) wgAcquire(profileID, sessionID string) {
	a.wgSessMu.Lock()
	defer a.wgSessMu.Unlock()
	if a.wgSessProfile == nil {
		a.wgSessProfile = map[string]string{}
	}
	a.wgSessProfile[sessionID] = profileID
	if t := a.wgStopTimers[profileID]; t != nil {
		t.Stop()
		delete(a.wgStopTimers, profileID)
	}
}

// wgRelease drops a session's claim; when it was the profile's last
// one, schedule the idle stop. Safe to call for sessions that never
// used a tunnel (no-op).
func (a *App) wgRelease(sessionID string) {
	a.wgSessMu.Lock()
	defer a.wgSessMu.Unlock()
	pid, ok := a.wgSessProfile[sessionID]
	if !ok {
		return
	}
	delete(a.wgSessProfile, sessionID)
	for _, p := range a.wgSessProfile {
		if p == pid {
			return // still in use
		}
	}
	if a.wgStopTimers == nil {
		a.wgStopTimers = map[string]*time.Timer{}
	}
	if t := a.wgStopTimers[pid]; t != nil {
		t.Stop()
	}
	a.wgStopTimers[pid] = time.AfterFunc(wgLinger, func() {
		a.wgSessMu.Lock()
		delete(a.wgStopTimers, pid)
		for _, p := range a.wgSessProfile {
			if p == pid {
				a.wgSessMu.Unlock()
				return // reacquired while lingering
			}
		}
		a.wgSessMu.Unlock()
		name := pid
		if t := a.wgman.Get(pid); t != nil {
			name = t.Name
		}
		log.Printf("wg: tunnel %s idle for %s, stopping", name, wgLinger)
		a.tunnelStop(pid)
		EventsEmit("network_tunnel_changed", pid)
	})
}

// wgTrackSession wires the indicator + lifecycle for a freshly
// connected session: returns the profile display name when the
// session dialed through a tunnel (and registers it for idle-stop
// accounting), "" otherwise.
func (a *App) wgTrackSession(sess *sshlayer.Session, settings *store.ResolvedSettings) string {
	if settings.NetworkProfileID == nil || sess.NetworkVia != "tunnel" {
		return ""
	}
	pid := *settings.NetworkProfileID
	a.wgAcquire(pid, sess.ID)
	if row, err := a.db.GetNetworkProfile(pid); err == nil {
		return row.Name
	}
	return pid
}

// stopTunnelsOnQuit tears down every running tunnel on app exit. WG
// devices die with the process anyway, but the NetBird helper
// processes are separate: closing them cleanly lets the peer
// deregister and avoids orphaned children. Best-effort, bounded by
// each manager's own stop timeouts.
func (a *App) stopTunnelsOnQuit() {
	// Release presence for every owned profile first, so another
	// machine sees them free promptly instead of waiting for the
	// records to go stale.
	a.presence.mu.Lock()
	owned := make([]string, 0, len(a.presence.owned))
	for pid := range a.presence.owned {
		owned = append(owned, pid)
	}
	a.presence.mu.Unlock()
	if len(owned) > 0 {
		self := a.machineID()
		a.presenceLoadSave(func(f *presence.File) {
			for _, pid := range owned {
				f.ClearOwner(pid, self)
			}
		})
	}
	a.stopPresencePoll()

	if a.wgman != nil {
		a.wgman.StopAll()
	}
	if a.nbman != nil {
		a.nbman.StopAll()
	}
}

// NetworkProfileTest brings the tunnel up and reports its status - a
// cheap "does this config load / can we register" check, for either
// kind. It does not verify a WG peer answers (WireGuard is silent
// until traffic flows); for NetBird a successful helper start already
// means registration + management sync worked.
func (a *App) NetworkProfileTest(id string) (*wg.Status, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	if _, err := a.ensureTunnel(row); err != nil {
		return nil, err
	}
	st := a.tunnelStatus(id)
	return &st, nil
}
