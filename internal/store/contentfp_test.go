package store

import (
	"path/filepath"
	"testing"
)

// ContentFingerprint is the sync change signal. It must move on a
// real mutation, stay put across reopen (same data), and NOT move on
// a VACUUM INTO snapshot (push runs one - the property whose absence
// caused an endless idle push loop).
func TestContentFingerprintTracksMutations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fp0 := db.ContentFingerprint()
	if _, err := db.ListConnections(nil); err != nil {
		t.Fatal(err)
	}
	if fp := db.ContentFingerprint(); fp != fp0 {
		t.Fatalf("read changed fingerprint: %q -> %q", fp0, fp)
	}
	c, err := db.CreateConnection(NewConnection{Name: "a", Hostname: "h"})
	if err != nil {
		t.Fatal(err)
	}
	fp1 := db.ContentFingerprint()
	if fp1 == fp0 {
		t.Fatalf("create did not change fingerprint: %q", fp1)
	}
	if err := db.DeleteConnection(c.ID); err != nil {
		t.Fatal(err)
	}
	if fp := db.ContentFingerprint(); fp == fp1 {
		t.Fatalf("delete did not change fingerprint: %q", fp)
	}
}

func TestContentFingerprintStableAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateConnection(NewConnection{Name: "a", Hostname: "h"}); err != nil {
		t.Fatal(err)
	}
	fp1 := db.ContentFingerprint()
	_ = db.Close()

	// Reopen: same data must yield the same fingerprint - the
	// per-connection data_version predecessor failed exactly here,
	// pushing on every launch.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if fp2 := db2.ContentFingerprint(); fp2 != fp1 {
		t.Fatalf("fingerprint changed across reopen: %q -> %q", fp1, fp2)
	}
}

func TestContentFingerprintStableAcrossVacuumInto(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateConnection(NewConnection{Name: "a", Hostname: "h"}); err != nil {
		t.Fatal(err)
	}
	before := db.ContentFingerprint()
	if _, err := db.Conn().Exec("VACUUM INTO ?", filepath.Join(dir, "snap.db")); err != nil {
		t.Fatal(err)
	}
	if after := db.ContentFingerprint(); after != before {
		t.Fatalf("VACUUM INTO changed fingerprint %q -> %q (would re-dirty + loop)", before, after)
	}
}
