<script lang="ts">
  import { untrack } from "svelte";
  // Copy SSH key: append one credential's public key to each host's
  // authorized_keys. Check first, then add; never removes or rewrites a
  // line, and root logins are left alone unless allowed.
  import { api, type CopyKeyResult } from "./api";
  import { credentials } from "./stores.svelte";
  import { fleet, type FleetOpen } from "./fleetStore.svelte";
  import { toast } from "./toast.svelte";
  import FleetShell from "./FleetShell.svelte";
  import { errMsg } from "./connectErrors";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();

  const keyCreds = $derived(credentials.list.filter((c) => c.public_key && c.public_key.trim()));
  let credId = $state("");
  $effect(() => {
    if (!credId && keyCreds.length) credId = keyCreds[0].id;
  });
  const cred = $derived(keyCreds.find((c) => c.id === credId));
  // The label that follows the key in authorized_keys, so whoever reads the
  // file later knows whose key it is. Starts from the key's own comment, or
  // the credential's name when the key has none; editable.
  let comment = $state("");
  const COMMENT_OK = /^[A-Za-z0-9@._+:,= -]{0,80}$/;
  function defaultComment(): string {
    const own = (cred?.public_key ?? "").trim().split(/\s+/).slice(2).join(" ");
    const base = own || cred?.name || "";
    return base.replace(/[^A-Za-z0-9@._+:,= -]+/g, "-").slice(0, 80);
  }
  // Only a new key pick resets it; a credentials reload must not
  // overwrite what the user typed.
  $effect(() => {
    void credId;
    untrack(() => { comment = defaultComment(); });
  });
  let allowRoot = $state(false);
  let busy = $state(false);
  let err = $state("");
  let results = $state<CopyKeyResult[] | null>(null);
  let applied = $state(false);

  async function go(apply: boolean) {
    if (!credId) return;
    busy = true;
    err = "";
    try {
      results = (await api.copySSHKey(scope.ids, credId, comment.trim(), apply, allowRoot)) ?? [];
      applied = apply;
      if (apply) {
        const n = results.filter((r) => r.result === "added").length;
        toast.push("ok", `Key added on ${n} host${n === 1 ? "" : "s"}`);
      }
    } catch (e: any) {
      err = errMsg(e);
    } finally {
      busy = false;
    }
  }
  // Checking again after a scope or option change keeps the button honest.
  function invalidate() {
    results = null;
    applied = false;
  }

  const toAdd = $derived((results ?? []).filter((r) => r.result === "would_add").length);
  const LABEL: Record<CopyKeyResult["result"], string> = {
    present: "already present",
    added: "added",
    would_add: "will add",
    skipped_root: "login user is root - skipped",
    error: "error",
  };
  const CLS: Record<CopyKeyResult["result"], string> = {
    present: "fdim", added: "fok", would_add: "", skipped_root: "fwarn", error: "ferr",
  };
  function keyPreview(k: string): string {
    const [t, b, ...c] = k.trim().split(/\s+/);
    return `${t} ${b ? b.slice(0, 12) + "…" + b.slice(-6) : ""} ${c.join(" ")}`;
  }
</script>

<FleetShell
  title="Copy SSH key"
  sub={`${scope.label} · ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"} · appends to ~/.ssh/authorized_keys of the login user; never removes a line`}
  onClose={() => fleet.close()}
>
  {#if keyCreds.length === 0}
    <p class="fwarn">No credential holds an SSH key with a public key yet. Add or import one under Credentials first.</p>
  {:else}
    <div class="col">
      <label for="ck-cred" class="fdim">Key (from your credentials)</label>
      <select id="ck-cred" class="fselect" bind:value={credId} onchange={invalidate}>
        {#each keyCreds as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
      </select>
      {#if cred?.public_key}<div class="fmono fdim small">{keyPreview(cred.public_key)}</div>{/if}
    </div>
    <div class="col">
      <label for="ck-comment" class="fdim">Comment on the server (after the key in authorized_keys)</label>
      <input id="ck-comment" class="finput fmono" bind:value={comment} spellcheck="false" placeholder="jane@laptop" />
      {#if !COMMENT_OK.test(comment)}<span class="ferr small">Letters, digits, spaces and @ . _ + - : , = only.</span>{/if}
      <span class="fdim small">A host that already has this key under another comment counts as "already present"; nothing is rewritten.</span>
    </div>
    <label class="check"><input type="checkbox" bind:checked={allowRoot} onchange={invalidate} />Also add for connections that log in as root</label>
    {#if err}<p class="ferr">{err}</p>{/if}
    {#if results}
      <table class="ftable">
        <thead><tr><th>Host</th><th>User</th><th>{applied ? "Result" : "Check"}</th></tr></thead>
        <tbody>
          {#each results as r (r.connection_id)}
            <tr>
              <td class="fmono" title={r.hostname}>{r.name}</td>
              <td>{r.user || "-"}</td>
              <td class={CLS[r.result]}>{LABEL[r.result]}{#if r.error}: {r.error}{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}

  {#snippet footer()}
    <button class="fbtn" onclick={() => fleet.close()}>{applied ? "Close" : "Cancel"}</button>
    {#if !results || applied}
      <button class="fbtn" disabled={busy || !credId} onclick={() => go(false)}>{busy ? "Checking…" : applied ? "Check again" : "Check hosts"}</button>
    {:else}
      <button class="fbtn" disabled={busy} onclick={() => go(false)}>Check again</button>
      <button class="fbtn primary" disabled={busy || toAdd === 0 || !COMMENT_OK.test(comment)} onclick={() => go(true)}>
        {busy ? "Adding…" : `Add to ${toAdd} host${toAdd === 1 ? "" : "s"}`}
      </button>
    {/if}
  {/snippet}
</FleetShell>

<style>
  .col { display: flex; flex-direction: column; gap: 0.3rem; margin-bottom: 0.7rem; }
  .small { font-size: 0.72rem; }
  .check { display: flex; gap: 0.45rem; align-items: center; margin-bottom: 0.7rem; }
</style>
