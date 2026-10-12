package main

// OpenVPN network profiles: a userspace OpenVPN 2.x client in the
// ssh-tool-openvpn plugin (separate repo), driven like the OpenConnect
// one - a SOCKS5 proxy the first SSH hop dials through, login forms
// relayed as auth events.
//
// Storage: the imported .ovpn keeps only its public parts in the profile
// (config_json). The client certificate + key become a certificate
// credential (vault, synced, expiry tracked like any other), the
// tls-auth / tls-crypt key goes to the vault under
// ovpn_static_key:<profile id>. Connect puts the file back together and
// hands it to the plugin in an environment variable, never argv.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/creds"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/totp"
	"ssh-tool/internal/tunnelhelper"
)

const kindOpenVPN = "openvpn"

// OpenVPNConfig is an OpenVPN profile's stored configuration.
type OpenVPNConfig struct {
	Kind string `json:"kind"` // "openvpn"
	// Config is the .ovpn text without <cert>, <key>, <tls-auth> and
	// <tls-crypt> (those live in the vault, see the file comment).
	Config string `json:"config"`
	// Summary of the configuration, from the plugin's check: what the UI
	// shows and asks for.
	Server          string   `json:"server"` // first remote, host:port/proto
	Remotes         []string `json:"remotes,omitempty"`
	AuthUserPass    bool     `json:"auth_user_pass,omitempty"`
	StaticChallenge string   `json:"static_challenge,omitempty"`
	// StaticKey is "tls-auth" / "tls-crypt" when the key is in the vault.
	StaticKey string `json:"static_key,omitempty"`

	// Login, same sources as OpenConnect: Username ("" = the credential's
	// default username, else asked), PasswordCredentialID ("" = asked).
	Username             string `json:"username,omitempty"`
	PasswordCredentialID string `json:"password_credential_id,omitempty"`
	// SecondPassword answers a static or dynamic challenge (a one-time
	// code): ocSecondPrompt / ocSecondTOTP / ocSecondCredential.
	SecondPassword     string `json:"second_password,omitempty"`
	SecondCredentialID string `json:"second_credential_id,omitempty"`

	ClientCertCredentialID string `json:"client_cert_credential_id,omitempty"`

	Mode   string `json:"mode,omitempty"`
	Paused bool   `json:"paused,omitempty"`
}

// OpenVPNProfileInput is the create / update payload. ConfigText is a
// freshly imported .ovpn (secrets included); "" on update keeps the
// stored one.
type OpenVPNProfileInput struct {
	Name       string        `json:"name"`
	ConfigText string        `json:"config_text"`
	Config     OpenVPNConfig `json:"config"`
}

func ovpnStaticKeyVaultKey(profileID string) string { return "ovpn_static_key:" + profileID }

// ovpnSecrets is what splitOvpnSecrets takes out of a configuration.
type ovpnSecrets struct {
	cert, key, staticKey, staticMode string
}

// splitOvpnSecrets removes the inline <cert>, <key>, <tls-auth> and
// <tls-crypt> blocks and returns them with the rest of the text.
func splitOvpnSecrets(text string) (string, ovpnSecrets) {
	var out strings.Builder
	var sec ovpnSecrets
	var block string
	var body strings.Builder
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		t := strings.TrimSpace(line)
		if block != "" {
			if t == "</"+block+">" {
				switch block {
				case "cert":
					sec.cert = body.String()
				case "key":
					sec.key = body.String()
				case "tls-auth", "tls-crypt":
					sec.staticKey, sec.staticMode = body.String(), block
				}
				block = ""
				body.Reset()
				continue
			}
			body.WriteString(line + "\n")
			continue
		}
		switch t {
		case "<cert>", "<key>", "<tls-auth>", "<tls-crypt>":
			block = t[1 : len(t)-1]
			continue
		}
		out.WriteString(line + "\n")
	}
	return out.String(), sec
}

// ovpnCheck is the plugin's --check summary.
type ovpnCheck struct {
	Event           string   `json:"event"`
	Error           string   `json:"error"`
	Remotes         []string `json:"remotes"`
	AuthUserPass    bool     `json:"auth_user_pass"`
	StaticChallenge string   `json:"static_challenge"`
	ClientCert      bool     `json:"client_cert"`
	TLSWrap         string   `json:"tls_wrap"`
	Warnings        []string `json:"warnings"`
}

// ovpnRunCheck validates a configuration with the installed plugin.
func ovpnRunCheck(text string) (*ovpnCheck, error) {
	exe, ok := pluginPath(kindOpenVPN)
	if !ok {
		return nil, errors.New("OpenVPN plugin is not installed - download it first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--check")
	cmd.Env = append(cmd.Environ(), "SSHTOOL_OVPN_CONFIG="+text)
	hideConsole(cmd)
	out, _ := cmd.Output()
	var ck ovpnCheck
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if json.Unmarshal([]byte(line), &ck) == nil && (ck.Event == "check" || ck.Event == "error") {
			break
		}
	}
	switch ck.Event {
	case "check":
		return &ck, nil
	case "error":
		return nil, errors.New(ck.Error)
	}
	return nil, errors.New("the OpenVPN plugin could not check the configuration - update the plugin")
}

// ovpnImport validates a freshly imported configuration and moves its
// secrets out: the certificate + key into a certificate credential, the
// static key into the vault (stored by the caller once the profile id
// exists). renewID is the certificate credential a re-import may renew:
// a file with a certificate replaces that credential's content (the
// usual "server sent a new .ovpn" case). Without one, a new credential
// is created, unless the form picked a stored certificate instead.
func (a *App) ovpnImport(name, text string, cfg *OpenVPNConfig, renewID string) (ovpnSecrets, error) {
	ck, err := ovpnRunCheck(text)
	if err != nil {
		return ovpnSecrets{}, fmt.Errorf("configuration: %w", err)
	}
	rest, sec := splitOvpnSecrets(text)
	if sec.cert != "" && sec.key != "" && renewID != "" && cfg.ClientCertCredentialID == renewID {
		if _, err := a.credSvc.ReplaceCertificate(renewID, []byte(sec.cert), []byte(sec.key), ""); err != nil {
			return ovpnSecrets{}, fmt.Errorf("client certificate: %w", err)
		}
	} else if sec.cert != "" && sec.key != "" && cfg.ClientCertCredentialID == "" {
		r, err := a.credSvc.Create(creds.CreateInput{
			Kind: string(store.CredCertificate), Name: name + " - OpenVPN certificate",
			CertData: []byte(sec.cert), CertKeyData: []byte(sec.key),
		})
		if err != nil {
			return ovpnSecrets{}, fmt.Errorf("client certificate: %w", err)
		}
		cfg.ClientCertCredentialID = r.Credential.ID
	}
	cfg.Config = rest
	cfg.Remotes = ck.Remotes
	if len(ck.Remotes) > 0 {
		cfg.Server = ck.Remotes[0]
	}
	cfg.AuthUserPass = ck.AuthUserPass
	cfg.StaticChallenge = ck.StaticChallenge
	cfg.StaticKey = sec.staticMode
	for _, w := range ck.Warnings {
		log.Printf("openvpn %s: config %s", name, w)
	}
	return sec, nil
}

func (c *OpenVPNConfig) normalize() {
	c.Kind = kindOpenVPN
	c.Username = strings.TrimSpace(c.Username)
	switch c.SecondPassword {
	case ocSecondTOTP, ocSecondCredential:
		if c.SecondCredentialID == "" {
			c.SecondPassword = ocSecondPrompt
		}
	default:
		c.SecondPassword, c.SecondCredentialID = ocSecondPrompt, ""
	}
}

// NetworkProfileCreateOpenVPN imports an .ovpn as a new profile.
func (a *App) NetworkProfileCreateOpenVPN(in OpenVPNProfileInput) (*NetworkProfileInfo, error) {
	if strings.TrimSpace(in.ConfigText) == "" {
		return nil, errors.New("import an .ovpn configuration first")
	}
	cfg := in.Config
	cfg.Mode, cfg.Paused = "", false
	cfg.normalize()
	sec, err := a.ovpnImport(in.Name, in.ConfigText, &cfg, "")
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(cfg)
	row, err := a.db.CreateNetworkProfile(in.Name, string(b))
	if err != nil {
		return nil, err
	}
	if sec.staticKey != "" {
		if err := a.vault.Put(ovpnStaticKeyVaultKey(row.ID), sec.staticKey); err != nil {
			_ = a.db.DeleteNetworkProfile(row.ID)
			return nil, fmt.Errorf("store the %s key: %w", sec.staticMode, err)
		}
	}
	a.recordAudit("network.profile.create", row.ID, map[string]string{"name": in.Name, "kind": kindOpenVPN})
	info := a.infoFor(*row)
	return &info, nil
}

// NetworkProfileUpdateOpenVPN edits a profile; a new ConfigText replaces
// the configuration (and its static key), else the stored one stays.
func (a *App) NetworkProfileUpdateOpenVPN(id string, in OpenVPNProfileInput) (*NetworkProfileInfo, error) {
	row, err := a.db.GetNetworkProfile(id)
	if err != nil {
		return nil, err
	}
	var old OpenVPNConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &old); err != nil {
		return nil, fmt.Errorf("bad config: %w", err)
	}
	cfg := in.Config
	cfg.Mode, cfg.Paused = old.Mode, old.Paused
	cfg.normalize()
	if strings.TrimSpace(in.ConfigText) != "" {
		sec, err := a.ovpnImport(in.Name, in.ConfigText, &cfg, old.ClientCertCredentialID)
		if err != nil {
			return nil, err
		}
		if sec.staticKey != "" {
			if err := a.vault.Put(ovpnStaticKeyVaultKey(id), sec.staticKey); err != nil {
				return nil, err
			}
		} else {
			_ = a.vault.Delete(ovpnStaticKeyVaultKey(id))
		}
	} else {
		cfg.Config, cfg.Server, cfg.Remotes = old.Config, old.Server, old.Remotes
		cfg.AuthUserPass, cfg.StaticChallenge, cfg.StaticKey = old.AuthUserPass, old.StaticChallenge, old.StaticKey
	}
	b, _ := json.Marshal(cfg)
	updated, err := a.db.UpdateNetworkProfile(id, in.Name, carryIdleStop(row.ConfigJSON, string(b)))
	if err != nil {
		return nil, err
	}
	a.tunnelStop(id)
	a.recordAudit("network.profile.update", id, map[string]string{"name": in.Name, "kind": kindOpenVPN})
	EventsEmit("network_tunnel_changed", id)
	info := a.infoFor(*updated)
	return &info, nil
}

// ovpnAssemble rebuilds the full configuration for the plugin.
func (a *App) ovpnAssemble(row *store.NetworkProfile, cfg *OpenVPNConfig) (string, error) {
	var b strings.Builder
	b.WriteString(cfg.Config)
	if cfg.ClientCertCredentialID != "" {
		bundle, err := a.resolveHelperSecret(row.Name, cfg.ClientCertCredentialID, "client certificate")
		if err != nil {
			return "", err
		}
		certPEM, keyPEM, err := creds.SplitCertBundle(bundle)
		if err != nil {
			return "", fmt.Errorf("profile %s: %w", row.Name, err)
		}
		fmt.Fprintf(&b, "<cert>\n%s</cert>\n<key>\n%s</key>\n", certPEM, keyPEM)
	}
	if cfg.StaticKey != "" {
		k, ok, err := a.vault.Get(ovpnStaticKeyVaultKey(row.ID))
		if err != nil {
			return "", fmt.Errorf("profile %s: %s key: %w", row.Name, cfg.StaticKey, err)
		}
		if !ok {
			if a.vault.Status().Kind == creds.StatusLocked {
				return "", fmt.Errorf("profile %s: vault is locked - unlock it, then retry", row.Name)
			}
			return "", fmt.Errorf("profile %s: the %s key is missing from the vault - import the configuration again", row.Name, cfg.StaticKey)
		}
		fmt.Fprintf(&b, "<%s>\n%s</%s>\n", cfg.StaticKey, k, cfg.StaticKey)
	}
	return b.String(), nil
}

// ensureOpenVPNTunnel spawns (or reuses) the OpenVPN helper.
func (a *App) ensureOpenVPNTunnel(row *store.NetworkProfile) (tunnelDialer, error) {
	if p := a.nbman.Get(row.ID); p != nil {
		a.wgTouch(row.ID)
		return p, nil
	}
	// One login per user on most servers: same take-over guard as
	// WireGuard and OpenConnect.
	if owner, blocked := a.presenceBlocksBringUp(row.ID); blocked {
		return nil, fmt.Errorf("%s%s|%s", errProfileBusyPrefix, row.ID, owner)
	}
	var cfg OpenVPNConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("profile %s: bad config: %w", row.Name, err)
	}
	exe, ok := pluginPath(kindOpenVPN)
	if !ok {
		return nil, fmt.Errorf("OpenVPN plugin is not installed - download it in Settings -> Network profiles")
	}
	text, err := a.ovpnAssemble(row, &cfg)
	if err != nil {
		return nil, err
	}
	filler := &ovpnAuthFiller{a: a, row: row, cfg: &cfg}
	connectStart := time.Now()
	p, err := a.nbman.EnsureWith(row.ID, row.Name, exe, nil, []string{"SSHTOOL_OVPN_CONFIG=" + text}, tunnelhelper.Options{
		ReadyTimeout: 6 * time.Minute, // typed one-time codes take a while
		OnAuth:       filler.handle,
		OnInfo: func(p *tunnelhelper.Proc) {
			a.rememberRoutes(row.ID, p.Info())
			EventsEmit("network_tunnel_changed", row.ID)
		},
		OnOpenURL: func(u string) { a.ovpnOpenLogin(row.Name, u) },
	})
	if err != nil {
		log.Printf("openvpn %s: connect to %s failed: %v", row.Name, cfg.Server, err)
		return nil, fmt.Errorf("profile %s: %w", row.Name, err)
	}
	a.rememberRoutes(row.ID, p.Info())
	log.Printf("openvpn %s: connected to %s in %s", row.Name, cfg.Server, time.Since(connectStart).Round(10*time.Millisecond))
	a.wgTouch(row.ID)
	a.recordAudit("network.tunnel.start", row.ID, map[string]string{"name": row.Name, "kind": kindOpenVPN})
	go a.presencePublish(row.ID, kindOpenVPN)
	EventsEmit("network_tunnel_changed", row.ID)
	return p, nil
}

// ovpnAuthFiller answers the plugin's login requests: username, password
// and, with a challenge, the response (one-time code). Every stored
// source is used once per login; a refused login (error set) is asked in
// full, so a wrong stored value cannot lock the account through retries.
type ovpnAuthFiller struct {
	a   *App
	row *store.NetworkProfile
	cfg *OpenVPNConfig

	mu       sync.Mutex
	usedPass bool
}

func (f *ovpnAuthFiller) handle(p *tunnelhelper.Proc, ch tunnelhelper.AuthChallenge) {
	values := map[string]string{}
	var missing []int
	f.mu.Lock()
	for i, fl := range ch.Fields {
		if ch.Error != "" && ch.Error != "reconnecting" {
			missing = append(missing, i)
			continue
		}
		switch fl.Name {
		case "username":
			if u := f.firstUser(); u != "" {
				values[fl.Key] = u
				continue
			}
		case "password":
			if !f.usedPass && f.cfg.PasswordCredentialID != "" {
				f.usedPass = true
				if s, err := f.a.ocCredentialSecret(f.cfg.PasswordCredentialID); err == nil {
					values[fl.Key] = s
					continue
				} else {
					log.Printf("openvpn %s: password credential: %v", f.row.Name, err)
				}
			}
		case "response":
			if s := f.response(); s != "" {
				values[fl.Key] = s
				continue
			}
		}
		missing = append(missing, i)
	}
	f.mu.Unlock()
	log.Printf("openvpn %s: %s", f.row.Name, ocDescribeRound(ch, values, missing, nil))
	if len(missing) > 0 {
		if err := f.prompt(ch, values, missing); err != nil {
			_ = p.Cancel(ch.ID)
			return
		}
	}
	if err := p.Answer(ch.ID, values); err != nil {
		log.Printf("openvpn %s: answer: %v", f.row.Name, err)
	}
}

func (f *ovpnAuthFiller) firstUser() string {
	if f.cfg.Username != "" {
		return f.cfg.Username
	}
	return f.a.ocCredentialUser(f.cfg.PasswordCredentialID)
}

// response is the one-time code / challenge answer from the configured
// source; "" = ask.
func (f *ovpnAuthFiller) response() string {
	switch f.cfg.SecondPassword {
	case ocSecondCredential:
		s, err := f.a.ocCredentialSecret(f.cfg.SecondCredentialID)
		if err != nil {
			log.Printf("openvpn %s: challenge credential: %v", f.row.Name, err)
		}
		return s
	case ocSecondTOTP:
		cred, err := f.a.db.GetCredential(f.cfg.SecondCredentialID)
		if err != nil || cred == nil {
			return ""
		}
		sp, err := totp.Parse(f.a.externalTOTPSecret(cred))
		if err != nil {
			log.Printf("openvpn %s: one-time code: %v", f.row.Name, err)
			return ""
		}
		return sp.Code(time.Now())
	}
	return ""
}

func (f *ovpnAuthFiller) prompt(ch tunnelhelper.AuthChallenge, values map[string]string, missing []int) error {
	host, port := ovpnHostPort(f.cfg.Server)
	qs := make([]sshlayer.InteractiveAuthPrompt, len(missing))
	for j, i := range missing {
		fl := ch.Fields[i]
		text := fl.Label
		if text == "" {
			text = fl.Name + ":"
		}
		if fl.Name == "response" && !strings.HasSuffix(text, ":") {
			text += ":"
		}
		qs[j] = sshlayer.InteractiveAuthPrompt{Echo: fl.Kind != "password", Text: text}
	}
	instruction := ch.Message
	if ch.Error != "" {
		instruction = ch.Error + "\n" + instruction
	}
	answers, err := f.a.promptInteractiveAuth(f.row.Name, host, port, f.firstUser(), "VPN login", instruction, qs)
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

// ovpnHostPort splits the "host:port/proto" summary for the prompt.
func ovpnHostPort(server string) (string, int) {
	hp := strings.SplitN(server, "/", 2)[0]
	i := strings.LastIndexByte(hp, ':')
	if i < 0 {
		return hp, 1194
	}
	port := 1194
	fmt.Sscan(hp[i+1:], &port)
	return hp[:i], port
}

// ovpnOpenLogin opens the server's login page in the user's browser: the
// server holds the connect open (AUTH_PENDING) until the login there is
// done. Only http(s); the URL itself is not logged (it can carry a
// one-time state token).
func (a *App) ovpnOpenLogin(profile, u string) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		log.Printf("openvpn %s: refusing a login URL that is not http(s)", profile)
		return
	}
	log.Printf("openvpn %s: the server asks for a login in the browser, opening it", profile)
	a.RequestAttention()
	if err := BrowserOpenURL(u); err != nil {
		log.Printf("openvpn %s: open the login page: %v", profile, err)
	}
}
