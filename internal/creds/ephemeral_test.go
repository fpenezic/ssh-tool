package creds

import "testing"

// A locked vault refuses Put - correctly, because a secret the user typed
// must not silently live only in RAM and vanish on restart. But an opkssh
// certificate is regenerable material obtained from an interactive browser
// login, and dropping it meant the next connect on that credential opened
// the browser again: connecting a folder of hosts with a locked vault asked
// for one sign-in per host.
//
// PutEphemeral is the escape hatch. Get must not be able to tell an
// ephemeral entry from a cached one, or the caller would re-login anyway.
func TestPutEphemeralIsVisibleToGet(t *testing.T) {
	v := &Vault{memory: map[string][]byte{}}

	// Confirm the premise: a locked vault (no file) rejects a normal Put.
	if err := v.Put("opkssh:cert", "pem"); err == nil {
		t.Fatal("Put succeeded on a locked vault; the fallback would be dead code")
	}
	if _, ok, _ := v.Get("opkssh:cert"); ok {
		t.Fatal("a rejected Put left a value behind")
	}

	v.PutEphemeral("opkssh:cert", "pem")

	got, ok, err := v.Get("opkssh:cert")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get did not see the ephemeral value; the next connect would re-login")
	}
	if got != "pem" {
		t.Fatalf("got %q, want %q", got, "pem")
	}
}

// Overwriting must wipe the previous buffer rather than leaving cert
// material in memory, matching what Put does.
func TestPutEphemeralOverwrites(t *testing.T) {
	v := &Vault{memory: map[string][]byte{}}

	v.PutEphemeral("k", "first")
	v.PutEphemeral("k", "second")

	got, ok, _ := v.Get("k")
	if !ok || got != "second" {
		t.Fatalf("got %q (ok=%v), want %q", got, ok, "second")
	}
}
