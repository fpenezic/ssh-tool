<script lang="ts">
  import { profileKindTitle, profileRunsHere, NOT_ON_MOBILE_TITLE } from "./networkKind";
  import { splitHostInput } from "./hostInput";
  import HomePane from "./HomePane.svelte";
  import { tree, credentials, selection, sessions, paneTabs, view, jumpChainHostnames } from "./stores.svelte";
  import { untrack } from "svelte";
  import { registerUnsaved, confirmLeave } from "./unsaved.svelte.ts";
  import { flipTri } from "./triSwitch";
  import TagInput from "./TagInput.svelte";
  import { isMobile } from "./platform";
  import { toast } from "./toast.svelte";
  import { tunnels } from "./tunnels.svelte.ts";
  import { writeClipboard, readClipboard } from "./clipboard";
  import PasswordInput from "./PasswordInput.svelte";
  import { effectiveAuthRef, isKeyAuthKind, isSelectableSSHCred } from "./authWarning";
  import { api, dbEngineInfo, type ResolvedSettings, type JumpHostOverride } from "./api";
  import { showPrompt } from "./promptModal.svelte.ts";
  import JumpChainEditor from "./JumpChainEditor.svelte";
  import PortForwards from "./PortForwards.svelte";
  import BatchPanel from "./BatchPanel.svelte";
  import FleetBar from "./FleetBar.svelte";
  import FleetCards from "./FleetCards.svelte";
  import { folderHostIds, unloadedDynamicUnder } from "./fleetStore.svelte";
  import BatchExecModal from "./BatchExecModal.svelte";
  import ColorPicker from "./ColorPicker.svelte";
  import IconPicker from "./IconPicker.svelte";
  import Icon from "./Icon.svelte";
  import { IconFolder, IconHost, IconLock, IconUser, IconClipboardCopy, IconTerminal, IconMonitor, IconStar, IconGlobe, IconBastion, IconTrash, IconKey, IconVpn, IconKafka, IconDb, IconChevronDown } from "./iconMap";
  import { contextMenu, type MenuItem } from "./contextMenu.svelte.ts";
  import appIcon from "../assets/images/app-icon-64.png";
  import type { TunnelInfo, NetworkProfileInfo } from "./api";
  import DeleteConfirm from "./DeleteConfirm.svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import { explain as explainConnectError, unwrapRaw as unwrapConnectErr, errMsg } from "./connectErrors";
  import { EventsOn } from "./wailsRuntime";
  import { renderMarkdown } from "./markdown";
  import SearchableSelect from "./SearchableSelect.svelte";
  import KeepassEntryPicker from "./KeepassEntryPicker.svelte";
  import BitwardenEntryPicker from "./BitwardenEntryPicker.svelte";
  import InfisicalEntryPicker from "./InfisicalEntryPicker.svelte";
  import PasswordStrengthMeter from "./PasswordStrengthMeter.svelte";
  import DynamicEntryDetail from "./DynamicEntryDetail.svelte";
  import { networkProfiles } from "./networkProfiles.svelte";
  import KafkaSettingsEditor from "./KafkaSettingsEditor.svelte";
  import { applyKafkaDraft, kafkaDraftFrom, kafkaDraftSame, type KafkaDraft } from "./kafkaDraft";
  import { kafkaEnabledFor, kafkaInheritedFrom } from "./kafkaState.svelte";
  import DbSettingsEditor from "./DbSettingsEditor.svelte";
  import { applyDbDraft, dbDraftFrom, dbDraftSame, type DbDraft } from "./dbDraft";
  import { dbEnabledFor, dbInheritedFrom } from "./dbState.svelte";

  // Populate the Network dropdowns; cached after the first load and
  // refreshed via the network_tunnel_changed event inside the store.
  $effect(() => { networkProfiles.load().catch(() => {}); });

  // The numeric editor fields are typed `string` but bound to <input
  // type="number">, which makes Svelte coerce the value to a number as soon
  // as the user types. Calling .trim() on that threw and killed the save
  // handler before it reached the IPC call - the Save button lit up but
  // clicking it did nothing. Normalise to a trimmed string instead of
  // trusting the declared type.
  function numText(v: string | number | undefined | null): string {
    if (v === undefined || v === null) return "";
    return String(v).trim();
  }

  // Blank means "inherit"; anything else is a whole number of milliseconds,
  // clamped so a typo cannot stall a connect for minutes. 0 is a real value
  // (send the lines back to back), not the same as blank.
  const MAX_INITIAL_COMMAND_DELAY_MS = 60000;
  function parseDelayMs(v: string): number | undefined {
    const t = numText(v);
    if (t === "") return undefined;
    const n = parseInt(t, 10);
    if (isNaN(n) || n < 0) return undefined;
    return Math.min(n, MAX_INITIAL_COMMAND_DELAY_MS);
  }

  // Stable option list for the credential dropdown - same shape both
  // editor instances need. flatGrouped() returns { cred, label } items
  // where `label` includes the folder path so duplicates by name are
  // disambiguated.
  // Credentials offerable as SSH auth. api_token is deliberately excluded:
  // those exist for the dynamic-inventory providers (Proxmox, Hetzner, ...)
  // and cannot authenticate an SSH session, so listing them here only invites
  // a connection that fails at connect time.
  //
  // An api_token already set on a connection stays selectable, so an existing
  // (if broken) config does not silently lose its reference when opened.
  function sshCredOptions(keepId: string | undefined) {
    return credentials
      .flatGrouped()
      .filter((g) => isSelectableSSHCred(g.cred.kind, g.cred.id, keepId))
      .map((g) => ({ value: g.cred.id, label: g.label }));
  }

  // "None" in the connection's credential picker: stored as auth_ref "",
  // which stops the folder's credential from being inherited (the backend
  // resolver turns "" into no credential). The connection's own password
  // is then used, or the user is asked at connect.
  const AUTH_NONE = "__none__";
  function encodeAuthRef(v: string | undefined): string {
    return v === "" ? AUTH_NONE : (v ?? "");
  }
  function decodeAuthRef(v: string): string | undefined {
    return v === AUTH_NONE ? "" : (v || undefined);
  }

  const multiCount = $derived(selection.multiCount());
  const folderMultiCount = $derived(selection.folderMultiCount());
  const dynamicMulti = $derived(selection.selectedDynamicEntries());

  const folder = $derived(selection.selectedFolder());
  // Every SSH host under the selected folder, for its Fleet bar.
  const folderFleetIds = $derived(folder ? folderHostIds(folder.id) : []);
  // Load the cached entries of dynamic subfolders nobody expanded, so the
  // Fleet bar counts and runs on their hosts too (folderFleetIds follows).
  $effect(() => {
    const fid = folder?.id;
    if (!fid) return;
    for (const d of unloadedDynamicUnder(fid)) void tree.loadDynamicEntries(d);
  });
  const conn = $derived(selection.selectedConnection());
  const credList = $derived(credentials.list);

  // Set of credential ids that reference a KeePass entry, so the auth picker
  // can badge them apart from vault-backed passwords.
  const keepassCredIds = $derived(
    new Set(credList.filter((c) => !!c.config?.keepass_ref).map((c) => c.id)),
  );

  // Same, for Bitwarden-backed credentials.
  const bitwardenCredIds = $derived(
    new Set(credList.filter((c) => !!c.config?.bitwarden_ref).map((c) => c.id)),
  );

  // Credentials that can supply a one-time code: only external items
  // (Bitwarden / KeePass) carry a TOTP secret.
  const otpCredOptions = $derived(
    credList
      .filter((c) => !!c.config?.bitwarden_ref || !!c.config?.keepass_ref)
      .map((c) => ({ value: c.id, label: c.name })),
  );

  // Same, for Infisical-backed credentials.
  const infisicalCredIds = $derived(
    new Set(credList.filter((c) => !!c.config?.infisical_ref).map((c) => c.id)),
  );

  // Whether any KeePass database is registered - the "From KeePass" button is
  // hidden entirely when none is, since it would open an empty picker. Checked
  // on mount and re-checked live when a database is added/removed in Settings
  // (keepass_dbs_changed), so the button appears without an app restart.
  let hasKeepass = $state(false);
  async function refreshHasKeepass() {
    try {
      const dbs = await api.keepassList();
      hasKeepass = (dbs?.length ?? 0) > 0;
    } catch {
      /* leave as-is */
    }
  }
  let keepassChecked = false;
  $effect(() => {
    if (keepassChecked) return;
    keepassChecked = true;
    refreshHasKeepass();
    const off = EventsOn("keepass_dbs_changed", () => refreshHasKeepass());
    return off;
  });

  // Whether any Bitwarden server is registered - same live-gate as KeePass.
  let hasBitwarden = $state(false);
  async function refreshHasBitwarden() {
    try {
      const srvs = await api.bitwardenList();
      hasBitwarden = (srvs?.length ?? 0) > 0;
    } catch {
      /* leave as-is */
    }
  }
  let bitwardenChecked = false;
  $effect(() => {
    if (bitwardenChecked) return;
    bitwardenChecked = true;
    refreshHasBitwarden();
    const off = EventsOn("bitwarden_servers_changed", () => refreshHasBitwarden());
    return off;
  });

  // Whether any Infisical server is registered - same live-gate as the others.
  let hasInfisical = $state(false);
  async function refreshHasInfisical() {
    try {
      const srvs = await api.infisicalList();
      hasInfisical = (srvs?.length ?? 0) > 0;
    } catch {
      /* leave as-is */
    }
  }
  let infisicalChecked = false;
  $effect(() => {
    if (infisicalChecked) return;
    infisicalChecked = true;
    refreshHasInfisical();
    const off = EventsOn("infisical_servers_changed", () => refreshHasInfisical());
    return off;
  });

  // KeePass entry-picker modal. `target` says which editor the chosen entry's
  // auto-created credential should be assigned to.
  let kpPickerOpen = $state(false);
  // "-otp" targets fill the One-time code picker instead of the credential.
  type PickerTarget = "folder" | "connection" | "folder-otp" | "connection-otp";
  let kpPickerTarget = $state<PickerTarget | null>(null);
  // assignPicked puts a credential made from a picked external item into
  // the field the picker was opened for.
  function assignPicked(target: PickerTarget | null, id: string) {
    if (target === "folder" && editingFolder) editingFolder = { ...editingFolder, authRef: id };
    else if (target === "folder-otp" && editingFolder) editingFolder = { ...editingFolder, otpRef: id };
    else if (target === "connection" && editing) editing = { ...editing, authRef: id };
    else if (target === "connection-otp" && editing) editing = { ...editing, otpRef: id };
  }

  function openKeepassPicker(target: PickerTarget) {
    kpPickerTarget = target;
    kpPickerOpen = true;
  }

  async function onKeepassPick(r: {
    db_id: string; entry_uuid: string; field: string;
    is_key: boolean; name: string; username: string;
  }) {
    kpPickerOpen = false;
    try {
      // folder_id null lets the backend file the credential under the
      // auto-created "KeePass" credential folder. It must NOT inherit the
      // connection's folder_id: that id lives in the connection tree (folders),
      // while credential.folder_id references credential_folders - a different
      // namespace, so reusing it hits a foreign-key error.
      const cred = await api.keepassEnsureCredential({
        db_id: r.db_id,
        entry_uuid: r.entry_uuid,
        field: r.field,
        is_key: r.is_key,
        name: r.name,
        username: r.username,
        folder_id: null,
      });
      await credentials.load();
      assignPicked(kpPickerTarget, cred.id);
      toast.ok(`Using KeePass entry "${r.name}"`);
    } catch (e) {
      toast.err("KeePass: " + errMsg(e));
    } finally {
      kpPickerTarget = null;
    }
  }

  // Bitwarden item-picker modal, same shape as the KeePass one.
  let bwPickerOpen = $state(false);
  let bwPickerTarget = $state<PickerTarget | null>(null);

  function openBitwardenPicker(target: PickerTarget) {
    bwPickerTarget = target;
    bwPickerOpen = true;
  }

  async function onBitwardenPick(r: {
    server_id: string; cipher_id: string; field: string;
    is_key: boolean; name: string; username: string;
  }) {
    bwPickerOpen = false;
    try {
      // folder_id null: the backend files the credential under the auto-created
      // "Bitwarden" credential folder. Never pass a connection's folder_id here -
      // same credential_folders vs folders namespace trap as KeePass.
      const cred = await api.bitwardenEnsureCredential({
        server_id: r.server_id,
        cipher_id: r.cipher_id,
        field: r.field,
        is_key: r.is_key,
        name: r.name,
        username: r.username,
        folder_id: null,
      });
      await credentials.load();
      assignPicked(bwPickerTarget, cred.id);
      toast.ok(`Using Bitwarden item "${r.name}"`);
    } catch (e) {
      toast.err("Bitwarden: " + errMsg(e));
    } finally {
      bwPickerTarget = null;
    }
  }

  // Infisical secret-picker modal, same shape as the KeePass / Bitwarden ones.
  let infPickerOpen = $state(false);
  let infPickerTarget = $state<"folder" | "connection" | null>(null);

  function openInfisicalPicker(target: "folder" | "connection") {
    infPickerTarget = target;
    infPickerOpen = true;
  }

  async function onInfisicalPick(r: {
    server_id: string; project_id: string; environment: string;
    secret_path: string; key: string; is_key: boolean; name: string;
  }) {
    infPickerOpen = false;
    try {
      // folder_id null: the backend files the credential under the auto-created
      // "Infisical" credential folder. Never pass a connection's folder_id here -
      // same credential_folders vs folders namespace trap as KeePass / Bitwarden.
      const cred = await api.infisicalEnsureCredential({
        server_id: r.server_id,
        project_id: r.project_id,
        environment: r.environment,
        secret_path: r.secret_path,
        key: r.key,
        is_key: r.is_key,
        name: r.name,
        folder_id: null,
      });
      await credentials.load();
      if (infPickerTarget === "folder" && editingFolder) {
        editingFolder = { ...editingFolder, authRef: cred.id };
      } else if (infPickerTarget === "connection" && editing) {
        editing = { ...editing, authRef: cred.id };
      }
      toast.ok(`Using Infisical secret "${r.name}"`);
    } catch (e) {
      toast.err("Infisical: " + errMsg(e));
    } finally {
      infPickerTarget = null;
    }
  }

  // A pasted "host:port" or "user@host:port" in the Hostname field moves
  // its port and user into their own fields.
  function splitHostField() {
    if (!editing) return;
    const t = splitHostInput(editing.hostname);
    if (t.port === undefined && t.user === undefined) return;
    editing.hostname = t.host;
    if (t.port !== undefined) editing.port = String(t.port);
    if (t.user !== undefined) editing.username = t.user;
  }

  // ----- Folder editing -----

  // autoReconnect tri-state encoded as string for <select> bind:value:
  //   "" = inherit (undefined / null in InheritableSettings)
  //   "on" = true
  //   "off" = false
  let editingFolder = $state<{
    name: string;
    username: string;
    port: string;
    authRef: string;
    otpRef: string;
    jumpHost: JumpHostOverride | undefined;
    colorTag: string;
    autoReconnect: string;
    sensitive: string;
    verbose: string;
    probeLiveness: string;
    keepalive: string;
    networkProfile: string;
    initialCommand: string;
    initialCommandDelay: string;
    tunnelOnly: string;
    tunnelAutostart: string;
    vncDefault: string;
    kafka: KafkaDraft;
    db: DbDraft;
  } | null>(null);

  // Same api_token exclusion as the connection picker, keyed on the folder's
  // own current value so an existing reference is not dropped on open.
  const folderCredOptions = $derived(sshCredOptions(editingFolder?.authRef));

  function encodeBool(v: boolean | undefined): string {
    if (v === true) return "on";
    if (v === false) return "off";
    return "";
  }
  function decodeBool(s: string): boolean | undefined {
    if (s === "on") return true;
    if (s === "off") return false;
    return undefined;
  }


  // network_profile_id tri-state for <select>: "" = inherit,
  // "__direct__" = explicit direct (stored as ""), else profile id.
  function encodeNetProfile(v: string | undefined): string {
    if (v === undefined) return "";
    if (v === "") return "__direct__";
    return v;
  }
  function decodeNetProfile(s: string): string | undefined {
    if (s === "") return undefined;
    if (s === "__direct__") return "";
    return s;
  }

  // Unsaved-edit guard: the tree, the editor tabs and the rest ask before
  // a navigation would drop the draft (unsaved.svelte.ts). Discard bumps
  // the epoch so both draft effects below reload from the stored values.
  let draftEpoch = $state(0);
  let draftConnId: string | null = null;
  let draftConnEpoch = 0;
  let draftFolderId: string | null = null;
  let draftFolderEpoch = 0;
  // The stored values each draft started from: what "the user changed it"
  // is measured against when the same item reloads.
  let draftConnOrig: Record<string, unknown> | null = null;
  let draftFolderOrig: Record<string, unknown> | null = null;
  function discardDraft() { draftEpoch++; }
  function edited(cur: object | null, orig: Record<string, unknown> | null): boolean {
    return !!cur && !!orig && JSON.stringify(cur) !== JSON.stringify(orig);
  }
  // Same item reloaded while edits are open: a field the user changed keeps
  // its value, every other field takes the refreshed one (a sync may have
  // changed it), so Save does not write stale values back.
  // The Database and Kafka drafts are groups of separate settings, merged
  // field by field (their lists stay whole); any other field is one value.
  const NESTED_DRAFTS = new Set(["db", "kafka"]);
  function mergeDraft<T extends object>(cur: T, orig: Record<string, unknown>, fresh: T): T {
    const out = { ...fresh } as Record<string, unknown>;
    const c = cur as Record<string, unknown>;
    for (const k of Object.keys(out)) {
      if (JSON.stringify(c[k]) === JSON.stringify(orig[k])) continue;
      const isGroup = NESTED_DRAFTS.has(k) && [c[k], orig[k], out[k]].every((v) => !!v && typeof v === "object" && !Array.isArray(v));
      out[k] = isGroup
        ? mergeDraft(c[k] as object, orig[k] as Record<string, unknown>, out[k] as object)
        : c[k];
    }
    return out as T;
  }
  $effect(() => registerUnsaved({
    label: () => conn?.name ?? folder?.name ?? "",
    dirty: () => (!!conn && dirty) || (!conn && !!folder && folderDirty),
    busy: () => connSaving || folderSaving,
    save: () => (conn ? saveConn() : folder ? saveFolder() : Promise.resolve(true)),
    discard: discardDraft,
  }));

  $effect(() => {
    if (folder) {
      const keep = folder.id === draftFolderId && draftEpoch === draftFolderEpoch && untrack(() => edited(editingFolder, draftFolderOrig));
      draftFolderId = folder.id;
      draftFolderEpoch = draftEpoch;
      const fresh = {
        name: folder.name,
        username: folder.settings.username ?? "",
        port: folder.settings.port?.toString() ?? "",
        authRef: folder.settings.auth_ref ?? "",
        otpRef: folder.settings.otp_ref ?? "",
        jumpHost: folder.settings.jump_host,
        colorTag: folder.settings.color_tag ?? "",
        autoReconnect: encodeBool(folder.settings.auto_reconnect),
        sensitive: encodeBool(folder.settings.sensitive),
        verbose: encodeBool(folder.settings.verbose),
        probeLiveness: encodeBool(folder.settings.probe_liveness),
        keepalive: folder.settings.keepalive_interval !== undefined
          ? String(folder.settings.keepalive_interval)
          : "",
        networkProfile: encodeNetProfile(folder.settings.network_profile_id),
        initialCommand: folder.settings.initial_command ?? "",
        initialCommandDelay:
          folder.settings.initial_command_line_delay_ms !== undefined
            ? String(folder.settings.initial_command_line_delay_ms)
            : "",
        tunnelOnly: encodeBool(folder.settings.tunnel_only),
        tunnelAutostart: encodeBool(folder.settings.tunnel_autostart),
        vncDefault: encodeBool(folder.settings.vnc_default),
        kafka: kafkaDraftFrom(folder.settings),
        db: dbDraftFrom(folder.settings),
      };
      const prev = untrack(() => editingFolder);
      editingFolder = keep && prev && draftFolderOrig ? mergeDraft(prev, draftFolderOrig, fresh) : fresh;
      draftFolderOrig = JSON.parse(JSON.stringify(fresh));
    } else {
      editingFolder = null;
    }
  });

  // Mirrors the connection `dirty` derived so the folder editor shows
  // the same unsaved-changes affordance (Save * / warn on switch).
  const folderDirty = $derived.by(() => {
    if (!folder || !editingFolder) return false;
    const s = folder.settings ?? {};
    return (
      editingFolder.name !== folder.name ||
      editingFolder.username !== (s.username ?? "") ||
      numText(editingFolder.port) !== (s.port?.toString() ?? "") ||
      editingFolder.authRef !== (s.auth_ref ?? "") ||
      editingFolder.otpRef !== (s.otp_ref ?? "") ||
      editingFolder.colorTag !== (s.color_tag ?? "") ||
      editingFolder.autoReconnect !== encodeBool(s.auto_reconnect) ||
      editingFolder.sensitive !== encodeBool(s.sensitive) ||
      editingFolder.verbose !== encodeBool(s.verbose) ||
      editingFolder.probeLiveness !== encodeBool(s.probe_liveness) ||
      editingFolder.tunnelOnly !== encodeBool(s.tunnel_only) ||
      editingFolder.tunnelAutostart !== encodeBool(s.tunnel_autostart) ||
      editingFolder.vncDefault !== encodeBool(s.vnc_default) ||
      !kafkaDraftSame(editingFolder.kafka, s) ||
      !dbDraftSame(editingFolder.db, s) ||
      numText(editingFolder.keepalive) !== (s.keepalive_interval !== undefined ? String(s.keepalive_interval) : "") ||
      editingFolder.networkProfile !== encodeNetProfile(s.network_profile_id) ||
      editingFolder.initialCommand !== (s.initial_command ?? "") ||
      numText(editingFolder.initialCommandDelay) !==
        (s.initial_command_line_delay_ms !== undefined
          ? String(s.initial_command_line_delay_ms)
          : "") ||
      JSON.stringify(editingFolder.jumpHost ?? null) !== JSON.stringify(s.jump_host ?? null)
    );
  });

  let folderSaving = false;
  async function saveFolder(): Promise<boolean> {
    if (!folder || !editingFolder || folderSaving) return false;
    folderSaving = true;
    const settings = { ...folder.settings };
    settings.username = editingFolder.username.trim() || undefined;
    {
      const p = numText(editingFolder.port);
      const n = p === "" ? NaN : parseInt(p, 10);
      settings.port = isNaN(n) ? undefined : n;
    }
    settings.auth_ref = editingFolder.authRef || undefined;
    settings.otp_ref = editingFolder.otpRef || undefined;
    settings.jump_host = editingFolder.jumpHost;
    settings.color_tag = editingFolder.colorTag || undefined;
    settings.auto_reconnect = decodeBool(editingFolder.autoReconnect);
    settings.sensitive = decodeBool(editingFolder.sensitive);
    settings.verbose = decodeBool(editingFolder.verbose);
    settings.probe_liveness = decodeBool(editingFolder.probeLiveness);
    settings.tunnel_only = decodeBool(editingFolder.tunnelOnly);
    settings.tunnel_autostart = decodeBool(editingFolder.tunnelAutostart);
    settings.vnc_default = decodeBool(editingFolder.vncDefault);
    applyKafkaDraft(settings, editingFolder.kafka);
    applyDbDraft(settings, editingFolder.db);
    {
      const k = numText(editingFolder.keepalive);
      if (k === "") {
        settings.keepalive_interval = undefined;
      } else {
        const n = parseInt(k, 10);
        settings.keepalive_interval = isNaN(n) ? undefined : n;
      }
    }
    settings.network_profile_id = decodeNetProfile(editingFolder.networkProfile);
    settings.initial_command = editingFolder.initialCommand.trim() || undefined;
    settings.initial_command_line_delay_ms = parseDelayMs(editingFolder.initialCommandDelay);
    try {
      await api.foldersUpdate({ id: folder.id, name: editingFolder.name, settings });
      await tree.load();
      showSaved();
      return true;
    } catch (e: any) {
      toast.err(`Save failed: ${errMsg(e)}`);
      return false;
    } finally {
      folderSaving = false;
    }
  }

  async function addSubfolder() {
    if (!folder) return;
    const name = await showPrompt("Subfolder name?");
    if (!name?.trim()) return;
    const created = await api.foldersCreate({ name: name.trim(), parentId: folder.id });
    await tree.load();
    // reveal, not select: selecting alone leaves the row hidden when the
    // parent folder is collapsed.
    if (created?.id) view.reveal("folder", created.id);
    else selection.select({ kind: "folder", id: folder.id });
  }

  async function addConnectionHere() {
    if (!folder) return;
    const name = await showPrompt("Connection name? (a label for the tree)");
    if (!name?.trim()) return;
    const hostname = await showPrompt("Hostname / IP address? (what SSH connects to)") ?? "";
    const conn = await api.connectionsCreate({ folderId: folder.id, name: name.trim(), hostname });
    await tree.load();
    selection.select({ kind: "connection", id: (conn as any).id });
  }

  // ----- Connection editing -----

  let resolved = $state<ResolvedSettings | null>(null);

  // This connection's tunnel, when it is a tunnel-only connection; the
  // header then offers Start / Stop tunnel ahead of Open terminal.
  const connTunnel = $derived(conn ? tunnels.byConnection(conn.id) : undefined);

  // A setting as the folder chain from folderId upwards sets it (the value
  // an "(inherit)" choice gets), with the name of the folder that sets it.
  // Walked live from the tree so the editor reacts to a select before
  // anything is saved.
  function inheritedSetting(folderId: string | null | undefined, key: string): { value: any; from: string | null } {
    for (let i = 0; i < 10000 && folderId; i++) {
      const f = tree.folderById(folderId);
      if (!f) break;
      const v = (f.settings as any)?.[key];
      if (v !== undefined && v !== null && v !== "") return { value: v, from: f.name };
      folderId = f.parent_id;
    }
    return { value: undefined, from: null };
  }
  function inheritedTunnelOnly(folderId: string | null | undefined): boolean {
    return !!inheritedSetting(folderId, "tunnel_only").value;
  }

  // ----- Editor tabs -----
  // One set of tabs for the connection and the folder editor, so a setting
  // sits in the same place in both. The last tab picked is kept (per
  // viewer, browser storage); a tab the current item lacks falls back to
  // General.
  // Database and Kafka share one Services tab; "db" / "kafka" (a stored
  // pick, a tree row asking for its settings) land there.
  type EdTab = "general" | "connection" | "terminal" | "forwards" | "vnc" | "services" | "advanced";
  const ED_TAB_KEY = "ssh-tool.editor.tab";
  function edTabOf(t: string | null | undefined): EdTab | null {
    if (!t) return null;
    return (t === "db" || t === "kafka" ? "services" : t) as EdTab;
  }
  let edTabPref = $state<EdTab>((() => {
    try { return edTabOf(localStorage.getItem(ED_TAB_KEY)) || "general"; } catch { return "general"; }
  })());
  async function setEdTab(t: EdTab) {
    // Only a sub-form inside the tab (the forward form) is lost here; the
    // connection draft itself survives a tab switch.
    if (!(await confirmLeave(["tab"]))) return;
    view.editorTabForce = null;
    edTabPref = t;
    try { localStorage.setItem(ED_TAB_KEY, t); } catch { /* private window */ }
  }

  // "Double-click opens": one choice over four stored flags (tunnel_only,
  // vnc_default, kafka_default, db_default), so they can no longer be on
  // together with one silently winning. The editor writes exactly one of
  // them on; a row that has several on from before resolves Tunnel > VNC >
  // Kafka > Database > Terminal, the order connectDefault checks them in.
  type OpenAs = "" | "terminal" | "tunnel" | "vnc" | "kafka" | "db";
  function openAsLabel(m: OpenAs): string {
    return m === "tunnel" ? "Tunnel" : m === "vnc" ? "VNC console" : m === "kafka" ? "Kafka" : m === "db" ? "Database" : "Terminal";
  }
  // The service choices of "Double-click opens", the ones that are on
  // first: the switched-off (greyed) ones sink to the bottom.
  function openAsServices(on: { vnc: boolean; kafka: boolean; db: boolean }, scope: "conn" | "folder") {
    const off = (what: string) => (scope === "conn" ? `turn ${what} on first` : `${what} is not on for this folder`);
    const list = [
      { value: "vnc", label: "VNC console", on: on.vnc, off: off("VNC") },
      { value: "kafka", label: "Kafka", on: on.kafka, off: off("Kafka") },
      { value: "db", label: "Database", on: on.db, off: off("the database") },
    ];
    return [...list.filter((o) => o.on), ...list.filter((o) => !o.on)];
  }
  function inheritedOpenAs(folderId: string | null | undefined): { mode: OpenAs; from: string | null } {
    const t = inheritedSetting(folderId, "tunnel_only");
    if (t.value) return { mode: "tunnel", from: t.from };
    const v = inheritedSetting(folderId, "vnc_default");
    const on = inheritedSetting(folderId, "vnc_enabled");
    if (v.value && on.value) return { mode: "vnc", from: v.from };
    const k = inheritedSetting(folderId, "kafka_default");
    const kOn = inheritedSetting(folderId, "kafka_enabled");
    if (k.value && kOn.value) return { mode: "kafka", from: k.from };
    const d = inheritedSetting(folderId, "db_default");
    const dOn = inheritedSetting(folderId, "db_enabled");
    if (d.value && dOn.value) return { mode: "db", from: d.from };
    return { mode: "terminal", from: t.from ?? v.from ?? k.from ?? d.from };
  }
  function openAsOf(
    tunnelOnly: string, vncDefault: string, kafkaDefault: string, dbDefault: string,
    inh: OpenAs, vncOn: boolean, kafkaOn: boolean, dbOn: boolean,
  ): OpenAs {
    if (tunnelOnly === "" && vncDefault === "" && kafkaDefault === "" && dbDefault === "") return "";
    const tunnel = tunnelOnly === "on" || (tunnelOnly === "" && inh === "tunnel");
    if (tunnel) return "tunnel";
    const vnc = vncDefault === "on" || (vncDefault === "" && inh === "vnc");
    if (vnc && vncOn) return "vnc";
    const kafka = kafkaDefault === "on" || (kafkaDefault === "" && inh === "kafka");
    if (kafka && kafkaOn) return "kafka";
    const db = dbDefault === "on" || (dbDefault === "" && inh === "db");
    return db && dbOn ? "db" : "terminal";
  }
  function openAsFlags(m: OpenAs): { tunnelOnly: string; vncDefault: string; kafkaDefault: string; dbDefault: string } {
    const off = { tunnelOnly: "off", vncDefault: "off", kafkaDefault: "off", dbDefault: "off" };
    if (m === "tunnel") return { ...off, tunnelOnly: "on" };
    if (m === "vnc") return { ...off, vncDefault: "on" };
    if (m === "kafka") return { ...off, kafkaDefault: "on" };
    if (m === "db") return { ...off, dbDefault: "on" };
    if (m === "terminal") return off;
    return { tunnelOnly: "", vncDefault: "", kafkaDefault: "", dbDefault: "" };
  }

  // Tunnel state words and colour, shared by the header pill and the
  // Forwards tab.
  function tunnelTone(t: TunnelInfo): string {
    if (t.state === "error") return "bad";
    if (t.state === "connected") return t.degraded ? "warn" : "ok";
    if (t.state === "stopped") return "idle";
    return "warn";
  }
  function tunnelText(t: TunnelInfo): string {
    const fw = t.forwards ?? [];
    const listening = fw.filter((f) => f.state === "listening").length;
    if (t.state === "connected") return `tunnel up, ${listening}/${fw.length} listening`;
    if (t.state === "error") return "tunnel failed";
    if (t.state === "stopped") return "tunnel stopped";
    return `tunnel ${t.state}`;
  }
  let resolveErr = $state<string | null>(null);
  let editing = $state<{
    name: string;
    hostname: string;
    notes: string;
    username: string;
    port: string;
    authRef: string;
    otpRef: string;
    jumpHost: JumpHostOverride | undefined;
    colorTag: string;
    autoReconnect: string;
    sensitive: string;
    verbose: string;
    keepalive: string;
    vncEnabled: string;
    vncPort: string;
    vncTunnel: string;
    vncDefault: string;
    networkProfile: string;
    initialCommand: string;
    initialCommandDelay: string;
    tunnelOnly: string;
    tunnelAutostart: string;
    kafka: KafkaDraft;
    db: DbDraft;
    localShellKind: string;
    localShellDir: string;
    openHidden: boolean;
    tags: string[];
  } | null>(null);

  // "Start tunnel on app launch" only means something for a tunnel, so the
  // editors show it only while Tunnel only is in effect.
  const connTunnelOnly = $derived(
    !!editing && (editing.tunnelOnly === "on" ||
      (editing.tunnelOnly === "" && inheritedTunnelOnly(conn?.folder_id)))
  );
  const folderTunnelOnly = $derived(
    !!editingFolder && (editingFolder.tunnelOnly === "on" ||
      (editingFolder.tunnelOnly === "" && inheritedTunnelOnly(folder?.parent_id)))
  );

  // The tabs this kind of connection has: a web bookmark is one page, a
  // local shell has no SSH settings.
  type EdTabDef = { id: EdTab; label: string; warn?: string };
  const edTabs = $derived.by((): EdTabDef[] => {
    if (!conn) return [];
    if (isWeb) return [{ id: "general", label: "General" }];
    if (isLocal) return [{ id: "general", label: "General" }, { id: "terminal", label: "Terminal" }];
    const t = connTunnel;
    const warn = t?.state === "error" ? "The tunnel failed" : t?.degraded ? "A forward is not listening" : undefined;
    const tabs: EdTabDef[] = [
      { id: "general", label: "General" },
      { id: "connection", label: "Connection" },
      { id: "terminal", label: "Terminal" },
      { id: "forwards", label: connTunnelOnly ? "Forwards & tunnel" : "Forwards", warn },
    ];
    if (!isMobile) tabs.push({ id: "vnc", label: "VNC" });
    tabs.push({ id: "services", label: "Services" });
    tabs.push({ id: "advanced", label: "Advanced" });
    return tabs;
  });
  function tabWith(force: string | null): EdTab {
    const forced = edTabOf(force);
    if (forced && edTabs.some((t) => t.id === forced)) return forced;
    return edTabs.some((t) => t.id === edTabPref) ? edTabPref : "general";
  }
  const edTab = $derived.by<EdTab>(() => tabWith(view.editorTabForce));
  view.editorTabFor = (force) => tabWith(force);
  // For view.forceEditorTab: which tab a forced change would leave.
  $effect(() => {
    view.editorTabShown = conn ? edTab : null;
  });

  function folderPathOf(folderId: string | null | undefined): string {
    const names: string[] = [];
    for (let i = 0; i < 10000 && folderId; i++) {
      const f = tree.folderById(folderId);
      if (!f) break;
      names.unshift(f.name);
      folderId = f.parent_id;
    }
    return names.join(" / ");
  }

  // Header: who and where, and what is running for this connection.
  const headSub = $derived.by(() => {
    if (!conn) return "";
    const path = folderPathOf(conn.folder_id);
    let target = "";
    if (isLocal) target = "local shell";
    else if (isWeb) target = conn.hostname;
    else {
      const user = resolved?.username ?? (editing?.username || "");
      const port = resolved?.port ?? 22;
      target = `${user ? user + "@" : ""}${conn.hostname}${port !== 22 ? ":" + port : ""}`;
    }
    return path ? `${target} - ${path}` : target;
  });
  const headPill = $derived.by((): { tone: string; text: string; title: string } | null => {
    if (!conn || isWeb) return null;
    if (connTunnel) return { tone: tunnelTone(connTunnel), text: tunnelText(connTunnel), title: connTunnel.error ?? "" };
    const open = sessions.tabs.filter((t) => t.connectionId === conn.id && t.status === "connected").length;
    if (open > 0) return { tone: "ok", text: open === 1 ? "connected" : `${open} sessions`, title: "Open terminal sessions on this connection" };
    return null;
  });

  // "Double-click opens" for the connection being edited.
  const connInhOpenAs = $derived(inheritedOpenAs(conn?.folder_id));
  const connKafkaOn = $derived(
    !!editing && (editing.kafka.enabled === "on" ||
      (editing.kafka.enabled === "" && kafkaInheritedFrom(conn?.folder_id)))
  );
  const vncInheritedOn = $derived(!!inheritedSetting(conn?.folder_id, "vnc_enabled").value);
  const connVncOn = $derived(
    !!editing && (editing.vncEnabled === "on" ||
      (editing.vncEnabled === "" && !!inheritedSetting(conn?.folder_id, "vnc_enabled").value))
  );
  const connDbOn = $derived(
    !!editing && (editing.db.enabled === "on" ||
      (editing.db.enabled === "" && dbInheritedFrom(conn?.folder_id, "db_enabled") === true))
  );
  const connOpenAs = $derived<OpenAs>(
    editing ? openAsOf(editing.tunnelOnly, editing.vncDefault, editing.kafka.def, editing.db.def, connInhOpenAs.mode, connVncOn, connKafkaOn, connDbOn) : ""
  );
  function setConnOpenAs(m: OpenAs) {
    if (!editing) return;
    const { kafkaDefault, dbDefault, ...flags } = openAsFlags(m);
    editing = { ...editing, ...flags, kafka: { ...editing.kafka, def: kafkaDefault }, db: { ...editing.db, def: dbDefault } };
  }
  const connInhAutostart = $derived(inheritedSetting(conn?.folder_id, "tunnel_autostart"));

  // Jump chain as cards: this computer, each hop with its connection's
  // icon, then this host. The editor opens on demand.
  let chainEditOpen = $state(false);
  type ChainHop = { label: string; title: string; conn: ReturnType<typeof tree.connectionById> | null };
  function chainHops(spec: any): ChainHop[] {
    const out: ChainHop[] = [];
    for (let cur = spec, i = 0; cur && i < 100; cur = cur.via, i++) {
      if (cur.connection_id) {
        const c = tree.connectionById(cur.connection_id) ?? null;
        out.push({ label: tree.jumpRefLabel(cur.connection_id), title: c?.hostname ?? "", conn: c });
      } else if (cur.hostname) {
        out.push({ label: cur.hostname, title: `${cur.username ? cur.username + "@" : ""}${cur.hostname}${cur.port ? ":" + cur.port : ""}`, conn: null });
      }
    }
    return out;
  }
  const connChainState = $derived.by((): { hops: ChainHop[]; note: string } => {
    if (!conn || !editing) return { hops: [], note: "" };
    const own = editing.jumpHost;
    if (own) {
      if (own.kind === "none") return { hops: [], note: "No jump host: the folder's chain is cleared for this connection." };
      const hops = chainHops((own as any).chain);
      return { hops, note: hops.length ? "Set on this connection." : "Direct, no jump host." };
    }
    const inh = tree.inheritedFieldForConnection(conn.id, "jump_host");
    const v = inh.value as any;
    if (inh.from && v?.kind === "chain") return { hops: chainHops(v.chain), note: `From ${inh.from.name}.` };
    if (inh.from && v?.kind === "none") return { hops: [], note: `Direct: ${inh.from.name} clears the chain.` };
    return { hops: [], note: "Direct, no jump host." };
  });
  // The network profile the first hop dials through, drawn ahead of the
  // chain. Mirrors the backend (resolver.ExpandJumpRefs): the connection's
  // own value, else its folders' ("" = Direct, which stops the walk -
  // inheritedFieldForConnection would skip over it), else, when none is
  // set at all, the profile of the bastion that supplies the first hop.
  function folderSetting(folderId: string | null, key: "network_profile_id" | "jump_host"): { v: any; from: string } | undefined {
    let f = tree.folderById(folderId);
    for (let i = 0; f && i < 10000; i++) {
      const v = (f.settings as any)?.[key];
      if (v !== undefined && v !== null) return { v, from: f.name };
      f = tree.folderById(f.parent_id ?? null);
    }
    return undefined;
  }
  // The network a chain's first hop adopts from the connection (or
  // inventory host) it references, recursing through that one's chain.
  function bastionNet(spec: any, depth = 0): { id: string; from: string } | undefined {
    const ref: string | undefined = spec?.connection_id;
    if (!ref || depth > 16) return undefined;
    const name = tree.jumpRefLabel(ref);
    let own: any;
    let folderId: string | null;
    if (ref.startsWith("dyn:")) {
      const rest = ref.slice(4);
      folderId = rest.slice(0, rest.indexOf("/"));
    } else {
      const c = tree.connectionById(ref);
      if (!c) return undefined;
      own = c.overrides;
      folderId = c.folder_id ?? null;
    }
    const net = own?.network_profile_id ?? folderSetting(folderId, "network_profile_id")?.v;
    if (net !== undefined && net !== null) return { id: net, from: `bastion ${name}` };
    const jh = own?.jump_host ?? folderSetting(folderId, "jump_host")?.v;
    return jh?.kind === "chain" ? bastionNet(jh.chain, depth + 1) : undefined;
  }
  const connNet = $derived.by((): { np: NetworkProfileInfo | null; id: string; from: string } | null => {
    if (!conn || !editing) return null;
    let id: string | undefined;
    let from = "";
    if (editing.networkProfile === "__direct__") return null;
    if (editing.networkProfile) id = editing.networkProfile;
    else {
      const inh = folderSetting(conn.folder_id ?? null, "network_profile_id");
      if (inh) { id = inh.v; from = inh.from; }
      else {
        const jh = editing.jumpHost ?? folderSetting(conn.folder_id ?? null, "jump_host")?.v;
        const b = jh?.kind === "chain" ? bastionNet((jh as any).chain) : undefined;
        if (b) { id = b.id; from = b.from; }
      }
    }
    if (!id) return null;
    return { np: networkProfiles.list.find((p) => p.id === id) ?? null, id, from };
  });
  let netSelectEl = $state<HTMLSelectElement | null>(null);
  function netState(np: NetworkProfileInfo): { text: string; tone: string } {
    if (np.paused) return { text: "paused", tone: "warn" };
    return np.status.running ? { text: "up", tone: "ok" } : { text: "down", tone: "off" };
  }
  const connChain = $derived(connChainState.hops);
  const connChainNote = $derived(connChainState.note);

  // The folder editor's tabs: no VNC (its settings live on connections);
  // Kafka is inherited, so a folder can set it for everything below.
  const folderTabs: EdTabDef[] = [
    { id: "general", label: "General" },
    { id: "connection", label: "Connection" },
    { id: "terminal", label: "Terminal" },
    { id: "forwards", label: "Tunnels" },
    { id: "services", label: "Services" },
    { id: "advanced", label: "Advanced" },
  ];
  const folderTab = $derived<EdTab>(folderTabs.some((t) => t.id === edTabPref) ? edTabPref : "general");
  const folderSub = $derived.by(() => {
    if (!folder) return "";
    const n = tree.connectionsIn(folder.id).length;
    const path = folderPathOf(folder.parent_id);
    const count = `${n} connection${n === 1 ? "" : "s"}`;
    return path ? `${count} - in ${path}` : count;
  });
  const folderInhOpenAs = $derived(inheritedOpenAs(folder?.parent_id));
  const folderVncOn = $derived(!!inheritedSetting(folder?.id, "vnc_enabled").value);
  const folderKafkaOn = $derived(
    !!editingFolder && (editingFolder.kafka.enabled === "on" ||
      (editingFolder.kafka.enabled === "" && kafkaInheritedFrom(folder?.parent_id)))
  );
  const folderDbOn = $derived(
    !!editingFolder && (editingFolder.db.enabled === "on" ||
      (editingFolder.db.enabled === "" && dbInheritedFrom(folder?.parent_id, "db_enabled") === true))
  );
  const folderOpenAs = $derived<OpenAs>(
    editingFolder ? openAsOf(editingFolder.tunnelOnly, editingFolder.vncDefault, editingFolder.kafka.def, editingFolder.db.def, folderInhOpenAs.mode, folderVncOn, folderKafkaOn, folderDbOn) : ""
  );
  function setFolderOpenAs(m: OpenAs) {
    if (!editingFolder) return;
    const { kafkaDefault, dbDefault, ...flags } = openAsFlags(m);
    editingFolder = { ...editingFolder, ...flags, kafka: { ...editingFolder.kafka, def: kafkaDefault }, db: { ...editingFolder.db, def: dbDefault } };
  }
  const folderInhAutostart = $derived(inheritedSetting(folder?.parent_id, "tunnel_autostart"));

  // Rare actions live behind the header's "..." button.
  // The header's Open button: the default database (or Kafka when there is
  // no database); its arrow lists every database and Kafka.
  const openItems = $derived(conn ? connectionActions.openMenuItems(conn.id) : []);
  function openDefaultService() {
    if (!conn) return;
    if (dbEnabledFor(conn)) {
      const o = conn.overrides ?? {};
      const def = (o.db_targets ?? []).some((t) => t.id === o.db_default_target) ? o.db_default_target! : "";
      connectionActions.openDb(conn.id, def);
    } else {
      connectionActions.openKafka(conn.id);
    }
  }

  function openMoreMenu(e: MouseEvent) {
    if (!conn) return;
    const items: MenuItem[] = [];
    if (!isLocal && !isWeb) {
      items.push({
        label: showOverride ? "Hide the different credential" : "Use a different credential...",
        iconComponent: IconKey,
        onSelect: () => (showOverride = !showOverride),
      });
    }
    items.push({ label: "Delete connection", iconComponent: IconTrash, danger: true, onSelect: () => deleteConn() });
    contextMenu.show(e, items);
  }

  // SSH auth credentials for the connection picker. api_token is filtered out
  // (see sshCredOptions) but a value already stored on this connection is
  // kept, so opening an existing config cannot silently drop its reference.
  const credOptions = $derived([
    { value: AUTH_NONE, label: "None - do not inherit, ask at connect" },
    ...sshCredOptions(editing?.authRef),
  ]);

  // A local-shell connection ("telnet", serial console, "claude", ...):
  // no SSH host/port/auth/jump/VNC - just a shell kind + a command. The
  // editor hides the SSH-only fields when this is true.
  const isLocal = $derived(conn?.protocol === "local");
  // A web bookmark: name, address, Network (which VPN browser opens it).
  const isWeb = $derived(conn?.protocol === "web");

  // Shell-kind options for a local connection, per platform (mirrors the
  // nav launcher's list in App.svelte). "" = auto. resolveShell validates
  // these on the backend.
  const localShellKindOptions: { value: string; label: string }[] = (() => {
    const nav =
      typeof navigator !== "undefined"
        ? (((navigator as any).userAgentData?.platform ?? navigator.platform ?? navigator.userAgent) as string).toLowerCase()
        : "";
    if (nav.includes("win")) {
      return [
        { value: "", label: "Auto (WSL if present, else PowerShell)" },
        { value: "wsl", label: "WSL" },
        { value: "powershell", label: "PowerShell" },
        { value: "cmd", label: "Command Prompt" },
      ];
    }
    if (nav.includes("mac")) {
      return [
        { value: "", label: "Auto ($SHELL, else zsh)" },
        { value: "zsh", label: "zsh" },
        { value: "bash", label: "bash" },
        { value: "sh", label: "sh" },
      ];
    }
    return [
      { value: "", label: "Auto ($SHELL, else bash)" },
      { value: "bash", label: "bash" },
      { value: "zsh", label: "zsh" },
      { value: "sh", label: "sh" },
    ];
  })();

  // Dial order, bastion-first; saved-connection hops by name.
  function jumpChainNames(spec: any): string[] {
    return jumpChainHostnames(spec);
  }

  // Reset the per-connect credential override when the selected
  // connection changes. Without this, opening "Use different
  // credential" on one connection left the panel open (and its values
  // staged) when you clicked another connection, so the override
  // silently applied to the wrong host on the next connect. Keyed on id
  // so a background tree reload (same connection) doesn't wipe an
  // override the user is mid-way through typing.
  let lastOverrideConnId: string | null = null;
  $effect(() => {
    const id = conn?.id ?? null;
    if (id !== lastOverrideConnId) {
      lastOverrideConnId = id;
      showOverride = false;
      chainEditOpen = false;
      overrideCredId = "";
      overrideUsername = "";
      overridePassword = "";
    }
  });

  $effect(() => {
    if (conn) {
      resolved = null;
      resolveErr = null;
      api.connectionsResolve(conn.id).then((r) => (resolved = r)).catch((e) => (resolveErr = String(e)));
      // A tree reload (sync, a refresh) hands in a new object for the same
      // connection: edits in progress stay. Discard bumps draftEpoch.
      const keep = conn.id === draftConnId && draftEpoch === draftConnEpoch && untrack(() => edited(editing, draftConnOrig));
      draftConnId = conn.id;
      draftConnEpoch = draftEpoch;
      const fresh = {
        name: conn.name,
        hostname: conn.hostname,
        notes: conn.notes,
        username: conn.overrides?.username ?? "",
        port: conn.overrides?.port?.toString() ?? "",
        authRef: encodeAuthRef(conn.overrides?.auth_ref),
        otpRef: encodeAuthRef(conn.overrides?.otp_ref),
        jumpHost: conn.overrides?.jump_host,
        colorTag: conn.overrides?.color_tag ?? "",
        autoReconnect: encodeBool(conn.overrides?.auto_reconnect),
        sensitive: encodeBool(conn.overrides?.sensitive),
        verbose: encodeBool(conn.overrides?.verbose),
        keepalive: conn.overrides?.keepalive_interval !== undefined
          ? String(conn.overrides.keepalive_interval)
          : "",
        vncEnabled: encodeBool(conn.overrides?.vnc_enabled),
        vncPort: conn.overrides?.vnc_port !== undefined
          ? String(conn.overrides.vnc_port)
          : "",
        vncTunnel: encodeBool(conn.overrides?.vnc_use_tunnel),
        vncDefault: encodeBool(conn.overrides?.vnc_default),
        networkProfile: encodeNetProfile(conn.overrides?.network_profile_id),
        initialCommand: conn.overrides?.initial_command ?? "",
        initialCommandDelay:
          conn.overrides?.initial_command_line_delay_ms !== undefined
            ? String(conn.overrides.initial_command_line_delay_ms)
            : "",
        tunnelOnly: encodeBool(conn.overrides?.tunnel_only),
        tunnelAutostart: encodeBool(conn.overrides?.tunnel_autostart),
        kafka: kafkaDraftFrom(conn.overrides),
        db: dbDraftFrom(conn.overrides),
        openHidden: !!conn.open_hidden,
        localShellKind: conn.local_shell_kind ?? "",
        localShellDir: conn.local_shell_dir ?? "",
        tags: [...(conn.tags ?? [])],
      };
      const prev = untrack(() => editing);
      editing = keep && prev && draftConnOrig ? mergeDraft(prev, draftConnOrig, fresh) : fresh;
      draftConnOrig = JSON.parse(JSON.stringify(fresh));
    } else {
      resolved = null;
      editing = null;
    }
  });

  // Dirty tracking: does the edit snapshot differ from the saved
  // connection? Drives the "unsaved changes" indicator on Save and the
  // warn-before-leave prompt. Compares the same fields saveConn writes.
  const dirty = $derived.by(() => {
    if (!conn || !editing) return false;
    const o = conn.overrides ?? {};
    const tagsEq =
      editing.tags.length === (conn.tags?.length ?? 0) &&
      editing.tags.every((t, i) => t === conn.tags?.[i]);
    return (
      editing.name !== conn.name ||
      editing.hostname !== conn.hostname ||
      editing.notes !== (conn.notes ?? "") ||
      editing.username !== (o.username ?? "") ||
      numText(editing.port) !== (o.port?.toString() ?? "") ||
      editing.authRef !== encodeAuthRef(o.auth_ref) ||
      editing.otpRef !== encodeAuthRef(o.otp_ref) ||
      editing.colorTag !== (o.color_tag ?? "") ||
      editing.autoReconnect !== encodeBool(o.auto_reconnect) ||
      editing.sensitive !== encodeBool(o.sensitive) ||
      editing.verbose !== encodeBool(o.verbose) ||
      numText(editing.keepalive) !== (o.keepalive_interval !== undefined ? String(o.keepalive_interval) : "") ||
      editing.vncEnabled !== encodeBool(o.vnc_enabled) ||
      numText(editing.vncPort) !== (o.vnc_port !== undefined ? String(o.vnc_port) : "") ||
      editing.vncTunnel !== encodeBool(o.vnc_use_tunnel) ||
      editing.vncDefault !== encodeBool(o.vnc_default) ||
      editing.tunnelOnly !== encodeBool(o.tunnel_only) ||
      editing.tunnelAutostart !== encodeBool(o.tunnel_autostart) ||
      !kafkaDraftSame(editing.kafka, o) ||
      !dbDraftSame(editing.db, o) ||
      editing.networkProfile !== encodeNetProfile(o.network_profile_id) ||
      editing.initialCommand !== (o.initial_command ?? "") ||
      numText(editing.initialCommandDelay) !==
        (o.initial_command_line_delay_ms !== undefined
          ? String(o.initial_command_line_delay_ms)
          : "") ||
      editing.openHidden !== !!conn.open_hidden ||
      editing.localShellKind !== (conn.local_shell_kind ?? "") ||
      editing.localShellDir !== (conn.local_shell_dir ?? "") ||
      JSON.stringify(editing.jumpHost ?? null) !== JSON.stringify(o.jump_host ?? null) ||
      !tagsEq
    );
  });

  // One save at a time, whoever calls (button, Ctrl+S, the leave prompt),
  // as the folder editor already does.
  let connSaving = false;
  async function saveConn(): Promise<boolean> {
    if (!conn || !editing || connSaving) return false;
    connSaving = true;
    try {
      return await saveConnNow();
    } finally {
      connSaving = false;
    }
  }
  async function saveConnNow(): Promise<boolean> {
    if (!conn || !editing) return false;
    const overrides = { ...conn.overrides };
    overrides.username = editing.username.trim() || undefined;
    {
      const p = numText(editing.port);
      const n = p === "" ? NaN : parseInt(p, 10);
      overrides.port = isNaN(n) ? undefined : n;
    }
    overrides.auth_ref = decodeAuthRef(editing.authRef);
    overrides.otp_ref = decodeAuthRef(editing.otpRef);
    overrides.jump_host = editing.jumpHost;
    overrides.color_tag = editing.colorTag || undefined;
    overrides.auto_reconnect = decodeBool(editing.autoReconnect);
    overrides.sensitive = decodeBool(editing.sensitive);
    overrides.verbose = decodeBool(editing.verbose);
    {
      const k = numText(editing.keepalive);
      if (k === "") {
        overrides.keepalive_interval = undefined;
      } else {
        const n = parseInt(k, 10);
        overrides.keepalive_interval = isNaN(n) ? undefined : n;
      }
    }
    overrides.vnc_enabled = decodeBool(editing.vncEnabled);
    {
      const p = numText(editing.vncPort);
      if (p === "") {
        overrides.vnc_port = undefined;
      } else {
        const n = parseInt(p, 10);
        overrides.vnc_port = isNaN(n) ? undefined : n;
      }
    }
    overrides.vnc_use_tunnel = decodeBool(editing.vncTunnel);
    overrides.vnc_default = decodeBool(editing.vncDefault);
    overrides.tunnel_only = decodeBool(editing.tunnelOnly);
    overrides.tunnel_autostart = decodeBool(editing.tunnelAutostart);
    applyKafkaDraft(overrides, editing.kafka);
    applyDbDraft(overrides, editing.db);
    overrides.network_profile_id = decodeNetProfile(editing.networkProfile);
    overrides.initial_command = editing.initialCommand.trim() || undefined;
    overrides.initial_command_line_delay_ms = parseDelayMs(editing.initialCommandDelay);
    const localKind = editing.localShellKind.trim();
    const localDir = editing.localShellDir.trim();
    return api.connectionsUpdate({
      id: conn.id,
      name: editing.name,
      hostname: editing.hostname,
      notes: editing.notes,
      overrides,
      tags: editing.tags,
      // For a local connection, persist the chosen shell kind ("" = auto
      // -> clear back to NULL). Harmless for SSH connections (they never
      // read it), but only send it when local to avoid touching the column.
      localShellKind: isLocal ? (localKind || null) : undefined,
      clearLocalShellKind: isLocal && localKind === "",
      localShellDir: isLocal ? (localDir || null) : undefined,
      clearLocalShellDir: isLocal && localDir === "",
      openHidden: editing.openHidden,
    }).then(async () => {
      await tree.load();
      if (conn) {
        resolved = await api.connectionsResolve(conn.id);
      }
      showSaved();
      return true;
    }).catch((e) => {
      toast.err(`Save failed: ${errMsg(e)}`);
      return false;
    });
  }

  // Save feedback - green pill above the form for a couple of seconds
  // after a successful save. Same hint surfaces folder + conn paths.
  let savedHint = $state<string | null>(null);
  let savedTimer: ReturnType<typeof setTimeout> | null = null;
  function showSaved(msg = "Saved") {
    savedHint = msg;
    if (savedTimer) clearTimeout(savedTimer);
    savedTimer = setTimeout(() => { savedHint = null; }, 1800);
    // Mirror to the global toast so the user sees confirmation even
    // when the inline pill is scrolled off-screen (typical after
    // Ctrl+S triggered far from the Save button).
    toast.ok(msg);
  }

  // Ctrl/Cmd+S anywhere in the DetailPane saves the active editor.
  // Used by power users; the explicit Save button stays in place.
  // We listen on window AND on the section so the shortcut works
  // even when focus lands inside a child portal-y element (the
  // SearchableSelect dropdown and the JumpChainEditor's internal
  // inputs were eating the section-scoped handler - focus was on
  // the child input, the section never saw the keydown bubble).
  function onPaneKey(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
      // Only intercept when the focused element is somewhere inside
      // this pane; otherwise other views (Settings, Credentials)
      // would lose their own Ctrl+S if they had one.
      if (!isFocusInsideDetail()) return;
      e.preventDefault();
      if (editing && conn) saveConn();
      else if (editingFolder && folder) saveFolder();
    }
  }

  function isFocusInsideDetail(): boolean {
    const sec = document.querySelector("section.detail");
    if (!sec) return false;
    const ae = document.activeElement;
    if (!ae) return false;
    return sec.contains(ae) || ae === document.body;
  }

  $effect(() => {
    if (!conn && !folder) return;
    window.addEventListener("keydown", onPaneKey);
    return () => window.removeEventListener("keydown", onPaneKey);
  });

  // Delete confirm modal state - used by every delete path (single
  // folder/conn, multi-select). The pending action runs on confirm.
  let deleteItems = $state<Array<{ kind: "folder" | "connection"; name: string; detail?: string; warn?: string }>>([]);
  let deletePending: (() => Promise<void>) | null = null;

  // Walk a folder subtree to collect the names of everything that
  // cascades. The list shown to the user mirrors what the SQLite FK
  // ON DELETE CASCADE will actually nuke.
  function collectFolderVictims(
    folderId: string,
    out: Array<{ kind: "folder" | "connection"; name: string; detail?: string; warn?: string }>
  ) {
    const f = tree.folderById(folderId);
    if (!f) return;
    const childFolders = tree.childrenOf(folderId);
    const childConns = tree.connectionsIn(folderId);
    out.push({
      kind: "folder",
      name: f.name,
      detail: childFolders.length || childConns.length
        ? `${childFolders.length + childConns.length} item${childFolders.length + childConns.length === 1 ? "" : "s"} inside`
        : undefined,
    });
    for (const c of childConns) {
      out.push({ kind: "connection", name: c.name, detail: c.hostname, warn: tree.deleteWarn(c) });
    }
    for (const sub of childFolders) {
      collectFolderVictims(sub.id, out);
    }
  }

  function openFolderDelete(selected: string[]) {
    // A subfolder of another selected folder goes with its parent (the
    // DB cascades); deleting it again would only report "not found".
    const chosen = new Set(selected);
    const folderIds = selected.filter((id) => {
      for (let p = tree.folderById(id)?.parent_id ?? null; p; p = tree.folderById(p)?.parent_id ?? null) {
        if (chosen.has(p)) return false;
      }
      return true;
    });
    const items: typeof deleteItems = [];
    for (const id of folderIds) collectFolderVictims(id, items);
    deleteItems = items;
    deletePending = async () => {
      // Top-level folders only - children are cascade-deleted by the DB.
      // What went (cascades included) leaves the selection after the
      // reload, without a prompt; a folder that failed keeps it.
      const gone = await deleteEach(folderIds, (id) => api.foldersDelete(id));
      await tree.load();
      selection.dropMissing(gone);
    };
  }

  // Deletes one by one, carries on past a failure (reported in a toast),
  // returns the ids that went.
  async function deleteEach(ids: string[], del: (id: string) => Promise<unknown>): Promise<string[]> {
    const gone: string[] = [];
    let first = "";
    for (const id of ids) {
      try { await del(id); gone.push(id); } catch (e) { first ||= errMsg(e); }
    }
    const failed = ids.length - gone.length;
    if (failed) toast.err(failed === ids.length ? `Delete failed: ${first}` : `${failed} of ${ids.length} not deleted: ${first}`);
    return gone;
  }

  function openConnDelete(connIds: string[]) {
    const items: typeof deleteItems = [];
    for (const id of connIds) {
      const c = tree.connectionById(id);
      if (c) items.push({ kind: "connection", name: c.name, detail: c.hostname, warn: tree.deleteWarn(c) });
    }
    deleteItems = items;
    deletePending = async () => {
      const gone = await deleteEach(connIds, (id) => api.connectionsDelete(id));
      await tree.load();
      selection.dropMissing(gone);
    };
  }

  async function confirmDelete() {
    const fn = deletePending;
    deletePending = null;
    deleteItems = [];
    if (fn) await fn();
  }
  function cancelDelete() {
    deletePending = null;
    deleteItems = [];
  }

  function deleteFolder() {
    if (!folder) return;
    openFolderDelete([folder.id]);
  }
  function deleteConn() {
    if (!conn) return;
    openConnDelete([conn.id]);
  }

  // ----- Per-connection password -----

  let passwordHasValue = $state(false);
  // A saved password alongside key-based auth is offered to the server as a
  // FALLBACK: x/crypto/ssh tries the key first, and if the server rejects it,
  // sends the password. A malicious or impersonated host can therefore fail
  // the pubkey step on purpose and harvest the password. This cannot be gated
  // when the config is built (the fallback is what makes the password useful
  // on hosts where the key is not installed), so the user is warned instead.
  // The credential that will actually authenticate this connection, whether
  // set here or inherited from a folder. Inheritance matters: a connection
  // with no auth_ref of its own still authenticates with the folder's key,
  // and carries exactly the same risk.
  const effectiveAuthCred = $derived.by(() => {
    if (editing?.authRef === AUTH_NONE) return null;
    const inherited = conn
      ? tree.inheritedFieldForConnection(conn.id, "auth_ref")
      : { value: undefined, from: null };
    const ref = effectiveAuthRef(
      editing?.authRef,
      inherited.from ? (inherited.value as string | undefined) : null,
    );
    return ref ? credentials.byId(ref) : null;
  });

  const keyAuthCredKind = $derived(
    isKeyAuthKind(effectiveAuthCred?.kind) ? effectiveAuthCred!.kind : null,
  );

  let passwordInput = $state("");
  let passwordSaving = $state(false);

  $effect(() => {
    if (conn) {
      api.getConnectionHasPassword(conn.id).then((v) => (passwordHasValue = v));
      passwordInput = "";
    } else {
      passwordHasValue = false;
      passwordInput = "";
    }
  });

  async function savePassword() {
    if (!conn || !passwordInput) return;
    passwordSaving = true;
    try {
      await api.setConnectionPassword(conn.id, passwordInput);
      passwordHasValue = true;
      passwordInput = "";
    } finally {
      passwordSaving = false;
    }
  }

  async function clearPassword() {
    if (!conn) return;
    await api.clearConnectionPassword(conn.id);
    passwordHasValue = false;
  }

  // ----- Per-connection VNC password (RFB auth) -----

  let vncPasswordHasValue = $state(false);
  let vncPasswordInput = $state("");
  let vncPasswordSaving = $state(false);

  $effect(() => {
    // VNC is desktop-only (the backend IPC is excluded on mobile), so skip
    // the has-password probe there - it would reject on a missing binding.
    if (conn && !isMobile) {
      api.getConnectionHasVncPassword(conn.id).then((v) => (vncPasswordHasValue = v));
      vncPasswordInput = "";
    } else {
      vncPasswordHasValue = false;
      vncPasswordInput = "";
    }
  });

  async function saveVncPassword() {
    if (!conn || !vncPasswordInput) return;
    vncPasswordSaving = true;
    try {
      await api.setConnectionVncPassword(conn.id, vncPasswordInput);
      vncPasswordHasValue = true;
      vncPasswordInput = "";
    } finally {
      vncPasswordSaving = false;
    }
  }

  async function clearVncPassword() {
    if (!conn) return;
    await api.clearConnectionVncPassword(conn.id);
    vncPasswordHasValue = false;
  }

  function openVncConsole() {
    if (conn) connectionActions.openVncConnection(conn.id);
  }

  // In flight from anywhere: this pane's button, a tree double-click,
  // Enter, a multi-select connect. The store tracks every attempt with the
  // stage the backend last reported ("" = no stage yet).
  let connectingHere = $state(false);
  const storeStage = $derived(conn ? connectionActions.connectStage[conn.id] : undefined);
  const connecting = $derived(connectingHere || storeStage !== undefined);
  const connectStage = $derived(storeStage || null);
  let copiedHint = $state<string | null>(null);
  let connectErrEl = $state<HTMLElement | null>(null);

  // Per-attempt override knobs. All in-memory; reset after each
  // Connect press so an override never carries over without the
  // user being able to see it. Empty values fall through to
  // normal resolution.
  let overrideCredId = $state<string>("");
  let overrideUsername = $state<string>("");
  let overridePassword = $state<string>("");
  let showOverride = $state(false);

  // Notes Edit/Preview toggle. Default to edit so a fresh selection
  // doesn't render an empty preview block; preview is opt-in.
  let notesMode = $state<"edit" | "preview">("edit");
  const renderedNotes = $derived(renderMarkdown(editing?.notes ?? ""));

  // When the user clicks a markdown link in preview mode, intercept
  // the navigation and route to the system browser instead of letting
  // the webview try to load the URL inside the app.
  function onNotesPreviewClick(e: MouseEvent) {
    const t = e.target as HTMLElement | null;
    if (!t) return;
    const a = t.closest("a[data-md-link]") as HTMLAnchorElement | null;
    if (!a) return;
    e.preventDefault();
    const href = a.getAttribute("href");
    if (href) api.openURL(href);
  }

  // Switching to a different connection resets the preview toggle so
  // we don't carry over preview mode from the previous selection.
  $effect(() => {
    void conn?.id;
    notesMode = "edit";
  });

  async function toggleFavorite() {
    if (!conn) return;
    await api.connectionsUpdate({ id: conn.id, favorite: !conn.favorite });
    await tree.load();
  }

  async function copySystemSshCommand() {
    if (!conn) return;
    try {
      const cmd = await api.sshSystemCommand(conn.id);
      await writeClipboard(cmd);
      copiedHint = "Copied: " + cmd;
      setTimeout(() => { copiedHint = null; }, 3500);
    } catch (e: any) {
      copiedHint = "Error: " + errMsg(e);
      setTimeout(() => { copiedHint = null; }, 4000);
    }
  }

  async function launchInSystemTerminal() {
    if (!conn) return;
    try {
      await api.sshLaunchInSystemTerminal(conn.id);
      copiedHint = "Launched system terminal";
      setTimeout(() => { copiedHint = null; }, 3000);
    } catch (e: any) {
      copiedHint = "Launch failed: " + errMsg(e);
      setTimeout(() => { copiedHint = null; }, 5000);
    }
  }

  // Quick-copy fields. Mirrors PaneNode's per-pane copy buttons but
  // works straight off the connection record - no live session
  // required (useful for sudo password copy from the editor pane).
  async function copyField(field: "username" | "hostname" | "password") {
    if (!conn) return;
    try {
      if (field === "password") {
        const pw = await api.connectionRevealPassword(conn.id);
        await writeClipboard(pw);
        copiedHint = "Password copied (clears in 30s)";
        // Self-clearing clipboard. Schedule a wipe; if the user copied
        // something else in between, leave it alone.
        setTimeout(async () => {
          try {
            const cur = await readClipboard();
            if (cur === pw) await writeClipboard("");
          } catch { /* best-effort */ }
        }, 30_000);
      } else {
        const info = await api.connectionCopyInfo(conn.id);
        const val = field === "username" ? info.username : info.hostname;
        if (!val) {
          copiedHint = `${field === "username" ? "Username" : "Host"}: (empty)`;
        } else {
          await writeClipboard(val);
          copiedHint = `${field === "username" ? "Username" : "Host"} copied`;
        }
      }
      setTimeout(() => { copiedHint = null; }, 3000);
    } catch (e: any) {
      // Reveal explains a missing password in a full sentence (key auth,
      // agent, opkssh); prefixing "Error:" both stutters and overstates it -
      // a key-auth host having no password is normal, not a failure.
      // errMsg, not e.message: Wails wraps the Go error in a JSON envelope
      // and e.message IS that envelope, so the raw JSON reached the hint.
      copiedHint = errMsg(e);
      setTimeout(() => { copiedHint = null; }, 5000);
    }
  }

  // The currently selected connection's failure record (if any).
  // Populated by connectionActions.connectOne from any callsite
  // (the editor's Connect button, double-click in the tree, Enter,
  // Connect-all in BatchPanel, …). Reactive so a fresh failure for
  // the same conn updates instantly. Keyed by conn.id, so selecting
  // another connection naturally drops the previous error.
  const lastError = $derived(conn ? connectionActions.lastConnectError[conn.id] : undefined);
  const connectErr = $derived(lastError?.message ?? null);
  const connectDebugLines = $derived<string[]>(lastError?.debug ?? []);

  // Scroll the error banner into view when a new failure shows up.
  // Keyed on conn?.id + lastError so it fires on a fresh failure for
  // the current connection, but not just on selection change.
  $effect(() => {
    const _trigger = connectErr;
    if (_trigger && connectErrEl) {
      connectErrEl.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
  });

  // Bulk-connect to every selected dynamic entry. Each call is
  // its own sshConnectDynamic - fire them in parallel so a slow
  // bastion in one hop doesn't gate the others. Each successful
  // entry pushes its own terminal tab; failures land in the
  // existing per-connection error store via recordFailure-equivalent.
  async function connectDynamicMany() {
    const sel = selection.selectedDynamicEntries();
    if (sel.length === 0) return;
    // Delegate to the shared action so dynamic hosts get the same
    // network-profile take-over prompt as everything else (their first
    // hop routes through the folder's profile too).
    await connectionActions.connectDynamicMany(sel);
  }

  // Batch-exec a command across selected dynamic entries. We reuse
  // the existing BatchExecModal which expects connection-id-shaped
  // entries; dynamic ones get "dyn:<entryId>" synthetic ids that
  // the backend accepts.
  let batchExecOpen = $state(false);
  let batchExecHosts = $state<Array<{ connection_id: string; name: string; hostname: string }>>([]);
  function batchExecDynamic() {
    const sel = selection.selectedDynamicEntries();
    if (sel.length === 0) return;
    batchExecHosts = sel.map(({ folderId, entryId }) => {
      const entry = (tree.dynamicEntries[folderId] ?? []).find((e) => e.id === entryId);
      return {
        connection_id: "dyn:" + entryId,
        name: entry?.name ?? entryId,
        hostname: entry?.hostname ?? "",
      };
    });
    batchExecOpen = true;
  }

  async function cancelConnect() {
    if (!conn) return;
    // Abort the in-flight connect on the backend (e.g. hung on opkssh
    // OIDC login). The awaited connect() then rejects and its finally
    // clears `connecting`; we don't flip it here to avoid a double-path.
    // Still queued behind a bulk connect: nothing reached the backend yet.
    if (connectionActions.cancelQueued(conn.id)) return;
    try { await api.sshCancelConnect(conn.id); } catch (e) { console.warn(e); }
  }

  // Show a connect failure as a toast (in addition to the inline banner at
  // the top of the form), so it's visible even when the user has scrolled the
  // form down. Accepts a raw error or an already-explained message string.
  function notifyConnectFailure(err: unknown) {
    const msg = typeof err === "string" ? err : errMsg(err);
    const name = conn?.name ? `${conn.name}: ` : "";
    toast.err(`${name}${msg}`);
  }

  async function connect() {
    if (!conn || connecting) return;
    connectingHere = true;
    const cid = overrideCredId;
    const ouser = overrideUsername;
    const opass = overridePassword;
    // Reset before the await so the picker visually clears even
    // if the connect takes a while.
    overrideCredId = "";
    overrideUsername = "";
    overridePassword = "";
    try {
      const hasAdvanced = cid || ouser || opass;
      if (hasAdvanced) {
        // Inline-equivalent of connectOne but using the advanced
        // IPC so all three knobs ride through. Failure flow
        // mirrors connectOne so the inline-error block keeps
        // working.
        const untrack = connectionActions.trackConnect(conn.id);
        try {
          const r = await api.sshConnectAdvanced(conn.id, cid, ouser, opass);
          sessions.add({
            sessionId: r.session_id,
            connectionId: conn.id,
            name: conn.name,
            hostname: conn.hostname ?? "",
            status: "connected",
          });
          paneTabs.addTab(r.session_id, conn.name);
          view.setTab("terminal");
          connectionActions.clearConnectError(conn.id);
        } catch (e) {
          connectionActions.recordConnectError(conn.id, e);
          notifyConnectFailure(e);
        } finally {
          untrack();
        }
      } else {
        // connectOne swallows the error (records the inline banner, returns
        // false). Surface a toast on failure too, so it's visible when the
        // user has scrolled the form down past the top-of-form banner.
        const ok = await connectionActions.connectOne(conn.id);
        if (!ok && connectErr) notifyConnectFailure(connectErr);
      }
      await tree.load();
    } finally {
      connectingHere = false;
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<!-- Ctrl+S is handled by the window listener in $effect; binding it
     here too made the keydown fire onPaneKey twice (section + window),
     double-saving and double-toasting. -->
<section class="detail">
  {#if folderMultiCount > 1}
    <header>
      <h1>{folderMultiCount} folders selected</h1>
      <div class="head-actions">
        <button class="danger" onclick={() => openFolderDelete(selection.selectedFolderIds())}>
          Delete {folderMultiCount} folders
        </button>
        <button onclick={() => selection.select({ kind: "none" })}>Clear</button>
      </div>
    </header>
    <p class="hint">
      Folder multi-select. Deleting removes each folder and every
      connection or subfolder inside it (cascade). Other batch ops on
      folders aren't wired yet.
    </p>
  {:else if multiCount > 1}
    <BatchPanel onDelete={(ids) => openConnDelete(ids)} />
  {:else if dynamicMulti.length > 1}
    <header>
      <h1>{dynamicMulti.length} dynamic entries selected</h1>
      <div class="head-actions">
        <button class="primary" onclick={() => connectDynamicMany()}>
          Connect all
        </button>
        <button onclick={() => batchExecDynamic()}>Batch exec…</button>
        <button onclick={() => selection.select({ kind: "none" })}>Clear</button>
      </div>
    </header>
    <FleetBar ids={selection.fleetHostIds()} label={selection.fleetHostIds().length === dynamicMulti.length ? `${dynamicMulti.length} dynamic entries` : `${selection.fleetHostIds().length} selected hosts`} />
    <p class="hint">
      Ctrl-click adds / removes; Shift-click selects a range. Connect
      all opens N tabs in parallel. Batch exec runs one command
      across every selection and shows per-host stdout / exit.
    </p>
  {:else if selection.current.kind === "dynamicEntry"}
    <DynamicEntryDetail
      folderId={selection.current.folderId}
      entryId={selection.current.entryId}
    />
  {:else if !folder && !conn}
    <HomePane />
  {:else if folder && editingFolder}
    <header class="ed-head">
      <div class="ed-title">
        <h1><IconFolder size={18} /> <input class="title-input" bind:value={editingFolder.name} /></h1>
        {#if folderSub}<span class="ed-sub">{folderSub}</span>{/if}
      </div>
      <div class="head-actions">
        {#if folderDirty}
          <button onclick={discardDraft} title="Throw the unsaved changes away">Discard</button>
        {/if}
        <button
          class="save-btn"
          class:dirty={folderDirty}
          class:just-saved={savedHint && !folderDirty}
          disabled={!folderDirty}
          title={folderDirty ? "Save unsaved changes" : "No unsaved changes"}
          onclick={saveFolder}
        >
          {#if savedHint && !folderDirty}✓ Saved{:else if folderDirty}Save *{:else}Save{/if}
        </button>
        <button onclick={addSubfolder} title="New subfolder">+ Folder</button>
        <button onclick={addConnectionHere} title="New connection here">+ Connection</button>
        <button class="danger" onclick={deleteFolder}>Delete</button>
      </div>
      <div class="ed-tabs" role="tablist" aria-label="Folder settings">
        {#each folderTabs as t (t.id)}
          <button role="tab" aria-selected={folderTab === t.id} class:active={folderTab === t.id} onclick={() => setEdTab(t.id)}>{t.label}</button>
        {/each}
      </div>
    </header>

    <FleetBar ids={folderFleetIds} label={folder.name} folderId={folder.id} />
    {#if folderTab === "general"}
    {@const inhSens = tree.inheritedFieldForFolder(folder.id, "sensitive")}
    <FleetCards folderId={folder.id} ids={folderFleetIds} label={folder.name} />
    <div class="form">
      <p class="section-label span-2">Inherited by the connections and subfolders in this folder unless they set their own value.</p>
      <label title="What a double-click or Enter does on the connections in this folder.">
        Double-click opens
        <select value={folderOpenAs} onchange={(e) => setFolderOpenAs(e.currentTarget.value as OpenAs)}>
          <option value="">(inherit: {openAsLabel(folderInhOpenAs.mode)}{folderInhOpenAs.from ? `, from ${folderInhOpenAs.from}` : ""})</option>
          <option value="terminal">Terminal</option>
          <option value="tunnel">Tunnel - no shell, only the forwards</option>
          {#each openAsServices({ vnc: folderVncOn, kafka: folderKafkaOn, db: folderDbOn }, "folder") as o (o.value)}
            <option value={o.value} disabled={!o.on}>{o.label}{o.on ? "" : ` (${o.off})`}</option>
          {/each}
        </select>
      </label>
      <label title="Production or otherwise critical. The LLM (MCP) cannot list, open, change or route through it; a Database tab asks before statements that change data; Kafka asks for the name before a delete. Covers every connection and subfolder here; one can opt out.">
        Sensitive
        <select bind:value={editingFolder.sensitive}>
          <option value="">(inherit: {inhSens.value ? "yes" : "no"}{inhSens.from ? `, from ${inhSens.from.name}` : ""})</option>
          <option value="on">Yes - off limits to the LLM, writes and deletes confirmed</option>
          <option value="off">No</option>
        </select>
      </label>
      <div class="row">
        <label class="grow">Default username
          <input bind:value={editingFolder.username} placeholder="(not set)" />
        </label>
        <label class="port">Default port
          <input type="number" bind:value={editingFolder.port} min="1" max="65535" placeholder="22" />
        </label>
      </div>
      <label>Credential
        <div class="cred-picker-row">
          <SearchableSelect
            bind:value={editingFolder.authRef}
            options={folderCredOptions}
            placeholder="Search credentials…"
          />
          {#if hasKeepass}
            <button type="button" class="kp-btn" onclick={() => openKeepassPicker("folder")}
              title="Pick a secret straight from a KeePass database">
              From KeePass
            </button>
          {/if}
          {#if hasBitwarden}
            <button type="button" class="kp-btn" onclick={() => openBitwardenPicker("folder")}
              title="Pick a secret straight from a Bitwarden server">
              From Bitwarden
            </button>
          {/if}
          {#if hasInfisical}
            <button type="button" class="kp-btn" onclick={() => openInfisicalPicker("folder")}
              title="Pick a secret straight from an Infisical server">
              From Infisical
            </button>
          {/if}
        </div>
        {#if editingFolder.authRef && keepassCredIds.has(editingFolder.authRef)}
          <span class="kp-badge">KeePass-backed - secret read from the .kdbx at connect</span>
        {/if}
        {#if editingFolder.authRef && bitwardenCredIds.has(editingFolder.authRef)}
          <span class="kp-badge">Bitwarden-backed - secret read from the server at connect</span>
        {/if}
        {#if editingFolder.authRef && infisicalCredIds.has(editingFolder.authRef)}
          <span class="kp-badge">Infisical-backed - secret read from the server at connect</span>
        {/if}
      </label>
      {#if otpCredOptions.length > 0 || editingFolder.otpRef || hasKeepass || hasBitwarden}
        <label>One-time code from
          <div class="cred-picker-row">
            <SearchableSelect
              bind:value={editingFolder.otpRef}
              options={otpCredOptions}
              placeholder="(none)"
            />
            {#if hasKeepass}
              <button type="button" class="kp-btn" onclick={() => openKeepassPicker("folder-otp")}
                title="Use the TOTP of a KeePass entry">From KeePass</button>
            {/if}
            {#if hasBitwarden}
              <button type="button" class="kp-btn" onclick={() => openBitwardenPicker("folder-otp")}
                title="Use the TOTP of a Bitwarden item">From Bitwarden</button>
            {/if}
          </div>
          <span class="inh-hint">Answers the server's verification-code question from this item's TOTP, next to the credential above.</span>
        </label>
      {/if}
      <div class="span-2"><ColorPicker
        value={editingFolder.colorTag}
        onChange={(v) => { if (editingFolder) editingFolder = { ...editingFolder, colorTag: v }; }}
        label="Color tag (inherited by children)"
      /></div>
      {#if folder}
        <div class="span-2"><IconPicker
          kind="folder"
          targetId={folder.id}
          currentIconId={folder.icon_image_id}
          currentIconName={folder.icon_name}
          currentIconColor={folder.icon_color}
          fallbackEmoji=""
          onChange={() => tree.load()}
          onNamedChange={() => tree.load()}
        /></div>
      {/if}
      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
    {:else if folderTab === "connection"}
    <div class="form">
      <p class="section-label span-2">Inherited by the connections and subfolders in this folder unless they set their own value.</p>
      <div class="span-2"><JumpChainEditor
        value={editingFolder.jumpHost}
        onChange={(v) => { if (editingFolder) editingFolder = { ...editingFolder, jumpHost: v }; }}
      /></div>
      <label title="Route the first SSH hop of every connection in this folder through a userspace VPN tunnel (WireGuard, NetBird, Tailscale, OpenConnect, OpenVPN or Check Point).">
        Network (inherited by children)
        <select bind:value={editingFolder.networkProfile}>
          <option value="">(inherit from parent)</option>
          <option value="__direct__">Direct - no tunnel</option>
          {#each networkProfiles.list as np (np.id)}
            <option value={np.id}>via {np.name} ({profileKindTitle(np)})</option>
          {/each}
        </select>
        <span class="field-note">
          {networkProfiles.list.length === 0 ? "No network profiles yet - " : "Network profiles live in "}
          <button class="linklike" onclick={(e) => { e.preventDefault(); view.setTabSettingsSection("network"); }}>
            Settings -&gt; Network profiles</button>.
        </span>
      </label>
      <label>Auto-reconnect
        <select bind:value={editingFolder.autoReconnect}>
          <option value="">(inherit from parent)</option>
          <option value="on">On - retry with backoff after drop</option>
          <option value="off">Off - drop = closed</option>
        </select>
      </label>
      <label title="Anti-idle: send an SSH keepalive every N seconds so a bastion or firewall doesn't drop connections under this folder when they're quiet. Inherited by connections; blank inherits the parent folder, 0 sends nothing. A dead connection is still detected either way - with 0 it just takes up to a minute longer.">
        Keepalive / anti-idle (s)
        <input
          type="number"
          min="0"
          max="3600"
          step="5"
          bind:value={editingFolder.keepalive}
          placeholder="(inherit · 0 = off)"
        />
        <span class="field-note">Stops idle drops. Inherited by connections. Blank = inherit, 0 = send nothing (a dead link is still detected, just slower).</span>
      </label>
      <label title="Show a reachability dot (TCP connect to the SSH port) on connections in this folder while it is expanded. Needs the global liveness probe enabled in Settings.">
        Liveness probe
        <select bind:value={editingFolder.probeLiveness}>
          <option value="">(inherit from parent)</option>
          <option value="on">On - probe hosts in this folder</option>
          <option value="off">Off - never probe this folder</option>
        </select>
      </label>
      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
    {:else if folderTab === "terminal"}
    <div class="form">
      <p class="section-label span-2">Inherited by the connections and subfolders in this folder unless they set their own value.</p>
      <label class="span-2" title="A command run in the shell right after connect - e.g. cd /var/www, tmux new -A -s main, source venv/bin/activate. Inherited by connections; blank inherits the parent folder.">
        Initial command
        <textarea
          class="cmd-input"
          rows="2"
          bind:value={editingFolder.initialCommand}
          placeholder="(inherit) e.g. cd /var/www"
        ></textarea>
        <span class="field-note">Run in the shell on connect. Inherited by connections. Blank = inherit. One command per line.</span>
      </label>
      <label title="Wait this long between the lines of a multi-line command. Needed when a line replaces the shell that reads the next one.">
        Delay between lines (ms)
        <input
          type="number"
          min="0"
          max="60000"
          bind:value={editingFolder.initialCommandDelay}
          placeholder="(inherit)"
        />
        <span class="field-note">Blank = inherit, 0 = no pause. Raise it when a line starts a new shell (<code>sudo su - user</code>) and the lines after it get swallowed.</span>
      </label>
      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
    {:else if folderTab === "forwards"}
    <div class="form">
      <p class="section-label span-2">Inherited by the connections and subfolders in this folder unless they set their own value.</p>
      {#if folderTunnelOnly}
      <label title="Start these tunnels when the app starts (once the vault is open). Only applies to tunnel-only connections.">Start with the app
        <select bind:value={editingFolder.tunnelAutostart}>
          <option value="">(inherit: {folderInhAutostart.value ? "on" : "off"}{folderInhAutostart.from ? `, from ${folderInhAutostart.from}` : ""})</option>
          <option value="on">On</option>
          <option value="off">Off</option>
        </select>
      </label>
        <p class="span-2 field-note">Connections here open as tunnels: no shell and no tab, and starting one brings up every forward it has. Their forwards are set on each connection.</p>
      {:else}
        <p class="span-2 field-note">Connections here open as terminals. Pick <strong>Tunnel</strong> under General -&gt; Double-click opens to run them as tunnels.</p>
      {/if}
      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
    {:else if folderTab === "services"}
    <div class="form">
      <p class="section-label span-2">Inherited by the connections and subfolders in this folder unless they set their own value.</p>
      <DbSettingsEditor
        bind:draft={editingFolder.db}
        inheritLabel="(inherit from parent)"
        inheritedOn={dbInheritedFrom(folder?.parent_id, "db_enabled") === true}
        savedEnabled={encodeBool(folder?.settings.db_enabled)}
        effectiveOn={editingFolder.db.enabled === "on" || (editingFolder.db.enabled === "" && dbInheritedFrom(folder?.parent_id, "db_enabled") === true)}
        effectiveEngine={dbInheritedFrom(folder?.parent_id, "db_engine") ?? ""}
        inheritedReadOnly={dbInheritedFrom(folder?.parent_id, "db_read_only") === true}
      />
      <KafkaSettingsEditor
        bind:draft={editingFolder.kafka}
        inheritLabel="(inherit from parent)"
        inheritedOn={kafkaInheritedFrom(folder?.parent_id)}
        savedEnabled={encodeBool(folder?.settings.kafka_enabled)}
        effectiveOn={editingFolder.kafka.enabled === "on" || (editingFolder.kafka.enabled === "" && kafkaInheritedFrom(folder?.parent_id))}
        inheritedReadOnly={dbInheritedFrom(folder?.parent_id, "kafka_read_only") === true}
      />
      <p class="span-2 field-note">To open the database or Kafka on a double-click instead of a terminal, pick it under General -&gt; Double-click opens.</p>
      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
    {:else if folderTab === "advanced"}
    <div class="form">
      <p class="section-label span-2">Inherited by the connections and subfolders in this folder unless they set their own value.</p>
      <label>Verbose connect log
        <select bind:value={editingFolder.verbose}>
          <option value="">(inherit from parent)</option>
          <option value="on">On - show TCP / handshake / auth diagnostics</option>
          <option value="off">Off</option>
        </select>
      </label>
      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
    {/if}
  {:else if conn && editing}
    <header class="ed-head">
      <div class="ed-title">
        <h1>
          <button
            class="fav-toggle"
            class:active={conn.favorite}
            title={conn.favorite ? "Remove from favourites" : "Mark as favourite"}
            onclick={toggleFavorite}
          ><IconStar size={16} fill={conn.favorite ? "currentColor" : "none"} /></button>
          <Icon imageId={conn.icon_image_id} iconName={conn.icon_name} iconColor={conn.icon_color} size={18}>
            {#if isLocal}<IconTerminal size={18} />{:else if isWeb}<IconGlobe size={18} />{:else}<IconHost size={18} />{/if}
          </Icon>
          <span class="ed-name">{conn.name}</span>
          {#if headPill}
            <span class="ed-pill {headPill.tone}" title={headPill.title}><span class="ed-dot"></span>{headPill.text}</span>
          {/if}
        </h1>
        {#if headSub}<span class="ed-sub" title={headSub}>{headSub}</span>{/if}
      </div>
      <div class="head-actions">
        {#if dirty}
          <button onclick={discardDraft} title="Throw the unsaved changes away">Discard</button>
        {/if}
        <button
          class="save-btn"
          class:dirty
          class:just-saved={savedHint && !dirty}
          disabled={!dirty}
          title={dirty ? "Save unsaved changes" : "No unsaved changes"}
          onclick={saveConn}
        >
          {#if savedHint && !dirty}✓ Saved{:else if dirty}Save *{:else}Save{/if}
        </button>
        {#if isWeb}
          <button class="primary" onclick={() => connectionActions.openWeb(conn!.id)} title="Open in the browser of the folder's network profile (its VPN), else your normal browser">Open</button>
        {:else if connTunnel && !overrideCredId}
          <!-- A tunnel connection: the header offers the tunnel first, a
               terminal second. A credential override stays a terminal
               connect (the branch below). -->
          {#if tunnels.running(connTunnel)}
            <button class="primary" onclick={() => tunnels.stop(conn!.id)}
              title="Stop this connection's tunnel ({connTunnel.state})">Stop tunnel</button>
          {:else}
            <button class="primary" onclick={() => tunnels.start(conn!.id)}
              title={connTunnel.state === "error" ? "Try the tunnel again" : "Start this connection's tunnel: no shell, no tab, just its port forwards"}>
              {connTunnel.state === "error" ? "Try again" : "Start tunnel"}
            </button>
          {/if}
          <button class="connect-btn" disabled={connecting} onclick={connect} title={connecting ? (connectStage ?? "Connecting...") : "Open a terminal on this host, next to the tunnel"}>
            {connecting ? (connectStage ?? "Connecting…") : "Terminal"}
          </button>
        {:else}
        <button class="primary connect-btn" disabled={connecting} onclick={connect} title={connecting ? (connectStage ?? "Connecting...") : undefined}>
          {connecting ? (connectStage ?? "Connecting…") : (overrideCredId ? "Connect (override)" : "Connect")}
        </button>
        {/if}
        {#if conn.overrides?.vnc_enabled}
          <button class="vnc-btn" title="Open the VNC console for this host" onclick={openVncConsole}>
            <IconMonitor size={13} /> VNC
          </button>
        {/if}
        {#if openItems.length > 0}
          <span class="open-split">
            <button class="vnc-btn" title={dbEnabledFor(conn) ? "Open the default database of this connection" : "Open the Kafka tab of this connection"} onclick={openDefaultService}>
              {#if dbEnabledFor(conn)}<IconDb size={13} />{:else}<IconKafka size={13} />{/if} Open
            </button>
            {#if openItems.length > 1}
              <button class="vnc-btn open-more" aria-label="Open a service" aria-haspopup="menu" title="Every database and Kafka of this connection" onclick={(e) => contextMenu.show(e, openItems)}>
                <IconChevronDown size={13} />
              </button>
            {/if}
          </span>
        {/if}
        {#if connecting}
          <!-- Abort a connect stuck on opkssh OIDC login (closed browser /
               wrong config) without restarting the app. -->
          <button class="ghost cancel-connect" title="Cancel the connection attempt" onclick={cancelConnect}>
            Cancel
          </button>
        {/if}
        {#if showOverride}
          <button class="ghost" title="Close the different-credential panel" onclick={() => (showOverride = false)}>✕ Credential</button>
        {/if}
        <button class="ghost more-btn" aria-label="More actions" title="More actions" onclick={openMoreMenu}><svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.8"></circle><circle cx="12" cy="12" r="1.8"></circle><circle cx="19" cy="12" r="1.8"></circle></svg></button>
      </div>
      {#if edTabs.length > 1}
        <div class="ed-tabs" role="tablist" aria-label="Connection settings">
          {#each edTabs as t (t.id)}
            <button role="tab" aria-selected={edTab === t.id} class:active={edTab === t.id} onclick={() => setEdTab(t.id)}>
              {t.label}{#if t.warn}<span class="ed-warn" title={t.warn}>!</span>{/if}
            </button>
          {/each}
        </div>
      {/if}
    </header>
    {#if showOverride}
      <div class="cred-override">
        <label class="cred-row">
          <span>Credential</span>
          <select bind:value={overrideCredId}>
            <option value="">(use the connection's credential)</option>
            {#each credentials.list as c (c.id)}
              <option value={c.id}>{c.name} ({c.kind})</option>
            {/each}
          </select>
        </label>
        <label class="cred-row">
          <span>Username</span>
          <input
            type="text"
            placeholder="(leave blank to inherit)"
            bind:value={overrideUsername}
            autocomplete="off"
          />
        </label>
        <label class="cred-row">
          <span>Password</span>
          <PasswordInput
            placeholder="(leave blank to use the credential)"
            bind:value={overridePassword}
            autocomplete="off"
          />
        </label>
        <p class="hint inline">
          All three fields are independent and reset after the next
          Connect press - nothing is saved. Jump-hosts keep their
          inherited credentials. The password is plain text in
          memory for the duration of the call.
        </p>
      </div>
    {/if}
    {#if connectErr}
      {@const friendly = explainConnectError(connectErr)}
      {@const raw = unwrapConnectErr(connectErr)}
      <div class="err connect-err" bind:this={connectErrEl}>
        <div class="err-summary">⚠ {friendly.summary}</div>
        {#if friendly.hint}<div class="err-hint">{friendly.hint}</div>{/if}
        {#if friendly.summary !== raw}
          <details class="err-raw">
            <summary>Show raw error</summary>
            <pre>{raw}</pre>
          </details>
        {/if}
      </div>
    {/if}
    {#if connectDebugLines.length > 0}
      <details class="debug-log" open>
        <summary>Connect diagnostics ({connectDebugLines.length} lines)</summary>
        <pre>{connectDebugLines.join("\n")}</pre>
      </details>
    {/if}
    {#if copiedHint}<div class="ok-hint">{copiedHint}</div>{/if}
    {@const inhUser = tree.inheritedFieldForConnection(conn.id, "username")}
    {@const inhPort = tree.inheritedFieldForConnection(conn.id, "port")}
    {@const inhAuth = tree.inheritedFieldForConnection(conn.id, "auth_ref")}
    {@const inhJump = tree.inheritedFieldForConnection(conn.id, "jump_host")}
    {#if edTab === "general"}
    <div class="form">
      <label title="A label for this connection in the tree - any text you like.">Name<input bind:value={editing.name} placeholder="My server" /></label>
      {#if !isLocal && !isWeb}
        <label title="What a double-click or Enter on this connection does. The other actions stay in the header and the right-click menu.">
          Double-click opens
          <select value={connOpenAs} onchange={(e) => setConnOpenAs(e.currentTarget.value as OpenAs)}>
            <option value="">(inherit: {openAsLabel(connInhOpenAs.mode)}{connInhOpenAs.from ? `, from ${connInhOpenAs.from}` : ""})</option>
            <option value="terminal">Terminal</option>
            <option value="tunnel">Tunnel - no shell, only the forwards</option>
            {#each openAsServices({ vnc: connVncOn, kafka: connKafkaOn, db: connDbOn }, "conn") as o (o.value)}
              <option value={o.value} disabled={!o.on}>{o.label}{o.on ? "" : ` (${o.off})`}</option>
            {/each}
          </select>
        </label>
        {@const inhSens = tree.inheritedFieldForConnection(conn.id, "sensitive")}
        <label title="Production or otherwise critical. The LLM (MCP) cannot list, open, change or route through it; a Database tab asks before statements that change data; Kafka asks for the name before a delete.">
          Sensitive
          <select bind:value={editing.sensitive}>
            <option value="">(inherit: {inhSens.value ? "yes" : "no"}{inhSens.from ? `, from ${inhSens.from.name}` : ""})</option>
            <option value="on">Yes - off limits to the LLM, writes and deletes confirmed</option>
            <option value="off">No</option>
          </select>
        </label>
      {/if}
      {#if isLocal}
        <label class="span-2" title="Which local shell to launch. Auto picks a sensible default for this OS. The Initial command below runs inside it.">
          Shell
          <select bind:value={editing.localShellKind}>
            {#each localShellKindOptions as opt (opt.value)}
              <option value={opt.value}>{opt.label}</option>
            {/each}
          </select>
          <span class="field-note">A local-shell connection runs on this machine - no host, no SSH. The Initial command below is what it runs (e.g. <code>telnet 10.0.0.5</code>, <code>claude</code>).</span>
        </label>
        <label class="span-2" title="Where this shell starts. Empty uses the app-wide setting, then your home directory.">
          Start in directory
          <input bind:value={editing.localShellDir} placeholder="Leave empty for the app default" />
          <span class="field-note">Empty falls back to the app-wide setting in Settings, and then to your home directory. <strong>WSL</strong> takes a Linux path (<code>/srv/app</code>).</span>
        </label>
      {:else if isWeb}
        <label class="span-2" title="The page to open. It goes through the network profile below (inherited from the folder) when there is one.">Address<input bind:value={editing.hostname} placeholder="https://grafana.customer.lan" /></label>
      <label title={isWeb ? "Which VPN's browser opens this page. Direct = your normal browser." : "Route the first SSH hop through a userspace VPN tunnel (no TUN adapter, no admin rights)."}>
        Network
        <select bind:value={editing.networkProfile}>
          <option value="">(inherit from folder)</option>
          <option value="__direct__">Direct - no tunnel</option>
          {#each networkProfiles.list as np (np.id)}
            <option value={np.id}>via {np.name} ({profileKindTitle(np)})</option>
          {/each}
        </select>
        <span class="field-note">
          {networkProfiles.list.length === 0 ? "No network profiles yet - " : "Network profiles live in "}
          <button class="linklike" onclick={(e) => { e.preventDefault(); view.setTabSettingsSection("network"); }}>
            Settings -&gt; Network profiles</button>.
        </span>
      </label>
      {:else}
      <div class="row">
        <label class="grow" title="The address to connect to: a DNS hostname or an IP. This is what SSH dials, not the display name above.">Hostname / IP address<input bind:value={editing.hostname} placeholder="host.example.com or 10.0.0.5" onblur={splitHostField} /></label>
        <label class="port">
          Port
          <input
            type="number"
            bind:value={editing.port}
            min="1"
            max="65535"
            placeholder={inhPort.value ? String(inhPort.value) : "22"}
          />
          {#if inhPort.from && !editing.port}
            <span class="inh-hint">from <strong>{inhPort.from.name}</strong>: {inhPort.value}</span>
          {/if}
        </label>
      </div>
      <label>
        Username
        <input
          bind:value={editing.username}
          placeholder={inhUser.value ? String(inhUser.value) : "(no inherited value)"}
        />
        {#if inhUser.from && !editing.username}
          <span class="inh-hint">inherited from <strong>{inhUser.from.name}</strong>: {inhUser.value}</span>
        {/if}
      </label>
      <label>Credential
        <div class="cred-picker-row">
          <SearchableSelect
            bind:value={editing.authRef}
            options={credOptions}
            placeholder="Search credentials…"
          />
          {#if hasKeepass}
            <button type="button" class="kp-btn" onclick={() => openKeepassPicker("connection")}
              title="Pick a secret straight from a KeePass database">
              From KeePass
            </button>
          {/if}
          {#if hasBitwarden}
            <button type="button" class="kp-btn" onclick={() => openBitwardenPicker("connection")}
              title="Pick a secret straight from a Bitwarden server">
              From Bitwarden
            </button>
          {/if}
          {#if hasInfisical}
            <button type="button" class="kp-btn" onclick={() => openInfisicalPicker("connection")}
              title="Pick a secret straight from an Infisical server">
              From Infisical
            </button>
          {/if}
        </div>
        {#if editing.authRef && keepassCredIds.has(editing.authRef)}
          <span class="kp-badge">KeePass-backed - secret read from the .kdbx at connect</span>
        {/if}
        {#if editing.authRef && bitwardenCredIds.has(editing.authRef)}
          <span class="kp-badge">Bitwarden-backed - secret read from the server at connect</span>
        {/if}
        {#if editing.authRef && infisicalCredIds.has(editing.authRef)}
          <span class="kp-badge">Infisical-backed - secret read from the server at connect</span>
        {/if}
        {#if inhAuth.from && !editing.authRef}
          {@const inhCredName = credentials.byId(String(inhAuth.value))?.name ?? String(inhAuth.value)}
          <span class="inh-hint">inherited from <strong>{inhAuth.from.name}</strong>: {inhCredName}</span>
        {/if}
        {#if editing.authRef === AUTH_NONE}
          <span class="inh-hint">
            No credential{#if inhAuth.from}{" "}(not inheriting <strong>{inhAuth.from.name}</strong>){/if}:
            {passwordHasValue ? "this connection's password is used" : "username and password are asked at connect when not set here"}.
          </span>
        {:else if passwordHasValue && effectiveAuthCred?.kind === "password"}
          <span class="inh-hint"><strong>Password overridden</strong> - this connection's password is tried first, the credential's only if it fails.</span>
        {/if}
      </label>
      {#if otpCredOptions.length > 0 || editing.otpRef || hasKeepass || hasBitwarden}
        {@const inhOtp = tree.inheritedFieldForConnection(conn.id, "otp_ref")}
        <label>One-time code from
          <div class="cred-picker-row">
            <SearchableSelect
              bind:value={editing.otpRef}
              options={[{ value: AUTH_NONE, label: "None - do not inherit" }, ...otpCredOptions]}
              placeholder="(none)"
            />
            {#if hasKeepass}
              <button type="button" class="kp-btn" onclick={() => openKeepassPicker("connection-otp")}
                title="Use the TOTP of a KeePass entry">From KeePass</button>
            {/if}
            {#if hasBitwarden}
              <button type="button" class="kp-btn" onclick={() => openBitwardenPicker("connection-otp")}
                title="Use the TOTP of a Bitwarden item">From Bitwarden</button>
            {/if}
          </div>
          {#if inhOtp.from && !editing.otpRef}
            <span class="inh-hint">inherited from <strong>{inhOtp.from.name}</strong>: {credentials.byId(String(inhOtp.value))?.name ?? String(inhOtp.value)}</span>
          {:else}
            <span class="inh-hint">Answers the server's verification-code question from this item's TOTP, next to the credential above.</span>
          {/if}
        </label>
      {/if}
      <label class="span-2">Password
        <div class="row pass-row">
          <PasswordInput
            bind:value={passwordInput}
            placeholder={passwordHasValue ? "(saved - type to change)" : "(not set)"}
          />
          <button disabled={!passwordInput || passwordSaving} onclick={savePassword}>
            {passwordSaving ? "…" : "Set"}
          </button>
          {#if passwordHasValue}
            <button class="danger" onclick={clearPassword}>Clear</button>
          {/if}
        </div>
        {#if passwordInput}
          <PasswordStrengthMeter password={passwordInput} showFeedback={false} />
        {/if}
        {#if passwordHasValue && keyAuthCredKind}
          <span class="pass-hint warn">
            This connection also authenticates with a {keyAuthCredKind === "opkssh" ? "certificate" : keyAuthCredKind}.
            The key is tried first; if the server rejects it, this password is sent as a
            fallback - so a host impersonating this one can make the key fail on purpose
            and collect the password. Keep it only if you need it on hosts where the key
            is not installed.
          </span>
        {:else if passwordHasValue}
          <span class="pass-hint">Password saved - will be used for auth</span>
        {/if}
      </label>
      {/if}
      <div class="span-2"><ColorPicker
        value={editing.colorTag}
        onChange={(v) => { if (editing) editing = { ...editing, colorTag: v }; }}
      /></div>
      <div class="span-2"><IconPicker
        kind="connection"
        targetId={conn.id}
        currentIconId={conn.icon_image_id}
        currentIconName={conn.icon_name}
        currentIconColor={conn.icon_color}
        fallbackEmoji=""
        onChange={() => tree.load()}
        onNamedChange={() => tree.load()}
      /></div>
      <div class="tag-editor span-2">
        <span class="tag-label">Tags</span>
        <TagInput bind:tags={editing.tags} known={tree.allTags()} />
      </div>
      <div class="span-2 notes-block">
        <div class="notes-head">
          <span>Notes</span>
          <div class="notes-mode">
            <button
              type="button"
              class:active={notesMode === "edit"}
              onclick={() => (notesMode = "edit")}
            >Edit</button>
            <button
              type="button"
              class:active={notesMode === "preview"}
              disabled={!editing.notes}
              onclick={() => (notesMode = "preview")}
            >Preview</button>
          </div>
        </div>
        {#if notesMode === "edit"}
          <textarea bind:value={editing.notes} rows="6" placeholder="Markdown: # heading, **bold**, `code`, fenced ```, - lists, [link](https://…)"></textarea>
        {:else}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div class="notes-preview selectable" onclick={onNotesPreviewClick}>{@html renderedNotes}</div>
        {/if}
      </div>
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {#if !isLocal && !isWeb}
    <div class="quick-actions">
      <h2>Quick actions</h2>
      <div class="actions-row">
        <button onclick={() => copyField("hostname")} title="Copy host to clipboard">
          <IconHost size={13} /> Host
        </button>
        <button onclick={() => copyField("username")} title="Copy username to clipboard">
          <IconUser size={13} /> User
        </button>
        <button onclick={() => copyField("password")} title="Copy password (clears clipboard after 30s)">
          <IconLock size={13} /> Password
        </button>
        <span class="actions-sep" aria-hidden="true"></span>
        <button onclick={copySystemSshCommand} title="Copy `ssh …` invocation to clipboard">
          <IconClipboardCopy size={13} /> ssh command
        </button>
        <button onclick={launchInSystemTerminal} title="Open this connection in your OS terminal (Windows Terminal / Terminal.app / gnome-terminal / …)">
          <IconTerminal size={13} /> Launch in system terminal
        </button>
      </div>
      {#if copiedHint}<div class="ok-hint">{copiedHint}</div>{/if}
    </div>
    {/if}
    {:else if edTab === "connection"}
    <div class="form">
      <div class="span-2 chain-field">
        <span class="chain-label">Path</span>
        <div class="chain-view">
          <img class="chain-app" src={appIcon} alt="This computer" title="This computer (ssh-tool)" />
          {#if connNet}
            {@const np = connNet.np}
            <svg class="chain-arrow" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 6l6 6-6 6"></path></svg>
            <button
              type="button"
              class="chain-hop chain-net"
              class:missing={!np}
              title={np
                ? `The first hop dials through the ${profileKindTitle(np)} profile ${np.name}${np.mode === "auto" ? ", only when the host is not reachable directly" : ""}${connNet.from ? ` (from ${connNet.from})` : ""}. Click to change it.`
                : "This network profile no longer exists. Click to pick another."}
              onclick={() => { netSelectEl?.scrollIntoView({ block: "nearest" }); netSelectEl?.focus(); }}
            >
              <IconVpn size={14} />
              {#if np}
                <span>{np.name}</span>
                <span class="net-meta">{profileKindTitle(np)}{np.mode === "auto" ? " - auto" : ""}</span>
                {#if profileRunsHere(np)}
                  {@const st = netState(np)}
                  <span class="net-state {st.tone}">{st.text}</span>
                {:else}
                  <span class="net-state warn" title={NOT_ON_MOBILE_TITLE}>{np.mode === "auto" ? "direct on mobile" : "not on mobile"}</span>
                {/if}
              {:else}
                <span>missing profile</span>
              {/if}
            </button>
          {/if}
          {#each connChain as hop, i (i)}
            <svg class="chain-arrow" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 6l6 6-6 6"></path></svg>
            <span class="chain-hop" title={hop.title}>
              {#if hop.conn}
                <Icon imageId={hop.conn.icon_image_id} iconName={hop.conn.icon_name} iconColor={hop.conn.icon_color} size={14}><IconBastion size={14} /></Icon>
              {:else}
                <IconBastion size={14} />
              {/if}
              <span>{hop.label}</span>
            </span>
          {/each}
          <svg class="chain-arrow" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 6l6 6-6 6"></path></svg>
          <span class="chain-hop self">
            <Icon imageId={conn.icon_image_id} iconName={conn.icon_name} iconColor={conn.icon_color} size={14}><IconHost size={14} /></Icon>
            <span>{conn.name}</span>
          </span>
          <span class="chain-spacer"></span>
          <button type="button" class="chain-edit" onclick={() => (chainEditOpen = !chainEditOpen)}>{chainEditOpen ? "Done" : "Edit chain"}</button>
        </div>
        <span class="inh-hint">{connChainNote}{#if connNet?.from} Network from {connNet.from}.{/if}</span>
        {#if chainEditOpen}
          <div class="chain-editor">
            <JumpChainEditor selfId={conn?.id} value={editing.jumpHost} onChange={(v) => { if (editing) editing = { ...editing, jumpHost: v }; }} />
          </div>
        {/if}
      </div>
      <label title={isWeb ? "Which VPN's browser opens this page. Direct = your normal browser." : "Route the first SSH hop through a userspace VPN tunnel (no TUN adapter, no admin rights)."}>
        Network
        <select bind:this={netSelectEl} bind:value={editing.networkProfile}>
          <option value="">(inherit from folder)</option>
          <option value="__direct__">Direct - no tunnel</option>
          {#each networkProfiles.list as np (np.id)}
            <option value={np.id}>via {np.name} ({profileKindTitle(np)})</option>
          {/each}
        </select>
        <span class="field-note">
          {networkProfiles.list.length === 0 ? "No network profiles yet - " : "Network profiles live in "}
          <button class="linklike" onclick={(e) => { e.preventDefault(); view.setTabSettingsSection("network"); }}>
            Settings -&gt; Network profiles</button>.
        </span>
      </label>
      <label>Auto-reconnect
        <select bind:value={editing.autoReconnect}>
          <option value="">(inherit from folder)</option>
          <option value="on">On - retry with backoff after drop</option>
          <option value="off">Off - drop = closed</option>
        </select>
      </label>
      <label title="Anti-idle: send an SSH keepalive every N seconds so a bastion or firewall doesn't drop the connection when it's quiet. Blank inherits the folder's value; 0 sends nothing. A dead connection is still detected either way - with 0 it just takes up to a minute longer.">
        Keepalive / anti-idle (s)
        <input
          type="number"
          min="0"
          max="3600"
          step="5"
          bind:value={editing.keepalive}
          placeholder="(inherit · 0 = off)"
        />
        <span class="field-note">Stops idle drops. Blank = inherit folder, 0 = send nothing (a dead link is still detected, just slower).</span>
      </label>
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {:else if edTab === "terminal"}
    <div class="form">
      <label class="span-2" title="A command run in the shell right after connect - e.g. cd /var/www, tmux new -A -s main, source venv/bin/activate. Blank inherits the folder's value.">
        {isLocal ? "Command" : "Initial command"}
        <textarea
          class="cmd-input"
          rows="2"
          bind:value={editing.initialCommand}
          placeholder={isLocal ? "e.g. telnet 10.0.0.5   ·   claude   ·   screen /dev/ttyUSB0" : "(inherit) e.g. cd /var/www"}
        ></textarea>
        <span class="field-note">
          {#if isLocal}
            The command this connection runs in the shell (the point of a local connection). Blank = just an interactive shell. One command per line.
          {:else}
            Run in the shell on connect. Blank = inherit folder. One command per line.
          {/if}
        </span>
      </label>
      <label title="Wait this long between the lines of a multi-line command. Needed when a line replaces the shell that reads the next one.">
        Delay between lines (ms)
        <input
          type="number"
          min="0"
          max="60000"
          bind:value={editing.initialCommandDelay}
          placeholder={isLocal ? "0" : "(inherit)"}
        />
        <span class="field-note">{isLocal ? "0 = no pause." : "Blank = inherit folder, 0 = no pause."} Raise it when a line starts a new shell (<code>sudo su - user</code>) and the lines after it get swallowed.</span>
      </label>
      <label class="span-2 inline-check" title="Connect as usual, but leave the tab out of the tab bar. For background helpers - a keepalive loop, a tail, a watcher - where the point is that it runs, not that you watch it.">
        <input type="checkbox" bind:checked={editing.openHidden} />
        <span class="inline-check-text">
          Open in the background
          <span class="field-note">
            The session connects and keeps running; its tab is hidden. The tab
            bar shows a count of hidden tabs, and Ctrl+K finds them.
          </span>
        </span>
      </label>
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {:else if edTab === "forwards"}
    {#if connTunnelOnly}
      <div class="form tunnel-card">
        <div class="span-2 tunnel-state">
          {#if connTunnel}
            <span class="ed-pill {tunnelTone(connTunnel)}"><span class="ed-dot"></span>{tunnelText(connTunnel)}</span>
          {/if}
          <span class="field-note">A tunnel has no shell and no tab: starting it brings up every forward below.</span>
          <span class="chain-spacer"></span>
          {#if connTunnel && tunnels.running(connTunnel)}
            <button onclick={() => tunnels.stop(conn!.id)}>Stop tunnel</button>
          {:else}
            <button class="primary" onclick={() => tunnels.start(conn!.id)}>{connTunnel?.state === "error" ? "Try again" : "Start tunnel"}</button>
          {/if}
        </div>
        <label title="Start this tunnel when the app starts (once the vault is open).">Start with the app
          <select bind:value={editing.tunnelAutostart}>
            <option value="">(inherit: {connInhAutostart.value ? "on" : "off"}{connInhAutostart.from ? `, from ${connInhAutostart.from}` : ""})</option>
            <option value="on">On</option>
            <option value="off">Off</option>
          </select>
        </label>
      </div>
    {/if}
    <div class="form-width"><PortForwards connection={conn} /></div>
    <div class="form">
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {:else if edTab === "vnc"}
    <div class="form">
      <div class="span-2 vnc-section" class:on={connVncOn}>
        <div class="vnc-head">
          <button type="button"
            class="vnc-switch"
            role="switch"
            aria-label="Enable VNC console"
            aria-checked={connVncOn}
            onclick={() => { if (editing) editing = { ...editing, vncEnabled: flipTri(editing.vncEnabled, encodeBool(conn?.overrides?.vnc_enabled), vncInheritedOn) }; }}
          >
            <span class="vnc-knob"></span>
          </button>
          <span class="vnc-title">VNC console</span>
          <span class="vnc-spacer"></span>
          {#if connVncOn}
            <button type="button" class="vnc-open" onclick={openVncConsole}>Open console</button>
          {/if}
        </div>
        {#if connVncOn}
        <div class="vnc-grid">
          <label>RFB port
            <input
              type="number"
              min="1"
              max="65535"
              bind:value={editing.vncPort}
              placeholder="(inherit · 5900)"
            />
          </label>
          <label>Reach the port
            <select bind:value={editing.vncTunnel}>
              <option value="">(inherit from folder)</option>
              <option value="off">Direct - dial host:port</option>
              <option value="on">Through SSH - dial 127.0.0.1:port on the remote</option>
            </select>
          </label>
          <label class="span-2">VNC password
            <div class="row pass-row">
              <PasswordInput
                bind:value={vncPasswordInput}
                placeholder={vncPasswordHasValue ? "(saved - type to change)" : "(none - noVNC prompts if required)"}
              />
              <button type="button" disabled={!vncPasswordInput || vncPasswordSaving} onclick={saveVncPassword}>
                {vncPasswordSaving ? "…" : "Set"}
              </button>
              {#if vncPasswordHasValue}
                <button type="button" class="danger" onclick={clearVncPassword}>Clear</button>
              {/if}
            </div>
          </label>
        </div>
        {/if}
      </div>
      <p class="span-2 field-note">To open VNC on a double-click instead of a terminal, pick <strong>VNC console</strong> under General -&gt; Double-click opens.</p>
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {:else if edTab === "services"}
    <div class="form">
      <DbSettingsEditor
        bind:draft={editing.db}
        inheritLabel="(inherit from folder)"
        inheritedOn={dbInheritedFrom(conn?.folder_id, "db_enabled") === true}
        savedEnabled={encodeBool(conn?.overrides?.db_enabled)}
        effectiveOn={connDbOn}
        effectiveEngine={dbInheritedFrom(conn?.folder_id, "db_engine") ?? ""}
        inheritedReadOnly={dbInheritedFrom(conn?.folder_id, "db_read_only") === true}
        onOpen={() => { if (conn) connectionActions.openDb(conn.id); }}
        showTargets
        connName={conn?.name ?? ""}
        onOpenTarget={(id) => { if (conn) connectionActions.openDb(conn.id, id); }}
      />
      <KafkaSettingsEditor
        bind:draft={editing.kafka}
        inheritLabel="(inherit from folder)"
        inheritedOn={kafkaInheritedFrom(conn?.folder_id)}
        savedEnabled={encodeBool(conn?.overrides?.kafka_enabled)}
        effectiveOn={editing.kafka.enabled === "on" || (editing.kafka.enabled === "" && kafkaInheritedFrom(conn?.folder_id))}
        inheritedReadOnly={dbInheritedFrom(conn?.folder_id, "kafka_read_only") === true}
        onOpen={() => { if (conn) connectionActions.openKafka(conn.id); }}
      />
      <p class="span-2 field-note">To open the database or Kafka on a double-click instead of a terminal, pick it under General -&gt; Double-click opens. Changes apply to the next tab you open.</p>
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {:else if edTab === "advanced"}
    <div class="form">
      <label>Verbose connect log
        <select bind:value={editing.verbose}>
          <option value="">(inherit from folder)</option>
          <option value="on">On - show TCP / handshake / auth diagnostics</option>
          <option value="off">Off</option>
        </select>
      </label>
      {#if dirty}
        <div class="span-2 dirty-hint">
          <span class="dirty-pill">Unsaved changes</span>
          <button class="link-save" onclick={saveConn}>Save now</button>
          <button class="link-save" onclick={discardDraft}>Discard</button>
        </div>
      {/if}
    </div>
    {@const jumpUsers = tree.bastionMap.get(conn.id) ?? []}
    {#if jumpUsers.length}
      <div class="jump-users">
        <h2>Jump host for ({jumpUsers.length})</h2>
        <ul>
          {#each jumpUsers as u (u.kind + u.id)}
            <li>
              <button class="usage-link" type="button" title={`Reveal ${u.label} in the connections tree`}
                onclick={() => view.reveal(u.kind, u.id)}>
                {#if u.kind === "folder"}
                  <IconFolder size={13} /> <strong>{tree.folderById(u.id)?.name ?? u.label}</strong> <span class="muted">(folder - everything in it)</span>
                {:else}
                  <IconHost size={13} /> <strong>{u.label}</strong> <span class="muted">- {tree.connectionById(u.id)?.hostname ?? ""}</span>
                {/if}
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
    <details class="resolved">
      <summary><h2>Resolved &amp; inherited</h2></summary>
      {#if resolveErr}
        <div class="err">{resolveErr}</div>
      {:else if resolved}
        {@const ovr = conn.overrides ?? {}}
        <table class="resolved-table">
          <thead>
            <tr>
              <th>Field</th>
              <th>Effective value</th>
              <th>Source</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>username</td>
              <td>{resolved.username ?? "-"}</td>
              <td>{ovr.username ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>port</td>
              <td>{resolved.port}</td>
              <td>{ovr.port !== undefined ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>credential</td>
              <td>{resolved.auth_ref ? (credentials.byId(resolved.auth_ref)?.name ?? resolved.auth_ref) : "-"}</td>
              <td>{ovr.auth_ref === "" ? "none (override)" : ovr.auth_ref ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>jump chain</td>
              <td>
                {#if resolved.jump_host}
                  {@const chain = jumpChainNames(resolved.jump_host)}
                  {chain.length > 0 ? chain.join(" → ") : "-"}
                {:else}
                  none
                {/if}
              </td>
              <td>{ovr.jump_host ? `override (${ovr.jump_host.kind})` : "inherited"}</td>
            </tr>
            <tr>
              <td>auto-reconnect</td>
              <td>{resolved.auto_reconnect ? "on" : "off"}</td>
              <td>{ovr.auto_reconnect !== undefined ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>verbose</td>
              <td>{resolved.verbose ? "on" : "off"}</td>
              <td>{ovr.verbose !== undefined ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>tunnel only</td>
              <td>{resolved.tunnel_only ? (resolved.tunnel_autostart ? "on, starts on launch" : "on") : "off"}</td>
              <td>{ovr.tunnel_only !== undefined || ovr.tunnel_autostart !== undefined ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>database</td>
              <td>{resolved.db_enabled ? `${dbEngineInfo(resolved.db_engine).short} ${resolved.db_addr || "127.0.0.1"}${resolved.db_name ? "/" + resolved.db_name : ""} · ${resolved.db_use_tunnel ? "through SSH" : "direct"}${resolved.db_read_only ? " · read-only" : ""}${resolved.db_tls !== "disable" ? ` · TLS ${resolved.db_tls}` : ""}${resolved.db_credential_ref ? "" : " · no login"}` : "off"}</td>
              <td>{Object.keys(ovr).some((k) => k.startsWith("db_")) ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>kafka</td>
              <td>{resolved.kafka_enabled ? `${resolved.kafka_bootstrap || "(no bootstrap)"} · ${resolved.kafka_use_tunnel ? "through SSH" : "direct"}${resolved.kafka_read_only ? " · read-only" : ""}${resolved.kafka_tls ? " · TLS" : ""}${resolved.kafka_sasl ? ` · SASL ${resolved.kafka_sasl}` : ""}` : "off"}</td>
              <td>{Object.keys(ovr).some((k) => k.startsWith("kafka_")) ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>keepalive</td>
              <td>{resolved.keepalive_interval ? `${resolved.keepalive_interval}s` : "default"}</td>
              <td>{ovr.keepalive_interval !== undefined ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>terminal type</td>
              <td>{resolved.terminal_type || "-"}</td>
              <td>{ovr.terminal_type ? "override" : "inherited"}</td>
            </tr>
            <tr>
              <td>color tag</td>
              <td>
                {#if resolved.color_tag}
                  <span class="tag-swatch" style:background={resolved.color_tag}></span>
                  {resolved.color_tag}
                {:else}-{/if}
              </td>
              <td>{ovr.color_tag ? "override" : "inherited"}</td>
            </tr>
          </tbody>
        </table>
        <details class="resolved-raw">
          <summary>Raw JSON</summary>
          <pre>{JSON.stringify(resolved, null, 2)}</pre>
        </details>
      {:else}
        <div class="muted">resolving…</div>
      {/if}
    </details>
    {/if}

  {/if}
</section>

{#if deleteItems.length > 0}
  <DeleteConfirm
    items={deleteItems}
    onConfirm={confirmDelete}
    onCancel={cancelDelete}
  />
{/if}

{#if batchExecOpen}
  <BatchExecModal
    onClose={() => (batchExecOpen = false)}
    hostsOverride={batchExecHosts}
  />
{/if}

{#if kpPickerOpen}
  <KeepassEntryPicker
    onClose={() => { kpPickerOpen = false; kpPickerTarget = null; }}
    onPick={onKeepassPick}
  />
{/if}

{#if bwPickerOpen}
  <BitwardenEntryPicker
    onClose={() => { bwPickerOpen = false; bwPickerTarget = null; }}
    onPick={onBitwardenPick}
  />
{/if}

{#if infPickerOpen}
  <InfisicalEntryPicker
    onClose={() => { infPickerOpen = false; infPickerTarget = null; }}
    onPick={onInfisicalPick}
  />
{/if}

<style>
  /* No top padding: the scrollport top must equal the content top so the
     sticky header can sit flush at top:0 with no gap for content to show
     through. The header supplies its own top padding. */
  .detail { padding: 0 1.25rem 1rem; overflow: auto; color: var(--text); }
  /* Small explanatory note under a form field (e.g. keepalive). */
  .linklike {
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    color: var(--blue);
    cursor: pointer;
    text-decoration: underline;
  }
  /* The initial command is a textarea so a multi-line snippet (a loop, a
     couple of setup lines) can be pasted as-is. Monospace because the
     content is shell, and vertically resizable only - horizontal growth
     would break the two-column grid. */
  .cmd-input {
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    resize: vertical;
    min-height: 2.2rem;
  }
  .field-note {
    display: block;
    margin-top: 0.15rem;
    font-size: 0.72rem;
    color: var(--overlay1, var(--subtext0));
    line-height: 1.3;
  }
  .title-input {
    background: transparent;
    border: none;
    border-bottom: 1px solid var(--surface0);
    color: var(--text);
    font: inherit;
    font-size: 1.1rem;
    font-weight: 600;
    padding: 0.1rem 0.2rem;
    width: 100%;
    max-width: 320px;
  }
  .title-input:focus { outline: none; border-bottom-color: var(--blue); }
  .section-label {
    font-size: 0.75rem;
    color: var(--overlay0);
    margin: 0 0 0.5rem;
    line-height: 1.4;
  }
  .muted { color: var(--overlay0); }
  header {
    display: flex; align-items: center; justify-content: space-between;
    border-bottom: 1px solid var(--surface0);
    padding-bottom: 0.5rem; margin-bottom: 1rem;
    /* Keep the name + Save / Connect / Use-different-credential / Delete
       actions visible while the form scrolls. The .detail container has
       1rem top / 1.25rem side padding; pull the sticky header out to the
       container edges and re-pad it so its background covers the full width
       and it sits flush at the top. */
    position: sticky;
    top: 0;
    z-index: 5;
    background: var(--base);
    /* .detail has no top padding now; pull out to the side edges only and
       supply the top spacing here so the header sits flush at top:0 with no
       gap above it for scrolling content to show through. */
    margin: 0 -1.25rem 1rem;
    padding: 1rem 1.25rem 0.5rem;
    flex-wrap: wrap;
    gap: 0.4rem 0.5rem;
  }
  .head-actions { display: flex; gap: 0.5rem; flex-wrap: wrap; }
  /* Editor header: title + who/where on the left, actions on the right,
     the tab bar under both. The tabs sit in the sticky header so they stay
     reachable while a long tab scrolls. */
  header.ed-head { padding-bottom: 0; }
  .ed-title { display: flex; flex-direction: column; gap: 0.15rem; min-width: 0; }
  .ed-title h1 { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; }
  .ed-name { overflow: hidden; text-overflow: ellipsis; }
  .ed-sub {
    font-size: 0.78rem; color: var(--subtext0);
    font-family: ui-monospace, monospace;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .ed-pill {
    display: inline-flex; align-items: center; gap: 0.35rem;
    font-size: 0.72rem; font-weight: 500;
    padding: 0.1rem 0.55rem; border-radius: 999px;
    border: 1px solid var(--surface1); color: var(--subtext0);
  }
  .ed-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
  .ed-pill.ok { color: var(--green); }
  .ed-pill.warn { color: var(--yellow); }
  .ed-pill.bad { color: var(--red); }
  .ed-pill.idle { color: var(--overlay1); }
  .more-btn { display: inline-flex; align-items: center; padding-inline: 0.45rem; }
  /* The stage text ("Not reachable directly - starting VPN ...") can be
     long; the full text is in the tooltip. */
  .connect-btn { max-width: 24rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ed-tabs {
    flex-basis: 100%;
    display: flex; flex-wrap: wrap; gap: 0.1rem;
    margin-top: 0.35rem;
  }
  .ed-tabs button {
    background: transparent; border: 0; border-radius: 0;
    border-bottom: 2px solid transparent;
    color: var(--subtext0);
    padding: 0.45rem 0.75rem;
    font: inherit; font-size: 0.85rem;
    cursor: pointer;
  }
  .ed-tabs button:hover { color: var(--text); }
  .ed-tabs button.active { color: var(--text); border-bottom-color: var(--blue); font-weight: 600; }
  .ed-warn {
    margin-left: 0.35rem; font-size: 0.68rem; font-weight: 700;
    color: var(--base); background: var(--yellow);
    border-radius: 999px; padding: 0 0.4rem;
  }
  /* Jump chain as cards: this computer -> hops -> this host. */
  .chain-field { display: flex; flex-direction: column; gap: 0.3rem; }
  .chain-label { font-size: 0.8rem; color: var(--subtext0); }
  .chain-view {
    display: flex; align-items: center; flex-wrap: wrap; gap: 0.4rem;
    background: var(--mantle); border: 1px solid var(--surface0);
    border-radius: 6px; padding: 0.45rem 0.6rem;
  }
  .chain-app { width: 22px; height: 22px; display: block; }
  .chain-arrow { color: var(--overlay1); flex: none; }
  .chain-hop {
    display: inline-flex; align-items: center; gap: 0.35rem;
    background: var(--surface0); border-radius: 5px;
    padding: 0.15rem 0.5rem 0.15rem 0.35rem;
    font-size: 0.8rem; font-family: ui-monospace, monospace;
  }
  .chain-hop.self { outline: 1px solid var(--blue); }
  /* The network profile: transport under the chain, not an SSH hop. */
  .chain-net {
    background: transparent; border: 1px dashed var(--mauve, #b675f0); color: var(--text);
    cursor: pointer; font: inherit; font-size: 0.8rem; font-family: ui-monospace, monospace;
  }
  .chain-net :global(svg) { color: var(--mauve, #b675f0); }
  .chain-net:hover { background: var(--surface0); }
  .chain-net.missing { border-color: var(--red); color: var(--red); }
  .net-meta { color: var(--overlay1); font-family: ui-sans-serif, system-ui, sans-serif; font-size: 0.72rem; }
  .net-state { font-family: ui-sans-serif, system-ui, sans-serif; font-size: 0.68rem; font-weight: 600; }
  .net-state.ok { color: var(--green); }
  .net-state.warn { color: var(--yellow); }
  .net-state.off { color: var(--overlay0); }
  .chain-spacer { flex: 1; }
  .chain-edit { font-size: 0.78rem; padding: 0.2rem 0.6rem; }
  .chain-editor { margin-top: 0.3rem; }
  /* Forwards tab: the tunnel's own controls above the forward list. */
  .tunnel-card {
    background: var(--mantle); border: 1px solid var(--surface0);
    border-radius: 8px; padding: 0.75rem 0.9rem; margin-bottom: 1rem;
  }
  .tunnel-state { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
  .fav-toggle {
    display: inline-flex;
    align-items: center;
    background: transparent;
    border: 0;
    color: var(--surface2);
    cursor: pointer;
    font: inherit;
    padding: 0 0.3rem;
    margin-right: 0.1rem;
  }
  .fav-toggle:hover { color: var(--yellow); }
  .fav-toggle.active { color: var(--yellow); }
  h1 { margin: 0; font-size: 1.1rem; font-weight: 600; }
  h2 {
    margin-top: 2rem; font-size: 0.85rem;
    text-transform: uppercase; letter-spacing: 0.05em; color: var(--subtext0);
  }
  /* Resolved-settings is opt-in noise - collapse it by default so the
     editor doesn't end with a wall of JSON. The h2 stays as the
     visible summary; <details>'s native marker reads as the toggle. */
  .resolved summary { cursor: pointer; }
  .resolved summary h2 { display: inline-block; margin: 0; padding: 0 0.3rem; }
  .resolved { margin-top: 2rem; }
  .resolved-table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
    font-size: 0.82rem;
  }
  .resolved-table th {
    text-align: left;
    color: var(--overlay0);
    font-weight: 600;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    border-bottom: 1px solid var(--surface0);
    padding: 0.3rem 0.5rem 0.3rem 0;
  }
  .resolved-table td {
    padding: 0.3rem 0.5rem 0.3rem 0;
    border-bottom: 1px solid var(--surface0);
    vertical-align: top;
  }
  .resolved-table td:first-child {
    color: var(--subtext0);
    font-family: ui-monospace, monospace;
    font-size: 0.78rem;
    width: 9rem;
  }
  .resolved-table td:last-child {
    color: var(--overlay0);
    font-size: 0.72rem;
    text-align: right;
  }
  .resolved-raw {
    margin-top: 0.6rem;
  }
  .resolved-raw summary {
    color: var(--overlay0);
    font-size: 0.75rem;
    cursor: pointer;
    padding: 0.3rem 0;
  }
  .resolved-raw pre {
    background: var(--mantle);
    padding: 0.6rem;
    border-radius: 3px;
    overflow: auto;
    font-size: 0.72rem;
    margin: 0.3rem 0 0;
  }
  .tag-swatch {
    display: inline-block;
    width: 0.8rem;
    height: 0.8rem;
    border-radius: 2px;
    vertical-align: middle;
    margin-right: 0.3rem;
    border: 1px solid var(--surface0);
  }
  pre {
    background: var(--crust); padding: 0.5rem 0.75rem;
    border-radius: 4px; font-size: 0.78rem; overflow: auto;
  }
  /* Grid lets fields land 2-up on wide screens - auto-fit keeps it
     single-column under ~620px so phone-skinny windows stay readable.
     Items that need full width (.row, jump editor, batch warnings)
     declare `grid-column: 1 / -1`. */
  .form {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 0.6rem 1rem;
    max-width: 1080px;
  }
  .form > .row,
  .form > .span-2 { grid-column: 1 / -1; }
  .save-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .saved-pill {
    color: var(--green);
    font-size: 0.78rem;
    font-weight: 600;
  }
  /* Save lives in the header next to Connect/Delete. It's disabled
     (greyed) when there's nothing to save, and tinted yellow when there
     are unsaved edits so it stands out as the thing to click. */
  .save-btn {
    font-weight: 600;
  }
  .save-btn:disabled {
    opacity: 0.45;
    cursor: default;
  }
  .save-btn.dirty {
    background: var(--yellow);
    color: var(--crust);
  }
  .save-btn.dirty:hover:not(:disabled) {
    background: var(--peach);
  }
  /* Just-saved confirmation: disabled (nothing to save) but shown in
     full-opacity green so the "✓ Saved" reads clearly for its 1.8s. */
  .save-btn.just-saved:disabled {
    opacity: 1;
    color: var(--green);
  }
  /* In-form reminder near the bottom that there are unsaved edits, with
     a shortcut to save without scrolling back up to the header. */
  .dirty-hint {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.2rem;
  }
  .dirty-pill {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    color: var(--yellow);
    font-size: 0.78rem;
    font-weight: 600;
  }
  .dirty-pill::before {
    content: "";
    width: 7px; height: 7px;
    border-radius: 50%;
    background: var(--yellow);
  }
  .link-save {
    background: transparent;
    border: 0;
    color: var(--blue);
    font: inherit;
    font-size: 0.78rem;
    text-decoration: underline;
    cursor: pointer;
    padding: 0;
  }
  .link-save:hover { color: var(--lavender); }
  .form label {
    display: flex; flex-direction: column; gap: 0.25rem;
    font-size: 0.8rem; color: var(--subtext0);
  }
  .form .row { display: flex; gap: 0.6rem; }
  .form .grow { flex: 1; }
  .form .port { width: 5rem; }
  .notes-block { display: flex; flex-direction: column; gap: 0.25rem; }
  .notes-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 0.8rem;
    color: var(--subtext0);
  }
  .notes-mode { display: flex; gap: 0.2rem; }
  .notes-mode button {
    background: transparent;
    color: var(--subtext0);
    border: 1px solid var(--surface0);
    padding: 0.15rem 0.55rem;
    font-size: 0.72rem;
    border-radius: 3px;
  }
  .notes-mode button:hover:not(:disabled) { background: var(--surface0); color: var(--text); }
  .notes-mode button.active {
    background: var(--surface0);
    color: var(--text);
    border-color: var(--surface1);
  }
  .notes-preview {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.6rem 0.8rem;
    color: var(--text);
    font-size: 0.88rem;
    line-height: 1.5;
    min-height: 5rem;
    max-height: 18rem;
    overflow-y: auto;
  }
  .notes-preview :global(h1),
  .notes-preview :global(h2),
  .notes-preview :global(h3),
  .notes-preview :global(h4),
  .notes-preview :global(h5),
  .notes-preview :global(h6) {
    margin: 0.6rem 0 0.3rem;
    color: var(--text);
    font-weight: 600;
  }
  .notes-preview :global(h1) { font-size: 1.05rem; }
  .notes-preview :global(h2) { font-size: 0.98rem; }
  .notes-preview :global(h3) { font-size: 0.92rem; }
  .notes-preview :global(h4),
  .notes-preview :global(h5),
  .notes-preview :global(h6) { font-size: 0.85rem; color: var(--subtext0); }
  .notes-preview :global(p) { margin: 0.35rem 0; }
  .notes-preview :global(ul),
  .notes-preview :global(ol) { margin: 0.35rem 0; padding-left: 1.4rem; }
  .notes-preview :global(li) { margin: 0.1rem 0; }
  .notes-preview :global(code) {
    background: var(--surface0);
    color: var(--pink);
    padding: 0.05rem 0.35rem;
    border-radius: 3px;
    font-family: ui-monospace, monospace;
    font-size: 0.82rem;
  }
  .notes-preview :global(pre) {
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.5rem 0.7rem;
    overflow-x: auto;
    margin: 0.4rem 0;
  }
  .notes-preview :global(pre code) {
    background: transparent;
    color: var(--text);
    padding: 0;
    font-size: 0.82rem;
  }
  .notes-preview :global(hr) {
    border: 0;
    border-top: 1px solid var(--surface0);
    margin: 0.6rem 0;
  }
  .notes-preview :global(.md-link) {
    color: var(--blue);
    text-decoration: underline;
    cursor: pointer;
  }
  .notes-preview :global(.md-link:hover) { color: var(--lavender); }
  .notes-preview :global(strong) { color: var(--peach); }
  .notes-preview :global(em) { color: var(--yellow); font-style: italic; }
  input, textarea, select {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.35rem 0.5rem; font: inherit;
  }
  input:focus, textarea:focus, select:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  button {
    background: var(--surface0); color: var(--text);
    /* 1px transparent border so .ghost (which paints a real border)
       doesn't grow 2px taller than .primary / .danger and break
       the head-actions row alignment. */
    border: 1px solid transparent;
    padding: 0.4rem 0.85rem; border-radius: 3px;
    cursor: pointer; font: inherit;
    line-height: 1.2;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  button.primary {
    background: var(--blue); color: var(--on-accent); font-weight: 600;
    align-self: flex-start;
  }
  button.primary:hover:not(:disabled) { background: var(--lavender); }
  button.danger { background: transparent; color: var(--red); }
  button.danger:hover { background: var(--red); color: var(--on-accent); }
  .err {
    color: var(--red); background: var(--crust);
    padding: 0.5rem 0.75rem; border-radius: 4px;
    border-left: 3px solid var(--red);
  }
  .tag-editor { display: flex; flex-direction: column; gap: 0.3rem; margin: 0.4rem 0; }
  .tag-label { font-size: 0.78rem; color: var(--subtext0); }

  /* Same cap as .form, so the forward cards line up with the fields above
     instead of stretching across a wide window. */
  .form-width { max-width: 1080px; }
  .jump-users { margin: 1rem 0; max-width: 1080px; }
  .jump-users h2 { margin: 0 0 0.4rem 0; }
  .jump-users ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.15rem; }
  .usage-link {
    display: inline-flex; align-items: center; gap: 0.35rem;
    background: none; border: 0; padding: 0.1rem 0.2rem; color: var(--text);
    font: inherit; font-size: 0.85rem; cursor: pointer; border-radius: 3px;
  }
  .usage-link:hover { background: var(--surface0); }
  .quick-actions {
    margin: 1rem 0;
    max-width: 1080px;
  }
  .quick-actions h2 {
    margin: 0 0 0.4rem 0;
  }
  .actions-row { display: flex; gap: 0.4rem; flex-wrap: wrap; align-items: center; }
  /* Tint the leading icon in each quick-action button so eyes find
     the right one without reading. Matches the pane-toolbar palette
     (Catppuccin host/user/pass/copy). */
  .actions-row > button:nth-child(1) :global(svg) { color: var(--blue); }
  .actions-row > button:nth-child(2) :global(svg) { color: var(--mauve); }
  .actions-row > button:nth-child(3) :global(svg) { color: var(--peach); }
  .actions-row > button:nth-child(5) :global(svg) { color: var(--green); }
  .actions-sep {
    display: inline-block;
    width: 1px;
    height: 1.4em;
    background: var(--surface0);
    margin: 0 0.3rem;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--surface1);
    color: var(--subtext0);
  }
  .ghost:hover { background: var(--surface0); color: var(--text); }

  .cred-override {
    margin-bottom: 0.75rem;
    padding: 0.5rem 0.75rem;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    font-size: 0.85rem;
  }
  .cred-override label {
    display: flex; gap: 0.5rem; align-items: center;
    flex-wrap: wrap;
  }
  .cred-row {
    margin: 0.25rem 0;
  }
  .cred-row > span:first-child {
    display: inline-block;
    min-width: 80px;
    color: var(--subtext0);
    font-size: 0.78rem;
  }
  .cred-override select,
  .cred-override input {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.25rem 0.4rem;
    font: inherit;
    font-size: 0.82rem;
    min-width: 220px;
  }

  .connect-err {
    margin-bottom: 0.75rem;
    font-size: 0.85rem;
    word-break: break-word;
  }
  .err-summary { font-weight: 600; }
  .err-hint {
    font-weight: 400;
    color: var(--pink);
    margin-top: 0.2rem;
    font-size: 0.8rem;
  }
  .err-raw {
    margin-top: 0.35rem;
    font-size: 0.75rem;
  }
  .err-raw summary {
    cursor: pointer;
    color: var(--subtext0);
    font-weight: 400;
    user-select: none;
  }
  .err-raw summary:hover { color: var(--text); }
  .err-raw pre {
    margin: 0.3rem 0 0;
    padding: 0.4rem 0.55rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    color: var(--red);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .debug-log {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.4rem 0.6rem;
    margin-bottom: 0.75rem;
    font-size: 0.78rem;
  }
  .debug-log summary {
    cursor: pointer;
    color: var(--yellow);
    font-weight: 500;
  }
  .debug-log pre {
    color: var(--text);
    background: var(--mantle);
    padding: 0.4rem 0;
    margin: 0.3rem 0 0;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-word;
    font-family: ui-monospace, monospace;
    font-size: 0.75rem;
    line-height: 1.5;
  }
  .ok-hint {
    color: var(--green); background: var(--crust);
    padding: 0.45rem 0.75rem; border-radius: 4px;
    border-left: 3px solid var(--green);
    font-family: ui-monospace, monospace;
    font-size: 0.82rem;
    margin-top: 0.4rem;
    /* break-all suits a copied value (a long ssh command, a host); it
       mangles a sentence by snapping words mid-character, and the
       no-password explanation is prose. break-word wraps both sanely. */
    overflow-wrap: break-word;
    word-break: normal;
  }
  .pass-row { align-items: center; }
  /* The password field is now a PasswordInput wrapper, not a bare input. */
  .pass-row > :global(.pw-wrap) { flex: 1; min-width: 0; }
  .pass-row button { flex-shrink: 0; }
  .pass-hint { font-size: 0.74rem; color: var(--green); margin-top: 0.15rem; }
  /* The password is a credential-disclosure risk here, not a completed
     setting, so it must not read as the green confirmation above. */
  .pass-hint.warn { color: var(--warn); line-height: 1.45; }

  .vnc-section {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.6rem 0.75rem;
    background: var(--surface1);
  }
  .vnc-head {
    display: flex;
    align-items: center;
    gap: 0.55rem;
    font-size: 0.85rem;
    color: var(--text1);
  }
  .vnc-head .vnc-spacer { flex: 1 1 auto; }
  .vnc-title { font-weight: 600; }
  /* Checkbox row inside the two-column grid. `.form label` sets column flex
     (label above field) and outranks a single class, which left the box
     stranded above its own text - so this needs the same two-class weight to
     win. Matches the VNC checkbox row: box left, label beside it. */
  .form label.inline-check {
    display: flex;
    flex-direction: row;
    align-items: flex-start;
    gap: 0.45rem;
    text-align: left;
    cursor: pointer;
  }
  .form label.inline-check input[type="checkbox"] {
    width: auto;
    flex: none;
    margin-top: 0.1rem;
  }
  .inline-check-text {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    /* Same size as the VNC checkbox row rather than the smaller field labels -
       this is a statement to read, not a field caption. */
    font-size: 0.85rem;
    color: var(--text);
  }
  .inline-check-text .field-note { font-size: 0.78rem; }

  /* Toggle switch (matches a modern on/off control). */
  .vnc-switch {
    position: relative;
    width: 34px;
    height: 18px;
    border-radius: 9px;
    border: 1px solid var(--border);
    background: var(--surface2);
    padding: 0;
    cursor: pointer;
    flex: 0 0 auto;
    transition: background 0.15s ease, border-color 0.15s ease;
  }
  .vnc-section.on .vnc-switch {
    background: var(--accent, #4a8);
    border-color: var(--accent, #4a8);
  }
  .vnc-knob {
    position: absolute;
    top: 1px;
    left: 1px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--text1);
    transition: transform 0.15s ease;
  }
  .vnc-section.on .vnc-knob {
    transform: translateX(16px);
    background: #fff;
  }
  .vnc-open {
    font-size: 0.78rem;
    padding: 0.2rem 0.6rem;
    background: var(--surface2);
    color: var(--text1);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
  }
  .vnc-open:hover { background: var(--surface3, var(--surface2)); }
  /* VNC quick-launch in the connection header, next to Connect. */
  .vnc-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.82rem;
    padding: 0.35rem 0.7rem;
    background: var(--surface1);
    color: var(--text);
    border: 1px solid var(--surface2);
    border-radius: 4px;
    cursor: pointer;
  }
  .vnc-btn:hover { background: var(--surface2); border-color: var(--overlay0); }
  .open-split { display: inline-flex; }
  .open-split .vnc-btn:has(+ .open-more) { border-top-right-radius: 0; border-bottom-right-radius: 0; }
  .open-split .open-more { border-top-left-radius: 0; border-bottom-left-radius: 0; border-left: none; padding-left: 0.3rem; padding-right: 0.3rem; }
  .vnc-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.6rem;
    margin-top: 0.6rem;
  }
  .vnc-grid .span-2 { grid-column: 1 / -1; }
  /* The credential cell keeps its normal single-column width (same as the
     Name field). The picker fills that width; the compact "From KeePass"
     button sits on its own line just below, right-aligned - so the picker
     width is never touched. */
  .cred-picker-row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 4px;
    margin-top: 4px;
  }
  .cred-picker-row :global(.search-select) {
    width: 100%;
  }
  .kp-btn {
    align-self: flex-end;
    width: auto;
    white-space: nowrap;
    font-size: 0.75rem;
    padding: 0.2rem 0.5rem;
  }
  .kp-badge {
    display: inline-block;
    font-size: 0.7rem;
    color: var(--overlay1, #9399b2);
    margin-top: 0.2rem;
  }
  .inh-hint {
    font-size: 0.7rem;
    color: var(--overlay0);
    margin-top: 0.15rem;
    font-style: italic;
  }
  .inh-hint strong {
    color: var(--subtext0);
    font-weight: 600;
    font-style: normal;
  }

  /* Narrow / phone: single-column form, tighter padding, and relax the
     wide min-widths that would overflow a ~360px viewport. */
  @media (max-width: 640px) {
    .detail { padding: 0.7rem 0.8rem; }
    .form {
      grid-template-columns: 1fr;
      gap: 0.55rem;
    }
    /* Inputs/selects fill the column instead of holding a fixed width. */
    .form :global(input),
    .form :global(select),
    .form :global(textarea) {
      max-width: 100%;
    }
  }
</style>
