package store

import (
	"path/filepath"
	"testing"
)

func TestMirrorFromReplacesProfile(t *testing.T) {
	dir := t.TempDir()
	// Source: the "other machine" with its own data.
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	sf, _ := src.CreateFolder(NewFolder{Name: "Infra"})
	if _, err := src.CreateConnection(NewConnection{Name: "web-01", Hostname: "h", FolderID: &sf.ID}); err != nil {
		t.Fatal(err)
	}
	// A network profile on the source. Regression guard: a live pull
	// must mirror network_profiles or a second machine gets the
	// connections that inherit a VPN profile but never the profile.
	np, err := src.CreateNetworkProfile("hetzner-wg", `{"kind":"wireguard"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = src.SetSetting("recent_connections_count", "20") // machine-local on dst
	_ = src.SetSetting("default_terminal_type", "xterm") // real profile setting
	_ = src.Close()

	// Destination: the live machine, different data + its own local state.
	dst, err := Open(filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := dst.CreateConnection(NewConnection{Name: "stale", Hostname: "old"}); err != nil {
		t.Fatal(err)
	}
	_ = dst.SetSetting("window_state_v1", `{"x":5}`)         // machine-local, must survive
	_ = dst.SetSetting("sync_generation", "42")              // sync key, must survive
	_ = dst.SetSetting("recent_connections_count", "7")      // machine-local, must survive
	_ = dst.SetSetting("default_terminal_type", "vt100")     // profile, must be overwritten

	if err := dst.MirrorFrom(filepath.Join(dir, "src.db")); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	// Connections now match the source.
	conns, _ := dst.ListConnections(nil)
	if len(conns) != 1 || conns[0].Name != "web-01" {
		t.Fatalf("connections not mirrored: %+v", conns)
	}
	// Folder came across, FK intact.
	folders, _ := dst.ListFolders()
	if len(folders) != 1 || folders[0].Name != "Infra" {
		t.Fatalf("folders not mirrored: %+v", folders)
	}
	if conns[0].FolderID == nil || *conns[0].FolderID != folders[0].ID {
		t.Fatalf("connection FK to folder broken: %+v", conns[0].FolderID)
	}

	// Network profile came across (regression guard for the live-pull
	// mirror omitting network_profiles).
	nps, _ := dst.ListNetworkProfiles()
	if len(nps) != 1 || nps[0].Name != "hetzner-wg" || nps[0].ID != np.ID {
		t.Fatalf("network profile not mirrored: %+v", nps)
	}

	// Machine-local + sync keys preserved; profile setting overwritten.
	check := func(key, want string) {
		got, _, _ := dst.GetSetting(key)
		if got != want {
			t.Fatalf("setting %s = %q, want %q", key, got, want)
		}
	}
	check("window_state_v1", `{"x":5}`)       // preserved
	check("sync_generation", "42")            // preserved
	check("recent_connections_count", "7")    // preserved (NOT 20 from source)
	check("default_terminal_type", "xterm")   // overwritten from source
}

// A sync pull from a version before schema 28 still marks a sensitive
// connection in the old column; the mirror moves it into the overrides.
func TestMirrorFromFoldsLegacySensitive(t *testing.T) {
	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := src.CreateConnection(NewConnection{Name: "prod-db", Hostname: "db.example.com"})
	if _, err := src.conn.Exec(`UPDATE connections SET sensitive = 1 WHERE id = ?`, c.ID); err != nil {
		t.Fatal(err)
	}
	_ = src.Close()
	dst, err := Open(filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.MirrorFrom(filepath.Join(dir, "src.db")); err != nil {
		t.Fatal(err)
	}
	got, err := dst.GetConnection(c.ID)
	if err != nil || got == nil {
		t.Fatalf("connection not mirrored: %v", err)
	}
	if got.Overrides.Sensitive == nil || !*got.Overrides.Sensitive {
		t.Errorf("legacy sensitive lost in the mirror: %+v", got.Overrides)
	}
}
