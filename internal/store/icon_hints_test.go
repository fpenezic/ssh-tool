package store

import "testing"

func iconHintDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestFolderIconHintsWalkUp mirrors the real tree shape: the customer folder
// carries the logo, the subsystem folders below it carry nothing, and the hint
// has to reach the logo from the bottom.
func TestFolderIconHintsWalkUp(t *testing.T) {
	db := iconHintDB(t)

	work, err := db.CreateFolder(NewFolder{Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	cust, err := db.CreateFolder(NewFolder{Name: "A1 - Bulgaria", ParentID: &work.ID})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := db.CreateFolder(NewFolder{Name: "NMS - Clickhouse", ParentID: &cust.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateFolder(NewFolder{Name: "Telekom", ParentID: &work.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetFolderNamedIcon(cust.ID, "building", "blue"); err != nil {
		t.Fatal(err)
	}

	hints, err := db.FolderIconHints()
	if err != nil {
		t.Fatalf("hints: %v", err)
	}

	h, ok := hints[sub.ID]
	if !ok {
		t.Fatal("subsystem folder got no hint; the walk up never reached the customer")
	}
	if h.IconName != "building" || h.IconColor != "blue" {
		t.Errorf("icon = %s/%s, want building/blue", h.IconName, h.IconColor)
	}
	if h.Depth != 1 {
		t.Errorf("depth = %d, want 1", h.Depth)
	}
	if h.FolderPath != "Work/A1 - Bulgaria" {
		t.Errorf("path = %q, want Work/A1 - Bulgaria", h.FolderPath)
	}
	if got := hints[cust.ID]; got.Depth != 0 {
		t.Errorf("the iconed folder itself should be depth 0, got %d", got.Depth)
	}
	if _, ok := hints[other.ID]; ok {
		t.Error("a folder with no iconed ancestor must be absent")
	}
	// An icon must never propagate upward: Work has no icon of its own.
	if _, ok := hints[work.ID]; ok {
		t.Error("an ancestor must not inherit its child's icon")
	}
}

// A folder where every connection wears the same uploaded logo is the
// copy-this case.
func TestFolderConnIconsUnanimous(t *testing.T) {
	db := iconHintDB(t)
	f, err := db.CreateFolder(NewFolder{Name: "Customer"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := db.PutImage([]byte("fake-png-bytes"), "image/png")
	if err != nil {
		t.Skipf("image storage unavailable: %v", err)
	}
	for _, n := range []string{"web-01", "web-02", "db-01"} {
		c, err := db.CreateConnection(NewConnection{Name: n, Hostname: n + ".example.com", FolderID: &f.ID, Protocol: "ssh"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetConnectionIcon(c.ID, img); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.FolderConnIcons(6)
	if err != nil {
		t.Fatalf("conn icons: %v", err)
	}
	u := got[f.ID]
	if u.Unanimous != img || !u.IsImage {
		t.Errorf("Unanimous = %q image=%v, want the uploaded image", u.Unanimous, u.IsImage)
	}
	if u.Total != 3 || u.WithIcon != 3 {
		t.Errorf("counts = %d/%d, want 3/3", u.WithIcon, u.Total)
	}
}

// Different built-in icons per role is the pick-by-role case: report samples,
// never a single winner.
func TestFolderConnIconsVaryByRole(t *testing.T) {
	db := iconHintDB(t)
	f, err := db.CreateFolder(NewFolder{Name: "Mixed"})
	if err != nil {
		t.Fatal(err)
	}
	for name, icon := range map[string]string{
		"db-01":  "database",
		"nfs-01": "hard-drive",
		"web-01": "globe",
	} {
		c, err := db.CreateConnection(NewConnection{Name: name, Hostname: name, FolderID: &f.ID, Protocol: "ssh"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetConnectionNamedIcon(c.ID, icon, ""); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.FolderConnIcons(6)
	if err != nil {
		t.Fatalf("conn icons: %v", err)
	}
	u := got[f.ID]
	if u.Unanimous != "" {
		t.Errorf("Unanimous = %q, want empty for a role-based folder", u.Unanimous)
	}
	if len(u.Examples) != 3 {
		t.Errorf("got %d examples, want 3 so the mapping is visible", len(u.Examples))
	}
}

// A folder where only some connections carry an icon has no settled
// convention, so it must not be reported as unanimous.
func TestFolderConnIconsPartialIsNotUnanimous(t *testing.T) {
	db := iconHintDB(t)
	f, err := db.CreateFolder(NewFolder{Name: "Half"})
	if err != nil {
		t.Fatal(err)
	}
	withIcon, err := db.CreateConnection(NewConnection{Name: "db-01", Hostname: "db-01", FolderID: &f.ID, Protocol: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetConnectionNamedIcon(withIcon.ID, "database", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateConnection(NewConnection{Name: "plain-01", Hostname: "plain-01", FolderID: &f.ID, Protocol: "ssh"}); err != nil {
		t.Fatal(err)
	}

	got, err := db.FolderConnIcons(6)
	if err != nil {
		t.Fatalf("conn icons: %v", err)
	}
	u := got[f.ID]
	if u.Unanimous != "" {
		t.Errorf("Unanimous = %q, but one of two connections has no icon", u.Unanimous)
	}
	if u.WithIcon != 1 || u.Total != 2 {
		t.Errorf("counts = %d/%d, want 1/2", u.WithIcon, u.Total)
	}
}

func TestFolderConnIconsSkipsIconlessFolders(t *testing.T) {
	db := iconHintDB(t)
	f, err := db.CreateFolder(NewFolder{Name: "Bare"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateConnection(NewConnection{Name: "a", Hostname: "a", FolderID: &f.ID, Protocol: "ssh"}); err != nil {
		t.Fatal(err)
	}

	got, err := db.FolderConnIcons(6)
	if err != nil {
		t.Fatalf("conn icons: %v", err)
	}
	if _, ok := got[f.ID]; ok {
		t.Error("a folder with no icons at all must be absent, not reported empty")
	}
}
