<script lang="ts">
  // Consumer view of one topic: start a backend consumer (assign-only, never
  // commits), page its buffer in, filter, inspect, export. The consumer, its
  // preview mirror and the form live in TopicState (kafkaTopicState), so this
  // view can be unmounted and mounted again (pane split, tab moved, a hidden
  // topic tab) without stopping it or losing the records.

  import { api } from "./api";
  import { topicState, CLUSTER_BUDGET } from "./kafkaTopicState.svelte";
  import { formatTime, matches, preview, pretty, formatCount, formatBytes } from "./kafkaFormat";
  import { copyText } from "./clipboard";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconPlay, IconStop, IconDownload, IconCopy, IconX } from "./iconMap";

  interface Props {
    clusterId: string;
    topic: string;
    partitions: number[];
    // The view is on screen. A topic tab that is not stays mounted (hidden);
    // its consumer polls slower meanwhile.
    active?: boolean;
  }
  let { clusterId, topic, partitions, active = true }: Props = $props();

  // A detail view longer than this shows its head; Copy still copies all.
  const DETAIL_SHOW = 1 << 20;
  const ROW_H = 22;

  const c = $derived(topicState(clusterId, topic));
  const shown = $derived(c.filter ? c.msgs.filter((m) => matches(m, c.filter)) : c.msgs);

  $effect(() => {
    const t = c;
    t.show(active);
    return () => t.show(false);
  });

  // ----- virtual list -----
  let listEl = $state<HTMLDivElement | null>(null);
  let scrollTop = $state(0);
  let viewH = $state(400);
  const first = $derived(Math.max(0, Math.floor(scrollTop / ROW_H) - 10));
  const last = $derived(Math.min(shown.length, Math.ceil((scrollTop + viewH) / ROW_H) + 10));

  function onScroll() {
    // A hidden list reports 0 and resets its scroll position; not the user.
    if (!listEl || listEl.clientHeight === 0) return;
    scrollTop = listEl.scrollTop;
    c.scroll = scrollTop;
    // Scrolling up stops following; back at the bottom resumes it.
    c.follow = listEl.scrollTop + listEl.clientHeight >= listEl.scrollHeight - ROW_H * 2;
  }
  function scrollToEnd() {
    if (listEl) listEl.scrollTop = listEl.scrollHeight;
  }
  // When the list changes (new records, a filter, a clear) follow the tail,
  // and re-read the position: the browser clamps scrollTop without a scroll
  // event when the list shrinks, and the window computed from the stale value
  // renders no rows. On coming back on screen (mounted again, or a hidden
  // topic tab shown) restore where the user was: display:none drops it.
  $effect(() => {
    void shown;
    if (!active) return;
    queueMicrotask(() => {
      if (!listEl || listEl.clientHeight === 0) return;
      if (c.follow) scrollToEnd();
      else if (listEl.scrollTop !== c.scroll) listEl.scrollTop = c.scroll;
      scrollTop = listEl.scrollTop;
    });
  });

  // The backend writes the file from its buffer: full payloads, the same
  // filter applied to the full text.
  async function exportBuffer() {
    if (!c.consumerId) return;
    try {
      const r = await api.kafkaConsumeExport(c.consumerId, topic, c.filter);
      if (r.path) toast.ok(`Saved ${r.records} records to ${r.path}`);
    } catch (e) {
      toast.err(`Export failed: ${errMsg(e)}`);
    }
  }

  const shownRecord = $derived(c.full ?? c.selected);
  const detail = $derived.by(() => {
    if (!shownRecord) return null;
    if (c.detailTab === "key") return pretty(shownRecord.key);
    if (c.detailTab === "value") return pretty(shownRecord.value);
    return null;
  });
  const detailText = $derived(
    detail && detail.text.length > DETAIL_SHOW
      ? detail.text.slice(0, DETAIL_SHOW) + `\n\n... ${formatBytes(detail.text.length - DETAIL_SHOW)} more - Copy copies all of it`
      : detail?.text ?? "",
  );
</script>

<div class="consumer">
  <div class="bar">
    <select bind:value={c.mode} title="Where to start reading">
      <option value="last">Last N per partition</option>
      <option value="earliest">From the beginning</option>
      <option value="latest">New records only</option>
      <option value="offset">From offset</option>
      <option value="timestamp">From time</option>
    </select>
    {#if c.mode === "last"}
      <input class="num" type="number" min="1" bind:value={c.n} title="Records per partition" />
    {:else if c.mode === "offset"}
      <input class="num" type="number" min="0" bind:value={c.offset} title="Offset (in every partition read)" />
    {:else if c.mode === "timestamp"}
      <input type="datetime-local" bind:value={c.when} title="First record at or after this local time" />
    {/if}
    <select bind:value={c.partition} title="Partition">
      <option value={-1}>All partitions</option>
      {#each partitions as p (p)}<option value={p}>Partition {p}</option>{/each}
    </select>
    <label class="lim" title="Stop after this many records (0 = no limit)">
      max <input class="num" type="number" min="0" bind:value={c.limit} />
    </label>
    <label class="chk" title="Stop once every partition reaches the end it had at start">
      <input type="checkbox" bind:checked={c.stopAtEnd} disabled={c.mode === "latest"} /> stop at end
    </label>
    {#if c.running}
      <button class="stop" onclick={() => c.stop()}><IconStop size={12} /> Stop</button>
    {:else}
      <button class="go" disabled={c.starting} onclick={() => c.start()}><IconPlay size={12} /> {c.starting ? "Starting..." : "Consume"}</button>
    {/if}
    <span class="spacer"></span>
    <input class="filter" placeholder="Filter key / value / headers" bind:value={c.filter} />
    <button title="Export the buffered records matching the filter as NDJSON, full payloads" disabled={!c.consumerId || !c.msgs.length} onclick={exportBuffer}><IconDownload size={12} /></button>
    <button title="Clear" disabled={!c.msgs.length || c.running} onclick={() => c.clear()}><IconX size={12} /></button>
  </div>

  <div class="bar match" title="Only records matching every filled field are kept; the rest are counted as read but not stored. Case-insensitive, over the full key / value / headers. Applies when Consume starts.">
    <span class="lbl">Keep only</span>
    <input class="mf" placeholder="key contains" bind:value={c.keyHas} disabled={c.running || c.starting} />
    <input class="mf" placeholder="value contains" bind:value={c.valueHas} disabled={c.running || c.starting} />
    <input class="mf" placeholder="header name / value contains" bind:value={c.headerHas} disabled={c.running || c.starting} />
  </div>

  <div class="status">
    {#if c.snap}
      <span class="st {c.snap.state}">{c.snap.state}</span>
      {#if c.snap.filtering}
        <span>{formatCount(c.snap.received)} matched of {formatCount(c.snap.scanned)} read</span>
      {:else}
        <span>{formatCount(c.snap.received)} read</span>
      {/if}
      {#if c.filter}<span>{shown.length} in view</span>{/if}
      {#if c.snap.dropped > 0}<span title="Older records left the buffer ({formatCount(c.snap.buffer_messages)} records / {formatBytes(c.snap.buffer_bytes)} {c.snap.buffer_bytes < CLUSTER_BUDGET ? 'its share of the cluster budget, shared with the other topics that hold records' : 'the whole cluster budget'})">{formatCount(c.snap.dropped)} dropped</span>{/if}
      {#if c.snap.error}<span class="err" title={c.snap.error}>{c.snap.error}</span>{/if}
    {:else}
      <span class="hint">Pick a start point and press Consume. Nothing is committed; the cluster's consumer groups are not touched.</span>
    {/if}
  </div>

  <div class="split">
    <div class="list" bind:this={listEl} bind:clientHeight={viewH} onscroll={onScroll}>
      <div class="rows" style:height="{shown.length * ROW_H}px">
        {#each shown.slice(first, last) as m, i (m.seq)}
          <button
            class="row"
            class:sel={c.selected?.seq === m.seq}
            style:top="{(first + i) * ROW_H}px"
            onclick={() => c.select(m)}
          >
            <span class="c-p">{m.partition}</span>
            <span class="c-o">{m.offset}</span>
            <span class="c-t">{formatTime(m.timestamp)}</span>
            <span class="c-k">{preview(m.key, 60)}</span>
            <span class="c-v">{preview(m.value)}</span>
          </button>
        {/each}
      </div>
    </div>

    {#if c.selected && shownRecord}
      <div class="detail">
        <div class="dbar">
          <span>p{c.selected.partition} · offset {c.selected.offset} · {formatTime(c.selected.timestamp)} · {formatBytes(c.selected.size)}</span>
          {#if !c.full && !c.fullErr}<span class="hint">loading the full record...</span>{/if}
          {#if c.fullErr}<span class="err" title={c.fullErr}>showing the 2 KiB preview: {c.fullErr}</span>{/if}
          <span class="spacer"></span>
          <button class:on={c.detailTab === "value"} onclick={() => (c.detailTab = "value")}>Value</button>
          <button class:on={c.detailTab === "key"} onclick={() => (c.detailTab = "key")}>Key</button>
          <button class:on={c.detailTab === "headers"} onclick={() => (c.detailTab = "headers")}>Headers ({shownRecord.headers.length})</button>
          {#if detail}
            <button title="Copy" onclick={() => copyText(detail.text)}><IconCopy size={12} /></button>
          {/if}
          <button title="Close" onclick={() => c.select(null)}><IconX size={12} /></button>
        </div>
        {#if detail}
          <pre class="body selectable {detail.kind}">{detailText}</pre>
        {:else}
          <table class="headers selectable">
            <tbody>
              {#each shownRecord.headers as h, i (i)}
                <tr><td>{h.key}</td><td>{h.value.encoding === "base64" ? `<${h.value.size} bytes binary> ${h.value.text}` : h.value.text}</td></tr>
              {:else}
                <tr><td class="hint">no headers</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .consumer { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .bar { display: flex; gap: 0.35rem; align-items: center; padding: 0.35rem 0.5rem; flex-wrap: wrap; border-bottom: 1px solid var(--surface0); }
  .bar select, .bar input { font-size: 0.75rem; }
  .num { width: 5.5rem; }
  .filter { width: 14rem; }
  .match { padding-top: 0.2rem; padding-bottom: 0.2rem; }
  .match .lbl { font-size: 0.7rem; color: var(--subtext0); }
  .mf { width: 11rem; }
  .lim, .chk { display: flex; align-items: center; gap: 0.25rem; font-size: 0.72rem; color: var(--subtext0); }
  .spacer { flex: 1; }
  .go { color: var(--green); }
  .stop { color: var(--red); }
  .status { display: flex; gap: 0.8rem; padding: 0.2rem 0.6rem; font-size: 0.7rem; color: var(--subtext0); border-bottom: 1px solid var(--surface0); min-height: 1.2rem; }
  .st { text-transform: uppercase; font-weight: 600; }
  .st.running { color: var(--green); }
  .st.error { color: var(--red); }
  .err { color: var(--red); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hint { color: var(--overlay0); }
  .split { flex: 1; display: flex; flex-direction: column; min-height: 0; }
  .list { flex: 1; overflow: auto; min-height: 4rem; position: relative; font-family: var(--mono-font, monospace); font-size: 0.72rem; }
  .rows { position: relative; }
  .row {
    position: absolute; left: 0; right: 0; height: 22px;
    display: grid; grid-template-columns: 2.5rem 5.5rem 11.5rem minmax(4rem, 12rem) 1fr;
    gap: 0.5rem; align-items: center; padding: 0 0.6rem; text-align: left;
    background: none; border: none; border-radius: 0; color: var(--text); font: inherit; cursor: pointer;
  }
  .row:hover { background: var(--surface0); }
  .row.sel { background: color-mix(in srgb, var(--blue) 22%, transparent); }
  .row span { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .c-p, .c-o { color: var(--subtext0); text-align: right; }
  .c-t { color: var(--overlay1); }
  .c-k { color: var(--mauve); }
  .detail { height: 45%; min-height: 8rem; display: flex; flex-direction: column; border-top: 1px solid var(--surface1); }
  .dbar { display: flex; gap: 0.3rem; align-items: center; padding: 0.25rem 0.5rem; font-size: 0.72rem; color: var(--subtext0); background: var(--mantle); }
  .dbar button.on { color: var(--blue); }
  .body { flex: 1; overflow: auto; margin: 0; padding: 0.5rem 0.7rem; font-size: 0.74rem; white-space: pre-wrap; word-break: break-all; user-select: text; }
  .body.binary { white-space: pre; }
  .headers { font-size: 0.74rem; margin: 0.4rem 0.7rem; user-select: text; }
  .headers td { padding: 0.1rem 0.6rem 0.1rem 0; vertical-align: top; word-break: break-all; }
  .headers td:first-child { color: var(--mauve); white-space: nowrap; }
</style>
