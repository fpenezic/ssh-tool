<script lang="ts">
  // Tiny color picker - preset palette + custom hex + "no color".
  // Used in the connection editor, folder editor, and BatchPanel.
  // Returns "" (empty string) when "no color" is chosen; the caller
  // decides whether that means "inherit" or "clear override".

  import { palette as presets } from "./palette";

  interface Props {
    value: string;
    onChange: (next: string) => void;
    label?: string;
  }
  let { value, onChange, label = "Color tag" }: Props = $props();

  // Track the textbox state separately. Re-init only when the prop's
  // identity (kind of value) actually changes - never overwrite a
  // partial input the user is still typing.
  let customVal = $state("");
  let lastSyncedValue = $state<string | null>(null);
  $effect(() => {
    if (value === lastSyncedValue) return;
    lastSyncedValue = value;
    customVal =
      value && !presets.some((p) => p.hex.toLowerCase() === value.toLowerCase())
        ? value
        : "";
  });

  function pickPreset(hex: string) {
    customVal = "";
    lastSyncedValue = hex;
    onChange(hex);
  }
  function clearColor() {
    customVal = "";
    lastSyncedValue = "";
    onChange("");
  }
  // Apply on every keystroke as soon as the entry parses as a valid
  // hex. Auto-prepends "#" when missing so pasted "a9a9a9" works the
  // same as "#a9a9a9". onchange/onblur were unreliable because the
  // Save button captured the click before the input's blur landed in
  // some WebView2 builds, so the parent's editing state never picked
  // up the override before the save IPC fired.
  function tryApply() {
    let v = customVal.trim();
    if (!v) return;
    if (v[0] !== "#") v = "#" + v;
    if (!/^#[0-9a-fA-F]{3,8}$/.test(v)) return;
    lastSyncedValue = v;
    onChange(v);
  }
</script>

<div class="picker">
  {#if label}<span class="lbl">{label}</span>{/if}
  <div class="row">
    <button
      class="swatch none"
      class:active={!value}
      onclick={clearColor}
      title="No color"
    >∅</button>
    {#each presets as p (p.hex)}
      <button
        class="swatch"
        class:active={value.toLowerCase() === p.hex.toLowerCase()}
        style="background: {p.hex}"
        onclick={() => pickPreset(p.hex)}
        title={p.name}
      ></button>
    {/each}
    <input
      class="hex"
      type="text"
      placeholder="#hex"
      bind:value={customVal}
      oninput={tryApply}
      onchange={tryApply}
      onblur={tryApply}
    />
    {#if value}
      <span class="preview" style="background: {value}"></span>
    {/if}
  </div>
</div>

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    margin: 0.4rem 0;
  }
  .lbl {
    font-size: 0.78rem;
    color: var(--text-muted);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    flex-wrap: wrap;
  }
  .swatch {
    width: 22px;
    height: 22px;
    border: 1px solid var(--border);
    border-radius: 3px;
    cursor: pointer;
    padding: 0;
    color: var(--text-subtle);
    font-size: 0.75rem;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .swatch:hover { border-color: var(--accent); }
  .swatch.active { outline: 2px solid var(--text); outline-offset: 1px; }
  .swatch.none { background: var(--bg-panel); }
  .hex {
    background: var(--bg-panel);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0.25rem 0.4rem;
    width: 7rem;
    font: inherit;
    font-size: 0.78rem;
  }
  .hex:focus { outline: 1px solid var(--accent); border-color: var(--accent); }
  .preview {
    width: 22px; height: 22px;
    border-radius: 3px;
    border: 1px solid var(--border);
  }
</style>
