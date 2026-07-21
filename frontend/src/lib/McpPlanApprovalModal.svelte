<script lang="ts">
  // Rich approval modal for an LLM (MCP bridge) provisioning plan. The LLM
  // staged folders / connections / forwards / SOCKS bookmarks via the create_*
  // tools; commit_plan renders the whole tree here for the user to approve or
  // reject. Credentials and network profiles are shown by NAME only - the LLM
  // never sets or sees secrets. Approve writes everything in one transaction;
  // reject discards the plan.
  import { IconBot } from "./iconMap";

  // Mirrors the Go McpPlanPreview payload (event: mcp_plan_approval_request).
  export interface PlanForwardPreview {
    kind: string;
    detail: string;
    bookmarks: string[];
  }
  export interface PlanConnPreview {
    name: string;
    target: string;
    folder: string;
    credential: string;
    via: string;
    network_profile: string;
    initial_command: string;
    forwards: PlanForwardPreview[];
  }
  export interface PlanFolderPreview {
    name: string;
    parent: string;
    defaults?: string[];
  }
  export interface PlanCounts {
    folders: number;
    connections: number;
    forwards: number;
    bookmarks: number;
  }
  export interface PlanPreview {
    approval_id: string;
    folders: PlanFolderPreview[];
    connections: PlanConnPreview[];
    warnings: string[];
    counts: PlanCounts;
  }

  interface Props {
    preview: PlanPreview;
    onRespond: (decision: "run" | "deny") => void;
  }
  let { preview, onRespond }: Props = $props();

  const c = $derived(preview.counts);
  const summary = $derived(
    [
      c.folders ? `${c.folders} folder${c.folders === 1 ? "" : "s"}` : "",
      c.connections ? `${c.connections} connection${c.connections === 1 ? "" : "s"}` : "",
      c.forwards ? `${c.forwards} forward${c.forwards === 1 ? "" : "s"}` : "",
      c.bookmarks ? `${c.bookmarks} bookmark${c.bookmarks === 1 ? "" : "s"}` : "",
    ]
      .filter(Boolean)
      .join(", "),
  );
</script>

<div class="overlay" role="dialog" aria-modal="true">
  <div class="modal">
    <header>
      <span class="icon"><IconBot size={18} /></span>
      <h1>LLM wants to create connections</h1>
    </header>
    <p>
      An external LLM has prepared the following to add to your connection tree.
      Nothing is written until you approve. It references existing vault
      credentials by name and never sets passwords.
    </p>
    <p class="summary">{summary || "empty plan"}</p>

    {#if preview.warnings?.length}
      <div class="warn">
        {#each preview.warnings as w}
          <div>! {w}</div>
        {/each}
      </div>
    {/if}

    <div class="tree">
      {#if preview.folders?.length}
        <div class="section-label">Folders</div>
        {#each preview.folders as f}
          <div class="folder">
            <div class="folder-head">
              <span class="fname">{f.name}</span>
              <span class="meta">in {f.parent}</span>
            </div>
            {#if f.defaults?.length}
              <div class="conn-meta">
                {#each f.defaults as d}<span class="chip">{d}</span>{/each}
              </div>
            {/if}
          </div>
        {/each}
      {/if}

      {#if preview.connections?.length}
        <div class="section-label">Connections</div>
        {#each preview.connections as conn}
          <div class="conn">
            <div class="conn-head">
              <span class="cname">{conn.name}</span>
              {#if conn.target}<span class="target">{conn.target}</span>{/if}
            </div>
            <div class="conn-meta">
              {#if conn.folder && conn.folder !== "(root)"}<span class="chip">folder: {conn.folder}</span>{/if}
              {#if conn.via}<span class="chip">via {conn.via}</span>{/if}
              {#if conn.network_profile}<span class="chip">net: {conn.network_profile}</span>{/if}
              {#if conn.credential}<span class="chip">cred: {conn.credential}</span>{/if}
              {#if conn.initial_command}<span class="chip">init: {conn.initial_command}</span>{/if}
            </div>
            {#each conn.forwards as fw}
              <div class="fwd">
                <span class="fwd-kind">{fw.kind}</span>
                <span class="fwd-detail">{fw.detail}</span>
                {#if fw.bookmarks?.length}
                  <div class="bookmarks">
                    {#each fw.bookmarks as bm}
                      <div class="bm">{bm}</div>
                    {/each}
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/each}
      {/if}
    </div>

    <div class="row">
      <button onclick={() => onRespond("deny")}>Reject</button>
      <button class="primary" onclick={() => onRespond("run")}>Approve and create</button>
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(680px, 94vw); padding: 1.25rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.6);
    display: flex; flex-direction: column; max-height: 88vh;
  }
  header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
  .icon { color: var(--blue); display: inline-flex; }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  p { margin: 0.4rem 0; font-size: 0.85rem; line-height: 1.5; }
  .summary { color: var(--overlay1); font-size: 0.8rem; font-weight: 600; }
  .warn {
    background: var(--crust); border-left: 3px solid var(--yellow);
    color: var(--yellow); font-size: 0.78rem; padding: 0.4rem 0.6rem;
    border-radius: 3px; margin: 0.5rem 0;
  }
  .tree {
    overflow-y: auto; margin: 0.5rem 0; padding-right: 0.25rem;
    border: 1px solid var(--surface0); border-radius: 4px;
    background: var(--mantle); padding: 0.6rem 0.7rem;
  }
  .section-label {
    font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--overlay0); margin: 0.5rem 0 0.3rem;
  }
  .section-label:first-child { margin-top: 0; }
  .folder { padding: 0.2rem 0; }
  .folder-head { display: flex; align-items: baseline; gap: 0.5rem; }
  .fname { font-weight: 600; }
  .meta { color: var(--overlay0); font-size: 0.75rem; }
  .conn {
    padding: 0.4rem 0; border-top: 1px solid var(--surface0);
  }
  .conn:first-of-type { border-top: 0; }
  .conn-head { display: flex; align-items: baseline; gap: 0.5rem; }
  .cname { font-weight: 600; }
  .target {
    font-family: ui-monospace, monospace; font-size: 0.78rem; color: var(--subtext0);
  }
  .conn-meta { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-top: 0.25rem; }
  .chip {
    background: var(--surface0); border-radius: 3px;
    padding: 0.05rem 0.4rem; font-size: 0.72rem; color: var(--subtext1);
  }
  .fwd {
    margin: 0.3rem 0 0 0.8rem; padding-left: 0.5rem;
    border-left: 2px solid var(--surface1);
    font-size: 0.78rem;
  }
  .fwd-kind {
    display: inline-block; background: var(--blue); color: var(--on-accent);
    border-radius: 3px; padding: 0 0.35rem; font-size: 0.68rem; font-weight: 600;
    margin-right: 0.4rem;
  }
  .fwd-detail { font-family: ui-monospace, monospace; color: var(--subtext0); }
  .bookmarks { margin: 0.2rem 0 0.1rem 0.4rem; }
  .bm {
    font-size: 0.74rem; color: var(--subtext0); font-family: ui-monospace, monospace;
    padding: 0.05rem 0;
  }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.75rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.85rem; border-radius: 3px; cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--lavender); }
</style>
