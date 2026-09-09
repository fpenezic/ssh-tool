package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestBuiltinIconsMatchFrontend guards the Go mirror of the frontend icon
// list. The two files are edited months apart, and a name that exists in only
// one of them fails silently at runtime: the MCP tool rejects an icon the
// picker offers, or accepts one the tree cannot draw.
func TestBuiltinIconsMatchFrontend(t *testing.T) {
	src, err := os.ReadFile("frontend/src/lib/builtinIcons.ts")
	if err != nil {
		t.Skipf("frontend source not available: %v", err)
	}
	start := strings.Index(string(src), "BUILTIN_ICONS")
	if start < 0 {
		t.Fatal("BUILTIN_ICONS not found in builtinIcons.ts")
	}
	body := string(src)[start:]
	if end := strings.Index(body, "];"); end >= 0 {
		body = body[:end]
	}

	re := regexp.MustCompile(`\{\s*name:\s*"([^"]+)"`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("parsed no icon names from builtinIcons.ts")
	}

	for name := range found {
		if !validIconName(name) {
			t.Errorf("icon %q exists in the frontend but not in builtinIconLabels", name)
		}
	}
	for name := range builtinIconLabels {
		if !found[name] {
			t.Errorf("icon %q is in builtinIconLabels but no longer in the frontend", name)
		}
	}
}

// TestIconPaletteMatchesFrontend does the same for the colour names.
func TestIconPaletteMatchesFrontend(t *testing.T) {
	src, err := os.ReadFile("frontend/src/lib/palette.ts")
	if err != nil {
		t.Skipf("frontend source not available: %v", err)
	}
	start := strings.Index(string(src), "export const palette")
	if start < 0 {
		t.Fatal("palette not found in palette.ts")
	}
	body := string(src)[start:]
	if end := strings.Index(body, "];"); end >= 0 {
		body = body[:end]
	}

	re := regexp.MustCompile(`\{\s*name:\s*"([^"]+)"`)
	var names []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatal("parsed no colour names from palette.ts")
	}

	for _, n := range names {
		if !validIconColor(n) {
			t.Errorf("colour %q exists in the frontend but not in iconPaletteColors", n)
		}
	}
	if len(names) != len(iconPaletteColors) {
		t.Errorf("colour count drifted: frontend has %d, Go has %d", len(names), len(iconPaletteColors))
	}
}

func TestValidIconColorAcceptsEmpty(t *testing.T) {
	// Empty means "no explicit colour" - the tools must not force one.
	if !validIconColor("") {
		t.Error("empty colour should be valid")
	}
	if validIconColor("#ff0000") {
		t.Error("hex colours are deliberately rejected by the tools")
	}
	if validIconColor("chartreuse") {
		t.Error("unknown colour name should be rejected")
	}
}
