package main

import (
	"strings"
	"testing"
)

// Runs append oldest first, the latest also lands in the single-snapshot
// key the older dialog read, the history is capped, and deleting the
// latest run makes the one before it the folder's report.
func TestFactsRunHistory(t *testing.T) {
	a := newResolveTestApp(t)
	const fid = "folder-1"
	if h, err := a.FactsHistory(fid); err != nil || len(h) != 0 {
		t.Fatalf("empty history = %v, %v", h, err)
	}
	for i := int64(1); i <= FactsHistoryMax+2; i++ {
		if err := a.FactsSaveRun(fid, FactsRun{At: i, Facts: []string{"os"}}); err != nil {
			t.Fatal(err)
		}
	}
	h, _ := a.FactsHistory(fid)
	if len(h) != FactsHistoryMax || h[0].At != 3 || h[len(h)-1].At != FactsHistoryMax+2 {
		t.Fatalf("history = %d runs, first %d, last %d", len(h), h[0].At, h[len(h)-1].At)
	}
	if err := a.FactsDeleteRun(fid, FactsHistoryMax+2); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := a.db.GetSetting(factsLastKey(fid))
	if raw == "" || !strings.Contains(raw, `"at":25`) {
		t.Errorf("latest after delete = %s", raw)
	}

	// A folder from before the history: its one snapshot is the history.
	_ = a.db.SetSetting(factsLastKey("old"), `{"at":7,"facts":["os"],"results":[]}`)
	if h, _ := a.FactsHistory("old"); len(h) != 1 || h[0].At != 7 {
		t.Errorf("legacy history = %+v", h)
	}
}

func TestFactsReportSettingsDefaults(t *testing.T) {
	a := newResolveTestApp(t)
	if rs := a.factsReportSettings(""); rs.DiskWarn != 80 || rs.ExpectedUnits == nil {
		t.Errorf("defaults = %+v", rs)
	}
	_ = a.db.SetSetting("fleet_report:f", `{"diskWarn":85,"expectedUnits":["irqbalance.service"]}`)
	rs := a.factsReportSettings("f")
	if rs.DiskWarn != 85 || rs.DiskBad != 90 || len(rs.ExpectedUnits) != 1 {
		t.Errorf("stored over defaults = %+v", rs)
	}
}
