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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/updater"
)

// knownPlugins is the set the UI offers.
var knownPlugins = []string{"netbird", "tailscale"}

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
	// Version is the installed helper's stamped version ("" if it
	// couldn't be read, "dev" for un-stamped local builds).
	Version string `json:"version"`
	// UpdateAvailable is true when the installed helper's version
	// differs from the running app - after an app update, the bundled
	// helper is a version behind and should be re-downloaded. Helper
	// and app share the release tag, so equality means up to date.
	UpdateAvailable bool `json:"update_available"`
	// Supported=false when this platform has no helper build
	// (android/ios - helpers are desktop-only).
	Supported bool `json:"supported"`
}

// pluginVersion runs `<helper> --version` and returns the stamped
// version line. Cheap (the flag exits before any network work); "" on
// any error so the UI just omits the version rather than failing.
func pluginVersion(exe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	hideConsole(cmd) // no console flash on Windows
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PluginsStatus reports every known plugin's install state + version.
func (a *App) PluginsStatus() []PluginInfo {
	out := make([]PluginInfo, 0, len(knownPlugins))
	// Only platforms the release actually builds a helper for. macOS
	// is excluded until the signed/notarised darwin helper ships (see
	// TODO) - otherwise the download would 404. A user who drops a
	// helper into the plugins dir by hand still works: pluginPath
	// finds it regardless of this flag.
	supported := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	// Latest published helper-release version, fetched once here (best
	// effort, short timeout). Helpers ship on their own helper-vN tag now,
	// decoupled from the app version, so "update available" compares the
	// installed helper against the newest helper RELEASE, not the app.
	// Empty on any failure (offline, rate-limited) -> we simply don't flag
	// an update rather than nagging or blocking the card.
	latestHelper := a.latestHelperVersion()
	for _, name := range knownPlugins {
		p, ok := pluginPath(name)
		info := PluginInfo{Name: name, Installed: ok, Path: p, Supported: supported}
		if ok {
			info.Version = pluginVersion(p)
			// Flag an update only when we could read a real installed
			// version AND know the latest helper release AND they differ.
			// A "dev" helper (un-stamped local build) never nags.
			if info.Version != "" && info.Version != "dev" &&
				latestHelper != "" && info.Version != latestHelper {
				info.UpdateAvailable = true
			}
		}
		out = append(out, info)
	}
	return out
}

// latestHelperVersion returns the tag of the newest helper release this
// app can speak to (helper-v<=MaxProtocol), or "" if it can't be
// determined right now. Best effort: any error yields "" so PluginsStatus
// degrades to "no update flagged" rather than failing.
func (a *App) latestHelperVersion() string {
	rel, err := updater.FetchGitHubHelperRelease(updateGitHubRepo, tunnelhelper.MaxProtocol(), "ssh-tool/"+appVersion)
	if err != nil {
		return ""
	}
	return rel.Version
}

// PluginDownload fetches the plugin binary for this platform from the
// newest compatible helper release, verifies the sha256 digest and
// installs it under DataDir/plugins. Returns the installed path.
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
	// Helpers ship on their own helper-vN tag now, decoupled from the app
	// version (docs/helper-release-plan.md). Pull from the newest helper
	// release whose protocol major this app speaks - so a helper can be
	// patched without an app release, and updating the app doesn't force a
	// helper re-download unless the protocol major actually changed.
	rel, err := updater.FetchGitHubHelperRelease(updateGitHubRepo, tunnelhelper.MaxProtocol(), ua)
	if err != nil {
		return "", fmt.Errorf("fetch helper release: %w", err)
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
