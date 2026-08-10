package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readServers pulls the mcpServers map out of a written config.
func readServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("written config is not valid JSON: %v\n%s", err, raw)
	}
	return top.Servers
}

// TestClaudeDesktopRegisterPreservesOtherServers is the property that matters
// most: we are editing someone else's config, and blowing away their other MCP
// servers would be a data-loss bug in another application.
func TestClaudeDesktopRegisterPreservesOtherServers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	path := claudeDesktopConfigPath()
	if path == "" {
		t.Skip("no Claude Desktop config path on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "globalShortcut": "Alt+Space",
  "mcpServers": {
    "filesystem": {"command": "npx", "args": ["-y", "server-filesystem"]}
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	if _, err := a.ClaudeDesktopRegister(); err != nil {
		t.Fatalf("register: %v", err)
	}

	servers := readServers(t, path)
	if _, ok := servers["filesystem"]; !ok {
		t.Error("the user's existing filesystem server was lost")
	}
	ours, ok := servers[claudeDesktopServerKey]
	if !ok {
		t.Fatal("ssh-tool entry was not written")
	}
	if ours["command"] == "" {
		t.Error("ssh-tool entry has no command")
	}
	args, _ := ours["args"].([]any)
	if len(args) != 1 || args[0] != "--mcp-bridge" {
		t.Errorf("args = %v, want [--mcp-bridge]", ours["args"])
	}

	// Unrelated top-level keys survive the round-trip.
	raw, _ := os.ReadFile(path)
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if top["globalShortcut"] != "Alt+Space" {
		t.Errorf("globalShortcut = %v, want it preserved", top["globalShortcut"])
	}

	// And the original is recoverable.
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(bak) != existing {
		t.Error("backup does not match the original file")
	}
}

// TestClaudeDesktopRegisterCreatesFile covers the first-run case: no config, no
// directory, and we are the first MCP server the user adds.
func TestClaudeDesktopRegisterCreatesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	path := claudeDesktopConfigPath()
	if path == "" {
		t.Skip("no Claude Desktop config path on this platform")
	}

	a := &App{}
	written, err := a.ClaudeDesktopRegister()
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if written != path {
		t.Errorf("wrote %q, want %q", written, path)
	}
	if _, ok := readServers(t, path)[claudeDesktopServerKey]; !ok {
		t.Error("ssh-tool entry missing from a freshly created config")
	}
}

// TestClaudeDesktopRegisterRefusesBrokenJSON pins the refusal: a file we cannot
// parse is a file we cannot safely merge into, and overwriting it would destroy
// whatever the user had.
func TestClaudeDesktopRegisterRefusesBrokenJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	path := claudeDesktopConfigPath()
	if path == "" {
		t.Skip("no Claude Desktop config path on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := `{"mcpServers": {"filesystem": {` // truncated
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	if _, err := a.ClaudeDesktopRegister(); err == nil {
		t.Fatal("register accepted an unparseable config")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != broken {
		t.Error("the unparseable config was modified anyway")
	}
}

// TestClaudeDesktopStatusDetectsStale covers the case the Stale flag exists
// for: a portable build that moved, leaving the registered command pointing at
// a binary that is no longer this one.
func TestClaudeDesktopStatusDetectsStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	path := claudeDesktopConfigPath()
	if path == "" {
		t.Skip("no Claude Desktop config path on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"mcpServers": {"ssh-tool": {"command": "/old/path/ssh-tool", "args": ["--mcp-bridge"]}}}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	info := a.ClaudeDesktopStatus()
	if !info.Exists || !info.Parseable {
		t.Fatalf("status: exists=%v parseable=%v, want both true", info.Exists, info.Parseable)
	}
	if !info.Registered {
		t.Error("registered = false, want true")
	}
	if !info.Stale {
		t.Error("stale = false, want true for a command pointing elsewhere")
	}

	// Re-registering fixes it, and the status then reads clean.
	if _, err := a.ClaudeDesktopRegister(); err != nil {
		t.Fatalf("register: %v", err)
	}
	if info := a.ClaudeDesktopStatus(); !info.Registered || info.Stale {
		t.Errorf("after re-register: registered=%v stale=%v, want true/false",
			info.Registered, info.Stale)
	}
}

// TestClaudeDesktopStatusFlagsBrokenJSON makes sure the UI can tell the
// difference between "no config yet" and "config we must not touch".
func TestClaudeDesktopStatusFlagsBrokenJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	path := claudeDesktopConfigPath()
	if path == "" {
		t.Skip("no Claude Desktop config path on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	info := a.ClaudeDesktopStatus()
	if !info.Exists {
		t.Error("exists = false for a file that is there")
	}
	if info.Parseable {
		t.Error("parseable = true for a file that is not JSON")
	}

	// A missing file, by contrast, is "parseable" - we would create it.
	os.Remove(path)
	if info := a.ClaudeDesktopStatus(); info.Exists || !info.Parseable {
		t.Errorf("missing file: exists=%v parseable=%v, want false/true",
			info.Exists, info.Parseable)
	}
}
