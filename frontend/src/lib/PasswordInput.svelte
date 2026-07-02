<script lang="ts">
  // Password field with an inline reveal (unmask) toggle. Drop-in
  // replacement for `<input type="password" bind:value={x} />`. The
  // eye button flips type between "password" and "text". Bindable
  // value so call sites keep `bind:value`.
  import type { FullAutoFill } from "svelte/elements";
  import { IconEye, IconEyeOff } from "./iconMap";

  type Props = {
    value: string;
    placeholder?: string;
    autocomplete?: FullAutoFill;
    spellcheck?: boolean;
    mono?: boolean;
    disabled?: boolean;
    id?: string;
    onkeydown?: (e: KeyboardEvent) => void;
    onblur?: (e: FocusEvent) => void;
  };

  let {
    value = $bindable(""),
    placeholder = "",
    autocomplete = "off",
    spellcheck = false,
    mono = false,
    disabled = false,
    id,
    onkeydown,
    onblur,
  }: Props = $props();

  let shown = $state(false);
</script>

<div class="pw-wrap" class:mono>
  <input
    {id}
    type={shown ? "text" : "password"}
    bind:value
    {placeholder}
    {autocomplete}
    {spellcheck}
    {disabled}
    {onkeydown}
    {onblur}
  />
  <button
    type="button"
    class="reveal"
    title={shown ? "Hide" : "Show"}
    aria-label={shown ? "Hide password" : "Show password"}
    onclick={() => (shown = !shown)}
    tabindex="-1"
  >
    {#if shown}<IconEyeOff size={15} />{:else}<IconEye size={15} />{/if}
  </button>
</div>

<style>
  .pw-wrap { position: relative; display: flex; align-items: stretch; }
  /* Self-contained field styling so the component looks like every other
     input regardless of call site (it replaced raw <input type=password>
     fields that carried their own per-site background/border/focus rules). */
  .pw-wrap input {
    flex: 1;
    width: 100%;
    min-width: 0;
    padding: 0.4rem 0.55rem;
    padding-right: 2rem;
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    font: inherit;
  }
  .pw-wrap input:focus {
    outline: 1px solid var(--blue);
    border-color: var(--blue);
  }
  .pw-wrap.mono input { font-family: monospace; font-size: 0.78rem; }
  .reveal {
    position: absolute;
    right: 1px;
    top: 1px;
    bottom: 1px;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 1.9rem;
    background: transparent;
    border: 0;
    border-radius: 0 3px 3px 0;
    color: var(--overlay0);
    cursor: pointer;
    padding: 0;
  }
  .reveal:hover { color: var(--text); }
</style>
