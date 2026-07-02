<script lang="ts">
  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api } from "./api";
  import { IconLock } from "./iconMap";
  import { isMobile } from "./platform";
  import { EventsOn } from "./wailsRuntime";
  import PasswordInput from "./PasswordInput.svelte";
  import {
    mobileSecureHasVaultPass,
    mobileSecureSetVaultPass,
    mobileSecureClearVaultPass,
    mobileBiometricUnlock,
    mobileUnlockWithStoredPass,
  } from "./mobileSecure";

  interface Props {
    onUnlocked: () => void;
    onSkip: () => void;
    allowAutoUnlock?: boolean;
  }
  let { onUnlocked, onSkip, allowAutoUnlock = true }: Props = $props();

  type Status = "not_initialized" | "locked" | "unlocked" | "loading";

  let status = $state<Status>("loading");
  let autoUnlockAvailable = $state(false);
  let passphrase = $state("");
  let confirm = $state("");
  let remember = $state(true);
  let busy = $state(false);
  let err = $state<string | null>(null);
  // Mobile-only: a stored passphrase exists, so offer biometric unlock.
  let biometricAvailable = $state(false);
  let biometricBusy = $state(false);

  onMount(async () => {
    // Desktop machine-bound sidecar auto-unlock (no-op / false on mobile).
    if (allowAutoUnlock && !isMobile) {
      try {
        const ok = await api.vaultAutoUnlock();
        if (ok) {
          status = "unlocked";
          onUnlocked();
          return;
        }
      } catch (e) {
        console.warn("auto-unlock failed", e);
      }
    }
    const s = await api.vaultStatus();
    status = s.state as Status;
    if (s.state === "locked") autoUnlockAvailable = !!s.auto_unlock_available;
    if (status === "unlocked") { onUnlocked(); return; }

    // Mobile biometric auto-unlock: if a passphrase is stored, prompt
    // immediately so a returning user just taps the fingerprint sensor.
    if (isMobile && status === "locked" && allowAutoUnlock) {
      const has = await mobileSecureHasVaultPass();
      if (has) {
        biometricAvailable = true;
        triggerBiometric();
      }
    }
  });

  // The native biometric result arrives as this event (forwarded into the
  // mobile poll queue by the Go bridge). On success, unlock with the stored
  // passphrase server-side - the secret never enters JS.
  EventsOn<{ ok: boolean; error?: string }>("common:biometric", async (res) => {
    if (!biometricBusy) return;
    biometricBusy = false;
    if (biometricTimer) { clearTimeout(biometricTimer); biometricTimer = null; }
    if (!res?.ok) {
      if (res?.error) err = res.error;
      return;
    }
    try {
      const ok = await mobileUnlockWithStoredPass();
      if (ok) {
        status = "unlocked";
        onUnlocked();
      } else {
        err = "Stored passphrase no longer valid - enter it manually.";
        biometricAvailable = false;
        await mobileSecureClearVaultPass();
      }
    } catch (e: any) {
      err = errMsg(e);
    }
  });

  let biometricTimer: ReturnType<typeof setTimeout> | null = null;

  function triggerBiometric() {
    err = null;
    biometricBusy = true;
    if (biometricTimer) clearTimeout(biometricTimer);
    // Safety net: if the prompt can't show (e.g. launched while the screen
    // is locked, so the system cancels it silently) the common:biometric
    // event may never arrive. Don't leave the UI stuck on "Waiting…" -
    // fall back to the passphrase field after a few seconds.
    biometricTimer = setTimeout(() => {
      if (biometricBusy) {
        biometricBusy = false;
        err = "Biometric unavailable - enter your passphrase.";
      }
    }, 8000);
    mobileBiometricUnlock().catch((e) => {
      biometricBusy = false;
      if (biometricTimer) clearTimeout(biometricTimer);
      err = errMsg(e);
    });
  }

  async function submit() {
    err = null;
    if (!passphrase) {
      err = "Passphrase required";
      return;
    }
    if (status === "not_initialized" && passphrase !== confirm) {
      err = "Passphrases don't match";
      return;
    }
    if (status === "not_initialized" && passphrase.length < 8) {
      err = "Use at least 8 characters";
      return;
    }
    busy = true;
    try {
      // On desktop, `remember` drives the machine-bound sidecar. On mobile
      // there is no sidecar, so pass false to the backend and instead store
      // the passphrase in the Keystore-backed secure store ourselves (gated
      // by biometrics on next launch).
      const rememberBackend = isMobile ? false : remember;
      if (status === "not_initialized") {
        await api.vaultInit(passphrase, rememberBackend);
      } else {
        await api.vaultUnlock(passphrase, rememberBackend);
      }
      if (isMobile) {
        if (remember) await mobileSecureSetVaultPass(passphrase);
        else await mobileSecureClearVaultPass();
      }
      passphrase = "";
      confirm = "";
      status = "unlocked";
      onUnlocked();
    } catch (e: any) {
      err = errMsg(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if status === "loading"}
  <div class="overlay"><div class="modal small"><p>Checking vault…</p></div></div>
{:else if status === "unlocked"}
  <!-- transparent -->
{:else}
  <div class="overlay" role="dialog" aria-modal="true">
    <div class="modal">
      <header>
        <h1>
          <IconLock size={18} />
          {#if status === "not_initialized"}
            Set master passphrase
          {:else}
            Unlock vault
          {/if}
        </h1>
      </header>

      {#if status === "not_initialized"}
        <p>
          The encrypted vault stores your credentials on disk. Choose a
          passphrase to protect it. <strong>Forgetting it means all stored
          credentials become unrecoverable.</strong>
        </p>
      {:else}
        <p>Enter your master passphrase to unlock stored credentials.</p>
        {#if autoUnlockAvailable}
          <p class="info">
            Auto-unlock sidecar exists but didn't succeed - likely the
            machine identity changed.
          </p>
        {/if}
      {/if}

      <label>
        Passphrase
        <PasswordInput
          bind:value={passphrase}
          autocomplete="current-password"
          onkeydown={(e) => { if (e.key === "Enter") submit(); }}
        />
      </label>

      {#if status === "not_initialized"}
        <label>
          Confirm
          <PasswordInput
            bind:value={confirm}
            onkeydown={(e) => { if (e.key === "Enter") submit(); }}
          />
        </label>
      {/if}

      <label class="checkbox">
        <input type="checkbox" bind:checked={remember} />
        <span>
          {#if isMobile}
            Unlock with fingerprint / face next time
          {:else}
            Remember on this machine (auto-unlock next time)
          {/if}
        </span>
      </label>

      {#if err}
        <div class="err">{err}</div>
      {/if}

      {#if isMobile && biometricAvailable}
        <div class="row">
          <button class="primary" disabled={biometricBusy} onclick={triggerBiometric}>
            {#if biometricBusy}Waiting for biometric…{:else}Unlock with biometrics{/if}
          </button>
        </div>
        <p class="hint">Or enter your passphrase below.</p>
      {/if}

      <div class="row">
        <button onclick={onSkip} disabled={busy}>Skip (memory only)</button>
        <button class="primary" disabled={busy} onclick={submit}>
          {#if busy}…{:else if status === "not_initialized"}Create vault{:else}Unlock{/if}
        </button>
      </div>

      <p class="hint">
        {#if isMobile}
          {#if remember}
            ⚠ Your passphrase is stored in the device's encrypted keystore and
            unlocked by biometrics. Anyone who can pass your device biometrics
            can open the vault.
          {:else}
            You'll be prompted for the passphrase every launch.
          {/if}
        {:else if remember}
          ⚠ Auto-unlock makes credentials accessible to anyone with access to
          your user account on this machine. Resists disk theft and transfer.
        {:else}
          You'll be prompted for the passphrase every launch.
        {/if}
      </p>
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.65);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 6px;
    width: min(460px, 92vw); padding: 1.2rem 1.4rem;
  }
  .modal.small { width: 280px; text-align: center; }
  header { margin-bottom: 0.5rem; }
  h1 { margin: 0; font-size: 1.05rem; }
  p { font-size: 0.85rem; color: var(--subtext0); }
  p.info {
    background: var(--crust); padding: 0.4rem 0.6rem;
    border-left: 3px solid var(--yellow);
    border-radius: 3px; font-size: 0.78rem;
  }
  label {
    display: flex; flex-direction: column; gap: 0.25rem;
    font-size: 0.8rem; color: var(--subtext0); margin: 0.6rem 0;
  }
  label.checkbox {
    flex-direction: row; align-items: center; gap: 0.45rem;
    cursor: pointer; margin-top: 0.4rem;
  }
  label.checkbox input { width: auto; margin: 0; }
  input:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .row {
    display: flex; justify-content: flex-end;
    gap: 0.5rem; margin-top: 1rem;
  }
  button {
    background: var(--surface0); color: var(--text);
    border: 0; padding: 0.4rem 0.85rem;
    border-radius: 3px; cursor: pointer; font: inherit;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  button.primary:hover:not(:disabled) { background: var(--lavender); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .err {
    color: var(--red); background: var(--crust);
    padding: 0.5rem 0.7rem; border-radius: 4px;
    border-left: 3px solid var(--red); font-size: 0.82rem; margin: 0.5rem 0;
  }
  .hint {
    font-size: 0.72rem; color: var(--overlay0);
    margin-top: 0.8rem; margin-bottom: 0;
  }
</style>
