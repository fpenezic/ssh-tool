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
