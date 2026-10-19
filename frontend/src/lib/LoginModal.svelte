<script lang="ts">
  import { loginModal } from "./loginModal.svelte.ts";

  let user = $state("");
  let password = $state("");
  let save = $state(true);
  let name = $state("");
  let userEl = $state<HTMLInputElement | null>(null);

  $effect(() => {
    const p = loginModal.pending;
    if (p) {
      user = p.defaultUser;
      password = "";
      save = true;
      name = p.defaultName;
      setTimeout(() => userEl?.focus(), 0);
    }
  });

  function ok() {
    if (!user.trim()) {
      userEl?.focus();
      return;
    }
    loginModal.done({ user: user.trim(), password, save: save || !loginModal.pending?.canSkipSave, name: name.trim() });
  }
  function cancel() {
    loginModal.done(null);
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") { e.preventDefault(); ok(); }
    if (e.key === "Escape") { e.preventDefault(); cancel(); }
  }
</script>

{#if loginModal.pending}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="overlay" onkeydown={onKey} role="dialog" aria-modal="true" tabindex="-1">
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <!-- No close on an outside click: a select's "+ New login..." opens
       this from its change event, and the click that picked the option
       lands right after - outside - and closed it at once. Cancel and
       Escape close it. -->
  <div class="modal" role="document" onkeydown={(e) => { e.stopPropagation(); onKey(e); }}>
    <h3>{loginModal.pending.title}</h3>
    {#if loginModal.pending.message}<p class="msg">{loginModal.pending.message}</p>{/if}
    <label>Username
      <input bind:this={userEl} bind:value={user} class="inp" autocomplete="off" spellcheck="false" />
    </label>
    <label>Password
      <input bind:value={password} class="inp" type="password" autocomplete="new-password" />
    </label>
    {#if loginModal.pending.canSkipSave}
      <label class="check"><input type="checkbox" bind:checked={save} /> Save in the vault as a credential</label>
    {/if}
    {#if save || !loginModal.pending.canSkipSave}
      <label>Credential name
        <input bind:value={name} class="inp" spellcheck="false" />
      </label>
    {/if}
    <div class="row">
      <button onclick={cancel}>Cancel</button>
      <button class="primary" onclick={ok}>{loginModal.pending.canSkipSave && !save ? "Use once" : "Save"}</button>
    </div>
  </div>
</div>
{/if}

<style>
  .overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 9500; }
  .modal {
    background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: 6px;
    width: min(400px, 90vw); padding: 1.1rem 1.3rem; box-shadow: 0 10px 40px rgba(0,0,0,0.5);
    display: flex; flex-direction: column; gap: 0.65rem;
  }
  h3 { margin: 0; font-size: 0.95rem; }
  .msg { margin: 0; font-size: 0.85rem; color: var(--subtext0); }
  label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--subtext0); }
  label.check { flex-direction: row; align-items: center; gap: 0.4rem; color: var(--text); }
  .inp {
    width: 100%; box-sizing: border-box; background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px; padding: 0.4rem 0.6rem; font: inherit; font-size: 0.9rem; outline: none;
  }
  .inp:focus { border-color: var(--blue); }
  .row { display: flex; justify-content: flex-end; gap: 0.5rem; }
  button { background: var(--surface0); color: var(--text); border: 0; padding: 0.35rem 0.8rem; border-radius: 3px; cursor: pointer; font: inherit; }
  button:hover { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover { background: var(--sapphire); }
</style>
