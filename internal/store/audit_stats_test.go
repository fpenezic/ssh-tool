package store

import (
	"path/filepath"
	"testing"
	"time"
)

func statsDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// appendAt backdates a row. AppendAudit always stamps time.Now(), and
// every window/bucket assertion below needs rows at a chosen ts.
func appendAt(t *testing.T, d *DB, ts int64, action, target string, meta map[string]string) {
	t.Helper()
	if err := d.AppendAudit(action, target, meta); err != nil {
		t.Fatalf("append %s: %v", action, err)
	}
	if _, err := d.audit.Exec(
		`UPDATE audit_events SET ts = ? WHERE id = (SELECT MAX(id) FROM audit_events)`, ts,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func TestAuditStatsPairsSessionDuration(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-2 * time.Hour).Unix()
	appendAt(t, d, base, "ssh.connect", "c1", map[string]string{
		"session_id": "s1", "host": "10.0.0.5", "user": "root",
	})
	appendAt(t, d, base+600, "ssh.disconnect", "c1", map[string]string{
		"session_id": "s1", "host": "10.0.0.5", "name": "web",
	})

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Connects != 1 {
		t.Errorf("connects = %d, want 1", st.Connects)
	}
	if st.SessionSecs != 600 {
		t.Errorf("sessionSecs = %d, want 600", st.SessionSecs)
	}
	if st.Unpaired != 0 {
		t.Errorf("unpaired = %d, want 0", st.Unpaired)
	}
	if len(st.Hosts) != 1 || st.Hosts[0].Host != "10.0.0.5" {
		t.Fatalf("hosts = %+v, want one entry for 10.0.0.5", st.Hosts)
	}
	if st.Hosts[0].Seconds != 600 {
		t.Errorf("host seconds = %d, want 600", st.Hosts[0].Seconds)
	}
	// The name only ever appears on the disconnect event, so it has
	// to survive being learned after the host row was created.
	if st.Hosts[0].Name != "web" {
		t.Errorf("host name = %q, want %q", st.Hosts[0].Name, "web")
	}
}

// The regression this whole design guards: a connect with no matching
// disconnect (app killed, or session still open) must not read as a
// zero-second session.
func TestAuditStatsUnpairedConnectIsNotZeroDuration(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-3 * time.Hour).Unix()
	appendAt(t, d, base, "ssh.connect", "c1", map[string]string{
		"session_id": "s1", "host": "srv-a",
	})
	appendAt(t, d, base+300, "ssh.disconnect", "c1", map[string]string{
		"session_id": "s1", "host": "srv-a",
	})
	// Still open, no disconnect row at all.
	appendAt(t, d, base+400, "ssh.connect", "c1", map[string]string{
		"session_id": "s2", "host": "srv-a",
	})

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Connects != 2 {
		t.Errorf("connects = %d, want 2", st.Connects)
	}
	if st.SessionSecs != 300 {
		t.Errorf("sessionSecs = %d, want 300 (open session contributes no time)", st.SessionSecs)
	}
	if st.Unpaired != 1 {
		t.Errorf("unpaired = %d, want 1", st.Unpaired)
	}
	if st.Hosts[0].Unpaired != 1 {
		t.Errorf("host unpaired = %d, want 1", st.Hosts[0].Unpaired)
	}
	// Average time per session is a UI-side division; it must divide
	// by paired sessions, and this is the data that makes that
	// visible. 300/2 would understate a live session as 150s.
	if paired := st.Connects - st.Unpaired; paired != 1 {
		t.Errorf("paired = %d, want 1", paired)
	}
}

// A disconnect whose connect predates the window has no start to
// measure from; counting it from the window edge would invent time.
func TestAuditStatsOrphanDisconnectAddsNoTime(t *testing.T) {
	d := statsDB(t)
	now := time.Now()
	appendAt(t, d, now.Add(-40*24*time.Hour).Unix(), "ssh.connect", "c1", map[string]string{
		"session_id": "old", "host": "srv-a",
	})
	appendAt(t, d, now.Add(-1*time.Hour).Unix(), "ssh.disconnect", "c1", map[string]string{
		"session_id": "old", "host": "srv-a",
	})

	st, err := d.AuditStats(now.Add(-7 * 24 * time.Hour).Unix())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.SessionSecs != 0 {
		t.Errorf("sessionSecs = %d, want 0 - the connect is outside the window", st.SessionSecs)
	}
	if st.Connects != 0 {
		t.Errorf("connects = %d, want 0", st.Connects)
	}
}

func TestAuditStatsWindowExcludesOlderRows(t *testing.T) {
	d := statsDB(t)
	now := time.Now()
	appendAt(t, d, now.Add(-60*24*time.Hour).Unix(), "vault.unlock", "", nil)
	appendAt(t, d, now.Add(-2*time.Hour).Unix(), "vault.unlock", "", nil)

	all, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if all.Total != 2 {
		t.Errorf("all-time total = %d, want 2", all.Total)
	}
	win, err := d.AuditStats(now.Add(-7 * 24 * time.Hour).Unix())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if win.Total != 1 {
		t.Errorf("7d total = %d, want 1", win.Total)
	}
}

func TestAuditStatsCollectsFailures(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-1 * time.Hour).Unix()
	appendAt(t, d, base, "vault.unlock.failed", "", nil)
	appendAt(t, d, base+1, "vault.unlock.failed", "", nil)
	appendAt(t, d, base+2, "share.violation", "", nil)
	appendAt(t, d, base+3, "vault.unlock", "", nil)

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	got := map[string]int64{}
	for _, f := range st.Failures {
		got[f.Key] = f.Count
	}
	if got["vault.unlock.failed"] != 2 {
		t.Errorf("vault.unlock.failed = %d, want 2", got["vault.unlock.failed"])
	}
	if got["share.violation"] != 1 {
		t.Errorf("share.violation = %d, want 1", got["share.violation"])
	}
	if _, ok := got["vault.unlock"]; ok {
		t.Error("vault.unlock is a success, must not be listed as a failure")
	}
}

func TestAuditStatsBucketsByHourAndDay(t *testing.T) {
	d := statsDB(t)
	// Build the timestamp from a local wall-clock time: the buckets
	// are local-time, so a UTC-derived ts would land in the wrong
	// hour for anyone not on UTC.
	when := time.Date(2026, 3, 4, 14, 30, 0, 0, time.Local)
	appendAt(t, d, when.Unix(), "ssh.connect", "c1", map[string]string{"session_id": "s1", "host": "h"})
	appendAt(t, d, when.Add(20*time.Minute).Unix(), "vault.lock", "", nil)

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(st.Hourly) != 24 {
		t.Fatalf("hourly buckets = %d, want 24", len(st.Hourly))
	}
	if st.Hourly[14] != 2 {
		t.Errorf("hour 14 = %d, want 2", st.Hourly[14])
	}
	if len(st.Daily) != 1 || st.Daily[0].Key != "2026-03-04" || st.Daily[0].Count != 2 {
		t.Errorf("daily = %+v, want one 2026-03-04 with 2", st.Daily)
	}
}

func TestAuditStatsEmptyLogIsNotAnError(t *testing.T) {
	d := statsDB(t)
	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats on empty log: %v", err)
	}
	if st.Total != 0 || len(st.Hosts) != 0 || st.SessionSecs != 0 {
		t.Errorf("empty log produced %+v", st)
	}
	if len(st.Hourly) != 24 {
		t.Errorf("hourly must still be 24 buckets, got %d", len(st.Hourly))
	}
}

func TestAuditStatsRanksHostsByTime(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-5 * time.Hour).Unix()
	// busy: one long session. chatty: three short ones.
	appendAt(t, d, base, "ssh.connect", "", map[string]string{"session_id": "a", "host": "busy"})
	appendAt(t, d, base+3600, "ssh.disconnect", "", map[string]string{"session_id": "a", "host": "busy"})
	for i, sid := range []string{"b", "c", "d"} {
		off := int64(i * 100)
		appendAt(t, d, base+off, "ssh.connect", "", map[string]string{"session_id": sid, "host": "chatty"})
		appendAt(t, d, base+off+10, "ssh.disconnect", "", map[string]string{"session_id": sid, "host": "chatty"})
	}

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(st.Hosts) != 2 {
		t.Fatalf("hosts = %+v, want 2", st.Hosts)
	}
	if st.Hosts[0].Host != "busy" {
		t.Errorf("top host = %q, want busy (ranked by time, not connect count)", st.Hosts[0].Host)
	}
	if st.Hosts[1].Connects != 3 {
		t.Errorf("chatty connects = %d, want 3", st.Hosts[1].Connects)
	}
	if st.LongestSecs != 3600 {
		t.Errorf("longest = %d, want 3600", st.LongestSecs)
	}
}

// Dynamic entries connect through a different action name and carry
// no connections-table row; they must still land in the host stats.
func TestAuditStatsIncludesDynamicConnects(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-1 * time.Hour).Unix()
	appendAt(t, d, base, "ssh.connect.dynamic", "dyn:e1", map[string]string{
		"session_id": "s1", "host": "10.9.9.9", "name": "prox-vm",
	})
	appendAt(t, d, base+120, "ssh.disconnect", "dyn:e1", map[string]string{
		"session_id": "s1", "host": "10.9.9.9",
	})

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Connects != 1 || st.SessionSecs != 120 {
		t.Errorf("connects=%d secs=%d, want 1 / 120", st.Connects, st.SessionSecs)
	}
	if len(st.Hosts) != 1 || st.Hosts[0].Name != "prox-vm" {
		t.Errorf("hosts = %+v, want prox-vm", st.Hosts)
	}
}

// Disconnects are now recorded on every teardown path, each carrying a
// reason. Pairing keys on session_id alone, so the reason must not
// affect it - a dropped session is as measurable as a clicked one.
func TestAuditStatsPairsRegardlessOfDisconnectReason(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-4 * time.Hour).Unix()
	for i, reason := range []string{"user", "closed", "app_quit"} {
		sid := string(rune('a' + i))
		off := int64(i) * 1000
		appendAt(t, d, base+off, "ssh.connect", "c", map[string]string{
			"session_id": sid, "host": "h1",
		})
		appendAt(t, d, base+off+60, "ssh.disconnect", "c", map[string]string{
			"session_id": sid, "host": "h1", "reason": reason,
		})
	}

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Connects != 3 {
		t.Errorf("connects = %d, want 3", st.Connects)
	}
	if st.SessionSecs != 180 {
		t.Errorf("sessionSecs = %d, want 180 (3 x 60s regardless of reason)", st.SessionSecs)
	}
	if st.Unpaired != 0 {
		t.Errorf("unpaired = %d, want 0 - every reason should pair", st.Unpaired)
	}
}

// History written before disconnects were logged everywhere still has
// bare connects in it. Those must keep counting as unpaired rather
// than breaking the newer rows around them.
func TestAuditStatsMixesLegacyAndNewRows(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-6 * time.Hour).Unix()
	// Legacy: connect with no disconnect ever written.
	appendAt(t, d, base, "ssh.connect.dynamic", "dyn:old", map[string]string{
		"session_id": "legacy", "host": "h-old",
	})
	// New: full pair.
	appendAt(t, d, base+100, "ssh.connect", "c", map[string]string{
		"session_id": "new", "host": "h-new",
	})
	appendAt(t, d, base+400, "ssh.disconnect", "c", map[string]string{
		"session_id": "new", "host": "h-new", "reason": "closed",
	})

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Connects != 2 {
		t.Errorf("connects = %d, want 2", st.Connects)
	}
	if st.Unpaired != 1 {
		t.Errorf("unpaired = %d, want 1 (the legacy row)", st.Unpaired)
	}
	if st.SessionSecs != 300 {
		t.Errorf("sessionSecs = %d, want 300 - the legacy row must not add time", st.SessionSecs)
	}
	// The measurable half must still be measurable: average is over
	// paired sessions, so one legacy row must not halve it.
	if paired := st.Connects - st.Unpaired; paired != 1 {
		t.Fatalf("paired = %d, want 1", paired)
	}
}

// The gate is the only field in the log that says whether a human
// approved what the LLM did. It is tallied across every mcp_* action,
// not per action kind - "how much ran unattended" is the question.
func TestAuditStatsTalliesLLMGates(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-2 * time.Hour).Unix()
	rows := []struct {
		action, gate string
	}{
		{"mcp_run", "yolo"},
		{"mcp_run", "yolo"},
		{"mcp_run", "approved"},
		{"mcp_connect", "approved"},
		{"mcp_run", "denied"},
		{"mcp_read", "auto"},
	}
	for i, r := range rows {
		appendAt(t, d, base+int64(i), r.action, "t", map[string]string{"gate": r.gate})
	}
	// A non-LLM row must not land in the gate tally.
	appendAt(t, d, base+50, "ssh.connect", "c", map[string]string{"session_id": "s", "host": "h"})

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.LLMActions != 6 {
		t.Errorf("llmActions = %d, want 6 (the ssh.connect must not count)", st.LLMActions)
	}
	got := map[string]int64{}
	for _, g := range st.Gates {
		got[g.Key] = g.Count
	}
	for k, want := range map[string]int64{"yolo": 2, "approved": 2, "denied": 1, "auto": 1} {
		if got[k] != want {
			t.Errorf("gate %s = %d, want %d", k, got[k], want)
		}
	}
	// Risk order is fixed so the panel does not reshuffle as counts
	// change: denied, yolo, auto, approved.
	var order []string
	for _, g := range st.Gates {
		order = append(order, g.Key)
	}
	want := []string{"denied", "yolo", "auto", "approved"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("gate order = %v, want %v", order, want)
		}
	}
}

// An mcp_ row written before the gate field existed must still be
// counted as an LLM action rather than dropped or crashing the tally.
func TestAuditStatsGatelessLLMRowCountsAsUnknown(t *testing.T) {
	d := statsDB(t)
	base := time.Now().Add(-1 * time.Hour).Unix()
	appendAt(t, d, base, "mcp_run", "t", map[string]string{"command": "ls"})

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.LLMActions != 1 {
		t.Errorf("llmActions = %d, want 1", st.LLMActions)
	}
	if len(st.Gates) != 1 || st.Gates[0].Key != "unknown" || st.Gates[0].Count != 1 {
		t.Errorf("gates = %+v, want one unknown=1", st.Gates)
	}
}

func TestAuditStatsNoLLMActivityYieldsNoGates(t *testing.T) {
	d := statsDB(t)
	appendAt(t, d, time.Now().Add(-time.Hour).Unix(), "vault.unlock", "", nil)

	st, err := d.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.LLMActions != 0 || len(st.Gates) != 0 {
		t.Errorf("llmActions=%d gates=%+v, want 0 / empty", st.LLMActions, st.Gates)
	}
}
