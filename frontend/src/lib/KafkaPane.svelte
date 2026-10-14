<script lang="ts">
  // A Kafka cluster tab. sessionId is the backend cluster handle
  // (app_kafka.go); it outlives this component on purpose - a detach / redock
  // remounts the pane and must find the cluster where it was. The handle is
  // closed when the tab's session is ended (endSession in kafkaState).

  import { onDestroy, untrack } from "svelte";
  import { api, type KafkaClusterInfo, type KafkaOverview, type KafkaTopicSummary } from "./api";
  import KafkaTopic from "./KafkaTopic.svelte";
  import KafkaGroups from "./KafkaGroups.svelte";
  import { kafkaTabs } from "./kafkaState.svelte";
  import { sessions } from "./stores.svelte";
  import { formatCount, parseConfigLines } from "./kafkaFormat";
  import { isMobile } from "./platform";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconRefresh, IconPlus, IconKafka } from "./iconMap";

  interface Props {
    sessionId: string;
  }
  let { sessionId }: Props = $props();

  let info = $state<KafkaClusterInfo | null>(null);
  $effect(() => {
    // The cached handle (set by whoever opened the tab) until the first
    // status call answers.
    if (!info) info = kafkaTabs.get(sessionId) ?? null;
  });
  let topics = $state<KafkaTopicSummary[] | null>(null);
  let loadErr = $state<string | null>(null);
  let loading = $state(false);
  let includeInternal = $state(false);
  let filter = $state("");
  let section = $state<"topics" | "groups" | "brokers">("topics");
  let selected = $state<string | null>(null);
  let creating = $state(false);
  let overview = $state<KafkaOverview | null>(null);

  const writable = $derived(!!info && !info.read_only && !isMobile);
  const shown = $derived((topics ?? []).filter((t) => !filter || t.name.toLowerCase().includes(filter.toLowerCase())));

  function setTabStatus(s: "connecting" | "connected" | "error", hint?: string) {
    sessions.setStatus(sessionId, s, hint);
  }

  async function refreshInfo() {
    try {
      info = await api.kafkaStatus(sessionId);
      kafkaTabs.set(info);
    } catch (e) {
      loadErr = errMsg(e);
    }
  }

  // Topics deleted from this tab, hidden until the cluster stops listing
  // them: a delete is asynchronous on the brokers, and the metadata a
  // reload gets right after it can still carry the topic.
  const deletedAt = new Map<string, number>();

  async function loadTopics() {
    loading = true;
    loadErr = null;
    if (!topics) setTabStatus("connecting");
    try {
      const now = Date.now();
      const listed = (await api.kafkaTopics(sessionId, includeInternal)) ?? [];
      for (const [name, at] of deletedAt) {
        if (now - at > 60_000 || !listed.some((t) => t.name === name)) deletedAt.delete(name);
      }
      topics = listed.filter((t) => !deletedAt.has(t.name));
      kafkaTabs.setTopics(sessionId, topics.map((t) => t.name));
      setTabStatus("connected");
      if (selected && !topics.some((t) => t.name === selected)) selected = null;
    } catch (e) {
      loadErr = errMsg(e);
      setTabStatus("error", loadErr);
    } finally {
      loading = false;
      refreshInfo();
    }
  }

  async function loadOverview() {
    try {
      overview = await api.kafkaOverview(sessionId);
    } catch (e) {
      toast.err(`Cluster overview: ${errMsg(e)}`);
    }
  }

  // The loaders read state they also write (topics, loading); untracked so
  // a finished load does not re-trigger the effect that started it.
  $effect(() => {
    void includeInternal;
    untrack(() => loadTopics());
  });
  $effect(() => {
    if (section === "brokers") untrack(() => loadOverview());
  });

  // While the SSH session is coming up, show where it is stuck.
  let statusTimer: ReturnType<typeof setInterval> | null = setInterval(() => {
    if (!loading) return;
    refreshInfo();
  }, 1500);
  onDestroy(() => {
    if (statusTimer) clearInterval(statusTimer);
    statusTimer = null;
  });

  // A quick-palette jump to a topic of this tab.
  $effect(() => {
    const p = kafkaTabs.pendingTopic;
    if (p && p.sessionId === sessionId) {
      kafkaTabs.pendingTopic = null;
      section = "topics";
      creating = false;
      selected = p.topic;
    }
  });

  // ----- new topic -----
  let newName = $state("");
  let newPartitions = $state(3);
  let newRF = $state(-1);
  let newConfigs = $state("");
  let createBusy = $state(false);

  async function createTopic() {
    const name = newName.trim();
    if (!name) return;
    const cfg = parseConfigLines(newConfigs);
    if (cfg.error) {
      toast.err(cfg.error);
      return;
    }
    createBusy = true;
    try {
      await api.kafkaCreateTopic(sessionId, { name, partitions: newPartitions, replication_factor: newRF, configs: cfg.set });
      toast.ok(`Created ${name}`);
      creating = false;
      newName = "";
      newConfigs = "";
      await loadTopics();
      selected = name;
    } catch (e) {
      toast.err(`Create failed: ${errMsg(e)}`);
    } finally {
      createBusy = false;
    }
  }

  const transportText = $derived.by(() => {
    if (!info) return "";
    if (info.transport === "tunnel") {
      if (info.state === "connecting" || info.state === "idle") return "Opening the SSH session...";
      if (info.state === "error") return `SSH: ${info.error ?? "connect failed"}`;
    }
    return "";
  });
</script>

<div class="kafka-pane">
  <aside class="side">
    <div class="sections">
      <button class:on={section === "topics"} onclick={() => (section = "topics")}>Topics</button>
      <button class:on={section === "groups"} onclick={() => (section = "groups")}>Groups</button>
      <button class:on={section === "brokers"} onclick={() => (section = "brokers")}>Brokers</button>
    </div>
    {#if section === "topics"}
      <div class="bar">
        <input placeholder="Filter topics" bind:value={filter} />
        <button title="Reload" disabled={loading} onclick={loadTopics}><IconRefresh size={12} /></button>
        {#if writable}
          <button title="New topic" onclick={() => { creating = true; selected = null; }}><IconPlus size={12} /></button>
        {/if}
      </div>
      <label class="chk"><input type="checkbox" bind:checked={includeInternal} /> internal topics</label>
      <div class="list">
        {#each shown as t (t.name)}
          <button class="item" class:sel={selected === t.name} onclick={() => { selected = t.name; creating = false; }} title={t.name}>
            <span class="tname">{t.name}</span>
            <span class="tmeta">
              {t.partitions}p · {formatCount(t.messages)}
              {#if t.offline > 0}<span class="bad" title="partitions without a leader">· {t.offline} offline</span>
              {:else if t.under_replicated > 0}<span class="warn" title="partitions with fewer in-sync replicas than replicas">· {t.under_replicated} under-replicated</span>{/if}
            </span>
          </button>
        {:else}
          {#if topics !== null}<div class="hint">{filter ? "No match." : "No topics."}</div>{/if}
        {/each}
      </div>
      {#if topics}<div class="foot">{shown.length} of {topics.length} topics</div>{/if}
    {/if}
  </aside>

  <section class="main">
    {#if loadErr && !topics}
      <div class="center">
        <IconKafka size={28} />
        <p class="error">{loadErr}</p>
        {#if transportText}<p class="hint">{transportText}</p>{/if}
        <button onclick={loadTopics}>Retry</button>
      </div>
    {:else if !topics}
      <div class="center">
        <div class="spinner"></div>
        <p class="hint">{transportText || `Connecting to ${info?.bootstrap.join(", ") ?? "the cluster"}...`}</p>
      </div>
    {:else if section === "groups"}
      <KafkaGroups clusterId={sessionId} {writable} sensitive={!!info?.sensitive} />
    {:else if section === "brokers"}
      <div class="scroll">
        {#if overview}
          <p class="meta">Cluster {overview.cluster_id || "(no id)"} · controller {overview.controller} · {overview.topics} topics · {overview.partitions} partitions</p>
          <table class="selectable">
            <thead><tr><th>ID</th><th>Advertised address</th><th>Rack</th></tr></thead>
            <tbody>
              {#each overview.brokers as b (b.id)}
                <tr><td>{b.id}{b.id === overview.controller ? " (controller)" : ""}</td><td>{b.host}:{b.port}</td><td>{b.rack ?? ""}</td></tr>
              {/each}
            </tbody>
          </table>
          <p class="hint">
            {info?.transport === "tunnel" ? "Every broker connection is a channel on one SSH session, so these names resolve on the SSH host - nothing is bound or mapped on this machine." : "Brokers are dialed directly (or from the last jump host)."}
          </p>
        {:else}
          <p class="hint">Loading...</p>
        {/if}
      </div>
    {:else if creating}
      <div class="scroll create">
        <h3>New topic</h3>
        <label>Name <input bind:value={newName} placeholder="orders.v1" /></label>
        <label>Partitions <input type="number" min="1" bind:value={newPartitions} /></label>
        <label>Replication factor <input type="number" min="-1" bind:value={newRF} title="-1 = the broker default" /></label>
        <label class="col">Config overrides <span class="hint">(optional, key=value a line)</span>
          <textarea bind:value={newConfigs} placeholder={"retention.ms=604800000\ncleanup.policy=delete"}></textarea>
        </label>
        <div class="row">
          <button class="primary" disabled={createBusy || !newName.trim()} onclick={createTopic}>Create</button>
          <button onclick={() => (creating = false)}>Cancel</button>
        </div>
      </div>
    {:else if selected}
      {#key selected}
        <KafkaTopic
          clusterId={sessionId}
          topic={selected}
          {writable}
          sensitive={!!info?.sensitive}
          onDeleted={() => {
            if (selected) {
              deletedAt.set(selected, Date.now());
              topics = (topics ?? []).filter((t) => t.name !== selected);
            }
            selected = null;
            setTimeout(loadTopics, 1500);
          }}
          onChanged={loadTopics}
        />
      {/key}
    {:else}
      <div class="center">
        <IconKafka size={28} />
        <p class="hint">
          {topics.length} topics on {info?.bootstrap.join(", ")}.
          {info?.read_only ? "This connection is read-only." : ""}
          Pick a topic to read its records.
        </p>
      </div>
    {/if}
  </section>
</div>

<style>
  /* Form controls of the whole Kafka tab (component styles are scoped, so
     the look other panes give their controls has to be stated here). Wrapped
     in :where() so these weigh nothing: any class rule of a component (the
     topic rows, the message rows) wins over them. */
  :global(:where(.kafka-pane) input:not([type="checkbox"])),
  :global(:where(.kafka-pane) select),
  :global(:where(.kafka-pane) textarea) {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.25rem 0.45rem; font-size: 0.75rem;
  }
  :global(:where(.kafka-pane) :is(input, select, textarea):focus) { outline: 1px solid var(--blue); border-color: var(--blue); }
  :global(:where(.kafka-pane) button) {
    background: var(--surface0); color: var(--text);
    border: 1px solid transparent; border-radius: 3px;
    padding: 0.22rem 0.55rem; font-size: 0.74rem; cursor: pointer;
    display: inline-flex; align-items: center; gap: 0.3rem;
  }
  :global(:where(.kafka-pane) button:hover:not(:disabled)) { background: var(--surface1); }
  :global(:where(.kafka-pane) button:disabled) { opacity: 0.5; cursor: default; }
  :global(:where(.kafka-pane) button.primary) { background: var(--blue); color: var(--crust); }
  .kafka-pane { display: grid; grid-template-columns: minmax(13rem, 18rem) 1fr; height: 100%; min-height: 0; background: var(--base); color: var(--text); }
  .side { display: flex; flex-direction: column; min-height: 0; border-right: 1px solid var(--surface0); background: var(--mantle); }
  .sections { display: flex; gap: 0.15rem; padding: 0.35rem 0.4rem 0; }
  .sections button { flex: 1; font-size: 0.74rem; }
  .sections button.on { color: var(--blue); border-color: var(--blue); }
  .bar { display: flex; gap: 0.25rem; padding: 0.35rem 0.4rem 0.15rem; }
  .bar input { flex: 1; min-width: 0; font-size: 0.75rem; }
  .chk { display: flex; gap: 0.3rem; align-items: center; padding: 0 0.6rem 0.3rem; font-size: 0.7rem; color: var(--subtext0); }
  .list { flex: 1; overflow: auto; }
  .item { display: flex; flex-direction: column; align-items: flex-start; width: 100%; padding: 0.25rem 0.6rem; background: none; border: none; border-radius: 0; text-align: left; color: var(--text); cursor: pointer; }
  .item:hover { background: var(--surface0); }
  .item.sel { background: color-mix(in srgb, var(--blue) 20%, transparent); }
  .tname { font-size: 0.77rem; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tmeta { font-size: 0.66rem; color: var(--subtext0); }
  .bad { color: var(--red); }
  .warn { color: var(--peach); }
  .foot { font-size: 0.66rem; color: var(--overlay1); padding: 0.25rem 0.6rem; border-top: 1px solid var(--surface0); }
  .main { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .main > :global(*) { flex: 1; min-height: 0; }
  .center { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 0.6rem; color: var(--overlay1); padding: 2rem; text-align: center; }
  .error { color: var(--red); font-size: 0.8rem; max-width: 40rem; white-space: pre-wrap; }
  .hint { color: var(--overlay1); font-size: 0.74rem; }
  .meta { font-size: 0.75rem; color: var(--subtext0); }
  .scroll { overflow: auto; padding: 0.6rem 0.8rem; }
  table { border-collapse: collapse; font-size: 0.75rem; width: 100%; user-select: text; }
  th { text-align: left; color: var(--subtext0); border-bottom: 1px solid var(--surface1); padding: 0.2rem 0.5rem; }
  td { padding: 0.15rem 0.5rem; border-bottom: 1px solid var(--surface0); }
  .create { display: flex; flex-direction: column; gap: 0.5rem; max-width: 34rem; }
  .create h3 { margin: 0 0 0.3rem; font-size: 0.9rem; }
  .create label { display: grid; grid-template-columns: 10rem 1fr; align-items: center; gap: 0.5rem; font-size: 0.78rem; }
  .create label.col { grid-template-columns: 1fr; }
  .create textarea { min-height: 6rem; font-family: var(--mono-font, monospace); font-size: 0.75rem; }
  .row { display: flex; gap: 0.5rem; }
  .spinner { width: 22px; height: 22px; border: 2px solid var(--surface1); border-top-color: var(--blue); border-radius: 50%; animation: spin 0.9s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (max-width: 700px) {
    .kafka-pane { grid-template-columns: 1fr; grid-template-rows: minmax(10rem, 35%) 1fr; }
    .side { border-right: none; border-bottom: 1px solid var(--surface0); }
  }
</style>
