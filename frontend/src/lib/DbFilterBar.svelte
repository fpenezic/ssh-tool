<script lang="ts">
  // The Data view's filter: conditions on columns, applied on the server
  // (every one must hold). Values are sent as parameters, never spliced
  // into SQL. Enter or Apply runs it; Clear drops it.

  import { untrack } from "svelte";
  import type { DbCond, DbCondOp, DbResultColumn } from "./api";
  import { isBinaryType } from "./dbSql";
  import { IconX } from "./iconMap";

  interface Props {
    engine: string;
    columns: DbResultColumn[];
    applied: DbCond[];
    onApply: (conds: DbCond[]) => void;
    onClose: () => void;
  }
  let { engine, columns, applied, onApply, onClose }: Props = $props();

  type Op = DbCondOp | "IS NULL" | "IS NOT NULL";
  interface Row { column: string; op: Op; value: string }
  const OPS: Op[] = ["=", "<>", "<", ">", "<=", ">=", "LIKE", "NOT LIKE", "IS NULL", "IS NOT NULL"];


  function fromConds(cs: DbCond[]): Row[] {
    return cs.map((c) => ({
      column: c.column,
      op: c.value === null ? (c.op === "<>" ? "IS NOT NULL" : "IS NULL") : ((c.op || "=") as Op),
      value: c.value ?? "",
    }));
  }
  let rows = $state<Row[]>([]);
  // The form follows what is applied (a new table, Clear), not its own
  // edits: only applied is read here.
  $effect(() => {
    const from = fromConds(applied);
    const first = untrack(() => columns[0]?.name ?? "");
    rows = from.length ? from : [{ column: first, op: "=", value: "" }];
  });

  function toConds(): DbCond[] {
    return rows
      .filter((r) => r.column)
      .map((r) => {
        const type = columns.find((c) => c.name === r.column)?.type ?? "";
        if (r.op === "IS NULL") return { column: r.column, value: null };
        if (r.op === "IS NOT NULL") return { column: r.column, value: null, op: "<>" as DbCondOp };
        return { column: r.column, value: r.value, op: r.op as DbCondOp, hex: isBinaryType(engine, type) || undefined };
      });
  }
  function apply() {
    onApply(toConds());
  }
  function clear() {
    rows = [{ column: columns[0]?.name ?? "", op: "=", value: "" }];
    onApply([]);
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      apply();
    }
  }
</script>

<div class="fbar">
  {#each rows as r, i (i)}
    <div class="frow">
      <select bind:value={r.column} title="Column">
        {#each columns as c (c.name)}<option value={c.name}>{c.name}</option>{/each}
      </select>
      <select bind:value={r.op} title="Comparison (LIKE: % any text, _ one character)">
        {#each OPS as o (o)}<option value={o}>{o}</option>{/each}
      </select>
      {#if r.op !== "IS NULL" && r.op !== "IS NOT NULL"}
        <input bind:value={r.value} onkeydown={onKey} placeholder="value" spellcheck="false" />
      {/if}
      <button class="ic" title="Remove this condition" onclick={() => (rows = rows.filter((_, k) => k !== i))}><IconX size={12} /></button>
    </div>
  {/each}
  <div class="factions">
    <button onclick={() => (rows = [...rows, { column: columns[0]?.name ?? "", op: "=", value: "" }])}>+ Condition</button>
    <button class="primary" onclick={apply}>Apply</button>
    <button onclick={clear} disabled={!applied.length}>Clear</button>
    <span class="hint">All conditions must hold. Values go to the server as parameters.</span>
    <span class="spacer"></span>
    <button class="ic" title="Hide the filter (it stays applied)" onclick={onClose}><IconX size={13} /></button>
  </div>
</div>

<style>
  .fbar { display: flex; flex-direction: column; gap: 0.25rem; padding: 0.35rem 0.5rem; border-bottom: 1px solid var(--surface0); font-size: var(--fs-sm); }
  .frow, .factions { display: flex; align-items: center; gap: 0.3rem; }
  select, input {
    font-size: var(--fs-sm); padding: 0.15rem 0.3rem; background: var(--base); color: var(--text);
    border: 1px solid var(--surface1); border-radius: var(--r-sm);
  }
  input { flex: 0 1 18rem; font-family: var(--mono-font, ui-monospace, monospace); }
  button {
    display: inline-flex; align-items: center; gap: 0.25rem; font-size: var(--fs-sm); padding: 0.15rem 0.5rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: var(--r-sm); cursor: pointer;
  }
  button:disabled { opacity: 0.45; cursor: default; }
  button.primary { border-color: var(--blue); }
  button.ic { padding: 0.15rem 0.25rem; background: none; border-color: transparent; color: var(--overlay1); }
  button.ic:hover { color: var(--text); }
  .hint { color: var(--subtext0); font-size: var(--fs-xs); }
  .spacer { flex: 1 1 auto; }
</style>
