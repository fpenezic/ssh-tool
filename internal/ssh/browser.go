package ssh

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// LaunchOptions controls per-call overrides. PreferredPath lets the user
// pin a specific browser binary; we sniff its name to pick the engine
// (chromium-family vs firefox) and apply the right flag set.
type LaunchOptions struct {
	PreferredPath string // explicit binary; empty = auto-detect
}

// LaunchIsolatedBrowser opens a browser pointed at a SOCKS5 proxy, in an
// isolated user-data-dir so cookies and sessions don't bleed into the
// user's everyday browsing.
//
// The dispatcher resolves a binary, sniffs the engine, and hands off to
// chromium- or firefox-specific launch. Returns the PID of the spawned
// child for the UI to track.
func LaunchIsolatedBrowser(socksAddr string, socksPort uint16, url string, opts LaunchOptions) (int, error) {
	if socksAddr == "" {
		socksAddr = "127.0.0.1"
	}

	// Resolve binary. Order: user override -> platform default.
	bin, engine, err := resolveBrowser(opts.PreferredPath)
	if err != nil {
		return 0, err
	}
	log.Printf("browser: bin=%s engine=%s", bin, engine)

	switch engine {
	case engineChromium:
		return launchChromium(bin, socksAddr, socksPort, url)
	case engineFirefox:
		return launchFirefox(bin, socksAddr, socksPort, url)
	default:
		return 0, fmt.Errorf("unknown browser engine for %s", bin)
	}
}

// ----- engine detection -----

type browserEngine int

const (
	engineUnknown browserEngine = iota
	engineChromium
	engineFirefox
)

func (e browserEngine) String() string {
	switch e {
	case engineChromium:
		return "chromium"
	case engineFirefox:
		return "firefox"
	}
	return "unknown"
}

// sniffEngine identifies the browser engine from the binary's filename.
// Conservative: defaults to chromium for unknown names (most TLS bin we
// haven't named explicitly is a chromium reskin - Vivaldi, Opera, Naver
// Whale, etc).
func sniffEngine(path string) browserEngine {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "firefox"),
		strings.Contains(base, "librewolf"),
		strings.Contains(base, "waterfox"),
		strings.Contains(base, "tor-browser"):
		return engineFirefox
	}
	return engineChromium
}

// resolveBrowser returns (binary path, engine, error). If preferredPath is
// supplied and exists+executable, we use it; otherwise platform default.
func resolveBrowser(preferredPath string) (string, browserEngine, error) {
	if preferredPath != "" {
		expanded := expandUserPath(preferredPath)
		if isExecutable(expanded) {
			return expanded, sniffEngine(expanded), nil
		}
		log.Printf("browser: preferred path not usable (%s); falling back to default", expanded)
	}

	// WSL is a special case: skip Linux-side browsers (snap chromium dies
	// under WSLg) and go straight to the Windows host's browser.
	if isWSL() {
		bin, err := findWindowsBrowser()
		if err != nil {
			return "", engineUnknown, err
		}
		return bin, sniffEngine(bin), nil
	}

	bin, err := findBrowser()
	if err != nil {
		return "", engineUnknown, err
	}
	return bin, sniffEngine(bin), nil
}

// ----- chromium-family launch -----

func launchChromium(bin, socksAddr string, socksPort uint16, url string) (int, error) {
	profile, err := chromiumProfileDir()
	if err != nil {
		return 0, err
	}
	args := []string{
		"--user-data-dir=" + profile,
		fmt.Sprintf("--proxy-server=socks5://%s:%d", proxyHostForBrowser(bin, socksAddr), socksPort),
		"--proxy-bypass-list=<-loopback>",
		"--no-default-browser-check",
		"--no-first-run",
		"--disable-features=ChromeWhatsNewUI",
	}
	if url != "" {
		args = append(args, url)
	}
	log.Printf("browser: %s %s", bin, strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn %s: %w", bin, err)
	}
	go func() { _ = cmd.Process.Release() }()
	return cmd.Process.Pid, nil
}

// proxyHostForBrowser picks the right address to give the browser:
//
//  - WSL → Windows browser: proxy listens inside WSL on 127.0.0.1:N, but
//    a Windows browser sees the WSL loopback as `localhost` thanks to
//    WSL2's mirror networking, so we just say "localhost".
//  - Everything else: pass through whatever the caller asked for.
func proxyHostForBrowser(bin, socksAddr string) string {
	if isWSL() && strings.HasPrefix(bin, "/mnt/") {
		return "localhost"
	}
	return socksAddr
}

// chromiumProfileDir picks a writable, isolated user-data-dir for this
// launch. We use the user's temp area + a timestamp suffix so concurrent
// "Open in browser" clicks each get their own profile.
func chromiumProfileDir() (string, error) {
	// On WSL → Windows browser, the user-data-dir must be a Windows path.
	if isWSL() {
		winTemp, err := winEnv("TEMP")
		if err != nil || winTemp == "" {
			if lad, e := winEnv("LOCALAPPDATA"); e == nil && lad != "" {
				winTemp = lad + `\Temp`
			} else {
				winTemp = `C:\Windows\Temp`
			}
		}
		return fmt.Sprintf(`%s\ssh-tool-browser-%d`, winTemp, time.Now().UnixNano()), nil
	}
	dir, err := os.MkdirTemp("", "ssh-tool-browser-*")
	if err != nil {
		return "", fmt.Errorf("temp profile: %w", err)
	}
	return dir, nil
}

// ----- firefox launch -----

func launchFirefox(bin, socksAddr string, socksPort uint16, url string) (int, error) {
	profile, err := os.MkdirTemp("", "ssh-tool-firefox-*")
	if err != nil {
		return 0, fmt.Errorf("temp profile: %w", err)
	}
	// Firefox needs the proxy preference written into prefs.js (well,
	// user.js, which Firefox copies into prefs.js on first launch). It
	// has no command-line flag for SOCKS.
	prefs := fmt.Sprintf(`
user_pref("network.proxy.type", 1);
user_pref("network.proxy.socks", "%s");
user_pref("network.proxy.socks_port", %d);
user_pref("network.proxy.socks_version", 5);
user_pref("network.proxy.socks_remote_dns", true);
user_pref("network.proxy.no_proxies_on", "");
user_pref("browser.shell.checkDefaultBrowser", false);
user_pref("startup.homepage_welcome_url", "");
user_pref("browser.startup.homepage_override.mstone", "ignore");
user_pref("toolkit.telemetry.reportingpolicy.firstRun", false);
`, socksAddr, socksPort)
	if err := os.WriteFile(filepath.Join(profile, "user.js"), []byte(prefs), 0o600); err != nil {
		return 0, fmt.Errorf("write firefox prefs: %w", err)
	}
	args := []string{"-profile", profile, "-no-remote"}
	if url != "" {
		args = append(args, url)
	}
	log.Printf("browser: %s %s", bin, strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn %s: %w", bin, err)
	}
	go func() { _ = cmd.Process.Release() }()
	return cmd.Process.Pid, nil
}

// ----- platform-default detection (chromium-only fallback path) -----

func findBrowser() (string, error) {
	for _, c := range chromiumCandidates() {
		if isExecutable(c) {
			return c, nil
		}
	}
	// PATH fallback. Firefox added here too so that on a Linux box where
	// the only browser is firefox, we still find something usable.
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"microsoft-edge", "microsoft-edge-stable", "brave-browser",
		"firefox", "librewolf",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no supported browser found; install chrome / chromium / edge / brave / firefox or pin a path in Settings")
}

func chromiumCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
			"/Applications/Firefox.app/Contents/MacOS/firefox",
			"/Applications/LibreWolf.app/Contents/MacOS/librewolf",
		}
	case "windows":
		pf := os.Getenv("ProgramFiles")
		pfx86 := os.Getenv("ProgramFiles(x86)")
		local := os.Getenv("LOCALAPPDATA")
		var out []string
		for _, root := range []string{pf, pfx86, local} {
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(root, "Chromium", "Application", "chrome.exe"),
				filepath.Join(root, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
				filepath.Join(root, "Vivaldi", "Application", "vivaldi.exe"),
				filepath.Join(root, "Mozilla Firefox", "firefox.exe"),
				filepath.Join(root, "LibreWolf", "librewolf.exe"),
			)
		}
		return out
	default:
		// Linux / BSD.
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
			"/usr/bin/brave-browser",
			"/usr/bin/vivaldi",
			"/usr/bin/firefox",
			"/usr/bin/librewolf",
			"/snap/bin/chromium",
			"/snap/bin/google-chrome",
			"/snap/bin/firefox",
		}
	}
}

// ----- WSL → Windows host detection -----

func isWSL() bool {
	return os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != ""
}

func findWindowsBrowser() (string, error) {
	candidates := []string{
		// Edge (preinstalled on Win10+)
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		// Chrome
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		// Brave
		`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		`C:\Program Files (x86)\BraveSoftware\Brave-Browser\Application\brave.exe`,
		// Vivaldi
		`C:\Program Files\Vivaldi\Application\vivaldi.exe`,
		// Chromium
		`C:\Program Files\Chromium\Application\chrome.exe`,
		// Firefox
		`C:\Program Files\Mozilla Firefox\firefox.exe`,
		`C:\Program Files (x86)\Mozilla Firefox\firefox.exe`,
	}
	for _, win := range candidates {
		mnt := winToMnt(win)
		if isExecutable(mnt) {
			return mnt, nil
		}
	}
	return "", fmt.Errorf("no Windows-side browser found at standard paths; install Edge / Chrome / Firefox on the Windows host or pin a path in Settings")
}

func winToMnt(p string) string {
	if len(p) < 3 || p[1] != ':' {
		return p
	}
	drive := strings.ToLower(string(p[0]))
	rest := strings.ReplaceAll(p[3:], `\`, "/")
	return "/mnt/" + drive + "/" + rest
}

// winEnv reads a Windows environment variable via cmd.exe. Resolved
// Win32 paths come back (e.g. C:\Users\Foo\AppData\Local\Temp).
func winEnv(name string) (string, error) {
	out, err := exec.Command("cmd.exe", "/c", "echo %"+name+"%").Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if strings.HasPrefix(s, "%") && strings.HasSuffix(s, "%") {
		return "", fmt.Errorf("env %s not set", name)
	}
	return s, nil
}

// ----- helpers -----

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// expandUserPath resolves ~/foo. Also tolerates Windows-style paths the
// user might paste in if they're on WSL - we leave those alone since
// they'll be exec'd via /mnt/c automatically when matched at runtime.
func expandUserPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	// If the user gave a Windows path while on WSL, translate to /mnt/c.
	if isWSL() && len(p) > 2 && p[1] == ':' {
		return winToMnt(p)
	}
	return p
}
