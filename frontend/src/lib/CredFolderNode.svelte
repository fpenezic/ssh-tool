<script lang="ts">
  // Recursive renderer for a credential folder + its contents. Each
  // level passes a deeper `depth` down so the row's CSS variable
  // --depth drives consistent indentation regardless of nesting level.

  import { credentials, selection, drag } from "./stores.svelte";
  import { expandedCredentials } from "./treeState.svelte";
  import {
    isInvalidCredDrop,
    applyCredDrop,
    type CredDragKind,
  } from "./credentialDnd";
  import type { CredentialRef, CredentialFolder } from "./api";
  import { expiryInfo } from "./credExpiry";
  import CredFolderNodeSelf from "./CredFolderNode.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { IconFolder, credentialKindIcon, IconFolderPlus, IconKey, IconPencil, IconTrash } from "./iconMap";
  import Icon from "./Icon.svelte";
  import { contextMenu } from "./contextMenu.svelte.ts";

  interface Props {
    folder: CredentialFolder;
    depth: number;
  }
  let { folder, depth }: Props = $props();

  const open = $derived(expandedCredentials.isExpanded(folder.id));
  const subFolders = $derived(credentials.foldersIn(folder.id));
  const folderCreds = $derived(credentials.credsIn(folder.id));
  const hasChildren = $derived(subFolders.length + folderCreds.length > 0);

  function toggle() {
    expandedCredentials.toggle(folder.id);
  }

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


  // ----- DnD wiring (mirrors what CredentialList already does) -----

  function currentSource(): { kind: CredDragKind; id: string } | null {
    if (drag.credentialId) return { kind: "credential", id: drag.credentialId };
    if (drag.credentialFolderId) return { kind: "credentialFolder", id: drag.credentialFolderId };
    return null;
  }

  function onDragOver(e: DragEvent) {
    const src = currentSource();
    if (!src) return;
    if (isInvalidCredDrop(src.kind, src.id, folder.id, drag.multiCredIds)) {
      drag.hoverCredFolder(null);
      return;
    }
    e.preventDefault();
    e.dataTransfer!.dropEffect = "move";
    drag.hoverCredFolder(folder.id);
  }
  function onDragLeave(e: DragEvent) {
    if ((e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) return;
    if (drag.overCredFolderId === folder.id) drag.hoverCredFolder(null);
  }
  async function onDrop(e: DragEvent) {
    const src = currentSource();
    if (!src) return;
    e.preventDefault();
    e.stopPropagation();
    if (isInvalidCredDrop(src.kind, src.id, folder.id, drag.multiCredIds)) return;
    const alsoMoving = src.kind === "credential" ? [...drag.multiCredIds] : [];
    drag.end();
    try { await applyCredDrop(src.kind, src.id, folder.id, alsoMoving); }
    catch (err) { console.error("cred drop failed", err); }
  }

  const dropHover = $derived(drag.overCredFolderId === folder.id);

  async function addSubfolder() {
    const name = await showPrompt("Folder name?");
    if (!name?.trim()) return;
    const { api } = await import("./api");
    await api.credentialFoldersCreate(name.trim(), folder.id);
    await credentials.load();
    expandedCredentials.set(folder.id, true);
  }

  async function renameFolder() {
    const next = await showPrompt("Rename folder", folder.name);
    if (!next?.trim() || next.trim() === folder.name) return;
    const { api } = await import("./api");
    await api.credentialFoldersUpdate(folder.id, next.trim());
    await credentials.load();
  }

  async function deleteFolder() {
    // Staged DeleteConfirm modal, same as the connections tree: lists
    // the folder + everything that goes with it, then the backend
    // cascades (creds through the vault-cleanup path). When this
    // folder is part of a multi-selection, the whole selection goes.
    const { connectionActions } = await import("./connectionActions.svelte");
    const selected = selection.selectedCredentialFolderIds();
    const ids = selected.includes(folder.id) && selected.length > 1 ? selected : [folder.id];
    connectionActions.openDeleteCredFolders(ids);
  }

  function openContext(e: MouseEvent) {
    contextMenu.show(e, [
      { label: "New subfolder…",     iconComponent: IconFolderPlus, onSelect: () => addSubfolder() },
      { label: "New credential here…", iconComponent: IconKey, onSelect: () => addCredentialHere() },
      { label: "Rename…",            iconComponent: IconPencil, onSelect: () => renameFolder() },
      { label: "Delete folder",      iconComponent: IconTrash, danger: true, onSelect: () => deleteFolder() },
    ]);
  }

  // Open the CredentialCreate modal pre-targeted at this folder.
  // Dispatched via a CustomEvent the App listens for so we don't
  // need to drill a prop through CredentialList -> CredFolderNode.
  function addCredentialHere() {
    window.dispatchEvent(new CustomEvent("credential-create-in-folder", { detail: folder.id }));
  }

  // Click on the row selects the folder so CredentialDetail shows the
  // header + folder-level actions (Rename, +Folder, +Credential,
  // Delete). The chevron handles expand/collapse separately so the
  // user can browse without committing the active selection.
  // Ctrl/Cmd toggles the folder in/out of the multi-set, Shift
  // ranges across the visible list - same gestures as connections.
  function selectFolder(e: MouseEvent) {
    e.stopPropagation();
    const ctrl = e.ctrlKey || lastMouseMods.ctrl;
    const meta = e.metaKey || lastMouseMods.meta;
    const shift = e.shiftKey || lastMouseMods.shift;
    lastMouseMods = { ctrl: false, meta: false, shift: false };
    if (shift) {
      selection.rangeCredentialFolder(folder.id, credentials.flatVisibleCredentialFolderIds());
      return;
    }
    if (ctrl || meta) {
      selection.toggleCredentialFolder(folder.id);
      return;
    }
    selection.select({ kind: "credentialFolder", id: folder.id });
  }

  function isSelected(): boolean {
    return selection.isCredentialFolderSelected(folder.id);
  }
</script>

<div
  class="folder-row row"
  class:drop-inside={dropHover}
  class:selected={isSelected()}
  style="--depth: {depth}"
  role="treeitem"
  tabindex="0"
  aria-expanded={open}
  aria-selected={isSelected()}
  draggable="true"
  onmousedown={recordMods}
  onclick={selectFolder}
  onkeydown={(e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selection.select({ kind: "credentialFolder", id: folder.id }); }
    else if (e.key === "ArrowRight") expandedCredentials.set(folder.id, true);
    else if (e.key === "ArrowLeft") expandedCredentials.set(folder.id, false);
  }}
  oncontextmenu={openContext}
  ondragstart={(e) => {
    drag.startCredentialFolder(folder.id);
    e.dataTransfer!.effectAllowed = "move";
    e.stopPropagation();
  }}
  ondragend={() => drag.end()}
  ondragover={onDragOver}
  ondragleave={onDragLeave}
  ondrop={onDrop}
>
  <button
    class="chev"
    onclick={(e) => { e.stopPropagation(); toggle(); }}
    title={open ? "Collapse" : "Expand"}
  >{hasChildren ? (open ? "▾" : "▸") : " "}</button>
  <span class="icon"><Icon imageId={null} iconName={folder.icon_name} iconColor={folder.icon_color} size={14}>
    <IconFolder size={14} />
  </Icon></span>
  <span class="name">{folder.name}</span>
  {#if hasChildren}<span class="count">{subFolders.length + folderCreds.length}</span>{/if}
</div>

{#if open}
  {#each subFolders as sf (sf.id)}
    <CredFolderNodeSelf folder={sf} depth={depth + 1} />
  {/each}
  {#each folderCreds as c (c.id)}
    {@const sel = selection.isCredentialSelected(c.id)}
    {@const KindIcon = credentialKindIcon(c.kind)}
    {@const ex = expiryInfo(c.expires_at)}
    <div
      class="row cred-row"
      class:selected={sel}
      style="--depth: {depth + 1}"
      role="treeitem"
      tabindex="0"
      aria-selected={sel}
      draggable="true"
      onmousedown={recordMods}
      onclick={(e) => { e.stopPropagation(); selectCred(c, e); }}
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
      <span class="icon"><Icon imageId={c.icon_image_id} iconName={c.icon_name} iconColor={c.icon_color} size={14}>
        <KindIcon size={14} />
      </Icon></span>
      <div class="meta">
        <div class="name">
          {c.name}
          {#if ex.level === "soon" || ex.level === "expired"}
            <span class="cred-expiry {ex.level}" title={ex.label}>{ex.level === "expired" ? "expired" : ex.label}</span>
          {/if}
        </div>
        <div class="sub">
          <span class="kind">{c.kind}</span>
          {#if c.hint}<span class="hint-text">· {c.hint}</span>{/if}
        </div>
      </div>
    </div>
  {/each}
{/if}

<style>
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    padding: var(--row-pad-y) 0.4rem var(--row-pad-y) calc(0.4rem + var(--depth, 0) * 1rem);
    cursor: pointer;
    border-radius: 3px;
    width: 100%;
    position: relative;
  }
  .row:hover { background: var(--surface0); }
  .row.selected { background: var(--surface1); }
  .row:focus { outline: 1px solid var(--blue); outline-offset: -1px; }
  .chev {
    width: 1rem; color: var(--overlay0); text-align: center;
    background: transparent; border: 0; padding: 0; cursor: pointer;
    font: inherit; font-size: 1.05rem; line-height: 1;
  }
  .chev:hover { color: var(--text); }
  .icon { width: 1.2rem; text-align: center; font-size: 0.85rem; }
  .name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { flex: 1; min-width: 0; }
  .sub { font-size: 0.72rem; color: var(--overlay1); margin-top: var(--row-sub-gap); }
  .kind { background: var(--surface0); padding: 0.05rem 0.3rem; border-radius: 2px; margin-right: 0.2rem; }
  .hint-text { color: var(--overlay0); }
  .cred-expiry { margin-left: 0.35rem; padding: 0 0.3rem; border-radius: 999px; font-size: 0.65rem; font-weight: 600; vertical-align: middle; }
  .cred-expiry.soon { background: var(--yellow); color: var(--on-accent); }
  .cred-expiry.expired { background: var(--red); color: var(--on-accent); }
  .count { color: var(--overlay0); font-size: 0.75rem; }
  .row.drop-inside {
    background: var(--blue)33;
    outline: 1px dashed var(--blue);
    outline-offset: -2px;
  }
</style>
