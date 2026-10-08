<script lang="ts">
  // Gather facts: pick read-only facts, run them on every host of the scope,
  // then read the report - grouped by size so "which servers look like this
  // one" is one click (Find similar), with reboot / security counts on top.
  import { onMount } from "svelte";
  import { api, type FactsHostResult } from "./api";
  import { fleet, type FleetOpen, type FactsSnapshot } from "./fleetStore.svelte";
  import { toast } from "./toast.svelte";
  import { connectionActions } from "./connectionActions.svelte";
  import FleetShell from "./FleetShell.svelte";
  import { errMsg } from "./connectErrors";

  interface Props { scope: FleetOpen; }
  let { scope }: Props = $props();

  const GROUPS: { title: string; facts: { key: string; label: string }[] }[] = [
    { title: "Hardware", facts: [
      { key: "cpu", label: "CPU cores and model" },
      { key: "mem", label: "Memory and swap" },
      { key: "disks", label: "Disks (size and use per filesystem)" },
      { key: "inodes", label: "Inode use" },
      { key: "virt", label: "Virtualization" },
    ] },
    { title: "System", facts: [
      { key: "os", label: "Distro and version" },
      { key: "kernel", label: "Kernel" },
      { key: "uptime", label: "Uptime" },
      { key: "timesync", label: "Time sync" },
    ] },
    { title: "Maintenance", facts: [
      { key: "updates", label: "Pending updates (and security)" },
      { key: "lastpatch", label: "Last package update" },
      { key: "kernelpending", label: "Newer kernel installed" },
      { key: "reboot", label: "Reboot required" },
      { key: "reboots", label: "Boots in the last 30 days" },
      { key: "failed", label: "Failed systemd units" },
    ] },
    { title: "Network", facts: [
      { key: "ips", label: "IP addresses" },
      { key: "gateway", label: "Default gateway" },
      { key: "dns", label: "DNS resolvers" },
      { key: "ports", label: "Listening TCP ports" },
    ] },
  ];
  const PRESETS: Record<string, string[]> = {
    sizing: ["cpu", "mem", "disks", "virt", "os", "kernel", "uptime"],
    patch: ["os", "kernel", "uptime", "updates", "lastpatch", "kernelpending", "reboot", "failed"],
    // What a recurring (monthly) report to a customer usually covers:
    // patch level, capacity, availability.
    report: ["os", "kernel", "uptime", "timesync", "updates", "lastpatch", "kernelpending", "reboot", "reboots", "failed", "disks", "inodes"],
    all: GROUPS.flatMap((g) => g.facts.map((f) => f.key)),
  };

  let preset = $state("sizing");
  let picked = $state<Record<string, boolean>>(Object.fromEntries(PRESETS.sizing.map((k) => [k, true])));
  let custom = $state("");
  let timeout = $state(30);
  let running = $state(false);
  let runErr = $state("");
  let snap = $state<FactsSnapshot | null>(null);

  function applyPreset(p: string) {
    preset = p;
    if (PRESETS[p]) picked = Object.fromEntries(PRESETS[p].map((k) => [k, true]));
  }
  function toggle(k: string) {
    picked[k] = !picked[k];
    preset = "custom";
  }
  const pickedKeys = $derived(Object.keys(picked).filter((k) => picked[k]));

  onMount(async () => {
    if (scope.showSnapshot && scope.folderId) {
      snap = await fleet.snapshot(scope.folderId);
    }
  });

  async function run() {
    running = true;
    runErr = "";
    try {
      const results = await api.gatherFacts({
        connection_ids: scope.ids,
        facts: pickedKeys,
        custom: custom.trim(),
        timeout_seconds: timeout,
      });
      snap = { at: Date.now(), facts: [...pickedKeys, ...(custom.trim() ? ["custom"] : [])], results: results ?? [] };
      if (scope.folderId) await fleet.saveSnapshot(scope.folderId, snap);
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
  const DISK_WARN = 80, DISK_BAD = 90;
  const PATCH_OLD_DAYS = 30;
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
    const col = columns.find((c) => c.h === sortBy);
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
  const withFailed = $derived(okRows.filter((r) => r.facts.failed > 0).length);
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
    const c: { h: string; v: (r: FactsHostResult) => string; n?: (r: FactsHostResult) => string; unit?: string; s?: (r: FactsHostResult) => number | string | null }[] = [
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
          const kbOf = (r: FactsHostResult) => (r.facts.disks ?? []).find((x) => x.mount === m)?.size_kb;
          const useOf = (r: FactsHostResult) => (r.facts.disks ?? []).find((x) => x.mount === m)?.used_pct;
          c.push({ h: netMounts.has(m) ? `${m} (net)` : m, v: (r) => { const k = kbOf(r), u = useOf(r); return k ? `${gib(k)}${u !== undefined ? ` · ${u}%` : ""}` : ""; }, n: (r) => { const k = kbOf(r); return k ? gibNum(k) : ""; } });
        }
      } else {
        c.push({ h: "Disks", v: diskLayout });
      }
      c.push({ h: "Disk total", v: (r) => gib(diskTotalKB(r)), n: (r) => gibNum(diskTotalKB(r)) });
      c.push({ h: "Disk use", v: (r) => { const u = diskUse(r); return u ? `${u.pct}% ${u.mount}` : "-"; },
        n: (r) => String(diskUse(r)?.pct ?? ""), unit: "% max", s: (r) => diskUse(r)?.pct ?? null });
    }
    if (has("inodes")) c.push({ h: "Inode use", v: (r) => { const u = inodeUse(r); return u ? `${u.pct}% ${u.mount}` : "-"; },
      n: (r) => String(inodeUse(r)?.pct ?? ""), unit: "% max", s: (r) => inodeUse(r)?.pct ?? null });
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
    if (has("ips")) c.push({ h: "IPs", v: (r) => (r.facts.ips ?? []).join(" ") || "-" });
    if (has("gateway")) c.push({ h: "Gateway", v: (r) => r.facts.gateway || "-" });
    if (has("dns")) c.push({ h: "DNS", v: (r) => (r.facts.dns ?? []).join(" ") || "-" });
    if (has("ports")) c.push({ h: "Ports", v: (r) => (r.facts.ports ?? []).join(" ") || "-" });
    if (has("custom")) c.push({ h: "Custom", v: (r) => r.facts.custom || "-" });
    return c;
  });
  function cellClass(h: string, r: FactsHostResult): string {
    if (h === "Reboot" && r.facts.reboot === "yes") return "fwarn";
    if (h === "Security" && r.facts.security > 0) return "ferr";
    if ((h === "Failed" || h === "Failed units") && r.facts.failed > 0) return "ferr";
    if (h === "Disk use" || h === "Inode use") {
      const p = (h === "Disk use" ? diskUse(r) : inodeUse(r))?.pct ?? 0;
      return p >= DISK_BAD ? "ferr" : p >= DISK_WARN ? "fwarn" : "";
    }
    if (h === "Newer kernel" && r.facts.kernel_pending === "yes") return "fwarn";
    if (h === "Last update" && (patchAgeDays(r) ?? 0) >= PATCH_OLD_DAYS) return "fwarn";
    return "";
  }

  function csv(): string {
    const esc = (v: string) => /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
    const lines = [columns.map((c) => esc(exHead(c))).join(",")];
    for (const r of sorted) lines.push(columns.map((c) => esc(exCell(c, r))).join(","));
    return lines.join("\n") + "\n";
  }
  function markdown(): string {
    const cell = (v: string) => v.replace(/\|/g, "\\|");
    const lines = [
      `| ${columns.map((c) => c.h).join(" | ")} |`,
      `| ${columns.map(() => "---").join(" | ")} |`,
      ...sorted.map((r) => `| ${columns.map((c) => cell(c.v(r))).join(" | ")} |`),
    ];
    return lines.join("\n") + "\n";
  }
  // The same rows as a real table: Teams and Outlook paste the HTML as a
  // table, Excel splits it into cells. The tab-separated text is the plain
  // fallback, which Excel also splits whatever the locale's list separator.
  function tsv(): string {
    const cell = (v: string) => v.replace(/[\t\r\n]+/g, " ");
    return [columns.map(exHead), ...sorted.map((r) => columns.map((c) => exCell(c, r)))]
      .map((row) => row.map(cell).join("\t")).join("\n") + "\n";
  }
  function html(): string {
    const esc = (v: string) => v.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    const th = columns.map((c) => `<th style="text-align:left;border:1px solid #999;padding:2px 6px">${esc(exHead(c))}</th>`).join("");
    const rows = sorted.map((r) => `<tr>${columns.map((c) => `<td style="border:1px solid #999;padding:2px 6px">${esc(exCell(c, r))}</td>`).join("")}</tr>`).join("");
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
    : `${scope.label} · ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"} · read-only commands, nothing is changed on the hosts`}
  wide={!!snap}
  onClose={() => fleet.close()}
>
  {#if !snap}
    <div class="form">
      <label class="row">
        <span class="fdim">Preset</span>
        <select class="fselect" value={preset} onchange={(e) => applyPreset((e.target as HTMLSelectElement).value)}>
          <option value="sizing">Sizing (hardware + OS)</option>
          <option value="patch">Patch day (updates, reboot, failed units)</option>
          <option value="report">Monthly report (patching, capacity, availability)</option>
          <option value="all">Everything</option>
          <option value="custom">Custom</option>
        </select>
      </label>
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
    </div>
    {#if has("cpu") || has("mem")}
      <div class="sizes"><span class="fdim">Sizes:</span>
        {#each sizeChips as [k, n] (k)}<span class="chip">{k} <span class="fdim">×{n}</span></span>{/each}
      </div>
    {/if}
    <div class="table-wrap">
      <table class="ftable">
        <thead><tr>{#each columns as c, i (i)}<th class:fmono={c.h.startsWith("/")} aria-sort={sortBy === c.h ? (sortDesc ? "descending" : "ascending") : "none"}><button class="sort" class:on={sortBy === c.h} title="Sort" onclick={() => sortOn(c.h)}>{c.h}<span class="arr">{sortBy === c.h ? (sortDesc ? "▼" : "▲") : ""}</span></button></th>{/each}<th></th></tr></thead>
        <tbody>
          {#each groups as [g, rows] (g)}
            {#if groupBy !== "none"}<tr class="group"><td colspan={columns.length + 1}>{g} · {rows.length} host{rows.length === 1 ? "" : "s"}</td></tr>{/if}
            {#each rows as r (r.connection_id)}
              <tr>
                {#each columns as c, i (i)}
                  <td class={cellClass(c.h, r)}>
                    {#if i === 0}<button class="linkish fmono" title="Connect" onclick={() => openHost(r)}>{c.v(r)}</button>{:else}{c.v(r)}{/if}
                  </td>
                {/each}
                <td><button class="linkish" onclick={() => (similarTo = similarTo?.connection_id === r.connection_id ? null : r)}>{similarTo?.connection_id === r.connection_id ? "Show all" : "Find similar"}</button></td>
              </tr>
            {/each}
          {/each}
          {#if badRows.length}
            <tr class="group"><td colspan={columns.length + 1}>Did not answer · {badRows.length}</td></tr>
            {#each badRows as r (r.connection_id)}
              <tr><td class="fmono">{r.name}</td><td colspan={columns.length} class="ferr">{r.error || r.state}</td></tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  {/if}

  {#snippet footer()}
    {#if !snap}
      <button class="fbtn" onclick={() => fleet.close()}>Cancel</button>
      <button class="fbtn primary" disabled={running || (pickedKeys.length === 0 && !custom.trim())} onclick={run}>
        {running ? `Collecting from ${scope.ids.length} hosts…` : `Run on ${scope.ids.length} host${scope.ids.length === 1 ? "" : "s"}`}
      </button>
    {:else}
      <button class="fbtn" onclick={() => (snap = null)}>Change facts…</button>
      <span class="spacer"></span>
      <button class="fbtn" onclick={copyTable} title="Paste into Excel, Teams or Outlook as a table">{copiedHint ? "✓ Copied" : "Copy table"}</button>
      <button class="fbtn" onclick={copyMd}>Copy as Markdown</button>
      <button class="fbtn" onclick={exportCsv}>Export CSV</button>
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
  .linkish { background: none; border: 0; padding: 0; color: var(--blue); font: inherit; cursor: pointer; }
  .linkish:hover { text-decoration: underline; }
</style>
