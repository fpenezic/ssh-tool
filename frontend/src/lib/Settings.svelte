<script lang="ts">
  import { ocFlavorTitle, pluginTitle, profileRunsHere, NOT_ON_MOBILE_TITLE } from "./networkKind";
  import { onMount, tick, untrack } from "svelte";
  import { EventsOn } from "./wailsRuntime";
  import { errMsg } from "./connectErrors";
  import { desktopAlerts } from "./desktopAlerts.svelte";
  import { isMobile } from "./platform";
  import PasswordInput from "./PasswordInput.svelte";
  import McpGrantsList from "./McpGrantsList.svelte";
  import KeepassSettings from "./KeepassSettings.svelte";
  import BitwardenSettings from "./BitwardenSettings.svelte";
  import InfisicalSettings from "./InfisicalSettings.svelte";
  import { api, type AboutInfo, type RdmImportSummary, type ImportSummary as ArcImportSummary, type SshConfigImportSummary, type MobaXtermImportSummary, type PuttyImportSummary, type SuperPuttyImportSummary, type KafkaToolImportSummary, type Snippet, type SnippetInput, type BackupInfo, type AutoBackupPrefs, type SyncConfig, type SyncStatusResult, type NetworkProfileInfo, type PluginDownloadProgress } from "./api";
  import { vaultState } from "./vaultState.svelte";
  import { networkProfiles } from "./networkProfiles.svelte";
  import { tree, credentials, paneTabs, view, sessions } from "./stores.svelte";
  import FolderPicker from "./FolderPicker.svelte";
  import type { Folder, ClaudeDesktopInfo, CIRunnerConfig, CIRunnerStatus } from "./api";
  import { copyPastePrefs, type CopyPasteMode } from "./copyPastePrefs.svelte";
  import { SETTINGS_SECTIONS, EXTERNAL_TABS, isExternalTab, type SectionId, type SectionDef, type ExternalTabId } from "./settingsSections";
  import { terminalPrefs, DEFAULT_FONT_FAMILY, DEFAULT_SCROLLBACK, DEFAULT_BG_SCROLLBACK_DELAY } from "./terminalPrefs.svelte";
  import { appPrefs } from "./appPrefs.svelte";
  import { connectPrefs, type IntPref, RECONNECT_ATTEMPTS, RECONNECT_MAX_DELAY, BULK_CONCURRENT, BULK_STAGGER_MS, BULK_CONFIRM_ABOVE, BASTION_LINGER } from "./connectPrefs.svelte";
  import { vaultPrefs } from "./vaultPrefs.svelte";
  import { lastSession } from "./lastSession.svelte";
  import { workspaces } from "./workspaces.svelte";
  import { deepLink } from "./deepLink.svelte";
  import { showPrompt } from "./promptModal.svelte.ts";
  import { showConfirm } from "./confirmModal.svelte.ts";
  import { toast } from "./toast.svelte.ts";
  import { probeState } from "./probeState.svelte";
  import { MCP_SYSTEM_PROMPT_HINT } from "./mcpSystemPrompt";
  import { copyText, writeClipboard } from "./clipboard";
  import { IconCopy, IconPlay, IconStop, IconPause, IconGlobe, IconArrowUp, IconArrowDown, IconRefresh } from "./iconMap";
  import { localShellPrefs } from "./localShellPrefs.svelte.ts";
  import { recordingsModal } from "./recording.svelte";
  import { syncState } from "./syncState.svelte";
  import { themes } from "./themes";
  import LogViewer from "./LogViewer.svelte";
  import UpdateModal from "./UpdateModal.svelte";
  import SearchableSelect from "./SearchableSelect.svelte";
  import { fileToBase64, CERT_FILE_ACCEPT } from "./fileB64";
  import { updateCheck } from "./updateCheck.svelte";
  import { registerUnsaved, confirmLeave } from "./unsaved.svelte.ts";
  import Switch from "./Switch.svelte";

  let browserPath = $state("");
  let browserPersistent = $state(false);

  // App-wide starting directory for local shells. Empty means "the
  // user's home", which is what every other terminal does; before this
  // setting existed the shell inherited the app's own cwd, i.e. the
  // directory the executable lives in.
  let localShellDir = $state("");
  let localShellDirTimer: ReturnType<typeof setTimeout> | null = null;

  function saveLocalShellDir(v: string) {
    localShellDir = v;
    if (localShellDirTimer) clearTimeout(localShellDirTimer);
    localShellDirTimer = setTimeout(() => {
      api.settingsSet("local_shell_dir", v.trim()).catch((e) =>
        console.warn("local_shell_dir set:", e),
      );
      localShellDirTimer = null;
    }, 300);
  }

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
  // Store-wide "manage" grant (create folders/connections). Not persisted -
  // read from the backend each open, dies with the process.
  let mcpManage = $state<boolean>(false);
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
  // Claude Desktop registration: status is re-read after every write so the
  // button reflects reality rather than what we hoped the write did.
  let claudeDesktop = $state<ClaudeDesktopInfo | null>(null);
  let claudeDesktopBusy = $state(false);
  let claudeDesktopMsg = $state<string>("");
  let claudeDesktopErr = $state<string>("");
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
  let updateChannel = $state<"stable" | "rc">("stable");
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
  let aboutInfo = $state<AboutInfo | null>(null);
  const frontendDeps = __FRONTEND_DEPS__;

  // Plain-text build + environment summary for a bug report. Only what the
  // About panel shows: no hosts, no profile contents, no secrets.
  function diagnosticsText(): string {
    const v = versionInfo, a = aboutInfo;
    const lines = [
      `${v?.name ?? "ssh-tool"} ${v?.version ?? "?"} (commit ${v?.commit ?? "?"}, ${a?.commit_date ?? "?"}, schema v${v?.schema_version ?? "?"})`,
      `Platform: ${a?.os_name ? a.os_name + " " : ""}(${a?.os ?? "?"})`,
      `Engine: ${a?.engine || "unknown"}`,
      `Go: ${a?.go_version ?? "?"}`,
      "",
      "Components:",
      ...(a?.components ?? []).map((c) => `  ${c.name}: ${c.version}`),
      ...frontendDeps.map((c) => `  ${c.name}: ${c.version}`),
      "",
      "Plugins:",
      ...(a?.plugins ?? []).map((p) => `  ${p.name}: ${p.installed ? (p.version || "installed") : "not installed"}`),
    ];
    return lines.join("\n");
  }

  async function copyDiagnostics() {
    try {
      await navigator.clipboard.writeText(diagnosticsText());
      toast.ok("Diagnostics copied");
    } catch (e: any) {
      toast.err(`Copy failed: ${e?.message ?? e}`);
    }
  }

  let versionInfo = $state<{ name: string; version: string; commit: string; schema_version: number } | null>(null);
  // Profile statistics for the About section. profileStats() counts
  // straight from the DB (incl. resolved VNC, configured forwards +
  // bookmarks, cached dynamic entries by kind); active tunnels and
  // session counts are runtime values on top of that.
  let profileStats = $state<import("./api").ProfileStats | null>(null);

  // Desktop integration (Linux). null until queried; can_offer is false
  // on a packaged install and on every other platform, so the block
  // simply never renders there.
  let installState = $state<Awaited<ReturnType<typeof api.getInstallState>> | null>(null);
  let installing = $state(false);
  // "the Start Menu" on Windows, "your applications menu" elsewhere -
  // the mechanism differs but the user-facing idea is the same.
  const menuName = $derived(
    navigator.userAgent.includes("Windows") ? "the Start Menu" : "your applications menu",
  );
  let installMsg = $state("");

  let installOfferDisabled = $state(false);

  async function setInstallOfferDisabled(v: boolean) {
    installOfferDisabled = v;
    try {
      await api.settingsSet("install_offer_disabled", v ? "1" : "0");
    } catch (e: any) {
      installMsg = errMsg(e);
      installOfferDisabled = !v;
    }
  }

  let uninstalling = $state(false);
  let uninstallMsg = $state("");

  async function uninstallUserPrefix() {
    // Uses the app's own confirm dialog rather than an inline two-step:
    // this deletes files, and the surrounding text is easy to skim past.
    const ok = await showConfirm({
      title: "Remove desktop integration",
      message:
        "Delete the launcher entry, icon and the installed copy? " +
        "Your connections, credentials and settings are not affected.",
      okLabel: "Remove",
      danger: true,
    });
    if (!ok) return;

    uninstalling = true;
    uninstallMsg = "";
    try {
      const removed = await api.uninstallUserPrefix();
      uninstallMsg = removed
        ? `Removed. ssh-tool keeps running from ${installState?.exe_path ?? "its current location"} until you close it.`
        : "Nothing to remove.";
      installState = await api.getInstallState();
    } catch (e: any) {
      uninstallMsg = errMsg(e);
    } finally {
      uninstalling = false;
    }
  }

  async function installToUserPrefix() {
    installing = true;
    installMsg = "";
    try {
      const path = await api.installToUserPrefix();
      installMsg = `Installed to ${path}.`;
      // Re-read: after a successful install there is nothing left to
      // offer, and the block should disappear on its own. The install
      // also re-points any existing registrations, so their staleness
      // warnings need refreshing too.
      installState = await api.getInstallState();
      await Promise.all([
        refreshURLSchemeStatus(),
        refreshExplorerMenuStatus(),
        refreshIntegrationInfo(),
      ]);

      // Same as the startup offer: the running process is still the
      // binary that was launched, so until it restarts the window is
      // bound to a path with no desktop entry. Telling the user to
      // restart and leaving them to do it is the worse half of the job.
      const restart = await showConfirm({
        title: "Restart now?",
        message:
          `Installed to ${path}. ssh-tool is still running the copy you launched; ` +
          "restarting switches to the installed one.",
        okLabel: "Restart",
        cancelLabel: "Later",
      });
      if (restart) {
        await api.relaunchFromInstall(path);
      }
    } catch (e: any) {
      installMsg = errMsg(e);
    } finally {
      installing = false;
    }
  }
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
    const trimmed = name?.trim();
    if (!trimmed) return;
    wsErr = null;
    // An existing name means "save into that one". Without this the UNIQUE
    // constraint answers with a raw SQL error and the user has no way to
    // save an edited workspace under the name it already has.
    const existing = workspaces.findByName(trimmed);
    try {
      if (existing) {
        const ok = await showConfirm({
          title: "Overwrite workspace",
          message: `"${existing.name}" already exists. Replace it with the current tabs?`,
          okLabel: "Overwrite",
        });
        if (!ok) return;
        await workspaces.overwrite(existing.id, existing.name);
      } else {
        await workspaces.saveCurrentAs(trimmed);
      }
    } catch (e: any) { wsErr = errMsg(e); }
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

  async function loadDefaultSnippets() {
    try {
      const added = await api.snippetLoadDefaults();
      await reloadSnippets();
      toast.ok(added > 0 ? `Added ${added} default snippet${added === 1 ? "" : "s"}` : "Defaults already present");
    } catch (e: any) {
      toast.err("Load defaults failed: " + errMsg(e));
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

  onMount(() => { void connectPrefs.load(); });
  onMount(async () => {
    try {
      const v = await api.settingsGet("preferred_browser_path");
      browserPath = v ?? "";
      savedPath = v ?? "";
      browserPersistent = (await api.settingsGet("browser_persistent_profile")) === "true";
      localShellDir = (await api.settingsGet("local_shell_dir")) ?? "";
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
    try {
      updateChannel = (await api.settingsGet("update_channel")) === "rc" ? "rc" : "stable";
    } catch { /* default stable */ }
    try { logDirPath = (await api.logDir()) ?? ""; } catch { /* ignore */ }
    try { recordingsDirPath = (await api.recordingsDir()) ?? ""; } catch { /* ignore */ }
    try { versionInfo = await api.appVersion(); } catch { /* ignore */ }
    try { aboutInfo = await api.appAbout(); } catch { /* ignore */ }
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
    try { mcpManage = await api.mcpGetManageStore(); } catch { /* default off */ }
    try { mcpReadonlyExtra = (await api.settingsGet("mcp_readonly_extra")) ?? ""; } catch { /* ignore */ }
    try { mcpExePath = (await api.appExePath()) ?? ""; } catch { /* ignore */ }
    try { mcpWslExePath = (await api.appWslExePath()) ?? ""; } catch { /* ignore */ }
    try { claudeDesktop = await api.claudeDesktopStatus(); } catch { /* block stays hidden */ }
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

  async function toggleMcpManage(next: boolean) {
    try {
      await api.mcpSetManageStore(next);
      mcpManage = next;
    } catch (e) { toast.err("Manage toggle failed: " + errMsg(e)); }
  }

  async function saveMcpReadonlyExtra() {
    try { await api.settingsSet("mcp_readonly_extra", mcpReadonlyExtra.trim()); }
    catch (e) { console.warn("mcp allowlist:", e); }
  }

  // Copy the exact registration command shown in a cmd-block. Each block is
  // something the user has to paste into another program, so every one of them
  // gets a copy button rather than making them select multi-line text by hand.
  // Scope for the generated `claude mcp add` line. Local (the CLI default)
  // registers the server for the current working directory only, which is a
  // common surprise - the connector is missing in every other project. User
  // scope registers it once for all of them.
  let mcpScope = $state<"user" | "local">("user");
  // Which shell the `claude mcp add` line will be pasted into. Same client and
  // same server - only the path to the binary and its quoting differ, so this
  // is one command with a shell picker rather than the separate blocks further
  // down the page it used to be (they read as unrelated registrations).
  let mcpWhere = $state<"pwsh" | "cmd" | "wsl">("pwsh");

  // Quote a path for the chosen shell. Windows paths routinely contain a space
  // ("C:\\Users\\First Last\\..."), which silently splits the command into the
  // wrong argv without this. The quoted path is an argument to `claude`, not
  // the command being invoked, so no PowerShell call operator is involved.
  function mcpQuote(path: string, shell: "pwsh" | "cmd" | "wsl"): string {
    if (!path.includes(" ")) return path;
    if (shell === "wsl") return `'${path}'`;
    return `"${path}"`;
  }

  async function copyCmd(text: string, what: string) {
    try {
      await writeClipboard(text);
      toast.ok(what + " copied");
    } catch {
      toast.err("Copy failed - clipboard unavailable");
    }
  }

  async function copyMcpSystemPrompt() {
    try {
      await writeClipboard(await api.mcpSystemPrompt());
      toast.ok("System prompt copied. " + MCP_SYSTEM_PROMPT_HINT);
    } catch {
      toast.err("Copy failed - clipboard unavailable");
    }
  }

  // Merge an ssh-tool entry into Claude Desktop's config. The backend refuses
  // to touch a file it can't parse and backs up whatever was there, so the
  // only thing to handle here is telling the user it worked and that Claude
  // Desktop has to restart before it re-reads the file.
  async function registerClaudeDesktop() {
    claudeDesktopBusy = true;
    claudeDesktopMsg = "";
    claudeDesktopErr = "";
    try {
      const path = await api.claudeDesktopRegister();
      claudeDesktopMsg = `Written to ${path}. Restart Claude Desktop to pick it up.`;
      toast.ok("Added to Claude Desktop - restart it to load the server");
    } catch (e) {
      claudeDesktopErr = String(e);
      toast.err("Could not write the Claude Desktop config");
    } finally {
      claudeDesktopBusy = false;
      try { claudeDesktop = await api.claudeDesktopStatus(); } catch { /* keep the old view */ }
      // This one is reported by the status-bar badge too.
      void desktopAlerts.refresh();
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
  // Switching channel re-checks at once, so the status bar pill reflects
  // the new channel without waiting for the 6-hour timer.
  async function setUpdateChannel(next: "stable" | "rc") {
    updateChannel = next;
    try { await api.settingsSet("update_channel", next); }
    catch (e) { console.warn("update_channel save:", e); return; }
    if (!updateCheckDisabled) updateCheck.run().catch(console.warn);
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
  // Dropbox transport: user-supplied app key (public OAuth client ID, no
  // secret - PKCE), an app-folder path, and a connected flag driven by whether
  // a refresh token sits in the vault.
  // The exact loopback redirect URI the OAuth flow uses; the user must register
  // it verbatim in each provider's app console.
  let oauthRedirectUri = $state("");
  let dropboxAppKey = $state("");
  let dropboxFolder = $state("/ssh-tool");
  let hasDropboxToken = $state(false);
  let dropboxConnecting = $state(false);
  // OneDrive + Google Drive: user-supplied OAuth client ID, an implicit per-app
  // folder (no path), a connected flag from the vault token.
  let onedriveClientId = $state("");
  let onedriveAccountType = $state("personal"); // personal | work | both
  let hasOnedriveToken = $state(false);
  let onedriveConnecting = $state(false);
  let gdriveClientId = $state("");
  let gdriveClientSecret = $state(""); // Google needs a secret even for Desktop apps
  let hasGdriveSecret = $state(false);
  let hasGdriveToken = $state(false);
  let gdriveConnecting = $state(false);

  // A transport form with unsaved changes keeps them across every reload
  // (the section reopened, another transport saved or connected, a push).
  // The one just saved matches the stored values by then and takes them.
  let syncInitial: { w: string; s: string; d: string } | null = null;
  async function syncLoadConfig(keepDrafts = true) {
    try {
      // Until a config has loaded there is nothing stored to compare with:
      // the form's values from the very first load stand in (taken before
      // any request, and kept when a load fails), so whatever was typed
      // since counts as a draft.
      const snap = () => JSON.stringify([syncUrl, syncUsername, syncPassword]);
      const snapSftp = () => JSON.stringify([sftpHost, sftpPort, sftpUser, sftpDir, sftpAuthMode, sftpCredId]);
      const snapDropbox = () => JSON.stringify([dropboxAppKey, dropboxFolder]);
      const first = syncCfg === null;
      syncInitial ??= { w: snap(), s: snapSftp(), d: snapDropbox() };
      const before = syncInitial;
      if (!oauthRedirectUri) {
        oauthRedirectUri = await api.oauthRedirectUri();
      }
      const cfg = await api.syncConfigGet();
      // Measured once the answer is here, against the config the form was
      // edited from, so typing during the request counts too.
      const had = keepDrafts && !first;
      const keepWebdav = keepDrafts && (had ? webdavDirty : snap() !== before.w);
      const keepSftp = keepDrafts && (had ? sftpDirty : snapSftp() !== before.s);
      const keepDropbox = keepDrafts && (had ? dropboxDirty : snapDropbox() !== before.d);
      const prev = { syncUrl, syncUsername, sftpHost, sftpPort, sftpUser, sftpDir, sftpAuthMode, sftpCredId, dropboxAppKey, dropboxFolder };
      syncCfg = cfg;
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
      dropboxAppKey = syncCfg.dropbox_app_key;
      dropboxFolder = syncCfg.dropbox_folder || "/ssh-tool";
      hasDropboxToken = syncCfg.has_dropbox_token;
      onedriveClientId = syncCfg.onedrive_client_id;
      onedriveAccountType = syncCfg.onedrive_account_type || "personal";
      hasOnedriveToken = syncCfg.has_onedrive_token;
      gdriveClientId = syncCfg.gdrive_client_id;
      hasGdriveSecret = syncCfg.has_gdrive_secret;
      hasGdriveToken = syncCfg.has_gdrive_token;
      if (keepWebdav) ({ syncUrl, syncUsername } = prev);
      if (keepSftp) ({ sftpHost, sftpPort, sftpUser, sftpDir, sftpAuthMode, sftpCredId } = prev);
      if (keepDropbox) ({ dropboxAppKey, dropboxFolder } = prev);
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
        passphrase: "", // passphrase is set separately (global field)
      });
      sftpInlinePassword = "";
      sftpInlineKeyPem = "";
      sftpInlineKeyPassphrase = "";
      await syncLoadConfig();
      toast.ok("SFTP sync settings saved");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function copyRedirectUri() {
    await copyText(oauthRedirectUri, { label: "Redirect URI" });
  }

  // The snapshot passphrase is one value shared by every backend (it seals the
  // snapshot, not the transport). Changing an EXISTING one is dangerous: any
  // backend whose snapshot was sealed with the old value can no longer be
  // pulled. Warn before overwriting; a first-time set needs no warning.
  async function saveSyncPassphrase() {
    if (!syncPassphrase.trim()) return;
    syncErr = null;
    if (syncCfg?.has_passphrase) {
      const ok = confirm(
        "Change the sync passphrase?\n\n" +
        "This passphrase seals the encrypted snapshot on EVERY backend " +
        "(WebDAV, SFTP, Dropbox, OneDrive, Google Drive). If you change it, " +
        "any snapshot already pushed with the old passphrase can no longer be " +
        "pulled until you push a fresh one over it.\n\n" +
        "Only change this if you know the new value is correct."
      );
      if (!ok) return;
    }
    try {
      await api.syncPassphraseSet(syncPassphrase);
      syncPassphrase = "";
      await syncLoadConfig();
      toast.ok("Sync passphrase saved");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function dropboxSaveConfig() {
    syncErr = null;
    try {
      await api.dropboxConfigSet(dropboxAppKey, dropboxFolder);
      await syncLoadConfig();
      toast.ok("Dropbox sync settings saved");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function dropboxConnect() {
    syncErr = null;
    dropboxConnecting = true;
    try {
      // Persist the app key + folder first so the connect flow reads them.
      await api.dropboxConfigSet(dropboxAppKey, dropboxFolder);
      await api.dropboxConnect();
      await syncLoadConfig();
      toast.ok("Dropbox connected");
    } catch (e) {
      syncErr = errMsg(e);
    } finally {
      dropboxConnecting = false;
    }
  }

  async function dropboxDisconnect() {
    syncErr = null;
    try {
      await api.dropboxDisconnect();
      await syncLoadConfig();
      toast.ok("Dropbox disconnected");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  // OneDrive + Google Drive share the same connect/disconnect shape as Dropbox
  // (user-supplied client ID, PKCE browser flow, vault-held refresh token); the
  // only difference is which api.* call runs and that they carry no folder.
  async function onedriveConnect() {
    syncErr = null;
    onedriveConnecting = true;
    try {
      await api.onedriveConfigSet(onedriveClientId, onedriveAccountType);
      await api.onedriveConnect();
      await syncLoadConfig();
      toast.ok("OneDrive connected");
    } catch (e) {
      syncErr = errMsg(e);
    } finally {
      onedriveConnecting = false;
    }
  }

  async function onedriveDisconnect() {
    syncErr = null;
    try {
      await api.onedriveDisconnect();
      await syncLoadConfig();
      toast.ok("OneDrive disconnected");
    } catch (e) {
      syncErr = errMsg(e);
    }
  }

  async function gdriveConnect() {
    syncErr = null;
    gdriveConnecting = true;
    try {
      await api.gdriveConfigSet(gdriveClientId, gdriveClientSecret);
      gdriveClientSecret = "";
      await api.gdriveConnect();
      await syncLoadConfig();
      toast.ok("Google Drive connected");
    } catch (e) {
      syncErr = errMsg(e);
    } finally {
      gdriveConnecting = false;
    }
  }

  async function gdriveDisconnect() {
    syncErr = null;
    try {
      await api.gdriveDisconnect();
      await syncLoadConfig();
      toast.ok("Google Drive disconnected");
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

  // Each transport's form against the stored config: Save is live only
  // when something differs (a typed secret always counts), Discard reloads.
  // No stored config read (the load failed): Save stays available.
  const webdavDirty = $derived(!syncCfg || (syncUrl !== syncCfg.url || syncUsername !== syncCfg.username || syncPassword !== ""));
  const sftpDirty = $derived(!syncCfg || (
    sftpHost !== syncCfg.sftp_host || sftpPort !== (syncCfg.sftp_port || 22) || sftpUser !== syncCfg.sftp_user ||
    sftpDir !== syncCfg.sftp_dir || sftpAuthMode !== (syncCfg.sftp_auth_mode || "credential") || sftpCredId !== syncCfg.sftp_cred_id ||
    sftpInlinePassword !== "" || sftpInlineKeyPem !== "" || sftpInlineKeyPassphrase !== ""));
  const dropboxDirty = $derived(!syncCfg || (dropboxAppKey !== syncCfg.dropbox_app_key || dropboxFolder !== (syncCfg.dropbox_folder || "/ssh-tool")));
  // Back to the stored values for one transport only: a draft on another
  // transport's form stays.
  function syncDiscard(kind: "webdav" | "sftp" | "dropbox") {
    const c = syncCfg;
    if (kind === "webdav") {
      syncPassword = "";
      if (c) { syncUrl = c.url; syncUsername = c.username; }
    } else if (kind === "sftp") {
      sftpInlinePassword = ""; sftpInlineKeyPem = ""; sftpInlineKeyPassphrase = "";
      if (c) {
        sftpHost = c.sftp_host; sftpPort = c.sftp_port || 22; sftpUser = c.sftp_user; sftpDir = c.sftp_dir;
        sftpAuthMode = c.sftp_auth_mode || "credential"; sftpCredId = c.sftp_cred_id;
      }
    } else if (c) {
      dropboxAppKey = c.dropbox_app_key; dropboxFolder = c.dropbox_folder || "/ssh-tool";
    }
  }

  async function syncSaveConfig() {
    syncErr = null;
    try {
      await api.syncConfigSet(syncUrl, syncUsername, syncPassword, "");
      syncPassword = "";
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
  type ImportSource = "rdm" | "sshconfig" | "mobaxterm" | "putty" | "superputty" | "kafkatool" | "archive";
  // Ordered by expected traffic: our own archive format first (also
  // hosts the ssh-tool:// handler registration), then the common
  // tool migrations, RDM last.
  let importSource = $state<ImportSource>("archive");
  const IMPORT_SOURCES: { id: ImportSource; name: string; desc: string }[] = [
    { id: "archive", name: "ssh-tool archive", desc: "TOML / JSON export from another ssh-tool" },
    { id: "sshconfig", name: "ssh_config", desc: "OpenSSH client config - Host blocks, ProxyJump" },
    { id: "putty", name: "PuTTY / KiTTY", desc: ".reg registry export - SSH sessions" },
    { id: "superputty", name: "SuperPuTTY", desc: "Sessions.xml - SSH sessions + folders" },
    { id: "mobaxterm", name: "MobaXterm", desc: ".mxtsessions export - SSH sessions + bookmark folders" },
    { id: "rdm", name: "Devolutions RDM", desc: "JSON export - folders, connections, jumps, icons" },
    { id: "kafkatool", name: "kafka-tool", desc: "profiles.json - Kafka clusters with their SSH tunnels" },
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

  // ----- SuperPuTTY import -----

  let superInputEl: HTMLInputElement | undefined = $state();
  let superBusy = $state(false);
  let superError = $state<string | null>(null);
  let superSummary = $state<SuperPuttyImportSummary | null>(null);
  let superTargetFolderID = $state(""); // "" = root

  async function onSuperPuttyFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    superBusy = true;
    superError = null;
    superSummary = null;
    try {
      const text = await readImportFile(f);
      superSummary = await api.superPuttyImport(text, superTargetFolderID || undefined);
      await tree.load();
    } catch (err) {
      superError = errMsg(err);
    } finally {
      superBusy = false;
      if (superInputEl) superInputEl.value = "";
    }
  }

  // ----- kafka-tool import -----

  let kafkaInputEl: HTMLInputElement | undefined = $state();
  let kafkaBusy = $state(false);
  let kafkaError = $state<string | null>(null);
  let kafkaSummary = $state<KafkaToolImportSummary | null>(null);
  let kafkaTargetFolderID = $state(""); // "" = root

  async function onKafkaToolFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    kafkaBusy = true;
    kafkaError = null;
    kafkaSummary = null;
    try {
      const text = await readImportFile(f);
      kafkaSummary = await api.kafkaToolImport(text, kafkaTargetFolderID || undefined);
      await tree.load();
    } catch (err) {
      kafkaError = errMsg(err);
    } finally {
      kafkaBusy = false;
      if (kafkaInputEl) kafkaInputEl.value = "";
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

  // Richer status for the Desktop integration section: registered, and
  // whether the stored path still points at this binary.
  let urlSchemeInfo = $state<import("./api").IntegrationStatus | null>(null);
  let explorerMenuInfo = $state<import("./api").IntegrationStatus | null>(null);

  async function refreshIntegrationInfo() {
    try {
      [urlSchemeInfo, explorerMenuInfo] = await Promise.all([
        api.urlSchemeIntegration(),
        api.explorerMenuIntegration(),
      ]);
    } catch { /* the plain status strings still render */ }
    // Keep the status-bar badge in step: fixing something here should
    // clear it without waiting for a restart.
    void desktopAlerts.refresh();
  }

  async function refreshURLSchemeStatus() {
    try { urlSchemeStatus = await api.urlSchemeStatus(); } catch {}
  }

  async function registerURLScheme() {
    urlSchemeBusy = true; urlSchemeMsg = null;
    try {
      await api.registerURLScheme();
      urlSchemeMsg = "Registered. ssh-tool:// links now launch this app.";
      // Refresh the staleness check too, or the warning stays up after
      // the thing it warns about has been fixed.
      await Promise.all([refreshURLSchemeStatus(), refreshIntegrationInfo()]);
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

  // Re-registering writes the current binary's path over the stored
  // one. Same call as adding it; the separate button exists so the
  // label says what the user actually wants here.
  async function repointExplorerMenu() {
    explorerMenuBusy = true; explorerMenuMsg = null;
    try {
      await api.explorerMenuRegister();
      explorerMenuMsg = "Re-pointed at this copy of ssh-tool.";
      await Promise.all([refreshExplorerMenuStatus(), refreshIntegrationInfo()]);
    } catch (e: any) {
      explorerMenuMsg = `Failed: ${errMsg(e)}`;
    } finally {
      explorerMenuBusy = false;
    }
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
      await Promise.all([refreshExplorerMenuStatus(), refreshIntegrationInfo()]);
    } catch (e: any) {
      explorerMenuMsg = `Failed: ${errMsg(e)}`;
    } finally {
      explorerMenuBusy = false;
    }
  }

  // Everything the Desktop integration section shows. Queried on mount
  // rather than when the section opens: the calls are cheap, and the
  // command palette can jump straight into the section.
  onMount(async () => {
    refreshURLSchemeStatus();
    refreshExplorerMenuStatus();
    refreshIntegrationInfo();
    try {
      installState = await api.getInstallState();
      installOfferDisabled = (await api.settingsGet("install_offer_disabled")) === "1";
    } catch { /* non-fatal: the block stays hidden */ }
  });

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
  // Section list + metadata live in ./settingsSections so the command
  // palette (App.svelte) can build a "Settings: X" jump action per
  // section without duplicating the list.
  const SECTIONS: SectionDef[] = SETTINGS_SECTIONS;

  let activeSection = $state<SectionId>("terminal");
  // Which backend tab is showing inside the "External secrets" section.
  let externalTab = $state<ExternalTabId>("keepass");

  // Deep-link / restore helper: keepass|bitwarden|infisical map to the
  // "external" section with that tab pre-selected; anything else is a
  // plain section id. Returns null for an unknown id.
  function resolveSection(id: string | null | undefined): SectionId | null {
    if (!id) return null;
    if (isExternalTab(id)) {
      externalTab = id;
      return "external";
    }
    return SECTIONS.some((s) => s.id === id) ? (id as SectionId) : null;
  }

  // ----- Network profiles (WireGuard + NetBird) -----
  let npName = $state("");
  let npConf = $state("");
  let npEditingId = $state<string | null>(null);
  let npEditKind = $state<"wireguard" | "netbird" | "tailscale" | "openconnect" | "openvpn" | "checkpoint">("wireguard");
  let npKind = $state<"wireguard" | "netbird" | "tailscale" | "openconnect" | "openvpn" | "checkpoint">("wireguard");
  // OpenConnect form fields
  const ocEmpty = (): import("./api").OpenConnectConfig => ({ server: "", flavor: "anyconnect", second_password: "prompt", password_credential_id: "", second_credential_id: "", client_cert_credential_id: "" });
  let oc = $state<import("./api").OpenConnectConfig>(ocEmpty());
  let ocMtu = $state("");
  // Backup gateways, one per line; their trusted certificates stay in the
  // backend, which carries them over by address.
  let ocBackups = $state("");
  // Inline secrets typed on the profile form: stored as credentials in the
  // vault when the profile is added / saved, then referenced by id.
  let ocNewPassword = $state("");
  let ocCertMode = $state<"pick" | "file" | "paste">("pick");
  let ocCertFile = $state<File | null>(null);
  let ocCertKeyFile = $state<File | null>(null);
  let ocCertPaste = $state("");
  let ocCertPassword = $state("");
  // OpenVPN form fields. ovText is a newly imported .ovpn (secrets
  // included; the backend moves them to the vault), "" = keep the stored.
  const ovEmpty = (): import("./api").OpenVPNConfig => ({ second_password: "prompt", password_credential_id: "", second_credential_id: "", client_cert_credential_id: "" });
  let ov = $state<import("./api").OpenVPNConfig>(ovEmpty());
  let ovText = $state("");
  let ovFileName = $state("");
  let ovPasteOpen = $state(false);
  let ovNewPassword = $state("");
  // What the imported (or stored) configuration asks for, to show only
  // the fields that matter.
  const ovNeedsLogin = $derived(ovText ? /^\s*auth-user-pass\b/m.test(ovText) : !!ov.auth_user_pass);
  const ovChallenge = $derived(ovText ? (ovText.match(/^\s*static-challenge\s+"([^"]*)"/m)?.[1] ?? "") : (ov.static_challenge ?? ""));
  const ovHasCert = $derived(/<cert>/.test(ovText));
  // Check Point form fields.
  const cpEmpty = (): import("./api").CheckpointConfig => ({ server: "", second_password: "prompt", password_credential_id: "", second_credential_id: "" });
  let cp = $state<import("./api").CheckpointConfig>(cpEmpty());
  let cpNewPassword = $state("");
  // Live tunnel details, per profile, shown on demand.
  let npInfoOpen = $state<Record<string, import("./api").VpnTunnelInfo | null | undefined>>({});
  let npBusy = $state(false);
  // A profile delete, from its confirmation to the end: part of npAnyBusy.
  let npDeleteBusy = $state(false);
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
  const ocInstalled = $derived(plugins.some((p) => p.name === "openconnect" && p.installed));
  const ovInstalled = $derived(plugins.some((p) => p.name === "openvpn" && p.installed));
  const cpInstalled = $derived(plugins.some((p) => p.name === "checkpoint" && p.installed));
  // Credentials holding a password (vault or an external backend).
  // Client certificate credentials for the OpenConnect profile.
  const certCredOptions = $derived(
    credentials.list
      .filter((c) => c.kind === "certificate")
      .map((c) => ({ value: c.id, label: `${c.name} - ${String(c.config?.subject ?? "")}` })),
  );
  // Why "Add profile" is disabled for an OpenConnect profile - a disabled
  // button alone gave no clue.
  const ocMissing = $derived(
    [!npName.trim() && "a name", !oc.server.trim() && "the gateway", !ocInstalled && "the OpenConnect plugin"].filter(Boolean).join(", "),
  );
  const ovMissing = $derived(
    [!npName.trim() && "a name", !ovText.trim() && "an .ovpn configuration", !ovInstalled && "the OpenVPN plugin"].filter(Boolean).join(", "),
  );
  const cpMissing = $derived(
    [!npName.trim() && "a name", !cp.server.trim() && "the gateway", !cpInstalled && "the Check Point plugin"].filter(Boolean).join(", "),
  );
  const passwordCredOptions = $derived(
    credentials.list
      .filter((c) => c.kind === "password")
      .map((c) => ({ value: c.id, label: c.name })),
  );
  // External items that can carry a TOTP secret.
  const totpCredOptions = $derived(
    credentials.list
      .filter((c) => !!c.config?.bitwarden_ref || !!c.config?.keepass_ref)
      .map((c) => ({ value: c.id, label: c.name })),
  );
  // api_token credentials for the setup-key picker.
  const apiTokenCredOptions = $derived(
    credentials.list
      .filter((c) => c.kind === "api_token")
      .map((c) => ({ value: c.id, label: c.name })),
  );

  let wgBindPhysical = $state(false);
  // Idle stop: minutes a tunnel stays up after its last SSH session
  // closes. -1 = never. The global value applies to every profile
  // without its own override; unset means the built-in 2 minutes.
  const IDLE_STOP_KEY = "network_idle_stop_minutes";
  const IDLE_STOP_DEFAULT = 2;
  const IDLE_STOP_PRESETS = [0, 1, 2, 5, 10, 15, 30, 60, 120, 240, -1];
  let idleStopGlobal = $state(IDLE_STOP_DEFAULT);
  function idleStopLabel(m: number): string {
    if (m < 0) return "Never";
    if (m === 0) return "Right away";
    if (m < 60 || m % 60) return `After ${m} min`;
    return `After ${m / 60} h`;
  }
  function idleStopShort(m: number): string {
    if (m < 0) return "never";
    if (m === 0) return "at once";
    return m < 60 || m % 60 ? `${m} min` : `${m / 60} h`;
  }
  function idleStopOptions(cur: number | null): number[] {
    return cur === null || IDLE_STOP_PRESETS.includes(cur) ? IDLE_STOP_PRESETS : [...IDLE_STOP_PRESETS, cur].sort((a, b) => (a < 0 ? 1 : b < 0 ? -1 : a - b));
  }
  async function setIdleStopGlobal(v: string) {
    try {
      const m = Number(v);
      if (m === IDLE_STOP_DEFAULT) await api.settingsDelete(IDLE_STOP_KEY);
      else await api.settingsSet(IDLE_STOP_KEY, String(m));
      idleStopGlobal = m;
    } catch (e) { toast.err(errMsg(e)); }
  }
  async function npSetIdleStop(np: NetworkProfileInfo, v: string) {
    try {
      await api.networkProfileSetIdleStop(np.id, v === "" ? null : Number(v));
      await networkProfiles.load(true);
    } catch (e) { toast.err(errMsg(e)); }
  }
  let livenessProbe = $state(false);
  let livenessDynamic = $state(false);
  let livenessKeepTunnel = $state(false);
  let livenessIntervalSec = $state(60);

  $effect(() => {
    if (activeSection === "network") {
      networkProfiles.load().catch(() => {});
      credentials.load().catch(() => {});
      refreshPlugins();
      api.wgBindPhysicalGet().then((v) => (wgBindPhysical = v)).catch(() => {});
      spLoad();
      if (!isMobile) untrack(() => ciLoad());
      npLoadUsage();
      api.settingsGet(IDLE_STOP_KEY).then((v) => {
        const n = parseInt(v, 10);
        idleStopGlobal = v.trim() !== "" && !isNaN(n) ? n : IDLE_STOP_DEFAULT;
      }).catch(() => {});
    }
    if (activeSection === "liveness") {
      api.settingsGet("liveness_probe_enabled").then((v) => (livenessProbe = v === "1")).catch(() => {});
      api.settingsGet("liveness_probe_dynamic").then((v) => (livenessDynamic = v === "1")).catch(() => {});
      api.settingsGet("liveness_probe_keep_tunnel").then((v) => (livenessKeepTunnel = v === "1")).catch(() => {});
      api.settingsGet("liveness_probe_interval_sec").then((v) => {
        const n = parseInt(v, 10);
        if (Number.isFinite(n) && n >= 10) livenessIntervalSec = n;
      }).catch(() => {});
    }
  });

  async function toggleWgBindPhysical(on: boolean) {
    try {
      await api.wgBindPhysicalSet(on);
      wgBindPhysical = on;
      networkProfiles.load(true).catch(() => {});
    } catch (e) {
      toast.err("WireGuard bind toggle failed: " + errMsg(e));
    }
  }

  async function toggleLivenessProbe(on: boolean) {
    try {
      await api.settingsSet("liveness_probe_enabled", on ? "1" : "0");
      livenessProbe = on;
      probeState.setEnabled(on);
    } catch (e) {
      toast.err("Liveness probe toggle failed: " + errMsg(e));
    }
  }

  async function toggleLivenessKeepTunnel(on: boolean) {
    try {
      await api.settingsSet("liveness_probe_keep_tunnel", on ? "1" : "0");
      livenessKeepTunnel = on;
    } catch (e) {
      toast.err("Keep-tunnel toggle failed: " + errMsg(e));
    }
  }

  async function toggleLivenessDynamic(on: boolean) {
    try {
      await api.settingsSet("liveness_probe_dynamic", on ? "1" : "0");
      livenessDynamic = on;
      probeState.setDynEnabled(on);
    } catch (e) {
      toast.err("Dynamic probe toggle failed: " + errMsg(e));
    }
  }

  async function saveLivenessInterval(sec: number) {
    const n = Math.max(10, Math.floor(sec) || 60);
    livenessIntervalSec = n;
    try {
      await api.settingsSet("liveness_probe_interval_sec", String(n));
      probeState.setIntervalSec(n);
    } catch (e) {
      toast.err("Interval save failed: " + errMsg(e));
    }
  }

  async function refreshPlugins() {
    try {
      plugins = (await api.pluginsStatus()) ?? [];
      updateCheck.applyPluginStatus(plugins);
    } catch { /* ignore */ }
  }

  // Download progress per plugin (the Go side reports resolve / download /
  // verify / install), and a refresh of both the plugin cards and About
  // when a plugin is installed or removed - About used to keep the list it
  // read when Settings opened.
  let pluginProgress = $state<Record<string, PluginDownloadProgress>>({});
  $effect(() => {
    const offProgress = EventsOn<PluginDownloadProgress>("plugin_download_progress", (p) => {
      pluginProgress = { ...pluginProgress, [p.name]: p };
    });
    const offChanged = EventsOn("plugins_changed", async () => {
      await refreshPlugins();
      try { aboutInfo = await api.appAbout(); } catch { /* ignore */ }
    });
    const offCi = EventsOn("ci_runner_changed", () => { if (!isMobile) void ciLoad(); });
    return () => { offProgress(); offChanged(); offCi(); };
  });
  function fmtMB(n: number): string {
    return (n / 1048576).toFixed(1);
  }
  function progressText(p: PluginDownloadProgress): string {
    switch (p.phase) {
      case "resolve": return "Looking up the latest plugin release…";
      case "download": return p.total > 0
        ? `Downloading ${p.version ?? ""} - ${fmtMB(p.read)} of ${fmtMB(p.total)} MB`
        : `Downloading ${p.version ?? ""} - ${fmtMB(p.read)} MB`;
      case "verify": return "Verifying the checksum…";
      case "install": return "Installing…";
    }
  }
  function engineLabel(name: string): string {
    return pluginTitle(name);
  }
  const kindTitle = pluginTitle;

  function ocConfigFromForm(): import("./api").OpenConnectConfig {
    const n = parseInt(ocMtu, 10);
    const backups = ocBackups.split("\n").map((l) => l.trim()).filter(Boolean).map((server) => ({ server }));
    return { ...oc, server: oc.server.trim(), backups, mtu: isNaN(n) ? 0 : n };
  }
  async function npCreateOpenConnect() {
    npBusy = true;
    try {
      await ocStoreInlineSecrets();
      await api.networkProfileCreateOpenConnect(npName.trim(), ocConfigFromForm());
      await networkProfiles.load(true);
      npDone();
      toast.ok("OpenConnect profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveOpenConnect() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await ocStoreInlineSecrets();
      await api.networkProfileUpdateOpenConnect(npEditingId, npName.trim(), ocConfigFromForm());
      await networkProfiles.load(true);
      npDone();
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function ovPickFile(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (!f) return;
    ovText = await f.text();
    ovFileName = f.name;
    if (!npName.trim()) npName = f.name.replace(/\.(ovpn|conf)$/i, "");
  }
  async function ovStoreInlineSecrets() {
    if (!ovNewPassword) return;
    const base = npName.trim() || "OpenVPN";
    const r = await api.credentialsCreate({
      kind: "password", name: `${base} - VPN password`, password: ovNewPassword,
      default_username: ov.username?.trim() || undefined,
    } as any);
    ov.password_credential_id = r.credential!.id;
    ovNewPassword = "";
    await credentials.load();
  }
  function ovResetInline() {
    ovText = ""; ovFileName = ""; ovPasteOpen = false; ovNewPassword = "";
  }
  async function npCreateOpenVPN() {
    npBusy = true;
    try {
      await ovStoreInlineSecrets();
      await api.networkProfileCreateOpenVPN(npName.trim(), ovText, { ...ov, username: ov.username?.trim() });
      await Promise.all([networkProfiles.load(true), credentials.load()]);
      npDone();
      toast.ok("OpenVPN profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveOpenVPN() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await ovStoreInlineSecrets();
      await api.networkProfileUpdateOpenVPN(npEditingId, npName.trim(), ovText, { ...ov, username: ov.username?.trim() });
      await Promise.all([networkProfiles.load(true), credentials.load()]);
      npDone();
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function cpStoreInlineSecrets() {
    if (!cpNewPassword) return;
    const base = npName.trim() || cp.server.trim();
    const r = await api.credentialsCreate({
      kind: "password", name: `${base} - VPN password`, password: cpNewPassword,
      default_username: cp.username?.trim() || undefined,
    } as any);
    cp.password_credential_id = r.credential!.id;
    cpNewPassword = "";
    await credentials.load();
  }
  function cpConfigFromForm(): import("./api").CheckpointConfig {
    return { ...cp, server: cp.server.trim(), username: cp.username?.trim(), login_option: cp.login_option?.trim() };
  }
  async function npCreateCheckpoint() {
    npBusy = true;
    try {
      await cpStoreInlineSecrets();
      await api.networkProfileCreateCheckpoint(npName.trim(), cpConfigFromForm());
      await networkProfiles.load(true);
      npDone();
      toast.ok("Check Point profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveCheckpoint() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await cpStoreInlineSecrets();
      await api.networkProfileUpdateCheckpoint(npEditingId, npName.trim(), cpConfigFromForm());
      await networkProfiles.load(true);
      npDone();
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  function tunnelDur(sec: number): string {
    const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60);
    if (h && m) return `${h}h ${m}m`;
    if (h) return `${h}h`;
    return m ? `${m} min` : `${sec}s`;
  }
  function tunnelInRel(unix: number): string {
    const left = unix - Math.floor(Date.now() / 1000);
    return left > 0 ? `in ${tunnelDur(left)}` : "already passed";
  }
  function tunnelInfoText(i: import("./api").VpnTunnelInfo): string {
    const lines = [
      `type:       ${ocFlavorTitle[i.flavor] ?? pluginTitle(i.flavor)}`,
      `server:     ${i.server}${i.gateway ? ` (${i.gateway})` : ""}`,
      `transport:  ${i.transport || "-"}${i.cipher ? `, ${i.cipher}` : ""}`,
      `address:    ${i.addresses.join(", ") || "-"}`,
      `mtu:        ${i.mtu || "-"}${i.local_mtu && i.local_mtu !== i.mtu ? ` (local ${i.local_mtu})` : ""}`,
      `dns:        ${i.dns.join(", ") || "-"}${i.tunnel_all_dns ? " (all queries)" : ""}`,
    ];
    if (i.search_domains?.length) lines.push(`search:     ${i.search_domains.join(", ")}`);
    if (i.split_dns?.length) lines.push(`split dns:  ${i.split_dns.join(", ")}`);
    lines.push(`routes:     ${i.routes.length ? i.routes.join("\n            ") : "all traffic"}`);
    if (i.excluded_routes?.length) lines.push(`excluded:   ${i.excluded_routes.join("\n            ")}`);
    if (i.auth_expires) lines.push(`session:    ends ${new Date(i.auth_expires * 1000).toLocaleString()} (${tunnelInRel(i.auth_expires)}) - the gateway disconnects then and asks for a new login`);
    if (i.idle_timeout_s) lines.push(`idle:       gateway drops the session after ${tunnelDur(i.idle_timeout_s)} without traffic`);
    if (i.banner) lines.push("", i.banner);
    return lines.join("\n");
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
      .then((n) => { if (!npEditingId && npKind === "netbird" && nbDevice.trim() === "") { nbDevice = n; npSuggested.nb = n; } })
      .catch(() => {});
  });

  // Same pre-fill for the Tailscale hostname (see the NetBird effect
  // above). Suggests "<hostname>" as the tailnet node name.
  $effect(() => {
    if (activeSection !== "network") return;
    if (npEditingId || npKind !== "tailscale") return;
    if (tsHostname.trim() !== "") return;
    api.suggestTailscaleHostname()
      .then((n) => { if (!npEditingId && npKind === "tailscale" && tsHostname.trim() === "") { tsHostname = n; npSuggested.ts = n; } })
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
    // Refresh the profile list itself while a tunnel is up so the live
    // rx/tx and last-handshake counters advance - the list otherwise only
    // reloads on the network_tunnel_changed event (connect/disconnect), so
    // the transfer counters stayed frozen at their connect-time value (0).
    const statusTimer = setInterval(() => {
      if (networkProfiles.list.some((p) => p.status?.running)) {
        networkProfiles.load(true).catch(() => {});
      }
    }, 3000);
    return () => { clearInterval(t); clearInterval(statusTimer); };
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
    pluginProgress = { ...pluginProgress, [name]: { name, phase: "resolve", read: 0, total: 0 } };
    try {
      await api.pluginDownload(name);
      await refreshPlugins();
      const p = plugins.find((x) => x.name === name);
      toast.ok(`${engineLabel(name)} plugin installed${p?.version ? ` (${p.version})` : ""}`);
    } catch (e: any) { toast.err(errMsg(e)); }
    finally {
      pluginBusy = false;
      const { [name]: _, ...rest } = pluginProgress;
      pluginProgress = rest;
    }
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
      await networkProfiles.load(true);
      npDone();
      toast.ok("NetBird profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveNetbird() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await api.networkProfileUpdateNetbird(npEditingId, npName.trim(), nbManagement.trim(), nbDevice.trim(), nbCredId);
      await networkProfiles.load(true);
      npDone();
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
      await networkProfiles.load(true);
      npDone();
      toast.ok("Tailscale profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npSaveTailscale() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await api.networkProfileUpdateTailscale(npEditingId, npName.trim(), tsControl.trim(), tsHostname.trim(), tsCredId);
      await networkProfiles.load(true);
      npDone();
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }

  async function npCreate() {
    npBusy = true;
    try {
      await api.networkProfileCreate(npName.trim(), npConf);
      await networkProfiles.load(true);
      npDone();
      toast.ok("Profile added");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  // Unsaved-edit guard for the profile's Settings form: the values it opened
  // with, compared with what is typed now. A profile switch asks first.
  let npBase = $state("");
  function npSnap(names: { nb: string; ts: string } = { nb: nbDevice, ts: tsHostname }): string {
    return JSON.stringify([npName, npConf, nbManagement, names.nb, nbCredId, nbNewKeyName, nbNewKeySecret, tsControl, names.ts, tsCredId, tsNewKeyName, tsNewKeySecret,
      oc, ocMtu, ocBackups, ocNewPassword, ocCertPaste, ocCertPassword, !!ocCertFile, !!ocCertKeyFile,
      ov, ovText, ovNewPassword, cp, cpNewPassword]);
  }
  const npDirty = $derived(npEditingId !== null && npSnap() !== npBase);
  function npStartEdit(np: NetworkProfileInfo) {
    npStartEditFields(np);
    npBase = npSnap();
  }
  // Saves the open Settings form with its kind's save; true once it is no
  // longer dirty (a failed save leaves it open, with the error shown).
  // A save or create, or an inline setup / auth key being created (it
  // writes its id into the form when done): the form is locked and no
  // save, create, discard or switch starts meanwhile.
  const npAnyBusy = $derived(npBusy || nbNewKeyBusy || tsNewKeyBusy || npDeleteBusy);
  async function npSaveCurrent(): Promise<boolean> {
    if (npAnyBusy) return false;
    const np = networkProfiles.list.find((p) => p.id === npEditingId);
    if (!np) return true;
    const save = { netbird: npSaveNetbird, openconnect: npSaveOpenConnect, openvpn: npSaveOpenVPN, checkpoint: npSaveCheckpoint, tailscale: npSaveTailscale }[np.kind as string] ?? npSaveEdit;
    await save();
    return !npDirty;
  }
  function npStartEditFields(np: NetworkProfileInfo) {
    npEditingId = np.id;
    npEditKind = np.kind;
    npName = np.name;
    if (np.kind === "netbird") {
      nbManagement = np.netbird?.management_url ?? "";
      nbDevice = np.netbird?.device_name ?? "";
      nbCredId = np.netbird?.setup_key_credential_id ?? "";
      return;
    }
    if (np.kind === "openvpn") {
      ov = { ...ovEmpty(), ...(np.openvpn ?? {}) };
      ovResetInline();
      return;
    }
    if (np.kind === "checkpoint") {
      cp = { ...cpEmpty(), ...(np.checkpoint ?? {}) };
      cpNewPassword = "";
      return;
    }
    if (np.kind === "openconnect") {
      oc = { ...ocEmpty(), ...(np.openconnect ?? {}) };
      ocResetInline();
      ocMtu = np.openconnect?.mtu ? String(np.openconnect.mtu) : "";
      ocBackups = (np.openconnect?.backups ?? []).map((b) => b.server).join("\n");
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
      .then((text) => {
        if (npEditingId !== np.id) return;
        // Typed before the config arrived: a name edit stays an edit; a
        // config already typed is not replaced.
        const touched = npSnap() !== npBase;
        if (npConf === "") npConf = text;
        if (!touched) { npBase = npSnap(); return; }
        const typedName = npName;
        npName = np.name;
        const typedConf = npConf;
        npConf = text;
        npBase = npSnap();
        npName = typedName;
        npConf = typedConf;
      })
      .catch((e) => toast.err(errMsg(e)));
  }
  function npCancelEdit() {
    npEditingId = null;
    npName = ""; npConf = "";
    nbManagement = ""; nbDevice = ""; nbCredId = "";
    tsControl = ""; tsHostname = ""; tsCredId = "";
    tsNewKeyOpen = false; tsNewKeyName = ""; tsNewKeySecret = "";
    nbNewKeyOpen = false; nbNewKeyName = ""; nbNewKeySecret = "";
    oc = ocEmpty(); ocMtu = ""; ocBackups = "";
    ocResetInline();
    ov = ovEmpty();
    ovResetInline();
    cp = cpEmpty(); cpNewPassword = "";
  }
  function ocResetInline() {
    ocNewPassword = ""; ocCertMode = "pick"; ocCertFile = null; ocCertKeyFile = null;
    ocCertPaste = ""; ocCertPassword = "";
  }
  // ocStoreInlineSecrets turns what was typed into the form into vault
  // credentials and points the profile at them. Runs before the profile
  // create / update, so a failed import leaves no half-made profile.
  async function ocStoreInlineSecrets() {
    const base = npName.trim() || oc.server.trim();
    if (ocNewPassword) {
      const r = await api.credentialsCreate({
        kind: "password", name: `${base} - VPN password`, password: ocNewPassword,
        default_username: oc.username?.trim() || undefined,
      } as any);
      oc.password_credential_id = r.credential!.id;
      ocNewPassword = "";
    }
    if (ocCertMode === "file" && ocCertFile || ocCertMode === "paste" && ocCertPaste.trim()) {
      const data = ocCertMode === "file" ? await fileToBase64(ocCertFile!) : btoa(unescape(encodeURIComponent(ocCertPaste)));
      const r = await api.credentialsCreate({
        kind: "certificate", name: `${base} - client certificate`, cert_data: data,
        cert_key_data: ocCertMode === "file" && ocCertKeyFile ? await fileToBase64(ocCertKeyFile) : "",
        cert_password: ocCertPassword,
      });
      oc.client_cert_credential_id = r.credential!.id;
      ocCertMode = "pick"; ocCertFile = null; ocCertKeyFile = null; ocCertPaste = ""; ocCertPassword = "";
    }
    await credentials.load();
  }
  async function npSaveEdit() {
    if (!npEditingId) return;
    npBusy = true;
    try {
      await api.networkProfileUpdate(npEditingId, npName.trim(), npConf);
      await networkProfiles.load(true);
      npDone();
      toast.ok("Profile saved");
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npBusy = false; }
  }
  async function npDelete(np: NetworkProfileInfo) {
    // Held from before the confirmation: a save or key creation must not
    // start while it is open, nor this delete start during one.
    if (npAnyBusy) return;
    npDeleteBusy = true;
    try {
      const ok = await showConfirm({
        title: "Delete network profile",
        message: `Delete "${np.name}"? Connections and folders set to it fall back to their inherited network.`,
        okLabel: "Delete",
      });
      if (!ok) return;
      await api.networkProfileDelete(np.id);
      // The delete also cleared the profile from connections / folders.
      await Promise.all([networkProfiles.load(true), tree.load()]);
      npSel = "general";
      npCancelEdit();
    } catch (e: any) { toast.err(errMsg(e)); }
    finally { npDeleteBusy = false; }
  }
  async function npSetPolicy(np: NetworkProfileInfo, mode: string, paused: boolean) {
    try {
      await api.networkProfileSetPolicy(np.id, mode, paused);
      await networkProfiles.load(true);
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  // Pause stops the tunnel; Resume brings it back up, so the button
  // undoes what it did rather than leaving the profile down until the
  // next connection (or a second click on Connect).
  async function npTogglePause(np: NetworkProfileInfo) {
    const mode = np.mode === "auto" ? "auto" : "always";
    if (!np.paused) {
      await npSetPolicy(np, mode, true);
      return;
    }
    await npSetPolicy(np, mode, false);
    const fresh = networkProfiles.list.find((p) => p.id === np.id);
    if (fresh && !fresh.paused) await npTest(fresh);
  }
  // Browser through a profile (web bookmarks live in the tree).
  async function npBrowser(np: NetworkProfileInfo, url: string) {
    try { await api.networkProfileOpenBrowser(np.id, url); } catch (e: any) { toast.err(errMsg(e)); }
  }
  // App proxy (the "shared" proxy in code): one SOCKS for every profile
  // marked shared, picking the profile by destination.
  let sp = $state<{ enabled: boolean; bind: string; port: number; priority: string[]; jumps: boolean; listening: boolean }>({ enabled: false, bind: "127.0.0.1", port: 1080, priority: [], jumps: false, listening: false });
  const spProfiles = $derived(networkProfiles.list.filter((p) => p.local_proxy?.shared));
  let spDetails = $state<import("./api").SharedProxyDetails | null>(null);
  // The stored listen address and port: the two fields are dirty while
  // they differ, and only Save sends them. The switches and priority clicks
  // save at once with the stored pair, not with half-typed text.
  let spBase = $state({ bind: "127.0.0.1", port: 1080 });
  const spDirty = $derived(sp.bind.trim() !== spBase.bind || Number(sp.port) !== spBase.port);
  function spDiscard() {
    sp.bind = spBase.bind;
    sp.port = spBase.port;
  }
  $effect(() => registerUnsaved({
    label: () => "App proxy",
    dirty: () => npSel === "general" && sp.enabled && spDirty,
    busy: () => spPending > 0,
    save: async () => { await spSave(true); return !spDirty; },
    discard: spDiscard,
    scope: "settings",
  }));
  async function spLoad() {
    try {
      const r = await api.networkSharedProxyGet();
      // Back on the page with an address still being typed: keep it.
      const keep = spDirty ? { bind: sp.bind, port: sp.port } : null;
      sp = { enabled: r.config.enabled, bind: r.config.bind || "127.0.0.1", port: r.config.port || 1080, priority: r.config.priority ?? [], jumps: !!r.config.jump_connections, listening: r.listening };
      spBase = { bind: sp.bind, port: sp.port };
      if (keep) { sp.bind = keep.bind; sp.port = keep.port; }
      spDetails = sp.enabled ? await api.networkSharedProxyDetails() : null;
    } catch { /* shown on save */ }
  }
  // Saves run one after another, each with the values from the moment it
  // was asked for, and only a reload with no newer save queued behind it
  // updates the form: quick priority clicks end in the order clicked last.
  let spQueue: Promise<void> = Promise.resolve();
  let spPending = 0;
  function spSave(fields = false): Promise<void> {
    // A switch sends the stored address and port as they are when its turn
    // in the queue comes, so an explicit Save queued before it is not
    // undone by the older pair.
    const typed = fields ? { bind: sp.bind.trim(), port: Number(sp.port) } : null;
    const rest = { enabled: sp.enabled, priority: $state.snapshot(sp.priority), jump_connections: sp.jumps };
    spPending++;
    spQueue = spQueue.then(async () => {
      const req = { ...rest, ...(typed ?? { bind: spBase.bind, port: spBase.port }) };
      try {
        await api.networkSharedProxySet(req);
        if (typed) spBase = { ...typed };
        if (spPending === 1) {
          const r = await api.networkSharedProxyGet();
          const d = r.config.enabled ? await api.networkSharedProxyDetails() : null;
          if (spPending === 1) {
            // Text typed while this request ran stays (spBase already
            // holds what a Save sent, so only later edits count as dirty).
            const keep = spDirty ? { bind: sp.bind, port: sp.port } : null;
            sp = { enabled: r.config.enabled, bind: r.config.bind || "127.0.0.1", port: r.config.port || 1080, priority: r.config.priority ?? [], jumps: !!r.config.jump_connections, listening: r.listening };
            spBase = { bind: sp.bind, port: sp.port };
            if (keep) { sp.bind = keep.bind; sp.port = keep.port; }
            spDetails = d;
          }
          toast.ok(req.enabled ? `App proxy on ${req.bind}:${req.port}` : "App proxy off");
        }
      } catch (e: any) { toast.err(errMsg(e)); } finally { spPending--; }
    });
    return spQueue;
  }
  // Priority for overlapping networks: listed profiles first, in order;
  // the rest (by route) after them.
  const spPrioList = $derived([
    ...sp.priority.map((id) => spProfiles.find((p) => p.id === id)).filter((p): p is NetworkProfileInfo => !!p),
    ...spProfiles.filter((p) => !sp.priority.includes(p.id)),
  ]);
  function spPrioMove(id: string, dir: -1 | 1 | 0) {
    // dir 0 toggles "listed"; -1 / 1 moves a listed profile.
    const list = sp.priority.filter((p) => spProfiles.some((x) => x.id === p));
    const i = list.indexOf(id);
    if (dir === 0) {
      if (i >= 0) list.splice(i, 1); else list.push(id);
    } else if (i >= 0) {
      const j = i + dir;
      if (j < 0 || j >= list.length) return;
      [list[i], list[j]] = [list[j], list[i]];
    }
    sp.priority = list;
    void spSave();
  }
  let spWhich = $state("");
  let spWhichOut = $state<{ ok: boolean; text: string } | null>(null);
  async function spWhichRun() {
    try {
      const r = await api.networkSharedProxyWhich(spWhich);
      spWhichOut = { ok: true, text: `${r.profile} - ${r.why}` };
    } catch (e: any) { spWhichOut = { ok: false, text: errMsg(e) }; }
  }
  async function spCopy(what: "url" | "gateway" | "routes") {
    try {
      const d = await api.networkSharedProxyDetails();
      spDetails = d;
      copyText(what === "url" ? d.url : what === "gateway" ? d.gateway : d.routes.join(","), { label: what === "url" ? "Proxy URL" : what === "gateway" ? "Gateway command" : "Routes" });
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  // CI runner (app_cirunner.go): a GitLab runner in Docker whose jobs
  // reach the VPNs through the app proxy's gateway.
  let ci = $state<{ config: CIRunnerConfig; status: CIRunnerStatus } | null>(null);
  type CiForm = { mode: "form" | "import"; url: string; name: string; image: string; token: string; toml: string; hostsText: string; directText: string; autoStart: boolean; stopOnLock: boolean };
  let ciForm = $state<CiForm>({ mode: "form", url: "", name: "", image: "", token: "", toml: "", hostsText: "", directText: "", autoStart: false, stopOnLock: true });
  let ciBusy = $state<"" | "start" | "stop">("");
  let ciLogs = $state<string | null>(null);
  async function ciLoad(fill = !ci) {
    try {
      ci = await api.ciRunnerGet();
      if (fill) {
        const c = ci.config;
        ciForm = {
          mode: c.config_toml ? "import" : "form", url: c.url ?? "", name: c.name ?? "", image: c.image ?? "", token: "",
          toml: c.config_toml ?? "", hostsText: (c.extra_hosts ?? []).join("\n"), directText: (c.direct ?? []).join("\n"), autoStart: !!c.auto_start, stopOnLock: !c.keep_on_lock,
        };
      }
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  async function ciSave(quiet = false): Promise<boolean> {
    const f = ciForm;
    const lines = (t: string) => t.split(/[\n,]+/).map((x) => x.trim()).filter(Boolean);
    try {
      await api.ciRunnerSet({
        url: f.mode === "form" ? f.url.trim() : "", name: f.name.trim(), image: f.image.trim(),
        config_toml: f.mode === "import" ? f.toml : "",
        extra_hosts: lines(f.hostsText), direct: lines(f.directText), auto_start: f.autoStart, keep_on_lock: !f.stopOnLock,
      }, f.mode === "form" ? f.token.trim() : "");
      await ciLoad(true);
      if (!quiet) toast.ok("CI runner saved");
      return true;
    } catch (e: any) { toast.err(errMsg(e)); return false; }
  }
  async function ciStart() {
    if (!(await ciSave(true))) return;
    ciBusy = "start";
    try {
      const st = await api.ciRunnerStart();
      if (ci) ci.status = st;
      toast.ok("CI runner started");
    } catch (e: any) { toast.err(errMsg(e)); await ciLoad(false); } finally { ciBusy = ""; }
  }
  async function ciStop() {
    ciBusy = "stop";
    try {
      const st = await api.ciRunnerStop();
      if (ci) ci.status = st;
      toast.ok("CI runner stopped");
    } catch (e: any) { toast.err(errMsg(e)); } finally { ciBusy = ""; }
  }
  async function ciShowLogs() {
    try { ciLogs = await api.ciRunnerLogs(); } catch (e: any) { toast.err(errMsg(e)); }
  }
  async function ciPickFile(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0];
    if (!file) return;
    ciForm.toml = await file.text();
    ciForm.mode = "import";
  }
  function ciSince(unix?: number): string {
    if (!unix) return "";
    const m = Math.max(0, Math.round((Date.now() / 1000 - unix) / 60));
    return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`;
  }
  // Local proxy panel per profile (form state while open).
  type LpForm = { enabled: boolean; shared: boolean; bind: string; port: number; networksText: string; details?: import("./api").LocalProxyDetails };
  let lpOpen = $state<Record<string, LpForm>>({});
  // The stored address, port and networks per profile: the form is dirty
  // while they differ. The two switches save at once and are not part of it.
  let lpBase = $state<Record<string, string>>({});
  function lpSnap(f: LpForm): string {
    return JSON.stringify([f.bind.trim(), Number(f.port), f.networksText.split(/[\s,]+/).filter(Boolean)]);
  }
  function lpDirty(id: string): boolean {
    const f = lpOpen[id];
    return !!f && lpBase[id] !== undefined && lpSnap(f) !== lpBase[id];
  }
  async function lpLoad(np: NetworkProfileInfo) {
    // Back on the Proxy tab with edits still open: keep them.
    if (lpDirty(np.id)) return;
    const c = np.local_proxy ?? { enabled: false, port: 0 };
    const f: LpForm = { enabled: c.enabled, shared: !!c.shared, bind: c.bind || "127.0.0.1", port: c.port || 1080, networksText: (c.networks ?? []).join("\n") };
    const open = lpOpen[np.id];
    const before = open ? lpSnap(open) : null;
    try { f.details = await api.networkProfileLocalProxyDetails(np.id); } catch { /* shown on save */ }
    // Typed into the open form while the details loaded: keep that. Compared
    // with the form as this load found it, since after a Discard there is no
    // baseline to compare with.
    const now = lpOpen[np.id];
    if (now && before !== null && lpSnap(now) !== before) {
      // After a Discard the baseline is gone: the stored values are it, so
      // what was typed shows as unsaved.
      if (lpBase[np.id] === undefined) lpBase = { ...lpBase, [np.id]: lpSnap(f) };
      return;
    }
    lpOpen = { ...lpOpen, [np.id]: f };
    lpBase = { ...lpBase, [np.id]: lpSnap(f) };
  }
  // Switch clicks and Saves go out one at a time, each built from the
  // configuration stored when its turn comes: two quick clicks no longer
  // send the same stale config with one change each.
  let lpChain: Promise<unknown> = Promise.resolve();
  // Requests queued or running: a profile switch waits for them (busy).
  let lpPending = 0;
  function lpQueue<T>(job: () => Promise<T>): Promise<T> {
    lpPending++;
    const run = lpChain.then(job, job).finally(() => { lpPending--; });
    lpChain = run.catch(() => {});
    return run;
  }
  function lpDiscard(np: NetworkProfileInfo) {
    const b = { ...lpBase };
    delete b[np.id];
    lpBase = b;
    void lpLoad(np);
  }
  // A switch: saved on the click, with the stored address, port and
  // networks, so half-typed text fields are not saved along with it.
  function lpToggle(np: NetworkProfileInfo, key: "shared" | "enabled", on: boolean) {
    return lpQueue(() => lpToggleNow(np, key, on));
  }
  async function lpToggleNow(np: NetworkProfileInfo, key: "shared" | "enabled", on: boolean) {
    const f = lpOpen[np.id];
    if (!f) return;
    const c = networkProfiles.list.find((p) => p.id === np.id)?.local_proxy ?? { enabled: false, port: 0 };
    try {
      await api.networkProfileSetLocalProxy(np.id, {
        enabled: c.enabled, shared: !!c.shared, bind: c.bind || "127.0.0.1", port: c.port || 1080,
        networks: c.networks ?? [], [key]: on,
      });
      f[key] = on;
      await networkProfiles.load(true);
      f.details = await api.networkProfileLocalProxyDetails(np.id);
      lpOpen = { ...lpOpen, [np.id]: f };
      toast.ok(key === "shared" ? (on ? `${np.name} reachable through the app proxy` : `${np.name} left out of the app proxy`) : (on ? `Separate proxy for ${np.name} on` : `Separate proxy for ${np.name} off`));
      spLoad();
    } catch (e: any) { toast.err(errMsg(e)); lpOpen = { ...lpOpen }; }
  }
  function lpSave(np: NetworkProfileInfo): Promise<boolean> {
    return lpQueue(() => lpSaveNow(np));
  }
  async function lpSaveNow(np: NetworkProfileInfo): Promise<boolean> {
    const f = lpOpen[np.id];
    if (!f) return true;
    // The baseline becomes what this request sends; text typed while it
    // runs stays dirty.
    const sent = lpSnap(f);
    try {
      await api.networkProfileSetLocalProxy(np.id, {
        enabled: f.enabled, shared: f.shared, bind: f.bind.trim(), port: Number(f.port),
        networks: f.networksText.split(/[\s,]+/).filter(Boolean),
      });
      await networkProfiles.load(true);
      f.details = await api.networkProfileLocalProxyDetails(np.id);
      lpOpen = { ...lpOpen, [np.id]: f };
      lpBase = { ...lpBase, [np.id]: sent };
      toast.ok("Proxy settings saved");
      spLoad();
      // Typed again while it saved: still unsaved, so a leave keeps asking.
      return !lpDirty(np.id);
    } catch (e: any) { toast.err(errMsg(e)); return false; }
  }
  async function lpCopy(np: NetworkProfileInfo, what: "url" | "gateway" | "routes") {
    try {
      const d = await api.networkProfileLocalProxyDetails(np.id);
      const text = what === "url" ? d.url : what === "gateway" ? d.gateway : d.routes.join(",");
      copyText(text, { label: what === "url" ? "Proxy URL" : what === "gateway" ? "Gateway command" : "Routes" });
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  // Tests run per profile: one slow VPN login must not grey out the
  // other profiles' buttons.
  let npTesting = $state<Record<string, boolean>>({});
  async function npTest(np: NetworkProfileInfo) {
    npTesting = { ...npTesting, [np.id]: true };
    try {
      const st = await api.networkProfileTest(np.id);
      toast.ok(st.running ? `Tunnel ${np.name} is up` : `Tunnel ${np.name} did not start`);
      await networkProfiles.load(true);
    } catch (e: any) { toast.err(errMsg(e)); }
    finally {
      const n = { ...npTesting };
      delete n[np.id];
      npTesting = n;
    }
  }

  // Page layout: a list (General, the profiles, New profile) beside the
  // selected item; a profile opens on tabs.
  type NpTab = "overview" | "settings" | "proxy" | "details";
  const NP_TABS: [NpTab, string][] = [["overview", "Overview"], ["settings", "Settings"], ["proxy", "Proxy"], ["details", "Details"]];
  let npSel = $state<string>("general");
  let npTab = $state<NpTab>("overview");
  const npCur = $derived(networkProfiles.list.find((p) => p.id === npSel) ?? null);
  const npUpCount = $derived(networkProfiles.list.filter((p) => p.status.running && !p.paused).length);
  let npUsage = $state<Record<string, { connections: number; web: number }>>({});
  let spShowRoutes = $state(false);
  function npLoadUsage() {
    api.networkProfilesUsage().then((u) => (npUsage = u ?? {})).catch(() => {});
  }
  // From a "plugin not installed" note: the plugin list, with that
  // plugin's card scrolled to and flashed.
  function showPlugin(name: string) {
    npSelect("general");
    void flashSetting(`plugin-${name}`);
  }
  // The New profile form against the empty form it opened as: any field
  // typed counts, the device / host names the app suggested on its own do
  // not (they count once edited).
  let npSuggested = $state({ nb: "", ts: "" });
  let npEmptySnap = $state("");
  const npNewDirty = $derived(npSel === "new" && npEditingId === null && npEmptySnap !== "" &&
    npSnap({ nb: nbDevice === npSuggested.nb ? "" : nbDevice, ts: tsHostname === npSuggested.ts ? "" : tsHostname }) !== npEmptySnap);
  // What each kind's Add profile button needs. The prompt's Save goes
  // through the same gate: an incomplete profile is not created (an inline
  // secret would otherwise be stored in the vault before the backend
  // rejected the profile).
  function npNewReady(): boolean {
    if (npBusy || !npName.trim()) return false;
    switch (npKind) {
      case "netbird": return nbInstalled && !!nbCredId;
      case "openconnect": return ocInstalled && !!oc.server.trim();
      case "openvpn": return ovInstalled && !!ovText.trim();
      case "checkpoint": return cpInstalled && !!cp.server.trim();
      case "tailscale": return tsInstalled && !!tsCredId;
      default: return !!npConf.trim();
    }
  }
  async function npCreateCurrent(): Promise<boolean> {
    if (npAnyBusy) return false;
    if (!npNewReady()) {
      toast.err("The new profile is not complete yet: finish it, or choose Discard.");
      return false;
    }
    const create = { netbird: npCreateNetbird, openconnect: npCreateOpenConnect, openvpn: npCreateOpenVPN, checkpoint: npCreateCheckpoint, tailscale: npCreateTailscale }[npKind as string] ?? npCreate;
    await create();
    return !npNewDirty;
  }
  $effect(() => registerUnsaved({
    label: () => npSel === "new" ? "The new profile" : networkProfiles.list.find((p) => p.id === npSel)?.name ?? "",
    dirty: () => npDirty || npNewDirty || lpDirty(npSel),
    busy: () => npAnyBusy || lpPending > 0,
    save: async () => {
      if (npNewDirty) return npCreateCurrent();
      const np = networkProfiles.list.find((p) => p.id === npSel);
      if (np && lpDirty(np.id) && !(await lpSave(np))) return false;
      return npDirty ? npSaveCurrent() : true;
    },
    discard: () => {
      const np = networkProfiles.list.find((p) => p.id === npSel);
      if (np && lpDirty(np.id)) lpDiscard(np);
      if (npDirty) npRevert();
      if (npNewDirty) npCancelEdit();
    },
    scope: "settings",
  }));
  // True once the item is open: already was, or the switch went ahead.
  async function npSelect(id: string): Promise<boolean> {
    // Reselecting the open item changes nothing (and keeps its draft).
    if (id === npSel) return true;
    // A save or create in flight reads the form when its requests return,
    // and an inline setup / auth key being created writes its id into it:
    // the form must not be cleared or switched under either.
    if (npAnyBusy) {
      toast.info("Wait for the profile to finish saving");
      return false;
    }
    if (!(await confirmLeave(["settings"]))) return false;
    npSel = id;
    npCancelEdit();
    if (id === "new") {
      npSuggested = { nb: "", ts: "" };
      npEmptySnap = npSnap();
    }
    const np = networkProfiles.list.find((p) => p.id === id);
    if (np) npOpenTab(npTab, np);
    return true;
  }
  // Deep link to one profile. Waits for the profile list, which may still
  // be loading when Settings mounts.
  $effect(() => {
    const id = view.pendingNetworkProfileId;
    if (!id) return;
    // A save in flight: the link stays pending and this effect runs again
    // when the flags clear.
    if (npAnyBusy) return;
    if (id === "ci") {
      view.pendingNetworkProfileId = null;
      npSelect("ci");
      void ciLoad(true);
      return;
    }
    const np = networkProfiles.list.find((p) => p.id === id);
    if (!np) return;
    view.pendingNetworkProfileId = null;
    // Through npSelect, so unsaved edits on the open profile ask first.
    void npSelect(id).then((ok) => { if (ok) npOpenTab("settings", np); });
  });
  function npOpenTab(t: NpTab, np: NetworkProfileInfo | null = npCur) {
    npTab = t;
    if (!np) return;
    if (t === "settings" && npEditingId !== np.id) npStartEdit(np);
    if (t === "proxy") lpLoad(np);
    if ((t === "details" || t === "overview") && np.status.running && pluginReportsInfo(np.kind)) npRefreshInfo(np);
  }
  // After a create or save: the list has reloaded; open the profile (a
  // new one on its Overview, a saved one stays on Settings).
  function npDone() {
    const id = npEditingId;
    const name = npName.trim();
    npCancelEdit();
    const np = id
      ? networkProfiles.list.find((p) => p.id === id)
      : networkProfiles.list.filter((p) => p.name === name).sort((a, b) => b.created_at - a.created_at)[0];
    npLoadUsage();
    if (!np) return;
    npSel = np.id;
    npOpenTab(id ? "settings" : "overview", np);
  }
  // Revert the Settings form to the stored profile.
  function npRevert() {
    if (npAnyBusy) return;
    const np = npCur;
    npCancelEdit();
    if (np) npStartEdit(np);
  }
  async function npRefreshInfo(np: NetworkProfileInfo) {
    try {
      npInfoOpen = { ...npInfoOpen, [np.id]: await api.networkProfileTunnelInfo(np.id) };
    } catch (e: any) { toast.err(errMsg(e)); }
  }
  // Kinds whose plugin describes the live tunnel itself (Details).
  function pluginReportsInfo(kind: string): boolean {
    return kind === "openconnect" || kind === "openvpn" || kind === "checkpoint";
  }
  function npKindLine(np: NetworkProfileInfo): string {
    if (np.kind === "openconnect") return `OpenConnect - ${ocFlavorTitle[np.openconnect?.flavor ?? "anyconnect"]}`;
    return kindTitle(np.kind);
  }
  function npServerLine(np: NetworkProfileInfo): string {
    switch (np.kind) {
      case "netbird": return [kindTitle(np.kind), np.netbird?.device_name || "ssh-tool", np.netbird?.management_url].filter(Boolean).join(" - ");
      case "tailscale": return [kindTitle(np.kind), np.tailscale?.hostname || "ssh-tool", np.tailscale?.control_url].filter(Boolean).join(" - ");
      case "openvpn": return [kindTitle(np.kind), np.openvpn?.server, np.openvpn?.static_key].filter(Boolean).join(" - ");
      case "openconnect": {
        const n = np.openconnect?.backups?.length ?? 0;
        return [npKindLine(np), np.openconnect?.server, n && `+${n} ${n === 1 ? "backup" : "backups"}`].filter(Boolean).join(" - ");
      }
      case "checkpoint": return [kindTitle(np.kind), np.checkpoint?.server, np.checkpoint?.login_option].filter(Boolean).join(" - ");
      default: return [kindTitle(np.kind), np.profile.peers?.[0]?.endpoint].filter(Boolean).join(" - ");
    }
  }
  function npUsageText(id: string): string {
    const u = npUsage[id];
    if (!u || (!u.connections && !u.web)) return "nothing yet - set it as the Network of a folder or connection";
    const parts = [];
    if (u.connections) parts.push(`${u.connections} ${u.connections === 1 ? "connection" : "connections"}`);
    if (u.web) parts.push(`${u.web} web ${u.web === 1 ? "bookmark" : "bookmarks"}`);
    return parts.join(", ");
  }
  function npSessionText(np: NetworkProfileInfo): string {
    const ti = npInfoOpen[np.id];
    if (!np.status.running || !ti?.auth_expires) return "";
    return `ends ${new Date(ti.auth_expires * 1000).toLocaleString()} (${tunnelInRel(ti.auth_expires)}, the gateway's limit)`;
  }
  function npPresenceText(np: NetworkProfileInfo): string {
    if (np.paused) return "Paused: connections dial directly.";
    const ro = npRemoteOwners[np.id];
    if (ro) return `Up on ${ro.machine_name}.`;
    if (np.status.running) return "Up on this machine. It stops by itself after the idle time without sessions.";
    return "Down. Comes up with the first connection through it.";
  }
  // WireGuard / NetBird / Tailscale details (OpenConnect, OpenVPN and
  // Check Point ask the plugin for theirs).
  function npStatusText(np: NetworkProfileInfo): string {
    if (!np.status.running) return "The tunnel is down.";
    const st = np.status;
    const lines = [`type:       ${kindTitle(np.kind)}`];
    if (np.kind === "wireguard") {
      lines.push(`address:    ${np.profile.addresses?.join(", ") || "-"}`);
      if (np.profile.peers?.length) lines.push(`peer:       ${np.profile.peers[0].endpoint}`);
      if (st.last_handshake > 0) lines.push(`handshake:  ${new Date(st.last_handshake * 1000).toLocaleTimeString()}`);
    } else if ((st.peers ?? 0) > 0) {
      lines.push(`peers:      ${st.peers}`);
    }
    if (st.rx_bytes > 0 || st.tx_bytes > 0) lines.push(`rx / tx:    ${fmtBytes(st.rx_bytes)} / ${fmtBytes(st.tx_bytes)}`);
    return lines.join("\n");
  }
  function fmtBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KiB`;
    return `${(n / 1024 / 1024).toFixed(1)} MiB`;
  }
  // The proxy password stays off the screen; Copy puts the full address
  // on the clipboard.
  function maskProxyURL(u: string): string {
    return u.replace(/:([^:@/]+)@/, ":******@");
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

  // Unlocking is the same flow everywhere: re-show VaultGate, which owns the
  // passphrase entry, the auto-unlock probe and the mobile biometric path.
  function onUnlockNow() {
    window.dispatchEvent(new CustomEvent("vault-unlock-now"));
  }

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
      vaultState.markLocked();
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

  // ----- Audit insights -----
  //
  // Aggregates come from the backend over the whole audit.db for the
  // chosen window. They are deliberately NOT derived from
  // auditEvents: that array is one capped page (auditLimit), so
  // summing it would report "the last 200 events" under a "last 30
  // days" heading.
  let statsWindow = $state(30); // days; 0 = all time
  let stats = $state<import("./api").AuditStats | null>(null);
  let statsBusy = $state(false);
  let statsError = $state<string | null>(null);
  let statsOpen = $state(true);

  const STAT_WINDOWS: { label: string; days: number }[] = [
    { label: "7d", days: 7 },
    { label: "30d", days: 30 },
    { label: "90d", days: 90 },
    { label: "All", days: 0 },
  ];

  async function loadStats() {
    statsBusy = true;
    statsError = null;
    try {
      stats = await api.auditStats(statsWindow);
    } catch (e: any) {
      statsError = errMsg(e);
    } finally {
      statsBusy = false;
    }
  }

  function setStatsWindow(days: number) {
    if (statsWindow === days) return;
    statsWindow = days;
    loadStats();
  }

  // Compact duration: the panel shows totals that range from seconds
  // to hundreds of hours, so a fixed unit would be unreadable at one
  // end or the other.
  function fmtDur(secs: number): string {
    if (!secs || secs < 0) return "0m";
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (h >= 24) {
      const d = Math.floor(h / 24);
      return `${d}d ${h % 24}h`;
    }
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m`;
    return `${secs}s`;
  }

  function fmtDay(key: string): string {
    const [, mo, da] = key.split("-");
    return `${da}.${mo}.`;
  }

  // Average is over PAIRED sessions only. Dividing the total by every
  // connect would count a session that is still open (no disconnect
  // yet, so no duration) as zero minutes and drag the mean down.
  const avgSessionSecs = $derived.by(() => {
    if (!stats) return 0;
    const paired = stats.connects - stats.unpaired;
    if (paired <= 0) return 0;
    return Math.round(stats.sessionSecs / paired);
  });

  const topHosts = $derived(stats ? stats.hosts.slice(0, 8) : []);
  const topActions = $derived(stats ? stats.actions.slice(0, 10) : []);
  const maxHostSecs = $derived(
    topHosts.reduce((m, h) => Math.max(m, h.seconds), 0) || 1,
  );
  const maxActionCount = $derived(
    topActions.reduce((m, a) => Math.max(m, a.count), 0) || 1,
  );
  const failureTotal = $derived(
    stats ? stats.failures.reduce((n, f) => n + f.count, 0) : 0,
  );

  // How much of what the LLM did ran without a human in the loop.
  // "yolo" is the session opted out of per-command prompts; "auto" is
  // an allowlisted read-only tool. Both are unattended, but only yolo
  // is a choice the user made, so they stay separate.
  const GATE_LABEL: Record<string, string> = {
    denied: "denied",
    yolo: "unattended (yolo)",
    auto: "auto (read-only)",
    approved: "approved by you",
    unknown: "unrecorded",
  };
  const gateTotal = $derived(
    stats ? stats.gates.reduce((n, g) => n + g.count, 0) : 0,
  );
  function gatePct(n: number): number {
    return gateTotal > 0 ? (n / gateTotal) * 100 : 0;
  }

  // Trailing slice of the daily series, zero-filled so gaps read as
  // quiet days rather than being collapsed out of the chart.
  const dailySeries = $derived.by(() => {
    if (!stats) return [] as { key: string; count: number }[];
    const span = statsWindow > 0 ? Math.min(statsWindow, 90) : 30;
    const have = new Map(stats.daily.map((d) => [d.key, d.count]));
    const out: { key: string; count: number }[] = [];
    const today = new Date();
    for (let i = span - 1; i >= 0; i--) {
      const d = new Date(today);
      d.setDate(today.getDate() - i);
      const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
      out.push({ key, count: have.get(key) ?? 0 });
    }
    return out;
  });
  const maxDaily = $derived(
    dailySeries.reduce((m, d) => Math.max(m, d.count), 0) || 1,
  );
  const maxHourly = $derived(
    stats ? stats.hourly.reduce((m, h) => Math.max(m, h), 0) || 1 : 1,
  );

  // "No events in this window, but the log is not empty" is a
  // different message from "nothing has ever happened", and only the
  // first has an obvious fix (widen the window).
  const emptyWindow = $derived(!!stats && stats.total === 0);

  $effect(() => {
    if (activeSection === "audit") loadAudit();
  });

  $effect(() => {
    if (activeSection === "audit" && stats === null && !statsBusy) loadStats();
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
      // Untracked: it reads the form to keep drafts, and the effect must
      // not rerun on every keystroke.
      untrack(() => void syncLoadConfig());
      void syncLoadCredOptions();
    }
  });

  // Sections that are desktop-only and hidden on mobile (the backend
  // features they configure are excluded on android).
  // "desktop" holds the install offer, the file-manager context menu and
  // the URL-scheme handler - all desktop-only, so on Android the section
  // would render a heading and nothing else.
  const MOBILE_HIDDEN_SECTIONS = new Set(["browser", "llm", "desktop", "shells"]);

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
      const resolved = resolveSection(v);
      // A deep link that landed while this read was in flight wins;
      // restoring over it reopened the previous section.
      if (resolved && !pinApplied) activeSection = resolved;
    }).catch(() => {});
  });
  let pinApplied = false;

  // Deep-link from elsewhere in the UI (e.g. the status-bar version
  // pill → About). When a section is staged in view.pendingSettingsSection
  // we honour it and clear the pin so the next plain "open Settings"
  // resumes the user's last section.
  //
  // A pin may name a setting too: "llm#mcp-manage" opens the section and
  // highlights the element carrying data-setting="mcp-manage". Several
  // ids separated by "|" are tried in order, for a setting that only
  // renders once another one is on ("llm#mcp-manage|mcp-enable").
  $effect(() => {
    const pin = view.pendingSettingsSection;
    if (!pin) return;
    const [sec, anchor] = pin.split("#");
    const resolved = resolveSection(sec);
    if (resolved) {
      pinApplied = true;
      activeSection = resolved;
      // Persist the concrete section id (keepass/bitwarden/infisical
      // resolve to "external"); restore re-derives the tab from it.
      api.settingsSet("settings_active_section", sec).catch(console.warn);
      if (anchor) flashSetting(anchor);
    }
    view.pendingSettingsSection = null;
  });

  function pickSection(s: SectionId, anchor?: string) {
    activeSection = s;
    api.settingsSet("settings_active_section", s).catch(console.warn);
    if (s === "snippets") reloadSnippets();
    if (s === "workspaces") reloadWorkspaces();
    if (anchor) flashSetting(anchor);
  }

  // Scroll to a setting and outline it for a moment, so a deep link lands
  // on the thing it is about instead of the top of a long section. Waits
  // for the section to render; a section that loads its state first can
  // take a few frames, hence the short retry.
  async function flashSetting(spec: string, tries = 10) {
    await tick();
    for (const id of spec.split("|")) {
      const el = document.querySelector<HTMLElement>(`[data-setting="${id}"]`);
      if (!el) continue;
      el.scrollIntoView({ block: "center", behavior: "smooth" });
      el.classList.remove("setting-flash");
      void el.offsetWidth; // restart the animation on a repeat visit
      el.classList.add("setting-flash");
      setTimeout(() => el.classList.remove("setting-flash"), 2600);
      return;
    }
    if (tries > 0) setTimeout(() => void flashSetting(spec, tries - 1), 100);
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
      Applies to the app itself. Terminal colours are set separately
      under <strong>Terminal</strong> and don't follow this. Pick
      High contrast if you can't read subtle labels in direct sunlight.
    </p>
    <fieldset class="modes">
      {#each [
        { id: "system", name: "Follow system",        desc: "Match the OS dark / light setting, switching as it changes." },
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

      <label class:active={terminalPrefs.serverStatsEnabled}>
        <input
          type="checkbox"
          checked={terminalPrefs.serverStatsEnabled}
          onchange={(e) => terminalPrefs.setServerStatsEnabled((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Show server status for the focused session</div>
          <div class="mode-desc">
            Load, memory, disk and users in the status bar, every 10s. Runs
            a read-only probe on the host.
          </div>
        </div>
      </label>

      <label class:active={terminalPrefs.serverStatsEnabled && terminalPrefs.failedUnitsChip}>
        <input
          type="checkbox"
          disabled={!terminalPrefs.serverStatsEnabled}
          checked={terminalPrefs.failedUnitsChip}
          onchange={(e) => terminalPrefs.setFailedUnitsChip((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Warn about failed systemd units</div>
          <div class="mode-desc">
            Red "N failed" next to the server status. Single units can be
            ignored per host in System status -> Services.
          </div>
        </div>
      </label>
    </fieldset>
  </div>
  {/if}

  {#if activeSection === "tree"}
  <div class="group">
    <h2>Connection tree</h2>
    <p class="hint">How rows in the connection tree look. Density and font size are under Appearance.</p>

    <h3>Jump host marks</h3>
    <fieldset class="modes">
      {#each [
        { id: "off", name: "Off", desc: "No marks." },
        { id: "bastions", name: "Bastions only", desc: "Marks hosts other connections jump through, with how many." },
        { id: "routed", name: "Bastions and routed connections", desc: "Also marks connections with a jump chain of their own. Chains inherited from a folder are left unmarked." },
      ] as m (m.id)}
        <label class:active={appPrefs.jumpMarks === m.id}>
          <input
            type="radio"
            name="jump-marks"
            checked={appPrefs.jumpMarks === m.id}
            onchange={() => appPrefs.setJumpMarks(m.id as any)}
          />
          <div>
            <div class="mode-name">{m.name}</div>
            <div class="mode-desc">{m.desc}</div>
          </div>
        </label>
      {/each}
    </fieldset>

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

    </fieldset>
  </div>
  {/if}

  {#if activeSection === "connection"}
  <div class="group">
    <h2>Connection</h2>
    <p class="hint">
      The connect timeout applies to the TCP dial and SSH handshake on every hop. The default
      (20s) is generous for most networks; raise it for slow or
      unreliable links, lower it if you want to fail fast.
    </p>
    <div class="pref-grid">
      <label for="pref-connect-timeout">Connect timeout</label>
      <input
        id="pref-connect-timeout"
        type="number"
        min="1"
        max="300"
        step="1"
        bind:value={connectTimeoutSeconds}
        onblur={saveConnectTimeout}
        onkeydown={(e) => { if (e.key === "Enter") saveConnectTimeout(); }}
      />
      <span class="unit">seconds, default 20{#if connectTimeoutSaved} <span class="saved-mark">saved</span>{/if}</span>
      <span></span>
    </div>

    {#snippet prefRow(p: IntPref, label: string, unit: string)}
      <label for={"pref-" + p.key}>{label}</label>
      <input
        id={"pref-" + p.key}
        type="number"
        min={p.min}
        max={p.max}
        step="1"
        value={connectPrefs.get(p)}
        onchange={(e) => { void connectPrefs.set(p, Number((e.target as HTMLInputElement).value)); }}
      />
      <span class="unit">{unit}</span>
      {#if connectPrefs.get(p) !== p.def}
        <button class="link-btn" onclick={() => connectPrefs.set(p, p.def)} title="Reset to default ({p.def})">Reset</button>
      {:else}<span></span>{/if}
    {/snippet}

    <h3>Auto-reconnect</h3>
    <p class="hint inline">
      When a session with auto-reconnect drops. The wait doubles from 1s
      per attempt, up to the cap.
    </p>
    <div class="pref-grid">
      {@render prefRow(RECONNECT_ATTEMPTS, "Attempts", `${RECONNECT_ATTEMPTS.min}-${RECONNECT_ATTEMPTS.max}, default ${RECONNECT_ATTEMPTS.def}`)}
      {@render prefRow(RECONNECT_MAX_DELAY, "Longest wait between attempts", `seconds, default ${RECONNECT_MAX_DELAY.def}`)}
    </div>

    <h3>Connect all</h3>
    <p class="hint inline">
      Opening many hosts at once (a folder, a multi-selection). Every
      handshake still in progress counts against the bastion's sshd
      MaxStartups (default 10): past that it drops new ones, seen as
      "handshake failed: EOF". Hence at most 10 at once and never less
      than 50 ms between starts.
    </p>
    <div class="pref-grid">
      {@render prefRow(BULK_CONCURRENT, "Handshakes at once", `${BULK_CONCURRENT.min}-${BULK_CONCURRENT.max}, default ${BULK_CONCURRENT.def}`)}
      {@render prefRow(BULK_STAGGER_MS, "Pause between starts", `ms, min ${BULK_STAGGER_MS.min}, default ${BULK_STAGGER_MS.def}`)}
      {@render prefRow(BULK_CONFIRM_ABOVE, "Ask before connecting more than", `hosts, default ${BULK_CONFIRM_ABOVE.def}`)}
    </div>

    <h3>Shared bastion</h3>
    <p class="hint inline">
      Sessions behind the same jump host share one connection to it. This
      is how long it stays open after the last of them closes - longer
      means a quick reconnect skips the bastion handshake. 0 closes it at
      once.
    </p>
    <div class="pref-grid">
      {@render prefRow(BASTION_LINGER, "Keep open after last session", `seconds, default ${BASTION_LINGER.def}`)}
    </div>

  </div>
  {/if}

  {#if activeSection === "shells"}
  <div class="group">
    {#if !isMobile}
    <h2>In-app local shell</h2>
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

    <label style="margin-top: 1rem;">
      Start in directory
      <input
        value={localShellDir}
        oninput={(e) => saveLocalShellDir((e.target as HTMLInputElement).value)}
        placeholder="Your home directory"
      />
    </label>
    <p class="hint">
      Where a local shell opens. Empty means your home directory. A
      saved local connection can override this with its own.
      <strong>WSL</strong> takes a Linux path
      (<code>/srv/app</code>); a Windows path works too and is mapped to
      <code>/mnt/...</code> the way WSL always does.
    </p>

    <p class="hint" style="margin-top: 1.5rem;">
      The right-click <strong>Open in ssh-tool</strong> menu moved to
      <strong>Desktop integration</strong>, with the rest of the
      desktop hooks.
    </p>

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
    {/if}
  </div>
  {/if}

  {#if activeSection === "window"}
  <div class="group">
    {#if !isMobile}
    <h2>Window</h2>
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
    {/if}

    <h2 style="margin-top: 1.5rem;">Startup</h2>
    <p class="hint">
      Reopen the tabs that were open when the app last quit - SSH
      connections, dynamic-inventory hosts and local shells, with
      titles, tab groups and split panes.
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

    <h3 style="margin-top:1.2rem">Channel</h3>
    <fieldset class="modes">
      {#each [
        { id: "stable", name: "Stable", desc: "Releases only. The default." },
        { id: "rc", name: "Release candidates", desc: "Also the test builds of the next release (vX.Y.Z-rcN), with their changelog, before everyone else gets them. You move on to the release when it ships. There is no way back to an older version: leaving this channel keeps the candidate until the next release overtakes it." },
      ] as c (c.id)}
        <label class:active={updateChannel === c.id}>
          <input
            type="radio"
            name="updateChannel"
            checked={updateChannel === c.id}
            disabled={updateCheckDisabled}
            onchange={() => setUpdateChannel(c.id as "stable" | "rc")}
          />
          <div>
            <div class="mode-name">{c.name}</div>
            <div class="mode-desc">{c.desc}</div>
          </div>
        </label>
      {/each}
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

    <fieldset class="check-cards">
      <label class:active={terminalPrefs.disableWebgl}>
        <input
          type="checkbox"
          checked={terminalPrefs.disableWebgl}
          onchange={(e) => terminalPrefs.setDisableWebgl((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Disable WebGL renderer (use canvas fallback)</div>
          <div class="mode-desc">
            On by default: WebGL garbles text on some GPUs. Applies to
            newly opened tabs.
          </div>
        </div>
      </label>
    </fieldset>

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

    <label class="num scrollback-row">
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
      Lines kept per tab. Default {DEFAULT_SCROLLBACK}. After a detach,
      redock or reload only the last ~8000 lines come back.
    </p>

    <label class="num scrollback-row">
      <span>Release background scrollback (s)</span>
      <input
        type="number"
        min="-1"
        max="600"
        step="5"
        value={terminalPrefs.bgScrollbackDelay}
        onblur={(e) => terminalPrefs.setBgScrollbackDelay(parseInt((e.target as HTMLInputElement).value, 10))}
        onkeydown={(e) => {
          if (e.key === "Enter") terminalPrefs.setBgScrollbackDelay(parseInt((e.currentTarget as HTMLInputElement).value, 10));
        }}
      />
    </label>
    <p class="hint inline">
      Frees a hidden tab's scrollback after this many seconds, to save
      memory; it comes back when you return. Default
      {DEFAULT_BG_SCROLLBACK_DELAY}s, 0 = at once, -1 = off.
    </p>

    <fieldset class="check-cards">
      <label class:active={terminalPrefs.closeOnCleanExit}>
        <input
          type="checkbox"
          checked={terminalPrefs.closeOnCleanExit}
          onchange={(e) => terminalPrefs.setCloseOnCleanExit((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Auto-close tab on clean exit</div>
          <div class="mode-desc">
            On <code>exit 0</code> or Ctrl+D. Errors and dropped connections
            stay open.
          </div>
        </div>
      </label>

      <label class:active={terminalPrefs.commandTimestamps}>
        <input
          type="checkbox"
          checked={terminalPrefs.commandTimestamps}
          onchange={(e) => terminalPrefs.setCommandTimestamps((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Show when each command was run</div>
          <div class="mode-desc">
            Time at the right edge of each command line. Always recorded,
            so it also shows commands run before you turned this on.
          </div>
        </div>
      </label>

      <label class:active={terminalPrefs.commandCopy}>
        <input
          type="checkbox"
          checked={terminalPrefs.commandCopy}
          onchange={(e) => terminalPrefs.setCommandCopy((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Copy button on command output</div>
          <div class="mode-desc">
            Point at the right side of a command's output and a Copy button
            appears for the command and everything it printed. <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>
            selects a command and its output; copy it with your usual copy
            key (<kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>C</kbd> works in every
            copy mode). Works with this option off too.
          </div>
        </div>
      </label>
    </fieldset>
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

    <fieldset class="check-cards">
      <label class:active={recordingConfirm}>
        <input
          type="checkbox"
          checked={recordingConfirm}
          onchange={toggleRecordingConfirm}
        />
        <div>
          <div class="mode-name">Ask before starting a recording</div>
          <div class="mode-desc">
            A confirmation step so a misclick doesn't quietly start writing the
            session to disk. Turn it off if you record routinely.
          </div>
        </div>
      </label>
    </fieldset>
  </div>

  {:else if activeSection === "network"}
  {#snippet missingPlugin(title: string, name: string)}
    <p class="hint warn-note">
      The {title} plugin is not installed.
      <button type="button" class="link-btn" onclick={() => showPlugin(name)}>Install it</button>
      first (Plugins &amp; proxy, at the top of the list), then create the profile.
    </p>
  {/snippet}

  {#snippet ciPage()}
    {@const st = ci?.status}
    <div class="np-h3 np-sp-head">
      <h3>CI runner</h3>
      {#if st}
        <span class="hint inline" class:warn={!!st.last_err && !st.running}>
          {st.busy ? "working..." : st.running ? `running${st.started_at ? " " + ciSince(st.started_at) : ""}` : st.gateway || st.runner ? `gateway ${st.gateway || "-"}, runner ${st.runner || "-"}` : "stopped"}
        </span>
      {/if}
      <span class="np-spacer"></span>
      {#if st?.running}
        <button onclick={ciStop} disabled={!!ciBusy}><IconStop size={13} /> Stop</button>
      {:else}
        <button onclick={ciStart} disabled={!!ciBusy || !st?.docker}><IconPlay size={13} /> {ciBusy === "start" ? "Starting..." : ciBusy === "stop" ? "Stopping..." : "Start"}</button>
      {/if}
      <button onclick={ciShowLogs} disabled={!st?.runner}>Logs</button>
    </div>
    <p class="hint">
      A GitLab runner in Docker whose jobs reach your customers through the
      VPNs marked <span class="np-badge">via proxy</span>, several at once:
      each address goes through the VPN whose networks contain it (App proxy,
      under Plugins &amp; proxy). The runner's own traffic, the clone and the
      cache stay on your network. Stops when ssh-tool quits.
    </p>
    {#if st}
      {#if !st.docker}
        <p class="hint warn-note" title={st.docker_err || ""}>
          Docker is not running, or not installed. Start Docker Desktop (Windows,
          macOS) or Docker Engine (Linux), then
          <button type="button" class="link-btn" onclick={() => ciLoad(false)}>check again</button>.
        </p>
      {:else}
        <p class="hint inline">Docker {st.docker}{st.desktop ? " (Docker Desktop)" : ""}.
          {#if !sp.enabled}<strong>Turn the App proxy on first</strong> (Plugins &amp; proxy).{:else if spDetails && Object.keys(spDetails.profiles).length}Through it: {Object.entries(spDetails.profiles).map(([n, c]) => `${n} (${c})`).join(", ")}.{:else}No VPN is marked via proxy yet.{/if}
        </p>
      {/if}
      {#if st.last_err && !st.running}<p class="hint inline warn">{st.last_err}</p>{/if}
    {/if}
    {#if ciLogs !== null}
      <div class="row" style="gap:0.5rem; align-items:center"><strong>Runner log</strong><span class="np-spacer"></span><button onclick={ciShowLogs}><IconRefresh size={13} /> Refresh</button><button onclick={() => (ciLogs = null)}>Close</button></div>
      <pre class="np-info ci-log">{ciLogs || "empty"}</pre>
    {/if}

    <div class="row" style="gap:1rem; margin-top:0.6rem">
      <label class="oc-choice"><input type="radio" bind:group={ciForm.mode} value="form" /> Set up here</label>
      <label class="oc-choice"><input type="radio" bind:group={ciForm.mode} value="import" /> Import config.toml</label>
    </div>
    {#if ciForm.mode === "form"}
      <div class="oc-form">
        <label class="oc-field" title="Your GitLab, as the runner reaches it.">
          <span>GitLab URL</span>
          <input bind:value={ciForm.url} placeholder="https://gitlab.example.com" />
        </label>
        <label class="oc-field" title="Create a runner in GitLab (project or group > Settings > CI/CD > Runners > New runner) and paste its token (glrt-...). Kept in the vault.">
          <span>Runner token</span>
          <input type="password" bind:value={ciForm.token} placeholder={st?.tokens ? "saved in the vault - paste to replace" : "glrt-..."} autocomplete="off" />
        </label>
        <label class="oc-field">
          <span>Job image</span>
          <input bind:value={ciForm.image} placeholder="alpine:3.22" />
        </label>
        <label class="oc-field">
          <span>Runner name</span>
          <input bind:value={ciForm.name} placeholder="ssh-tool" />
        </label>
      </div>
    {:else}
      <p class="hint">
        Your existing runner's config.toml: cache, image, volumes, extra_hosts
        and the rest are used as written. Tokens and cache keys (S3, GCS,
        Azure) move to the vault and read <code>(saved in the vault)</code>
        here: leave that to keep one, type a new value to replace it, empty
        it to remove it. The app puts the jobs on its own network
        (network_mode). Only the docker executor is supported.
        {#if st && ciForm.toml}{st.runners} {st.runners === 1 ? "runner" : "runners"}, {st.tokens} {st.tokens === 1 ? "token" : "tokens"} in the vault.{/if}
      </p>
      <input type="file" accept=".toml,text/plain" onchange={ciPickFile} />
      <textarea class="ci-toml" bind:value={ciForm.toml} rows="10" spellcheck="false" placeholder={"concurrent = 1\n\n[[runners]]\n  url = \"https://gitlab.example.com\"\n  token = \"glrt-...\"\n  executor = \"docker\"\n  ..."}></textarea>
    {/if}

    <div class="np-use">
      <label class="oc-field" title="Names the jobs must resolve, one name:ip per line - customer hosts their VPN's DNS would answer.">
        <span>Extra hosts (name:ip)</span>
        <textarea bind:value={ciForm.hostsText} rows="4" spellcheck="false" placeholder="app01.customer.example:10.1.2.3"></textarea>
      </label>
      <label class="oc-field" title="Addresses, networks or names that never go through a VPN, even when a customer's networks contain them. The GitLab host and the S3 cache are added on their own.">
        <span>Never through a VPN</span>
        <textarea bind:value={ciForm.directText} rows="4" spellcheck="false" placeholder="192.0.2.0/24"></textarea>
      </label>
    </div>
    <div class="row" style="gap:0.5rem; align-items:center; margin-top:0.5rem">
      <label class="oc-choice"><input type="checkbox" bind:checked={ciForm.autoStart} /> Start with the app (once the vault is open)</label>
      <label class="oc-choice" title="A running runner holds its tokens and cache keys in its container. Off keeps it running while the vault is locked (and through an idle auto-lock).">
        <input type="checkbox" bind:checked={ciForm.stopOnLock} /> Stop when the vault locks
      </label>
      <span class="np-spacer"></span>
      <button onclick={() => ciSave()}>Save</button>
    </div>
    {#if st?.running}<p class="hint inline">Saved changes apply on the next Start.</p>{/if}
  {/snippet}

  {#snippet npForm()}
    <label>
      <span>Name</span>
      <input bind:value={npName} placeholder="e.g. office-vpn" />
    </label>

    {#if (npEditingId ? npEditKind : npKind) === "netbird"}
      {#if !nbInstalled}
        {@render missingPlugin("NetBird", "netbird")}
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
            emptyLabel="Pick an API-token credential holding the setup key…"
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
          <button class="primary" onclick={npSaveNetbird} disabled={npBusy || !npDirty || !npName.trim()}>Save changes</button>
          <button onclick={npRevert} disabled={!npDirty} title="Throw the unsaved changes away">Discard</button>
        {:else}
          <button class="primary" onclick={npCreateNetbird} disabled={npBusy || !nbInstalled || !npName.trim() || !nbCredId}>Add profile</button>
        {/if}
      </div>
    {:else if (npEditingId ? npEditKind : npKind) === "openconnect"}
      {#if !ocInstalled}
        {@render missingPlugin("OpenConnect", "openconnect")}
      {/if}
      <div class="oc-form">
        <label class="oc-field">
          <span>VPN type</span>
          <select bind:value={oc.flavor}>
            {#each Object.entries(ocFlavorTitle) as [v, t]}<option value={v}>{t}</option>{/each}
          </select>
        </label>
        <label class="oc-field" title="Address or URL, with the group path if the gateway uses one">
          <span>Gateway</span>
          <input bind:value={oc.server} placeholder="vpn.example.com/group" />
        </label>
        <label class="oc-field oc-span" title="Further gateways of the same VPN, tried in order when the one before does not answer at all. A rejected login is not tried elsewhere.">
          <span>Backup gateways <span class="hint inline">(one per line, optional)</span></span>
          <textarea rows="2" class="mono" bind:value={ocBackups} placeholder="vpn2.example.com/group"></textarea>
        </label>

        <label class="oc-field" title="Blank = the password credential's username, else asked at login">
          <span>Username</span>
          <input bind:value={oc.username} placeholder="alice" />
        </label>
        <div class="oc-field">
          <span>Password</span>
          {#if ocNewPassword || !oc.password_credential_id}
            <PasswordInput bind:value={ocNewPassword} placeholder={oc.password_credential_id ? "" : "blank = asked at login"} />
          {/if}
          {#if !ocNewPassword}
            <SearchableSelect bind:value={oc.password_credential_id} options={passwordCredOptions} emptyLabel="or pick a stored password…" />
          {/if}
        </div>

        <label class="oc-field" title="Asked at login when blank, if the gateway asks for one">
          <span>Second username <span class="hint inline">(blank = asked)</span></span>
          <input bind:value={oc.second_username} />
        </label>
        <label class="oc-field" title="A secondary login or a one-time code, when the gateway asks">
          <span>Second password</span>
          <select bind:value={oc.second_password}>
            <option value="prompt">Ask at login (hardware / app token)</option>
            <option value="totp">One-time code from Bitwarden / KeePass</option>
            <option value="credential">Stored password</option>
          </select>
        </label>
        {#if oc.second_password === "totp"}
          <div class="oc-field oc-span" title="Both factors then live in one place - your call">
            <span>Item with the TOTP</span>
            <SearchableSelect bind:value={oc.second_credential_id} options={totpCredOptions} placeholder="Search…" emptyLabel="Pick a Bitwarden / KeePass item…" />
          </div>
        {:else if oc.second_password === "credential"}
          <div class="oc-field oc-span">
            <span>Second password credential</span>
            <SearchableSelect bind:value={oc.second_credential_id} options={passwordCredOptions} placeholder="Search…" emptyLabel="Pick a credential…" />
          </div>
        {/if}

        <div class="oc-field oc-span">
          <div class="oc-cert-head">
            <span>Client certificate <span class="hint inline">(optional, stored in the vault)</span></span>
            <div class="oc-choices">
              <label class="oc-choice"><input type="radio" bind:group={ocCertMode} value="pick" /> Stored</label>
              <label class="oc-choice"><input type="radio" bind:group={ocCertMode} value="file" /> Import file</label>
              <label class="oc-choice"><input type="radio" bind:group={ocCertMode} value="paste" /> Paste PEM</label>
            </div>
          </div>
          {#if ocCertMode === "pick"}
            <SearchableSelect bind:value={oc.client_cert_credential_id} options={certCredOptions} emptyLabel="None" />
          {:else}
            <div class="oc-form oc-nested">
              {#if ocCertMode === "file"}
                <label class="oc-field">
                  <span>Certificate (.pfx / .p12 / .pem)</span>
                  <input type="file" accept={CERT_FILE_ACCEPT} onchange={(e) => (ocCertFile = (e.target as HTMLInputElement).files?.[0] ?? null)} />
                </label>
                <label class="oc-field" title="Only when the key is not inside the certificate file">
                  <span>Separate key file <span class="hint inline">(optional)</span></span>
                  <input type="file" accept={CERT_FILE_ACCEPT} onchange={(e) => (ocCertKeyFile = (e.target as HTMLInputElement).files?.[0] ?? null)} />
                </label>
              {:else}
                <label class="oc-field oc-span">
                  <span>Certificate and private key (PEM)</span>
                  <textarea rows="5" class="mono" bind:value={ocCertPaste} spellcheck="false"
                    placeholder={"-----BEGIN CERTIFICATE-----\n...\n-----BEGIN PRIVATE KEY-----\n..."}></textarea>
                </label>
              {/if}
              <label class="oc-field" title="Opens a .pfx / .p12 or an encrypted key now; it is not stored">
                <span>File password <span class="hint inline">(if any)</span></span>
                <PasswordInput bind:value={ocCertPassword} />
              </label>
            </div>
          {/if}
        </div>

        <label class="oc-field" title="Picks this login group when the gateway offers several">
          <span>Group <span class="hint inline">(optional)</span></span>
          <input bind:value={oc.auth_group} placeholder="staff" />
        </label>
        <div class="oc-field">
          <span>Tunnel</span>
          <div class="oc-tunnel">
            <input type="number" min="576" max="9000" bind:value={ocMtu} placeholder="MTU" title="Blank = the gateway's MTU" />
            <label class="oc-choice"><input type="checkbox" bind:checked={oc.no_udp} /> TLS only (no DTLS)</label>
          </div>
        </div>
        {#if oc.flavor === "anyconnect"}
          <div class="oc-field">
            <span>Idle stop</span>
            <label class="oc-choice" title="Off (default): the idle stop leaves the gateway session open, and the next connect resumes it without a login while the gateway still holds it. On: the idle stop logs out.">
              <input type="checkbox" bind:checked={oc.logout_on_idle} /> Log out of the gateway
            </label>
          </div>
        {/if}
      </div>
      {#if npEditingId && oc.pins?.length}
        <p class="hint inline">Trusted gateway certificate: <code>{oc.pins[0]}</code> (changing the gateway address forgets it).</p>
      {/if}
      <div class="row" style="gap:0.5rem">
        {#if npEditingId}
          <button class="primary" onclick={npSaveOpenConnect} disabled={npBusy || !npDirty || !npName.trim() || !oc.server.trim()}>Save changes</button>
          <button onclick={npRevert} disabled={!npDirty} title="Throw the unsaved changes away">Discard</button>
        {:else}
          <button class="primary" onclick={npCreateOpenConnect} disabled={npBusy || !ocInstalled || !npName.trim() || !oc.server.trim()}>Add profile</button>
          {#if ocMissing}<span class="hint inline">Needs {ocMissing}.</span>{/if}
        {/if}
      </div>
    {:else if (npEditingId ? npEditKind : npKind) === "openvpn"}
      {#if !ovInstalled}
        {@render missingPlugin("OpenVPN", "openvpn")}
      {/if}
      <div class="oc-form">
        <div class="oc-field oc-span">
          <span>Configuration (.ovpn) {#if npEditingId && !ovText}<span class="hint inline">- {ov.server}{ov.static_key ? `, ${ov.static_key}` : ""}; import again to replace</span>{/if}</span>
          <div class="oc-tunnel">
            <input type="file" accept=".ovpn,.conf,text/plain" onchange={ovPickFile} />
            <label class="oc-choice"><input type="checkbox" bind:checked={ovPasteOpen} /> Paste instead</label>
          </div>
          {#if ovPasteOpen}
            <textarea rows="6" class="mono" bind:value={ovText} spellcheck="false" placeholder={"client\nremote vpn.example.com 1194 udp\n<ca>\n...\n</ca>"}></textarea>
          {:else if ovFileName}
            <span class="hint inline">{ovFileName}: {ovNeedsLogin ? "username + password" : "certificate only"}{ovChallenge ? `, challenge "${ovChallenge}"` : ""}{ovHasCert ? "; its certificate and key go to a certificate credential" : ""}</span>
          {/if}
        </div>

        {#if ovNeedsLogin}
          <label class="oc-field" title="Blank = the password credential's username, else asked at login">
            <span>Username</span>
            <input bind:value={ov.username} placeholder="alice" />
          </label>
          <div class="oc-field">
            <span>Password</span>
            {#if ovNewPassword || !ov.password_credential_id}
              <PasswordInput bind:value={ovNewPassword} placeholder={ov.password_credential_id ? "" : "blank = asked at login"} />
            {/if}
            {#if !ovNewPassword}
              <SearchableSelect bind:value={ov.password_credential_id} options={passwordCredOptions} emptyLabel="or pick a stored password…" />
            {/if}
          </div>
          <label class="oc-field" title="The answer to the server's challenge (static-challenge in the configuration, or a question the server asks at login), usually a one-time code">
            <span>One-time code {#if ovChallenge}<span class="hint inline">({ovChallenge})</span>{/if}</span>
            <select bind:value={ov.second_password}>
              <option value="prompt">Ask at login (hardware / app token)</option>
              <option value="totp">From Bitwarden / KeePass (TOTP)</option>
              <option value="credential">Stored password</option>
            </select>
          </label>
          {#if ov.second_password === "totp"}
            <div class="oc-field" title="Both factors then live in one place - your call">
              <span>Item with the TOTP</span>
              <SearchableSelect bind:value={ov.second_credential_id} options={totpCredOptions} placeholder="Search…" emptyLabel="Pick a Bitwarden / KeePass item…" />
            </div>
          {:else if ov.second_password === "credential"}
            <div class="oc-field">
              <span>Credential with the answer</span>
              <SearchableSelect bind:value={ov.second_credential_id} options={passwordCredOptions} placeholder="Search…" emptyLabel="Pick a credential…" />
            </div>
          {/if}
        {/if}

        <div class="oc-field oc-span" title="Taken from the .ovpn when it has one; pick another stored certificate to replace it">
          <span>Client certificate <span class="hint inline">({ovHasCert && !ov.client_cert_credential_id ? "from the .ovpn" : "stored in the vault"})</span></span>
          <SearchableSelect bind:value={ov.client_cert_credential_id} options={certCredOptions} emptyLabel={ovHasCert ? "The one in the .ovpn" : "None"} />
        </div>
      </div>
      <div class="row" style="gap:0.5rem">
        {#if npEditingId}
          <button class="primary" onclick={npSaveOpenVPN} disabled={npBusy || !npDirty || !npName.trim()}>Save changes</button>
          <button onclick={npRevert} disabled={!npDirty} title="Throw the unsaved changes away">Discard</button>
        {:else}
          <button class="primary" onclick={npCreateOpenVPN} disabled={npBusy || !ovInstalled || !npName.trim() || !ovText.trim()}>Add profile</button>
          {#if ovMissing}<span class="hint inline">Needs {ovMissing}.</span>{/if}
        {/if}
      </div>
    {:else if (npEditingId ? npEditKind : npKind) === "checkpoint"}
      {#if !cpInstalled}
        {@render missingPlugin("Check Point", "checkpoint")}
      {/if}
      <div class="oc-form">
        <label class="oc-field oc-span" title="The gateway's address, as in Endpoint Security VPN or snx (host, or host:port)">
          <span>Gateway</span>
          <input bind:value={cp.server} placeholder="vpn.example.com" />
        </label>
        <label class="oc-field" title="Blank = the password credential's username, else asked at login">
          <span>Username</span>
          <input bind:value={cp.username} placeholder="alice" />
        </label>
        <div class="oc-field">
          <span>Password</span>
          {#if cpNewPassword || !cp.password_credential_id}
            <PasswordInput bind:value={cpNewPassword} placeholder={cp.password_credential_id ? "" : "blank = asked at login"} />
          {/if}
          {#if !cpNewPassword}
            <SearchableSelect bind:value={cp.password_credential_id} options={passwordCredOptions} emptyLabel="or pick a stored password…" />
          {/if}
        </div>
        <label class="oc-field" title="When the gateway asks for a further factor after the password, usually a one-time code">
          <span>One-time code</span>
          <select bind:value={cp.second_password}>
            <option value="prompt">Ask at login (hardware / app token)</option>
            <option value="totp">From Bitwarden / KeePass (TOTP)</option>
            <option value="credential">Stored password</option>
          </select>
        </label>
        {#if cp.second_password === "totp"}
          <div class="oc-field" title="Both factors then live in one place - your call">
            <span>Item with the TOTP</span>
            <SearchableSelect bind:value={cp.second_credential_id} options={totpCredOptions} placeholder="Search…" emptyLabel="Pick a Bitwarden / KeePass item…" />
          </div>
        {:else if cp.second_password === "credential"}
          <div class="oc-field">
            <span>Credential with the answer</span>
            <SearchableSelect bind:value={cp.second_credential_id} options={passwordCredOptions} placeholder="Search…" emptyLabel="Pick a credential…" />
          </div>
        {/if}
        <label class="oc-field" title="The gateway's login option id, when it offers several (vpn_Username_Password is the usual username + password one). Certificate and browser (SAML) logins are not supported yet.">
          <span>Login option <span class="hint inline">(optional)</span></span>
          <input bind:value={cp.login_option} placeholder="vpn_Username_Password" />
        </label>
      </div>
      {#if npEditingId && cp.pins?.length}
        <p class="hint inline">Trusted gateway certificate: <code>{cp.pins[0]}</code> (changing the gateway address forgets it).</p>
      {/if}
      <div class="row" style="gap:0.5rem">
        {#if npEditingId}
          <button class="primary" onclick={npSaveCheckpoint} disabled={npBusy || !npDirty || !npName.trim() || !cp.server.trim()}>Save changes</button>
          <button onclick={npRevert} disabled={!npDirty} title="Throw the unsaved changes away">Discard</button>
        {:else}
          <button class="primary" onclick={npCreateCheckpoint} disabled={npBusy || !cpInstalled || !npName.trim() || !cp.server.trim()}>Add profile</button>
          {#if cpMissing}<span class="hint inline">Needs {cpMissing}.</span>{/if}
        {/if}
      </div>
    {:else if (npEditingId ? npEditKind : npKind) === "tailscale"}
      {#if !tsInstalled}
        {@render missingPlugin("Tailscale", "tailscale")}
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
            emptyLabel="Pick an API-token credential holding the auth key…"
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
          <button class="primary" onclick={npSaveTailscale} disabled={npBusy || !npDirty || !npName.trim()}>Save changes</button>
          <button onclick={npRevert} disabled={!npDirty} title="Throw the unsaved changes away">Discard</button>
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
          <button class="primary" onclick={npSaveEdit} disabled={npBusy || !npDirty || !npName.trim()}>Save changes</button>
          <button onclick={npRevert} disabled={!npDirty} title="Throw the unsaved changes away">Discard</button>
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
  {/snippet}
  <div class="group group-wide np-page">
    <div class="np-title">
      <h2>Network profiles</h2>
      {#if npUpCount}<span class="hint inline">{npUpCount} {npUpCount === 1 ? "tunnel" : "tunnels"} up</span>{/if}
    </div>
    <p class="hint">
      Userspace tunnels - no TUN adapter, no admin rights, no system
      routes. Assign a profile to a folder or connection (Network
      setting) and its first SSH hop dials through the tunnel.
    </p>
    {#if networkProfiles.error}
      <p class="hint" style="color: var(--red)">{networkProfiles.error}</p>
    {/if}

    <div class="np-split">
      <nav class="np-list" aria-label="Network profiles">
        <button class="np-row" class:sel={npSel === "general"} onclick={() => npSelect("general")}>
          <span class="np-row-text"><strong>Plugins &amp; proxy</strong><small>VPN plugins, app proxy</small></span>
        </button>
        {#if !isMobile}
          <button class="np-row" class:sel={npSel === "ci"} onclick={() => { npSelect("ci"); void ciLoad(true); }}>
            <span class="np-dot" class:up={ci?.status.running}></span>
            <span class="np-row-text"><strong>CI runner</strong><small>{ci?.status.running ? "running" : "jobs through the VPNs"}</small></span>
          </button>
        {/if}
        <div class="np-list-sep"></div>
        {#each networkProfiles.list as np (np.id)}
          <button class="np-row" class:sel={npSel === np.id} onclick={() => npSelect(np.id)}>
            <span class="np-dot" class:up={np.status.running && !np.paused} class:paused={np.paused} class:remote={!!npRemoteOwners[np.id]}></span>
            <span class="np-row-text"><strong>{np.name}</strong><small>{npKindLine(np)}</small></span>
            {#if np.local_proxy?.shared}<span class="np-badge" title="Reachable through the app proxy">via proxy</span>{/if}
            {#if !profileRunsHere(np)}<span class="np-badge" title={NOT_ON_MOBILE_TITLE}>not on mobile</span>{/if}
          </button>
        {/each}
        {#if networkProfiles.list.length === 0 && !networkProfiles.loading}
          <p class="hint np-list-empty">No profiles yet.</p>
        {/if}
        <button class="np-new" class:sel={npSel === "new"} onclick={() => npSelect("new")}>+ New profile</button>
      </nav>

      <div class="np-detail">
        {#if npSel === "general"}
          <h3>Plugins</h3>
          <p class="hint">
            Optional VPN clients, kept out of the main app to keep it small.
            Downloaded from the ssh-tool-plugins releases and verified by
            checksum before install. WireGuard is built in.
          </p>
          {#each plugins as pl (pl.name)}
            <div class="np-card" data-setting={`plugin-${pl.name}`}>
              <div class="np-head">
                <strong>{pluginTitle(pl.name)}</strong>
                {#if !pl.supported && !pl.installed}
                  <span class="np-pill">no download for this platform</span>
                {:else if pl.installed && pl.update_available}
                  <span class="np-pill paused">update available</span>
                {:else if pl.installed}
                  <span class="np-pill running">installed{pl.version ? ` ${pl.version}` : ""}</span>
                {:else}
                  <span class="np-pill">not installed</span>
                {/if}
                {#if pl.installed && pl.engine_version}<span class="np-kind">{engineLabel(pl.name)} {pl.engine_version}</span>{/if}
                {#if pl.installed}<span class="np-meta">{pl.path}</span>{/if}
              </div>
              {#if !pl.supported}
                <!-- No release build for this OS (macOS): a plugin built by
                     hand and put in the plugins dir still runs, so say where,
                     and allow removing one that is there. -->
                {#if pl.installed && pl.beside_app}
                  <p class="hint" style="margin:0.2rem 0">Found next to the app; remove it there.</p>
                {:else if pl.installed}
                  <div class="np-actions">
                    <button class="danger" onclick={() => pluginRemove(pl.name)} disabled={pluginBusy}>Remove</button>
                  </div>
                {:else}
                  <p class="hint" style="margin:0.2rem 0">
                    {#if isMobile}
                      Plugins do not run on mobile.
                    {:else}
                      No release build for this platform. Build it from ssh-tool-plugins and place it as <code>{pl.path}</code>.
                    {/if}
                  </p>
                {/if}
              {:else}
                {#if pluginProgress[pl.name]}
                  {@const pp = pluginProgress[pl.name]}
                  <div class="plugin-progress">
                    <div class="plugin-progress-bar">
                      <!-- Empty while the release is looked up, then only ever
                           grows: download fills it, verify / install hold it full.
                           An animated placeholder here flashed full and snapped
                           back to 0% when the download started. -->
                      <div
                        class="plugin-progress-fill"
                        style:width={pp.phase === "resolve" ? "0%"
                          : pp.phase === "download" ? (pp.total > 0 ? `${Math.min(100, (pp.read / pp.total) * 100)}%` : "0%")
                          : "100%"}
                      ></div>
                    </div>
                    <span class="hint">{progressText(pp)}</span>
                  </div>
                {:else if pl.update_available}
                  <p class="hint" style="margin:0.2rem 0">
                    Installed {pl.version || "(unknown)"}; {pl.latest || "a newer helper"} is available.
                  </p>
                {/if}
                <div class="np-actions">
                  {#if pl.installed}
                    <button class:primary={pl.update_available} onclick={() => pluginDownload(pl.name)} disabled={pluginBusy}>
                      {pl.update_available ? "Update" : "Reinstall"}
                    </button>
                    {#if !pl.beside_app}
                      <button class="danger" onclick={() => pluginRemove(pl.name)} disabled={pluginBusy}>Remove</button>
                    {/if}
                  {:else}
                    <button class="primary" onclick={() => pluginDownload(pl.name)} disabled={pluginBusy}>Download</button>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}

          <h3 class="np-h3">Defaults for every profile</h3>
          <label class="np-field">
            <span>Stop an idle tunnel</span>
            <select value={String(idleStopGlobal)} onchange={(e) => setIdleStopGlobal((e.target as HTMLSelectElement).value)}>
              {#each idleStopOptions(idleStopGlobal) as m (m)}
                <option value={String(m)}>{idleStopLabel(m)}{m === IDLE_STOP_DEFAULT ? " (default)" : ""}</option>
              {/each}
            </select>
            <span class="hint inline">Counted from when the last session through the tunnel closes. A profile can override it.</span>
          </label>
          <fieldset class="check-cards" style="margin-top:0.8rem">
            <label class:active={wgBindPhysical}>
              <input
                type="checkbox"
                checked={wgBindPhysical}
                onchange={(e) => toggleWgBindPhysical((e.target as HTMLInputElement).checked)}
              />
              <div>
                <div class="mode-name">WireGuard: send through the physical network card</div>
                <div class="mode-desc">
                  For when another VPN on this machine (for example a UniFi
                  Identity "TUN Mode" split tunnel) takes the default route and
                  swallows the WireGuard handshake. Leave off otherwise.
                  Running tunnels restart when you change this.
                </div>
              </div>
            </label>
          </fieldset>

          <div class="np-h3 np-sp-head">
            <Switch checked={sp.enabled} label="App proxy" onchange={(on) => { sp.enabled = on; void spSave(); }} />
            <h3>App proxy</h3>
            {#if sp.enabled}
              <span class="hint inline" class:warn={!sp.listening}>{sp.listening ? `listening on ${sp.bind}:${sp.port}` : "not listening - port taken?"}</span>
            {/if}
          </div>
          <p class="hint">
            Lets programs outside ssh-tool reach your customers through these
            VPNs: one SOCKS5 proxy for every profile marked
            <span class="np-badge">via proxy</span> (a profile's Proxy tab).
            Each address goes through the VPN whose networks contain it;
            anything else is refused. The VPN comes up on first use, with its
            login.
          </p>
          {#if sp.enabled}
            <div class="oc-form np-sp-form">
              <label class="oc-field" title="127.0.0.1 is enough for Docker Desktop (host.docker.internal). A Linux Docker host needs the docker bridge address or 0.0.0.0 - firewall the port then.">
                <span>Listen address</span>
                <input bind:value={sp.bind} placeholder="127.0.0.1" />
              </label>
              <label class="oc-field">
                <span>Port</span>
                <div class="row" style="gap:0.5rem; margin:0">
                  <input type="number" min="1" max="65535" bind:value={sp.port} style="width:7rem" />
                  <button class="primary" onclick={() => spSave(true)} disabled={!spDirty}>Save</button>
                  <button onclick={spDiscard} disabled={!spDirty} title="Throw the unsaved changes away">Discard</button>
                  {#if spDirty}<span class="dirty-note">Unsaved changes</span>{/if}
                </div>
              </label>
            </div>
            {#if spProfiles.length > 1}
              <div class="np-prio">
                <strong>When networks overlap</strong>
                <p class="hint">
                  A ticked VPN takes every address its networks contain, the
                  higher one first. The rest go by the most specific network,
                  then the VPN that is up. Addresses added by hand on a
                  profile's Proxy tab, and the addresses of its connections,
                  always go through that profile.
                </p>
                {#each spPrioList as p (p.id)}
                  {@const at = sp.priority.indexOf(p.id)}
                  <div class="np-prio-row">
                    <label class="oc-choice"><input type="checkbox" checked={at >= 0} onchange={() => spPrioMove(p.id, 0)} /> {p.name}</label>
                    {#if at >= 0}
                      <span class="hint inline">priority {at + 1}</span>
                      <button class="np-prio-btn" title="Higher" disabled={at === 0} onclick={() => spPrioMove(p.id, -1)}><IconArrowUp size={13} /></button>
                      <button class="np-prio-btn" title="Lower" disabled={at === sp.priority.length - 1} onclick={() => spPrioMove(p.id, 1)}><IconArrowDown size={13} /></button>
                    {:else}
                      <span class="hint inline">by route</span>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
            <div class="np-prio">
              <label class="oc-choice"><input type="checkbox" checked={sp.jumps} onchange={(e) => { sp.jumps = (e.target as HTMLInputElement).checked; void spSave(); }} /> <strong>Connections with a jump host</strong></label>
              <p class="hint">
                A connection in the tree with a jump host (on it or its folder,
                a connection or a host typed) is reached the way its terminal
                connects: its VPN comes up as its mode says, then the jump
                chain, and the address is opened from the last hop.
              </p>
            </div>
            <div class="row np-which">
              <input bind:value={spWhich} placeholder="Which VPN for... (address or name)" onkeydown={(e) => { if (e.key === "Enter") void spWhichRun(); }} />
              <button onclick={spWhichRun} disabled={!spWhich.trim()}>Check</button>
            </div>
            {#if spWhichOut}
              <p class="hint inline" class:warn={!spWhichOut.ok}>{spWhichOut.text}</p>
            {/if}
            {#if spDetails}
              <p class="hint">
                {#if Object.keys(spDetails.profiles).length}
                  Through it: {Object.entries(spDetails.profiles).map(([n, c]) => `${n} (${c} ${c === 1 ? "network" : "networks"})`).join(", ")}.
                  {spDetails.overlaps.length ? "" : "No overlaps."}
                {:else}
                  No profile goes through it yet: open a profile's Proxy tab and tick Reachable through the app proxy.
                {/if}
              </p>
              {#each spDetails.overlaps as o}
                <p class="hint inline warn">Overlap {o}</p>
              {/each}
              <div class="np-use">
                <div class="np-use-card">
                  <strong>A program on this machine</strong>
                  <p class="hint">curl, a script, a database tool: give it the proxy address with its password.</p>
                  <code class="np-code">{maskProxyURL(spDetails.url)}</code>
                  <div class="row" style="gap:0.5rem"><button onclick={() => spCopy("url")}>Copy proxy address</button></div>
                </div>
                <div class="np-use-card">
                  <strong>A container</strong>
                  <p class="hint">
                    For a GitLab runner use
                    <button class="link-btn" onclick={() => { npSelect("ci"); void ciLoad(true); }}>CI runner</button>.
                    Another container that cannot use a proxy: a small gateway
                    sends only these networks into the proxy; run the container
                    with <code>--network container:sshtool-gw</code>.
                  </p>
                  <div class="row" style="gap:0.5rem">
                    <button onclick={() => spCopy("gateway")}>Copy gateway command</button>
                    <button onclick={() => (spShowRoutes = !spShowRoutes)}>{spShowRoutes ? "Hide networks" : `Show networks (${spDetails.routes.length})`}</button>
                  </div>
                  {#if spShowRoutes}
                    <pre class="np-info">{spDetails.routes.join("\n") || "none yet"}</pre>
                  {/if}
                  <p class="hint inline">After adding a VPN or a network, recreate the gateway with a freshly copied command.</p>
                </div>
              </div>
            {/if}
          {/if}

        {:else if npSel === "ci" && !isMobile}
          {@render ciPage()}

        {:else if npSel === "new"}
          <h3>New profile</h3>
          <div class="row" style="gap:0.5rem; align-items:center">
            <span class="hint" style="margin:0">Type:</span>
            <label class="np-kindpick"><input type="radio" bind:group={npKind} value="wireguard" /> WireGuard</label>
            <label class="np-kindpick"><input type="radio" bind:group={npKind} value="netbird" /> NetBird</label>
            <label class="np-kindpick"><input type="radio" bind:group={npKind} value="tailscale" /> Tailscale</label>
            <label class="np-kindpick"><input type="radio" bind:group={npKind} value="openconnect" /> OpenConnect</label>
            <label class="np-kindpick"><input type="radio" bind:group={npKind} value="openvpn" /> OpenVPN</label>
            <label class="np-kindpick"><input type="radio" bind:group={npKind} value="checkpoint" /> Check Point</label>
          </div>
          <!-- Locked while a save or create runs: it clears the form when done,
               so a field typed meanwhile would be lost without a word. -->
          <fieldset class="np-fieldset" disabled={npAnyBusy}>{@render npForm()}</fieldset>

        {:else if npCur}
          {@const np = npCur}
          <div class="np-head">
            <h3 class="np-name">{np.name}</h3>
            {#if np.paused}
              <span class="np-pill paused">paused</span>
            {:else if np.status.running}
              <span class="np-pill running">up</span>
            {:else}
              <span class="np-pill">down</span>
            {/if}
            {#if npRemoteOwners[np.id]}
              <span class="np-pill remote" title={np.kind === "wireguard" || pluginReportsInfo(np.kind) ? "This tunnel is live on another synced machine. Only one machine can hold it at a time." : "This tunnel is also up on another synced machine."}>
                up on {npRemoteOwners[np.id].machine_name}
              </span>
            {/if}
          </div>
          <div class="np-meta">{npServerLine(np)}</div>
          <div class="np-actions">
            <!-- One state-driven primary action, Pause as a toggle that shows
                 its state, then Browser behind a divider: it does not depend
                 on the tunnel's state. -->
            {#if np.status.running}
              <button class="np-act danger" onclick={() => api.networkProfileStop(np.id).catch((e) => toast.err(errMsg(e)))} title="Stop the running tunnel; connections through it drop">
                <IconStop size={13} /> Disconnect
              </button>
            {:else}
              <button class="np-act primary" onclick={() => npTest(np)} disabled={npTesting[np.id] || np.paused}
                title={np.paused ? "Paused: resume the profile to connect" : "Bring the tunnel up now"}>
                <IconPlay size={13} /> {npTesting[np.id] ? "Connecting…" : "Connect"}
              </button>
            {/if}
            <button class="np-act" class:paused={np.paused} onclick={() => npTogglePause(np)}
              title={np.paused ? "Paused: connections dial direct. Click to resume and connect" : "Dial direct and stop the tunnel"}>
              {#if np.paused}<IconPlay size={13} /> Resume{:else}<IconPause size={13} /> Pause{/if}
            </button>
            <span class="np-sep" aria-hidden="true"></span>
            <button class="np-act" onclick={() => npBrowser(np, "")} title="An isolated browser through this VPN: the customer's networks go through the tunnel, the rest directly">
              <IconGlobe size={13} /> Browser
            </button>
            {#if npRemoteOwners[np.id]}
              <button
                onclick={() => npDisconnectRemote(np)}
                disabled={npDisconnecting[np.id]}
                title="Ask {npRemoteOwners[np.id].machine_name} to drop this tunnel so it's free"
              >
                {npDisconnecting[np.id] ? "Disconnecting…" : `Disconnect on ${npRemoteOwners[np.id].machine_name}`}
              </button>
            {/if}
          </div>

          <div class="np-tabs" role="tablist">
            {#each NP_TABS as [t, label] (t)}
              <button role="tab" aria-selected={npTab === t} class:sel={npTab === t} onclick={() => npOpenTab(t)}>{label}</button>
            {/each}
          </div>

          {#if npTab === "overview"}
            <div class="np-grid">
              <label class="np-field" title="Always: every connect goes through the tunnel. Auto: try direct first, fall back to the tunnel.">
                <span>Mode</span>
                <select
                  value={np.mode === "auto" ? "auto" : "always"}
                  onchange={(e) => npSetPolicy(np, (e.target as HTMLSelectElement).value, np.paused)}
                >
                  <option value="always">Always via tunnel</option>
                  <option value="auto">Auto (direct first)</option>
                </select>
              </label>
              <label class="np-field" title="How long the tunnel stays up after its last session closes">
                <span>Stop when idle</span>
                <select
                  value={np.idle_stop_minutes === null || np.idle_stop_minutes === undefined ? "" : String(np.idle_stop_minutes)}
                  onchange={(e) => npSetIdleStop(np, (e.target as HTMLSelectElement).value)}
                >
                  <option value="">Global ({idleStopShort(idleStopGlobal)})</option>
                  {#each idleStopOptions(np.idle_stop_minutes ?? null) as m (m)}
                    <option value={String(m)}>{idleStopLabel(m)}</option>
                  {/each}
                </select>
              </label>
            </div>
            <dl class="np-facts">
              <dt>Used by</dt><dd>{npUsageText(np.id)}</dd>
              {#if npSessionText(np)}<dt>Session</dt><dd>{npSessionText(np)}</dd>{/if}
              <dt>Proxy</dt>
              <dd>
                {np.local_proxy?.shared ? "via the app proxy" : ""}{np.local_proxy?.shared && np.local_proxy?.enabled ? ", " : ""}{np.local_proxy?.enabled ? `own proxy on port ${np.local_proxy.port}` : ""}{!np.local_proxy?.shared && !np.local_proxy?.enabled ? "off" : ""}
              </dd>
            </dl>
            <p class="hint">{npPresenceText(np)}</p>

          {:else if npTab === "settings"}
            <!-- Locked while a save or create runs: it clears the form when done,
               so a field typed meanwhile would be lost without a word. -->
          <fieldset class="np-fieldset" disabled={npAnyBusy}>{@render npForm()}</fieldset>
            <div class="np-danger">
              <button class="danger" onclick={() => npDelete(np)} disabled={npAnyBusy}>Delete profile</button>
            </div>

          {:else if npTab === "proxy"}
            {@const lp = lpOpen[np.id]}
            {#if !lp}
              <p class="hint">Loading…</p>
            {:else}
              <div class="oc-form np-sp-form">
                <label class="oc-choice oc-span">
                  <input type="checkbox" checked={lp.shared} onchange={(e) => lpToggle(np, "shared", (e.target as HTMLInputElement).checked)} /> Reachable through the app proxy
                  <button class="link-btn" onclick={() => npSelect("general")}>(Plugins &amp; proxy &gt; App proxy)</button>
                </label>
                <label class="oc-choice oc-span" title="A SOCKS5 proxy for this VPN alone, with its own password; the tunnel comes up on the first connection">
                  <input type="checkbox" checked={lp.enabled} onchange={(e) => lpToggle(np, "enabled", (e.target as HTMLInputElement).checked)} /> Separate proxy for this VPN only
                </label>
                {#if lp.enabled}
                  <label class="oc-field" title="127.0.0.1 is enough for Docker Desktop (host.docker.internal). A Linux Docker host needs the docker bridge address or 0.0.0.0 - firewall the port then.">
                    <span>Listen address</span>
                    <input bind:value={lp.bind} placeholder="127.0.0.1" />
                  </label>
                  <label class="oc-field">
                    <span>Port</span>
                    <input type="number" min="1" max="65535" bind:value={lp.port} />
                  </label>
                {/if}
                <label class="oc-field oc-span" title="Destinations sent through this VPN besides the networks the server pushes and the addresses of connections using it - one CIDR or address per line">
                  <span>Extra networks <span class="hint inline">(one per line)</span></span>
                  <textarea rows="2" class="mono" bind:value={lp.networksText} placeholder="10.20.0.0/16"></textarea>
                </label>
                <div class="row oc-span" style="gap:0.5rem">
                  <button class="primary" onclick={() => lpSave(np)} disabled={!lpDirty(np.id)}>Save</button>
                  <button onclick={() => lpDiscard(np)} disabled={!lpDirty(np.id)} title="Throw the unsaved changes away">Discard</button>
                  {#if lpDirty(np.id)}<span class="dirty-note">Unsaved changes</span>{/if}
                </div>
              </div>
              {#if lp.details}
                <h3 class="np-h3">Networks {np.local_proxy?.shared ? "the app proxy sends" : "a proxy sends"} through this VPN</h3>
                <pre class="np-info">{lp.details.routes.join("\n") || "none yet - connect once, or add connections or networks"}</pre>
                <p class="hint inline">{lp.details.sources.join("; ")}</p>
              {/if}
              {#if np.local_proxy?.enabled && lp.details?.url}
                <h3 class="np-h3">This VPN's own proxy {#if !np.local_proxy_listening}<span class="hint inline warn">not listening - port taken?</span>{/if}</h3>
                <div class="np-use">
                  <div class="np-use-card">
                    <strong>A program on this machine</strong>
                    <code class="np-code">{maskProxyURL(lp.details.url)}</code>
                    <div class="row" style="gap:0.5rem"><button onclick={() => lpCopy(np, "url")}>Copy proxy address</button></div>
                  </div>
                  <div class="np-use-card">
                    <strong>A CI runner in Docker</strong>
                    <p class="hint">A gateway container for this VPN only; the runner joins it with <code>network_mode = "container:NAME"</code>.</p>
                    <div class="row" style="gap:0.5rem"><button onclick={() => lpCopy(np, "gateway")}>Copy gateway command</button></div>
                  </div>
                </div>
              {/if}
            {/if}

          {:else if npTab === "details"}
            {#if pluginReportsInfo(np.kind)}
              {@const ti = npInfoOpen[np.id]}
              {#if !np.status.running}
                <p class="hint">The tunnel is down.</p>
              {:else if ti === undefined}
                <p class="hint">Loading…</p>
              {:else if ti === null}
                <p class="hint">The tunnel reported nothing yet.</p>
              {:else}
                <pre class="np-info">{tunnelInfoText(ti)}</pre>
                <div class="row" style="gap:0.5rem">
                  <button onclick={() => copyText(tunnelInfoText(ti), { label: "Tunnel details" })}>Copy</button>
                  <button onclick={() => npRefreshInfo(np)}>Refresh</button>
                </div>
              {/if}
            {:else}
              <pre class="np-info">{npStatusText(np)}</pre>
            {/if}
          {/if}
        {:else}
          <p class="hint">Pick a profile on the left.</p>
        {/if}
      </div>
    </div>
  </div>

  {:else if activeSection === "liveness"}
  <div class="group">
    <h2>Liveness probe</h2>
    <p class="hint">
      Show a reachability dot on connection rows inside folders you have open.
      Green means a TCP connect to the SSH port succeeded, red means it was
      refused or timed out; hosts that aren't probed stay blank. Only
      connections in currently-expanded folders are probed, so it stays cheap
      on a large tree.
    </p>

    <fieldset class="check-cards" style="margin-top:1rem">
      <label class:active={livenessProbe}>
        <input
          type="checkbox"
          checked={livenessProbe}
          onchange={(e) => toggleLivenessProbe((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Enable liveness probe</div>
          <div class="mode-desc">
            Routes through a connection's WireGuard profile or an already-open
            bastion when there is one; it never brings a tunnel or bastion up
            just to probe (a host behind a sleeping VPN or an unopened bastion
            shows no dot rather than a false red). Any folder can opt out in its
            own settings.
          </div>
        </div>
      </label>
    </fieldset>

    {#if livenessProbe}
      <fieldset class="check-cards" style="margin-top:0.8rem">
        <label class:active={livenessKeepTunnel}>
          <input
            type="checkbox"
            checked={livenessKeepTunnel}
            onchange={(e) => toggleLivenessKeepTunnel((e.target as HTMLInputElement).checked)}
          />
          <div>
            <div class="mode-name">Keep the WireGuard tunnel warm while probing</div>
            <div class="mode-desc">
              Off by default: the probe rides an open tunnel but does not hold it
              up, so a tunnel left running only for status dots stops on its own
              once the last SSH session closes. Turn this on to keep the tunnel
              warm while the folder stays expanded - handy for a quick reconnect,
              at the cost of the VPN staying up with no active session.
            </div>
          </div>
        </label>
      </fieldset>

      <fieldset class="check-cards" style="margin-top:0.8rem">
        <label class:active={livenessDynamic}>
          <input
            type="checkbox"
            checked={livenessDynamic}
            onchange={(e) => toggleLivenessDynamic((e.target as HTMLInputElement).checked)}
          />
          <div>
            <div class="mode-name">Also probe dynamic inventory hosts</div>
            <div class="mode-desc">
              Probe dynamic-inventory entries (Proxmox, Hetzner, Ansible, …) too,
              not just saved connections. Only entries the provider reports as
              running are probed - a stopped VM keeps its provider status and
              gets no dot. Each entry uses its folder's SSH port, network
              profile and jump host, same as connecting to it would.
            </div>
          </div>
        </label>
      </fieldset>

      <label class="inline-num" style="margin-top:0.8rem; display:block">
        Re-probe every
        <input
          type="number"
          min="10"
          step="5"
          style="width:5rem"
          value={livenessIntervalSec}
          onchange={(e) => saveLivenessInterval(parseInt((e.target as HTMLInputElement).value, 10))}
        />
        seconds (minimum 10)
      </label>
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
      <button class="primary" onclick={save} disabled={browserPath.trim() === (savedPath ?? "").trim()}>Save</button>
      {#if browserPath.trim() !== (savedPath ?? "").trim()}
        <button onclick={() => (browserPath = savedPath ?? "")} title="Throw the unsaved changes away">Discard</button>
        <span class="dirty-note">Unsaved changes</span>
      {:else if savedAt}
        <span class="ok">Saved {savedAt}</span>
      {/if}
    </div>
    <p class="hint">
      Currently active: <code>{savedPath || "(auto-detect)"}</code>
    </p>

    <h2 style="margin-top:1.5rem">Profile</h2>
    <fieldset class="check-cards">
      <label class:active={browserPersistent}>
        <input
          type="checkbox"
          checked={browserPersistent}
          onchange={(e) => toggleBrowserPersistent((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Use a persistent browser profile</div>
          <div class="mode-desc">
            Keeps logins and cookies between launches, so a tunnelled site that
            needs your saved credentials stays signed in. Still a dedicated
            profile - separate from your everyday browser, so normal browsing
            isn't routed through the tunnel. Off (default) opens a fresh isolated
            profile each time. Works with both Chromium- and Firefox-family
            browsers (on WSL, a persistent Firefox profile falls back to isolated).
          </div>
        </div>
      </label>
    </fieldset>
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
          <div style="display:flex; gap:0.4rem">
            <button onclick={loadDefaultSnippets} title="Add the built-in starter snippets (skips any you already have)">Load defaults</button>
            <button class="primary" onclick={newSnippet}>+ New</button>
          </div>
        </div>
        {#if snippets.length === 0}
          <div class="empty">No snippets yet. Add one, or click <strong>Load defaults</strong>.</div>
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
          <small class="hint">Use <code>{"${var}"}</code> or <code>{"${var:default}"}</code> to prompt for values when firing (e.g. <code>{"journalctl -u ${svc} --since ${since:-1h}"}</code>).</small>
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

    {#if vaultState.locked}
      <h3 style="margin-top:1.2rem">Unlock</h3>
      <p class="hint">
        The vault is locked, so stored passwords, keys and API tokens
        cannot be read. Unlocking re-prompts for the master passphrase
        (or uses the auto-unlock sidecar where one is set up).
      </p>
      <div class="row">
        <button class="primary" onclick={onUnlockNow}>Unlock vault</button>
      </div>
    {:else}
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
    {/if}

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

  {:else if activeSection === "external"}
  <div class="group group-wide">
    <div class="ext-tabs" role="tablist">
      {#each EXTERNAL_TABS as t (t.id)}
        <button
          role="tab"
          class="ext-tab"
          class:active={externalTab === t.id}
          aria-selected={externalTab === t.id}
          onclick={() => (externalTab = t.id)}
        >{t.title}</button>
      {/each}
    </div>
    {#if externalTab === "keepass"}
      <KeepassSettings />
    {:else if externalTab === "bitwarden"}
      <BitwardenSettings />
    {:else if externalTab === "infisical"}
      <InfisicalSettings />
    {/if}
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
    <section class="insights">
      <header class="ins-head">
        <button
          class="ins-toggle"
          onclick={() => (statsOpen = !statsOpen)}
          aria-expanded={statsOpen}
        >
          <span class="chev" class:open={statsOpen}>&#9656;</span>
          Insights
        </button>
        <div class="ins-windows">
          {#each STAT_WINDOWS as w (w.days)}
            <button
              class="win"
              class:active={statsWindow === w.days}
              onclick={() => setStatsWindow(w.days)}
              disabled={statsBusy}
            >{w.label}</button>
          {/each}
          <button class="win" onclick={loadStats} disabled={statsBusy} title="Recompute">
            {statsBusy ? "…" : "\u21bb"}
          </button>
        </div>
      </header>

      {#if statsOpen}
        {#if statsError}
          <div class="err">{statsError}</div>
        {:else if !stats}
          <p class="hint">Computing…</p>
        {:else if emptyWindow}
          <p class="hint">
            No events in this window.
            {#if statsWindow > 0}Try a wider range.{/if}
          </p>
        {:else}
          <div class="tiles">
            <div class="tile">
              <span class="tv">{stats.total.toLocaleString()}</span>
              <span class="tl">events</span>
            </div>
            <div class="tile">
              <span class="tv">{stats.connects.toLocaleString()}</span>
              <span class="tl">SSH connects</span>
            </div>
            <div class="tile">
              <span class="tv">{fmtDur(stats.sessionSecs)}</span>
              <span class="tl">time connected</span>
            </div>
            <div class="tile">
              <span class="tv">{fmtDur(avgSessionSecs)}</span>
              <span class="tl">avg session</span>
            </div>
            <div class="tile">
              <span class="tv">{fmtDur(stats.longestSecs)}</span>
              <span class="tl">longest</span>
            </div>
            <div class="tile" class:warn={failureTotal > 0}>
              <span class="tv">{failureTotal.toLocaleString()}</span>
              <span class="tl">failures</span>
            </div>
          </div>

          {#if stats.unpaired > 0}
            <p class="hint sub-note">
              {stats.unpaired}
              {stats.unpaired === 1 ? "connect has" : "connects have"}
              no matching disconnect - still open, or the app closed
              before it was recorded. They count as connects but add
              no time, so the totals above are a lower bound.
            </p>
          {/if}

          <div class="charts">
            <div class="chart">
              <h4>Activity</h4>
              <div class="spark" role="img" aria-label="Events per day">
                {#each dailySeries as d (d.key)}
                  <div
                    class="bar"
                    class:zero={d.count === 0}
                    style="height:{Math.max(2, (d.count / maxDaily) * 100)}%"
                    title="{d.key}: {d.count} event{d.count === 1 ? '' : 's'}"
                  ></div>
                {/each}
              </div>
              <div class="axis">
                <span>{dailySeries.length ? fmtDay(dailySeries[0].key) : ""}</span>
                <span>{dailySeries.length ? fmtDay(dailySeries[dailySeries.length - 1].key) : ""}</span>
              </div>
            </div>

            <div class="chart">
              <h4>By hour</h4>
              <div class="spark hours" role="img" aria-label="Events by hour of day">
                {#each stats.hourly as h, i (i)}
                  <div
                    class="bar hour"
                    class:zero={h === 0}
                    style="height:{Math.max(2, (h / maxHourly) * 100)}%"
                    title="{String(i).padStart(2, '0')}:00 - {h} event{h === 1 ? '' : 's'}"
                  ></div>
                {/each}
              </div>
              <div class="axis">
                <span>00</span><span>12</span><span>23</span>
              </div>
            </div>
          </div>

          <div class="lists">
            <div class="lst">
              <h4>Top hosts</h4>
              {#if topHosts.length === 0}
                <p class="hint">No SSH connects in this window.</p>
              {:else}
                {#each topHosts as h (h.host)}
                  <div class="lrow">
                    <span class="lname" title={h.name ? `${h.name} (${h.host})` : h.host}>
                      {h.name || h.host}
                    </span>
                    <span class="ltrack">
                      <span class="lfill" style="width:{(h.seconds / maxHostSecs) * 100}%"></span>
                    </span>
                    <span class="lval">
                      {fmtDur(h.seconds)}
                      <span class="lsub">{h.connects}x</span>
                    </span>
                  </div>
                {/each}
              {/if}
            </div>

            <div class="lst">
              <h4>Top actions</h4>
              {#each topActions as a (a.key)}
                <div class="lrow">
                  <button
                    class="lname link"
                    title="Filter the table by {a.key}"
                    onclick={() => (auditFilter = a.key)}
                  >{a.key}</button>
                  <span class="ltrack">
                    <span
                      class="lfill"
                      class:bad={a.key.endsWith(".failed") || a.key === "share.violation" || a.key === "share.deny"}
                      style="width:{(a.count / maxActionCount) * 100}%"
                    ></span>
                  </span>
                  <span class="lval">{a.count}</span>
                </div>
              {/each}
            </div>
          </div>

          {#if stats.gates.length > 0 || stats.llmLoggingOff}
            <div class="fails">
              <h4>LLM access ({stats.llmActions.toLocaleString()} actions)</h4>
              <div class="gate-bar" role="img" aria-label="LLM actions by approval gate">
                {#each stats.gates as g (g.key)}
                  <span
                    class="gseg g-{g.key}"
                    style="width:{gatePct(g.count)}%"
                    title="{GATE_LABEL[g.key] ?? g.key}: {g.count}"
                  ></span>
                {/each}
              </div>
              {#if stats.llmLoggingOff}
                <p class="hint gap-note">
                  LLM activity logging was switched off during this
                  period{stats.llmLoggingOffTs
                    ? " (last on " + new Date(stats.llmLoggingOffTs * 1000).toLocaleString() + ")"
                    : ""}, so anything the LLM did while it was off is
                  not in the log. These counts are a floor, not a total.
                </p>
              {/if}
              <div class="chips gate-chips">
                {#each stats.gates as g (g.key)}
                  <button
                    class="chip gate g-{g.key}"
                    title="Filter the table by gate={g.key}"
                    onclick={() => (auditFilter = g.key)}
                  >
                    <span class="dot g-{g.key}"></span>
                    {GATE_LABEL[g.key] ?? g.key} <b>{g.count}</b>
                  </button>
                {/each}
              </div>
            </div>
          {/if}

          {#if stats.failures.length > 0}
            <div class="fails">
              <h4>Failures and denials</h4>
              <div class="chips">
                {#each stats.failures as f (f.key)}
                  <button
                    class="chip bad"
                    title="Filter the table by {f.key}"
                    onclick={() => (auditFilter = f.key)}
                  >{f.key} <b>{f.count}</b></button>
                {/each}
              </div>
            </div>
          {/if}
        {/if}
      {/if}
    </section>

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
    <fieldset class="check-cards">
      <label class:active={autoBackupPrefs.enabled}>
        <input
          type="checkbox"
          checked={autoBackupPrefs.enabled}
          onchange={(e) => saveAutoBackupPrefs({ ...autoBackupPrefs, enabled: (e.target as HTMLInputElement).checked })}
        />
        <div>
          <div class="mode-name">Run a backup in the background every 24 hours</div>
          <div class="mode-desc">
            Needs the vault auto-unlock sidecar; if the vault is locked
            and no sidecar is set up, the run is silently skipped.
          </div>
        </div>
      </label>
    </fieldset>
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
        <option value="dropbox">Dropbox</option>
        <option value="onedrive">OneDrive</option>
        <option value="gdrive">Google Drive</option>
      </select>
      <span class="field-hint">
        {#if syncTransport === "sftp"}
          Stores the snapshot in a directory on an SSH server, authenticated
          with a credential from your vault. Rename is atomic on POSIX hosts.
        {:else if syncTransport === "dropbox"}
          Stores the snapshot in a Dropbox app folder. Connect once in the
          browser; no server to run. Uses your own Dropbox app key (PKCE, no
          secret stored).
        {:else if syncTransport === "onedrive"}
          Stores the snapshot in the app's OneDrive folder. Connect once in the
          browser. Uses your own Azure app (client) ID (PKCE, no secret stored).
        {:else if syncTransport === "gdrive"}
          Stores the snapshot in the app's hidden Google Drive data folder.
          Connect once in the browser. Uses your own Google OAuth client ID
          (PKCE, no secret stored).
        {:else}
          Stores the snapshot on a WebDAV server with basic auth over https.
        {/if}
      </span>
    </label>
    <label>Sync passphrase
      <div class="row" style="gap:0.4rem; align-items:center">
        <PasswordInput bind:value={syncPassphrase} placeholder={syncCfg?.has_passphrase ? "saved - leave blank to keep" : "seals the snapshot"} />
        <button type="button" onclick={saveSyncPassphrase} disabled={!syncPassphrase.trim()}>
          {syncCfg?.has_passphrase ? "Change" : "Set"}
        </button>
      </div>
      <span class="field-hint">
        Encrypts the snapshot - one value for ALL backends. Use the same one on
        every machine. Stored in this machine's vault (sync needs an unlocked
        vault). {#if syncCfg?.has_passphrase}A passphrase is set; leave blank to keep it.{/if}
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
    <div class="row" style="gap:0.5rem">
      <button class="primary" onclick={syncSaveConfig} disabled={!webdavDirty}>Save</button>
      {#if webdavDirty}
        <button onclick={() => syncDiscard("webdav")} title="Throw the unsaved changes away">Discard</button>
        <span class="dirty-note">Unsaved changes</span>
      {/if}
    </div>
  </div>
  {:else if syncTransport === "sftp"}
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
    <div class="row" style="gap:0.5rem">
      <button class="primary" onclick={syncSaveSftp} disabled={!sftpDirty}>Save</button>
      {#if sftpDirty}
        <button onclick={() => syncDiscard("sftp")} title="Throw the unsaved changes away">Discard</button>
        <span class="dirty-note">Unsaved changes</span>
      {/if}
    </div>
  </div>
  {:else if syncTransport === "dropbox"}
  <!-- 1c. Dropbox -->
  <div class="group">
    <h2>Dropbox</h2>
    <p class="hint">
      Create a Dropbox app (Scoped access, App-folder) in the Dropbox App
      Console. Enable the <code>files.content.read</code> and
      <code>files.content.write</code> scopes (Permissions tab, then Submit).
      Under OAuth 2, add the redirect URI below exactly, and keep
      <strong>Allow public clients (PKCE)</strong> on. Then paste the
      <strong>app key</strong>. No secret is stored (PKCE).
    </p>
    <label>Redirect URI (add this to your Dropbox app)
      <div class="row" style="gap:0.4rem; align-items:center">
        <input value={oauthRedirectUri} spellcheck="false" class="mono" style="flex:1" onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} oninput={(e) => (e.currentTarget as HTMLInputElement).value = oauthRedirectUri} />
        <button type="button" onclick={copyRedirectUri}>Copy</button>
      </div>
      <span class="field-hint">Must match exactly - Dropbox rejects any other value. Click to select, then copy.</span>
    </label>
    <label>App key
      <input bind:value={dropboxAppKey} placeholder="your Dropbox app key" spellcheck="false" class="mono" />
    </label>
    <label>Sync folder
      <input bind:value={dropboxFolder} placeholder="/ssh-tool" spellcheck="false" class="mono" />
      <span class="field-hint">Path inside the app folder. Created on first push.</span>
    </label>
    <div class="row" style="gap:0.5rem; align-items:center">
      <button class="primary" onclick={dropboxSaveConfig} disabled={!dropboxDirty}>Save</button>
      {#if dropboxDirty}
        <button onclick={() => syncDiscard("dropbox")} title="Throw the unsaved changes away">Discard</button>
        <span class="dirty-note">Unsaved changes</span>
      {/if}
      {#if hasDropboxToken}
        <span class="field-hint" style="color:var(--ok, #3a3)">Connected</span>
        <button onclick={dropboxDisconnect}>Disconnect</button>
      {:else}
        <button onclick={dropboxConnect} disabled={dropboxConnecting || !dropboxAppKey.trim()}>
          {dropboxConnecting ? "Waiting for browser..." : "Connect Dropbox"}
        </button>
      {/if}
    </div>
  </div>
  {:else if syncTransport === "onedrive"}
  <!-- 1d. OneDrive -->
  <div class="group">
    <h2>OneDrive</h2>
    <p class="hint">
      Register an app in the Azure Portal (Entra ID) as a
      <strong>Mobile and desktop applications</strong> platform (public client),
      add the redirect URI below, and grant the
      <code>Files.ReadWrite.AppFolder</code> + <code>offline_access</code>
      delegated permissions. Then paste its
      <strong>Application (client) ID</strong>. No secret is stored (PKCE).
    </p>
    <label>Redirect URI (add this to your Azure app)
      <div class="row" style="gap:0.4rem; align-items:center">
        <input value={oauthRedirectUri} spellcheck="false" class="mono" style="flex:1" onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} oninput={(e) => (e.currentTarget as HTMLInputElement).value = oauthRedirectUri} />
        <button type="button" onclick={copyRedirectUri}>Copy</button>
      </div>
      <span class="field-hint">Add under Authentication - Mobile and desktop applications. Must match exactly.</span>
    </label>
    <label>Account type
      <select bind:value={onedriveAccountType}>
        <option value="personal">Personal Microsoft account</option>
        <option value="work">Work / school account</option>
        <option value="both">Either (personal or work/school)</option>
      </select>
      <span class="field-hint">
        Pick "Work / school" if you only have a business OneDrive. This must
        match your Azure app's "Supported account types" setting.
      </span>
    </label>
    <label>Application (client) ID
      <input bind:value={onedriveClientId} placeholder="Azure app client ID" spellcheck="false" class="mono" />
    </label>
    <div class="row" style="gap:0.5rem; align-items:center">
      {#if hasOnedriveToken}
        <span class="field-hint" style="color:var(--ok, #3a3)">Connected</span>
        <button onclick={onedriveDisconnect}>Disconnect</button>
      {:else}
        <button class="primary" onclick={onedriveConnect} disabled={onedriveConnecting || !onedriveClientId.trim()}>
          {onedriveConnecting ? "Waiting for browser..." : "Connect OneDrive"}
        </button>
      {/if}
    </div>
  </div>
  {:else if syncTransport === "gdrive"}
  <!-- 1e. Google Drive -->
  <div class="group">
    <h2>Google Drive</h2>
    <p class="hint">
      Create an OAuth client of type <strong>Desktop app</strong> in the Google
      Cloud Console and add the redirect URI below. Add the
      <code>drive.appdata</code> scope on the OAuth consent screen, then paste
      the <strong>client ID</strong> and <strong>client secret</strong>. Google
      requires the secret even for a Desktop app - for an installed app it is
      not truly confidential, and it is kept in this machine's vault. A personal
      project can stay in "testing" mode - fine for your own account.
    </p>
    <label>Redirect URI (add this to your Google OAuth client)
      <div class="row" style="gap:0.4rem; align-items:center">
        <input value={oauthRedirectUri} spellcheck="false" class="mono" style="flex:1" onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} oninput={(e) => (e.currentTarget as HTMLInputElement).value = oauthRedirectUri} />
        <button type="button" onclick={copyRedirectUri}>Copy</button>
      </div>
      <span class="field-hint">Add under Authorized redirect URIs. Google also accepts other loopback ports, but this one is registered here.</span>
    </label>
    <label>OAuth client ID
      <input bind:value={gdriveClientId} placeholder="Google OAuth client ID" spellcheck="false" class="mono" />
    </label>
    <label>OAuth client secret
      <PasswordInput bind:value={gdriveClientSecret} placeholder={hasGdriveSecret ? "saved - leave blank to keep" : "Google client secret"} />
      <span class="field-hint">From the same Desktop-app credential in the Google Cloud Console.</span>
    </label>
    <div class="row" style="gap:0.5rem; align-items:center">
      {#if hasGdriveToken}
        <span class="field-hint" style="color:var(--ok, #3a3)">Connected</span>
        <button onclick={gdriveDisconnect}>Disconnect</button>
      {:else}
        <button class="primary" onclick={gdriveConnect} disabled={gdriveConnecting || !gdriveClientId.trim() || (!gdriveClientSecret.trim() && !hasGdriveSecret)}>
          {gdriveConnecting ? "Waiting for browser..." : "Connect Google Drive"}
        </button>
      {/if}
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
    <p class="hint">
      Any other format - a CSV, a spreadsheet, an inventory file, another
      tool's export, a list pasted into a chat - can go through an LLM
      client instead: with <button class="link" onclick={() => pickSection("llm", "mcp-manage|mcp-enable")}>LLM (MCP) access</button>
      on and <strong>Allow manage</strong> ticked, it reads the file and
      stages the folders and connections the way you ask for them. Nothing
      is written until you approve the whole plan in the app.
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
          Import done -
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

  {:else if importSource === "superputty"}
  <div class="group">
    <h2>SuperPuTTY</h2>
    <p class="hint">
      SuperPuTTY keeps its sessions in a <code>Sessions.xml</code> file
      (usually <code>%APPDATA%\SuperPuTTY\Sessions.xml</code>). Load it
      here - SSH sessions and their folder tree come along; RDP / telnet /
      other types are skipped. SuperPuTTY stores no passwords, so nothing
      is lost - attach credentials afterwards.
    </p>
    <label>Target folder
      <select bind:value={superTargetFolderID} disabled={superBusy}>
        <option value="">(root)</option>
        {#each rdmFolderOptions as f (f.id)}
          <option value={f.id}>{folderLabel(f)}</option>
        {/each}
      </select>
    </label>
    <div class="row">
      <input
        type="file"
        accept=".xml,text/xml,text/plain"
        bind:this={superInputEl}
        onchange={onSuperPuttyFile}
        disabled={superBusy}
      />
      {#if superBusy}<span class="hint">Importing…</span>{/if}
    </div>
    {#if superError}<div class="err">{superError}</div>{/if}
    {#if superSummary}
      <div class="import-summary ok">
        <strong>SuperPuTTY import:</strong>
        <ul>
          <li>Connections created: {superSummary.connections_created}</li>
          <li>Folders created: {superSummary.folders_created}</li>
          <li>Skipped (name conflict): {superSummary.connections_skipped}</li>
          <li>Skipped (not SSH): {superSummary.skipped_non_ssh}</li>
          {#if superSummary.warnings.length}
            <li class="warn">Warnings: {superSummary.warnings.length}</li>
          {/if}
        </ul>
        {#if superSummary.warnings.length}
          <details>
            <summary>Warnings</summary>
            <ul>{#each superSummary.warnings as w}<li>{w}</li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}
  </div>

  {:else if importSource === "kafkatool"}
  <div class="group">
    <h2>kafka-tool</h2>
    <p class="hint">
      kafka-tool keeps its cluster profiles in <code>~/.config/kafka-tool/profiles.json</code>.
      Each profile becomes a connection with Kafka enabled, in a <code>kafka-tool</code> folder:
      the SSH host (and its proxy jump) when the profile used a tunnel, else the brokers directly.
      SSH passwords go to the vault (unlock it first); key files are not read - attach a key
      credential afterwards.
    </p>
    <label>Target folder
      <select bind:value={kafkaTargetFolderID} disabled={kafkaBusy}>
        <option value="">(root)</option>
        {#each rdmFolderOptions as f (f.id)}
          <option value={f.id}>{folderLabel(f)}</option>
        {/each}
      </select>
    </label>
    <div class="row">
      <input
        type="file"
        accept=".json,application/json,text/plain"
        bind:this={kafkaInputEl}
        onchange={onKafkaToolFile}
        disabled={kafkaBusy}
      />
      {#if kafkaBusy}<span class="hint">Importing…</span>{/if}
    </div>
    {#if kafkaError}<div class="err">{kafkaError}</div>{/if}
    {#if kafkaSummary}
      <div class="import-summary ok">
        <strong>kafka-tool import:</strong>
        <ul>
          <li>Connections created: {kafkaSummary.connections_created}</li>
          <li>Folders created: {kafkaSummary.folders_created}</li>
          <li>Skipped: {kafkaSummary.connections_skipped}</li>
          <li>SSH passwords stored in the vault: {kafkaSummary.passwords_stored}</li>
          {#if kafkaSummary.warnings.length}
            <li class="warn">Notes: {kafkaSummary.warnings.length}</li>
          {/if}
        </ul>
        {#if kafkaSummary.warnings.length}
          <details open>
            <summary>Notes</summary>
            <ul>{#each kafkaSummary.warnings as w}<li>{w}</li>{/each}</ul>
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
  <div class="group">
    <h2>LLM (MCP) access to sessions</h2>
    <p class="hint">
      Let an external LLM client (Claude Code, etc.) connect to ssh-tool and
      help you debug a live SSH session - read what's on screen, pull logs,
      propose and run commands. Off by default. Reads are safe; commands that
      change state always need your approval.
    </p>

    <fieldset class="check-cards">
      <label class:active={mcpEnabled} data-setting="mcp-enable">
        <input
          type="checkbox"
          checked={mcpEnabled}
          onchange={(e) => toggleMcp((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Allow LLM (MCP) access to shared sessions</div>
          <div class="mode-desc">
            Starts a local-only bridge (a unix socket on Linux/macOS, a
            loopback pipe on Windows) an MCP client can connect to. Nothing is
            exposed to the network, and no session is reachable until you
            explicitly share it with the pane's Share-with-LLM button.
          </div>
        </div>
      </label>
    </fieldset>

    <fieldset class="check-cards">
      <label class:active={notificationsEnabled}>
        <input
          type="checkbox"
          checked={notificationsEnabled}
          onchange={(e) => toggleNotifications((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Desktop notifications for prompts that need you</div>
          <div class="mode-desc">
            When the app is in the background, pop an OS notification (plus a
            taskbar flash) for a blocking prompt - an LLM approval request or a
            host-key confirmation - so you don't leave it waiting unseen.
          </div>
        </div>
      </label>

      <label class:active={mcpAuditEnabled}>
        <input
          type="checkbox"
          checked={mcpAuditEnabled}
          onchange={(e) => toggleMcpAudit((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Keep a persistent log of LLM activity (audit)</div>
          <div class="mode-desc">
            Record every command the LLM runs, types or connects to the local
            audit log so it survives restarts. The live LLM-activity panel (robot
            icon in the status bar / pane toolbar) works either way; this only
            controls the durable copy.
          </div>
        </div>
      </label>
    </fieldset>

    {#if mcpAuditEnabled}
      <fieldset class="check-cards" style="margin-left:1.6rem">
        <label class:active={mcpAuditOutput}>
          <input
            type="checkbox"
            checked={mcpAuditOutput}
            onchange={(e) => toggleMcpAuditOutput((e.target as HTMLInputElement).checked)}
          />
          <div>
            <div class="mode-name">Also store command output in the audit log</div>
            <div class="mode-desc">
              Off by default. Command output can contain secrets the LLM read
              (a <code>.env</code> file, environment variables, kubernetes
              secrets), and the audit log is a plaintext file on disk - not
              encrypted like the vault. Enable only if you accept that.
            </div>
          </div>
        </label>
      </fieldset>
    {/if}

    {#if mcpEnabled}
      <h3 style="margin-top:1.2rem" data-setting="mcp-register">Register with your LLM client</h3>
      <p class="hint">
        Add ssh-tool as an MCP server once. It runs as a small bridge process
        the client launches (<code>{mcpExePath || "ssh-tool"} --mcp-bridge</code>).
      </p>

      <p class="hint"><strong>Claude Code:</strong></p>
      <div class="scope-row">
        {#if mcpWslExePath}
          <div class="scope-seg" role="group" aria-label="Shell you paste into">
            <button
              class="scope-btn" class:active={mcpWhere === "pwsh"}
              onclick={() => (mcpWhere = "pwsh")}
              title="Claude Code in PowerShell"
            >PowerShell</button>
            <button
              class="scope-btn" class:active={mcpWhere === "cmd"}
              onclick={() => (mcpWhere = "cmd")}
              title="Claude Code in Command Prompt"
            >CMD</button>
            <button
              class="scope-btn" class:active={mcpWhere === "wsl"}
              onclick={() => (mcpWhere = "wsl")}
              title="Claude Code inside WSL - points at the Windows binary"
            >WSL</button>
          </div>
        {/if}
        <div class="scope-seg" role="group" aria-label="Registration scope">
          <button
            class="scope-btn" class:active={mcpScope === "user"}
            onclick={() => (mcpScope = "user")}
            title="Register for every project on this machine"
          >All projects</button>
          <button
            class="scope-btn" class:active={mcpScope === "local"}
            onclick={() => (mcpScope = "local")}
            title="Register only for the directory you run this in"
          >This folder only</button>
        </div>
      </div>
      {@const mcpBin = mcpQuote(
        mcpWhere === "wsl" && mcpWslExePath ? mcpWslExePath : (mcpExePath || "ssh-tool"),
        mcpWhere,
      )}
      {@const claudeCmd = `claude mcp add ${mcpScope === "user" ? "--scope user " : ""}ssh-tool -- ${mcpBin} --mcp-bridge`}
      <div class="cmd-wrap">
        <pre class="cmd-block">{claudeCmd}</pre>
        <button class="cmd-copy" title="Copy command" onclick={() => copyCmd(claudeCmd, "Command")}>
          <IconCopy size={13} />
        </button>
      </div>
      <p class="hint">
        {#if mcpScope === "user"}Registers once for every project on this machine.
        {:else}Registers only for the directory you run it in - Claude Code's default;
          other projects will not see ssh-tool.{/if}
        {#if mcpWhere === "wsl"} Run it inside WSL; it points at the Windows binary.{/if}
      </p>

      {#if claudeDesktop?.supported}
        <p class="hint"><strong>Claude Desktop:</strong></p>
        {#if claudeDesktop.exists && !claudeDesktop.parseable}
          <p class="hint warn-note">
            <code>{claudeDesktop.path}</code> exists but is not valid JSON, so
            it can't be merged into safely. Fix or remove it, then reopen this
            page.
          </p>
        {:else}
          <div class="cd-row">
            <button
              class="primary"
              disabled={claudeDesktopBusy}
              onclick={registerClaudeDesktop}
            >
              {claudeDesktopBusy ? "Writing…"
                : claudeDesktop.stale ? "Update the path in Claude Desktop"
                : claudeDesktop.registered ? "Re-register"
                : "Add to Claude Desktop"}
            </button>
            {#if claudeDesktop.stale}
              <span class="cd-state warn">registered, but pointing at another binary</span>
            {:else if claudeDesktop.registered}
              <span class="cd-state ok">already registered</span>
            {/if}
          </div>
          <p class="hint">
            Merges an <code>ssh-tool</code> entry into
            <code>{claudeDesktop.path}</code>, leaving any other MCP servers in
            there untouched and keeping a <code>.bak</code> copy of the
            original. Claude Desktop reads the file at startup, so restart it
            afterwards.
          </p>
          {#if claudeDesktopMsg}<p class="hint cd-done">{claudeDesktopMsg}</p>{/if}
          {#if claudeDesktopErr}<p class="hint warn-note">{claudeDesktopErr}</p>{/if}
        {/if}
      {/if}

      <p class="hint"><strong>LM Studio</strong> (Program -> Edit mcp.json):</p>
      {@const lmJson = `{
  "mcpServers": {
    "ssh-tool": {
      "command": ${mcpExeJson},
      "args": ["--mcp-bridge"]
    }
  }
}`}
      <div class="cmd-wrap">
        <pre class="cmd-block">{lmJson}</pre>
        <button class="cmd-copy" title="Copy config" onclick={() => copyCmd(lmJson, "Config")}>
          <IconCopy size={13} />
        </button>
      </div>
      <p class="hint">
        Any MCP client works the same way - point its "command" at the path
        above with the <code>--mcp-bridge</code> argument.
      </p>

      <p class="hint">
        Then share a session: click the <strong>Share with LLM</strong> button
        in the pane toolbar (robot icon, next to tunnels). The LLM sees only
        shared sessions, and can also search and open your saved connections
        (with your approval). To let the LLM create connections from a pasted
        server list, turn on <strong>Allow manage</strong> below - that needs no
        open session.
      </p>
      <p class="hint">
        Paste the ssh-tool system prompt into your LLM client so it uses these
        tools well and treats terminal output as untrusted. It belongs where
        the client keeps instructions - <strong>Claude Desktop</strong>:
        Project -> Instructions, then chat inside that project;
        <strong>Claude Code</strong>: <code>CLAUDE.md</code>; others: the
        system-prompt field. Sent as an ordinary chat message it may simply be
        ignored, and that is the model behaving correctly.
      </p>
      <div class="cd-row">
        <button class="primary" onclick={copyMcpSystemPrompt}>Copy system prompt</button>
      </div>

      <fieldset class="check-cards">
        <label class:active={mcpTcp}>
          <input
            type="checkbox"
            checked={mcpTcp}
            onchange={(e) => toggleMcpTcp((e.target as HTMLInputElement).checked)}
          />
          <div>
            <div class="mode-name">Also listen on loopback TCP (for WSL / cross-boundary clients)</div>
            <div class="mode-desc">
              Needed when the LLM client runs in WSL but ssh-tool runs on
              Windows (WSL forwards localhost to the host but can't see the
              Windows pipe). Binds 127.0.0.1 only, guarded by a token the bridge
              reads from a private file. Leave off if the client runs on the same
              OS as ssh-tool.
            </div>
          </div>
        </label>
        <label class:active={mcpManage} data-setting="mcp-manage">
          <input
            type="checkbox"
            checked={mcpManage}
            onchange={(e) => toggleMcpManage((e.target as HTMLInputElement).checked)}
          />
          <div>
            <div class="mode-name">Allow manage (create connections)</div>
            <div class="mode-desc">
              Lets the LLM propose new folders, connections and port forwards -
              for example from a pasted server list - which you approve in one
              modal before anything is written. No session needs to be open. It
              never sets passwords; connections can only reference credentials
              already in your vault.
            </div>
            <div class="mode-desc warn">
              Off by default, and ends when you quit ssh-tool. A grant that
              writes to your whole tree should never come back on its own.
            </div>
          </div>
        </label>
      </fieldset>

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
  </div>

  {:else if activeSection === "sharing"}
  <div class="group">
    <h2>Share a session to a browser</h2>
    <p class="hint">
      Let a colleague watch - or, with your explicit approval, type into - a
      live session from a plain web browser, no ssh-tool needed on their side.
      Off by default. When on, right-click a tab and choose "Share to browser".
      The connection is encrypted; you confirm a short word-code with your guest
      to be sure no one is intercepting it, and every guest waits for you to
      allow them in.
    </p>

    <fieldset class="check-cards">
      <label class:active={shareEnabled}>
        <input
          type="checkbox"
          checked={shareEnabled}
          onchange={(e) => toggleShare((e.target as HTMLInputElement).checked)}
        />
        <div>
          <div class="mode-name">Enable session sharing</div>
          <div class="mode-desc">
            Makes the "Share to browser" action available. Each share picks its
            own network interface and is reachable only while it's running; a
            guest sees nothing until you approve them.
          </div>
        </div>
      </label>
    </fieldset>

    {#if shareEnabled}
      <div style="margin-top:1rem">
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

      <fieldset class="check-cards" style="margin-top:1rem">
        <label class:active={shareAuditOutput}>
          <input
            type="checkbox"
            checked={shareAuditOutput}
            onchange={toggleShareAuditOutput}
          />
          <div>
            <div class="mode-name">Record guest keystrokes in the audit log</div>
            <div class="mode-desc">
              Off by default. Who joined, when, and from where is always logged;
              this also stores what a controlling guest TYPES. The audit log is a
              plaintext file on disk, and guest keystrokes can include passwords -
              enable only if you accept that.
            </div>
          </div>
        </label>
      </fieldset>
    {/if}
  </div>

  {:else if activeSection === "desktop"}
  <div class="group">
    <h2>Desktop integration</h2>
    <p class="hint">
      How ssh-tool hooks into the rest of the desktop: where it lives,
      what launches it, and which links and menus open it. All of it is
      per-user - nothing here needs administrator rights.
    </p>

    <!-- Every registration below stores an absolute path to the
         executable. Moving the binary (installing it, or just dragging
         the download somewhere else) leaves them pointing at the old
         location: they keep working until that file is gone, then fail
         with nothing to explain why. Saying so beats letting the user
         discover it when a link stops opening. -->
    {#if urlSchemeInfo?.stale || explorerMenuInfo?.stale}
      <p class="hint warn-note">
        Some registrations still point at a different copy of ssh-tool
        than the one running ({installState?.exe_path}). They will keep
        launching that copy until re-registered below.
      </p>
    {/if}

    <!-- The MCP entry lives in Claude Desktop's config file, not in
         ours. Rewriting another application's configuration without
         being asked is a bigger liberty than re-pointing our own
         registrations, so this warns and links rather than fixing it
         silently. -->
    {#if claudeDesktop?.stale}
      <p class="hint warn-note">
        Claude Desktop's MCP entry points at
        <code>{claudeDesktop.path}</code>'s older command. Re-register it
        under <strong>LLM (MCP) access</strong>, then restart Claude
        Desktop.
      </p>
    {/if}

    <!-- Linux only, and only while there is something to do: a binary
         run from wherever it was downloaded works, but has no menu entry
         and no icon. The startup toast offers this once; this is where
         it lives permanently, for anyone who dismissed it. -->
    {#if installState?.can_offer}
      <h3 style="margin-top:0.8rem">Install to {menuName}</h3>
      {#if installState.replaces}
        <p class="hint">
          This copy is running from <code>{installState.exe_path}</code>,
          but {menuName} opens
          <code>{installState.target_path}</code>{installState.installed_version
            ? ` (${installState.installed_version})`
            : ""}. Until you replace it, launching ssh-tool from
          {menuName} keeps starting the other one.
        </p>
      {:else}
        <p class="hint">
          ssh-tool is running from <code>{installState.exe_path}</code>, so
          it has no entry in {menuName}. Installing copies it to
          <code>{installState.target_path}</code> and creates the shortcut.
          No administrator rights, and it stays updatable from inside the
          app.
        </p>
      {/if}
      <button class="btn" onclick={installToUserPrefix} disabled={installing}>
        {installing
          ? "Installing..."
          : installState.replaces
            ? "Replace the installed copy"
            : `Add to ${menuName}`}
      </button>
      {#if installMsg}
        <p class="hint" style="margin-top:0.4rem">{installMsg}</p>
      {/if}
      <fieldset class="check-cards" style="margin-top:0.6rem">
        <label class:active={installOfferDisabled}>
          <input
            type="checkbox"
            checked={installOfferDisabled}
            onchange={(e) => setInstallOfferDisabled((e.target as HTMLInputElement).checked)}
          />
          <div>
            <div class="mode-name">Don't offer this at startup</div>
            <div class="mode-desc">
              Stops the prompt appearing when ssh-tool is launched from
              somewhere it is not installed. This section stays here
              either way.
            </div>
          </div>
        </label>
      </fieldset>
    {/if}

    <!-- Removing what an install put in place. Only shown when there is
         something to remove, so it cannot be confused with uninstalling
         the app itself - the running binary and your data stay put. -->
    {#if installState && (installState.kind === "user" || installState.replaces || installState.desktop_entry)}
      <h3 style="margin-top:0.8rem">Remove desktop integration</h3>
      <p class="hint">
        Deletes the {menuName} entry and icon, and the copy in
        <code>{installState.kind === "user" ? installState.exe_path : installState.target_path}</code>.
        Your connections, credentials and settings are not touched.
      </p>
      <button class="btn" onclick={uninstallUserPrefix} disabled={uninstalling}>
        {uninstalling ? "Removing..." : "Remove"}
      </button>
      {#if uninstallMsg}
        <p class="hint" style="margin-top:0.4rem">{uninstallMsg}</p>
      {/if}
    {/if}

    {#if !isMobile && !settingsIsMac}
      <h2 style="margin-top: 1.5rem;" data-setting="file-manager">File manager</h2>
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
        <!-- Three states, not two: registered-and-current, registered-
             but-pointing-elsewhere, and absent. A stale registration
             needs re-pointing, and offering only "Remove" there sends
             the user the wrong way. -->
        {#if explorerMenuInfo?.stale}
          <button class="picker-btn" disabled={explorerMenuBusy} onclick={repointExplorerMenu}>
            {explorerMenuBusy ? "Working…" : "Re-point to this copy"}
          </button>
        {/if}
        <button class="picker-btn" disabled={explorerMenuBusy} onclick={toggleExplorerMenu}>
          {explorerMenuBusy ? "Working…" : explorerMenuStatus ? "Remove" : "Add to menu"}
        </button>
      </div>
      {#if explorerMenuInfo?.stale}
        <p class="hint warn-note">
          Points at <code>{explorerMenuInfo.target}</code>, not this copy.
          Right-clicking a folder will open that one instead.
        </p>
      {/if}
      {#if explorerMenuMsg}
        <p class="hint">{explorerMenuMsg}</p>
      {/if}
    {/if}

    <h2 style="margin-top: 1.5rem;" data-setting="url-scheme">Links</h2>
    <p class="hint">
      Binds <code>ssh-tool://</code> links to this app, so "Open in
      ssh-tool" buttons on a web page or in another tool launch it here.
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
      {#if urlSchemeInfo?.stale}
        <p class="hint warn-note">
          Points at <code>{urlSchemeInfo.target}</code>, not this copy.
          ssh-tool:// links will open that one instead.
        </p>
      {/if}
      {#if urlSchemeMsg}
        <p class="hint">{urlSchemeMsg}</p>
      {/if}
    {/if}
  </div>

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
        {#if aboutInfo?.commit_date && aboutInfo.commit_date !== "unknown"}
          <dt>Built from</dt>
          <dd>{aboutInfo.commit_date}</dd>
        {/if}
        <dt>Schema</dt>
        <dd><code>v{versionInfo.schema_version}</code></dd>
        {#if aboutInfo}
          <dt>Platform</dt>
          <dd>{aboutInfo.os_name ? aboutInfo.os_name + " " : ""}<span class="hint inline">({aboutInfo.os})</span></dd>
          <dt>Engine</dt>
          <dd>{aboutInfo.engine || "unknown"}</dd>
          <dt>Go</dt>
          <dd><code>{aboutInfo.go_version}</code></dd>
          <dt>Plugins</dt>
          <dd>
            {#each aboutInfo.plugins as p, i (p.name)}
              {i > 0 ? " · " : ""}{p.name}
              <span class="hint inline">{p.installed ? `${p.version || "installed"}${p.engine_version ? `, ${engineLabel(p.name)} ${p.engine_version}` : ""}` : "not installed"}</span>
            {/each}
          </dd>
          <dt>Data</dt>
          <dd class="about-path">
            <code>{aboutInfo.data_dir}</code>
            <button class="link-btn" onclick={() => api.aboutOpenDir("data").catch((e) => toast.err(String(e)))}>Open</button>
          </dd>
          <dt>Logs</dt>
          <dd class="about-path">
            <code>{aboutInfo.log_dir}</code>
            <button class="link-btn" onclick={() => api.aboutOpenDir("logs").catch((e) => toast.err(String(e)))}>Open</button>
          </dd>
        {/if}
      </dl>
      {#if aboutInfo}
        <details class="about-components">
          <summary>Components ({aboutInfo.components.length + frontendDeps.length})</summary>
          <dl class="about-list">
            {#each aboutInfo.components as c (c.name)}
              <dt>{c.name}</dt>
              <dd><code>{c.version}</code></dd>
            {/each}
            {#each frontendDeps as c (c.name)}
              <dt>{c.name}</dt>
              <dd><code>{c.version}</code></dd>
            {/each}
          </dl>
        </details>
      {/if}
      <div class="about-actions">
        <button onclick={copyDiagnostics} title="Version, platform and component versions as text for a bug report - no hosts or profile data">
          <IconCopy size={13} /> Copy diagnostics
        </button>
        <button class="link-btn" onclick={() => api.openURL("https://github.com/fpenezic/ssh-tool")}>GitHub</button>
        <button class="link-btn" onclick={() => api.openURL("https://github.com/fpenezic/ssh-tool/issues")}>Report an issue</button>
        <span class="hint inline">Apache 2.0</span>
      </div>
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
  </div>
  {/if}

  </div>
</section>

<style>
  /* Only there to disable the profile form during a save: no box of its own. */
  .np-fieldset { display: contents; border: 0; padding: 0; margin: 0; min-width: 0; }
  /* Same marker as the connection editor's "Unsaved changes" pill. */
  .dirty-note { display: inline-flex; align-items: center; gap: 0.3rem; color: var(--yellow); font-size: 0.78rem; font-weight: 600; }
  .dirty-note::before { content: ""; width: 7px; height: 7px; border-radius: 50%; background: var(--yellow); }
  /* Network profiles page: list beside the selected item. */
  .np-title { display: flex; align-items: baseline; gap: 0.8rem; }
  /* The count's own vertical margin would grow the row and push the
     heading down whenever a tunnel comes up. */
  .np-title .hint { margin: 0; }
  .np-split {
    display: grid;
    grid-template-columns: 15rem minmax(0, 1fr);
    gap: 1.2rem;
    margin-top: 0.8rem;
    align-items: start;
  }
  .np-list {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    position: sticky;
    top: 0;
  }
  .np-row, .np-new {
    display: flex;
    align-items: center;
    gap: 0.55rem;
    min-height: 2.6rem;
    padding: 0.3rem 0.6rem;
    border: 1px solid transparent;
    border-radius: 6px;
    background: transparent;
    color: var(--text);
    text-align: left;
    cursor: pointer;
  }
  .np-row:hover, .np-new:hover { background: var(--surface0); }
  .np-row.sel, .np-new.sel { background: var(--surface0); border-color: var(--blue); }
  .np-new { color: var(--blue); margin-top: 0.3rem; min-height: 2.2rem; }
  .np-row-text { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .np-row-text strong, .np-row-text small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .np-row-text small { color: var(--subtext0); font-size: 0.75rem; }
  .np-list-sep { height: 1px; background: var(--surface0); margin: 0.3rem 0; }
  .np-list-empty { margin: 0.3rem 0.6rem; }
  .np-dot {
    width: 9px; height: 9px; flex: none;
    border-radius: 50%;
    border: 2px solid var(--overlay0);
  }
  .np-dot.up { background: var(--green); border-color: var(--green); }
  .np-dot.paused { border-color: var(--yellow); }
  .np-dot.remote:not(.up) { border-color: var(--mauve); }
  .np-badge {
    font-size: 0.68rem;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    border: 1px solid var(--blue);
    color: var(--blue);
    white-space: nowrap;
  }
  .np-detail { min-width: 0; }
  .np-detail h3:first-child { margin-top: 0; }
  .np-h3 { margin-top: 1.4rem; }
  .np-name { margin: 0; }
  .np-sp-head { display: flex; align-items: center; gap: 0.8rem; }
  .np-sp-head h3 { margin: 0; }
  .np-sp-form { margin-top: 0.4rem; }
  .np-prio { margin-top: 0.6rem; }
  .np-prio .hint { margin: 0.2rem 0 0.4rem; }
  .np-prio-row { display: flex; align-items: center; gap: 0.4rem; min-height: 1.8rem; }
  .np-prio-row .oc-choice { min-width: 12rem; }
  .np-prio-btn { padding: 0.1rem 0.35rem; line-height: 1; }
  .np-which { gap: 0.5rem; margin-top: 0.6rem; max-width: 30rem; }
  .np-which input { flex: 1; }
  .ci-toml { width: 100%; font-family: var(--mono, monospace); font-size: 0.8rem; margin-top: 0.4rem; }
  .ci-log { max-height: 16rem; overflow: auto; }
  .np-field {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    font-size: 0.85rem;
    color: var(--subtext1);
  }
  .np-field select { max-width: 20rem; }
  .np-tabs {
    display: flex;
    gap: 0.2rem;
    border-bottom: 1px solid var(--surface0);
    margin: 0.8rem 0 0.9rem;
  }
  .np-tabs button {
    background: transparent;
    border: 0;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 0.45rem 0.9rem;
    color: var(--subtext0);
    cursor: pointer;
  }
  .np-tabs button.sel { color: var(--text); border-bottom-color: var(--blue); font-weight: 600; }
  .np-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.8rem 1rem; }
  .np-facts {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 0.35rem 1rem;
    margin: 1rem 0 0.4rem;
    font-size: 0.88rem;
  }
  .np-facts dt { color: var(--subtext0); }
  .np-facts dd { margin: 0; }
  .np-danger { margin-top: 1.4rem; padding-top: 0.8rem; border-top: 1px solid var(--surface0); }
  .np-danger .danger { color: var(--red); }
  .np-use { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.8rem; margin-top: 0.6rem; }
  .np-use-card {
    border: 1px solid var(--surface0);
    border-radius: 6px;
    padding: 0.7rem 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-width: 0;
  }
  .np-use-card .hint { margin: 0; }
  .np-code {
    font-size: 0.78rem;
    overflow-wrap: anywhere;
    background: var(--mantle);
    padding: 0.3rem 0.45rem;
    border-radius: 4px;
    user-select: text;
  }
  .hint.warn { color: var(--yellow); }
  @media (max-width: 760px) {
    .np-split { grid-template-columns: minmax(0, 1fr); }
    .np-list { position: static; }
    .np-grid, .np-use { grid-template-columns: minmax(0, 1fr); }
  }
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
  .np-proxy {
    margin-top: 0.5rem;
  }
  .np-info {
    margin: 0.4rem 0 0.3rem;
    padding: 0.5rem 0.6rem;
    background: var(--bg-2, rgba(127,127,127,0.08));
    border-radius: 4px;
    font-size: 0.8rem;
    white-space: pre-wrap;
    user-select: text;
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
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .np-actions button { padding: 0.3rem 0.6rem; }
  .np-act { display: inline-flex; align-items: center; gap: 0.35rem; }
  .np-sep { align-self: stretch; width: 1px; margin: 0.15rem 0.3rem; background: var(--surface1); }
  .np-act.danger { color: var(--red); border-color: var(--red); }
  .np-act.paused {
    color: var(--yellow);
    border-color: var(--yellow);
    background: color-mix(in srgb, var(--yellow) 14%, transparent);
  }
  .np-spacer { flex: 1; }
  /* OpenConnect form: two fixed columns, one per field pair; the base
     label style (column, top margin) is reset so rows line up. */
  .oc-form {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.75rem 1rem;
    align-items: start;
    margin-top: 0.8rem;
  }
  .oc-form.oc-nested { margin-top: 0.4rem; }
  .oc-field { display: flex; flex-direction: column; gap: 0.3rem; margin: 0; font-size: 0.85rem; color: var(--subtext1); min-width: 0; }
  .oc-field input:not([type="checkbox"]):not([type="radio"]), .oc-field select, .oc-field textarea { max-width: none; width: 100%; box-sizing: border-box; }
  .oc-span { grid-column: 1 / -1; }
  .oc-cert-head { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
  .oc-choices { display: flex; gap: 1rem; }
  .oc-choice { display: inline-flex; flex-direction: row; align-items: center; gap: 0.35rem; margin: 0; cursor: pointer; color: var(--text); }
  .oc-tunnel { display: flex; align-items: center; gap: 1rem; }
  .oc-tunnel input[type="number"] { width: 7rem !important; }
  .oc-field textarea { font-family: var(--mono, monospace); font-size: 0.8rem; }
  @media (max-width: 640px) {
    .oc-form { grid-template-columns: minmax(0, 1fr); }
  }
  .np-mode {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.78rem;
    color: var(--subtext0);
  }
  .np-actions button.danger { color: var(--red); }
  /* No global disabled look exists, and a disabled "Add profile" read as
     a button that silently did nothing. */
  button:disabled { opacity: 0.5; cursor: not-allowed; }
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
    flex-direction: row;
    margin-top: 0;
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
  /* Deep-link target (see flashSetting): three pulses of a mauve ring.
     Not blue - blue already marks a ticked option card. */
  :global(.setting-flash) {
    animation: setting-flash 0.85s ease-out 3;
    border-radius: 4px;
  }
  @keyframes -global-setting-flash {
    0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--mauve) 70%, transparent); background-color: color-mix(in srgb, var(--mauve) 14%, transparent); }
    70% { box-shadow: 0 0 0 8px transparent; background-color: color-mix(in srgb, var(--mauve) 6%, transparent); }
    100% { box-shadow: 0 0 0 0 transparent; background-color: transparent; }
  }
  @media (prefers-reduced-motion: reduce) {
    :global(.setting-flash) { animation: none; box-shadow: 0 0 0 2px var(--mauve); }
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
  /* The two scrollback rows sit directly above one another, so their inputs
     have to line up; without a fixed label column each starts wherever its
     own label happens to end. Scoped to these two rather than applied to
     .num generally - elsewhere (the audit filter/limit row) short labels in
     a wrapping flex row would be stretched by a min-width. */
  .num.scrollback-row > span:first-child {
    flex: 0 0 auto;
    min-width: 15rem;
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
  .about-path { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
  .about-path code { word-break: break-all; }
  /* Connection settings: one aligned grid per group - label, input,
     unit/default, reset. */
  .pref-grid {
    display: grid;
    grid-template-columns: 16rem 6rem minmax(0, 1fr) auto;
    align-items: center;
    gap: 0.35rem 0.6rem;
    margin: 0.3rem 0 0.4rem;
    max-width: 44rem;
  }
  .pref-grid > label { font-size: 0.85rem; color: var(--subtext1); }
  .pref-grid input[type="number"] {
    width: 100%; box-sizing: border-box;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface1); border-radius: 4px;
    padding: 0.25rem 0.45rem; font: inherit; font-size: 0.85rem;
  }
  .pref-grid .unit { color: var(--overlay1); font-size: 0.75rem; }
  @media (max-width: 640px) {
    .pref-grid { grid-template-columns: 1fr 6rem; }
    .pref-grid .unit, .pref-grid > :nth-child(4n) { grid-column: 1 / -1; }
  }
  .link-btn {
    background: none; border: 0; padding: 0;
    color: var(--sapphire); cursor: pointer; font: inherit; font-size: 0.8rem;
  }
  .link-btn:hover { text-decoration: underline; }
  .about-components { margin: 0 0 0.8rem; font-size: 0.85rem; }
  .about-components summary { cursor: pointer; color: var(--subtext0); }
  .about-actions { display: flex; align-items: center; gap: 0.9rem; flex-wrap: wrap; margin-bottom: 0.8rem; }
  .about-actions > button:first-child { display: inline-flex; align-items: center; gap: 0.35rem; }
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
  /* Claude Desktop registration row: button plus a state chip. */
  .cd-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin: 0.5rem 0;
    flex-wrap: wrap;
  }
  .cd-state {
    font-size: 0.8rem;
  }
  .cd-state.ok { color: var(--green); }
  .cd-state.warn { color: var(--yellow); }
  .cd-done {
    border-left: 3px solid var(--green);
    padding: 0.4rem 0.7rem;
    background: color-mix(in srgb, var(--green) 10%, transparent);
    border-radius: 0 4px 4px 0;
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
  /* ----- Audit insights ----- */
  .insights {
    margin: 0.9rem 0 1.1rem;
    padding: 0.8rem 0.9rem;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 6px;
  }
  .ins-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .ins-toggle {
    background: transparent;
    padding: 0;
    font-weight: 600;
    font-size: 0.95rem;
    color: var(--text);
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .ins-toggle:hover { background: transparent; color: var(--blue); }
  .chev {
    display: inline-block;
    transition: transform 0.12s ease;
    color: var(--overlay1);
    font-size: 0.8rem;
  }
  .chev.open { transform: rotate(90deg); }
  .ins-windows { display: flex; gap: 0.25rem; }
  .win {
    padding: 0.2rem 0.55rem;
    font-size: 0.78rem;
    background: var(--surface0);
    color: var(--subtext1);
    border-radius: 3px;
  }
  .win:hover { background: var(--surface1); color: var(--text); }
  .win.active {
    background: var(--blue);
    color: var(--on-accent);
    font-weight: 600;
  }

  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(7.5rem, 1fr));
    gap: 0.5rem;
    margin-top: 0.8rem;
  }
  .tile {
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 5px;
    padding: 0.55rem 0.7rem;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .tile.warn { border-color: var(--red); }
  .tile.warn .tv { color: var(--red); }
  .tv {
    font-size: 1.25rem;
    font-weight: 600;
    line-height: 1.1;
    font-variant-numeric: tabular-nums;
  }
  .tl {
    font-size: 0.74rem;
    color: var(--subtext0);
    text-transform: uppercase;
    letter-spacing: 0.02em;
  }
  .sub-note { margin-top: 0.6rem; font-size: 0.82rem; }

  .charts {
    display: grid;
    grid-template-columns: 2fr 1fr;
    gap: 0.9rem;
    margin-top: 0.9rem;
  }
  @media (max-width: 720px) {
    .charts { grid-template-columns: 1fr; }
  }
  .chart h4,
  .lst h4,
  .fails h4 {
    margin: 0 0 0.4rem;
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--subtext1);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .spark {
    display: flex;
    align-items: flex-end;
    gap: 1px;
    height: 62px;
    padding: 0.3rem 0.4rem;
    background: var(--crust);
    border: 1px solid var(--surface0);
    border-radius: 5px;
  }
  .spark.hours { gap: 2px; }
  .bar {
    flex: 1;
    min-width: 2px;
    background: var(--blue);
    border-radius: 1px 1px 0 0;
  }
  /* A quiet day is a real data point, so it keeps a visible stub
     rather than vanishing and shifting the dates either side. */
  .bar.zero { background: var(--surface1); }
  .bar.hour { background: var(--mauve); }
  .bar.hour.zero { background: var(--surface1); }
  .axis {
    display: flex;
    justify-content: space-between;
    font-size: 0.7rem;
    color: var(--subtext0);
    margin-top: 0.2rem;
    font-variant-numeric: tabular-nums;
  }

  .lists {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.9rem;
    margin-top: 0.9rem;
  }
  @media (max-width: 720px) {
    .lists { grid-template-columns: 1fr; }
  }
  .lrow {
    display: grid;
    grid-template-columns: minmax(4rem, 9rem) 1fr auto;
    align-items: center;
    gap: 0.5rem;
    padding: 0.15rem 0;
    font-size: 0.82rem;
  }
  .lname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text);
  }
  .lname.link {
    background: transparent;
    padding: 0;
    text-align: left;
    font-size: inherit;
    font-family: var(--font-mono, monospace);
    color: var(--subtext1);
    cursor: pointer;
  }
  .lname.link:hover { background: transparent; color: var(--blue); }
  .ltrack {
    height: 7px;
    background: var(--surface0);
    border-radius: 4px;
    overflow: hidden;
  }
  .lfill {
    display: block;
    height: 100%;
    background: var(--green);
    border-radius: 4px;
  }
  .lfill.bad { background: var(--red); }
  .lval {
    font-variant-numeric: tabular-nums;
    color: var(--subtext1);
    font-size: 0.78rem;
    white-space: nowrap;
  }
  .lsub { color: var(--overlay1); margin-left: 0.3rem; }

  .fails { margin-top: 0.9rem; }
  /* Gate mix: one stacked bar, so "how much ran unattended" reads as
     a proportion at a glance rather than four numbers to compare. */
  .gate-bar {
    display: flex;
    height: 10px;
    border-radius: 5px;
    overflow: hidden;
    background: var(--surface0);
    margin-bottom: 0.45rem;
  }
  .gseg { display: block; height: 100%; }
  .gseg.g-denied,
  .dot.g-denied { background: var(--red); }
  .gseg.g-yolo,
  .dot.g-yolo { background: var(--peach); }
  .gseg.g-auto,
  .dot.g-auto { background: var(--blue); }
  .gseg.g-approved,
  .dot.g-approved { background: var(--green); }
  .gseg.g-unknown,
  .dot.g-unknown { background: var(--overlay1); }
  .gate-chips { margin-top: 0.1rem; }
  /* The gap warning sits between the bar and the chips, where it is
     read before the numbers it qualifies. */
  .gap-note {
    margin: 0.45rem 0 0.5rem;
    padding-left: 0.6rem;
    border-left: 2px solid var(--peach);
    font-size: 0.82rem;
  }
  .chip.gate {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-family: inherit;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    display: inline-block;
  }
  .chips { display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .chip {
    font-size: 0.76rem;
    padding: 0.2rem 0.5rem;
    border-radius: 10px;
    background: var(--surface0);
    color: var(--subtext1);
    font-family: var(--font-mono, monospace);
  }
  .chip.bad { color: var(--red); }
  .chip.bad:hover { background: var(--surface1); }
  .chip b { color: var(--text); margin-left: 0.15rem; }

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
  /* Same active visual as the radio (.modes) cards: accent border +
     subtle accent-tinted fill, so checkbox and radio option cards
     read identically across Settings. */
  fieldset.check-cards label.active {
    border-color: var(--accent);
    background: color-mix(in oklab, var(--accent) 6%, transparent);
  }
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
  /* The manage grant is deliberately not persisted; that is a safety property
     worth reading, not a footnote at the end of a paragraph. */
  .mode-desc.warn {
    color: var(--yellow);
    margin-top: 0.4rem; padding: 0.3rem 0.45rem;
    background: color-mix(in srgb, var(--yellow) 12%, transparent);
    border-left: 2px solid var(--yellow);
    border-radius: 2px;
  }
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
  /* The copy button floats over the block's top-right corner. The block keeps
     user-select:all so selecting by hand still works for anyone who prefers it. */
  .scope-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin: 0.6rem 0 0.1rem;
  }
  .scope-seg {
    display: inline-flex;
    border: 1px solid var(--surface1);
    border-radius: 4px;
    overflow: hidden;
  }
  .scope-btn {
    background: var(--surface0);
    border: none;
    color: var(--subtext0);
    font-size: 0.72rem;
    padding: 0.2rem 0.5rem;
    cursor: pointer;
  }
  .scope-btn + .scope-btn { border-left: 1px solid var(--surface1); }
  .scope-btn:hover { color: var(--text); }
  .scope-btn.active { background: var(--blue); color: var(--on-accent); }

  .cmd-wrap { position: relative; }
  .cmd-wrap .cmd-block { padding-right: 2rem; }
  .cmd-copy {
    position: absolute;
    /* Top-aligned, sharing the block's own top padding, so it sits centred on
       a one-line command and tucks into the corner of the multi-line JSON
       instead of floating in the middle of it. */
    top: 0.5rem;
    right: 0.45rem;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0.15rem;
    background: var(--surface0);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    color: var(--subtext0);
    cursor: pointer;
    line-height: 0;
  }
  .cmd-copy:hover { color: var(--text); background: var(--surface1); }
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
  /* Tab strip inside the "External secrets" section - one row per
     backend (KeePass / Bitwarden / Infisical) so they share a single
     side-nav entry instead of three. */
  .ext-tabs {
    display: flex;
    gap: 0.25rem;
    border-bottom: 1px solid var(--surface0);
    margin-bottom: 1rem;
  }
  .ext-tab {
    background: transparent;
    color: var(--subtext0);
    border: 0;
    border-bottom: 2px solid transparent;
    padding: 0.45rem 0.9rem;
    font: inherit;
    font-size: 0.9rem;
    cursor: pointer;
    margin-bottom: -1px;
  }
  .ext-tab:hover { color: var(--text); }
  .ext-tab.active {
    color: var(--blue);
    border-bottom-color: var(--blue);
    font-weight: 500;
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
  .plugin-progress { display: flex; flex-direction: column; gap: 0.25rem; margin: 0.35rem 0; }
  .plugin-progress-bar { height: 4px; background: var(--surface0); border-radius: 2px; overflow: hidden; }
  .plugin-progress-fill { height: 100%; background: var(--blue); transition: width 0.15s linear; }
</style>
