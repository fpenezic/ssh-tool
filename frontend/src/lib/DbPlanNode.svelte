<script lang="ts">
  // One node of an EXPLAIN plan. A PostgreSQL node (it has "Node Type")
  // shows as one line - what it does, on what, its estimated cost and rows,
  // its conditions - with its child plans under it. Anything else (MySQL's
  // JSON plan) is a collapsible tree whose table and access lines stand out.
  import DbPlanNode from "./DbPlanNode.svelte";

  interface Props {
    node: unknown;
    label?: string;
    depth?: number;
  }
  let { node, label = "", depth = 0 }: Props = $props();

  let open = $state(true);
  const obj = $derived(node !== null && typeof node === "object" ? (node as Record<string, unknown>) : null);
  const pg = $derived(obj && typeof obj["Node Type"] === "string" ? obj : null);

  const PG_DETAIL = ["Filter", "Index Cond", "Recheck Cond", "Hash Cond", "Merge Cond", "Join Filter", "Sort Key", "Group Key", "Cache Key"];
  function pgTitle(n: Record<string, unknown>): string {
    let t = String(n["Node Type"]);
    if (n["Join Type"] && t !== "Hash") t = `${n["Join Type"]} ${t}`;
    const on = n["Relation Name"] ? ` on ${n["Schema"] ? n["Schema"] + "." : ""}${n["Relation Name"]}${n["Alias"] && n["Alias"] !== n["Relation Name"] ? " " + n["Alias"] : ""}` : "";
    const idx = n["Index Name"] ? ` using ${n["Index Name"]}` : "";
    return t + idx + on;
  }
  function pgCost(n: Record<string, unknown>): string {
    return `cost ${n["Startup Cost"]}..${n["Total Cost"]} · rows ${n["Plan Rows"]} · width ${n["Plan Width"]}`;
  }
  const isArray = $derived(Array.isArray(node));
  const entries = $derived(obj && !pg ? Object.entries(obj) : []);
  // MySQL: the keys that say what touches which table, and how.
  const strong = (k: string) => /^(table_name|access_type|key|rows_examined_per_scan|attached_condition|using_filesort|using_temporary_table)$/.test(k);
</script>

{#if pg}
  <div class="pg" style:padding-left="{depth * 1.1}rem">
    <div class="line">
      {#if (pg["Plans"] as unknown[] | undefined)?.length}
        <button class="tw" onclick={() => (open = !open)}>{open ? "-" : "+"}</button>
      {:else}<span class="tw"></span>{/if}
      <span class="what">{pgTitle(pg)}</span>
      <span class="cost">{pgCost(pg)}</span>
    </div>
    {#each PG_DETAIL as k (k)}
      {#if pg[k] !== undefined}
        <div class="detail" style:padding-left="1.4rem">{k}: <span class="mono">{Array.isArray(pg[k]) ? (pg[k] as unknown[]).join(", ") : String(pg[k])}</span></div>
      {/if}
    {/each}
  </div>
  {#if open}
    {#each (pg["Plans"] as unknown[] | undefined) ?? [] as c, i (i)}
      <DbPlanNode node={c} depth={depth + 1} />
    {/each}
  {/if}
{:else if isArray}
  {#each node as unknown[] as c, i (i)}
    <DbPlanNode node={c} label={label ? `${label}[${i}]` : ""} {depth} />
  {/each}
{:else if obj}
  <div class="gen" style:padding-left="{depth * 1.1}rem">
    {#if label}
      <button class="tw" onclick={() => (open = !open)}>{open ? "-" : "+"}</button><span class="key">{label}</span>
    {/if}
  </div>
  {#if open || !label}
    {#each entries as [k, v] (k)}
      {#if v !== null && typeof v === "object"}
        <DbPlanNode node={v} label={k} depth={depth + (label ? 1 : 0)} />
      {:else}
        <div class="leaf" class:strong={strong(k)} style:padding-left="{(depth + (label ? 1 : 0)) * 1.1 + 1.4}rem">
          <span class="key">{k}</span>: <span class="mono">{String(v)}</span>
        </div>
      {/if}
    {/each}
  {/if}
{:else}
  <div class="leaf" style:padding-left="{depth * 1.1}rem"><span class="mono">{String(node)}</span></div>
{/if}

<style>
  .line { display: flex; align-items: baseline; gap: 0.5rem; }
  .tw { width: 1rem; flex: 0 0 1rem; padding: 0; border: 0; background: none; color: var(--overlay1); cursor: pointer; font-family: var(--mono-font, ui-monospace, monospace); }
  .what { font-weight: 600; color: var(--text); }
  .cost { color: var(--subtext0); font-size: 0.72rem; }
  .detail, .leaf { color: var(--subtext1); font-size: 0.74rem; }
  .leaf.strong { color: var(--text); font-weight: 600; }
  .key { color: var(--blue); }
  .mono { font-family: var(--mono-font, ui-monospace, monospace); }
  .pg, .gen, .leaf, .detail { line-height: 1.55; }
</style>
