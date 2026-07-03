// Thin TS facade over Wails-generated bindings.
//
// We define plain-object interfaces here rather than re-using the
// auto-generated classes (which carry a `convertValues` member that TS
// treats as required). That lets call sites write object literals freely.

import * as G from "../../bindings/ssh-tool/app.js";

// Wails v3 bindings declare Promise<T | null> for every call: Go nil
// returns map to JS null. Our app rarely tolerates a null payload (it
// would mean the IPC method returned a nil pointer without erroring),
// and existing callsites pre-date the new signature. nn() strips the
// null from a CancellablePromise so casts stay terse.
function nn<T>(p: PromiseLike<T | null>): Promise<T> {
  return Promise.resolve(p).then((v) => {
    if (v === null || v === undefined) {
      throw new Error("backend returned null");
    }
    return v;
  });
}

export interface JumpHostSpec {
  hostname: string;
  port?: number;
  username?: string;
  auth_ref?: string;
  via?: JumpHostSpec;
}

export interface JumpHostOverride {
  kind: "none" | "chain";
  chain?: JumpHostSpec;
}

export interface InheritableSettings {
  username?: string;
  port?: number;
  auth_ref?: string;
  jump_host?: JumpHostOverride;
  ssh_options?: Record<string, string>;
  env_vars?: Record<string, string>;
  color_tag?: string;
  broadcast_group_id?: string;
  keepalive_interval?: number;
  terminal_type?: string;
  auto_reconnect?: boolean;
  verbose?: boolean;
  vnc_enabled?: boolean;
  vnc_port?: number;
  vnc_use_tunnel?: boolean;
}

export interface Folder {
  id: string;
  parent_id: string | null;
  name: string;
  sort_order: number;
  settings: InheritableSettings;
  icon_image_id: string | null;
  created_at: number;
  updated_at: number;
}

export interface Connection {
  id: string;
  folder_id: string | null;
  name: string;
  hostname: string;
  sort_order: number;
  overrides: InheritableSettings;
  tags: string[];
  notes: string;
  favorite: boolean;
  sensitive: boolean;
  icon_image_id: string | null;
  last_used_at: number | null;
  created_at: number;
  updated_at: number;
  vnc_password_vault_key?: string;
}

export interface ResolvedSettings {
  hostname: string;
  username: string | null;
  port: number;
  auth_ref: string | null;
  jump_host: JumpHostSpec | null;
  ssh_options: Record<string, string>;
  env_vars: Record<string, string>;
  color_tag: string | null;
  broadcast_group_id: string | null;
  keepalive_interval: number;
  terminal_type: string;
  auto_reconnect: boolean;
  verbose: boolean;
  vnc_enabled: boolean;
  vnc_port: number;
  vnc_use_tunnel: boolean;
}

export interface VncSession {
  session_id: string;
  ws_url: string;
  username: string;
  password: string;
  title: string;
  // How the RFB upstream is reached: "direct", "jump:<host>", "tunnel" or
  // "proxmox". Drives the connecting-status message.
  transport: string;
}

export interface CredentialFolder {
  id: string;
  parent_id: string | null;
  name: string;
  sort_order: number;
  created_at: number;
  updated_at: number;
}

export interface CredentialRef {
  id: string;
  folder_id: string | null;
  name: string;
  kind: "password" | "key" | "agent" | "opkssh" | "vault" | "api_token";
  storage_mode: "managed" | "file_ref" | "external";
  hint: string;
  tags: string[];
  config: Record<string, unknown>;
  public_key: string | null;
  vault_key: string | null;
  default_username: string | null;
  last_rotated_at: number | null;
  expires_at: number | null;
  rotation_reminder_days: number | null;
  retain_history: boolean;
  icon_image_id: string | null;
  created_at: number;
  updated_at: number;
}

export interface CredentialHistoryEntry {
  id: string;
  credential_id: string;
  changed_at: number;
  note: string;
  rotated_by: string;
  has_value: boolean;
}

export interface CredentialSecretHistoryEntry {
  id: string;
  credential_id: string;
  rotated_at: number;
  vault_account: string;
  note: string;
  rotated_by: string;
}

export interface UsageRef {
  kind: "folder" | "connection";
  id: string;
  name: string;
  hostname?: string;
}

export interface AuditEvent {
  id: number;
  ts: number;
  action: string;
  target: string;
  metadata: Record<string, string>;
}

export interface VaultStatus {
  state: "not_initialized" | "locked" | "unlocked";
  auto_unlock_available?: boolean;
}

export interface GenerateKeyParams {
  key_type: "ed25519" | "rsa" | "ecdsa";
  bits?: number;
  comment: string;
  passphrase?: string;
}

// Optional folder placement shared by every credential kind.
interface CommonCreateExtras {
  folder_id?: string;
}

export type CredentialCreateInput =
  | ({
      kind: "password";
      name: string;
      password: string;
      hint?: string;
      tags?: string[];
      default_username?: string;
      rotation_reminder_days?: number;
    } & CommonCreateExtras)
  | ({
      kind: "key_generate";
      name: string;
      params: GenerateKeyParams;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "key_import_paste";
      name: string;
      private_openssh: string;
      passphrase?: string;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "key_file_ref";
      name: string;
      key_path: string;
      passphrase?: string;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "agent";
      name: string;
      socket_path?: string;
      fingerprint?: string;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "opkssh";
      name: string;
      key_basename: string;
      config_path?: string;
      provider_hint?: string;
      max_cert_age_hours?: number;
      min_remaining_before_refresh_minutes?: number;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "api_token";
      name: string;
      api_token_id: string;
      api_token_secret: string;
      hint?: string;
      tags?: string[];
    } & CommonCreateExtras);

export interface CredentialCreateResult {
  credential?: CredentialRef;
  public_key?: string;
  fingerprint?: string;
}

export const api = {
  ping: (name: string) => G.Ping(name),
  setWindowTitle: (title: string) => G.SetWindowTitle(title),

  foldersList: () => G.FoldersList() as unknown as Promise<Folder[]>,
  foldersGet: (id: string) => G.FoldersGet(id) as unknown as Promise<Folder>,
  foldersCreate: (input: {
    parentId?: string;
    name: string;
    sortOrder?: number;
    settings?: InheritableSettings;
  }) =>
    G.FoldersCreate({
      parent_id: input.parentId,
      name: input.name,
      sort_order: input.sortOrder ?? 0,
      settings: input.settings ?? ({} as InheritableSettings),
    } as any),
  foldersUpdate: (input: {
    id: string;
    parentId?: string;
    clearParent?: boolean;
    name?: string;
    sortOrder?: number;
    settings?: InheritableSettings;
  }) =>
    G.FoldersUpdate({
      id: input.id,
      parent_id: input.parentId,
      clear_parent: input.clearParent ?? false,
      name: input.name,
      sort_order: input.sortOrder,
      settings: input.settings,
    } as any),
  foldersDelete: (id: string) => G.FoldersDelete(id),

  connectionsList: (folderId?: string) =>
    G.ConnectionsList(folderId ?? null) as unknown as Promise<Connection[]>,
  connectionsGet: (id: string) =>
    G.ConnectionsGet(id) as unknown as Promise<Connection>,
  connectionsCreate: (input: {
    folderId?: string;
    name: string;
    hostname: string;
    sortOrder?: number;
    overrides?: InheritableSettings;
    tags?: string[];
    notes?: string;
  }) =>
    nn(G.ConnectionsCreate({
      folder_id: input.folderId,
      name: input.name,
      hostname: input.hostname,
      sort_order: input.sortOrder ?? 0,
      overrides: input.overrides ?? ({} as InheritableSettings),
      tags: input.tags ?? [],
      notes: input.notes ?? "",
    } as any)),
  connectionsUpdate: (input: {
    id: string;
    folderId?: string;
    clearFolder?: boolean;
    name?: string;
    hostname?: string;
    sortOrder?: number;
    overrides?: InheritableSettings;
    tags?: string[];
    notes?: string;
    favorite?: boolean;
    sensitive?: boolean;
  }) =>
    G.ConnectionsUpdate({
      id: input.id,
      folder_id: input.folderId,
      clear_folder: input.clearFolder ?? false,
      name: input.name,
      hostname: input.hostname,
      sort_order: input.sortOrder,
      overrides: input.overrides,
      tags: input.tags,
      notes: input.notes,
      favorite: input.favorite,
      sensitive: input.sensitive,
    } as any),
  connectionsDelete: (id: string) => G.ConnectionsDelete(id),
  connectionsClone: (id: string) => G.ConnectionsClone(id) as Promise<Connection | null>,
  connectionsRecent: () => G.ConnectionsRecent() as Promise<Connection[]>,
  connectionsFavorites: () => G.ConnectionsFavorites() as Promise<Connection[]>,
  connectionsSetFavorite: (id: string, fav: boolean) => G.ConnectionsSetFavorite(id, fav),
  broadcastList: () => G.BroadcastList() as unknown as Promise<string[]>,
  broadcastAdd: (sessionId: string) => G.BroadcastAdd(sessionId),
  broadcastRemove: (sessionId: string) => G.BroadcastRemove(sessionId),
  broadcastClear: () => G.BroadcastClear(),
  broadcastSetAll: (ids: string[]) => G.BroadcastSetAll(ids),
  broadcastFanOut: (originId: string, b64: string) =>
    G.BroadcastFanOut(originId, b64) as unknown as Promise<string>,
  broadcastListGroups: () =>
    G.BroadcastListGroups() as unknown as Promise<Record<string, string[]>>,
  broadcastAddTo: (groupId: string, sessionId: string) =>
    G.BroadcastAddTo(groupId, sessionId),
  broadcastRemoveFrom: (groupId: string, sessionId: string) =>
    G.BroadcastRemoveFrom(groupId, sessionId),
  broadcastClearGroup: (groupId: string) => G.BroadcastClearGroup(groupId),
  broadcastGroupDelete: (groupId: string) => G.BroadcastGroupDelete(groupId),
  broadcastSetAllInGroup: (groupId: string, ids: string[]) =>
    G.BroadcastSetAllInGroup(groupId, ids),
  pathIsDir: (path: string) => G.PathIsDir(path) as unknown as Promise<boolean>,
  imagesUpload: (b64: string, mime: string) => G.ImagesUpload(b64, mime) as Promise<string>,
  imagesList: () => G.ImagesList() as unknown as Promise<Array<{ id: string; mime: string; use_count: number }>>,
  imagesSetFolder: (folderId: string, imageId: string) => G.ImagesSetFolder(folderId, imageId),
  imagesSetConnection: (connId: string, imageId: string) => G.ImagesSetConnection(connId, imageId),
  imagesSetCredential: (credId: string, imageId: string) => G.ImagesSetCredential(credId, imageId),
  connectionsBatchUpdate: (input: {
    ids: string[];
    patch: InheritableSettings;
    clear_fields: string[];
    add_tags?: string[];
    remove_tags?: string[];
  }) =>
    G.ConnectionsBatchUpdate(input as any) as unknown as Promise<{ updated: number }>,
  connectionsTouch: (id: string) => G.ConnectionsTouch(id),
  connectionsResolve: (id: string) =>
    G.ConnectionsResolve(id) as unknown as Promise<ResolvedSettings>,

  credentialsList: () =>
    G.CredentialsList() as unknown as Promise<CredentialRef[]>,
  credentialsGet: (id: string) =>
    G.CredentialsGet(id) as unknown as Promise<CredentialRef>,
  credentialsCreate: (input: CredentialCreateInput) =>
    G.CredentialsCreate(input as any) as unknown as Promise<CredentialCreateResult>,
  credentialsUpdate: (input: {
    id: string;
    kind?: string;
    folder_id?: string;
    set_folder_to_null?: boolean;
    name?: string;
    hint?: string;
    default_username?: string;
    set_default_username_to_null?: boolean;
    config?: Record<string, any>;
    set_public_key_to_null?: boolean;
  }) => G.CredentialsUpdate(input as any) as unknown as Promise<CredentialRef>,
  credentialsDelete: (id: string) => G.CredentialsDelete(id),
  credentialsRotatePassword: (id: string, newPassword: string) =>
    G.CredentialsRotatePassword(id, newPassword) as unknown as Promise<CredentialRef>,
  credentialsRevealSecret: (id: string) =>
    G.CredentialsRevealSecret(id) as unknown as Promise<string>,
  credentialsRotateKey: (input: {
    id: string;
    generate_new: boolean;
    private_openssh?: string;
    passphrase?: string;
  }) => G.CredentialsRotateKey(input as any) as unknown as Promise<CredentialRef>,
  credentialsRotateAPIToken: (input: {
    id: string;
    token_id?: string | null;   // null = leave unchanged
    new_secret?: string;        // "" = leave unchanged
  }) => G.CredentialsRotateAPIToken(input as any) as unknown as Promise<CredentialRef>,
  credentialsUsage: (id: string) =>
    G.CredentialsUsage(id) as unknown as Promise<UsageRef[]>,
  credentialsHistory: (id: string) =>
    G.CredentialsHistory(id) as unknown as Promise<CredentialHistoryEntry[]>,
  credentialsSecretHistory: (id: string) =>
    G.CredentialsSecretHistory(id) as unknown as Promise<CredentialSecretHistoryEntry[]>,
  credentialsRevealSecretHistory: (historyId: string) =>
    G.CredentialsRevealSecretHistory(historyId) as unknown as Promise<string>,
  credentialsDeleteSecretHistory: (historyId: string) =>
    G.CredentialsDeleteSecretHistory(historyId),

  credentialFoldersList: () =>
    G.CredentialFoldersList() as unknown as Promise<CredentialFolder[]>,
  credentialFoldersCreate: (name: string, parentId?: string) =>
    G.CredentialFoldersCreate(name, parentId ?? null) as unknown as Promise<CredentialFolder>,
  credentialFoldersUpdate: (id: string, name?: string, parentId?: string, clearParent?: boolean) =>
    G.CredentialFoldersUpdate(id, name ?? null, parentId ?? null, clearParent ?? false) as unknown as Promise<CredentialFolder>,
  credentialFoldersDelete: (id: string) => G.CredentialFoldersDelete(id),

  vaultStatus: () => G.VaultStatus() as unknown as Promise<VaultStatus>,
  vaultInit: (passphrase: string, rememberOnMachine: boolean) =>
    G.VaultInit(passphrase, rememberOnMachine),
  vaultUnlock: (passphrase: string, rememberOnMachine: boolean) =>
    G.VaultUnlock(passphrase, rememberOnMachine),
  vaultAutoUnlock: () => G.VaultAutoUnlock(),
  vaultLock: (forgetSidecar: boolean) => G.VaultLock(forgetSidecar),
  vaultChangePassphrase: (oldPassphrase: string, newPassphrase: string) =>
    G.VaultChangePassphrase(oldPassphrase, newPassphrase),

  auditList: (action: string, limit: number, before: number) =>
    G.AuditList({ action, limit, before }) as unknown as Promise<AuditEvent[]>,
  auditPurge: (olderThanDays: number) => G.AuditPurge(olderThanDays) as unknown as Promise<number>,

  backupsCreate: (passphrase: string, destPath?: string) =>
    nn(G.BackupsCreate({ passphrase, dest_path: destPath ?? "" })) as Promise<BackupCreateResult>,
  backupsList: () => G.BackupsList() as unknown as Promise<BackupInfo[]>,
  backupsRestore: (srcPath: string, passphrase: string) =>
    G.BackupsRestore({ src_path: srcPath, passphrase }),
  backupsDelete: (path: string) => G.BackupsDelete(path),
  autoBackupPrefsGet: () => G.AutoBackupPrefsGet() as unknown as Promise<AutoBackupPrefs>,
  autoBackupPrefsSet: (prefs: AutoBackupPrefs) => G.AutoBackupPrefsSet(prefs),

  sshConnect: (connectionId: string) => nn(G.SshConnect(connectionId)),
  // overrideCredentialId: empty string falls through to SshConnect
  // behaviour (use the connection's persisted auth_ref). Non-empty
  // forces this credential for the target hop on this one attempt
  // only - the persisted auth_ref is left untouched.
  sshConnectWithOverride: (connectionId: string, overrideCredentialId: string) =>
    nn(G.SshConnectWithOverride(connectionId, overrideCredentialId)),
  sshConnectAdvanced: (connectionId: string, overrideCredentialId: string, overrideUsername: string, overridePassword: string) =>
    nn(G.SshConnectAdvanced(connectionId, overrideCredentialId, overrideUsername, overridePassword)),
  // One-shot server health probe (load / memory / disk / users) for the
  // focused session's host. ok=false means the host answered but nothing
  // parsed (network gear, non-Linux); rejects if the session is gone.
  sshServerStats: (sessionId: string) =>
    G.SshServerStats(sessionId) as unknown as Promise<ServerStats | null>,
  // Abort an in-flight connect (e.g. hung on opkssh OIDC login) by its
  // connection id. No-op if nothing is connecting for that id.
  sshCancelConnect: (connectionId: string) =>
    G.SshCancelConnect(connectionId) as unknown as Promise<boolean>,
  sshGetConnectDebug: (connectionId: string) =>
    G.SshGetConnectDebug(connectionId) as unknown as Promise<string[]>,
  appGetLogs: () => G.AppGetLogs() as unknown as Promise<string[]>,
  appClearLogs: () => G.AppClearLogs(),
  frontendLog: (line: string) => G.FrontendLog(line),
  appGetLogTailEnabled: () => G.AppGetLogTailEnabled() as unknown as Promise<boolean>,
  appSetLogTailEnabled: (on: boolean) => G.AppSetLogTailEnabled(on),
  setConnectionPassword: (connectionId: string, password: string) =>
    G.SetConnectionPassword(connectionId, password),
  clearConnectionPassword: (connectionId: string) =>
    G.ClearConnectionPassword(connectionId),
  getConnectionHasPassword: (connectionId: string) =>
    G.GetConnectionHasPassword(connectionId) as unknown as Promise<boolean>,
  sshWrite: (sessionId: string, dataB64: string) => G.SshWrite(sessionId, dataB64),
  sshGetScrollback: (sessionId: string) =>
    G.SshGetScrollback(sessionId) as unknown as Promise<{ b64: string; cum: number }>,
  sshResize: (sessionId: string, cols: number, rows: number) =>
    G.SshResize(sessionId, cols, rows),
  sshDisconnect: (sessionId: string) => G.SshDisconnect(sessionId),
  sshCancelReconnect: (oldSessionId: string) => G.SshCancelReconnect(oldSessionId),
  sshSystemCommand: (connectionId: string) =>
    G.SshSystemCommand(connectionId) as unknown as Promise<string>,
  sshLaunchInSystemTerminal: (connectionId: string) =>
    G.SshLaunchInSystemTerminal(connectionId),
  openURL: (url: string) => G.OpenURL(url),
  logDir: () => G.LogDir() as unknown as Promise<string>,
  appVersion: () => G.AppVersion() as unknown as Promise<AppVersionInfo>,
  snippetsList: (connectionId: string) =>
    G.SnippetsList(connectionId) as unknown as Promise<Snippet[]>,
  snippetCreate: (input: SnippetInput) =>
    nn(G.SnippetCreate(input as any)) as Promise<Snippet>,
  snippetUpdate: (id: string, input: SnippetInput) =>
    nn(G.SnippetUpdate(id, input as any)) as Promise<Snippet>,
  snippetDelete: (id: string) => G.SnippetDelete(id),
  snippetSendToSession: (snippetId: string, sessionId: string) =>
    G.SnippetSendToSession(snippetId, sessionId),
  tcpdumpProbe: (sessionId: string) =>
    nn(G.TcpdumpProbe(sessionId)) as Promise<TcpdumpProbeResult>,
  tcpdumpListInterfaces: (sessionId: string) =>
    G.TcpdumpListInterfaces(sessionId) as unknown as Promise<string[]>,
  tcpdumpStart: (input: TcpdumpStartInput) =>
    G.TcpdumpStart(input as any) as unknown as Promise<string>,
  tcpdumpProvidePassword: (dumpId: string, password: string) =>
    G.TcpdumpProvidePassword(dumpId, password),
  tcpdumpStop: (dumpId: string) => G.TcpdumpStop(dumpId),
  tcpdumpCheckRoute: (sessionId: string, queries: TcpdumpRouteQuery[]) =>
    G.TcpdumpCheckRoute(sessionId, queries as any) as unknown as Promise<RouteResult[]>,
  tcpdumpActiveForSession: (sessionId: string) =>
    nn(G.TcpdumpActiveForSession(sessionId)) as Promise<TcpdumpActiveInfo>,
  tcpdumpSnapshot: (dumpId: string) =>
    nn(G.TcpdumpSnapshot(dumpId)) as Promise<TcpdumpSnapshotResult>,
  recordingStart: (sessionId: string) =>
    nn(G.RecordingStart(sessionId)) as Promise<RecordingState>,
  recordingStop: (sessionId: string) =>
    nn(G.RecordingStop(sessionId)) as Promise<RecordingState>,
  recordingActive: () =>
    G.RecordingActive() as unknown as Promise<RecordingState[]>,
  recordingsDir: () => G.RecordingsDir() as unknown as Promise<string>,
  recordingsPickDir: () =>
    G.RecordingsPickDir() as unknown as Promise<string>,
  recordingsOpenDir: () => G.RecordingsOpenDir(),
  recordingsList: () =>
    G.RecordingsList() as unknown as Promise<RecordingFileInfo[]>,
  recordingRead: (path: string) =>
    G.RecordingRead(path) as unknown as Promise<string>,
  recordingDelete: (path: string) => G.RecordingDelete(path),
  opksshCertStatus: (credentialId: string) =>
    nn(G.OpksshCertStatus(credentialId)) as Promise<OpksshCertStatus>,
  syncConfigGet: () => G.SyncConfigGet() as unknown as Promise<SyncConfig>,
  syncConfigSet: (url: string, username: string, webdavPassword: string, passphrase: string) =>
    G.SyncConfigSet(url, username, webdavPassword, passphrase),
  syncTransportSet: (transport: string) => G.SyncTransportSet(transport),
  syncSftpConfigSet: (input: SyncSftpConfigInput) =>
    G.SyncSftpConfigSet(input as any),
  syncStatus: () => nn(G.SyncStatus()) as Promise<SyncStatusResult>,
  syncPush: (force: boolean) =>
    nn(G.SyncPush(force)) as Promise<{ generation: number; snapshot_size: number }>,
  syncPull: () =>
    nn(G.SyncPull()) as Promise<{ generation: number; device: string; updated_at: string }>,
  syncPullLive: () =>
    nn(G.SyncPullLive()) as Promise<{ generation: number; device: string; updated_at: string; vault_restart_needed: boolean }>,
  syncAutoApplySet: (enabled: boolean) => G.SyncAutoApplySet(enabled),
  syncAutoSet: (enabled: boolean, checkMinutes: number) =>
    G.SyncAutoSet(enabled, checkMinutes),
  appRelaunch: () => G.AppRelaunch(),
  httpDo: (req: HttpRequest) =>
    nn(G.HttpDo(req as any)) as Promise<HttpResponse>,
  batchExec: (input: BatchExecInput) =>
    G.BatchExec(input as any) as unknown as Promise<BatchHostResult[]>,
  workspacesList: () =>
    G.WorkspacesList() as unknown as Promise<Workspace[]>,
  workspaceCreate: (name: string, layoutJSON: string) =>
    nn(G.WorkspaceCreate(name, layoutJSON)) as Promise<Workspace>,
  workspaceUpdate: (id: string, name: string, layoutJSON: string) =>
    nn(G.WorkspaceUpdate(id, name, layoutJSON)) as Promise<Workspace>,
  workspaceDelete: (id: string) => G.WorkspaceDelete(id),
  workspaceTouchLastOpened: (id: string) => G.WorkspaceTouchLastOpened(id),
  connectionRevealPassword: (connectionId: string) =>
    G.ConnectionRevealPassword(connectionId) as unknown as Promise<string>,

  // ----- multi-window -----
  windowDetachTab: (tabId: string, sessions: string, layout: string) =>
    G.WindowDetachTab(tabId, sessions, layout) as unknown as Promise<string>,
  windowDetachTabAt: (tabId: string, screenX: number, screenY: number, sessions: string, layout: string) =>
    G.WindowDetachTabAt(tabId, screenX, screenY, sessions, layout) as unknown as Promise<string>,
  windowRedockTab: (tabId: string, sessions: string, layout: string) =>
    G.WindowRedockTab(tabId, sessions, layout),
  windowCloseSelf: (windowName: string) => G.WindowCloseSelf(windowName),
  windowStartTabDrag: (tabId: string, sessions: string, layout: string) =>
    G.WindowStartTabDrag(tabId, sessions, layout),
  windowAcceptTabDrag: () =>
    G.WindowAcceptTabDrag() as unknown as Promise<{ tab_id: string; sessions: string; layout: string } | null>,
  windowCancelTabDrag: () => G.WindowCancelTabDrag(),
  connectionCopyInfo: (connectionId: string) =>
    G.ConnectionCopyInfo(connectionId) as unknown as Promise<{
      username: string;
      hostname: string;
      port: number;
      has_password: boolean;
      ssh_command: string;
    }>,
  sshActiveSessions: () =>
    G.SshActiveSessions() as unknown as Promise<
      { session_id: string; connection_id: string; name: string; hostname: string }[]
    >,
  sshActiveSessionCount: () => G.SshActiveSessionCount() as unknown as Promise<number>,
  confirmQuit: () => G.ConfirmQuit(),
  launchExternalTerminal: (connectionId: string, kind: string) =>
    G.LaunchExternalTerminal(connectionId, kind) as unknown as Promise<void>,
  openNativeTerminal: (kind: string) =>
    G.OpenNativeTerminal(kind) as unknown as Promise<void>,

  localShellOpen: (kind: string, dir: string, cols: number, rows: number) =>
    G.LocalShellOpen(kind, dir, cols, rows) as unknown as Promise<{
      session_id: string;
      kind: string;
      display: string;
    }>,
  localShellWrite: (sessionId: string, dataB64: string) =>
    G.LocalShellWrite(sessionId, dataB64),
  localShellResize: (sessionId: string, cols: number, rows: number) =>
    G.LocalShellResize(sessionId, cols, rows),
  localShellDisconnect: (sessionId: string) => G.LocalShellDisconnect(sessionId),
  localShellGetScrollback: (sessionId: string) =>
    G.LocalShellGetScrollback(sessionId) as unknown as Promise<{ b64?: string; cum: number }>,
  localShellList: () =>
    G.LocalShellList() as unknown as Promise<
      { session_id: string; kind: string; display: string }[]
    >,

  // VNC console. Each open returns a loopback ws URL (single-use token)
  // plus an optional RFB password noVNC presents.
  vncOpenProxmox: (folderId: string, entryId: string) =>
    G.VncOpenProxmox(folderId, entryId) as unknown as Promise<VncSession>,
  vncOpenConnection: (connectionId: string) =>
    G.VncOpenConnection(connectionId) as unknown as Promise<VncSession>,
  vncOpenPinnedProxmox: (connectionId: string) =>
    G.VncOpenPinnedProxmox(connectionId) as unknown as Promise<VncSession>,
  vncClose: (sessionId: string) => G.VncClose(sessionId),
  vncLastError: (sessionId: string) =>
    G.VncLastError(sessionId) as unknown as Promise<string>,
  vncSessionList: () =>
    G.VncSessionList() as unknown as Promise<VncSession[]>,
  clipboardGetText: () => G.ClipboardGetText() as unknown as Promise<string>,
  clipboardSetText: (text: string) =>
    G.ClipboardSetText(text) as unknown as Promise<boolean>,
  setConnectionVncPassword: (connectionId: string, password: string) =>
    G.SetConnectionVncPassword(connectionId, password),
  clearConnectionVncPassword: (connectionId: string) =>
    G.ClearConnectionVncPassword(connectionId),
  getConnectionHasVncPassword: (connectionId: string) =>
    G.GetConnectionHasVncPassword(connectionId) as unknown as Promise<boolean>,

  dynamicFolderCreate: (input: {
    parent_id?: string | null;
    name: string;
    settings: any;
    provider: string;
    config: Record<string, any>;
    refresh_seconds: number;
  }) => G.DynamicFolderCreate(input as any) as unknown as Promise<{ id: string; name: string; parent_id: string | null }>,
  dynamicFolderUpdate: (input: {
    folder_id: string;
    provider: string;
    config: Record<string, any>;
    refresh_seconds: number;
  }) => G.DynamicFolderUpdate(input as any),
  dynamicFolderGet: (folderId: string) =>
    G.DynamicFolderGet(folderId) as unknown as Promise<{
      folder_id: string;
      provider: string;
      config: Record<string, any>;
      refresh_seconds: number;
      last_pulled_at: number | null;
      last_error: string;
    } | null>,
  dynamicFoldersList: () =>
    G.DynamicFoldersList() as unknown as Promise<Array<{
      folder_id: string;
      provider: string;
      config: Record<string, any>;
      refresh_seconds: number;
      last_pulled_at: number | null;
      last_error: string;
    }>>,
  dynamicFolderRefreshNow: (folderId: string) =>
    G.DynamicFolderRefreshNow(folderId),
  dynamicEntriesList: (folderId: string) =>
    G.DynamicEntriesList(folderId) as unknown as Promise<Array<{
      id: string;
      external_id: string;
      name: string;
      hostname: string;
      kind: string;
      status: string;
      tags: string[];
      raw?: any;
    }>>,
  sshConnectDynamic: (folderId: string, entryId: string) =>
    G.SshConnectDynamic(folderId, entryId) as unknown as Promise<{ session_id: string }>,
  sshConnectDynamicWithOverride: (folderId: string, entryId: string, overrideCredentialId: string) =>
    G.SshConnectDynamicWithOverride(folderId, entryId, overrideCredentialId) as unknown as Promise<{ session_id: string }>,
  sshConnectDynamicAdvanced: (folderId: string, entryId: string, overrideCredentialId: string, overrideUsername: string, overridePassword: string) =>
    G.SshConnectDynamicAdvanced(folderId, entryId, overrideCredentialId, overrideUsername, overridePassword) as unknown as Promise<{ session_id: string }>,
  sshConnectDynamicWithJumpOverride: (folderId: string, entryId: string, overrideCredentialId: string, overrideUsername: string, overridePassword: string, jumpHostOverride: string, jumpCredentialOverride: string) =>
    G.SshConnectDynamicWithJumpOverride(folderId, entryId, overrideCredentialId, overrideUsername, overridePassword, jumpHostOverride, jumpCredentialOverride) as unknown as Promise<{ session_id: string }>,
  pinDynamicEntry: (input: {
    folder_id: string;
    entry_id: string;
    target_folder_id?: string;
    name?: string;
    override_credential_id?: string;
    tags?: string[];
  }) => G.PinDynamicEntry(input as any) as unknown as Promise<any>,
  unpinConnection: (connectionId: string) =>
    G.UnpinConnection(connectionId) as unknown as Promise<string>,
  convertDynamicFolderToStatic: (folderId: string) =>
    G.ConvertDynamicFolderToStatic(folderId) as unknown as Promise<number>,
  checkForUpdate: () =>
    G.CheckForUpdate() as unknown as Promise<{
      current: string;
      latest: string;
      is_newer: boolean;
      changelog_url: string;
      download_url: string;
      download_size?: number;
      released_at: string;
      error?: string;
    }>,
  // No url / script-path params: the backend downloads and applies
  // only what it derived from its own update check.
  downloadUpdate: () =>
    G.DownloadUpdate() as unknown as Promise<{
      staged_path: string;
      size: number;
      sha256: string;
      verified: boolean;
      apply_script?: string;
      needs_restart: boolean;
    }>,
  applyUpdate: () => G.ApplyUpdate(),
  fetchReleaseNotes: (version: string) =>
    G.FetchReleaseNotes(version) as unknown as Promise<{
      version: string;
      released_at: string;
      notes_md: string;
      error?: string;
    }>,

  pickAnsibleInventoryFile: () =>
    G.PickAnsibleInventoryFile() as unknown as Promise<string>,

  forwardsList: (connectionId: string) =>
    G.ForwardsList(connectionId) as unknown as Promise<PortForward[]>,
  forwardsListAll: () => G.ForwardsListAll() as unknown as Promise<PortForward[]>,
  forwardsCreate: (input: ForwardCreateInput) =>
    G.ForwardsCreate(input as any) as unknown as Promise<PortForward>,
  forwardsUpdate: (input: ForwardUpdateInput) =>
    G.ForwardsUpdate(input as any) as unknown as Promise<PortForward>,
  forwardsDelete: (id: string) => G.ForwardsDelete(id),
  forwardsSetBookmarks: (forwardId: string, bookmarks: ProxyBookmark[]) =>
    G.ForwardsSetBookmarks(forwardId, bookmarks as any),

  forwardsActive: (sessionId: string) =>
    G.ForwardsActive(sessionId) as unknown as Promise<ForwardStatus[]>,
  forwardsStart: (forwardId: string, sessionId: string) =>
    G.ForwardsStart(forwardId, sessionId) as unknown as Promise<ForwardStatus>,
  forwardsStop: (forwardId: string) => G.ForwardsStop(forwardId),
  sshLaunchBrowser: (forwardId: string, url: string) =>
    G.SshLaunchBrowser(forwardId, url) as unknown as Promise<{ pid: number }>,

  settingsGet: (key: string) => G.SettingsGet(key) as unknown as Promise<string>,
  settingsSet: (key: string, value: string) => G.SettingsSet(key, value),
  settingsDelete: (key: string) => G.SettingsDelete(key),

  sshRespondHostKey: (challengeId: string, accept: boolean, remember: boolean, hostname: string, port: number, keyType: string, keyB64: string, fingerprint: string) =>
    G.SshRespondHostKey(challengeId, accept, remember, hostname, port, keyType, keyB64, fingerprint),

  imagesGet: (id: string) =>
    G.ImagesGet(id) as unknown as Promise<{ mime: string; b64: string }>,

  rdmImport: (jsonText: string, rootFolderID?: string) =>
    G.RdmImport(jsonText, rootFolderID ?? "") as unknown as Promise<RdmImportSummary>,

  sshConfigImport: (text: string, rootFolderID?: string) =>
    G.SshConfigImport(text, rootFolderID ?? "") as unknown as Promise<SshConfigImportSummary>,
  mobaXtermImport: (text: string, rootFolderID?: string) =>
    nn(G.MobaXtermImport(text, rootFolderID ?? "")) as Promise<MobaXtermImportSummary>,
  puttyRegImport: (text: string, rootFolderID?: string) =>
    nn(G.PuttyRegImport(text, rootFolderID ?? "")) as Promise<PuttyImportSummary>,

  saveTextFile: (suggestedName: string, content: string) =>
    G.SaveTextFile(suggestedName, content) as unknown as Promise<string>,
  loadTextFile: (title: string) =>
    G.LoadTextFile(title) as unknown as Promise<{ path: string; content: string }>,
  exportSubtree: (req: {
    roots: string[];
    extra: string[];
    format: "toml" | "json";
    include_credentials: boolean;
    passphrase: string;
    strip_notes?: boolean;
    strip_tags?: boolean;
    strip_color?: boolean;
    strip_icon?: boolean;
    convert_auth_ref_to_inherit?: boolean;
  }) => G.ExportSubtree(req as any) as unknown as Promise<ExportSubtreeResult>,
  fetchArchiveURL: (url: string) =>
    G.FetchArchiveURL(url) as unknown as Promise<string>,
  registerURLScheme: () =>
    G.RegisterURLScheme() as unknown as Promise<void>,
  urlSchemeStatus: () =>
    G.URLSchemeStatus() as unknown as Promise<string>,
  explorerMenuRegister: () =>
    G.ExplorerMenuRegister() as unknown as Promise<void>,
  explorerMenuUnregister: () =>
    G.ExplorerMenuUnregister() as unknown as Promise<void>,
  explorerMenuStatus: () =>
    G.ExplorerMenuStatus() as unknown as Promise<string>,
  importArchive: (req: {
    text: string;
    conflict: "skip" | "rename" | "overwrite";
    dry_run: boolean;
    passphrase: string;
    target_folder_id?: string;
  }) => G.ImportArchive(req as any) as unknown as Promise<ImportSummary>,

  sftpList: (sessionId: string, remotePath: string) =>
    G.SftpList(sessionId, remotePath) as unknown as Promise<{
      path: string;
      entries: SftpEntry[];
    }>,
  sftpStat: (sessionId: string, remotePath: string) =>
    G.SftpStat(sessionId, remotePath) as unknown as Promise<SftpEntry>,
  sftpMkdir: (sessionId: string, remotePath: string) =>
    G.SftpMkdir(sessionId, remotePath),
  sftpRemove: (sessionId: string, remotePath: string) =>
    G.SftpRemove(sessionId, remotePath),
  sftpRename: (sessionId: string, oldPath: string, newPath: string) =>
    G.SftpRename(sessionId, oldPath, newPath),
  sftpReadPreview: (sessionId: string, remotePath: string, maxBytes: number) =>
    G.SftpReadPreview(sessionId, remotePath, maxBytes) as unknown as Promise<{
      b64: string;
      truncated: boolean;
      size: number;
    }>,
  sftpPickDownloadDest: (suggestedName: string) =>
    G.SftpPickDownloadDest(suggestedName) as unknown as Promise<string>,
  sftpPickUploadSource: () =>
    G.SftpPickUploadSource() as unknown as Promise<string>,
  sftpStartDownload: (sessionId: string, remotePath: string, localPath: string) =>
    G.SftpStartDownload(sessionId, remotePath, localPath) as unknown as Promise<string>,
  sftpStartUpload: (sessionId: string, localPath: string, remotePath: string) =>
    G.SftpStartUpload(sessionId, localPath, remotePath) as unknown as Promise<string>,
  sftpStartDownloadDir: (sessionId: string, remoteRoot: string, localRoot: string) =>
    G.SftpStartDownloadDir(sessionId, remoteRoot, localRoot) as unknown as Promise<string>,
  sftpStartUploadDir: (sessionId: string, localRoot: string, remoteRoot: string) =>
    G.SftpStartUploadDir(sessionId, localRoot, remoteRoot) as unknown as Promise<string>,
  sftpPickUploadDirSource: () =>
    G.SftpPickUploadDirSource() as unknown as Promise<string>,
  sftpPickDownloadDirDest: () =>
    G.SftpPickDownloadDirDest() as unknown as Promise<string>,
  sftpCancelTransfer: (transferId: string) => G.SftpCancelTransfer(transferId),
};

export interface SftpEntry {
  name: string;
  path: string;
  is_dir: boolean;
  is_link: boolean;
  size: number;
  mode: number;
  mode_str: string;
  mod_time: number;
  target?: string;
}

export interface SftpTransferProgress {
  transfer_id: string;
  bytes: number;
  total: number;
  done: boolean;
  err?: string;
  // Recursive transfer extras (zero for single-file transfers).
  files_done?: number;
  files_total?: number;
  current_path?: string;
}

export interface ExportSubtreeResult {
  format: string;
  body: string;
  bytes: number;
}

export interface ImportSummary {
  folders_created: string[];
  folders_updated: string[];
  folders_skipped: string[];
  conns_created: string[];
  conns_updated: string[];
  conns_skipped: string[];
  creds_created: string[];
  creds_skipped: string[];
  secrets_imported: number;
  conn_passwords_imported: number;
  images_imported: number;
  warnings: string[];
}

export interface SshConfigImportSummary {
  connections_created: number;
  connections_skipped: number;
  jump_resolved: number;
  jump_unresolved: string[];
  identity_files_noted: number;
  warnings: string[];
}

export interface MobaXtermImportSummary {
  folders_created: number;
  connections_created: number;
  connections_skipped: number;
  skipped_non_ssh: number;
  warnings: string[];
}

export interface PuttyImportSummary {
  connections_created: number;
  connections_skipped: number;
  skipped_non_ssh: number;
  warnings: string[];
}

export interface RdmImportSummary {
  folders_created: number;
  connections_created: number;
  images_stored: number;
  jump_resolved: number;
  jump_unresolved: number;
  skipped_non_ssh: number;
  credentials_created: number;
  credentials_need_secret: number;
  unresolved_jumps: string[];
  unresolved_creds: string[];
  needs_attention: RdmAttentionItem[];
  warnings: string[];
}

export interface RdmAttentionItem {
  name: string;
  hostname: string;
  // "external-cred-ref" | "private-key-file" | "inline-username"
  reason: string;
  detail?: string;
}

export interface ProxyBookmark {
  name: string;
  url: string;
}

export interface Snippet {
  id: string;
  connection_id?: string | null;
  name: string;
  body: string;
  tags: string[];
  use_count: number;
  last_used_at?: number | null;
  created_at: number;
  updated_at: number;
}

export interface SnippetInput {
  connection_id?: string | null;
  name: string;
  body: string;
  tags: string[];
}

export interface TcpdumpProbeResult {
  root_user: boolean;
  sudo_no_pwd: boolean;
  has_candidate_password: boolean;
}

export interface TcpdumpStartInput {
  session_id: string;
  iface: string;
  bpf_filter: string;
  max_count: number;
  root_user: boolean;
  sudo_no_pwd: boolean;
  use_saved_password: boolean;
  verbose: boolean;
  // insights enables the live network-health analyzer (routing /
  // wrong-interface anomalies). Independent of verbose.
  insights?: boolean;
  // port_overrides maps a non-standard port (string key) to a
  // protocol name so the decoder treats that port as the named
  // proto. Empty / omitted = built-in port table only.
  port_overrides?: Record<string, string>;
}

// Insight is a single network-health finding emitted live during a
// capture via the `tcpdump_insight:<dumpId>` event.
export interface Insight {
  kind: string;
  severity: "error" | "warn" | "info";
  title: string;
  detail: string;
  flow_key: string;
  src_ip: string;
  dst_ip: string;
  suggest_route_check: boolean;
}

// RouteResult is one `ip route get` answer: the egress interface and
// source IP the kernel would actually use to reach a peer.
export interface RouteResult {
  dst: string;
  from: string;
  dev: string;
  src: string;
  via: string;
  raw: string;
  error?: string;
}

export interface TcpdumpRouteQuery {
  dst: string;
  from: string;
}

// One packet as the backend ships it (header line + parse + optional
// decode + the monotonic seq used for snapshot dedupe).
export interface TcpdumpPacket {
  raw: string;
  timestamp: string;
  proto: string;
  src_ip: string;
  src_port: number;
  dst_ip: string;
  dst_port: number;
  length: number;
  info: string;
  flow_key: string;
  decoded?: { type: string; summary: string; fields: Record<string, string> };
  seq: number;
}

export interface TcpdumpSnapshotResult {
  packets: TcpdumpPacket[];
  cum: number;
}

// Describes a capture already running for a session (returned to a
// window attaching after a detach). dump_id is "" when none is running.
export interface TcpdumpActiveInfo {
  dump_id: string;
  iface: string;
  bpf_filter: string;
  verbose: boolean;
  insights: boolean;
  continuous: boolean;
  max_count: number;
}

export interface RecordingState {
  session_id: string;
  recording: boolean;
  path: string;
}

export interface SyncConfig {
  url: string;
  username: string;
  has_password: boolean;
  has_passphrase: boolean;
  generation: number;
  last_sync_at: number;
  device: string;
  auto: boolean;
  auto_apply: boolean;
  check_minutes: number;
  transport: string;
  sftp_host: string;
  sftp_port: number;
  sftp_user: string;
  sftp_dir: string;
  sftp_auth_mode: string;
  sftp_cred_id: string;
  sftp_cred_name: string;
  sftp_has_password: boolean;
  sftp_has_key: boolean;
}

export interface SyncSftpConfigInput {
  host: string;
  port: number;
  user: string;
  dir: string;
  auth_mode: string;
  cred_id: string;
  inline_password: string;
  inline_key_pem: string;
  inline_key_passphrase: string;
  passphrase: string;
}

export interface SyncStatusResult {
  state: "empty" | "in_sync" | "remote_ahead" | "remote_behind";
  local_generation: number;
  remote_generation: number;
  remote_device: string;
  remote_updated_at: string;
  snapshot_size: number;
  local_dirty: boolean;
}

export interface OpksshCertStatus {
  vault_locked: boolean;
  has_cert: boolean;
  issued_at: number;
  valid_before: number;
  renew_at: number;
}

export interface RecordingFileInfo {
  name: string;
  path: string;
  size: number;
  mod_time: number;
  title: string;
  width: number;
  height: number;
  duration: number;
}

export interface HttpHeader {
  name: string;
  value: string;
}

export interface HttpRequest {
  method: string;
  url: string;
  headers: HttpHeader[];
  body: string;
  tls_skip_verify: boolean;
  socks_addr: string;
  timeout_seconds: number;
}

export interface HttpResponse {
  status: string;
  status_code: number;
  headers: HttpHeader[];
  body: string;
  truncated: boolean;
  duration_ms: number;
}

export interface BatchExecInput {
  connection_ids: string[];
  command: string;
  timeout_seconds: number;
}

export interface BatchHostResult {
  connection_id: string;
  hostname: string;
  name: string;
  state: "ok" | "error" | "skipped";
  stdout: string;
  stderr: string;
  exit_code: number;
  duration_ms: number;
  error?: string;
}

export interface AppVersionInfo {
  name: string;
  version: string;
  commit: string;
  schema_version: number;
}

export interface BackupCreateResult {
  path: string;
}

export interface BackupInfo {
  path: string;
  filename: string;
  size: number;
  created_at: string;
}

export interface AutoBackupPrefs {
  enabled: boolean;
  keep_last: number;
}

export interface Workspace {
  id: string;
  name: string;
  layout_json: string;
  last_opened_at?: number | null;
  created_at: number;
  updated_at: number;
}

export interface PortForward {
  id: string;
  connection_id: string;
  kind: "local" | "remote" | "dynamic";
  local_addr: string | null;
  local_port: number | null;
  remote_host: string | null;
  remote_port: number | null;
  auto_start: boolean;
  description: string;
  bookmarks: ProxyBookmark[];
}

export interface ServerStats {
  ok: boolean;
  load1: number;
  load5: number;
  load15: number;
  mem_used_pct: number;   // 0..100, -1 if unknown
  disk_used_pct: number;  // 0..100 for /, -1 if unknown
  users: number;          // -1 if unknown
}

export interface ForwardStatus {
  id: string;
  kind: "local" | "remote" | "dynamic";
  session_id: string;
  local_addr: string;
  local_port: number;
  remote_host?: string;
  remote_port?: number;
  state: "stopped" | "listening" | "error";
  error?: string;
  bytes_in: number;
  bytes_out: number;
  started_at: number;
}

export interface ForwardCreateInput {
  connection_id: string;
  kind: "local" | "remote" | "dynamic";
  local_addr?: string;
  local_port?: number;
  remote_host?: string;
  remote_port?: number;
  auto_start: boolean;
  description: string;
}

export interface ForwardUpdateInput {
  id: string;
  local_addr?: string;
  clear_local_addr?: boolean;
  local_port?: number;
  clear_local_port?: boolean;
  remote_host?: string;
  clear_remote_host?: boolean;
  remote_port?: number;
  clear_remote_port?: boolean;
  auto_start?: boolean;
  description?: string;
}
