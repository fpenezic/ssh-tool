# Helper release split + protocol versioning (pre-Tailscale)

Groundwork before adding Tailscale as a third tunnel kind. Goal: the
sidecar helpers (NetBird today, Tailscale next) evolve on their OWN
release cadence, decoupled from the app version, with an explicit
protocol handshake so an app + helper mismatch fails clearly instead of
mysteriously.

> **Superseded in v0.111.0:** the helpers moved to their own repository,
> [ssh-tool-plugins](https://github.com/fpenezic/ssh-tool-plugins), one
> tag namespace per plugin. See "Move to ssh-tool-plugins" at the end;
> the sections before it are the history of how the split started.

Decision (author, v0.50.0): SAME repo, SEPARATE release tag. Helpers live in
`ssh-tool` (`netbird-helper/`, later `tailscale-helper/`) but ship under
a `helper-vN` tag on their own GitHub release. The app fetches from the
newest `helper-*` release, not from its own app release.

## Why now

- Tailscale will be a sidecar too (tsnet userspace + SOCKS5, same shape
  as netbird-helper). Adding it under the current model (helpers keyed
  to the app tag) then moving both to a separate cadence = building the
  TS helper twice. Do the split first, add TS on the clean base.
- App and helper today share a version string (`-X main.version` from
  the app's `git describe`) and the download is keyed to the app's own
  release tag (`appReleaseTag()` in app_plugins.go). That couples them:
  every app release forces a helper re-download even if the helper
  binary is byte-identical, and a helper can't be patched without an app
  release.

## Protocol versioning

The helper protocol already exists (line-JSON on stdout: `ready` /
`status` / `error`; stdin-close = shutdown - see tunnelhelper.go). It
just has no version negotiation.

Add a `protocol` integer to the `ready` event:

```json
{"event":"ready","socks":"127.0.0.1:PORT","protocol":1}
```

- The app declares a supported range (`minProtocol`..`maxProtocol`,
  currently 1..1) in tunnelhelper.
- On `ready`, if `protocol` is outside the range, stop the helper and
  return a clear error: "helper speaks protocol N, app needs M - update
  the helper" (or the app). The Settings plugin card already surfaces
  helper errors; word it so the user knows which side to update.
- A missing `protocol` field = protocol 0 (pre-versioning helper) ->
  treated as too old, prompts a re-download. Old helpers thus fail
  loudly with an actionable message instead of a silent behavioural
  drift.
- Bump maxProtocol only on a breaking wire change (new required field,
  changed shutdown semantics). Additive events (a new optional status
  field) don't need a bump.

## Release + tagging

- New tag namespace: `helper-vN` (e.g. `helper-v1`), a plain integer
  major that tracks the protocol major. A helper release contains all
  helper binaries for all platforms (netbird + tailscale x os/arch).
- Patched rebuilds of the same protocol (dependency fixes) ship as
  `helper-vN.P` (`helper-v1.1`): release tags are immutable, so
  `helper-vN` cannot be re-released. The app picks the highest major it
  speaks, then the highest patch. Apps from before patch tags ignore
  `helper-vN.P` and keep `helper-vN` until the app itself is updated.
- CI: a `helper-*` tag triggers a helpers-only workflow (build-helpers
  job, no app/android). Publishes a GitHub release named `helper-vN`
  with the helper assets. The app-tag workflow STOPS building helpers.
- Asset names unchanged: `ssh-tool-<provider>-<os>-<arch>[.exe]`, so the
  app's `pluginAssetName()` still matches.

## App-side download rework (app_plugins.go)

- `PluginDownload` currently calls `appReleaseTag()` then
  `FetchGitHubByTag/Latest`. Change it to fetch the newest `helper-*`
  release whose protocol major the app supports:
  - Query releases, filter to tags matching `helper-v<maxProtocol>`
    (the app knows which major it speaks), take the newest.
  - Fall back to the highest `helper-v<=maxProtocol>` if the exact major
    has no release yet.
- Version tracking: the helper's `--version` still prints its own stamp,
  but the app's "update available" check compares against the newest
  `helper-*` release version, NOT the app version. So a helper update
  ships without an app release, and installing the app no longer flags a
  phantom helper update.
- `updater.FetchGitHub*` may need a "list releases + filter by tag
  prefix" variant; check what's there.

## Build stamp

- Helper `-X main.version` comes from the `helper-vN` describe, not the
  app's. Update the helper Taskfile(s) / CI so the stamp is the helper
  release tag. (Desktop app stamp logic untouched.)

## Migration / back-compat

- A user on v0.49.0 (helpers still in the app release) upgrading the app
  to the first post-split version: their installed helper is protocol 0
  (no field) -> flagged for re-download from `helper-v1`. One re-download,
  then decoupled forever.
- Keep the app able to READ a helper from the app-release era (protocol
  0) only far enough to give the "update your helper" message, not to
  run it.

## Status

Done and shipped in v0.50.0 / helper-v1: the split, protocol v1, the
NetBird helper on the new track, AND the Tailscale helper below.

## Tailscale (implemented)

Built on the split + protocol v1 base, released together as helper-v1:
- `tailscale-helper/` module: tsnet userspace + the same SOCKS5 + the
  same protocol (ready/status/error, protocol:1).
- Auth: Tailscale auth key (`tskey-auth-...`) stored as an api_token
  credential, same as the NetBird setup key. Reusable key for multi-
  machine sync (each machine its own node), same guidance as NB.
- App: `kindTailscale`, profile create/edit UI, device/hostname default
  (`<hostname>` as the tailnet node name), presence excluded like NB
  (each machine its own node - no single-owner conflict).
- Needs a Tailscale account/tailnet to test - author will set up a test
  tenant when we get there.

## Move to ssh-tool-plugins (v0.111.0)

The OpenConnect plugin brought a GPL-3.0 engine (sing-openconnect). It
cannot share a module tree with the Apache-2.0 app, and a third plugin
on one shared `helper-vN` tag made every plugin release rebuild the
others. So all plugins moved out:

- Repository `fpenezic/ssh-tool-plugins`: `netbird/`, `tailscale/`,
  `openconnect/`, each its own Go module and `LICENSE` (Apache-2.0,
  Apache-2.0, GPL-3.0-or-later). The protocol is documented there in
  `PROTOCOL.md`; the app talks to a plugin only over that process
  boundary.
- Tags per plugin: `netbird-vN[.P]`, `tailscale-vN[.P]`,
  `openconnect-vN[.P]`, N = protocol major as before. Each tag builds
  and publishes only that plugin (linux amd64 / arm64, windows amd64),
  never marked Latest, with a SHA256SUMS file next to the assets.
- App: `sourceOf(name)` = plugins repo + `<name>-v`;
  `FetchGitHubPluginRelease(repo, prefix, maxMajor)` picks the release.
  The asset names did not change.
- The first plugins-repo releases continue the patch numbering
  (`netbird-v1.2`, `tailscale-v1.2`) so nobody reads them as older than
  `helper-v1.1`. An installed helper still stamped `helper-v1.1` differs
  from the new tag and is offered the update once.
- The app repository keeps its `helper-v1` / `helper-v1.1` releases for
  app versions from before the move; nothing new is published there.

The OpenVPN plugin (`openvpn/`, Apache-2.0, own client in
`openvpn/internal/ovpn`) joined later on `openvpn-vN[.P]` tags, same
protocol 1: `auth` events with `username` / `password` / `response`
fields, and a `--check` flag the app runs on import to validate a
configuration.

The Check Point plugin (`checkpoint/`, AGPL-3.0: its Go client in
`checkpoint/internal/snx` is based on snx-rs, which is AGPL) followed on `checkpoint-vN[.P]` tags, protocol
1 unchanged: the same `username` / `password` / `response` auth fields as
OpenVPN and the `cert_pin` trust flow of OpenConnect.

Protocol 1 grew additively for OpenConnect (still 1): `auth` events and
`auth` / `cancel` commands for interactive login, `info` events with the
tunnel details, and `cert_pin` / `cert_subject` / `cert_changed` on an
`error` for trust-on-first-use of the gateway certificate.
