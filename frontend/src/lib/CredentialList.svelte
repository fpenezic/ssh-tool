<script lang="ts">
  import { credentials, selection, drag } from "./stores.svelte";
  import { expandedCredentials } from "./treeState.svelte";
  import { api, type CredentialRef } from "./api";
  import { isInvalidCredDrop, applyCredDrop, type CredDragKind } from "./credentialDnd";
  import CredFolderNode from "./CredFolderNode.svelte";
  import Icon from "./Icon.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { credentialKindIcon, IconFolderPlus, IconPlus, IconRotateCw, IconKey } from "./iconMap";
  import { connectionActions } from "./connectionActions.svelte";

  interface Props {
    onCreate: () => void;
  }
  let { onCreate }: Props = $props();

  // Delete key on the credentials tree stages the same DeleteConfirm
  // modal the connections tree uses - victims listed, folders cascade
  // (backend deletes contained credentials through the vault-cleanup
  // path), multi-selection deletes everything selected.
  function onCredTreeKey(e: KeyboardEvent) {
    if (e.key !== "Delete" && !(e.key === "Backspace" && e.metaKey)) return;
    const folderIds = selection.selectedCredentialFolderIds();
    if (folderIds.length > 0) {
      e.preventDefault();
      connectionActions.openDeleteCredFolders(folderIds);
      return;
    }
    const ids = selection.selectedCredentialIds();
    if (ids.length === 0) return;
    e.preventDefault();
    connectionActions.openDeleteCredentials(ids);
  }

  // Modifier-capture fallback for the WebView2 / WebKitGTK quirk
  // where draggable rows lose modifier flags on `click`.
  let lastMouseMods = { ctrl: false, meta: false, shift: false };
  function recordMods(e: MouseEvent) {
    lastMouseMods = { ctrl: e.ctrlKey, meta: e.metaKey, shift: e.shiftKey };
  }

  function selectCred(c: CredentialRef, e?: MouseEvent) {
    const ctrl  = (e?.ctrlKey  ?? false) || lastMouseMods.ctrl;
    const meta  = (e?.metaKey  ?? false) || lastMouseMods.meta;
    const shift = (e?.shiftKey ?? false) || lastMouseMods.shift;
    lastMouseMods = { ctrl: false, meta: false, shift: false };
    if (shift) {
      selection.rangeCredential(c.id, credentials.flatVisibleCredentialIds());
      return;
    }
    if (ctrl || meta) {
      selection.toggleCredential(c.id);
      return;
    }
    selection.selectCredentialById(c.id);
  }

  async function addFolder() {
    const name = await showPrompt("Folder name?");
    if (!name) return;
    await api.credentialFoldersCreate(name);
    await credentials.load();
  }

  const rootFolders = $derived(credentials.foldersIn(null));
  const rootCreds = $derived(credentials.credsIn(null));

  // ---------- DnD: only the "drop to root" handlers stay here; per-
  // folder drop is handled inside CredFolderNode. ----------

  function currentSource(): { kind: CredDragKind; id: string } | null {
    if (drag.credentialId) return { kind: "credential", id: drag.credentialId };
    if (drag.credentialFolderId) return { kind: "credentialFolder", id: drag.credentialFolderId };
    return null;
  }

  function onTreeDragOver(e: DragEvent) {
    const src = currentSource();
    if (!src) return;
    if (isInvalidCredDrop(src.kind, src.id, null, drag.multiCredIds)) {
      drag.hoverCredFolder(null);
      return;
    }
    e.preventDefault();
    e.dataTransfer!.dropEffect = "move";
    drag.hoverCredFolder("ROOT");
  }

  function onTreeDragLeave(e: DragEvent) {
    if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
    if (drag.overCredFolderId === "ROOT") drag.hoverCredFolder(null);
  }

  async function onTreeDrop(e: DragEvent) {
    const src = currentSource();
    if (!src) return;
    e.preventDefault();
    if (isInvalidCredDrop(src.kind, src.id, null, drag.multiCredIds)) return;
    const alsoMoving = src.kind === "credential" ? [...drag.multiCredIds] : [];
    drag.end();
    try { await applyCredDrop(src.kind, src.id, null, alsoMoving); }
    catch (err) { console.error("cred drop failed", err); }
  }

</script>

<aside class="sidebar">
  <header>
    <h2>Credentials</h2>
    <div class="actions">
      <button onclick={addFolder} title="New folder" class="iconbtn">
        <IconFolderPlus size={14} />
      </button>
      <button onclick={onCreate} title="New credential" class="iconbtn">
        <IconPlus size={14} /><IconKey size={14} />
      </button>
      <button onclick={() => credentials.load()} title="Reload" class="iconbtn">
        <IconRotateCw size={14} />
      </button>
    </div>
  </header>

  {#if credentials.loading}
    <div class="skeleton">
      {#each Array(6) as _, i (i)}
        <div class="sk-row" style="--w: {55 + (i * 11) % 35}%"></div>
      {/each}
    </div>
  {:else if credentials.error}
    <div class="err">{credentials.error}</div>
  {:else if credentials.folders.length === 0 && credentials.list.length === 0}
    <div class="empty">
      <div class="ico"><IconKey size={36} /></div>
      <p>No credentials yet.</p>
      <p class="muted">Add one above to connect with key or password auth.</p>
    </div>
  {:else}
    <div
      class="tree"
      class:drop-root={drag.overCredFolderId === "ROOT"}
      role="tree"
      tabindex="-1"
      onkeydown={onCredTreeKey}
      ondragover={onTreeDragOver}
      ondragleave={onTreeDragLeave}
      ondrop={onTreeDrop}
    >
      {#each rootFolders as f (f.id)}
        <CredFolderNode folder={f} depth={0} />
      {/each}

      {#each rootCreds as c (c.id)}
        {@const sel = selection.isCredentialSelected(c.id)}
        {@const KindIcon = credentialKindIcon(c.kind)}
        <div class="row cred-row"
          class:selected={sel}
          style="--depth: 0"
          role="treeitem"
          tabindex="0"
          aria-selected={sel}
          draggable="true"
          onmousedown={recordMods}
          onclick={(e) => selectCred(c, e)}
          onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selectCred(c); } }}
          ondragstart={(e) => {
            const inMulti = selection.isCredentialSelected(c.id) && selection.credentialMultiCount() > 1;
            drag.startCredential(c.id, inMulti ? selection.selectedCredentialIds() : []);
            e.dataTransfer!.effectAllowed = "move";
            e.stopPropagation();
          }}
          ondragend={() => drag.end()}
        >
          <span class="chev"> </span>
          <span class="icon"><Icon imageId={c.icon_image_id}>
            <KindIcon size={14} />
          </Icon></span>
          <div class="meta">
            <div class="name">{c.name}</div>
            <div class="sub">
              <span class="kind">{c.kind}</span>
              {#if c.hint}<span class="hint-text">· {c.hint}</span>{/if}
            </div>
          </div>
        </div>
      {/each}

      {#if rootFolders.length === 0 && rootCreds.length === 0}
        <div class="hint">No credentials yet. Use the new-credential button above.</div>
      {/if}
    </div>
  {/if}
</aside>

<style>
  .empty {
    padding: 2rem 1rem;
    text-align: center;
    color: var(--subtext0);
  }
  .empty .ico { font-size: 2.5rem; opacity: 0.4; margin-bottom: 0.6rem; }
  .empty p { margin: 0.3rem 0; font-size: 0.85rem; }
  .empty p.muted { color: var(--overlay0); font-size: 0.78rem; }

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

  .sidebar {
    background: var(--crust); color: var(--text);
    border-right: 1px solid var(--surface0);
    display: flex; flex-direction: column;
    min-width: 0; overflow: hidden;
  }
  header {
    padding: 0.6rem 0.8rem;
    border-bottom: 1px solid var(--surface0);
    display: flex; align-items: center; justify-content: space-between;
  }
  h2 {
    margin: 0; font-size: 0.85rem;
    text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--subtext0);
  }
  .actions { display: flex; gap: 0.25rem; }
  .actions button {
    background: transparent; border: 0; color: var(--subtext0);
    cursor: pointer; padding: 0.15rem 0.35rem;
    border-radius: 3px;
  }
  .actions button:hover { background: var(--surface0); color: var(--text); }
  .actions .iconbtn { display: inline-flex; align-items: center; gap: 0.1rem; }
  .tree { flex: 1; overflow: auto; padding: 0.4rem 0; }
  .hint, .err { padding: 0.6rem 0.8rem; color: var(--overlay0); }
  .err { color: var(--red); }
  .row {
    display: flex; align-items: center; gap: 0.25rem;
    padding: var(--row-pad-y) 0.4rem var(--row-pad-y) calc(0.4rem + var(--depth, 0) * 1rem);
    cursor: pointer; border-radius: 3px;
    width: 100%;
  }
  .row:hover { background: var(--surface0); }
  .row.selected { background: var(--surface1); }
  .row:focus { outline: 1px solid var(--blue); outline-offset: -1px; }
  .chev { width: 1rem; color: var(--overlay0); font-size: 0.85rem; text-align: center; }
  .icon { width: 1.2rem; text-align: center; font-size: 0.85rem; }
  .name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { flex: 1; min-width: 0; }
  .sub { font-size: 0.72rem; color: var(--overlay1); margin-top: var(--row-sub-gap); }
  .kind { background: var(--surface0); padding: 0.05rem 0.3rem; border-radius: 2px; margin-right: 0.2rem; }
  .hint-text { color: var(--overlay0); }
  .tree.drop-root {
    box-shadow: inset 0 0 0 2px var(--blue)55;
  }
</style>
