<script lang="ts">
  // Compare one file across hosts: read it everywhere, group hosts by
  // identical content, and diff any two variants side by side. Read-only -
  // there is no "copy left to right".
  import { api, type FileReadResult } from "./api";
  import { fleet, type FleetOpen } from "./fleetStore.svelte";
  import { lineDiff, splitLines } from "./lineDiff";
  import FleetShell from "./FleetShell.svelte";
  import { errMsg } from "./connectErrors";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();

  let path = $state("/etc/");
  let running = $state(false);
  let err = $state("");
  let results = $state<FileReadResult[] | null>(null);
  let leftId = $state("");
  let rightId = $state("");
  let ignoreWs = $state(true);
  let onlyChanges = $state(false);

  async function read() {
    running = true;
    err = "";
    try {
      results = (await api.readFileAcross(scope.ids, path.trim())) ?? [];
      const ok = results.filter((r) => r.state === "ok");
      // Start on the two most common variants: the usual question is "how
      // does the odd one out differ from the rest".
      const v = variantsOf(ok);
      leftId = v[0]?.hosts[0]?.connection_id ?? ok[0]?.connection_id ?? "";
      rightId = v[1]?.hosts[0]?.connection_id ?? ok[1]?.connection_id ?? "";
    } catch (e: any) {
      err = errMsg(e);
    } finally {
      running = false;
    }
  }

  function variantsOf(ok: FileReadResult[]) {
    const m = new Map<string, FileReadResult[]>();
    for (const r of ok) {
      if (!m.has(r.sha256)) m.set(r.sha256, []);
      m.get(r.sha256)!.push(r);
    }
    return [...m.entries()].map(([sha, hosts]) => ({ sha, hosts })).sort((a, b) => b.hosts.length - a.hosts.length);
  }
  const ok = $derived((results ?? []).filter((r) => r.state === "ok"));
  const failed = $derived((results ?? []).filter((r) => r.state !== "ok"));
  const variants = $derived(variantsOf(ok));
  const left = $derived(ok.find((r) => r.connection_id === leftId));
  const right = $derived(ok.find((r) => r.connection_id === rightId));
  const rows = $derived(left && right ? lineDiff(splitLines(left.content), splitLines(right.content), ignoreWs) : null);
  const changed = $derived((rows ?? []).filter((r) => r.kind !== "same").length);
  // With "only changes", keep 2 lines of context around each change.
  const visible = $derived.by(() => {
    if (!rows || !onlyChanges) return rows;
    const keep = new Set<number>();
    rows.forEach((r, i) => {
      if (r.kind !== "same") for (let k = i - 2; k <= i + 2; k++) keep.add(k);
    });
    return rows.filter((_, i) => keep.has(i));
  });
  // One colour per variant, so the host chips and the Left/Right headers
  // say at a glance which hosts share a file. Variant 1 (most common) is
  // green; the odd ones out get warmer colours.
  const VARIANT_COLORS = ["--green", "--peach", "--mauve", "--sky", "--pink", "--yellow", "--flamingo", "--teal"];
  function variantColor(n: number): string {
    return n > 0 ? `var(${VARIANT_COLORS[(n - 1) % VARIANT_COLORS.length]})` : "var(--overlay1)";
  }
  // "Most common" only when it really is: a tie at the top has no majority.
  const hasMajority = $derived(variants.length > 1 && variants[0].hosts.length > variants[1].hosts.length);

  function diffAgainstFirst(vi: number) {
    leftId = variants[0].hosts[0].connection_id;
    rightId = variants[vi].hosts[0].connection_id;
  }

  // Click a host chip to put it on the right, Shift+click for the left.
  function pickHost(e: MouseEvent, id: string) {
    if (e.shiftKey) leftId = id;
    else rightId = id;
  }

  function variantOf(id: string): number {
    const r = ok.find((x) => x.connection_id === id);
    return r ? variants.findIndex((v) => v.sha === r.sha256) + 1 : 0;
  }
</script>

<FleetShell
  title="Compare file"
  sub={`${scope.label} · ${scope.ids.length} hosts · read as each host's login user`}
  wide={!!results}
  onClose={() => fleet.close()}
>
  <div class="row">
    <label for="cmp-path" class="fdim">Path</label>
    <input id="cmp-path" class="finput fmono path" bind:value={path} spellcheck="false"
      onkeydown={(e) => { if (e.key === "Enter" && !running) void read(); }} />
    <button class="fbtn primary" disabled={running || !path.trim()} onclick={read}>{running ? "Reading…" : results ? "Read again" : "Read"}</button>
    {#if results}
      <span class="fdim">{ok.length} read · <span class={variants.length > 1 ? "fwarn" : "fok"}>{variants.length} variant{variants.length === 1 ? "" : "s"}</span>
        {#if variants.length > 1}({variants.map((v) => v.hosts.length).join(" · ")}){/if}</span>
    {/if}
  </div>
  {#if err}<p class="ferr">{err}</p>{/if}

  {#if results}
    {#if failed.length}
      <details class="failed"><summary class="ferr">{failed.length} host{failed.length === 1 ? "" : "s"} could not be read</summary>
        {#each failed as f (f.connection_id)}<div><span class="fmono">{f.name}</span> <span class="fdim">{f.error}</span></div>{/each}
      </details>
    {/if}
    {#if ok.length >= 2 && variants.length === 1}
      <p class="fok same">Identical on all {ok.length} hosts.</p>
    {/if}
    {#if variants.length > 1}
      <div class="variants">
        {#each variants as v, vi (v.sha)}
          <div class="variant" style:--vc={variantColor(vi + 1)}>
            <div class="vhead">
              <span class="vname">Variant {vi + 1}</span>
              <span class="fdim">{v.hosts.length} host{v.hosts.length === 1 ? "" : "s"}{vi === 0 && hasMajority ? " · most common" : ""} · {v.sha.slice(0, 10)}</span>
              {#if vi > 0}
                <button class="fbtn vdiff" onclick={() => diffAgainstFirst(vi)} title="Diff this variant against variant 1">Diff vs variant 1</button>
              {/if}
            </div>
            <div class="chips">
              {#each v.hosts as h (h.connection_id)}
                <button
                  class="chip fmono"
                  class:on-left={h.connection_id === leftId}
                  class:on-right={h.connection_id === rightId}
                  title="Click: show on the right · Shift+click: show on the left"
                  onclick={(e) => pickHost(e, h.connection_id)}
                >{h.name}{h.connection_id === leftId ? " (L)" : h.connection_id === rightId ? " (R)" : ""}</button>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    {/if}
    {#if ok.length >= 2}
      <div class="row">
        <label class="row"><span class="fdim">Left</span>
          <select class="fselect" bind:value={leftId}>
            {#each ok as r (r.connection_id)}<option value={r.connection_id}>{r.name} (variant {variantOf(r.connection_id)})</option>{/each}
          </select>
        </label>
        <label class="row"><span class="fdim">Right</span>
          <select class="fselect" bind:value={rightId}>
            {#each ok as r (r.connection_id)}<option value={r.connection_id}>{r.name} (variant {variantOf(r.connection_id)})</option>{/each}
          </select>
        </label>
        <label class="row"><input type="checkbox" bind:checked={ignoreWs} />Ignore whitespace</label>
        <label class="row"><input type="checkbox" bind:checked={onlyChanges} />Only changes</label>
        {#if rows}<span class="fdim">{changed === 0 ? "identical" : `${changed} changed line${changed === 1 ? "" : "s"}`}</span>{/if}
      </div>
      {#if left && right}
        {#if !visible}
          <p class="fwarn">These files are too large to diff here.</p>
        {:else}
          <div class="diff fmono">
            <div class="dh" style:--vc={variantColor(variantOf(left.connection_id))}><span class="vdot"></span>{left.name} · variant {variantOf(left.connection_id)} · {left.sha256.slice(0, 10)}{left.truncated ? " · first 1 MiB" : ""}</div>
            <div class="dh" style:--vc={variantColor(variantOf(right.connection_id))}><span class="vdot"></span>{right.name} · variant {variantOf(right.connection_id)} · {right.sha256.slice(0, 10)}{right.truncated ? " · first 1 MiB" : ""}</div>
            {#each visible as r, i (i)}
              {#if r.kind === "same"}
                <div class="dl"><span class="n">{r.ln}</span>{r.left}</div>
                <div class="dl"><span class="n">{r.rn}</span>{r.right}</div>
              {:else if r.kind === "chg"}
                <div class="dl del"><span class="n">{r.ln}</span>{r.left}</div>
                <div class="dl add"><span class="n">{r.rn}</span>{r.right}</div>
              {:else if r.kind === "del"}
                <div class="dl del"><span class="n">{r.ln}</span>{r.left}</div>
                <div class="dl gap"></div>
              {:else}
                <div class="dl gap"></div>
                <div class="dl add"><span class="n">{r.rn}</span>{r.right}</div>
              {/if}
            {/each}
          </div>
        {/if}
      {/if}
    {:else}
      <p class="fdim">Fewer than two hosts returned the file - nothing to compare.</p>
    {/if}
  {/if}

  {#snippet footer()}
    <button class="fbtn" onclick={() => fleet.close()}>Close</button>
  {/snippet}
</FleetShell>

<style>
  .row { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.6rem; flex-wrap: wrap; }
  .path { width: 26rem; }
  .failed { margin-bottom: 0.6rem; font-size: 0.76rem; }
  .same { margin: 0 0 0.6rem; }
  .variants { display: flex; flex-direction: column; gap: 0.4rem; margin-bottom: 0.7rem; }
  .variant {
    border: 1px solid color-mix(in srgb, var(--vc) 45%, var(--surface0));
    border-left: 3px solid var(--vc);
    border-radius: 5px;
    padding: 0.35rem 0.6rem;
    background: color-mix(in srgb, var(--vc) 6%, transparent);
  }
  .vhead { display: flex; align-items: center; gap: 0.6rem; font-size: 0.78rem; margin-bottom: 0.3rem; }
  .vname { font-weight: 600; color: var(--vc); }
  .vdiff { margin-left: auto; font-size: 0.72rem; padding: 0.1rem 0.5rem; }
  .chips { display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .chip {
    font-size: 0.72rem;
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    padding: 0.05rem 0.45rem;
    cursor: pointer;
  }
  .chip:hover { border-color: var(--vc); }
  .chip.on-left, .chip.on-right { border-color: var(--vc); background: color-mix(in srgb, var(--vc) 20%, var(--mantle)); }
  .vdot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--vc); margin-right: 0.4rem; vertical-align: middle; }
  .diff {
    display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    font-size: 0.74rem; line-height: 1.55; border: 1px solid var(--surface0); border-radius: 6px; overflow: hidden;
  }
  .dh { background: var(--mantle); color: var(--subtext0); padding: 0.3rem 0.6rem; font-family: inherit; border-bottom: 1px solid var(--surface0); }
  .dl { padding: 0 0.6rem; white-space: pre-wrap; word-break: break-all; min-height: 1.55em; }
  .dl:nth-child(odd) { border-right: 1px solid var(--surface0); }
  .dl .n { display: inline-block; width: 3.2em; color: var(--overlay1); user-select: none; }
  .dl.del { background: color-mix(in srgb, var(--red) 14%, transparent); }
  .dl.add { background: color-mix(in srgb, var(--green) 14%, transparent); }
  .dl.gap { background: color-mix(in srgb, var(--surface0) 45%, transparent); }
</style>
