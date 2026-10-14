<script lang="ts">
  import { untrack } from "svelte";
  // Consumer groups: list with lag, one group's members and per-partition
  // lag, offset reset (only while the group has no members) and delete.

  import { api, type KafkaGroupDetail, type KafkaGroupSummary, type KafkaStartMode } from "./api";
  import { formatCount } from "./kafkaFormat";
  import { confirmKafkaDestructive } from "./kafkaConfirm";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconRefresh, IconTrash } from "./iconMap";

  interface Props {
    clusterId: string;
    writable: boolean;
    sensitive: boolean;
  }
  let { clusterId, writable, sensitive }: Props = $props();

  let groups = $state<KafkaGroupSummary[] | null>(null);
  let loadErr = $state<string | null>(null);
  let filter = $state("");
  let selected = $state<string | null>(null);
  let detail = $state<KafkaGroupDetail | null>(null);
  let detailErr = $state<string | null>(null);
  let busy = $state(false);

  const shown = $derived((groups ?? []).filter((g) => !filter || g.name.toLowerCase().includes(filter.toLowerCase())));

  async function load() {
    loadErr = null;
    try {
      groups = (await api.kafkaGroups(clusterId)) ?? [];
    } catch (e) {
      loadErr = errMsg(e);
    }
  }
  async function loadDetail(name: string) {
    detailErr = null;
    try {
      detail = await api.kafkaGroup(clusterId, name);
    } catch (e) {
      detail = null;
      detailErr = errMsg(e);
    }
  }
  $effect(() => {
    void clusterId;
    untrack(() => load());
  });
  $effect(() => {
    const name = selected;
    untrack(() => {
      if (name) loadDetail(name);
      else detail = null;
    });
  });

  // ----- reset -----
  let resetTopic = $state("");
  let resetMode = $state<KafkaStartMode>("latest");
  let resetN = $state(0);
  let resetWhen = $state("");
  $effect(() => {
    if (detail && !detail.topics.includes(resetTopic)) resetTopic = detail.topics[0] ?? "";
  });

  async function reset() {
    if (!detail || !resetTopic) return;
    const what =
      resetMode === "earliest" ? "the earliest offset (re-read everything retained)" :
      resetMode === "latest" ? "the end (skip everything not yet read)" :
      resetMode === "last" ? `the last ${resetN} records of every partition` :
      resetMode === "offset" ? `offset ${resetN}` : `the first record at or after ${resetWhen}`;
    const ok = await showConfirm({
      title: "Reset group offsets",
      message: `Move group "${detail.name}" on topic "${resetTopic}" to ${what}? Its consumers will start from there when they rejoin.`,
      okLabel: "Reset offsets",
      danger: true,
    });
    if (!ok) return;
    busy = true;
    try {
      await api.kafkaResetGroupOffsets(clusterId, {
        group: detail.name,
        topic: resetTopic,
        mode: resetMode,
        n: resetN,
        timestamp: resetMode === "timestamp" ? new Date(resetWhen).getTime() : 0,
      });
      toast.ok(`Offsets of ${detail.name} reset`);
      await loadDetail(detail.name);
      await load();
    } catch (e) {
      toast.err(`Reset failed: ${errMsg(e)}`);
    } finally {
      busy = false;
    }
  }

  async function del() {
    if (!detail) return;
    if (!(await confirmKafkaDestructive("Delete group", detail.name, sensitive,
      `Delete consumer group "${detail.name}" and its committed offsets?`))) return;
    busy = true;
    try {
      await api.kafkaDeleteGroup(clusterId, detail.name);
      toast.ok(`Deleted group ${detail.name}`);
      selected = null;
      await load();
    } catch (e) {
      toast.err(`Delete failed: ${errMsg(e)}`);
    } finally {
      busy = false;
    }
  }
</script>

<div class="groups">
  <div class="left">
    <div class="bar">
      <input placeholder="Filter groups" bind:value={filter} />
      <button title="Reload" onclick={load}><IconRefresh size={12} /></button>
    </div>
    {#if loadErr}<div class="error">{loadErr}</div>{/if}
    <div class="list">
      {#if groups === null && !loadErr}
        <div class="hint">Loading...</div>
      {/if}
      {#each shown as g (g.name)}
        <button class="item" class:sel={selected === g.name} onclick={() => (selected = g.name)}>
          <span class="gname">{g.name}</span>
          <span class="gmeta">
            <span class="state s-{g.state.toLowerCase()}">{g.state}</span>
            · {g.members} member{g.members === 1 ? "" : "s"}
            · lag <b class:lagging={g.lag > 0}>{formatCount(g.lag)}</b>
          </span>
        </button>
      {:else}
        {#if groups !== null}<div class="hint">No consumer groups.</div>{/if}
      {/each}
    </div>
  </div>

  <div class="right">
    {#if detailErr}<div class="error">{detailErr}</div>{/if}
    {#if detail}
      <div class="dhead">
        <b>{detail.name}</b>
        <span class="meta">{detail.state} · {detail.protocol_type || "?"}{detail.assignor ? ` / ${detail.assignor}` : ""} · lag {formatCount(detail.lag)}</span>
        <span class="spacer"></span>
        <button title="Reload" onclick={() => loadDetail(detail!.name)}><IconRefresh size={12} /></button>
        {#if writable}
          <button class="danger" disabled={busy || detail.members > 0} title={detail.members > 0 ? "Stop the group's consumers first" : "Delete group"} onclick={del}><IconTrash size={12} /></button>
        {/if}
      </div>
      <div class="scroll">
        <h4>Members</h4>
        <table class="selectable">
          <tbody>
            {#each detail.member_list as m (m.id)}
              <tr>
                <td>{m.client_id}</td>
                <td>{m.host}</td>
                <td>{Object.entries(m.assigned).map(([t, ps]) => `${t} [${ps.join(",")}]`).join("; ")}</td>
              </tr>
            {:else}
              <tr><td class="hint">No members - the group is not consuming right now.</td></tr>
            {/each}
          </tbody>
        </table>

        <h4>Partitions</h4>
        <table class="selectable">
          <thead><tr><th>Topic</th><th>Partition</th><th>Committed</th><th>End</th><th>Lag</th><th>Member</th></tr></thead>
          <tbody>
            {#each detail.partitions as p (p.topic + "/" + p.partition)}
              <tr>
                <td>{p.topic}</td>
                <td>{p.partition}</td>
                <td>{p.committed < 0 ? "-" : p.committed}</td>
                <td>{p.end}</td>
                <td class:lagging={p.lag > 0}>{p.lag < 0 ? "?" : p.lag}</td>
                <td>{p.member ?? ""}</td>
              </tr>
            {/each}
          </tbody>
        </table>

        {#if writable && detail.topics.length}
          <h4>Reset offsets</h4>
          {#if detail.members > 0}
            <p class="hint">Stop the group's consumers first - Kafka refuses a reset while the group has members.</p>
          {:else}
            <div class="reset">
              <select bind:value={resetTopic}>
                {#each detail.topics as t (t)}<option value={t}>{t}</option>{/each}
              </select>
              <select bind:value={resetMode}>
                <option value="latest">to the end</option>
                <option value="earliest">to the beginning</option>
                <option value="last">back N records</option>
                <option value="offset">to an offset</option>
                <option value="timestamp">to a time</option>
              </select>
              {#if resetMode === "last" || resetMode === "offset"}
                <input type="number" min="0" bind:value={resetN} />
              {:else if resetMode === "timestamp"}
                <input type="datetime-local" bind:value={resetWhen} />
              {/if}
              <button class="danger" disabled={busy || (resetMode === "timestamp" && !resetWhen)} onclick={reset}>Reset</button>
            </div>
          {/if}
        {/if}
      </div>
    {:else if !detailErr}
      <div class="hint pad">Select a group.</div>
    {/if}
  </div>
</div>

<style>
  .groups { display: grid; grid-template-columns: minmax(14rem, 22rem) 1fr; height: 100%; min-height: 0; }
  .left { display: flex; flex-direction: column; border-right: 1px solid var(--surface0); min-height: 0; }
  .bar { display: flex; gap: 0.3rem; padding: 0.35rem 0.5rem; }
  .bar input { flex: 1; font-size: 0.75rem; }
  .list { flex: 1; overflow: auto; }
  .item { display: flex; flex-direction: column; align-items: flex-start; width: 100%; padding: 0.3rem 0.6rem; background: none; border: none; border-radius: 0; text-align: left; color: var(--text); cursor: pointer; }
  .item:hover { background: var(--surface0); }
  .item.sel { background: color-mix(in srgb, var(--blue) 20%, transparent); }
  .gname { font-size: 0.78rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%; }
  .gmeta { font-size: 0.68rem; color: var(--subtext0); }
  .state.s-stable { color: var(--green); }
  .state.s-empty { color: var(--overlay1); }
  .state.s-dead { color: var(--red); }
  .lagging { color: var(--peach); }
  .right { display: flex; flex-direction: column; min-height: 0; }
  .dhead { display: flex; gap: 0.5rem; align-items: center; padding: 0.35rem 0.6rem; border-bottom: 1px solid var(--surface0); background: var(--mantle); }
  .meta { font-size: 0.72rem; color: var(--subtext0); }
  .spacer { flex: 1; }
  .scroll { flex: 1; overflow: auto; padding: 0.4rem 0.7rem; }
  h4 { margin: 0.6rem 0 0.3rem; font-size: 0.75rem; color: var(--subtext0); }
  table { border-collapse: collapse; font-size: 0.74rem; width: 100%; user-select: text; }
  th { text-align: left; color: var(--subtext0); border-bottom: 1px solid var(--surface1); padding: 0.2rem 0.5rem; }
  td { padding: 0.15rem 0.5rem; border-bottom: 1px solid var(--surface0); }
  .reset { display: flex; gap: 0.4rem; align-items: center; font-size: 0.75rem; flex-wrap: wrap; }
  .reset input[type="number"] { width: 7rem; }
  .danger { color: var(--red); }
  .error { color: var(--red); padding: 0.4rem 0.7rem; font-size: 0.75rem; }
  .hint { color: var(--overlay1); font-size: 0.72rem; padding: 0.3rem 0.6rem; }
  .pad { padding: 1rem; }
</style>
