<script lang="ts">
  // Gather facts: pick read-only facts, run them on every host of the scope,
  // then read the report - grouped by size so "which servers look like this
  // one" is one click (Find similar), with reboot / security counts on top.
  import { onMount } from "svelte";
  import { api, type FactsHostResult } from "./api";
  import { fleet, containerIssues, containerProblem, folderHostIds, folderHostIdsLoaded, type FleetOpen, type FactsSnapshot, type ReportSettings } from "./fleetStore.svelte";
  import { toast } from "./toast.svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import FleetShell from "./FleetShell.svelte";
  import FactsReport, { REPORT_CSS } from "./FactsReport.svelte";
  import { errMsg } from "./connectErrors";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();
  // A folder's hosts as the tree has them now, not as they were when the
  // dialog opened: "Run again" after a pin, a refresh or a new VM must not
  // ask a host that is gone (or miss one that came).
  const hostIds = $derived(scope.folderId ? folderHostIds(scope.folderId) : scope.ids);

  // Facts and presets come from Go (FactsCatalog), the same list the MCP
  // tools offer; the form waits for it.
  const catalog = $derived(fleet.catalog);
  const GROUPS = $derived.by(() => {
    const out: { title: string; facts: { key: string; label: string }[] }[] = [];
    for (const f of catalog?.facts ?? []) {
      let g = out.find((x) => x.title === f.group);
      if (!g) out.push((g = { title: f.group, facts: [] }));
      g.facts.push({ key: f.key, label: f.label });
    }
    return out;
  });
  const PRESETS = $derived(Object.fromEntries((catalog?.presets ?? []).map((p) => [p.key, p.facts])) as Record<string, string[]>);

  let preset = $state("sizing");
  let picked = $state<Record<string, boolean>>({});
  let custom = $state("");
  let timeout = $state(30);
  let running = $state(false);
  let runErr = $state("");
  let snap = $state<FactsSnapshot | null>(null);

  // Presets the user saved (global), and per folder the choice its last
  // run used, so a recurring report opens on the same facts.
  interface UserPreset { name: string; facts: string[]; custom: string; timeout: number; }
  let userPresets = $state<UserPreset[]>([]);
  let savingPreset = $state(false);
  let presetName = $state("");
  const USER = "user:";

  function applyPreset(p: string) {
    preset = p;
    if (PRESETS[p]) picked = Object.fromEntries(PRESETS[p].map((k) => [k, true]));
    const u = p.startsWith(USER) ? userPresets.find((x) => x.name === p.slice(USER.length)) : null;
    if (u) {
      picked = Object.fromEntries(u.facts.map((k) => [k, true]));
      custom = u.custom;
      timeout = u.timeout;
    }
  }
  async function loadPresets() {
    try {
      const raw = await api.settingsGet("fleet_facts_presets");
      if (raw) userPresets = JSON.parse(raw);
    } catch { /* none */ }
    if (!scope.folderId) return;
    try {
      const raw = await api.settingsGet(`fleet_facts_last:${scope.folderId}`);
      if (!raw) return;
      const last = JSON.parse(raw) as { preset: string } & UserPreset;
      // A saved preset that was deleted since falls back to its facts.
      const known = PRESETS[last.preset] || userPresets.some((u) => USER + u.name === last.preset);
      preset = known ? last.preset : "custom";
      picked = Object.fromEntries(last.facts.map((k) => [k, true]));
      custom = last.custom ?? "";
      timeout = last.timeout || 30;
    } catch { /* first run here */ }
  }
  async function storePresets(list: UserPreset[]) {
    userPresets = list;
    await api.settingsSet("fleet_facts_presets", JSON.stringify(list)).catch(console.warn);
  }
  async function savePreset() {
    const name = presetName.trim();
    if (!name) return;
    const p: UserPreset = { name, facts: pickedKeys, custom: custom.trim(), timeout };
    await storePresets([...userPresets.filter((u) => u.name !== name), p].sort((a, b) => a.name.localeCompare(b.name)));
    preset = USER + name;
    savingPreset = false;
    presetName = "";
    toast.push("ok", `Preset "${name}" saved`);
  }
  async function deletePreset() {
    const name = preset.slice(USER.length);
    await storePresets(userPresets.filter((u) => u.name !== name));
    preset = "custom";
  }
  function toggle(k: string) {
    picked[k] = !picked[k];
    preset = "custom";
  }
  $effect(() => {
    // A custom column or timeout edit leaves a saved preset too.
    const u = preset.startsWith(USER) ? userPresets.find((x) => x.name === preset.slice(USER.length)) : null;
    if (u && (u.custom !== custom.trim() || u.timeout !== timeout)) preset = "custom";
  });
  const pickedKeys = $derived(Object.keys(picked).filter((k) => picked[k]));

  onMount(async () => {
    const cat = await fleet.loadCatalog();
    picked = Object.fromEntries((cat.presets.find((p) => p.key === "sizing")?.facts ?? []).map((k) => [k, true]));
    await loadPresets();
    rs = { ...(await fleet.loadReportSettings(scope.folderId)) };
    await fleet.loadReportAuthor();
    await fleet.loadReportLogo();
    if (scope.folderId) {
      const hist = await fleet.loadHistory(scope.folderId);
      if (scope.showSnapshot) snap = hist[hist.length - 1] ?? (await fleet.snapshot(scope.folderId));
    }
  });

  // ---- report view ----
  // Thresholds and expected failed units of this scope; the table's
  // colours and chips follow them too.
  // Filled in onMount (defaults from the catalog) before anything reads it.
  let rs = $state<ReportSettings>({} as ReportSettings);
  let showRs = $state(false);
  let unitsText = $state("");
  let containersText = $state("");
  // The panel edits a copy; Apply saves it, Cancel drops it.
  let draft = $state<ReportSettings>({} as ReportSettings);
  let authorDraft = $state("");
  function openRs() {
    draft = { ...rs };
    authorDraft = fleet.reportAuthor;
    unitsText = rs.expectedUnits.join(" ");
    containersText = (rs.expectedContainers ?? []).join(" ");
    showRs = !showRs;
  }
  async function saveRs(next: ReportSettings) {
    rs = next;
    await fleet.saveReportSettings(scope.folderId, { ...next });
  }
  function applyRs() {
    const num = (v: unknown, def: number) => (Number.isFinite(Number(v)) && Number(v) > 0 ? Math.round(Number(v)) : def);
    const words = (t: string) => [...new Set(t.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean))];
    saveRs({
      expectedUnits: words(unitsText),
      expectedContainers: words(containersText),
      containerRestartDays: num(draft.containerRestartDays, rs.containerRestartDays),
      diskWarn: num(draft.diskWarn, rs.diskWarn),
      diskBad: num(draft.diskBad, rs.diskBad),
      securityGraceDays: num(draft.securityGraceDays, rs.securityGraceDays),
      rebootGraceDays: num(draft.rebootGraceDays, rs.rebootGraceDays),
      staleDays: num(draft.staleDays, rs.staleDays),
    });
    if (authorDraft.trim() !== fleet.reportAuthor) fleet.saveReportAuthor(authorDraft.trim());
    showRs = false;
  }
  // Logo: raster images are scaled to 112 px high (sharp at the 56 px the
  // report shows, on a 2x screen) and stored as PNG; SVG is kept as is.
  let logoInput = $state<HTMLInputElement | null>(null);
  let logoErr = $state("");
  async function onLogo(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    (e.target as HTMLInputElement).value = "";
    if (!f) return;
    logoErr = "";
    try {
      const url = await new Promise<string>((res, rej) => {
        const fr = new FileReader();
        fr.onload = () => res(String(fr.result));
        fr.onerror = () => rej(fr.error);
        fr.readAsDataURL(f);
      });
      if (f.type === "image/svg+xml") {
        await fleet.saveReportLogo(url);
        return;
      }
      const img = new Image();
      await new Promise<void>((res, rej) => { img.onload = () => res(); img.onerror = () => rej(new Error("not an image")); img.src = url; });
      const h = Math.min(112, img.naturalHeight);
      const w = Math.round(img.naturalWidth * (h / img.naturalHeight));
      const c = document.createElement("canvas");
      c.width = w; c.height = h;
      c.getContext("2d")!.drawImage(img, 0, 0, w, h);
      await fleet.saveReportLogo(c.toDataURL("image/png"));
    } catch (err: any) {
      logoErr = errMsg(err);
    }
  }
  function expectContainer(n: string) {
    const cur = rs.expectedContainers ?? [];
    if (!cur.includes(n)) saveRs({ ...rs, expectedContainers: [...cur, n].sort() });
  }
  function expectUnit(u: string) {
    if (!rs.expectedUnits.includes(u)) saveRs({ ...rs, expectedUnits: [...rs.expectedUnits, u].sort() });
  }
  let view = $state<"table" | "report">("table");
  let reportRef = $state<{ html: () => string } | null>(null);
  const earlier = $derived(scope.folderId && snap ? (fleet.history[scope.folderId] ?? []).filter((h) => h.at < snap!.at) : []);
  async function saveReport() {
    const body = reportRef?.html();
    if (!body || !snap) return;
    const d = new Date(snap.at);
    const day = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
    const title = `Health report ${scope.label} ${day}`.replace(/[<>&]/g, "");
    const doc = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${title}</title><style>body{margin:0;background:#fff}${REPORT_CSS}</style></head><body>${body}</body></html>`;
    try {
      const path = await api.saveTextFile(`health-${scope.label.replace(/[^a-z0-9._-]+/gi, "-")}-${day}.html`, doc);
      if (path) toast.push("ok", `Saved ${path} - open it in a browser to print or save as PDF`);
    } catch (e: any) {
      toast.err(errMsg(e));
    }
  }

  async function run() {
    running = true;
    runErr = "";
    try {
      const results = await api.gatherFacts({
        connection_ids: scope.folderId ? await folderHostIdsLoaded([scope.folderId]) : scope.ids,
        facts: pickedKeys,
        custom: custom.trim(),
        timeout_seconds: timeout,
      });
      snap = { at: Date.now(), facts: [...pickedKeys, ...(custom.trim() ? ["custom"] : [])], results: results ?? [] };
      if (scope.folderId) {
        await fleet.saveSnapshot(scope.folderId, snap);
        api.settingsSet(`fleet_facts_last:${scope.folderId}`, JSON.stringify({ preset, facts: pickedKeys, custom: custom.trim(), timeout })).catch(console.warn);
      }
    } catch (e: any) {
      runErr = errMsg(e);
    } finally {
      running = false;
    }
  }

  // ---- report ----
  let groupBy = $state<"size" | "os" | "kernel" | "none">("size");
  let filter = $state("");
  let similarTo = $state<FactsHostResult | null>(null);
  const has = (k: string) => !!snap?.facts.includes(k);

  function gib(kb: number): string {
    if (!kb) return "-";
    const g = kb / 1048576;
    return g >= 10 ? `${Math.round(g)} GiB` : `${(Math.round(g * 10) / 10).toFixed(1)} GiB`;
  }
  // RAM as the OS reports it is a bit under the nominal size (7.6 GiB for
  // an 8 GiB box): round to the size a person would order.
  // Exports carry whole numbers only: a decimal point reads as a date or
  // text in spreadsheets set to a comma-decimal locale. Disks in GiB (whole
  // GiB is precise enough there), memory in MiB (7.6 GiB would round to 8).
  function gibNum(kb: number): string {
    return kb ? String(Math.round(kb / 1048576)) : "";
  }
  function mibNum(kb: number): string {
    return kb ? String(Math.round(kb / 1024)) : "";
  }
  // Export header and cell: sizes go out as numbers with the unit moved up.
  function exHead(c: { h: string; n?: unknown; unit?: string }): string {
    return c.n ? `${c.h} (${c.unit ?? "GiB"})` : c.h;
  }
  function exCell(c: { v: (r: FactsHostResult) => string; n?: (r: FactsHostResult) => string }, r: FactsHostResult): string {
    return c.n ? c.n(r) : c.v(r);
  }
  function memClass(kb: number): string {
    if (!kb) return "?";
    const g = kb / 1048576;
    const steps = [0.5, 1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512, 768, 1024];
    const s = steps.find((x) => g <= x * 1.02) ?? Math.round(g);
    return `${s} GiB`;
  }
  // The host's own filesystems: network shares (CIFS, NFS) are listed in
  // their own column but stay out of totals and "Find similar".
  function localDisks(r: FactsHostResult) {
    return (r.facts.disks ?? []).filter((d) => !d.network);
  }
  function diskLayout(r: FactsHostResult): string {
    return (r.facts.disks ?? []).map((d) => `${d.mount}${d.network ? " (net)" : ""} ${gib(d.size_kb)}${d.used_pct !== undefined ? ` ${d.used_pct}%` : ""}`).join(", ") || "-";
  }
  // Same mount points, each within 5% of the other's size: two "80 GB"
  // disks never report the same byte count, and rounding to whole GiB
  // would still split 79.4 from 80.2.
  function sameDisks(a: FactsHostResult, b: FactsHostResult): boolean {
    const da = localDisks(a), db = localDisks(b);
    if (da.length !== db.length) return false;
    const bm = new Map(db.map((d) => [d.mount, d.size_kb]));
    return da.every((d) => {
      const o = bm.get(d.mount);
      return o !== undefined && Math.abs(o - d.size_kb) <= 0.05 * Math.max(o, d.size_kb);
    });
  }
  function diskTotalKB(r: FactsHostResult): number {
    return localDisks(r).reduce((n, d) => n + d.size_kb, 0);
  }
  function sizeKey(r: FactsHostResult): string {
    const f = r.facts;
    return `${f.cpu_cores || "?"} cores · ${memClass(f.mem_kb)}`;
  }
  function uptime(sec: number): string {
    if (!sec) return "-";
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600);
    return d > 0 ? `${d}d ${h}h` : `${h}h ${Math.floor((sec % 3600) / 60)}m`;
  }

  // Highest use across a host's filesystems, with the mount it is on.
  function maxUse(list: { mount: string; pct: number }[]): { mount: string; pct: number } | null {
    return list.reduce<{ mount: string; pct: number } | null>((m, d) => (!m || d.pct > m.pct ? d : m), null);
  }
  function diskUse(r: FactsHostResult) {
    return maxUse((r.facts.disks ?? []).filter((d) => d.used_pct !== undefined).map((d) => ({ mount: d.mount, pct: d.used_pct! })));
  }
  function inodeUse(r: FactsHostResult) {
    return maxUse(r.facts.inodes ?? []);
  }
  const DISK_WARN = $derived(rs.diskWarn), DISK_BAD = $derived(rs.diskBad);
  const PATCH_OLD_DAYS = $derived(rs.staleDays);
  // Age of the last package change as of the snapshot, not of today: a
  // report opened next week must say what was true when it was collected.
  function patchAgeDays(r: FactsHostResult): number | null {
    const t = r.facts.last_patch ?? 0;
    return t > 0 && snap ? Math.floor((snap.at / 1000 - t) / 86400) : null;
  }
  function isoDate(sec: number): string {
    const d = new Date(sec * 1000);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  }

  const okRows = $derived((snap?.results ?? []).filter((r) => r.state === "ok"));
  const badRows = $derived((snap?.results ?? []).filter((r) => r.state !== "ok"));
  const shown = $derived(okRows.filter((r) => {
    if (filter) {
      const hay = `${r.name} ${r.hostname} ${r.facts.os} ${r.facts.kernel} ${r.facts.cpu_model}`.toLowerCase();
      if (!hay.includes(filter.toLowerCase())) return false;
    }
    if (similarTo) {
      return sizeKey(r) === sizeKey(similarTo) && (!has("disks") || sameDisks(r, similarTo));
    }
    return true;
  }));
  // Header click sorts by that column (asc, desc, back to name); groups
  // keep their order and sort inside. Exports follow the table.
  let sortBy = $state("");
  let sortDesc = $state(false);
  function sortOn(h: string) {
    if (sortBy !== h) { sortBy = h; sortDesc = false; }
    else if (!sortDesc) sortDesc = true;
    else { sortBy = ""; sortDesc = false; }
  }
  const sorted = $derived.by(() => {
    const byName = (a: FactsHostResult, b: FactsHostResult) => a.name.localeCompare(b.name, undefined, { numeric: true });
    const col = tableCols.find((c) => c.h === sortBy);
    if (!col) return [...shown].sort(byName);
    const key = (r: FactsHostResult): number | string | null => {
      if (col.s) return col.s(r);
      if (col.n) { const x = parseFloat(col.n(r)); return Number.isFinite(x) ? x : null; }
      const v = col.v(r);
      return v === "" || v === "-" ? null : v;
    };
    const dir = sortDesc ? -1 : 1;
    return [...shown].sort((a, b) => {
      const ka = key(a), kb = key(b);
      // Hosts without a value sink to the bottom either way.
      if (ka === null || kb === null) return ka === kb ? byName(a, b) : ka === null ? 1 : -1;
      const d = typeof ka === "number" && typeof kb === "number"
        ? ka - kb
        : String(ka).localeCompare(String(kb), undefined, { numeric: true });
      return d * dir || byName(a, b);
    });
  });
  const groups = $derived.by(() => {
    const key = (r: FactsHostResult) =>
      groupBy === "size" ? sizeKey(r) : groupBy === "os" ? (r.facts.os || "?") : groupBy === "kernel" ? (r.facts.kernel || "?") : "";
    const m = new Map<string, FactsHostResult[]>();
    for (const r of sorted) {
      const k = key(r);
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(r);
    }
    return [...m.entries()].sort((a, b) => b[1].length - a[1].length);
  });
  const sizeChips = $derived.by(() => {
    const m = new Map<string, number>();
    for (const r of okRows) m.set(sizeKey(r), (m.get(sizeKey(r)) ?? 0) + 1);
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  });
  const needReboot = $derived(okRows.filter((r) => r.facts.reboot === "yes").length);
  const withSecurity = $derived(okRows.filter((r) => r.facts.security > 0).length);
  const withFailed = $derived(okRows.filter((r) => r.facts.failed_units ? r.facts.failed_units.some((u) => !rs.expectedUnits.includes(u)) : r.facts.failed > 0).length);
  const diskFull = $derived(okRows.filter((r) => Math.max(diskUse(r)?.pct ?? 0, inodeUse(r)?.pct ?? 0) >= DISK_WARN).length);
  const oldKernel = $derived(okRows.filter((r) => r.facts.kernel_pending === "yes").length);
  const notPatched = $derived(okRows.filter((r) => (patchAgeDays(r) ?? 0) >= PATCH_OLD_DAYS).length);

  // Every mount point seen in this report, "/" first. Up to MAX_MOUNT_COLS
  // of them each get a column (a host without that mount leaves it empty),
  // so Excel can sort and filter per mount; past that the table would get
  // too wide and they fold back into one list cell.
  const MAX_MOUNT_COLS = 6;
  const netMounts = $derived(new Set(okRows.flatMap((r) => (r.facts.disks ?? []).filter((d) => d.network).map((d) => d.mount))));
  const allMounts = $derived.by(() => {
    const set = new Set<string>();
    for (const r of okRows) for (const d of r.facts.disks ?? []) set.add(d.mount);
    return [...set].sort((a, b) => (a === "/" ? -1 : b === "/" ? 1 : a.localeCompare(b)));
  });

  // One place that decides the columns, used by the table, CSV and Markdown.
  const columns = $derived.by(() => {
    // n: the plain number for exports (CSV, Copy table), so a spreadsheet
    // sorts and sums it; the header then names the unit instead.
    // s: a sort key where neither the text nor n sorts right; null = no value.
    // only: a column that exists in the table or in the exports alone (the
    // table shows "40 GiB · 72%" in one cell, exports split it in two).
    const c: { h: string; v: (r: FactsHostResult) => string; n?: (r: FactsHostResult) => string; unit?: string; s?: (r: FactsHostResult) => number | string | null; only?: "table" | "export" }[] = [
      { h: "Host", v: (r) => r.name },
      { h: "Address", v: (r) => r.hostname },
    ];
    if (has("cpu")) {
      c.push({ h: "Cores", v: (r) => String(r.facts.cpu_cores || "-"), s: (r) => r.facts.cpu_cores || null });
      c.push({ h: "CPU", v: (r) => r.facts.cpu_model || "-" });
    }
    if (has("mem")) {
      c.push({ h: "RAM", v: (r) => gib(r.facts.mem_kb), n: (r) => mibNum(r.facts.mem_kb), unit: "MiB" });
      c.push({ h: "Swap", v: (r) => gib(r.facts.swap_kb), n: (r) => mibNum(r.facts.swap_kb), unit: "MiB" });
    }
    if (has("disks")) {
      if (allMounts.length <= MAX_MOUNT_COLS) {
        for (const m of allMounts) {
          const dOf = (r: FactsHostResult) => (r.facts.disks ?? []).find((x) => x.mount === m);
          const iOf = (r: FactsHostResult) => (r.facts.inodes ?? []).find((x) => x.mount === m)?.pct;
          const h = netMounts.has(m) ? `${m} (net)` : m;
          // Inodes only show in the cell when they are the problem.
          c.push({ h, only: "table", v: (r) => {
            const d = dOf(r), i = iOf(r);
            if (!d) return "";
            return `${gib(d.size_kb)}${d.used_pct !== undefined ? ` · ${d.used_pct}%` : ""}${i !== undefined && i >= DISK_WARN ? ` · inodes ${i}%` : ""}`;
          }, s: (r) => dOf(r)?.used_pct ?? null });
          c.push({ h, only: "export", v: () => "", n: (r) => { const d = dOf(r); return d ? gibNum(d.size_kb) : ""; } });
          c.push({ h: `${h} used`, only: "export", v: () => "", n: (r) => String(dOf(r)?.used_pct ?? ""), unit: "%" });
          if (has("inodes")) c.push({ h: `${h} inodes`, only: "export", v: () => "", n: (r) => String(iOf(r) ?? ""), unit: "%" });
        }
      } else {
        c.push({ h: "Disks", v: diskLayout });
      }
      c.push({ h: "Disk total", v: (r) => gib(diskTotalKB(r)), n: (r) => gibNum(diskTotalKB(r)) });
    }
    if (has("virt")) c.push({ h: "Virt", v: (r) => r.facts.virt === "none" ? "bare metal" : r.facts.virt || "-" });
    if (has("os")) c.push({ h: "OS", v: (r) => r.facts.os || "-" });
    if (has("kernel")) c.push({ h: "Kernel", v: (r) => r.facts.kernel || "-" });
    if (has("uptime")) c.push({ h: "Uptime", v: (r) => uptime(r.facts.uptime_sec), n: (r) => r.facts.uptime_sec > 0 ? String(Math.floor(r.facts.uptime_sec / 86400)) : "", unit: "days",
      s: (r) => r.facts.uptime_sec > 0 ? r.facts.uptime_sec : null });
    if (has("timesync")) c.push({ h: "NTP", v: (r) => r.facts.timesync || "-" });
    if (has("updates")) {
      c.push({ h: "Updates", v: (r) => r.facts.updates < 0 ? "-" : String(r.facts.updates), s: (r) => r.facts.updates < 0 ? null : r.facts.updates });
      c.push({ h: "Security", v: (r) => r.facts.security < 0 ? "-" : String(r.facts.security), s: (r) => r.facts.security < 0 ? null : r.facts.security });
    }
    if (has("lastpatch")) c.push({ h: "Last update", v: (r) => { const t = r.facts.last_patch ?? 0; const d = patchAgeDays(r); return t > 0 ? `${isoDate(t)} (${d}d)` : "-"; },
      n: (r) => (r.facts.last_patch ?? 0) > 0 ? isoDate(r.facts.last_patch!) : "", unit: "date", s: (r) => (r.facts.last_patch ?? 0) || null });
    if (has("kernelpending")) c.push({ h: "Newer kernel", v: (r) => r.facts.kernel_pending === "yes" ? (r.facts.kernel_latest || "yes") : r.facts.kernel_pending || "-" });
    if (has("reboot")) c.push({ h: "Reboot", v: (r) => r.facts.reboot || "-" });
    if (has("reboots")) c.push({ h: "Boots 30d", v: (r) => (r.facts.reboots_30d ?? -1) < 0 ? "-" : String(r.facts.reboots_30d), s: (r) => (r.facts.reboots_30d ?? -1) < 0 ? null : r.facts.reboots_30d! });
    if (has("failed")) {
      c.push({ h: "Failed", v: (r) => r.facts.failed < 0 ? "-" : String(r.facts.failed), s: (r) => r.facts.failed < 0 ? null : r.facts.failed });
      // Older snapshots carry only the count.
      if (okRows.some((r) => r.facts.failed_units)) c.push({ h: "Failed units", v: (r) => (r.facts.failed_units ?? []).join(" ") || "" });
    }
    if (has("containers")) c.push({ h: "Containers", v: (r) => {
      const f = r.facts;
      if (f.container_access === "noaccess") return "no access";
      if (f.container_access === "none") return "-";
      const list = f.containers ?? [];
      if (!list.length) return f.container_access === "ok" ? "0" : "-";
      const run = list.filter((x) => x.state === "running").length;
      const bad = containerIssues(f, rs, snap?.at ?? Date.now()).problems;
      return `${run}/${list.length} running${bad.length ? ` · ${bad.map((x) => `${x.name} ${containerProblem(x)}`).join(", ")}` : ""}`;
    } });
    if (has("ips")) c.push({ h: "IPs", v: (r) => (r.facts.ips ?? []).join(" ") || "-" });
    if (has("gateway")) c.push({ h: "Gateway", v: (r) => r.facts.gateway || "-" });
    if (has("dns")) c.push({ h: "DNS", v: (r) => (r.facts.dns ?? []).join(" ") || "-" });
    if (has("ports")) c.push({ h: "Ports", v: (r) => (r.facts.ports ?? []).join(" ") || "-" });
    if (has("custom")) c.push({ h: "Custom", v: (r) => r.facts.custom || "-" });
    return c;
  });
  const tableCols = $derived(columns.filter((c) => c.only !== "export"));
  const exportCols = $derived(columns.filter((c) => c.only !== "table"));
  const appendix = $derived({ headers: tableCols.map((c) => c.h), rows: sorted.map((r) => tableCols.map((c) => c.v(r))) });

  function cellClass(h: string, r: FactsHostResult): string {
    if (h === "Reboot" && r.facts.reboot === "yes") return "fwarn";
    if (h === "Security" && r.facts.security > 0) return "ferr";
    if ((h === "Failed" || h === "Failed units") && r.facts.failed > 0) return "ferr";
    // A mount column: the fuller of space and inodes decides.
    const mount = h.replace(/ \(net\)$/, "");
    if (allMounts.includes(mount)) {
      const d = (r.facts.disks ?? []).find((x) => x.mount === mount)?.used_pct ?? 0;
      const i = (r.facts.inodes ?? []).find((x) => x.mount === mount)?.pct ?? 0;
      const p = Math.max(d, i);
      return p >= DISK_BAD ? "ferr" : p >= DISK_WARN ? "fwarn" : "";
    }
    if (h === "Newer kernel" && r.facts.kernel_pending === "yes") return "fwarn";
    if (h === "Containers") {
      const ci = containerIssues(r.facts, rs, snap?.at ?? Date.now());
      return ci.problems.length ? "ferr" : ci.restarted.length ? "fwarn" : "";
    }
    if (h === "Last update" && (patchAgeDays(r) ?? 0) >= PATCH_OLD_DAYS) return "fwarn";
    return "";
  }

  function csv(): string {
    const esc = (v: string) => /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
    const lines = [exportCols.map((c) => esc(exHead(c))).join(",")];
    for (const r of sorted) lines.push(exportCols.map((c) => esc(exCell(c, r))).join(","));
    return lines.join("\n") + "\n";
  }
  function markdown(): string {
    const cell = (v: string) => v.replace(/\|/g, "\\|");
    const lines = [
      `| ${tableCols.map((c) => c.h).join(" | ")} |`,
      `| ${tableCols.map(() => "---").join(" | ")} |`,
      ...sorted.map((r) => `| ${tableCols.map((c) => cell(c.v(r))).join(" | ")} |`),
    ];
    return lines.join("\n") + "\n";
  }
  // The same rows as a real table: Teams and Outlook paste the HTML as a
  // table, Excel splits it into cells. The tab-separated text is the plain
  // fallback, which Excel also splits whatever the locale's list separator.
  function tsv(): string {
    const cell = (v: string) => v.replace(/[\t\r\n]+/g, " ");
    return [exportCols.map(exHead), ...sorted.map((r) => exportCols.map((c) => exCell(c, r)))]
      .map((row) => row.map(cell).join("\t")).join("\n") + "\n";
  }
  function html(): string {
    const esc = (v: string) => v.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    const th = exportCols.map((c) => `<th style="text-align:left;border:1px solid #999;padding:2px 6px">${esc(exHead(c))}</th>`).join("");
    const rows = sorted.map((r) => `<tr>${exportCols.map((c) => `<td style="border:1px solid #999;padding:2px 6px">${esc(exCell(c, r))}</td>`).join("")}</tr>`).join("");
    return `<table style="border-collapse:collapse"><thead><tr>${th}</tr></thead><tbody>${rows}</tbody></table>`;
  }
  // Same clipboard pattern as Run command's "Copy all": both flavours in
  // one write, the Go-side plain text when the rich write is missing or
  // refused (permissions, unfocused document).
  let copiedHint = $state(false);
  async function copyTable() {
    const text = tsv();
    const done = () => {
      copiedHint = true;
      setTimeout(() => { copiedHint = false; }, 1600);
    };
    try {
      if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
        await navigator.clipboard.write([
          new ClipboardItem({
            "text/html": new Blob([html()], { type: "text/html" }),
            "text/plain": new Blob([text], { type: "text/plain" }),
          }),
        ]);
      } else {
        await api.clipboardSetText(text);
      }
      done();
    } catch (e: any) {
      try {
        await api.clipboardSetText(text);
        done();
      } catch {
        toast.err("Copy failed: " + errMsg(e));
      }
    }
  }
  async function exportCsv() {
    try {
      // The BOM tells Excel the file is UTF-8, so names with accents survive.
      const path = await api.saveTextFile(`facts-${scope.label.replace(/[^a-z0-9._-]+/gi, "-")}.csv`, "\ufeff" + csv());
      if (path) toast.push("ok", `Saved ${path}`);
    } catch (e: any) {
      toast.err(errMsg(e));
    }
  }
  async function copyMd() {
    await api.clipboardSetText(markdown());
    toast.push("ok", "Report copied as Markdown");
  }
  function openHost(r: FactsHostResult) {
    if (r.connection_id.startsWith("dyn:")) return;
    fleet.close();
    connectionActions.connectMany([r.connection_id]);
  }
</script>

<FleetShell
  title={snap ? `Facts: ${scope.label}` : "Gather facts"}
  sub={snap
    ? `${snap.results.length} hosts · ${okRows.length} answered${badRows.length ? ` · ${badRows.length} failed` : ""} · collected ${new Date(snap.at).toLocaleString()}`
    : `${scope.label} · ${hostIds.length} host${hostIds.length === 1 ? "" : "s"} · read-only commands, nothing is changed on the hosts`}
  wide={!!snap}
  onClose={() => fleet.close()}
>
  {#if !snap}
    <div class="form">
      <div class="row">
        <span class="fdim">Preset</span>
        <select class="fselect" value={preset} onchange={(e) => applyPreset((e.target as HTMLSelectElement).value)}>
          {#each catalog?.presets ?? [] as p (p.key)}<option value={p.key}>{p.label}</option>{/each}
          {#if userPresets.length}
            <optgroup label="Saved">
              {#each userPresets as u (u.name)}<option value={USER + u.name}>{u.name}</option>{/each}
            </optgroup>
          {/if}
          <option value="custom">Custom</option>
        </select>
        {#if savingPreset}
          <input class="finput" bind:value={presetName} placeholder="Preset name" onkeydown={(e) => { if (e.key === "Enter") savePreset(); if (e.key === "Escape") savingPreset = false; }} />
          <button class="fbtn primary" disabled={!presetName.trim() || (pickedKeys.length === 0 && !custom.trim())} onclick={savePreset}>Save</button>
          <button class="fbtn" onclick={() => (savingPreset = false)}>Cancel</button>
        {:else}
          <button class="fbtn" disabled={pickedKeys.length === 0 && !custom.trim()} title="Keep the ticked facts, custom column and timeout under a name" onclick={() => { presetName = preset.startsWith(USER) ? preset.slice(USER.length) : ""; savingPreset = true; }}>Save as preset…</button>
          {#if preset.startsWith(USER)}<button class="fbtn" title="Delete this saved preset" onclick={deletePreset}>Delete</button>{/if}
        {/if}
      </div>
      <div class="groups">
        {#each GROUPS as g (g.title)}
          <fieldset>
            <legend>{g.title}</legend>
            {#each g.facts as f (f.key)}
              <label class="check"><input type="checkbox" checked={!!picked[f.key]} onchange={() => toggle(f.key)} />{f.label}</label>
            {/each}
          </fieldset>
        {/each}
      </div>
      <label class="col">
        <span class="fdim">Custom column (optional): one read-only command, first line of its output</span>
        <input class="finput fmono" bind:value={custom} placeholder="nginx -v" spellcheck="false" />
      </label>
      <label class="row">
        <span class="fdim">Timeout per host</span>
        <input class="finput num" type="number" min="5" max="300" bind:value={timeout} /> <span class="fdim">s · 8 hosts at a time</span>
      </label>
      <p class="fdim note">Update counts come from each host's cached package lists (apt -s, dnf -C), so they are as fresh as the host's last apt update / dnf makecache. "Last package update" is the last upgrade in apt or dnf history (the last package change where neither exists); "Boots" reads wtmp and shows "-" where the host keeps none.</p>
      {#if runErr}<p class="ferr">{runErr}</p>{/if}
    </div>
  {:else}
    <div class="toolbar">
      <div class="seg" role="tablist">
        <button class="fbtn" class:primary={view === "table"} onclick={() => (view = "table")}>Table</button>
        <button class="fbtn" class:primary={view === "report"} onclick={() => (view = "report")}>Report</button>
      </div>
      {#if view === "report"}
        <button class="fbtn" onclick={openRs} title={scope.folderId ? "Thresholds and expected failed units, kept for this folder" : "Thresholds and expected failed units"}>Report settings…</button>
      {/if}
      {#if view === "table"}
      <label class="row"><span class="fdim">Group by</span>
        <select class="fselect" bind:value={groupBy}>
          <option value="size">Size (cores / RAM)</option>
          <option value="os">OS</option>
          <option value="kernel">Kernel</option>
          <option value="none">None</option>
        </select>
      </label>
      <input class="finput" type="search" placeholder="Filter hosts" bind:value={filter} />
      {#if similarTo}
        <span class="chip similar">Similar to {similarTo.name} · {shown.length} host{shown.length === 1 ? "" : "s"}</span>
        <button class="fbtn" onclick={() => (similarTo = null)}>Show all</button>
      {/if}
      <span class="spacer"></span>
      {#if has("reboot") && needReboot}<span class="chip warn">{needReboot} need reboot</span>{/if}
      {#if has("updates") && withSecurity}<span class="chip bad">{withSecurity} with security updates</span>{/if}
      {#if has("failed") && withFailed}<span class="chip bad">{withFailed} with failed units</span>{/if}
      {#if (has("disks") || has("inodes")) && diskFull}<span class="chip warn" title="A filesystem at {DISK_WARN}% or more, space or inodes">{diskFull} with a filesystem over {DISK_WARN}%</span>{/if}
      {#if has("kernelpending") && oldKernel}<span class="chip warn">{oldKernel} not on the newest kernel</span>{/if}
      {#if has("lastpatch") && notPatched}<span class="chip warn">{notPatched} not updated in {PATCH_OLD_DAYS}+ days</span>{/if}
      {/if}
    </div>
    {#if view === "report"}
      <div class="report-wrap">
        {#if showRs}
          <div class="rs-panel">
            <label class="col"><span class="fdim">Prepared by (on every report; empty leaves it out)</span>
              <input class="finput" bind:value={authorDraft} placeholder="Jane Doe, Example Ltd." /></label>
            <div class="row"><span class="fdim">Logo (on every report)</span>
              {#if fleet.reportLogo}<img class="logo-prev" src={fleet.reportLogo} alt="" />{/if}
              <button class="fbtn" onclick={() => logoInput?.click()}>{fleet.reportLogo ? "Change…" : "Choose…"}</button>
              {#if fleet.reportLogo}<button class="fbtn" onclick={() => fleet.saveReportLogo("")}>Remove</button>{/if}
              <input bind:this={logoInput} type="file" accept="image/png,image/svg+xml,image/jpeg,image/webp" hidden onchange={onLogo} />
              {#if logoErr}<span class="ferr">{logoErr}</span>{/if}
            </div>
            <label class="col"><span class="fdim">Expected failed units (not counted, listed apart), separated by spaces</span>
              <input class="finput fmono" bind:value={unitsText} placeholder="irqbalance.service" spellcheck="false" /></label>
            <label class="col"><span class="fdim">Containers stopped on purpose (not counted, listed apart), by name, separated by spaces</span>
              <input class="finput fmono" bind:value={containersText} placeholder="backup-job" spellcheck="false" /></label>
            <div class="rs-nums">
              <label class="row"><span class="fdim">Filesystem warn at</span><input class="finput num" type="number" min="1" max="100" bind:value={draft.diskWarn} /><span class="fdim">%</span></label>
              <label class="row"><span class="fdim">critical at</span><input class="finput num" type="number" min="1" max="100" bind:value={draft.diskBad} /><span class="fdim">%</span></label>
              <label class="row"><span class="fdim">Security updates flagged when the last update is</span><input class="finput num" type="number" min="1" bind:value={draft.securityGraceDays} /><span class="fdim">+ days old</span></label>
              <label class="row"><span class="fdim">Container restarts flagged within</span><input class="finput num" type="number" min="1" bind:value={draft.containerRestartDays} /><span class="fdim">days</span></label>
              <label class="row"><span class="fdim">Reboot pending flagged after</span><input class="finput num" type="number" min="1" bind:value={draft.rebootGraceDays} /><span class="fdim">days</span></label>
              <label class="row"><span class="fdim">Not updated after</span><input class="finput num" type="number" min="1" bind:value={draft.staleDays} /><span class="fdim">days</span></label>
            </div>
            <div class="row"><span class="spacer"></span><button class="fbtn" onclick={() => (showRs = false)}>Cancel</button><button class="fbtn primary" onclick={applyRs}>Apply</button></div>
          </div>
        {/if}
        <FactsReport bind:this={reportRef} label={scope.label} {snap} {earlier} {appendix} {rs} author={fleet.reportAuthor} logo={fleet.reportLogo} onExpectUnit={expectUnit} onExpectContainer={expectContainer}
          onDeleteRun={scope.folderId ? (at) => fleet.deleteRun(scope.folderId!, at) : undefined} />
      </div>
    {:else}
    {#if has("cpu") || has("mem")}
      <div class="sizes"><span class="fdim">Sizes:</span>
        {#each sizeChips as [k, n] (k)}<span class="chip">{k} <span class="fdim">×{n}</span></span>{/each}
      </div>
    {/if}
    <div class="table-wrap">
      <table class="ftable">
        <thead><tr>{#each tableCols as c, i (i)}<th class:fmono={c.h.startsWith("/")} aria-sort={sortBy === c.h ? (sortDesc ? "descending" : "ascending") : "none"}><button class="sort" class:on={sortBy === c.h} title="Sort" onclick={() => sortOn(c.h)}>{c.h}<span class="arr">{sortBy === c.h ? (sortDesc ? "▼" : "▲") : ""}</span></button></th>{/each}<th></th></tr></thead>
        <tbody>
          {#each groups as [g, rows] (g)}
            {#if groupBy !== "none"}<tr class="group"><td colspan={tableCols.length + 1}>{g} · {rows.length} host{rows.length === 1 ? "" : "s"}</td></tr>{/if}
            {#each rows as r (r.connection_id)}
              <tr>
                {#each tableCols as c, i (i)}
                  <td class={cellClass(c.h, r)}>
                    {#if i === 0}<button class="linkish fmono" title="Connect" onclick={() => openHost(r)}>{c.v(r)}</button>{:else}{c.v(r)}{/if}
                  </td>
                {/each}
                <td><button class="linkish" onclick={() => (similarTo = similarTo?.connection_id === r.connection_id ? null : r)}>{similarTo?.connection_id === r.connection_id ? "Show all" : "Find similar"}</button></td>
              </tr>
            {/each}
          {/each}
          {#if badRows.length}
            <tr class="group"><td colspan={tableCols.length + 1}>Did not answer · {badRows.length}</td></tr>
            {#each badRows as r (r.connection_id)}
              <tr><td class="fmono">{r.name}</td><td colspan={tableCols.length} class="ferr">{r.error || r.state}</td></tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
    {/if}
  {/if}

  {#snippet footer()}
    {#if !snap}
      <button class="fbtn" onclick={() => fleet.close()}>Cancel</button>
      <button class="fbtn primary" disabled={running || (pickedKeys.length === 0 && !custom.trim())} onclick={run}>
        {running ? `Collecting from ${hostIds.length} hosts…` : `Run on ${hostIds.length} host${hostIds.length === 1 ? "" : "s"}`}
      </button>
    {:else}
      <button class="fbtn" onclick={() => (snap = null)}>Change facts…</button>
      <span class="spacer"></span>
      {#if view === "report"}
        <button class="fbtn" onclick={saveReport} title="A single HTML file: open it in a browser to print or save as PDF">Save report…</button>
      {:else}
      <button class="fbtn" onclick={copyTable} title="Paste into Excel, Teams or Outlook as a table">{copiedHint ? "✓ Copied" : "Copy table"}</button>
      <button class="fbtn" onclick={copyMd}>Copy as Markdown</button>
      <button class="fbtn" onclick={exportCsv}>Export CSV</button>
      {/if}
      <button class="fbtn primary" disabled={running} onclick={run}>{running ? "Collecting…" : "Run again"}</button>
    {/if}
  {/snippet}
</FleetShell>

<style>
  .form { display: flex; flex-direction: column; gap: 0.8rem; }
  .row { display: flex; align-items: center; gap: 0.5rem; }
  .col { display: flex; flex-direction: column; gap: 0.3rem; }
  .groups { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.6rem; }
  fieldset { border: 1px solid var(--surface0); border-radius: 6px; margin: 0; padding: 0.4rem 0.7rem 0.55rem; display: flex; flex-direction: column; gap: 0.3rem; }
  legend { color: var(--subtext0); font-size: 0.68rem; letter-spacing: 0.06em; text-transform: uppercase; padding: 0 0.3rem; }
  .check { display: flex; gap: 0.45rem; align-items: center; cursor: pointer; }
  .num { width: 4.5rem; }
  .note { margin: 0; font-size: 0.74rem; }
  .toolbar { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-bottom: 0.5rem; }
  .sizes { display: flex; gap: 0.4rem; flex-wrap: wrap; align-items: center; margin-bottom: 0.6rem; }
  .spacer { flex: 1; }
  .table-wrap { overflow: auto; flex: 1 1 0 !important; min-height: 8rem; border: 1px solid var(--surface0); border-radius: 6px; }
  /* Host names stay in view while scrolling sideways through the columns. */
  .table-wrap :global(.ftable th:first-child),
  .table-wrap :global(.ftable td:first-child) { position: sticky; left: 0; background: var(--base); z-index: 1; }
  .table-wrap :global(.ftable th:first-child) { z-index: 2; }
  .table-wrap :global(.ftable tr.group td:first-child) { background: var(--mantle); }
  .similar { background: color-mix(in srgb, var(--mauve) 20%, transparent); color: var(--mauve); }
  .sort { background: none; border: 0; padding: 0; font: inherit; color: inherit; cursor: pointer; white-space: nowrap; display: inline-flex; gap: 0.25rem; align-items: center; }
  .sort:hover, .sort.on { color: var(--text); }
  .arr { font-size: 0.6rem; min-width: 0.6rem; }
  .seg { display: inline-flex; gap: 0.2rem; }
  .rs-panel { display: flex; flex-direction: column; gap: 0.5rem; padding: 0.6rem 0.8rem; margin: 0.5rem; border: 1px solid var(--surface1); border-radius: 6px; background: var(--mantle); color: var(--text); }
  .logo-prev { max-height: 28px; max-width: 120px; object-fit: contain; background: #fff; border-radius: 3px; padding: 2px; }
  .rs-nums { display: flex; flex-wrap: wrap; gap: 0.4rem 1rem; }
  .report-wrap { overflow: auto; flex: 1 1 0 !important; min-height: 8rem; border: 1px solid var(--surface0); border-radius: 6px; background: #fff; }
  .linkish { background: none; border: 0; padding: 0; color: var(--blue); font: inherit; cursor: pointer; }
  .linkish:hover { text-decoration: underline; }
</style>
