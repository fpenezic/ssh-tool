<script lang="ts">
  // Record mode for writers of a catalog record: the switch between "My
  // settings" (this machine's local layer, the default) and "Catalog
  // record" (the record itself, for everyone with access to its folder),
  // the documentation editor, and what a save needs answered first: names
  // for new slots, or a conflict with a change someone else saved.
  import type { CatalogRecordInfo, CatalogSaveResult } from "./api";
  import { IconUsers, IconAlertTriangle, IconLock } from "./iconMap";

  interface Props {
    rec: CatalogRecordInfo;
    kind: "folders" | "connections";
    recordMode: boolean;
    onMode: (on: boolean) => void;
    doc: string;
    pending: CatalogSaveResult | null;
    slotNames: Record<string, string>;
    resolve: Record<string, string>;
    stillLocal: string[];
    busy: boolean;
    onRetry: () => void;
    onDiscard: () => void;
  }
  let { rec, kind, recordMode, onMode, doc = $bindable(), pending, slotNames = $bindable(), resolve = $bindable(), stillLocal, busy, onRetry, onDiscard }: Props = $props();

  const fieldNames: Record<string, string> = {
    name: "Name", hostname: "Address", protocol: "Protocol", tags: "Tags", notes: "Notes", doc_md: "Documentation",
    icon_name: "Icon", icon_color: "Icon colour", "settings.username": "Username", "settings.port": "Port",
    "settings.auth_ref": "Credential", "settings.otp_ref": "One-time code", "settings.jump_host": "Jump host",
    "settings.network_profile_id": "Network", "settings.initial_command": "Initial command", "settings.sensitive": "Sensitive",
    "settings.kafka_read_only": "Kafka read-only", "settings.db_read_only": "Database read-only",
  };
  const label = (f: string) => fieldNames[f] ?? f.replace(/^settings\./, "");
  const conflicts = $derived(pending?.plan?.conflicts ?? []);
  const needSlots = $derived(pending?.plan?.need_slots ?? []);
</script>

<div class="rm span-2" class:on={recordMode}>
  <div class="rm-switch" role="radiogroup" aria-label="What Save changes">
    <button type="button" class:active={!recordMode} onclick={() => onMode(false)} disabled={busy}>My settings <span class="dim">this machine</span></button>
    <button type="button" class:active={recordMode} onclick={() => onMode(true)} disabled={busy}><IconUsers size={12} /> Catalog record <span class="dim">everyone</span></button>
  </div>
  {#if recordMode}
    <p class="rm-note"><IconUsers size={12} /> Save changes the record for everyone with access to "{rec.folder_name}"{kind === "folders" ? ", and through the folder outline also readers of every folder below it" : ""}. Your own local settings and bindings stay as they are.</p>
    <label class="rm-doc">Documentation <span class="dim">(markdown, shown to everyone; never put secrets here)</span>
      <textarea rows="3" bind:value={doc} placeholder="How to get access, who to ask, what this is for"></textarea>
    </label>
    {#if stillLocal.length > 0}
      <p class="rm-local"><IconLock size={11} /> Saved for everyone, but your local value still applies on this machine for: {stillLocal.map(label).join(", ")}. Reset it in My settings to follow the catalog.</p>
    {/if}
  {/if}

  {#if needSlots.length > 0}
    <div class="rm-box">
      <p><strong>New slot{needSlots.length === 1 ? "" : "s"} for this record</strong> <span class="dim">placeholders each colleague binds to their own; yours is bound to what you picked. The name is what they see.</span></p>
      {#each needSlots as s (s.key)}
        <label class="rm-slot">{s.type === "cred" ? "Credential" : s.type === "net" ? "Network" : "Bastion"} for your "{s.bind_name}"
          <input value={slotNames[s.key] ?? s.name} oninput={(e) => (slotNames = { ...slotNames, [s.key]: (e.currentTarget as HTMLInputElement).value })} />
        </label>
      {/each}
      <div class="rm-actions">
        <button type="button" onclick={onDiscard} disabled={busy}>Cancel</button>
        <button type="button" class="primary" onclick={() => { for (const s of needSlots) if (!(s.key in slotNames)) slotNames = { ...slotNames, [s.key]: s.name }; onRetry(); }} disabled={busy}>Create and save</button>
      </div>
    </div>
  {/if}

  {#if conflicts.length > 0}
    <div class="rm-box conflict">
      <p><IconAlertTriangle size={12} /> <strong>Someone saved this record since you opened it.</strong> Your changes, next to what is in the catalog now:</p>
      <table>
        <thead><tr><th>Field</th><th>When you opened it</th><th>Yours</th><th>Now in the catalog</th><th></th></tr></thead>
        <tbody>
          {#each conflicts as c (c.field)}
            <tr class:both={c.both}>
              <td>{label(c.field)}</td>
              <td class="v">{c.base || "(empty)"}</td>
              <td class="v">{c.mine || "(empty)"}</td>
              <td class="v">{c.theirs || "(empty)"}</td>
              <td>
                {#if c.both}
                  <label><input type="radio" name={"r-" + c.field} checked={(resolve[c.field] ?? "mine") === "mine"} onchange={() => (resolve = { ...resolve, [c.field]: "mine" })} /> keep yours</label>
                  <label><input type="radio" name={"r-" + c.field} checked={resolve[c.field] === "theirs"} onchange={() => (resolve = { ...resolve, [c.field]: "theirs" })} /> keep theirs</label>
                {:else}
                  <span class="dim">only you changed it</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      <div class="rm-actions">
        <button type="button" onclick={onDiscard} disabled={busy}>Discard mine</button>
        <button type="button" class="primary" onclick={onRetry} disabled={busy}>Re-apply my changes</button>
      </div>
    </div>
  {/if}
</div>

<style>
  .rm { display: flex; flex-direction: column; gap: 6px; margin-bottom: 4px; }
  .rm-switch { display: inline-flex; align-self: flex-start; border: 1px solid var(--surface1); border-radius: var(--r-md); overflow: hidden; }
  .rm-switch button { border: 0; border-radius: 0; background: transparent; padding: 3px 10px; display: inline-flex; align-items: center; gap: 4px; font-size: var(--fs-sm); }
  .rm-switch button.active { background: var(--surface0); color: var(--text); }
  .rm.on .rm-switch button.active { background: color-mix(in srgb, var(--peach) 30%, var(--surface0)); }
  .dim { color: var(--subtext0); font-size: var(--fs-sm); }
  .rm-note { margin: 0; font-size: var(--fs-sm); color: var(--peach); display: flex; gap: 5px; align-items: flex-start; }
  .rm-local { margin: 0; font-size: var(--fs-sm); color: var(--teal); display: flex; gap: 5px; align-items: flex-start; }
  .rm-doc { display: flex; flex-direction: column; gap: 2px; font-size: var(--fs-sm); }
  .rm-box { border: 1px solid var(--surface1); border-radius: var(--r-md); padding: 6px 8px; font-size: var(--fs-sm); display: flex; flex-direction: column; gap: 6px; }
  .rm-box p { margin: 0; }
  .rm-box.conflict { border-color: color-mix(in srgb, var(--yellow) 55%, var(--surface1)); }
  .rm-slot { display: flex; flex-direction: column; gap: 2px; }
  table { border-collapse: collapse; width: 100%; }
  th, td { text-align: left; padding: 2px 6px; vertical-align: top; border-bottom: 1px solid var(--surface0); }
  th { color: var(--subtext0); font-weight: 400; }
  td.v { font-family: var(--font-mono, monospace); word-break: break-all; white-space: pre-wrap; }
  tr.both td:first-child { color: var(--yellow); font-weight: 600; }
  td label { display: flex; gap: 4px; align-items: center; white-space: nowrap; }
  .rm-actions { display: flex; justify-content: flex-end; gap: 6px; }
  button.primary { background: var(--blue); color: var(--base); border: 0; }
</style>
