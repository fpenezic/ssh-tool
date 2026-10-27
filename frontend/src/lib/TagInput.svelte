<script lang="ts">
  // Tag editor shared by connections and credentials: chips, Enter (or a
  // comma, or leaving the field) adds what was typed, Backspace in an
  // empty field removes the last chip. Existing tags are offered while
  // typing, so "prod" does not become "Prod" and "production" on the next
  // three items. Most used first, the ones already on this item left out.
  // Shown on focus too, empty input included: picking beats remembering.
  interface Props {
    tags: string[];
    // Every tag in use on this kind of item, with its count.
    known: Array<{ tag: string; count: number }>;
    placeholder?: string;
  }
  let { tags = $bindable(), known, placeholder = "add tag and press Enter" }: Props = $props();

  let input = $state("");
  let focus = $state(false);
  let pick = $state(-1);

  const suggestions = $derived.by(() => {
    if (!focus) return [];
    const q = input.trim().toLowerCase();
    const have = new Set(tags);
    const all = known.filter((t) => !have.has(t.tag));
    const starts = all.filter((t) => t.tag.toLowerCase().startsWith(q));
    const contains = q ? all.filter((t) => !t.tag.toLowerCase().startsWith(q) && t.tag.toLowerCase().includes(q)) : [];
    return [...starts, ...contains].slice(0, 8);
  });

  function add() {
    // A pasted "a, b" becomes two tags.
    const parts = input.split(",").map((s) => s.trim()).filter(Boolean);
    input = "";
    const next = [...tags];
    for (const p of parts) if (!next.includes(p)) next.push(p);
    if (next.length !== tags.length) tags = next;
  }
  function choose(tag: string) {
    input = tag;
    pick = -1;
    add();
  }
  function remove(tag: string) {
    tags = tags.filter((t) => t !== tag);
  }

  function onKey(e: KeyboardEvent) {
    const n = suggestions.length;
    if (e.key === "ArrowDown" && n) {
      e.preventDefault();
      pick = (pick + 1) % n;
    } else if (e.key === "ArrowUp" && n) {
      e.preventDefault();
      pick = pick <= 0 ? n - 1 : pick - 1;
    } else if (e.key === "Enter" || e.key === "," || (e.key === "Tab" && pick >= 0)) {
      if (e.key === "Tab" && !input.trim() && pick < 0) return;
      e.preventDefault();
      if (pick >= 0 && pick < n) choose(suggestions[pick].tag);
      else add();
    } else if (e.key === "Backspace" && input === "" && tags.length) {
      tags = tags.slice(0, -1);
    } else if (e.key === "Escape") {
      focus = false;
    } else {
      pick = -1;
    }
  }
</script>

<div class="tag-row">
  {#each tags as t (t)}
    <span class="tag-chip">
      {t}
      <button type="button" class="tag-x" onclick={() => remove(t)} title="Remove tag">×</button>
    </span>
  {/each}
  <div class="tag-input-wrap">
    <input
      class="tag-input"
      bind:value={input}
      {placeholder}
      onkeydown={onKey}
      onfocus={() => { focus = true; pick = -1; }}
      onblur={() => { focus = false; add(); }}
    />
    {#if suggestions.length}
      <div class="tag-suggest" role="listbox">
        {#each suggestions as s, i (s.tag)}
          <!-- mousedown, not click: click lands after the input's blur
               has already committed whatever was typed. -->
          <button
            type="button"
            role="option"
            aria-selected={i === pick}
            class:picked={i === pick}
            onmousedown={(e) => { e.preventDefault(); choose(s.tag); }}
          >
            <span>{s.tag}</span>
            <span class="tag-count">{s.count}</span>
          </button>
        {/each}
      </div>
    {/if}
  </div>
</div>

<style>
  .tag-row {
    display: flex; flex-wrap: wrap; gap: 0.3rem; align-items: center;
    padding: 0.35rem 0.5rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    min-height: 2rem;
    box-sizing: border-box;
  }
  .tag-chip {
    display: inline-flex; align-items: center; gap: 0.25rem;
    background: var(--surface0); color: var(--text);
    padding: 0.15rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
  }
  .tag-x {
    background: transparent; border: 0; color: var(--overlay0);
    cursor: pointer; padding: 0; font-size: 0.9rem; line-height: 1;
  }
  .tag-x:hover { color: var(--red); }
  .tag-input {
    flex: 1; min-width: 8rem; width: 100%;
    background: transparent; border: 0; color: var(--text);
    font: inherit; font-size: 0.82rem;
    padding: 0.15rem 0;
  }
  .tag-input:focus { outline: none; }
  .tag-input-wrap { position: relative; flex: 1; min-width: 8rem; display: flex; }
  .tag-suggest {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    min-width: 12rem;
    max-width: 20rem;
    z-index: 20;
    display: flex;
    flex-direction: column;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.35);
    padding: 3px;
  }
  .tag-suggest button {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    background: transparent;
    border: 0;
    color: var(--text);
    font: inherit;
    font-size: 0.8rem;
    text-align: left;
    padding: 0.25rem 0.5rem;
    border-radius: 3px;
    cursor: pointer;
  }
  .tag-suggest button:hover, .tag-suggest button.picked { background: var(--surface0); }
  .tag-count { color: var(--overlay1); font-size: 0.72rem; }
</style>
