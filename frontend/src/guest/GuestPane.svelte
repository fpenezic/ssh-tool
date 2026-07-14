<script lang="ts">
  // Recursive pane-tree renderer for the guest. Mirrors the desktop PaneNode
  // split/leaf structure but read/stream-only: a leaf is a GuestTerminal, a
  // split is two children with a fixed ratio. No drag-to-resize, no toggles.
  import type { PaneNode } from "../lib/panetypes";
  import type { GuestClient, Level, ManifestSession } from "./ws";
  import GuestTerminal from "./GuestTerminal.svelte";
  import GuestPane from "./GuestPane.svelte";

  interface Props {
    node: PaneNode;
    sessions: Map<string, ManifestSession>;
    level: Level;
    client: GuestClient;
  }
  let { node, sessions, level, client }: Props = $props();
</script>

{#if node.kind === "pane"}
  {@const meta = sessions.get(node.sessionId)}
  {#if node.view === "unavailable"}
    <div class="unavailable">
      This pane (SFTP / console) isn't available in a shared view.
    </div>
  {:else if meta}
    <GuestTerminal
      slot={node.sessionId}
      cols={meta.cols}
      rows={meta.rows}
      {level}
      {client}
    />
  {:else}
    <div class="unavailable">Unknown session.</div>
  {/if}
{:else}
  <div class="split" class:vertical={node.direction === "vertical"}>
    <div class="pane-a" style="flex-basis: {node.ratio * 100}%">
      <GuestPane node={node.a} {sessions} {level} {client} />
    </div>
    <div class="divider"></div>
    <div class="pane-b" style="flex-basis: {(1 - node.ratio) * 100}%">
      <GuestPane node={node.b} {sessions} {level} {client} />
    </div>
  </div>
{/if}

<style>
  .split {
    display: flex;
    flex-direction: row;
    width: 100%;
    height: 100%;
  }
  .split.vertical {
    flex-direction: column;
  }
  .pane-a,
  .pane-b {
    overflow: hidden;
    min-width: 0;
    min-height: 0;
  }
  .divider {
    background: #313244;
    flex: 0 0 2px;
  }
  .unavailable {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    color: #a6adc8;
    font-size: 0.85rem;
    padding: 1rem;
    text-align: center;
  }
</style>
