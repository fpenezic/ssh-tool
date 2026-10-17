<script lang="ts">
  // Consumer view of one topic: start a backend consumer (assign-only, never
  // commits), page its buffer in, filter, inspect, export. The buffer lives
  // in the backend. This view mirrors it as PREVIEWS (every key / value /
  // header cut to 2 KiB by the backend), bounded in records and bytes, and
  // fetches the one full record the user opens. A heavy topic with large
  // records would otherwise put gigabytes into the webview.

  import { onDestroy } from "svelte";
  import { api, type KafkaMessage, type KafkaStartMode, type KafkaConsumeSnapshot } from "./api";
  import { formatTime, matches, preview, pretty, formatCount, formatBytes, messageTextBytes } from "./kafkaFormat";
  import { copyText } from "./clipboard";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconPlay, IconStop, IconDownload, IconCopy, IconX } from "./iconMap";

  interface Props {
    clusterId: string;
    topic: string;
    partitions: number[];
  }
  let { clusterId, topic, partitions }: Props = $props();

  const MIRROR_CAP = 10_000;
  // Bound on the mirror's preview text. Previews are at most ~2 KiB a field,
  // so this only bites on records with many large headers.
  const MIRROR_BYTES = 24 << 20;
  // A detail view longer than this shows its head; Copy still copies all.
  const DETAIL_SHOW = 1 << 20;
  const ROW_H = 22;

  let mode = $state<KafkaStartMode>("last");
  let n = $state(20);
  let offset = $state(0);
  let when = $state(localNow(-3600_000));
  let partition = $state(-1);
  let limit = $state(0);
  let stopAtEnd = $state(true);

  let consumerId = $state<string | null>(null);
  let snap = $state<Omit<KafkaConsumeSnapshot, "messages"> | null>(null);
  // $state.raw: thousands of records must not become deep proxies. The array
  // is replaced, never mutated, so reassignment is the only signal needed.
  let msgs = $state.raw<KafkaMessage[]>([]);
  let mirrorBytes = 0;
  let lastSeq = 0;
  let starting = $state(false);
  let filter = $state("");
  let follow = $state(true);
  // The row picked in the list (a preview) and, once fetched, its full record.
  let selected = $state.raw<KafkaMessage | null>(null);
  let full = $state.raw<KafkaMessage | null>(null);
  let fullErr = $state<string | null>(null);
  let detailTab = $state<"value" | "key" | "headers">("value");

  const shown = $derived(filter ? msgs.filter((m) => matches(m, filter)) : msgs);
  const running = $derived(!!consumerId && snap?.state === "running");

  function localNow(deltaMs: number): string {
    const d = new Date(Date.now() + deltaMs);
    const p = (x: number) => String(x).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  let timer: ReturnType<typeof setInterval> | null = null;

  async function start() {
    await drop();
    msgs = [];
    mirrorBytes = 0;
    lastSeq = 0;
    select(null);
    snap = null;
    starting = true;
    try {
      consumerId = await api.kafkaConsumeStart(clusterId, {
        topic,
        mode,
        n: mode === "last" ? n : mode === "offset" ? offset : 0,
        timestamp: mode === "timestamp" ? new Date(when).getTime() : 0,
        partition,
        limit: Math.max(0, limit | 0),
        stop_at_end: mode !== "latest" && stopAtEnd,
      });
      timer = setInterval(poll, 400);
      poll();
    } catch (e) {
      toast.err(`Consume: ${errMsg(e)}`);
    } finally {
      starting = false;
    }
  }

  let polling = false;
  async function poll() {
    const id = consumerId;
    if (!id || polling) return;
    polling = true;
    try {
      for (let round = 0; round < 20; round++) {
        const s = await api.kafkaConsumeSnapshot(id, lastSeq, 1000);
        if (id !== consumerId) return;
        const { messages, ...rest } = s;
        snap = rest;
        if (messages.length) {
          lastSeq = messages[messages.length - 1].seq;
          let next = msgs.concat(messages);
          for (const m of messages) mirrorBytes += messageTextBytes(m);
          let cut = 0;
          while (cut < next.length - 1 && (next.length - cut > MIRROR_CAP || mirrorBytes > MIRROR_BYTES)) {
            mirrorBytes -= messageTextBytes(next[cut]);
            cut++;
          }
          if (cut > 0) next = next.slice(cut);
          msgs = next;
          if (follow) queueMicrotask(scrollToEnd);
        }
        // A short page means caught up: the backend also cuts pages at
        // about 2 MiB, so compare with what was asked, not with 1000.
        if (!messages.length || (s.buffered > 0 && messages[messages.length - 1].seq >= s.received)) break;
      }
      if (snap && snap.state !== "running" && timer) {
        clearInterval(timer);
        timer = null;
      }
    } catch {
      // The consumer is gone (tab closed elsewhere); stop polling.
      if (timer) clearInterval(timer);
      timer = null;
    } finally {
      polling = false;
    }
  }

  // Stop fetching. The backend keeps the buffer, so records read so far can
  // still be opened in full and exported.
  async function stop() {
    if (timer) clearInterval(timer);
    timer = null;
    const id = consumerId;
    if (!id) return;
    await api.kafkaConsumeHalt(id).catch(() => {});
    await poll();
    if (snap && snap.state === "running") snap = { ...snap, state: "stopped" };
  }

  // Stop and let the backend drop the buffer (new consume, clear, close).
  async function drop() {
    if (timer) clearInterval(timer);
    timer = null;
    const id = consumerId;
    consumerId = null;
    if (id) await api.kafkaConsumeStop(id).catch(() => {});
  }

  function clearAll() {
    drop();
    msgs = [];
    mirrorBytes = 0;
    select(null);
    snap = null;
  }

  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (consumerId) api.kafkaConsumeStop(consumerId).catch(() => {});
  });

  // Open a record: show its preview at once, then the full record when any
  // part of it was cut.
  async function select(m: KafkaMessage | null) {
    selected = m;
    full = null;
    fullErr = null;
    if (!m || !consumerId) return;
    const cut = m.key?.truncated || m.value?.truncated || m.headers.some((h) => h.value.truncated);
    if (!cut) {
      full = m;
      return;
    }
    try {
      const r = await api.kafkaConsumeMessage(consumerId, m.seq);
      if (selected?.seq === m.seq) full = r;
    } catch (e) {
      if (selected?.seq === m.seq) fullErr = errMsg(e);
    }
  }

  // ----- virtual list -----
  let listEl = $state<HTMLDivElement | null>(null);
  let scrollTop = $state(0);
  let viewH = $state(400);
  const first = $derived(Math.max(0, Math.floor(scrollTop / ROW_H) - 10));
  const last = $derived(Math.min(shown.length, Math.ceil((scrollTop + viewH) / ROW_H) + 10));

  function onScroll() {
    if (!listEl) return;
    scrollTop = listEl.scrollTop;
    // Scrolling up stops following; back at the bottom resumes it.
    follow = listEl.scrollTop + listEl.clientHeight >= listEl.scrollHeight - ROW_H * 2;
  }
  function scrollToEnd() {
    if (listEl) listEl.scrollTop = listEl.scrollHeight;
  }
  // When the list shrinks (a filter, a clear) the browser clamps scrollTop
  // without a scroll event, and the window computed from the stale value
  // renders no rows. Re-read it after every change of what is shown.
  $effect(() => {
    void shown.length;
    queueMicrotask(() => {
      if (!listEl) return;
      if (follow) scrollToEnd();
      scrollTop = listEl.scrollTop;
    });
  });

  // The backend writes the file from its buffer: full payloads, the same
  // filter applied to the full text.
  async function exportBuffer() {
    if (!consumerId) return;
    try {
      const r = await api.kafkaConsumeExport(consumerId, topic, filter);
      if (r.path) toast.ok(`Saved ${r.records} records to ${r.path}`);
    } catch (e) {
      toast.err(`Export failed: ${errMsg(e)}`);
    }
  }

  const shownRecord = $derived(full ?? selected);
  const detail = $derived.by(() => {
    if (!shownRecord) return null;
    if (detailTab === "key") return pretty(shownRecord.key);
    if (detailTab === "value") return pretty(shownRecord.value);
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
    <select bind:value={mode} title="Where to start reading">
      <option value="last">Last N per partition</option>
      <option value="earliest">From the beginning</option>
      <option value="latest">New records only</option>
      <option value="offset">From offset</option>
      <option value="timestamp">From time</option>
    </select>
    {#if mode === "last"}
      <input class="num" type="number" min="1" bind:value={n} title="Records per partition" />
    {:else if mode === "offset"}
      <input class="num" type="number" min="0" bind:value={offset} title="Offset (in every partition read)" />
    {:else if mode === "timestamp"}
      <input type="datetime-local" bind:value={when} title="First record at or after this local time" />
    {/if}
    <select bind:value={partition} title="Partition">
      <option value={-1}>All partitions</option>
      {#each partitions as p (p)}<option value={p}>Partition {p}</option>{/each}
    </select>
    <label class="lim" title="Stop after this many records (0 = no limit)">
      max <input class="num" type="number" min="0" bind:value={limit} />
    </label>
    <label class="chk" title="Stop once every partition reaches the end it had at start">
      <input type="checkbox" bind:checked={stopAtEnd} disabled={mode === "latest"} /> stop at end
    </label>
    {#if running}
      <button class="stop" onclick={stop}><IconStop size={12} /> Stop</button>
    {:else}
      <button class="go" disabled={starting} onclick={start}><IconPlay size={12} /> {starting ? "Starting..." : "Consume"}</button>
    {/if}
    <span class="spacer"></span>
    <input class="filter" placeholder="Filter key / value / headers" bind:value={filter} />
    <button title="Export the buffered records matching the filter as NDJSON, full payloads" disabled={!consumerId || !msgs.length} onclick={exportBuffer}><IconDownload size={12} /></button>
    <button title="Clear" disabled={!msgs.length || running} onclick={clearAll}><IconX size={12} /></button>
  </div>

  <div class="status">
    {#if snap}
      <span class="st {snap.state}">{snap.state}</span>
      <span>{formatCount(snap.received)} read</span>
      {#if filter}<span>{shown.length} match</span>{/if}
      {#if snap.dropped > 0}<span title="Older records left the buffer (10k records / 64 MiB)">{formatCount(snap.dropped)} dropped</span>{/if}
      {#if snap.error}<span class="err" title={snap.error}>{snap.error}</span>{/if}
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
            class:sel={selected?.seq === m.seq}
            style:top="{(first + i) * ROW_H}px"
            onclick={() => select(m)}
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

    {#if selected && shownRecord}
      <div class="detail">
        <div class="dbar">
          <span>p{selected.partition} · offset {selected.offset} · {formatTime(selected.timestamp)} · {formatBytes(selected.size)}</span>
          {#if !full && !fullErr}<span class="hint">loading the full record...</span>{/if}
          {#if fullErr}<span class="err" title={fullErr}>showing the 2 KiB preview: {fullErr}</span>{/if}
          <span class="spacer"></span>
          <button class:on={detailTab === "value"} onclick={() => (detailTab = "value")}>Value</button>
          <button class:on={detailTab === "key"} onclick={() => (detailTab = "key")}>Key</button>
          <button class:on={detailTab === "headers"} onclick={() => (detailTab = "headers")}>Headers ({shownRecord.headers.length})</button>
          {#if detail}
            <button title="Copy" onclick={() => copyText(detail.text)}><IconCopy size={12} /></button>
          {/if}
          <button title="Close" onclick={() => select(null)}><IconX size={12} /></button>
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
