<script lang="ts">
  import { IconKeyRound } from "./iconMap";
  import type { AuthPromptQuestion } from "./stores.svelte";

  interface Props {
    promptId: string;
    kind: "username" | "auth";
    label: string;
    host: string;
    port: number;
    name?: string;
    instruction?: string;
    questions: AuthPromptQuestion[];
    // onRespond(answers) submits; onRespond(null) cancels.
    onRespond: (answers: string[] | null) => void;
    queueLength?: number;
  }
  let { kind, label, host, port, name, instruction, questions, onRespond, queueLength = 0 }: Props = $props();

  // One answer per question, initially blank. questions is fixed for the
  // lifetime of this modal instance (a new prompt mounts a fresh component),
  // so capturing its length once is intentional.
  // svelte-ignore state_referenced_locally
  let answers = $state<string[]>(new Array(questions.length).fill(""));

  const title = $derived(kind === "username" ? "Username required" : "Authentication required");

  // Focus the first field on mount. HTML autofocus is unreliable for a
  // dynamically mounted modal (and doesn't re-fire), so grab the first input
  // via an action and focus it. A microtask defer lets the overlay settle so
  // the focus sticks.
  function setFirst(node: HTMLInputElement, index: number) {
    if (index === 0) {
      queueMicrotask(() => node.focus());
    }
  }

  function submit() {
    onRespond([...answers]);
  }
  function cancel() {
    onRespond(null);
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      submit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1" onkeydown={onKey}>
  <div class="modal">
    <header>
      <span class="icon"><IconKeyRound size={18} /></span>
      <h1>{title}</h1>
    </header>

    {#if kind === "username"}
      <p>
        <strong>{host}:{port}</strong> ({label}) has no username configured.
        Enter one to connect.
      </p>
    {:else}
      <p>
        <strong>{host}:{port}</strong> ({label}) is asking for authentication.
        {#if name}<span class="srv">{name}</span>{/if}
      </p>
      {#if instruction}<p class="instr">{instruction}</p>{/if}
    {/if}

    <div class="fields">
      {#each questions as q, i (i)}
        <label>
          <span class="q">{kind === "username" ? "Username" : q.text}</span>
          {#if q.echo || kind === "username"}
            <input type="text" bind:value={answers[i]} autocomplete="off"
              use:setFirst={i} />
          {:else}
            <input type="password" bind:value={answers[i]} autocomplete="off"
              use:setFirst={i} />
          {/if}
        </label>
      {/each}
    </div>

    {#if queueLength > 0}
      <p class="queue-hint">
        ⓘ <strong>{queueLength}</strong> more prompt{queueLength === 1 ? "" : "s"} waiting after this one.
      </p>
    {/if}

    <div class="row">
      <button onclick={cancel}>Cancel</button>
      <button class="primary" onclick={submit}>Continue</button>
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
    width: min(460px, 92vw); padding: 1.25rem;
    box-shadow: 0 10px 40px rgba(0,0,0,0.6);
  }
  header {
    display: flex; align-items: center; gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .icon { font-size: 1.3rem; }
  h1 { margin: 0; font-size: 1rem; font-weight: 600; }
  p { margin: 0.5rem 0; font-size: 0.875rem; line-height: 1.5; }
  .srv { color: var(--overlay0); }
  .instr { color: var(--overlay0); font-size: 0.82rem; white-space: pre-wrap; }
  .fields { display: flex; flex-direction: column; gap: 0.6rem; margin: 0.75rem 0; }
  .fields label { display: flex; flex-direction: column; gap: 0.25rem; }
  .fields .q { font-size: 0.8rem; color: var(--overlay0); }
  .fields input {
    background: var(--crust); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.4rem 0.55rem; font: inherit; width: 100%; box-sizing: border-box;
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
