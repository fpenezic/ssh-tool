package main

// Check Point network profiles: a Check Point remote access gateway (the
// SSL Network Extender tunnel that Endpoint Security VPN and snx use),
// run by the ssh-tool-checkpoint plugin (separate repo, AGPL-3.0: a Go
// client based on snx-rs; the app talks to it only as a process)
// in userspace and reached through its SOCKS5 proxy, like OpenConnect and
// OpenVPN. The login is username + password and whatever further factor
// the gateway asks (a one-time code), relayed as auth events and answered
// by loginFiller. Check Point gateways mostly present a certificate from
// their own internal CA, so the first connect usually asks to trust it
// (cert_pin, the same flow as OpenConnect).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
)

const kindCheckpoint = "checkpoint"

// CheckpointConfig is the stored (secretless) Check Point profile.
type CheckpointConfig struct {
	Kind string `json:"kind"` // "checkpoint"
	// Server is the gateway, host or host:port.
	Server string `json:"server"`
	// LoginOption is the gateway's login option id ("" = its username +
	// password one, vpn_Username_Password).
	LoginOption string `json:"login_option,omitempty"`
	// Pins are the trusted gateway certificates (pin-sha256:...), set by the
	// trust-on-first-use prompt. Empty = the system trust store decides.
	Pins []string `json:"pins,omitempty"`

	// Login, same sources as OpenVPN: Username ("" = the credential's
	// default username, else asked), PasswordCredentialID ("" = asked).
	Username             string `json:"username,omitempty"`
	PasswordCredentialID string `json:"password_credential_id,omitempty"`
	// SecondPassword answers a further factor the gateway asks for (a
	// one-time code): ocSecondPrompt / ocSecondTOTP / ocSecondCredential.
	SecondPassword     string `json:"second_password,omitempty"`
	SecondCredentialID string `json:"second_credential_id,omitempty"`

	Mode   string `json:"mode,omitempty"`
	Paused bool   `json:"paused,omitempty"`
}

// CheckpointProfileInput is the create / update payload.
type CheckpointProfileInput struct {
	Name   string           `json:"name"`
	Config CheckpointConfig `json:"config"`
}

func (c *CheckpointConfig) normalize() error {
	c.Kind = kindCheckpoint
	s := strings.TrimSpace(c.Server)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i] // a pasted portal URL: the gateway is its host
	}
	if s == "" {
		return errors.New("gateway address is required")
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		if n, err := strconv.Atoi(p); h == "" || err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("gateway address %q is not host or host:port", s)
		}
	} else if strings.ContainsAny(s, " :") {
		return fmt.Errorf("gateway address %q is not host or host:port", s)
	}
	c.Server = s
	c.LoginOption = strings.TrimSpace(c.LoginOption)
	c.Username = strings.TrimSpace(c.Username)
	switch c.SecondPassword {
	case "":
		c.SecondPassword = ocSecondPrompt
	case ocSecondPrompt, ocSecondCredential, ocSecondTOTP:
	default:
		return fmt.Errorf("unknown one-time code source %q", c.SecondPassword)
	}
	if c.SecondPassword != ocSecondPrompt && c.SecondCredentialID == "" {
		return errors.New("pick the credential for the one-time code")
	}
	if c.SecondPassword == ocSecondPrompt {
		c.SecondCredentialID = ""
	}
	return nil
}

// hostPort splits the gateway for the prompts (443 when absent).
func (c *CheckpointConfig) hostPort() (string, int) {
	if h, p, err := net.SplitHostPort(c.Server); err == nil {
		n, _ := strconv.Atoi(p)
		return h, n
	}
	return c.Server, 443
}

// NetworkProfileCreateCheckpoint stores a Check Point profile.
func (a *App) NetworkProfileCreateCheckpoint(in CheckpointProfileInput) (*NetworkProfileInfo, error) {
	if _, ok := pluginPath(kindCheckpoint); !ok {
		return nil, errors.New("Check Point plugin is not installed - download it first")
	}
	cfg := in.Config
	cfg.Pins, cfg.Mode, cfg.Paused = nil, "", false
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	row, err := a.db.CreateNetworkProfile(in.Name, string(b))
	if err != nil {
		return nil, err
	}
	a.recordAudit("network.profile.create", row.ID, map[string]string{"name": in.Name, "kind": kindCheckpoint})
	info := a.infoFor(*row)
	return &info, nil
}

// NetworkProfileUpdateCheckpoint edits a Check Point profile, keeping its
// policy and trusted certificate (unless the gateway address changed).
// Restarts the helper so the new settings take effect.
func (a *App) NetworkProfileUpdateCheckpoint(id string, in CheckpointProfileInput) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	var old CheckpointConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &old); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	cfg := in.Config
	cfg.Mode, cfg.Paused = old.Mode, old.Paused
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	cfg.Pins = old.Pins
	if cfg.Server != old.Server {
		cfg.Pins = nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	updated, err := a.db.UpdateNetworkProfile(id, in.Name, carryIdleStop(row.ConfigJSON, string(b)))
	if err != nil {
		return nil, err
	}
	a.tunnelStop(id)
	a.recordAudit("network.profile.update", id, map[string]string{"name": in.Name, "kind": kindCheckpoint})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

func cpArgs(cfg *CheckpointConfig, pins []string) []string {
	args := []string{"--server", cfg.Server}
	if cfg.LoginOption != "" {
		args = append(args, "--login-option", cfg.LoginOption)
	}
	for _, p := range pins {
		args = append(args, "--pin", p)
	}
	return args
}

// ensureCheckpointTunnel spawns (or reuses) the Check Point helper for the
// profile. An untrusted or changed gateway certificate asks the user once
// and, when accepted, retries with that certificate pinned.
func (a *App) ensureCheckpointTunnel(row *store.NetworkProfile) (tunnelDialer, error) {
	if p := a.nbman.Get(row.ID); p != nil {
		a.wgTouch(row.ID)
		return p, nil
	}
	// One session per user on the gateway: a second login from another
	// synced machine drops the first. Same take-over guard as OpenConnect.
	if owner, blocked := a.presenceBlocksBringUp(row.ID); blocked {
		return nil, fmt.Errorf("%s%s|%s", errProfileBusyPrefix, row.ID, owner)
	}
	var cfg CheckpointConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("profile %s: bad config: %w", row.Name, err)
	}
	exe, ok := pluginPath(kindCheckpoint)
	if !ok {
		return nil, errors.New("Check Point plugin is not installed - download it in Settings -> Network profiles")
	}
	host, port := cfg.hostPort()
	pins := cfg.Pins
	for attempt := 0; ; attempt++ {
		filler := &loginFiller{a: a, row: row, tag: kindCheckpoint, host: host, port: port, login: loginSources{
			Username: cfg.Username, PasswordCredentialID: cfg.PasswordCredentialID,
			SecondPassword: cfg.SecondPassword, SecondCredentialID: cfg.SecondCredentialID,
		}}
		connectStart := time.Now()
		p, err := a.nbman.EnsureWith(row.ID, row.Name, exe, cpArgs(&cfg, pins), nil, tunnelhelper.Options{
			ReadyTimeout: 6 * time.Minute, // typed one-time codes take a while
			OnAuth:       filler.handle,
			OnInfo: func(p *tunnelhelper.Proc) {
				a.rememberRoutes(row.ID, p.Info())
				EventsEmit("network_tunnel_changed", row.ID)
			},
		})
		var ce *tunnelhelper.CertUntrustedError
		if errors.As(err, &ce) && attempt == 0 {
			accepted, remember := a.gatewayTrustPrompt(host, port, cfg.Pins, ce)
			if !accepted {
				return nil, fmt.Errorf("profile %s: gateway certificate not trusted", row.Name)
			}
			pins = []string{ce.Pin}
			if remember {
				if err := a.ocSavePins(row.ID, "", pins); err != nil {
					log.Printf("checkpoint: save pin for %s: %v", row.Name, err)
				}
			}
			continue
		}
		if err != nil {
			log.Printf("checkpoint %s: connect to %s failed: %v", row.Name, cfg.Server, err)
			return nil, fmt.Errorf("profile %s: %w", row.Name, err)
		}
		log.Printf("checkpoint %s: connected to %s in %s", row.Name, cfg.Server, time.Since(connectStart).Round(10*time.Millisecond))
		a.wgTouch(row.ID)
		a.rememberRoutes(row.ID, p.Info())
		a.recordAudit("network.tunnel.start", row.ID, map[string]string{"name": row.Name, "kind": kindCheckpoint})
		go a.presencePublish(row.ID, kindCheckpoint)
		EventsEmit("network_tunnel_changed", row.ID)
		return p, nil
	}
}
