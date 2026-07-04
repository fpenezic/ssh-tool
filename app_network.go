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
	"time"

	"ssh-tool/internal/store"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/wg"
)

// directProbeTimeout is how long ModeAuto waits for a direct TCP dial
// before deciding the host is only reachable through the tunnel.
const directProbeTimeout = 3 * time.Second

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
	var p wg.Profile
	_ = json.Unmarshal([]byte(row.ConfigJSON), &p)

	direct := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	if p.Paused {
		return direct, nil
	}
	if p.Mode == wg.ModeAuto {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			pctx, cancel := context.WithTimeout(ctx, directProbeTimeout)
			c, derr := direct(pctx, network, addr)
			cancel()
			if derr == nil {
				log.Printf("wg: %s reachable directly, skipping tunnel %s", addr, row.Name)
				return c, nil
			}
			t, terr := a.ensureWgTunnel(profileID)
			if terr != nil {
				return nil, fmt.Errorf("direct dial failed (%v) and tunnel failed: %w", derr, terr)
			}
			log.Printf("wg: %s not reachable directly, dialing via tunnel %s", addr, row.Name)
			return t.DialContext(ctx, network, addr)
		}, nil
	}
	t, err := a.ensureWgTunnel(profileID)
	if err != nil {
		return nil, err
	}
	return t.DialContext, nil
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
// it (vault secrets + userspace device) when needed.
func (a *App) ensureWgTunnel(profileID string) (*wg.Tunnel, error) {
	if t := a.wgman.Get(profileID); t != nil {
		return t, nil
	}
	p, err := a.loadWgProfile(profileID)
	if err != nil {
		return nil, err
	}
	t, err := a.wgman.Ensure(p)
	if err != nil {
		return nil, err
	}
	a.recordAudit("network.tunnel.start", profileID, map[string]string{"name": p.Name})
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

// NetworkProfileInfo is the list shape for the UI: the stored row
// plus the parsed secretless profile for display.
type NetworkProfileInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Profile   wg.Profile `json:"profile"`
	Status    wg.Status  `json:"status"`
	CreatedAt int64      `json:"created_at"`
	UpdatedAt int64      `json:"updated_at"`
}

func (a *App) infoFor(row store.NetworkProfile) NetworkProfileInfo {
	info := NetworkProfileInfo{
		ID: row.ID, Name: row.Name,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Status: a.wgman.Status(row.ID),
	}
	_ = json.Unmarshal([]byte(row.ConfigJSON), &info.Profile)
	info.Profile.ID = row.ID
	info.Profile.Name = row.Name
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
	if p.PrivateKey == "" {
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
	a.wgman.Stop(id)
	a.recordAudit("network.profile.update", id, map[string]string{"name": name, "config_replaced": fmt.Sprintf("%t", confText != "")})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// NetworkProfileDelete stops the tunnel and removes the row + vault
// secrets. Connections still referencing the id fail to connect with
// "not found" - visible, not silent.
func (a *App) NetworkProfileDelete(id string) error {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return err
	}
	a.wgman.Stop(id)
	var p wg.Profile
	_ = json.Unmarshal([]byte(row.ConfigJSON), &p)
	_ = a.vault.Delete(wgPrivateKeyVaultKey(id))
	for _, peer := range p.Peers {
		if peer.HasPSK {
			_ = a.vault.Delete(wgPSKVaultKey(id, peer.PublicKey))
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
	a.wgman.Stop(id)
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
	var p wg.Profile
	if err := json.Unmarshal([]byte(row.ConfigJSON), &p); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	p.Mode = mode
	p.Paused = paused
	cfg, err := secretlessJSON(&p)
	if err != nil {
		return nil, err
	}
	updated, err := a.db.UpdateNetworkProfile(id, row.Name, cfg)
	if err != nil {
		return nil, err
	}
	if paused {
		a.wgman.Stop(id)
	}
	a.recordAudit("network.profile.policy", id, map[string]string{"mode": mode, "paused": fmt.Sprintf("%t", paused)})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// NetworkProfileTest brings the tunnel up (vault + device) and
// reports its status - a cheap "does this config load" check. It does
// not verify the peer answers (WireGuard is silent until traffic
// flows); LastHandshake stays 0 until the first real dial.
func (a *App) NetworkProfileTest(id string) (*wg.Status, error) {
	if _, err := a.ensureWgTunnel(id); err != nil {
		return nil, err
	}
	st := a.wgman.Status(id)
	return &st, nil
}
