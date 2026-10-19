<script lang="ts">
  // One topic: messages (consumer), produce, partitions, config, metrics,
  // and the topic-level admin actions. Writes are hidden on a read-only
  // connection (the backend refuses them anyway) and on mobile.

  import { onDestroy, untrack } from "svelte";
  import { api, type KafkaTopicDetail, type KafkaTopicMetrics } from "./api";
  import KafkaConsumer from "./KafkaConsumer.svelte";
  import { topicState } from "./kafkaTopicState.svelte";
  import KafkaProducer from "./KafkaProducer.svelte";
  import { formatBytes, formatCount, parseConfigLines, ratePerSec } from "./kafkaFormat";
  import { confirmKafkaDestructive } from "./kafkaConfirm";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconRefresh, IconTrash } from "./iconMap";

  interface Props {
    clusterId: string;
    topic: string;
    writable: boolean;
    sensitive: boolean;
    onDeleted: () => void;
    onChanged: () => void;
    // On screen; a topic tab that is not stays mounted.
    active?: boolean;
  }
  let { clusterId, topic, writable, sensitive, onDeleted, onChanged, active = true }: Props = $props();

  // The subtab lives with the topic's consumer state, so a remount keeps it.
  const ts = $derived(topicState(clusterId, topic));
  let detail = $state<KafkaTopicDetail | null>(null);
  let loadErr = $state<string | null>(null);
  let busy = $state(false);

  const partitionIds = $derived(detail ? detail.partitions.map((p) => p.partition) : []);

  async function load() {
    loadErr = null;
    try {
      detail = await api.kafkaTopicDetail(clusterId, topic);
    } catch (e) {
      loadErr = errMsg(e);
    }
  }
  $effect(() => {
    void topic;
    untrack(() => {
      detail = null;
      metrics = [];
      load();
    });
  });

  async function run(label: string, fn: () => Promise<unknown>) {
    busy = true;
    try {
      await fn();
      toast.ok(label);
      await load();
      onChanged();
    } catch (e) {
      toast.err(`${label.split(" ")[0]} failed: ${errMsg(e)}`);
    } finally {
      busy = false;
    }
  }

  async function truncate() {
    if (!(await confirmKafkaDestructive("Truncate topic", topic, sensitive,
      `Delete every record currently in "${topic}"? The topic and its config stay.`))) return;
    run("Truncated " + topic, () => api.kafkaTruncate(clusterId, topic));
  }

  async function del() {
    if (!(await confirmKafkaDestructive("Delete topic", topic, sensitive,
      `Delete topic "${topic}" and all its records? This cannot be undone.`))) return;
    busy = true;
    try {
      await api.kafkaDeleteTopic(clusterId, topic);
      toast.ok(`Deleted ${topic}`);
      onDeleted();
    } catch (e) {
      toast.err(`Delete failed: ${errMsg(e)}`);
    } finally {
      busy = false;
    }
  }

  async function addPartitions() {
    if (!detail) return;
    const cur = detail.partitions.length;
    const v = await showPrompt(`New partition count for ${topic} (now ${cur}; Kafka can only raise it, and keyed records will hash to new partitions)`, String(cur + 1));
    if (!v) return;
    const total = parseInt(v, 10);
    if (!(total > cur)) {
      toast.err(`Partition count must be above ${cur}`);
      return;
    }
    run(`Partitions of ${topic} set to ${total}`, () => api.kafkaSetPartitions(clusterId, topic, total));
  }

  async function setRF() {
    if (!detail) return;
    const v = await showPrompt(`New replication factor for ${topic} (now ${detail.replication_factor}). Partitions are reassigned round robin; the brokers copy the data in the background.`, String(detail.replication_factor));
    if (!v) return;
    const rf = parseInt(v, 10);
    if (!(rf >= 1) || rf === detail.replication_factor) return;
    run(`Replication factor of ${topic} set to ${rf}`, () => api.kafkaSetReplicationFactor(clusterId, topic, rf));
  }

  // ----- config -----
  let showAll = $state(false);
  let editing = $state(false);
  let editText = $state("");
  const overrides = $derived(detail ? detail.configs.filter((c) => c.override) : []);
  const configRows = $derived(detail ? (showAll ? detail.configs : overrides) : []);

  function beginEdit() {
    editText = overrides.map((c) => `${c.name}=${c.value}`).join("\n");
    editing = true;
  }
  async function saveConfig() {
    const parsed = parseConfigLines(editText);
    if (parsed.error) {
      toast.err(parsed.error);
      return;
    }
    const before = Object.fromEntries(overrides.map((c) => [c.name, c.value]));
    const set: Record<string, string> = {};
    for (const [k, v] of Object.entries(parsed.set)) if (before[k] !== v) set[k] = v;
    const remove = Object.keys(before).filter((k) => !(k in parsed.set));
    if (!Object.keys(set).length && !remove.length) {
      editing = false;
      return;
    }
    await run(`Config of ${topic} updated`, () => api.kafkaAlterTopicConfig(clusterId, topic, set, remove));
    editing = false;
  }

  // ----- metrics -----
  let metrics = $state<KafkaTopicMetrics[]>([]);
  let metricsErr = $state<string | null>(null);
  let metricsTimer: ReturnType<typeof setInterval> | null = null;
  async function sample() {
    try {
      const m = await api.kafkaTopicMetrics(clusterId, topic);
      metrics = [...metrics.slice(-1), m];
      metricsErr = null;
    } catch (e) {
      metricsErr = errMsg(e);
    }
  }
  $effect(() => {
    if (ts.subtab !== "metrics" || !active) return;
    untrack(() => sample());
    metricsTimer = setInterval(sample, 15_000);
    return () => {
      if (metricsTimer) clearInterval(metricsTimer);
      metricsTimer = null;
    };
  });
  onDestroy(() => {
    if (metricsTimer) clearInterval(metricsTimer);
  });
  const cur = $derived(metrics.length ? metrics[metrics.length - 1] : null);
  const prev = $derived(metrics.length > 1 ? metrics[0] : null);
  const msgRate = $derived(cur ? ratePerSec(prev && { at: prev.sampled_at, v: prev.end_offsets }, { at: cur.sampled_at, v: cur.end_offsets }) : null);
  const byteRate = $derived(cur ? ratePerSec(prev && { at: prev.sampled_at, v: prev.leader_bytes }, { at: cur.sampled_at, v: cur.leader_bytes }) : null);
</script>

<div class="topic">
  <div class="head">
    <span class="name" title={topic}>{topic}</span>
    {#if detail}
      <span class="meta">{detail.partitions.length} partitions · rf {detail.replication_factor} · {formatCount(detail.messages)} messages{detail.internal ? " · internal" : ""}</span>
    {/if}
    <span class="spacer"></span>
    <nav>
      <button class:on={ts.subtab === "messages"} onclick={() => (ts.subtab = "messages")}>Messages</button>
      {#if writable}<button class:on={ts.subtab === "produce"} onclick={() => (ts.subtab = "produce")}>Produce</button>{/if}
      <button class:on={ts.subtab === "partitions"} onclick={() => (ts.subtab = "partitions")}>Partitions</button>
      <button class:on={ts.subtab === "config"} onclick={() => (ts.subtab = "config")}>Config</button>
      <button class:on={ts.subtab === "metrics"} onclick={() => (ts.subtab = "metrics")}>Metrics</button>
    </nav>
    <button title="Reload" onclick={load}><IconRefresh size={12} /></button>
    {#if writable}
      <details class="actions">
        <summary title="Topic actions">Actions</summary>
        <div class="menu">
          <button disabled={busy || !detail} onclick={addPartitions}>Add partitions...</button>
          <button disabled={busy || !detail} onclick={setRF}>Replication factor...</button>
          <button class="danger" disabled={busy} onclick={truncate}>Truncate (delete all records)</button>
          <button class="danger" disabled={busy} onclick={del}><IconTrash size={12} /> Delete topic</button>
        </div>
      </details>
    {/if}
  </div>

  {#if loadErr}
    <div class="error">{loadErr} <button onclick={load}>Retry</button></div>
  {/if}

  <div class="body">
    <!-- Mounted whatever the subtab, so the list keeps its place. -->
    <div class="msgs" class:off={ts.subtab !== "messages"}>
      <KafkaConsumer {clusterId} {topic} partitions={partitionIds} active={active && ts.subtab === "messages"} />
    </div>
    {#if ts.subtab === "produce"}
      <KafkaProducer {clusterId} {topic} partitions={partitionIds} onSent={load} />
    {:else if ts.subtab === "partitions" && detail}
      <div class="scroll">
        <table class="selectable">
          <thead><tr><th>Partition</th><th>Leader</th><th>Replicas</th><th>ISR</th><th>Start</th><th>End</th><th>Messages</th></tr></thead>
          <tbody>
            {#each detail.partitions as p (p.partition)}
              <tr class:bad={p.leader < 0 || p.isr.length < p.replicas.length}>
                <td>{p.partition}</td>
                <td>{p.leader < 0 ? "none" : p.leader}</td>
                <td>{p.replicas.join(", ")}</td>
                <td>{p.isr.join(", ")}{p.offline.length ? ` (offline ${p.offline.join(", ")})` : ""}</td>
                <td>{p.start_offset}</td>
                <td>{p.end_offset}</td>
                <td>{p.start_offset >= 0 && p.end_offset >= 0 ? p.end_offset - p.start_offset : "?"}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else if ts.subtab === "config" && detail}
      <div class="scroll">
        {#if detail.config_error}
          <div class="error">Configs not readable: {detail.config_error}</div>
        {:else if editing}
          <div class="cfg-edit">
            <p class="hint">Overrides set on the topic, one <code>key=value</code> a line. Remove a line to fall back to the broker default.</p>
            <textarea class="mono" bind:value={editText} spellcheck="false"></textarea>
            <div class="row">
              <button class="primary" disabled={busy} onclick={saveConfig}>Save</button>
              <button onclick={() => (editing = false)}>Cancel</button>
            </div>
          </div>
        {:else}
          <div class="row">
            <label class="chk"><input type="checkbox" bind:checked={showAll} /> show broker defaults too</label>
            <span class="spacer"></span>
            {#if writable}<button onclick={beginEdit}>Edit overrides</button>{/if}
          </div>
          <table class="selectable">
            <thead><tr><th>Name</th><th>Value</th><th>Source</th></tr></thead>
            <tbody>
              {#each configRows as c (c.name)}
                <tr class:ovr={c.override}>
                  <td>{c.name}</td>
                  <td class="mono">{c.sensitive ? "(sensitive)" : c.value}</td>
                  <td class="src">{c.source}</td>
                </tr>
              {:else}
                <tr><td colspan="3" class="hint">No overrides - every value comes from the broker defaults.</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    {:else if ts.subtab === "metrics"}
      <div class="scroll">
        {#if metricsErr}<div class="error">{metricsErr}</div>{/if}
        {#if cur}
          <div class="tiles">
            <div class="tile"><span>Messages</span><b>{formatCount(cur.messages)}</b></div>
            <div class="tile"><span>Rate</span><b>{msgRate === null ? "..." : `${msgRate.toFixed(1)} msg/s`}</b></div>
            <div class="tile"><span>Throughput</span><b>{byteRate === null ? "..." : `${formatBytes(Math.max(0, Math.round(byteRate)))}/s`}</b></div>
            <div class="tile"><span>Avg stored size</span><b>{cur.messages > 0 && cur.leader_bytes >= 0 ? formatBytes(Math.round(cur.leader_bytes / cur.messages)) : "?"}</b></div>
            <div class="tile"><span>On disk (leaders)</span><b>{formatBytes(cur.leader_bytes)}</b></div>
            <div class="tile"><span>On disk (all replicas)</span><b>{formatBytes(cur.total_bytes)}</b></div>
          </div>
          <p class="hint">Sampled every 15 s while this view is open{prev ? "" : "; rates appear after the second sample"}.{cur.size_error ? ` Sizes unavailable: ${cur.size_error}` : ""}</p>
        {:else if !metricsErr}
          <p class="hint">Sampling...</p>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .topic { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .head { display: flex; align-items: center; gap: 0.5rem; padding: 0.35rem 0.6rem; border-bottom: 1px solid var(--surface0); background: var(--mantle); }
  .name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 30%; }
  .meta { font-size: 0.72rem; color: var(--subtext0); white-space: nowrap; }
  .spacer { flex: 1; }
  nav { display: flex; gap: 0.15rem; }
  nav button { font-size: 0.74rem; }
  nav button.on { color: var(--blue); border-color: var(--blue); }
  .actions { position: relative; font-size: 0.74rem; }
  .actions summary { cursor: pointer; list-style: none; padding: 0.15rem 0.5rem; border: 1px solid var(--surface1); border-radius: 4px; }
  .actions .menu { position: absolute; right: 0; top: 1.6rem; z-index: 20; display: flex; flex-direction: column; gap: 0.15rem; padding: 0.3rem; background: var(--base); border: 1px solid var(--surface1); border-radius: 6px; min-width: 14rem; box-shadow: 0 6px 18px #0006; }
  .actions .menu button { text-align: left; }
  .danger { color: var(--red); }
  .error { color: var(--red); padding: 0.4rem 0.7rem; font-size: 0.75rem; }
  .body { flex: 1; min-height: 0; }
  .msgs { height: 100%; }
  .msgs.off { display: none; }
  .scroll { height: 100%; overflow: auto; padding: 0.5rem 0.7rem; box-sizing: border-box; }
  table { border-collapse: collapse; font-size: 0.74rem; width: 100%; user-select: text; }
  th { text-align: left; color: var(--subtext0); font-weight: 600; border-bottom: 1px solid var(--surface1); padding: 0.2rem 0.5rem; }
  td { padding: 0.15rem 0.5rem; border-bottom: 1px solid var(--surface0); word-break: break-all; }
  tr.bad td { color: var(--peach); }
  tr.ovr td:first-child { color: var(--blue); }
  .src { color: var(--overlay1); font-size: 0.68rem; }
  .mono { font-family: var(--mono-font, monospace); }
  .row { display: flex; gap: 0.5rem; align-items: center; margin-bottom: 0.4rem; font-size: 0.74rem; }
  .chk { display: flex; gap: 0.3rem; align-items: center; color: var(--subtext0); }
  .hint { color: var(--overlay1); font-size: 0.72rem; }
  .cfg-edit { display: flex; flex-direction: column; gap: 0.4rem; height: 100%; }
  .cfg-edit textarea { flex: 1; min-height: 10rem; font-size: 0.75rem; }
  .tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr)); gap: 0.5rem; }
  .tile { display: flex; flex-direction: column; gap: 0.2rem; padding: 0.5rem 0.7rem; border: 1px solid var(--surface0); border-radius: 6px; background: var(--mantle); }
  .tile span { font-size: 0.7rem; color: var(--subtext0); }
  .tile b { font-size: 1rem; }
</style>
