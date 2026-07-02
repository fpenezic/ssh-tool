<script lang="ts">
  // Modal that lists exactly what's about to be deleted and asks for
  // confirmation. Used by the connections tree (folders + connections)
  // and by the credentials tree.
  import { IconFolder, IconHost, IconKey } from "./iconMap";
  import type { Component } from "svelte";
  import { clickOutside } from "./clickOutside";

  interface Item {
    kind: "folder" | "connection" | "credentialFolder" | "credential";
    name: string;
    detail?: string;  // hostname, child count, etc.
  }

  interface Props {
    items: Item[];
    onConfirm: () => void;
    onCancel: () => void;
  }
  let { items, onConfirm, onCancel }: Props = $props();

  let dangerBtn = $state<HTMLButtonElement | null>(null);
  $effect(() => { dangerBtn?.focus(); });

  function iconFor(k: Item["kind"]): Component {
    switch (k) {
      case "folder":           return IconFolder;
      case "connection":       return IconHost;
      case "credentialFolder": return IconFolder;
      case "credential":       return IconKey;
    }
  }
</script>

<div class="overlay" role="presentation">
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    use:clickOutside={{ onOutside: onCancel }}
    onkeydown={(e) => { if (e.key === "Escape") onCancel(); }}
  >
    <h2>Delete {items.length} item{items.length === 1 ? "" : "s"}?</h2>
    <p class="hint">
      Folders are removed recursively - every connection or credential inside
      them goes with the folder. This cannot be undone.
    </p>
    <ul class="list">
      {#each items as it}
        {@const Ic = iconFor(it.kind)}
        <li>
          <span class="ic"><Ic size={13} /></span>
          <span class="nm">{it.name}</span>
          {#if it.detail}<span class="dt">{it.detail}</span>{/if}
        </li>
      {/each}
    </ul>
    <div class="actions">
      <button onclick={onCancel}>Cancel</button>
      <button class="danger" bind:this={dangerBtn} onclick={onConfirm}>Delete</button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
  }
  .modal {
    background: var(--crust);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    padding: 1rem 1.2rem;
    max-width: 520px;
    width: 90vw;
    max-height: 80vh;
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  h2 { margin: 0; font-size: 1rem; }
  .hint { color: var(--subtext0); font-size: 0.82rem; margin: 0; line-height: 1.5; }
  .list {
    list-style: none;
    margin: 0;
    padding: 0.4rem 0.5rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    overflow-y: auto;
    max-height: 50vh;
  }
  .list li {
    display: grid;
    grid-template-columns: 1.4rem 1fr auto;
    align-items: center;
    gap: 0.4rem;
    padding: 0.15rem 0;
    font-size: 0.85rem;
  }
  .ic { text-align: center; }
  .nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dt { color: var(--overlay0); font-size: 0.78rem; }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.4rem;
    margin-top: 0.3rem;
  }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.9rem; border-radius: 3px; cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.danger { background: var(--red); color: var(--on-accent); font-weight: 600; }
  button.danger:hover { background: var(--maroon); }
</style>
