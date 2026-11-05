<script lang="ts">
  // "Referenced by" for the picked row: the tables whose foreign keys point
  // at it, each with how many of their rows do (counted up to a cap, so a
  // huge child table is not scanned), and a click to open those rows.

  import { api, type DbCond, type DbForeignKey, type DbResultColumn } from "./api";
  import { errMsg } from "./connectErrors";

  interface Props {
    sessionId: string;
    database: string;
    schema: string;
    name: string;
    columns: DbResultColumn[];
    row: (string | null)[] | null;
    onOpen: (t: { database: string; schema: string; name: string; where: DbCond[] }) => void;
  }
  let { sessionId, database, schema, name, columns, row, onOpen }: Props = $props();

  const CAP = 50;
  const BINARY = /binary|blob|bytea|\braw\b|bit data|image/i;

  let keys = $state<DbForeignKey[] | null>(null);
  let keysErr = $state<string | null>(null);
  $effect(() => {
    const t = `${database}/${schema}/${name}`;
    keys = null;
    keysErr = null;
    api.dbReferencedBy(sessionId, database, schema, name).then(
      (k) => { if (t === `${database}/${schema}/${name}`) keys = k; },
      (e) => { keysErr = errMsg(e); },
    );
  });

  // The rows of key k that point at row (null: the row has a NULL key part).
  function whereFor(k: DbForeignKey, r: (string | null)[]): DbCond[] | null {
    const out: DbCond[] = [];
    for (let i = 0; i < (k.columns ?? []).length; i++) {
      const ci = columns.findIndex((c) => c.name === k.ref_columns?.[i]);
      if (ci < 0 || r[ci] === null) return null;
      out.push({ column: k.columns![i], value: r[ci], hex: BINARY.test(columns[ci].type) || undefined });
    }
    return out;
  }

  interface Ref { key: DbForeignKey; where: DbCond[] | null; count: number | null; err?: string }
  let refs = $state<Ref[]>([]);
  let timer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const r = row;
    const ks = keys;
    clearTimeout(timer);
    if (!r || !ks) {
      refs = [];
      return;
    }
    refs = ks.map((k) => ({ key: k, where: whereFor(k, r), count: null }));
    // Arrow keys move the pick quickly: count once it settles.
    // One count at a time: a table referenced by many keys would otherwise
    // ask for all of them at once from the small tree pool.
    timer = setTimeout(async () => {
      for (let i = 0; i < refs.length; i++) {
        const ref = refs[i];
        if (!ref.where) continue;
        if (row !== r) return; // the pick moved on
        try {
          const p = await api.dbBrowse(sessionId, ref.key.database ?? database, ref.key.schema ?? "", ref.key.table ?? "", ref.where, [], 0, CAP + 1);
          if (row === r && refs[i]) refs[i].count = (p.rows ?? []).length;
        } catch (e) {
          if (row === r && refs[i]) refs[i].err = errMsg(e);
        }
      }
    }, 250);
    return () => clearTimeout(timer);
  });
</script>

<div class="refs">
  <span class="title">Referenced by</span>
  {#if keysErr}
    <span class="err">{keysErr}</span>
  {:else if keys === null}
    <span class="muted">...</span>
  {:else if keys.length === 0}
    <span class="muted">no foreign key points at {name}</span>
  {:else if !row}
    <span class="muted">pick a row to see what points at it ({keys.length} key{keys.length === 1 ? "" : "s"})</span>
  {:else}
    {#each refs as ref (ref.key.database + "/" + ref.key.schema + "/" + ref.key.table + "/" + ref.key.name)}
      <button
        disabled={!ref.where || ref.count === 0}
        title={`${ref.key.table}.${(ref.key.columns ?? []).join(", ")} -> ${name} (${ref.key.name})`}
        onclick={() => ref.where && onOpen({ database: ref.key.database ?? database, schema: ref.key.schema ?? "", name: ref.key.table ?? "", where: ref.where })}
      >
        {ref.key.table}<span class="n">{ref.err ? "!" : ref.where === null ? "-" : ref.count === null ? "..." : ref.count > CAP ? `${CAP}+` : ref.count}</span>
      </button>
    {/each}
  {/if}
</div>

<style>
  .refs { display: flex; align-items: center; gap: 0.35rem; flex-wrap: wrap; padding: 0.3rem 0.5rem; border-top: 1px solid var(--surface0); font-size: 0.75rem; }
  .title { color: var(--subtext0); font-weight: 600; }
  .muted { color: var(--overlay1); }
  .err { color: var(--red); }
  button {
    display: inline-flex; align-items: center; gap: 0.35rem; font-size: 0.75rem; padding: 0.12rem 0.45rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; cursor: pointer;
  }
  button:disabled { opacity: 0.5; cursor: default; }
  .n { color: var(--blue); font-variant-numeric: tabular-nums; }
</style>
