<script lang="ts">
  import { credentials, selection, view } from "./stores.svelte";
  import { toast } from "./toast.svelte";
  import { expiryInfo } from "./credExpiry";
  import { expandedCredentials } from "./treeState.svelte";
  import { api, type UsageRef, type CredentialHistoryEntry, type OpksshCertStatus } from "./api";
  import { connectionActions } from "./connectionActions.svelte";
  import { IconFolder, IconHost } from "./iconMap";
  import Icon from "./Icon.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { writeClipboard } from "./clipboard";
  import IconPicker from "./IconPicker.svelte";
  import PasswordStrengthMeter from "./PasswordStrengthMeter.svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import { errMsg } from "./connectErrors";

  type Props = {
    onCreateCredential?: (folderId: string | null) => void;
  };
  let { onCreateCredential }: Props = $props();

  const cred = $derived(selection.selectedCredential());
  const credMultiCount = $derived(selection.credentialMultiCount());

  // Deletes stage through connectionActions (rendered by App.svelte's
  // shared DeleteConfirm) - one mechanism for connections AND creds.
  function openCredMultiDelete() {
    connectionActions.openDeleteCredentials(selection.selectedCredentialIds());
  }

  const credFolder = $derived(selection.selectedCredentialFolder());

  // ----- credential folder actions -----
  let folderRenaming = $state(false);
  let folderRenameVal = $state("");
  let folderRenameErr = $state<string | null>(null);

  function startFolderRename() {
    if (!credFolder) return;
    folderRenameVal = credFolder.name;
    folderRenameErr = null;
    folderRenaming = true;
  }

  async function saveFolderRename() {
    if (!credFolder) return;
    try {
      await api.credentialFoldersUpdate(credFolder.id, folderRenameVal);
      await credentials.load();
      folderRenaming = false;
      toast.ok("Folder renamed");
    } catch (e: any) {
      folderRenameErr = errMsg(e);
      toast.err(`Rename failed: ${errMsg(e)}`);
    }
  }

  async function deleteCredFolder() {
    if (!credFolder) return;
    // Same staged DeleteConfirm as the connections tree - lists the
    // folder plus everything that cascades with it. A multi-selection
    // including this folder deletes the whole selection.
    const selected = selection.selectedCredentialFolderIds();
    const ids = selected.includes(credFolder.id) && selected.length > 1 ? selected : [credFolder.id];
    connectionActions.openDeleteCredFolders(ids);
  }

  async function addSubfolderHere() {
    if (!credFolder) return;
    const name = await showPrompt("Folder name?");
    if (!name?.trim()) return;
    try {
      await api.credentialFoldersCreate(name.trim(), credFolder.id);
      expandedCredentials.set(credFolder.id, true);
      await credentials.load();
    } catch (e: any) { toast.err(errMsg(e)); }
  }

  // ----- opkssh config -----
  let opksshYaml = $state("");
  let opksshBasename = $state("id_ecdsa");
  let opksshProviderHint = $state("");
  // Duration fields are free text ("7d", "6d23h", "90m", bare number
  // = hours/minutes respectively); parsed back to the numeric config
  // keys on save.
  let opksshMaxAgeText = $state("7d");
  let opksshMinRemainingText = $state("1h");
  const opksshMaxAgeSecs = $derived(parseDur(opksshMaxAgeText, "h"));
  const opksshMinRemainingSecs = $derived(parseDur(opksshMinRemainingText, "m"));

  // Provider aliases parsed from the YAML so the hint can be a dropdown
  // instead of a free-text field the user has to spell exactly. Matches
  // "  - alias: google azure" lines; an entry can list several space-
  // separated aliases (the opkssh format), any of which selects it - we
  // surface the first as the option value. The backend's selectProvider
  // resolves the chosen alias; blank means use default_provider / first.
  const opksshAliases = $derived.by(() => {
    const out: string[] = [];
    for (const line of opksshYaml.split("\n")) {
      const m = line.match(/^\s*-?\s*alias:\s*(.+?)\s*$/);
      if (m) {
        const first = m[1].split(/\s+/)[0];
        if (first && !out.includes(first)) out.push(first);
      }
    }
    return out;
  });
  // The default_provider value (if any) so the "use default" option can
  // name it. "webchooser" is an opkssh-CLI sentinel this native client
  // does not implement, so we flag it as unsupported in the UI.
  const opksshDefaultProvider = $derived.by(() => {
    const m = opksshYaml.match(/^\s*default_provider:\s*(.+?)\s*$/m);
    return m ? m[1].trim() : "";
  });
  let opksshSaving = $state(false);
  let opksshSaveErr = $state<string | null>(null);
  let opksshSaveOk = $state(false);
  let opksshCertStatus = $state<OpksshCertStatus | null>(null);

  $effect(() => {
    if (cred?.kind === "opkssh") {
      opksshYaml = (cred.config?.opkssh_config_yaml as string) ?? "";
      opksshBasename = (cred.config?.key_basename as string) ?? "id_ecdsa";
      opksshProviderHint = (cred.config?.provider_hint as string) ?? "";
      const ma = cred.config?.max_cert_age_hours;
      opksshMaxAgeText = fmtDur((typeof ma === "number" && ma > 0 ? ma : 168) * 3600);
      const mr = cred.config?.min_remaining_before_refresh_minutes;
      opksshMinRemainingText = fmtDur((typeof mr === "number" && mr > 0 ? mr : 60) * 60);
      opksshCertStatus = null;
      api.opksshCertStatus(cred.id)
        .then((st) => (opksshCertStatus = st))
        .catch(() => (opksshCertStatus = null));
    }
  });

  // Parses "7d", "6d23h", "1h30m", "90m" or a bare number (taken in
  // defaultUnit) into seconds. Null = unparseable. Inverse of fmtDur
  // for every value fmtDur can produce, so load -> display -> save
  // round-trips losslessly.
  function parseDur(input: string, defaultUnit: "h" | "m"): number | null {
    const s = input.trim().toLowerCase();
    if (!s) return null;
    if (/^\d+(\.\d+)?$/.test(s)) {
      return parseFloat(s) * (defaultUnit === "h" ? 3600 : 60);
    }
    const re = /(\d+(?:\.\d+)?)\s*([dhm])/g;
    let total = 0;
    let consumed = "";
    let m: RegExpExecArray | null;
    while ((m = re.exec(s))) {
      total += parseFloat(m[1]) * (m[2] === "d" ? 86400 : m[2] === "h" ? 3600 : 60);
      consumed += m[0];
    }
    // Reject inputs with garbage between/around the matched parts.
    if (consumed.replace(/\s+/g, "") !== s.replace(/\s+/g, "")) return null;
    return total > 0 ? total : null;
  }

  // Compact duration: "6d23h59m", "23h59m", "45m", "<1m". Every
  // non-zero unit is shown so the hint never displays less than what
  // gets saved.
  function fmtDur(seconds: number): string {
    const s = Math.max(0, Math.floor(seconds));
    if (s < 60) return "<1m";
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    let out = "";
    if (d > 0) out += `${d}d`;
    if (h > 0) out += `${h}h`;
    if (m > 0) out += `${m}m`;
    return out;
  }

  const opksshCertLine = $derived.by(() => {
    const st = opksshCertStatus;
    if (!st) return null;
    if (st.vault_locked) return "Vault locked - cert status unavailable.";
    if (!st.has_cert) return "No certificate in the vault yet - the first connect opens the browser login.";
    const now = Date.now() / 1000;
    const parts: string[] = [];
    if (st.issued_at > 0) parts.push(`issued ${fmtDur(now - st.issued_at)} ago`);
    if (st.renew_at > 0) {
      parts.push(st.renew_at <= now
        ? "re-login on next connect"
        : `re-login in ~${fmtDur(st.renew_at - now)}`);
    }
    return "Certificate in vault" + (parts.length ? ": " + parts.join(" - ") : "") + ".";
  });

  // Ctrl/Cmd+S handler. Dispatches to whichever editor is currently
  // active so power users don't have to chase the right Save button:
  //   - metadata edit form open → saveEdit
  //   - folder rename in flight → saveFolderRename
  //   - api-token form has unsaved changes → saveAPITokenChanges
  //   - opkssh credential viewed → saveOpksshConfig (YAML / hint /
  //     basename fields are always editable inline; "active editor"
  //     here means just "the panel is showing an opkssh cred")
  // First match wins; ignored when none apply (e.g. cred is a plain
  // password with no edit form open).
  function onPaneKey(e: KeyboardEvent) {
    if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "s") return;
    e.preventDefault();
    if (editing && cred) { saveEdit(); return; }
    if (folderRenaming) { saveFolderRename(); return; }
    if (cred?.kind === "api_token" && (apiTokenIDDirty || apiTokenSecretNew)) {
      saveAPITokenChanges();
      return;
    }
    if (cred?.kind === "opkssh" && !opksshSaving) {
      saveOpksshConfig();
      return;
    }
  }

  async function saveOpksshConfig() {
    if (!cred) return;
    if (opksshMaxAgeSecs == null) {
      opksshSaveErr = `Max cert age: can't parse "${opksshMaxAgeText}" - use e.g. 7d, 6d23h, 48h or a number of hours.`;
      return;
    }
    if (opksshMinRemainingSecs == null) {
      opksshSaveErr = `Refresh threshold: can't parse "${opksshMinRemainingText}" - use e.g. 1h, 90m or a number of minutes.`;
      return;
    }
    opksshSaving = true; opksshSaveErr = null; opksshSaveOk = false;
    try {
      const cfg = {
        ...(cred.config ?? {}),
        key_basename: opksshBasename,
        opkssh_config_yaml: opksshYaml,
        provider_hint: opksshProviderHint,
        max_cert_age_hours: Math.max(1, Math.round(opksshMaxAgeSecs / 3600)),
        min_remaining_before_refresh_minutes: Math.max(
          1,
          Math.round(opksshMinRemainingSecs / 60),
        ),
      };
      await api.credentialsUpdate({ id: cred.id, config: cfg });
      await credentials.load();
      opksshSaveOk = true;
      setTimeout(() => (opksshSaveOk = false), 2000);
      toast.ok("opkssh config saved");
    } catch (e: any) {
      opksshSaveErr = errMsg(e);
      toast.err(`Save failed: ${errMsg(e)}`);
    }
    finally { opksshSaving = false; }
  }

  // ----- edit metadata -----
  let editing = $state(false);
  let editName = $state("");
  let editHint = $state("");
  let editUsername = $state("");
  let editExpires = $state(""); // YYYY-MM-DD, "" = no expiry
  let editSaving = $state(false);
  let editErr = $state<string | null>(null);

  // Exit edit mode when the selected credential changes. Without this,
  // clicking another entry while editing left the old edit form open;
  // hitting Save then wrote the *previous* credential's name onto the
  // newly-selected one, tripping "credential name already exists".
  let lastEditedCredId: string | null = null;
  $effect(() => {
    const id = cred?.id ?? null;
    if (id !== lastEditedCredId) {
      lastEditedCredId = id;
      editing = false;
      editErr = null;
    }
  });

  function startEdit() {
    if (!cred) return;
    editName = cred.name;
    editHint = cred.hint ?? "";
    editUsername = cred.default_username ?? "";
    // unix -> YYYY-MM-DD (local) for the date input; "" when no expiry.
    editExpires = cred.expires_at
      ? new Date(cred.expires_at * 1000).toLocaleDateString("en-CA")
      : "";
    editErr = null;
    editing = true;
  }

  async function saveEdit() {
    if (!cred) return;
    editSaving = true; editErr = null;
    try {
      await api.credentialsUpdate({
        id: cred.id,
        name: editName || undefined,
        // Send the hint verbatim - including "" - so clearing it
        // actually persists. `editHint || undefined` collapsed an
        // emptied field to undefined, which the backend reads as
        // "no change" and the old hint came back on save.
        hint: editHint,
        default_username: editUsername || undefined,
        set_default_username_to_null: editUsername === "" && cred.default_username !== null,
        expires_at: editExpires ? Math.floor(new Date(editExpires + "T00:00:00").getTime() / 1000) : undefined,
        set_expires_at_to_null: editExpires === "" && cred.expires_at !== null,
      });
      await credentials.load();
      editing = false;
      toast.ok("Credential saved");
    } catch (e: any) {
      editErr = errMsg(e);
      toast.err(`Save failed: ${errMsg(e)}`);
    }
    finally { editSaving = false; }
  }

  // ----- reveal secret -----
  let revealedSecret = $state<string | null>(null);
  let revealErr = $state<string | null>(null);
  let revealTimer: ReturnType<typeof setTimeout> | null = null;

  async function revealSecret() {
    if (!cred) return;
    revealErr = null;
    try {
      revealedSecret = await api.credentialsRevealSecret(cred.id);
      if (revealTimer) clearTimeout(revealTimer);
      revealTimer = setTimeout(() => { revealedSecret = null; }, 30000);
    } catch (e: any) { revealErr = errMsg(e); }
  }

  function hideSecret() {
    revealedSecret = null;
    if (revealTimer) { clearTimeout(revealTimer); revealTimer = null; }
  }

  $effect(() => {
    // clear reveal when switching credential
    if (cred) { revealedSecret = null; revealErr = null; }
  });

  // ----- secret history (sealed previous values) -----
  // Lazily-loaded list of past rotations for this credential. Plaintexts
  // never live in the entries array - UI calls revealHistorySecret(id)
  // and treats the result with the same 30s auto-clear discipline as
  // the live reveal above.
  let secretHistory = $state<import("./api").CredentialSecretHistoryEntry[]>([]);
  let secretHistoryOpen = $state(false);
  let secretHistoryErr = $state<string | null>(null);
  let revealedHistory = $state<Record<string, string>>({});
  // Per-row clear timers so each reveal has its own 30s window.
  let historyClearTimers: Record<string, ReturnType<typeof setTimeout>> = {};

  async function loadSecretHistory() {
    if (!cred) return;
    secretHistoryErr = null;
    try {
      secretHistory = (await api.credentialsSecretHistory(cred.id)) ?? [];
    } catch (e: any) {
      secretHistoryErr = errMsg(e);
    }
  }

  $effect(() => {
    // Drop revealed history + reload metadata when credential changes.
    if (!cred) return;
    revealedHistory = {};
    for (const t of Object.values(historyClearTimers)) clearTimeout(t);
    historyClearTimers = {};
    if (secretHistoryOpen) loadSecretHistory();
  });

  async function toggleSecretHistory() {
    secretHistoryOpen = !secretHistoryOpen;
    if (secretHistoryOpen) await loadSecretHistory();
  }

  async function revealHistorySecret(historyId: string) {
    secretHistoryErr = null;
    try {
      const v = await api.credentialsRevealSecretHistory(historyId);
      revealedHistory = { ...revealedHistory, [historyId]: v };
      if (historyClearTimers[historyId]) clearTimeout(historyClearTimers[historyId]);
      historyClearTimers[historyId] = setTimeout(() => {
        const next = { ...revealedHistory };
        delete next[historyId];
        revealedHistory = next;
        delete historyClearTimers[historyId];
      }, 30_000);
    } catch (e: any) {
      secretHistoryErr = errMsg(e);
    }
  }

  function hideHistorySecret(historyId: string) {
    if (historyClearTimers[historyId]) {
      clearTimeout(historyClearTimers[historyId]);
      delete historyClearTimers[historyId];
    }
    const next = { ...revealedHistory };
    delete next[historyId];
    revealedHistory = next;
  }

  async function copyHistorySecret(historyId: string) {
    secretHistoryErr = null;
    try {
      const v = revealedHistory[historyId] ?? (await api.credentialsRevealSecretHistory(historyId));
      await writeClipboard(v);
      toast.ok("Copied - clipboard clears in 30s");
      setTimeout(() => {
        writeClipboard("").catch(() => {});
      }, 30_000);
    } catch (e: any) {
      secretHistoryErr = errMsg(e);
      toast.err(`Copy failed: ${errMsg(e)}`);
    }
  }

  async function deleteHistorySecret(historyId: string) {
    const ok = await showConfirm({
      title: "Forget rotation",
      message: "Forget this rotation? The sealed previous value will be deleted.",
      okLabel: "Forget",
      danger: true,
    });
    if (!ok) return;
    secretHistoryErr = null;
    try {
      await api.credentialsDeleteSecretHistory(historyId);
      hideHistorySecret(historyId);
      await loadSecretHistory();
      toast.ok("Snapshot deleted");
    } catch (e: any) {
      secretHistoryErr = errMsg(e);
      toast.err(`Delete failed: ${errMsg(e)}`);
    }
  }

  function fmtTS(ts: number): string {
    return new Date(ts * 1000).toLocaleString();
  }

  // ----- rotate key -----
  let rotateOpen = $state(false);
  let rotateMode = $state<"generate" | "import">("generate");
  let rotatePem = $state("");
  let rotatePassphrase = $state("");
  let rotating = $state(false);
  let rotateErr = $state<string | null>(null);

  async function doRotateKey() {
    if (!cred) return;
    rotating = true; rotateErr = null;
    try {
      await api.credentialsRotateKey({
        id: cred.id,
        generate_new: rotateMode === "generate",
        private_openssh: rotateMode === "import" ? rotatePem : undefined,
        passphrase: rotatePassphrase || undefined,
      });
      await credentials.load();
      rotateOpen = false;
      rotatePem = "";
      rotatePassphrase = "";
      toast.ok("Key rotated");
    } catch (e: any) { rotateErr = errMsg(e); }
    finally { rotating = false; }
  }

  // ----- rotate password -----
  let pwRotateOpen = $state(false);
  let newPassword = $state("");
  let pwRotating = $state(false);
  let pwRotateErr = $state<string | null>(null);

  async function doRotatePassword() {
    if (!cred) return;
    pwRotating = true; pwRotateErr = null;
    try {
      await api.credentialsRotatePassword(cred.id, newPassword);
      await credentials.load();
      pwRotateOpen = false;
      newPassword = "";
      toast.ok("Password saved");
    } catch (e: any) { pwRotateErr = errMsg(e); }
    finally { pwRotating = false; }
  }

  // ----- usage / history -----
  let usage = $state<UsageRef[]>([]);
  let history = $state<CredentialHistoryEntry[]>([]);
  let usageErr = $state<string | null>(null);

  $effect(() => {
    if (!cred) { usage = []; history = []; return; }
    usageErr = null;
    api.credentialsUsage(cred.id).then((u) => (usage = u ?? [])).catch((e) => (usageErr = errMsg(e)));
    api.credentialsHistory(cred.id).then((h) => (history = h ?? [])).catch(() => {});
  });

  // ----- delete -----
  let deleteErr = $state<string | null>(null);

  function deleteCred() {
    if (!cred) return;
    deleteErr = null;
    // Staged DeleteConfirm, multi-selection aware - same as the
    // connections DetailPane delete.
    const selected = selection.selectedCredentialIds();
    const ids = selected.includes(cred.id) && selected.length > 1 ? selected : [cred.id];
    connectionActions.openDeleteCredentials(ids);
  }

  function copyText(s: string) { writeClipboard(s).catch(() => {}); }

  function fmtTs(t: number | null | undefined): string {
    if (!t) return "-";
    return new Date(t * 1000).toLocaleString();
  }

  // label for the reveal button depending on kind
  const secretLabel = $derived(
    cred?.kind === "password" ? "Show password"
    : cred?.kind === "api_token" ? "Show token secret"
    : "Show private key"
  );

  // ----- api_token edit/rotate -----
  // token_id is a config field (may be empty for providers like Hetzner).
  // Secret rotation goes through CredentialsRotateAPIToken.
  let apiTokenIDEdit = $state("");
  let apiTokenIDOriginal = $state("");
  let apiTokenSecretNew = $state("");
  let apiTokenIDDirty = $derived(apiTokenIDEdit !== apiTokenIDOriginal);
  let apiTokenSaving = $state(false);
  let apiTokenErr = $state<string | null>(null);
  let apiTokenOk = $state<string | null>(null);

  $effect(() => {
    if (cred?.kind === "api_token") {
      const tid = (cred.config?.token_id as string) ?? "";
      apiTokenIDEdit = tid;
      apiTokenIDOriginal = tid;
      apiTokenSecretNew = "";
      apiTokenErr = null;
      apiTokenOk = null;
    }
  });

  async function saveAPITokenChanges() {
    if (!cred || cred.kind !== "api_token") return;
    if (!apiTokenIDDirty && !apiTokenSecretNew) return;
    apiTokenSaving = true; apiTokenErr = null; apiTokenOk = null;
    try {
      await api.credentialsRotateAPIToken({
        id: cred.id,
        token_id: apiTokenIDDirty ? apiTokenIDEdit : null,
        new_secret: apiTokenSecretNew,
      });
      await credentials.load();
      apiTokenIDOriginal = apiTokenIDEdit;
      const parts: string[] = [];
      if (apiTokenIDDirty) parts.push("token id");
      if (apiTokenSecretNew) parts.push("secret");
      apiTokenOk = `Saved: ${parts.join(" + ")}`;
      apiTokenSecretNew = "";
      setTimeout(() => (apiTokenOk = null), 3000);
      toast.ok(`API token saved (${parts.join(" + ")})`);
    } catch (e: any) {
      apiTokenErr = errMsg(e);
      toast.err(`Save failed: ${errMsg(e)}`);
    } finally {
      apiTokenSaving = false;
    }
  }

  function clearAPITokenSecret() {
    apiTokenSecretNew = "";
  }

  // ----- change type -----
  const ALL_KINDS = ["password", "key", "agent", "opkssh", "vault"] as const;
  let changeTypeOpen = $state(false);
  let changeTypeTarget = $state<string>("");
  let changeTypeBusy = $state(false);
  let changeTypeErr = $state<string | null>(null);

  function openChangeType() {
    if (!cred) return;
    changeTypeTarget = cred.kind;
    changeTypeErr = null;
    changeTypeOpen = true;
  }

  async function doChangeType() {
    if (!cred || changeTypeTarget === cred.kind) return;
    changeTypeBusy = true; changeTypeErr = null;
    try {
      await api.credentialsUpdate({
        id: cred.id,
        kind: changeTypeTarget,
        config: {},
        set_public_key_to_null: true,
      });
      await credentials.load();
      changeTypeOpen = false;
      toast.ok(`Credential type changed to ${changeTypeTarget}`);
    } catch (e: any) {
      changeTypeErr = errMsg(e);
      toast.err(`Type change failed: ${errMsg(e)}`);
    }
    finally { changeTypeBusy = false; }
  }
</script>

<svelte:window onkeydown={onPaneKey} />

<section class="detail">
  {#if credMultiCount > 1}
    <header>
      <h1>{credMultiCount} credentials selected</h1>
      <div class="head-actions">
        <button class="danger" onclick={openCredMultiDelete}>Delete {credMultiCount}</button>
        <button onclick={() => selection.select({ kind: "none" })}>Clear</button>
      </div>
    </header>
    <p class="muted">Bulk credential edit isn't wired yet - only delete.</p>
  {:else if credFolder}
    <header>
      {#if folderRenaming}
        <input class="edit-name" bind:value={folderRenameVal}
          onkeydown={(e) => { if (e.key === "Enter") saveFolderRename(); if (e.key === "Escape") folderRenaming = false; }} />
      {:else}
        <h1>
          <Icon imageId={null} iconName={credFolder.icon_name} iconColor={credFolder.icon_color} size={18}>
            <IconFolder size={18} />
          </Icon>
          {credFolder.name}
        </h1>
      {/if}
      <div class="head-actions">
        {#if folderRenaming}
          <button class="primary" onclick={saveFolderRename}>Save</button>
          <button onclick={() => folderRenaming = false}>Cancel</button>
        {:else}
          <button onclick={startFolderRename}>Rename</button>
          <button onclick={addSubfolderHere} title="New subfolder">+ Folder</button>
          {#if onCreateCredential}
            <button onclick={() => onCreateCredential!(credFolder.id)} title="New credential in this folder">+ Credential</button>
          {/if}
          <button class="danger" onclick={deleteCredFolder}>Delete</button>
        {/if}
      </div>
    </header>
    {#if folderRenameErr}<div class="err">{folderRenameErr}</div>{/if}
    <div class="folder-icon-row">
      <IconPicker
        kind="credentialFolder"
        targetId={credFolder.id}
        currentIconName={credFolder.icon_name}
        currentIconColor={credFolder.icon_color}
        fallbackEmoji=""
        onNamedChange={() => credentials.load()}
      />
    </div>
  {:else if !cred}
    <div class="empty">
      <p>Select a credential on the left.</p>
      <p class="muted">Or click the new-credential button to create one.</p>
    </div>
  {:else}
    <header>
      {#if editing}
        <input class="edit-name" bind:value={editName} />
      {:else}
        <h1>{cred.name}</h1>
      {/if}
      <div class="head-actions">
        {#if editing}
          <button class="primary" disabled={editSaving} onclick={saveEdit}>
            {editSaving ? "Saving…" : "Save"}
          </button>
          <button onclick={() => (editing = false)}>Cancel</button>
        {:else}
          <button onclick={startEdit}>Edit</button>
          <button class="danger" onclick={deleteCred}>Delete</button>
        {/if}
      </div>
    </header>

    {#if deleteErr}<div class="err inline-err">{deleteErr}</div>{/if}

    {#if editing}
      <div class="form edit-form">
        <label>Hint
          <input bind:value={editHint} placeholder="Short reminder (not the secret)" />
        </label>
        <label>Default username
          <input bind:value={editUsername} placeholder="e.g. ubuntu, admin" />
        </label>
        {#if cred.kind === "api_token" || cred.kind === "password" || cred.kind === "key"}
          <label>Expires <span class="hint inline">(optional - clear to remove)</span>
            <input type="date" bind:value={editExpires} />
          </label>
        {/if}
        <IconPicker
          kind="credential"
          targetId={cred.id}
          currentIconId={cred.icon_image_id ?? null}
          currentIconName={cred.icon_name}
          currentIconColor={cred.icon_color}
          fallbackEmoji="🔑"
          onChange={() => credentials.load()}
          onNamedChange={() => credentials.load()}
        />
        {#if editErr}<div class="err">{editErr}</div>{/if}
      </div>
    {:else}
      <dl>
        <dt>Kind</dt><dd><code>{cred.kind}</code></dd>
        <dt>Storage</dt><dd><code>{cred.storage_mode}</code></dd>
        {#if cred.hint}<dt>Hint</dt><dd>{cred.hint}</dd>{/if}
        {#if cred.default_username}<dt>Default user</dt><dd><code>{cred.default_username}</code></dd>{/if}
        <dt>Last rotated</dt><dd>{fmtTs(cred.last_rotated_at)}</dd>
        {#if cred.expires_at}
          {@const ex = expiryInfo(cred.expires_at)}
          <dt>Expires</dt>
          <dd>
            {fmtTs(cred.expires_at)}
            {#if ex.level !== "none"}
              <span class="expiry-badge {ex.level}">{ex.label}</span>
            {/if}
          </dd>
        {/if}
        {#if cred.tags?.length}
          <dt>Tags</dt>
          <dd>{#each cred.tags as t}<span class="tag">{t}</span>{/each}</dd>
        {/if}
      </dl>
    {/if}

    <!-- Reveal secret (password / managed key / api_token) -->
    {#if cred.storage_mode === "managed" && (cred.kind === "password" || cred.kind === "key" || cred.kind === "api_token")}
      <h2>Secret</h2>
      {#if revealedSecret !== null}
        <div class="secret-box">
          <textarea readonly rows={cred.kind === "key" ? 10 : 2} class="mono secret-ta">{revealedSecret}</textarea>
          <div class="secret-actions">
            <button onclick={() => copyText(revealedSecret!)}>Copy</button>
            <button onclick={hideSecret}>Hide</button>
            <span class="muted small">auto-hides in 30s</span>
          </div>
        </div>
      {:else}
        {#if revealErr}<div class="err">{revealErr}</div>{/if}
        <button onclick={revealSecret}>{secretLabel}</button>
      {/if}

      <!-- Password / API token history: sealed previous values, last
           5 rotations. Lazy-loaded; collapsed by default to avoid
           leaking the count on detail-pane open. -->
      {#if cred.kind === "password" || cred.kind === "api_token"}
        <div class="history-section">
          <button class="history-toggle" onclick={toggleSecretHistory}>
            {secretHistoryOpen ? "▾" : "▸"} Previous secrets
            {#if secretHistoryOpen && secretHistory.length > 0}
              <span class="muted small">({secretHistory.length})</span>
            {/if}
          </button>
          {#if secretHistoryOpen}
            {#if secretHistoryErr}<div class="err">{secretHistoryErr}</div>{/if}
            {#if secretHistory.length === 0}
              <p class="muted small">
                No previous values stored yet - appears here after the
                first rotation. Keeps the last 5.
              </p>
            {:else}
              <ul class="history-list">
                {#each secretHistory as h (h.id)}
                  <li class="history-row">
                    <div class="history-meta">
                      <span class="history-ts">{fmtTS(h.rotated_at)}</span>
                      <span class="muted small">{h.note}</span>
                    </div>
                    <div class="history-actions">
                      {#if revealedHistory[h.id]}
                        <input
                          readonly
                          class="mono history-val"
                          value={revealedHistory[h.id]}
                        />
                        <button onclick={() => copyHistorySecret(h.id)}>Copy</button>
                        <button onclick={() => hideHistorySecret(h.id)}>Hide</button>
                      {:else}
                        <button onclick={() => revealHistorySecret(h.id)}>Reveal</button>
                        <button onclick={() => copyHistorySecret(h.id)}>Copy</button>
                      {/if}
                      <button class="danger small" onclick={() => deleteHistorySecret(h.id)} title="Delete this snapshot">×</button>
                    </div>
                  </li>
                {/each}
              </ul>
            {/if}
          {/if}
        </div>
      {/if}
    {/if}

    <!-- Public key -->
    {#if cred.public_key}
      <h2>Public key</h2>
      <pre class="pubkey">{cred.public_key}</pre>
      <button onclick={() => copyText(cred.public_key!)}>Copy public key</button>
    {/if}

    <!-- Rotate password -->
    {#if cred.kind === "password" && cred.storage_mode === "managed"}
      <h2>Rotate password</h2>
      {#if pwRotateOpen}
        <div class="form">
          <label>New password
            <PasswordInput bind:value={newPassword} autocomplete="new-password" />
          </label>
          <PasswordStrengthMeter password={newPassword} />
          {#if pwRotateErr}<div class="err">{pwRotateErr}</div>{/if}
          <div class="row-btns">
            <button class="primary" disabled={pwRotating || !newPassword} onclick={doRotatePassword}>
              {pwRotating ? "Saving…" : "Set new password"}
            </button>
            <button onclick={() => { pwRotateOpen = false; newPassword = ""; pwRotateErr = null; }}>Cancel</button>
          </div>
        </div>
      {:else}
        <button onclick={() => (pwRotateOpen = true)}>Rotate password…</button>
      {/if}
    {/if}

    <!-- API token fields -->
    {#if cred.kind === "api_token"}
      <h2>API token</h2>
      <div class="form">
        <label>Token ID
          <input
            bind:value={apiTokenIDEdit}
            placeholder="e.g. user@pam!sshtool - leave empty if the provider has no token id (Hetzner)"
            spellcheck="false"
            class="mono"
          />
          <span class="field-hint">
            Provider-specific. Proxmox uses <code>user@realm!tokenid</code>;
            Hetzner Cloud uses bearer-only tokens, so leave this blank.
          </span>
        </label>
        <label>New token secret
          <PasswordInput
            bind:value={apiTokenSecretNew}
            placeholder="Leave empty to keep the current secret"
            mono
            autocomplete="new-password"
          />
          <span class="field-hint">
            The current secret is preserved unless you type a new one.
            Use the "Show token secret" button above to copy the
            current value if you need it.
          </span>
        </label>
        {#if apiTokenErr}<div class="err">{apiTokenErr}</div>{/if}
        {#if apiTokenOk}<div class="ok-hint">{apiTokenOk}</div>{/if}
        <div class="row-btns">
          <button
            class="primary"
            disabled={apiTokenSaving || (!apiTokenIDDirty && !apiTokenSecretNew)}
            onclick={saveAPITokenChanges}
          >
            {apiTokenSaving ? "Saving…" : "Save changes"}
          </button>
          {#if apiTokenSecretNew}
            <button onclick={clearAPITokenSecret}>Clear secret field</button>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Rotate key -->
    {#if cred.kind === "key" && cred.storage_mode === "managed"}
      <h2>Rotate key</h2>
      {#if rotateOpen}
        <div class="form">
          <div class="toggle-btns">
            <button class:active={rotateMode === "generate"} onclick={() => (rotateMode = "generate")}>
              Generate new keypair
            </button>
            <button class:active={rotateMode === "import"} onclick={() => (rotateMode = "import")}>
              Import PEM
            </button>
          </div>
          {#if rotateMode === "import"}
            <label>Private key (PEM)
              <textarea bind:value={rotatePem} rows="10" class="mono" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"></textarea>
            </label>
            <label>Passphrase (if encrypted)
              <PasswordInput bind:value={rotatePassphrase} placeholder="Leave empty if not encrypted" />
            </label>
          {:else}
            <p class="muted">A new keypair will be generated matching the current key type. The old key will be overwritten in the vault.</p>
          {/if}
          {#if rotateErr}<div class="err">{rotateErr}</div>{/if}
          <div class="row-btns">
            <button
              class="primary"
              disabled={rotating || (rotateMode === "import" && !rotatePem)}
              onclick={doRotateKey}
            >
              {rotating ? "Rotating…" : rotateMode === "generate" ? "Generate & replace" : "Import & replace"}
            </button>
            <button onclick={() => { rotateOpen = false; rotatePem = ""; rotatePassphrase = ""; rotateErr = null; }}>Cancel</button>
          </div>
        </div>
      {:else}
        <button onclick={() => (rotateOpen = true)}>Rotate key…</button>
      {/if}
    {/if}

    <!-- opkssh config -->
    {#if cred.kind === "opkssh"}
      <h2>opkssh config</h2>
      {#if opksshCertLine}
        <p class="cert-status">{opksshCertLine}</p>
      {/if}
      <div class="form">
        <label>Key basename
          <input bind:value={opksshBasename} placeholder="id_ecdsa" />
        </label>
        <label>Provider
          <select bind:value={opksshProviderHint}>
            <option value="">
              {#if opksshDefaultProvider && opksshDefaultProvider !== "webchooser" && opksshAliases.includes(opksshDefaultProvider)}
                Use default ({opksshDefaultProvider})
              {:else if opksshAliases.length > 0}
                Use first provider ({opksshAliases[0]})
              {:else}
                Use default from YAML
              {/if}
            </option>
            {#each opksshAliases as alias}
              <option value={alias}>{alias}</option>
            {/each}
            {#if opksshProviderHint && !opksshAliases.includes(opksshProviderHint)}
              <!-- Keep a saved alias that's no longer in the YAML so it
                   isn't silently dropped on save. -->
              <option value={opksshProviderHint}>{opksshProviderHint} (not in YAML)</option>
            {/if}
          </select>
          <span class="field-hint">
            Which provider from the YAML to log in with. Pick one to skip
            the chooser and avoid always opening the first provider.
            {#if opksshDefaultProvider === "webchooser"}
              <br /><strong>Note:</strong> <code>default_provider: webchooser</code>
              is not supported here - it would just use the first provider.
              Pick one explicitly above.
            {/if}
          </span>
        </label>
        <label>Max cert age
          <input bind:value={opksshMaxAgeText} placeholder="7d" spellcheck="false" />
          <span class="field-hint" class:invalid={opksshMaxAgeSecs == null}>
            Force re-login after the cert has been in the vault this
            long, even if it nominally has time left. Accepts 7d,
            6d23h, 48h or a bare number of hours. Default 7d.
            {#if opksshMaxAgeSecs == null}
              - <strong>can't parse "{opksshMaxAgeText}"</strong>
            {:else}
              <!-- Stored as whole hours - show what actually saves. -->
              = <strong>{fmtDur(Math.max(1, Math.round(opksshMaxAgeSecs / 3600)) * 3600)}</strong>
            {/if}
          </span>
        </label>
        <label>Refresh threshold
          <input bind:value={opksshMinRemainingText} placeholder="1h" spellcheck="false" />
          <span class="field-hint" class:invalid={opksshMinRemainingSecs == null}>
            Refresh proactively when the cert has less than this much
            time remaining (only applies when the cert has an explicit
            <code>valid_before</code>; the "forever-cert" path uses
            Max cert age instead). Accepts 1h, 90m or a bare number
            of minutes. Default 1h.
            {#if opksshMinRemainingSecs == null}
              - <strong>can't parse "{opksshMinRemainingText}"</strong>
            {:else}
              <!-- Stored as whole minutes - show what actually saves. -->
              = <strong>{fmtDur(Math.max(1, Math.round(opksshMinRemainingSecs / 60)) * 60)}</strong>
            {/if}
          </span>
        </label>
        <label>Provider YAML
          <textarea bind:value={opksshYaml} rows="14" placeholder="Paste your opkssh provider YAML here" spellcheck="false" class="mono"></textarea>
        </label>
        {#if opksshSaveErr}<div class="err">{opksshSaveErr}</div>{/if}
        <button class="primary" disabled={opksshSaving} onclick={saveOpksshConfig}>
          {opksshSaving ? "Saving…" : opksshSaveOk ? "Saved ✓" : "Save"}
        </button>
      </div>
    {:else if cred.kind !== "password" && cred.kind !== "key"}
      <h2>Config</h2>
      <pre>{JSON.stringify(cred.config, null, 2)}</pre>
    {/if}

    <!-- Change type -->
    {#if !editing}
      <h2>Type</h2>
      {#if changeTypeOpen}
        <div class="form change-type-form">
          <div class="kind-grid">
            {#each ALL_KINDS as k}
              <button
                class="kind-btn"
                class:active={changeTypeTarget === k}
                class:current={cred.kind === k}
                onclick={() => (changeTypeTarget = k)}
              >
                {k}
                {#if cred.kind === k}<span class="current-tag">current</span>{/if}
              </button>
            {/each}
          </div>
          {#if changeTypeTarget !== cred.kind}
            <p class="warn-text">
              Changing type to <strong>{changeTypeTarget}</strong> will clear the stored config
              and public key. The vault secret will be overwritten when the new type's auth runs.
            </p>
          {/if}
          {#if changeTypeErr}<div class="err">{changeTypeErr}</div>{/if}
          <div class="row-btns">
            <button
              class="primary"
              disabled={changeTypeBusy || changeTypeTarget === cred.kind}
              onclick={doChangeType}
            >
              {changeTypeBusy ? "Changing…" : "Convert"}
            </button>
            <button onclick={() => (changeTypeOpen = false)}>Cancel</button>
          </div>
        </div>
      {:else}
        <div class="type-row">
          <code>{cred.kind}</code>
          <button class="link-btn" onclick={openChangeType}>Change type…</button>
        </div>
      {/if}
    {/if}

    <!-- Usage -->
    <h2>Used by ({usage.length})</h2>
    {#if usageErr}
      <div class="err">{usageErr}</div>
    {:else if usage.length === 0}
      <p class="muted">Not referenced anywhere yet.</p>
    {:else}
      <ul class="usage">
        {#each usage as u (u.kind + u.id)}
          <li>
            <button
              class="usage-link"
              type="button"
              title={`Reveal ${u.name} in the connections tree`}
              onclick={() => view.reveal(u.kind, u.id)}
            >
              {#if u.kind === "folder"}
                <IconFolder size={13} /> <strong>{u.name}</strong> <span class="muted">(folder)</span>
              {:else}
                <IconHost size={13} /> <strong>{u.name}</strong> <span class="muted">- {u.hostname}</span>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}

    <!-- History -->
    <h2>History</h2>
    {#if history.length === 0}
      <p class="muted">No history yet.</p>
    {:else}
      <ul class="history">
        {#each history as h (h.id)}
          <li>
            <span class="ts">{fmtTs(h.changed_at)}</span>
            <span class="by">[{h.rotated_by}]</span>
            <span>{h.note}</span>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>


<style>
  .detail { padding: 1rem 1.25rem; overflow: auto; color: var(--text); }
  .empty { color: var(--overlay0); margin-top: 4rem; text-align: center; }
  .muted { color: var(--overlay0); }
  .small { font-size: 0.72rem; }
  header {
    display: flex; align-items: center; justify-content: space-between;
    border-bottom: 1px solid var(--surface0);
    padding-bottom: 0.5rem; margin-bottom: 1rem;
  }
  .head-actions { display: flex; gap: 0.4rem; }
  h1 { margin: 0; font-size: 1.1rem; font-weight: 600; }
  h2 {
    margin-top: 1.5rem; font-size: 0.78rem;
    text-transform: uppercase; letter-spacing: 0.05em; color: var(--subtext0);
  }
  .edit-name {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--blue); border-radius: 3px;
    padding: 0.25rem 0.5rem; font: inherit; font-size: 1.1rem; font-weight: 600;
    flex: 1; margin-right: 0.75rem;
  }
  .edit-form { margin-bottom: 1rem; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 0.4rem 1rem; }
  dt { color: var(--overlay0); font-size: 0.8rem; }
  dd { margin: 0; }
  code { font-size: 0.8rem; background: var(--crust); padding: 0.1rem 0.3rem; border-radius: 3px; }
  pre {
    background: var(--crust); padding: 0.5rem 0.75rem;
    border-radius: 4px; font-size: 0.78rem; overflow: auto;
  }
  .pubkey { word-break: break-all; white-space: pre-wrap; }
  .tag { background: var(--surface0); color: var(--text); padding: 0.1rem 0.4rem; border-radius: 2px; margin-right: 0.3rem; font-size: 0.75rem; }
  .expiry-badge { margin-left: 0.5rem; padding: 0.05rem 0.4rem; border-radius: 999px; font-size: 0.7rem; font-weight: 600; }
  .expiry-badge.ok { background: var(--surface0); color: var(--subtext0); }
  .expiry-badge.soon { background: var(--yellow); color: var(--on-accent); }
  .expiry-badge.expired { background: var(--red); color: var(--on-accent); }
  ul { margin: 0; padding-left: 1.2rem; }
  .usage li, .history li { margin: 0.2rem 0; font-size: 0.85rem; }
  .usage li { padding-left: 0; }
  .usage-link {
    display: inline-flex; align-items: center; gap: 0.3rem;
    background: transparent; border: 0; padding: 0.15rem 0.3rem;
    margin-left: -0.3rem; border-radius: 3px;
    color: var(--text); font: inherit; font-size: 0.85rem;
    cursor: pointer; text-align: left; width: 100%;
  }
  .usage-link:hover { background: var(--surface0); }
  .usage-link strong { font-weight: 600; }
  .ts { color: var(--overlay0); font-size: 0.75rem; margin-right: 0.4rem; }
  .by { color: var(--subtext0); font-size: 0.75rem; margin-right: 0.4rem; }
  .form { display: flex; flex-direction: column; gap: 0.6rem; max-width: 560px; }
  .form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--subtext0); }
  .field-hint { color: var(--overlay0); font-size: 0.72rem; line-height: 1.45; }
  .cert-status {
    margin: 0 0 0.6rem;
    padding: 0.45rem 0.6rem;
    background: var(--mantle);
    border-left: 3px solid var(--blue);
    border-radius: 0 3px 3px 0;
    font-size: 0.8rem;
    color: var(--subtext0);
  }
  .field-hint code { background: var(--mantle); padding: 0 0.25rem; border-radius: 2px; }
  .ok-hint {
    color: var(--green); font-size: 0.82rem;
    background: color-mix(in oklab, var(--green) 12%, var(--bg-panel)); border: 1px solid color-mix(in oklab, var(--green) 30%, var(--bg-panel));
    padding: 0.3rem 0.55rem; border-radius: 3px;
  }
  input, textarea {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  input:focus, textarea:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .mono { font-family: monospace; font-size: 0.78rem; resize: vertical; }
  button {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.35rem 0.7rem; border-radius: 3px;
    cursor: pointer; font: inherit;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  button.primary { background: var(--blue); color: var(--on-accent); font-weight: 600; align-self: flex-start; }
  button.primary:hover:not(:disabled) { background: var(--lavender); }
  button.danger { background: transparent; color: var(--red); }
  button.danger:hover { background: var(--red); color: var(--on-accent); }
  .err { color: var(--red); background: var(--crust); padding: 0.5rem 0.75rem; border-radius: 4px; border-left: 3px solid var(--red); }
  .inline-err { margin-bottom: 0.5rem; }
  .secret-box { display: flex; flex-direction: column; gap: 0.4rem; max-width: 560px; }
  .secret-ta { width: 100%; }
  .secret-actions { display: flex; align-items: center; gap: 0.5rem; }
  .history-section {
    margin-top: 0.8rem;
    max-width: 560px;
  }
  .history-toggle {
    background: transparent;
    border: 0;
    color: var(--subtext0);
    padding: 0.2rem 0;
    font: inherit;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .history-toggle:hover { color: var(--text); }
  .history-list {
    list-style: none;
    padding: 0;
    margin: 0.4rem 0 0;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  .history-row {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.4rem 0.5rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
  }
  .history-meta {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    font-size: 0.8rem;
  }
  .history-ts {
    color: var(--text);
    font-family: ui-monospace, monospace;
  }
  .history-actions {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .history-val {
    flex: 1;
    min-width: 200px;
    background: var(--base);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.2rem 0.4rem;
    font-size: 0.8rem;
  }
  .history-actions .danger.small {
    background: transparent;
    color: var(--red);
    border: 1px solid var(--surface0);
    padding: 0.1rem 0.4rem;
    font-size: 0.8rem;
  }
  .history-actions .danger.small:hover {
    background: var(--red);
    color: var(--on-accent);
  }
  .toggle-btns { display: flex; gap: 0; }
  .toggle-btns button { border-radius: 0; border: 1px solid var(--surface0); }
  .toggle-btns button:first-child { border-radius: 3px 0 0 3px; }
  .toggle-btns button:last-child { border-radius: 0 3px 3px 0; }
  .toggle-btns button.active { background: var(--surface1); color: var(--text); }
  .row-btns { display: flex; gap: 0.5rem; align-items: center; }
  .type-row { display: flex; align-items: center; gap: 0.75rem; }
  .link-btn {
    background: transparent; color: var(--blue); border: 0;
    padding: 0; font: inherit; font-size: 0.8rem;
    text-decoration: underline; cursor: pointer;
  }
  .link-btn:hover { color: var(--lavender); }
  .change-type-form { margin-top: 0.5rem; }
  .kind-grid {
    display: flex; flex-wrap: wrap; gap: 0.4rem;
  }
  .kind-btn {
    display: flex; align-items: center; gap: 0.4rem;
    padding: 0.35rem 0.75rem;
    background: var(--mantle); border: 1px solid var(--surface0); border-radius: 4px;
    color: var(--subtext0); font: inherit; font-size: 0.82rem; cursor: pointer;
  }
  .kind-btn:hover { border-color: var(--surface1); color: var(--text); }
  .kind-btn.active { border-color: var(--blue); color: var(--text); background: var(--base); }
  .kind-btn.current { color: var(--overlay0); }
  .kind-btn.active.current { border-color: var(--blue); color: var(--text); }
  .current-tag {
    font-size: 0.65rem; background: var(--surface0); color: var(--subtext0);
    padding: 0 0.3rem; border-radius: 2px;
  }
  .warn-text {
    color: var(--yellow); font-size: 0.8rem;
    background: var(--base); border-left: 3px solid var(--yellow);
    padding: 0.4rem 0.6rem; border-radius: 4px; margin: 0;
  }
</style>
