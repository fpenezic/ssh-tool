package main

import (
	"strings"
	"testing"
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
