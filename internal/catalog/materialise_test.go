package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

func newTranslator(in *store.CatalogInputsTx) *translator {
	if in.Slots == nil {
		in.Slots = map[string]store.CatalogSlot{}
	}
	if in.Bindings == nil {
		in.Bindings = map[string]store.CatalogBinding{}
	}
	return &translator{catID: "cat", in: in}
}

func TestTranslateReferences(t *testing.T) {
	tr := newTranslator(&store.CatalogInputsTx{
		Slots: map[string]store.CatalogSlot{
			"cred-bound":   {UUID: "cred-bound", Type: "cred", Kind: "slot"},
			"cred-free":    {UUID: "cred-free", Type: "cred", Kind: "slot"},
			"cred-deadtgt": {UUID: "cred-deadtgt", Type: "cred", Kind: "slot"},
			"net-bound":    {UUID: "net-bound", Type: "net"},
			"jump-bound":   {UUID: "jump-bound", Type: "jump"},
			"jump-free":    {UUID: "jump-free", Type: "jump"},
		},
		Bindings: map[string]store.CatalogBinding{
			"cred-bound":   {SlotUUID: "cred-bound", Kind: "cred", TargetID: "my-cred"},
			"cred-deadtgt": {SlotUUID: "cred-deadtgt", Kind: "cred", TargetID: "deleted-cred"},
			"net-bound":    {SlotUUID: "net-bound", Kind: "net", TargetID: "my-vpn"},
			"jump-bound":   {SlotUUID: "jump-bound", Kind: "jump", TargetID: "dyn:f1/101"},
		},
		ConnIDs:     map[string]string{"bastion-uuid": "bastion-local"},
		Credentials: map[string]bool{"my-cred": true},
		Profiles:    map[string]bool{"my-vpn": true},
		Connections: map[string]bool{"bastion-local": true},
	})
	raw := `{
		"auth_ref": "cred-bound", "otp_ref": "cred-free", "kafka_credential_ref": "", "db_credential_ref": "cred-deadtgt",
		"network_profile_id": "net-bound", "tunnel_autostart": true, "broadcast_group_id": "g", "port": 2222,
		"db_targets": [{"id": "t1", "name": "prod", "credential_ref": "cred-bound"}, {"id": "t2", "name": "x", "credential_ref": "unknown-slot"}],
		"jump_host": {"kind": "chain", "chain": {"jump_slot": "jump-free", "name": "x",
			"via": {"connection_id": "bastion-uuid", "auth_ref": "net-bound",
				"via": {"connection_id": "gone-uuid", "via": {"jump_slot": "jump-bound"}}}}},
		"future_key": 1
	}`
	s, err := tr.translate(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	unbound := func(slot string) string { return store.CatalogUnboundRef("cat", slot) }
	check := func(name string, got *string, want string) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %q", name, got, want)
		}
	}
	check("auth_ref (bound)", s.AuthRef, "my-cred")
	check("otp_ref (unbound)", s.OTPRef, unbound("cred-free"))
	check("kafka_credential_ref (explicit none)", s.KafkaCredentialRef, "")
	check("db_credential_ref (target deleted)", s.DbCredentialRef, unbound("cred-deadtgt"))
	check("network_profile_id", s.NetworkProfileID, "my-vpn")
	if s.TunnelAutostart != nil {
		t.Error("tunnel_autostart from the catalog was kept: it may only start things once enabled locally")
	}
	if s.BroadcastGroupID != nil {
		t.Error("broadcast_group_id kept")
	}
	check("db_targets[0]", s.DbTargets[0].CredentialRef, "my-cred")
	check("db_targets[1] (slot not held)", s.DbTargets[1].CredentialRef, store.CatalogMissingRef("cat", "unknown-slot"))
	h := s.JumpHost.Chain
	check("hop 1 jump slot unbound", h.ConnectionID, unbound("jump-free"))
	if h.Name != "" {
		t.Error("hop name kept")
	}
	check("hop 2 catalog connection", h.Via.ConnectionID, "bastion-local")
	check("hop 2 auth_ref with a net slot", h.Via.AuthRef, unbound("net-bound"))
	check("hop 3 connection not held", h.Via.Via.ConnectionID, store.CatalogMissingRef("cat", "gone-uuid"))
	check("hop 4 jump slot bound to an inventory host", h.Via.Via.Via.ConnectionID, "dyn:f1/101")
}

func TestOverlay(t *testing.T) {
	str := func(s string) *string { return &s }
	port := uint16(2222)
	f := false
	base := store.InheritableSettings{
		Username: str("deploy"), Port: &port, SSHOptions: map[string]string{"a": "1", "b": "2"},
		DbTargets: []store.DbTarget{{ID: "prod", Name: "prod", Database: str("app")}},
		JumpHost:  &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{Hostname: "bastion"}},
		DbLabel:   str("Main"), Sensitive: func() *bool { b := true; return &b }(),
	}
	layer := store.InheritableSettings{
		Username: str("me"), SSHOptions: map[string]string{"b": "x", "c": "3"},
		DbTargets: []store.DbTarget{{ID: "prod", Database: str("app_ro")}, {ID: "mine", Name: "scratch", Database: str("s")}},
		JumpHost:  &store.JumpHostOverride{Kind: "none"}, Sensitive: &f,
	}
	o := Overlay(base, layer)
	if *o.Username != "me" || *o.Port != 2222 {
		t.Errorf("scalars: %v %v", *o.Username, *o.Port)
	}
	if o.SSHOptions["a"] != "1" || o.SSHOptions["b"] != "x" || o.SSHOptions["c"] != "3" {
		t.Errorf("maps: %v", o.SSHOptions)
	}
	if len(o.DbTargets) != 2 || *o.DbTargets[0].Database != "app_ro" || o.DbTargets[1].ID != "mine" {
		t.Errorf("db targets: %+v", o.DbTargets)
	}
	if o.JumpHost.Kind != "none" {
		t.Error("jump chain not replaced whole")
	}
	if o.DbLabel == nil || *o.DbLabel != "Main" {
		t.Error("an empty layer field wiped a catalog value")
	}
	if o.Sensitive == nil || *o.Sensitive {
		t.Error("explicit false in the layer did not replace the catalog's true")
	}
	if base.SSHOptions["b"] != "2" {
		t.Error("overlay changed its input")
	}
}

// materialised: a catalog synced through the fake feed, then bound and
// layered.
func TestMaterialiseRowsAndRebinding(t *testing.T) {
	e := newSyncEnv(t)
	cred, err := e.db.CreateCredential(store.NewCredential{Name: "my-key", Kind: store.CredAgent})
	if err != nil {
		t.Fatal(err)
	}
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, map[string]any{"uuid": "space", "rev": 1, "parent_uuid": nil, "name": "Space", "access": "skeleton",
			"settings": map[string]any{"auth_ref": "s-admin", "tunnel_autostart": true}}),
		up(KindSlots, map[string]any{"uuid": "s-admin", "rev": 1, "folder_uuid": "space", "type": "cred", "name": "admin", "kind": "slot"}),
		up(KindConnections, connRec("web", 1, "space", "web-1")),
		up(KindForwards, fwdRec("fw", 1, "web")),
	}}})
	e.sync()
	web := e.conn("web")
	sp := e.folder("space")
	if *web.Overrides.Port != 2222 {
		t.Fatalf("connection settings not materialised: %+v", web.Overrides)
	}
	if sp.Settings.AuthRef == nil || *sp.Settings.AuthRef != store.CatalogUnboundRef(e.cat.ID, "s-admin") {
		t.Fatalf("unbound slot not marked: %+v", sp.Settings.AuthRef)
	}
	if sp.Settings.TunnelAutostart != nil {
		t.Fatal("catalog tunnel_autostart materialised")
	}
	// Bind the slot; materialise again.
	if err := e.db.SetCatalogBinding(e.cat.ID, "s-admin", "cred", cred.ID); err != nil {
		t.Fatal(err)
	}
	// A layer on the skeleton folder is ignored; one on the connection and
	// the forward applies.
	_ = e.db.SetCatalogOverride(e.cat.ID, "space", "folders", `{"username":"nope"}`)
	_ = e.db.SetCatalogOverride(e.cat.ID, "web", "connections", `{"username":"me","tunnel_autostart":true}`)
	_ = e.db.SetCatalogOverride(e.cat.ID, "fw", "forwards", `{"local_port":25432}`)
	if err := Materialise(e.db, e.cat.ID, nil); err != nil {
		t.Fatal(err)
	}
	sp, web = e.folder("space"), e.conn("web")
	if *sp.Settings.AuthRef != cred.ID || sp.Settings.Username != nil {
		t.Fatalf("folder after binding: %+v", sp.Settings)
	}
	if *web.Overrides.Username != "me" || web.Overrides.TunnelAutostart == nil || !*web.Overrides.TunnelAutostart {
		t.Fatalf("connection layer (tunnel autostart enabled locally is allowed): %+v", web.Overrides)
	}
	if f := e.fwd("fw"); *f.LocalPort != 25432 {
		t.Fatalf("forward local port layer: %v", *f.LocalPort)
	}
	rs, err := resolver.ResolveConnection(e.db, web.ID)
	if err != nil || rs.AuthRef == nil || *rs.AuthRef != cred.ID || *rs.Username != "me" {
		t.Fatalf("resolved: %+v %v", rs, err)
	}
	// The bound credential is deleted: back to the marker, never nil.
	if err := e.db.DeleteCredential(cred.ID); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	if err := Materialise(e.db, e.cat.ID, nil); err != nil {
		t.Fatal(err)
	}
	if sp = e.folder("space"); *sp.Settings.AuthRef != store.CatalogUnboundRef(e.cat.ID, "s-admin") {
		t.Fatalf("deleted binding target: %v", *sp.Settings.AuthRef)
	}
	// Local columns stay writable after materialisation (the encodings
	// match, so the full-row rewrite passes the guard).
	fav := true
	if _, err := e.db.UpdateConnection(store.UpdateConnection{ID: web.ID, Favorite: &fav}); err != nil {
		t.Fatalf("favourite on a materialised row: %v", err)
	}
}

type nopSink struct{}

func (nopSink) EmitState(string, sshlayer.SessionState) {}
func (nopSink) EmitOutput(string, []byte, uint64)       {}
func (nopSink) EmitExitStatus(string, uint32)           {}
func (nopSink) EmitDebug(string, string)                {}

// Every path a marker can reach while connecting ends in a typed error,
// before anything is dialled, and never in another credential, the agent,
// a direct route or another bastion.
func TestMarkersNeverFallBackAtConnect(t *testing.T) {
	// A listener standing in for the target: nothing may reach it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&accepted, 1)
			c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	cred := func(s string) *string { return &s }

	e := newSyncEnv(t)
	host := "127.0.0.1"
	settings := func(extra map[string]any) map[string]any {
		s := map[string]any{"port": addr.Port, "username": "u"}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	conn := func(uuid string, s map[string]any) map[string]any {
		r := connRec(uuid, 1, "space", uuid)
		r["hostname"], r["settings"] = host, s
		return r
	}
	agent, _ := e.db.CreateCredential(store.NewCredential{Name: "agent", Kind: store.CredAgent})
	_ = cred
	e.fs.serve(map[string]Feed{"": {Reset: true, Cursor: "c1", Items: []Item{
		up(KindFolders, folderRec("space", 1, nil, "Space")),
		up(KindSlots, map[string]any{"uuid": "s-cred", "rev": 1, "folder_uuid": "space", "type": "cred", "name": "c", "kind": "slot"}),
		up(KindSlots, map[string]any{"uuid": "s-net", "rev": 1, "folder_uuid": "space", "type": "net", "name": "n"}),
		up(KindSlots, map[string]any{"uuid": "s-jump", "rev": 1, "folder_uuid": "space", "type": "jump", "name": "j"}),
		up(KindSlots, map[string]any{"uuid": "s-bound", "rev": 1, "folder_uuid": "space", "type": "cred", "name": "b", "kind": "slot"}),
		up(KindConnections, conn("auth", settings(map[string]any{"auth_ref": "s-cred"}))),
		// No credential (explicit none) and, below, a per-connection
		// password: the connect reaches the first-hop dial, where the
		// network marker must stop it.
		up(KindConnections, conn("net", settings(map[string]any{"auth_ref": "", "network_profile_id": "s-net"}))),
		up(KindConnections, conn("jumpslot", settings(map[string]any{"auth_ref": "s-bound",
			"jump_host": map[string]any{"kind": "chain", "chain": map[string]any{"jump_slot": "s-jump"}}}))),
		up(KindConnections, conn("jumpgone", settings(map[string]any{"auth_ref": "s-bound",
			"jump_host": map[string]any{"kind": "chain", "chain": map[string]any{"connection_id": "not-held"}}}))),
		up(KindConnections, conn("hopauth", settings(map[string]any{"auth_ref": "s-bound",
			"jump_host": map[string]any{"kind": "chain", "chain": map[string]any{"hostname": host, "port": addr.Port, "auth_ref": "s-cred"}}}))),
	}}})
	if err := e.db.SetCatalogBinding(e.cat.ID, "s-bound", "cred", agent.ID); err != nil {
		t.Fatal(err)
	}
	e.sync()

	// The app's first-hop hook starts with the profile lookup.
	sshlayer.FirstHopDialerHook = func(ctx context.Context, s *store.ResolvedSettings) (sshlayer.ContextDialer, error) {
		if _, err := e.db.GetNetworkProfile(*s.NetworkProfileID); err != nil {
			return nil, err
		}
		return nil, errors.New("test: no tunnel")
	}
	defer func() { sshlayer.FirstHopDialerHook = nil }()

	for _, tc := range []struct {
		uuid, what string
		atResolve  bool
	}{
		{"auth", "credential", false},
		{"net", "network profile", false},
		{"jumpslot", "jump host", true},
		{"jumpgone", "jump host", true},
		{"hopauth", "credential", false},
	} {
		c := e.conn(tc.uuid)
		rs, err := resolver.ResolveConnection(e.db, c.ID)
		if tc.atResolve {
			var re *store.CatalogRefError
			if !errors.As(err, &re) || re.What != tc.what {
				t.Errorf("%s: resolve should fail with a %s marker error, got %v", tc.uuid, tc.what, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: resolve: %v", tc.uuid, err)
		}
		if tc.uuid == "net" {
			// A per-connection password, so authentication is settled and
			// the connect reaches the first-hop dial.
			pw := "pw"
			rs.PasswordOverride = &pw
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = sshlayer.Connect(ctx, e.db, nil, rs, nopSink{}, nil, nil, 2*time.Second, nil)
		cancel()
		var re *store.CatalogRefError
		if !errors.As(err, &re) || re.What != tc.what {
			t.Errorf("%s: connect should fail with a %s marker error, got %v", tc.uuid, tc.what, err)
		}
		if err != nil && !strings.Contains(err.Error(), "not bound") && !strings.Contains(err.Error(), "not available") {
			t.Errorf("%s: the message does not tell the user what to do: %v", tc.uuid, err)
		}
	}
	if n := atomic.LoadInt32(&accepted); n != 0 {
		t.Fatalf("%d connection(s) reached the target despite an unresolved reference", n)
	}
}
