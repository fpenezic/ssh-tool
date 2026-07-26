package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedDefaultSnippets(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Fresh store: seeds the default set.
	if err := db.SeedDefaultSnippets(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := db.ListSnippets(nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected default snippets on a fresh store")
	}
	first := len(got)

	// At least one carries a ${var} placeholder (feature discovery).
	hasVar := false
	for _, s := range got {
		if strings.Contains(s.Body, "${") {
			hasVar = true
			break
		}
	}
	if !hasVar {
		t.Fatal("expected at least one default snippet with a ${var} placeholder")
	}

	// Idempotent: running again on a non-empty store adds nothing.
	if err := db.SeedDefaultSnippets(); err != nil {
		t.Fatalf("seed again: %v", err)
	}
	again, _ := db.ListSnippets(nil)
	if len(again) != first {
		t.Fatalf("re-seed changed count: %d -> %d", first, len(again))
	}
}
