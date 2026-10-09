// Fleet tools state: which fleet dialog is open, over which hosts, and the
// last results worth keeping (a folder's facts snapshot, per-connection TLS
// expiry for the tree badges). Snapshots live in the settings table so a
// restart keeps them; they are what was measured, never refreshed on
// their own.

import { api, type FactsCatalog, type FactsContainer, type FactsHostResult, type ReportSettingsWire, type TLSCertResult } from "./api";
import { tree, sessions } from "./stores.svelte";

export type FleetTool = "facts" | "tls" | "compare" | "copykey" | "upload" | "download";

export interface FleetOpen {
  tool: FleetTool;
  ids: string[];
  // Set when the hosts are "everything in this folder": the facts
  // snapshot is stored per folder.
  folderId?: string;
  label: string;
  // Facts: open straight on the stored report instead of the form.
  showSnapshot?: boolean;
}

export interface TransferStatus {
  tool: "upload" | "download";
  running: boolean;
  hosts: number;
  done: number;
  failed: number;
  bytes: number;
  total: number;
}

export interface FactsSnapshot {
  at: number;
  facts: string[];
  results: FactsHostResult[];
}

export interface TlsMark {
  // Host name at check time, for cards on folders that hold no facts
  // snapshot to look it up in (older marks lack it).
  name?: string;
  days_left: number;
  not_after: number;
  port: number;
  at: number;
}

// What a health report counts as a problem, per folder: every contract
// has its own thresholds, and every fleet a unit or two that fails by
// design (irqbalance on a small VM). Security updates and a pending reboot
// colour a host only past their grace days: what arrived on this week's
// patch day is normal. Defaults and the fact catalog come from Go
// (FactsCatalog), shared with the MCP tools.
export type ReportSettings = ReportSettingsWire;

// Containers that need someone: unhealthy, restarting, dead, or exited
// with an error. Expected (stopped on purpose) ones never count. Shared by
// the table colours and the report.
export function containerProblem(c: FactsContainer): string | null {
  if (c.health === "unhealthy") return "unhealthy";
  if (c.state === "restarting") return "restarting";
  if (c.state === "dead") return "dead";
  if (c.state === "exited" && c.exit_code !== 0) return `exited with code ${c.exit_code}`;
  return null;
}
export function containerIssues(f: { containers?: FactsContainer[] | null }, rs: ReportSettings, atMs: number) {
  const expected = new Set(rs.expectedContainers ?? []);
  const list = (f.containers ?? []).filter((c) => !expected.has(c.name));
  const problems = list.filter((c) => containerProblem(c));
  // Restarted by the engine (crash, OOM) and started again recently: it
  // runs now, but it fell over. A redeploy recreates the container and
  // resets the count, so a deploy alone never shows here.
  const restarted = list.filter((c) => !containerProblem(c) && c.restarts > 0 && c.started_at > 0 &&
    atMs / 1000 - c.started_at < (rs.containerRestartDays ?? 7) * 86400);
  return { problems, restarted };
}

// The badge threshold: under this many days a cert shows in the tree.
export const TLS_WARN_DAYS = 14;

class FleetStore {
  open = $state<FleetOpen | null>(null);
  snapshots = $state<Record<string, FactsSnapshot>>({});
  // Oldest first, the latest run included. Loaded on demand per folder.
  history = $state<Record<string, FactsSnapshot[]>>({});
  tls = $state<Record<string, TlsMark>>({});
  private loaded = false;

  // Fact list, presets, report defaults and history size, loaded once.
  catalog = $state<FactsCatalog | null>(null);
  async loadCatalog(): Promise<FactsCatalog> {
    if (!this.catalog) this.catalog = await api.factsCatalog();
    return this.catalog;
  }

  // A run stored by someone else (the MCP gather_facts tool) makes the
  // cached copies stale.
  forgetFacts(folderId: string) {
    const { [folderId]: _h, ...hist } = this.history;
    const { [folderId]: _s, ...snaps } = this.snapshots;
    this.history = hist;
    this.snapshots = snaps;
  }

  async init() {
    if (this.loaded) return;
    this.loaded = true;
    try {
      const raw = await api.settingsGet("fleet_tls");
      if (raw) this.tls = JSON.parse(raw);
    } catch { /* no marks yet */ }
  }

  // Upload / download live in their own slot: they can run for minutes,
  // so the dialog can be minimised to the status bar while the other
  // fleet tools are used. One transfer at a time.
  transfer = $state<FleetOpen | null>(null);
  transferMinimized = $state(false);
  // What the status bar shows for a minimised transfer, kept up to date by
  // the dialog.
  transferStatus = $state<TransferStatus | null>(null);

  show(o: FleetOpen) {
    if (o.tool === "upload" || o.tool === "download") {
      if (this.transfer && this.transferStatus?.running) {
        this.transferMinimized = false;
        return; // finish (or cancel) the running one first; it is shown
      }
      this.transfer = o;
      this.transferMinimized = false;
      this.transferStatus = null;
      return;
    }
    this.open = o;
  }
  close() {
    this.open = null;
  }
  closeTransfer() {
    this.transfer = null;
    this.transferMinimized = false;
    this.transferStatus = null;
  }
  minimizeTransfer() {
    if (this.transfer) this.transferMinimized = true;
  }
  restoreTransfer() {
    this.transferMinimized = false;
  }

  async snapshot(folderId: string): Promise<FactsSnapshot | null> {
    if (this.snapshots[folderId]) return this.snapshots[folderId];
    try {
      const raw = await api.settingsGet(`fleet_facts:${folderId}`);
      if (!raw) return null;
      const s = JSON.parse(raw) as FactsSnapshot;
      this.snapshots = { ...this.snapshots, [folderId]: s };
      return s;
    } catch {
      return null;
    }
  }

  async saveSnapshot(folderId: string, s: FactsSnapshot) {
    this.snapshots = { ...this.snapshots, [folderId]: s };
    await api.factsSaveRun(folderId, s).catch(console.warn);
    this.history = { ...this.history, [folderId]: (await api.factsHistory(folderId).catch(() => null)) ?? [s] };
  }

  // Runs stored for a folder, oldest first. A folder from before the
  // history existed starts with its one stored snapshot.
  async loadHistory(folderId: string): Promise<FactsSnapshot[]> {
    if (this.history[folderId]) return this.history[folderId];
    const hist = (await api.factsHistory(folderId).catch(() => null)) ?? [];
    this.history = { ...this.history, [folderId]: hist };
    return hist;
  }

  // Drop one stored run (a test run, a run against the wrong hosts).
  async deleteRun(folderId: string, at: number) {
    await api.factsDeleteRun(folderId, at).catch(console.warn);
    this.forgetFacts(folderId);
    await this.loadHistory(folderId);
  }

  reportSettings = $state<Record<string, ReportSettings>>({});
  // "Prepared by" on every report: one person signs them all, so it is
  // kept once rather than per folder.
  reportAuthor = $state("");
  async loadReportAuthor(): Promise<string> {
    try {
      this.reportAuthor = (await api.settingsGet("fleet_report_author")) ?? "";
    } catch { /* none */ }
    return this.reportAuthor;
  }
  async saveReportAuthor(v: string) {
    this.reportAuthor = v;
    await api.settingsSet("fleet_report_author", v).catch(console.warn);
  }
  // Company logo on every report, as a data: URI so the saved HTML file
  // carries it. "" = none.
  reportLogo = $state("");
  async loadReportLogo(): Promise<string> {
    try {
      this.reportLogo = (await api.settingsGet("fleet_report_logo")) ?? "";
    } catch { /* none */ }
    return this.reportLogo;
  }
  async saveReportLogo(v: string) {
    this.reportLogo = v;
    await api.settingsSet("fleet_report_logo", v).catch(console.warn);
  }

  // Folder runs have their own; a hand-picked set of hosts shares one.
  async loadReportSettings(folderId?: string): Promise<ReportSettings> {
    const key = folderId || "_";
    if (this.reportSettings[key]) return this.reportSettings[key];
    const rs = await api.factsReportSettingsGet(folderId ?? "");
    this.reportSettings = { ...this.reportSettings, [key]: rs };
    return rs;
  }
  async saveReportSettings(folderId: string | undefined, rs: ReportSettings) {
    const key = folderId || "_";
    this.reportSettings = { ...this.reportSettings, [key]: rs };
    await api.factsReportSettingsSet(folderId ?? "", rs).catch(console.warn);
  }

  // Keep the soonest-expiring port per connection; a host that answered on
  // no port loses its mark.
  async recordTls(results: TLSCertResult[]) {
    const next = { ...this.tls };
    const seen = new Set<string>();
    for (const r of results) {
      if (r.state === "skipped") continue;
      if (!seen.has(r.connection_id)) {
        delete next[r.connection_id];
        seen.add(r.connection_id);
      }
      if (r.state !== "ok") continue;
      const cur = next[r.connection_id];
      if (!cur || r.days_left < cur.days_left) {
        next[r.connection_id] = { name: r.name, days_left: r.days_left, not_after: r.not_after, port: r.port, at: Date.now() };
      }
    }
    this.tls = next;
    await api.settingsSet("fleet_tls", JSON.stringify(next)).catch(console.warn);
  }

  // Forget the marks of these hosts (card + tree badge) until the next
  // check - for certs nobody will renew (lab, self-signed, retired).
  async clearTls(ids: string[]) {
    const next = { ...this.tls };
    for (const id of ids) delete next[id];
    this.tls = next;
    await api.settingsSet("fleet_tls", JSON.stringify(next)).catch(console.warn);
  }

  // Days left as of now, from the stored expiry - the badge must not show
  // "9d" for weeks after the check.
  daysLeft(connectionId: string): number | null {
    const m = this.tls[connectionId];
    if (!m) return null;
    return Math.floor((m.not_after * 1000 - Date.now()) / 86_400_000);
  }
}

export const fleet = new FleetStore();

// Display name for a fleet host id: the saved connection, else the loaded
// dynamic entry ("dyn:<id>"), an open tab's session ("session:<id>", the
// transfers on selected tabs), else what the caller last saw.
export function fleetHostName(id: string, fallback?: string): string {
  if (id.startsWith("session:")) {
    const s = sessions.tabs.find((x) => x.sessionId === id.slice(8));
    return s?.name || s?.hostname || fallback || "session";
  }
  if (id.startsWith("dyn:")) {
    const eid = id.slice(4);
    for (const list of Object.values(tree.dynamicEntries)) {
      const e = list.find((x) => x.id === eid);
      if (e) return e.name || e.hostname || fallback || id;
    }
    return fallback || id;
  }
  return tree.connectionById(id)?.name ?? fallback ?? id;
}

// Dynamic (cloud inventory) folders under folderId, itself included, whose
// entries were never loaded into the tree - a subfolder nobody expanded.
// folderHostIds cannot see their hosts until they are.
export function unloadedDynamicUnder(folderId: string): string[] {
  const out: string[] = [];
  const walk = (fid: string) => {
    if (tree.isDynamic(fid) && tree.dynamicEntries[fid] === undefined) out.push(fid);
    for (const f of tree.folders.filter((x) => x.parent_id === fid)) walk(f.id);
  };
  walk(folderId);
  return out;
}

// folderHostIds after loading the cached entries of every dynamic subfolder
// that was never expanded. Reads what the last inventory refresh stored;
// it does not refresh the provider.
// Display names for the transfer IPCs: open-tab hosts have no row in the
// database for the backend to read a name from.
export function fleetLabels(ids: string[]): Record<string, string> {
  return Object.fromEntries(ids.filter((id) => id.startsWith("session:")).map((id) => [id, fleetHostName(id)]));
}

export async function folderHostIdsLoaded(folderIds: string[]): Promise<string[]> {
  const missing = [...new Set(folderIds.flatMap(unloadedDynamicUnder))];
  await Promise.all(missing.map((fid) => tree.loadDynamicEntries(fid)));
  return [...new Set(folderIds.flatMap(folderHostIds))];
}

// Every SSH host under a folder, subfolders included: saved connections
// plus the loaded entries of dynamic (cloud inventory) folders as
// "dyn:<id>". Local shells, RDP and VNC-only entries are left out - there
// is nothing to run a command on - and so are inventory VMs the provider
// reports stopped: each would only cost a connect timeout and land in
// "Did not answer".
export function folderHostIds(folderId: string): string[] {
  const out: string[] = [];
  const walk = (fid: string) => {
    for (const c of tree.connectionsIn(fid)) {
      if ((c.protocol || "ssh") === "ssh") out.push(c.id);
    }
    for (const e of tree.dynamicEntries[fid] ?? []) {
      if (e.status !== "stopped") out.push(`dyn:${e.id}`);
    }
    for (const f of tree.folders.filter((x) => x.parent_id === fid)) walk(f.id);
  };
  walk(folderId);
  return out;
}
