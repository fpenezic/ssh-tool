<script lang="ts">
  import { tree, selection, drag, sessions, paneTabs, view } from "./stores.svelte";
  import TreeNode from "./TreeNode.svelte";
  import { api } from "./api";
  import { computeIntent, isInvalidDrop, applyDrop, applyDropToRoot, applyMultiDrop, applyMultiDropToRoot, type DragKind } from "./treeDnd";
  import { setMultiDragImage } from "./dragImage";
  import QuickAccess from "./QuickAccess.svelte";
  import { IconFolderPlus, IconPlus, IconRotateCw, IconHost, IconLoading, IconX, IconGlobe, IconExpandAll, IconCollapseAll,
    IconRefresh, IconPlay, IconExternalLink, IconStar, IconMoveToFolder, IconDownload, IconTrash, IconTerminal } from "./iconMap";
  import { expandedConnections } from "./treeState.svelte";
  import TagFilter from "./TagFilter.svelte";
  import { tagFilter } from "./tagFilter.svelte.ts";
  import { nameFilter } from "./nameFilter.svelte.ts";
  import { IconSearch } from "./iconMap";
  import { contextMenu } from "./contextMenu.svelte.ts";
  import { exportModal } from "./exportModal.svelte.ts";
  import { connectionActions } from "./connectionActions.svelte";
  import { dynEditor } from "./dynEditor.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";

  // Right-click on empty tree area (between rows or below the last
  // row): offer New connection / New folder shortcut at root level.
  // Row right-clicks have their own menus and stopPropagation, so
  // this fires only when the user actually missed every row.
  function openEmptyAreaMenu(e: MouseEvent) {
    // Skip if the target is a row - row's own handler should win.
    const t = e.target as HTMLElement | null;
    if (t?.closest('[role="treeitem"]')) return;
    e.preventDefault();
    contextMenu.show(e, [
      { label: "New connection",     iconComponent: IconHost, onSelect: addConnection },
      { label: "New local shell…",   iconComponent: IconTerminal, onSelect: addLocalConnection },
      { label: "New folder",         iconComponent: IconFolderPlus, onSelect: addRootFolder },
      { label: "New dynamic folder…", iconComponent: IconRefresh, onSelect: () => dynEditor.showNew(null) },
    ]);
  }

  function openConnMenu(e: MouseEvent, conn: { id: string; name: string; hostname: string | null }) {
    if (!selection.isConnectionSelected(conn.id)) selection.selectConnection(conn.id);
    const ids = selection.selectedConnectionIds();
    const allFav = ids.every((id) => tree.connectionById(id)?.favorite);
    contextMenu.show(e, [
      {
        label: ids.length > 1 ? `Connect all (${ids.length})` : "Connect",
        iconComponent: IconPlay,
        onSelect: () => connectionActions.connectMany(ids),
      },
      ...(ids.length === 1 ? [{
        label: "Open in external terminal",
        iconComponent: IconExternalLink,
        onSelect: () => connectionActions.launchExternal(ids[0]),
      }] : []),
      {
        label: allFav
          ? (ids.length > 1 ? "Remove from favourites" : "Remove favourite")
          : (ids.length > 1 ? "Mark as favourites" : "Mark as favourite"),
        iconComponent: IconStar,
        onSelect: () => connectionActions.toggleFavorites(ids),
      },
      {
        label: ids.length > 1 ? `Move ${ids.length} connections to…` : "Move to folder…",
        iconComponent: IconMoveToFolder,
        onSelect: () => connectionActions.openMoveTo(ids, []),
      },
      {
        label: ids.length > 1 ? `Export ${ids.length}…` : "Export…",
        iconComponent: IconDownload,
        onSelect: () => {
          const name = ids.length === 1
            ? (tree.connectionById(ids[0])?.name ?? "connection")
            : `${ids.length}-connections`;
          exportModal.show(ids, name.replace(/[^a-z0-9._-]+/gi, "-"));
        },
      },
      {
        label: ids.length > 1 ? `Delete ${ids.length} connections` : "Delete connection",
        iconComponent: IconTrash,
        danger: true,
        onSelect: () => connectionActions.openDeleteConnections(ids),
      },
    ]);
  }

  let connectingId = $state<string | null>(null);
  let connectErrId = $state<string | null>(null);
  let connectErr   = $state<string | null>(null);

  async function quickConnect(connId: string, _name: string, _hostname: string) {
    if (connectingId === connId) return;
    connectingId = connId;
    connectErrId = null;
    connectErr   = null;
    const ok = await connectionActions.connectOne(connId);
    if (!ok) {
      const last = connectionActions.lastConnectError[connId];
      connectErrId = connId;
      connectErr   = last?.message ?? "connect failed";
    }
    connectingId = null;
  }

  const liveConnIds = $derived(
    new Set(
      sessions.tabs
        .filter((t) => t.status === "connected")
        .map((t) => t.connectionId)
    )
  );

  let lastClickId = "";
  let lastClickAt = 0;

  // Same modifier-capture trick as TreeNode - some WebView2 /
  // WebKitGTK combos drop modifier flags off `click` when the target
  // is draggable. mousedown fires first and keeps them.
  let lastMouseMods = { ctrl: false, meta: false, shift: false };
  function recordMods(e: MouseEvent) {
    lastMouseMods = { ctrl: e.ctrlKey, meta: e.metaKey, shift: e.shiftKey };
  }

  function handleConnClick(e: MouseEvent, connId: string, name: string, hostname: string) {
    const ctrl = e.ctrlKey || lastMouseMods.ctrl;
    const meta = e.metaKey || lastMouseMods.meta;
    const shift = e.shiftKey || lastMouseMods.shift;
    lastMouseMods = { ctrl: false, meta: false, shift: false };

    if (shift) {
      selection.rangeConnection(connId, tree.flatVisibleConnectionIds());
      return;
    }
    if (ctrl || meta) {
      selection.toggleConnection(connId);
      return;
    }
    selection.select({ kind: "connection", id: connId });
    const now = Date.now();
    if (connId === lastClickId && now - lastClickAt < 400) {
      lastClickId = "";
      quickConnect(connId, name, hostname);
    } else {
      lastClickId = connId;
      lastClickAt = now;
    }
  }

  const roots = $derived.by(() => {
    void tree.version;
    void nameFilter.query;
    return tree.childrenOf(null).filter(
      (f) => tagFilter.folderHasMatch(f.id) && nameFilter.folderHasMatch(f.id),
    );
  });
  const rootConns = $derived.by(() => {
    void tree.version;
    void nameFilter.query;
    return tree.connectionsIn(null).filter(
      (c) => tagFilter.connectionMatches(c.id) && nameFilter.connectionMatches(c.id),
    );
  });

  // Where a "new folder / connection" from the header toolbar lands:
  // - a selected folder -> inside it
  // - a selected connection -> alongside it (same parent folder), so the
  //   user doesn't have to DnD out of root after creating
  // - nothing selected -> root
  // Returns undefined for root (the create IPCs treat undefined as root).
  function targetFolderId(): string | undefined {
    const cur = selection.current;
    if (cur.kind === "folder") return cur.id;
    if (cur.kind === "connection") {
      return tree.connectionById(cur.id)?.folder_id ?? undefined;
    }
    return undefined;
  }

  async function addRootFolder() {
    const name = await showPrompt("Folder name?");
    if (!name) return;
    await api.foldersCreate({ name, parentId: targetFolderId() });
    await tree.load();
  }

  async function addConnection() {
    const folderId = targetFolderId();
    const name = await showPrompt("Connection name?");
    if (!name) return;
    const hostname = await showPrompt("Hostname?") ?? "";
    const conn = await api.connectionsCreate({
      folderId,
      name,
      hostname,
    });
    await tree.load();
    selection.select({ kind: "connection", id: conn.id });
  }

  // Create a local-shell connection (telnet client, serial console,
  // "claude", any command). Just a name prompt - shell kind + the command
  // are set in the editor. Starts with shell = auto, no command.
  async function addLocalConnection() {
    const folderId = targetFolderId();
    const name = await showPrompt("Local shell connection name?");
    if (!name) return;
    const conn = await api.connectionsCreate({
      folderId,
      name,
      hostname: "",
      protocol: "local",
    });
    await tree.load();
    selection.select({ kind: "connection", id: conn.id });
  }

  // ----- drag & drop for root-level rows + empty-space drop -----

  function currentSource(): { kind: DragKind; id: string } | null {
    if (drag.connectionId) return { kind: "connection", id: drag.connectionId };
    if (drag.folderId)     return { kind: "folder",     id: drag.folderId };
    return null;
  }

  function onRootConnDragOver(e: DragEvent, rowEl: HTMLElement, connId: string) {
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
  async function onRootConnDrop(e: DragEvent, connId: string) {
    const src = currentSource();
    if (!src) return;
    e.preventDefault();
    e.stopPropagation();
    const isMulti = drag.multiConnIds.length + drag.multiFolderIds.length > 1;
    if (isMulti) {
      const conn = drag.multiConnIds.slice();
      const folder = drag.multiFolderIds.slice();
      drag.end();
      try { await applyMultiDrop(conn, folder, "connection", connId); }
      catch (err) { console.error(err); }
      return;
    }
    const intent = computeIntent(e, e.currentTarget as HTMLElement, false);
    if (isInvalidDrop(src.kind, src.id, "connection", connId, intent)) return;
    drag.end();
    try {
      await applyDrop(src.kind, src.id, "connection", connId, intent);
    } catch (err) { console.error(err); }
  }

  // Empty-area drop: move dragged item to top-level.
  let treeAreaDropActive = $state(false);
  function onTreeAreaDragOver(e: DragEvent) {
    const src = currentSource();
    if (!src) return;
    // Don't override row-level indicators if one is showing.
    if (drag.overTreeId) {
      treeAreaDropActive = false;
      return;
    }
    e.preventDefault();
    e.dataTransfer!.dropEffect = "move";
    treeAreaDropActive = true;
  }
  function onTreeAreaDragLeave() { treeAreaDropActive = false; }

  // Scroll position preservation across tree reloads.
  //
  // `tree.load()` reassigns the folders/connections arrays which
  // re-renders the each-block. The {#each} is keyed by id so nodes
  // are reused, but if anything changes the visible structure (sort
  // order, new rows) the browser nudges scrollTop back. We track the
  // last user-driven scrollTop in a plain (non-reactive) variable
  // and restore it whenever tree.version bumps.
  let treeEl = $state<HTMLDivElement | undefined>(undefined);
  let searchInputEl = $state<HTMLInputElement | undefined>(undefined);
  let lastScrollTop = 0;

  // Consume a pending reveal queued by view.reveal() (e.g. from a
  // credential's "Used by" entry). The ancestor folders are already
  // expanded by reveal(); we wait a frame for the rows to render, then
  // scroll the matching [data-kind][data-id] row into view.
  $effect(() => {
    const r = view.pendingTreeReveal;
    if (!r || !treeEl) return;
    void tree.version; // re-run once the tree has rendered
    requestAnimationFrame(() => {
      const el = treeEl?.querySelector<HTMLElement>(
        `[role="treeitem"][data-kind="${r.kind}"][data-id="${r.id}"]`,
      );
      el?.scrollIntoView({ block: "center" });
      view.pendingTreeReveal = null;
    });
  });
  function onTreeScroll() {
    if (treeEl) lastScrollTop = treeEl.scrollTop;
  }
  $effect(() => {
    void tree.version; // depend on reload signal
    if (!treeEl) return;
    // Apply on the next frame so the re-rendered children are laid
    // out before we set scrollTop (otherwise the container's
    // scrollHeight may still be smaller than the saved offset).
    requestAnimationFrame(() => {
      if (treeEl && lastScrollTop > 0) treeEl.scrollTop = lastScrollTop;
    });
  });

  // Tree-level keyboard navigation. ArrowUp/Down move focus to the
  // previous/next visible row (folder or connection) - DOM is the
  // source of truth: `.tree [role="treeitem"]` excluding any rows
  // nested in collapsed children (querySelectorAll only sees rendered
  // nodes, so filtering isn't required). Home / End jump to first /
  // last. Wraps at the edges so heavy keyboard users don't get stuck.
  // Keys handled while the search input has focus. Escape clears the
  // filter and returns focus to the tree; ArrowDown / Enter jump to
  // the first visible row so the user can navigate without grabbing
  // the mouse.
  function onSearchKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      nameFilter.clear();
      treeEl?.focus();
      return;
    }
    if (e.key === "ArrowDown" || e.key === "Enter") {
      const row = treeEl?.querySelector<HTMLElement>('[role="treeitem"]');
      if (row) {
        e.preventDefault();
        row.focus();
      }
    }
  }

  function onTreeKey(e: KeyboardEvent) {
    // Escape from anywhere in the tree clears an active filter
    // and returns focus to the search input. If no filter is
    // active, fall through so other Esc handlers (modals) can
    // still see the key.
    if (e.key === "Escape" && nameFilter.isActive()) {
      e.preventDefault();
      nameFilter.clear();
      searchInputEl?.focus();
      return;
    }

    // Delete key removes the selection. Picks up multi-select
    // automatically since connectionActions reads selection state.
    // Connections take precedence when both are selected (typical
    // path); folder-only selection routes through the folder-delete
    // flow which cascades.
    if (e.key === "Delete" || (e.key === "Backspace" && e.metaKey)) {
      const conns = selection.selectedConnectionIds();
      const folders = selection.selectedFolderIds();
      if (conns.length > 0) {
        e.preventDefault();
        connectionActions.openDeleteConnections(conns);
      } else if (folders.length > 0) {
        e.preventDefault();
        connectionActions.openDeleteFolders(folders);
      }
      return;
    }
    // Single printable key → forward to the search input. Keeps
    // the existing focused row alive; the user starts typing and
    // the filter narrows. Modifier-prefixed keys (Ctrl+C copy
    // etc.) are skipped so we don't swallow shortcuts.
    if (
      e.key.length === 1 &&
      !e.ctrlKey && !e.metaKey && !e.altKey &&
      searchInputEl &&
      document.activeElement !== searchInputEl
    ) {
      searchInputEl.focus();
      // Don't preventDefault - let the key land in the input
      // naturally so the first character isn't lost.
      return;
    }
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown" && e.key !== "Home" && e.key !== "End") {
      return;
    }
    const root = e.currentTarget as HTMLElement;
    const rows = Array.from(
      root.querySelectorAll<HTMLElement>('[role="treeitem"]')
    );
    if (rows.length === 0) return;
    const active = document.activeElement as HTMLElement | null;
    let idx = active ? rows.indexOf(active) : -1;
    if (e.key === "Home") idx = 0;
    else if (e.key === "End") idx = rows.length - 1;
    else if (e.key === "ArrowDown") idx = idx < 0 ? 0 : (idx + 1) % rows.length;
    else if (e.key === "ArrowUp") idx = idx < 0 ? rows.length - 1 : (idx - 1 + rows.length) % rows.length;
    e.preventDefault();
    const next = rows[idx];
    if (!next) return;
    next.focus();
    next.scrollIntoView({ block: "nearest" });
    // Auto-select so DetailPane reflects the highlighted row.
    const kind = next.dataset.kind;
    const id = next.dataset.id;
    if (kind === "folder" && id) {
      selection.select({ kind: "folder", id });
    } else if (kind === "connection" && id) {
      selection.select({ kind: "connection", id });
    }
  }
  async function onTreeAreaDrop(e: DragEvent) {
    treeAreaDropActive = false;
    const src = currentSource();
    if (!src) return;
    // If a row indicator was active, defer to it (drop event on the row
    // will fire first because of stopPropagation there).
    if (drag.overTreeId) return;
    e.preventDefault();
    const isMulti = drag.multiConnIds.length + drag.multiFolderIds.length > 1;
    const ids = { conn: drag.multiConnIds.slice(), folder: drag.multiFolderIds.slice() };
    drag.end();
    try {
      if (isMulti) {
        await applyMultiDropToRoot(ids.conn, ids.folder);
      } else {
        await applyDropToRoot(src.kind, src.id);
      }
    } catch (err) { console.error(err); }
  }

  function indicatorClass(connId: string): string {
    if (drag.overTreeKind === "connection" && drag.overTreeId === connId) {
      return "drop-" + (drag.overTreeIntent ?? "");
    }
    return "";
  }
</script>

<aside class="sidebar">
  <header>
    <h2>Connections</h2>
    <div class="actions">
      <button onclick={addRootFolder} title="New folder" class="iconbtn">
        <IconFolderPlus size={14} />
      </button>
      <button onclick={addConnection} title="New connection" class="iconbtn">
        <IconPlus size={14} /><IconHost size={14} />
      </button>
      <button onclick={addLocalConnection} title="New local shell connection (telnet / serial / claude / any command)" class="iconbtn">
        <IconPlus size={14} /><IconTerminal size={14} />
      </button>
      <button onclick={() => dynEditor.showNew(null)} title="New dynamic folder (cloud / hypervisor)" class="iconbtn">
        <IconGlobe size={14} />
      </button>
      {#if tree.folders.length > 0}
        {#if expandedConnections.ids.size > 0}
          <button onclick={() => expandedConnections.setAll([], false)} title="Collapse all folders" class="iconbtn">
            <IconCollapseAll size={14} />
          </button>
        {:else}
          <button onclick={() => expandedConnections.setAll(tree.folders.map((f) => f.id), true)} title="Expand all folders" class="iconbtn">
            <IconExpandAll size={14} />
          </button>
        {/if}
      {/if}
      <button onclick={() => tree.load()} title="Reload" class="iconbtn">
        <IconRotateCw size={14} />
      </button>
    </div>
  </header>

  {#if tree.loading}
    <!-- Skeleton rows instead of "Loading…" - flashes less on a cold
         cache load. Eight rows is roughly what a 320px sidebar shows. -->
    <div class="skeleton">
      {#each Array(8) as _, i (i)}
        <div class="sk-row" style="--w: {60 + (i * 13) % 35}%"></div>
      {/each}
    </div>
  {:else if tree.error}
    <div class="err">{tree.error}</div>
  {:else if tree.folders.length === 0 && tree.connections.length === 0}
    <div class="empty">
      <div class="ico"><IconHost size={36} /></div>
      <p>No connections yet.</p>
      <p class="muted">
        Click the new-connection button above to add one, or import an RDM
        JSON export from Settings.
      </p>
    </div>
  {:else}
    <div class="search-row">
      <IconSearch size={12} />
      <input
        type="text"
        class="search-input"
        bind:this={searchInputEl}
        placeholder="Filter by name or hostname… (start typing in the tree)"
        value={nameFilter.query}
        oninput={(e) => nameFilter.set((e.target as HTMLInputElement).value)}
        onkeydown={onSearchKey}
      />
      {#if nameFilter.isActive()}
        <button
          class="search-clear"
          onclick={() => nameFilter.clear()}
          title="Clear filter"
        >
          <IconX size={12} />
        </button>
      {/if}
    </div>
    <QuickAccess />
    <TagFilter />
    <div
      class="tree"
      bind:this={treeEl}
      onscroll={onTreeScroll}
      class:drop-area={treeAreaDropActive && drag.active}
      ondragover={onTreeAreaDragOver}
      ondragleave={onTreeAreaDragLeave}
      ondrop={onTreeAreaDrop}
      onkeydown={onTreeKey}
      oncontextmenu={openEmptyAreaMenu}
      role="tree"
      tabindex="-1"
    >
      {#each roots as f (f.id)}
        <TreeNode folder={f} depth={0} />
      {/each}
      {#each rootConns as conn (conn.id)}
        {@const sel = selection.isConnectionSelected(conn.id)}
        {@const isConn = connectingId === conn.id}
        {@const isLive = liveConnIds.has(conn.id)}
        <div
          class="row conn {indicatorClass(conn.id)}"
          class:selected={sel}
          class:connecting={isConn}
          class:live={isLive}
          role="treeitem"
          tabindex="0"
          aria-selected={sel}
          data-kind="connection"
          data-id={conn.id}
          draggable={!isConn}
          onmousedown={recordMods}
          onclick={(e) => handleConnClick(e, conn.id, conn.name, conn.hostname ?? "")}
          oncontextmenu={(e) => openConnMenu(e, conn)}
          onkeydown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (selection.multiCount() > 1 && selection.isConnectionSelected(conn.id)) {
                connectionActions.connectMany(selection.selectedConnectionIds());
              } else {
                quickConnect(conn.id, conn.name, conn.hostname ?? "");
              }
            } else if (e.key === " ") {
              e.preventDefault();
              selection.select({ kind: "connection", id: conn.id });
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
          ondragover={(e) => onRootConnDragOver(e, e.currentTarget as HTMLElement, conn.id)}
          ondragleave={(e) => {
            if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
            if (drag.overTreeKind === "connection" && drag.overTreeId === conn.id) {
              drag.hoverTree(null, null, null);
            }
          }}
          ondrop={(e) => onRootConnDrop(e, conn.id)}
        >
          <span class="chev">{isLive ? "●" : " "}</span>
          <span class="icon">
            {#if isConn}<IconLoading size={13} class="spin" />{:else}<IconHost size={13} />{/if}
          </span>
          <span class="name">{conn.name}</span>
          {#if isConn}
            <span class="conn-hint">connecting…</span>
          {:else if connectErrId === conn.id}
            <span class="conn-err" title={connectErr ?? ""}><IconX size={12} /></span>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</aside>

<style>
  .sidebar {
    background: var(--crust);
    color: var(--text);
    border-right: 1px solid var(--surface0);
    display: flex;
    flex-direction: column;
    min-width: 0;
    overflow: hidden;
  }
  header {
    padding: 0.6rem 0.8rem;
    border-bottom: 1px solid var(--surface0);
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  /* Header icon buttons - keep tight padding so two icons fit in one
     button (e.g. plus + host) without ballooning the chrome. */
  .iconbtn {
    display: inline-flex;
    align-items: center;
    gap: 0.15rem;
    padding: 0.2rem 0.35rem;
  }
  h2 {
    margin: 0;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--subtext0);
  }
  .actions { display: flex; gap: 0.25rem; }
  .actions button {
    background: transparent;
    border: 0;
    color: var(--subtext0);
    cursor: pointer;
    padding: 0.15rem 0.35rem;
    border-radius: 3px;
  }
  .actions button:hover { background: var(--surface0); color: var(--text); }
  .tree { flex: 1; overflow: auto; padding: 0.4rem 0; }

  .search-row {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--surface0);
    color: var(--overlay0);
  }
  .search-input {
    flex: 1;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    color: var(--text);
    font: inherit;
    font-size: 0.78rem;
    padding: 0.25rem 0.4rem;
    outline: none;
  }
  .search-input:focus { border-color: var(--surface2); }
  .search-clear {
    background: transparent;
    border: 0;
    color: var(--overlay0);
    cursor: pointer;
    padding: 2px;
    display: inline-flex;
    align-items: center;
  }
  .search-clear:hover { color: var(--text); }
  .hint, .err { padding: 0.6rem 0.8rem; color: var(--overlay0); }
  .err { color: var(--red); }
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    width: 100%;
    padding: 0.2rem 0.4rem;
    background: transparent;
    color: inherit;
    cursor: pointer;
    border-radius: 3px;
  }
  .row:hover { background: var(--surface0); }
  .row.selected { background: var(--surface1); }
  .chev { width: 1rem; }
  .icon { width: 1.2rem; text-align: center; font-size: 0.85rem; }
  .name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row.connecting { opacity: 0.7; cursor: wait; }
  .row.conn.live .name { font-weight: 600; color: var(--green); }
  .row.conn.live .chev { color: var(--green); }
  .conn-hint { color: var(--yellow); font-size: 0.72rem; margin-left: 0.3rem; }

  /* Empty state - shown when the tree finished loading with zero rows. */
  .empty {
    padding: 2rem 1rem;
    text-align: center;
    color: var(--subtext0);
  }
  .empty .ico { font-size: 2.5rem; opacity: 0.4; margin-bottom: 0.6rem; }
  .empty p { margin: 0.3rem 0; font-size: 0.85rem; }
  .empty p.muted { color: var(--overlay0); font-size: 0.78rem; }

  /* Skeleton rows. Pulsing opacity gives a hint of motion without a
     full shimmer (and keeps the styles tiny). */
  .skeleton { padding: 0.5rem 0.6rem; display: flex; flex-direction: column; gap: 0.4rem; }
  .sk-row {
    height: 0.7rem;
    width: var(--w, 70%);
    background: var(--surface0);
    border-radius: 3px;
    animation: pulse 1.4s ease-in-out infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 0.35; }
    50%      { opacity: 0.6; }
  }
  .conn-err  { color: var(--red); font-size: 0.78rem; margin-left: 0.3rem; cursor: help; }

  .row { position: relative; }
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

  .tree.drop-area {
    background: var(--blue)11;
    box-shadow: inset 0 0 0 1px var(--blue)44;
  }
</style>
