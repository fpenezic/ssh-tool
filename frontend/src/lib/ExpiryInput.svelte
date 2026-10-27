<script lang="ts">
  // Expiry date picker that also takes "in N days": typing a number of
  // days (or a quick 30 / 90 / 365) sets the date, and picking a date
  // shows how many days away it is. value is YYYY-MM-DD, "" = no expiry.
  import { dateInDays, daysUntil } from "./credExpiry";

  let { value = $bindable("") }: { value?: string } = $props();

  const days = $derived(value ? daysUntil(value) : null);

  function setDays(raw: string) {
    const n = parseInt(raw, 10);
    if (raw.trim() === "") value = "";
    else if (!isNaN(n) && n >= 0 && n <= 36500) value = dateInDays(n);
  }
</script>

<div class="exp">
  <input type="date" bind:value />
  <label class="days">in
    <input type="number" min="0" max="36500" value={days ?? ""} placeholder="N"
      oninput={(e) => setDays((e.target as HTMLInputElement).value)} />
    days
  </label>
  {#each [30, 90, 365] as n (n)}
    <button type="button" class="chip" class:on={days === n} onclick={() => (value = dateInDays(n))}>{n}d</button>
  {/each}
  {#if value}
    <button type="button" class="chip" onclick={() => (value = "")} title="No expiry">Clear</button>
  {/if}
</div>

<style>
  .exp { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; }
  .days { display: flex; align-items: center; gap: 0.3rem; font-size: 0.85rem; color: var(--subtext0); }
  .days input { width: 5rem; }
  /* Own styles: the editors' input / button rules are scoped to them and
     do not reach into this component, which left the date field, the day
     count and the chips in the browser's default look. Same values as the
     credential editor's fields and buttons. */
  input {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  input:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  input[type="date"]::-webkit-calendar-picker-indicator { cursor: pointer; opacity: 0.7; }
  input[type="date"]::-webkit-calendar-picker-indicator:hover { opacity: 1; }
  .chip {
    background: var(--surface0); color: var(--text);
    border: 1px solid transparent; border-radius: 3px;
    padding: 0.3rem 0.55rem; font: inherit; font-size: 0.8rem;
    cursor: pointer;
  }
  .chip:hover { background: var(--surface1); }
  .chip.on { border-color: var(--blue); color: var(--blue); }
</style>
