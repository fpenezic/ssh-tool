package main

// Plugin (sidecar helper) management: optional binaries that keep
// heavy overlay-network clients out of the main app (see
// netbird-helper/). Stored under <DataDir>/plugins/ - per-user
// writable, survives app updates; the directory of the main
// executable is honoured as a read-only fallback for portable / dev
// setups. Downloads come from the app's own GitHub release (same tag,
// so app and helper always match) and are sha256-verified against the
// release digest before being moved into place - the same trust chain
// the app updater itself uses.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ssh-tool/internal/store"
	"ssh-tool/internal/updater"
)

// knownPlugins is the set the UI offers. Tailscale will join here.
var knownPlugins = []string{"netbird"}

func pluginsDir() string {
	return filepath.Join(store.DataDir(), "plugins")
}

func pluginBinaryName(name string) string {
	bin := "ssh-tool-" + name
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin
}

// pluginPath returns the installed path of a plugin binary and
// whether it exists. DataDir/plugins wins; next to the app executable
// is the portable fallback.
func pluginPath(name string) (string, bool) {
	primary := filepath.Join(pluginsDir(), pluginBinaryName(name))
	if st, err := os.Stat(primary); err == nil && !st.IsDir() {
		return primary, true
	}
	if exe, err := os.Executable(); err == nil {
		side := filepath.Join(filepath.Dir(exe), pluginBinaryName(name))
		if st, err := os.Stat(side); err == nil && !st.IsDir() {
			return side, true
		}
	}
	return primary, false
}

// pluginAssetName is the GitHub release asset filename for this
// platform: ssh-tool-netbird-linux-amd64, ssh-tool-netbird-windows-amd64.exe...
func pluginAssetName(name string) string {
	asset := fmt.Sprintf("ssh-tool-%s-%s-%s", name, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	return asset
}

// PluginInfo is the Settings "Plugins" card row.
type PluginInfo struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	// Supported=false when this platform has no helper build
	// (android/ios - helpers are desktop-only).
	Supported bool `json:"supported"`
}

// PluginsStatus reports every known plugin's install state.
func (a *App) PluginsStatus() []PluginInfo {
	out := make([]PluginInfo, 0, len(knownPlugins))
	supported := runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	for _, name := range knownPlugins {
		p, ok := pluginPath(name)
		out = append(out, PluginInfo{Name: name, Installed: ok, Path: p, Supported: supported})
	}
	return out
}

// PluginDownload fetches the plugin binary for this platform from the
// app's own GitHub release, verifies the sha256 digest and installs
// it under DataDir/plugins. Returns the installed path.
func (a *App) PluginDownload(name string) (string, error) {
	valid := false
	for _, k := range knownPlugins {
		if k == name {
			valid = true
		}
	}
	if !valid {
		return "", fmt.Errorf("unknown plugin %q", name)
	}

	ua := "ssh-tool/" + appVersion
	var rel *updater.ReleaseInfo
	var err error
	// Prefer the release matching the running app so app + helper
	// always come from the same tag; dev builds fall back to latest.
	if strings.HasPrefix(appVersion, "v") && !strings.Contains(appVersion, "-") {
		rel, err = updater.FetchGitHubByTag(updateGitHubRepo, appVersion, ua)
	} else {
		rel, err = updater.FetchGitHubLatest(updateGitHubRepo, ua)
	}
	if err != nil {
		return "", fmt.Errorf("fetch release: %w", err)
	}
	asset, ok := rel.AssetsByName[pluginAssetName(name)]
	if !ok {
		return "", fmt.Errorf("release %s has no %s asset", rel.Version, pluginAssetName(name))
	}
	if asset.SHA256 == "" {
		return "", fmt.Errorf("release asset carries no sha256 digest; refusing unverified download")
	}

	if err := os.MkdirAll(pluginsDir(), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(pluginsDir(), ".dl-"+name+"-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(asset.URL)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		tmp.Close()
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 512<<20)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, asset.SHA256) {
		return "", fmt.Errorf("sha256 mismatch: got %s want %s", got, asset.SHA256)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(pluginsDir(), pluginBinaryName(name))
	// Replace atomically; Windows needs the old file gone first.
	_ = os.Remove(dst)
	if err := os.Rename(tmpPath, dst); err != nil {
		return "", fmt.Errorf("install: %w", err)
	}
	a.recordAudit("plugin.install", name, map[string]string{"version": rel.Version, "sha256": asset.SHA256})
	EventsEmit("plugins_changed", name)
	return dst, nil
}

// PluginRemove deletes an installed plugin binary (DataDir copy only;
// a portable side-by-side binary is the user's own to manage).
func (a *App) PluginRemove(name string) error {
	dst := filepath.Join(pluginsDir(), pluginBinaryName(name))
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	a.recordAudit("plugin.remove", name, nil)
	EventsEmit("plugins_changed", name)
	return nil
}
