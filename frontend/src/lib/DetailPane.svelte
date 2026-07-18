<script lang="ts">
  import { tree, credentials, selection, sessions, paneTabs, view } from "./stores.svelte";
  import { isMobile } from "./platform";
  import { toast } from "./toast.svelte";
  import PasswordInput from "./PasswordInput.svelte";
  import { api, type ResolvedSettings, type JumpHostOverride } from "./api";
  import { showPrompt } from "./promptModal.svelte.ts";
  import JumpChainEditor from "./JumpChainEditor.svelte";
  import PortForwards from "./PortForwards.svelte";
  import BatchPanel from "./BatchPanel.svelte";
  import BatchExecModal from "./BatchExecModal.svelte";
  import ColorPicker from "./ColorPicker.svelte";
  import IconPicker from "./IconPicker.svelte";
  import Icon from "./Icon.svelte";
  import { IconFolder, IconHost, IconLock, IconUser, IconClipboardCopy, IconTerminal, IconMonitor, IconStar } from "./iconMap";
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

  // Stable option list for the credential dropdown - same shape both
  // editor instances need. flatGrouped() returns { cred, label } items
  // where `label` includes the folder path so duplicates by name are
  // disambiguated.
  const credOptions = $derived(
    credentials.flatGrouped().map((g) => ({
      value: g.cred.id,
      label: g.label,
    })),
  );

  const multiCount = $derived(selection.multiCount());
  const folderMultiCount = $derived(selection.folderMultiCount());
  const dynamicMulti = $derived(selection.selectedDynamicEntries());

  const folder = $derived(selection.selectedFolder());
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
  let kpPickerTarget = $state<"folder" | "connection" | null>(null);

  function openKeepassPicker(target: "folder" | "connection") {
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
      if (kpPickerTarget === "folder" && editingFolder) {
        editingFolder = { ...editingFolder, authRef: cred.id };
      } else if (kpPickerTarget === "connection" && editing) {
        editing = { ...editing, authRef: cred.id };
      }
      toast.ok(`Using KeePass entry "${r.name}"`);
    } catch (e) {
      toast.err("KeePass: " + errMsg(e));
    } finally {
      kpPickerTarget = null;
    }
  }

  // Bitwarden item-picker modal, same shape as the KeePass one.
  let bwPickerOpen = $state(false);
  let bwPickerTarget = $state<"folder" | "connection" | null>(null);

  function openBitwardenPicker(target: "folder" | "connection") {
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
      if (bwPickerTarget === "folder" && editingFolder) {
        editingFolder = { ...editingFolder, authRef: cred.id };
      } else if (bwPickerTarget === "connection" && editing) {
        editing = { ...editing, authRef: cred.id };
      }
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
    jumpHost: JumpHostOverride | undefined;
    colorTag: string;
    autoReconnect: string;
    verbose: string;
    keepalive: string;
    networkProfile: string;
    initialCommand: string;
  } | null>(null);

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

  $effect(() => {
    if (folder) {
      editingFolder = {
        name: folder.name,
        username: folder.settings.username ?? "",
        port: folder.settings.port?.toString() ?? "",
        authRef: folder.settings.auth_ref ?? "",
        jumpHost: folder.settings.jump_host,
        colorTag: folder.settings.color_tag ?? "",
        autoReconnect: encodeBool(folder.settings.auto_reconnect),
        verbose: encodeBool(folder.settings.verbose),
        keepalive: folder.settings.keepalive_interval !== undefined
          ? String(folder.settings.keepalive_interval)
          : "",
        networkProfile: encodeNetProfile(folder.settings.network_profile_id),
        initialCommand: folder.settings.initial_command ?? "",
      };
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
      editingFolder.colorTag !== (s.color_tag ?? "") ||
      editingFolder.autoReconnect !== encodeBool(s.auto_reconnect) ||
      editingFolder.verbose !== encodeBool(s.verbose) ||
      numText(editingFolder.keepalive) !== (s.keepalive_interval !== undefined ? String(s.keepalive_interval) : "") ||
      editingFolder.networkProfile !== encodeNetProfile(s.network_profile_id) ||
      editingFolder.initialCommand !== (s.initial_command ?? "") ||
      JSON.stringify(editingFolder.jumpHost ?? null) !== JSON.stringify(s.jump_host ?? null)
    );
  });

  let folderSaving = false;
  async function saveFolder() {
    if (!folder || !editingFolder || folderSaving) return;
    folderSaving = true;
    const settings = { ...folder.settings };
    settings.username = editingFolder.username.trim() || undefined;
    {
      const p = numText(editingFolder.port);
      const n = p === "" ? NaN : parseInt(p, 10);
      settings.port = isNaN(n) ? undefined : n;
    }
    settings.auth_ref = editingFolder.authRef || undefined;
    settings.jump_host = editingFolder.jumpHost;
    settings.color_tag = editingFolder.colorTag || undefined;
    settings.auto_reconnect = decodeBool(editingFolder.autoReconnect);
    settings.verbose = decodeBool(editingFolder.verbose);
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
    try {
      await api.foldersUpdate({ id: folder.id, name: editingFolder.name, settings });
      await tree.load();
      showSaved();
    } catch (e: any) {
      toast.err(`Save failed: ${errMsg(e)}`);
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
    selection.select({ kind: "folder", id: (created as any).id ?? folder.id });
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
  let resolveErr = $state<string | null>(null);
  let editing = $state<{
    name: string;
    hostname: string;
    notes: string;
    username: string;
    port: string;
    authRef: string;
    jumpHost: JumpHostOverride | undefined;
    colorTag: string;
    autoReconnect: string;
    verbose: string;
    keepalive: string;
    vncEnabled: string;
    vncPort: string;
    vncTunnel: string;
    networkProfile: string;
    initialCommand: string;
    localShellKind: string;
    tags: string[];
  } | null>(null);
  let newTagInput = $state("");

  // A local-shell connection ("telnet", serial console, "claude", ...):
  // no SSH host/port/auth/jump/VNC - just a shell kind + a command. The
  // editor hides the SSH-only fields when this is true.
  const isLocal = $derived(conn?.protocol === "local");

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

  // Walk a JumpHostSpec linked list (outer = closest to target,
  // innermost via = furthest bastion) and return bastion-first
  // hostnames. Same shape resolved settings emit.
  function jumpChainNames(spec: any): string[] {
    if (!spec) return [];
    const out: string[] = [];
    let cur = spec.via;
    while (cur) {
      if (cur.hostname) out.unshift(cur.hostname);
      cur = cur.via;
    }
    if (spec.hostname) out.push(spec.hostname);
    return out;
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
      editing = {
        name: conn.name,
        hostname: conn.hostname,
        notes: conn.notes,
        username: conn.overrides?.username ?? "",
        port: conn.overrides?.port?.toString() ?? "",
        authRef: conn.overrides?.auth_ref ?? "",
        jumpHost: conn.overrides?.jump_host,
        colorTag: conn.overrides?.color_tag ?? "",
        autoReconnect: encodeBool(conn.overrides?.auto_reconnect),
        verbose: encodeBool(conn.overrides?.verbose),
        keepalive: conn.overrides?.keepalive_interval !== undefined
          ? String(conn.overrides.keepalive_interval)
          : "",
        vncEnabled: encodeBool(conn.overrides?.vnc_enabled),
        vncPort: conn.overrides?.vnc_port !== undefined
          ? String(conn.overrides.vnc_port)
          : "",
        vncTunnel: encodeBool(conn.overrides?.vnc_use_tunnel),
        networkProfile: encodeNetProfile(conn.overrides?.network_profile_id),
        initialCommand: conn.overrides?.initial_command ?? "",
        localShellKind: conn.local_shell_kind ?? "",
        tags: [...(conn.tags ?? [])],
      };
      newTagInput = "";
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
      editing.authRef !== (o.auth_ref ?? "") ||
      editing.colorTag !== (o.color_tag ?? "") ||
      editing.autoReconnect !== encodeBool(o.auto_reconnect) ||
      editing.verbose !== encodeBool(o.verbose) ||
      numText(editing.keepalive) !== (o.keepalive_interval !== undefined ? String(o.keepalive_interval) : "") ||
      editing.vncEnabled !== encodeBool(o.vnc_enabled) ||
      numText(editing.vncPort) !== (o.vnc_port !== undefined ? String(o.vnc_port) : "") ||
      editing.vncTunnel !== encodeBool(o.vnc_use_tunnel) ||
      editing.networkProfile !== encodeNetProfile(o.network_profile_id) ||
      editing.initialCommand !== (o.initial_command ?? "") ||
      editing.localShellKind !== (conn.local_shell_kind ?? "") ||
      JSON.stringify(editing.jumpHost ?? null) !== JSON.stringify(o.jump_host ?? null) ||
      !tagsEq
    );
  });

  function addTag() {
    if (!editing) return;
    const v = newTagInput.trim();
    if (!v) return;
    if (editing.tags.includes(v)) { newTagInput = ""; return; }
    editing = { ...editing, tags: [...editing.tags, v] };
    newTagInput = "";
  }
  function removeTag(tag: string) {
    if (!editing) return;
    editing = { ...editing, tags: editing.tags.filter((t) => t !== tag) };
  }

  async function saveConn() {
    if (!conn || !editing) return;
    const overrides = { ...conn.overrides };
    overrides.username = editing.username.trim() || undefined;
    {
      const p = numText(editing.port);
      const n = p === "" ? NaN : parseInt(p, 10);
      overrides.port = isNaN(n) ? undefined : n;
    }
    overrides.auth_ref = editing.authRef || undefined;
    overrides.jump_host = editing.jumpHost;
    overrides.color_tag = editing.colorTag || undefined;
    overrides.auto_reconnect = decodeBool(editing.autoReconnect);
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
    overrides.network_profile_id = decodeNetProfile(editing.networkProfile);
    overrides.initial_command = editing.initialCommand.trim() || undefined;
    const localKind = editing.localShellKind.trim();
    await api.connectionsUpdate({
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
    }).then(async () => {
      await tree.load();
      if (conn) {
        resolved = await api.connectionsResolve(conn.id);
      }
      showSaved();
    }).catch((e) => {
      toast.err(`Save failed: ${errMsg(e)}`);
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
  let deleteItems = $state<Array<{ kind: "folder" | "connection"; name: string; detail?: string }>>([]);
  let deletePending: (() => Promise<void>) | null = null;

  // Walk a folder subtree to collect the names of everything that
  // cascades. The list shown to the user mirrors what the SQLite FK
  // ON DELETE CASCADE will actually nuke.
  function collectFolderVictims(
    folderId: string,
    out: Array<{ kind: "folder" | "connection"; name: string; detail?: string }>
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
      out.push({ kind: "connection", name: c.name, detail: c.hostname });
    }
    for (const sub of childFolders) {
      collectFolderVictims(sub.id, out);
    }
  }

  function openFolderDelete(folderIds: string[]) {
    const items: typeof deleteItems = [];
    for (const id of folderIds) collectFolderVictims(id, items);
    deleteItems = items;
    deletePending = async () => {
      // Top-level folders only - children are cascade-deleted by the DB.
      for (const id of folderIds) {
        try { await api.foldersDelete(id); } catch {}
      }
      selection.select({ kind: "none" });
      await tree.load();
    };
  }

  function openConnDelete(connIds: string[]) {
    const items: typeof deleteItems = [];
    for (const id of connIds) {
      const c = tree.connectionById(id);
      if (c) items.push({ kind: "connection", name: c.name, detail: c.hostname });
    }
    deleteItems = items;
    deletePending = async () => {
      for (const id of connIds) {
        try { await api.connectionsDelete(id); } catch {}
      }
      selection.select({ kind: "none" });
      await tree.load();
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

  let connecting = $state(false);
  let connectStage = $state<string | null>(null);
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

  // Subscribe to live connect-progress events while a connect is
  // in flight. Backend emits `connect_progress:<connectionID>` with
  // short stage strings ("TCP dial bastion1", "SSH handshake target",
  // "Opening shell") so the spinner can say what it's currently
  // stuck on. Cleanup on conn change or once connecting ends.
  $effect(() => {
    if (!connecting || !conn) { connectStage = null; return; }
    const un = EventsOn(`connect_progress:${conn.id}`, (stage: string) => {
      connectStage = stage;
    });
    return () => { un(); connectStage = null; };
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
      await navigator.clipboard.writeText(cmd);
      copiedHint = "Copied: " + cmd;
      setTimeout(() => { copiedHint = null; }, 3500);
    } catch (e: any) {
      copiedHint = "Error: " + (e?.message ?? e);
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
      copiedHint = "Launch failed: " + (e?.message ?? e);
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
        await navigator.clipboard.writeText(pw);
        copiedHint = "Password copied (clears in 30s)";
        // Self-clearing clipboard. Schedule a wipe; if the user copied
        // something else in between, leave it alone.
        setTimeout(async () => {
          try {
            const cur = await navigator.clipboard.readText();
            if (cur === pw) await navigator.clipboard.writeText("");
          } catch { /* best-effort */ }
        }, 30_000);
      } else {
        const info = await api.connectionCopyInfo(conn.id);
        const val = field === "username" ? info.username : info.hostname;
        if (!val) {
          copiedHint = `${field === "username" ? "Username" : "Host"}: (empty)`;
        } else {
          await navigator.clipboard.writeText(val);
          copiedHint = `${field === "username" ? "Username" : "Host"} copied`;
        }
      }
      setTimeout(() => { copiedHint = null; }, 3000);
    } catch (e: any) {
      copiedHint = "Error: " + (e?.message ?? e);
      setTimeout(() => { copiedHint = null; }, 4000);
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
    if (!conn) return;
    connecting = true;
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
      connecting = false;
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
    <div class="empty">
      <p>Select a folder or connection on the left.</p>
    </div>
  {:else if folder && editingFolder}
    <header>
      <h1><IconFolder size={18} /> <input class="title-input" bind:value={editingFolder.name} /></h1>
      <div class="head-actions">
        <button class="primary save-btn" class:dirty={folderDirty} onclick={saveFolder}>
          {#if savedHint && !folderDirty}✓ Saved{:else if folderDirty}Save *{:else}Save{/if}
        </button>
        <button onclick={addSubfolder} title="New subfolder">+ Folder</button>
        <button onclick={addConnectionHere} title="New connection here">+ Connection</button>
        <button class="danger" onclick={deleteFolder}>Delete</button>
      </div>
    </header>

    <div class="form">
      <p class="section-label span-2">Inherited settings - applied to all connections in this folder unless overridden</p>

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
            options={credOptions}
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

      <div class="span-2"><JumpChainEditor
        value={editingFolder.jumpHost}
        onChange={(v) => { if (editingFolder) editingFolder = { ...editingFolder, jumpHost: v }; }}
      /></div>

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
          fallbackEmoji="📁"
          onChange={() => tree.load()}
          onNamedChange={() => tree.load()}
        /></div>
      {/if}

      <label>Auto-reconnect
        <select bind:value={editingFolder.autoReconnect}>
          <option value="">(inherit from parent)</option>
          <option value="on">On - retry with backoff after drop</option>
          <option value="off">Off - drop = closed</option>
        </select>
      </label>

      <label>Verbose connect log
        <select bind:value={editingFolder.verbose}>
          <option value="">(inherit from parent)</option>
          <option value="on">On - show TCP / handshake / auth diagnostics</option>
          <option value="off">Off</option>
        </select>
      </label>

      <label title="Route the first SSH hop of every connection in this folder through a userspace WireGuard tunnel.">
        Network (inherited by children)
        <select bind:value={editingFolder.networkProfile}>
          <option value="">(inherit from parent)</option>
          <option value="__direct__">Direct - no tunnel</option>
          {#each networkProfiles.list as np (np.id)}
            <option value={np.id}>via {np.name} ({np.kind === "netbird" ? "NetBird" : "WireGuard"})</option>
          {/each}
        </select>
        <span class="field-note">
          {networkProfiles.list.length === 0 ? "No network profiles yet - " : "Network profiles live in "}
          <button class="linklike" onclick={(e) => { e.preventDefault(); view.setTabSettingsSection("network"); }}>
            Settings -&gt; Network profiles</button>.
        </span>
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

      <label class="span-2" title="A command run in the shell right after connect - e.g. cd /var/www, tmux new -A -s main, source venv/bin/activate. Inherited by connections; blank inherits the parent folder.">
        Initial command
        <input
          bind:value={editingFolder.initialCommand}
          placeholder="(inherit) e.g. cd /var/www"
        />
        <span class="field-note">Run in the shell on connect. Inherited by connections. Blank = inherit.</span>
      </label>

      {#if savedHint}
        <div class="span-2 save-row"><span class="saved-pill">✓ {savedHint}</span></div>
      {/if}
    </div>
  {:else if conn && editing}
    <header>
      <h1>
        <button
          class="fav-toggle"
          class:active={conn.favorite}
          title={conn.favorite ? "Remove from favourites" : "Mark as favourite"}
          onclick={toggleFavorite}
        ><IconStar size={16} fill={conn.favorite ? "currentColor" : "none"} /></button>
        <Icon imageId={conn.icon_image_id} iconName={conn.icon_name} iconColor={conn.icon_color} size={18}>
          {#if isLocal}<IconTerminal size={18} />{:else}<IconHost size={18} />{/if}
        </Icon>
        {conn.name}
      </h1>
      <div class="head-actions">
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
        <button class="primary" disabled={connecting} onclick={connect}>
          {connecting ? (connectStage ?? "Connecting…") : (overrideCredId ? "Connect (override)" : "Connect")}
        </button>
        {#if conn.overrides?.vnc_enabled}
          <button class="vnc-btn" title="Open the VNC console for this host" onclick={openVncConsole}>
            <IconMonitor size={13} /> VNC
          </button>
        {/if}
        {#if connecting}
          <!-- Abort a connect stuck on opkssh OIDC login (closed browser /
               wrong config) without restarting the app. -->
          <button class="ghost cancel-connect" title="Cancel the connection attempt" onclick={cancelConnect}>
            Cancel
          </button>
        {/if}
        {#if !isLocal}
          <button
            class="ghost"
            title="Use a different credential just for the next connect attempt"
            onclick={() => (showOverride = !showOverride)}
          >
            {showOverride ? "✕" : "Use different credential…"}
          </button>
        {/if}
        <button class="danger" onclick={deleteConn}>Delete</button>
      </div>
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
    <div class="form">
      <label title="A label for this connection in the tree - any text you like.">Name<input bind:value={editing.name} placeholder="My server" /></label>
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
      {:else}
        <label title="The address to connect to: a DNS hostname or an IP. This is what SSH dials, not the display name above.">Hostname / IP address<input bind:value={editing.hostname} placeholder="host.example.com or 10.0.0.5" /></label>
      {/if}
      {#if !isLocal}
      <div class="row">
        <label class="grow">
          Username
          <input
            bind:value={editing.username}
            placeholder={inhUser.value ? String(inhUser.value) : "(no inherited value)"}
          />
          {#if inhUser.from && !editing.username}
            <span class="inh-hint">inherited from <strong>{inhUser.from.name}</strong>: {inhUser.value}</span>
          {/if}
        </label>
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
            <span class="inh-hint">inherited from <strong>{inhPort.from.name}</strong>: {inhPort.value}</span>
          {/if}
        </label>
      </div>
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
      </label>
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
        {#if passwordHasValue}
          <span class="pass-hint">Password saved - will be used for auth</span>
        {/if}
      </label>
      <div class="span-2">
        <JumpChainEditor value={editing.jumpHost} onChange={(v) => { if (editing) editing = { ...editing, jumpHost: v }; }} />
        {#if inhJump.from && !editing.jumpHost}
          {@const inhChain = jumpChainNames((inhJump.value as any)?.chain)}
          <span class="inh-hint">
            inherited from <strong>{inhJump.from.name}</strong>:
            {#if inhChain.length > 0}
              via {inhChain.join(" → ")}
            {:else if (inhJump.value as any)?.kind === "none"}
              no jump (cleared)
            {:else}
              (empty)
            {/if}
          </span>
        {/if}
      </div>
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
        fallbackEmoji="🖥"
        onChange={() => tree.load()}
        onNamedChange={() => tree.load()}
      /></div>

      {#if !isLocal}
      <label>Auto-reconnect
        <select bind:value={editing.autoReconnect}>
          <option value="">(inherit from folder)</option>
          <option value="on">On - retry with backoff after drop</option>
          <option value="off">Off - drop = closed</option>
        </select>
      </label>
      <label>Verbose connect log
        <select bind:value={editing.verbose}>
          <option value="">(inherit from folder)</option>
          <option value="on">On - show TCP / handshake / auth diagnostics</option>
          <option value="off">Off</option>
        </select>
      </label>
      <label title="Route the first SSH hop through a userspace WireGuard tunnel (no TUN adapter, no admin rights).">
        Network
        <select bind:value={editing.networkProfile}>
          <option value="">(inherit from folder)</option>
          <option value="__direct__">Direct - no tunnel</option>
          {#each networkProfiles.list as np (np.id)}
            <option value={np.id}>via {np.name} ({np.kind === "netbird" ? "NetBird" : "WireGuard"})</option>
          {/each}
        </select>
        <span class="field-note">
          {networkProfiles.list.length === 0 ? "No network profiles yet - " : "Network profiles live in "}
          <button class="linklike" onclick={(e) => { e.preventDefault(); view.setTabSettingsSection("network"); }}>
            Settings -&gt; Network profiles</button>.
        </span>
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
      {/if}

      <label class="span-2" title="A command run in the shell right after connect - e.g. cd /var/www, tmux new -A -s main, source venv/bin/activate. Blank inherits the folder's value.">
        {isLocal ? "Command" : "Initial command"}
        <input
          bind:value={editing.initialCommand}
          placeholder={isLocal ? "e.g. telnet 10.0.0.5   ·   claude   ·   screen /dev/ttyUSB0" : "(inherit) e.g. cd /var/www"}
        />
        <span class="field-note">
          {#if isLocal}
            The command this connection runs in the shell (the point of a local connection). Blank = just an interactive shell.
          {:else}
            Run in the shell on connect. Blank = inherit folder.
          {/if}
        </span>
      </label>

      {#if !isMobile && !isLocal}
      <div class="span-2 vnc-section" class:on={editing.vncEnabled === "on"}>
        <div class="vnc-head">
          <button type="button"
            class="vnc-switch"
            role="switch"
            aria-label="Enable VNC console"
            aria-checked={editing.vncEnabled === "on"}
            onclick={() => { if (editing) editing = { ...editing, vncEnabled: editing.vncEnabled === "on" ? "off" : "on" }; }}
          >
            <span class="vnc-knob"></span>
          </button>
          <span class="vnc-title">VNC console</span>
          <span class="vnc-spacer"></span>
          {#if editing.vncEnabled === "on"}
            <button type="button" class="vnc-open" onclick={openVncConsole}>Open console</button>
          {/if}
        </div>
        {#if editing.vncEnabled === "on"}
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
      {/if}

      <div class="tag-editor span-2">
        <span class="tag-label">Tags</span>
        <div class="tag-row">
          {#each editing.tags as t (t)}
            <span class="tag-chip">
              {t}
              <button type="button" class="tag-x" onclick={() => removeTag(t)} title="Remove tag">×</button>
            </span>
          {/each}
          <input
            class="tag-input"
            bind:value={newTagInput}
            placeholder="add tag and press Enter"
            onkeydown={(e) => { if (e.key === "Enter") { e.preventDefault(); addTag(); } }}
            onblur={addTag}
          />
        </div>
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
        </div>
      {/if}
    </div>

    {#if !isLocal}
    <PortForwards connection={conn} />

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

    {#if !isLocal}
    <details class="resolved" open>
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
              <td>{ovr.auth_ref ? "override" : "inherited"}</td>
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
  .empty { color: var(--overlay0); margin-top: 4rem; text-align: center; }
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
  .head-actions { display: flex; gap: 0.5rem; }
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
  .tag-row {
    display: flex; flex-wrap: wrap; gap: 0.3rem; align-items: center;
    padding: 0.35rem 0.5rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    min-height: 2rem;
  }
  .tag-chip {
    display: inline-flex; align-items: center; gap: 0.25rem;
    background: var(--surface0); color: var(--text);
    padding: 0.15rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
  }
  .tag-x {
    background: transparent; border: 0; color: var(--overlay0);
    cursor: pointer; padding: 0; font-size: 0.9rem; line-height: 1;
  }
  .tag-x:hover { color: var(--red); }
  .tag-input {
    flex: 1; min-width: 8rem;
    background: transparent; border: 0; color: var(--text);
    font: inherit; font-size: 0.82rem;
    padding: 0.15rem 0;
  }
  .tag-input:focus { outline: none; }

  .quick-actions {
    margin: 1rem 0;
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
    word-break: break-all;
  }
  .pass-row { align-items: center; }
  /* The password field is now a PasswordInput wrapper, not a bare input. */
  .pass-row > :global(.pw-wrap) { flex: 1; min-width: 0; }
  .pass-row button { flex-shrink: 0; }
  .pass-hint { font-size: 0.74rem; color: var(--green); margin-top: 0.15rem; }

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
