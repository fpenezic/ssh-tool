package mobaxterm

import (
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

const sample = `[Bookmarks]
SubRep=
ImgNum=42
web-01=#109#0%192.0.2.10%22%root%%-1%-1%%%%%0%0%0%%%-1%0%0%0%%1080%%0%0%1#MobaFont%10%0%0%-1%15%236,236,236%30,30,30%180,180,192%0%-1%0%%xterm%-1%-1%0%_Std_Colors_0_%80%24%0%1%-1%<none>%%0%1%-1#0# #-1
rdp-box=#91#4%10.0.0.5%3389%admin
[Bookmarks_1]
SubRep=Prod\DB
ImgNum=41
db-01=#109#0%db.internal.example%2222%admin%%-1%-1%%%%%0%0%0%%%-1%0%0%0%%1080%%0%0%1#MobaFont%10
[Bookmarks_2]
SubRep=Prod
ImgNum=41
app-01=#109#0%app.internal.example%22%%
`

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
	e := entries[0]
	if e.Name != "web-01" || e.Host != "192.0.2.10" || e.Port != 22 || e.User != "root" || e.Folder != "" {
		t.Fatalf("bad entry 0: %+v", e)
	}
	e = entries[1]
	if e.Name != "db-01" || e.Host != "db.internal.example" || e.Port != 2222 || e.User != "admin" || e.Folder != `Prod\DB` {
		t.Fatalf("bad entry 1: %+v", e)
	}
	e = entries[2]
	if e.Name != "app-01" || e.User != "" || e.Folder != "Prod" {
		t.Fatalf("bad entry 2: %+v", e)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, _, err := Parse("just some text\nnothing here"); err == nil {
		t.Fatalf("garbage input should error")
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
	// Prod + Prod\DB: "Prod" is shared, so 2 folders total.
	if sum.FoldersCreated != 2 {
		t.Fatalf("want 2 folders (Prod, DB), got %d", sum.FoldersCreated)
	}

	folders, _ := db.ListFolders()
	byName := map[string]store.Folder{}
	for _, f := range folders {
		byName[f.Name] = f
	}
	db1, ok := byName["DB"]
	if !ok || db1.ParentID == nil || *db1.ParentID != byName["Prod"].ID {
		t.Fatalf("DB should be child of Prod: %+v", byName)
	}

	conns, _ := db.ListConnections(nil)
	for _, c := range conns {
		if c.Name == "db-01" {
			if c.FolderID == nil || *c.FolderID != db1.ID {
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
