package main

import (
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// Notes carry what a source said about a host that has no field of its own
// ("don't restart this one"). Before they existed an LLM could only turn such
// a warning into tags.
func TestPlanNotesCreateAndEdit(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	tmp, err := a.planAddConnection(planConnInput{
		Name: "rpt-legacy", Host: "10.40.1.30", Notes: "  Do not restart - finance still uses it.  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := a.getOrInitPlan()
	if pv := a.buildPlanPreview(p); len(pv.Connections) != 1 || pv.Connections[0].Notes != "Do not restart - finance still uses it." {
		t.Fatalf("preview notes: %+v", pv.Connections)
	}
	if _, err := a.writePlan(p); err != nil {
		t.Fatal(err)
	}
	a.planDiscard()

	conns, _ := a.db.ListConnections(nil)
	if len(conns) != 1 || conns[0].Notes != "Do not restart - finance still uses it." {
		t.Fatalf("stored notes: %+v (temp %s)", conns, tmp)
	}

	// Edit replaces them, and the preview shows old -> new.
	notes := "Retired 2026-10; read-only."
	if err := a.planEditConnection(editConnInput{ConnID: conns[0].ID, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	p = a.getOrInitPlan()
	pv := a.buildPlanPreview(p)
	if len(pv.Edits) != 1 || !strings.Contains(strings.Join(pv.Edits[0].Changes, "\n"), "notes:") {
		t.Fatalf("edit preview: %+v", pv.Edits)
	}
	if _, err := a.writePlan(p); err != nil {
		t.Fatal(err)
	}
	if c, _ := a.db.GetConnection(conns[0].ID); c.Notes != notes {
		t.Fatalf("edited notes = %q", c.Notes)
	}
}

// The approval modal must show everything the plan will write: an icon the
// LLM set, and whether a forward starts by itself on connect. Both used to
// be written without appearing in the preview.
func TestPlanPreviewShowsIconAndAutoStart(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	tmp, err := a.planAddConnection(planConnInput{
		Name: "pg-primary", Host: "10.40.2.10", Icon: "database", IconColor: "mauve",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.planAddForward("tmp:"+tmp, "local", "", 3000, "10.40.3.5", 3000, true, ""); err != nil {
		t.Fatal(err)
	}
	pv := a.buildPlanPreview(a.getOrInitPlan())
	c := pv.Connections[0]
	if c.IconName != "database" || c.IconColor != "mauve" {
		t.Fatalf("icon not in preview: %+v", c)
	}
	if len(c.Forwards) != 1 || !c.Forwards[0].AutoStart {
		t.Fatalf("auto-start not in preview: %+v", c.Forwards)
	}
}

// A local forward to a web UI takes an auto port and reaches it through a
// {port} bookmark; the preview must neither show ":0" nor warn that the
// bookmark is ignored (it is not - the forward panel opens it).
func TestPlanLocalForwardAutoPortWithBookmark(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	c, err := a.planAddConnection(planConnInput{Name: "app1", Host: "10.40.1.11"})
	if err != nil {
		t.Fatal(err)
	}
	fw, err := a.planAddForward("tmp:"+c, "local", "", 0, "10.40.3.5", 3000, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.planSetBookmarks("tmp:"+fw, []store.ProxyBookmark{{Name: "Grafana", URL: "http://{host}:{port}/"}}); err != nil {
		t.Fatal(err)
	}
	pv := a.buildPlanPreview(a.getOrInitPlan())
	f := pv.Connections[0].Forwards[0]
	if !strings.Contains(f.Detail, "auto port") || strings.Contains(f.Detail, ":0 ") {
		t.Fatalf("detail = %q", f.Detail)
	}
	if len(f.Bookmarks) != 1 || len(pv.Warnings) != 0 {
		t.Fatalf("bookmarks %v warnings %v", f.Bookmarks, pv.Warnings)
	}
}

// Staging the same folder twice (an LLM that lost its own temp id) is
// refused with the id it should have used; so is recreating an existing one.
func TestPlanRejectsDuplicateFolder(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	cust, err := a.db.CreateFolder(store.NewFolder{Name: "Customers"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.planAddFolder("Northwind", cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.planAddFolder("northwind", cust.ID); err == nil || !strings.Contains(err.Error(), "tmp:"+id) {
		t.Fatalf("second staging: %v", err)
	}
	if _, err := a.planAddFolder("Customers", ""); err == nil || !strings.Contains(err.Error(), cust.ID) {
		t.Fatalf("existing folder: %v", err)
	}
	// The same name under a different parent is fine.
	if _, err := a.planAddFolder("prod", "tmp:"+id); err != nil {
		t.Fatal(err)
	}
}

// Hosts behind one bastion that each carry it inline get flagged - in the
// create_connection result for the LLM and in the approval modal - even when
// other hosts in the folder (a public staging box) have no jump at all.
func TestPlanFlagsRepeatedJump(t *testing.T) {
	a := newResolveTestApp(t)
	a.mcp = newMcpState()
	a.mcp.manageStore = true

	f, _ := a.planAddFolder("Northwind", "")
	add := func(name string, jump bool) string {
		in := planConnInput{Name: name, Host: name + ".example.com", Folder: "tmp:" + f}
		if jump {
			in.JumpHost, in.JumpUser, in.JumpPort = "jump.example.com", "deploy", 2222
		}
		id, err := a.planAddConnection(in)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := add("app1", true)
	if h := a.jumpRepeatHint(first); h != "" {
		t.Fatalf("hint on the first one: %q", h)
	}
	second := add("app2", true)
	if h := a.jumpRepeatHint(second); !strings.Contains(h, "2 connections") {
		t.Fatalf("hint = %q", h)
	}
	add("staging", false)

	pv := a.buildPlanPreview(a.getOrInitPlan())
	found := false
	for _, w := range pv.Warnings {
		if strings.Contains(w, "2 of 3 connections") && strings.Contains(w, "subfolder") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings: %v", pv.Warnings)
	}
}
