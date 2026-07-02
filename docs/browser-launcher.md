# Browser launcher

How "Open in browser" works per platform and how the user can override it.

## What we need

When the user starts a SOCKS5 dynamic forward and clicks "Open in browser",
we want to:

1. Spawn the user's browser (or one they prefer for tunneled traffic).
2. Configure it to route HTTP(S) traffic through `socks5://localhost:<port>`
   so domain-based services behind the bastion resolve remotely.
3. Use an isolated user-data-dir so we don't pollute the user's everyday
   browsing session with proxy settings.

## Per-platform default detection

`findBrowser()` walks platform-specific candidate paths and returns the
first executable. Order chosen to match what users likely already have
configured as their default.

### Linux native (not WSL)

Candidates checked in order:

```
/usr/bin/google-chrome
/usr/bin/google-chrome-stable
/usr/bin/chromium
/usr/bin/chromium-browser
/usr/bin/microsoft-edge
/usr/bin/microsoft-edge-stable
/usr/bin/brave-browser
/snap/bin/chromium
/snap/bin/google-chrome
```

Plus PATH fallback via `exec.LookPath` for the same binaries.

**Gotcha**: snap-packaged chromium runs fine on native Linux but fails
under WSLg with `ptrace: Operation not permitted`. We handle WSL
separately (see below). On native Linux we don't avoid snap chromium -
it works.

### macOS

Candidates:

```
/Applications/Google Chrome.app/Contents/MacOS/Google Chrome
/Applications/Chromium.app/Contents/MacOS/Chromium
/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge
/Applications/Brave Browser.app/Contents/MacOS/Brave Browser
```

Direct binary spawn with the chromium flag set.

### Windows native

Candidates assembled from `%ProgramFiles%`, `%ProgramFiles(x86)%`,
`%LOCALAPPDATA%`:

```
<root>\Google\Chrome\Application\chrome.exe
<root>\Microsoft\Edge\Application\msedge.exe
<root>\Chromium\Application\chrome.exe
<root>\BraveSoftware\Brave-Browser\Application\brave.exe
```

Direct exec; Go's exec on Windows handles arg quoting cleanly.

### WSL (Windows Subsystem for Linux)

Snap chromium dies under WSLg sandboxing, so we ignore in-WSL browsers
entirely and spawn the **Windows host's** browser through the WSL → Win32
boundary by exec-ing the .exe directly via its `/mnt/c/...` path. WSL2
exposes Win32 binaries this way; arg lists pass through cleanly without a
shell-quoting middleman.

The SOCKS port listening inside WSL on `127.0.0.1:N` is reachable from the
Windows side as `localhost:N` thanks to WSL2's loopback forwarding.

The user-data-dir must be a Windows-side path. We resolve `%TEMP%` via
`cmd.exe /c echo %TEMP%` once at launch and substitute the result in
`--user-data-dir`. Chromium does not expand env vars in flag values.

If a user installs Google Chrome on the WSL side via the Google deb repo
(not snap), they can override the launcher to use it - see "User override"
below.

## Per-engine flag differences

### Chromium-family (Chrome / Chromium / Edge / Brave / Vivaldi)

All accept the same flags:

```
--proxy-server=socks5://host:port
--proxy-bypass-list=<-loopback>
--user-data-dir=<path>
--no-default-browser-check
--no-first-run
--disable-features=ChromeWhatsNewUI
[url]
```

`socks5://` (without the 'h') already tunnels DNS through the proxy in
Chromium - we don't need `--host-resolver-rules` tricks.

### Firefox / LibreWolf

Firefox has **no command-line flag for SOCKS proxy**. To tunnel:

1. Create a temp profile directory.
2. Drop a `user.js` (or `prefs.js`) with:
   ```
   user_pref("network.proxy.type", 1);
   user_pref("network.proxy.socks", "localhost");
   user_pref("network.proxy.socks_port", 5056);
   user_pref("network.proxy.socks_version", 5);
   user_pref("network.proxy.socks_remote_dns", true);
   user_pref("network.proxy.no_proxies_on", "localhost,127.0.0.1");
   user_pref("browser.shell.checkDefaultBrowser", false);
   user_pref("startup.homepage_welcome_url", "");
   ```
3. Spawn `firefox -profile <dir> -no-remote [url]`.

The `-no-remote` flag forces a new process (otherwise Firefox attaches to
an existing instance and ignores our profile).

### Safari / Other

Not supported. Safari has no per-process proxy CLI; system-wide only.

## User override

A `preferred_browser_path` setting (in `app_settings` table) lets the user
pin a specific binary. We detect the engine by binary name:

- contains `firefox` / `librewolf` / `waterfox` → Firefox flow
- otherwise → Chromium flow

The launcher falls back to platform detection if `preferred_browser_path`
is empty or the file doesn't exist.

## TODO

- [x] Linux native default detection
- [x] macOS default detection
- [x] Windows native default detection
- [x] WSL → Windows host browser detection
- [ ] User-overridable browser path in app settings UI
- [ ] Firefox engine flow (profile dir + prefs.js)
- [ ] Per-launch "Choose browser" dialog (one-shot override)
- [ ] Test on real macOS + real Windows install (currently only WSL/Linux
  tested)
- [ ] Detect Tor Browser specifically (different default args than vanilla
  Firefox; tor-browser-bundle launcher script)
