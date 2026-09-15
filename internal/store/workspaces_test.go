package store

import (
	"strings"
	"testing"
)

// A duplicate name is the everyday case, not an edge case: the user saves an
// edited workspace under the name it already has. The UI offers to overwrite
// before it reaches the store, so what matters here is that any other caller
// gets a sentence rather than a raw SQL constraint message.
func TestCreateWorkspaceDuplicateName(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.CreateWorkspace("prod", `{"version":2,"tabs":[]}`); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := db.CreateWorkspace("prod", `{"version":2,"tabs":[]}`)
	if err == nil {
		t.Fatal("expected an error creating a second workspace named prod")
	}
	if strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Errorf("raw SQL error reached the caller: %v", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want an 'already exists' message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "prod") {
		t.Errorf("want the name in the message, got: %v", err)
	}
}

// Renaming a workspace onto a name another one already holds hits the same
// constraint through a different statement.
func TestUpdateWorkspaceOntoTakenName(t *testing.T) {
	db := openTestDB(t)

	a, err := db.CreateWorkspace("prod", "{}")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := db.CreateWorkspace("staging", "{}"); err != nil {
		t.Fatalf("create b: %v", err)
	}

	_, err = db.UpdateWorkspace(a.ID, "staging", "{}")
	if err == nil {
		t.Fatal("expected an error renaming prod onto staging")
	}
	if strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Errorf("raw SQL error reached the caller: %v", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("want an 'already exists' message, got: %v", err)
	}
}

// Overwriting a workspace under its OWN name is the save path the UI uses and
// must not trip the constraint.
func TestUpdateWorkspaceSameNameSucceeds(t *testing.T) {
	db := openTestDB(t)

	w, err := db.CreateWorkspace("prod", `{"version":2,"tabs":[]}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	layout := `{"version":2,"tabs":[{"title":"web01","sessions":[{"kind":"ssh","connectionId":"c1"}],"root":{"kind":"pane","ref":0}}]}`
	got, err := db.UpdateWorkspace(w.ID, "prod", layout)
	if err != nil {
		t.Fatalf("overwrite under the same name: %v", err)
	}
	if got.LayoutJSON != layout {
		t.Errorf("layout not written back\n got: %s\nwant: %s", got.LayoutJSON, layout)
	}
	if got.Name != "prod" {
		t.Errorf("name changed: %q", got.Name)
	}
}

// The store persists the layout blob verbatim - it is the frontend's shape,
// and a pane tree must survive the round trip untouched.
func TestWorkspaceLayoutRoundTrip(t *testing.T) {
	db := openTestDB(t)

	layout := `{"version":2,"tabs":[{"title":"pair","sessions":[{"kind":"ssh","connectionId":"c1"},{"kind":"local","shellKind":"wsl"}],"root":{"kind":"split","direction":"vertical","ratio":0.7,"a":{"kind":"pane","ref":0},"b":{"kind":"pane","ref":1}}}]}`
	w, err := db.CreateWorkspace("split-test", layout)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetWorkspace(w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LayoutJSON != layout {
		t.Errorf("layout changed in the store\n got: %s\nwant: %s", got.LayoutJSON, layout)
	}
}

func TestCreateWorkspaceRequiresName(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.CreateWorkspace("", "{}"); err == nil {
		t.Fatal("expected an error for an empty name")
	}
}
