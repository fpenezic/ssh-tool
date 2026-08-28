package main

import (
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

func newAuditTestApp(t *testing.T) *App {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{db: db, sessionMeta: map[string]sessionMetaEntry{}}
}

func lastAudit(t *testing.T, a *App) store.AuditEvent {
	t.Helper()
	evs, err := a.db.ListAudit(store.AuditFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("no audit events recorded")
	}
	return evs[0]
}

// Only an explicit Disconnect click used to be logged. A session that
// ended because the server hung up, the network dropped, or the app
// quit left a connect with no disconnect, so anything pairing the two
// measured a small fraction of real usage.
func TestRecordSessionClosedWritesDisconnect(t *testing.T) {
	a := newAuditTestApp(t)
	a.sessionMeta["s1"] = sessionMetaEntry{
		connectionID: "conn-1", name: "web-01", hostname: "10.0.0.5",
	}

	a.recordSessionClosed("s1", "closed")

	ev := lastAudit(t, a)
	if ev.Action != "ssh.disconnect" {
		t.Errorf("action = %q, want ssh.disconnect", ev.Action)
	}
	if ev.Target != "conn-1" {
		t.Errorf("target = %q, want conn-1", ev.Target)
	}
	// session_id is what pairing keys on; without it the row is inert.
	if ev.Metadata["session_id"] != "s1" {
		t.Errorf("session_id = %q, want s1", ev.Metadata["session_id"])
	}
	if ev.Metadata["host"] != "10.0.0.5" {
		t.Errorf("host = %q, want 10.0.0.5", ev.Metadata["host"])
	}
	// The host stats learn the display name from the disconnect event.
	if ev.Metadata["name"] != "web-01" {
		t.Errorf("name = %q, want web-01", ev.Metadata["name"])
	}
	if ev.Metadata["reason"] != "closed" {
		t.Errorf("reason = %q, want closed", ev.Metadata["reason"])
	}
}

// Meta is deleted shortly after teardown; a race or an unknown session
// must still produce a pairable row rather than nothing at all.
func TestRecordSessionClosedWithoutMeta(t *testing.T) {
	a := newAuditTestApp(t)

	a.recordSessionClosed("ghost", "app_quit")

	ev := lastAudit(t, a)
	if ev.Action != "ssh.disconnect" {
		t.Errorf("action = %q, want ssh.disconnect", ev.Action)
	}
	if ev.Metadata["session_id"] != "ghost" {
		t.Errorf("session_id = %q, want ghost", ev.Metadata["session_id"])
	}
	if ev.Metadata["reason"] != "app_quit" {
		t.Errorf("reason = %q, want app_quit", ev.Metadata["reason"])
	}
}

// End to end through the store aggregate: a connect plus a
// drop-recorded disconnect must produce ONE paired, measured session.
func TestDroppedSessionBecomesMeasurable(t *testing.T) {
	a := newAuditTestApp(t)
	a.sessionMeta["s1"] = sessionMetaEntry{
		connectionID: "dyn:e1", name: "lab-vm", hostname: "10.1.1.1",
	}
	a.recordAudit("ssh.connect.dynamic", "dyn:e1", map[string]string{
		"session_id": "s1", "host": "10.1.1.1",
	})
	a.recordSessionClosed("s1", "closed")

	st, err := a.db.AuditStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Connects != 1 {
		t.Errorf("connects = %d, want 1", st.Connects)
	}
	if st.Unpaired != 0 {
		t.Errorf("unpaired = %d, want 0 - the drop is now recorded", st.Unpaired)
	}
	if len(st.Hosts) != 1 || st.Hosts[0].Name != "lab-vm" {
		t.Fatalf("hosts = %+v, want one named lab-vm", st.Hosts)
	}
}
