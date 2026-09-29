<script lang="ts">
  // Last facts snapshot for a folder, as four small cards under its Fleet
  // bar. Nothing shows until the folder has been gathered once; the numbers
  // are that run's, not live.
  import { fleet, fleetHostName, TLS_WARN_DAYS } from "./fleetStore.svelte";

  interface Props { folderId: string; ids: string[]; label: string; }
  let { folderId, ids, label }: Props = $props();

  $effect(() => { void fleet.snapshot(folderId); });
  const snap = $derived(fleet.snapshots[folderId] ?? null);
  const ok = $derived((snap?.results ?? []).filter((r) => r.state === "ok"));

  function memClass(kb: number): string {
    const g = kb / 1048576;
    const steps = [0.5, 1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512, 768, 1024];
    return `${steps.find((x) => g <= x * 1.02) ?? Math.round(g)}G`;
  }
  const sizes = $derived.by(() => {
    const m = new Map<string, number>();
    for (const r of ok) {
      if (!r.facts.cpu_cores) continue;
      const k = `${r.facts.cpu_cores}c/${memClass(r.facts.mem_kb)}`;
      m.set(k, (m.get(k) ?? 0) + 1);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]).slice(0, 4);
  });
  const reboot = $derived(ok.filter((r) => r.facts.reboot === "yes").length);
  const security = $derived(ok.filter((r) => r.facts.security > 0).length);
  const failed = $derived(ok.filter((r) => r.facts.failed > 0).length);
  const has = (k: string) => !!snap?.facts.includes(k);
  const certs = $derived(
    ids.map((id) => ({ id, d: fleet.daysLeft(id) }))
      .filter((x): x is { id: string; d: number } => x.d !== null && x.d < TLS_WARN_DAYS)
      .sort((a, b) => a.d - b.d),
  );
  function nameOf(id: string): string {
    return fleetHostName(id, snap?.results.find((r) => r.connection_id === id)?.name ?? fleet.tls[id]?.name);
  }
</script>

{#if snap || certs.length}
  <div class="cards">
    {#if snap}
      <div class="card">
        <div class="h">Last facts</div>
        <div>{new Date(snap.at).toLocaleString()} · {ok.length} / {snap.results.length} hosts</div>
        <button class="link" onclick={() => fleet.show({ tool: "facts", ids, label, folderId, showSnapshot: true })}>Open report</button>
      </div>
      {#if sizes.length}
        <div class="card">
          <div class="h">Sizes</div>
          <div>{sizes.map(([k, n]) => `${k} ×${n}`).join(" · ")}</div>
        </div>
      {/if}
      {#if has("reboot") || has("updates") || has("failed")}
        <div class="card" class:warn={reboot || security || failed}>
          <div class="h">Maintenance</div>
          <div>
            {#if has("reboot")}{reboot} need reboot{/if}{#if has("updates")}{has("reboot") ? " · " : ""}{security} with security updates{/if}{#if has("failed")}{has("reboot") || has("updates") ? " · " : ""}{failed} with failed units{/if}
          </div>
        </div>
      {/if}
    {/if}
    {#if certs.length}
      <div class="card bad" title={`From the last TLS check of each host in this folder and its subfolders:\n${certs.map((c) => `${nameOf(c.id)} ${c.d < 0 ? "expired" : `in ${c.d}d`}`).join("\n")}`}>
        <div class="h">TLS</div>
        <div>{certs.slice(0, 3).map((c) => `${nameOf(c.id)} ${c.d < 0 ? "expired" : `in ${c.d}d`}`).join(" · ")}{certs.length > 3 ? ` +${certs.length - 3}` : ""}</div>
        <div class="acts">
          <button class="link" onclick={() => fleet.show({ tool: "tls", ids: certs.map((c) => c.id), label: `${label} · expiring`, folderId })}>Check again</button>
          <button class="link" title="Hides these warnings here and in the tree until the next TLS check of those hosts" onclick={() => fleet.clearTls(certs.map((c) => c.id))}>Clear</button>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .acts { display: flex; gap: 0.8rem; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); gap: 0.5rem; margin-bottom: 0.8rem; }
  .card { background: var(--mantle); border: 1px solid var(--surface0); border-radius: 6px; padding: 0.45rem 0.65rem; font-size: 0.78rem; }
  .card.warn { border-color: color-mix(in srgb, var(--yellow) 45%, transparent); }
  .card.bad { border-color: color-mix(in srgb, var(--red) 45%, transparent); }
  .h { color: var(--subtext0); font-size: 0.7rem; margin-bottom: 0.15rem; }
  .card.warn .h { color: var(--yellow); }
  .card.bad .h { color: var(--red); }
  .link { background: none; border: 0; padding: 0; margin-top: 0.15rem; color: var(--blue); font: inherit; cursor: pointer; }
  .link:hover { text-decoration: underline; }
</style>
