<script lang="ts">
  // "Match my connections": pairs the user's own connections with this
  // catalog's records (same host and port) and moves what was theirs onto
  // the record. The backend plans it again on Apply; this only shows it
  // and collects the ticks and picks.
  import { api, type CatalogMatchPreview, type CatalogMatchChange } from "./api";
  import { tree } from "./stores.svelte";
  import { workspaces } from "./workspaces.svelte";
  import { toast } from "./toast.svelte.ts";
  import { untrack } from "svelte";
  import { IconX, IconAlertTriangle, IconChevronRight, IconChevronDown } from "./iconMap";

  let { catalogId, catalogName, onclose }: { catalogId: string; catalogName: string; onclose: () => void } = $props();

  let pv = $state<CatalogMatchPreview | null>(null);
  let picks = $state<Record<string, string>>({});
  let off = $state<Record<string, boolean>>({});
  let busy = $state(false);
  let err = $state("");
  let filter = $state("");
  let confirmUndo = $state(false);

  // Only the newest preview counts: picks made in quick succession must
  // not be overwritten by an older answer arriving last. Apply waits for it.
  let seq = 0;
  let loading = $state(false);
  async function load() {
    const my = ++seq;
    loading = true;
    err = "";
    try {
      const r = await api.catalogMatchPreview(catalogId, picks);
      if (my === seq) pv = r;
    } catch (e) {
      if (my === seq) err = String(e);
    } finally {
      if (my === seq) loading = false;
    }
  }
  $effect(() => { void catalogId; untrack(() => void load()); });

  const pairs = $derived(pv?.pairs ?? []);
  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(q ? pairs.filter((p) => `${p.local.name} ${p.local.path} ${p.catalog.name} ${p.catalog.path} ${p.catalog.hostname}`.toLowerCase().includes(q)) : pairs);
  const picked = $derived(pairs.filter((p) => !off[p.local.id]));

  // Grouped by the catalog folder the pairs land in. What lands on that
  // folder (one value for all its connections) and slot bindings (one for
  // the whole catalog) show once, not on every row.
  type Group = { path: string; pairs: typeof pairs; folder: CatalogMatchChange[]; each: CatalogMatchChange[] };
  const groups = $derived.by(() => {
    const m = new Map<string, Group>();
    for (const p of shown) {
      let g = m.get(p.catalog.path);
      if (!g) { g = { path: p.catalog.path, pairs: [], folder: [], each: [] }; m.set(p.catalog.path, g); }
      g.pairs.push(p);
      for (const c of p.changes ?? []) {
        if (c.where === "folder" && !g.folder.some((x) => x.field === c.field)) g.folder.push(c);
      }
    }
    // A My settings value every row of the group gets shows once on the
    // group ("On each connection") instead of on every row.
    for (const g of m.values()) {
      if (g.pairs.length < 2) continue;
      const first = (g.pairs[0].changes ?? []).filter((c) => c.where === "connection");
      g.each = first.filter((c) => g.pairs.every((p) => (p.changes ?? []).some((x) => x.where === "connection" && x.field === c.field && x.value === c.value)));
    }
    return [...m.values()].sort((x, y) => x.path.localeCompare(y.path));
  });
  const eachKey = $derived.by(() => {
    const k = new Map<string, Set<string>>();
    for (const g of groups) k.set(g.path, new Set(g.each.map((c) => c.field + "|" + c.value)));
    return k;
  });
  const slotLines = $derived.by(() => {
    const seen = new Map<string, CatalogMatchChange>();
    for (const p of pairs) for (const c of p.changes ?? []) if (c.where === "slot") seen.set(c.field + "|" + c.value, c);
    return [...seen.values()];
  });
  const rowChanges = (p: (typeof pairs)[number]) => {
    const each = eachKey.get(p.catalog.path);
    return (p.changes ?? []).filter((c) => c.where !== "folder" && c.where !== "slot" && !(c.where === "connection" && each?.has(c.field + "|" + c.value)));
  };
  // Big lists start folded; a filter opens what it finds.
  let openGroups = $state<Record<string, boolean>>({});
  const foldByDefault = $derived(pairs.length > 25 && groups.length > 1);
  const isOpen = (path: string) => !!q || (openGroups[path] ?? !foldByDefault);
  function groupState(g: Group): "all" | "none" | "some" {
    const on = g.pairs.filter((p) => !off[p.local.id]).length;
    return on === 0 ? "none" : on === g.pairs.length ? "all" : "some";
  }
  function setGroup(g: Group, on: boolean) {
    const next = { ...off };
    for (const p of g.pairs) next[p.local.id] = !on;
    off = next;
  }
  function summary(g: Group): string {
    let fw = 0, pw = 0;
    for (const p of g.pairs) for (const c of p.changes ?? []) {
      if (c.where === "forward") fw++;
      if (c.field === "Saved password" || c.field === "VNC password") pw++;
    }
    return [`${g.pairs.length} connection${g.pairs.length === 1 ? "" : "s"}`, fw ? `${fw} forward${fw === 1 ? "" : "s"}` : "", pw ? `${pw} password${pw === 1 ? "" : "s"}` : ""].filter(Boolean).join(", ");
  }
  let showDups = $state(false);

  // Several catalog connections fit: one choice of catalog folder settles
  // every such connection with an option there.
  const ambPaths = $derived.by(() => {
    const n = new Map<string, number>();
    for (const a of pv?.ambiguous ?? []) for (const p of new Set(a.options.map((o) => o.path))) n.set(p, (n.get(p) ?? 0) + 1);
    return [...n.entries()].sort((x, y) => y[1] - x[1] || x[0].localeCompare(y[0]));
  });
  function preferPath(path: string) {
    if (!path) return;
    const next = { ...picks };
    for (const a of pv?.ambiguous ?? []) {
      const o = a.options.find((x) => x.path === path);
      if (o) next[a.local.id] = o.id;
    }
    picks = next;
    void load();
  }

  function pick(localId: string, catalogConnId: string) {
    picks = { ...picks, [localId]: catalogConnId };
    void load();
  }
  function setAll(on: boolean) {
    const next = { ...off };
    for (const p of shown) next[p.local.id] = !on;
    off = next;
  }

  const whereLabel: Record<string, string> = {
    slot: "binds the slot for the whole catalog",
    folder: "on the catalog folder, for all its connections",
    connection: "My settings",
    record: "the catalog has none",
    local: "",
    forward: "",
    reference: "moves to the catalog connection",
  };
  function chip(c: CatalogMatchChange): string {
    return c.value ? `${c.field}: ${c.value}` : c.field;
  }

  // The folders the copies came from go too once they are empty (Undo
  // brings them back).
  let removeEmpty = $state(true);

  async function apply() {
    busy = true;
    err = "";
    try {
      const m: Record<string, string> = {};
      for (const p of picked) m[p.local.id] = p.catalog.id;
      const r = await api.catalogMatchApply(catalogId, m, removeEmpty);
      await Promise.all([tree.load(), workspaces.load()]);
      const extra = [r.bindings ? `${r.bindings} slot${r.bindings === 1 ? "" : "s"} bound` : "", r.forwards ? `${r.forwards} forward${r.forwards === 1 ? "" : "s"}` : "",
        r.passwords ? `${r.passwords} password${r.passwords === 1 ? "" : "s"}` : "", r.passwords_skipped ? `${r.passwords_skipped} password${r.passwords_skipped === 1 ? "" : "s"} not copied (vault locked)` : "",
        r.folders_removed ? `${r.folders_removed} empty folder${r.folders_removed === 1 ? "" : "s"} removed` : ""].filter(Boolean).join(", ");
      toast.info(`${r.pairs} connection${r.pairs === 1 ? "" : "s"} matched to ${catalogName}${extra ? " - " + extra : ""}. Your copies are in "${r.folder}".`);
      onclose();
    } catch (e) {
      // Refresh first (it clears the error), then show why Apply failed.
      const why = String(e);
      await load();
      err = why;
    } finally {
      busy = false;
    }
  }

  async function undo() {
    busy = true;
    err = "";
    try {
      const r = await api.catalogMatchUndo(catalogId);
      await Promise.all([tree.load(), workspaces.load()]);
      toast.info(`Match undone: ${r.pairs} connection${r.pairs === 1 ? "" : "s"} back as they were${r.kept ? `; ${r.kept} value${r.kept === 1 ? "" : "s"} changed since were kept` : ""}.`);
      confirmUndo = false;
      picks = {};
      await load();
    } catch (e) {
      err = String(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="overlay" role="presentation">
  <div class="modal" role="dialog" aria-modal="true" tabindex="-1" onkeydown={(e) => { if (e.key === "Escape" && !busy) onclose(); }}>
    <header>
      <h2>Match my connections to {catalogName}</h2>
      <button class="close" onclick={onclose} title="Close (Esc)" disabled={busy}><IconX size={14} /></button>
    </header>
    <p class="hint intro" title="Credential, user, network, jump host, logins, saved passwords, forwards, workspaces and snippets go over; the catalog's own settings stay. Profile sync takes the result to your other devices, so match on one of them.">Your connections to the same host and port as a catalog connection. What is yours moves onto the catalog connection; your copies go to "Matched to {catalogName}" and keep working. Undo puts it back.</p>

    {#if !pv && !err}
      <p class="hint">Looking for matches...</p>
    {:else if pv}
      {#if pairs.length === 0 && (pv.ambiguous ?? []).length === 0}
        <p class="hint">None of your connections matches a connection of this catalog{pv.unmatched ? ` (${pv.unmatched} checked)` : ""}.</p>
      {/if}

      {#if slotLines.length > 0}
        <div class="slots">
          <span class="label">Slots bound for the whole catalog</span>
          <div class="chips">{#each slotLines as c}<span class="chip slot">{chip(c)}</span>{/each}</div>
        </div>
      {/if}

      {#if pairs.length > 0}
        <div class="bar">
          <span class="label">{picked.length} of {pairs.length} to match{groups.length > 1 ? ` in ${groups.length} folders` : ""}</span>
          {#if pairs.length > 8}<input class="filter" placeholder="Filter by name, host or folder..." bind:value={filter} />{/if}
          <button class="linklike" onclick={() => setAll(true)}>All</button>
          <button class="linklike" onclick={() => setAll(false)}>None</button>
          {#if groups.length > 1 && !q}
            <button class="linklike" onclick={() => { const n: Record<string, boolean> = {}; const anyClosed = groups.some((g) => !isOpen(g.path)); for (const g of groups) n[g.path] = anyClosed; openGroups = n; }}>{groups.some((g) => !isOpen(g.path)) ? "Expand all" : "Collapse all"}</button>
          {/if}
        </div>
        <div class="list">
          {#each groups as g (g.path)}
            {@const st = groupState(g)}
            <div class="group">
              <div class="ghead">
                <button class="tw" onclick={() => (openGroups = { ...openGroups, [g.path]: !isOpen(g.path) })} title={isOpen(g.path) ? "Collapse" : "Expand"}>
                  {#if isOpen(g.path)}<IconChevronDown size={12} />{:else}<IconChevronRight size={12} />{/if}
                </button>
                <input type="checkbox" checked={st === "all"} indeterminate={st === "some"} disabled={busy} onchange={(e) => setGroup(g, (e.currentTarget as HTMLInputElement).checked)} title="Match every connection of this folder" />
                <button class="gname" onclick={() => (openGroups = { ...openGroups, [g.path]: !isOpen(g.path) })}>{g.path || catalogName}</button>
                <span class="dim gsum">{summary(g)}</span>
              </div>
              {#if g.folder.length > 0}
                <div class="chips gfolder" title="Set once on the catalog folder: every connection in it gets it">
                  <span class="dim">On the folder:</span>
                  {#each g.folder as c}<span class="chip folder">{chip(c)}</span>{/each}
                </div>
              {/if}
              {#if g.each.length > 0}
                <div class="chips gfolder" title="Set in My settings of each matched connection; not every connection of this folder is matched, so it does not go on the folder">
                  <span class="dim">On each connection:</span>
                  {#each g.each as c}<span class="chip">{chip(c)}</span>{/each}
                </div>
              {/if}
              {#if isOpen(g.path)}
                {#each g.pairs as p (p.local.id)}
                  {@const rc = rowChanges(p)}
                  <label class="pair" class:dim={off[p.local.id]}>
                    <input type="checkbox" checked={!off[p.local.id]} disabled={busy} onchange={(e) => (off = { ...off, [p.local.id]: !(e.currentTarget as HTMLInputElement).checked })} />
                    <div class="body">
                      <div class="names">
                        <span title={p.local.path}>{p.local.name}</span>
                        <span class="arrow">to</span>
                        <span title={p.catalog.hostname}>{p.catalog.name}</span>
                        {#if p.local.path}<span class="dim path" title="Where your copy is now">from {p.local.path}</span>{/if}
                      </div>
                      {#if rc.length > 0}
                        <div class="chips">
                          {#each rc as c}<span class="chip" title={whereLabel[c.where] ?? ""}>{chip(c)}</span>{/each}
                        </div>
                      {/if}
                    </div>
                  </label>
                {/each}
              {/if}
            </div>
          {/each}
        </div>
      {/if}

      {#if (pv.ambiguous ?? []).length > 0}
        <div class="bar">
          <span class="label">{(pv.ambiguous ?? []).length} fit several catalog connections; pick one</span>
          {#if (pv.ambiguous ?? []).length > 1 && ambPaths.length > 1}
            <select class="prefer" onchange={(e) => preferPath((e.currentTarget as HTMLSelectElement).value)} disabled={busy}>
              <option value="">Prefer a catalog folder...</option>
              {#each ambPaths as [path, n] (path)}<option value={path}>{path} ({n})</option>{/each}
            </select>
          {/if}
        </div>
        <div class="list short">
          {#each pv.ambiguous ?? [] as a (a.local.id)}
            <div class="pair">
              <div class="body">
                <div class="names"><span>{a.local.name}</span><span class="dim path">{a.local.hostname}{a.local.username ? `, user ${a.local.username}` : ""}</span></div>
                <select onchange={(e) => pick(a.local.id, (e.currentTarget as HTMLSelectElement).value)} disabled={busy}>
                  <option value="">Pick...</option>
                  {#each a.options as o (o.id)}<option value={o.id}>{o.name} - {o.path}{o.username ? ` (${o.username})` : ""}</option>{/each}
                </select>
              </div>
            </div>
          {/each}
        </div>
      {/if}

      {#if (pv.duplicates ?? []).length > 0}
        <button class="linklike dups" onclick={() => (showDups = !showDups)}>
          {(pv.duplicates ?? []).length} more cop{(pv.duplicates ?? []).length === 1 ? "y" : "ies"} of connections above stay where they are {showDups ? "(hide)" : "(show)"}
        </button>
        {#if showDups}
          <div class="list short">
            {#each pv.duplicates ?? [] as d (d.local.id)}
              <div class="pair">
                <div class="body names">
                  <span>{d.local.name}</span><span class="dim path">{d.local.path}</span>
                  <button class="linklike" disabled={busy} onclick={() => pick(d.local.id, d.catalog_conn_id)}>Match this one instead</button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      {/if}
    {/if}

    {#if err}<p class="err"><IconAlertTriangle size={13} /> {err}</p>{/if}
    {#if pv && (pv.pairs?.length ?? 0) > 0}
      <label class="opt" title="After moving your copies, remove the local folders they leave with nothing in them. Undo brings them back.">
        <input type="checkbox" bind:checked={removeEmpty} disabled={busy} /> Remove folders left empty
      </label>
    {/if}
    <footer>
      {#if pv && pv.last_run}
        {#if confirmUndo}
          <span class="label undo-q">Undo the match of {new Date(pv.last_run).toLocaleString()} ({pv.last_run_pairs})?</span>
          <button onclick={() => (confirmUndo = false)} disabled={busy}>No</button>
          <button class="danger" onclick={undo} disabled={busy}>Undo</button>
        {:else}
          <button class="left" onclick={() => (confirmUndo = true)} disabled={busy} title="Put the last match back: your copies, forwards, workspaces and My settings as they were">Undo last match</button>
        {/if}
      {/if}
      <button onclick={onclose} disabled={busy}>Cancel</button>
      <button class="primary" onclick={apply} disabled={busy || loading || picked.length === 0}>{busy ? "Matching..." : `Match ${picked.length}`}</button>
    </footer>
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(17, 17, 27, 0.6); z-index: 1000; display: flex; align-items: center; justify-content: center; }
  .modal { background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: var(--r-md); width: min(820px, 94vw); max-height: 90vh; display: flex; flex-direction: column; gap: 0.6rem; padding: 1rem 1.2rem; box-shadow: 0 8px 30px rgba(0,0,0,0.5); }
  header { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  header h2 { margin: 0; font-size: var(--fs-md); overflow-wrap: anywhere; }
  .close { background: transparent; border: 0; color: var(--subtext0); cursor: pointer; padding: 0.15rem 0.35rem; border-radius: var(--r-sm); }
  .hint, .dim { color: var(--subtext0); font-size: var(--fs-sm); margin: 0; line-height: 1.45; }
  .label { font-size: var(--fs-sm); color: var(--subtext1); }
  .bar { display: flex; align-items: center; gap: 10px; }
  .filter { margin-left: auto; max-width: 220px; }
  /* The pair list takes what the window has left; the side lists stay small. */
  .list { display: flex; flex-direction: column; gap: 3px; overflow-y: auto; min-height: 120px; flex: 1 1 auto; }
  .list.short { flex: 0 1 auto; min-height: 48px; max-height: 18vh; }
  .pair { display: flex; align-items: flex-start; gap: 8px; background: var(--mantle); border: 1px solid var(--surface0); border-radius: var(--r-sm); padding: 5px 8px; font-size: var(--fs-sm); }
  label.pair { cursor: pointer; }
  .pair.dim { opacity: 0.55; }
  .body { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1; }
  .names { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px; }
  .arrow { color: var(--subtext0); }
  .path { margin-left: auto; }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .chip { background: var(--surface0); border-radius: var(--r-sm); padding: 1px 6px; font-size: var(--fs-xs, 11px); color: var(--subtext1); overflow-wrap: anywhere; }
  .chip.slot { color: var(--green); }
  .chip.folder { color: var(--blue); }
  .group { display: flex; flex-direction: column; gap: 3px; }
  .ghead { display: flex; align-items: center; gap: 6px; padding: 4px 2px 2px; position: sticky; top: 0; background: var(--base); z-index: 1; }
  .gname { background: none; border: 0; color: var(--text); font-weight: 600; cursor: pointer; padding: 0; font-size: var(--fs-sm); text-align: left; }
  .gsum { margin-left: auto; }
  .gfolder { padding-left: 22px; align-items: center; }
  .tw { width: 16px; display: inline-flex; align-items: center; justify-content: center; background: none; border: 0; color: var(--subtext0); cursor: pointer; padding: 0; }
  .group > .pair { margin-left: 22px; }
  .slots { display: flex; flex-direction: column; gap: 4px; }
  .prefer { margin-left: auto; max-width: 320px; }
  .dups { align-self: flex-start; }
  .linklike { background: none; border: 0; color: var(--blue); cursor: pointer; padding: 0; font-size: var(--fs-sm); }
  .err { color: var(--red); font-size: var(--fs-sm); margin: 0; display: flex; gap: 5px; }
  footer { display: flex; justify-content: flex-end; align-items: center; gap: 0.5rem; border-top: 1px solid var(--surface0); padding-top: 0.5rem; }
  footer .left, .undo-q { margin-right: auto; }
  .opt { display: flex; align-items: center; gap: 6px; font-size: var(--fs-sm); color: var(--subtext1); align-self: flex-end; cursor: pointer; }
  footer button { background: var(--surface0); color: var(--text); border: 0; border-radius: var(--r-sm); height: var(--ctl-h); padding: 0 0.9rem; font: inherit; cursor: pointer; }
  footer button:not(.primary):hover:not(:disabled) { background: var(--surface1); }
  footer button:disabled { opacity: 0.6; cursor: default; }
  button.primary { background: var(--blue); color: var(--base); border: 0; }
  button.danger { color: var(--red); }
</style>
