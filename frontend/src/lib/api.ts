// Thin TS facade over Wails-generated bindings.
//
// We define plain-object interfaces here rather than re-using the
// auto-generated classes (which carry a `convertValues` member that TS
// treats as required). That lets call sites write object literals freely.

import * as G from "../../bindings/ssh-tool/app.js";
import { recordNetworkVia } from "./networkVia";

// Connect results may carry network_via (the WireGuard profile the
// first hop actually dialed through). Stash it so SessionStore.add
// can decorate the tab - the add always runs after this resolves.
function recordVia<T extends { session_id: string; network_via?: string }>(r: T): T {
  recordNetworkVia(r.session_id, r.network_via);
  return r;
}

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
  initial_command?: string;
  auto_reconnect?: boolean;
  verbose?: boolean;
  vnc_enabled?: boolean;
  vnc_port?: number;
  vnc_use_tunnel?: boolean;
  // "" = explicitly direct (breaks an inherited profile); id = a
  // network_profiles row; absent = inherit.
  network_profile_id?: string;
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
  initial_command: string;
  auto_reconnect: boolean;
  verbose: boolean;
  vnc_enabled: boolean;
  vnc_port: number;
  vnc_use_tunnel: boolean;
  network_profile_id: string | null;
}

// ----- Network profiles (userspace WireGuard) -----

export interface WgPeer {
  public_key: string;
  has_psk: boolean;
  endpoint: string;
  allowed_ips: string[];
  keepalive: number;
}

export interface WgProfile {
  id: string;
  name: string;
  mode?: "always" | "auto";
  paused?: boolean;
  addresses: string[];
  dns: string[];
  mtu: number;
  peers: WgPeer[];
}

export interface WgStatus {
  profile_id: string;
  running: boolean;
  started_at: number;
  last_handshake: number;
  rx_bytes: number;
  tx_bytes: number;
  peers?: number;
}

export interface NetbirdConfig {
  kind: string;
  management_url: string;
  device_name: string;
  setup_key_credential_id: string;
  mode?: "always" | "auto";
  paused?: boolean;
}

export interface TailscaleConfig {
  kind: string;
  control_url: string;
  hostname: string;
  auth_key_credential_id: string;
  mode?: "always" | "auto";
  paused?: boolean;
}

export interface NetworkProfileInfo {
  id: string;
  name: string;
  kind: "wireguard" | "netbird" | "tailscale";
  mode: "always" | "auto" | "";
  paused: boolean;
  profile: WgProfile;
  netbird?: NetbirdConfig;
  tailscale?: TailscaleConfig;
  status: WgStatus;
  created_at: number;
  updated_at: number;
}

export interface PluginInfo {
  name: string;
  installed: boolean;
  path: string;
  version: string;
  update_available: boolean;
  supported: boolean;
}

export interface KeepassDatabaseInfo {
  id: string;
  name: string;
  source: "local" | "webdav" | "sftp";
  path: string;
  url: string;
  master_ref: string;
  keyfile_ref: string;
  remote_config: Record<string, string>;
  last_fetched_at: number | null;
  last_etag: string;
  created_at: number;
  updated_at: number;
}

export interface KeepassSaveInput {
  id?: string;
  name: string;
  source: string;
  path?: string;
  url?: string;
  master?: string;
  set_master?: boolean;
  key_file?: string;
  set_key_file?: boolean;
  remote_config?: Record<string, string>;
  remote_pass?: string;
  set_remote?: boolean;
}

export interface KeepassEntryInfo {
  uuid: string;
  title: string;
  username: string;
  has_pass: boolean;
  attachments: string[] | null;
  custom_keys: string[] | null;
  group_path: string;
}

export interface KeepassGroupInfo {
  name: string;
  path: string;
  groups: KeepassGroupInfo[] | null;
  entries: KeepassEntryInfo[] | null;
}

export interface BitwardenServerInfo {
  id: string;
  name: string;
  server_url: string;
  api_key_ref: string;
  master_ref: string;
  network_profile_id: string;
  last_synced_at: number | null;
  last_hash: string;
  created_at: number;
  updated_at: number;
}

export interface BitwardenSaveInput {
  id?: string;
  name: string;
  server_url: string;
  api_key_cred_id: string;
  network_profile_id?: string;
  master?: string;
  set_master?: boolean;
}

// Bitwarden cipher (item) for the picker; decrypted metadata only.
export interface BitwardenCipherInfo {
  id: string;
  name: string;
  username: string;
  type: number;
  is_ssh_key: boolean;
  has_password: boolean;
  has_totp: boolean;
  custom_keys: string[] | null;
  attachments: string[] | null;
}

export interface BitwardenCollectionInfo {
  id: string;
  name: string;
  ciphers: BitwardenCipherInfo[] | null;
}

// A browse node: the personal vault (org_id "") or an organization, holding
// collections plus any uncollected items.
export interface BitwardenGroupInfo {
  org_id: string;
  name: string;
  collections: BitwardenCollectionInfo[] | null;
  ciphers: BitwardenCipherInfo[] | null;
}

export interface InfisicalServerInfo {
  id: string;
  name: string;
  server_url: string;
  api_key_ref: string;
  network_profile_id: string;
  last_used_at: number | null;
  created_at: number;
  updated_at: number;
}

export interface InfisicalSaveInput {
  id?: string;
  name: string;
  server_url: string;
  api_key_cred_id: string;
  network_profile_id?: string;
}

// One selectable secret in the picker: a key at a folder path in an environment.
export interface InfisicalEntryInfo {
  key: string;
  path: string;
  has_value: boolean;
  comment: string;
  is_key: boolean;
}

// One environment (dev / prod ...) with its secrets.
export interface InfisicalEnvInfo {
  name: string;
  slug: string;
  entries: InfisicalEntryInfo[] | null;
}

// A browse node: one project (workspace) holding its environments.
export interface InfisicalGroupInfo {
  project_id: string;
  name: string;
  environments: InfisicalEnvInfo[] | null;
}

export interface RemoteOwner {
  active: boolean;
  machine_name: string;
  kind: string;
  since_unix: number;
  estimate_seconds: number;
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
  // How strongly the auto-unlock sidecar is bound to this machine. "weak" =
  // the v1 format whose key derivation can fall back to the hostname (macOS,
  // or a container with no /etc/machine-id); the UI warns on it. Absent when
  // there is no sidecar.
  sidecar_strength?: "strong" | "weak" | "none";
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
  // Unix timestamp when the secret expires (nil = no expiry). Honoured
  // for the user-set secret kinds (api_token, password, key); agent /
  // opkssh ignore it (they have no user-set expiry).
  expires_at?: number;
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
    } & CommonCreateExtras)
  | ({
      kind: "keepass";
      name: string;
      keepass_db_id: string;
      keepass_entry_uuid: string;
      keepass_field: string;
      keepass_is_key?: boolean;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "bitwarden";
      name: string;
      bitwarden_server_id: string;
      bitwarden_cipher_id: string;
      bitwarden_field: string;
      bitwarden_is_key?: boolean;
      hint?: string;
      tags?: string[];
      default_username?: string;
    } & CommonCreateExtras)
  | ({
      kind: "infisical";
      name: string;
      infisical_server_id: string;
      infisical_project_id: string;
      infisical_environment: string;
      infisical_secret_path?: string;
      infisical_key: string;
      infisical_is_key?: boolean;
      hint?: string;
      tags?: string[];
      default_username?: string;
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
    expires_at?: number;
    set_expires_at_to_null?: boolean;
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

  sshConnect: (connectionId: string) => nn(G.SshConnect(connectionId)).then(recordVia),
  // overrideCredentialId: empty string falls through to SshConnect
  // behaviour (use the connection's persisted auth_ref). Non-empty
  // forces this credential for the target hop on this one attempt
  // only - the persisted auth_ref is left untouched.
  sshConnectWithOverride: (connectionId: string, overrideCredentialId: string) =>
    nn(G.SshConnectWithOverride(connectionId, overrideCredentialId)).then(recordVia),
  sshConnectAdvanced: (connectionId: string, overrideCredentialId: string, overrideUsername: string, overridePassword: string) =>
    nn(G.SshConnectAdvanced(connectionId, overrideCredentialId, overrideUsername, overridePassword)).then(recordVia),
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
  profileStats: () => G.ProfileStats() as unknown as Promise<ProfileStats>,

  keepassList: () =>
    G.KeepassList() as unknown as Promise<KeepassDatabaseInfo[]>,
  keepassSave: (input: KeepassSaveInput) =>
    G.KeepassSave(input as unknown as Parameters<typeof G.KeepassSave>[0]) as unknown as Promise<KeepassDatabaseInfo>,
  keepassDelete: (id: string) => G.KeepassDelete(id),
  keepassRefresh: (id: string) => G.KeepassRefresh(id) as unknown as Promise<string>,
  keepassBrowse: (id: string) =>
    G.KeepassBrowse(id) as unknown as Promise<KeepassGroupInfo[]>,
  keepassPickFile: () => G.KeepassPickFile() as unknown as Promise<string>,
  keepassEnsureCredential: (input: {
    db_id: string;
    entry_uuid: string;
    field: string;
    is_key: boolean;
    name: string;
    username?: string;
    folder_id?: string | null;
  }) =>
    G.KeepassEnsureCredential(input as unknown as Parameters<typeof G.KeepassEnsureCredential>[0]) as unknown as Promise<CredentialRef>,

  bitwardenList: () =>
    G.BitwardenList() as unknown as Promise<BitwardenServerInfo[]>,
  bitwardenSave: (input: BitwardenSaveInput) =>
    G.BitwardenSave(input as unknown as Parameters<typeof G.BitwardenSave>[0]) as unknown as Promise<BitwardenServerInfo>,
  bitwardenDelete: (id: string) => G.BitwardenDelete(id),
  bitwardenSync: (id: string) => G.BitwardenSync(id) as unknown as Promise<string>,
  bitwardenBrowse: (id: string) =>
    G.BitwardenBrowse(id) as unknown as Promise<BitwardenGroupInfo[]>,
  bitwardenEnsureCredential: (input: {
    server_id: string;
    cipher_id: string;
    field: string;
    is_key: boolean;
    name: string;
    username?: string;
    folder_id?: string | null;
  }) =>
    G.BitwardenEnsureCredential(input as unknown as Parameters<typeof G.BitwardenEnsureCredential>[0]) as unknown as Promise<CredentialRef>,

  infisicalList: () =>
    G.InfisicalList() as unknown as Promise<InfisicalServerInfo[]>,
  infisicalSave: (input: InfisicalSaveInput) =>
    G.InfisicalSave(input as unknown as Parameters<typeof G.InfisicalSave>[0]) as unknown as Promise<InfisicalServerInfo>,
  infisicalDelete: (id: string) => G.InfisicalDelete(id),
  infisicalTestLogin: (id: string) => G.InfisicalTestLogin(id),
  infisicalBrowse: (id: string) =>
    G.InfisicalBrowse(id) as unknown as Promise<InfisicalGroupInfo[]>,
  infisicalEnsureCredential: (input: {
    server_id: string;
    project_id: string;
    environment: string;
    secret_path: string;
    key: string;
    is_key: boolean;
    name: string;
    username?: string;
    folder_id?: string | null;
  }) =>
    G.InfisicalEnsureCredential(input as unknown as Parameters<typeof G.InfisicalEnsureCredential>[0]) as unknown as Promise<CredentialRef>,

  networkProfilesList: () =>
    G.NetworkProfilesList() as unknown as Promise<NetworkProfileInfo[]>,
  networkProfileCreate: (name: string, confText: string) =>
    G.NetworkProfileCreate(name, confText) as unknown as Promise<NetworkProfileInfo>,
  networkProfileUpdate: (id: string, name: string, confText: string) =>
    G.NetworkProfileUpdate(id, name, confText) as unknown as Promise<NetworkProfileInfo>,
  networkProfileDelete: (id: string) => G.NetworkProfileDelete(id),
  networkProfileRenderConf: (id: string) =>
    G.NetworkProfileRenderConf(id) as unknown as Promise<string>,
  networkProfileStop: (id: string) => G.NetworkProfileStop(id),
  networkProfileSetPolicy: (id: string, mode: string, paused: boolean) =>
    G.NetworkProfileSetPolicy(id, mode, paused) as unknown as Promise<NetworkProfileInfo>,
  networkProfileTest: (id: string) =>
    G.NetworkProfileTest(id) as unknown as Promise<WgStatus>,
  networkProfileCreateNetbird: (name: string, managementURL: string, deviceName: string, setupKeyCredentialId: string) =>
    G.NetworkProfileCreateNetbird(name, managementURL, deviceName, setupKeyCredentialId) as unknown as Promise<NetworkProfileInfo>,
  networkProfileUpdateNetbird: (id: string, name: string, managementURL: string, deviceName: string, setupKeyCredentialId: string) =>
    G.NetworkProfileUpdateNetbird(id, name, managementURL, deviceName, setupKeyCredentialId) as unknown as Promise<NetworkProfileInfo>,
  networkProfileCreateTailscale: (name: string, controlURL: string, hostname: string, authKeyCredentialId: string) =>
    G.NetworkProfileCreateTailscale(name, controlURL, hostname, authKeyCredentialId) as unknown as Promise<NetworkProfileInfo>,
  networkProfileUpdateTailscale: (id: string, name: string, controlURL: string, hostname: string, authKeyCredentialId: string) =>
    G.NetworkProfileUpdateTailscale(id, name, controlURL, hostname, authKeyCredentialId) as unknown as Promise<NetworkProfileInfo>,
  pluginsStatus: () => G.PluginsStatus() as unknown as Promise<PluginInfo[]>,
  pluginDownload: (name: string) => G.PluginDownload(name) as unknown as Promise<string>,
  pluginRemove: (name: string) => G.PluginRemove(name),
  networkProfilePresence: (profileId: string) =>
    G.NetworkProfilePresence(profileId) as unknown as Promise<RemoteOwner>,
  networkProfileTakeOver: (profileId: string) =>
    G.NetworkProfileTakeOver(profileId) as unknown as Promise<number>,
  networkProfileConnectAnyway: (profileId: string) =>
    G.NetworkProfileConnectAnyway(profileId),
  networkProfileDisconnectRemote: (profileId: string) =>
    G.NetworkProfileDisconnectRemote(profileId) as unknown as Promise<number>,
  suggestNetbirdDeviceName: () =>
    G.SuggestNetbirdDeviceName() as unknown as Promise<string>,
  suggestTailscaleHostname: () =>
    G.SuggestTailscaleHostname() as unknown as Promise<string>,
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
  windowListTargets: (callerName: string) =>
    G.WindowListTargets(callerName) as unknown as Promise<{ name: string; label: string }[]>,
  windowSendTab: (callerName: string, targetName: string, tabId: string, sessions: string, layout: string) =>
    G.WindowSendTab(callerName, targetName, tabId, sessions, layout),
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
    (G.SshConnectDynamic(folderId, entryId) as unknown as Promise<{ session_id: string }>).then(recordVia),
  sshConnectDynamicWithOverride: (folderId: string, entryId: string, overrideCredentialId: string) =>
    (G.SshConnectDynamicWithOverride(folderId, entryId, overrideCredentialId) as unknown as Promise<{ session_id: string }>).then(recordVia),
  sshConnectDynamicAdvanced: (folderId: string, entryId: string, overrideCredentialId: string, overrideUsername: string, overridePassword: string) =>
    (G.SshConnectDynamicAdvanced(folderId, entryId, overrideCredentialId, overrideUsername, overridePassword) as unknown as Promise<{ session_id: string }>).then(recordVia),
  sshConnectDynamicWithJumpOverride: (folderId: string, entryId: string, overrideCredentialId: string, overrideUsername: string, overridePassword: string, jumpHostOverride: string, jumpCredentialOverride: string) =>
    (G.SshConnectDynamicWithJumpOverride(folderId, entryId, overrideCredentialId, overrideUsername, overridePassword, jumpHostOverride, jumpCredentialOverride) as unknown as Promise<{ session_id: string }>).then(recordVia),
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
  fetchReleaseNotesRange: (fromVersion: string, toVersion: string) =>
    G.FetchReleaseNotesRange(fromVersion, toVersion) as unknown as Promise<{
      version: string;
      released_at: string;
      notes_md: string;
      error?: string;
    }[]>,

  pickAnsibleInventoryFile: () =>
    G.PickAnsibleInventoryFile() as unknown as Promise<string>,
  pickPuttyKeyFile: () =>
    G.PickPuttyKeyFile() as unknown as Promise<string>,

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
  sshGiveInternet: (sessionId: string, remotePort: number, allowInternal = false) =>
    G.SshGiveInternet(sessionId, remotePort, allowInternal) as unknown as Promise<GiveInternetResult>,
  sshLaunchBrowser: (forwardId: string, url: string) =>
    G.SshLaunchBrowser(forwardId, url) as unknown as Promise<{ pid: number }>,

  // MCP bridge: share live sessions with an external LLM.
  mcpShareSession: (sessionId: string, level: McpGrantLevel) =>
    G.McpShareSession(sessionId, level),
  mcpUnshareSession: (sessionId: string) => G.McpUnshareSession(sessionId),
  mcpListGrants: () => G.McpListGrants() as unknown as Promise<McpGrantInfo[]>,
  mcpApprovalRespond: (approvalId: string, decision: McpDecision) =>
    G.McpApprovalRespond(approvalId, decision),
  mcpActivityList: (sessionId: string) =>
    G.McpActivityList(sessionId) as unknown as Promise<McpActivity[]>,
  appExePath: () => G.AppExePath() as unknown as Promise<string>,
  appWslExePath: () => G.AppWslExePath() as unknown as Promise<string>,
  requestAttention: () => G.RequestAttention(),
  clearAttention: () => G.ClearAttention(),
  sendPromptNotification: (title: string, body: string) => G.SendPromptNotification(title, body),

  // ----- browser session sharing -----
  shareStart: (input: ShareStartInput) =>
    G.ShareStart(input as any) as unknown as Promise<ShareStartResult>,
  shareStop: (shareId: string) => G.ShareStop(shareId),
  shareUpdate: (shareId: string, input: ShareStartInput) =>
    G.ShareUpdate(shareId, input as any),
  shareKick: (shareId: string, remoteIp: string) => G.ShareKick(shareId, remoteIp),
  shareActive: () => G.ShareActive() as unknown as Promise<ShareStatus[]>,
  shareSetActiveTab: (shareId: string, index: number) => G.ShareSetActiveTab(shareId, index),
  shareInterfaces: () => G.ShareInterfaces() as unknown as Promise<ShareInterface[]>,
  shareFingerprint: () => G.ShareFingerprint() as unknown as Promise<ShareFingerprint>,
  shareRegenerateCert: () => G.ShareRegenerateCert() as unknown as Promise<ShareFingerprint>,
  shareApprovalRespond: (approvalId: string, decision: "allow" | "deny") =>
    G.ShareApprovalRespond(approvalId, decision),

  settingsGet: (key: string) => G.SettingsGet(key) as unknown as Promise<string>,
  settingsSet: (key: string, value: string) => G.SettingsSet(key, value),
  settingsDelete: (key: string) => G.SettingsDelete(key),

  sshRespondHostKey: (challengeId: string, accept: boolean, remember: boolean, hostname: string, port: number, keyType: string, keyB64: string, fingerprint: string) =>
    G.SshRespondHostKey(challengeId, accept, remember, hostname, port, keyType, keyB64, fingerprint),

  sshRespondAuthPrompt: (promptId: string, answers: string[], cancel: boolean) =>
    G.SshRespondAuthPrompt(promptId, answers, cancel),

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
  superPuttyImport: (text: string, rootFolderID?: string) =>
    nn(G.SuperPuttyImport(text, rootFolderID ?? "")) as Promise<SuperPuttyImportSummary>,

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

// SuperPuTTY rebuilds a folder tree, so it carries folders_created (unlike the
// flat PuTTY-reg summary). Same shape as the MobaXterm summary.
export interface SuperPuttyImportSummary {
  folders_created: number;
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

export interface ProfileStats {
  connections: number;
  vnc_enabled: number;
  folders: number;
  dynamic_folders: number;
  forwards: number;
  bookmarks: number;
  credentials: number;
  dynamic_hosts: number;
  dynamic_vms: number;
  dynamic_lxc: number;
  dynamic_servers: number;
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

export interface GiveInternetResult {
  forward_id: string;
  remote_port: number;
  export_command: string;
}

export type McpGrantLevel = "read" | "read-run" | "read-run-yolo";
export type McpDecision = "run" | "type" | "deny";

// ----- browser session sharing -----

export type ShareLevel = "read" | "control";

export interface ShareStartInput {
  bind_ip: string;
  port: number;
  level: ShareLevel;
  scrollback: boolean;
  active_tab: number;
  tabs_blob: string; // projected {tabs:[...]} JSON, sessionIds -> guest slots
  sessions: { slot: string; real_id: string; name: string }[];
}

export interface ShareFingerprint {
  Hex: string;
  Short: string;
  Words: string;
}

export interface ShareStartResult {
  share_id: string;
  url: string;
  bind: string;
  fingerprint: ShareFingerprint;
  regenerated: boolean;
}

export interface ShareInterface {
  name: string;
  ip: string;
}

export interface ShareGuest {
  remote_ip: string;
  joined_at: number;
  level: string;
}

export interface ShareStatus {
  share_id: string;
  level: string;
  bind: string;
  guests: ShareGuest[];
}

// Payload of the "share_approval_request" event.
export interface ShareApprovalRequest {
  approval_id: string;
  share_id: string;
  remote_ip: string;
  fingerprint: string; // the words to compare
  level: string;
  tabs: string[];
}

export interface McpGrantInfo {
  session_id: string;
  name: string;
  hostname: string;
  level: McpGrantLevel;
}

// One recorded LLM action (event: "mcp_activity" for live, McpActivityList for
// history). Output is capped server-side.
export interface McpActivity {
  seq: number;
  ts: number;
  session_id: string;
  session: string;
  kind: "run" | "type" | "connect" | "read";
  command: string;
  output?: string;
  exit?: "ok" | "error" | "";
  gate: "auto" | "approved" | "denied" | "n/a" | "";
}

// Event payload for the approval modal (event: "mcp_approval_request").
export interface McpApprovalRequest {
  approval_id: string;
  session_id: string;
  session_name: string;
  kind: "run" | "type" | "connect";
  command: string;
}

export interface ForwardStatus {
  id: string;
  kind: "local" | "remote" | "dynamic" | "reverse-proxy";
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
