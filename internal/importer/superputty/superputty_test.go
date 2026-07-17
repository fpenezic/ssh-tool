package superputty

import (
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

const sample = `<?xml version="1.0" encoding="utf-8"?>
<ArrayOfSessionData xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <SessionData SessionId="web-01" SessionName="web-01" Host="192.0.2.10" Port="22" Username="root" Proto="SSH" />
  <SessionData SessionId="Prod/DB/db-01" SessionName="db-01" Host="db.internal.example" Port="2222" Username="admin" Proto="SSH" />
  <SessionData SessionId="Prod/app-01" SessionName="app-01" Host="app.internal.example" Port="22" Username="" Proto="SSH" />
  <SessionData SessionId="rdp-box" SessionName="rdp-box" Host="10.0.0.5" Port="3389" Username="admin" Proto="RDP" />
</ArrayOfSessionData>`

func TestParse(t *testing.T) {
	entries, sum, err := Parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 ssh entries, got %d: %+v", len(entries), entries)
	}
	if sum.SkippedNonSSH != 1 {
		t.Fatalf("want 1 non-ssh skipped (rdp), got %d", sum.SkippedNonSSH)
	}

	// Root session.
	e := entries[0]
	if e.Name != "web-01" || e.Host != "192.0.2.10" || e.Port != 22 || e.User != "root" || e.Folder != "" {
		t.Fatalf("bad entry 0: %+v", e)
	}
	// Nested two levels: Prod\DB.
	e = entries[1]
	if e.Name != "db-01" || e.Folder != "Prod\\DB" || e.Port != 2222 || e.User != "admin" {
		t.Fatalf("bad entry 1: %+v", e)
	}
	// Nested one level, default port, no user.
	e = entries[2]
	if e.Name != "app-01" || e.Folder != "Prod" || e.Port != 22 || e.User != "" {
		t.Fatalf("bad entry 2: %+v", e)
	}
}

func TestParseDefaultsAndFallbacks(t *testing.T) {
	// No SessionName -> leaf of SessionId; blank Port -> 22.
	const x = `<ArrayOfSessionData>
	  <SessionData SessionId="a/b/leaf" Host="h" Port="" Proto="ssh" />
	</ArrayOfSessionData>`
	entries, _, err := Parse(x)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Name != "leaf" || e.Folder != "a\\b" || e.Port != 22 {
		t.Fatalf("fallbacks wrong: %+v", e)
	}
}

func TestParseBadXML(t *testing.T) {
	if _, _, err := Parse("<not-closed"); err == nil {
		t.Fatal("expected an error for malformed XML")
	}
}

func TestApply(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	entries, sum, err := Parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sum, err = Apply(db, entries, sum, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sum.ConnectionsCreated != 3 {
		t.Fatalf("want 3 created, got %+v", sum)
	}
	// Prod + Prod\DB: "Prod" shared, so 2 folders.
	if sum.FoldersCreated != 2 {
		t.Fatalf("want 2 folders (Prod, DB), got %d", sum.FoldersCreated)
	}

	folders, _ := db.ListFolders()
	byName := map[string]store.Folder{}
	for _, f := range folders {
		byName[f.Name] = f
	}
	dbF, ok := byName["DB"]
	if !ok || dbF.ParentID == nil || *dbF.ParentID != byName["Prod"].ID {
		t.Fatalf("DB should be a child of Prod: %+v", byName)
	}

	conns, _ := db.ListConnections(nil)
	for _, c := range conns {
		if c.Name == "db-01" {
			if c.FolderID == nil || *c.FolderID != dbF.ID {
				t.Fatalf("db-01 should sit in Prod\\DB, got %v", c.FolderID)
			}
			if c.Overrides.Port == nil || *c.Overrides.Port != 2222 {
				t.Fatalf("db-01 port override lost: %+v", c.Overrides)
			}
		}
	}

	// Re-import: names collide, nothing new, folders reused.
	entries2, sum2, _ := Parse(sample)
	sum2, err = Apply(db, entries2, sum2, "")
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if sum2.ConnectionsCreated != 0 || sum2.ConnectionsSkipped != 3 || sum2.FoldersCreated != 0 {
		t.Fatalf("re-import should skip everything, got %+v", sum2)
	}
}
