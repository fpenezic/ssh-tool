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
	for _, name := range knownPlugins {
		p, ok := pluginPath(name)
		info := PluginInfo{Name: name, Installed: ok, Path: p, Supported: supported}
		if ok {
			info.Version = pluginVersion(p)
			// Only flag an update when we could read a real version and
			// it differs from the app. A "dev" app or unreadable helper
			// version stays quiet - we don't nag on dev builds.
			if info.Version != "" && info.Version != "dev" &&
				appVersion != "dev" && appVersion != "" &&
				info.Version != appVersion {
				info.UpdateAvailable = true
			}
		}
		out = append(out, info)
	}
	return out
}

// appReleaseTag returns the published release tag this app was built
// from, or "" for a local dev build that isn't tied to a tag.
//
// A tagged build stamps appVersion with exactly the tag: "v0.48.0" or
// a prerelease "v0.48.0-rc1". An untagged build stamps `git describe`
// output, "<tag>-<N>-g<sha>", where N is the commit count since the
// tag - that trailing "-<N>-g<sha>" is what tells a dev build apart
// from a real prerelease. "dev"/"" is a plain `go run`.
func appReleaseTag() string {
	v := appVersion
	if v == "" || v == "dev" || !strings.HasPrefix(v, "v") {
		return ""
	}
	// `git describe` suffix "-<N>-g<sha>": a dev build ahead of a tag.
	// A real prerelease ("-rc1", "-beta2") has no "-g<hex>" segment.
	if i := strings.LastIndex(v, "-g"); i >= 0 {
		// Everything after the last "-g" is a short hash on a dev build.
		if isHex(v[i+2:]) {
			return ""
		}
	}
	return v
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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
	// Fetch the release the helper should come from. app and helper are
	// built from the same tag, so the exact tag matching this app is the
	// right source - INCLUDING prereleases (an -rcN app must pull the
	// helper from its own -rcN release, not the latest stable). The tag
	// this app was built from is derived from appVersion:
	//   "v0.48.0"      -> v0.48.0        (clean release)
	//   "v0.48.0-rc1"  -> v0.48.0-rc1    (prerelease, exact)
	//   "v0.47.0-11-g<sha>" / "dev" -> latest stable (a local dev build
	//     that isn't tied to any published tag; best effort)
	tag := appReleaseTag()
	var rel *updater.ReleaseInfo
	var err error
	if tag != "" {
		rel, err = updater.FetchGitHubByTag(updateGitHubRepo, tag, ua)
	} else {
		rel, err = updater.FetchGitHubLatest(updateGitHubRepo, ua)
	}
	if err != nil {
		return "", fmt.Errorf("fetch release %s: %w", tag, err)
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
