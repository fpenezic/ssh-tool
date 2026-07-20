<script lang="ts">
  import { tree, credentials, selection } from "./stores.svelte";
  import { errMsg } from "./connectErrors";
  import { connectionActions } from "./connectionActions.svelte";
  import { api, type Connection, type InheritableSettings, type JumpHostOverride } from "./api";
  import JumpChainEditor from "./JumpChainEditor.svelte";
  import ColorPicker from "./ColorPicker.svelte";
  import { IconHost } from "./iconMap";
  import BatchExecModal from "./BatchExecModal.svelte";

  // Tri-state per editable field. "leave" = touch nothing (default),
  // "inherit" = clear the override so the folder's value wins,
  // "set" = write the value below to every selected connection.
  type Mode = "leave" | "inherit" | "set";

  interface Props {
    onDelete?: (ids: string[]) => void;
  }
  let { onDelete }: Props = $props();

  const ids = $derived(selection.selectedConnectionIds());
  const conns = $derived(
    ids.map((id) => tree.connectionById(id)).filter((c): c is Connection => !!c)
  );
  const credList = $derived(credentials.list);

  // Form state. Resets when selection changes.
  let usernameMode = $state<Mode>("leave");
  let usernameVal = $state("");
  let portMode = $state<Mode>("leave");
  let portVal = $state("");
  let authMode = $state<Mode>("leave");
  let authVal = $state("");
  let jumpMode = $state<"leave" | "inherit" | "none" | "set">("leave");
  let jumpChain = $state<JumpHostOverride | undefined>(undefined);
  let colorMode = $state<Mode>("leave");
  let colorVal = $state("");
  let keepaliveMode = $state<Mode>("leave");
  let keepaliveVal = $state("");
  let autoMode = $state<"leave" | "inherit" | "on" | "off">("leave");
  let verboseMode = $state<"leave" | "inherit" | "on" | "off">("leave");

  // Tags are kept on the connection itself (not on the inheritable
  // settings) so they need add/remove ops, not the tri-state pattern.
  // The user types comma- or space-separated tag names.
  let addTagsRaw = $state("");
  let removeTagsRaw = $state("");
  function parseTagList(raw: string): string[] {
    return raw.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean);
  }

  let savedAt = $state<string | null>(null);
  let saveError = $state<string | null>(null);
  let saving = $state(false);

  // Reset all editor state when the selection changes.
  let lastSig = "";
  $effect(() => {
    const sig = ids.join(",");
    if (sig === lastSig) return;
    lastSig = sig;
    usernameMode = "leave"; usernameVal = "";
    portMode = "leave"; portVal = "";
    authMode = "leave"; authVal = "";
    jumpMode = "leave"; jumpChain = undefined;
    colorMode = "leave"; colorVal = "";
    keepaliveMode = "leave"; keepaliveVal = "";
    autoMode = "leave";
    verboseMode = "leave";
    addTagsRaw = ""; removeTagsRaw = "";
    savedAt = null; saveError = null;
  });

  // portVal / keepaliveVal are typed string but bound to <input type="number">,
  // which makes Svelte hand back a number the moment the user types. Calling
  // .trim() on that threw and aborted the whole patch build, so Apply silently
  // did nothing. Normalise to a trimmed string first.
  function numText(v: string | number | undefined | null): string {
    if (v === undefined || v === null) return "";
    return String(v).trim();
  }

  function buildPatch(): { patch: InheritableSettings; clear: string[] } {
    const patch: InheritableSettings = {};
    const clear: string[] = [];

    if (usernameMode === "inherit") clear.push("username");
    else if (usernameMode === "set") patch.username = usernameVal.trim();

    if (portMode === "inherit") clear.push("port");
    else if (portMode === "set" && numText(portVal)) {
      const n = parseInt(numText(portVal), 10);
      if (!isNaN(n)) patch.port = n;
    }

    if (authMode === "inherit") clear.push("auth_ref");
    else if (authMode === "set") patch.auth_ref = authVal;

    if (jumpMode === "inherit") clear.push("jump_host");
    else if (jumpMode === "none") patch.jump_host = { kind: "none" };
    else if (jumpMode === "set" && jumpChain?.kind === "chain") {
      patch.jump_host = jumpChain;
    }

    if (colorMode === "inherit") clear.push("color_tag");
    else if (colorMode === "set") patch.color_tag = colorVal;

    if (keepaliveMode === "inherit") clear.push("keepalive_interval");
    else if (keepaliveMode === "set" && numText(keepaliveVal)) {
      const n = parseInt(numText(keepaliveVal), 10);
      if (!isNaN(n)) patch.keepalive_interval = n;
    }

    if (autoMode === "inherit") clear.push("auto_reconnect");
    else if (autoMode === "on") patch.auto_reconnect = true;
    else if (autoMode === "off") patch.auto_reconnect = false;

    if (verboseMode === "inherit") clear.push("verbose");
    else if (verboseMode === "on") patch.verbose = true;
    else if (verboseMode === "off") patch.verbose = false;

    return { patch, clear };
  }

  async function applyBatch() {
    if (saving || ids.length === 0) return;
    saving = true;
    saveError = null;
    try {
      const { patch, clear } = buildPatch();
      const addTags = parseTagList(addTagsRaw);
      const removeTags = parseTagList(removeTagsRaw);
      if (
        clear.length === 0 &&
        Object.keys(patch).length === 0 &&
        addTags.length === 0 &&
        removeTags.length === 0
      ) {
        saveError = "Nothing to apply - change at least one field or tag list.";
        return;
      }
      const r = await api.connectionsBatchUpdate({
        ids,
        patch,
        clear_fields: clear,
        add_tags: addTags.length ? addTags : undefined,
        remove_tags: removeTags.length ? removeTags : undefined,
      });
      await tree.load();
      savedAt = `${r.updated} updated at ${new Date().toLocaleTimeString()}`;
    } catch (e: any) {
      saveError = errMsg(e);
    } finally {
      saving = false;
    }
  }

  let connectingAll = $state(false);
  let showBatchExec = $state(false);

  async function connectAll() {
    if (connectingAll || conns.length === 0) return;
    connectingAll = true;
    // Delegate to the shared action so multi-connect gets the same
    // >5-hosts confirm, background toast, shared-bastion reuse, stagger
    // and per-connection error handling as every other Connect-all entry
    // point. (This panel used to fire its own bare parallel connects,
    // bypassing all of that.)
    try {
      await connectionActions.connectMany(ids);
    } finally {
      connectingAll = false;
    }
  }

  function clearSelection() {
    selection.select({ kind: "none" });
  }
</script>

<section class="batch">
  <header>
    <h1>{conns.length} connections selected</h1>
    <div class="head-actions">
      <button class="primary" disabled={connectingAll} onclick={connectAll}>
        {connectingAll ? "Connecting…" : `Connect all (${conns.length})`}
      </button>
      <button onclick={() => (showBatchExec = true)}>
        ▶ Run command…
      </button>
      <button onclick={() => connectionActions.openMoveTo(ids, [])}>
        ↪ Move to folder…
      </button>
      {#if onDelete}
        <button class="danger" onclick={() => onDelete!(ids)}>Delete {ids.length}</button>
      {/if}
      <button onclick={clearSelection}>Clear selection</button>
    </div>
  </header>

  <div class="list">
    {#each conns as c}
      <div class="conn-pill" title={c.hostname}>
        <span class="dot"><IconHost size={12} /></span><span class="n">{c.name}</span>
      </div>
    {/each}
  </div>

  <div class="form">
    <p class="section-label">Batch edit overrides - apply the same change to all selected.</p>
    <p class="hint">
      <strong>Leave</strong>: no change. <strong>Inherit</strong>: clear the
      override so the folder's value takes effect. <strong>Set</strong>:
      overwrite with the value entered below.
    </p>

    <div class="field">
      <span class="lbl">Username</span>
      <div class="modes">
        <label><input type="radio" bind:group={usernameMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={usernameMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={usernameMode} value="set" /> set</label>
      </div>
      <input class="val" disabled={usernameMode !== "set"} bind:value={usernameVal} placeholder="e.g. root" />
    </div>

    <div class="field">
      <span class="lbl">Port</span>
      <div class="modes">
        <label><input type="radio" bind:group={portMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={portMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={portMode} value="set" /> set</label>
      </div>
      <input class="val" type="number" min="1" max="65535" disabled={portMode !== "set"} bind:value={portVal} placeholder="22" />
    </div>

    <div class="field">
      <span class="lbl">Credential</span>
      <div class="modes">
        <label><input type="radio" bind:group={authMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={authMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={authMode} value="set" /> set</label>
      </div>
      <select class="val" disabled={authMode !== "set"} bind:value={authVal}>
        <option value="">(pick a credential)</option>
        {#each credentials.flatGrouped() as g (g.cred.id)}
          <option value={g.cred.id}>{g.label}</option>
        {/each}
      </select>
    </div>

    <div class="field jump">
      <span class="lbl">Jump host</span>
      <div class="modes">
        <label><input type="radio" bind:group={jumpMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={jumpMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={jumpMode} value="none" /> direct (no jump)</label>
        <label><input type="radio" bind:group={jumpMode} value="set" /> set chain</label>
      </div>
      {#if jumpMode === "set"}
        <div class="val">
          <JumpChainEditor value={jumpChain} onChange={(v) => (jumpChain = v)} />
        </div>
      {/if}
    </div>

    <div class="field color">
      <span class="lbl">Color tag</span>
      <div class="modes">
        <label><input type="radio" bind:group={colorMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={colorMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={colorMode} value="set" /> set</label>
      </div>
      {#if colorMode === "set"}
        <div class="val">
          <ColorPicker value={colorVal} onChange={(v) => (colorVal = v)} label="" />
        </div>
      {/if}
    </div>

    <div class="field tags">
      <span class="lbl">Tags</span>
      <div class="tag-edit">
        <label class="tag-line">
          <span class="tag-op add">+ Add</span>
          <input
            type="text"
            bind:value={addTagsRaw}
            placeholder="comma or space separated"
          />
        </label>
        <label class="tag-line">
          <span class="tag-op remove">- Remove</span>
          <input
            type="text"
            bind:value={removeTagsRaw}
            placeholder="comma or space separated"
          />
        </label>
      </div>
      <p class="tag-hint">
        Add merges into each row's existing tags (deduped). Remove filters
        listed tags out. Leave both empty to skip.
      </p>
    </div>

    <div class="field">
      <span class="lbl">Keepalive (s)</span>
      <div class="modes">
        <label><input type="radio" bind:group={keepaliveMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={keepaliveMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={keepaliveMode} value="set" /> set</label>
      </div>
      <input class="val" type="number" min="0" disabled={keepaliveMode !== "set"} bind:value={keepaliveVal} placeholder="30" />
    </div>

    <div class="field">
      <span class="lbl">Auto-reconnect</span>
      <div class="modes">
        <label><input type="radio" bind:group={autoMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={autoMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={autoMode} value="on" /> on</label>
        <label><input type="radio" bind:group={autoMode} value="off" /> off</label>
      </div>
    </div>

    <div class="field">
      <span class="lbl">Verbose</span>
      <div class="modes">
        <label><input type="radio" bind:group={verboseMode} value="leave" /> leave</label>
        <label><input type="radio" bind:group={verboseMode} value="inherit" /> inherit</label>
        <label><input type="radio" bind:group={verboseMode} value="on" /> on</label>
        <label><input type="radio" bind:group={verboseMode} value="off" /> off</label>
      </div>
    </div>

    <div class="actions">
      <button class="primary" disabled={saving} onclick={applyBatch}>
        {saving ? "Applying…" : `Apply to ${ids.length} connection${ids.length === 1 ? "" : "s"}`}
      </button>
      {#if savedAt}<span class="ok">{savedAt}</span>{/if}
      {#if saveError}<span class="bad">{saveError}</span>{/if}
    </div>
  </div>
</section>

{#if showBatchExec}
  <BatchExecModal onClose={() => (showBatchExec = false)} />
{/if}

<style>
  .batch {
    padding: 1rem 1.25rem;
    overflow: auto;
    color: var(--text);
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--surface0);
    padding-bottom: 0.5rem;
    margin-bottom: 0.8rem;
  }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  .head-actions { display: flex; gap: 0.5rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.9rem; border-radius: 3px; cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--lavender); }
  button.danger { background: var(--red); color: var(--on-accent); font-weight: 600; }
  button.danger:hover { background: var(--maroon); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .list {
    display: flex; flex-wrap: wrap; gap: 0.3rem;
    margin-bottom: 0.9rem; padding: 0.5rem; background: var(--crust);
    border: 1px solid var(--surface0); border-radius: 4px;
    max-height: 6rem; overflow-y: auto;
  }
  .conn-pill {
    display: inline-flex; align-items: center; gap: 0.3rem;
    background: var(--surface0); padding: 0.15rem 0.5rem; border-radius: 999px;
    font-size: 0.78rem;
  }
  .conn-pill .dot { font-size: 0.7rem; }
  .form {
    background: var(--crust); border: 1px solid var(--surface0); border-radius: 4px;
    padding: 0.9rem 1rem;
  }
  .section-label {
    font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--subtext0); margin: 0 0 0.3rem;
  }
  .hint { color: var(--subtext0); font-size: 0.78rem; margin: 0 0 0.9rem; line-height: 1.5; }
  .field {
    display: grid;
    grid-template-columns: 7rem 1fr;
    column-gap: 0.6rem; row-gap: 0.25rem;
    align-items: center;
    margin-bottom: 0.6rem;
  }
  .field.jump { grid-template-columns: 7rem 1fr; align-items: start; }
  .lbl { font-size: 0.8rem; color: var(--subtext0); }
  .modes { display: flex; gap: 0.7rem; font-size: 0.78rem; color: var(--text); }
  .modes label { display: inline-flex; align-items: center; gap: 0.25rem; cursor: pointer; }
  .val { grid-column: 2; }
  .tag-edit { display: flex; flex-direction: column; gap: 0.3rem; }
  .tag-line { display: flex; align-items: center; gap: 0.4rem; }
  .tag-op {
    font-size: 0.72rem; font-weight: 600;
    padding: 0.1rem 0.45rem; border-radius: 3px;
    color: var(--on-accent); min-width: 4.5rem; text-align: center;
  }
  .tag-op.add { background: var(--green); }
  .tag-op.remove { background: var(--red); }
  .tag-line input { flex: 1; }
  .tag-hint { color: var(--overlay1); font-size: 0.72rem; margin: 0.3rem 0 0; }
  input, select {
    background: var(--mantle); color: var(--text); border: 1px solid var(--surface0);
    border-radius: 3px; padding: 0.35rem 0.5rem; font: inherit;
  }
  input:disabled, select:disabled { opacity: 0.5; }
  input:focus, select:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .actions {
    display: flex; align-items: center; gap: 0.6rem;
    margin-top: 0.8rem; padding-top: 0.6rem; border-top: 1px solid var(--surface0);
  }
  .ok { color: var(--green); font-size: 0.8rem; }
  .bad { color: var(--red); font-size: 0.8rem; }
</style>
