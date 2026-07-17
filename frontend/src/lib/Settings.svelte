<script lang="ts">
  import { onMount } from "svelte";
  import { errMsg } from "./connectErrors";
  import { isMobile } from "./platform";
  import PasswordInput from "./PasswordInput.svelte";
  import McpGrantsList from "./McpGrantsList.svelte";
  import KeepassSettings from "./KeepassSettings.svelte";
  import BitwardenSettings from "./BitwardenSettings.svelte";
  import InfisicalSettings from "./InfisicalSettings.svelte";
  import { api, type RdmImportSummary, type ImportSummary as ArcImportSummary, type SshConfigImportSummary, type MobaXtermImportSummary, type PuttyImportSummary, type Snippet, type SnippetInput, type BackupInfo, type AutoBackupPrefs, type SyncConfig, type SyncStatusResult, type NetworkProfileInfo } from "./api";
  import { networkProfiles } from "./networkProfiles.svelte";
  import { tree, credentials, paneTabs, view, sessions } from "./stores.svelte";
  import FolderPicker from "./FolderPicker.svelte";
  import type { Folder } from "./api";
  import { copyPastePrefs, type CopyPasteMode } from "./copyPastePrefs.svelte";
  import { terminalPrefs, DEFAULT_FONT_FAMILY, DEFAULT_SCROLLBACK } from "./terminalPrefs.svelte";
  import { appPrefs } from "./appPrefs.svelte";
  import { vaultPrefs } from "./vaultPrefs.svelte";
  import { lastSession } from "./lastSession.svelte";
  import { workspaces } from "./workspaces.svelte";
  import { deepLink } from "./deepLink.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { MCP_SYSTEM_PROMPT, MCP_SYSTEM_PROMPT_HINT } from "./mcpSystemPrompt";
  import { copyText } from "./clipboard";
  import { localShellPrefs } from "./localShellPrefs.svelte.ts";
  import { recordingsModal } from "./recording.svelte";
  import { syncState } from "./syncState.svelte";
  import { themes } from "./themes";
  import LogViewer from "./LogViewer.svelte";
  import UpdateModal from "./UpdateModal.svelte";
  import SearchableSelect from "./SearchableSelect.svelte";
  import { updateCheck } from "./updateCheck.svelte";

  let browserPath = $state("");
  let browserPersistent = $state(false);

  async function toggleBrowserPersistent(on: boolean) {
    browserPersistent = on;
    try {
      await api.settingsSet("browser_persistent_profile", on ? "true" : "false");
    } catch (e) {
      console.warn("browser_persistent_profile set:", e);
    }
  }
  let savedPath = $state("");
  let savedAt = $state<string | null>(null);

  // Connect timeout - app-wide seconds. Empty / 0 = use default (20s).
  let connectTimeoutSeconds = $state<number>(20);
  let connectTimeoutSaved = $state(false);
  let closeToTray = $state<boolean>(false);
  let minimizeToTray = $state<boolean>(false);

  // LLM (MCP) bridge access.
  let mcpEnabled = $state<boolean>(false);
  let mcpTcp = $state<boolean>(false);
  let notificationsEnabled = $state<boolean>(true);
  let mcpAuditEnabled = $state<boolean>(true);
  let mcpAuditOutput = $state<boolean>(false);
  let recordingConfirm = $state<boolean>(true);
  let shareEnabled = $state<boolean>(false);
  let shareAuditOutput = $state<boolean>(false);
  let shareFingerprint = $state<string>("");
  let shareFingerprintShort = $state<string>("");
  let mcpReadonlyExtra = $state<string>("");
  let vaultSidecarStrength = $state<"strong" | "weak" | "none" | "">("");
  let mcpExePath = $state<string>("");
  // JSON-safe form of the exe path for the LM Studio mcp.json block: backslashes
  // in a Windows path (C:\...) must be doubled or the JSON is invalid.
  const mcpExeJson = $derived(JSON.stringify(mcpExePath || "ssh-tool"));
  let mcpWslExePath = $state<string>("");
  let externalTerminal = $state<"windowsterminal" | "powershell" | "cmd" | "wsl">("windowsterminal");

  // Per-platform list of in-app local shells the radio shows. Matches
  // the dropdown in App.svelte so Settings and the top-bar menu stay
  // in sync - no Linux user should see a "WSL" radio.
  const settingsPlatform = (typeof navigator !== "undefined"
    ? navigator.userAgent.toLowerCase()
    : "");
  const settingsIsWin = settingsPlatform.includes("windows");
  const settingsIsMac = settingsPlatform.includes("mac");
  const localShellChoices: { kind: "" | "wsl" | "powershell" | "cmd" | "bash" | "zsh" | "sh"; name: string; desc: string }[] = [
    { kind: "", name: "Auto", desc: "Backend picks the first available shell for this platform." },
    ...(settingsIsWin
      ? [
          { kind: "wsl"        as const, name: "WSL (default distro)", desc: "Runs wsl.exe against the user's default distro." },
          { kind: "powershell" as const, name: "PowerShell",            desc: "powershell.exe." },
          { kind: "cmd"        as const, name: "Command Prompt",        desc: "cmd.exe." },
        ]
      : settingsIsMac
        ? [
            { kind: "zsh"  as const, name: "zsh",  desc: "Looked up on $PATH." },
            { kind: "bash" as const, name: "bash", desc: "Looked up on $PATH." },
          ]
        : [
            { kind: "bash" as const, name: "bash", desc: "Looked up on $PATH." },
            { kind: "zsh"  as const, name: "zsh",  desc: "Looked up on $PATH." },
            { kind: "sh"   as const, name: "sh",   desc: "POSIX shell fallback." },
          ]),
  ];
  let updateCheckDisabled = $state<boolean>(false);
  let updateBusy = $state(false);
  let updateError = $state<string | null>(null);
  let updateMsg = $state<string | null>(null);
  let updateModalOpen = $state(false);

  async function onCheckUpdates() {
    updateBusy = true;
    updateError = null;
    updateMsg = null;
    try {
      // Refresh the shared updateCheck store so the modal renders
      // fresh data and the status-bar pill stays in sync.
      await updateCheck.run();
      if (updateCheck.lastError) {
        updateError = updateCheck.lastError;
        return;
      }
      if (updateCheck.available) {
        updateModalOpen = true;
      } else {
        updateMsg = `You're on the latest version (${updateCheck.latest || updateCheck.current}).`;
      }
    } catch (e: any) {
      updateError = errMsg(e);
    } finally {
      updateBusy = false;
    }
  }

  let logDirPath = $state<string>("");
  let recordingsDirPath = $state<string>("");

  async function changeRecordingsDir() {
    try {
      const picked = await api.recordingsPickDir();
      if (!picked) return;
      await api.settingsSet("recordings_dir", picked);
      recordingsDirPath = picked;
      toast.ok("Recordings folder updated");
    } catch (e) {
      toast.err(errMsg(e));
    }
  }

  async function resetRecordingsDir() {
    try {
      await api.settingsDelete("recordings_dir");
      recordingsDirPath = (await api.recordingsDir()) ?? "";
      toast.ok("Recordings folder reset to default");
    } catch (e) {
      toast.err(errMsg(e));
    }
  }
  let versionInfo = $state<{ name: string; version: string; commit: string; schema_version: number } | null>(null);
  // Profile statistics for the About section. profileStats() counts
  // straight from the DB (incl. resolved VNC, configured forwards +
  // bookmarks, cached dynamic entries by kind); active tunnels and
  // session counts are runtime values on top of that.
  let profileStats = $state<import("./api").ProfileStats | null>(null);
  let activeForwards = $state<number | null>(null);
  const connectedSessions = $derived(
    sessions.tabs.filter((s) => s.status === "connected").length
  );

  // --- Workspaces state --------------------------------------------------
  let wsBusyId = $state<string | null>(null);
  let wsErr = $state<string | null>(null);

  async function reloadWorkspaces() {
    wsErr = null;
    try { await workspaces.load(); }
    catch (e: any) { wsErr = errMsg(e); }
  }
  async function wsOpen(id: string) {
    wsErr = null; wsBusyId = id;
    try { await workspaces.open(id); }
    catch (e: any) { wsErr = errMsg(e); }
    finally { wsBusyId = null; }
  }
  async function wsOverwrite(id: string, name: string) {
    const ok = await showConfirm({
      title: "Overwrite workspace",
      message: `Overwrite "${name}" with the current tab layout?`,
      okLabel: "Overwrite",
    });
    if (!ok) return;
    wsErr = null; wsBusyId = id;
    try { await workspaces.overwrite(id, name); }
    catch (e: any) { wsErr = errMsg(e); }
    finally { wsBusyId = null; }
  }
  async function wsDelete(id: string, name: string) {
    const ok = await showConfirm({
      title: "Delete workspace",
      message: `Delete workspace "${name}"?`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    wsErr = null;
    try { await workspaces.delete(id); }
    catch (e: any) { wsErr = errMsg(e); }
  }
  async function wsSaveNew() {
    const name = await showPrompt("New workspace name?");
    if (!name?.trim()) return;
    wsErr = null;
    try { await workspaces.saveCurrentAs(name.trim()); }
    catch (e: any) { wsErr = errMsg(e); }
  }

  // --- Snippets state ----------------------------------------------------
  let snippets = $state<Snippet[]>([]);
  let snippetEditing = $state<Snippet | null>(null);
  let snippetForm = $state<SnippetInput>({ name: "", body: "", tags: [], connection_id: null });
  let snippetTagsRaw = $state(""); // comma-separated for the input
  let snippetErr = $state<string | null>(null);
  let snippetBusy = $state(false);

  async function reloadSnippets() {
    try {
      snippets = (await api.snippetsList("")) ?? [];
    } catch (e: any) {
      snippetErr = errMsg(e);
    }
  }

  function newSnippet() {
    snippetEditing = null;
    snippetForm = { name: "", body: "", tags: [], connection_id: null };
    snippetTagsRaw = "";
    snippetErr = null;
  }

  function editSnippet(s: Snippet) {
    snippetEditing = s;
    snippetForm = {
      name: s.name,
      body: s.body,
      tags: [...(s.tags ?? [])],
      connection_id: s.connection_id ?? null,
    };
    snippetTagsRaw = (s.tags ?? []).join(", ");
    snippetErr = null;
  }

  async function saveSnippet() {
    snippetErr = null;
    if (!snippetForm.name.trim()) { snippetErr = "Name required"; return; }
    if (!snippetForm.body) { snippetErr = "Body required"; return; }
    const tags = snippetTagsRaw.split(",").map((t) => t.trim()).filter(Boolean);
    const input: SnippetInput = {
      name: snippetForm.name.trim(),
      body: snippetForm.body,
      tags,
      connection_id: snippetForm.connection_id || null,
    };
    snippetBusy = true;
    try {
      if (snippetEditing) {
        await api.snippetUpdate(snippetEditing.id, input);
      } else {
        await api.snippetCreate(input);
      }
      await reloadSnippets();
      newSnippet();
    } catch (e: any) {
      snippetErr = errMsg(e);
    } finally {
      snippetBusy = false;
    }
  }

  async function deleteSnippet(s: Snippet) {
    const ok = await showConfirm({
      title: "Delete snippet",
      message: `Delete snippet "${s.name}"?`,
      okLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.snippetDelete(s.id);
      if (snippetEditing?.id === s.id) newSnippet();
      await reloadSnippets();
    } catch (e: any) {
      snippetErr = errMsg(e);
    }
  }

  onMount(async () => {
    try {
      const v = await api.settingsGet("preferred_browser_path");
      browserPath = v ?? "";
      savedPath = v ?? "";
      browserPersistent = (await api.settingsGet("browser_persistent_profile")) === "true";
    } catch (e) {
      console.warn("settings load:", e);
    }
    try {
      const raw = await api.settingsGet("connect_timeout_seconds");
      const parsed = parseInt(raw ?? "", 10);
      if (!isNaN(parsed) && parsed > 0) connectTimeoutSeconds = parsed;
    } catch { /* leave default */ }
    try {
      const v = await api.settingsGet("close_to_tray");
      closeToTray = v === "1" || v === "true";
    } catch { /* default false */ }
    try {
      const v = await api.settingsGet("minimize_to_tray");
      minimizeToTray = v === "1" || v === "true";
    } catch { /* default false */ }
    try {
      const v = await api.settingsGet("external_terminal_kind");
      if (
        v === "powershell" ||
        v === "cmd" ||
        v === "windowsterminal" ||
        v === "wsl"
      ) {
        externalTerminal = v;
      }
    } catch { /* default windowsterminal */ }
    try {
      const v = await api.settingsGet("update_check_disabled");
      updateCheckDisabled = v === "1" || v === "true";
    } catch { /* default enabled */ }
    try { logDirPath = (await api.logDir()) ?? ""; } catch { /* ignore */ }
    try { recordingsDirPath = (await api.recordingsDir()) ?? ""; } catch { /* ignore */ }
    try { versionInfo = await api.appVersion(); } catch { /* ignore */ }
    // Make sure the terminal prefs are loaded so the radio sits on the
    // right value when this page first opens.
    await copyPastePrefs.load();
    await terminalPrefs.load();
    await vaultPrefs.load();
    await localShellPrefs.load();
    try {
      const v = await api.settingsGet("mcp_bridge_enabled");
      mcpEnabled = v === "1" || v === "true";
    } catch { /* default off */ }
    try {
      const v = await api.settingsGet("mcp_bridge_tcp");
      mcpTcp = v === "1" || v === "true";
    } catch { /* default off */ }
    try { mcpReadonlyExtra = (await api.settingsGet("mcp_readonly_extra")) ?? ""; } catch { /* ignore */ }
    try { mcpExePath = (await api.appExePath()) ?? ""; } catch { /* ignore */ }
    try { mcpWslExePath = (await api.appWslExePath()) ?? ""; } catch { /* ignore */ }
    try {
      const v = await api.settingsGet("notifications_enabled");
      notificationsEnabled = v === "" || v === "1" || v === "true"; // default on
    } catch { /* default on */ }
    try {
      const v = await api.settingsGet("mcp_audit_enabled");
      mcpAuditEnabled = v === "" || v === "1" || v === "true"; // default on
    } catch { /* default on */ }
    try {
      const v = await api.settingsGet("mcp_audit_output");
      mcpAuditOutput = v === "1" || v === "true"; // default OFF
    } catch { /* default off */ }
    try {
      const st = await api.vaultStatus();
      vaultSidecarStrength = st.sidecar_strength ?? "";
    } catch { /* ignore */ }
    try {
      // Stored inverted (recording_confirm_DISABLED) so the safe behaviour is
      // the one you get with no setting written at all.
      const v = await api.settingsGet("recording_confirm_disabled");
      recordingConfirm = !(v === "1" || v === "true"); // default ON
    } catch { /* default on */ }
    try {
      const v = await api.settingsGet("share_enabled");
      shareEnabled = v === "1" || v === "true"; // default OFF
    } catch { /* default off */ }
    try {
      const v = await api.settingsGet("share_audit_output");
      shareAuditOutput = v === "1" || v === "true"; // default OFF
    } catch { /* default off */ }
  });

  async function toggleNotifications(next: boolean) {
    notificationsEnabled = next;
    try { await api.settingsSet("notifications_enabled", next ? "1" : "0"); }
    catch (e) { console.warn("notifications toggle:", e); }
  }

  async function toggleMcpAudit(next: boolean) {
    mcpAuditEnabled = next;
    try { await api.settingsSet("mcp_audit_enabled", next ? "1" : "0"); }
    catch (e) { console.warn("mcp audit toggle:", e); }
  }

  async function toggleMcpAuditOutput(next: boolean) {
    mcpAuditOutput = next;
    try { await api.settingsSet("mcp_audit_output", next ? "1" : "0"); }
    catch (e) { console.warn("mcp audit output toggle:", e); }
  }

  async function toggleRecordingConfirm(e: Event) {
    const next = (e.target as HTMLInputElement).checked;
    recordingConfirm = next;
    // Inverted on the way out: the setting records the OPT-OUT, so an unset
    // key means "ask", which is the behaviour we want by default.
    try { await api.settingsSet("recording_confirm_disabled", next ? "0" : "1"); }
    catch (err) { console.warn("recording confirm toggle:", err); }
  }

  async function toggleMcp(next: boolean) {
    mcpEnabled = next;
    try { await api.settingsSet("mcp_bridge_enabled", next ? "1" : "0"); }
    catch (e) { console.warn("mcp toggle:", e); }
  }

  async function toggleShare(next: boolean) {
    shareEnabled = next;
    try {
      await api.settingsSet("share_enabled", next ? "1" : "0");
      if (next) await loadShareFingerprint();
    } catch (e) { console.warn("share toggle:", e); }
  }

  async function toggleShareAuditOutput(e: Event) {
    const next = (e.target as HTMLInputElement).checked;
    shareAuditOutput = next;
    try { await api.settingsSet("share_audit_output", next ? "1" : "0"); }
    catch (err) { console.warn("share audit output toggle:", err); }
  }

  async function loadShareFingerprint() {
    try {
      const fp = await api.shareFingerprint();
      shareFingerprint = fp.Words;
      shareFingerprintShort = fp.Short;
    } catch (e) { console.warn("share fingerprint:", e); }
  }

  async function regenerateShareCert() {
    if (!(await showConfirm({
      title: "Regenerate sharing certificate?",
      message: "Every guest who saved your current fingerprint will see a different one. Only do this if you think the certificate was compromised.",
      okLabel: "Regenerate",
      danger: true,
    }))) return;
    try {
      const fp = await api.shareRegenerateCert();
      shareFingerprint = fp.Words;
      shareFingerprintShort = fp.Short;
      toast.ok("Certificate regenerated");
    } catch (e) { toast.err("Regenerate failed: " + errMsg(e)); }
  }

  async function toggleMcpTcp(next: boolean) {
    mcpTcp = next;
    try { await api.settingsSet("mcp_bridge_tcp", next ? "1" : "0"); }
    catch (e) { console.warn("mcp tcp toggle:", e); }
  }

  async function saveMcpReadonlyExtra() {
    try { await api.settingsSet("mcp_readonly_extra", mcpReadonlyExtra.trim()); }
    catch (e) { console.warn("mcp allowlist:", e); }
  }

  async function copyMcpSystemPrompt() {
    try {
      await navigator.clipboard.writeText(MCP_SYSTEM_PROMPT);
      toast.ok("System prompt copied. " + MCP_SYSTEM_PROMPT_HINT);
    } catch {
      toast.err("Copy failed - clipboard unavailable");
    }
  }

  async function toggleCloseToTray(next: boolean) {
    closeToTray = next;
    try { await api.settingsSet("close_to_tray", next ? "1" : "0"); }
    catch (e) { console.warn("close_to_tray save:", e); }
  }
  async function toggleMinimizeToTray(next: boolean) {
    minimizeToTray = next;
    try { await api.settingsSet("minimize_to_tray", next ? "1" : "0"); }
    catch (e) { console.warn("minimize_to_tray save:", e); }
  }
  async function setExternalTerminal(next: typeof externalTerminal) {
    externalTerminal = next;
    try { await api.settingsSet("external_terminal_kind", next); }
    catch (e) { console.warn("external_terminal_kind save:", e); }
  }
  async function toggleUpdateCheckDisabled(next: boolean) {
    updateCheckDisabled = next;
    try { await api.settingsSet("update_check_disabled", next ? "1" : "0"); }
    catch (e) { console.warn("update_check_disabled save:", e); }
  }

  async function saveConnectTimeout() {
    const n = Math.max(1, Math.min(300, Math.floor(connectTimeoutSeconds || 0)));
    connectTimeoutSeconds = n;
    await api.settingsSet("connect_timeout_seconds", String(n));
    connectTimeoutSaved = true;
    setTimeout(() => { connectTimeoutSaved = false; }, 1500);
  }

  function pickCopyPaste(m: CopyPasteMode) {
    copyPastePrefs.set(m);
  }

  async function save() {
    const v = browserPath.trim();
    if (v === "") {
      await api.settingsDelete("preferred_browser_path");
    } else {
      await api.settingsSet("preferred_browser_path", v);
    }
    savedPath = v;
    savedAt = new Date().toLocaleTimeString();
  }

  // ----- Sync (encrypted WebDAV snapshots) -----

  let syncCfg = $state<SyncConfig | null>(null);
  let syncUrl = $state("");
  let syncUsername = $state("");
  let syncPassword = $state("");   // blank = keep saved
  let syncPassphrase = $state(""); // blank = keep saved
  let syncBusy = $state(false);
  let syncErr = $state<string | null>(null);
  let syncStatusRes = $state<SyncStatusResult | null>(null);
  let syncPulled = $state(false);

  let syncAuto = $state(false);
  let syncAutoApply = $state(false);
  let syncCheckMinutes = $state(5);

  // Transport: "webdav" (default) or "sftp". SFTP reuses a vault credential
  // from the connection tree for auth.
  let syncTransport = $state("webdav");
  let sftpHost = $state("");
  let sftpPort = $state(22);
  let sftpUser = $state("");
  let sftpDir = $state("");
  let sftpAuthMode = $state("credential"); // "credential" | "inline"
  let sftpCredId = $state("");
  let sftpInlinePassword = $state(""); // blank = keep saved
  let sftpInlineKeyPem = $state("");   // blank = keep saved
  let sftpInlineKeyPassphrase = $state("");
  let syncCredOptions = $state<{ id: string; name: string }[]>([]);

  async function syncLoadConfig() {
    try {
      syncCfg = await api.syncConfigGet();
      syncUrl = syncCfg.url;
      syncUsername = syncCfg.username;
      syncAuto = syncCfg.auto;
      syncAutoApply = syncCfg.auto_apply;
      syncCheckMinutes = syncCfg.check_minutes || 5;
      syncTransport = syncCfg.transport || "webdav";
      sftpHost = syncCfg.sftp_host;
      sftpPort = syncCfg.sftp_port || 22;
      sftpUser = syncCfg.sftp_user;
      sftpDir = syncCfg.sftp_dir;
      sftpAuthMode = syncCfg.sftp_auth_mode || "credential";
      sftpCredId = syncCfg.sftp_cred_id;
    } catch { /* ignore */ }
  }

  // Credential list for the SFTP auth picker. Only password / key / opkssh
  // kinds make sense for an SSH login.
  async function syncLoadCredOptions() {
    try {
      const creds = await api.credentialsList();
      syncCredOptions = creds
        .filter((c) => c.kind === "password" || c.kind === "key" || c.kind === "agent" || c.kind === "opkssh")
        .map((c) => ({ id: c.id, name: c.name }));
    } catch { /* ignore */ }
  }

  async function syncSaveTransport(t: string) {
    syncErr = null;
    try {
      await api.syncTransportSet(t);
      syncTransport = t;
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function syncSaveSftp() {
    syncErr = null;
    try {
      await api.syncSftpConfigSet({
        host: sftpHost,
        port: Math.max(1, Math.floor(sftpPort || 22)),
        user: sftpUser,
        dir: sftpDir,
        auth_mode: sftpAuthMode,
        cred_id: sftpCredId,
        inline_password: sftpInlinePassword,
        inline_key_pem: sftpInlineKeyPem,
        inline_key_passphrase: sftpInlineKeyPassphrase,
        passphrase: syncPassphrase,
      });
      sftpInlinePassword = "";
      sftpInlineKeyPem = "";
      sftpInlineKeyPassphrase = "";
      syncPassphrase = "";
      await syncLoadConfig();
      toast.ok("SFTP sync settings saved");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function syncSaveAuto() {
    try {
      await api.syncAutoSet(syncAuto, Math.max(1, Math.floor(syncCheckMinutes || 5)));
      toast.ok(syncAuto ? "Auto sync on" : "Auto sync off");
    } catch (e) {
      toast.err(errMsg(e));
    }
  }

  async function syncSaveAutoApply() {
    try {
      await api.syncAutoApplySet(syncAutoApply);
      toast.ok(syncAutoApply ? "Auto-apply on" : "Auto-apply off");
    } catch (e) {
      toast.err(errMsg(e));
    }
  }

  async function syncSaveConfig() {
    syncErr = null;
    try {
      await api.syncConfigSet(syncUrl, syncUsername, syncPassword, syncPassphrase);
      syncPassword = "";
      syncPassphrase = "";
      await syncLoadConfig();
      toast.ok("Sync settings saved");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function syncCheckStatus() {
    syncBusy = true; syncErr = null; syncStatusRes = null;
    try {
      syncStatusRes = await api.syncStatus();
      // Manual check is authoritative for the status-bar pill.
      if (syncStatusRes.state === "remote_ahead") {
        syncState.remoteAhead = {
          generation: syncStatusRes.remote_generation,
          device: syncStatusRes.remote_device,
          updated_at: syncStatusRes.remote_updated_at,
          autoApply: false, // manual check - never auto-applies
        };
      } else {
        syncState.clear();
      }
    } catch (e) {
      syncErr = errMsg(e);
    } finally { syncBusy = false; }
  }

  async function syncDoPush(force: boolean) {
    if (force) {
      const ok = await showConfirm({
        title: "Force push",
        message: "Overwrite the remote snapshot even though it has changes this machine hasn't pulled? The other machine's unsynced changes will be lost.",
        okLabel: "Force push",
        danger: true,
      });
      if (!ok) return;
    }
    syncBusy = true; syncErr = null;
    try {
      const res = await api.syncPush(force);
      toast.ok(`Pushed generation ${res.generation} (${(res.snapshot_size / 1024).toFixed(0)} KiB)`);
      await syncLoadConfig();
      await syncCheckStatus();
    } catch (e) {
      syncErr = errMsg(e);
    } finally { syncBusy = false; }
  }

  async function syncDoPull() {
    const ok = await showConfirm({
      title: "Pull from sync",
      message: "Replace this machine's entire profile (connections, credentials, settings) with the remote snapshot? It applies live - no restart, SSH sessions stay open. A safety backup is kept locally.",
      okLabel: "Pull and replace",
      danger: true,
    });
    if (!ok) return;
    syncBusy = true; syncErr = null;
    try {
      const res = await api.syncPullLive();
      syncState.clear();
      await syncCheckStatus();
      if (res.vault_restart_needed) {
        syncPulled = true; // shows the restart banner for the secrets
        toast.ok(`Pulled from ${res.device} - restart to apply passwords/keys`, 8000);
      } else {
        syncPulled = false;
        toast.ok(`Pulled from ${res.device} - applied`, 4000);
      }
    } catch (e) {
      syncErr = errMsg(e);
    } finally { syncBusy = false; }
  }

  // ----- Import hub -----

  // One Settings section, many sources. The picker at the top selects
  // which importer's flow renders below; each keeps its own state so
  // switching sources doesn't wipe a summary you were reading.
  type ImportSource = "rdm" | "sshconfig" | "mobaxterm" | "putty" | "archive";
  // Ordered by expected traffic: our own archive format first (also
  // hosts the ssh-tool:// handler registration), then the common
  // tool migrations, RDM last.
  let importSource = $state<ImportSource>("archive");
  const IMPORT_SOURCES: { id: ImportSource; name: string; desc: string }[] = [
    { id: "archive", name: "ssh-tool archive", desc: "TOML / JSON export from another ssh-tool" },
    { id: "sshconfig", name: "ssh_config", desc: "OpenSSH client config - Host blocks, ProxyJump" },
    { id: "putty", name: "PuTTY / KiTTY", desc: ".reg registry export - SSH sessions" },
    { id: "mobaxterm", name: "MobaXterm", desc: ".mxtsessions export - SSH sessions + bookmark folders" },
    { id: "rdm", name: "Devolutions RDM", desc: "JSON export - folders, connections, jumps, icons" },
  ];

  // Reads a session-export file as text. File.text() always decodes
  // UTF-8, but reg.exe writes .reg files as UTF-16LE - sniff the BOM
  // and decode accordingly.
  async function readImportFile(f: File): Promise<string> {
    const buf = new Uint8Array(await f.arrayBuffer());
    if (buf.length >= 2 && buf[0] === 0xff && buf[1] === 0xfe) {
      return new TextDecoder("utf-16le").decode(buf.subarray(2));
    }
    if (buf.length >= 2 && buf[0] === 0xfe && buf[1] === 0xff) {
      return new TextDecoder("utf-16be").decode(buf.subarray(2));
    }
    return new TextDecoder("utf-8").decode(buf);
  }

  // ----- MobaXterm import -----

  let mobaInputEl: HTMLInputElement | undefined = $state();
  let mobaBusy = $state(false);
  let mobaError = $state<string | null>(null);
  let mobaSummary = $state<MobaXtermImportSummary | null>(null);
  let mobaTargetFolderID = $state(""); // "" = root

  async function onMobaFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    mobaBusy = true;
    mobaError = null;
    mobaSummary = null;
    try {
      const text = await readImportFile(f);
      mobaSummary = await api.mobaXtermImport(text, mobaTargetFolderID || undefined);
      await tree.load();
    } catch (err) {
      mobaError = errMsg(err);
    } finally {
      mobaBusy = false;
      if (mobaInputEl) mobaInputEl.value = "";
    }
  }

  // ----- PuTTY import -----

  let puttyInputEl: HTMLInputElement | undefined = $state();
  let puttyBusy = $state(false);
  let puttyError = $state<string | null>(null);
  let puttySummary = $state<PuttyImportSummary | null>(null);
  let puttyTargetFolderID = $state(""); // "" = root

  async function onPuttyFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    puttyBusy = true;
    puttyError = null;
    puttySummary = null;
    try {
      const text = await readImportFile(f);
      puttySummary = await api.puttyRegImport(text, puttyTargetFolderID || undefined);
      await tree.load();
    } catch (err) {
      puttyError = errMsg(err);
    } finally {
      puttyBusy = false;
      if (puttyInputEl) puttyInputEl.value = "";
    }
  }

  // ----- RDM import -----

  let rdmInputEl: HTMLInputElement | undefined = $state();
  let rdmSummaryEl: HTMLDivElement | undefined = $state();
  let rdmBusy = $state(false);
  let rdmError = $state<string | null>(null);
  let rdmSummary = $state<RdmImportSummary | null>(null);
  let rdmTargetFolderID = $state(""); // "" = root

  // Build a display label for a folder showing the full ancestor path.
  function folderLabel(f: Folder): string {
    const parts: string[] = [f.name];
    let cur = tree.folderById(f.parent_id);
    let guard = 0;
    while (cur && guard++ < 20) {
      parts.unshift(cur.name);
      cur = tree.folderById(cur.parent_id);
    }
    return parts.join(" / ");
  }

  // Sorted flat list of all folders for the picker.
  const rdmFolderOptions = $derived(
    [...tree.folders].sort((a, b) => folderLabel(a).localeCompare(folderLabel(b)))
  );

  async function onRdmFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    rdmBusy = true;
    rdmError = null;
    rdmSummary = null;
    try {
      const text = await f.text();
      const summary = await api.rdmImport(text, rdmTargetFolderID || undefined);
      if (!summary) {
        // Wails v3 declares pointer returns as Promise<T | null>. A
        // null here means the backend handed back a nil Summary
        // without erroring - shouldn't happen for a successful import,
        // but if it does we'd otherwise show no feedback at all.
        rdmError = "Import completed but no summary was returned (likely a backend bug).";
      } else {
        rdmSummary = summary;
        // Refresh the tree so the imported folders + connections appear.
        await Promise.all([tree.load(), credentials.load()]);
        // Scroll the summary into view so the user actually sees it; the
        // file picker doesn't take focus back to the settings page.
        setTimeout(() => {
          rdmSummaryEl?.scrollIntoView({ behavior: "smooth", block: "start" });
        }, 50);
      }
    } catch (err: any) {
      rdmError = typeof err === "object" && err?.message ? err.message : String(err);
    } finally {
      rdmBusy = false;
      // Reset the input so picking the same file twice still fires change.
      if (rdmInputEl) rdmInputEl.value = "";
    }
  }

  function attentionByReason(items: { reason: string }[]) {
    const m = new Map<string, number>();
    for (const it of items) m.set(it.reason, (m.get(it.reason) ?? 0) + 1);
    return m;
  }
  function attentionLabel(reason: string): string {
    switch (reason) {
      case "external-cred-ref":
        return "Credential lived in an external RDM vault - attach a credential before connecting";
      case "private-key-file":
        return "Connection used a private-key file on disk - import the key + assign it";
      case "inline-username":
        return "RDM entry had a typed username but no credential - set a password / key";
      default:
        return reason;
    }
  }

  // ---------- ssh_config import ----------
  let sshConfigText = $state("");
  let sshConfigTargetFolderID = $state("");
  let sshConfigBusy = $state(false);
  let sshConfigErr = $state<string | null>(null);
  let sshConfigSummary = $state<SshConfigImportSummary | null>(null);

  async function importSshConfig() {
    sshConfigBusy = true;
    sshConfigErr = null;
    sshConfigSummary = null;
    try {
      sshConfigSummary = await api.sshConfigImport(sshConfigText, sshConfigTargetFolderID || undefined);
      await tree.load();
    } catch (e: any) {
      sshConfigErr = errMsg(e);
    } finally {
      sshConfigBusy = false;
    }
  }

  // ---------- Export / Import ----------
  let exportFormat = $state<"toml" | "json">("toml");
  let exportRoots = $state<string[]>([]);
  let exportExtras = $state<string[]>([]);
  let exportIncludeCreds = $state(false);
  let exportPassphrase = $state("");
  let exportBusy = $state(false);
  let exportError = $state<string | null>(null);
  let exportPreview = $state<string | null>(null);

  function toggleExportRoot(id: string) {
    exportRoots = exportRoots.includes(id)
      ? exportRoots.filter((x) => x !== id)
      : [...exportRoots, id];
  }

  async function runExport() {
    exportBusy = true;
    exportError = null;
    exportPreview = null;
    try {
      const res = await api.exportSubtree({
        roots: exportRoots,
        extra: exportExtras,
        format: exportFormat,
        include_credentials: exportIncludeCreds,
        passphrase: exportIncludeCreds ? exportPassphrase : "",
      });
      exportPreview = res.body;
    } catch (e: any) {
      exportError = errMsg(e);
    } finally {
      exportBusy = false;
    }
  }

  async function copyExport() {
    if (!exportPreview) return;
    await copyText(exportPreview, { label: "Export" });
  }

  let importText = $state("");
  let importURL = $state("");
  let importFilePath = $state("");
  let importConflict = $state<"skip" | "rename" | "overwrite">("skip");
  let importPassphrase = $state("");
  let importTargetFolderId = $state<string | null>(null);
  let importTargetPickerOpen = $state(false);
  let importBusy = $state(false);
  let importError = $state<string | null>(null);
  let importPreview = $state<ArcImportSummary | null>(null);
  let importDone = $state<ArcImportSummary | null>(null);

  async function fetchImportFromURL() {
    if (!importURL.trim()) return;
    importBusy = true; importError = null;
    try {
      importText = await api.fetchArchiveURL(importURL.trim());
      importFilePath = "";
    } catch (e: any) {
      importError = errMsg(e);
    } finally {
      importBusy = false;
    }
  }

  async function loadImportFromFile() {
    importBusy = true; importError = null;
    try {
      const res = await api.loadTextFile("Choose an archive file");
      if (res.path) {
        importText = res.content;
        importFilePath = res.path;
      }
    } catch (e: any) {
      importError = errMsg(e);
    } finally {
      importBusy = false;
    }
  }

  // --- ssh-tool:// URL scheme handler registration ---
  let urlSchemeStatus = $state("");
  let urlSchemeBusy = $state(false);
  let urlSchemeMsg = $state<string | null>(null);

  async function refreshURLSchemeStatus() {
    try { urlSchemeStatus = await api.urlSchemeStatus(); } catch {}
  }

  async function registerURLScheme() {
    urlSchemeBusy = true; urlSchemeMsg = null;
    try {
      await api.registerURLScheme();
      urlSchemeMsg = "Registered. ssh-tool:// links now launch this app.";
      await refreshURLSchemeStatus();
    } catch (e: any) {
      urlSchemeMsg = `Failed: ${errMsg(e)}`;
    } finally {
      urlSchemeBusy = false;
    }
  }

  // --- "Open in ssh-tool" file-manager context menu ---
  let explorerMenuStatus = $state("");
  let explorerMenuBusy = $state(false);
  let explorerMenuMsg = $state<string | null>(null);

  async function refreshExplorerMenuStatus() {
    try { explorerMenuStatus = await api.explorerMenuStatus(); } catch {}
  }

  async function toggleExplorerMenu() {
    explorerMenuBusy = true; explorerMenuMsg = null;
    try {
      if (explorerMenuStatus) {
        await api.explorerMenuUnregister();
        explorerMenuMsg = "Removed from the file manager's right-click menu.";
      } else {
        await api.explorerMenuRegister();
        explorerMenuMsg = "Added. Right-click a folder (or inside one) to see \"Open in ssh-tool\".";
      }
      await refreshExplorerMenuStatus();
    } catch (e: any) {
      explorerMenuMsg = `Failed: ${errMsg(e)}`;
    } finally {
      explorerMenuBusy = false;
    }
  }

  onMount(() => { refreshURLSchemeStatus(); refreshExplorerMenuStatus(); });

  // Watch the deep-link store: when App.svelte gets
  // `deep_link_import` it sets pendingImportURL here, we navigate
  // to the Import section's archive source, prefill the URL, fetch it, and
  // clear the pending flag so we don't re-trigger on subsequent
  // section visits.
  $effect(() => {
    const u = deepLink.pendingImportURL;
    if (!u) return;
    importURL = u;
    activeSection = "import";
    importSource = "archive";
    deepLink.clearImportURL();
    fetchImportFromURL();
  });

  const importTargetLabel = $derived.by(() => {
    void tree.version;
    if (!importTargetFolderId) return "(root)";
    const parts: string[] = [];
    let cur: { id: string; parent_id: string | null; name: string } | null =
      tree.folderById(importTargetFolderId);
    while (cur) {
      parts.unshift(cur.name);
      cur = cur.parent_id ? tree.folderById(cur.parent_id) : null;
    }
    return parts.join(" / ") || "(missing folder)";
  });

  async function importDryRun() {
    importBusy = true;
    importError = null;
    importPreview = null;
    importDone = null;
    try {
      importPreview = await api.importArchive({
        text: importText,
        conflict: importConflict,
        dry_run: true,
        passphrase: importPassphrase,
        target_folder_id: importTargetFolderId ?? "",
      });
    } catch (e: any) {
      importError = errMsg(e);
    } finally {
      importBusy = false;
    }
  }

  async function importApply() {
    importBusy = true;
    importError = null;
    try {
      importDone = await api.importArchive({
        text: importText,
        conflict: importConflict,
        dry_run: false,
        passphrase: importPassphrase,
        target_folder_id: importTargetFolderId ?? "",
      });
      importPreview = null;
      await tree.load();
      await credentials.load();
    } catch (e: any) {
      importError = errMsg(e);
    } finally {
      importBusy = false;
    }
  }

  // ---------- Navigation ----------
  // The page used to be one long scroll of eight stacked sections;
  // a side nav keeps the visible area small and scoped to one task
  // (terminal vs. import vs. logs etc.). State is persisted in the
  // settings DB so the next open lands where you were.
  type SectionId =
    | "appearance"
    | "connection"
    | "network"
    | "terminal"
    | "recording"
    | "browser"
    | "snippets"
    | "workspaces"
    | "vault"
    | "keepass"
    | "bitwarden"
    | "infisical"
    | "backup"
    | "sync"
    | "audit"
    | "import"
    | "export"
    | "llm"
    | "sharing"
    | "logs"
    | "updates"
    | "about";

  type SectionDef = {
    id: SectionId;
    title: string;
    group: "Appearance" | "Security" | "Import / Export" | "Integrations" | "App" | "Diagnostics";
  };

  const SECTIONS: SectionDef[] = [
    { id: "appearance",        title: "Appearance",       group: "Appearance" },
    { id: "connection",        title: "Connection",       group: "Appearance" },
    { id: "network",           title: "Network profiles", group: "Appearance" },
    { id: "terminal",          title: "Terminal",         group: "Appearance" },
    { id: "browser",           title: "Browser launcher", group: "Appearance" },
    { id: "snippets",          title: "Snippets",         group: "Appearance" },
    { id: "workspaces",        title: "Workspaces",       group: "Appearance" },
    { id: "recording",         title: "Session recording", group: "Security" },
    { id: "vault",             title: "Vault",            group: "Security" },
    { id: "keepass",           title: "KeePass",          group: "Security" },
    { id: "bitwarden",         title: "Bitwarden",        group: "Security" },
    { id: "infisical",         title: "Infisical",        group: "Security" },
    { id: "backup",            title: "Backup & restore", group: "Security" },
    { id: "sync",              title: "Sync",             group: "Security" },
    { id: "audit",             title: "Audit log",        group: "Security" },
    { id: "import",            title: "Import",           group: "Import / Export" },
    { id: "export",            title: "Export connections", group: "Import / Export" },
    { id: "llm",               title: "LLM (MCP) access",  group: "Integrations" },
    { id: "sharing",           title: "Sharing",          group: "Integrations" },
    { id: "updates",           title: "Updates",          group: "App" },
    { id: "logs",              title: "Logs",             group: "Diagnostics" },
    { id: "about",             title: "About",            group: "Diagnostics" },
  ];

  let activeSection = $state<SectionId>("terminal");

  // ----- Network profiles (WireGuard + NetBird) -----
  let npName = $state("");
  let npConf = $state("");
  let npEditingId = $state<string | null>(null);
  let npEditKind = $state<"wireguard" | "netbird" | "tailscale">("wireguard");
  let npKind = $state<"wireguard" | "netbird" | "tailscale">("wireguard");
  let npBusy = $state(false);
  // NetBird form fields
  let nbManagement = $state("");
  let nbDevice = $state("");
  let nbCredId = $state("");
  // Tailscale form fields
  let tsControl = $state("");
  let tsHostname = $state("");
  let tsCredId = $state("");
  // Inline "create auth-key credential" (mirrors the NetBird one).
  let tsNewKeyOpen = $state(false);
  let tsNewKeyName = $state("");
  let tsNewKeySecret = $state("");
  let tsNewKeyBusy = $state(false);
  // Inline "create setup-key credential" (the picker only lists
  // existing api_token creds; this avoids a trip to the Credentials
  // tab mid-flow).
  let nbNewKeyOpen = $state(false);
  let nbNewKeyName = $state("");
  let nbNewKeySecret = $state("");
  let nbNewKeyBusy = $state(false);
  // Plugins
  let plugins = $state<import("./api").PluginInfo[]>([]);
  let pluginBusy = $state(false);
  const nbInstalled = $derived(plugins.some((p) => p.name === "netbird" && p.installed));
  const tsInstalled = $derived(plugins.some((p) => p.name === "tailscale" && p.installed));
  // api_token credentials for the setup-key picker.
  const apiTokenCredOptions = $derived(
    credentials.list
      .filter((c) => c.kind === "api_token")
      .map((c) => ({ value: c.id, label: c.name })),
  );

  $effect(() => {
    if (activeSection === "network") {
      networkProfiles.load().catch(() => {});
      credentials.load().catch(() => {});
      refreshPlugins();
    }
  });

  async function refreshPlugins() {
    try { plugins = (await api.pluginsStatus()) ?? []; } catch { /* ignore */ }
  }

  // Pre-fill the NetBird device name with "<hostname>.ssh-tool" the
  // first time the create form shows the NetBird fields, so a peer is
  // recognisable in the dashboard without the user typing anything. Only
  // when creating (not editing an existing profile) and only while the
  // field is still empty, so it never clobbers a value the user typed or
  // an existing profile's name.
  $effect(() => {
    if (activeSection !== "network") return;
    if (npEditingId || npKind !== "netbird") return;
    if (nbDevice.trim() !== "") return;
    api.suggestNetbirdDeviceName()
      .then((n) => { if (!npEditingId && npKind === "netbird" && nbDevice.trim() === "") nbDevice = n; })
      .catch(() => {});
  });

  // Same pre-fill for the Tailscale hostname (see the NetBird effect
  // above). Suggests "<hostname>" as the tailnet node name.
  $effect(() => {
    if (activeSection !== "network") return;
    if (npEditingId || npKind !== "tailscale") return;
    if (tsHostname.trim() !== "") return;
    api.suggestTailscaleHostname()
      .then((n) => { if (!npEditingId && npKind === "tailscale" && tsHostname.trim() === "") tsHostname = n; })
      .catch(() => {});
  });

  // Passive presence: while the Network section is open, poll each
  // WireGuard profile's presence so the card can show "up on <machine>"
  // when the tunnel is live on another synced machine, and offer a
  // remote disconnect. NetBird is excluded - each machine is its own
  // peer, so there's no single-owner conflict to surface. Keyed by
  // profile id -> the foreign owner (or absent when free / ours).
  let npRemoteOwners = $state<Record<string, import("./api").RemoteOwner>>({});
  let npDisconnecting = $state<Record<string, boolean>>({});

  async function pollNpPresence() {
    const wg = networkProfiles.list.filter((p) => p.kind === "wireguard");
    if (wg.length === 0) { npRemoteOwners = {}; return; }
    const next: Record<string, import("./api").RemoteOwner> = {};
    await Promise.all(wg.map(async (p) => {
      try {
        const ro = await api.networkProfilePresence(p.id);
        if (ro.active) next[p.id] = ro;
      } catch { /* transient - just omit this cycle */ }
    }));
    npRemoteOwners = next;
  }

  $effect(() => {
    if (activeSection !== "network") return;
    // Depend on the list so a create/delete re-primes the poll set.
    void networkProfiles.list.length;
    pollNpPresence();
    const t = setInterval(pollNpPresence, 8000);
    return () => clearInterval(t);
  });

  // Ask the machine that holds a WG profile's tunnel to drop it. Not a
  // take-over (we don't bring it up here) - just frees it. Shows a short
  // "disconnecting" state, then re-polls to confirm the owner cleared.
  async function npDisconnectRemote(np: NetworkProfileInfo) {
    npDisconnecting = { ...npDisconnecting, [np.id]: true };
    try {
      const estimate = await api.networkProfileDisconnectRemote(np.id);
      if (estimate > 0) {
        // Poll until the owner's record clears or the estimate elapses.
        const deadline = Date.now() + (estimate + 5) * 1000;
        while (Date.now() < deadline) {
          await new Promise((r) => setTimeout(r, 3000));
          try {
            const ro = await api.networkProfilePresence(np.id);
            if (!ro.active) break;
          } catch { /* keep waiting */ }
        }
      }
      await pollNpPresence();
    } catch (e: any) {
      toast.err(errMsg(e));
    } finally {
      npDisconnecting = { ...npDisconnecting, [np.id]: false };
    }
  }
  async function pluginDownload(name: string) {
    pluginBusy = true;
    try {
      await api.pluginDownload(name);
      await refreshPlugins();
      toast.ok(`${name} plugin installed`);
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { pluginBusy = false; }
  }
  async function pluginRemove(name: string) {
    const ok = await showConfirm({
      title: "Remove plugin",
      message: `Remove the ${name} plugin? Profiles using it will fail to connect until it's reinstalled.`,
      okLabel: "Remove",
    });
    if (!ok) return;
    pluginBusy = true;
    try { await api.pluginRemove(name); await refreshPlugins(); }
    catch (e: any) { toast.err(errMsg(e)); }
    finally { pluginBusy = false; }
  }

  async function nbCreateSetupKey() {
    if (!nbNewKeyName.trim() || !nbNewKeySecret) {
      toast.err("Name and setup key are required");
      return;
    }
    nbNewKeyBusy = true;
    try {
      const res: any = await api.credentialsCreate({
        kind: "api_token",
        name: nbNewKeyName.trim(),
        api_token_id: "",
        api_token_secret: nbNewKeySecret,
      } as any);
      await credentials.load();
      const newId = res?.credential?.id ?? res?.id;
      if (newId) nbCredId = newId;
      nbNewKeyOpen = false;
      nbNewKeyName = "";
      nbNewKeySecret = "";
      toast.ok("Setup-key credential created");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { nbNewKeyBusy = false; }
  }

  async function npCreateNetbird() {
    npBusy = true;
    try {
      await api.networkProfileCreateNetbird(npName.trim(), nbManagement.trim(), nbDevice.trim(), nbCredId);
      npCancelEdit();
      await networkProfiles.load(true);
      toast.ok("NetBird profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveNetbird() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await api.networkProfileUpdateNetbird(npEditingId, npName.trim(), nbManagement.trim(), nbDevice.trim(), nbCredId);
      npCancelEdit();
      await networkProfiles.load(true);
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }

  async function tsCreateAuthKey() {
    if (!tsNewKeyName.trim() || !tsNewKeySecret) {
      toast.err("Name and auth key are required");
      return;
    }
    tsNewKeyBusy = true;
    try {
      const res: any = await api.credentialsCreate({
        kind: "api_token",
        name: tsNewKeyName.trim(),
        api_token_id: "",
        api_token_secret: tsNewKeySecret,
      } as any);
      await credentials.load();
      const newId = res?.credential?.id ?? res?.id;
      if (newId) tsCredId = newId;
      tsNewKeyOpen = false;
      tsNewKeyName = "";
      tsNewKeySecret = "";
      toast.ok("Auth-key credential created");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { tsNewKeyBusy = false; }
  }

  async function npCreateTailscale() {
    npBusy = true;
    try {
      await api.networkProfileCreateTailscale(npName.trim(), tsControl.trim(), tsHostname.trim(), tsCredId);
      npCancelEdit();
      await networkProfiles.load(true);
      toast.ok("Tailscale profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveTailscale() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await api.networkProfileUpdateTailscale(npEditingId, npName.trim(), tsControl.trim(), tsHostname.trim(), tsCredId);
      npCancelEdit();
      await networkProfiles.load(true);
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }

  async function npCreate() {
    npBusy = true;
    try {
      await api.networkProfileCreate(npName.trim(), npConf);
      npName = ""; npConf = "";
      await networkProfiles.load(true);
      toast.ok("Profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  function npStartEdit(np: NetworkProfileInfo) {
    npEditingId = np.id;
    npEditKind = np.kind;
    npName = np.name;
    if (np.kind === "netbird") {
      nbManagement = np.netbird?.management_url ?? "";
      nbDevice = np.netbird?.device_name ?? "";
      nbCredId = np.netbird?.setup_key_credential_id ?? "";
      return;
    }
    if (np.kind === "tailscale") {
      tsControl = np.tailscale?.control_url ?? "";
      tsHostname = np.tailscale?.hostname ?? "";
      tsCredId = np.tailscale?.auth_key_credential_id ?? "";
      return;
    }
    // WireGuard: prefill with the stored config; secrets render as
    // **KEEP** placeholders, which the backend translates back to
    // "keep the vault value" on save. Clearing the textarea keeps the
    // current config (rename-only).
    npConf = "";
    api.networkProfileRenderConf(np.id)
      .then((text) => { if (npEditingId === np.id) npConf = text; })
      .catch((e) => toast.err(errMsg(e)));
  }
  function npCancelEdit() {
    npEditingId = null;
    npName = ""; npConf = "";
    nbManagement = ""; nbDevice = ""; nbCredId = "";
    tsControl = ""; tsHostname = ""; tsCredId = "";
    tsNewKeyOpen = false; tsNewKeyName = ""; tsNewKeySecret = "";
  }
  async function npSaveEdit() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await api.networkProfileUpdate(npEditingId, npName.trim(), npConf);
      npCancelEdit();
      await networkProfiles.load(true);
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npDelete(np: NetworkProfileInfo) {
    const ok = await showConfirm({
      title: "Delete network profile",
      message: `Delete "${np.name}"? Connections still assigned to it will fail to connect until you change their Network setting.`,
      okLabel: "Delete",
    });
    if (!ok) return;
    try {
      await api.networkProfileDelete(np.id);
      await networkProfiles.load(true);
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  async function npSetPolicy(np: NetworkProfileInfo, mode: string, paused: boolean) {
    try {
      await api.networkProfileSetPolicy(np.id, mode, paused);
      await networkProfiles.load(true);
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  async function npTest(np: NetworkProfileInfo) {
    npBusy = true;
    try {
      const st = await api.networkProfileTest(np.id);
      toast.ok(st.running ? `Tunnel ${np.name} is up` : `Tunnel ${np.name} did not start`);
      await networkProfiles.load(true);
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }

  // Refresh the profile counts + active-tunnel count every time the
  // About section opens; "" asks the backend for forwards across all
  // sessions.
  $effect(() => {
    if (activeSection !== "about") return;
    api.profileStats()
      .then((s) => (profileStats = s))
      .catch(() => (profileStats = null));
    api.forwardsActive("")
      .then((l) => (activeForwards = l?.length ?? 0))
      .catch(() => (activeForwards = null));
  });

  // ----- Vault manual lock -----
  let lockBusy = $state(false);
  let lockNotice = $state<string | null>(null);

  async function onLockNow() {
    const ok = await showConfirm({
      title: "Lock vault",
      message: "Lock the vault now? Next vault-backed action will re-prompt for the passphrase. The auto-unlock sidecar is also forgotten so the next launch prompts too.",
      okLabel: "Lock",
    });
    if (!ok) return;
    lockBusy = true;
    lockNotice = null;
    try {
      await api.vaultLock(true);
      lockNotice = "Vault locked.";
      // Tell the rest of the app so VaultGate re-prompts.
      window.dispatchEvent(new CustomEvent("vault-lock-now"));
      setTimeout(() => { lockNotice = null; }, 2500);
    } catch (e: any) {
      lockNotice = `Lock failed: ${errMsg(e)}`;
    } finally {
      lockBusy = false;
    }
  }

  // ----- Vault passphrase rotation -----
  let rotateOld = $state("");
  let rotateNew = $state("");
  let rotateConfirm = $state("");
  let rotateBusy = $state(false);
  let rotateError = $state<string | null>(null);
  let rotateNotice = $state<string | null>(null);

  async function onRotatePassphrase() {
    rotateError = null;
    rotateNotice = null;
    if (!rotateOld) { rotateError = "Enter current passphrase."; return; }
    if (!rotateNew) { rotateError = "Enter new passphrase."; return; }
    if (rotateNew !== rotateConfirm) { rotateError = "New passphrases don't match."; return; }
    if (rotateNew === rotateOld) { rotateError = "New passphrase is identical to current one."; return; }
    rotateBusy = true;
    try {
      await api.vaultChangePassphrase(rotateOld, rotateNew);
      rotateNotice = "Master passphrase rotated. All credentials re-encrypted with the new key.";
      rotateOld = ""; rotateNew = ""; rotateConfirm = "";
    } catch (e: any) {
      rotateError = errMsg(e);
    } finally {
      rotateBusy = false;
    }
  }

  // ----- Audit log -----
  let auditEvents = $state<import("./api").AuditEvent[]>([]);
  let auditFilter = $state("");
  let auditLimit = $state(200);
  let auditBusy = $state(false);
  let auditError = $state<string | null>(null);
  let auditPurgeDays = $state(90);
  // Sort: which column drives the ORDER BY. Backend always pages
  // newest-first by ts, so client-side sort just re-orders the
  // already-fetched page. "ts" is the natural default.
  let auditSortBy = $state<"ts" | "action">("ts");
  let auditSortDir = $state<"asc" | "desc">("desc");
  // Expanded rows: show full metadata blob instead of the column
  // summary. Keyed by event id.
  let auditExpanded = $state<Set<number>>(new Set());

  // Ctrl/Cmd+A inside the audit table selects only its rows, not the
  // whole Settings GUI.
  function onAuditKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && (e.key === "a" || e.key === "A")) {
      e.preventDefault();
      const el = e.currentTarget as HTMLElement;
      const sel = window.getSelection();
      if (sel) {
        sel.removeAllRanges();
        const range = document.createRange();
        range.selectNodeContents(el);
        sel.addRange(range);
      }
    }
  }

  function toggleAuditSort(col: "ts" | "action") {
    if (auditSortBy === col) {
      auditSortDir = auditSortDir === "asc" ? "desc" : "asc";
    } else {
      auditSortBy = col;
      auditSortDir = col === "ts" ? "desc" : "asc";
    }
  }

  function toggleAuditExpand(id: number) {
    if (auditExpanded.has(id)) auditExpanded.delete(id);
    else auditExpanded.add(id);
    auditExpanded = new Set(auditExpanded); // trigger reactivity
  }

  // Columns we pull out as their own table cells. Order matters -
  // host first because that's what the user looks for first during
  // an incident review. Everything else falls into the catch-all
  // "extra" column rendered as key=value chips.
  const AUDIT_EXTRACTED = ["host", "port", "user", "name", "session_id"];

  function extractedMeta(ev: import("./api").AuditEvent, key: string): string {
    return ev.metadata?.[key] ?? "";
  }
  function extraMeta(ev: import("./api").AuditEvent): [string, string][] {
    if (!ev.metadata) return [];
    return Object.entries(ev.metadata).filter(([k]) => !AUDIT_EXTRACTED.includes(k));
  }

  const sortedAuditEvents = $derived.by(() => {
    const needle = auditFilter.trim().toLowerCase();
    let arr = auditEvents;
    if (needle) {
      // Substring match across action, target, and every metadata
      // value so the filter behaves like a tag search rather than
      // an exact-action lookup. "vault" matches vault.*, "10.0" or
      // "root" matches by host or user.
      arr = arr.filter((ev) => {
        if (ev.action.toLowerCase().includes(needle)) return true;
        if (ev.target.toLowerCase().includes(needle)) return true;
        for (const v of Object.values(ev.metadata ?? {})) {
          if (String(v).toLowerCase().includes(needle)) return true;
        }
        return false;
      });
    } else {
      arr = [...arr];
    }
    const dir = auditSortDir === "asc" ? 1 : -1;
    if (auditSortBy === "action") {
      arr.sort((a, b) => a.action.localeCompare(b.action) * dir);
    } else {
      arr.sort((a, b) => (a.ts - b.ts) * dir);
    }
    return arr;
  });

  async function loadAudit() {
    auditBusy = true;
    auditError = null;
    try {
      // Always fetch unfiltered. The Filter input is applied
      // client-side over the already-loaded page so substring
      // matches work without round-tripping to the DB on each
      // keystroke (and without the backend having to grow a
      // LIKE-with-wildcard-escape path).
      auditEvents = await api.auditList("", auditLimit, 0);
    } catch (e: any) {
      auditError = errMsg(e);
    } finally {
      auditBusy = false;
    }
  }

  async function onAuditPurge() {
    if (auditPurgeDays <= 0) return;
    const ok = await showConfirm({
      title: "Purge audit events",
      message: `Delete audit events older than ${auditPurgeDays} days?`,
      okLabel: "Purge",
      danger: true,
    });
    if (!ok) return;
    try {
      const n = await api.auditPurge(auditPurgeDays);
      auditError = null;
      await loadAudit();
      toast.ok(`Purged ${n} events.`);
    } catch (e: any) {
      auditError = errMsg(e);
    }
  }

  function exportAuditCSV() {
    const rows = [["ts", "action", "target", "metadata"]];
    for (const ev of auditEvents) {
      rows.push([
        new Date(ev.ts * 1000).toISOString(),
        ev.action,
        ev.target,
        JSON.stringify(ev.metadata),
      ]);
    }
    const csv = rows.map(r => r.map(c => `"${(c ?? "").replace(/"/g, '""')}"`).join(",")).join("\n");
    const blob = new Blob([csv], { type: "text/csv" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `ssh-tool-audit-${new Date().toISOString().slice(0,10)}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }

  $effect(() => {
    if (activeSection === "audit") loadAudit();
  });

  // ----- Backup & restore -----
  let backupList = $state<BackupInfo[]>([]);
  let backupBusy = $state(false);
  let backupError = $state<string | null>(null);
  let backupNotice = $state<string | null>(null);

  async function loadBackups() {
    backupError = null;
    try {
      backupList = await api.backupsList();
    } catch (e: any) {
      backupError = errMsg(e);
    }
  }

  async function onBackupCreate() {
    backupError = null;
    backupNotice = null;
    const pp = await showPrompt(
      "Enter your vault master passphrase. The backup will be sealed with the same key.",
      { password: true },
    );
    if (!pp) return;
    backupBusy = true;
    try {
      const res = await api.backupsCreate(pp);
      backupNotice = `Saved: ${res.path}`;
      await loadBackups();
    } catch (e: any) {
      backupError = errMsg(e);
    } finally {
      backupBusy = false;
    }
  }

  async function onBackupRestore(b: BackupInfo) {
    backupError = null;
    backupNotice = null;
    const confirmed = await showPrompt(
      `Restore ${b.filename}? This overwrites the current store and vault. A safety copy of the current state is written to backups/pre-restore-<timestamp>/ first. Type RESTORE to confirm.`
    );
    if (confirmed !== "RESTORE") {
      backupError = "Restore cancelled.";
      return;
    }
    const pp = await showPrompt(
      "Enter the passphrase used when this backup was created.",
      { password: true },
    );
    if (!pp) return;
    backupBusy = true;
    try {
      await api.backupsRestore(b.path, pp);
      backupNotice = "Restore staged. Quit and reopen the app to complete it - the new store and vault are applied at the next start.";
    } catch (e: any) {
      backupError = errMsg(e);
    } finally {
      backupBusy = false;
    }
  }

  async function onBackupDelete(b: BackupInfo) {
    backupError = null;
    backupNotice = null;
    const confirmed = await showPrompt(`Permanently remove ${b.filename}? Type yes to confirm.`);
    if (confirmed !== "yes") return;
    backupBusy = true;
    try {
      await api.backupsDelete(b.path);
      await loadBackups();
    } catch (e: any) {
      backupError = errMsg(e);
    } finally {
      backupBusy = false;
    }
  }

  let autoBackupPrefs = $state<AutoBackupPrefs>({ enabled: false, keep_last: 7 });
  let autoBackupLoaded = false;

  async function loadAutoBackupPrefs() {
    if (autoBackupLoaded) return;
    try {
      autoBackupPrefs = await api.autoBackupPrefsGet();
    } catch (e) {
      console.warn("auto-backup prefs load", e);
    }
    autoBackupLoaded = true;
  }

  async function saveAutoBackupPrefs(next: AutoBackupPrefs) {
    autoBackupPrefs = next;
    try {
      await api.autoBackupPrefsSet(next);
    } catch (e: any) {
      backupError = errMsg(e);
    }
  }

  $effect(() => {
    if (activeSection === "backup") {
      void loadBackups();
      void loadAutoBackupPrefs();
    }
    if (activeSection === "sync") {
      void syncLoadConfig();
      void syncLoadCredOptions();
    }
  });

  // Sections that are desktop-only and hidden on mobile (the backend
  // features they configure are excluded on android).
  const MOBILE_HIDDEN_SECTIONS = new Set(["browser", "llm"]);

  // Group sections by their group label for the side nav.
  const sectionsByGroup = $derived.by(() => {
    const map = new Map<string, SectionDef[]>();
    for (const s of SECTIONS) {
      if (isMobile && MOBILE_HIDDEN_SECTIONS.has(s.id)) continue;
      const arr = map.get(s.group) ?? [];
      arr.push(s);
      map.set(s.group, arr);
    }
    return [...map.entries()];
  });

  $effect(() => {
    // Restore last section on first mount. Pre-unification ids
    // ("import-rdm", "import-ssh-config", "import-archive") map to
    // the merged "import" section so an old persisted value still
    // lands somewhere sensible.
    api.settingsGet("settings_active_section").then((v) => {
      if (v?.startsWith("import-")) v = "import";
      const valid = SECTIONS.some((s) => s.id === v);
      if (valid) activeSection = v as SectionId;
    }).catch(() => {});
  });

  // Deep-link from elsewhere in the UI (e.g. the status-bar version
  // pill → About). When a section is staged in view.pendingSettingsSection
  // we honour it and clear the pin so the next plain "open Settings"
  // resumes the user's last section.
  $effect(() => {
    const pin = view.pendingSettingsSection;
    if (!pin) return;
    const valid = SECTIONS.some((s) => s.id === pin);
    if (valid) {
      activeSection = pin as SectionId;
      api.settingsSet("settings_active_section", pin).catch(console.warn);
    }
    view.pendingSettingsSection = null;
  });

  function pickSection(s: SectionId) {
    activeSection = s;
    api.settingsSet("settings_active_section", s).catch(console.warn);
    if (s === "snippets") reloadSnippets();
    if (s === "workspaces") reloadWorkspaces();
  }

  // Auto-load snippets if the initial section is "snippets".
  $effect(() => {
    if (activeSection === "snippets" && snippets.length === 0) {
      reloadSnippets();
    }
  });
</script>

<section class="settings">
  <aside class="nav">
    <h1>Settings</h1>
    {#each sectionsByGroup as [group, items] (group)}
      <div class="nav-group">
        <div class="nav-group-label">{group}</div>
        {#each items as s (s.id)}
          <button
            class="nav-item"
            class:active={activeSection === s.id}
            onclick={() => pickSection(s.id)}
          >{s.title}</button>
        {/each}
      </div>
    {/each}
  </aside>

  <div class="content">

  {#if activeSection === "appearance"}
  <div class="group">
    <h2>Appearance</h2>
    <p class="hint">
      Density and base font size affect the connection / credential
      trees and most list rows. Doesn't touch the terminal itself -
      that lives under <strong>Terminal</strong>.
    </p>

    <h3 style="margin-top:0.8rem">UI theme</h3>
    <p class="hint">
      Default is Catppuccin Mocha with slightly lifted muted text
      for outdoor / bright-room readability. Pick High contrast if
      you still can't read subtle labels in direct sunlight.
    </p>
    <fieldset class="modes">
      {#each [
        { id: "mocha", name: "Mocha (default dark)", desc: "Catppuccin Mocha - soft dark, easy on the eyes." },
        { id: "latte", name: "Latte (light)",        desc: "Catppuccin Latte - light background, dark text. For bright rooms / projectors." },
        { id: "hc",    name: "High contrast (dark)", desc: "Mocha with text + borders pushed up for direct-sun visibility." },
      ] as t (t.id)}
        <label class:active={appPrefs.uiTheme === t.id}>
          <input
            type="radio"
            name="uiTheme"
            checked={appPrefs.uiTheme === t.id}
            onchange={() => appPrefs.setUITheme(t.id as any)}
          />
          <div>
            <div class="mode-name">{t.name}</div>
            <div class="mode-desc">{t.desc}</div>
          </div>
        </label>
      {/each}
    </fieldset>

    {#if !isMobile}
    <h3 style="margin-top:0.8rem">Density</h3>
    <fieldset class="modes">
      {#each ["compact","comfortable","cozy"] as d (d)}
        <label class:active={appPrefs.density === d}>
          <input
            type="radio"
            name="density"
            checked={appPrefs.density === d}
            onchange={() => appPrefs.setDensity(d as any)}
          />
          <div>
            <div class="mode-name">{d.charAt(0).toUpperCase() + d.slice(1)}</div>
            <div class="mode-desc">
              {#if d === "compact"}Tight rows - best for 200+ connections at once.
              {:else if d === "comfortable"}Default-ish; more breathing room.
              {:else}Lots of vertical padding; readable on a 4K monitor.
              {/if}
            </div>
          </div>
        </label>
      {/each}
    </fieldset>

    <label class="num font-size-row">
      <span>UI font size (px)</span>
      <div class="font-controls">
        <button
          type="button"
          class="font-step"
          onclick={() => appPrefs.setBaseFontSize(appPrefs.baseFontSize - 1)}
          disabled={appPrefs.baseFontSize <= 11}
          title="Smaller"
        >-</button>
        <span class="font-value">{appPrefs.baseFontSize}px</span>
        <button
          type="button"
          class="font-step"
          onclick={() => appPrefs.setBaseFontSize(appPrefs.baseFontSize + 1)}
          disabled={appPrefs.baseFontSize >= 18}
          title="Larger"
        >+</button>
        <button
          type="button"
          class="font-step ghost"
          onclick={() => appPrefs.setBaseFontSize(13)}
          disabled={appPrefs.baseFontSize === 13}
          title="Reset to default (13)"
        >Reset</button>
      </div>
    </label>
    <p class="hint inline">
      Scales the whole app's <code>rem</code>-based sizes (tree, panels,
      modals). Default 13. Terminal font size has its own setting.
    </p>
    {/if}

    <fieldset class="check-cards">
      <label class:active={appPrefs.tagBackground}>
        <input
          type="checkbox"
          checked={appPrefs.tagBackground}
          onchange={(e) => appPrefs.setTagBackground((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Color tag as row background</div>
          <div class="mode-desc">
            In addition to the left strip, tint the whole row with
            the connection / folder's colour tag. Stronger visual
            grouping at the cost of contrast.
          </div>
        </div>
      </label>

      <label class:active={appPrefs.activeRowEmphasis}>
        <input
          type="checkbox"
          checked={appPrefs.activeRowEmphasis}
          onchange={(e) => appPrefs.setActiveRowEmphasis((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Emphasise active session row</div>
          <div class="mode-desc">
            The tree row matching the currently focused terminal
            tab gets a brighter highlight so it stands out from
            other live connections.
          </div>
        </div>
      </label>

      <label class:active={appPrefs.tabTimer}>
        <input
          type="checkbox"
          checked={appPrefs.tabTimer}
          onchange={(e) => appPrefs.setTabTimer((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Show session uptime in tab bar</div>
          <div class="mode-desc">
            Small "5m" / "2h" timer next to each connected tab's
            name showing how long the session has been up.
          </div>
        </div>
      </label>
    </fieldset>
  </div>
  {/if}

  {#if activeSection === "connection"}
  <div class="group">
    <h2>Connection</h2>
    <p class="hint">
      Applies to TCP dial + SSH handshake on every hop. The default
      (20s) is generous for most networks; raise it for slow or
      unreliable links, lower it if you want to fail fast.
    </p>
    <label class="num">
      <span>Connect timeout (seconds)</span>
      <input
        type="number"
        min="1"
        max="300"
        step="1"
        bind:value={connectTimeoutSeconds}
        onblur={saveConnectTimeout}
        onkeydown={(e) => { if (e.key === "Enter") saveConnectTimeout(); }}
      />
      {#if connectTimeoutSaved}<span class="saved-mark">saved</span>{/if}
    </label>

    {#if !isMobile}
    <h2 style="margin-top: 1.5rem;">In-app local shell</h2>
    <p class="hint">
      Which shell the top-bar <strong>Local shell</strong> button
      opens on plain click (the dropdown chevron next to it still
      lets you launch any of the others one-off). The shell runs
      as a tab inside ssh-tool's terminal pane, sharing the same
      PTY pool as SSH sessions. <strong>Auto</strong> lets the
      backend pick a sensible default per platform - on Windows
      that's WSL when present, then PowerShell, then cmd; on
      Unix it follows <code>$SHELL</code>.
    </p>
    <fieldset class="modes">
      {#each localShellChoices as opt (opt.kind)}
        <label class:active={localShellPrefs.kind === opt.kind}>
          <input
            type="radio"
            name="local-shell"
            checked={localShellPrefs.kind === opt.kind}
            onchange={() => localShellPrefs.set(opt.kind)}
          />
          <div>
            <div class="mode-name">{opt.name}</div>
            <div class="mode-desc">{opt.desc}</div>
          </div>
        </label>
      {/each}
    </fieldset>

    {#if !isMobile && !settingsIsMac}
      <h2 style="margin-top: 1.5rem;">File manager integration</h2>
      <p class="hint">
        Adds <strong>Open in ssh-tool</strong> to the right-click menu
        on directories (Windows Explorer; Dolphin and the Nautilus
        Scripts menu on Linux). Picking it opens the default local
        shell above as a tab, already in that directory. Per-user
        registration, no admin rights.
      </p>
      <div class="scheme-row">
        <span class="lbl">Context menu</span>
        {#if explorerMenuStatus}
          <span class="status-ok">installed</span>
          <code class="status-detail mono">{explorerMenuStatus}</code>
        {:else}
          <span class="status-warn">not installed</span>
        {/if}
        <button class="picker-btn" disabled={explorerMenuBusy} onclick={toggleExplorerMenu}>
          {explorerMenuBusy ? "Working…" : explorerMenuStatus ? "Remove" : "Add to menu"}
        </button>
      </div>
      {#if explorerMenuMsg}
        <p class="hint">{explorerMenuMsg}</p>
      {/if}
    {/if}

    <h2 style="margin-top: 1.5rem;">External terminal</h2>
    <p class="hint">
      Used by two actions: <strong>Native terminal</strong> in the
      top tab bar (opens a fresh shell, nothing attached) and the
      <strong>Open in external terminal</strong> connection
      right-click (spawns the same terminal but runs
      <code>ssh user@host</code> with resolved port + jump chain).
    </p>
    {#if !settingsIsWin}
      <p class="hint">
        {#if settingsIsMac}
          Nothing to configure on macOS - both actions open
          Terminal.app.
        {:else}
          Nothing to configure on Linux - both actions use
          <code>$TERMINAL</code> when set, otherwise the first
          available of <code>x-terminal-emulator</code>,
          gnome-terminal, konsole, xfce4-terminal, alacritty, kitty,
          foot, xterm.
        {/if}
      </p>
    {:else}
    <fieldset class="modes">
      <label class:active={externalTerminal === "windowsterminal"}>
        <input
          type="radio"
          name="ext-term"
          checked={externalTerminal === "windowsterminal"}
          onchange={() => setExternalTerminal("windowsterminal")}
        />
        <div>
          <div class="mode-name">Windows Terminal</div>
          <div class="mode-desc">
            <code>wt.exe new-tab - ssh …</code>. Falls back to
            PowerShell if wt isn't installed.
          </div>
        </div>
      </label>
      <label class:active={externalTerminal === "powershell"}>
        <input
          type="radio"
          name="ext-term"
          checked={externalTerminal === "powershell"}
          onchange={() => setExternalTerminal("powershell")}
        />
        <div>
          <div class="mode-name">PowerShell</div>
          <div class="mode-desc">
            <code>powershell.exe -NoExit -Command ssh …</code>.
          </div>
        </div>
      </label>
      <label class:active={externalTerminal === "cmd"}>
        <input
          type="radio"
          name="ext-term"
          checked={externalTerminal === "cmd"}
          onchange={() => setExternalTerminal("cmd")}
        />
        <div>
          <div class="mode-name">Command Prompt</div>
          <div class="mode-desc">
            <code>cmd.exe /k ssh …</code>.
          </div>
        </div>
      </label>
      <label class:active={externalTerminal === "wsl"}>
        <input
          type="radio"
          name="ext-term"
          checked={externalTerminal === "wsl"}
          onchange={() => setExternalTerminal("wsl")}
        />
        <div>
          <div class="mode-name">WSL (default distro)</div>
          <div class="mode-desc">
            Opens <code>wsl.exe</code> inside Windows Terminal
            (or a console window when WT isn't installed). For
            the connection action, runs
            <code>wsl.exe -e bash -lc "ssh …"</code> so SSH uses
            the WSL distro's OpenSSH client, <code>~/.ssh/config</code>,
            and <code>known_hosts</code>.
          </div>
        </div>
      </label>
    </fieldset>
    {/if}

    <h2 style="margin-top: 1.5rem;">Window</h2>
    {#if settingsIsMac}
      <p class="hint">
        Tray options don't apply on macOS - minimising goes to the
        Dock and the menu-bar icon already offers Show window / Quit.
      </p>
    {:else}
    <p class="hint">
      Both options send the window to the system tray instead of
      down to the taskbar / off entirely. SSH sessions and port
      forwards keep running in the background; click the tray
      icon (or use Show window in the tray menu) to bring the
      window back. Use Quit from the tray menu to actually exit.
      {#if !settingsIsWin}
        Note: on Linux the tray icon needs a desktop environment
        with StatusNotifier support - stock GNOME requires the
        AppIndicator extension. Without a visible tray icon,
        close-to-tray makes the window hard to get back.
      {/if}
    </p>
    <fieldset class="check-cards">
      <label class:active={minimizeToTray}>
        <input
          type="checkbox"
          checked={minimizeToTray}
          onchange={(e) => toggleMinimizeToTray((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Minimise to tray</div>
          <div class="mode-desc">
            The minimise button hides the window into the tray
            instead of dropping it to the taskbar.
          </div>
        </div>
      </label>
      <label class:active={closeToTray}>
        <input
          type="checkbox"
          checked={closeToTray}
          onchange={(e) => toggleCloseToTray((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Close to tray</div>
          <div class="mode-desc">
            Clicking the window's <kbd>×</kbd> button hides the
            window to the tray instead of quitting. Use Quit from
            the tray menu to actually exit.
          </div>
        </div>
      </label>
    </fieldset>
    {/if}
    {/if}<!-- /!isMobile: local shell + external terminal + window/tray -->

    <h2 style="margin-top: 1.5rem;">Startup</h2>
    <p class="hint">
      Reopen the tabs that were open when the app last quit - SSH
      connections, dynamic-inventory hosts and local shells, with
      titles and tab groups included. Pane splits inside a tab are
      not restored yet.
    </p>
    <fieldset class="modes">
      {#each [
        { id: "ask",    name: "Ask on startup (default)", desc: "If the last session had tabs, offer to reconnect them after the vault unlocks." },
        { id: "always", name: "Always reopen",            desc: "Reconnect the last session's tabs silently, no prompt." },
        { id: "never",  name: "Never",                    desc: "Start with no tabs. The last session is still remembered if you switch back." },
      ] as m (m.id)}
        <label class:active={lastSession.mode === m.id}>
          <input
            type="radio"
            name="reopenLastSession"
            checked={lastSession.mode === m.id}
            onchange={() => lastSession.setMode(m.id as any)}
          />
          <div>
            <div class="mode-name">{m.name}</div>
            <div class="mode-desc">{m.desc}</div>
          </div>
        </label>
      {/each}
    </fieldset>
  </div>
  {/if}

  {#if activeSection === "updates"}
  <div class="group">
    <h2>Updates</h2>
    <p class="hint">
      The app checks GitHub Releases
      (<code>github.com/fpenezic/ssh-tool</code>) 5 s after launch
      and then every 6 hours, falling back to
      <code>sshtool.app/api/latest</code> when GitHub is
      unreachable. A new release shows up as a green pill in the
      status bar; click it to open the changelog in your browser.
      No telemetry is sent - the request is a plain GET with the
      User-Agent header.
    </p>
    <fieldset class="check-cards">
      <label class:active={!updateCheckDisabled}>
        <input
          type="checkbox"
          checked={!updateCheckDisabled}
          onchange={(e) => toggleUpdateCheckDisabled(!(e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Check for updates</div>
          <div class="mode-desc">
            Opt-out - on by default. With this off the pill never
            appears and no HTTP requests leave the app.
          </div>
        </div>
      </label>
    </fieldset>

    <h3 style="margin-top:1.2rem">Install update</h3>
    <p class="hint">
      Checks the release server for a newer build. If one's there
      the release-notes modal opens with a Download + Restart and
      install flow. The same modal pops up when you click the
      "vX.Y.Z available" pill in the status bar.
    </p>
    <div class="row" style="gap:0.5rem">
      <button class="primary" onclick={onCheckUpdates} disabled={updateBusy}>
        {updateBusy ? "Checking…" : "Check now"}
      </button>
    </div>
    {#if updateError}<div class="err">{updateError}</div>{/if}
    {#if updateMsg}<div class="ok">{updateMsg}</div>{/if}
  </div>
  {/if}

  {#if updateModalOpen}
    <UpdateModal onClose={() => (updateModalOpen = false)} />
  {/if}

  {#if activeSection === "terminal"}
  <div class="group">
    <h2>Terminal</h2>
    {#if !isMobile}
    <p class="hint">
      Copy / paste behavior. Auto-detected from your OS on first launch
      - pick a different model if you prefer.
    </p>

    <fieldset class="modes">
      <label class:active={copyPastePrefs.mode === "windows"}>
        <input
          type="radio"
          name="cp-mode"
          checked={copyPastePrefs.mode === "windows"}
          onchange={() => pickCopyPaste("windows")}
        />
        <div>
          <div class="mode-name">Windows</div>
          <div class="mode-desc">
            Ctrl+Shift+C copy · Ctrl+Shift+V paste · right-click is smart
            (copy when selection exists, paste otherwise). Ctrl+C copies
            and clears the selection when there is one, so the next Ctrl+C
            interrupts; with nothing selected it is SIGINT as usual.
          </div>
        </div>
      </label>
      <label class:active={copyPastePrefs.mode === "linux"}>
        <input
          type="radio"
          name="cp-mode"
          checked={copyPastePrefs.mode === "linux"}
          onchange={() => pickCopyPaste("linux")}
        />
        <div>
          <div class="mode-name">Linux</div>
          <div class="mode-desc">
            Auto-copy on selection · Ctrl+Shift+C/V or middle-click ·
            right-click pastes. Ctrl+C is always SIGINT.
          </div>
        </div>
      </label>
      <label class:active={copyPastePrefs.mode === "mac"}>
        <input
          type="radio"
          name="cp-mode"
          checked={copyPastePrefs.mode === "mac"}
          onchange={() => pickCopyPaste("mac")}
        />
        <div>
          <div class="mode-name">macOS</div>
          <div class="mode-desc">
            Cmd+C copy · Cmd+V paste · right-click pastes. Ctrl+C is
            always SIGINT.
          </div>
        </div>
      </label>
    </fieldset>
    {/if}

    <p class="hint">
      Font size: <strong>{terminalPrefs.fontSize}px</strong>
      - adjust with {#if isMobile}a two-finger pinch{:else}<kbd>Ctrl</kbd>+wheel{/if} inside any terminal.
      <button class="link" onclick={() => terminalPrefs.resetFontSize()}>Reset to 13</button>
    </p>

    <label class="num">
      <span>Font family</span>
      <input
        type="text"
        value={terminalPrefs.fontFamily}
        onblur={(e) => terminalPrefs.setFontFamily((e.target as HTMLInputElement).value)}
        onkeydown={(e) => {
          if (e.key === "Enter") terminalPrefs.setFontFamily((e.currentTarget as HTMLInputElement).value);
        }}
        placeholder={DEFAULT_FONT_FAMILY}
        style="width: 28rem; max-width: 100%;"
      />
    </label>
    <p class="hint inline">
      CSS font-family stack. Defaults to <code>{DEFAULT_FONT_FAMILY}</code>.
      Clear to reset.
    </p>

    <label class="num">
      <span>Scrollback (lines)</span>
      <input
        type="number"
        min="500"
        max="100000"
        step="500"
        value={terminalPrefs.scrollback}
        onblur={(e) => terminalPrefs.setScrollback(parseInt((e.target as HTMLInputElement).value, 10))}
        onkeydown={(e) => {
          if (e.key === "Enter") terminalPrefs.setScrollback(parseInt((e.currentTarget as HTMLInputElement).value, 10));
        }}
      />
    </label>
    <p class="hint inline">
      How many lines xterm keeps in memory per session. Default {DEFAULT_SCROLLBACK}.
      Applies to newly-opened sessions immediately; existing ones get
      the new limit but won't grow the buffer they already have.
      Note: this is the live on-screen history. When a tab is detached,
      re-docked, or the UI reloads, only roughly the last ~2000 lines
      replay from the backend - the full scrollback above isn't preserved
      across those events.
    </p>

    <label class="toggle">
      <input
        type="checkbox"
        checked={terminalPrefs.closeOnCleanExit}
        onchange={(e) => terminalPrefs.setCloseOnCleanExit((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Auto-close tab on clean exit</strong>
        <span class="hint inline">
          - when the remote shell exits normally (Ctrl+D, <code>exit 0</code>),
          close the tab automatically. Non-zero exits and network drops stay
          open so you can see what happened.
        </span>
      </span>
    </label>

    <label class="toggle">
      <input
        type="checkbox"
        checked={terminalPrefs.disableWebgl}
        onchange={(e) => terminalPrefs.setDisableWebgl((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Disable WebGL renderer (use canvas fallback)</strong>
        <span class="hint inline">
          - on by default: the WebGL glyph atlas can spontaneously corrupt
          into garbled text on some GPUs. Untick to opt back into WebGL
          for faster rendering of heavy output. Reopen each terminal tab
          for the change to take effect.
        </span>
      </span>
    </label>

    <label class="toggle">
      <input
        type="checkbox"
        checked={terminalPrefs.serverStatsEnabled}
        onchange={(e) => terminalPrefs.setServerStatsEnabled((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Show server status for the focused session</strong>
        <span class="hint inline">
          - the status bar shows load, memory, disk and logged-in users for
          the SSH host of whichever pane is focused, refreshed every 10s.
          Off by default: it runs a small read-only probe (reads /proc, df,
          who) on the remote, so only enable it for hosts where that's
          wanted. Non-Linux hosts / network gear simply show nothing.
        </span>
      </span>
    </label>

    <p class="hint">Color scheme</p>
    <div class="themes">
      {#each themes as t (t.id)}
        {@const active = terminalPrefs.themeId === t.id}
        <button
          class="theme-card"
          class:active
          onclick={() => terminalPrefs.setTheme(t.id)}
          title={t.name}
        >
          <div
            class="theme-preview"
            style="background: {t.background}; color: {t.foreground}"
          >
            <span style="color: {t.red}">●</span><span
              style="color: {t.green}">●</span><span
              style="color: {t.yellow}">●</span><span
              style="color: {t.blue}">●</span><span
              style="color: {t.magenta}">●</span><span
              style="color: {t.cyan}">●</span>
            <div class="prompt">$ ls -la</div>
          </div>
          <div class="theme-label">
            {t.name}
            {#if t.isLight}<span class="light-tag">light</span>{/if}
          </div>
        </button>
      {/each}
    </div>
  </div>

  {:else if activeSection === "recording"}
  <div class="group">
    <h2>Session recording</h2>
    <p class="hint">
      Right-click a tab (or use the command palette) to record a
      session's terminal output to an asciicast v2 <code>.cast</code>
      file - replayable with asciinema or any web player. Output only:
      keystrokes are never written to the file, so typed passwords
      can't leak into a recording.
    </p>
    <p class="hint">
      What the session prints does land in the file, in plaintext - a
      <code>cat</code> of a config, a token a command echoes back. The file is
      not encrypted; treat it like the terminal it came from.
    </p>
    {#if recordingsDirPath}
      <p class="hint">
        Recordings folder: <code>{recordingsDirPath}</code>
      </p>
    {/if}
    <div class="row" style="gap:0.5rem">
      <button onclick={() => recordingsModal.open()}>Browse recordings…</button>
      <button onclick={changeRecordingsDir}>Change folder…</button>
      <button onclick={resetRecordingsDir}>Reset to default</button>
      <button onclick={() => api.recordingsOpenDir().catch((e) => toast.err(errMsg(e)))}>
        Open folder
      </button>
    </div>

    <label class="check" style="margin-top:0.9rem">
      <input
        type="checkbox"
        checked={recordingConfirm}
        onchange={toggleRecordingConfirm}
      />
      <span>
        <strong>Ask before starting a recording</strong>
        <span class="field-note">
          - a confirmation step so a misclick doesn't quietly start writing the
          session to disk. Turn it off if you record routinely.
        </span>
      </span>
    </label>
  </div>

  {:else if activeSection === "network"}
  <div class="group">
    <h2>Network profiles</h2>
    <p class="hint">
      Userspace tunnels - no TUN adapter, no admin rights, no system
      routes. Assign a profile to a folder or connection (Network
      setting) and its first SSH hop dials through the tunnel.
      WireGuard is built in; NetBird needs the optional plugin below.
    </p>

    <h3 style="margin-top:0.8rem">Plugins</h3>
    <p class="hint">
      Optional overlay-network clients, kept out of the main app to
      keep it small. Downloaded from the GitHub release matching this
      version and verified by checksum before install.
    </p>
    {#each plugins as pl (pl.name)}
      <div class="np-card">
        <div class="np-head">
          <strong>{pl.name === "netbird" ? "NetBird" : pl.name === "tailscale" ? "Tailscale" : pl.name}</strong>
          {#if !pl.supported}
            <span class="np-pill">not on this platform</span>
          {:else if pl.installed && pl.update_available}
            <span class="np-pill paused">update available</span>
          {:else if pl.installed}
            <span class="np-pill running">installed{pl.version ? ` ${pl.version}` : ""}</span>
          {:else}
            <span class="np-pill">not installed</span>
          {/if}
          {#if pl.installed}<span class="np-meta">{pl.path}</span>{/if}
        </div>
        {#if pl.supported}
          {#if pl.update_available}
            <p class="hint" style="margin:0.2rem 0">
              Installed {pl.version || "(unknown)"}, app is {versionInfo?.version ?? "?"}. Reinstall to match.
            </p>
          {/if}
          <div class="np-actions">
            {#if pl.installed}
              <button class:primary={pl.update_available} onclick={() => pluginDownload(pl.name)} disabled={pluginBusy}>
                {pl.update_available ? "Update" : "Reinstall"}
              </button>
              <button class="danger" onclick={() => pluginRemove(pl.name)} disabled={pluginBusy}>Remove</button>
            {:else}
              <button class="primary" onclick={() => pluginDownload(pl.name)} disabled={pluginBusy}>Download</button>
            {/if}
          </div>
        {/if}
      </div>
    {/each}

    <h3 style="margin-top:1.2rem">Profiles</h3>

    {#if networkProfiles.error}
      <p class="hint" style="color: var(--red)">{networkProfiles.error}</p>
    {/if}

    {#each networkProfiles.list as np (np.id)}
      <div class="np-card">
        <div class="np-head">
          <strong>{np.name}</strong>
          <span class="np-kind">{np.kind === "netbird" ? "NetBird" : np.kind === "tailscale" ? "Tailscale" : "WireGuard"}</span>
          {#if np.paused}
            <span class="np-pill paused">paused</span>
          {:else if np.status.running}
            <span class="np-pill running" title={np.kind === "wireguard" && np.status.last_handshake > 0
              ? `last handshake ${new Date(np.status.last_handshake * 1000).toLocaleTimeString()}`
              : "up"}>up</span>
          {:else}
            <span class="np-pill">idle</span>
          {/if}
          {#if npRemoteOwners[np.id]}
            <span class="np-pill remote" title="This WireGuard tunnel is live on another synced machine. Only one machine can hold it at a time.">
              up on {npRemoteOwners[np.id].machine_name}
            </span>
          {/if}
          <span class="np-meta">
            {#if np.kind === "netbird"}
              {np.netbird?.device_name || "ssh-tool"}
              {#if np.netbird?.management_url}&nbsp;· {np.netbird.management_url}{/if}
              {#if np.status.running && (np.status.peers ?? 0) > 0}&nbsp;· {np.status.peers} peer{np.status.peers === 1 ? "" : "s"}{/if}
            {:else if np.kind === "tailscale"}
              {np.tailscale?.hostname || "ssh-tool"}
              {#if np.tailscale?.control_url}&nbsp;· {np.tailscale.control_url}{/if}
              {#if np.status.running && (np.status.peers ?? 0) > 0}&nbsp;· {np.status.peers} peer{np.status.peers === 1 ? "" : "s"}{/if}
            {:else}
              {np.profile.addresses?.join(", ")}
              {#if np.profile.peers?.length}&nbsp;→ {np.profile.peers[0].endpoint}{/if}
              {#if np.status.running && (np.status.rx_bytes > 0 || np.status.tx_bytes > 0)}
                &nbsp;· rx {(np.status.rx_bytes / 1024).toFixed(0)}K tx {(np.status.tx_bytes / 1024).toFixed(0)}K
              {/if}
            {/if}
          </span>
        </div>
        <div class="np-actions">
          <label class="np-mode">Mode
            <select
              value={np.mode === "auto" ? "auto" : "always"}
              onchange={(e) => npSetPolicy(np, (e.target as HTMLSelectElement).value, np.paused)}
            >
              <option value="always">Always via tunnel</option>
              <option value="auto">Auto - direct first, tunnel fallback</option>
            </select>
          </label>
          <button onclick={() => npSetPolicy(np, np.mode === "auto" ? "auto" : "always", !np.paused)}>
            {np.paused ? "Resume" : "Pause (go direct)"}
          </button>
          <button onclick={() => npTest(np)} disabled={npBusy || np.paused}>Test</button>
          {#if np.status.running}
            <button onclick={() => api.networkProfileStop(np.id).catch((e) => toast.err(errMsg(e)))}>Stop tunnel</button>
          {/if}
          {#if npRemoteOwners[np.id]}
            <button
              onclick={() => npDisconnectRemote(np)}
              disabled={npDisconnecting[np.id]}
              title="Ask {npRemoteOwners[np.id].machine_name} to drop this tunnel so it's free"
            >
              {npDisconnecting[np.id] ? "Disconnecting…" : `Disconnect on ${npRemoteOwners[np.id].machine_name}`}
            </button>
          {/if}
          <button onclick={() => npStartEdit(np)}>Edit</button>
          <button class="danger" onclick={() => npDelete(np)}>Delete</button>
        </div>
      </div>
    {/each}
    {#if networkProfiles.list.length === 0 && !networkProfiles.loading}
      <p class="hint">No profiles yet - add one below.</p>
    {/if}

    <h3 style="margin-top:1.2rem">{npEditingId ? "Edit profile" : "Add profile"}</h3>

    {#if !npEditingId}
      <div class="row" style="gap:0.5rem; align-items:center">
        <span class="hint" style="margin:0">Type:</span>
        <label class="np-kindpick"><input type="radio" bind:group={npKind} value="wireguard" /> WireGuard</label>
        <label class="np-kindpick"><input type="radio" bind:group={npKind} value="netbird" /> NetBird</label>
        <label class="np-kindpick"><input type="radio" bind:group={npKind} value="tailscale" /> Tailscale</label>
      </div>
    {/if}

    <label>
      <span>Name</span>
      <input bind:value={npName} placeholder="e.g. office-vpn" />
    </label>

    {#if (npEditingId ? npEditKind : npKind) === "netbird"}
      {#if !nbInstalled}
        <p class="hint warn-note">
          The NetBird plugin is not installed. Install it in the Plugins
          card above before creating a NetBird profile.
        </p>
      {/if}
      <label>
        <span>Management URL <span class="hint inline">(blank = netbird.io cloud)</span></span>
        <input bind:value={nbManagement} placeholder="https://netbird.example.com" />
      </label>
      <label>
        <span>Device name</span>
        <input bind:value={nbDevice} placeholder="laptop.ssh-tool" />
      </label>
      <label>
        <span class="row" style="justify-content:space-between; align-items:center; gap:0.5rem">
          Setup key credential
          <button type="button" class="token-add" onclick={() => (nbNewKeyOpen = !nbNewKeyOpen)}>
            {nbNewKeyOpen ? "Cancel" : "+ New"}
          </button>
        </span>
        {#if !nbNewKeyOpen}
          <SearchableSelect
            bind:value={nbCredId}
            options={apiTokenCredOptions}
            placeholder="Pick an API-token credential holding the setup key…"
          />
          <span class="hint inline">
            A NetBird <strong>setup key</strong> (from Setup Keys in the
            dashboard - a UUID like <code>A1B2C3D4-...</code>), NOT a
            personal access token. Use a <strong>reusable</strong> key if
            you sync this profile across machines - each registers as its
            own peer.
          </span>
        {/if}
      </label>
      {#if nbNewKeyOpen}
        <div class="np-card" style="gap:0.5rem">
          <label>
            <span>Credential name</span>
            <input bind:value={nbNewKeyName} placeholder="netbird-setup-key" />
          </label>
          <label>
            <span>Setup key <span class="hint inline">(from NetBird dashboard -&gt; Setup Keys, not a PAT)</span></span>
            <PasswordInput bind:value={nbNewKeySecret} placeholder="e.g. A1B2C3D4-E5F6-..." />
          </label>
          <div class="row" style="gap:0.5rem">
            <button class="primary" onclick={nbCreateSetupKey} disabled={nbNewKeyBusy || !nbNewKeyName.trim() || !nbNewKeySecret}>Create</button>
          </div>
        </div>
      {/if}
      <div class="row" style="gap:0.5rem">
        {#if npEditingId}
          <button class="primary" onclick={npSaveNetbird} disabled={npBusy || !npName.trim()}>Save changes</button>
          <button onclick={npCancelEdit}>Cancel</button>
        {:else}
          <button class="primary" onclick={npCreateNetbird} disabled={npBusy || !nbInstalled || !npName.trim() || !nbCredId}>Add profile</button>
        {/if}
      </div>
    {:else if (npEditingId ? npEditKind : npKind) === "tailscale"}
      {#if !tsInstalled}
        <p class="hint warn-note">
          The Tailscale plugin is not installed. Install it in the Plugins
          card above before creating a Tailscale profile.
        </p>
      {/if}
      <label>
        <span>Control URL <span class="hint inline">(blank = Tailscale's own; set for self-hosted Headscale)</span></span>
        <input bind:value={tsControl} placeholder="https://headscale.example.com" />
      </label>
      <label>
        <span>Hostname <span class="hint inline">(tailnet node name)</span></span>
        <input bind:value={tsHostname} placeholder="laptop" />
      </label>
      <label>
        <span class="row" style="justify-content:space-between; align-items:center; gap:0.5rem">
          Auth key credential
          <button type="button" class="token-add" onclick={() => (tsNewKeyOpen = !tsNewKeyOpen)}>
            {tsNewKeyOpen ? "Cancel" : "+ New"}
          </button>
        </span>
        {#if !tsNewKeyOpen}
          <SearchableSelect
            bind:value={tsCredId}
            options={apiTokenCredOptions}
            placeholder="Pick an API-token credential holding the auth key…"
          />
          <span class="hint inline">
            A Tailscale <strong>auth key</strong> (Settings -&gt; Keys in the
            admin console - starts with <code>tskey-auth-</code>). Use a
            <strong>reusable</strong> key if you sync this profile across
            machines - each registers as its own node.
          </span>
        {/if}
      </label>
      {#if tsNewKeyOpen}
        <div class="np-card" style="gap:0.5rem">
          <label>
            <span>Credential name</span>
            <input bind:value={tsNewKeyName} placeholder="tailscale-auth-key" />
          </label>
          <label>
            <span>Auth key <span class="hint inline">(from Tailscale admin -&gt; Settings -&gt; Keys)</span></span>
            <PasswordInput bind:value={tsNewKeySecret} placeholder="tskey-auth-..." />
          </label>
          <div class="row" style="gap:0.5rem">
            <button class="primary" onclick={tsCreateAuthKey} disabled={tsNewKeyBusy || !tsNewKeyName.trim() || !tsNewKeySecret}>Create</button>
          </div>
        </div>
      {/if}
      <div class="row" style="gap:0.5rem">
        {#if npEditingId}
          <button class="primary" onclick={npSaveTailscale} disabled={npBusy || !npName.trim()}>Save changes</button>
          <button onclick={npCancelEdit}>Cancel</button>
        {:else}
          <button class="primary" onclick={npCreateTailscale} disabled={npBusy || !tsInstalled || !npName.trim() || !tsCredId}>Add profile</button>
        {/if}
      </div>
    {:else}
      <label>
        <span>{npEditingId ? "Config (**KEEP** = stored secret stays; paste a new key to replace it)" : "wg-quick config"}</span>
        <textarea
          bind:value={npConf}
          rows="10"
          spellcheck="false"
          placeholder={"[Interface]\nPrivateKey = ...\nAddress = 10.0.0.2/32\nDNS = 10.0.0.1\n\n[Peer]\nPublicKey = ...\nEndpoint = vpn.example.com:51820\nAllowedIPs = 10.0.0.0/24"}
        ></textarea>
      </label>
      <div class="row" style="gap:0.5rem">
        {#if npEditingId}
          <button class="primary" onclick={npSaveEdit} disabled={npBusy || !npName.trim()}>Save changes</button>
          <button onclick={npCancelEdit}>Cancel</button>
        {:else}
          <button class="primary" onclick={npCreate} disabled={npBusy || !npName.trim() || !npConf.trim()}>Add profile</button>
        {/if}
      </div>
      <p class="hint">
        PostUp/PostDown/Table lines are ignored (there is no system
        interface). The endpoint must be reachable from the normal
        network; DNS servers listed in the config resolve hostnames
        inside the tunnel.
      </p>
      <p class="hint warn-note">
        <strong>One identity across machines.</strong> A WireGuard profile
        carries a single key and overlay IP. If it is synced and the tunnel
        is left up on another machine, bringing it up here makes both peers
        fight for the same identity and degrades both. Stop it on the other
        machine first, or use NetBird (peer-per-device) when you routinely
        connect from more than one machine.
      </p>
    {/if}
  </div>

  {:else if activeSection === "browser"}
  <div class="group">
    <h2>Browser launcher</h2>
    <p class="hint">
      When you click <strong>Open in browser</strong> on a SOCKS5 forward, we
      try to launch a chromium-family or Firefox-family browser with the proxy
      preconfigured. By default the profile is isolated (fresh each time); the
      Profile option below keeps it persistent so saved logins survive.
    </p>
    <p class="hint">
      Leave the field below empty for auto-detection (preferred). Pin a path
      only if the default isn't what you want - for example, you've installed
      Chrome on WSL from the Google deb repo and want to use it instead of
      the Windows host's browser.
    </p>
    <label>
      Preferred browser binary
      <input
        bind:value={browserPath}
        placeholder="e.g. /usr/bin/google-chrome, /Applications/Firefox.app/Contents/MacOS/firefox"
      />
    </label>
    <div class="row">
      <button class="primary" onclick={save}>Save</button>
      {#if savedAt}
        <span class="ok">Saved {savedAt}</span>
      {/if}
    </div>
    <p class="hint">
      Currently active: <code>{savedPath || "(auto-detect)"}</code>
    </p>

    <h2 style="margin-top:1.5rem">Profile</h2>
    <label class="row" style="align-items:flex-start;gap:0.5rem">
      <input
        type="checkbox"
        checked={browserPersistent}
        onchange={(e) => toggleBrowserPersistent((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Use a persistent browser profile</strong>
        <span class="hint" style="display:block">
          Keeps logins and cookies between launches, so a tunnelled site that
          needs your saved credentials stays signed in. Still a dedicated
          profile - separate from your everyday browser, so normal browsing
          isn't routed through the tunnel. Off (default) opens a fresh isolated
          profile each time. Works with both Chromium- and Firefox-family
          browsers (on WSL, a persistent Firefox profile falls back to isolated).
        </span>
      </span>
    </label>
  </div>

  {:else if activeSection === "snippets"}
  <div class="group">
    <h2>Snippets</h2>
    <p class="hint">
      Reusable command snippets you can fire into the active terminal
      with <kbd>Ctrl+Shift+P</kbd>. Useful for "sudo apt update", health
      checks, log tails - anything you re-type a dozen times a day.
      Global by default; leave per-connection scoping for later.
    </p>

    <div class="snip-grid">
      <div class="snip-list">
        <div class="snip-list-head">
          <strong>Library</strong>
          <button class="primary" onclick={newSnippet}>+ New</button>
        </div>
        {#if snippets.length === 0}
          <div class="empty">No snippets yet. Add one on the right.</div>
        {:else}
          <div class="snip-rows">
            {#each snippets as s (s.id)}
              <div
                class="snip-row"
                class:active={snippetEditing?.id === s.id}
                role="button"
                tabindex="0"
                onclick={() => editSnippet(s)}
                onkeydown={(e) => { if (e.key === "Enter") editSnippet(s); }}
              >
                <div class="snip-row-main">
                  <div class="snip-name">{s.name}</div>
                  <div class="snip-preview">{s.body.split("\n")[0].slice(0, 80)}</div>
                </div>
                <button
                  class="del"
                  title="Delete"
                  onclick={(e) => { e.stopPropagation(); deleteSnippet(s); }}
                >✕</button>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <div class="snip-editor">
        <strong>{snippetEditing ? "Edit" : "New"} snippet</strong>
        {#if snippetErr}<div class="err">{snippetErr}</div>{/if}
        <label>
          <span>Name</span>
          <input bind:value={snippetForm.name} placeholder="e.g. tail syslog" />
        </label>
        <label>
          <span>Body</span>
          <textarea bind:value={snippetForm.body} rows="6" placeholder="sudo tail -f /var/log/syslog"></textarea>
        </label>
        <label>
          <span>Tags (comma-separated)</span>
          <input bind:value={snippetTagsRaw} placeholder="diag, logs" />
        </label>
        <div class="snip-actions">
          <button class="primary" disabled={snippetBusy} onclick={saveSnippet}>
            {snippetBusy ? "Saving…" : (snippetEditing ? "Save changes" : "Create")}
          </button>
          {#if snippetEditing}
            <button onclick={newSnippet}>New</button>
          {/if}
        </div>
      </div>
    </div>
  </div>

  {:else if activeSection === "workspaces"}
  <div class="group">
    <h2>Workspaces</h2>
    <p class="hint">
      Workspaces are named bundles of "these tabs in this layout".
      Use them to switch between, say, "Client A production" and
      "client B staging" sets with one click. Saving snapshots only
      the active leaf of each tab - multi-pane splits aren't
      restored yet.
    </p>

    {#if wsErr}<div class="err">{wsErr}</div>{/if}

    <div class="snip-actions">
      <button class="primary" onclick={wsSaveNew} disabled={paneTabs.tabs.length === 0}>
        + Save current as workspace
      </button>
      <span class="hint inline">
        {paneTabs.tabs.length} tab{paneTabs.tabs.length === 1 ? "" : "s"} currently open
      </span>
    </div>

    {#if workspaces.list.length === 0}
      <div class="empty">No workspaces yet.</div>
    {:else}
      <div class="snip-rows">
        {#each workspaces.list as w (w.id)}
          <div class="snip-row">
            <div class="snip-row-main">
              <div class="snip-name">{w.name}</div>
              <div class="snip-preview">
                {#if w.last_opened_at}
                  Last opened {new Date(w.last_opened_at * 1000).toLocaleString()}
                {:else}
                  Never opened
                {/if}
              </div>
            </div>
            <button
              disabled={wsBusyId === w.id}
              onclick={() => wsOpen(w.id)}
              title="Disconnect current tabs and open this workspace"
            >{wsBusyId === w.id ? "…" : "Open"}</button>
            <button
              disabled={wsBusyId === w.id || paneTabs.tabs.length === 0}
              onclick={() => wsOverwrite(w.id, w.name)}
              title="Overwrite this workspace with the current tab set"
            >Save here</button>
            <button
              class="del"
              title="Delete"
              onclick={() => wsDelete(w.id, w.name)}
            >✕</button>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  {:else if activeSection === "vault"}
  <div class="group">
    <h2>Vault</h2>
    <p class="hint">
      The vault holds your credentials' encrypted secrets. Once unlocked,
      it stays unlocked for the lifetime of the app process (or until you
      hit Lock somewhere). Set an idle timeout below to re-lock it
      automatically.
    </p>
    {#if vaultSidecarStrength === "weak"}
      <div class="warn-note hint">
        <strong>Auto-unlock is weakly bound to this machine.</strong>
        This platform stores the auto-unlock sidecar in the older format,
        whose key can fall back to the hostname. Someone who steals both the
        vault file and the sidecar, and can guess or spoof this machine's
        hostname, could unlock it. Your typed passphrase and the encrypted
        vault itself are unaffected - this only concerns the convenience
        auto-unlock. To require the passphrase on every launch instead, use
        "Lock vault now" (it forgets the sidecar).
      </div>
    {/if}
    <label class="num">
      <span>Auto-lock after idle (minutes)</span>
      <input
        type="number"
        min="0"
        max="240"
        step="1"
        value={vaultPrefs.autoLockMinutes}
        onblur={(e) => vaultPrefs.setAutoLockMinutes(parseInt((e.target as HTMLInputElement).value, 10))}
        onkeydown={(e) => { if (e.key === "Enter") vaultPrefs.setAutoLockMinutes(parseInt((e.currentTarget as HTMLInputElement).value, 10)); }}
      />
    </label>
    <p class="hint inline">
      0 = never auto-lock (current default). Activity = mouse movement,
      key press, scroll. Open SSH sessions and port forwards keep
      running across a lock; only the credential tree gets re-protected.
    </p>

    <h3 style="margin-top:1.2rem">Lock now</h3>
    <p class="hint">
      Forget the in-memory vault key immediately. The next
      vault-backed action (new SSH connection, credential edit,
      passphrase rotation) will re-prompt. Also forgets the
      auto-unlock sidecar so the next app launch prompts too.
    </p>
    <div class="row">
      <button disabled={lockBusy} onclick={onLockNow}>
        {lockBusy ? "Locking…" : "Lock vault now"}
      </button>
      {#if lockNotice}<span class="ok inline">{lockNotice}</span>{/if}
    </div>

    <h3 style="margin-top:1.2rem">Change master passphrase</h3>
    <p class="hint">
      Re-derives the file key from a new passphrase and re-encrypts
      every secret. Vault must be unlocked. If an auto-unlock sidecar
      is set up, it is refreshed with the new passphrase. Existing
      backups still need the OLD passphrase to restore - make a fresh
      backup right after rotating.
    </p>
    <div class="form-grid">
      <label>
        <span>Current passphrase</span>
        <PasswordInput
          autocomplete="current-password"
          bind:value={rotateOld}
          disabled={rotateBusy}
        />
      </label>
      <label>
        <span>New passphrase</span>
        <PasswordInput
          autocomplete="new-password"
          bind:value={rotateNew}
          disabled={rotateBusy}
        />
      </label>
      <label>
        <span>Confirm new passphrase</span>
        <PasswordInput
          autocomplete="new-password"
          bind:value={rotateConfirm}
          disabled={rotateBusy}
        />
      </label>
    </div>
    {#if rotateError}<div class="err">{rotateError}</div>{/if}
    {#if rotateNotice}<div class="ok">{rotateNotice}</div>{/if}
    <div class="row">
      <button class="primary" disabled={rotateBusy} onclick={onRotatePassphrase}>
        {rotateBusy ? "Rotating…" : "Rotate passphrase"}
      </button>
    </div>
  </div>

  {:else if activeSection === "keepass"}
  <div class="group group-wide">
    <KeepassSettings />
  </div>

  {:else if activeSection === "bitwarden"}
  <div class="group group-wide">
    <BitwardenSettings />
  </div>

  {:else if activeSection === "infisical"}
  <div class="group group-wide">
    <InfisicalSettings />
  </div>

  {:else if activeSection === "audit"}
  <div class="group group-wide">
    <h2>Audit log</h2>
    <p class="hint">
      Local append-only record of sensitive operations: vault
      unlock / lock / rotate, backup create / restore, SSH connect /
      disconnect. Lives in <code>store.db</code> next to the rest of
      your data - never sent anywhere. Useful for "did I really
      connect to that host yesterday" and for shipping evidence to
      a compliance review.
    </p>
    <div class="row" style="gap:0.5rem; flex-wrap:wrap">
      <label class="num" style="flex:1; min-width:14rem">
        <span>Filter</span>
        <input
          type="text"
          placeholder="vault, ssh, 10.0.1.5, root, …"
          bind:value={auditFilter}
        />
      </label>
      <label class="num">
        <span>Limit</span>
        <input
          type="number"
          min="50"
          max="5000"
          step="50"
          bind:value={auditLimit}
        />
      </label>
      <button onclick={loadAudit} disabled={auditBusy}>{auditBusy ? "Loading…" : "Refresh"}</button>
      <button onclick={exportAuditCSV} disabled={auditEvents.length === 0}>Export CSV</button>
    </div>
    {#if auditError}<div class="err">{auditError}</div>{/if}

    {#if auditEvents.length === 0}
      <p class="hint">No events yet.</p>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
      <div
        class="audit-tbl-wrap selectable"
        tabindex="0"
        role="log"
        onkeydown={onAuditKeydown}
      >
        <table class="audit-tbl">
          <colgroup>
            <col class="col-time" />
            <col class="col-action" />
            <col class="col-host" />
            <col class="col-user" />
            <col class="col-target" />
            <col class="col-extra" />
          </colgroup>
          <thead>
            <tr>
              <th
                class="sortable"
                onclick={() => toggleAuditSort("ts")}
                title="Click to sort by time"
              >
                Time
                {#if auditSortBy === "ts"}
                  <span class="sort-ind">{auditSortDir === "desc" ? "▼" : "▲"}</span>
                {/if}
              </th>
              <th
                class="sortable"
                onclick={() => toggleAuditSort("action")}
                title="Click to sort by action"
              >
                Action
                {#if auditSortBy === "action"}
                  <span class="sort-ind">{auditSortDir === "desc" ? "▼" : "▲"}</span>
                {/if}
              </th>
              <th>Host</th>
              <th>User</th>
              <th>Target</th>
              <th>Details</th>
            </tr>
          </thead>
          <tbody>
            {#each sortedAuditEvents as ev (ev.id)}
              {@const expanded = auditExpanded.has(ev.id)}
              {@const extras = extraMeta(ev)}
              {@const host = extractedMeta(ev, "host")}
              {@const port = extractedMeta(ev, "port")}
              {@const user = extractedMeta(ev, "user")}
              {@const name = extractedMeta(ev, "name")}
              <tr>
                <td class="ts"><time>{new Date(ev.ts * 1000).toLocaleString()}</time></td>
                <td><code>{ev.action}</code></td>
                <td class="host">{host}{port && port !== "0" && port !== "22" ? `:${port}` : ""}</td>
                <td class="user">{user}</td>
                <td class="target" title={ev.target}>{name || ev.target}</td>
                <td class="extra">
                  {#if expanded}
                    <div class="meta-full">
                      {#each Object.entries(ev.metadata) as [k, v]}
                        <div class="kv-row"><b>{k}</b><span>{v}</span></div>
                      {/each}
                      {#if ev.target && !name}
                        <div class="kv-row"><b>target</b><span>{ev.target}</span></div>
                      {/if}
                    </div>
                    <button class="link-btn" onclick={() => toggleAuditExpand(ev.id)}>collapse</button>
                  {:else}
                    {#if extras.length > 0}
                      <div class="meta-summary">
                        {#each extras.slice(0, 2) as [k, v]}
                          <span class="kv"><b>{k}</b>={v}</span>
                        {/each}
                        {#if extras.length > 2}
                          <span class="kv more">+{extras.length - 2}</span>
                        {/if}
                      </div>
                    {/if}
                    {#if Object.keys(ev.metadata).length > 0 || ev.target}
                      <button class="link-btn" onclick={() => toggleAuditExpand(ev.id)}>expand</button>
                    {/if}
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    <h3 style="margin-top:1.2rem">Retention</h3>
    <div class="row" style="gap:0.5rem">
      <label class="num">
        <span>Delete events older than (days)</span>
        <input
          type="number"
          min="1"
          max="3650"
          step="1"
          bind:value={auditPurgeDays}
        />
      </label>
      <button onclick={onAuditPurge}>Purge</button>
    </div>
  </div>

  {:else if activeSection === "backup"}
  <div class="group">
    <h2>Backup & restore</h2>
    <p class="hint">
      An encrypted snapshot of <code>store.db</code> +
      <code>vault.enc</code>, sealed with your vault master passphrase.
      Backups land in the app's data directory under
      <code>backups/</code> so they sit next to the live data - handy
      for crash recovery, awkward for off-site safety. Copy them
      elsewhere too if you care.
    </p>

    <h3 style="margin-top:0.8rem">Automatic daily backup</h3>
    <label class="toggle">
      <input
        type="checkbox"
        checked={autoBackupPrefs.enabled}
        onchange={(e) => saveAutoBackupPrefs({ ...autoBackupPrefs, enabled: (e.target as HTMLInputElement).checked })}
      />
      <span>
        <strong>Run a backup in the background every 24 hours</strong>
        <span class="hint inline">
          - needs the vault auto-unlock sidecar; if the vault is locked
          and no sidecar is set up, the run is silently skipped.
        </span>
      </span>
    </label>
    <label class="num">
      <span>Keep last N backups</span>
      <input
        type="number"
        min="1"
        max="365"
        step="1"
        value={autoBackupPrefs.keep_last}
        onblur={(e) => saveAutoBackupPrefs({ ...autoBackupPrefs, keep_last: parseInt((e.target as HTMLInputElement).value, 10) || 7 })}
        onkeydown={(e) => { if (e.key === "Enter") saveAutoBackupPrefs({ ...autoBackupPrefs, keep_last: parseInt((e.currentTarget as HTMLInputElement).value, 10) || 7 }); }}
      />
    </label>
    <p class="hint inline">
      Older auto-backups and pre-restore safety snapshots are pruned to
      the same N. Manual backups (the ones you create with the button
      below) are never auto-deleted.
    </p>

    <h3 style="margin-top:1rem">Manual backup</h3>
    <div class="row">
      <button class="primary" disabled={backupBusy} onclick={onBackupCreate}>
        {backupBusy ? "Working…" : "Create backup now"}
      </button>
      <button disabled={backupBusy} onclick={loadBackups}>Refresh</button>
    </div>
    {#if backupError}
      <div class="err">{backupError}</div>
    {/if}
    {#if backupNotice}
      <div class="ok">{backupNotice}</div>
    {/if}

    <h3 style="margin-top:1rem">Existing backups</h3>
    {#if backupList.length === 0}
      <p class="hint">No backups yet.</p>
    {:else}
      <ul class="backup-list">
        {#each backupList as b (b.path)}
          <li>
            <div class="meta">
              <div class="fname">{b.filename}</div>
              <div class="sub">
                {new Date(b.created_at).toLocaleString()}
                &middot;
                {(b.size / 1024).toFixed(1)} KiB
              </div>
            </div>
            <div class="actions">
              <button disabled={backupBusy} onclick={() => onBackupRestore(b)}>Restore</button>
              <button disabled={backupBusy} onclick={() => onBackupDelete(b)}>Delete</button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}

    <p class="hint" style="margin-top:0.8rem">
      Restore overwrites the live <code>store.db</code> and
      <code>vault.enc</code> with the chosen backup. A safety copy of
      the current files is written to
      <code>backups/pre-restore-&lt;timestamp&gt;/</code> first so you
      always have one step back. You must restart the app after a
      restore.
    </p>
  </div>

  {:else if activeSection === "sync"}
  <!-- Intro -->
  <div class="group">
    <h2>Sync</h2>
    <p class="hint">
      Keep your whole profile - connections, credentials, custom icons,
      settings - in step across machines through a server you control:
      a WebDAV host (Nextcloud, Apache, <code>rclone serve webdav</code>)
      or any SSH/SFTP server. Everything is encrypted on this machine
      before upload; the server only ever stores ciphertext.
    </p>
    <label>Transport
      <select value={syncTransport} onchange={(e) => syncSaveTransport((e.currentTarget as HTMLSelectElement).value)}>
        <option value="webdav">WebDAV</option>
        <option value="sftp">SSH / SFTP</option>
      </select>
      <span class="field-hint">
        {#if syncTransport === "sftp"}
          Stores the snapshot in a directory on an SSH server, authenticated
          with a credential from your vault. Rename is atomic on POSIX hosts.
        {:else}
          Stores the snapshot on a WebDAV server with basic auth over https.
        {/if}
      </span>
    </label>
  </div>

  {#if syncTransport === "webdav"}
  <!-- 1a. WebDAV server -->
  <div class="group">
    <h2>WebDAV server</h2>
    <label>WebDAV URL
      <input bind:value={syncUrl} placeholder="https://cloud.example.com/remote.php/dav/files/you/ssh-tool/" spellcheck="false" class="mono" />
      <span class="field-hint">Must be https (or localhost). A dedicated empty folder is best.</span>
    </label>
    <label>Username
      <input bind:value={syncUsername} spellcheck="false" />
    </label>
    <label>WebDAV password
      <PasswordInput bind:value={syncPassword} placeholder={syncCfg?.has_password ? "saved - leave blank to keep" : "app password / token"} />
    </label>
    <label>Sync passphrase
      <PasswordInput bind:value={syncPassphrase} placeholder={syncCfg?.has_passphrase ? "saved - leave blank to keep" : "seals the snapshot"} />
      <span class="field-hint">
        Encrypts the snapshot. Use the same one on every machine.
        Stored in this machine's vault, so sync needs an unlocked vault.
      </span>
    </label>
    <div class="row" style="gap:0.5rem">
      <button class="primary" onclick={syncSaveConfig}>Save</button>
    </div>
  </div>
  {:else}
  <!-- 1b. SFTP server -->
  <div class="group">
    <h2>SSH / SFTP server</h2>
    <div class="form-grid">
      <label>Host
        <input bind:value={sftpHost} placeholder="sync.example.com" spellcheck="false" class="mono" />
      </label>
      <label>Port
        <input type="number" min="1" max="65535" bind:value={sftpPort} class="mono" style="width:6rem" />
      </label>
    </div>
    <label>Username
      <input bind:value={sftpUser} placeholder="backup" spellcheck="false" class="mono" />
    </label>
    <label>Remote directory
      <input bind:value={sftpDir} placeholder="/home/backup/ssh-tool-sync" spellcheck="false" class="mono" />
      <span class="field-hint">Created on first push. Use a dedicated directory.</span>
    </label>
    <label>Authentication
      <select bind:value={sftpAuthMode}>
        <option value="credential">Use a vault credential</option>
        <option value="inline">Type it here (for a new machine)</option>
      </select>
      <span class="field-hint">
        {#if sftpAuthMode === "credential"}
          Reuses a credential from your vault tree - convenient on a machine
          that already has it.
        {:else}
          Type the auth directly. Pick this to set up sync on a fresh machine:
          a vault credential wouldn't exist there until the first pull brings
          it in.
        {/if}
      </span>
    </label>

    {#if sftpAuthMode === "credential"}
      <label>Credential
        <select bind:value={sftpCredId}>
          <option value="">- pick a credential -</option>
          {#each syncCredOptions as c (c.id)}
            <option value={c.id}>{c.name}</option>
          {/each}
        </select>
        <span class="field-hint">
          A key / password / opkssh credential from your vault. The host key
          is verified like any other host.
        </span>
      </label>
    {:else}
      <label>Password
        <PasswordInput bind:value={sftpInlinePassword} placeholder={syncCfg?.sftp_has_password ? "saved - leave blank to keep" : "leave blank if using a key"} />
      </label>
      <label>Private key
        <textarea
          bind:value={sftpInlineKeyPem}
          rows="4"
          spellcheck="false"
          class="mono"
          placeholder={syncCfg?.sftp_has_key ? "saved - leave blank to keep" : "-----BEGIN OPENSSH PRIVATE KEY-----"}
        ></textarea>
        <span class="field-hint">Paste a private key, or use the password above.</span>
      </label>
      {#if sftpInlineKeyPem}
        <label>Key passphrase
          <PasswordInput bind:value={sftpInlineKeyPassphrase} placeholder="only if the key is encrypted" />
        </label>
      {/if}
    {/if}
    <label>Sync passphrase
      <PasswordInput bind:value={syncPassphrase} placeholder={syncCfg?.has_passphrase ? "saved - leave blank to keep" : "seals the snapshot"} />
      <span class="field-hint">
        Encrypts the snapshot. Use the same one on every machine.
      </span>
    </label>
    <div class="row" style="gap:0.5rem">
      <button class="primary" onclick={syncSaveSftp}>Save</button>
    </div>
  </div>
  {/if}

  <!-- 2. Status -->
  <div class="group">
    <h2>Status</h2>
    {#if syncCfg && (syncCfg.generation > 0 || syncCfg.last_sync_at > 0)}
      <p class="hint">
        This machine{syncCfg.device ? ` (${syncCfg.device})` : ""}: version
        <code>{syncCfg.generation}</code>
        {#if syncCfg.last_sync_at > 0}
          · last synced {new Date(syncCfg.last_sync_at * 1000).toLocaleString()}
        {/if}
      </p>
    {:else}
      <p class="hint">Not synced yet from this machine.</p>
    {/if}

    {#if syncStatusRes}
      <div class="import-summary" class:ok={syncStatusRes.state === "in_sync"}>
        {#if syncStatusRes.state === "empty"}
          <strong>Server is empty.</strong> Push to seed it.
        {:else if syncStatusRes.state === "in_sync"}
          <strong>In sync.</strong> Version {syncStatusRes.remote_generation},
          last from {syncStatusRes.remote_device}.
        {:else if syncStatusRes.state === "remote_ahead"}
          <strong>Server is ahead.</strong> Version {syncStatusRes.remote_generation}
          from {syncStatusRes.remote_device} (you're on {syncStatusRes.local_generation}) - pull to catch up.
        {:else}
          <strong>This machine is ahead.</strong>
          You're on {syncStatusRes.local_generation}, server on {syncStatusRes.remote_generation} - push to update it.
        {/if}
      </div>
    {/if}
    <div class="row" style="gap:0.5rem">
      <button disabled={syncBusy} onclick={syncCheckStatus}>
        {syncBusy ? "Checking…" : "Check now"}
      </button>
    </div>
  </div>

  <!-- 3. Manual actions -->
  <div class="group">
    <h2>Manual sync</h2>
    <p class="hint">
      <strong>Push</strong> sends this machine's profile to the server.
      <strong>Pull</strong> replaces this machine's profile with the
      server's - applied live, no restart, sessions stay open.
    </p>
    <div class="row" style="gap:0.5rem">
      <button class="primary" disabled={syncBusy} onclick={() => syncDoPush(false)}>
        {syncBusy ? "Working…" : "Push"}
      </button>
      <button disabled={syncBusy} onclick={syncDoPull}>Pull</button>
      <button disabled={syncBusy} onclick={() => syncDoPush(true)}>Force push</button>
    </div>
    <span class="field-hint">
      Force push overwrites the server even if it has changes you
      haven't pulled - use only to resolve a conflict deliberately.
    </span>
    {#if syncErr}<div class="err">{syncErr}</div>{/if}
    {#if syncPulled}
      <div class="import-summary ok">
        <strong>Pulled - one restart needed for new passwords/keys.</strong>
        The connections are already live; the vault secrets apply on the
        next start.
        <div style="margin-top:0.4rem">
          {#if isMobile}
            <span class="field-hint">
              Close and reopen the app (swipe it away from Recents, then
              launch again) to apply the new passwords and keys.
            </span>
          {:else}
            <button class="primary" onclick={() => api.appRelaunch().catch((e) => toast.err(errMsg(e)))}>
              Restart now
            </button>
          {/if}
        </div>
      </div>
    {/if}
  </div>

  <!-- 4. Automatic -->
  <div class="group">
    <h2>Automatic</h2>
    <label class="row-inline">
      <input type="checkbox" bind:checked={syncAuto} onchange={syncSaveAuto} />
      <span>Auto sync</span>
    </label>
    <span class="field-hint">
      Pushes your changes ~90 s after you stop editing (and on quit),
      and checks the server for newer versions periodically.
    </span>

    {#if syncAuto}
      <label class="row-inline" style="margin-top:0.5rem">
        <span>Check the server every</span>
        <input
          type="number" min="1" max="120" step="1"
          style="width:4rem"
          bind:value={syncCheckMinutes}
          onchange={syncSaveAuto}
        />
        <span>minutes</span>
      </label>

      <label class="row-inline" style="margin-top:0.6rem">
        <input type="checkbox" bind:checked={syncAutoApply} onchange={syncSaveAutoApply} />
        <span>Apply incoming changes automatically</span>
      </label>
      <span class="field-hint">
        When another machine pushes a newer version, pull it in the
        background instead of just notifying. Only happens when this
        machine has no unsaved changes and you're not mid-edit;
        otherwise it waits or leaves a notification.
      </span>
    {/if}
  </div>

  {:else if activeSection === "import"}
  <div class="group">
    <h2>Import</h2>
    <p class="hint">
      Bring connections in from another tool. Imports are additive -
      existing rows are never modified, re-running is safe.
    </p>
    <div class="import-sources">
      {#each IMPORT_SOURCES as s (s.id)}
        <button
          class="src-card"
          class:active={importSource === s.id}
          onclick={() => (importSource = s.id)}
        >
          <span class="src-name">{s.name}</span>
          <span class="src-desc">{s.desc}</span>
        </button>
      {/each}
    </div>
  </div>

  {#if importSource === "rdm"}
  <div class="group">
    <h2>Devolutions RDM</h2>
    <p class="hint">
      Upload a Remote Desktop Manager JSON export. Folders, SSH connections,
      jump host VPNs, and icons all come along. Credentials referenced by
      RDM are <em>not</em> imported automatically - the summary will tell
      you which credential paths to re-attach manually.
    </p>
    <p class="hint">
      Import is additive: existing rows are never modified. You can re-run
      safely; duplicates will land alongside without overwriting.
    </p>
    <label class="folder-label">
      Import into folder
      <select bind:value={rdmTargetFolderID} disabled={rdmBusy} class="folder-select">
        <option value="">- Root -</option>
        {#each rdmFolderOptions as f (f.id)}
          <option value={f.id}>{folderLabel(f)}</option>
        {/each}
      </select>
    </label>
    <div class="row">
      <input
        type="file"
        accept=".json,application/json"
        bind:this={rdmInputEl}
        onchange={onRdmFile}
        disabled={rdmBusy}
      />
      {#if rdmBusy}<span class="hint">Importing…</span>{/if}
    </div>
    {#if rdmError}
      <div class="err">{rdmError}</div>
    {/if}
    {#if rdmSummary}
      <div class="summary" bind:this={rdmSummaryEl}>
        <div class="banner ok-banner">
          ✅ Import done -
          <strong>{rdmSummary.connections_created}</strong> connections,
          <strong>{rdmSummary.folders_created}</strong> folders,
          <strong>{rdmSummary.images_stored}</strong> icons.
        </div>

        {#if rdmSummary.credentials_created > 0}
          <div class="line">
            <strong>{rdmSummary.credentials_created}</strong> credentials imported
            {#if rdmSummary.credentials_need_secret > 0}
              · <span class="warn">{rdmSummary.credentials_need_secret} need secret set manually</span>
            {/if}
          </div>
        {/if}

        <div class="banner warn-banner">
          ⚠ Passwords and SSH keys are <strong>not</strong> imported from
          RDM - secrets stay in their original vault. For every connection
          listed below, attach a credential in the Credentials tab and
          assign it on the connection's editor before connecting.
        </div>

        <div class="line">
          jump hosts: <strong>{rdmSummary.jump_resolved}</strong> resolved,
          {#if rdmSummary.jump_unresolved > 0}
            <span class="warn">{rdmSummary.jump_unresolved} unresolved</span>
          {:else}
            <span class="ok">0 unresolved</span>
          {/if}
          · skipped (non-SSH): {rdmSummary.skipped_non_ssh}
        </div>

        {#if rdmSummary.needs_attention.length > 0}
          {@const byReason = attentionByReason(rdmSummary.needs_attention)}
          <h3>Connections needing credentials ({rdmSummary.needs_attention.length})</h3>
          {#each [...byReason.entries()] as [reason, count]}
            <details open>
              <summary>
                <span class="reason-count">{count}</span>
                {attentionLabel(reason)}
              </summary>
              <ul class="atn-list">
                {#each rdmSummary.needs_attention.filter((x) => x.reason === reason) as a}
                  <li>
                    <span class="atn-name">{a.name}</span>
                    {#if a.hostname}<span class="atn-host">{a.hostname}</span>{/if}
                    {#if a.detail}<span class="atn-detail">{a.detail}</span>{/if}
                  </li>
                {/each}
              </ul>
            </details>
          {/each}
        {/if}

        {#if rdmSummary.unresolved_jumps.length > 0}
          <details>
            <summary>Unresolved jump host names ({rdmSummary.unresolved_jumps.length})</summary>
            <ul>{#each rdmSummary.unresolved_jumps as j}<li>{j}</li>{/each}</ul>
          </details>
        {/if}
        {#if rdmSummary.warnings.length > 0}
          <details>
            <summary>Warnings ({rdmSummary.warnings.length})</summary>
            <ul>{#each rdmSummary.warnings as w}<li>{w}</li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}
  </div>

  {:else if importSource === "sshconfig"}
  <div class="group">
    <h2>OpenSSH config (~/.ssh/config)</h2>
    <p class="hint">
      Paste an OpenSSH client config. Each non-wildcard Host block becomes
      a connection; ProxyJump turns into a jump chain (resolved against
      other hosts in the same paste, otherwise carried as a raw hostname).
      IdentityFile paths are NOT auto-imported - for security we record
      them in the connection's Notes so you can attach the right key
      from the credentials manager.
    </p>
    <label>Target folder
      <select bind:value={sshConfigTargetFolderID}>
        <option value="">(root)</option>
        {#each tree.folders as f (f.id)}
          <option value={f.id}>{f.name}</option>
        {/each}
      </select>
    </label>
    <label>ssh_config text
      <textarea bind:value={sshConfigText} rows="10" placeholder="Host bastion\n  HostName bastion.example.com\n  User ops\n..."></textarea>
    </label>
    <div class="actions">
      <button class="primary" disabled={sshConfigBusy || !sshConfigText} onclick={importSshConfig}>
        {sshConfigBusy ? "Importing…" : "Import"}
      </button>
    </div>
    {#if sshConfigErr}<div class="err">{sshConfigErr}</div>{/if}
    {#if sshConfigSummary}
      <div class="import-summary ok">
        <strong>ssh_config import:</strong>
        <ul>
          <li>Connections created: {sshConfigSummary.connections_created}</li>
          <li>Skipped (name conflict): {sshConfigSummary.connections_skipped}</li>
          <li>Jump hops resolved: {sshConfigSummary.jump_resolved}</li>
          <li>Jump hops unresolved (kept as raw hostname): {sshConfigSummary.jump_unresolved.length}</li>
          <li>IdentityFile paths recorded in notes: {sshConfigSummary.identity_files_noted}</li>
          {#if sshConfigSummary.warnings.length}
            <li class="warn">Warnings: {sshConfigSummary.warnings.length}</li>
          {/if}
        </ul>
        {#if sshConfigSummary.jump_unresolved.length}
          <details>
            <summary>Unresolved jumps</summary>
            <ul>{#each sshConfigSummary.jump_unresolved as j}<li>{j}</li>{/each}</ul>
          </details>
        {/if}
        {#if sshConfigSummary.warnings.length}
          <details>
            <summary>Warnings</summary>
            <ul>{#each sshConfigSummary.warnings as w}<li>{w}</li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}
  </div>

  {:else if importSource === "mobaxterm"}
  <div class="group">
    <h2>MobaXterm</h2>
    <p class="hint">
      In MobaXterm: right-click <strong>User sessions</strong> &gt;
      <strong>Export</strong> to get a <code>.mxtsessions</code> file,
      then load it here. SSH sessions and their bookmark folders come
      along; RDP / telnet / other types are skipped. Passwords are not
      part of MobaXterm's export - attach credentials afterwards.
    </p>
    <label>Target folder
      <select bind:value={mobaTargetFolderID} disabled={mobaBusy}>
        <option value="">(root)</option>
        {#each rdmFolderOptions as f (f.id)}
          <option value={f.id}>{folderLabel(f)}</option>
        {/each}
      </select>
    </label>
    <div class="row">
      <input
        type="file"
        accept=".mxtsessions,.ini,text/plain"
        bind:this={mobaInputEl}
        onchange={onMobaFile}
        disabled={mobaBusy}
      />
      {#if mobaBusy}<span class="hint">Importing…</span>{/if}
    </div>
    {#if mobaError}<div class="err">{mobaError}</div>{/if}
    {#if mobaSummary}
      <div class="import-summary ok">
        <strong>MobaXterm import:</strong>
        <ul>
          <li>Connections created: {mobaSummary.connections_created}</li>
          <li>Folders created: {mobaSummary.folders_created}</li>
          <li>Skipped (name conflict): {mobaSummary.connections_skipped}</li>
          <li>Skipped (not SSH): {mobaSummary.skipped_non_ssh}</li>
          {#if mobaSummary.warnings.length}
            <li class="warn">Warnings: {mobaSummary.warnings.length}</li>
          {/if}
        </ul>
        {#if mobaSummary.warnings.length}
          <details>
            <summary>Warnings</summary>
            <ul>{#each mobaSummary.warnings as w}<li>{w}</li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}
  </div>

  {:else if importSource === "putty"}
  <div class="group">
    <h2>PuTTY / KiTTY</h2>
    <p class="hint">
      PuTTY has no export button - sessions live in the registry.
      Export them from a Command Prompt, then load the file here:
    </p>
    <pre class="cmd">reg export "HKCU\Software\SimonTatham\PuTTY\Sessions" putty-sessions.reg</pre>
    <p class="hint">
      KiTTY exports work too (<code>HKCU\Software\9bis.com\KiTTY\Sessions</code>).
      Only <code>Protocol=ssh</code> sessions are imported; PuTTY never
      stores passwords, so nothing is lost.
    </p>
    <label>Target folder
      <select bind:value={puttyTargetFolderID} disabled={puttyBusy}>
        <option value="">(root)</option>
        {#each rdmFolderOptions as f (f.id)}
          <option value={f.id}>{folderLabel(f)}</option>
        {/each}
      </select>
    </label>
    <div class="row">
      <input
        type="file"
        accept=".reg,text/plain"
        bind:this={puttyInputEl}
        onchange={onPuttyFile}
        disabled={puttyBusy}
      />
      {#if puttyBusy}<span class="hint">Importing…</span>{/if}
    </div>
    {#if puttyError}<div class="err">{puttyError}</div>{/if}
    {#if puttySummary}
      <div class="import-summary ok">
        <strong>PuTTY import:</strong>
        <ul>
          <li>Connections created: {puttySummary.connections_created}</li>
          <li>Skipped (name conflict): {puttySummary.connections_skipped}</li>
          <li>Skipped (not SSH): {puttySummary.skipped_non_ssh}</li>
          {#if puttySummary.warnings.length}
            <li class="warn">Warnings: {puttySummary.warnings.length}</li>
          {/if}
        </ul>
        {#if puttySummary.warnings.length}
          <details>
            <summary>Warnings</summary>
            <ul>{#each puttySummary.warnings as w}<li>{w}</li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}
  </div>

  {:else if importSource === "archive"}
  <div class="group">
    <h2>ssh-tool archive</h2>
    <p class="hint">
      Paste a TOML or JSON archive (format auto-detected), or pull one
      from a URL (e.g. ssh-tool-catalog `/api/bundle?ids=…`). Run a
      dry-run first to see what will land.
    </p>

    {#if !isMobile}
      <!-- The ssh-tool:// handler binds "Open in ssh-tool" links to this app
           (registry / .desktop / Launch Services). The control is desktop-only
           because there's nothing to register at runtime on Android - the
           scheme is declared in the manifest and bound at install time (the
           handler IS wired there: MainActivity -> deep_link_import). Pulling an
           archive from a URL below works regardless. -->
      <div class="scheme-row">
        <span class="lbl">ssh-tool:// handler</span>
        {#if urlSchemeStatus}
          <span class="status-ok">registered</span>
          <code class="status-detail mono">{urlSchemeStatus}</code>
        {:else}
          <span class="status-warn">not registered - "Open in ssh-tool" buttons won't launch this app</span>
        {/if}
        <button class="picker-btn" disabled={urlSchemeBusy} onclick={registerURLScheme}>
          {urlSchemeBusy ? "Working…" : urlSchemeStatus ? "Re-register" : "Register handler"}
        </button>
      </div>
      {#if urlSchemeMsg}
        <p class="hint">{urlSchemeMsg}</p>
      {/if}
    {/if}

    <div class="url-fetch">
      <input
        bind:value={importURL}
        placeholder="https://catalog.example.com/api/bundle?ids=…"
        spellcheck="false"
        class="mono"
        onkeydown={(e) => { if (e.key === "Enter") { e.preventDefault(); fetchImportFromURL(); } }}
      />
      <button class="primary" disabled={importBusy || !importURL.trim()} onclick={fetchImportFromURL}>
        {importBusy ? "Fetching…" : "Fetch"}
      </button>
      <button disabled={importBusy} onclick={loadImportFromFile}>
        Load file…
      </button>
    </div>
    <label>Archive text
      <textarea bind:value={importText} rows="8" placeholder="Paste the archive here, or use the URL fetcher above…"></textarea>
    </label>
    {#if importFilePath}
      <p class="hint">
        Loaded from <code class="mono">{importFilePath}</code>
      </p>
    {/if}
    <div class="target-folder">
      <span class="lbl">Import into</span>
      <span class="path">{importTargetLabel}</span>
      <button class="picker-btn" onclick={() => (importTargetPickerOpen = true)}>
        Pick folder…
      </button>
      {#if importTargetFolderId}
        <button class="picker-btn ghost" onclick={() => (importTargetFolderId = null)}>
          Clear
        </button>
      {/if}
    </div>
    <label>On conflict
      <select bind:value={importConflict}>
        <option value="skip">Skip - keep existing rows (default)</option>
        <option value="rename">Rename - append " (imported)" to new row</option>
        <option value="overwrite">Overwrite - replace existing row</option>
      </select>
    </label>
    <label>Passphrase (if archive includes encrypted secrets)
      <PasswordInput bind:value={importPassphrase} placeholder="leave blank if not needed" />
    </label>
    <div class="actions">
      <button disabled={importBusy || !importText} onclick={importDryRun}>
        Dry-run
      </button>
      <button class="primary" disabled={importBusy || !importText} onclick={importApply}>
        {importBusy ? "Importing…" : "Apply"}
      </button>
    </div>
    {#if importError}<div class="err">{importError}</div>{/if}
    {#if importPreview}
      {@const sum = importPreview}
      <div class="import-summary">
        <strong>Dry-run summary:</strong>
        <ul>
          <li>Folders: {sum.folders_created.length} new, {sum.folders_updated.length} updated, {sum.folders_skipped.length} skipped</li>
          <li>Connections: {sum.conns_created.length} new, {sum.conns_updated.length} updated, {sum.conns_skipped.length} skipped</li>
          <li>Credentials: {sum.creds_created.length} new, {sum.creds_skipped.length} skipped</li>
          {#if sum.warnings.length}
            <li class="warn">{sum.warnings.length} warning(s)</li>
          {/if}
        </ul>
      </div>
    {/if}
    {#if importDone}
      {@const sum = importDone}
      <div class="import-summary ok">
        <strong>Applied:</strong>
        <ul>
          <li>Folders created: {sum.folders_created.length}</li>
          <li>Connections created: {sum.conns_created.length}</li>
          <li>Credentials created: {sum.creds_created.length}</li>
          <li>Secrets restored: {sum.secrets_imported}</li>
          {#if sum.conn_passwords_imported > 0}
            <li>Connection passwords restored: {sum.conn_passwords_imported}</li>
          {/if}
          {#if sum.images_imported > 0}
            <li>Custom icons imported: {sum.images_imported}</li>
          {/if}
          {#if sum.warnings.length}
            <li class="warn">Warnings: {sum.warnings.length}</li>
          {/if}
        </ul>
        {#if sum.warnings.length}
          <details>
            <summary>Warnings</summary>
            <ul>{#each sum.warnings as w}<li>{w}</li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}
    {#if importTargetPickerOpen}
      <FolderPicker
        title="Import into folder…"
        onPick={(id) => {
          importTargetFolderId = id;
          importTargetPickerOpen = false;
        }}
        onCancel={() => (importTargetPickerOpen = false)}
      />
    {/if}
  </div>
  {/if}

  {:else if activeSection === "export"}
  <div class="group">
    <h2>Export connections</h2>
    <p class="hint">
      Serialise a subset of the tree to TOML or JSON. Credentials are
      excluded by default - when enabled, credential secrets AND
      per-connection passwords are wrapped with a passphrase using the
      same crypto as the main vault.
    </p>
    <label>Format
      <select bind:value={exportFormat}>
        <option value="toml">TOML (human-readable)</option>
        <option value="json">JSON</option>
      </select>
    </label>

    <p class="hint">
      Tick the folder subtrees you want exported. With nothing ticked,
      the whole tree is exported.
    </p>
    <div class="folder-list">
      {#each tree.folders.filter((f) => f.parent_id == null) as f (f.id)}
        <label class="folder-row">
          <input
            type="checkbox"
            checked={exportRoots.includes(f.id)}
            onchange={() => toggleExportRoot(f.id)}
          />
          <span>{f.name}</span>
        </label>
      {/each}
    </div>

    <label class="row-inline">
      <input type="checkbox" bind:checked={exportIncludeCreds} />
      <span>Include credentials (encrypted)</span>
    </label>
    {#if exportIncludeCreds}
      <label>Export passphrase
        <PasswordInput bind:value={exportPassphrase} placeholder="required to wrap secrets" />
      </label>
    {/if}

    <div class="actions">
      <button class="primary" disabled={exportBusy} onclick={runExport}>
        {exportBusy ? "Exporting…" : "Generate"}
      </button>
      {#if exportPreview}
        <button onclick={copyExport}>Copy</button>
      {/if}
    </div>
    {#if exportError}<div class="err">{exportError}</div>{/if}
    {#if exportPreview}
      <details class="preview" open>
        <summary>Preview ({exportPreview.length} chars)</summary>
        <pre>{exportPreview}</pre>
      </details>
    {/if}
  </div>

  {:else if activeSection === "llm"}
    <h2>LLM (MCP) access to sessions</h2>
    <p class="hint">
      Let an external LLM client (Claude Code, etc.) connect to ssh-tool and
      help you debug a live SSH session - read what's on screen, pull logs,
      propose and run commands. Off by default. Reads are safe; commands that
      change state always need your approval.
    </p>

    <label class="toggle">
      <input
        type="checkbox"
        checked={mcpEnabled}
        onchange={(e) => toggleMcp((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Allow LLM (MCP) access to shared sessions</strong>
        <span class="hint inline">
          - starts a local-only bridge (a unix socket on Linux/macOS, a
          loopback pipe on Windows) an MCP client can connect to. Nothing is
          exposed to the network, and no session is reachable until you
          explicitly share it with the pane's Share-with-LLM button.
        </span>
      </span>
    </label>

    <label class="toggle">
      <input
        type="checkbox"
        checked={notificationsEnabled}
        onchange={(e) => toggleNotifications((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Desktop notifications for prompts that need you</strong>
        <span class="hint inline">
          - when the app is in the background, pop an OS notification (plus a
          taskbar flash) for a blocking prompt - an LLM approval request or a
          host-key confirmation - so you don't leave it waiting unseen.
        </span>
      </span>
    </label>

    <label class="toggle">
      <input
        type="checkbox"
        checked={mcpAuditEnabled}
        onchange={(e) => toggleMcpAudit((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Keep a persistent log of LLM activity (audit)</strong>
        <span class="hint inline">
          - record every command the LLM runs, types or connects to the local
          audit log so it survives restarts. The live LLM-activity panel (robot
          icon in the status bar / pane toolbar) works either way; this only
          controls the durable copy.
        </span>
      </span>
    </label>

    {#if mcpAuditEnabled}
      <label class="toggle" style="margin-left:1.6rem">
        <input
          type="checkbox"
          checked={mcpAuditOutput}
          onchange={(e) => toggleMcpAuditOutput((e.target as HTMLInputElement).checked)}
        />
        <span>
          <strong>Also store command output in the audit log</strong>
          <span class="hint inline">
            - off by default. Command output can contain secrets the LLM read
            (a <code>.env</code> file, environment variables, kubernetes
            secrets), and the audit log is a plaintext file on disk - not
            encrypted like the vault. Enable only if you accept that.
          </span>
        </span>
      </label>
    {/if}

    {#if mcpEnabled}
      <h3 style="margin-top:1.2rem">Register with your LLM client</h3>
      <p class="hint">
        Add ssh-tool as an MCP server once. It runs as a small bridge process
        the client launches (<code>{mcpExePath || "ssh-tool"} --mcp-bridge</code>).
      </p>

      <p class="hint"><strong>Claude Code:</strong></p>
      <pre class="cmd-block">claude mcp add ssh-tool -- {mcpExePath || "ssh-tool"} --mcp-bridge</pre>

      <p class="hint"><strong>LM Studio</strong> (Program -> Edit mcp.json):</p>
      <pre class="cmd-block">{`{
  "mcpServers": {
    "ssh-tool": {
      "command": ${mcpExeJson},
      "args": ["--mcp-bridge"]
    }
  }
}`}</pre>
      <p class="hint">
        Any MCP client works the same way - point its "command" at the path
        above with the <code>--mcp-bridge</code> argument.
      </p>

      <p class="hint">
        Then share a session: click the <strong>Share with LLM</strong> button
        in the pane toolbar (robot icon, next to tunnels). The LLM sees only
        shared sessions, and can also search and open your saved connections
        (with your approval).
      </p>
      <p class="hint">
        Paste the ssh-tool system prompt into your LLM client so it uses these
        tools well and treats terminal output as untrusted.
        <button class="link-btn" onclick={copyMcpSystemPrompt}>Copy system prompt</button>
      </p>

      <label class="toggle">
        <input
          type="checkbox"
          checked={mcpTcp}
          onchange={(e) => toggleMcpTcp((e.target as HTMLInputElement).checked)}
        />
        <span>
          <strong>Also listen on loopback TCP (for WSL / cross-boundary clients)</strong>
          <span class="hint inline">
            - needed when the LLM client runs in WSL but ssh-tool runs on
            Windows (WSL forwards localhost to the host but can't see the
            Windows pipe). Binds 127.0.0.1 only, guarded by a token the bridge
            reads from a private file. Leave off if the client runs on the same
            OS as ssh-tool.
          </span>
        </span>
      </label>

      {#if mcpWslExePath}
        <p class="hint" style="margin-top:0.8rem">
          <strong>WSL client:</strong> turn on the toggle above, then run this
          inside your WSL Claude Code (it points at the Windows binary):
        </p>
        <pre class="cmd-block">claude mcp add ssh-tool -- {mcpWslExePath} --mcp-bridge</pre>
      {/if}

      <h3 style="margin-top:1.2rem">Auto-run allowlist</h3>
      <p class="hint">
        Read-only commands (ls, cat, journalctl, systemctl status, ...) run
        without a prompt. Anything else asks you first. Add extra command names
        to auto-run here, one per line or space-separated. Mutating commands
        (sudo, rm, ...) always prompt regardless.
      </p>
      <textarea
        class="allowlist"
        rows="3"
        placeholder="e.g. mytool anothertool"
        bind:value={mcpReadonlyExtra}
        onblur={saveMcpReadonlyExtra}
      ></textarea>

      <div class="mcp-grants">
        <h3 style="margin-top:1.2rem">Currently shared sessions</h3>
        <McpGrantsList />
      </div>
    {/if}

  {:else if activeSection === "sharing"}
    <h2>Share a session to a browser</h2>
    <p class="hint">
      Let a colleague watch - or, with your explicit approval, type into - a
      live session from a plain web browser, no ssh-tool needed on their side.
      Off by default. When on, right-click a tab and choose "Share to browser".
      The connection is encrypted; you confirm a short word-code with your guest
      to be sure no one is intercepting it, and every guest waits for you to
      allow them in.
    </p>

    <label class="toggle">
      <input
        type="checkbox"
        checked={shareEnabled}
        onchange={(e) => toggleShare((e.target as HTMLInputElement).checked)}
      />
      <span>
        <strong>Enable session sharing</strong>
        <span class="hint inline">
          - makes the "Share to browser" action available. Each share picks its
          own network interface and is reachable only while it's running; a
          guest sees nothing until you approve them.
        </span>
      </span>
    </label>

    {#if shareEnabled}
      <div class="group" style="margin-top:1rem">
        <h3>Certificate fingerprint</h3>
        <p class="hint">
          Read these words to your guest (by phone or chat) so they can confirm
          they're really connected to you. They stay the same across shares and
          restarts - a change means either you regenerated the certificate or
          something is wrong.
        </p>
        {#if shareFingerprint}
          <div class="fp-readout">{shareFingerprint}</div>
          <div class="hint" style="font-family:monospace">{shareFingerprintShort}</div>
        {:else}
          <button onclick={loadShareFingerprint}>Show fingerprint</button>
        {/if}
        <div style="margin-top:0.6rem">
          <button onclick={regenerateShareCert}>Regenerate certificate…</button>
        </div>
      </div>

      <label class="toggle" style="margin-top:1rem">
        <input
          type="checkbox"
          checked={shareAuditOutput}
          onchange={toggleShareAuditOutput}
        />
        <span>
          <strong>Record guest keystrokes in the audit log</strong>
          <span class="hint inline">
            - off by default. Who joined, when, and from where is always logged;
            this also stores what a controlling guest TYPES. The audit log is a
            plaintext file on disk, and guest keystrokes can include passwords -
            enable only if you accept that.
          </span>
        </span>
      </label>
    {/if}

  {:else if activeSection === "logs"}
  <div class="group">
    <h2>Logs</h2>
    <p class="hint">
      Live tail of backend log lines. Useful when an SSH connect, RDM
      import, or other operation fails silently. Buffer keeps the last
      2000 lines.
    </p>
    {#if logDirPath}
      <p class="hint">
        Persistent log file:
        <code>{logDirPath}\app.log</code>
        - rotated at 5 MiB, last 3 retained.
      </p>
    {/if}
    <LogViewer />
  </div>

  {:else if activeSection === "about"}
  <div class="group about">
    <h2>About</h2>
    {#if versionInfo}
      <dl class="about-list">
        <dt>Application</dt>
        <dd>{versionInfo.name}</dd>
        <dt>Version</dt>
        <dd>
          <code>{versionInfo.version}</code>
          {#if versionInfo.version === "dev"}
            <span class="dev-pill">dev build</span>
          {/if}
        </dd>
        <dt>Commit</dt>
        <dd><code>{versionInfo.commit}</code></dd>
        <dt>Schema</dt>
        <dd><code>v{versionInfo.schema_version}</code></dd>
      </dl>
    {:else}
      <p class="hint">Loading…</p>
    {/if}
    <h3 style="margin-top:1.2rem">Profile statistics</h3>
    {#if profileStats}
      {@const ps = profileStats}
      <dl class="about-list">
        <dt>Connections</dt>
        <dd>
          {ps.connections}
          {#if ps.vnc_enabled > 0}
            <span class="hint inline">({ps.vnc_enabled} with VNC)</span>
          {/if}
        </dd>
        <dt>Folders</dt>
        <dd>
          {ps.folders}
          {#if ps.dynamic_folders > 0}
            <span class="hint inline">({ps.dynamic_folders} dynamic)</span>
          {/if}
        </dd>
        {#if ps.dynamic_hosts + ps.dynamic_vms + ps.dynamic_lxc + ps.dynamic_servers > 0}
          <dt>Dynamic inventory</dt>
          <dd>
            {[
              ps.dynamic_hosts > 0 ? `${ps.dynamic_hosts} host${ps.dynamic_hosts === 1 ? "" : "s"}` : "",
              ps.dynamic_vms > 0 ? `${ps.dynamic_vms} VM${ps.dynamic_vms === 1 ? "" : "s"}` : "",
              ps.dynamic_lxc > 0 ? `${ps.dynamic_lxc} LXC` : "",
              ps.dynamic_servers > 0 ? `${ps.dynamic_servers} server${ps.dynamic_servers === 1 ? "" : "s"}` : "",
            ].filter(Boolean).join(", ")}
          </dd>
        {/if}
        <dt>Tunnels</dt>
        <dd>
          {ps.forwards} configured
          {#if ps.bookmarks > 0}
            <span class="hint inline">({ps.bookmarks} bookmark{ps.bookmarks === 1 ? "" : "s"})</span>
          {/if}
        </dd>
        <dt>Credentials</dt>
        <dd>
          {ps.credentials}
          {#if credentials.folders.length > 0}
            <span class="hint inline">(in {credentials.folders.length} folder{credentials.folders.length === 1 ? "" : "s"})</span>
          {/if}
        </dd>
        <dt>Open sessions</dt>
        <dd>
          {sessions.tabs.length}
          {#if sessions.tabs.length > 0}
            <span class="hint inline">({connectedSessions} connected)</span>
          {/if}
        </dd>
        <dt>Active tunnels</dt>
        <dd>{activeForwards ?? "-"}</dd>
      </dl>
    {:else}
      <p class="hint">Loading…</p>
    {/if}
    <p class="hint">
      Version and commit are injected at build time via ldflags. A
      <code>dev</code> tag means the binary was built without those
      flags (typically <code>go run .</code> during development).
    </p>
    <p class="hint">
      Source + issues: see project docs.
    </p>
  </div>
  {/if}

  </div>
</section>

<style>
  .np-card {
    border: 1px solid var(--surface0);
    border-radius: 6px;
    padding: 0.6rem 0.7rem;
    margin: 0.4rem 0;
    display: flex;
    flex-direction: column;
    gap: 0.45rem;
  }
  .np-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .np-meta {
    color: var(--subtext0);
    font-size: 0.78rem;
    font-family: ui-monospace, monospace;
  }
  .np-pill {
    font-size: 0.65rem;
    font-weight: 600;
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    background: var(--surface0);
    color: var(--subtext0);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .np-pill.running { background: var(--green); color: var(--on-accent); }
  .np-pill.paused  { background: var(--yellow); color: var(--on-accent); }
  .np-pill.remote  { background: var(--mauve); color: var(--on-accent); text-transform: none; }
  .np-actions {
    display: flex;
    align-items: end;
    gap: 0.45rem;
    flex-wrap: wrap;
  }
  .np-mode {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.78rem;
    color: var(--subtext0);
  }
  .np-actions button.danger { color: var(--red); }
  .np-kind {
    font-size: 0.68rem;
    font-weight: 600;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    background: var(--surface1);
    color: var(--subtext0);
  }
  .np-kindpick {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .token-add {
    font-size: 0.75rem;
    padding: 0.1rem 0.5rem;
  }

  .settings {
    display: grid;
    grid-template-columns: 220px 1fr;
    height: 100%;
    color: var(--text);
    min-height: 0;
  }

  /* Narrow / phone: the 220px side-nav + content doesn't fit. Stack them -
     the nav becomes a horizontal scrolling strip of chips on top, content
     fills below with tighter padding. */
  /* NOTE: selectors here are prefixed with .settings to outrank the base
     .nav / .nav-item rules that are declared LATER in this file (equal
     specificity would otherwise let the later source win, which is exactly
     why the nav stayed 220px-wide and scrolled forever on mobile). */
  @media (max-width: 640px) {
    .settings {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr;
    }
    .settings .nav {
      border-right: 0;
      border-bottom: 1px solid var(--surface0);
      padding: 0.4rem 0.5rem;
      display: flex;
      flex-wrap: nowrap;
      align-items: center;
      gap: 0.3rem;
      overflow-x: auto;
      overflow-y: hidden;
      scrollbar-width: none;
      flex: 0 0 auto;
      min-height: 0;
    }
    .settings .nav::-webkit-scrollbar { display: none; }
    .settings .nav :global(h1) { display: none; }
    .settings .nav-group {
      margin-bottom: 0;
      display: flex;
      flex-wrap: nowrap;
      gap: 0.3rem;
      align-items: center;
      flex: 0 0 auto;
    }
    .settings .nav-group-label { display: none; }
    .settings .nav-item {
      display: inline-block;
      width: auto;
      flex: 0 0 auto;
      white-space: nowrap;
      padding: 0.35rem 0.7rem;
      border: 1px solid var(--surface0);
    }
    .settings .content {
      padding: 0.9rem 0.7rem 3rem;
    }
    /* Keep every section within the viewport: groups/rows shrink and wrap,
       and form controls fill the column instead of holding a fixed width
       that would push a horizontal scroll. */
    .settings .content :global(.group),
    .settings .content :global(.row) {
      min-width: 0;
      max-width: 100%;
    }
    .settings .content :global(.row) { flex-wrap: wrap; }
    .settings .content :global(input),
    .settings .content :global(select),
    .settings .content :global(textarea) {
      max-width: 100%;
    }
    .settings .content :global(input[style*="width"]) {
      width: 100% !important;
      max-width: 100% !important;
    }
    /* Multi-column grids stack to one column on a phone: the Snippets
       library|editor split (220px-min + 2fr) and the Vault 3-column
       form-grid would both overflow. */
    .settings .content :global(.snip-grid),
    .settings .content :global(.form-grid) {
      grid-template-columns: 1fr;
    }
  }
  /* Side nav: persistent on the left, scrolls independently of the
     content pane. Mirrors the rest of the app's layout convention
     (sidebar + main). */
  .nav {
    background: var(--crust);
    border-right: 1px solid var(--surface0);
    padding: 1rem 0.5rem;
    overflow-y: auto;
    min-height: 0;
  }
  .nav h1 {
    margin: 0 0.5rem 1rem;
    font-size: 1rem;
    font-weight: 600;
    color: var(--text);
  }
  .nav-group { margin-bottom: 0.6rem; }
  .nav-group-label {
    padding: 0.25rem 0.7rem;
    font-size: 0.68rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--overlay1);
  }
  .nav-item {
    display: block;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    color: var(--subtext1);
    padding: 0.4rem 0.7rem;
    border-radius: 4px;
    font: inherit;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .nav-item:hover { background: var(--surface0); color: var(--text); }
  .nav-item.active {
    background: var(--surface0);
    color: var(--text);
    box-shadow: inset 3px 0 0 var(--blue);
  }
  /* Content pane is a centred column: section cards stack in the
     middle of the pane (a readable max width) rather than hugging the
     left edge with dead space on a wide / maximised window. The base
     font is nudged from the 13px UI default so Settings reads
     comfortably; every rem-based size inside scales with it. */
  .content {
    display: block;
    padding: 1.5rem 1.5rem 3rem;
    overflow-y: auto;
    min-height: 0;
  }
  /* Centre each section card in the pane with a readable max width.
     margin:auto on a block child is the reliable centring here -
     flex/align-items proved fragile across the grid column. */
  .content > .group {
    margin: 0 auto 0.9rem;
  }
  h2 {
    margin: 0 0 0.6rem;
    font-size: 1.05rem;
    font-weight: 600;
    color: var(--text);
    letter-spacing: normal;
    text-transform: none;
  }
  .group {
    width: 100%;
    max-width: 880px;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    padding: 1.1rem 1.4rem;
  }
  .num {
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
    margin-top: 0.4rem;
  }
  .num input[type="number"] {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.25rem 0.45rem;
    font: inherit;
    font-size: 0.85rem;
    width: 6rem;
  }
  .saved-mark {
    color: var(--green);
    font-size: 0.75rem;
  }
  .about-list {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.4rem 1rem;
    margin: 0.5rem 0 0.8rem;
    font-size: 0.85rem;
  }
  .about-list dt {
    color: var(--subtext0);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-size: 0.72rem;
    align-self: baseline;
  }
  .about-list dd {
    margin: 0;
    color: var(--text);
  }
  .about-list dd code {
    background: var(--surface0);
    padding: 0.05rem 0.4rem;
    border-radius: 3px;
    font-family: ui-monospace, monospace;
    font-size: 0.82rem;
  }
  .dev-pill {
    background: var(--yellow);
    color: var(--on-accent);
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    font-size: 0.65rem;
    font-weight: 600;
    margin-left: 0.4rem;
  }
  .snip-grid {
    display: grid;
    grid-template-columns: minmax(220px, 1fr) 2fr;
    gap: 1rem;
    margin-top: 0.6rem;
  }
  .snip-list {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    padding: 0.5rem;
    min-height: 240px;
  }
  .snip-list-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid var(--surface0);
    margin-bottom: 0.4rem;
  }
  .snip-rows { display: flex; flex-direction: column; gap: 0.1rem; }
  .snip-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.4rem 0.5rem;
    border-radius: 3px;
    cursor: pointer;
    border-left: 3px solid transparent;
  }
  .snip-row:hover { background: var(--crust); }
  .snip-row.active { background: var(--surface0); border-left-color: var(--blue); }
  .snip-row-main { flex: 1; min-width: 0; }
  .snip-name { color: var(--text); font-size: 0.88rem; }
  .snip-preview {
    color: var(--overlay0);
    font-size: 0.72rem;
    font-family: ui-monospace, monospace;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .del {
    background: transparent;
    color: var(--overlay0);
    border: 0;
    padding: 0.2rem 0.4rem;
    cursor: pointer;
    border-radius: 3px;
    font: inherit;
  }
  .del:hover { background: var(--surface1); color: var(--red); }
  .snip-editor {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    padding: 0.6rem 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .snip-editor label {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.78rem;
    color: var(--subtext0);
  }
  .snip-editor input,
  .snip-editor textarea {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.35rem 0.5rem;
    font: inherit;
    font-size: 0.85rem;
  }
  .snip-editor textarea {
    font-family: ui-monospace, "JetBrains Mono", monospace;
    resize: vertical;
  }
  .snip-actions {
    display: flex;
    gap: 0.4rem;
    margin-top: 0.2rem;
  }
  .empty {
    color: var(--overlay0);
    font-size: 0.78rem;
    padding: 0.4rem;
  }
  .hint {
    color: var(--subtext1);
    font-size: 0.88rem;
    margin: 0.4rem 0;
    line-height: 1.55;
  }
  .warn-note {
    border-left: 3px solid var(--yellow);
    padding: 0.4rem 0.7rem;
    background: color-mix(in srgb, var(--yellow) 10%, transparent);
    border-radius: 0 4px 4px 0;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    font-size: 0.85rem;
    color: var(--subtext1);
    margin-top: 0.8rem;
  }
  input {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.45rem 0.55rem;
    font: inherit;
    /* Cap stretch on a wide section so a password field isn't a
       half-metre line; the mono URL field opts out to stay full width. */
    max-width: 34rem;
  }
  input.mono {
    max-width: none;
  }
  input[type="checkbox"], input[type="radio"] {
    max-width: none;
  }
  input:focus {
    outline: 1px solid var(--blue);
    border-color: var(--blue);
  }
  .field-hint {
    font-size: 0.8rem;
    line-height: 1.5;
    color: var(--subtext0);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin-top: 0.7rem;
  }
  button {
    background: var(--surface0);
    color: var(--text);
    border: 0;
    padding: 0.4rem 0.9rem;
    border-radius: 3px;
    cursor: pointer;
    font: inherit;
  }
  button:hover { background: var(--surface1); }
  button.primary {
    background: var(--blue);
    color: var(--on-accent);
    font-weight: 600;
  }
  button.primary:hover { background: var(--lavender); }
  .ok { color: var(--green); font-size: 0.78rem; }
  code {
    background: var(--mantle);
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    font-size: 0.78rem;
  }
  .err {
    color: var(--red);
    background: var(--mantle);
    padding: 0.5rem 0.7rem;
    border-left: 3px solid var(--red);
    border-radius: 4px;
    margin-top: 0.5rem;
    font-size: 0.85rem;
  }
  .summary {
    margin-top: 0.7rem;
    padding: 0.6rem 0.8rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    font-size: 0.85rem;
  }
  .summary .line { margin: 0.2rem 0; }
  .banner {
    padding: 0.6rem 0.8rem;
    border-radius: 4px;
    margin: 0.4rem 0;
    font-size: 0.85rem;
    line-height: 1.5;
  }
  .ok-banner {
    background: color-mix(in oklab, var(--green) 12%, var(--bg-panel));
    border-left: 3px solid var(--green);
    color: var(--green);
  }
  .warn-banner {
    background: color-mix(in oklab, var(--yellow) 16%, var(--bg-panel));
    border-left: 3px solid var(--yellow);
    color: var(--yellow);
  }
  .summary h3 {
    margin: 0.8rem 0 0.3rem;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text);
  }
  .reason-count {
    background: var(--yellow);
    color: var(--on-accent);
    border-radius: 999px;
    padding: 0 0.45rem;
    font-size: 0.7rem;
    font-weight: 600;
    margin-right: 0.4rem;
  }
  .atn-list {
    list-style: none;
    margin: 0.3rem 0;
    padding: 0;
  }
  .atn-list li {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    padding: 0.1rem 0;
    font-size: 0.78rem;
  }
  .atn-name { color: var(--text); font-weight: 500; }
  .atn-host { color: var(--overlay0); }
  .atn-detail { color: var(--surface2); font-family: ui-monospace, monospace; font-size: 0.72rem; }
  .summary .warn { color: var(--yellow); }
  .summary .ok   { color: var(--green); }
  .summary details { margin-top: 0.5rem; }
  .summary summary { cursor: pointer; color: var(--blue); font-size: 0.8rem; }
  .summary ul { margin: 0.3rem 0 0.3rem 1.2rem; font-size: 0.78rem; }
  input[type="file"] {
    padding: 0;
    color: var(--text);
  }
  .folder-label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.78rem;
    color: var(--subtext0);
    margin-top: 0.6rem;
    max-width: 400px;
  }
  .folder-select {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.35rem 0.5rem;
    font: inherit;
    font-size: 0.82rem;
    cursor: pointer;
  }
  .folder-select:focus { outline: 1px solid var(--blue); border-color: var(--blue); }
  .folder-select:disabled { opacity: 0.5; cursor: not-allowed; }
  fieldset.modes {
    border: 0;
    padding: 0;
    margin: 0.5rem 0 0.7rem;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  fieldset.modes label {
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
    padding: 0.5rem 0.7rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    cursor: pointer;
    margin: 0;
  }
  fieldset.modes label:hover { border-color: var(--surface1); }
  /* Match the .toggle (checkbox) "selected" visual: accent border
     + subtle accent-tinted fill. Without the tint the active radio
     card was just a 1px blue outline, easy to miss next to the
     bolder checkbox panels on the same page. */
  fieldset.modes label.active {
    border-color: var(--accent);
    background: color-mix(in oklab, var(--accent) 6%, transparent);
  }
  fieldset.modes input[type="radio"] {
    margin-top: 0.2rem;
    flex-shrink: 0;
    accent-color: var(--accent);
  }

  fieldset.check-cards {
    border: 0;
    padding: 0;
    margin: 0.5rem 0 0.7rem;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  fieldset.check-cards label {
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
    padding: 0.5rem 0.7rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    cursor: pointer;
    margin: 0;
  }
  fieldset.check-cards label:hover { border-color: var(--surface1); }
  fieldset.check-cards label.active { border-color: var(--blue); background: var(--mantle); }
  fieldset.check-cards input[type="checkbox"] {
    margin-top: 0.2rem;
    flex-shrink: 0;
    width: 14px;
    height: 14px;
    accent-color: var(--blue);
    cursor: pointer;
  }
  .mode-name { font-weight: 600; font-size: 0.85rem; color: var(--text); }
  .mode-desc { font-size: 0.78rem; color: var(--subtext0); line-height: 1.5; margin-top: 0.15rem; }
  kbd {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0 0.3rem;
    font-size: 0.75rem;
    font-family: ui-monospace, monospace;
  }
  button.link {
    background: transparent;
    color: var(--blue);
    border: 0;
    padding: 0;
    text-decoration: underline;
    cursor: pointer;
    font: inherit;
  }
  .fp-readout {
    font-family: monospace;
    font-size: 1.25rem;
    font-weight: 700;
    color: var(--blue);
    margin: 0.3rem 0;
  }
  .toggle {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    padding: 0.5rem 0.7rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    margin: 0.5rem 0 0.7rem;
    cursor: pointer;
    font-size: 0.85rem;
  }
  .toggle input {
    margin-top: 0.2rem;
    flex-shrink: 0;
    /* Use the app's accent for the checked fill; without this the
       default WebKit/WebView2 rendering on Windows draws a tiny
       grey check on a near-black box which is effectively
       invisible against our dark settings panel. accent-color is
       the modern one-liner that styles native checkbox + radio. */
    accent-color: var(--blue);
  }
  .toggle strong { color: var(--text); display: block; margin-bottom: 0.1rem; }
  .toggle .hint.inline { display: inline; padding: 0; margin: 0; font-size: 0.78rem; }
  .cmd-block {
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    padding: 0.5rem 0.6rem;
    margin: 0.4rem 0;
    font-family: ui-monospace, monospace;
    font-size: 0.78rem;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
    user-select: all;
  }
  .allowlist {
    width: 100%;
    box-sizing: border-box;
    background: var(--mantle);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    color: var(--text);
    padding: 0.4rem 0.5rem;
    font: inherit;
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    resize: vertical;
  }
  .themes {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 0.5rem;
    margin: 0.4rem 0 0.6rem;
  }
  .theme-card {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    padding: 0.4rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    font: inherit;
    color: inherit;
  }
  .theme-card:hover { border-color: var(--surface1); }
  .theme-card.active { border-color: var(--blue); outline: 1px solid var(--blue); }
  .theme-preview {
    padding: 0.5rem 0.6rem;
    border-radius: 3px;
    font-family: ui-monospace, monospace;
    font-size: 0.75rem;
    line-height: 1.3;
    letter-spacing: 0.04em;
  }
  .theme-preview .prompt { margin-top: 0.3rem; }
  .theme-label {
    font-size: 0.78rem;
    color: var(--text);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.3rem;
  }
  .light-tag {
    background: var(--yellow);
    color: var(--on-accent);
    font-size: 0.65rem;
    padding: 0 0.3rem;
    border-radius: 2px;
  }

  /* Export / Import section */
  .actions {
    display: flex;
    gap: 0.4rem;
    margin-top: 0.7rem;
  }
  .row-inline {
    display: flex;
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
    margin-top: 0.6rem;
  }
  .folder-list {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.4rem 0.6rem;
    max-height: 180px;
    overflow-y: auto;
    margin-top: 0.4rem;
  }
  .folder-row {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
    margin: 0;
    font-size: 0.8rem;
  }
  .preview pre {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.5rem 0.7rem;
    font-size: 0.7rem;
    max-height: 320px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
  }
  textarea {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.4rem 0.6rem;
    font: inherit;
    font-family: ui-monospace, monospace;
    font-size: 0.78rem;
    resize: vertical;
  }
  .import-sources {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 0.5rem;
  }
  .src-card {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.2rem;
    text-align: left;
    padding: 0.55rem 0.7rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    cursor: pointer;
  }
  .src-card:hover { border-color: var(--surface2); }
  .src-card.active {
    border-color: var(--blue);
    background: color-mix(in oklab, var(--blue) 10%, var(--mantle));
  }
  .src-name { font-weight: 600; font-size: 0.85rem; color: var(--text); }
  .src-desc { font-size: 0.72rem; color: var(--subtext0); }
  .cmd {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.45rem 0.6rem;
    font-family: ui-monospace, monospace;
    font-size: 0.75rem;
    overflow-x: auto;
    user-select: all;
  }
  .import-summary {
    margin-top: 0.7rem;
    padding: 0.6rem 0.8rem;
    background: var(--mantle);
    border-left: 3px solid var(--blue);
    border-radius: 0 3px 3px 0;
    font-size: 0.82rem;
  }
  .import-summary.ok {
    border-left-color: var(--green);
  }
  .import-summary ul { margin: 0.3rem 0 0; padding-left: 1.2rem; }
  .import-summary .warn { color: var(--yellow); }
  .url-fetch { display: flex; gap: 0.4rem; margin-bottom: 0.5rem; }
  .url-fetch input { flex: 1; }
  .scheme-row {
    display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap;
    margin: 0.5rem 0 0.4rem;
    padding: 0.5rem 0.7rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
  }
  .scheme-row .lbl { color: var(--overlay0); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .scheme-row .status-ok { color: var(--green); font-size: 0.85rem; }
  .scheme-row .status-warn { color: var(--yellow); font-size: 0.82rem; flex: 1; }
  .scheme-row .status-detail { color: var(--overlay0); font-size: 0.72rem; }
  .target-folder {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.6rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    margin: 0.4rem 0;
    font-size: 0.8rem;
  }
  .target-folder .lbl { color: var(--overlay0); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .target-folder .path { color: var(--text); font-family: ui-monospace, monospace; flex: 1; }
  .picker-btn {
    background: var(--surface0); color: var(--text);
    border: 0; border-radius: 3px;
    padding: 0.25rem 0.55rem;
    font: inherit; font-size: 0.78rem;
    cursor: pointer;
  }
  .picker-btn:hover { background: var(--surface1); }
  .picker-btn.ghost { background: transparent; color: var(--overlay0); }
  .picker-btn.ghost:hover { background: var(--surface0); color: var(--text); }

  .font-size-row { align-items: center; }
  .font-controls {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .font-step {
    background: var(--surface0);
    color: var(--text);
    border: 0;
    border-radius: 3px;
    padding: 0.25rem 0.55rem;
    font: inherit;
    font-size: 0.85rem;
    cursor: pointer;
    min-width: 32px;
  }
  .font-step:hover:not(:disabled) { background: var(--surface1); }
  .font-step:disabled { opacity: 0.4; cursor: not-allowed; }
  .font-step.ghost { background: transparent; color: var(--overlay0); min-width: auto; }
  .font-step.ghost:hover:not(:disabled) { background: var(--surface0); color: var(--text); }
  .font-value {
    min-width: 3.2em;
    text-align: center;
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 0.85rem;
  }

  .backup-list {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  .backup-list li {
    display: flex;
    align-items: center;
    gap: 0.8rem;
    padding: 0.5rem 0.7rem;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 4px;
  }
  .backup-list .meta {
    flex: 1;
    min-width: 0;
  }
  .backup-list .fname {
    font-family: ui-monospace, monospace;
    font-size: 0.82rem;
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .backup-list .sub {
    font-size: 0.72rem;
    color: var(--overlay1);
    margin-top: 0.1rem;
  }
  .backup-list .actions {
    display: flex;
    gap: 0.35rem;
  }
  .form-grid {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 0.5rem;
    margin: 0.6rem 0;
  }
  .form-grid label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.78rem;
  }
  /* The form-grid (vault rotate) fields are PasswordInput components now,
     which style themselves; the bare-input rules that lived here are gone. */
  /* Audit log section uses a wider container than the rest of
     Settings because the row carries 6 columns. group-wide drops
     the usual max-width so the table fills whatever the user has. */
  .group.group-wide {
    max-width: 1200px;
    width: 100%;
  }
  .audit-tbl-wrap {
    width: 100%;
    margin-top: 0.6rem;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
    background: var(--bg-panel);
  }
  .audit-tbl {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.78rem;
    table-layout: fixed;
  }
  .audit-tbl col.col-time   { width: 11rem; }
  .audit-tbl col.col-action { width: 11rem; }
  .audit-tbl col.col-host   { width: 12rem; }
  .audit-tbl col.col-user   { width: 7rem; }
  .audit-tbl col.col-target { width: 12rem; }
  .audit-tbl col.col-extra  { width: auto; }
  .audit-tbl th,
  .audit-tbl td {
    border-bottom: 1px solid var(--border);
    padding: 0.4rem 0.55rem;
    text-align: left;
    vertical-align: top;
    word-break: break-word;
    overflow-wrap: anywhere;
  }
  .audit-tbl tbody tr:last-child td { border-bottom: 0; }
  .audit-tbl tbody tr:hover { background: color-mix(in oklab, var(--accent) 5%, var(--bg-panel)); }
  .audit-tbl th {
    color: var(--text-muted);
    font-weight: 600;
    background: var(--bg-panel-2, var(--bg-panel));
    position: sticky;
    top: 0;
    user-select: none;
  }
  .audit-tbl th.sortable {
    cursor: pointer;
  }
  .audit-tbl th.sortable:hover {
    color: var(--text);
  }
  .audit-tbl th .sort-ind {
    color: var(--accent);
    font-size: 0.7rem;
    margin-left: 0.25rem;
  }
  .audit-tbl td.ts {
    color: var(--text);
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .audit-tbl td.host,
  .audit-tbl td.user,
  .audit-tbl td.target {
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 0.74rem;
  }
  .audit-tbl td.target {
    color: var(--text-muted);
  }
  .audit-tbl code {
    font-size: 0.72rem;
    color: var(--accent);
  }
  .audit-tbl td.extra {
    color: var(--text-muted);
    font-size: 0.72rem;
  }
  .audit-tbl .meta-summary {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  .audit-tbl .kv {
    display: inline-flex;
    align-items: baseline;
    gap: 0.15rem;
    padding: 0.05rem 0.35rem;
    background: var(--mantle);
    border: 1px solid var(--border);
    border-radius: 3px;
  }
  .audit-tbl .kv b {
    color: var(--text);
    font-weight: 500;
  }
  .audit-tbl .kv.more {
    color: var(--text-muted);
    font-style: italic;
  }
  .audit-tbl .meta-full {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.1rem 0.6rem;
    margin-bottom: 0.3rem;
  }
  .audit-tbl .kv-row b {
    color: var(--text-muted);
    font-weight: 500;
  }
  .audit-tbl .kv-row span {
    color: var(--text);
    font-family: ui-monospace, monospace;
    word-break: break-all;
  }
  .audit-tbl .kv-row {
    display: contents;
  }
  .audit-tbl .link-btn {
    background: transparent;
    border: 0;
    color: var(--accent);
    cursor: pointer;
    padding: 0;
    font: inherit;
    font-size: 0.7rem;
    text-decoration: underline;
  }
</style>
