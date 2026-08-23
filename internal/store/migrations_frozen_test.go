package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A migration that has already run somewhere is frozen. The runner skips
// anything at or below the recorded schema_meta.version:
//
//	if m.version <= current { continue }
//
// so editing the SQL of an applied migration is a no-op on every database
// that has seen it - including the author's own dirty local build. The edit
// looks correct in review, passes on a fresh database, and silently never
// reaches an existing one. The result is two populations of databases with
// different schemas and no version number that distinguishes them.
//
// This test pins the SQL of every migration up to frozenThrough by hash. To
// change the schema, add a NEW migration; the only legitimate reason to
// touch a hash here is adding a new entry as you raise frozenThrough.
//
// Deliberately not a golden file: the hashes live next to the reason so the
// person who breaks the test reads why before deciding what to do.
const frozenThrough = 25

var frozenMigrationHashes = map[int64]string{
	1:  "c8cf85ffd5690e51477c8d3a1f2a4ef841ed74d39b772d328e75cf9e925bcf54",
	2:  "5dcd128cc069e0e4e620d9bd1dfcc0794cbbbe53e8ca17a80e24713149a1ed44",
	3:  "41e55294a257051829881a1dccaec50b57384deccc21e989a123a27b20b41a5c",
	4:  "a41b083138f4a192240eb10c75535d11e1ad6c1b582adc9b2432cde87756a864",
	5:  "f4443d3bd2253a1ac4efab91c3d7d8e013b6a0725a9f39e055657250690a3273",
	6:  "6b3226b96ec2c36b090b35bc55634a7deb99d5014f92dd3fab9fcbb2c466136e",
	7:  "250943b12b9c5eb3e7c6d652976997ccb5813274ee19c8deca08125b47e17d08",
	8:  "ff1f3ebc1a152e40c5a6fcd74bb106f4d68aefa8fd5cbcfee954ece97afe8be0",
	9:  "5128731b5453e9764a79b0fd35ded4a6e60925ead40703f1b78aa35e740a72bc",
	10: "ddb2665bebde6a1f3763d3bb38bb4e21d434aa6f140232b3a14f57dec982f884",
	11: "407368add1c9a2dcd8d520ffb519ad7468fc85de092beebebd41c498cc25e607",
	12: "1ea59ff06b32dec429fb29d2c12d2e39a18f722635639f8f1b7d87f2f60a3eb6",
	13: "7df6c2d90bc5e9173f732eae1efa7fe47afdfb8f7cd05568e7f0c1ca6626f865",
	14: "b945953f1b8a1f98e14d2ece5a15e0c7870bef2b47e84ca8a355eebb35a0032f",
	15: "0ca5f7b0a6cb5ca04b52971f9425b4c299dd3e6ed6d7fa7b8071dad78a9ff20b",
	16: "d939012a194c8515fc1b372bf070d086ce0ee24db46d52732e10fa76ab11b9d3",
	17: "3bcc7484d3e097e2a8651e68ad177e5ab640525b874703af605796f61ece77d4",
	18: "4771f638cd950b28e1369ccf3d035a0713575e85be04f93d945b7006e5e18e4a",
	19: "a4f510042f4156f3978ee11390976ffd6f5e238baa64502b829a52d229cf506a",
	20: "2b984e4c29d3b94ddceefcd67ed2f16d8691bd95ef8b43c6c931b8fa8e58322b",
	21: "42741451ef4ca834e6dfdb80129e80535416214fc5e7796923830a6c134e544f",
	// 22 and 23 hash identically on purpose: v23 is a repair migration that
	// re-runs v22's statements verbatim. It exists BECAUSE v22 was amended
	// after it had already run - the exact failure this test now prevents.
	// Databases recorded at version 22 never saw the appended credential
	// columns, so reads failed with "no such column: icon_name" until v23
	// re-added all four. Read v23's comment in migrations.go for the story.
	22: "de482bb35987048efaa4a8b9625d85126f1237a0333c3b0912db24a64086b94e",
	23: "de482bb35987048efaa4a8b9625d85126f1237a0333c3b0912db24a64086b94e",
	24: "6eff8f59d67824b47de6557dddd16e40e4a08388b2ee197c7a0fedeb9cdd1bee",
	25: "1aa0872eece351f16bf1744b8ccf0dc71d27b9194994025590748f0abeb16002",
}

// TestMigrationsAreFrozen is the guard described above.
func TestMigrationsAreFrozen(t *testing.T) {
	got := migrationSQLFromSource(t)

	for ver, wantHash := range frozenMigrationHashes {
		sql, ok := got[ver]
		if !ok {
			t.Errorf("migration %d is pinned here but no longer exists in migrations.go", ver)
			continue
		}
		if h := hashSQL(sql); h != wantHash {
			t.Errorf("migration %d changed.\n"+
				"  want %s\n  got  %s\n"+
				"A migration that has already run is frozen: the runner skips "+
				"everything at or below the recorded schema_meta.version, so this "+
				"edit will never reach a database that already applied it - "+
				"including your own local one. Add a NEW migration instead.",
				ver, wantHash, h)
		}
	}
}

// TestEveryAppliedMigrationIsPinned makes the freeze self-maintaining: a new
// migration must be pinned once it is part of a release, otherwise the guard
// silently stops covering the newest entries.
func TestEveryAppliedMigrationIsPinned(t *testing.T) {
	got := migrationSQLFromSource(t)
	var missing []int64
	for ver := range got {
		if ver <= frozenThrough {
			if _, ok := frozenMigrationHashes[ver]; !ok {
				missing = append(missing, ver)
			}
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	if len(missing) > 0 {
		t.Errorf("migrations %v are at or below frozenThrough=%d but not pinned in "+
			"frozenMigrationHashes; run TestPrintMigrationHashes and paste the output",
			missing, frozenThrough)
	}
}

// TestMigrationVersionsAreSequential catches a duplicated or skipped version
// number. The runner compares against a single integer watermark, so a gap
// or a repeat quietly changes which migrations run on which databases.
func TestMigrationVersionsAreSequential(t *testing.T) {
	got := migrationSQLFromSource(t)
	vers := make([]int64, 0, len(got))
	for v := range got {
		vers = append(vers, v)
	}
	sort.Slice(vers, func(i, j int) bool { return vers[i] < vers[j] })

	for i, v := range vers {
		if want := int64(i + 1); v != want {
			t.Fatalf("migration versions are not 1..N with no gaps: expected %d at "+
				"position %d, found %d (full list %v)", want, i, v, vers)
		}
	}
	if int64(len(vers)) < frozenThrough {
		t.Errorf("frozenThrough=%d but only %d migrations exist; frozenThrough must "+
			"never exceed the highest migration", frozenThrough, len(vers))
	}
}

// TestPrintMigrationHashes is not a test; it prints the pin table so adding a
// migration is a copy-paste. Run: go test ./internal/store/ -run
// TestPrintMigrationHashes -v
func TestPrintMigrationHashes(t *testing.T) {
	got := migrationSQLFromSource(t)
	vers := make([]int64, 0, len(got))
	for v := range got {
		vers = append(vers, v)
	}
	sort.Slice(vers, func(i, j int) bool { return vers[i] < vers[j] })

	var sb strings.Builder
	for _, v := range vers {
		sb.WriteString(fmt.Sprintf("\t%d: %q,\n", v, hashSQL(got[v])))
	}
	t.Log("frozenMigrationHashes entries:\n" + sb.String())
}

func hashSQL(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// migrationSQLFromSource reads the migrations table out of migrations.go via
// go/ast rather than importing the (unexported) variable, so the test sees
// exactly the literal text in the file and is not affected by how the slice
// is built at runtime.
func migrationSQLFromSource(t *testing.T) map[int64]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "migrations.go", nil, 0)
	if err != nil {
		t.Fatalf("parse migrations.go: %v", err)
	}

	out := map[int64]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "migrations" {
			return true
		}
		if len(vs.Values) != 1 {
			return false
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, el := range lit.Elts {
			entry, ok := el.(*ast.CompositeLit)
			if !ok || len(entry.Elts) != 2 {
				continue
			}
			verLit, ok := entry.Elts[0].(*ast.BasicLit)
			if !ok || verLit.Kind != token.INT {
				continue
			}
			ver, err := strconv.ParseInt(verLit.Value, 10, 64)
			if err != nil {
				continue
			}
			sqlLit, ok := entry.Elts[1].(*ast.BasicLit)
			if !ok || sqlLit.Kind != token.STRING {
				continue
			}
			sql, err := strconv.Unquote(sqlLit.Value)
			if err != nil {
				// Raw string literals unquote fine; anything else is a
				// shape we did not expect and must not silently skip.
				t.Fatalf("migration %d: cannot unquote SQL literal: %v", ver, err)
			}
			if _, dup := out[ver]; dup {
				t.Fatalf("migration version %d appears twice in migrations.go", ver)
			}
			out[ver] = sql
		}
		return false
	})

	if len(out) == 0 {
		t.Fatal("found no migrations in migrations.go - did the table move or change shape?")
	}
	return out
}
