<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api, type SftpEntry, type SftpTransferProgress } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import { IconFolder, IconFile, IconLink } from "./iconMap";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import SftpViewModal from "./SftpViewModal.svelte";
  import SftpFileView from "./SftpFileView.svelte";
  import { sessionCwd } from "./sessionCwd.svelte";
  import { toast } from "./toast.svelte";
  import { focusSessionTerminal } from "./paneFocus";

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

  // SFTP v3 carries numeric ids only; the backend resolves them against the
  // host's /etc/passwd and /etc/group. Names are used when available and the
  // id is the fallback, so an LDAP account still renders as a number rather
  // than as blank. Servers that omit the attrs send -1, shown as a dash.
  function fmtOwner(e: SftpEntry): string {
    if (e.uid < 0 && e.gid < 0) return "-";
    const u = e.owner || (e.uid < 0 ? "?" : String(e.uid));
    const g = e.group || (e.gid < 0 ? "?" : String(e.gid));
    return `${u}:${g}`;
  }

  // The tooltip always shows the raw ids - the name can be ambiguous across
  // hosts, the number is what the filesystem actually stores.
  function ownerTitle(e: SftpEntry): string {
    if (e.uid < 0 && e.gid < 0) return "Owner not reported by the server";
    const u = e.uid < 0 ? "unknown" : String(e.uid);
    const g = e.gid < 0 ? "unknown" : String(e.gid);
    return `uid ${u}, gid ${g}`;
  }

  function openEntry(entry: SftpEntry) {
    if (entry.is_dir) {
      load(entry.path);
      return;
    }
    // Files open in the read-only quick view. A symlink to a directory
    // still lands here (we only know the target string, not its type),
    // in which case the preview reports the read error - acceptable, and
    // cheaper than a stat round-trip on every double click.
    openView(entry);
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

  // Follow the shell's directory. Opt-in per pane: a listing that jumps
  // around while you are working in it is worse than one that stays put.
  // The shell reports its directory via OSC 7 (see sessionCwd); shells that
  // do not emit it leave shellCwd empty and the toggle disabled.
  const shellCwd = $derived(sessionCwd.get(sessionId));
  const following = $derived(sessionCwd.isFollowing(sessionId));

  $effect(() => {
    if (!following) return;
    const target = shellCwd;
    // untrack the comparison: this effect must re-run when the shell moves,
    // not when our own load() writes cwd back.
    if (target && target !== untrack(() => cwd)) load(target);
  });

  // Send the shell to the directory being browsed.
  //
  // Ctrl+U first: without it a second click appends to whatever the first
  // one typed, giving `cd '/x'cd '/x'`. Ctrl+U is the readline "kill line"
  // binding, so it clears the input the shell is holding - including a
  // half-typed command of the user's, which is the intended trade: the line
  // is visibly replaced rather than silently concatenated.
  //
  // No trailing newline on purpose: pressing Enter for the user could run
  // something they did not intend. They see `cd '...'` and confirm it.
  // Focus moves to the terminal so that Enter actually reaches the shell -
  // otherwise it lands in this pane and nothing happens.
  async function cdHere() {
    if (!cwd) return;
    // Single-quote the path and escape any embedded quote the POSIX way -
    // a directory name can legally contain almost anything.
    const quoted = `\u0015cd '${cwd.replace(/'/g, `'\\''`)}'`;
    try {
      const b64 = btoa(String.fromCharCode(...new TextEncoder().encode(quoted)));
      await api.sshWrite(sessionId, b64);
      if (!focusSessionTerminal(sessionId)) {
        toast.push("ok", "Typed into the terminal - press Enter to run it");
      }
    } catch (e) {
      toast.push("err", errMsg(e));
    }
  }

  function toggleFollow() {
    const next = !following;
    sessionCwd.setFollowing(sessionId, next);
    // Turning it on should act immediately rather than waiting for the next
    // prompt - the user just asked to be where the shell is.
    if (next && shellCwd && shellCwd !== cwd) load(shellCwd);
  }

  // Quick view of a remote text file. Directories and symlinks are skipped
  // (a link's target may well be a directory), so only regular files open.
  //
  // The view docks inside this pane by default rather than opening a modal.
  // SFTP opens as a split beside the terminal, so a modal covered the very
  // shell the user was reading the file for; docked, a README or a config
  // stays on screen while they type next door. `expanded` sends the same
  // file to the modal for the cases where the split is simply too short.
  let viewing = $state<{ path: string; name: string } | null>(null);
  let expanded = $state(false);

  // Height of the docked view, as a share of the pane. Kept in a ratio
  // rather than pixels so it survives a pane resize.
  const VIEW_MIN = 0.15;
  const VIEW_MAX = 0.85;
  let viewFrac = $state(0.5);
  let paneEl = $state<HTMLDivElement | null>(null);

  function openView(entry: SftpEntry) {
    if (entry.is_dir) return;
    viewing = { path: entry.path, name: entry.name };
    expanded = false;
  }

  // Drag the splitter between the listing and the docked view. Pointer
  // capture keeps the drag alive over the iframe-less webview even when the
  // cursor leaves the splitter, and avoids the mouseup-outside case that
  // would otherwise strand us mid-drag.
  function startResize(e: PointerEvent) {
    if (!paneEl) return;
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    const rect = paneEl.getBoundingClientRect();
    const onMove = (ev: PointerEvent) => {
      const frac = (rect.bottom - ev.clientY) / rect.height;
      viewFrac = Math.min(VIEW_MAX, Math.max(VIEW_MIN, frac));
    };
    const onUp = (ev: PointerEvent) => {
      (e.currentTarget as HTMLElement).releasePointerCapture(ev.pointerId);
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  }

  function viewSelected() {
    if (selected.size !== 1) return;
    const e = entries.find((x) => x.path === [...selected][0]);
    if (e) openView(e);
  }

  // Path bar: the crumbs double as an editable field so a path can be
  // pasted instead of clicked through. Clicking the empty strip (or the
  // pencil) swaps in an input seeded with cwd; Enter navigates, Escape
  // restores the crumbs. Kept as one control rather than a permanent
  // second row so the toolbar height doesn't change.
  let editingPath = $state(false);
  let pathDraft = $state("");
  let pathInput = $state<HTMLInputElement | null>(null);

  function startEditPath() {
    pathDraft = cwd || "/";
    editingPath = true;
  }

  // Bind:this lands before the element is in the DOM on the same tick,
  // so focus/select waits for the effect that runs after it is attached.
  $effect(() => {
    if (editingPath && pathInput) {
      pathInput.focus();
      pathInput.select();
    }
  });

  function commitPath() {
    const next = pathDraft.trim();
    editingPath = false;
    if (!next || next === cwd) return;
    // The backend expands ~ / ~/sub and the SFTP server resolves relative
    // paths against the login CWD; load() reports back whatever it landed
    // on, so cwd and the crumbs stay canonical.
    load(next);
  }

  function onPathKey(ev: KeyboardEvent) {
    if (ev.key === "Enter") {
      ev.preventDefault();
      commitPath();
    } else if (ev.key === "Escape") {
      ev.preventDefault();
      editingPath = false;
    }
  }

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

<div
  class="sftp"
  bind:this={paneEl}
  data-file-drop-target="sftp-{sessionId}"
  data-sftp-session={sessionId}
  data-cwd={cwd || "/"}
>
  <div class="toolbar">
    <button onclick={() => load(parentDir())} disabled={!cwd || cwd === "/"} title="Parent directory">↑</button>
    <button onclick={refresh} title="Refresh">↻</button>
    <button
      class:active={following}
      onclick={toggleFollow}
      disabled={!shellCwd && !following}
      title={shellCwd
        ? (following ? `Following the shell (${shellCwd}) - click to stop` : `Follow the shell's directory (${shellCwd})`)
        : "The shell hasn't reported its directory - its prompt has to emit OSC 7. See the user guide (Follow the shell) for the one-liner."}
      aria-pressed={following}
    >⇄</button>
    <button
      onclick={cdHere}
      disabled={!cwd}
      title="Type 'cd <this directory>' into the terminal (you press Enter)"
    >cd here</button>
    {#if editingPath}
      <input
        class="path-input"
        bind:this={pathInput}
        bind:value={pathDraft}
        onkeydown={onPathKey}
        onblur={() => (editingPath = false)}
        spellcheck="false"
        autocomplete="off"
        placeholder="/path/to/dir"
      />
    {:else}
      <div class="crumbs">
        {#each crumbs as c, i (c.path)}
          {#if i > 0}<span class="sep">/</span>{/if}
          <button class="crumb" onclick={() => load(c.path)}>{c.name}</button>
        {/each}
        <button
          class="path-edit"
          onclick={startEditPath}
          title="Edit path (paste a path and press Enter)"
          aria-label="Edit path"
        >✎</button>
        <button
          class="path-gap"
          onclick={startEditPath}
          tabindex="-1"
          aria-hidden="true"
        ></button>
      </div>
    {/if}
    <div class="actions">
      <button onclick={uploadFile} title="Upload file">⬆ Upload</button>
      <button onclick={uploadFolder} title="Upload folder (recursive)">⬆ Folder</button>
      <button onclick={viewSelected} disabled={selected.size !== 1} title="Quick view (Enter / double click)">View</button>
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
      <span class="col owner">Owner</span>
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
          <span class="col owner" title={ownerTitle(e)}>{fmtOwner(e)}</span>
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

  {#if viewing && !expanded}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="vsplit"
      role="separator"
      aria-orientation="horizontal"
      aria-label="Resize file preview"
      onpointerdown={startResize}
      ondblclick={() => (viewFrac = 0.5)}
      title="Drag to resize, double-click to reset"
    ></div>
    <div class="viewdock" style="height: {(viewFrac * 100).toFixed(1)}%">
      <!-- Keyed on the path: the view loads its file on mount, so swapping
           to another file has to build a new instance, not reuse this one. -->
      {#key viewing.path}
        <SftpFileView
          {sessionId}
          path={viewing.path}
          name={viewing.name}
          chrome="inline"
          onClose={() => (viewing = null)}
          onExpand={() => (expanded = true)}
        />
      {/key}
    </div>
  {/if}
</div>

{#if viewing && expanded}
  {#key viewing.path}
    <SftpViewModal
      {sessionId}
      path={viewing.path}
      name={viewing.name}
      onClose={() => { viewing = null; expanded = false; }}
    />
  {/key}
{/if}

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
  /* Pencil sits right after the last crumb; the gap button is the rest of
     the strip, so clicking the empty space also opens the path editor. */
  .path-edit {
    background: transparent !important;
    color: var(--overlay1) !important;
    padding: 0.1rem 0.25rem !important;
    margin-left: 0.15rem;
    flex: 0 0 auto;
  }
  .toolbar button.active {
    background: var(--surface1) !important;
    color: var(--blue) !important;
  }
  .path-edit:hover {
    background: var(--surface0) !important;
    color: var(--text) !important;
  }
  .path-gap {
    flex: 1 1 auto;
    min-width: 1rem;
    align-self: stretch;
    background: transparent !important;
    border: none !important;
    padding: 0 !important;
    cursor: text;
  }
  .path-input {
    flex: 1; min-width: 0;
    margin: 0 0.4rem;
    padding: 0.15rem 0.4rem;
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--blue);
    border-radius: 3px;
    font-family: inherit;
    font-size: inherit;
  }
  .path-input:focus { outline: none; }
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
    /* flex:1 with min-height:0 so the docked preview below can claim its
       share instead of pushing the listing past the pane. */
    flex: 1;
    min-height: 0;
  }
  /* Docked file preview. Height is driven by the splitter; flex-shrink:0
     keeps it from collapsing when the listing is long. */
  .viewdock {
    flex: 0 0 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .vsplit {
    flex: 0 0 auto;
    height: 5px;
    cursor: row-resize;
    background: var(--surface0);
  }
  .vsplit:hover { background: var(--blue); }
  .row {
    display: grid;
    grid-template-columns: 1fr 80px 140px 90px 110px;
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
  .col.size, .col.date, .col.owner, .col.mode { color: var(--subtext0); }
  .col.owner { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
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
