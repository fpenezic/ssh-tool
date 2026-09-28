<script lang="ts">
  // Mounted once in App: whichever fleet dialog is open, over its hosts.
  import { onMount } from "svelte";
  import { fleet } from "./fleetStore.svelte";
  import FactsModal from "./FactsModal.svelte";
  import TlsModal from "./TlsModal.svelte";
  import CompareModal from "./CompareModal.svelte";
  import CopyKeyModal from "./CopyKeyModal.svelte";

  onMount(() => { void fleet.init(); });
</script>

{#if fleet.open}
  {#key fleet.open}
    {#if fleet.open.tool === "facts"}
      <FactsModal scope={fleet.open} />
    {:else if fleet.open.tool === "tls"}
      <TlsModal scope={fleet.open} />
    {:else if fleet.open.tool === "compare"}
      <CompareModal scope={fleet.open} />
    {:else}
      <CopyKeyModal scope={fleet.open} />
    {/if}
  {/key}
{/if}
