<script lang="ts">
  // Send one record to a topic. Key / value are text, or base64 for binary;
  // "null" checkboxes send a null key / value (a null value is a tombstone).

  import { api, type KafkaPayloadInput } from "./api";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { IconSend, IconPlus, IconX } from "./iconMap";

  interface Props {
    clusterId: string;
    topic: string;
    partitions: number[];
    onSent?: () => void;
  }
  let { clusterId, topic, partitions, onSent }: Props = $props();

  let key = $state("");
  let keyNull = $state(true);
  let value = $state("");
  let valueNull = $state(false);
  let binary = $state(false);
  let partition = $state(-1);
  let headers = $state<{ key: string; value: string }[]>([]);
  let sending = $state(false);
  let last = $state<string | null>(null);

  const jsonError = $derived.by(() => {
    const t = value.trim();
    if (binary || valueNull || !(t.startsWith("{") || t.startsWith("["))) return "";
    try {
      JSON.parse(t);
      return "";
    } catch (e) {
      return String((e as Error).message);
    }
  });

  function payload(text: string): KafkaPayloadInput {
    return { text, encoding: binary ? "base64" : "utf8" };
  }

  async function send() {
    sending = true;
    try {
      const r = await api.kafkaProduce(clusterId, {
        topic,
        partition,
        key: keyNull ? null : payload(key),
        value: valueNull ? null : payload(value),
        headers: headers.filter((h) => h.key.trim()).map((h) => ({ key: h.key.trim(), value: { text: h.value, encoding: "utf8" } })),
      });
      last = `Sent to partition ${r.partition} at offset ${r.offset}`;
      onSent?.();
    } catch (e) {
      toast.err(`Produce: ${errMsg(e)}`);
    } finally {
      sending = false;
    }
  }

  function prettify() {
    try {
      value = JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      // not JSON; leave it
    }
  }
</script>

<div class="producer">
  <div class="row">
    <label>Partition
      <select bind:value={partition}>
        <option value={-1}>Auto (by key)</option>
        {#each partitions as p (p)}<option value={p}>{p}</option>{/each}
      </select>
    </label>
    <label class="chk" title="Key, value and headers are base64 (binary payloads)">
      <input type="checkbox" bind:checked={binary} /> base64
    </label>
  </div>

  <div class="field">
    <div class="lbl">Key <label class="chk"><input type="checkbox" bind:checked={keyNull} /> null</label></div>
    <input class="mono" bind:value={key} disabled={keyNull} placeholder={keyNull ? "null key" : "key"} />
  </div>

  <div class="field grow">
    <div class="lbl">
      Value <label class="chk"><input type="checkbox" bind:checked={valueNull} /> null (tombstone)</label>
      <span class="spacer"></span>
      {#if jsonError}<span class="warn" title={jsonError}>not valid JSON</span>{/if}
      <button disabled={valueNull || binary} onclick={prettify}>Format JSON</button>
    </div>
    <textarea class="mono" bind:value={value} disabled={valueNull} spellcheck="false" placeholder={valueNull ? "null value" : "value"}></textarea>
  </div>

  <div class="field">
    <div class="lbl">Headers <button title="Add header" onclick={() => (headers = [...headers, { key: "", value: "" }])}><IconPlus size={12} /></button></div>
    {#each headers as h, i (i)}
      <div class="hdr">
        <input class="mono" placeholder="name" bind:value={h.key} />
        <input class="mono" placeholder="value" bind:value={h.value} />
        <button title="Remove" onclick={() => (headers = headers.filter((_, j) => j !== i))}><IconX size={12} /></button>
      </div>
    {/each}
  </div>

  <div class="row">
    <button class="send" disabled={sending} onclick={send}><IconSend size={12} /> {sending ? "Sending..." : `Send to ${topic}`}</button>
    {#if last}<span class="ok">{last}</span>{/if}
  </div>
</div>

<style>
  .producer { display: flex; flex-direction: column; gap: 0.5rem; padding: 0.6rem; height: 100%; box-sizing: border-box; min-height: 0; }
  .row { display: flex; gap: 0.8rem; align-items: center; font-size: 0.75rem; }
  .row label { display: flex; gap: 0.35rem; align-items: center; }
  .field { display: flex; flex-direction: column; gap: 0.25rem; }
  .field.grow { flex: 1; min-height: 6rem; }
  .lbl { display: flex; gap: 0.6rem; align-items: center; font-size: 0.72rem; color: var(--subtext0); }
  .chk { display: flex; gap: 0.25rem; align-items: center; font-size: 0.7rem; color: var(--subtext0); }
  .spacer { flex: 1; }
  .mono { font-family: var(--mono-font, monospace); font-size: 0.75rem; }
  textarea { flex: 1; resize: none; min-height: 5rem; }
  .hdr { display: grid; grid-template-columns: 12rem 1fr auto; gap: 0.3rem; }
  .warn { color: var(--peach); }
  .ok { color: var(--green); }
  .send { color: var(--blue); }
</style>
