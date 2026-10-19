<script lang="ts">
  // The Kafka section of the connection and folder editors. Edits a
  // KafkaDraft in place; the parent saves it with applyKafkaDraft. Styled
  // like the VNC section next to it (a switch heading a collapsible block).

  import { credentials } from "./stores.svelte";
  import { KAFKA_NONE, type KafkaDraft } from "./kafkaDraft";
  import { createLoginCredential } from "./loginModal.svelte.ts";

  const NEW_LOGIN = "__new_login__";

  interface Props {
    draft: KafkaDraft;
    // "(inherit from folder)" on a connection, "(inherit from parent)" on a
    // folder.
    inheritLabel: string;
    // Whether Kafka is on once inheritance is applied - the block opens
    // for an inherited "on" too.
    effectiveOn: boolean;
    onOpen?: () => void;
  }
  let { draft = $bindable(), inheritLabel, effectiveOn, onOpen }: Props = $props();

  // A SASL login is a username + password: password / vault credentials,
  // and ones backed by KeePass / Bitwarden / Infisical (also kind password).
  const credOptions = $derived(
    credentials
      .flatGrouped()
      .filter((g) => g.cred.kind === "password" || g.cred.kind === "vault" || g.cred.id === draft.cred)
      .map((g) => ({ value: g.cred.id, label: g.label })),
  );

  function cycle() {
    draft = { ...draft, enabled: draft.enabled === "on" ? "off" : "on" };
  }
</script>

<div class="kafka-section" class:on={effectiveOn}>
  <div class="vnc-head">
    <button type="button" class="vnc-switch" role="switch" aria-label="Enable Kafka" aria-checked={draft.enabled === "on" || (draft.enabled === "" && effectiveOn)} onclick={cycle}>
      <span class="vnc-knob"></span>
    </button>
    <span class="vnc-title">Kafka</span>
    {#if draft.enabled === ""}<span class="inh">{effectiveOn ? "on, inherited" : "inherited"}</span>{/if}
    <span class="vnc-spacer"></span>
    {#if draft.enabled !== ""}
      <button type="button" class="link" title="Inherit Kafka on/off again" onclick={() => (draft = { ...draft, enabled: "" })}>inherit</button>
    {/if}
    {#if effectiveOn && onOpen}
      <button type="button" class="vnc-open" onclick={onOpen}>Open Kafka</button>
    {/if}
  </div>
  {#if effectiveOn}
    <div class="vnc-grid">
      <label class="span-2" title="Seed brokers, comma separated; a missing port means 9092. Written as the brokers are seen from where they are dialed: the SSH host when reached through SSH (often 127.0.0.1:9092 on a broker host).">Bootstrap servers
        <input bind:value={draft.bootstrap} placeholder={inheritLabel === "(inherit from folder)" ? "(inherit) e.g. 127.0.0.1:9092" : "e.g. broker1:9092,broker2:9092"} spellcheck="false" />
      </label>
      <label title="Through SSH: one SSH session to this host carries every broker connection, and broker names resolve there. Direct: brokers are dialed from the last jump host, or from here over the network profile.">Reach the brokers
        <select bind:value={draft.tunnel}>
          <option value="">{inheritLabel} · through SSH</option>
          <option value="on">Through SSH (from this host)</option>
          <option value="off">Direct / from the jump host</option>
        </select>
      </label>
      <label title="Refuse every write - produce, topic and group changes - in the backend, not only in the UI.">Writes
        <select bind:value={draft.readOnly}>
          <option value="">{inheritLabel}</option>
          <option value="off">Allowed</option>
          <option value="on">Read-only</option>
        </select>
      </label>
      <label>TLS
        <select bind:value={draft.tls}>
          <option value="">{inheritLabel}</option>
          <option value="off">Plaintext</option>
          <option value="on">TLS (inside the tunnel)</option>
        </select>
      </label>
      {#if draft.tls === "on"}
        <label title="Accept any broker certificate. Only for test clusters with self-signed certificates.">Certificate check
          <select bind:value={draft.tlsSkip}>
            <option value="">{inheritLabel}</option>
            <option value="off">Verify</option>
            <option value="on">Skip (insecure)</option>
          </select>
        </label>
      {/if}
      <label>SASL
        <select bind:value={draft.sasl}>
          <option value="">{inheritLabel}</option>
          <option value={KAFKA_NONE}>None</option>
          <option value="plain">PLAIN</option>
          <option value="scram-sha-256">SCRAM-SHA-256</option>
          <option value="scram-sha-512">SCRAM-SHA-512</option>
        </select>
      </label>
      {#if draft.sasl !== "" && draft.sasl !== KAFKA_NONE || draft.cred !== ""}
        <label title="Its default username and its password are the SASL login.">SASL credential
          <select value={draft.cred} onchange={async (e) => {
            const v = (e.currentTarget as HTMLSelectElement).value;
            if (v !== NEW_LOGIN) { draft = { ...draft, cred: v }; return; }
            (e.currentTarget as HTMLSelectElement).value = draft.cred;
            const id = await createLoginCredential("Kafka SASL");
            if (id) draft = { ...draft, cred: id };
          }}>
            <option value="">{inheritLabel}</option>
            <option value={KAFKA_NONE}>None</option>
            {#each credOptions as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
            <option value={NEW_LOGIN}>+ New login...</option>
          </select>
        </label>
      {/if}
    </div>
  {/if}
</div>

<style>
  /* Same look as DetailPane's VNC section; repeated here because those rules
     are scoped to DetailPane. */
  .kafka-section {
    grid-column: 1 / -1;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.6rem 0.75rem;
    background: var(--surface1);
  }
  .vnc-head { display: flex; align-items: center; gap: 0.55rem; font-size: 0.85rem; color: var(--text1); }
  .vnc-spacer { flex: 1 1 auto; }
  .vnc-title { font-weight: 600; }
  .vnc-switch {
    position: relative; width: 34px; height: 18px; border-radius: 9px;
    border: 1px solid var(--border); background: var(--surface2); padding: 0;
    cursor: pointer; flex: 0 0 auto; transition: background 0.15s ease, border-color 0.15s ease;
  }
  .kafka-section.on .vnc-switch { background: var(--accent, #4a8); border-color: var(--accent, #4a8); }
  .vnc-knob {
    position: absolute; top: 1px; left: 1px; width: 14px; height: 14px;
    border-radius: 50%; background: var(--text1); transition: transform 0.15s ease;
  }
  .kafka-section.on .vnc-knob { transform: translateX(16px); background: #fff; }
  .vnc-open {
    font-size: 0.78rem; padding: 0.2rem 0.6rem; background: var(--surface2); color: var(--text1);
    border: 1px solid var(--border); border-radius: 4px; cursor: pointer;
  }
  .vnc-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.6rem; margin-top: 0.6rem; }
  .vnc-grid .span-2 { grid-column: 1 / -1; }
  .vnc-grid label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--subtext0); }
  /* DetailPane's field look (its input / select rules are scoped). */
  .vnc-grid input, .vnc-grid select {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  .vnc-grid input:focus, .vnc-grid select:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .inh { font-size: 0.7rem; color: var(--overlay1); }
  .link { background: none; border: none; color: var(--overlay1); font-size: 0.7rem; cursor: pointer; text-decoration: underline; }
  @media (max-width: 640px) {
    .vnc-grid { grid-template-columns: 1fr; }
  }
</style>
