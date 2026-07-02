# Architecture

> **Note:** Earlier drafts described a Tauri / russh stack. That
> implementation lives on the `rust-legacy` branch but isn't the
> active code. See `04-tech-stack.md` for the current stack.

## High-level

```
┌────────────────────────────────────────────────────────────────┐
│  Wails v3 application process                                  │
│                                                                │
│  ┌────────────────────────────┐  ┌────────────────────────┐    │
│  │  Main window (Svelte 5)    │  │  Go backend            │    │
│  │  ┌──────────────────────┐  │  │  ┌──────────────────┐  │    │
│  │  │  Sidebar (conn/cred) │  │  │  │  store           │  │    │
│  │  │  Tab bar             │  │  │  │  (SQLite, v11)   │  │    │
│  │  │  Split panes         │◄─┼──┼──┤                  │  │    │
│  │  │   ↳ xterm.js × N     │  │  │  ├──────────────────┤  │    │
│  │  │  Detail panes        │  │  │  │  creds (vault)   │  │    │
│  │  └──────────────────────┘  │  │  ├──────────────────┤  │    │
│  └────────────────────────────┘  │  │  resolver        │  │    │
│           ▲                      │  │  (inheritance)   │  │    │
│           │  IPC                 │  ├──────────────────┤  │    │
│           │  (commands + events) │  │  ssh             │  │    │
│           ▼                      │  │  (session pool,  │  │    │
│  ┌────────────────────────────┐  │  │   opkssh, fwds,  │  │    │
│  │  Detached window 1..N      │  │  │   tcpdump)       │  │    │
│  │  (Svelte, xterm.js)        │◄─┼──┤                  │  │    │
│  │  Shares session pool       │  │  ├──────────────────┤  │    │
│  └────────────────────────────┘  │  │  inventory       │  │    │
│                                  │  │  (Proxmox/Hetzner│  │    │
│                                  │  │  refresh timers) │  │    │
│                                  │  ├──────────────────┤  │    │
│                                  │  │  importer (RDM,  │  │    │
│                                  │  │  ssh_config)     │  │    │
│                                  │  ├──────────────────┤  │    │
│                                  │  │  httpc (probe)   │  │    │
│                                  │  ├──────────────────┤  │    │
│                                  │  │  local (PTY)     │  │    │
│                                  │  └──────────────────┘  │    │
│                                  └────────────────────────┘    │
└────────────────────────────────────────────────────────────────┘
```

## Process model

- **One Wails application process** holds all backend logic and
  the SQLite connection.
- **Multiple webview windows**: main + detached. Each is its own
  OS window; the backend is shared.
- **SSH sessions live in the backend** as goroutines, independent
  of webview lifecycle. Closing a detached window can disconnect
  its sessions (configurable) but the rest stay live.

## IPC model

All IPC is generated from `app.go` `App` methods via
`wails3 generate bindings`. The frontend `api.ts` facade wraps the
generated bindings with plain TS types so call sites don't see the
`convertValues` member.

Representative commands (front → back, request/response):
- `FoldersList()`, `ConnectionsList()`, `ConnectionsCreate(...)`,
  `ConnectionsUpdate(...)`, `ConnectionsDelete(...)`
- `ConnectionResolve(id)` → merged settings after inheritance
- `SshConnect(connectionID)` → `{ session_id }` (blocks until PTY
  is open)
- `SshDisconnect(sessionID)`
- `SshWrite(sessionID, data)`, `SshResize(sessionID, cols, rows)`
- `ForwardStart(sessionID, spec)`, `ForwardStop(forwardID)`
- `DynamicFoldersList()`, `DynamicEntriesList(folderID)`,
  `DynamicFolderRefreshNow(folderID)`,
  `SshConnectDynamic(folderID, entryID)`
- `ExportSubtree(folderIDs, options)`,
  `ImportArchive(path, options)`

Events (back → front, push):
- `pty_output:{sessionID}` → bytes
- `session_state:{sessionID}` → connected | disconnected | error
- `forward_state:{forwardID}` → listening | error | closed
- `host_key_challenge` / response via `SshRespondHostKey(...)`
- `connect_progress:{connectionID}` → "TCP dial", "Handshake", …
- `dynamic_folder_refreshed:{folderID}`
- `broadcast_changed`, `quit_request`, `window_redock`, `app_log`

## State management

**Backend = source of truth.** The frontend reads everything via
IPC. Svelte stores cache recent reads and invalidate on the
relevant events. The `tree.version` counter is bumped on every
`tree.load()` so `$derived` consumers that need a guaranteed re-
run can read it.

**Multi-window state sync**: every webview subscribes to the same
events. The backend's `runtime.EventsEmit` fans out to all
registered windows.

## Threading

- Go runtime, multiple goroutines per session (read loop, write
  pump, forward listeners).
- SQLite: single write connection (serial), read connections
  share the underlying DB handle through `modernc.org/sqlite`.
- Per-SSH-session: a `Session` struct with mutex-protected
  scrollback + cum watermark. Forward listeners attach as child
  goroutines and are cleaned up on session close via
  `Session.SetOnClose`.
- BroadcastManager: in-memory `map[string]struct{}` of session ids
  in the current group, guarded by a mutex.

## Failure isolation

- A crash inside one SSH goroutine doesn't bring down the rest -
  goroutines log + clean up via `closedOnce.Do`. Panics propagate
  to the runtime; structured errors return through the channel.
- Crash in the backend = whole app down (acceptable for a desktop
  app).
- Crash in a detached webview = other windows keep working; the
  backend session pool is unaffected.

## Detach + redock flow

1. User drags a tab off the tabbar; `dragover` heartbeat detects
   the drop is outside the window.
2. Frontend calls `WindowStartTabDrag(tabID, sessionIDs, layout)`
   then `WindowDetachTab(tabID, sessionIDs, layout)`. `layout` is
   a URL-safe base64 JSON blob holding the full `PaneTab`
   (title, pane tree, group meta) so splits / titles / group
   chips survive the move.
3. Backend opens a new `WebviewWindow` with URL
   `?detached=<tabID>&sessions=<csv>&layout=<b64>` and
   `Name = detached-<tabID>`.
4. New window mounts its xterm instances and subscribes to
   `pty_output:{sessionID}` - the backend session pool is
   untouched, so scrollback survives. The pane tree is restored
   from the layout blob; internal pane / split / tab ids are
   regenerated so the moved tab can't collide with anything in
   the destination window.
5. Re-dock: detached window registers the drag payload (with the
   layout blob) under its *original* tabID. Main window picks it
   up via `WindowAcceptTabDrag`, rebuilds the tab from the layout
   (falling back to flat one-tab-per-session reconstruction only
   when the layout is missing - defensive path for legacy
   detaches), calls `WindowCloseSelf(name)` on the detached
   window.

See `docs/gotchas.md` for the heartbeat trick, the original-
tabID pitfall, and why the layout blob is required (without it,
detach used to flatten every split pane into a separate tab).
