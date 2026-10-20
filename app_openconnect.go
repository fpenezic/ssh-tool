package main

// OpenConnect network profiles: an SSL VPN (Cisco AnyConnect / ocserv,
// Fortinet, F5, GlobalProtect, Pulse) run by the ssh-tool-openconnect
// plugin in userspace and reached through its SOCKS5 proxy, like the
// NetBird / Tailscale helpers. Unlike those, the gateway logs in with forms
// (user, password, a one-time code, in one or several rounds), so the helper
// forwards every form here: ocAuthFiller answers what the profile can
// (vault credentials, a TOTP from Bitwarden / KeePass) and the rest goes to
// the same prompt SSH keyboard-interactive uses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/creds"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/totp"
	"ssh-tool/internal/tunnelhelper"
)

const kindOpenConnect = "openconnect"

// Second-password sources.
const (
	ocSecondPrompt     = "prompt"     // typed at login (hardware / app token)
	ocSecondCredential = "credential" // a stored password
	ocSecondTOTP       = "totp"       // generated from a Bitwarden / KeePass item
)

// OpenConnectConfig is the stored (secretless) OpenConnect profile.
// Secrets stay in credentials and are resolved at login.
type OpenConnectConfig struct {
	Kind      string `json:"kind"` // "openconnect"
	Server    string `json:"server"`
	Flavor    string `json:"flavor"` // anyconnect | fortinet | f5 | gp | pulse | nc
	AuthGroup string `json:"auth_group,omitempty"`
	// Pins are the trusted gateway certificates (pin-sha256:...), set by the
	// trust-on-first-use prompt. Empty = the system trust store decides.
	Pins []string `json:"pins,omitempty"`
	// Backups are further gateways of the same VPN, tried in order when
	// the one before does not answer at all. A rejected login is not
	// retried elsewhere. Each keeps its own trusted certificate.
	Backups []OpenConnectGateway `json:"backups,omitempty"`
	MTU     int                  `json:"mtu,omitempty"`
	BaseMTU int                  `json:"base_mtu,omitempty"`
	NoUDP   bool                 `json:"no_udp,omitempty"`

	// First login: Username ("" = the credential's default username, else
	// asked) and PasswordCredentialID ("" = asked).
	Username             string `json:"username,omitempty"`
	PasswordCredentialID string `json:"password_credential_id,omitempty"`
	// Second login (AnyConnect secondary authentication, an OTP round):
	// SecondUsername ("" = asked at login), SecondPassword one of the
	// ocSecond* sources, SecondCredentialID the credential behind it.
	SecondUsername     string `json:"second_username,omitempty"`
	SecondPassword     string `json:"second_password,omitempty"`
	SecondCredentialID string `json:"second_credential_id,omitempty"`

	// ClientCertCredentialID names a certificate credential: the
	// imported certificate + key live in the vault (and sync with it).
	ClientCertCredentialID string `json:"client_cert_credential_id,omitempty"`

	// LogoutOnIdle ends the gateway session when the idle stop takes the
	// tunnel down. Default (false): the helper detaches, the session
	// stays open at the gateway until its own idle timeout, and the next
	// connect resumes it with the cookie instead of a new login.
	LogoutOnIdle bool `json:"logout_on_idle,omitempty"`

	Mode   string `json:"mode,omitempty"`
	Paused bool   `json:"paused,omitempty"`
}

// OpenConnectGateway is one gateway address with its trusted certificates.
type OpenConnectGateway struct {
	Server string   `json:"server"`
	Pins   []string `json:"pins,omitempty"`
}

// gateways lists the main gateway, then the backups, in connect order.
func (c *OpenConnectConfig) gateways() []OpenConnectGateway {
	return append([]OpenConnectGateway{{Server: c.Server, Pins: c.Pins}}, c.Backups...)
}

// ocSessions holds the AnyConnect session cookie of each profile whose
// tunnel is up or was detached by the idle stop: memory only (never the
// vault, the store or sync), so it dies with the app. A cookie is a
// bearer credential for the session, as good as a completed login.
// The gateway that issued the cookie is kept with it: another gateway of
// the profile does not know the session.
var ocSessions = struct {
	sync.Mutex
	m map[string]ocSession
}{m: map[string]ocSession{}}

type ocSession struct{ server, cookie string }

func ocSessionGet(profileID string) string {
	ocSessions.Lock()
	defer ocSessions.Unlock()
	return ocSessions.m[profileID].cookie
}

// ocSessionFor is the profile's cookie when server issued it.
func ocSessionFor(profileID, server string) string {
	ocSessions.Lock()
	defer ocSessions.Unlock()
	if s := ocSessions.m[profileID]; s.server == server {
		return s.cookie
	}
	return ""
}

// ocSessionSet saves the cookie server issued; an empty cookie forgets it.
func ocSessionSet(profileID, server, cookie string) {
	ocSessions.Lock()
	defer ocSessions.Unlock()
	if cookie == "" {
		delete(ocSessions.m, profileID)
		return
	}
	ocSessions.m[profileID] = ocSession{server, cookie}
}

// idleStopTunnel is the idle stop: an OpenConnect tunnel with a session
// cookie detaches (the gateway session survives for a resume), anything
// else stops for good.
func (a *App) idleStopTunnel(profileID, kind string) {
	if kind == kindOpenConnect && ocSessionGet(profileID) != "" {
		row, err := a.db.GetNetworkProfile(profileID)
		var cfg OpenConnectConfig
		if err == nil && json.Unmarshal([]byte(row.ConfigJSON), &cfg) == nil && !cfg.LogoutOnIdle {
			a.nbman.Detach(profileID)
			go a.presenceClear(profileID)
			log.Printf("openconnect %s: gateway session kept for the next connect", row.Name)
			return
		}
	}
	a.tunnelStop(profileID)
}

var ocFlavors = map[string]bool{"anyconnect": true, "fortinet": true, "f5": true, "gp": true, "pulse": true, "nc": true}

func (c *OpenConnectConfig) normalize() error {
	c.Kind = kindOpenConnect
	c.Server = strings.TrimSpace(c.Server)
	if c.Server == "" {
		return fmt.Errorf("gateway address is required")
	}
	var err error
	if c.Server, err = ocGatewayURL(c.Server); err != nil {
		return err
	}
	seen := map[string]bool{c.Server: true}
	var backups []OpenConnectGateway
	for _, b := range c.Backups {
		if strings.TrimSpace(b.Server) == "" {
			continue
		}
		if b.Server, err = ocGatewayURL(strings.TrimSpace(b.Server)); err != nil {
			return err
		}
		if !seen[b.Server] {
			seen[b.Server] = true
			backups = append(backups, b)
		}
	}
	c.Backups = backups
	if c.Flavor == "" {
		c.Flavor = "anyconnect"
	}
	if !ocFlavors[c.Flavor] {
		return fmt.Errorf("unknown VPN type %q", c.Flavor)
	}
	switch c.SecondPassword {
	case "":
		c.SecondPassword = ocSecondPrompt
	case ocSecondPrompt, ocSecondCredential, ocSecondTOTP:
	default:
		return fmt.Errorf("unknown second password source %q", c.SecondPassword)
	}
	if c.SecondPassword != ocSecondPrompt && c.SecondCredentialID == "" {
		return fmt.Errorf("pick the credential for the second password")
	}
	if c.MTU < 0 || c.MTU > 9000 || c.BaseMTU < 0 || c.BaseMTU > 9000 {
		return fmt.Errorf("MTU out of range")
	}
	return nil
}

// ocGatewayURL is a gateway address as a URL, https:// when it has no scheme.
func ocGatewayURL(s string) (string, error) {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	if u, err := url.Parse(s); err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("gateway address %q is not a valid URL", s)
	}
	return s, nil
}

// ensureOpenConnectTunnel spawns (or reuses) the OpenConnect helper for the
// profile. An untrusted or changed gateway certificate asks the user once
// and, when accepted, retries with that certificate pinned.
func (a *App) ensureOpenConnectTunnel(row *store.NetworkProfile) (tunnelDialer, error) {
	if p := a.nbman.Get(row.ID); p != nil {
		a.wgTouch(row.ID)
		return p, nil
	}
	// A synced profile logs in as the same VPN user everywhere, and most
	// gateways allow one session per user: the second login drops the
	// first. Same take-over guard as WireGuard.
	if owner, blocked := a.presenceBlocksBringUp(row.ID); blocked {
		return nil, fmt.Errorf("%s%s|%s", errProfileBusyPrefix, row.ID, owner)
	}
	var cfg OpenConnectConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("profile %s: bad config: %w", row.Name, err)
	}
	exe, ok := pluginPath(kindOpenConnect)
	if !ok {
		return nil, fmt.Errorf("OpenConnect plugin is not installed - download it in Settings -> Network profiles")
	}
	env, err := a.ocClientCertEnv(row.Name, &cfg)
	if err != nil {
		return nil, err
	}
	gws := ocConnectOrder(row.ID, cfg.gateways())
	for i, gw := range gws {
		p, err := a.ocConnect(row, &cfg, exe, env, gw)
		var ue *tunnelhelper.UnreachableError
		if errors.As(err, &ue) && i+1 < len(gws) {
			log.Printf("openconnect %s: %s does not answer, trying %s", row.Name, gw.Server, gws[i+1].Server)
			continue
		}
		return p, err
	}
	return nil, fmt.Errorf("profile %s: no gateway", row.Name)
}

// ocConnectOrder puts the gateway holding the profile's detached session
// first, so it resumes there; the rest keep the profile's order.
func ocConnectOrder(profileID string, gws []OpenConnectGateway) []OpenConnectGateway {
	for i, gw := range gws {
		if i > 0 && ocSessionFor(profileID, gw.Server) != "" {
			return append([]OpenConnectGateway{gw}, append(gws[:i:i], gws[i+1:]...)...)
		}
	}
	return gws
}

// ocConnect runs the helper against one gateway of the profile. An
// untrusted or changed certificate asks the user once and, when accepted,
// retries with that certificate pinned.
func (a *App) ocConnect(row *store.NetworkProfile, cfg *OpenConnectConfig, exe string, env []string, gw OpenConnectGateway) (tunnelDialer, error) {
	pins := gw.Pins
	for attempt := 0; ; attempt++ {
		args := ocArgs(cfg, gw.Server, pins)
		filler := &ocAuthFiller{a: a, row: row, cfg: cfg, server: gw.Server}
		runEnv := env
		if cfg.Flavor == "anyconnect" {
			// Ask for the session cookie, and resume a detached session
			// with the one from the last run. A helper that predates
			// this ignores both variables and logs in as before.
			runEnv = append(append([]string{}, env...), "SSHTOOL_OC_SESSION=1")
			if c := ocSessionFor(row.ID, gw.Server); c != "" {
				runEnv = append(runEnv, "SSHTOOL_OC_COOKIE="+c)
				filler.resuming = true
				log.Printf("openconnect %s: resuming the gateway session", row.Name)
			}
		}
		connectStart := time.Now()
		p, err := a.nbman.EnsureWith(row.ID, row.Name, exe, args, runEnv, tunnelhelper.Options{
			ReadyTimeout: 6 * time.Minute, // typed one-time codes take a while
			OnAuth:       filler.handle,
			OnInfo: func(p *tunnelhelper.Proc) {
				a.rememberRoutes(row.ID, p.Info())
				EventsEmit("network_tunnel_changed", row.ID)
			},
			OnSession: func(c string) { ocSessionSet(row.ID, gw.Server, c) },
		})
		var ce *tunnelhelper.CertUntrustedError
		if errors.As(err, &ce) && attempt == 0 {
			accepted, remember := a.ocTrustPrompt(gw, ce)
			if !accepted {
				return nil, fmt.Errorf("profile %s: gateway certificate not trusted", row.Name)
			}
			pins = []string{ce.Pin}
			if remember {
				if err := a.ocSavePins(row.ID, gw.Server, pins); err != nil {
					log.Printf("openconnect: save pin for %s: %v", row.Name, err)
				}
			}
			continue
		}
		if err != nil {
			// An unreachable gateway keeps the cookie: it may belong to
			// the backup tried next.
			var ue *tunnelhelper.UnreachableError
			if !errors.As(err, &ue) {
				ocSessionSet(row.ID, "", "")
			}
			log.Printf("openconnect %s: connect to %s failed: %v", row.Name, gw.Server, err)
			if filler.changeSent() {
				log.Printf("openconnect %s: a password change was sent before the failure - the gateway may already use the new password; the stored credential was not updated", row.Name)
			}
			return nil, fmt.Errorf("profile %s: %w", row.Name, err)
		}
		if filler.wasResumed() {
			log.Printf("openconnect %s: connected to %s in %s (session resumed, no login)", row.Name, gw.Server, time.Since(connectStart).Round(10*time.Millisecond))
		} else {
			log.Printf("openconnect %s: connected to %s in %s", row.Name, gw.Server, time.Since(connectStart).Round(10*time.Millisecond))
		}
		filler.commitChange()
		a.wgTouch(row.ID)
		a.rememberRoutes(row.ID, p.Info())
		a.recordAudit("network.tunnel.start", row.ID, map[string]string{"name": row.Name, "kind": kindOpenConnect})
		go a.presencePublish(row.ID, kindOpenConnect)
		EventsEmit("network_tunnel_changed", row.ID)
		return p, nil
	}
}

func ocArgs(cfg *OpenConnectConfig, server string, pins []string) []string {
	args := []string{"--server", server, "--flavor", cfg.Flavor}
	if cfg.AuthGroup != "" {
		args = append(args, "--authgroup", cfg.AuthGroup)
	}
	for _, p := range pins {
		args = append(args, "--pin", p)
	}
	if cfg.MTU > 0 {
		args = append(args, "--mtu", fmt.Sprint(cfg.MTU))
	}
	if cfg.BaseMTU > 0 {
		args = append(args, "--base-mtu", fmt.Sprint(cfg.BaseMTU))
	}
	if cfg.NoUDP {
		args = append(args, "--no-udp")
	}
	return args
}

// ocClientCertEnv hands the profile's client certificate (a vault-held
// bundle, see creds.ParseClientCert) to the helper through its
// environment, never argv.
func (a *App) ocClientCertEnv(profileName string, cfg *OpenConnectConfig) ([]string, error) {
	if cfg.ClientCertCredentialID == "" {
		return nil, nil
	}
	bundle, err := a.resolveHelperSecret(profileName, cfg.ClientCertCredentialID, "client certificate")
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := creds.SplitCertBundle(bundle)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", profileName, err)
	}
	return []string{"SSHTOOL_OC_CERT=" + string(certPEM), "SSHTOOL_OC_KEY=" + string(keyPEM)}, nil
}

// ocCredentialSecret resolves a credential's password through the same path
// SSH uses, so vault, KeePass, Bitwarden and Infisical all work.
func (a *App) ocCredentialSecret(credID string) (string, error) {
	cred, err := a.db.GetCredential(credID)
	if err != nil {
		return "", err
	}
	m, err := sshlayer.ResolveAuth(context.Background(), cred, a.vault, nil)
	if err != nil {
		return "", err
	}
	if m.Password == "" {
		return "", fmt.Errorf("credential %s holds no password", cred.Name)
	}
	return m.Password, nil
}

func (a *App) ocCredentialUser(credID string) string {
	if credID == "" {
		return ""
	}
	cred, err := a.db.GetCredential(credID)
	if err != nil || cred.DefaultUsername == nil {
		return ""
	}
	return *cred.DefaultUsername
}

// ocTrustPrompt asks about an untrusted / changed OpenConnect gateway
// certificate. Returns (accepted, remember).
func (a *App) ocTrustPrompt(gw OpenConnectGateway, ce *tunnelhelper.CertUntrustedError) (bool, bool) {
	u, _ := url.Parse(gw.Server)
	host, port := u.Hostname(), 443
	if p := u.Port(); p != "" {
		fmt.Sscan(p, &port)
	}
	return a.gatewayTrustPrompt(host, port, gw.Pins, ce)
}

// gatewayTrustPrompt asks about an untrusted / changed VPN gateway
// certificate with the host-key dialog (any plugin that reports cert_pin).
// pins are the ones trusted so far. Returns (accepted, remember).
func (a *App) gatewayTrustPrompt(host string, port int, pins []string, ce *tunnelhelper.CertUntrustedError) (bool, bool) {
	status, oldFP := "unknown", ""
	if ce.Changed {
		status = "changed"
		oldFP = strings.Join(pins, ", ")
	}
	challengeID, ch := a.registerTrustChallenge()
	defer a.unregisterTrustChallenge(challengeID)
	EventsEmit("host_key_challenge", HostKeyChallengeEvent{
		ChallengeID: challengeID, Hostname: host, Port: port,
		KeyType: ocCertKeyType + " - " + ce.Subject, Fingerprint: ce.Pin,
		Status: status, OldFingerprint: oldFP,
	})
	a.RequestAttention()
	select {
	case r := <-ch:
		return r.accept, r.remember
	case <-a.ctx.Done():
	case <-time.After(promptTimeout):
	}
	return false, false
}

// ocCertKeyType marks a host-key challenge that is really a VPN gateway
// certificate: the dialog words it accordingly and the answer is saved
// on the profile, not in known_hosts.
const ocCertKeyType = "TLS certificate"

type trustReply struct{ accept, remember bool }

var (
	trustMu      sync.Mutex
	trustPending = map[string]chan trustReply{}
)

func (a *App) registerTrustChallenge() (string, chan trustReply) {
	id, _ := a.registerPrompt() // only for a unique id
	a.unregisterPrompt(id)
	ch := make(chan trustReply, 1)
	trustMu.Lock()
	trustPending[id] = ch
	trustMu.Unlock()
	return id, ch
}

func (a *App) unregisterTrustChallenge(id string) {
	trustMu.Lock()
	delete(trustPending, id)
	trustMu.Unlock()
}

// respondTrustChallenge routes a host-key dialog answer that belongs to a
// gateway certificate. Reports whether the id was one.
func respondTrustChallenge(id string, accept, remember bool) bool {
	trustMu.Lock()
	ch, ok := trustPending[id]
	trustMu.Unlock()
	if ok {
		ch <- trustReply{accept, remember}
	}
	return ok
}

// ocSavePins trusts pins for the profile's gateway server: the main one
// ("" too, for the profile kinds with one gateway) or a backup. The rest
// of the stored config stays as it is.
func (a *App) ocSavePins(profileID, server string, pins []string) error {
	row, err := a.db.GetNetworkProfile(profileID)
	if err != nil {
		return err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(row.ConfigJSON), &m); err != nil {
		return err
	}
	var main string
	_ = json.Unmarshal(m["server"], &main)
	if server == "" || server == main {
		b, _ := json.Marshal(pins)
		m["pins"] = b
	} else {
		var backups []OpenConnectGateway
		_ = json.Unmarshal(m["backups"], &backups)
		found := false
		for i := range backups {
			if backups[i].Server == server {
				backups[i].Pins, found = pins, true
			}
		}
		if !found {
			return fmt.Errorf("gateway %s is no longer in the profile", server)
		}
		b, _ := json.Marshal(backups)
		m["backups"] = b
	}
	out, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = a.db.UpdateNetworkProfile(profileID, row.Name, string(out))
	if err == nil {
		a.recordAudit("network.profile.pin", profileID, map[string]string{"pin": pins[0]})
	}
	return err
}

// ocAuthFiller answers one login's forms. Every source is used once: an
// answer the gateway rejected goes to the user next time instead of being
// resent (a retry loop could lock the account).
type ocAuthFiller struct {
	a   *App
	row *store.NetworkProfile
	cfg *OpenConnectConfig
	// server is the gateway this login goes to.
	server string

	mu                         sync.Mutex
	resuming                   bool // a saved session cookie is being tried
	user1, pass1, user2, pass2 bool
	sawPassword                bool
	// lastPassCred is the credential that answered the latest password
	// field ("" when the user typed it or it was a one-time code): the
	// account a following password-change form is about.
	lastPassCred string
	// changeCred / newPassword hold a password change the gateway was
	// sent, written to the credential once the tunnel is up.
	changeCred, newPassword string
	// roles names each field fill saw (by key) as the first or second
	// login's username / password, so the prompt and the log can say
	// which one the gateway is asking for.
	roles  map[string]string
	byName map[string]string // field name -> first role it was given
}

const (
	ocRoleUser1 = "1st login - username"
	ocRolePass1 = "1st login - password"
	ocRoleUser2 = "2nd login - username"
	ocRolePass2 = "2nd login - password / one-time code"

	ocRoleOldPass     = "password change - current password"
	ocRoleNewPass     = "password change - new password"
	ocRoleConfirmPass = "password change - confirm new password"
)

var (
	ocChangeMsgRe    = regexp.MustCompile(`(?i)expire|must be changed|change (your |the )?password|password change|new password|reset`)
	ocConfirmFieldRe = regexp.MustCompile(`(?i)confirm|verify|re-?enter|re-?type|repeat|again`)
	ocNewFieldRe     = regexp.MustCompile(`(?i)\bnew`)
	ocOldFieldRe     = regexp.MustCompile(`(?i)\bold|current|existing`)
	ocVerifyCodeRe   = regexp.MustCompile(`(?i)verification|code|token|otp`)
)

// ocIsPasswordChange spots a "your password expired, pick a new one"
// form. Gateways name it differently, so it looks at both the message
// and the fields: two or more password fields where one is a new or a
// confirm field, or a change wording with at least one password field.
// A one-time code field ("verification code") is never a confirm field.
func ocIsPasswordChange(ch tunnelhelper.AuthChallenge) bool {
	pw, named := 0, false
	for _, fl := range ch.Fields {
		if fl.Kind != "password" {
			continue
		}
		pw++
		id := fl.Name + " " + fl.Label
		if ocVerifyCodeRe.MatchString(id) && !ocNewFieldRe.MatchString(id) {
			continue
		}
		if ocNewFieldRe.MatchString(id) || ocConfirmFieldRe.MatchString(id) {
			named = true
		}
	}
	if pw == 0 {
		return false
	}
	return (pw >= 2 && named) || ocChangeMsgRe.MatchString(ch.Message+" "+ch.Error)
}

func ocChangeRole(fl tunnelhelper.AuthField) string {
	id := fl.Name + " " + fl.Label
	switch {
	case ocConfirmFieldRe.MatchString(id):
		return ocRoleConfirmPass
	case ocOldFieldRe.MatchString(id):
		return ocRoleOldPass
	}
	return ocRoleNewPass
}

func (f *ocAuthFiller) setRole(key, name, role string) {
	if f.roles == nil {
		f.roles, f.byName = map[string]string{}, map[string]string{}
	}
	f.roles[key] = role
	if _, ok := f.byName[name]; !ok {
		f.byName[name] = role
	}
}

var ocOTPRe = regexp.MustCompile(`(?i)\botp\b|one[- ]?time|token|passcode|verification|code|2fa|mfa|second`)

func (f *ocAuthFiller) handle(p *tunnelhelper.Proc, ch tunnelhelper.AuthChallenge) {
	f.mu.Lock()
	if f.resuming {
		// A login form while resuming: the gateway no longer knows the
		// session (expired, or ended elsewhere). Normal login from here.
		f.resuming = false
		ocSessionSet(f.row.ID, "", "")
		log.Printf("openconnect %s: the saved gateway session has expired, logging in", f.row.Name)
	}
	f.mu.Unlock()
	if ocIsPasswordChange(ch) {
		f.handleChange(p, ch)
		return
	}
	values, missing := f.fill(ch)
	f.mu.Lock()
	line := ocDescribeRound(ch, values, missing, f.roles)
	f.mu.Unlock()
	log.Printf("openconnect %s: %s", f.row.Name, line)
	if len(missing) > 0 {
		if err := f.prompt(ch, values, missing); err != nil {
			_ = p.Cancel(ch.ID)
			return
		}
	}
	if err := p.Answer(ch.ID, values); err != nil {
		log.Printf("openconnect %s: answer: %v", f.row.Name, err)
	}
}

// ocDescribeRound is the log line for one login round: what the gateway
// asked and where each answer came from, never the values. Login forms
// differ per gateway, and this line is what makes a failed login
// diagnosable from a user's log.
func ocDescribeRound(ch tunnelhelper.AuthChallenge, values map[string]string, missing []int, roles map[string]string) string {
	ask := map[int]bool{}
	for _, i := range missing {
		ask[i] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "login round %s message=%q", ch.ID, ch.Message)
	if ch.Error != "" {
		fmt.Fprintf(&b, " error=%q", ch.Error)
	}
	for i, fl := range ch.Fields {
		src := "empty"
		switch {
		case ask[i]:
			src = "prompt"
		case values[fl.Key] != "" && fl.Kind == "select":
			src = "option"
		case values[fl.Key] != "":
			src = "filled"
		}
		role := ""
		if r := roles[fl.Key]; r != "" {
			role = " (" + r + ")"
		}
		fmt.Fprintf(&b, " [%s name=%q label=%q%s -> %s]", fl.Kind, fl.Name, fl.Label, role, src)
	}
	return b.String()
}

// fill returns the answers it can give plus the indexes of fields left for
// the user.
func (f *ocAuthFiller) fill(ch tunnelhelper.AuthChallenge) (map[string]string, []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := f.cfg
	values := map[string]string{}
	var missing []int
	otpRound := ocOTPRe.MatchString(ch.Message)
	if ch.Error != "" {
		// The gateway rejected the last answer and asks again. Every
		// stored source was used once already, and guessing by order
		// here would put the second password into the first one's
		// field. Ask the user for all of it, labelled as before.
		for i, fl := range ch.Fields {
			if fl.Kind == "select" {
				if v := pickOption(fl, cfg.AuthGroup); v != "" {
					values[fl.Key] = v
					continue
				}
			}
			if r := f.byName[fl.Name]; r != "" {
				f.setRole(fl.Key, fl.Name, r)
			}
			missing = append(missing, i)
		}
		f.lastPassCred = ""
		return values, missing
	}
	for i, fl := range ch.Fields {
		second := strings.Contains(strings.ToLower(fl.Name), "secondary") || (otpRound && f.sawPassword)
		switch fl.Kind {
		case "select":
			if v := pickOption(fl, cfg.AuthGroup); v != "" {
				values[fl.Key] = v
				continue
			}
		case "text":
			if second || f.user1 {
				f.setRole(fl.Key, fl.Name, ocRoleUser2)
			} else {
				f.setRole(fl.Key, fl.Name, ocRoleUser1)
			}
			if second {
				if !f.user2 {
					f.user2 = true
					// Blank is asked, not copied from the first login:
					// a gateway that shows a second username field
					// usually wants a different account there.
					if u := cfg.SecondUsername; u != "" {
						values[fl.Key] = u
						continue
					}
				}
			} else if !f.user1 {
				f.user1 = true
				if u := f.firstUser(); u != "" {
					values[fl.Key] = u
					continue
				}
			}
		case "password":
			if !second && !otpRound && !f.pass1 {
				f.setRole(fl.Key, fl.Name, ocRolePass1)
				f.pass1, f.sawPassword = true, true
				if cfg.PasswordCredentialID != "" {
					if s, err := f.a.ocCredentialSecret(cfg.PasswordCredentialID); err == nil {
						values[fl.Key] = s
						f.lastPassCred = cfg.PasswordCredentialID
						continue
					} else {
						log.Printf("openconnect %s: password credential: %v", f.row.Name, err)
					}
				}
			} else {
				f.setRole(fl.Key, fl.Name, ocRolePass2)
			}
			if f.roles[fl.Key] == ocRolePass2 && !f.pass2 {
				f.pass2, f.sawPassword = true, true
				if s := f.secondSecret(); s != "" {
					values[fl.Key] = s
					f.lastPassCred = ""
					if cfg.SecondPassword == ocSecondCredential {
						f.lastPassCred = cfg.SecondCredentialID
					}
					continue
				}
			}
		}
		if fl.Value != "" && fl.Kind != "password" {
			values[fl.Key] = fl.Value // a prefilled default
			continue
		}
		if fl.Kind == "password" {
			f.lastPassCred = ""
		}
		missing = append(missing, i)
	}
	return values, missing
}

// handleChange answers a password-change form. Nothing stored goes into
// the new-password fields: the user types them (or leaves them blank to
// skip an optional change), and where the gateway
// asks twice the two must match before anything is sent - a mismatch the
// gateway rejects is one more failed login on the account. A "current
// password" field is filled from the credential that just logged in.
// The new password is written to that credential once the tunnel is up
// (commitChange), not before: the gateway may still refuse it.
func (f *ocAuthFiller) handleChange(p *tunnelhelper.Proc, ch tunnelhelper.AuthChallenge) {
	f.mu.Lock()
	cred := f.lastPassCred
	values := map[string]string{}
	var missing []int
	for i, fl := range ch.Fields {
		switch fl.Kind {
		case "select":
			if v := pickOption(fl, f.cfg.AuthGroup); v != "" {
				values[fl.Key] = v
				continue
			}
		case "password":
			role := ocChangeRole(fl)
			f.setRole(fl.Key, fl.Name, role)
			if role == ocRoleOldPass && cred != "" && ch.Error == "" {
				if s, err := f.a.ocCredentialSecret(cred); err == nil {
					values[fl.Key] = s
					continue
				}
			}
		default:
			if fl.Value != "" {
				values[fl.Key] = fl.Value
				continue
			}
		}
		missing = append(missing, i)
	}
	line := ocDescribeRound(ch, values, missing, f.roles)
	f.mu.Unlock()
	src := "typed at login"
	if cred != "" {
		src = "credential " + cred
	}
	log.Printf("openconnect %s: password change requested (previous password: %s): %s", f.row.Name, src, line)
	var newPW string
	for attempt := 1; ; attempt++ {
		if err := f.prompt(ch, values, missing); err != nil {
			log.Printf("openconnect %s: password change cancelled", f.row.Name)
			_ = p.Cancel(ch.ID)
			return
		}
		var ok bool
		newPW, ok = ocNewPassword(ch, values, f.roles)
		if ok {
			break
		}
		log.Printf("openconnect %s: password change: the new passwords do not match (attempt %d)", f.row.Name, attempt)
		if attempt == 3 {
			_ = p.Cancel(ch.ID)
			return
		}
		ch.Error = "The new passwords do not match - type them again."
	}
	if newPW == "" {
		log.Printf("openconnect %s: password change skipped (new password left blank)", f.row.Name)
	} else {
		f.mu.Lock()
		f.changeCred, f.newPassword = cred, newPW
		f.mu.Unlock()
		log.Printf("openconnect %s: password change sent to the gateway", f.row.Name)
	}
	if err := p.Answer(ch.ID, values); err != nil {
		log.Printf("openconnect %s: answer: %v", f.row.Name, err)
	}
}

// ocNewPassword returns the new password typed into a change form and
// whether every new / confirm field agrees on it. All of them left blank
// agree on "": an optional change (Cisco ASA: "Leave both boxes blank to
// continue using current password") is skipped that way, and a gateway
// that insists answers with an error round.
func ocNewPassword(ch tunnelhelper.AuthChallenge, values map[string]string, roles map[string]string) (string, bool) {
	var pw string
	seen := false
	for _, fl := range ch.Fields {
		r := roles[fl.Key]
		if r != ocRoleNewPass && r != ocRoleConfirmPass {
			continue
		}
		v := values[fl.Key]
		if seen && v != pw {
			return "", false
		}
		pw, seen = v, true
	}
	return pw, true
}

// commitChange stores a password the gateway accepted (the tunnel came
// up after the change) in the credential it replaced. A typed or an
// external (Bitwarden / KeePass) password is only logged: there is
// nothing here to write it to.
func (f *ocAuthFiller) commitChange() {
	f.mu.Lock()
	cred, pw := f.changeCred, f.newPassword
	f.mu.Unlock()
	if pw == "" {
		return
	}
	name := f.row.Name
	if cred == "" {
		log.Printf("openconnect %s: password changed at the gateway; the old one was typed, no credential to update", name)
		return
	}
	c, err := f.a.db.GetCredential(cred)
	if err != nil || c == nil {
		log.Printf("openconnect %s: password changed at the gateway; credential %s not found: %v", name, cred, err)
		return
	}
	if c.StorageMode != store.StorageManaged || c.Kind != store.CredPassword {
		log.Printf("openconnect %s: password changed at the gateway; credential %q is %s/%s - update it there", name, c.Name, c.StorageMode, c.Kind)
		return
	}
	if _, err := f.a.credSvc.RotatePassword(cred, pw); err != nil {
		log.Printf("openconnect %s: password changed at the gateway; updating credential %q failed: %v", name, c.Name, err)
		return
	}
	f.a.recordAudit("credential.rotate", cred, map[string]string{"via": "vpn password change", "profile": name})
	log.Printf("openconnect %s: password changed at the gateway, credential %q updated", name, c.Name)
}

func (f *ocAuthFiller) firstUser() string {
	if f.cfg.Username != "" {
		return f.cfg.Username
	}
	return f.a.ocCredentialUser(f.cfg.PasswordCredentialID)
}

func (f *ocAuthFiller) secondSecret() string {
	cfg := f.cfg
	switch cfg.SecondPassword {
	case ocSecondCredential:
		s, err := f.a.ocCredentialSecret(cfg.SecondCredentialID)
		if err != nil {
			log.Printf("openconnect %s: second password credential: %v", f.row.Name, err)
		}
		return s
	case ocSecondTOTP:
		cred, err := f.a.db.GetCredential(cfg.SecondCredentialID)
		if err != nil {
			return ""
		}
		sp, err := totp.Parse(f.a.externalTOTPSecret(cred))
		if err != nil {
			log.Printf("openconnect %s: one-time code: %v", f.row.Name, err)
			return ""
		}
		return sp.Code(time.Now())
	}
	return ""
}

func pickOption(fl tunnelhelper.AuthField, want string) string {
	if len(fl.Options) == 1 {
		return fl.Options[0].Value
	}
	if want == "" {
		return ""
	}
	for _, o := range fl.Options {
		if strings.EqualFold(o.Value, want) || strings.EqualFold(o.Label, want) {
			return o.Value
		}
	}
	return ""
}

// prompt asks the user for the fields fill left, with the gateway's own
// message, through the SSH keyboard-interactive dialog.
func (f *ocAuthFiller) prompt(ch tunnelhelper.AuthChallenge, values map[string]string, missing []int) error {
	u, _ := url.Parse(f.server)
	qs := make([]sshlayer.InteractiveAuthPrompt, len(missing))
	for j, i := range missing {
		fl := ch.Fields[i]
		text := fl.Label
		if text == "" {
			text = fl.Name + ":"
		}
		if fl.Kind == "select" && len(fl.Options) > 0 {
			var opts []string
			for _, o := range fl.Options {
				opts = append(opts, o.Value)
			}
			text += " (" + strings.Join(opts, " / ") + ")"
		}
		if r := f.roles[fl.Key]; r != "" {
			text = "[" + r + "] " + text
		}
		qs[j] = sshlayer.InteractiveAuthPrompt{Echo: fl.Kind != "password", Text: text}
	}
	instruction := ch.Message
	if ch.Error != "" {
		instruction = ch.Error + "\n" + instruction
	}
	if ch.Banner != "" {
		instruction = ch.Banner + "\n\n" + instruction
	}
	port := 443
	if p := u.Port(); p != "" {
		fmt.Sscan(p, &port)
	}
	answers, err := f.a.promptInteractiveAuth(f.row.Name, u.Hostname(), port, f.firstUser(), "VPN login", instruction, qs)
	if err != nil {
		return err
	}
	for j, i := range missing {
		if j < len(answers) {
			values[ch.Fields[i].Key] = answers[j]
		}
	}
	return nil
}

// OpenConnectProfileInput is the create / update payload.
type OpenConnectProfileInput struct {
	Name   string            `json:"name"`
	Config OpenConnectConfig `json:"config"`
}

// NetworkProfileCreateOpenConnect stores an OpenConnect profile.
func (a *App) NetworkProfileCreateOpenConnect(in OpenConnectProfileInput) (*NetworkProfileInfo, error) {
	if _, ok := pluginPath(kindOpenConnect); !ok {
		return nil, fmt.Errorf("OpenConnect plugin is not installed - download it first")
	}
	cfg := in.Config
	cfg.Pins, cfg.Mode, cfg.Paused = nil, "", false
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	for i := range cfg.Backups {
		cfg.Backups[i].Pins = nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	row, err := a.db.CreateNetworkProfile(in.Name, string(b))
	if err != nil {
		return nil, err
	}
	a.recordAudit("network.profile.create", row.ID, map[string]string{"name": in.Name, "kind": kindOpenConnect})
	info := a.infoFor(*row)
	return &info, nil
}

// NetworkProfileUpdateOpenConnect edits an OpenConnect profile, keeping its
// policy and trusted certificate (unless the gateway address changed).
// Restarts the helper so the new settings take effect.
func (a *App) NetworkProfileUpdateOpenConnect(id string, in OpenConnectProfileInput) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	var old OpenConnectConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &old); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	cfg := in.Config
	cfg.Mode, cfg.Paused = old.Mode, old.Paused
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	// Trusted certificates stay with their gateway address.
	trusted := map[string][]string{}
	for _, gw := range old.gateways() {
		trusted[gw.Server] = gw.Pins
	}
	cfg.Pins = trusted[cfg.Server]
	for i := range cfg.Backups {
		cfg.Backups[i].Pins = trusted[cfg.Backups[i].Server]
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
	a.recordAudit("network.profile.update", id, map[string]string{"name": in.Name, "kind": kindOpenConnect})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// NetworkProfileTunnelInfo is the running helper's own description of the
// tunnel (addresses, routes, DNS, MTU, transport...), as JSON; "" when the
// tunnel is down or the helper reports none.
func (a *App) NetworkProfileTunnelInfo(id string) string {
	return string(a.nbman.Status(id).Info)
}

func (f *ocAuthFiller) changeSent() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newPassword != ""
}

// wasResumed reports a connect that came up on the saved session, with
// no login form in between.
func (f *ocAuthFiller) wasResumed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resuming
}
