<script lang="ts">
  import { tree, credentials, view, sessions, paneTabs, hostKeyStore, authPromptStore, mcpApprovalStore, mcpShared, mcpBridge, shareApprovalStore, shareShared, shareBridge, decodePaneLayoutsMulti, closedTabs, selection, type HostKeyChallenge } from "./lib/stores.svelte";
  import { isMobile } from "./lib/platform";
  import { installMobileBackNav } from "./lib/mobileBackNav";
  import { api } from "./lib/api";
  import { EventsOn } from "./lib/wailsRuntime";
  import { focusActiveTerminal } from "./lib/terminalFocus";
  import Sidebar from "./lib/Sidebar.svelte";
  import DetailPane from "./lib/DetailPane.svelte";
  import CredentialList from "./lib/CredentialList.svelte";
  import CredentialDetail from "./lib/CredentialDetail.svelte";
  import CredentialCreate from "./lib/CredentialCreate.svelte";
  import TerminalArea from "./lib/TerminalArea.svelte";
  import VaultGate from "./lib/VaultGate.svelte";
  import { broadcast } from "./lib/broadcast.svelte";
  import { recording, recordingsModal } from "./lib/recording.svelte";
  import { syncState } from "./lib/syncState.svelte";
  import RecordingsModal from "./lib/RecordingsModal.svelte";
  import { deepLink } from "./lib/deepLink.svelte";
  import { IconHost, IconKey, IconSettings, IconTerminal, IconSearch } from "./lib/iconMap";
  import Settings from "./lib/Settings.svelte";
  import QuickPalette from "./lib/QuickPalette.svelte";
  import SnippetPalette from "./lib/SnippetPalette.svelte";
  import ToastHost from "./lib/ToastHost.svelte";
  import { toast } from "./lib/toast.svelte.ts";
  import { showConfirm } from "./lib/confirmModal.svelte.ts";
  import type { PaletteAction } from "./lib/QuickPalette.svelte";
  import { localShellPrefs, type LocalShellKind } from "./lib/localShellPrefs.svelte.ts";
  import HostKeyModal from "./lib/HostKeyModal.svelte";
  import AuthPromptModal from "./lib/AuthPromptModal.svelte";
  import McpApprovalModal from "./lib/McpApprovalModal.svelte";
  import ShareApprovalModal from "./lib/ShareApprovalModal.svelte";
  import ContextMenu from "./lib/ContextMenu.svelte";
  import ExportConnectionsModal from "./lib/ExportConnectionsModal.svelte";
  import { exportModal } from "./lib/exportModal.svelte.ts";
  import FolderPicker from "./lib/FolderPicker.svelte";
  import DeleteConfirm from "./lib/DeleteConfirm.svelte";
  import PromptModal from "./lib/PromptModal.svelte";
  import ConfirmModal from "./lib/ConfirmModal.svelte";
  import PresenceTakeoverModal from "./lib/PresenceTakeoverModal.svelte";
  import { connectionActions } from "./lib/connectionActions.svelte";
  import DetachedWindow from "./lib/DetachedWindow.svelte";
  import ResizeHandle from "./lib/ResizeHandle.svelte";
  import { layoutPrefs } from "./lib/layoutPrefs.svelte";
  import { appPrefs } from "./lib/appPrefs.svelte";
  import { updateCheck } from "./lib/updateCheck.svelte";
  import { dynEditor } from "./lib/dynEditor.svelte";
  import DynamicFolderEditor from "./lib/DynamicFolderEditor.svelte";
  import { vaultPrefs } from "./lib/vaultPrefs.svelte";
  import { lastSession } from "./lib/lastSession.svelte";
  import StatusBar from "./lib/StatusBar.svelte";

  // Load UI preferences early so density / base font size apply
  // before any tree row renders - avoids the brief "compact then
  // jump to cozy" reflow on app start.
  appPrefs.load();
  vaultPrefs.load();
  localShellPrefs.load();
  lastSession.load();

  let showCreate = $state(false);
  // Folder context for the upcoming credential create - set when the
  // user triggers "+ Credential" from inside a folder header. Reset
  // after the modal opens so the next time + button at the top of
  // the list opens with no preselected folder.
  let createTargetFolderId = $state<string | null>(null);
  let vaultReady = $state(false);
  // When the user (or the idle timer) locks the vault, we want the next
  // VaultGate to demand the passphrase - not silently auto-unlock through
  // the sidecar. Set on lock, cleared on a real unlock.
  let suppressAutoUnlock = $state(false);
  let showPalette = $state(false);
  let showSnippetPalette = $state(false);
  let showShortcuts = $state(false);

  // Detached-window mode: the backend opens new top-level windows with
  // URL like "/?detached=<tabId>". When that param exists we render a
  // minimal layout (just TerminalArea + a Redock button) instead of
  // the full main-window UI.
  const urlParams = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : new URLSearchParams();
  const detachedTab = urlParams.get("detached");
  const isDetached = detachedTab !== null;
  // The window name is derived from the tabId in the backend
  // (WindowDetachTab) so detached windows can reference themselves
  // when closing on redock.
  const detachedWindowName = detachedTab ? `detached-${detachedTab}` : "";

  // Global Ctrl+K / Cmd+K toggles the quick palette. Captured at window
  // level so it works regardless of which view is focused.
  //
  // Tab-navigation shortcuts (Ctrl+Tab / Ctrl+Shift+Tab / Ctrl+1..9 /
  // Ctrl+Shift+W / Ctrl+Shift+T) only fire when we're already in the
  // terminal view - otherwise they'd hijack key combos in Settings /
  // Connections (e.g. Ctrl+1 picking a sidebar item). Ctrl+W / Ctrl+T
  // unshifted are reserved for the shell (readline delete-word /
  // transpose-chars) so we never grab those.
  function onGlobalKey(e: KeyboardEvent) {
    if (!vaultReady) return;
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      showPalette = !showPalette;
      return;
    }
    // Ctrl+Shift+P opens the snippet palette. The palette renders
    // its own "no active session" empty state when there are no
    // terminal tabs, so we don't gate on session count anymore -
    // the user should at least see the palette open instead of
    // wondering whether the shortcut is wired.
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === "p") {
      e.preventDefault();
      showSnippetPalette = !showSnippetPalette;
      return;
    }

    const ctrl = e.ctrlKey || e.metaKey;
    if (!ctrl) return;

    // Ctrl+Tab / Ctrl+Shift+Tab - cycle through terminal tabs with
    // wrap-around. Fires only when the terminal view is showing; in
    // Connections / Settings the combo does nothing so we don't
    // surprise the user mid-form.
    if (e.key === "Tab") {
      if (view.tab !== "terminal") return;
      e.preventDefault();
      paneTabs.cycleActive(e.shiftKey ? -1 : 1);
      focusActiveTerminal();
      return;
    }

    // Ctrl+1..8 - jump to tab N. Ctrl+9 - jump to last tab
    // (Chrome / VS Code parity).
    if (!e.shiftKey && !e.altKey && /^[1-9]$/.test(e.key)) {
      if (view.tab !== "terminal") return;
      const n = Number(e.key);
      const ok = n === 9
        ? paneTabs.activateIndex(paneTabs.tabs.length - 1)
        : paneTabs.activateIndex(n - 1);
      if (ok) {
        e.preventDefault();
        focusActiveTerminal();
      }
      return;
    }

    // Ctrl+Shift+W - close the active terminal tab. Shifted variant
    // chosen over plain Ctrl+W so we don't fight readline's
    // delete-word in the embedded shell.
    if (e.shiftKey && e.key.toLowerCase() === "w") {
      if (view.tab !== "terminal") return;
      const id = paneTabs.activeTabId;
      if (!id) return;
      e.preventDefault();
      closeActiveTabFromShortcut(id);
      return;
    }

    // Ctrl+Shift+T - reopen the most recently closed tab. Pulls one
    // entry off the closed-tabs stack and sshConnect's each
    // connection it carried; local shells aren't reopenable so they
    // were skipped at close time.
    if (e.shiftKey && e.key.toLowerCase() === "t") {
      e.preventDefault();
      reopenLastClosedTab();
      return;
    }
  }

  // closeActiveTabFromShortcut is the Ctrl+Shift+W path. We don't
  // import TerminalArea's closeTab directly (it's component-local),
  // so we inline the same disconnect-then-remove sequence here. The
  // tab is already pushed to closedTabs via the same path TerminalArea
  // uses when it tears it down - except here we have to do it
  // ourselves before removing.
  async function closeActiveTabFromShortcut(tabId: string) {
    const tab = paneTabs.tabs.find((t) => t.tabId === tabId);
    if (!tab) return;
    const root = tab.root;
    const sessionIds = new Set<string>();
    function collect(node: typeof root) {
      if (node.kind === "pane") sessionIds.add(node.sessionId);
      else { collect(node.a); collect(node.b); }
    }
    collect(root);
    const reopenIds: string[] = [];
    for (const sid of sessionIds) {
      const s = sessions.tabs.find((x) => x.sessionId === sid);
      if (s && s.kind !== "local" && s.connectionId) reopenIds.push(s.connectionId);
    }
    if (reopenIds.length > 0) {
      closedTabs.push({
        title: tab.title,
        connectionIds: reopenIds,
        groupName: tab.groupName,
        groupColor: tab.groupColor,
        closedAt: Date.now(),
      });
    }
    for (const sid of sessionIds) {
      const s = sessions.tabs.find((x) => x.sessionId === sid);
      try {
        if (s?.kind === "local") await api.localShellDisconnect(sid);
        else await api.sshDisconnect(sid);
      } catch {}
      sessions.remove(sid);
    }
    paneTabs.removeTab(tabId);
    if (paneTabs.tabs.length === 0) view.setTab("connections");
  }

  async function reopenLastClosedTab() {
    const entry = closedTabs.pop();
    if (!entry) return;
    let firstSession: string | null = null;
    for (const connId of entry.connectionIds) {
      const c = tree.connectionById(connId);
      if (!c) continue;
      try {
        const r = await api.sshConnect(connId);
        sessions.add({
          sessionId: r.session_id,
          connectionId: connId,
          name: c.name,
          hostname: c.hostname,
          status: "connected",
        });
        if (!firstSession) {
          firstSession = r.session_id;
          const t = paneTabs.addTab(r.session_id, entry.title || c.name);
          if (entry.groupName) paneTabs.setGroup(t.tabId, entry.groupName, entry.groupColor);
        } else {
          paneTabs.addTab(r.session_id, c.name);
        }
      } catch (e) {
        console.error("reopen connect failed", connId, e);
      }
    }
    if (firstSession) view.setTab("terminal");
  }

  // window_redock fires from a detached window when the user clicks
  // "Re-dock". The backend session pool is shared, so we just refresh
  // our session list - any session living in that detached tab
  // already exists here too (via sshActiveSessions); the redock signal
  // is mostly cosmetic for now but the hook is wired for future
  // session-routing finesse.
  if (!isDetached) {
    EventsOn<{ tabId: string; sessions: string; layout: string }>("window_redock", async ({ sessions: sids, layout: layoutBlob }) => {
      const sessionIds = sids ? sids.split(",") : [];
      if (sessionIds.length === 0) return;
      const wanted = new Set(sessionIds);
      // A redock can carry MULTIPLE tabs (the whole detached window). Decode
      // them all so none are lost.
      const layouts = decodePaneLayoutsMulti(layoutBlob ?? "");
      try {
        // Hydrate SessionStore for every session referenced by the
        // redock. Layout restore (below) maps tree leaves onto these.
        const live = (await api.sshActiveSessions()) ?? [];
        for (const s of live) {
          if (!wanted.has(s.session_id)) continue;
          if (!sessions.tabs.find((t) => t.sessionId === s.session_id)) {
            sessions.add({
              sessionId: s.session_id,
              connectionId: s.connection_id,
              name: s.name,
              hostname: s.hostname,
              status: "connected",
            });
          }
        }
        // Local PTY side - without this, redocking a tab that
        // carried a local terminal showed nothing in the main
        // window: the session pool still has the PTY but the
        // store was never told about it.
        const locals = (await api.localShellList()) ?? [];
        for (const l of locals) {
          if (!wanted.has(l.session_id)) continue;
          if (!sessions.tabs.find((t) => t.sessionId === l.session_id)) {
            sessions.add({
              sessionId: l.session_id,
              connectionId: "local:" + l.session_id,
              name: l.display || l.kind,
              hostname: l.kind,
              status: "connected",
              kind: "local",
            });
          }
        }
        // Restore the pane tree from the serialized layout so splits
        // and group metadata come back intact. Fall back to one tab
        // per session when the detached window predates the layout
        // payload (defensive - both sides ship in the same release).
        if (layouts.length > 0) {
          for (const lay of layouts) paneTabs.addTabFromLayout(lay);
        } else {
          for (const s of live) {
            if (!wanted.has(s.session_id)) continue;
            paneTabs.addTab(s.session_id, s.name);
          }
          for (const l of locals) {
            if (!wanted.has(l.session_id)) continue;
            paneTabs.addTab(l.session_id, l.display || l.kind);
          }
        }
        view.setTab("terminal");
      } catch (e) {
        console.error("redock restore failed", e);
      }
    });
  }

  // Warn before quit if the user closes the window with active SSH
  // sessions. Backend cancels the close and emits "quit_request"; we
  // surface a modal and call ConfirmQuit() if the user accepts.
  let quitPromptCount = $state<number | null>(null);
  EventsOn("quit_request", (count: any) => {
    quitPromptCount = typeof count === "number" ? count : Number(count ?? 0);
    api.requestAttention().catch(() => {});
  });

  async function confirmQuitAccept() {
    quitPromptCount = null;
    api.clearAttention().catch(() => {});
    try { await api.confirmQuit(); } catch (e) { console.error("confirm quit failed", e); }
  }

  let termMenuOpen = $state(false);

  // Per-platform list of in-app local shells the dropdown offers.
  // The bare "Local shell" button uses "" / auto which picks a
  // sensible default ($SHELL / wsl / powershell) on the backend.
  const platform = (typeof navigator !== "undefined"
    ? navigator.userAgent.toLowerCase()
    : "");
  const isWin = platform.includes("windows");
  const isMac = platform.includes("mac");

  // On a narrow/mobile screen the two-pane split (tree | detail) doesn't
  // fit, so it collapses to single-pane: the tree by default, sliding to
  // the detail pane once the user selects a connection or folder. The
  // detail pane shows a Back affordance to return. Desktop ignores this
  // (both panes always visible).
  const mobileShowDetail = $derived(
    isMobile &&
      (selection.selectedConnection() !== null ||
        selection.selectedFolder() !== null ||
        selection.current.kind === "dynamicEntry"),
  );
  const mobileShowCredDetail = $derived(
    isMobile && (selection.selectedCredential() !== null || selection.selectedCredentialFolder() !== null),
  );
  function mobileBack() {
    selection.select({ kind: "none" });
  }

  // System-back (Android) routing. A back step is meaningful when a mobile
  // detail pane is open, or when we're on a secondary tab (terminal /
  // settings / credentials) rather than the connections list. goBack()
  // reverses one such step; at the root there's nothing to reverse and the
  // OS back exits the app.
  const mobileCanGoBack = $derived(
    isMobile && (mobileShowDetail || mobileShowCredDetail || view.tab !== "connections"),
  );
  function mobileGoBack() {
    if (mobileShowDetail || mobileShowCredDetail) {
      mobileBack();
    } else if (view.tab !== "connections") {
      view.setTab("connections");
    }
  }
  let mobileBackTick: (() => void) | null = null;
  $effect(() => {
    if (!isMobile) return;
    const nav = installMobileBackNav({
      canGoBack: () => mobileCanGoBack,
      goBack: mobileGoBack,
    });
    mobileBackTick = nav.tick;
    nav.tick();
    return () => {
      mobileBackTick = null;
      nav.dispose();
    };
  });
  // Re-arm a synthetic history entry whenever a back step becomes available.
  $effect(() => {
    void mobileCanGoBack;
    mobileBackTick?.();
  });
  const localShellOptions: { kind: string; label: string; short: string }[] = isWin
    ? [
        { kind: "wsl",        label: "WSL (default distro)", short: "WSL" },
        { kind: "powershell", label: "PowerShell",            short: "PowerShell" },
        { kind: "cmd",        label: "Command Prompt",        short: "cmd" },
      ]
    : isMac
      ? [
          { kind: "zsh",  label: "zsh",  short: "zsh" },
          { kind: "bash", label: "bash", short: "bash" },
        ]
      : [
          { kind: "bash", label: "bash", short: "bash" },
          { kind: "zsh",  label: "zsh",  short: "zsh"  },
          { kind: "sh",   label: "sh",   short: "sh"   },
        ];

  // Short label shown on the bare "Local shell" button so the user
  // sees which kind a plain click will open. Falls back to a generic
  // word when no preference is set yet.
  const localShellButtonLabel = $derived.by(() => {
    const k = localShellPrefs.kind;
    if (!k) return "Local shell";
    const opt = localShellOptions.find((o) => o.kind === k);
    return opt ? `Local: ${opt.short}` : "Local shell";
  });

  // Resolve the kind to actually spawn. Explicit `kind` argument
  // wins (dropdown click), then the saved preference, then "" so the
  // backend picks an auto fallback.
  async function openLocalShell(kind?: string, dir?: string) {
    const effective = kind ?? localShellPrefs.kind ?? "";
    try {
      const res = await api.localShellOpen(effective, dir ?? "", 120, 32);
      sessions.add({
        sessionId: res.session_id,
        connectionId: "",
        name: res.display,
        hostname: res.kind,
        kind: "local",
        status: "connected",
      });
      paneTabs.addTab(res.session_id, res.display);
      view.setTab("terminal");
    } catch (e: any) {
      toast.err(`Failed to open local shell: ${e?.message ?? String(e)}`);
    }
  }

  // App commands for the quick palette (">" prefix or fuzzy match).
  // Workspace-open rows are built inside the palette itself.
  const paletteActions: PaletteAction[] = [
    {
      id: "open-settings",
      title: "Open Settings",
      hint: "open",
      keywords: ["settings", "preferences", "config", "options"],
      run: () => view.setTab("settings"),
    },
    {
      id: "new-local-shell",
      title: "New local shell tab",
      hint: "open",
      keywords: ["shell", "terminal", "local", "cmd", "powershell", "bash"],
      run: () => openLocalShell(),
    },
    {
      id: "lock-vault",
      title: "Lock vault",
      hint: "lock",
      keywords: ["vault", "lock", "security", "passphrase"],
      run: async () => {
        const ok = await showConfirm({
          title: "Lock vault",
          message: "Lock the vault now? You'll be prompted to unlock on the next vault-backed action.",
          okLabel: "Lock",
        });
        if (!ok) return;
        // Forget the sidecar so the next launch prompts too - same
        // semantics as the status-bar lock.
        await api.vaultLock(true);
        window.dispatchEvent(new CustomEvent("vault-lock-now"));
        toast.ok("Vault locked");
      },
    },
    {
      id: "toggle-recording",
      title: "Record / stop recording session",
      hint: "record",
      keywords: ["record", "recording", "cast", "asciinema", "capture", "session"],
      run: async () => {
        const tabId = paneTabs.activeTabId;
        const sid = tabId ? paneTabs.activePane(tabId)?.sessionId : null;
        if (!sid) {
          toast.err("No active terminal session");
          return;
        }
        await recording.toggle(sid);
      },
    },
    {
      id: "open-recordings",
      title: "Browse session recordings",
      hint: "open",
      keywords: ["recordings", "cast", "playback", "replay", "asciinema", "player"],
      run: () => recordingsModal.open(),
    },
    {
      id: "check-updates",
      title: "Check for updates",
      hint: "run",
      keywords: ["update", "upgrade", "version", "release"],
      run: async () => {
        await updateCheck.run();
        if (updateCheck.available) {
          toast.ok(`Update available: ${updateCheck.latest}`);
        } else if (updateCheck.lastError) {
          toast.err(`Update check failed: ${updateCheck.lastError}`);
        } else {
          toast.ok(`Up to date (${updateCheck.current})`);
        }
      },
    },
  ];

  // Close the term menu on outside click.
  $effect(() => {
    if (!termMenuOpen) return;
    function onDoc(e: MouseEvent) {
      const el = (e.target as HTMLElement)?.closest(".term-menu-wrap");
      if (!el) termMenuOpen = false;
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  });

  async function openNativeTerminal() {
    let kind = "windowsterminal";
    try {
      const saved = await api.settingsGet("external_terminal_kind");
      if (saved) kind = saved;
    } catch { /* default */ }
    try {
      await api.openNativeTerminal(kind);
    } catch (e: any) {
      toast.err(`Open native terminal failed: ${e?.message ?? String(e)}`);
    }
  }
  function confirmQuitCancel() {
    quitPromptCount = null;
    api.clearAttention().catch(() => {});
  }

  // Dynamic-inventory folder refresh notification - reload entries
  // for the affected folder so the tree picks up new VMs / status
  // changes without a global tree.load().
  EventsOn("dynamic_folder_refreshed", async (folderId: any) => {
    if (typeof folderId === "string" && folderId) {
      await tree.loadDynamicEntries(folderId);
    }
  });

  // Deep link from ssh-tool-catalog: `ssh-tool://import?source=URL`
  // arrived as a CLI arg. Land in Settings → Import (archive source)
  // with the URL pre-fetched.
  EventsOn("deep_link_import", (url: any) => {
    if (typeof url === "string" && url) {
      deepLink.setImportURL(url);
      view.setTab("settings");
    }
  });

  // OS file-manager integration: "Open in ssh-tool" on a directory
  // launches/reuses the app with --open-dir; the backend forwards the
  // path here and we open the default local shell cd'd into it.
  EventsOn("open_dir_shell", (dir: any) => {
    if (typeof dir === "string" && dir) {
      openLocalShell(undefined, dir);
    }
  });

  // Auto-sync notifications: another machine pushed a newer profile
  // (periodic remote check or a refused auto-push). Pull is manual -
  // clicking the toast jumps to Settings > Sync, and the status-bar
  // pill (syncState) stays lit in case the toast is missed.
  syncState.init((info) => {
    const dev = info.device ? ` from ${info.device}` : "";
    toast.info(
      `Sync: newer profile available${dev} - click to pull`,
      10000,
      () => syncState.quickPull(),
    );
  });
  EventsOn("sync_auto_pushed", (gen: any) => {
    toast.info(`Synced (generation ${gen})`, 2000);
  });
  // Live pull applied the other machine's profile into the running DB.
  // Reload every store that reads from it; SSH sessions are untouched.
  EventsOn("profile_reloaded", async () => {
    await Promise.all([tree.load(), credentials.load()]);
  });

  // Listen for host key challenges emitted by the Go backend during SSH connect.
  EventsOn("host_key_challenge", (data: any) => {
    api.requestAttention().catch(() => {});
    api.sendPromptNotification(
      "Host key verification",
      `Confirm the host key for ${data.hostname ?? "a host"} to continue connecting.`,
    ).catch(() => {});
    hostKeyStore.set({
      challengeId: data.challenge_id,
      hostname: data.hostname,
      port: data.port,
      keyType: data.key_type,
      fingerprint: data.fingerprint,
      status: data.status,
      oldFingerprint: data.old_fingerprint,
      keyB64: data.key_b64 ?? "",
    });
  });

  // Interactive username prompt (hop has no configured user).
  EventsOn("username_prompt", (data: any) => {
    api.requestAttention().catch(() => {});
    api.sendPromptNotification(
      "Username required",
      `Enter a username to connect to ${data.host ?? "the server"}.`,
    ).catch(() => {});
    authPromptStore.enqueue({
      promptId: data.prompt_id,
      kind: "username",
      label: data.label ?? "",
      host: data.host ?? "",
      port: data.port ?? 22,
      questions: [{ echo: true, text: "Username" }],
    });
  });

  // Interactive keyboard-interactive / password prompt (server 2FA or a
  // rejected key falling back to a live password).
  EventsOn("auth_prompt", (data: any) => {
    api.requestAttention().catch(() => {});
    api.sendPromptNotification(
      "Authentication required",
      `${data.host ?? "The server"} is asking for a password or verification code.`,
    ).catch(() => {});
    authPromptStore.enqueue({
      promptId: data.prompt_id,
      kind: "auth",
      label: data.label ?? "",
      host: data.host ?? "",
      port: data.port ?? 22,
      name: data.name ?? "",
      instruction: data.instruction ?? "",
      questions: (data.questions ?? []).map((q: any) => ({ echo: !!q.echo, text: q.text ?? "" })),
    });
  });

  // LLM (MCP bridge) command-approval requests.
  EventsOn("mcp_approval_request", (data: any) => {
    api.requestAttention().catch(() => {});
    const verb = data.kind === "connect" ? "open a connection"
      : data.kind === "type" ? "type into the terminal"
      : "run a command";
    api.sendPromptNotification(
      "LLM approval needed",
      `An LLM wants to ${verb} on ${data.session_name ?? "a session"}.`,
    ).catch(() => {});
    mcpApprovalStore.enqueue({
      approvalId: data.approval_id,
      sessionId: data.session_id,
      sessionName: data.session_name,
      kind: data.kind,
      command: data.command,
    });
  });

  // Keep the "shared with LLM" tab markers in sync.
  EventsOn("mcp_grants_changed", (data: any) => {
    mcpShared.setFrom((data as { session_id: string }[]) ?? []);
  });
  api.mcpListGrants().then((g) => mcpShared.setFrom(g ?? [])).catch(() => {});

  // Whether the MCP bridge is enabled - gates the robot affordances.
  EventsOn("mcp_bridge_toggled", (on: any) => { mcpBridge.setEnabled(!!on); });
  api.settingsGet("mcp_bridge_enabled").then((v) => mcpBridge.setEnabled(v === "1" || v === "true")).catch(() => {});

  // ----- browser session sharing -----

  // A guest opened a share link and is waiting; the host must allow/deny after
  // comparing the fingerprint words out-of-band.
  EventsOn("share_approval_request", (data: any) => {
    api.requestAttention().catch(() => {});
    api.sendPromptNotification(
      "Someone wants to join your shared session",
      `${data.remote_ip ?? "A guest"} is waiting - check the fingerprint before allowing.`,
    ).catch(() => {});
    shareApprovalStore.enqueue({
      approvalId: data.approval_id,
      shareId: data.share_id,
      remoteIp: data.remote_ip,
      fingerprint: data.fingerprint,
      level: data.level,
      tabs: data.tabs ?? [],
    });
  });

  // Active shares changed (start / attach / detach / stop) - refresh the badges.
  EventsOn("share_changed", (data: any) => {
    shareShared.setFrom((data as any[]) ?? []);
  });

  // A guest switched to a different tab than the host - show where they are.
  EventsOn("share_guest_tab", (data: any) => {
    if (data?.share_id !== undefined && data?.index !== undefined) {
      shareShared.guestViewingTab(data.share_id, data.index);
    }
  });

  // Whether sharing is enabled - gates the share affordances.
  EventsOn("share_toggled", (on: any) => { shareBridge.setEnabled(!!on); });
  api.settingsGet("share_enabled").then((v) => shareBridge.setEnabled(v === "1" || v === "true")).catch(() => {});

  // The LLM opened a session via the MCP bridge's connect tool. The backend
  // holds the live session but no tab exists (the frontend normally creates
  // it after its own connect). Add the tab + switch to it so the user sees it.
  EventsOn("mcp_session_opened", (data: any) => {
    if (isDetached) return; // only the main window owns tab creation here
    const sid = data.session_id as string;
    if (!sid || sessions.tabs.find((s) => s.sessionId === sid)) return;
    sessions.add({
      sessionId: sid,
      connectionId: data.connection_id ?? "",
      name: data.name ?? "",
      hostname: data.hostname ?? "",
      status: "connected",
    });
    paneTabs.addTab(sid, data.name ?? "session");
    view.setTab("terminal");
  });

  // Load saved layout once at boot so the sidebar comes up at the
  // last-used width instead of the 320 default.
  layoutPrefs.load();

  // Update check: 5 s after boot (let the UI settle) then every
  // 6 h. Honours the update_check_disabled setting on the backend
  // side; if disabled, the IPC fast-returns with Error set and
  // the status bar stays quiet.
  setTimeout(() => { updateCheck.run(); }, 5000);
  setInterval(() => { updateCheck.run(); }, 6 * 60 * 60 * 1000);
  // Subscribe to the backend-owned broadcast set so every window's
  // local mirror stays in sync. Idempotent - safe to call from
  // both the main App and DetachedWindow.
  //
  // Has to go inside $effect (post-mount) rather than top-level
  // script so the Wails runtime is guaranteed initialized. Calling
  // Events.On before window._wails is ready silently registers a
  // listener that nothing dispatches into.
  $effect(() => {
    broadcast.init();
    recording.init();
  });

  // CredFolderNode dispatches this when the user picks "New
  // credential here" from the right-click menu - pre-target the
  // create modal at that folder. Single listener at App scope so
  // CredentialList / CredFolderNode don't have to know about each
  // other.
  $effect(() => {
    const handler = (e: Event) => {
      const fid = (e as CustomEvent<string>).detail;
      createTargetFolderId = fid;
      showCreate = true;
    };
    window.addEventListener("credential-create-in-folder", handler);
    return () => window.removeEventListener("credential-create-in-folder", handler);
  });

  // Vault auto-lock: when the user is idle for vaultPrefs.autoLockMinutes
  // we call VaultLock(false) and flip vaultReady back to false so
  // VaultGate re-prompts. Sessions / port-forwards keep running - only
  // the credential tree gets re-protected, which matches what most
  // password managers do.
  //
  // 0 = disabled. Activity = mousemove, keydown, mousedown, wheel.
  // We reset on any of those; touchstart too for completeness.
  $effect(() => {
    if (!vaultReady) return;
    const minutes = vaultPrefs.autoLockMinutes;
    console.debug("[vault] auto-lock effect", { vaultReady, minutes });
    if (minutes <= 0) return;
    const idleMs = minutes * 60_000;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const reset = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        console.debug("[vault] idle timeout fired - locking");
        api.vaultLock(false).catch(console.warn);
        suppressAutoUnlock = true;
        vaultReady = false;
      }, idleMs);
    };
    const opts = { capture: true, passive: true } as AddEventListenerOptions;
    window.addEventListener("mousemove", reset, opts);
    window.addEventListener("keydown", reset, opts);
    window.addEventListener("mousedown", reset, opts);
    window.addEventListener("wheel", reset, opts);
    window.addEventListener("touchstart", reset, opts);
    reset(); // arm
    return () => {
      if (timer) clearTimeout(timer);
      window.removeEventListener("mousemove", reset, opts);
      window.removeEventListener("keydown", reset, opts);
      window.removeEventListener("mousedown", reset, opts);
      window.removeEventListener("wheel", reset, opts);
      window.removeEventListener("touchstart", reset, opts);
    };
  });

  // Manual-lock escape hatch from the status bar. Status bar fires
  // the event; we flip vaultReady so VaultGate re-prompts on the
  // next user action.
  $effect(() => {
    function onLock() {
      if (vaultReady) {
        suppressAutoUnlock = true;
        vaultReady = false;
      }
    }
    window.addEventListener("vault-lock-now", onLock);
    return () => window.removeEventListener("vault-lock-now", onLock);
  });

  // Reflect the active connection in the OS window / taskbar title.
  // Wails v3 alpha doesn't propagate document.title to the native title,
  // so we push it through SetWindowTitle. Shows the active tab's title
  // on the terminal view; falls back to the section name elsewhere.
  $effect(() => {
    const base = "ssh-tool";
    let t = base;
    if (view.tab === "terminal") {
      const active = paneTabs.activeTitle();
      if (active) t = `${active} - ${base}`;
    } else if (view.tab === "credentials") {
      t = `Credentials - ${base}`;
    } else if (view.tab === "settings") {
      t = `Settings - ${base}`;
    }
    document.title = t; // harmless; some platforms also use it
    api.setWindowTitle(t).catch(() => {});
  });

  // Backend signals "vault locked during connect" when an SSH
  // attempt failed because the credential's secret couldn't be
  // decrypted. Flip vaultReady so VaultGate pops the prompt and
  // toast the user with the connection target so they know which
  // attempt was blocked.
  $effect(() => {
    if (!vaultReady) return;
    const unsub = EventsOn("vault_locked_during_connect", (p: { hostname?: string; message?: string }) => {
      suppressAutoUnlock = true;
      vaultReady = false;
      toast.err(`Vault locked while connecting to ${p?.hostname ?? "host"}. Unlock and retry.`);
    });
    return () => unsub?.();
  });

  async function onVaultReady() {
    suppressAutoUnlock = false;
    vaultReady = true;
    await Promise.all([tree.load(), credentials.load()]);
    // Recover any SSH sessions / forwards that the Go backend kept alive
    // across a UI reload (Ctrl+R, Vite HMR, etc.). The backend goroutines
    // were never killed - only this frontend view of them was lost.
    let recovered = 0;
    try {
      const live = (await api.sshActiveSessions()) ?? [];
      for (const s of live) {
        if (sessions.tabs.find((t) => t.sessionId === s.session_id)) continue;
        sessions.add({
          sessionId: s.session_id,
          connectionId: s.connection_id,
          name: s.name,
          hostname: s.hostname,
          status: "connected",
        });
        paneTabs.addTab(s.session_id, s.name);
        recovered++;
      }
    } catch (e) {
      console.warn("session recovery failed", e);
    }
    // Same flow for local shells - backend kept them alive across the
    // UI reload; the tab disappeared but the PTY didn't.
    try {
      const liveLocal = (await api.localShellList()) ?? [];
      for (const s of liveLocal) {
        if (sessions.tabs.find((t) => t.sessionId === s.session_id)) continue;
        sessions.add({
          sessionId: s.session_id,
          connectionId: "",
          name: s.display,
          hostname: s.kind,
          kind: "local",
          status: "connected",
        });
        paneTabs.addTab(s.session_id, s.display);
        recovered++;
      }
    } catch (e) {
      console.warn("local shell recovery failed", e);
    }
    // Reopen the tabs from the last quit (opt-in, cold start only -
    // if recovery brought anything back this was a UI reload and the
    // backend sessions are already live).
    lastSession.restoreOnStartup(recovered).catch(console.warn);
  }

  // Continuous last-session snapshot: any tab/session mutation
  // schedules a coalesced save. Gated inside the store until the
  // startup restore decision has been made. pagehide flushes any
  // pending write so quitting right after opening a tab keeps it.
  $effect(() => {
    void paneTabs.tabs;
    void sessions.tabs;
    lastSession.schedule();
  });
  $effect(() => {
    const flush = () => lastSession.flush();
    window.addEventListener("pagehide", flush);
    return () => window.removeEventListener("pagehide", flush);
  });
</script>

<svelte:window onkeydown={onGlobalKey} />

{#if isDetached}
  <!-- Detached window: shared backend, slim UI. The vault is already
       unlocked in the main process (otherwise the user couldn't have
       opened a tab to detach), so we skip VaultGate entirely. -->
  <DetachedWindow detachedTabKey={detachedTab!} windowName={detachedWindowName} />
  <ToastHost />
{:else}

{#if !vaultReady}
  <VaultGate
    onUnlocked={onVaultReady}
    onSkip={onVaultReady}
    allowAutoUnlock={!suppressAutoUnlock}
  />
{/if}

{#if showSnippetPalette && vaultReady}
  <SnippetPalette onClose={() => {
    showSnippetPalette = false;
    if (view.tab === "terminal") focusActiveTerminal();
  }} />
{/if}

{#if showPalette && vaultReady}
  <QuickPalette actions={paletteActions} onClose={() => {
    showPalette = false;
    if (view.tab === "terminal") focusActiveTerminal();
  }} />
{/if}

{#if showShortcuts}
  <div
    class="shortcuts-backdrop"
    role="presentation"
    onclick={() => (showShortcuts = false)}
  ></div>
  <div class="shortcuts" role="dialog" aria-label="Keyboard shortcuts">
    <header>
      <h2>Keyboard shortcuts</h2>
      <button class="x" onclick={() => (showShortcuts = false)} aria-label="Close">×</button>
    </header>
    <dl>
      <dt><kbd>Ctrl</kbd>+<kbd>K</kbd></dt>
      <dd>Quick palette - jump to a connection, credential, or setting</dd>
      <dt><kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>P</kbd></dt>
      <dd>Snippet palette - send a saved snippet into the active terminal (broadcast aware)</dd>
      <dt><kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>F</kbd></dt>
      <dd>Find in terminal scrollback (F3 / Shift+F3 to step through matches)</dd>
      <dt><kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>C</kbd> / <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>V</kbd></dt>
      <dd>Terminal copy / paste (Windows + Linux). macOS: <kbd>Cmd</kbd>+<kbd>C</kbd> / <kbd>Cmd</kbd>+<kbd>V</kbd>.</dd>
      <dt><kbd>Ctrl</kbd>+wheel</dt>
      <dd>Terminal font zoom</dd>
      <dt>Middle-click tab</dt>
      <dd>Close tab</dd>
      <dt>Drag tab → tab label</dt>
      <dd>Reorder. Drop onto a pane edge to split. Hover over a non-source tab during a drag to activate it.</dd>
      <dt>Ctrl-click / Shift-click in tree</dt>
      <dd>Multi-select connections or dynamic-inventory entries (Shift = range)</dd>
    </dl>
  </div>
{/if}

<div class="app">
  <nav class="tabs">
    <div class="tabs-group tabs-left">
      <button class:active={view.tab === "connections"} onclick={() => view.setTab("connections")} title="Connections">
        <IconHost size={14} /> <span class="tab-label">Connections</span>
      </button>
      <button class:active={view.tab === "credentials"} onclick={() => view.setTab("credentials")} title="Credentials">
        <IconKey size={14} /> <span class="tab-label">Credentials</span>
      </button>
      <button class:active={view.tab === "settings"} onclick={() => view.setTab("settings")} title="Settings">
        <IconSettings size={14} /> <span class="tab-label">Settings</span>
      </button>
      {#if sessions.tabs.length > 0}
        {@const liveCount = sessions.tabs.filter(
          (s) => s.status === "connected" || s.status === "connecting" || s.status === "reconnecting"
        ).length}
        {@const allConnected = liveCount === sessions.tabs.length}
        <button class:active={view.tab === "terminal"} onclick={() => view.setTab("terminal")} title="Terminal">
          <IconTerminal size={14} /> <span class="tab-label">Terminal</span>
          <span class="count" class:all-connected={allConnected}>{liveCount}</span>
        </button>
      {/if}
    </div>
    <div class="tabs-group tabs-right">
    {#if !isMobile}
    <div class="term-menu-wrap">
      <button
        class="search-btn"
        onclick={() => openLocalShell()}
        title="Open the default local shell as a tab (set the default in Settings → Connection)"
      >
        <IconTerminal size={14} /> {localShellButtonLabel}
      </button>
      <button
        class="search-btn chev"
        onclick={() => (termMenuOpen = !termMenuOpen)}
        title="More terminal options"
      >▾</button>
      {#if termMenuOpen}
        <div class="term-menu" role="menu">
          {#each localShellOptions as opt (opt.kind)}
            <button onclick={() => { openLocalShell(opt.kind); termMenuOpen = false; }}>
              {opt.label}
              <span class="dim">
                {localShellPrefs.kind === opt.kind ? "default" : "in-app tab"}
              </span>
            </button>
          {/each}
          <div class="term-menu-sep"></div>
          <div class="term-menu-default-row">
            <label>
              <span class="dim">Default for plain click</span>
              <select
                value={localShellPrefs.kind}
                onchange={(e) => localShellPrefs.set(((e.target as HTMLSelectElement).value) as LocalShellKind)}
              >
                <option value="">Auto (backend picks)</option>
                {#each localShellOptions as opt (opt.kind)}
                  <option value={opt.kind}>{opt.label}</option>
                {/each}
              </select>
            </label>
          </div>
          <div class="term-menu-sep"></div>
          <button onclick={() => { openNativeTerminal(); termMenuOpen = false; }}>
            External OS terminal
            <span class="dim">opens a separate window</span>
          </button>
        </div>
      {/if}
    </div>
    {/if}
    <button
      class="search-btn"
      onclick={() => (showPalette = true)}
      title="Search connections (Ctrl+K)"
    >
      <IconSearch size={14} /> <span class="tab-label">Search</span>
    </button>
    <button
      class="search-btn"
      onclick={() => (showShortcuts = !showShortcuts)}
      title="Keyboard shortcuts"
      aria-label="Keyboard shortcuts"
    >?</button>
    </div><!-- /tabs-right -->
  </nav>

  <!--
    All three tab views stay mounted; only display switches. This keeps
    xterm instances alive (preserving scrollback) and PortForwards /
    Terminal etc. don't lose state when the user briefly visits another
    tab.
  -->
  <div class="body">
    {#if isMobile}
      <!-- Mobile: a single full-width pane. Show the list, or (when an item
           is selected) a Back bar + the detail. No split grid, no resize
           handle - those are desktop-only and were overflowing the viewport. -->
      <div class="view mobile-pane" class:active={view.tab === "connections"}>
        {#if mobileShowDetail}
          <button class="mobile-back" onclick={mobileBack} aria-label="Back to list">‹ Back</button>
          <div class="mobile-detail-body"><DetailPane /></div>
        {:else}
          <Sidebar />
        {/if}
      </div>
      <div class="view mobile-pane" class:active={view.tab === "credentials"}>
        {#if mobileShowCredDetail}
          <button class="mobile-back" onclick={mobileBack} aria-label="Back to list">‹ Back</button>
          <div class="mobile-detail-body">
            <CredentialDetail onCreateCredential={(folderId) => { createTargetFolderId = folderId; showCreate = true; }} />
          </div>
        {:else}
          <CredentialList onCreate={() => { createTargetFolderId = null; showCreate = true; }} />
        {/if}
      </div>
    {:else}
      <div
        class="view split"
        class:active={view.tab === "connections"}
        style="--sidebar-width: {layoutPrefs.sidebarWidth}px"
      >
        <Sidebar />
        <ResizeHandle
          width={layoutPrefs.sidebarWidth}
          onResize={(px) => layoutPrefs.setSidebarWidth(px)}
        />
        <DetailPane />
      </div>
      <div
        class="view split"
        class:active={view.tab === "credentials"}
        style="--sidebar-width: {layoutPrefs.sidebarWidth}px"
      >
        <CredentialList onCreate={() => { createTargetFolderId = null; showCreate = true; }} />
        <ResizeHandle
          width={layoutPrefs.sidebarWidth}
          onResize={(px) => layoutPrefs.setSidebarWidth(px)}
        />
        <CredentialDetail onCreateCredential={(folderId) => { createTargetFolderId = folderId; showCreate = true; }} />
      </div>
    {/if}
    <div class="view full" class:active={view.tab === "settings"}>
      <Settings />
    </div>
    {#if paneTabs.tabs.length > 0}
      <div class="view full" class:active={view.tab === "terminal"}>
        <TerminalArea />
      </div>
    {/if}
  </div>

  {#if vaultReady}
    <StatusBar />
  {/if}

  <ToastHost />

  {#if showCreate}
    <CredentialCreate
      defaultFolderId={createTargetFolderId}
      onClose={() => { showCreate = false; createTargetFolderId = null; }}
    />
  {/if}

  {#if hostKeyStore.pending}
    <HostKeyModal
      challengeId={hostKeyStore.pending.challengeId}
      hostname={hostKeyStore.pending.hostname}
      port={hostKeyStore.pending.port}
      keyType={hostKeyStore.pending.keyType}
      fingerprint={hostKeyStore.pending.fingerprint}
      status={hostKeyStore.pending.status}
      oldFingerprint={hostKeyStore.pending.oldFingerprint}
      keyB64={hostKeyStore.pending.keyB64}
      queueLength={Math.max(0, hostKeyStore.queue.length - 1)}
      onRespond={async (accept, remember) => {
        const c = hostKeyStore.pending!;
        // Shift first so the next queued challenge is rendered
        // immediately; the IPC call awaits but the UI doesn't block.
        hostKeyStore.clear();
        if (hostKeyStore.queue.length === 0) api.clearAttention().catch(() => {});
        await api.sshRespondHostKey(c.challengeId, accept, remember, c.hostname, c.port, c.keyType, c.keyB64, c.fingerprint);
      }}
    />
  {/if}

  {#if authPromptStore.pending}
    <AuthPromptModal
      promptId={authPromptStore.pending.promptId}
      kind={authPromptStore.pending.kind}
      label={authPromptStore.pending.label}
      host={authPromptStore.pending.host}
      port={authPromptStore.pending.port}
      name={authPromptStore.pending.name}
      instruction={authPromptStore.pending.instruction}
      questions={authPromptStore.pending.questions}
      queueLength={Math.max(0, authPromptStore.queue.length - 1)}
      onRespond={async (answers) => {
        const p = authPromptStore.pending!;
        authPromptStore.shift();
        if (authPromptStore.queue.length === 0) api.clearAttention().catch(() => {});
        await api.sshRespondAuthPrompt(p.promptId, answers ?? [], answers === null);
      }}
    />
  {/if}

  {#if mcpApprovalStore.pending}
    <McpApprovalModal
      sessionName={mcpApprovalStore.pending.sessionName}
      kind={mcpApprovalStore.pending.kind}
      command={mcpApprovalStore.pending.command}
      queueLength={Math.max(0, mcpApprovalStore.queue.length - 1)}
      onRespond={async (decision) => {
        const a = mcpApprovalStore.pending!;
        mcpApprovalStore.shift();
        if (mcpApprovalStore.queue.length === 0) api.clearAttention().catch(() => {});
        await api.mcpApprovalRespond(a.approvalId, decision);
      }}
    />
  {/if}

  {#if shareApprovalStore.pending}
    <ShareApprovalModal
      remoteIp={shareApprovalStore.pending.remoteIp}
      fingerprint={shareApprovalStore.pending.fingerprint}
      level={shareApprovalStore.pending.level}
      queueLength={Math.max(0, shareApprovalStore.queue.length - 1)}
      onRespond={async (decision) => {
        const a = shareApprovalStore.pending!;
        shareApprovalStore.shift();
        if (shareApprovalStore.queue.length === 0) api.clearAttention().catch(() => {});
        await api.shareApprovalRespond(a.approvalId, decision);
      }}
    />
  {/if}

  {#if quitPromptCount !== null}
    <div class="overlay" role="presentation">
      <div class="quit-modal" role="dialog" aria-modal="true">
        <h2>Quit ssh-tool?</h2>
        <p>
          {quitPromptCount} active SSH session{quitPromptCount === 1 ? "" : "s"}
          will be disconnected.
        </p>
        <div class="actions">
          <button onclick={confirmQuitCancel}>Cancel</button>
          <button class="danger" onclick={confirmQuitAccept}>
            Disconnect and quit
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if dynEditor.open}
    <DynamicFolderEditor
      parentId={dynEditor.parentId}
      existingFolderId={dynEditor.existingFolderId}
      onClose={() => dynEditor.close()}
    />
  {/if}

  <ContextMenu />
  {#if exportModal.open}
    <ExportConnectionsModal
      connectionIds={exportModal.connectionIds}
      folderIds={exportModal.folderIds}
      suggestedName={exportModal.suggestedName}
      onClose={() => exportModal.close()}
    />
  {/if}
  <RecordingsModal />
  <PromptModal />
  <ConfirmModal />
  <PresenceTakeoverModal />

  {#if connectionActions.movePending}
    <FolderPicker
      title={connectionActions.movePending.title}
      excludeIds={connectionActions.movePending.excludeIds}
      onPick={(id) => connectionActions.commitMove(id)}
      onCancel={() => connectionActions.cancelMove()}
    />
  {/if}

  {#if connectionActions.deleteItems.length > 0}
    <DeleteConfirm
      items={connectionActions.deleteItems}
      onConfirm={() => connectionActions.commitDelete()}
      onCancel={() => connectionActions.cancelDelete()}
    />
  {/if}
</div>
{/if}

<style>
  :global(html, body) {
    margin: 0;
    height: 100%;
    background: var(--base);
    color: var(--text);
    font-family: system-ui, -apple-system, sans-serif;
    font-size: var(--ui-font-size, 13px);
  }
  :global(*) { box-sizing: border-box; }
  .app {
    display: grid;
    /* Tab row was 36px fixed; that clipped the second row when the
       nav wrapped (Connections / Credentials / Settings / Terminal /
       Local shell / Search couldn't fit on a single line). auto
       grows to whatever wrapped height the nav needs and the
       content row below shrinks to compensate. */
    grid-template-rows: auto 1fr auto;
    height: 100vh;
    overflow: hidden;
    /* Grid columns default to min-content as their floor; without this a
       wide child (a form field, a long path) blows the column past the
       viewport and the whole app scrolls sideways. min-width:0 lets the
       single column shrink so children must wrap/scroll within it. */
    min-width: 0;
    max-width: 100vw;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    padding: 0 0.5rem;
    gap: 0.25rem 0.5rem;
  }
  .tabs-group {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.25rem;
  }
  /* Right-side group sticks to the end of the row; when the window
     is narrow enough that left + right won't both fit, the wrap
     pushes the whole right group onto a new line - Local shell +
     Search stay clickable instead of disappearing off-screen. */
  .tabs-right { margin-left: auto; }
  .tabs button {
    background: transparent;
    border: 0;
    color: var(--overlay0);
    cursor: pointer;
    padding: 0.4rem 0.85rem;
    font: inherit;
    border-bottom: 2px solid transparent;
    margin-top: 0.2rem;
    display: flex;
    align-items: center;
    gap: 0.35rem;
  }
  .tabs button:hover { color: var(--text); }
  .tabs button.active { color: var(--text); border-bottom-color: var(--blue); }
  .search-btn {
    color: var(--subtext0);
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .term-menu-wrap { position: relative; display: inline-flex; }
  .search-btn.chev { padding: 0 0.3rem; min-width: 18px; }
  .term-menu {
    position: absolute;
    top: 100%;
    right: 0;
    margin-top: 4px;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    min-width: 240px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.5);
    z-index: 80;
    padding: 0.2rem 0;
  }
  .term-menu button {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    background: transparent;
    color: var(--text);
    border: 0;
    padding: 0.4rem 0.7rem;
    font: inherit;
    font-size: 0.8rem;
    text-align: left;
    cursor: pointer;
  }
  .term-menu button:hover { background: var(--surface0); }
  .term-menu .dim { color: var(--overlay0); font-size: 0.7rem; margin-left: 1rem; }
  .term-menu-sep { height: 1px; background: var(--surface0); margin: 0.2rem 0; }
  .term-menu-default-row {
    padding: 0.4rem 0.7rem;
  }
  .term-menu-default-row label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.78rem;
  }
  .term-menu-default-row select {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    padding: 0.25rem 0.4rem;
    font: inherit;
    font-size: 0.78rem;
  }
  .count {
    background: var(--surface0);
    color: var(--subtext0);
    font-size: 0.7rem;
    padding: 0.1rem 0.45rem;
    border-radius: 8px;
    font-weight: 600;
  }
  .count.all-connected {
    background: color-mix(in oklab, var(--green) 20%, var(--bg-panel));
    color: var(--green);
  }
  .body { position: relative; overflow: hidden; min-height: 0; }
  /*
    Tab views stack absolutely on top of each other; only .active is
    visible. Keeping all mounted preserves xterm scrollback + form state
    when the user switches tabs.
   */
  .view {
    position: absolute;
    inset: 0;
    overflow: hidden;
    min-height: 0;
    opacity: 0;
    pointer-events: none;
    z-index: 0;
  }
  .view.active {
    opacity: 1;
    pointer-events: auto;
    z-index: 1;
  }
  .view.split {
    display: grid;
    /* sidebar | handle | detail. Sidebar width is driven by the
       inline --sidebar-width var which the layoutPrefs store updates
       during drag, then persists to settings. */
    grid-template-columns: var(--sidebar-width, 320px) auto 1fr;
  }
  /* Let grid children shrink below their content width (default grid item
     min-width is auto = content size), so a wide form/path inside the
     detail pane scrolls within the pane instead of widening the column. */
  .view.split > :global(*) { min-width: 0; }
  .view.full {
    display: flex;
  }

  /* Mobile: compact the top nav. The tab/search labels are dropped so the
     icons fit on a single row instead of wrapping to three. Active tab
     keeps its label for orientation. Tighter padding reclaims vertical
     space for content. */
  @media (max-width: 640px) {
    .tabs {
      flex-wrap: nowrap;
      overflow-x: auto;
      scrollbar-width: none;
      padding: 0 0.25rem;
      gap: 0.1rem;
    }
    .tabs::-webkit-scrollbar { display: none; }
    .tabs-group { flex-wrap: nowrap; flex: 0 0 auto; }
    .tabs button { padding: 0.4rem 0.5rem; }
    /* Hide labels except on the active tab (keeps a text anchor for the
       current view); inactive tabs are icon-only. */
    .tabs button:not(.active) .tab-label { display: none; }
    .search-btn .tab-label { display: none; }
  }

  /* Mobile single-pane view: one full-width column. When a list item is
     selected the markup swaps to a Back bar + the detail body; otherwise
     it shows the list. No split grid / resize handle on mobile (those
     overflow a phone). The .view absolute-positioning + .active toggle is
     shared with the desktop views above. */
  .mobile-pane {
    display: flex;
    flex-direction: column;
    min-width: 0;
    overflow: hidden;
  }
  .mobile-pane > :global(*) { min-width: 0; }
  /* The list (Sidebar / CredentialList) fills the pane and scrolls. */
  .mobile-pane > :global(:only-child) { flex: 1; min-height: 0; }
  .mobile-detail-body {
    flex: 1;
    min-height: 0;
    min-width: 0;
    overflow: auto;
  }
  .mobile-back {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    flex: 0 0 auto;
    width: 100%;
    padding: 0.55rem 0.8rem;
    background: var(--bg-elevated);
    color: var(--accent);
    border: 0;
    border-bottom: 1px solid var(--border);
    font: inherit;
    font-size: 0.92rem;
    text-align: left;
    cursor: pointer;
  }
  /* The single child (Settings / TerminalArea) fills the view so its
     own internal grid gets a real height - otherwise the Settings
     sidebar collapses to its content height and leaves an empty strip
     below it. */
  .view.full > :global(*) {
    flex: 1;
    min-width: 0;
    min-height: 0;
  }

  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.55);
    display: flex; align-items: center; justify-content: center;
    /* The quit prompt must never hide behind another modal (the
       recordings player sits at 1000, ConfirmModal at 9500) - a
       user who can't see it just thinks quit is broken. */
    z-index: 9600;
  }
  .quit-modal {
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-radius: 6px;
    padding: 1rem 1.2rem;
    min-width: 320px;
    max-width: 460px;
    box-shadow: 0 10px 40px rgba(0,0,0,0.5);
  }
  .quit-modal h2 { margin: 0 0 0.5rem 0; font-size: 1rem; }
  .quit-modal p { margin: 0 0 1rem 0; color: var(--subtext0); font-size: 0.85rem; }
  .quit-modal .actions { display: flex; gap: 0.5rem; justify-content: flex-end; }
  .quit-modal button {
    background: var(--surface0); color: var(--text);
    border: 0; padding: 0.4rem 0.8rem; border-radius: 4px;
    font: inherit; font-size: 0.85rem; cursor: pointer;
  }
  .quit-modal button:hover { background: var(--surface1); }
  .quit-modal button.danger { background: var(--red); color: var(--on-accent); font-weight: 600; }
  .quit-modal button.danger:hover { background: var(--maroon); }

  .shortcuts-backdrop {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.5);
    z-index: 9000;
  }
  .shortcuts {
    position: fixed;
    top: 50%; left: 50%;
    transform: translate(-50%, -50%);
    width: min(640px, 92vw);
    max-height: 80vh;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    z-index: 9001;
    overflow: hidden;
    display: flex; flex-direction: column;
    color: var(--text);
  }
  .shortcuts header {
    display: flex; justify-content: space-between; align-items: center;
    padding: 0.6rem 1rem;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
  }
  .shortcuts h2 { font-size: 1rem; margin: 0; }
  .shortcuts .x {
    background: transparent; border: 0; color: var(--subtext0);
    font-size: 1.4rem; line-height: 1; cursor: pointer;
    padding: 0 0.3rem; border-radius: 3px;
  }
  .shortcuts .x:hover { background: var(--surface0); color: var(--text); }
  .shortcuts dl {
    margin: 0; padding: 0.75rem 1rem;
    overflow-y: auto;
    font-size: 0.85rem;
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.5rem 1rem;
    align-items: baseline;
  }
  .shortcuts dt {
    white-space: nowrap;
    font-family: ui-monospace, monospace;
  }
  .shortcuts dd { margin: 0; color: var(--subtext0); }
  .shortcuts kbd {
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-bottom-width: 2px;
    border-radius: 3px;
    padding: 0.05rem 0.35rem;
    font-family: inherit;
    font-size: 0.78rem;
    color: var(--text);
  }
</style>
