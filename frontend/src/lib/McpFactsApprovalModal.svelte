<script lang="ts" module>
  // Mirrors the Go McpFactsApprovalRequest (event: mcp_facts_approval_request).
  export interface FactsApprovalRequest {
    approval_id: string;
    scope: string;
    hosts: { id: string; name: string; folder: string }[];
    facts: { key: string; label: string; group: string }[];
    script: string;
    timeout_seconds: number;
    skipped: string[] | null;
    stored: boolean;
  }
</script>

<script lang="ts">
  // An external LLM asks to collect read-only facts from many hosts. The
  // user sees every host (and can untick some), every fact and the exact
  // script; nothing connects before Approve.
  import { IconBot } from "./iconMap";

  interface Props {
    req: FactsApprovalRequest;
    onRespond: (approve: boolean, hostIds: string[]) => void;
  }
  let { req, onRespond }: Props = $props();

  // One modal per request (App mounts a new one), so the initial host list
  // is the list.
  // svelte-ignore state_referenced_locally
  let keep = $state<Record<string, boolean>>(Object.fromEntries(req.hosts.map((h) => [h.id, true])));
  let showScript = $state(false);
  const kept = $derived(req.hosts.filter((h) => keep[h.id]).map((h) => h.id));
  const byFolder = $derived.by(() => {
    const m = new Map<string, typeof req.hosts>();
    for (const h of req.hosts) m.set(h.folder || "(root)", [...(m.get(h.folder || "(root)") ?? []), h]);
    return [...m.entries()];
  });
  function setAll(v: boolean) {
    keep = Object.fromEntries(req.hosts.map((h) => [h.id, v]));
  }
</script>

<div class="overlay" role="dialog" aria-modal="true">
  <div class="modal">
    <header>
      <span class="icon"><IconBot size={18} /></span>
      <h1>LLM wants to gather facts from {req.hosts.length} host{req.hosts.length === 1 ? "" : "s"}</h1>
    </header>
    <p>
      An external LLM asks to connect to these hosts and run fixed, read-only
      commands as the login user (no sudo), {Math.min(8, req.hosts.length)} at a
      time, {req.timeout_seconds}s each once connected. The results go back to the LLM{req.stored ? " and are stored in the folder's report history" : ""}.
      Untick hosts to leave them out.
    </p>
    <p class="summary">Scope: {req.scope}</p>

    {#if req.skipped?.length}
      <div class="warn">{#each req.skipped as s}<div>Not offered: {s}</div>{/each}</div>
    {/if}

    <div class="cols">
      <div class="box">
        <div class="section-label">
          Hosts · {kept.length} of {req.hosts.length}
          <span class="spacer"></span>
          <button class="link" onclick={() => setAll(true)}>all</button>
          <button class="link" onclick={() => setAll(false)}>none</button>
        </div>
        {#each byFolder as [folder, hosts] (folder)}
          <div class="folder">{folder}</div>
          {#each hosts as h (h.id)}
            <label class="host"><input type="checkbox" bind:checked={keep[h.id]} />{h.name}</label>
          {/each}
        {/each}
      </div>
      <div class="box">
        <div class="section-label">Facts · {req.facts.length}</div>
        {#each req.facts as f (f.key)}<div class="fact">{f.label}</div>{/each}
        <button class="link script-toggle" onclick={() => (showScript = !showScript)}>{showScript ? "Hide" : "Show"} the exact script</button>
      </div>
    </div>
    {#if showScript}<pre class="script">{req.script}</pre>{/if}

    <footer>
      <button class="btn" onclick={() => onRespond(false, [])}>Deny</button>
      <button class="btn primary" disabled={kept.length === 0} onclick={() => onRespond(true, kept)}>
        Gather from {kept.length} host{kept.length === 1 ? "" : "s"}
      </button>
    </footer>
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal {
    background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: 6px;
    width: min(760px, 94vw); padding: 1.25rem; box-shadow: 0 10px 40px rgba(0,0,0,0.6);
    display: flex; flex-direction: column; max-height: 88vh;
  }
  header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
  .icon { color: var(--blue); display: inline-flex; }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  p { margin: 0.4rem 0; font-size: 0.85rem; line-height: 1.5; }
  .summary { color: var(--overlay1); font-size: 0.8rem; font-weight: 600; }
  .warn { background: var(--crust); border-left: 3px solid var(--yellow); color: var(--yellow); font-size: 0.78rem; padding: 0.4rem 0.6rem; border-radius: 3px; margin: 0.4rem 0; }
  .cols { display: grid; grid-template-columns: 3fr 2fr; gap: 0.6rem; min-height: 0; flex: 1 1 auto; overflow: hidden; margin: 0.4rem 0; }
  .box { overflow-y: auto; border: 1px solid var(--surface0); border-radius: 4px; background: var(--mantle); padding: 0.5rem 0.7rem; font-size: 0.82rem; }
  .section-label { display: flex; gap: 0.4rem; align-items: center; color: var(--subtext0); font-size: 0.7rem; letter-spacing: 0.05em; text-transform: uppercase; margin-bottom: 0.3rem; }
  .spacer { flex: 1; }
  .folder { color: var(--overlay1); font-size: 0.75rem; margin: 0.4rem 0 0.1rem; }
  .host { display: flex; gap: 0.4rem; align-items: center; cursor: pointer; padding: 0.05rem 0; }
  .fact { padding: 0.08rem 0; }
  .link { background: none; border: 0; padding: 0; color: var(--blue); font: inherit; cursor: pointer; text-transform: none; letter-spacing: 0; }
  .link:hover { text-decoration: underline; }
  .script-toggle { margin-top: 0.5rem; font-size: 0.78rem; }
  .script { max-height: 10rem; overflow: auto; background: var(--crust); border-radius: 4px; padding: 0.5rem; font-size: 0.72rem; white-space: pre-wrap; word-break: break-all; margin: 0 0 0.4rem; }
  footer { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.5rem; }
  .btn { background: var(--surface0); color: var(--text); border: 1px solid var(--surface1); border-radius: 4px; padding: 0.35rem 0.9rem; cursor: pointer; font: inherit; font-size: 0.85rem; }
  .btn.primary { background: var(--blue); color: var(--base); border-color: var(--blue); font-weight: 600; }
  .btn:disabled { opacity: 0.5; cursor: default; }
</style>
