<script lang="ts">
  // Approval modal for an LLM (MCP bridge) command. For a "run" request the
  // choices are Run (execute via side channel) or Deny. For a "type" request
  // the choice is Type-into-terminal (inject the text at the prompt with NO
  // Enter, so the user reviews and submits) or Deny.
  import { IconBot } from "./iconMap";
  import type { McpDecision } from "./api";

  interface Props {
    sessionName: string;
    kind: "run" | "type" | "connect";
    command: string;
    queueLength?: number;
    onRespond: (decision: McpDecision) => void;
  }
  let { sessionName, kind, command, queueLength = 0, onRespond }: Props = $props();

  const title = $derived(
    kind === "run" ? "run a command"
    : kind === "type" ? "type into the terminal"
    : "open a connection",
  );
</script>

<div class="overlay" role="dialog" aria-modal="true">
  <div class="modal">
    <header>
      <span class="icon"><IconBot size={18} /></span>
      <h1>LLM wants to {title}</h1>
    </header>
    {#if kind === "connect"}
      <p>
        An external LLM is requesting to open an SSH session for
        <strong>{command}</strong> and work on it. Approving spends the saved
        credentials for this connection.
      </p>
    {:else}
      <p>
        An external LLM is requesting to
        {kind === "run" ? "run this command on" : "type this into"}
        <strong>{sessionName}</strong>. Review it before allowing.
      </p>
      <pre class="cmd">{command}</pre>
    {/if}

    {#if kind === "type"}
      <p class="hint">
        On approval the text is typed at the prompt without pressing Enter -
        you review it in the terminal and submit it yourself.
      </p>
    {:else if kind === "run"}
      <p class="hint">
        This command isn't on the read-only allowlist, so it needs your
        approval. It runs on a side channel and its output goes back to the LLM.
      </p>
    {:else}
      <p class="hint">
        The session opens as if you clicked Connect, and is then shared with the
        LLM. A host-key prompt may still appear the first time.
      </p>
    {/if}

    {#if queueLength > 0}
      <p class="queue-hint">
        ⓘ <strong>{queueLength}</strong> more request{queueLength === 1 ? "" : "s"} waiting after this one.
      </p>
    {/if}

    <div class="row">
      <button onclick={() => onRespond("deny")}>Deny</button>
      {#if kind === "run"}
        <button class="primary" onclick={() => onRespond("run")}>Run command</button>
      {:else if kind === "connect"}
        <button class="primary" onclick={() => onRespond("run")}>Connect</button>
      {:else}
        <button class="primary" onclick={() => onRespond("type")}>Type into terminal</button>
      {/if}
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
    width: min(560px, 92vw); padding: 1.25rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.6);
  }
  header {
    display: flex; align-items: center; gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .icon { color: var(--blue); display: inline-flex; }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  p { margin: 0.5rem 0; font-size: 0.875rem; line-height: 1.5; }
  .hint { color: var(--overlay0); font-size: 0.8rem; }
  .cmd {
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    padding: 0.5rem 0.6rem;
    margin: 0.6rem 0;
    font-family: ui-monospace, monospace;
    font-size: 0.82rem;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 30vh;
    overflow-y: auto;
  }
  .queue-hint {
    background: var(--crust);
    border-left: 3px solid var(--blue);
    color: var(--blue);
    font-size: 0.8rem;
    padding: 0.4rem 0.6rem;
    margin-top: 0.8rem;
    border-radius: 3px;
  }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 1rem; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.4rem 0.85rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--lavender); }
</style>
