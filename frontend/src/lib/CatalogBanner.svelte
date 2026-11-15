<script lang="ts">
  // The catalog banner at the top of a catalog folder's or connection's
  // editor: where it comes from, what stays the catalog's, the catalog's
  // documentation for it, the user's local changes with "reset to
  // catalog", and, for a record that left its catalog, keep / delete.
  import { api, type CatalogRecordInfo } from "./api";
  import { catalogs } from "./catalog.svelte";
  import { renderMarkdown } from "./markdown";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { tree } from "./stores.svelte";
  import { IconUsers, IconAlertTriangle, IconLock } from "./iconMap";

  interface Props {
    kind: "folders" | "connections";
    id: string;
    name: string;
    rec: CatalogRecordInfo;
    onChanged: () => void;
  }
  let { kind, id, name, rec, onChanged }: Props = $props();

  // Human names for the layer's keys (the rest show their key).
  const fieldNames: Record<string, string> = {
    username: "Username", port: "Port", auth_ref: "Credential", otp_ref: "One-time code",
    jump_host: "Jump host", network_profile_id: "Network", initial_command: "Initial command",
    tunnel_only: "Tunnel only", tunnel_autostart: "Start the tunnel with the app", sensitive: "Sensitive",
    kafka_read_only: "Kafka read-only", db_read_only: "Database read-only", db_targets: "Databases",
    db_addr: "Database address", db_name: "Database name", kafka_bootstrap: "Kafka bootstrap",
    color_tag: "Color tag", keepalive_interval: "Keepalive", terminal_type: "Terminal type",
    auto_reconnect: "Auto reconnect", verbose: "Verbose", vnc_port: "VNC port",
  };
  const local = $derived(Object.keys(rec.layer ?? {}));

  async function reset(keys: string[]) {
    const next = { ...(rec.layer ?? {}) } as Record<string, unknown>;
    for (const k of keys) delete next[k];
    try {
      await api.catalogSetLayer(kind, id, JSON.stringify(next));
      toast.ok(keys.length === 1 ? `${fieldNames[keys[0]] ?? keys[0]} reset to the catalog's` : "Reset to the catalog's settings");
      onChanged();
    } catch (e) {
      toast.err("Reset: " + errMsg(e), 8000);
    }
  }

  async function keepLocal() {
    try {
      await api.catalogKeepLocal(kind, id);
      toast.ok(`${name} is yours now`);
      await tree.load();
      onChanged();
    } catch (e) {
      toast.err("Keep as local: " + errMsg(e), 8000);
    }
  }
  async function del() {
    const ok = await showConfirm({ title: "Delete", message: `Delete "${name}"${kind === "folders" ? " and everything in it" : ""}? It left its catalog; your local settings on it go too.`, okLabel: "Delete", danger: true });
    if (!ok) return;
    try {
      await api.catalogDeleteRemoved(kind, id);
      await tree.load();
    } catch (e) {
      toast.err("Delete: " + errMsg(e), 8000);
    }
  }

  function onDocClick(e: MouseEvent) {
    const a = (e.target as HTMLElement | null)?.closest("a[data-md-link]") as HTMLAnchorElement | null;
    if (!a) return;
    e.preventDefault();
    const href = a.getAttribute("href");
    if (href) api.openURL(href);
  }
</script>

<div class="cat-banner span-2" class:removed={rec.removed}>
  <div class="cb-head">
    <IconUsers size={14} />
    <span>From the catalog <button class="linklike" onclick={() => catalogs.open(rec.catalog_id)}>{rec.catalog_name}</button></span>
    {#if rec.updated_by}<span class="cb-meta">last changed by {rec.updated_by}</span>{/if}
  </div>
  {#if rec.removed}
    <div class="cb-removed">
      <IconAlertTriangle size={13} />
      <span>Removed from the catalog. You had local settings here, so it was kept. Keep it as your own, or delete it.</span>
      <button onclick={keepLocal}>Keep as local</button>
      <button class="danger" onclick={del}>Delete</button>
    </div>
  {:else}
    <p class="cb-note"><IconLock size={11} /> {kind === "connections" ? "Name, address, tags and notes" : "The name"} are the catalog's. Settings you change here stay on this machine.</p>
  {/if}
  {#if rec.doc_md}
    <!-- eslint-disable-next-line svelte/no-at-html-tags -->
    <div class="cb-doc md" role="presentation" onclick={onDocClick}>{@html renderMarkdown(rec.doc_md)}</div>
  {/if}
  {#if rec.suggested_tunnel_autostart}
    <p class="cb-note">The catalog suggests starting this tunnel with the app. It is up to you: Tunnel tab, "Start with the app".</p>
  {/if}
  {#if local.length > 0}
    <div class="cb-local">
      <span>Your changes:</span>
      {#each local as k (k)}
        <span class="cb-chip">{fieldNames[k] ?? k}<button title="Reset to the catalog's value" onclick={() => reset([k])}>reset</button></span>
      {/each}
      {#if local.length > 1}<button class="linklike" onclick={() => reset(local)}>reset all to catalog</button>{/if}
    </div>
  {/if}
</div>

<style>
  .cat-banner { border: 1px solid color-mix(in srgb, var(--sapphire) 35%, var(--border, #333)); border-radius: var(--r-md); padding: 8px 10px; display: flex; flex-direction: column; gap: 6px; margin-bottom: 8px; }
  .cat-banner.removed { border-color: color-mix(in srgb, var(--yellow) 55%, var(--border, #333)); }
  .cb-head { display: flex; align-items: center; gap: 6px; font-weight: 600; }
  .cb-meta { margin-left: auto; font-weight: 400; font-size: var(--fs-sm); opacity: 0.65; }
  .cb-note { margin: 0; font-size: var(--fs-sm); opacity: 0.8; display: flex; align-items: center; gap: 4px; }
  .cb-removed { display: flex; align-items: center; gap: 8px; color: var(--yellow); font-size: var(--fs-sm); }
  .cb-removed span { flex: 1; }
  .cb-doc { font-size: var(--fs-sm); padding: 4px 8px; border-left: 2px solid var(--sapphire); }
  .cb-local { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: var(--fs-sm); }
  .cb-chip { display: inline-flex; align-items: center; gap: 4px; padding: 1px 4px 1px 8px; border-radius: 999px; border: 1px solid color-mix(in srgb, var(--teal) 50%, transparent); color: var(--teal); }
  .cb-chip button, .linklike { background: none; border: none; padding: 0 2px; color: var(--sapphire); cursor: pointer; font-size: var(--fs-sm); }
  .danger { color: var(--danger, #e66); }
</style>
