<!--
  Update-available modal. Opens when the user clicks the "vX.Y.Z
  available" pill in the status bar. Shows: current vs new version,
  rendered release notes, "Download" button (opens the binary URL
  in the system browser since we don't auto-update yet), and a
  "View all releases" link as fallback.

  Notes are fetched on mount from FetchReleaseNotes(version); a
  network failure surfaces the error in-place with a fallback to
  open the browser page.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import { errMsg as humanError } from "./connectErrors";
  import { api } from "./api";
  import { renderMarkdown } from "./markdown";
  import { updateCheck } from "./updateCheck.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { EventsOn } from "./wailsRuntime";

  interface Props {
    onClose: () => void;
  }
  let { onClose }: Props = $props();

  // One entry per version between the installed one and latest (newest first).
  let notes = $state<{ version: string; releasedAt: string; html: string }[]>([]);
  let loading = $state(true);
  let errMsg = $state<string>("");

  // In-app auto-update state: (1) Download streams the new binary
  // into the app's own directory while the backend verifies its
  // sha256 against the release manifest, (2) Restart and install
  // spawns the helper that swaps it in.
  let downloadBusy = $state(false);
  let downloadErr = $state<string>("");
  let staged = $state<{ staged_path: string; size: number; sha256: string; verified: boolean; apply_script?: string; needs_restart: boolean } | null>(null);

  // Download progress (bytes). total <= 0 = no Content-Length from
  // the server; render an indeterminate bar instead of a percentage.
  let progRead = $state(0);
  let progTotal = $state(0);
  const progPct = $derived(progTotal > 0 ? Math.min(100, Math.round((progRead / progTotal) * 100)) : 0);

  function fmtMB(bytes: number): string {
    return `${Math.round((bytes / 1024 / 1024) * 10) / 10} MB`;
  }

  onMount(async () => {
    try {
      // Show every version between what's installed and the latest, since the
      // download jumps straight to the newest.
      const list = await api.fetchReleaseNotesRange(updateCheck.current, updateCheck.latest);
      const good = (list ?? []).filter((r) => !r.error && (r.notes_md ?? "").trim() !== "");
      if (good.length === 0) {
        const only = (list ?? [])[0];
        errMsg = only?.error || "No release notes available.";
      } else {
        notes = good.map((r) => ({
          version: r.version,
          releasedAt: r.released_at ?? "",
          html: renderMarkdown(r.notes_md ?? ""),
        }));
      }
    } catch (e: any) {
      errMsg = humanError(e);
    } finally {
      loading = false;
    }
  });

  async function startDownload() {
    if (!updateCheck.downloadURL || downloadBusy) return;
    downloadBusy = true;
    downloadErr = "";
    progRead = 0;
    progTotal = 0;
    const unProg = EventsOn("update_download_progress", (p: { read: number; total: number }) => {
      progRead = p.read;
      progTotal = p.total;
    });
    try {
      staged = await api.downloadUpdate();
    } catch (e: any) {
      downloadErr = humanError(e);
    } finally {
      unProg();
      downloadBusy = false;
    }
  }

  async function applyUpdate() {
    if (!staged) return;
    const ok = await showConfirm({
      title: "Restart and install update",
      message: "The app will close and relaunch on the new version. Continue?",
      okLabel: "Restart",
    });
    if (!ok) return;
    try {
      await api.applyUpdate();
    } catch (e: any) {
      downloadErr = humanError(e);
    }
  }

  function openReleasesPage() {
    if (updateCheck.changelogURL) {
      api.openURL(updateCheck.changelogURL).catch(console.warn);
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") onClose();
  }

  function fmtDate(s: string): string {
    if (!s) return "";
    const d = new Date(s);
    if (isNaN(d.getTime())) return s;
    return d.toLocaleDateString();
  }
</script>

<svelte:window onkeydown={onKey} />

<div
  class="backdrop"
  role="button"
  tabindex="-1"
  onclick={onClose}
  onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") onClose(); }}
></div>
<div class="modal" role="dialog" aria-labelledby="update-title">
  <header>
    <div class="title-row">
      <h2 id="update-title">Update available</h2>
      <button class="x" onclick={onClose} aria-label="Close">×</button>
    </div>
    <div class="version-row">
      <span class="chip current">{updateCheck.current}</span>
      <span class="arrow">→</span>
      <span class="chip new">{updateCheck.latest}</span>
      {#if notes.length > 1}
        <span class="released">{notes.length} versions</span>
      {/if}
    </div>
  </header>

  <div class="body">
    {#if loading}
      <p class="hint">Loading release notes…</p>
    {:else if errMsg}
      <p class="err">Couldn't load release notes: {errMsg}</p>
      <p class="hint">
        You can still view the full changelog on the
        <button class="linkish" onclick={openReleasesPage}>releases page</button>.
      </p>
    {:else if notes.length > 0}
      {#each notes as n (n.version)}
        <section class="version-notes">
          <div class="version-head">
            <span class="v-tag">{n.version}</span>
            {#if n.releasedAt}<span class="released">released {fmtDate(n.releasedAt)}</span>{/if}
          </div>
          <div class="notes">{@html n.html}</div>
        </section>
      {/each}
    {:else}
      <p class="hint">No release notes were posted for this version.</p>
    {/if}
  </div>

  {#if downloadErr}
    <div class="staged err">Update failed: {downloadErr}</div>
  {/if}
  {#if downloadBusy}
    <div class="staged progress-row">
      <div class="bar" class:indeterminate={progTotal <= 0}>
        <div class="fill" style={progTotal > 0 ? `width: ${progPct}%` : ""}></div>
      </div>
      <span class="bar-label">
        {#if progTotal > 0}
          {fmtMB(progRead)} / {fmtMB(progTotal)} ({progPct}%)
        {:else}
          {fmtMB(progRead)} downloaded…
        {/if}
      </span>
    </div>
  {/if}
  {#if staged}
    <div class="staged ok">
      Staged at <code>{staged.staged_path}</code>
      ({fmtMB(staged.size)}) ·
      sha256 <code>{staged.sha256.slice(0, 16)}…</code>
      {#if staged.verified}
        <span class="verified">checksum verified</span>
      {:else}
        <span class="unverified">checksum not verified (manifest carried no hash)</span>
      {/if}
    </div>
  {/if}

  <footer>
    <button class="secondary" onclick={openReleasesPage}>View all releases</button>
    <div class="spacer"></div>
    <button class="secondary" onclick={onClose}>Later</button>
    {#if !staged}
      <button
        class="primary"
        onclick={startDownload}
        disabled={!updateCheck.downloadURL || downloadBusy}
      >
        {downloadBusy
          ? "Downloading…"
          : `Download ${updateCheck.latest}${updateCheck.downloadSize > 0 ? ` (${fmtMB(updateCheck.downloadSize)})` : ""}`}
      </button>
    {:else}
      <button class="primary" onclick={applyUpdate}>
        Restart and install
      </button>
    {/if}
  </footer>
</div>

<style>
  .backdrop {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.5);
    z-index: 9000;
  }
  .modal {
    position: fixed;
    top: 50%; left: 50%;
    transform: translate(-50%, -50%);
    width: min(720px, 92vw);
    max-height: 80vh;
    display: flex; flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    z-index: 9001;
    overflow: hidden;
  }
  header {
    padding: 0.75rem 1rem 0.5rem;
    border-bottom: 1px solid var(--surface0);
    background: var(--mantle);
  }
  .title-row {
    display: flex; align-items: center; justify-content: space-between;
  }
  h2 { font-size: 1rem; margin: 0; color: var(--text); }
  .x {
    background: transparent; border: 0; color: var(--subtext0);
    font-size: 1.4rem; line-height: 1; cursor: pointer;
    padding: 0 0.3rem; border-radius: 3px;
  }
  .x:hover { background: var(--surface0); color: var(--text); }
  .version-row {
    margin-top: 0.4rem;
    display: flex; align-items: center; gap: 0.5rem;
    font-size: 0.8rem;
    color: var(--subtext0);
  }
  .chip {
    font-family: ui-monospace, 'JetBrains Mono', Menlo, monospace;
    font-size: 0.78rem;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    background: var(--surface0);
    color: var(--text);
  }
  .chip.new { background: var(--blue); color: var(--on-accent); }
  .arrow { color: var(--overlay0); }
  .released { color: var(--overlay0); margin-left: 0.4rem; }
  .version-notes { margin-bottom: 1rem; }
  .version-notes + .version-notes { border-top: 1px solid var(--surface0); padding-top: 0.75rem; }
  .version-head { display: flex; align-items: baseline; gap: 0.4rem; margin-bottom: 0.3rem; }
  .v-tag {
    font-weight: 700; color: var(--blue); font-family: ui-monospace, monospace;
    font-size: 0.9rem;
  }
  .body {
    padding: 0.75rem 1rem;
    overflow-y: auto;
    flex: 1;
    min-height: 0;
    color: var(--text);
    font-size: 0.85rem;
    line-height: 1.55;
  }
  .body :global(h1),
  .body :global(h2),
  .body :global(h3) { color: var(--text); margin-top: 1rem; }
  .body :global(h2) { font-size: 0.95rem; }
  .body :global(h3) { font-size: 0.88rem; }
  .body :global(ul) { padding-left: 1.2rem; }
  .body :global(li) { margin: 0.2rem 0; }
  .body :global(code) {
    background: var(--mantle);
    padding: 0.05rem 0.3rem;
    border-radius: 3px;
    font-size: 0.82rem;
  }
  .body :global(a) { color: var(--blue); }
  .body :global(strong) { color: var(--text); }
  .hint { color: var(--subtext0); }
  .err { color: var(--red); }
  .linkish {
    background: transparent; border: 0; color: var(--blue);
    text-decoration: underline; cursor: pointer; padding: 0; font: inherit;
  }
  footer {
    padding: 0.6rem 1rem;
    border-top: 1px solid var(--surface0);
    background: var(--mantle);
    display: flex; align-items: center; gap: 0.5rem;
  }
  .spacer { flex: 1; }
  footer button {
    font: inherit; font-size: 0.82rem;
    padding: 0.35rem 0.7rem;
    border-radius: 4px;
    cursor: pointer;
    border: 1px solid var(--surface0);
  }
  .secondary { background: var(--surface0); color: var(--text); }
  .secondary:hover { background: var(--surface1); }
  .primary { background: var(--blue); color: var(--on-accent); border-color: var(--blue); }
  .primary:hover { background: #74a8ee; }
  .primary:disabled { background: var(--surface1); color: var(--overlay0); border-color: var(--surface1); cursor: not-allowed; }
  .staged {
    padding: 0.5rem 1rem;
    font-size: 0.78rem;
    border-top: 1px solid var(--surface0);
    background: var(--mantle);
    color: var(--text-muted);
  }
  .staged code {
    background: var(--base);
    padding: 0.05rem 0.3rem;
    border-radius: 3px;
    font-size: 0.72rem;
  }
  .staged.err { color: var(--red); }
  .staged.ok { color: var(--green); }
  .verified { color: var(--green); margin-left: 0.4rem; }
  .unverified { color: var(--yellow); margin-left: 0.4rem; }
  .progress-row {
    display: flex; align-items: center; gap: 0.6rem;
  }
  .bar {
    flex: 1;
    height: 6px;
    border-radius: 3px;
    background: var(--surface0);
    overflow: hidden;
    position: relative;
  }
  .bar .fill {
    height: 100%;
    background: var(--blue);
    border-radius: 3px;
    transition: width 0.15s linear;
  }
  .bar.indeterminate .fill {
    position: absolute;
    width: 30%;
    animation: slide 1.2s ease-in-out infinite;
  }
  @keyframes slide {
    0% { left: -30%; }
    100% { left: 100%; }
  }
  .bar-label {
    color: var(--subtext0);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
</style>
