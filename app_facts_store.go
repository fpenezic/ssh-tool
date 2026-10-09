package main

import (
	"encoding/json"
	"fmt"

	sshlayer "ssh-tool/internal/ssh"
)

// Gather facts runs kept per folder, and what a health report counts as a
// problem. Both live in the settings table: the dialog and the MCP tools
// read and write them through here, so the format has one owner.

// FactsHistoryMax is how many runs a folder keeps; a weekly run covers
// about half a year.
const FactsHistoryMax = 24

// FactsRun is one stored Gather facts run.
type FactsRun struct {
	At      int64             `json:"at"` // unix milliseconds
	Facts   []string          `json:"facts"`
	Results []FactsHostResult `json:"results"`
}

// FactsReportSettings: thresholds and expected failed units of one folder
// (or "_" for hand-picked hosts). JSON names match what the dialog stored
// before this moved to Go.
type FactsReportSettings struct {
	ExpectedUnits     []string `json:"expectedUnits"`
	DiskWarn          int      `json:"diskWarn"`
	DiskBad           int      `json:"diskBad"`
	SecurityGraceDays int      `json:"securityGraceDays"`
	RebootGraceDays   int      `json:"rebootGraceDays"`
	StaleDays         int      `json:"staleDays"`
	// Containers stopped on purpose (by name), and how recent an engine
	// restart must be to flag a container (a crash loop that settled).
	ExpectedContainers   []string `json:"expectedContainers"`
	ContainerRestartDays int      `json:"containerRestartDays"`
}

var factsReportDefaults = FactsReportSettings{
	ExpectedUnits: []string{}, DiskWarn: 80, DiskBad: 90,
	SecurityGraceDays: 7, RebootGraceDays: 14, StaleDays: 30,
	ExpectedContainers: []string{}, ContainerRestartDays: 7,
}

// FactsCatalogInfo is everything the dialog needs to draw its form.
type FactsCatalogInfo struct {
	Facts          []sshlayer.FactInfo   `json:"facts"`
	Presets        []sshlayer.FactPreset `json:"presets"`
	ReportDefaults FactsReportSettings   `json:"report_defaults"`
	HistoryMax     int                   `json:"history_max"`
}

// FactsCatalog is the fact list, presets and report defaults.
func (a *App) FactsCatalog() FactsCatalogInfo {
	return FactsCatalogInfo{
		Facts: sshlayer.FactCatalog, Presets: sshlayer.FactPresets,
		ReportDefaults: factsReportDefaults, HistoryMax: FactsHistoryMax,
	}
}

func factsHistKey(folderID string) string { return "fleet_facts_hist:" + folderID }
func factsLastKey(folderID string) string { return "fleet_facts:" + folderID }

// FactsHistory returns a folder's stored runs, oldest first. A folder from
// before the history existed starts with its one stored snapshot.
func (a *App) FactsHistory(folderID string) ([]FactsRun, error) {
	var hist []FactsRun
	if raw, ok, err := a.db.GetSetting(factsHistKey(folderID)); err != nil {
		return nil, err
	} else if ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &hist); err != nil {
			return nil, fmt.Errorf("stored facts history: %w", err)
		}
	}
	if len(hist) == 0 {
		if raw, ok, _ := a.db.GetSetting(factsLastKey(folderID)); ok && raw != "" {
			var last FactsRun
			if json.Unmarshal([]byte(raw), &last) == nil && last.At > 0 {
				hist = []FactsRun{last}
			}
		}
	}
	if hist == nil {
		hist = []FactsRun{}
	}
	return hist, nil
}

// FactsSaveRun appends a run to the folder's history (keeping the last
// FactsHistoryMax) and makes it the folder's latest report.
func (a *App) FactsSaveRun(folderID string, run FactsRun) error {
	hist, err := a.FactsHistory(folderID)
	if err != nil {
		return err
	}
	hist = append(hist, run)
	if len(hist) > FactsHistoryMax {
		hist = hist[len(hist)-FactsHistoryMax:]
	}
	if err := a.writeFactsHistory(folderID, hist); err != nil {
		return err
	}
	EventsEmit("fleet_facts_saved", map[string]string{"folder_id": folderID})
	return nil
}

// FactsDeleteRun drops one stored run (a test run, a run on the wrong
// hosts); the latest report follows the history.
func (a *App) FactsDeleteRun(folderID string, at int64) error {
	hist, err := a.FactsHistory(folderID)
	if err != nil {
		return err
	}
	out := hist[:0]
	for _, h := range hist {
		if h.At != at {
			out = append(out, h)
		}
	}
	return a.writeFactsHistory(folderID, out)
}

func (a *App) writeFactsHistory(folderID string, hist []FactsRun) error {
	b, err := json.Marshal(hist)
	if err != nil {
		return err
	}
	if err := a.db.SetSetting(factsHistKey(folderID), string(b)); err != nil {
		return err
	}
	if len(hist) == 0 {
		return a.db.DeleteSetting(factsLastKey(folderID))
	}
	last, err := json.Marshal(hist[len(hist)-1])
	if err != nil {
		return err
	}
	return a.db.SetSetting(factsLastKey(folderID), string(last))
}

// factsReportSettings reads a folder's report settings over the defaults.
func (a *App) factsReportSettings(folderID string) FactsReportSettings {
	if folderID == "" {
		folderID = "_"
	}
	rs := factsReportDefaults
	if raw, ok, _ := a.db.GetSetting("fleet_report:" + folderID); ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &rs)
	}
	if rs.ExpectedUnits == nil {
		rs.ExpectedUnits = []string{}
	}
	if rs.ExpectedContainers == nil {
		rs.ExpectedContainers = []string{}
	}
	return rs
}

// FactsReportSettingsGet: a folder's report settings ("" for hand-picked
// hosts) over the defaults.
func (a *App) FactsReportSettingsGet(folderID string) FactsReportSettings {
	return a.factsReportSettings(folderID)
}

// FactsReportSettingsSet stores a folder's report settings.
func (a *App) FactsReportSettingsSet(folderID string, rs FactsReportSettings) error {
	if folderID == "" {
		folderID = "_"
	}
	if rs.ExpectedUnits == nil {
		rs.ExpectedUnits = []string{}
	}
	if rs.ExpectedContainers == nil {
		rs.ExpectedContainers = []string{}
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	return a.db.SetSetting("fleet_report:"+folderID, string(b))
}
