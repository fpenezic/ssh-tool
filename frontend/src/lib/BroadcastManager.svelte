<script lang="ts">
  // Modal panel that lists every live session in the backend pool and
  // lets the user tick/untick which ones are in the broadcast group.
  // Uses sshActiveSessions() so the picker shows sessions owned by
  // other windows too - broadcast crosses window boundaries.

  import { broadcast } from "./broadcast.svelte";
  import { sessions } from "./stores.svelte";
  import { api } from "./api";
  import { IconBroadcast, IconX } from "./iconMap";
  import { clickOutside } from "./clickOutside";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";

  type Props = {
    open: boolean;
    onClose: () => void;
  };

  let { open, onClose }: Props = $props();

  type LiveSession = { session_id: string; name: string; kind: string | undefined };

  // Pull from the per-window sessions store directly so local PTYs
  // show up alongside SSH sessions. The previous version called
  // SshActiveSessions which only enumerates the SSH pool - local
  // shells were invisible to the broadcast picker even though the
  // backend BroadcastFanOut now writes to both pools.
  //
  // Cross-window membership still works because broadcast.members
  // is shared backend state (the membership set is global, only
  // the picker UI is per-window).
  const allLiveSessions = $derived<LiveSession[]>(
    sessions.tabs
      .filter((t) => t.status === "connected")
      .map((t) => ({
        session_id: t.sessionId,
        name: t.name || t.hostname || t.sessionId,
        kind: t.kind,
      }))
  );

  // When showOtherGroupMembers is off, hide sessions that already
  // sit in a different group - keeps the picker for the active
  // group focused on candidates instead of every live session.
  // Sessions already in the active group are always shown so the
  // user can toggle them off.
  let showOtherGroupMembers = $state(false);
  const liveSessions = $derived<LiveSession[]>(
    showOtherGroupMembers
      ? allLiveSessions
      : allLiveSessions.filter((s) => {
          if (activeMembers.has(s.session_id)) return true;
          // Hide if in any other group.
          const others = broadcast.groupsOf(s.session_id).filter((g) => g !== activeGroup);
          return others.length === 0;
        })
  );

  // Active group in the manager. "" = legacy default group.
  let activeGroup = $state("");
  // Touch groupsVersion so the derived re-runs when the same key is
  // mutated (Svelte deep tracking misses Set-replace inside Record).
  const activeMembers = $derived.by(() => {
    void broadcast.groupsVersion;
    return broadcast.groups[activeGroup] ?? new Set<string>();
  });

  async function selectAll() {
    // Operate only on what the user sees. With the 'Show sessions
    // in other groups' filter off, that's just the candidate set
    // for this group - clicking Select all should not silently
    // pull in 50 sessions hidden behind the filter.
    try {
      await api.broadcastSetAllInGroup(activeGroup, liveSessions.map((s) => s.session_id));
    } catch (e) { console.warn("select all:", e); }
  }
  async function selectNone() {
    try { await api.broadcastClearGroup(activeGroup); }
    catch (e) { console.warn("select none:", e); }
  }
  async function invert() {
    const cur = activeMembers;
    try {
      await api.broadcastSetAllInGroup(
        activeGroup,
        liveSessions.map((s) => s.session_id).filter((id) => !cur.has(id)),
      );
    } catch (e) { console.warn("invert:", e); }
  }
  async function toggleInActive(sessionId: string) {
    await broadcast.toggleIn(activeGroup, sessionId);
  }
  async function addGroup() {
    const name = await showPrompt("New broadcast group name:");
    if (!name) return;
    const trimmed = name.trim();
    if (!trimmed) return;
    if (broadcast.groups[trimmed]) {
      activeGroup = trimmed;
      return;
    }
    // Adding any session creates the group; until the user picks one
    // we don't make a backend call, just switch the picker to a
    // placeholder so the next checkbox lands in the new group.
    activeGroup = trimmed;
    // Create empty group on the backend so it shows up in the
    // dropdown for other windows too.
    void api.broadcastSetAllInGroup(trimmed, []);
  }
  async function deleteGroup() {
    if (!activeGroup) return; // default group can't be deleted
    const ok = await showConfirm({
      title: "Delete broadcast group",
      message: `Remove group "${activeGroup}"? Members keep running; they're just no longer in this group.`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    await broadcast.deleteGroup(activeGroup);
    activeGroup = "";
  }
</script>

{#if open}
  <div class="overlay" role="presentation">
    <div
      class="modal"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      use:clickOutside={{ onOutside: onClose }}
      onkeydown={(e) => { if (e.key === "Escape") onClose(); }}
    >
      <header>
        <h2>
          <IconBroadcast size={16} />
          Broadcast manager
        </h2>
        <button class="close" onclick={onClose} title="Close (Esc)">
          <IconX size={14} />
        </button>
      </header>
      <p class="hint">
        Keystrokes typed in any selected session fan out to every
        other session in the same group. A session in two groups
        broadcasts to the union. Output stays per-session.
      </p>
      <div class="group-row">
        <label class="group-pick">
          <span>Group</span>
          <select bind:value={activeGroup}>
            {#each broadcast.groupNames() as g (g)}
              <option value={g}>
                {g === "" ? "Default" : g} ({broadcast.groupSize(g)})
              </option>
            {/each}
          </select>
        </label>
        <button onclick={addGroup}>+ New group</button>
        {#if activeGroup}
          <button class="danger" onclick={deleteGroup}>Delete group</button>
        {/if}
      </div>
      <div class="bulk">
        <button onclick={selectAll}>Select all</button>
        <button onclick={selectNone}>Select none</button>
        <button onclick={invert}>Invert</button>
        <label class="show-other">
          <input type="checkbox" bind:checked={showOtherGroupMembers} />
          <span>Show sessions in other groups</span>
        </label>
        <span class="count">
          {activeMembers.size} of {allLiveSessions.length} selected
          {#if activeGroup}in &ldquo;{activeGroup}&rdquo;{:else}in default group{/if}
        </span>
      </div>
      {#if liveSessions.length === 0}
        <div class="empty">
          No live sessions yet. Connect to something first.
        </div>
      {:else}
        <ul class="sessions">
          {#each liveSessions as s (s.session_id)}
            {@const checked = activeMembers.has(s.session_id)}
            {@const otherGroups = broadcast.groupsOf(s.session_id).filter((g) => g !== activeGroup)}
            <li>
              <label>
                <input
                  type="checkbox"
                  {checked}
                  onchange={() => toggleInActive(s.session_id)}
                />
                <span class="nm">{s.name}</span>
              </label>
              {#if otherGroups.length > 0}
                <span class="other-groups" title="Also broadcasting in">
                  {#each otherGroups as og (og)}
                    <span class="gchip">{og === "" ? "default" : og}</span>
                  {/each}
                </span>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if broadcast.lastError}
        <div class="err">
          <strong>Recent fan-out errors:</strong>
          <pre>{broadcast.lastError}</pre>
          <button onclick={() => broadcast.dismissError()}>Dismiss</button>
        </div>
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
    width: min(520px, 92vw);
    max-height: 80vh;
    display: flex;
    flex-direction: column;
    padding: 1rem 1.2rem;
    box-shadow: 0 8px 30px rgba(0,0,0,0.5);
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.4rem;
  }
  header h2 {
    display: flex; align-items: center; gap: 0.4rem;
    margin: 0;
    font-size: 0.9rem;
    text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--peach);
  }
  .close {
    background: transparent; border: 0; color: var(--subtext0); cursor: pointer;
    padding: 0.15rem 0.35rem; border-radius: 3px;
  }
  .close:hover { background: var(--surface0); color: var(--text); }
  .hint { color: var(--subtext0); font-size: 0.78rem; margin: 0.2rem 0 0.6rem; line-height: 1.5; }
  .group-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }
  .group-pick {
    display: inline-flex; align-items: center; gap: 0.3rem;
    font-size: 0.78rem; color: var(--subtext0);
  }
  .group-pick select {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 3px;
    padding: 0.2rem 0.4rem;
    font: inherit; font-size: 0.78rem;
  }
  .group-row button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.25rem 0.6rem;
    cursor: pointer; font: inherit; font-size: 0.78rem;
  }
  .group-row button:hover { background: var(--surface1); }
  .group-row button.danger { color: var(--red); }
  .group-row button.danger:hover { background: var(--red); color: var(--on-accent); }
  .other-groups { display: inline-flex; gap: 0.25rem; flex-wrap: wrap; }
  .gchip {
    display: inline-block;
    background: var(--surface0);
    color: var(--subtext0);
    padding: 0 0.4rem;
    border-radius: 8px;
    font-size: 0.7rem;
  }
  .bulk {
    display: flex; align-items: center; gap: 0.4rem;
    padding: 0.4rem 0;
    border-top: 1px solid var(--surface0);
    border-bottom: 1px solid var(--surface0);
  }
  .bulk button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.25rem 0.6rem;
    cursor: pointer; font: inherit; font-size: 0.78rem;
  }
  .bulk button:hover { background: var(--surface1); }
  .bulk .show-other {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.74rem;
    color: var(--subtext0);
    margin-left: 0.4rem;
    cursor: pointer;
  }
  .bulk .count { margin-left: auto; color: var(--subtext0); font-size: 0.78rem; }
  .sessions {
    list-style: none; margin: 0.6rem 0 0; padding: 0;
    overflow-y: auto;
  }
  .sessions li { padding: 0.25rem 0; }
  .sessions label {
    display: flex; align-items: center; gap: 0.5rem;
    cursor: pointer; padding: 0.2rem 0.3rem; border-radius: 3px;
    font-size: 0.85rem;
  }
  .sessions label:hover { background: var(--surface0); }
  .sessions .nm { flex: 1; }
  .empty {
    padding: 1.2rem 0.5rem;
    color: var(--overlay1);
    text-align: center;
    font-style: italic;
  }
  .err {
    margin-top: 0.6rem;
    background: color-mix(in oklab, var(--red) 12%, var(--bg-panel));
    border-left: 3px solid var(--red);
    padding: 0.5rem 0.8rem;
    border-radius: 0 3px 3px 0;
    font-size: 0.78rem;
  }
  .err pre {
    white-space: pre-wrap; word-break: break-word;
    font-size: 0.72rem; margin: 0.3rem 0;
  }
  .err button {
    background: var(--surface0); color: var(--text); border: 0;
    border-radius: 3px; padding: 0.2rem 0.5rem; cursor: pointer;
  }
</style>
