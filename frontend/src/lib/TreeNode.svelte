<script lang="ts">
  import { tree, selection, drag, sessions, paneTabs, view } from "./stores.svelte";
  import { errMsg } from "./connectErrors";
  import { expandedConnections } from "./treeState.svelte";
  import { tagFilter } from "./tagFilter.svelte.ts";
  import { nameFilter } from "./nameFilter.svelte.ts";
  import { appPrefs } from "./appPrefs.svelte";
  import { api, type Folder } from "./api";
  import TreeNodeSelf from "./TreeNode.svelte";
  import Icon from "./Icon.svelte";
  import { IconFolder, IconHost, IconLoading, IconX, IconStar, IconGlobe, dynamicEntryIcon } from "./iconMap";
  import { computeIntent, isInvalidDrop, applyDrop, applyMultiDrop, type DragKind, type DropIntent } from "./treeDnd";
  import { setMultiDragImage } from "./dragImage";
  import { contextMenu } from "./contextMenu.svelte.ts";
  import { exportModal } from "./exportModal.svelte.ts";
  import { connectionActions } from "./connectionActions.svelte";
  import { dynEditor } from "./dynEditor.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { isMobile } from "./platform";

  interface Props {
    folder: Folder;
    depth: number;
  }
  let { folder, depth }: Props = $props();

  let connectingId = $state<string | null>(null);
  let connectErrId = $state<string | null>(null);
  let connectErr   = $state<string | null>(null);

  // Dynamic-entry connect: hits SshConnectDynamic which builds a
  // synthetic Connection from the cached entry + the dynamic
  // folder's inherit cascade, then runs it through the standard
  // SSH layer. Stopped VMs get an extra confirm step.
  async function connectDynamic(folderId: string, entry: { id: string; name: string; hostname: string; status: string }) {
    if (entry.status === "stopped") {
      const ok = await showConfirm({
        title: "Host is stopped",
        message:
          `${entry.name} is stopped in the provider.\n\nConnect anyway? ` +
          `(useful if the VM is reachable on another address or the status is stale.)`,
        okLabel: "Connect",
      });
      if (!ok) return;
    }
    try {
      const res = await api.sshConnectDynamic(folderId, entry.id);
      sessions.add({
        sessionId: res.session_id,
        connectionId: "dyn:" + entry.id,
        name: entry.name,
        hostname: entry.hostname,
        status: "connected",
      });
      paneTabs.addTab(res.session_id, entry.name);
      view.setTab("terminal");
    } catch (e: any) {
      // Surface the failure on the dynamic entry detail pane
      // instead of a blocking alert. Selecting the entry switches
      // the right pane to DynamicEntryDetail, which renders the
      // error via connectionActions.lastConnectError keyed on the
      // synthetic dyn:<id>.
      connectionActions.recordConnectError("dyn:" + entry.id, e);
      selection.selectDynamicEntry(folderId, entry.id);
    }
  }

  async function quickConnect(connId: string, _name: string, _hostname: string) {
    if (connectingId === connId) return;
    connectingId = connId;
    connectErrId = null;
    connectErr   = null;
    const ok = await connectionActions.connectOne(connId);
    if (!ok) {
      // Surface a brief inline marker. The full error + debug log
      // lives in DetailPane via connectionActions.lastConnectError.
      const last = connectionActions.lastConnectError[connId];
      connectErrId = connId;
      connectErr   = last?.message ?? "connect failed";
    }
    connectingId = null;
  }

  let lastClickId = "";
  let lastClickAt = 0;

  // Some WebView2 / WebKitGTK combinations strip modifier flags off
  // the `click` event when the target is `draggable="true"`. Capture
  // them on mousedown (which fires before dragstart) so handleConnClick
  // can still see them. Cleared after consumption.
  let lastMouseMods = { ctrl: false, meta: false, shift: false };
  function recordMods(e: MouseEvent) {
    lastMouseMods = {
      ctrl: e.ctrlKey,
      meta: e.metaKey,
      shift: e.shiftKey,
    };
  }

  function onDynRowClick(e: MouseEvent, folderId: string, entryId: string) {
    const ctrl = e.ctrlKey || lastMouseMods.ctrl;
    const meta = e.metaKey || lastMouseMods.meta;
    const shift = e.shiftKey || lastMouseMods.shift;
    lastMouseMods = { ctrl: false, meta: false, shift: false };
    if (shift) {
      // Range walks the VISIBLE entries only - what the user sees
      // is what gets picked. Without this filter, typing "vpn" to
      // narrow the list and then shift-clicking would still pull
      // in every hidden hostname between anchor and target. Hosts
      // come before guests; matches the visual bucket order
      // rendered below.
      const ordered = [
        ...dynamicBuckets.hosts.map((m) => m.id),
        ...dynamicBuckets.guests.map((m) => m.id),
      ];
      selection.rangeDynamic(folderId, entryId, ordered);
      return;
    }
    if (ctrl || meta) {
      selection.toggleDynamic(folderId, entryId);
      return;
    }
    selection.selectDynamicEntry(folderId, entryId);
  }

  function handleConnClick(e: MouseEvent, connId: string, name: string, hostname: string) {
    // Modifier flags come from the event or the last mousedown
    // (fallback for draggable-element WebView quirk).
    const ctrl = e.ctrlKey || lastMouseMods.ctrl;
    const meta = e.metaKey || lastMouseMods.meta;
    const shift = e.shiftKey || lastMouseMods.shift;
    lastMouseMods = { ctrl: false, meta: false, shift: false };

    console.debug("[tree] conn click", {
      ctrl, meta, shift,
      rawCtrl: e.ctrlKey, rawShift: e.shiftKey, rawMeta: e.metaKey,
      button: e.button, connId,
    });

    // Ctrl/Cmd+click toggles multi; Shift+click selects range within this
    // folder. Plain click clears multi and anchors here. Modifier clicks
    // never trigger the double-click quick-connect.
    if (shift) {
      // Cross-folder range - pass the full visible flat list so anchor
      // and target can sit under different parents. Honour the active
      // name/tag filters so a typed query narrows what the range
      // walker can pick up.
      selection.rangeConnection(
        connId,
        tree.flatVisibleConnectionIds(
          (cid) => tagFilter.connectionMatches(cid) && nameFilter.connectionMatches(cid),
        ),
      );
      return;
    }
    if (ctrl || meta) {
      selection.toggleConnection(connId);
      return;
    }
    selection.selectConnection(connId);
    const now = Date.now();
    if (connId === lastClickId && now - lastClickAt < 400) {
      lastClickId = "";
      quickConnect(connId, name, hostname);
    } else {
      lastClickId = connId;
      lastClickAt = now;
    }
  }

  // Force-expand while a tag filter narrows the tree: the user wants
  // to see matches, not click through folders. Off when no filter
  // active so the user's collapsed state is preserved.
  const expanded = $derived(
    tagFilter.isFilterActive() || nameFilter.isActive()
      ? (tagFilter.folderHasMatch(folder.id) && nameFilter.folderHasMatch(folder.id))
      : expandedConnections.isExpanded(folder.id)
  );

  // Lazy-load dynamic entries when a dynamic folder first expands.
  // Subsequent refreshes ride through the backend
  // `dynamic_folder_refreshed` event already wired in App.svelte.
  $effect(() => {
    if (!expanded || !isDynamicFolder) return;
    if (tree.dynamicEntries[folder.id]) return;
    tree.loadDynamicEntries(folder.id);
  });

  // Surface the last refresh error as a toast the first time the
  // user expands a broken dynamic folder. The red dot stays visible
  // afterwards for the click-to-retry path; the toast just makes
  // sure the user doesn't miss the dot when expanding.
  let toastedErrorFor = $state<string>("");
  $effect(() => {
    if (!expanded || !isDynamicFolder) return;
    const err = dynamicMeta?.last_error;
    if (!err) return;
    if (toastedErrorFor === err) return;
    toastedErrorFor = err;
    toast.err(`${folder.name}: refresh failed - ${err}`);
  });

  async function retryDynamicRefresh() {
    try {
      await api.dynamicFolderRefreshNow(folder.id);
      toast.ok(`${folder.name}: refresh triggered`);
    } catch (e: any) {
      toast.err(`${folder.name}: ${errMsg(e)}`);
    }
  }

  // Filter visible children + connections by the active tag set.
  // Folders survive iff any descendant connection matches.
  //
  // We read tree.folders / tree.connections explicitly inside the
  // derived bodies. Calling tree.childrenOf(...) alone wasn't enough
  // for Svelte 5 to register the underlying $state arrays as
  // dependencies - after tree.load() reassigned them, the derived
  // sometimes didn't re-run and stale rows lingered until a manual
  // refresh.
  const children = $derived.by(() => {
    void tree.version;
    void nameFilter.query;
    return tree.childrenOf(folder.id).filter(
      (f) => tagFilter.folderHasMatch(f.id) && nameFilter.folderHasMatch(f.id),
    );
  });
  const isDynamicFolder = $derived.by(() => {
    void tree.version;
    return tree.isDynamic(folder.id);
  });
  const dynamicMeta = $derived.by(() => {
    void tree.version;
    return tree.dynamicFolders[folder.id] ?? null;
  });
  const dynamicTooltip = $derived.by(() => {
    if (!dynamicMeta) return "";
    const parts: string[] = [`provider: ${dynamicMeta.provider}`];
    if (dynamicMeta.last_pulled_at) {
      const delta = Math.floor(Date.now() / 1000 - dynamicMeta.last_pulled_at);
      const t = delta < 60 ? `${delta}s`
        : delta < 3600 ? `${Math.floor(delta / 60)}m`
        : `${Math.floor(delta / 3600)}h`;
      parts.push(`refreshed ${t} ago`);
    } else {
      parts.push("never refreshed");
    }
    if (dynamicMeta.last_error) parts.push(`error: ${dynamicMeta.last_error}`);
    return parts.join(" · ");
  });
  // Dynamic folder pseudo-children: { hosts, guests } each a list
  // filtered by name-filter (tag filter doesn't apply to dynamic
  // entries' provider tags - they're a different namespace).
  const dynamicBuckets = $derived.by(() => {
    void tree.version;
    void nameFilter.query;
    const entries = tree.dynamicEntries[folder.id] ?? [];
    const q = nameFilter.query.trim().toLowerCase();
    // Tag match included so typing a group name (Ansible) or label
    // (Hetzner) narrows the dynamic entry list under an expanded
    // folder. Without this the filter only looked at name / hostname
    // and tags felt like a dead-end.
    const passes = (e: typeof entries[0]) => {
      if (!q) return true;
      if (e.name.toLowerCase().includes(q)) return true;
      if (e.hostname.toLowerCase().includes(q)) return true;
      for (const t of e.tags ?? []) {
        if (t.toLowerCase().includes(q)) return true;
      }
      return false;
    };
    return {
      // PVE hypervisor nodes + generic-host entries (Ansible static
      // inventory, future flat sources) share the "hosts" bucket -
      // neither has the hypervisor guest/host distinction the
      // "guests" bucket implies.
      hosts: entries.filter((e) => (e.kind === "host" || e.kind === "server") && passes(e)),
      guests: entries.filter((e) => (e.kind === "guest_vm" || e.kind === "guest_lxc") && passes(e)),
    };
  });
  const conns = $derived.by(() => {
    void tree.version;
    void nameFilter.query;
    return tree.connectionsIn(folder.id).filter(
      (c) => tagFilter.connectionMatches(c.id) && nameFilter.connectionMatches(c.id),
    );
  });

  // Set of connection_ids that currently have at least one connected session.
  // Used to highlight tree rows for live connections.
  const liveConnIds = $derived(
    new Set(
      sessions.tabs
        .filter((t) => t.status === "connected")
        .map((t) => t.connectionId)
    )
  );

  // The connection backing the currently focused terminal pane (across
  // the active tab's pane tree). Used to mark "this is the row you're
  // looking at" with a stronger highlight when the appearance pref is
  // on. Falls back to "" when nothing's focused.
  const activeConnId = $derived.by(() => {
    const tabId = paneTabs.activeTabId;
    if (!tabId) return "";
    const leaf = paneTabs.activePane(tabId);
    if (!leaf) return "";
    const sess = sessions.tabs.find((s) => s.sessionId === leaf.sessionId);
    return sess?.connectionId ?? "";
  });

  // How many live connections live in this folder's subtree. Used for the
  // folder-row badge.
  function countLiveInSubtree(id: string): number {
    let n = 0;
    for (const c of tree.connectionsIn(id)) {
      if (liveConnIds.has(c.id)) n++;
    }
    for (const sub of tree.childrenOf(id)) {
      n += countLiveInSubtree(sub.id);
    }
    return n;
  }
  const liveInSubtree = $derived(countLiveInSubtree(folder.id));
  const folderColor = $derived(tree.resolveColorForFolder(folder.id));

  // On mobile a plain tap on a folder row toggles its expansion - the
  // chevron alone is a tiny touch target and double-click is awkward on a
  // phone. Modifier taps (rare on mobile but possible with a keyboard) still
  // fall through to range/multi select. Desktop keeps single-click = select,
  // double-click = expand. The chevron's own onclick handles both platforms.
  function onFolderRowClick(e: MouseEvent) {
    if (isMobile && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
      // Toggle expansion in place only. Selecting the folder would flip
      // mobileShowDetail and navigate to the folder's settings pane, hiding
      // the tree the user just expanded. Folder settings stay reachable via
      // the long-press context menu (Rename / etc).
      lastMouseMods = { ctrl: false, meta: false, shift: false };
      expandedConnections.toggle(folder.id);
      return;
    }
    selectFolder(e);
  }

  function selectFolder(e?: MouseEvent) {
    const shift = (e?.shiftKey ?? false) || lastMouseMods.shift;
    const ctrl  = (e?.ctrlKey  ?? false) || lastMouseMods.ctrl;
    const meta  = (e?.metaKey  ?? false) || lastMouseMods.meta;
    lastMouseMods = { ctrl: false, meta: false, shift: false };
    if (shift) {
      selection.rangeFolder(
        folder.id,
        tree.flatVisibleFolderIds(
          (fid) => tagFilter.folderHasMatch(fid) && nameFilter.folderHasMatch(fid),
        ),
      );
      return;
    }
    if (ctrl || meta) {
      selection.toggleFolder(folder.id);
      return;
    }
    selection.selectFolderById(folder.id);
  }

  function openFolderMenu(e: MouseEvent) {
    // Desktop right-click selects the row first (both panes are visible, so
    // selecting is harmless and lets the menu act on the multi-selection).
    // On mobile, selecting flips mobileShowDetail and navigates to the
    // folder settings pane - so a long-press would pop the menu AND slide
    // away the tree underneath it. Skip the auto-select on mobile and act on
    // this single folder; "Folder settings" below is the deliberate path in.
    if (!isMobile && !selection.isFolderSelected(folder.id)) {
      selection.selectFolderById(folder.id);
    }
    const ids = isMobile && !selection.isFolderSelected(folder.id)
      ? [folder.id]
      : selection.selectedFolderIds();
    const single = ids.length === 1;
    const isDyn = tree.isDynamic(folder.id);
    contextMenu.show(e, [
      ...(isMobile && single ? [
        { label: "Folder settings…", icon: "⚙", onSelect: () => selection.selectFolderById(folder.id) },
      ] : []),
      ...(single && !isDyn ? [
        { label: "New subfolder…",       icon: "📁", onSelect: () => connectionActions.addSubfolderUnder(folder.id) },
        { label: "New connection here…", icon: "🖥", onSelect: () => connectionActions.addConnectionUnder(folder.id) },
        { label: "New dynamic subfolder…", icon: "⟳", onSelect: () => dynEditor.showNew(folder.id) },
        { label: "Rename…",              icon: "✎", onSelect: () => connectionActions.renameFolder(folder.id) },
      ] : []),
      ...(single && isDyn ? [
        { label: "Refresh now",            icon: "⟳", onSelect: () => api.dynamicFolderRefreshNow(folder.id).catch(console.warn) },
        { label: "Edit dynamic config…",   icon: "⚙", onSelect: () => dynEditor.showEdit(folder.id) },
        { label: "Rename…",                icon: "✎", onSelect: () => connectionActions.renameFolder(folder.id) },
      ] : []),
      {
        label: single ? "Move to folder…" : `Move ${ids.length} folders to…`,
        icon: "↪",
        onSelect: () => connectionActions.openMoveTo([], ids),
      },
      {
        label: single
          ? `Export folder…`
          : `Export ${ids.length} folders…`,
        icon: "⤓",
        onSelect: () => {
          const baseName = single
            ? (tree.folderById(folder.id)?.name ?? "folder")
            : `${ids.length}-folders`;
          exportModal.showFolders(
            ids,
            baseName.replace(/[^a-z0-9._-]+/gi, "-"),
          );
        },
      },
      {
        label: single ? "Delete folder" : `Delete ${ids.length} folders`,
        icon: "🗑",
        danger: true,
        onSelect: () => connectionActions.openDeleteFolders(ids),
      },
    ]);
  }

  function openConnMenu(e: MouseEvent, conn: { id: string; name: string; hostname: string }) {
    if (!selection.isConnectionSelected(conn.id)) selection.selectConnection(conn.id);
    const ids = selection.selectedConnectionIds();
    const allFav = ids.every((id) => tree.connectionById(id)?.favorite);
    contextMenu.show(e, [
      {
        label: ids.length > 1 ? `Connect all (${ids.length})` : "Connect",
        icon: "▶",
        onSelect: () => connectionActions.connectMany(ids),
      },
      ...(ids.length === 1 ? [{
        label: "Open in external terminal",
        icon: "↗",
        onSelect: () => connectionActions.launchExternal(ids[0]),
      }] : []),
      ...(ids.length === 1 && tree.connectionById(ids[0])?.overrides?.vnc_enabled ? [{
        label: "Open VNC console",
        icon: "🖳",
        onSelect: () => connectionActions.openVncConnection(ids[0]),
      }] : []),
      {
        label: allFav
          ? (ids.length > 1 ? "Remove from favourites" : "Remove favourite")
          : (ids.length > 1 ? "Mark as favourites" : "Mark as favourite"),
        icon: allFav ? "☆" : "★",
        onSelect: () => connectionActions.toggleFavorites(ids),
      },
      ...(ids.length === 1 ? [{
        label: "Clone connection",
        icon: "⎘",
        onSelect: () => connectionActions.cloneConnection(ids[0]),
      }] : []),
      {
        label: ids.length > 1 ? `Export ${ids.length}…` : "Export…",
        icon: "⤓",
        onSelect: () => {
          const name = ids.length === 1
            ? (tree.connectionById(ids[0])?.name ?? "connection")
            : `${ids.length}-connections`;
          exportModal.show(ids, name.replace(/[^a-z0-9._-]+/gi, "-"));
        },
      },
      {
        label: ids.length > 1 ? `Move ${ids.length} connections to…` : "Move to folder…",
        icon: "↪",
        onSelect: () => connectionActions.openMoveTo(ids, []),
      },
      {
        label: ids.length > 1 ? `Delete ${ids.length} connections` : "Delete connection",
        icon: "🗑",
        danger: true,
        onSelect: () => connectionActions.openDeleteConnections(ids),
      },
    ]);
  }
  function toggle(e: Event) {
    e.stopPropagation();
    expandedConnections.toggle(folder.id);
  }
  function onRowKey(e: KeyboardEvent) {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      selectFolder();
    } else if (e.key === "ArrowRight") {
      expandedConnections.set(folder.id, true);
    } else if (e.key === "ArrowLeft") {
      expandedConnections.set(folder.id, false);
    }
  }
  const isSelected = $derived(selection.isFolderSelected(folder.id));
  const isAnchorFolder = $derived(
    selection.current.kind === "folder" && selection.current.id === folder.id
  );
  const dynEntryCount = $derived.by(() => {
    void tree.version;
    return (tree.dynamicEntries[folder.id] ?? []).length;
  });
  const hasChildren = $derived(
    children.length + conns.length > 0
    || (isDynamicFolder && (dynEntryCount > 0 || !tree.dynamicEntries[folder.id]))
  );

  // ---------- drag & drop reorganization ----------
  //
  // Tree DnD operates alongside the existing drag-into-pane workflow (which
  // sets drag.connectionId/tabId on dragstart). We don't add a separate
  // store; we just intercept dragover/drop on tree rows.
  //
  // Source kind is inferred from the drag store: connectionId set =
  // dragging a connection; folderId set = dragging a folder.

  function currentSource(): { kind: DragKind; id: string } | null {
    if (drag.connectionId) return { kind: "connection", id: drag.connectionId };
    if (drag.folderId)     return { kind: "folder",     id: drag.folderId };
    return null;
  }

  function handleFolderDragOver(e: DragEvent, rowEl: HTMLElement) {
    const src = currentSource();
    if (!src) return; // not a tree drag (could be tab→pane, ignore)
    const intent = computeIntent(e, rowEl, true);
    if (isInvalidDrop(src.kind, src.id, "folder", folder.id, intent)) {
      drag.hoverTree(null, null, null);
      return;
    }
    e.preventDefault();          // accept the drop
    e.dataTransfer!.dropEffect = "move";
    drag.hoverTree("folder", folder.id, intent);
  }

  function handleConnDragOver(e: DragEvent, rowEl: HTMLElement, connId: string) {
    const src = currentSource();
    if (!src) return;
    const intent = computeIntent(e, rowEl, false);
    if (isInvalidDrop(src.kind, src.id, "connection", connId, intent)) {
      drag.hoverTree(null, null, null);
      return;
    }
    e.preventDefault();
    e.dataTransfer!.dropEffect = "move";
    drag.hoverTree("connection", connId, intent);
  }

  // When the dragged set contains more than one item, applyMultiDrop
  // collapses every intent to "into target's parent folder" - reorder
  // semantics for multi would be ambiguous and rarely useful.
  function isMultiDrag(): boolean {
    return drag.multiConnIds.length + drag.multiFolderIds.length > 1;
  }

  async function handleFolderDrop(e: DragEvent) {
    const src = currentSource();
    if (!src) return;
    e.preventDefault();
    e.stopPropagation();
    if (isMultiDrag()) {
      // Snapshot the ids before drag.end() - it clears the buckets.
      const conns = drag.multiConnIds.slice();
      const folders = drag.multiFolderIds.slice();
      drag.end();
      try {
        await applyMultiDrop(conns, folders, "folder", folder.id);
      } catch (err) {
        console.error("multi tree drop failed", err);
      }
      return;
    }
    // Compute intent fresh from the drop event - don't rely on overTreeIntent
    // which may have been cleared by a dragleave racing the drop.
    const intent = computeIntent(e, e.currentTarget as HTMLElement, true);
    if (isInvalidDrop(src.kind, src.id, "folder", folder.id, intent)) return;
    drag.end();
    try {
      await applyDrop(src.kind, src.id, "folder", folder.id, intent);
    } catch (err) {
      console.error("tree drop failed", err);
    }
  }

  async function handleConnDrop(e: DragEvent, connId: string) {
    const src = currentSource();
    if (!src) return;
    e.preventDefault();
    e.stopPropagation();
    if (isMultiDrag()) {
      const conns = drag.multiConnIds.slice();
      const folders = drag.multiFolderIds.slice();
      drag.end();
      try {
        await applyMultiDrop(conns, folders, "connection", connId);
      } catch (err) {
        console.error("multi tree drop failed", err);
      }
      return;
    }
    const intent = computeIntent(e, e.currentTarget as HTMLElement, false);
    if (isInvalidDrop(src.kind, src.id, "connection", connId, intent)) return;
    drag.end();
    try {
      await applyDrop(src.kind, src.id, "connection", connId, intent);
    } catch (err) {
      console.error("tree drop failed", err);
    }
  }

  function indicatorClass(
    kind: "folder" | "connection",
    id: string
  ): string {
    if (drag.overTreeKind === kind && drag.overTreeId === id) {
      return "drop-" + (drag.overTreeIntent ?? "");
    }
    return "";
  }
</script>

<div class="node">
  <div
    class="row {indicatorClass('folder', folder.id)}"
    class:selected={isSelected}
    class:anchor={isAnchorFolder && isSelected}
    class:tagged={!!folderColor}
    class:tag-bg={!!folderColor && appPrefs.tagBackground}
    style="--depth: {depth}; --tag-color: {folderColor};"
    role="treeitem"
    tabindex="0"
    aria-expanded={expanded}
    aria-selected={isSelected}
    data-kind="folder"
    data-id={folder.id}
    draggable="true"
    onmousedown={recordMods}
    onclick={(e) => onFolderRowClick(e)}
    ondblclick={toggle}
    oncontextmenu={openFolderMenu}
    onkeydown={onRowKey}
    ondragstart={(e) => {
      // If the user grabbed a folder that's part of the current multi-
      // folder selection, drag the whole set; otherwise normal single-
      // drag. The single folderId is still set so existing validity /
      // hover code sees a grab.
      const inMulti = selection.isFolderSelected(folder.id) && selection.folderMultiCount() > 1;
      if (inMulti) {
        const ids = selection.selectedFolderIds();
        drag.startFolder(folder.id, ids);
        setMultiDragImage(e.dataTransfer, ids.length, ids.length === 1 ? "folder" : "folders");
      } else {
        drag.startFolder(folder.id);
      }
      e.dataTransfer!.effectAllowed = "move";
      e.stopPropagation();
    }}
    ondragend={() => drag.end()}
    ondragover={(e) => handleFolderDragOver(e, e.currentTarget as HTMLElement)}
    ondragleave={(e) => {
      // Ignore dragleave that fires when entering a child element of this row.
      if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
      if (drag.overTreeKind === "folder" && drag.overTreeId === folder.id) {
        drag.hoverTree(null, null, null);
      }
    }}
    ondrop={handleFolderDrop}
  >
    <span
      class="chev"
      role="button"
      tabindex="-1"
      aria-label={expanded ? "collapse" : "expand"}
      onclick={toggle}
      onkeydown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          toggle(e);
        }
      }}
    >
      {#if hasChildren}
        {expanded ? "▾" : "▸"}
      {:else}
        &nbsp;
      {/if}
    </span>
    <span class="icon" class:dyn-icon={isDynamicFolder}><Icon imageId={folder.icon_image_id}>
      {#if isDynamicFolder}
        <IconGlobe size={14} />
      {:else}
        <IconFolder size={14} />
      {/if}
    </Icon></span>
    <span
      class="name"
      class:has-live={liveInSubtree > 0}
      class:dyn-name={isDynamicFolder}
      title={isDynamicFolder ? dynamicTooltip : ""}
    >{folder.name}</span>
    {#if isDynamicFolder && dynamicMeta?.last_error}
      <button
        class="dyn-err-dot"
        title={`Refresh error - click to retry. ${dynamicMeta.last_error}`}
        onclick={(e) => { e.stopPropagation(); retryDynamicRefresh(); }}
      >!</button>
    {/if}
    {#if isDynamicFolder}
      <span class="dyn-tag" title={dynamicTooltip}>{dynamicMeta?.provider ?? "dyn"}</span>
    {/if}
    {#if liveInSubtree > 0}
      <span class="live-badge" title="{liveInSubtree} connected">●{liveInSubtree}</span>
    {/if}
    {#if !isDynamicFolder}
      <span class="count">{hasChildren ? children.length + conns.length : ""}</span>
    {:else}
      {@const dc = (tree.dynamicEntries[folder.id] ?? []).length}
      {#if dc > 0}<span class="count">{dc}</span>{/if}
    {/if}
  </div>

  {#if expanded}
    <div class="children">
      {#each children as child (child.id)}
        <TreeNodeSelf folder={child} depth={depth + 1} />
      {/each}

      {#if isDynamicFolder}
        {#if dynamicBuckets.hosts.length > 0}
          <div class="row dyn-bucket" style="--depth: {depth + 1};">
            <span class="bucket-label">Hosts ({dynamicBuckets.hosts.length})</span>
          </div>
          {#each dynamicBuckets.hosts as e (e.id)}
            {@const HIc = dynamicEntryIcon(e.kind)}
            <div
              class="row dyn-entry"
              class:stopped={e.status === "stopped"}
              class:selected={selection.isDynamicEntrySelected(folder.id, e.id)}
              style="--depth: {depth + 2};"
              role="treeitem"
              aria-selected={selection.isDynamicEntrySelected(folder.id, e.id)}
              tabindex="0"
              onclick={(ev) => onDynRowClick(ev, folder.id, e.id)}
              ondblclick={() => connectDynamic(folder.id, e)}
              onkeydown={(ev) => {
                if (ev.key === "Enter") {
                  ev.preventDefault();
                  const selected = selection.selectedDynamicEntries();
                  if (selected.length > 1 && selection.isDynamicEntrySelected(folder.id, e.id)) {
                    connectionActions.connectDynamicMany(selected);
                  } else {
                    connectDynamic(folder.id, e);
                  }
                }
                else if (ev.key === " ") { ev.preventDefault(); selection.selectDynamicEntry(folder.id, e.id); }
              }}
              title="Click to inspect, double-click to connect ({e.hostname})"
            >
              <span class="dyn-icon"><HIc size={13} /></span>
              <span class="dyn-name">{e.name}</span>
              <span class="dyn-host mono">{e.hostname}</span>
              {#if e.status === "stopped"}<span class="dyn-status">stopped</span>{/if}
            </div>
          {/each}
        {/if}
        {#if dynamicBuckets.guests.length > 0}
          <div class="row dyn-bucket" style="--depth: {depth + 1};">
            <span class="bucket-label">Guests ({dynamicBuckets.guests.length})</span>
          </div>
          {#each dynamicBuckets.guests as e (e.id)}
            {@const GIc = dynamicEntryIcon(e.kind)}
            <div
              class="row dyn-entry"
              class:stopped={e.status === "stopped"}
              class:selected={selection.isDynamicEntrySelected(folder.id, e.id)}
              style="--depth: {depth + 2};"
              role="treeitem"
              aria-selected={selection.isDynamicEntrySelected(folder.id, e.id)}
              tabindex="0"
              onclick={(ev) => onDynRowClick(ev, folder.id, e.id)}
              ondblclick={() => connectDynamic(folder.id, e)}
              onkeydown={(ev) => {
                if (ev.key === "Enter") {
                  ev.preventDefault();
                  const selected = selection.selectedDynamicEntries();
                  if (selected.length > 1 && selection.isDynamicEntrySelected(folder.id, e.id)) {
                    connectionActions.connectDynamicMany(selected);
                  } else {
                    connectDynamic(folder.id, e);
                  }
                }
                else if (ev.key === " ") { ev.preventDefault(); selection.selectDynamicEntry(folder.id, e.id); }
              }}
              title="Click to inspect, double-click to connect ({e.hostname})"
            >
              <span class="dyn-icon"><GIc size={13} /></span>
              <span class="dyn-kind mono">{e.kind === "guest_vm" ? "VM" : "LXC"}</span>
              <span class="dyn-name">{e.name}</span>
              <span class="dyn-host mono">{e.hostname}</span>
              {#if e.status === "stopped"}<span class="dyn-status">stopped</span>{/if}
            </div>
          {/each}
        {/if}
        {#if !tree.dynamicEntries[folder.id]}
          <div class="row dyn-loading" style="--depth: {depth + 1};">Loading…</div>
        {/if}
      {/if}

      {#each conns as conn (conn.id)}
        {@const selConn = selection.isConnectionSelected(conn.id)}
        {@const isAnchor =
          selection.current.kind === "connection" &&
          selection.current.id === conn.id}
        {@const isConn = connectingId === conn.id}
        {@const isLive = liveConnIds.has(conn.id)}
        {@const connColor = tree.resolveColorForConnection(conn.id)}
        {@const jumpChain = tree.resolveJumpChainForConnection(conn.id)}
        {@const rowTitle = jumpChain.length > 0
          ? `${conn.hostname}\nvia ${jumpChain.join(" -> ")}`
          : conn.hostname}
        <div
          class="row conn {indicatorClass('connection', conn.id)}"
          class:selected={selConn}
          class:anchor={isAnchor && selConn}
          class:connecting={isConn}
          class:live={isLive}
          class:tagged={!!connColor}
          class:tag-bg={!!connColor && appPrefs.tagBackground}
          class:active-session={appPrefs.activeRowEmphasis && conn.id === activeConnId}
          style="--depth: {depth + 1}; --tag-color: {connColor};"
          role="treeitem"
          tabindex="0"
          aria-selected={selConn}
          data-kind="connection"
          data-id={conn.id}
          draggable={!isConn}
          title={rowTitle}
          onmousedown={recordMods}
          onclick={(e) => handleConnClick(e, conn.id, conn.name, conn.hostname)}
          oncontextmenu={(e) => openConnMenu(e, conn)}
          onkeydown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              // Multi-select active: fan out to every selected
              // connection. Single connection still hits quickConnect
              // so the existing dedupe + tab-add path keeps working.
              if (selection.multiCount() > 1 && selection.isConnectionSelected(conn.id)) {
                connectionActions.connectMany(selection.selectedConnectionIds());
              } else {
                quickConnect(conn.id, conn.name, conn.hostname);
              }
            } else if (e.key === " ") {
              e.preventDefault();
              selection.selectConnection(conn.id);
            }
          }}
          ondragstart={(e) => {
            if (isConn) return;
            const inMulti = selection.isConnectionSelected(conn.id) && selection.multiCount() > 1;
            if (inMulti) {
              const ids = selection.selectedConnectionIds();
              drag.startConnection(conn.id, ids);
              setMultiDragImage(e.dataTransfer, ids.length, ids.length === 1 ? "connection" : "connections");
            } else {
              drag.startConnection(conn.id);
            }
            e.dataTransfer!.effectAllowed = "move";
            e.stopPropagation();
          }}
          ondragend={() => drag.end()}
          ondragover={(e) => handleConnDragOver(e, e.currentTarget as HTMLElement, conn.id)}
          ondragleave={(e) => {
            if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
            if (drag.overTreeKind === "connection" && drag.overTreeId === conn.id) {
              drag.hoverTree(null, null, null);
            }
          }}
          ondrop={(e) => handleConnDrop(e, conn.id)}
        >
          <span class="chev">{isLive ? "●" : " "}</span>
          <span class="icon">
            {#if isConn}
              <IconLoading size={14} class="spin" />
            {:else}
              <Icon imageId={conn.icon_image_id}>
                <IconHost size={14} />
              </Icon>
            {/if}
          </span>
          <span class="name">{conn.name}</span>
          {#if conn.favorite}<span class="fav" title="Favourite"><IconStar size={11} fill="var(--yellow)" /></span>{/if}
          {#if isConn}
            <span class="conn-hint">connecting…</span>
          {:else if connectErrId === conn.id}
            <span class="conn-err" title={connectErr ?? ""}><IconX size={12} /></span>
          {:else if conn.hostname}
            <span class="host">{conn.hostname}</span>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    width: 100%;
    padding: var(--row-pad-y) 0.4rem var(--row-pad-y) calc(0.4rem + var(--depth, 0) * 1rem);
    background: transparent;
    color: inherit;
    cursor: pointer;
    border-radius: 3px;
    position: relative;
  }
  .row:hover { background: var(--surface0); }
  .row.selected { background: var(--surface1); }
  /* Unified selected-row visual for both static connections and
     dynamic-inventory entries. Same accent-tint background +
     left-edge accent strip so the two row types look identical
     when picked - earlier rules separated them which led to the
     light-theme regression where dynamic rows rendered with a
     darker tint than the regular ones (or vice-versa, depending
     on whether the user had tag-background enabled). */
  .row.conn.selected:not(.anchor),
  .row.dyn-entry.selected,
  .row.selected:not(.conn):not(.anchor):not(.dyn-entry) {
    background: color-mix(in oklab, var(--accent) 25%, var(--surface0));
    box-shadow: inset 2px 0 0 var(--blue);
  }
  .row:focus { outline: 1px solid var(--blue); outline-offset: -1px; }
  /* Color-tag: 3px stripe along the left edge via box-shadow (so it
     stacks cleanly with the drop-before/after pseudos and the
     selection's own inset shadow). */
  .row.tagged {
    box-shadow: inset 3px 0 0 var(--tag-color);
  }
  /* Multi-select extras already use inset 2px blue. Combine them. */
  .row.conn.selected.tagged:not(.anchor) {
    box-shadow: inset 3px 0 0 var(--tag-color), inset 5px 0 0 var(--blue);
  }
  /* Optional: tint the whole row with the tag colour. Driven by
     appPrefs.tagBackground via a class on the rows. color-mix gives
     us a low-opacity wash without per-tag CSS. */
  .row.tagged.tag-bg {
    background: color-mix(in srgb, var(--tag-color) 14%, transparent);
  }
  .row.tagged.tag-bg:hover {
    background: color-mix(in srgb, var(--tag-color) 22%, transparent);
  }
  .row.tagged.tag-bg.selected {
    background: color-mix(in srgb, var(--tag-color) 28%, var(--surface1));
  }
  /* "This is the row you're looking at" - the connection whose
     session backs the focused terminal pane. Bright cyan strip on
     the right edge so it's distinguishable from selection and the
     left-edge color tag. Driven by appPrefs.activeRowEmphasis. */
  .row.conn.active-session {
    box-shadow: inset -3px 0 0 var(--teal);
  }
  .row.conn.tagged.active-session {
    box-shadow: inset 3px 0 0 var(--tag-color), inset -3px 0 0 var(--teal);
  }
  .row.conn.tagged.active-session.selected:not(.anchor) {
    box-shadow: inset 3px 0 0 var(--tag-color), inset 5px 0 0 var(--blue), inset -3px 0 0 var(--teal);
  }
  .chev { width: 1rem; color: var(--overlay0); font-size: 0.85rem; text-align: center; }
  /* Touch: bigger rows + a wider chevron hit area so folders are easy to
     expand with a fingertip. The whole folder row also toggles on tap
     (see onFolderRowClick), this just makes the chevron itself forgiving. */
  @media (pointer: coarse) {
    .row { padding-top: 0.5rem; padding-bottom: 0.5rem; }
    .chev { width: 1.8rem; font-size: 1.05rem; }
  }
  .icon { width: 1.2rem; text-align: center; font-size: 0.85rem; }
  .name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .count { color: var(--overlay1); font-size: 0.75rem; }
  .host { color: var(--overlay1); font-size: 0.75rem; margin-left: 0.4rem; }
  .row.connecting { opacity: 0.7; cursor: wait; }
  .row.conn.live .name { font-weight: 600; color: var(--green); }
  .row.conn.live .chev { color: var(--green); }
  .name.has-live { color: var(--green); font-weight: 600; }
  .live-badge {
    color: var(--green);
    font-size: 0.7rem;
    background: color-mix(in oklab, var(--green) 12%, var(--bg-panel));
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    margin-left: 0.3rem;
  }
  .conn-hint { color: var(--yellow); font-size: 0.72rem; margin-left: 0.3rem; }
  .fav { color: var(--yellow); font-size: 0.78rem; margin-left: 0.25rem; }
  .conn-err  { color: var(--red); font-size: 0.78rem; margin-left: 0.3rem; cursor: help; }

  /* Drop indicators */
  .row.drop-before::before,
  .row.drop-after::after {
    content: "";
    position: absolute;
    left: 0;
    right: 0;
    height: 2px;
    background: var(--blue);
    z-index: 5;
  }
  .row.drop-before::before { top: -1px; }
  .row.drop-after::after   { bottom: -1px; }
  .row.drop-inside {
    background: var(--blue)33;
    outline: 1px dashed var(--blue);
    outline-offset: -2px;
  }
  /* Dynamic folder accent: globe icon + tinted name + provider pill */
  .icon.dyn-icon { color: var(--teal); }
  .name.dyn-name { color: var(--teal); }
  .dyn-tag {
    color: var(--teal);
    background: color-mix(in oklab, var(--teal) 16%, var(--bg-panel));
    font-size: 0.62rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 0.05rem 0.35rem;
    border-radius: 2px;
    margin-left: 0.3rem;
  }
  .dyn-err-dot {
    color: var(--red);
    background: color-mix(in oklab, var(--red) 12%, var(--bg-panel));
    border: 0;
    padding: 0;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 0.7rem;
    font-weight: 700;
    margin-left: 0.2rem;
    cursor: pointer;
    font-family: inherit;
  }
  .dyn-err-dot:hover {
    background: var(--red);
    color: var(--on-accent);
  }

  /* Dynamic inventory rows */
  .row.dyn-bucket {
    cursor: default;
    pointer-events: none;
    color: var(--overlay0);
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding-top: 0.35rem;
    padding-bottom: 0.1rem;
  }
  .row.dyn-bucket:hover { background: transparent; }
  .row.dyn-loading {
    color: var(--overlay0);
    font-style: italic;
    cursor: default;
    pointer-events: none;
  }
  .row.dyn-entry {
    gap: 0.5rem;
  }
  .row.dyn-entry .dyn-icon {
    display: inline-flex;
    align-items: center;
    color: var(--subtext0);
    flex-shrink: 0;
  }
  .row.dyn-entry .dyn-name { color: var(--text); flex-shrink: 1; }
  .row.dyn-entry .dyn-host {
    color: var(--subtext0);
    font-size: 0.72rem;
    font-family: ui-monospace, monospace;
    flex-shrink: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row.dyn-entry .dyn-kind {
    color: var(--overlay0);
    font-size: 0.65rem;
    background: var(--surface0);
    padding: 0.05rem 0.3rem;
    border-radius: 2px;
    font-family: ui-monospace, monospace;
  }
  .row.dyn-entry .dyn-status {
    color: var(--yellow);
    font-size: 0.65rem;
    background: color-mix(in oklab, var(--yellow) 14%, var(--bg-panel));
    padding: 0.05rem 0.3rem;
    border-radius: 2px;
    margin-left: auto;
  }
  .row.dyn-entry.stopped .dyn-name,
  .row.dyn-entry.stopped .dyn-host { opacity: 0.55; }
</style>
