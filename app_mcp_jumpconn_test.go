package main

import (
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// A bastion staged in the same plan can be the jump host of a folder created
// earlier and of connections created before or after it: the tmp: reference
// resolves to the bastion's real id at commit.
func TestPlanJumpConnectionToStagedBastion(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	f, err := a.planAddFolder("Behind bastion", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.planAddConnection(planConnInput{Name: "early", Host: "early.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.planAddConnection(planConnInput{Name: "bastion", Host: "bastion.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.planSetFolderSettings("tmp:"+f, folderSettingsInput{JumpConnection: "tmp:" + b}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.planAddConnection(planConnInput{Name: "late", Host: "late.example.com", JumpConnection: "tmp:" + b}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.planAddConnection(planConnInput{Name: "x", Host: "x.example.com", JumpHost: "j.example.com", JumpConnection: "tmp:" + b}); err == nil {
		t.Fatal("jump_host and jump_connection together accepted")
	}

	pv := a.buildPlanPreview(a.getOrInitPlan())
	joined := ""
	for _, c := range pv.Connections {
		joined += c.Via + "\n"
	}
	if !strings.Contains(joined, "bastion (created in this plan)") {
		t.Errorf("preview via = %q", joined)
	}

	if _, err := a.writePlan(a.getOrInitPlan()); err != nil {
		t.Fatal(err)
	}
	conns, _ := a.db.ListConnections(nil)
	byName := map[string]store.Connection{}
	for _, c := range conns {
		byName[c.Name] = c
	}
	bid := byName["bastion"].ID
	late := byName["late"]
	if late.Overrides.JumpHost == nil || *late.Overrides.JumpHost.Chain.ConnectionID != bid {
		t.Fatalf("late jump = %+v, want ref %s", late.Overrides.JumpHost, bid)
	}
	folders, _ := a.db.ListFolders()
	var fs *store.Folder
	for i := range folders {
		if folders[i].Name == "Behind bastion" {
			fs = &folders[i]
		}
	}
	if fs == nil || fs.Settings.JumpHost == nil || *fs.Settings.JumpHost.Chain.ConnectionID != bid {
		t.Fatalf("folder jump not resolved: %+v", fs)
	}
}

// Existing ids are checked when staged; an inline bastion that matches a
// saved connection gets a hint pointing at it.
func TestPlanJumpConnectionExistingAndHint(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	saved, err := a.db.CreateConnection(store.NewConnection{Name: "edge", Hostname: "edge.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.planAddConnection(planConnInput{Name: "a", Host: "a.example.com", JumpConnection: "no-such-id"}); err == nil {
		t.Fatal("unknown connection id accepted")
	}
	id, err := a.planAddConnection(planConnInput{Name: "b", Host: "b.example.com", JumpHost: "EDGE.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if h := a.jumpRepeatHint(id); !strings.Contains(h, "jump_connection "+saved.ID) {
		t.Errorf("hint = %q", h)
	}
	if err := a.planEditConnection(editConnInput{ConnID: saved.ID, JumpConnection: &saved.ID}); err == nil {
		t.Error("a connection as its own jump host accepted")
	}
}

// A sensitive connection is off limits to the LLM in every plan tool, not
// only connect: no edit, no forward on it, no hop through it, and its name
// does not come back in a hint.
func TestPlanRefusesSensitiveConnection(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	c, err := a.db.CreateConnection(store.NewConnection{Name: "vault-db", Hostname: "db.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := a.db.UpdateConnection(store.UpdateConnection{ID: c.ID, Sensitive: &yes}); err != nil {
		t.Fatal(err)
	}
	host := "evil.example.com"
	if err := a.planEditConnection(editConnInput{ConnID: c.ID, Host: &host}); err != errSensitiveConn {
		t.Errorf("edit: err = %v", err)
	}
	if _, err := a.planAddForward(c.ID, "local", "", 0, "10.0.0.1", 5432, false, ""); err != errSensitiveConn {
		t.Errorf("forward: err = %v", err)
	}
	if _, err := a.planAddConnection(planConnInput{Name: "x", Host: "x.example.com", JumpConnection: c.ID}); err == nil {
		t.Error("jump through a sensitive connection accepted")
	}
	if id, name := a.savedConnAt("db.example.com", 22); id != "" || name != "" {
		t.Errorf("savedConnAt named a sensitive connection: %s %s", id, name)
	}
}

func TestJumpsThroughSensitive(t *testing.T) {
	a := newResolveTestApp(t)
	bastion, _ := a.db.CreateConnection(store.NewConnection{Name: "bastion", Hostname: "bastion.example.com"})
	yes := true
	if _, err := a.db.UpdateConnection(store.UpdateConnection{ID: bastion.ID, Sensitive: &yes}); err != nil {
		t.Fatal(err)
	}
	mid, _ := a.db.CreateConnection(store.NewConnection{Name: "mid", Hostname: "mid.example.com",
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain",
			Chain: &store.JumpHostSpec{ConnectionID: &bastion.ID}}}})
	inner, _ := a.db.CreateConnection(store.NewConnection{Name: "inner", Hostname: "inner.example.com",
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain",
			Chain: &store.JumpHostSpec{ConnectionID: &mid.ID}}}})
	plain, _ := a.db.CreateConnection(store.NewConnection{Name: "plain", Hostname: "plain.example.com"})

	if !a.jumpsThroughSensitive(mid.ID) {
		t.Error("direct hop through a sensitive bastion not caught")
	}
	if !a.jumpsThroughSensitive(inner.ID) {
		t.Error("sensitive bastion two references deep not caught")
	}
	if a.jumpsThroughSensitive(plain.ID) {
		t.Error("plain connection flagged")
	}
}
