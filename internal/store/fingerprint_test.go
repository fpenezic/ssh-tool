package store

import (
	"path/filepath"
	"testing"
	"time"
)

// Edits that keep the row count still move the sync fingerprint.
func TestFingerprintSeesInPlaceEdits(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := db.CreateConnection(NewConnection{Name: "c", Hostname: "h.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	lp, rp, rh := uint16(8080), uint16(80), "127.0.0.1"
	fw, err := db.CreatePortForward(NewPortForward{ConnectionID: c.ID, Kind: "local", LocalPort: &lp, RemoteHost: &rh, RemotePort: &rp})
	if err != nil {
		t.Fatal(err)
	}
	f, err := db.CreateCredentialFolder("Team", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := db.ContentFingerprint()

	lp2 := uint16(9090)
	if _, err := db.UpdatePortForward(UpdatePortForward{ID: fw.ID, LocalPort: &lp2}); err != nil {
		t.Fatal(err)
	}
	afterFwd := db.ContentFingerprint()
	if afterFwd == before {
		t.Fatal("a forward edit did not move the fingerprint")
	}
	time.Sleep(1100 * time.Millisecond) // updated_at has second resolution
	name := "Ops"
	if _, err := db.UpdateCredentialFolder(f.ID, &name, nil, false); err != nil {
		t.Fatal(err)
	}
	if db.ContentFingerprint() == afterFwd {
		t.Fatal("a credential folder rename did not move the fingerprint")
	}
}
