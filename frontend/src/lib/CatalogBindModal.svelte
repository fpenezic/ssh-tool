<script lang="ts">
  // The connect dialog for unbound catalog slots (catalogBindGate): one row
  // per slot the connection needs, in connect order, each bound to
  // something of the user's own. A one-time code slot may be skipped (the
  // user types the code); the skip is remembered on this machine.
  import SearchableSelect from "./SearchableSelect.svelte";
  import { api, type CatalogPendingSlot } from "./api";
  import { bindGate } from "./catalogBindGate.svelte";
  import { catalogs } from "./catalog.svelte";
  import { credentials, tree, view } from "./stores.svelte";
  import { networkProfiles } from "./networkProfiles.svelte";
  import { renderMarkdown } from "./markdown";
  import { errMsg } from "./connectErrors";
  import { IconKey, IconVpn, IconBastion, IconX, IconAlertTriangle, IconUsers, IconExternalLink } from "./iconMap";
  import { externalBackendLabel } from "./catalogMarks";

  const slots = $derived(bindGate.pending?.slots ?? []);
  const key = (s: CatalogPendingSlot) => s.catalog_id + "/" + s.slot_uuid;
  let choice = $state<Record<string, string>>({});
  let skip = $state<Record<string, boolean>>({});
  let busy = $state(false);
  let err = $state("");

  // A new set of slots starts clean; one-time code rows start skipped.
  let shownFor = "";
  $effect(() => {
    const id = slots.map(key).join(",");
    if (id === shownFor) return;
    shownFor = id;
    choice = {};
    skip = Object.fromEntries(slots.filter((s) => s.role === "otp").map((s) => [key(s), true]));
    err = "";
    if (slots.length > 0) {
      void credentials.load();
      void networkProfiles.load();
    }
  });

  const credOptions = $derived(
    credentials.list
      .filter((c) => !c.id.startsWith("catcred-") && c.kind !== "api_token" && c.kind !== "certificate")
      .map((c) => ({ value: c.id, label: c.name })),
  );
  const netOptions = $derived(networkProfiles.list.map((p) => ({ value: p.id, label: p.name })));
  const jumpOptions = $derived(
    tree.connections
      .filter((c) => c.protocol !== "local" && c.protocol !== "web" && c.catalog_state !== "removed" && !bindGate.pending?.ids.includes(c.id))
      .map((c) => ({ value: c.id, label: c.name + (c.hostname ? ` (${c.hostname})` : "") })),
  );
  const optionsFor = (s: CatalogPendingSlot) => (s.type === "cred" ? credOptions : s.type === "net" ? netOptions : jumpOptions);
  const roleLabel = { jump: "Jump host", net: "Network profile", cred: "Credential", otp: "One-time code" } as const;
  const noneYet = { cred: "You have no credentials yet.", net: "You have no network profiles yet.", jump: "You have no connections to use as a bastion yet." } as const;

  const ready = $derived(slots.length > 0 && slots.every((s) => skip[key(s)] || !!choice[key(s)]));

  async function bindAll() {
    busy = true;
    err = "";
    try {
      for (const s of slots) {
        if (skip[key(s)]) await api.catalogSkipOTP(s.catalog_id, s.slot_uuid);
        else await api.catalogBind(s.catalog_id, s.slot_uuid, choice[key(s)]);
      }
      void tree.load();
      void catalogs.load();
      bindGate.finish(true);
    } catch (e) {
      err = errMsg(e);
    } finally {
      busy = false;
    }
  }
  function cancel() {
    if (!busy) bindGate.finish(false);
  }
  function goCreate(tab: "credentials" | "settings") {
    bindGate.finish(false);
    view.setTab(tab);
  }
  async function openExternal(url: string) {
    try {
      await api.openURL(url);
    } catch (e) {
      err = errMsg(e);
    }
  }
  function onDocClick(e: MouseEvent) {
    const a = (e.target as HTMLElement).closest("a");
    if (!a) return;
    e.preventDefault();
    const href = a.getAttribute("href");
    if (href) api.openURL(href);
  }
</script>

{#if bindGate.pending}
  <div class="overlay" role="presentation">
    <div class="modal" role="dialog" aria-modal="true" tabindex="-1" onkeydown={(e) => { if (e.key === "Escape") cancel(); }}>
      <header>
        <h2><IconUsers size={15} /> Choose your own for the catalog</h2>
        <button class="close" onclick={cancel} title="Cancel (Esc)" disabled={busy}><IconX size={14} /></button>
      </header>
      <p class="hint">This connection comes from a catalog. It uses {slots.length === 1 ? "a placeholder" : "placeholders"} that each colleague fills with their own; pick yours once and every connection that uses {slots.length === 1 ? "it" : "them"} works.</p>

      {#each slots as s (key(s))}
        <div class="row">
          <div class="row-head">
            <span class="icon">{#if s.role === "jump"}<IconBastion size={14} />{:else if s.role === "net"}<IconVpn size={14} />{:else}<IconKey size={14} />{/if}</span>
            <strong>{roleLabel[s.role]}</strong> <span class="name">"{s.name}"</span>
            <span class="dim">used by {s.connections} {s.connections === 1 ? "connection" : "connections"} in this catalog ({s.catalog_name}){#if s.folder_name}, defined on {s.folder_name}{/if}</span>
            {#if s.open_url}
              <button class="open-ext" onclick={() => openExternal(s.open_url!)} title={"Open this item in " + externalBackendLabel(s.backend) + " with your own account, for example to ask for access"}><IconExternalLink size={12} /> Open in {externalBackendLabel(s.backend)}</button>
            {/if}
          </div>
          {#if s.doc_md}
            <!-- eslint-disable-next-line svelte/no-at-html-tags -->
            <div class="doc md" role="presentation" onclick={onDocClick}>{@html renderMarkdown(s.doc_md)}</div>
          {/if}
          {#if s.role === "otp"}
            <label class="skip"><input type="checkbox" checked={skip[key(s)]} onchange={(e) => (skip = { ...skip, [key(s)]: (e.currentTarget as HTMLInputElement).checked })} disabled={busy} /> I type the code myself (asked once; change it later under Settings - Catalogs)</label>
          {/if}
          {#if !skip[key(s)]}
            {#if optionsFor(s).length === 0}
              <p class="hint">{noneYet[s.type]} <button class="link" onclick={() => goCreate(s.type === "cred" ? "credentials" : "settings")}>Add one first</button></p>
            {:else}
              <SearchableSelect value={choice[key(s)] ?? ""} options={optionsFor(s)} placeholder={`Pick a ${roleLabel[s.role].toLowerCase()}...`} emptyLabel="(choose)"
                onChange={(v: string) => (choice = { ...choice, [key(s)]: v })} />
            {/if}
          {/if}
        </div>
      {/each}

      {#if err}<p class="err"><IconAlertTriangle size={13} /> {err}</p>{/if}
      <footer>
        <button onclick={cancel} disabled={busy}>Cancel</button>
        <button class="primary" onclick={bindAll} disabled={busy || !ready}>{busy ? "Binding..." : "Bind and connect"}</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  /* The overlay scrolls a tall list (the buttons stay reachable); the
     dialog has no scroll area of its own, so the pickers drop down past
     its edge. */
  .overlay { position: fixed; inset: 0; background: rgba(17, 17, 27, 0.6); z-index: 1000; display: flex; align-items: safe center; justify-content: center; overflow-y: auto; padding: 4vh 0; }
  .modal { background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: var(--r-md); width: min(560px, 94vw); display: flex; flex-direction: column; gap: 0.6rem; padding: 1rem 1.2rem; box-shadow: 0 8px 30px rgba(0,0,0,0.5); }
  header { display: flex; align-items: center; justify-content: space-between; }
  header h2 { margin: 0; font-size: var(--fs-md); display: flex; align-items: center; gap: 6px; }
  .close { background: transparent; border: 0; color: var(--subtext0); cursor: pointer; padding: 0.15rem 0.35rem; border-radius: var(--r-sm); }
  .hint, .dim { color: var(--subtext0); font-size: var(--fs-sm); margin: 0; line-height: 1.45; }
  .row { display: flex; flex-direction: column; gap: 5px; border: 1px solid var(--surface0); border-radius: var(--r-md); padding: 6px 8px; }
  .row-head { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; font-size: var(--fs-sm); }
  .icon { color: var(--peach); display: inline-flex; }
  .open-ext {
    margin-left: auto; background: var(--surface0); color: var(--text); border: 0; border-radius: var(--r-sm);
    height: var(--ctl-h-sm); padding: 0 6px; font: inherit; font-size: var(--fs-sm); cursor: pointer;
    display: inline-flex; align-items: center; gap: 4px;
  }
  .open-ext:hover { background: var(--surface1); }
  .name { font-family: var(--font-mono, monospace); }
  .doc { font-size: var(--fs-sm); color: var(--subtext1); border-left: 2px solid var(--surface1); padding-left: 8px; }
  .doc :global(p) { margin: 0.2em 0; }
  .skip { display: flex; align-items: center; gap: 6px; font-size: var(--fs-sm); }
  .err { color: var(--red); font-size: var(--fs-sm); margin: 0; display: flex; gap: 5px; align-items: flex-start; }
  .link { background: none; border: 0; padding: 0; color: var(--blue); cursor: pointer; text-decoration: underline; font-size: inherit; }
  footer { display: flex; justify-content: flex-end; gap: 0.5rem; border-top: 1px solid var(--surface0); padding-top: 0.5rem; }
  button.primary { background: var(--blue); color: var(--base); border: 0; }
</style>
