package presence

import (
	"testing"
	"time"
)

func TestLiveOwnerFreshnessAndSelf(t *testing.T) {
	now := time.Now().Unix()
	f := emptyFile()
	f.Presence["p1"] = Record{MachineID: "pc", MachineName: "work-pc", Heartbeat: now}

	// Fresh, foreign -> a live owner.
	if r := f.LiveOwner("p1", "laptop"); r == nil || r.MachineID != "pc" {
		t.Fatalf("expected live owner pc, got %+v", r)
	}
	// Our own record is not a conflict.
	if r := f.LiveOwner("p1", "pc"); r != nil {
		t.Fatalf("own record should not be a conflict, got %+v", r)
	}
	// Stale record -> free.
	f.Presence["p1"] = Record{MachineID: "pc", Heartbeat: now - int64(StaleAfter.Seconds()) - 5}
	if r := f.LiveOwner("p1", "laptop"); r != nil {
		t.Fatalf("stale record should read as free, got %+v", r)
	}
	// Unknown profile -> free.
	if r := f.LiveOwner("nope", "laptop"); r != nil {
		t.Fatalf("unknown profile should be free")
	}
}

func TestKillRequestAndHandled(t *testing.T) {
	now := time.Now().Unix()
	f := emptyFile()
	f.Presence["p1"] = Record{MachineID: "pc", MachineName: "work-pc", Heartbeat: now}

	nonce := NewNonce("laptop")
	got, ok := f.RequestKill("p1", "laptop", "laptop-1", nonce)
	if !ok || got != nonce {
		t.Fatalf("RequestKill failed: ok=%v nonce=%q", ok, got)
	}
	// Owner sees the kill targeting it.
	if k := f.PendingKillFor("p1", "pc", map[string]bool{}); k == nil || k.Nonce != nonce {
		t.Fatalf("owner should see kill, got %+v", k)
	}
	// A different machine is not the target.
	if k := f.PendingKillFor("p1", "other", map[string]bool{}); k != nil {
		t.Fatalf("non-target should see no kill")
	}
	// Already-handled nonce is ignored (owner reconnected later).
	if k := f.PendingKillFor("p1", "pc", map[string]bool{nonce: true}); k != nil {
		t.Fatalf("handled nonce must not re-fire")
	}
	// No kill when the requester owns the profile.
	f.Presence["p2"] = Record{MachineID: "laptop", Heartbeat: now}
	if _, ok := f.RequestKill("p2", "laptop", "laptop-1", NewNonce("x")); ok {
		t.Fatalf("cannot request kill on own profile")
	}
}

func TestSetOwnerPreservesSince(t *testing.T) {
	f := emptyFile()
	f.SetOwner("p1", Record{MachineID: "pc", Since: 100, Heartbeat: 100})
	// Heartbeat later keeps the original Since.
	f.SetOwner("p1", Record{MachineID: "pc", Since: 200, Heartbeat: 200})
	if f.Presence["p1"].Since != 100 {
		t.Fatalf("Since should be preserved on heartbeat, got %d", f.Presence["p1"].Since)
	}
	if f.Presence["p1"].Heartbeat != 200 {
		t.Fatalf("Heartbeat should update, got %d", f.Presence["p1"].Heartbeat)
	}
}

func TestClearOwnerOnlyOwn(t *testing.T) {
	f := emptyFile()
	f.Presence["p1"] = Record{MachineID: "pc", Heartbeat: time.Now().Unix()}
	// A different machine can't clear pc's record.
	f.ClearOwner("p1", "laptop")
	if _, ok := f.Presence["p1"]; !ok {
		t.Fatalf("foreign ClearOwner must not delete")
	}
	f.ClearOwner("p1", "pc")
	if _, ok := f.Presence["p1"]; ok {
		t.Fatalf("own ClearOwner should delete")
	}
}

func TestGC(t *testing.T) {
	now := time.Now()
	f := emptyFile()
	f.Presence["fresh"] = Record{MachineID: "a", Heartbeat: now.Unix()}
	f.Presence["stale"] = Record{MachineID: "b", Heartbeat: now.Unix() - int64(StaleAfter.Seconds()) - 1}
	f.Kills["recent"] = Kill{At: now.Unix()}
	f.Kills["expired"] = Kill{At: now.Unix() - int64(KillTTL.Seconds()) - 1}

	gc(f, now)

	if _, ok := f.Presence["fresh"]; !ok {
		t.Errorf("fresh presence dropped")
	}
	if _, ok := f.Presence["stale"]; ok {
		t.Errorf("stale presence kept")
	}
	if _, ok := f.Kills["recent"]; !ok {
		t.Errorf("recent kill dropped")
	}
	if _, ok := f.Kills["expired"]; ok {
		t.Errorf("expired kill kept")
	}
}

// fakeTransport is an in-memory Transport for Load/Save round-trips.
type fakeTransport struct {
	data map[string][]byte
}

func (f *fakeTransport) Get(name string) ([]byte, error) {
	b, ok := f.data[name]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}
func (f *fakeTransport) Put(name string, data []byte) error {
	f.data[name] = data
	return nil
}

func TestLoadSaveRoundTrip(t *testing.T) {
	tr := &fakeTransport{data: map[string][]byte{}}
	notFound := func(err error) bool { return err == ErrNotFound }

	// Missing file -> empty, no error.
	f, err := Load(tr, notFound)
	if err != nil || len(f.Presence) != 0 {
		t.Fatalf("load empty: %v %+v", err, f)
	}

	f.SetOwner("p1", Record{MachineID: "pc", MachineName: "work-pc", Since: 1, Heartbeat: time.Now().Unix()})
	if err := Save(tr, f); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Reload sees it.
	f2, err := Load(tr, notFound)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r := f2.LiveOwner("p1", "laptop"); r == nil || r.MachineName != "work-pc" {
		t.Fatalf("round-trip lost owner: %+v", r)
	}
}
