package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"ssh-tool/internal/store"
)

// Settings -> About: what this binary is made of and what it runs on, read
// at runtime so nothing has to be kept up to date by hand. Module versions
// come from the build info Go embeds in every binary (it survives -trimpath
// and -s -w); the rendering engine from Wails' environment probe. Nothing
// here touches the network or the profile, so "Copy diagnostics" is safe to
// paste into a public issue.

// AboutComponent is one named dependency and its version.
type AboutComponent struct {
	Name    string `json:"name"`
	Module  string `json:"module,omitempty"`
	Version string `json:"version"`
}

// AboutPlugin is an installed (or missing) sidecar helper.
type AboutPlugin struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// EngineVersion: the NetBird / Tailscale library inside the helper.
	EngineVersion string `json:"engine_version,omitempty"`
}

// AboutInfo is everything the About panel shows beyond AppVersion.
type AboutInfo struct {
	CommitDate string           `json:"commit_date"`
	GoVersion  string           `json:"go_version"`
	OS         string           `json:"os"`      // runtime.GOOS/GOARCH
	OSName     string           `json:"os_name"` // "Windows 11 Pro 24H2", distro name, ...
	Engine     string           `json:"engine"`  // "WebView2 140.0..." / "WebKitGTK 2.48.1"
	Components []AboutComponent `json:"components"`
	Plugins    []AboutPlugin    `json:"plugins"`
	DataDir    string           `json:"data_dir"`
	LogDir     string           `json:"log_dir"`
}

// The modules worth naming, in display order. Everything else in the build
// info is a transitive detail nobody files a bug about.
var aboutModules = []struct{ name, module string }{
	{"Wails", "github.com/wailsapp/wails/v3"},
	{"opkssh", "github.com/openpubkey/opkssh"},
	{"OpenPubkey", "github.com/openpubkey/openpubkey"},
	{"Go SSH (x/crypto)", "golang.org/x/crypto"},
	{"WireGuard", "golang.zx2c4.com/wireguard"},
	{"gVisor netstack", "gvisor.dev/gvisor"},
	{"SQLite (modernc)", "modernc.org/sqlite"},
	{"go-pty", "github.com/aymanbagabas/go-pty"},
}

// moduleVersions maps module path -> version from the embedded build info,
// following replace directives so a local override shows what really
// shipped.
func moduleVersions() map[string]string {
	out := map[string]string{}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}
	for _, d := range bi.Deps {
		m := d
		if d.Replace != nil {
			m = d.Replace
		}
		v := m.Version
		if v == "" || v == "(devel)" {
			v = "local"
		}
		out[d.Path] = v
	}
	return out
}

func aboutComponents() []AboutComponent {
	vers := moduleVersions()
	out := make([]AboutComponent, 0, len(aboutModules))
	for _, m := range aboutModules {
		v, ok := vers[m.module]
		if !ok {
			continue // not linked into this build (platform-specific)
		}
		out = append(out, AboutComponent{Name: m.name, Module: m.module, Version: v})
	}
	return out
}

// engineVersion reads the webview engine from Wails' environment probe:
// "WebView2" on Windows, "webkitgtk*-runtime" on Linux. Empty when the app
// is not up yet or the platform reports nothing (macOS).
func engineVersion() (engine, osName string) {
	app := application.Get()
	if app == nil || app.Env == nil {
		return "", ""
	}
	info := app.Env.Info()
	if info.OSInfo != nil {
		osName = strings.TrimSpace(strings.Join(nonEmpty(info.OSInfo.Name, info.OSInfo.Version, info.OSInfo.Branding), " "))
	}
	if v, ok := info.PlatformInfo["WebView2"].(string); ok && v != "" {
		return "WebView2 " + v, osName
	}
	keys := make([]string, 0, len(info.PlatformInfo))
	for k := range info.PlatformInfo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "webkitgtk") && strings.HasSuffix(k, "-runtime") {
			return fmt.Sprintf("WebKitGTK %v", info.PlatformInfo[k]), osName
		}
	}
	return "", osName
}

func nonEmpty(xs ...string) []string {
	out := xs[:0:0]
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

// AppAbout returns the About panel's build and environment details.
func (a *App) AppAbout() AboutInfo {
	engine, osName := engineVersion()
	info := AboutInfo{
		CommitDate: appCommitDate,
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS + "/" + runtime.GOARCH,
		OSName:     osName,
		Engine:     engine,
		Components: aboutComponents(),
		DataDir:    store.DataDir(),
		LogDir:     filepath.Join(store.DataDir(), "logs"),
	}
	// Installed helpers and their stamped version only - unlike
	// PluginsStatus this never asks GitHub for the latest release.
	for _, name := range knownPlugins {
		p, ok := pluginPath(name)
		ap := AboutPlugin{Name: name, Installed: ok}
		if ok {
			if _, err := os.Stat(p); err == nil {
				ap.Version = pluginVersion(p)
				ap.EngineVersion = pluginEngineVersion(name, p)
			}
		}
		info.Plugins = append(info.Plugins, ap)
	}
	return info
}

// AboutOpenDir reveals the data or log folder in the file manager. Takes a
// name, not a path: the frontend never gets to pick what is opened.
func (a *App) AboutOpenDir(which string) error {
	switch which {
	case "data":
		return openDirInFileManager(store.DataDir())
	case "logs":
		return openDirInFileManager(filepath.Join(store.DataDir(), "logs"))
	}
	return fmt.Errorf("unknown folder %q", which)
}
