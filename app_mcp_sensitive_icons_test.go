package main

import (
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

// A connection sensitive through its folder is not named to the LLM by
// the icon listings.
func TestMcpIconListingsHideSensitive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	on := true
	secret, err := db.CreateFolder(store.NewFolder{Name: "Vault", Settings: store.InheritableSettings{Sensitive: &on}})
	if err != nil {
		t.Fatal(err)
	}
	open, err := db.CreateFolder(store.NewFolder{Name: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := db.PutImage([]byte("\x89PNG\r\n\x1a\nfake"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, folder string }{{"hidden-db", secret.ID}, {"web-01", open.ID}} {
		f := c.folder
		conn, err := db.CreateConnection(store.NewConnection{Name: c.name, Hostname: "h.example.com", FolderID: &f})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetConnectionIcon(conn.ID, img); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{db: db}
	hidden, err := a.mcpSensitiveIDs()
	if err != nil {
		t.Fatal(err)
	}
	usage, err := db.ListImageUsage(3, hidden)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range usage {
		names = append(names, u.Examples...)
		if u.UseCount != 1 {
			t.Fatalf("use count %d counts the hidden connection", u.UseCount)
		}
	}
	if got := strings.Join(names, ","); strings.Contains(got, "hidden-db") || !strings.Contains(got, "web-01") {
		t.Fatalf("image examples: %s", got)
	}
	icons, err := db.FolderConnIcons(6, hidden)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := icons[secret.ID]; ok {
		t.Fatal("the sensitive folder's connections were listed")
	}
	if _, ok := icons[open.ID]; !ok {
		t.Fatal("the open folder is missing")
	}
}
