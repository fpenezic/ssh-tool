package rdm

import (
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// A connection with no group lands under the chosen root folder, and an
// out-of-range port is refused with a warning instead of wrapping.
func TestImportRootFolderAndPort(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := db.CreateFolder(store.NewFolder{Name: "Imported"})
	if err != nil {
		t.Fatal(err)
	}
	f := File{Connections: []Entry{{
		Name: "web", ConnectionType: 77, Group: "",
		Terminal: &Terminal{Host: "web.example.com", HostPort: 70000, Username: "u"},
	}}}
	sum, err := NewImporter(db, f, root.ID).Import()
	if err != nil {
		t.Fatal(err)
	}
	conns, err := db.ListConnections(&root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 {
		t.Fatalf("%d connections under the root folder", len(conns))
	}
	if conns[0].Overrides.Port != nil {
		t.Fatalf("port wrapped to %d", *conns[0].Overrides.Port)
	}
	if !strings.Contains(strings.Join(sum.Warnings, "\n"), "out of range") {
		t.Fatalf("no warning: %v", sum.Warnings)
	}
}
