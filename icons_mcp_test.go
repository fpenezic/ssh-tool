package main

import (
	"database/sql"
	"testing"

	"ssh-tool/internal/store"
)

// iconTestDB spins up a scratch store with one connection, returning its id.
func iconTestDB(t *testing.T) (*store.DB, string) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	c, err := db.CreateConnection(store.NewConnection{
		Name: "web-01", Hostname: "web01.example.com", Protocol: "ssh",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	return db, c.ID
}

func TestSetConnectionNamedIconTxRoundTrip(t *testing.T) {
	db, id := iconTestDB(t)

	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.SetConnectionNamedIconTx(tx, id, "database", "blue")
	}); err != nil {
		t.Fatalf("set icon: %v", err)
	}

	got, err := db.GetConnection(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.IconName == nil || *got.IconName != "database" {
		t.Errorf("icon_name = %v, want database", got.IconName)
	}
	if got.IconColor == nil || *got.IconColor != "blue" {
		t.Errorf("icon_color = %v, want blue", got.IconColor)
	}
}

// An empty name clears the icon, and takes the colour with it - a colour with
// no icon has nothing to paint and would linger as dead state.
func TestSetConnectionNamedIconTxClears(t *testing.T) {
	db, id := iconTestDB(t)

	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.SetConnectionNamedIconTx(tx, id, "flame", "red")
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := db.WithTx(func(tx *sql.Tx) error {
		return db.SetConnectionNamedIconTx(tx, id, "", "")
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}

	got, err := db.GetConnection(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.IconName != nil && *got.IconName != "" {
		t.Errorf("icon_name = %v, want cleared", *got.IconName)
	}
	if got.IconColor != nil && *got.IconColor != "" {
		t.Errorf("icon_color = %v, want cleared", *got.IconColor)
	}
}

func TestSetConnectionNamedIconTxMissingRow(t *testing.T) {
	db, _ := iconTestDB(t)
	err := db.WithTx(func(tx *sql.Tx) error {
		return db.SetConnectionNamedIconTx(tx, "no-such-id", "server", "")
	})
	if err == nil {
		t.Fatal("expected an error for a missing connection")
	}
}

func TestImageExistsRejectsUnknown(t *testing.T) {
	db, _ := iconTestDB(t)
	if db.ImageExists("") {
		t.Error("empty id must not count as existing")
	}
	if db.ImageExists("2f1c9a4e-0000-0000-0000-000000000000") {
		t.Error("unknown id must not count as existing")
	}
}

func TestValidIconNameKnowsTheBuiltins(t *testing.T) {
	for _, name := range []string{"server", "database", "flame", "bot"} {
		if !validIconName(name) {
			t.Errorf("%q should be a valid built-in icon", name)
		}
	}
	// A plausible-sounding name a model might invent from a hostname.
	if validIconName("postgres") {
		t.Error("postgres is not a built-in icon and must be rejected")
	}
}

// iconLabel feeds the approval modal. An uploaded image outranks a named icon
// because the two are mutually exclusive in the schema, and a row carrying an
// image should never be described by a stale name column.
func TestIconLabel(t *testing.T) {
	s := func(v string) *string { return &v }
	empty := ""

	cases := []struct {
		name  string
		iname *string
		img   *string
		want  string
	}{
		{"none", nil, nil, "(none)"},
		{"named", s("database"), nil, "database"},
		{"uploaded", nil, s("img-1"), "uploaded icon"},
		{"uploaded wins", s("database"), s("img-1"), "uploaded icon"},
		{"empty strings", &empty, &empty, "(none)"},
	}
	for _, tc := range cases {
		if got := iconLabel(tc.iname, tc.img); got != tc.want {
			t.Errorf("%s: iconLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}
