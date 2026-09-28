<script lang="ts">
  // Check TLS certificates: read the certificate each host serves on the
  // given ports and show how long it has left. Results feed the tree's
  // "cert Nd" badges through fleet.recordTls.
  import { onMount } from "svelte";
  import { api, type TLSCertResult } from "./api";
  import { fleet, TLS_WARN_DAYS, type FleetOpen } from "./fleetStore.svelte";
  import FleetShell from "./FleetShell.svelte";
  import { errMsg } from "./connectErrors";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();

  let portsText = $state("443");
  let running = $state(false);
  let err = $state("");
  let results = $state<TLSCertResult[] | null>(null);

  const ports = $derived(
    portsText.split(/[\s,]+/).map((p) => parseInt(p, 10)).filter((p) => p > 0 && p < 65536),
  );

  async function run() {
    running = true;
    err = "";
    try {
      results = (await api.checkTLSCerts({ connection_ids: scope.ids, ports })) ?? [];
      await fleet.recordTls(results);
    } catch (e: any) {
      err = errMsg(e);
    } finally {
      running = false;
    }
  }
  // One host from the pane's Tools menu: no form worth showing, just check.
  onMount(() => {
    if (scope.ids.length === 1) void run();
  });

  const sorted = $derived(
    [...(results ?? [])].sort((a, b) => {
      const rank = (r: TLSCertResult) => (r.state === "ok" ? 0 : r.state === "error" ? 1 : 2);
      return rank(a) - rank(b) || a.days_left - b.days_left || a.name.localeCompare(b.name);
    }),
  );
  const skipped = $derived((results ?? []).filter((r) => r.state === "skipped").length);
  function daysClass(d: number): string {
    return d < 7 ? "ferr" : d < TLS_WARN_DAYS ? "fwarn" : "";
  }
</script>

<FleetShell
  title="TLS certificates"
  sub={`${scope.label} · ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"} · dialled directly from this machine, by the name each connection uses`}
  onClose={() => fleet.close()}
>
  <div class="row">
    <label for="tls-ports" class="fdim">Ports</label>
    <input id="tls-ports" class="finput" bind:value={portsText} placeholder="443, 8443" />
    <span class="fdim">Hosts reached by IP are skipped: a certificate is issued for a name.</span>
  </div>
  {#if err}<p class="ferr">{err}</p>{/if}
  {#if results}
    <table class="ftable">
      <thead><tr><th>Host</th><th>Port</th><th>Expires</th><th>Left</th><th>Subject</th><th>Issuer</th><th>Trust</th></tr></thead>
      <tbody>
        {#each sorted.filter((r) => r.state !== "skipped") as r (r.connection_id + ":" + r.port)}
          <tr>
            <td class="fmono" title={r.hostname}>{r.name}</td>
            <td>{r.port}</td>
            {#if r.state === "ok"}
              <td>{new Date(r.not_after * 1000).toLocaleDateString()}</td>
              <td class={daysClass(r.days_left)}>{r.days_left < 0 ? `expired ${-r.days_left}d ago` : `${r.days_left} days`}</td>
              <td>{r.subject}</td>
              <td class="fdim">{r.issuer}</td>
              <td>{#if r.trusted}<span class="fok">trusted</span>{:else}<span class="fwarn" title={r.trust_error}>not trusted</span>{/if}</td>
            {:else}
              <td colspan="5" class="ferr">{r.error}</td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
    {#if skipped}<p class="fdim">{skipped} skipped (connected by IP address).</p>{/if}
    <p class="fdim">Under {TLS_WARN_DAYS} days a certificate shows as a badge next to the host in the tree.</p>
  {/if}

  {#snippet footer()}
    <button class="fbtn" onclick={() => fleet.close()}>Close</button>
    <button class="fbtn primary" disabled={running || ports.length === 0} onclick={run}>
      {running ? "Checking…" : results ? "Check again" : "Check"}
    </button>
  {/snippet}
</FleetShell>

<style>
  .row { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.7rem; flex-wrap: wrap; }
  .row .finput { width: 10rem; }
</style>
