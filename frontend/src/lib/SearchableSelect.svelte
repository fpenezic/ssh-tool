<script lang="ts">
  // Searchable dropdown that behaves like <select> for the surrounding
  // form: bind:value gives the same shape (option value string), and
  // the displayed label is looked up from the options list. Includes
  // a free-text filter input so you don't have to scroll 200 entries
  // to find one.
  //
  // Keyboard: ArrowUp / ArrowDown navigate, Enter selects, Esc
  // closes, typing filters. Click outside to close.

  type Option = { value: string; label: string; group?: string };

  type Props = {
    value: string;
    options: Option[];
    placeholder?: string;
    onChange?: (v: string) => void;
  };

  let { value = $bindable(), options, placeholder = "Search…", onChange }: Props = $props();

  let open = $state(false);
  let query = $state("");
  let activeIdx = $state(0);
  let rootEl: HTMLDivElement | undefined = $state();
  let inputEl: HTMLInputElement | undefined = $state();
  let listEl: HTMLDivElement | undefined = $state();

  // The label shown on the trigger button - looked up from options
  // every render so changes to value (or the option list) propagate.
  const triggerLabel = $derived.by(() => {
    if (!value) return "(inherit)";
    const hit = options.find((o) => o.value === value);
    return hit?.label ?? value;
  });

  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((o) =>
      o.label.toLowerCase().includes(q) ||
      (o.group ?? "").toLowerCase().includes(q),
    );
  });

  // Reset highlighted row whenever the filtered list changes.
  $effect(() => {
    void filtered;
    activeIdx = 0;
  });

  function pickOption(v: string) {
    value = v;
    onChange?.(v);
    closeDropdown();
  }

  function openDropdown() {
    open = true;
    query = "";
    setTimeout(() => inputEl?.focus(), 0);
  }
  function closeDropdown() {
    open = false;
    query = "";
  }

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === "Escape") { e.preventDefault(); closeDropdown(); return; }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = Math.min(filtered.length - 1, activeIdx + 1);
      scrollActiveIntoView();
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = Math.max(-1, activeIdx - 1);
      scrollActiveIntoView();
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      if (activeIdx === -1) pickOption("");
      else if (filtered[activeIdx]) pickOption(filtered[activeIdx].value);
    }
  }

  function scrollActiveIntoView() {
    if (!listEl) return;
    const row = listEl.querySelector(`[data-idx="${activeIdx}"]`);
    row?.scrollIntoView({ block: "nearest" });
  }

  // Close on outside click.
  $effect(() => {
    if (!open) return;
    function onDocClick(e: MouseEvent) {
      if (!rootEl) return;
      if (!rootEl.contains(e.target as Node)) closeDropdown();
    }
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="search-select" bind:this={rootEl} onkeydown={onKey} tabindex="-1">
  <button
    type="button"
    class="trigger"
    onclick={() => (open ? closeDropdown() : openDropdown())}
  >
    <span class="label">{triggerLabel}</span>
    <span class="chev">▾</span>
  </button>
  {#if open}
    <div class="popover">
      <input
        bind:this={inputEl}
        bind:value={query}
        placeholder={placeholder}
        spellcheck="false"
        autocomplete="off"
      />
      <div class="list" bind:this={listEl}>
        <button
          type="button"
          class="row inherit"
          class:active={activeIdx === -1}
          data-idx={-1}
          onmousemove={() => (activeIdx = -1)}
          onclick={() => pickOption("")}
        >
          (inherit)
        </button>
        {#each filtered as o, i (o.value)}
          <button
            type="button"
            class="row"
            class:active={i === activeIdx}
            class:selected={o.value === value}
            data-idx={i}
            onmousemove={() => (activeIdx = i)}
            onclick={() => pickOption(o.value)}
          >
            <span class="row-label">{o.label}</span>
            {#if o.group}<span class="row-group">{o.group}</span>{/if}
          </button>
        {/each}
        {#if filtered.length === 0}
          <div class="empty">No matches.</div>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .search-select { position: relative; }
  .trigger {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.35rem 0.5rem;
    font: inherit;
    font-size: 0.85rem;
    cursor: pointer;
    text-align: left;
  }
  .trigger:hover { border-color: var(--surface1); }
  .trigger .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .trigger .chev { color: var(--overlay0); font-size: 0.75rem; margin-left: 0.4rem; }
  .popover {
    position: absolute;
    top: 100%;
    left: 0;
    right: 0;
    margin-top: 2px;
    background: var(--base);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.45);
    z-index: 50;
    max-height: 320px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .popover input {
    background: var(--mantle);
    color: var(--text);
    border: 0;
    border-bottom: 1px solid var(--surface0);
    padding: 0.45rem 0.55rem;
    font: inherit;
    font-size: 0.85rem;
    outline: none;
  }
  .list { overflow-y: auto; flex: 1; padding: 0.2rem 0; }
  .row {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    width: 100%;
    text-align: left;
    background: transparent;
    color: var(--text);
    border: 0;
    padding: 0.3rem 0.6rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.85rem;
    border-left: 2px solid transparent;
  }
  .row-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row-group {
    margin-left: auto;
    flex-shrink: 0;
    color: var(--overlay0);
    font-size: 0.72rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 45%;
  }
  .row.active { background: var(--surface0); border-left-color: var(--blue); }
  .row.selected { color: var(--blue); font-weight: 500; }
  .row.inherit { color: var(--subtext0); font-style: italic; }
  .empty {
    padding: 0.55rem 0.6rem;
    color: var(--overlay0);
    font-size: 0.78rem;
  }
</style>
