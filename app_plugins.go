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
	"debug/buildinfo"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
	"ssh-tool/internal/updater"
)

// knownPlugins is the set the UI offers.
var knownPlugins = []string{"netbird", "tailscale", "openconnect", "openvpn", "checkpoint"}

// pluginSource is where a plugin's releases live: the separate plugins
// repository, one tag namespace per plugin (<name>-vN[.P]), each plugin
// with its own licence (the OpenConnect one is GPL). App versions before
// the move fetched NetBird and Tailscale from this repository's helper-vN
// releases; those stay published for them.
type pluginSource struct {
	repo, tagPrefix string
}

const pluginsGitHubRepo = "fpenezic/ssh-tool-plugins"

func sourceOf(name string) pluginSource {
	return pluginSource{pluginsGitHubRepo, name + "-v"}
}

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
	// EngineVersion is the NetBird / Tailscale library the helper was
	// built with (e.g. "0.80.0"), read from the binary's Go build info, so
	// it works for helpers published before anyone thought to print it.
	EngineVersion string `json:"engine_version"`
	// Latest is the newest helper release this app speaks ("" when it
	// could not be fetched).
	Latest string `json:"latest"`
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

// pluginEngineModule is the Go module each helper embeds.
var pluginEngineModule = map[string]string{
	"netbird":     "github.com/netbirdio/netbird",
	"tailscale":   "tailscale.com",
	"openconnect": "github.com/sagernet/sing-openconnect",
}

// pluginEngineVersion reads the embedded engine's module version from the
// helper binary's Go build info (no exec, so no console flash and no
// helper change needed). "" when the binary has none or is not Go.
func pluginEngineVersion(name, exe string) string {
	mod := pluginEngineModule[name]
	if mod == "" {
		return ""
	}
	bi, err := buildinfo.ReadFile(exe)
	if err != nil {
		return ""
	}
	for _, d := range bi.Deps {
		if d.Path != mod {
			continue
		}
		v := d.Version
		if d.Replace != nil && d.Replace.Version != "" {
			v = d.Replace.Version
		}
		return strings.TrimPrefix(v, "v")
	}
	return ""
}

// PluginDownloadProgress is the plugin_download_progress event: which
// step a plugin download is on, and bytes for the download step.
type PluginDownloadProgress struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"` // resolve | download | verify | install
	Version string `json:"version,omitempty"`
	Read    int64  `json:"read"`
	Total   int64  `json:"total"`
}

// PluginsStatus reports every known plugin's install state + version.
func (a *App) PluginsStatus() []PluginInfo {
	// Only platforms the release actually builds a helper for. macOS
	// is excluded until the signed/notarised darwin helper ships (see
	// TODO) - otherwise the download would 404. A user who drops a
	// helper into the plugins dir by hand still works: pluginPath
	// finds it regardless of this flag.
	supported := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	// Latest published release per plugin: plugins ship on their own
	// <name>-vN tags, decoupled from the app version, so "update
	// available" compares the installed plugin against its newest
	// RELEASE, not the app. A plugin still stamped helper-vN (installed
	// before the move) differs from any <name>-vN tag, so it is offered
	// the update. One API call for all of them, cached (pluginLatest);
	// "" on any failure, so the card never nags or blocks offline.
	latest := a.pluginLatest()
	// The --version probes start a process each (a console-less one on
	// Windows, still ~100 ms): run them side by side.
	out := make([]PluginInfo, len(knownPlugins))
	var wg sync.WaitGroup
	for i, name := range knownPlugins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, ok := pluginPath(name)
			info := PluginInfo{Name: name, Installed: ok, Path: p, Supported: supported, Latest: latest[name]}
			if ok {
				info.Version = pluginVersion(p)
				info.EngineVersion = pluginEngineVersion(name, p)
				// Flag an update only when we could read a real installed
				// version AND know the latest release AND they differ. A
				// "dev" helper (un-stamped local build) never nags.
				if info.Version != "" && info.Version != "dev" &&
					info.Latest != "" && info.Version != info.Latest {
					info.UpdateAvailable = true
				}
			}
			out[i] = info
		}()
	}
	wg.Wait()
	return out
}

// pluginLatestCache is the newest release tag per plugin, refreshed at
// most every pluginLatestTTL (the Settings page and the update check both
// ask; GitHub's anonymous API allows 60 calls an hour).
var pluginLatestCache struct {
	sync.Mutex
	at     time.Time
	latest map[string]string
}

const pluginLatestTTL = 10 * time.Minute

func (a *App) pluginLatest() map[string]string {
	pluginLatestCache.Lock()
	defer pluginLatestCache.Unlock()
	if pluginLatestCache.latest != nil && time.Since(pluginLatestCache.at) < pluginLatestTTL {
		return pluginLatestCache.latest
	}
	prefixes := make([]string, 0, len(knownPlugins))
	for _, name := range knownPlugins {
		prefixes = append(prefixes, sourceOf(name).tagPrefix)
	}
	rels, err := updater.FetchGitHubPluginReleases(pluginsGitHubRepo, prefixes, tunnelhelper.MaxProtocol(), "ssh-tool/"+appVersion)
	latest := map[string]string{}
	if err != nil {
		return latest // not cached: the next look tries again
	}
	for _, name := range knownPlugins {
		if r := rels[sourceOf(name).tagPrefix]; r != nil {
			latest[name] = r.Version
		}
	}
	pluginLatestCache.at, pluginLatestCache.latest = time.Now(), latest
	return latest
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

	progress := func(p PluginDownloadProgress) {
		p.Name = name
		EventsEmit("plugin_download_progress", p)
	}
	progress(PluginDownloadProgress{Phase: "resolve"})

	ua := "ssh-tool/" + appVersion
	// Plugins ship on their own <name>-vN tags, decoupled from the app
	// version (docs/helper-release-plan.md). Pull from the newest plugin
	// release whose protocol major this app speaks - so a helper can be
	// patched without an app release, and updating the app doesn't force a
	// helper re-download unless the protocol major actually changed.
	src := sourceOf(name)
	rel, err := updater.FetchGitHubPluginRelease(src.repo, src.tagPrefix, tunnelhelper.MaxProtocol(), ua)
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
	total := resp.ContentLength
	if total <= 0 {
		total = asset.Size
	}
	var lastEmit time.Time
	pw := &progressCounter{onWrite: func(read int64) {
		if now := time.Now(); now.Sub(lastEmit) >= 150*time.Millisecond || read == total {
			lastEmit = now
			progress(PluginDownloadProgress{Phase: "download", Version: rel.Version, Read: read, Total: total})
		}
	}}
	if _, err := io.Copy(io.MultiWriter(tmp, h, pw), io.LimitReader(resp.Body, 512<<20)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	progress(PluginDownloadProgress{Phase: "verify", Version: rel.Version, Read: total, Total: total})
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, asset.SHA256) {
		return "", fmt.Errorf("sha256 mismatch: got %s want %s", got, asset.SHA256)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	progress(PluginDownloadProgress{Phase: "install", Version: rel.Version, Read: total, Total: total})
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

// progressCounter counts bytes written through it and reports the running
// total.
type progressCounter struct {
	n       int64
	onWrite func(n int64)
}

func (p *progressCounter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	p.onWrite(p.n)
	return len(b), nil
}
