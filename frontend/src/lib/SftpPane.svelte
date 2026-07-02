<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api, type SftpEntry, type SftpTransferProgress } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import { IconFolder, IconFile, IconLink } from "./iconMap";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";

  interface Props {
    sessionId: string;
  }
  let { sessionId }: Props = $props();

  let cwd = $state("");
  let entries = $state<SftpEntry[]>([]);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let sortKey = $state<"name" | "size" | "mod_time">("name");
  let sortDir = $state<"asc" | "desc">("asc");
  let selected = $state<Set<string>>(new Set());

  type ActiveTransfer = {
    id: string;
    direction: "up" | "down";
    name: string;
    bytes: number;
    total: number;
    err?: string;
    cancelled?: boolean;
    filesDone?: number;
    filesTotal?: number;
    currentPath?: string;
  };
  let transfers = $state<ActiveTransfer[]>([]);
  const eventUnsubs: Array<() => void> = [];

  // Sort + show directories first within each direction.
  const sorted = $derived.by(() => {
    const dirs = entries.filter((e) => e.is_dir);
    const files = entries.filter((e) => !e.is_dir);
    const cmp = (a: SftpEntry, b: SftpEntry) => {
      let r = 0;
      switch (sortKey) {
        case "name":     r = a.name.localeCompare(b.name); break;
        case "size":     r = a.size - b.size; break;
        case "mod_time": r = a.mod_time - b.mod_time; break;
      }
      return sortDir === "asc" ? r : -r;
    };
    dirs.sort(cmp);
    files.sort(cmp);
    return [...dirs, ...files];
  });

  async function load(path: string) {
    loading = true;
    error = null;
    selected = new Set();
    try {
      const r = await api.sftpList(sessionId, path);
      cwd = r.path;
      entries = r.entries ?? [];
    } catch (e: any) {
      error = errMsg(e);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    load("");
    // Native OS file-drop listener. Wails forwards drops on any element
    // tagged data-file-drop-target back into Go where we re-emit a
    // 'file_drop' event with the resolved fs paths. Every SftpPane
    // shares the same global event bus, so we filter by the target's
    // data-sftp-session attribute to make sure only the pane that was
    // dropped on starts the upload.
    const un = EventsOn("file_drop", (payload: any) => {
      const targetSession = payload?.attrs?.["data-sftp-session"];
      if (targetSession !== sessionId) return;
      const files: string[] = payload?.filenames ?? [];
      if (files.length === 0) return;
      void onNativeDrop(files);
    });
    eventUnsubs.push(un);
  });
  onDestroy(() => { eventUnsubs.forEach((fn) => fn()); });

  function setSort(k: "name" | "size" | "mod_time") {
    if (sortKey === k) sortDir = sortDir === "asc" ? "desc" : "asc";
    else { sortKey = k; sortDir = "asc"; }
  }

  function toggleSelect(p: string, e: MouseEvent) {
    const next = new Set(selected);
    if (e.ctrlKey || e.metaKey) {
      if (next.has(p)) next.delete(p); else next.add(p);
    } else {
      next.clear();
      next.add(p);
    }
    selected = next;
  }

  function openEntry(entry: SftpEntry) {
    if (entry.is_dir) load(entry.path);
    // file open: future - for now nothing (download via button)
  }

  // Breadcrumbs from absolute cwd; click on a segment navigates there.
  const crumbs = $derived.by(() => {
    if (!cwd) return [] as Array<{ name: string; path: string }>;
    const parts = cwd.split("/").filter(Boolean);
    const out: Array<{ name: string; path: string }> = [{ name: "/", path: "/" }];
    let acc = "";
    for (const p of parts) {
      acc += "/" + p;
      out.push({ name: p, path: acc });
    }
    return out;
  });

  function parentDir(): string {
    if (!cwd || cwd === "/") return "/";
    const i = cwd.lastIndexOf("/");
    if (i <= 0) return "/";
    return cwd.slice(0, i);
  }

  // ---------- file ops ----------

  async function refresh() { load(cwd); }

  async function mkdir() {
    const name = await showPrompt("New folder name?");
    if (!name?.trim()) return;
    try {
      await api.sftpMkdir(sessionId, joinPath(cwd, name.trim()));
      await refresh();
    } catch (e: any) { error = errMsg(e); }
  }

  async function renameSelected() {
    const sel = [...selected];
    if (sel.length !== 1) return;
    const src = sel[0];
    const base = src.substring(src.lastIndexOf("/") + 1);
    const next = await showPrompt("Rename to?", base);
    if (!next || next === base) return;
    try {
      await api.sftpRename(sessionId, src, joinPath(cwd, next));
      await refresh();
    } catch (e: any) { error = errMsg(e); }
  }

  async function deleteSelected() {
    const sel = [...selected];
    if (sel.length === 0) return;
    const ok = await showConfirm({
      title: sel.length === 1 ? "Delete item" : "Delete items",
      message: `Delete ${sel.length} item${sel.length === 1 ? "" : "s"}?`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      for (const p of sel) await api.sftpRemove(sessionId, p);
      await refresh();
    } catch (e: any) { error = errMsg(e); }
  }

  function joinPath(dir: string, name: string): string {
    if (dir === "/" || dir === "") return "/" + name;
    return dir + "/" + name;
  }

  // ---------- transfers ----------

  function watchTransfer(transferId: string, direction: "up" | "down", name: string) {
    const t: ActiveTransfer = { id: transferId, direction, name, bytes: 0, total: 0 };
    transfers = [...transfers, t];
    const un = EventsOn(`sftp_progress:${transferId}`, (p: SftpTransferProgress) => {
      transfers = transfers.map((x) =>
        x.id === transferId
          ? {
              ...x,
              bytes: p.bytes,
              total: p.total,
              err: p.err,
              filesDone: p.files_done,
              filesTotal: p.files_total,
              currentPath: p.current_path,
            }
          : x
      );
      if (p.done) {
        un();
        if (direction === "up") refresh();
        // Auto-remove successful transfers after 4s.
        if (!p.err) {
          setTimeout(() => {
            transfers = transfers.filter((x) => x.id !== transferId);
          }, 4000);
        }
      }
    });
    eventUnsubs.push(un);
  }

  async function uploadFile() {
    let localPath: string;
    try {
      localPath = await api.sftpPickUploadSource();
    } catch (e: any) { error = errMsg(e); return; }
    if (!localPath) return;
    const name = localPath.replace(/\\/g, "/").split("/").pop() ?? "upload";
    const remotePath = joinPath(cwd, name);
    try {
      const id = await api.sftpStartUpload(sessionId, localPath, remotePath);
      watchTransfer(id, "up", name);
    } catch (e: any) { error = errMsg(e); }
  }

  // Handle native OS drag-and-drop. Wails delivers the resolved
  // filesystem paths (Windows: 'C:\\Users\\...\\file.txt', POSIX:
  // '/home/.../file.txt'). For each entry we ask the backend whether
  // it's a file or directory and route to the matching upload IPC.
  // Each item becomes its own transfer in the queue so progress is
  // visible per drop.
  async function onNativeDrop(paths: string[]) {
    for (const p of paths) {
      let isDir = false;
      try {
        isDir = await api.pathIsDir(p);
      } catch (e: any) {
        error = `${p}: ${e?.message ?? e}`;
        continue;
      }
      const name = p.replace(/\\/g, "/").replace(/\/$/, "").split("/").pop() ?? "drop";
      const remotePath = joinPath(cwd, name);
      try {
        if (isDir) {
          const id = await api.sftpStartUploadDir(sessionId, p, remotePath);
          watchTransfer(id, "up", name + "/");
        } else {
          const id = await api.sftpStartUpload(sessionId, p, remotePath);
          watchTransfer(id, "up", name);
        }
      } catch (e: any) {
        error = `${p}: ${e?.message ?? e}`;
      }
    }
  }

  async function uploadFolder() {
    let localPath: string;
    try {
      localPath = await api.sftpPickUploadDirSource();
    } catch (e: any) { error = errMsg(e); return; }
    if (!localPath) return;
    const name = localPath.replace(/\\/g, "/").replace(/\/$/, "").split("/").pop() ?? "upload";
    const remotePath = joinPath(cwd, name);
    try {
      const id = await api.sftpStartUploadDir(sessionId, localPath, remotePath);
      watchTransfer(id, "up", name + "/");
    } catch (e: any) { error = errMsg(e); }
  }

  async function downloadSelected() {
    const sel = [...selected];
    if (sel.length !== 1) return;
    const entry = entries.find((e) => e.path === sel[0]);
    if (!entry) return;
    if (entry.is_dir) {
      // Recursive directory download: ask for a parent dir locally,
      // mirror remote tree under <parent>/<entry name>.
      let parent: string;
      try { parent = await api.sftpPickDownloadDirDest(); }
      catch (e: any) { error = errMsg(e); return; }
      if (!parent) return;
      const localRoot = (parent.endsWith("/") || parent.endsWith("\\"))
        ? parent + entry.name
        : parent + (parent.includes("\\") ? "\\" : "/") + entry.name;
      try {
        const id = await api.sftpStartDownloadDir(sessionId, entry.path, localRoot);
        watchTransfer(id, "down", entry.name + "/");
      } catch (e: any) { error = errMsg(e); }
      return;
    }
    // Plain file path.
    let dest: string;
    try { dest = await api.sftpPickDownloadDest(entry.name); }
    catch (e: any) { error = errMsg(e); return; }
    if (!dest) return;
    try {
      const id = await api.sftpStartDownload(sessionId, entry.path, dest);
      watchTransfer(id, "down", entry.name);
    } catch (e: any) { error = errMsg(e); }
  }

  function cancelTransfer(id: string) {
    api.sftpCancelTransfer(id);
    transfers = transfers.map((x) => x.id === id ? { ...x, cancelled: true } : x);
  }

  // ---------- helpers ----------

  function fmtSize(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} K`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} M`;
    return `${(n / 1024 / 1024 / 1024).toFixed(1)} G`;
  }
  function fmtDate(unix: number): string {
    const d = new Date(unix * 1000);
    return d.toISOString().slice(0, 16).replace("T", " ");
  }
  function pct(t: ActiveTransfer): number {
    if (!t.total) return 0;
    return Math.min(100, Math.floor((t.bytes / t.total) * 100));
  }
</script>

<div class="sftp" data-file-drop-target="sftp-{sessionId}" data-sftp-session={sessionId} data-cwd={cwd || "/"}>
  <div class="toolbar">
    <button onclick={() => load(parentDir())} disabled={!cwd || cwd === "/"} title="Parent directory">↑</button>
    <button onclick={refresh} title="Refresh">↻</button>
    <div class="crumbs">
      {#each crumbs as c, i (c.path)}
        {#if i > 0}<span class="sep">/</span>{/if}
        <button class="crumb" onclick={() => load(c.path)}>{c.name}</button>
      {/each}
    </div>
    <div class="actions">
      <button onclick={uploadFile} title="Upload file">⬆ Upload</button>
      <button onclick={uploadFolder} title="Upload folder (recursive)">⬆ Folder</button>
      <button onclick={downloadSelected} disabled={selected.size !== 1} title="Download selected (folder = recursive)">⬇ Download</button>
      <button onclick={mkdir} title="New folder">＋ Folder</button>
      <button onclick={renameSelected} disabled={selected.size !== 1}>Rename</button>
      <button class="danger" onclick={deleteSelected} disabled={selected.size === 0}>Delete</button>
    </div>
  </div>

  {#if error}
    <div class="err">{error}</div>
  {/if}

  <div class="listing">
    <div class="head row">
      <button class="col name" onclick={() => setSort("name")}>Name {sortKey === "name" ? (sortDir === "asc" ? "▲" : "▼") : ""}</button>
      <button class="col size" onclick={() => setSort("size")}>Size {sortKey === "size" ? (sortDir === "asc" ? "▲" : "▼") : ""}</button>
      <button class="col date" onclick={() => setSort("mod_time")}>Modified {sortKey === "mod_time" ? (sortDir === "asc" ? "▲" : "▼") : ""}</button>
      <span class="col mode">Mode</span>
    </div>
    {#if loading && entries.length === 0}
      <div class="hint">Loading…</div>
    {:else if entries.length === 0}
      <div class="hint">Empty directory</div>
    {:else}
      {#each sorted as e (e.path)}
        <div
          class="row entry"
          class:selected={selected.has(e.path)}
          ondblclick={() => openEntry(e)}
          onclick={(ev) => toggleSelect(e.path, ev)}
          onkeydown={(ev) => {
            if (ev.key === "Enter") { ev.preventDefault(); openEntry(e); }
          }}
          role="button"
          tabindex="0"
        >
          <span class="col name">
            <span class="ico">
              {#if e.is_dir}<IconFolder size={13} />{:else if e.is_link}<IconLink size={13} />{:else}<IconFile size={13} />{/if}
            </span>
            <span class="nm">{e.name}</span>
            {#if e.is_link && e.target}<span class="link-tgt">→ {e.target}</span>{/if}
          </span>
          <span class="col size">{e.is_dir ? "" : fmtSize(e.size)}</span>
          <span class="col date">{fmtDate(e.mod_time)}</span>
          <span class="col mode">{e.mode_str}</span>
        </div>
      {/each}
    {/if}
  </div>

  {#if transfers.length > 0}
    <div class="transfers">
      {#each transfers as t (t.id)}
        {@const isDir = (t.filesTotal ?? 0) > 0}
        <div class="transfer" class:err={t.err}>
          <span class="dir">{t.direction === "up" ? "⬆" : "⬇"}</span>
          <span class="tname">
            {t.name}
            {#if isDir && t.currentPath}
              <span class="cur">- {t.currentPath}</span>
            {/if}
          </span>
          <div class="bar"><div class="fill" style="width: {pct(t)}%"></div></div>
          <span class="pct">
            {#if t.err}
              <span class="bad">{t.err}</span>
            {:else if t.cancelled}
              cancelled
            {:else if isDir}
              {t.filesDone}/{t.filesTotal} files · {fmtSize(t.bytes)}/{fmtSize(t.total)}
            {:else}
              {pct(t)}% ({fmtSize(t.bytes)}/{fmtSize(t.total)})
            {/if}
          </span>
          {#if !t.err && !t.cancelled && t.bytes < t.total}
            <button class="x" onclick={() => cancelTransfer(t.id)} title="Cancel">✕</button>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .sftp {
    /* Flex column instead of fixed grid template - the .err and
       .transfers rows are conditional, so a 4-row grid mis-aligns
       them when err is absent and transfers slides under the
       listing's overflow region (the original bug here). */
    display: flex;
    flex-direction: column;
    height: 100%;
    color: var(--text);
    background: var(--base);
    font-size: 0.82rem;
    min-height: 0;
    position: relative;
  }
  /* Wails toggles .file-drop-target-active on a data-file-drop-target
     element while a native OS drag hovers over it. We paint a visible
     drop overlay so the user knows they're about to upload here. */
  :global(.sftp.file-drop-target-active)::after {
    content: "Drop to upload to " attr(data-cwd);
    position: absolute; inset: 0;
    background: rgba(137, 180, 250, 0.12);
    border: 2px dashed var(--blue);
    color: var(--blue);
    display: flex;
    align-items: center;
    justify-content: center;
    font-weight: 600;
    font-size: 0.95rem;
    pointer-events: none;
    z-index: 50;
  }
  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.3rem 0.5rem;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
    flex-wrap: wrap;
  }
  .toolbar button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.2rem 0.5rem; cursor: pointer; font: inherit;
  }
  .toolbar button:disabled { opacity: 0.4; cursor: not-allowed; }
  .toolbar button:hover:not(:disabled) { background: var(--surface1); }
  .toolbar button.danger:hover { background: var(--red); color: var(--on-accent); }
  .crumbs {
    flex: 1; display: flex; align-items: center;
    overflow: hidden; min-width: 0; gap: 0.1rem;
    padding: 0 0.4rem;
  }
  .crumb {
    background: transparent !important;
    color: var(--blue) !important;
    padding: 0.1rem 0.25rem !important;
  }
  .crumb:hover { background: var(--surface0) !important; }
  .sep { color: var(--overlay0); }
  .actions { display: flex; gap: 0.25rem; }
  .err {
    background: var(--mantle); color: var(--red);
    border-left: 3px solid var(--red);
    padding: 0.3rem 0.6rem;
    font-size: 0.78rem;
  }
  .listing {
    overflow: auto;
    background: var(--base);
  }
  .row {
    display: grid;
    grid-template-columns: 1fr 80px 140px 110px;
    align-items: center;
    padding: 0.15rem 0.5rem;
    border-bottom: 1px solid var(--crust);
  }
  .row.entry { cursor: pointer; }
  .row.entry:hover { background: var(--surface0); }
  .row.entry.selected { background: var(--surface1); }
  .head {
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
    position: sticky; top: 0; z-index: 1;
  }
  .head button.col {
    background: transparent !important;
    color: var(--subtext0) !important;
    text-align: left !important;
    padding: 0.2rem 0 !important;
    border: 0;
    cursor: pointer;
    font: inherit;
  }
  .col.size, .col.date, .col.mode { color: var(--subtext0); }
  .name { display: flex; align-items: center; gap: 0.3rem; min-width: 0; overflow: hidden; }
  .nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .link-tgt { color: var(--overlay0); font-size: 0.72rem; }
  .ico { width: 1rem; text-align: center; }
  .hint { padding: 0.8rem; color: var(--overlay0); }

  .transfers {
    background: var(--crust);
    border-top: 1px solid var(--surface0);
    padding: 0.3rem 0.5rem;
    max-height: 30%;
    overflow-y: auto;
  }
  .transfer {
    display: grid;
    grid-template-columns: 1.2rem 1fr 100px 1fr auto;
    align-items: center;
    gap: 0.5rem;
    padding: 0.2rem 0;
    font-size: 0.78rem;
  }
  .transfer.err { color: var(--red); }
  .tname { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cur { color: var(--overlay0); font-size: 0.7rem; margin-left: 0.3rem; }
  .bar {
    background: var(--surface0);
    height: 6px;
    border-radius: 3px;
    overflow: hidden;
  }
  .fill {
    background: var(--blue);
    height: 100%;
    transition: width 0.15s linear;
  }
  .transfer.err .fill { background: var(--red); }
  .pct { color: var(--subtext0); }
  .bad { color: var(--red); }
  .x {
    background: transparent; border: 0; color: var(--red);
    cursor: pointer; padding: 0 0.3rem;
  }
</style>
