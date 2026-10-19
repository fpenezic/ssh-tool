package resolver

import (
	"testing"

	"ssh-tool/internal/store"
)

func ptr[T any](v T) *T { return &v }

func folder(id string, parent *string, s store.InheritableSettings) store.Folder {
	return store.Folder{ID: id, ParentID: parent, Name: id, Settings: s}
}

func conn(id string, folderID *string, hostname string, ov store.InheritableSettings) store.Connection {
	return store.Connection{ID: id, FolderID: folderID, Name: id, Hostname: hostname, Overrides: ov}
}

func TestRootFolderScalarInherited(t *testing.T) {
	f := folder("root", nil, store.InheritableSettings{
		Username: ptr("ops"),
		Port:     ptr(uint16(2222)),
	})
	c := conn("c1", ptr("root"), "host.example", store.InheritableSettings{})
	r := ResolveWith(c, []store.Folder{f})
	if r.Username == nil || *r.Username != "ops" {
		t.Fatalf("username: got %v", r.Username)
	}
	if r.Port != 2222 {
		t.Fatalf("port: got %d", r.Port)
	}
	if r.Hostname != "host.example" {
		t.Fatalf("hostname: got %s", r.Hostname)
	}
}

func TestChildOverridesParentScalar(t *testing.T) {
	root := folder("root", nil, store.InheritableSettings{
		Port:     ptr(uint16(22)),
		Username: ptr("root_user"),
	})
	child := folder("child", ptr("root"), store.InheritableSettings{
		Username: ptr("child_user"),
	})
	c := conn("c1", ptr("child"), "h", store.InheritableSettings{})
	r := ResolveWith(c, []store.Folder{root, child})
	if r.Port != 22 {
		t.Fatalf("port: got %d", r.Port)
	}
	if *r.Username != "child_user" {
		t.Fatalf("username: got %s", *r.Username)
	}
}

func TestConnectionOverridesEverything(t *testing.T) {
	root := folder("root", nil, store.InheritableSettings{
		Port:     ptr(uint16(22)),
		Username: ptr("ops"),
	})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{
		Port:     ptr(uint16(2200)),
		Username: ptr("admin"),
	})
	r := ResolveWith(c, []store.Folder{root})
	if r.Port != 2200 {
		t.Fatalf("port: got %d", r.Port)
	}
	if *r.Username != "admin" {
		t.Fatalf("username: got %s", *r.Username)
	}
}

func TestSSHOptionsDeepMerge(t *testing.T) {
	parentOpts := map[string]string{
		"StrictHostKeyChecking": "ask",
		"ServerAliveInterval":   "60",
	}
	connOpts := map[string]string{
		"ServerAliveInterval": "30",
		"Compression":         "yes",
	}
	f := folder("root", nil, store.InheritableSettings{SSHOptions: parentOpts})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{SSHOptions: connOpts})
	r := ResolveWith(c, []store.Folder{f})
	if r.SSHOptions["StrictHostKeyChecking"] != "ask" {
		t.Fatalf("StrictHostKeyChecking: got %s", r.SSHOptions["StrictHostKeyChecking"])
	}
	if r.SSHOptions["ServerAliveInterval"] != "30" {
		t.Fatalf("ServerAliveInterval: got %s", r.SSHOptions["ServerAliveInterval"])
	}
	if r.SSHOptions["Compression"] != "yes" {
		t.Fatalf("Compression: got %s", r.SSHOptions["Compression"])
	}
}

func TestJumpHostInheritedAtomic(t *testing.T) {
	spec := &store.JumpHostSpec{
		Hostname: "bastion",
		Username: ptr("jump"),
		AuthRef:  ptr("cred-a"),
	}
	f := folder("root", nil, store.InheritableSettings{
		JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: spec},
	})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{})
	r := ResolveWith(c, []store.Folder{f})
	if r.JumpHost == nil {
		t.Fatal("jump_host: nil")
	}
	if r.JumpHost.Hostname != "bastion" {
		t.Fatalf("hostname: got %s", r.JumpHost.Hostname)
	}
}

func TestJumpHostExplicitNoneStripsInherited(t *testing.T) {
	spec := &store.JumpHostSpec{Hostname: "bastion"}
	f := folder("root", nil, store.InheritableSettings{
		JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: spec},
	})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{
		JumpHost: &store.JumpHostOverride{Kind: "none"},
	})
	r := ResolveWith(c, []store.Folder{f})
	if r.JumpHost != nil {
		t.Fatalf("jump_host should be nil, got %+v", r.JumpHost)
	}
}

func TestJumpHostAtomicReplace(t *testing.T) {
	parent := &store.JumpHostSpec{Hostname: "bastion-old", AuthRef: ptr("cred-old")}
	child := &store.JumpHostSpec{
		Hostname: "bastion-new", Port: ptr(uint16(2222)),
		Username: ptr("ops"), AuthRef: ptr("cred-new"),
	}
	f := folder("root", nil, store.InheritableSettings{
		JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: parent},
	})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{
		JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: child},
	})
	r := ResolveWith(c, []store.Folder{f})
	if r.JumpHost == nil || r.JumpHost.Hostname != "bastion-new" {
		t.Fatalf("wrong jump host: %+v", r.JumpHost)
	}
	if r.JumpHost.AuthRef == nil || *r.JumpHost.AuthRef != "cred-new" {
		t.Fatalf("wrong jump auth")
	}
}

func TestDefaultsApply(t *testing.T) {
	c := conn("c1", nil, "h", store.InheritableSettings{})
	r := ResolveWith(c, nil)
	if r.Port != 22 {
		t.Fatalf("port: %d", r.Port)
	}
	if r.KeepaliveInterval != 0 {
		t.Fatalf("keepalive: %d", r.KeepaliveInterval)
	}
	if r.TerminalType != "xterm-256color" {
		t.Fatalf("term: %s", r.TerminalType)
	}
	if r.InitialCommand != "" {
		t.Fatalf("initial command should default empty, got %q", r.InitialCommand)
	}
	if !r.ProbeLiveness {
		t.Fatalf("probe_liveness should default true when unset")
	}
}

func TestProbeLivenessInheritedAndOverridden(t *testing.T) {
	// A folder opts out of probing; a child connection with no override
	// inherits the opt-out.
	f := folder("root", nil, store.InheritableSettings{
		ProbeLiveness: ptr(false),
	})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{})
	if r := ResolveWith(c, []store.Folder{f}); r.ProbeLiveness {
		t.Fatalf("child should inherit folder probe=off")
	}
	// A connection override wins over the folder opt-out.
	c2 := conn("c2", ptr("root"), "h", store.InheritableSettings{ProbeLiveness: ptr(true)})
	if r := ResolveWith(c2, []store.Folder{f}); !r.ProbeLiveness {
		t.Fatalf("connection override probe=on should win over folder off")
	}
}

func TestInitialCommandInheritedAndOverridden(t *testing.T) {
	// A folder sets an initial command; a child connection with no override
	// inherits it.
	f := folder("root", nil, store.InheritableSettings{
		InitialCommand: ptr("cd /var/www"),
	})
	c := conn("c1", ptr("root"), "h", store.InheritableSettings{})
	r := ResolveWith(c, []store.Folder{f})
	if r.InitialCommand != "cd /var/www" {
		t.Fatalf("inherited initial command: got %q", r.InitialCommand)
	}

	// A connection override wins over the folder.
	c2 := conn("c2", ptr("root"), "h", store.InheritableSettings{
		InitialCommand: ptr("tmux new -A -s main"),
	})
	r2 := ResolveWith(c2, []store.Folder{f})
	if r2.InitialCommand != "tmux new -A -s main" {
		t.Fatalf("override initial command: got %q", r2.InitialCommand)
	}

	// An explicit "" override breaks the inherited command.
	c3 := conn("c3", ptr("root"), "h", store.InheritableSettings{
		InitialCommand: ptr(""),
	})
	r3 := ResolveWith(c3, []store.Folder{f})
	if r3.InitialCommand != "" {
		t.Fatalf("explicit-empty should strip inherited, got %q", r3.InitialCommand)
	}
}

func TestDeepInheritanceThreeLevels(t *testing.T) {
	g := folder("g", nil, store.InheritableSettings{
		Port:     ptr(uint16(2222)),
		Username: ptr("a"),
	})
	p := folder("p", ptr("g"), store.InheritableSettings{
		Username: ptr("b"),
	})
	l := folder("l", ptr("p"), store.InheritableSettings{
		AuthRef: ptr("cred-x"),
	})
	c := conn("c", ptr("l"), "host", store.InheritableSettings{})
	r := ResolveWith(c, []store.Folder{g, p, l})
	if r.Port != 2222 || *r.Username != "b" || *r.AuthRef != "cred-x" {
		t.Fatalf("wrong: port=%d user=%s auth=%v", r.Port, *r.Username, r.AuthRef)
	}
}

func TestNetworkProfileInheritedAndBroken(t *testing.T) {
	f := folder("root", nil, store.InheritableSettings{
		NetworkProfileID: ptr("wg-prod"),
	})
	// Child with no override inherits the folder's profile.
	c1 := conn("c1", ptr("root"), "h", store.InheritableSettings{})
	r1 := ResolveWith(c1, []store.Folder{f})
	if r1.NetworkProfileID == nil || *r1.NetworkProfileID != "wg-prod" {
		t.Fatalf("expected inherited wg-prod, got %v", r1.NetworkProfileID)
	}
	// Explicit "" breaks the inheritance -> normalized to nil (direct).
	c2 := conn("c2", ptr("root"), "h", store.InheritableSettings{
		NetworkProfileID: ptr(""),
	})
	r2 := ResolveWith(c2, []store.Folder{f})
	if r2.NetworkProfileID != nil {
		t.Fatalf("expected nil (explicit direct), got %q", *r2.NetworkProfileID)
	}
	// Child override replaces the parent's profile.
	c3 := conn("c3", ptr("root"), "h", store.InheritableSettings{
		NetworkProfileID: ptr("wg-lab"),
	})
	r3 := ResolveWith(c3, []store.Folder{f})
	if r3.NetworkProfileID == nil || *r3.NetworkProfileID != "wg-lab" {
		t.Fatalf("expected wg-lab, got %v", r3.NetworkProfileID)
	}
}

// The per-line delay follows the same inheritance rules as the command it
// paces, with one wrinkle the other numeric settings do not have: 0 is a
// meaningful value (send the lines back to back) rather than "unset", so an
// override of 0 has to survive a non-zero folder value.
func TestInitialCommandLineDelayInheritedAndOverridden(t *testing.T) {
	f := folder("root", nil, store.InheritableSettings{
		InitialCommand:            ptr("sudo su - deploy\nwhoami"),
		InitialCommandLineDelayMs: ptr(uint32(500)),
	})

	// No override: inherit the folder's pause.
	r := ResolveWith(conn("c1", ptr("root"), "h", store.InheritableSettings{}), []store.Folder{f})
	if r.InitialCommandLineDelayMs != 500 {
		t.Fatalf("inherited delay: got %d, want 500", r.InitialCommandLineDelayMs)
	}

	// A connection override wins.
	c2 := conn("c2", ptr("root"), "h", store.InheritableSettings{
		InitialCommandLineDelayMs: ptr(uint32(1200)),
	})
	if got := ResolveWith(c2, []store.Folder{f}).InitialCommandLineDelayMs; got != 1200 {
		t.Fatalf("override delay: got %d, want 1200", got)
	}

	// An explicit 0 means "no pause here", not "fall back to the folder".
	c3 := conn("c3", ptr("root"), "h", store.InheritableSettings{
		InitialCommandLineDelayMs: ptr(uint32(0)),
	})
	if got := ResolveWith(c3, []store.Folder{f}).InitialCommandLineDelayMs; got != 0 {
		t.Fatalf("explicit zero should override the folder's 500, got %d", got)
	}

	// Nothing set anywhere: no pause, which is what every connection did
	// before this setting existed.
	bare := folder("bare", nil, store.InheritableSettings{})
	r4 := ResolveWith(conn("c4", ptr("bare"), "h", store.InheritableSettings{}), []store.Folder{bare})
	if r4.InitialCommandLineDelayMs != 0 {
		t.Fatalf("unset delay should be 0, got %d", r4.InitialCommandLineDelayMs)
	}
}

// "None" on a connection: an explicit "" credential stops the folder's from
// being inherited and resolves to no credential at all.
func TestEmptyAuthRefStopsInheritance(t *testing.T) {
	f := folder("f", nil, store.InheritableSettings{AuthRef: ptr("cred-folder")})
	c := conn("c", ptr("f"), "h.example.com", store.InheritableSettings{AuthRef: ptr("")})
	if rs := ResolveWith(c, []store.Folder{f}); rs.AuthRef != nil {
		t.Errorf("auth ref = %q, want nil", *rs.AuthRef)
	}
	c2 := conn("c2", ptr("f"), "h.example.com", store.InheritableSettings{})
	if rs := ResolveWith(c2, []store.Folder{f}); rs.AuthRef == nil || *rs.AuthRef != "cred-folder" {
		t.Errorf("inherited auth ref = %v", rs.AuthRef)
	}
}

// The one-time code source inherits on its own, independent of the auth
// credential, and "" stops it.
func TestOTPRefInheritedIndependently(t *testing.T) {
	f := folder("f", nil, store.InheritableSettings{OTPRef: ptr("cred-bw")})
	c := conn("c", ptr("f"), "h.example.com", store.InheritableSettings{AuthRef: ptr("cred-key")})
	rs := ResolveWith(c, []store.Folder{f})
	if rs.OTPRef == nil || *rs.OTPRef != "cred-bw" || rs.AuthRef == nil || *rs.AuthRef != "cred-key" {
		t.Fatalf("otp=%v auth=%v", rs.OTPRef, rs.AuthRef)
	}
	c2 := conn("c2", ptr("f"), "h.example.com", store.InheritableSettings{OTPRef: ptr("")})
	if rs := ResolveWith(c2, []store.Folder{f}); rs.OTPRef != nil {
		t.Errorf("otp ref = %q, want nil", *rs.OTPRef)
	}
}

// A pasted hostname with a trailing newline used to reach the dialer as
// "10.0.0.5\n:22" ("no such host").
func TestResolveTrimsHostname(t *testing.T) {
	rs := ResolveWith(store.Connection{Hostname: " 10.0.0.5\r\n"}, nil)
	if rs.Hostname != "10.0.0.5" {
		t.Fatalf("hostname = %q", rs.Hostname)
	}
}

func TestTunnelOnlyInheritedAndOverridden(t *testing.T) {
	// A folder of tunnels: children inherit tunnel_only and autostart.
	f := folder("tunnels", nil, store.InheritableSettings{
		TunnelOnly:      ptr(true),
		TunnelAutostart: ptr(true),
	})
	c := conn("c1", ptr("tunnels"), "h", store.InheritableSettings{})
	if r := ResolveWith(c, []store.Folder{f}); !r.TunnelOnly || !r.TunnelAutostart {
		t.Fatalf("child should inherit tunnel_only + autostart, got %v %v", r.TunnelOnly, r.TunnelAutostart)
	}
	// One connection in the folder opts out of the launch start only.
	c2 := conn("c2", ptr("tunnels"), "h", store.InheritableSettings{TunnelAutostart: ptr(false)})
	if r := ResolveWith(c2, []store.Folder{f}); !r.TunnelOnly || r.TunnelAutostart {
		t.Fatalf("autostart override should win, got %v %v", r.TunnelOnly, r.TunnelAutostart)
	}
	// Unset everywhere resolves off: a plain connection opens a terminal.
	c3 := conn("c3", nil, "h", store.InheritableSettings{})
	if r := ResolveWith(c3, nil); r.TunnelOnly || r.TunnelAutostart {
		t.Fatalf("default should be off, got %v %v", r.TunnelOnly, r.TunnelAutostart)
	}
}

func TestKafkaSettingsInherited(t *testing.T) {
	// A folder holding a cluster's hosts sets Kafka once.
	f := folder("kafka", nil, store.InheritableSettings{
		KafkaEnabled:   ptr(true),
		KafkaBootstrap: ptr(" 127.0.0.1:9092\n"),
	})
	c := conn("c1", ptr("kafka"), "h", store.InheritableSettings{})
	r := ResolveWith(c, []store.Folder{f})
	if !r.KafkaEnabled || r.KafkaBootstrap != "127.0.0.1:9092" || !r.KafkaUseTunnel {
		t.Fatalf("child should inherit kafka, through the tunnel by default: %v %q %v",
			r.KafkaEnabled, r.KafkaBootstrap, r.KafkaUseTunnel)
	}
	// One host reaches the brokers directly and names its own seed.
	c2 := conn("c2", ptr("kafka"), "h", store.InheritableSettings{
		KafkaUseTunnel: ptr(false),
		KafkaBootstrap: ptr("b1:9093"),
	})
	r = ResolveWith(c2, []store.Folder{f})
	if !r.KafkaEnabled || r.KafkaBootstrap != "b1:9093" || r.KafkaUseTunnel {
		t.Fatalf("overrides should win: %v %q %v", r.KafkaEnabled, r.KafkaBootstrap, r.KafkaUseTunnel)
	}
	// Unset everywhere: off, no bootstrap, writable, plaintext.
	r = ResolveWith(conn("c3", nil, "h", store.InheritableSettings{}), nil)
	if r.KafkaEnabled || r.KafkaBootstrap != "" || r.KafkaReadOnly || r.KafkaTLS || r.KafkaSASL != "" || r.KafkaCredentialRef != nil {
		t.Fatalf("default should be off: %+v", r)
	}
	// A production folder: read-only, SCRAM over TLS with a shared login.
	prod := folder("prod", nil, store.InheritableSettings{
		KafkaReadOnly:      ptr(true),
		KafkaTLS:           ptr(true),
		KafkaSASL:          ptr("scram-sha-512"),
		KafkaCredentialRef: ptr("cred-1"),
	})
	r = ResolveWith(conn("c4", ptr("prod"), "h", store.InheritableSettings{}), []store.Folder{prod})
	if !r.KafkaReadOnly || !r.KafkaTLS || r.KafkaSASL != "scram-sha-512" || r.KafkaCredentialRef == nil || *r.KafkaCredentialRef != "cred-1" {
		t.Fatalf("prod folder not inherited: %+v", r)
	}
	// "" on the connection stops the inherited credential.
	r = ResolveWith(conn("c5", ptr("prod"), "h", store.InheritableSettings{KafkaCredentialRef: ptr("")}), []store.Folder{prod})
	if r.KafkaCredentialRef != nil {
		t.Fatalf("explicit none should clear the credential: %v", *r.KafkaCredentialRef)
	}
}

func TestDbSettingsInherited(t *testing.T) {
	// A folder of MySQL hosts sets engine and login once.
	f := folder("dbs", nil, store.InheritableSettings{
		DbEnabled:       ptr(true),
		DbEngine:        ptr("mysql"),
		DbCredentialRef: ptr("cred-db"),
		DbReadOnly:      ptr(true),
	})
	r := ResolveWith(conn("c1", ptr("dbs"), "h", store.InheritableSettings{}), []store.Folder{f})
	if !r.DbEnabled || r.DbEngine != "mysql" || !r.DbUseTunnel || !r.DbReadOnly || r.DbCredentialRef == nil || *r.DbCredentialRef != "cred-db" {
		t.Fatalf("child should inherit the database settings: %+v", r)
	}
	// One host has its own address and database, directly, and is writable.
	r = ResolveWith(conn("c2", ptr("dbs"), "h", store.InheritableSettings{
		DbAddr: ptr(" 10.0.0.5:3307 "), DbName: ptr("app"), DbUseTunnel: ptr(false), DbReadOnly: ptr(false),
		DbCredentialRef: ptr(""),
	}), []store.Folder{f})
	if r.DbAddr != "10.0.0.5:3307" || r.DbName != "app" || r.DbUseTunnel || r.DbReadOnly || r.DbCredentialRef != nil {
		t.Fatalf("overrides should win: %+v", r)
	}
	// Unset everywhere: off, Postgres, plaintext, through the tunnel.
	r = ResolveWith(conn("c3", nil, "h", store.InheritableSettings{}), nil)
	if r.DbEnabled || r.DbEngine != "postgres" || r.DbTLS != "disable" || !r.DbUseTunnel || r.DbAddr != "" {
		t.Fatalf("defaults: %+v", r)
	}
}
