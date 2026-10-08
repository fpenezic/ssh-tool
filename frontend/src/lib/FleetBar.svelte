<script lang="ts">
  // The Fleet row in the right pane for a folder or a multi-selection: the
  // tools that act on many hosts at once, in plain sight instead of behind
  // a right-click. Read-only tools first; the one that writes stands apart.
  import { fleet, type FleetTool } from "./fleetStore.svelte";
  import { IconTable, IconShieldCheck, IconCompare, IconKeyRound, IconUpload, IconDownload } from "./iconMap";

  interface Props {
    ids: string[];
    label: string;
    folderId?: string;
  }
  let { ids, label, folderId }: Props = $props();

  function open(tool: FleetTool) {
    fleet.show({ tool, ids, label, folderId });
  }
</script>

<div class="fleet-bar" role="toolbar" aria-label="Fleet tools">
  <span class="tag">Fleet</span>
  <button disabled={ids.length === 0} onclick={() => open("facts")}><IconTable size={13} />Gather facts…</button>
  <button disabled={ids.length === 0} onclick={() => open("tls")}><IconShieldCheck size={13} />Check TLS certificates…</button>
  <button disabled={ids.length < 2} title={ids.length < 2 ? "Needs two or more hosts" : ""} onclick={() => open("compare")}><IconCompare size={13} />Compare file…</button>
  <span class="sep"></span>
  <button disabled={ids.length === 0} onclick={() => open("download")}><IconDownload size={13} />Download files…</button>
  <span class="sep"></span>
  <button disabled={ids.length === 0} onclick={() => open("upload")}><IconUpload size={13} />Upload file…</button>
  <button disabled={ids.length === 0} onclick={() => open("copykey")}><IconKeyRound size={13} />Copy SSH key…</button>
  {#if ids.length === 0}<span class="none">No SSH hosts here.</span>{/if}
</div>

<style>
  .fleet-bar {
    display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap;
    padding: 0.45rem 0.6rem; margin: 0.4rem 0 0.6rem;
    background: var(--mantle); border: 1px solid var(--surface0); border-radius: 6px;
  }
  .tag { font-size: 0.66rem; letter-spacing: 0.08em; text-transform: uppercase; color: var(--subtext0); margin-right: 0.3rem; }
  button {
    display: inline-flex; align-items: center; gap: 0.35rem;
    background: var(--surface0); color: var(--text); border: 1px solid var(--surface1);
    border-radius: 5px; font: inherit; font-size: 0.78rem; padding: 0.22rem 0.65rem; cursor: pointer;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.5; cursor: default; }
  .sep { width: 1px; height: 18px; background: var(--surface1); margin: 0 0.2rem; }
  .none { color: var(--subtext0); font-size: 0.76rem; }
</style>
