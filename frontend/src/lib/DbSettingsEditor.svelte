<script lang="ts">
  // The Database section of the connection and folder editors. Edits a
  // DbDraft in place; the parent saves it with applyDbDraft. Same look as
  // the Kafka section (a switch heading a collapsible block).

  import { credentials } from "./stores.svelte";
  import { DB_NONE, type DbDraft } from "./dbDraft";

  interface Props {
    draft: DbDraft;
    // "(inherit from folder)" on a connection, "(inherit from parent)" on a
    // folder.
    inheritLabel: string;
    // Whether the database is on once inheritance is applied.
    effectiveOn: boolean;
    // The engine once inheritance is applied, for the port hint.
    effectiveEngine: string;
    onOpen?: () => void;
  }
  let { draft = $bindable(), inheritLabel, effectiveOn, effectiveEngine, onOpen }: Props = $props();

  // A login is a username + password: password / vault credentials, and
  // ones backed by KeePass / Bitwarden / Infisical (also kind password).
  const credOptions = $derived(
    credentials
      .flatGrouped()
      .filter((g) => g.cred.kind === "password" || g.cred.kind === "vault" || g.cred.id === draft.cred)
      .map((g) => ({ value: g.cred.id, label: g.label })),
  );
  const engine = $derived(draft.engine || effectiveEngine || "postgres");
  const port = $derived(engine === "mysql" ? 3306 : 5432);

  function cycle() {
    draft = { ...draft, enabled: draft.enabled === "on" ? "off" : "on" };
  }
</script>

<div class="db-section" class:on={effectiveOn}>
  <div class="vnc-head">
    <button type="button" class="vnc-switch" role="switch" aria-label="Enable the database" aria-checked={draft.enabled === "on" || (draft.enabled === "" && effectiveOn)} onclick={cycle}>
      <span class="vnc-knob"></span>
    </button>
    <span class="vnc-title">Database</span>
    {#if draft.enabled === ""}<span class="inh">{effectiveOn ? "on, inherited" : "inherited"}</span>{/if}
    <span class="vnc-spacer"></span>
    {#if draft.enabled !== ""}
      <button type="button" class="link" title="Inherit the database on/off again" onclick={() => (draft = { ...draft, enabled: "" })}>inherit</button>
    {/if}
    {#if effectiveOn && onOpen}
      <button type="button" class="vnc-open" onclick={onOpen}>Open database</button>
    {/if}
  </div>
  {#if effectiveOn}
    <div class="vnc-grid">
      <label>Engine
        <select bind:value={draft.engine}>
          <option value="">{inheritLabel}{effectiveEngine ? ` · ${effectiveEngine === "mysql" ? "MySQL / MariaDB" : "PostgreSQL"}` : " · PostgreSQL"}</option>
          <option value="postgres">PostgreSQL</option>
          <option value="mysql">MySQL / MariaDB</option>
        </select>
      </label>
      <label title="Host and port as seen from where the database is dialed: the SSH host when reached through SSH (usually 127.0.0.1 on the database host itself). Empty: 127.0.0.1 and the engine's port.">Address
        <input bind:value={draft.addr} placeholder={`127.0.0.1:${port}`} spellcheck="false" />
      </label>
      <label title="The database the tab opens first. Empty: the server's default for the login.">Database
        <input bind:value={draft.name} placeholder={inheritLabel === "(inherit from folder)" ? "(inherit) e.g. app" : "e.g. app"} spellcheck="false" />
      </label>
      <label title="Its default username and its password are the database login. A credential without a password works for trust authentication.">Login
        <select bind:value={draft.cred}>
          <option value="">{inheritLabel}</option>
          <option value={DB_NONE}>None</option>
          {#each credOptions as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
        </select>
      </label>
      <label title="Through SSH: one SSH session to this host carries every database connection, and the address resolves there. Direct: dialed from the last jump host, or from here over the network profile.">Reach the database
        <select bind:value={draft.tunnel}>
          <option value="">{inheritLabel} · through SSH</option>
          <option value="on">Through SSH (from this host)</option>
          <option value="off">Direct / from the jump host</option>
        </select>
      </label>
      <label title="Read-only: every statement runs in a READ ONLY transaction that is rolled back, and only SELECT / WITH / SHOW / EXPLAIN / VALUES / TABLE / DESCRIBE are sent. A login with read-only grants is the real guarantee.">Writes
        <select bind:value={draft.readOnly}>
          <option value="">{inheritLabel}</option>
          <option value="off">Allowed</option>
          <option value="on">Read-only</option>
        </select>
      </label>
      <label title="TLS runs end to end, inside the tunnel. Require: encrypted, certificate not checked. Verify: the certificate must match the address's host name.">TLS
        <select bind:value={draft.tls}>
          <option value="">{inheritLabel}</option>
          <option value="disable">Off</option>
          <option value="require">Require (no certificate check)</option>
          <option value="verify-full">Verify certificate</option>
        </select>
      </label>
    </div>
  {/if}
</div>

<style>
  /* Same look as the Kafka section; repeated because those rules are
     scoped to KafkaSettingsEditor. */
  .db-section {
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
  .db-section.on .vnc-switch { background: var(--accent, #4a8); border-color: var(--accent, #4a8); }
  .vnc-knob {
    position: absolute; top: 1px; left: 1px; width: 14px; height: 14px;
    border-radius: 50%; background: var(--text1); transition: transform 0.15s ease;
  }
  .db-section.on .vnc-knob { transform: translateX(16px); background: #fff; }
  .vnc-open {
    font-size: 0.78rem; padding: 0.2rem 0.6rem; background: var(--surface2); color: var(--text1);
    border: 1px solid var(--border); border-radius: 4px; cursor: pointer;
  }
  .vnc-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.6rem; margin-top: 0.6rem; }
  .vnc-grid label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--subtext0); }
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
