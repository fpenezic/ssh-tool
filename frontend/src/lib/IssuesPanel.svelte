<script lang="ts">
  // The status bar's issues list: one row per issue, each with what it
  // means and a button to the place that fixes it. Replaces a count whose
  // details only lived in a hover tooltip.
  import { clickOutside } from "./clickOutside";
  import { issues } from "./issues.svelte";

  let { onClose }: { onClose: () => void } = $props();

  function run(action: () => void) {
    action();
    onClose();
  }
</script>

<div class="pop" use:clickOutside={{ onOutside: onClose }}>
  <header>
    <span class="title">Issues</span>
    <button class="close" title="Close" onclick={onClose}>×</button>
  </header>
  {#if issues.items.length === 0}
    <div class="empty">Nothing needs attention.</div>
  {:else}
    <ul>
      {#each issues.items as it (it.id)}
        <li class={it.severity}>
          <div class="text">
            <div class="head">{it.title}</div>
            <div class="detail">{it.detail}</div>
          </div>
          <button class="act" onclick={() => run(it.action)}>{it.actionLabel}</button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .pop {
    position: absolute;
    right: 0;
    bottom: calc(100% + 4px);
    z-index: 200;
    width: min(480px, 96vw);
    max-height: 60vh;
    display: flex;
    flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
    font-size: 0.82rem;
    color: var(--text);
  }
  header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.45rem 0.6rem; border-bottom: 1px solid var(--surface0);
  }
  .title { font-weight: 600; }
  .close {
    background: transparent; border: 0; color: var(--overlay0);
    font-size: 1.1rem; line-height: 1; cursor: pointer; padding: 0 0.2rem;
  }
  .close:hover { color: var(--text); }
  .empty { padding: 0.8rem 0.6rem; color: var(--subtext0); }
  ul { list-style: none; margin: 0; padding: 0; overflow-y: auto; }
  li {
    display: flex; align-items: flex-start; gap: 0.6rem;
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--surface0);
    border-left: 3px solid var(--yellow);
  }
  li:last-child { border-bottom: 0; }
  li.error { border-left-color: var(--red); }
  .text { flex: 1; min-width: 0; }
  .head { font-weight: 600; overflow-wrap: anywhere; }
  .detail { color: var(--subtext0); margin-top: 0.15rem; line-height: 1.35; }
  .act {
    flex: 0 0 auto;
    background: var(--surface0); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px;
    padding: 0.2rem 0.55rem; font-size: 0.76rem; cursor: pointer; white-space: nowrap;
  }
  .act:hover { background: var(--surface1); }
</style>
