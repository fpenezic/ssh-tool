// One-click MCP registration for Claude Desktop.
//
// Claude Desktop discovers MCP servers from a plain JSON file it reads once at
// startup. The entry we need is the same one the Settings page already shows
// for LM Studio - command + `--mcp-bridge` - so the only thing missing was
// knowing where the file lives and putting it there.
//
// This writes another application's configuration, which sets the bar for how
// careful the code has to be:
//
//   - MERGE, never replace. The user almost certainly has other MCP servers in
//     there, and stomping them would be a data-loss bug in someone else's app.
//   - Back the file up before touching it, so there is always a way back.
//   - Refuse to write when the existing file doesn't parse. A file we can't
//     read is a file we can't safely merge into; say so and let the user fix
//     it by hand rather than "helpfully" overwriting.
//   - Write atomically (temp file in the same directory, then rename), so an
//     interrupted write can't leave Claude Desktop with a truncated config.
//
// Caveat worth knowing: re-marshalling through a map sorts the top-level keys
// alphabetically, so the file comes back with its keys reordered. Harmless to
// a JSON parser, but it is why the backup exists.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// ClaudeDesktopInfo is what the Settings page renders: where the config is,
// whether we are already in it, and whether the entry still points at this
// binary (it won't after the app moves, e.g. a portable build on a new path).
type ClaudeDesktopInfo struct {
	Supported  bool   `json:"supported"`  // false on platforms Claude Desktop doesn't ship for
	Path       string `json:"path"`       // absolute path of claude_desktop_config.json
	Exists     bool   `json:"exists"`     // the file is there
	Parseable  bool   `json:"parseable"`  // it parses as JSON (true when absent - we'd create it)
	Registered bool   `json:"registered"` // an ssh-tool entry is present
	Stale      bool   `json:"stale"`      // present, but its command is a different binary
	ExePath    string `json:"exe_path"`   // the command we would write
}

// claudeDesktopServerKey is the name the entry gets under mcpServers. Matches
// the name used in the Claude Code and LM Studio snippets so the LLM sees the
// same server name everywhere.
const claudeDesktopServerKey = "ssh-tool"

// claudeDesktopConfigPath resolves the per-OS location of Claude Desktop's
// config. Empty string means "no Claude Desktop on this platform", which the
// UI turns into a hidden block rather than an error.
func claudeDesktopConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return ""
		}
		return filepath.Join(appdata, "Claude", "claude_desktop_config.json")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Claude",
			"claude_desktop_config.json")
	case "linux":
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "Claude", "claude_desktop_config.json")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	}
	return ""
}

// ClaudeDesktopStatus reports whether ssh-tool is registered with Claude
// Desktop. Read-only; safe to call on every Settings render.
func (a *App) ClaudeDesktopStatus() ClaudeDesktopInfo {
	info := ClaudeDesktopInfo{
		Path:    claudeDesktopConfigPath(),
		ExePath: a.AppExePath(),
	}
	if info.Path == "" {
		return info
	}
	info.Supported = true

	raw, err := os.ReadFile(info.Path)
	if err != nil {
		// Absent is the normal first-run case: we would create the file, so
		// nothing is unparseable and nothing is registered.
		info.Parseable = true
		return info
	}
	info.Exists = true

	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return info // Parseable stays false - the UI tells the user to fix it
	}
	info.Parseable = true

	servers := map[string]json.RawMessage{}
	if v, ok := top["mcpServers"]; ok {
		if json.Unmarshal(v, &servers) != nil {
			return info
		}
	}
	entry, ok := servers[claudeDesktopServerKey]
	if !ok {
		return info
	}
	info.Registered = true

	var parsed struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(entry, &parsed) == nil && parsed.Command != info.ExePath {
		info.Stale = true
	}
	return info
}

// ClaudeDesktopRegister merges an ssh-tool entry into Claude Desktop's config,
// creating the file (and its directory) if needed. Returns the path written.
//
// Deliberately NOT idempotent-silent: re-registering after the binary moves is
// the point of the Stale flag, so this always rewrites our own entry while
// leaving every other server untouched.
func (a *App) ClaudeDesktopRegister() (string, error) {
	path := claudeDesktopConfigPath()
	if path == "" {
		return "", fmt.Errorf("Claude Desktop is not available on this platform")
	}
	exe := a.AppExePath()
	if exe == "" {
		return "", fmt.Errorf("could not determine this application's path")
	}

	top := map[string]json.RawMessage{}
	raw, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := json.Unmarshal(raw, &top); err != nil {
			return "", fmt.Errorf("%s exists but is not valid JSON (%v) - fix or "+
				"remove it first; refusing to overwrite it", filepath.Base(path), err)
		}
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("read %s: %w", path, readErr)
	}

	servers := map[string]json.RawMessage{}
	if v, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(v, &servers); err != nil {
			return "", fmt.Errorf("the mcpServers section of %s is not an object (%v) - "+
				"refusing to overwrite it", filepath.Base(path), err)
		}
	}

	entry, err := json.Marshal(map[string]any{
		"command": exe,
		"args":    []string{"--mcp-bridge"},
	})
	if err != nil {
		return "", err
	}
	servers[claudeDesktopServerKey] = entry

	encoded, err := json.Marshal(servers)
	if err != nil {
		return "", err
	}
	top["mcpServers"] = encoded

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	// Back up whatever was there before we replace it. Best effort: a missing
	// backup is not a reason to refuse an otherwise valid write, but it is
	// worth a log line.
	if readErr == nil {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			log.Printf("claude desktop: backup %s: %v", path+".bak", err)
		}
	}
	if err := writeFileAtomic(path, out, 0o600); err != nil {
		return "", err
	}
	a.recordAudit("claude_desktop_register", claudeDesktopServerKey, map[string]string{
		"path":    path,
		"command": exe,
	})
	return path, nil
}

// writeFileAtomic writes via a temp file in the same directory and renames, so
// a crash mid-write leaves the original intact rather than a half-file that
// Claude Desktop would fail to parse on next start.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
