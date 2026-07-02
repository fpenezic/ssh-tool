<script lang="ts">
  // Recordings browser: list of .cast files in the recordings folder
  // with an embedded player. Opened from the quick palette or
  // Settings; state lives in recordingsModal so any call site can
  // also deep-link straight into playback of one file.
  import { api, type RecordingFileInfo } from "./api";
  import { recordingsModal } from "./recording.svelte";
  import RecordingPlayer from "./RecordingPlayer.svelte";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte";
  import { errMsg } from "./connectErrors";
  import { focusActiveTerminal } from "./terminalFocus";

  let files = $state<RecordingFileInfo[]>([]);
  let listError = $state<string | null>(null);
  let playingPath = $state<string | null>(null);
  let modalEl = $state<HTMLDivElement | null>(null);

  async function refresh() {
    try {
      files = (await api.recordingsList()) ?? [];
      listError = null;
    } catch (e) {
      listError = errMsg(e);
    }
  }

  // Reload the list every time the modal opens; honour a deep-link
  // into one file's playback. Focus must move INTO the modal: the
  // overlay blocks clicks, but keyboard focus would otherwise stay
  // in a live background terminal's textarea and every keystroke
  // would land in that SSH session.
  $effect(() => {
    if (!recordingsModal.isOpen) return;
    playingPath = recordingsModal.initialPath;
    refresh();
    setTimeout(() => modalEl?.focus(), 0);
  });

  function onKey(e: KeyboardEvent) {
    if (e.key !== "Escape") return;
    e.preventDefault();
    if (playingPath) {
      playingPath = null;
      fullscreen = false;
      setTimeout(() => modalEl?.focus(), 0);
    } else {
      close();
    }
  }

  function fmtSize(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
  }

  function fmtDuration(s: number): string {
    const m = Math.floor(s / 60);
    const ss = Math.floor(s % 60);
    return `${m}:${ss.toString().padStart(2, "0")}`;
  }

  function fmtWhen(unix: number): string {
    return new Date(unix * 1000).toLocaleString();
  }

  async function remove(f: RecordingFileInfo) {
    const ok = await showConfirm({
      title: "Delete recording",
      message: `Delete ${f.name}? The file is removed from disk.`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.recordingDelete(f.path);
      await refresh();
    } catch (e) {
      toast.err(errMsg(e));
    }
  }

  let fullscreen = $state(false);

  function close() {
    playingPath = null;
    fullscreen = false;
    recordingsModal.close();
    // Hand focus back to the live terminal the user was in.
    focusActiveTerminal();
  }
</script>

{#if recordingsModal.isOpen}
  <div class="overlay" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) close(); }}>
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div
      class="modal"
      class:wide={!!playingPath}
      class:full={fullscreen && !!playingPath}
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      bind:this={modalEl}
      onkeydown={onKey}
    >
      <header>
        <h2>
          {#if playingPath}
            <button class="back" onclick={() => { playingPath = null; fullscreen = false; }} title="Back to list">←</button>
            {files.find((f) => f.path === playingPath)?.title || files.find((f) => f.path === playingPath)?.name || "Playback"}
          {:else}
            Session recordings
          {/if}
        </h2>
        <div class="header-actions">
          {#if playingPath}
            <button
              class="close"
              onclick={() => (fullscreen = !fullscreen)}
              title={fullscreen ? "Exit full screen" : "Full screen"}
            >⛶</button>
          {/if}
          <button class="close" onclick={close} title="Close">✕</button>
        </div>
      </header>

      {#if playingPath}
        {#key playingPath}
          <RecordingPlayer path={playingPath} />
        {/key}
      {:else}
        {#if listError}
          <p class="error">{listError}</p>
        {:else if files.length === 0}
          <p class="empty">
            No recordings yet. Right-click a terminal tab and pick
            <strong>Record session</strong>.
          </p>
        {:else}
          <div class="list">
            <table>
              <thead>
                <tr>
                  <th>Recording</th>
                  <th>When</th>
                  <th class="num">Duration</th>
                  <th class="num">Size</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {#each files as f (f.path)}
                  <tr>
                    <td class="name" title={f.path}>
                      {f.title || f.name}
                      <span class="dim">{f.width}x{f.height}</span>
                    </td>
                    <td>{fmtWhen(f.mod_time)}</td>
                    <td class="num">{fmtDuration(f.duration)}</td>
                    <td class="num">{fmtSize(f.size)}</td>
                    <td class="actions">
                      <button onclick={() => (playingPath = f.path)}>Play</button>
                      <button class="danger" onclick={() => remove(f)}>Delete</button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        <footer>
          <button onclick={() => api.recordingsOpenDir().catch((e) => toast.err(errMsg(e)))}>
            Open folder
          </button>
          <button onclick={refresh}>Refresh</button>
        </footer>
      {/if}
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(17, 17, 27, 0.6);
    z-index: 1000;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .modal {
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    width: min(760px, 92vw);
    max-height: 86vh;
    display: flex;
    flex-direction: column;
    padding: 1rem 1.2rem;
    box-shadow: 0 8px 30px rgba(0,0,0,0.5);
  }
  /* Playback wants room for the recorded grid. */
  .modal.wide {
    width: min(1100px, 94vw);
    height: 86vh;
  }
  /* Full screen: take the whole window, the player's fit-to-window
     font scaling does the rest. */
  .modal.full {
    width: 100vw;
    height: 100vh;
    max-height: 100vh;
    border: none;
    border-radius: 0;
  }
  .header-actions {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    flex-shrink: 0;
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.6rem;
    flex-shrink: 0;
  }
  header h2 {
    margin: 0;
    font-size: 1rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .back {
    padding: 0.1rem 0.45rem;
  }
  .close {
    background: none;
    border: none;
    color: var(--subtext0);
    cursor: pointer;
    font-size: 0.95rem;
  }
  .close:hover { color: var(--text); }
  .list {
    overflow-y: auto;
    min-height: 0;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85rem;
  }
  th {
    text-align: left;
    color: var(--subtext0);
    font-weight: 600;
    padding: 0.3rem 0.5rem;
    border-bottom: 1px solid var(--surface0);
    position: sticky;
    top: 0;
    background: var(--base);
  }
  td {
    padding: 0.35rem 0.5rem;
    border-bottom: 1px solid var(--surface0);
    vertical-align: middle;
  }
  .num { text-align: right; font-variant-numeric: tabular-nums; }
  .name .dim {
    color: var(--subtext0);
    font-size: 0.75rem;
    margin-left: 0.4rem;
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .actions button {
    margin-left: 0.3rem;
    padding: 0.15rem 0.55rem;
  }
  .danger { color: var(--red); }
  .empty, .error {
    color: var(--subtext0);
    padding: 1rem 0;
  }
  .error { color: var(--red); }
  footer {
    display: flex;
    gap: 0.5rem;
    justify-content: flex-end;
    margin-top: 0.7rem;
    flex-shrink: 0;
  }
</style>
