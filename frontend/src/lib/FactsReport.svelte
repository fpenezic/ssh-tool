<script lang="ts" module>
  // The report's own stylesheet, also embedded in the saved HTML: the page
  // must look the same in a browser, printed to PDF, or mailed.
  export const REPORT_CSS = `
.rp { -webkit-print-color-adjust: exact; print-color-adjust: exact; font: 13px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; color: #1f2328; background: #fff; padding: 1.2rem 1.4rem; }
.rp h1 { font-size: 1.35rem; margin: 0 0 0.2rem; }
.rp .rp-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.rp .rp-logo { max-height: 56px; max-width: 220px; object-fit: contain; }
.rp h2 { font-size: 1rem; margin: 1.4rem 0 0.5rem; padding-bottom: 0.2rem; border-bottom: 1px solid #d0d7de; }
.rp .rp-meta { color: #59636e; margin: 0 0 0.8rem; }
.rp .rp-ctl { margin: 0 0 0.8rem; padding-bottom: 0.5rem; border-bottom: 1px dashed #d0d7de; }
.rp .rp-sign { margin-top: -0.6rem; }
.rp .rp-end { margin: 1.2rem 0 0; padding-top: 0.4rem; border-top: 1px solid #d0d7de; }
.rp .rp-tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(9.5rem, 1fr)); gap: 0.5rem; }
.rp .rp-tile { border: 1px solid #d0d7de; border-radius: 6px; padding: 0.45rem 0.6rem; }
.rp .rp-tile b { display: block; font-size: 1.35rem; line-height: 1.2; }
.rp .rp-tile span { color: #59636e; font-size: 0.78rem; }
.rp .rp-tile.red { border-color: #cf222e; } .rp .rp-tile.red b { color: #cf222e; }
.rp .rp-tile.yellow { border-color: #bf8700; } .rp .rp-tile.yellow b { color: #9a6700; }
.rp .rp-tile.green b { color: #1a7f37; }
.rp .rp-bar { display: flex; height: 1.5rem; border-radius: 4px; overflow: hidden; margin: 0.2rem 0 0.4rem; font-size: 0.78rem; font-weight: 600; }
.rp .rp-bar span { display: flex; align-items: center; justify-content: center; color: #fff; white-space: nowrap; overflow: hidden; min-width: 2.5rem; }
.rp .rp-bar .g { background: #1a7f37; } .rp .rp-bar .y { background: #bf8700; } .rp .rp-bar .r { background: #cf222e; }
.rp .rp-lead { font-size: 0.95rem; margin: 0 0 0.6rem; }
.rp .rp-sec { border-left: 3px solid #d0d7de; padding: 0.1rem 0 0.1rem 0.7rem; margin: 0.6rem 0 0.9rem; break-inside: avoid-page; }
.rp .rp-sec.red { border-left-color: #cf222e; } .rp .rp-sec.yellow { border-left-color: #d4a72c; }
.rp .rp-sec h3 { margin-top: 0.2rem; }
.rp .rp-sec p { margin: 0 0 0.3rem; }
.rp .rp-info-h { color: #59636e; }
.rp .rp-info { margin: 0.2rem 0 0; padding-left: 1.1rem; color: #59636e; }
.rp .rp-info li { margin: 0.15rem 0; }
.rp .rp-hosts { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-top: 0.6rem; }
.rp .rp-host { border-radius: 4px; padding: 0.05rem 0.45rem; font-size: 0.8rem; border: 1px solid; }
.rp .rp-host.red { color: #cf222e; border-color: #cf222e; background: #ffebe9; }
.rp .rp-host.yellow { color: #9a6700; border-color: #d4a72c; background: #fff8c5; }
.rp .rp-host.green { color: #1a7f37; border-color: #4ac26b; background: #dafbe1; }
.rp table { border-collapse: collapse; width: 100%; margin: 0.2rem 0 0.4rem; }
.rp th, .rp td { text-align: left; border-bottom: 1px solid #d0d7de; padding: 0.22rem 0.5rem 0.22rem 0; vertical-align: top; }
.rp th { color: #59636e; font-weight: 600; font-size: 0.78rem; }
.rp td.num, .rp th.num { text-align: right; padding-right: 0.8rem; }
.rp .red { color: #cf222e; } .rp .yellow { color: #9a6700; } .rp .green { color: #1a7f37; } .rp .dim { color: #59636e; }
.rp .rp-none { color: #1a7f37; margin: 0.2rem 0; }
.rp .rp-appendix { font-size: 0.75rem; overflow-x: auto; }
.rp svg { vertical-align: middle; }
.rp .rp-link { background: none; border: 0; padding: 0; margin-left: 0.25rem; color: #0969da; font: inherit; font-size: 0.75rem; cursor: pointer; }
.rp .rp-link:hover { text-decoration: underline; }
.rp h3 { font-size: 0.86rem; margin: 0.8rem 0 0.2rem; }
@page wide { size: A4 landscape; }
@media print { .rp .rp-appendix-wrap { page: wide; break-before: page; } .rp .rp-appendix { overflow: visible; font-size: 7.5pt; } .rp .rp-appendix td, .rp .rp-appendix th { padding: 0.12rem 0.3rem 0.12rem 0; } .rp { padding: 0; } .rp .rp-noprint { display: none; } .rp h2 { break-after: avoid; } .rp tr { break-inside: avoid; } }
`;
</script>

<script lang="ts">
  // Health report over one facts run: a summary with a status per host,
  // only the findings that need someone, what changed since an earlier
  // run, and disk trends over the stored runs. The full table goes last as
  // an appendix. Everything is derived from stored snapshots; nothing is
  // fetched here.
  import type { FactsHostResult } from "./api";
  import { fleet, containerIssues, containerProblem, type FactsSnapshot, type ReportSettings } from "./fleetStore.svelte";

  interface Props {
    label: string;
    snap: FactsSnapshot;
    // Earlier runs of the same scope, oldest first (snap itself excluded).
    earlier: FactsSnapshot[];
    appendix: { headers: string[]; rows: string[][] };
    rs: ReportSettings;
    // Who prepared the report; "" leaves the line out.
    author: string;
    // Company logo as a data: URI; "" for none.
    logo: string;
    // In-app only: mark a failed unit as expected for this scope.
    onExpectUnit?: (unit: string) => void;
    // In-app only: mark a container as stopped on purpose.
    onExpectContainer?: (name: string) => void;
    // In-app only: drop a stored run of this folder.
    onDeleteRun?: (at: number) => void;
  }
  let { label, snap, earlier, appendix, rs, author, logo, onExpectUnit, onExpectContainer, onDeleteRun }: Props = $props();

  const DISK_WARN = $derived(rs.diskWarn), DISK_BAD = $derived(rs.diskBad), PATCH_OLD_DAYS = $derived(rs.staleDays);
  const expected = $derived(new Set(rs.expectedUnits));
  // Failed units that count: the expected ones are listed apart. Older
  // runs carry only a count, which counts as is.
  const unexpectedUnits = (r: FactsHostResult) => (r.facts.failed_units ?? []).filter((u) => !expected.has(u));
  const failedCount = (r: FactsHostResult) => r.facts.failed_units ? unexpectedUnits(r).length : Math.max(r.facts.failed, 0);
  const DAY = 86400;

  // The run to compare with: the latest earlier one by default.
  // Comparing is a choice: only a run the user picks (last month's, the
  // last report's) says what "changes since" covers. 0 = no comparison,
  // and the section is left out.
  let compareAt = $state(0);
  const base = $derived(earlier.find((s) => s.at === compareAt) ?? null);

  const has = (s: FactsSnapshot | null, k: string) => !!s?.facts.includes(k);
  const byName = (a: FactsHostResult, b: FactsHostResult) => a.name.localeCompare(b.name, undefined, { numeric: true });
  const ok = $derived(snap.results.filter((r) => r.state === "ok").sort(byName));
  const bad = $derived(snap.results.filter((r) => r.state !== "ok").sort(byName));
  const byId = (s: FactsSnapshot | null) => new Map((s?.results ?? []).filter((r) => r.state === "ok").map((r) => [r.connection_id, r]));

  const fmtDate = (ms: number) => new Date(ms).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
  const fmtDateTime = (ms: number) => new Date(ms).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  const patchDays = (r: FactsHostResult) => {
    const t = r.facts.last_patch ?? 0;
    return t > 0 ? Math.floor((snap.at / 1000 - t) / DAY) : null;
  };
  const needsReboot = (r: FactsHostResult) => r.facts.reboot === "yes" || r.facts.kernel_pending === "yes";
  // Since when a reboot has been waiting: the newest kernel's install
  // time, else (a library update, no kernel) the last upgrade.
  const rebootSince = (r: FactsHostResult): number | null => {
    if (!needsReboot(r)) return null;
    if (r.facts.kernel_pending === "yes" && (r.facts.kernel_latest_at ?? 0) > 0) return r.facts.kernel_latest_at!;
    return (r.facts.last_patch ?? 0) > 0 ? r.facts.last_patch! : null;
  };
  const rebootDays = (r: FactsHostResult) => {
    const t = rebootSince(r);
    return t === null ? null : Math.max(0, Math.floor((snap.at / 1000 - t) / DAY));
  };
  const rebootFlagged = (r: FactsHostResult) => {
    if (!needsReboot(r)) return false;
    const d = rebootDays(r);
    return d === null || d >= rs.rebootGraceDays;
  };
  const fsUse = (r: FactsHostResult) => [
    ...(r.facts.disks ?? []).filter((d) => d.used_pct !== undefined).map((d) => ({ mount: d.mount, pct: d.used_pct!, kind: d.network ? "space (network share)" : "space" })),
    ...(r.facts.inodes ?? []).map((d) => ({ mount: d.mount, pct: d.pct, kind: "inodes" })),
  ];

  // ---- per-host status ----
  function issues(r: FactsHostResult): { level: "red" | "yellow"; text: string }[] {
    const out: { level: "red" | "yellow"; text: string }[] = [];
    const f = r.facts;
    const ct = containerIssues(f, rs, snap.at);
    if (ct.problems.length) out.push({ level: "red", text: ct.problems.map((c) => `${c.name} ${containerProblem(c)}`).join(", ") });
    if (ct.restarted.length) out.push({ level: "yellow", text: `${ct.restarted.map((c) => c.name).join(", ")} restarted by the engine` });
    const nf = failedCount(r);
    if (has(snap, "failed") && nf > 0) out.push({ level: "red", text: `${nf} failed unit${nf === 1 ? "" : "s"}` });
    for (const u of fsUse(r)) {
      if (u.pct >= DISK_BAD) out.push({ level: "red", text: `${u.mount} ${u.pct}%` });
      else if (u.pct >= DISK_WARN) out.push({ level: "yellow", text: `${u.mount} ${u.pct}%` });
    }
    const pd = patchDays(r);
    if (has(snap, "updates") && f.security > 0 && (pd === null || pd >= rs.securityGraceDays)) {
      out.push({ level: "yellow", text: `${f.security} security updates${pd !== null ? `, last update ${pd} days ago` : ""}` });
    }
    if (rebootFlagged(r)) {
      const d = rebootDays(r);
      out.push({ level: "yellow", text: d === null ? "reboot pending" : `reboot pending for ${d} days` });
    }
    if (pd !== null && pd >= PATCH_OLD_DAYS) out.push({ level: "yellow", text: `not updated for ${pd} days` });
    return out;
  }
  const hostStatus = $derived(ok.map((r) => {
    const is = issues(r);
    return { r, is, level: is.some((i) => i.level === "red") ? "red" : is.length ? "yellow" : "green" };
  }).sort((a, b) => a.r.name.localeCompare(b.r.name, undefined, { numeric: true })));
  const count = (l: string) => hostStatus.filter((h) => h.level === l).length;

  // ---- findings ----
  const failedRows = $derived(ok.filter((r) => failedCount(r) > 0));
  const ctOn = $derived(has(snap, "containers"));
  const ctRows = $derived(ok.flatMap((r) => containerIssues(r.facts, rs, snap.at).problems.map((c) => ({ r, c }))));
  const ctRestartRows = $derived(ok.flatMap((r) => containerIssues(r.facts, rs, snap.at).restarted.map((c) => ({ r, c }))));
  const ctNoAccess = $derived(ok.filter((r) => r.facts.container_access === "noaccess"));
  const ctCleanExit = $derived.by(() => {
    const exp = new Set(rs.expectedContainers ?? []);
    return ok.flatMap((r) => (r.facts.containers ?? []).filter((c) => c.state === "exited" && c.exit_code === 0 && !exp.has(c.name)).map((c) => `${c.name} on ${r.name}`));
  });
  const ctExpectedSeen = $derived.by(() => {
    const exp = new Set(rs.expectedContainers ?? []);
    const m = new Map<string, number>();
    for (const r of ok) for (const c of r.facts.containers ?? []) if (exp.has(c.name) && c.state !== "running") m.set(c.name, (m.get(c.name) ?? 0) + 1);
    return [...m.entries()];
  });
  const ctTotals = $derived.by(() => {
    let run = 0, all = 0;
    for (const r of ok) for (const c of r.facts.containers ?? []) { all++; if (c.state === "running") run++; }
    return { run, all, hosts: ok.filter((r) => (r.facts.containers ?? []).length).length };
  });
  // Expected units that did fail, with the hosts: listed, not counted.
  const expectedSeen = $derived.by(() => {
    const m = new Map<string, string[]>();
    for (const r of ok) for (const u of r.facts.failed_units ?? []) if (expected.has(u)) m.set(u, [...(m.get(u) ?? []), r.name]);
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  });
  const fullRows = $derived(ok.flatMap((r) => fsUse(r).filter((u) => u.pct >= DISK_WARN).map((u) => ({ r, ...u, trend: trendOf(r.connection_id, u.mount, u.kind) })))
    .sort((a, b) => b.pct - a.pct));
  const rebootRows = $derived(ok.filter(needsReboot).sort((a, b) => (rebootDays(b) ?? 1e9) - (rebootDays(a) ?? 1e9) || byName(a, b)));
  const rebootFlaggedRows = $derived(rebootRows.filter(rebootFlagged));
  const rebootWithin = $derived(rebootRows.filter((r) => !rebootFlagged(r)));
  const rebootFlaggedCount = $derived(rebootFlaggedRows.length);
  // Without the Newer kernel fact the wait counts from the last upgrade,
  // which can only understate it.
  const kernelKnown = $derived(has(snap, "kernelpending"));
  const secRows = $derived(ok.filter((r) => r.facts.security > 0).sort((a, b) => b.facts.security - a.facts.security));
  // Past the grace period: these colour the host (and the tile).
  const secIsFlagged = (r: FactsHostResult) => { const pd = patchDays(r); return pd === null || pd >= rs.securityGraceDays; };
  const secFlaggedRows = $derived(secRows.filter(secIsFlagged));
  const secWithin = $derived(secRows.filter((r) => !secIsFlagged(r)));
  const secFlagged = $derived(secFlaggedRows.length);
  // Hosts within a grace period, folded into one line: "a (15), b (14)".
  const names = (rows: FactsHostResult[], extra?: (r: FactsHostResult) => string) =>
    rows.map((r) => (extra ? `${r.name} (${extra(r)})` : r.name)).join(", ");
  const staleRows = $derived(ok.filter((r) => (patchDays(r) ?? 0) >= PATCH_OLD_DAYS));

  // ---- trends ----
  // Use % of one filesystem across every stored run that measured it.
  function seriesOf(id: string, mount: string, kind: string): { t: number; pct: number }[] {
    const out: { t: number; pct: number }[] = [];
    for (const s of [...earlier, snap]) {
      const r = s.results.find((x) => x.connection_id === id && x.state === "ok");
      if (!r) continue;
      const u = fsUse(r).find((x) => x.mount === mount && x.kind === kind);
      if (u) out.push({ t: s.at / 1000, pct: u.pct });
    }
    return out;
  }
  // Least-squares growth in points per 30 days and the day it would reach
  // 100% at that pace; null under two runs a day apart.
  function trendOf(id: string, mount: string, kind: string) {
    const pts = seriesOf(id, mount, kind);
    if (pts.length < 2 || pts[pts.length - 1].t - pts[0].t < DAY) return null;
    const n = pts.length;
    const mx = pts.reduce((a, p) => a + p.t, 0) / n, my = pts.reduce((a, p) => a + p.pct, 0) / n;
    const den = pts.reduce((a, p) => a + (p.t - mx) ** 2, 0);
    const slope = den ? pts.reduce((a, p) => a + (p.t - mx) * (p.pct - my), 0) / den : 0; // pct per second
    const last = pts[n - 1];
    const fullAt = slope > 0 ? last.t + (100 - last.pct) / slope : null;
    return { pts, per30: slope * 30 * DAY, fullAt };
  }
  const trendRows = $derived(ok.flatMap((r) => fsUse(r).filter((u) => u.kind !== "inodes").map((u) => ({ r, ...u, trend: trendOf(r.connection_id, u.mount, u.kind) })))
    .filter((x) => x.trend && (x.pct >= 60 || x.trend.per30 >= 2))
    .sort((a, b) => (b.trend!.per30 - a.trend!.per30) || b.pct - a.pct));

  function spark(pts: { t: number; pct: number }[]): string {
    const w = 120, h = 24;
    const t0 = pts[0].t, t1 = pts[pts.length - 1].t || t0 + 1;
    const xy = pts.map((p) => `${(((p.t - t0) / (t1 - t0 || 1)) * (w - 2) + 1).toFixed(1)},${(h - 1 - (p.pct / 100) * (h - 2)).toFixed(1)}`);
    const warn = (h - 1 - (DISK_WARN / 100) * (h - 2)).toFixed(1);
    return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}"><line x1="0" x2="${w}" y1="${warn}" y2="${warn}" stroke="#d4a72c" stroke-dasharray="2 2" stroke-width="0.8"/><polyline fill="none" stroke="#0969da" stroke-width="1.5" points="${xy.join(" ")}"/></svg>`;
  }

  // ---- changes since the compared run ----
  const changes = $derived.by(() => {
    if (!base) return null;
    const prev = byId(base), cur = byId(snap);
    const out: { host: string; what: string; level?: string }[] = [];
    const name = (r: FactsHostResult) => r.name || r.hostname;
    for (const [id, r] of cur) if (!prev.has(id)) out.push({ host: name(r), what: "new in this report (or did not answer last time)" });
    for (const [id, r] of prev) if (!cur.has(id)) out.push({ host: name(r), what: "missing from this report", level: "red" });
    for (const [id, r] of cur) {
      const p = prev.get(id);
      if (!p) continue;
      const a = p.facts, b = r.facts;
      if (has(base, "os") && has(snap, "os") && a.os && b.os && a.os !== b.os) out.push({ host: name(r), what: `OS ${a.os} -> ${b.os}` });
      if (a.kernel && b.kernel && a.kernel !== b.kernel) out.push({ host: name(r), what: `kernel ${a.kernel} -> ${b.kernel}` });
      if (has(base, "uptime") && has(snap, "uptime") && b.uptime_sec > 0 && b.uptime_sec < (snap.at - base.at) / 1000) out.push({ host: name(r), what: `rebooted (up ${Math.floor(b.uptime_sec / DAY)}d)` });
      if (a.failed_units && b.failed_units) {
        const pa = new Set(a.failed_units), pb = new Set(b.failed_units);
        for (const u of pb) if (!pa.has(u) && !expected.has(u)) out.push({ host: name(r), what: `${u} failed`, level: "red" });
        for (const u of pa) if (!pb.has(u) && !expected.has(u)) out.push({ host: name(r), what: `${u} no longer failed`, level: "green" });
      }
      if (has(base, "ports") && has(snap, "ports")) {
        const pa = new Set(a.ports ?? []), pb = new Set(b.ports ?? []);
        const opened = [...pb].filter((x) => !pa.has(x)), closed = [...pa].filter((x) => !pb.has(x));
        if (opened.length) out.push({ host: name(r), what: `listening on ${opened.join(", ")} (new)`, level: "yellow" });
        if (closed.length) out.push({ host: name(r), what: `no longer listening on ${closed.join(", ")}` });
      }
      for (const d of b.disks ?? []) {
        const o = (a.disks ?? []).find((x) => x.mount === d.mount);
        if (o?.used_pct !== undefined && d.used_pct !== undefined && Math.abs(d.used_pct - o.used_pct) >= 5) {
          out.push({ host: name(r), what: `${d.mount} ${o.used_pct}% -> ${d.used_pct}%`, level: d.used_pct > o.used_pct ? "yellow" : undefined });
        }
      }
      if (a.containers && b.containers) {
        const exp = new Set(rs.expectedContainers ?? []);
        const pa = new Map(a.containers.map((c) => [c.name, c])), pb = new Map(b.containers.map((c) => [c.name, c]));
        for (const [n, c] of pb) {
          if (exp.has(n)) continue;
          const o = pa.get(n);
          if (!o) out.push({ host: name(r), what: `container ${n} added (${c.image})` });
          else if (o.state !== c.state) out.push({ host: name(r), what: `container ${n} ${o.state} -> ${c.state}`, level: c.state === "running" ? "green" : "yellow" });
          else if (o.image !== c.image) out.push({ host: name(r), what: `container ${n} image ${o.image} -> ${c.image}` });
        }
        for (const n of pa.keys()) if (!pb.has(n) && !exp.has(n)) out.push({ host: name(r), what: `container ${n} removed` });
      }
      if (a.updates >= 0 && b.updates === 0 && a.updates > 0) out.push({ host: name(r), what: `all ${a.updates} pending updates installed`, level: "green" });
    }
    return out.sort((x, y) => x.host.localeCompare(y.host, undefined, { numeric: true }));
  });

  let root = $state<HTMLElement | null>(null);
  // The rendered report for the saved HTML file, without the in-app
  // controls (the compare picker).
  export function html(): string {
    if (!root) return "";
    const c = root.cloneNode(true) as HTMLElement;
    c.querySelectorAll(".rp-noprint").forEach((e) => e.remove());
    // "Generated" is the moment the file is written, not when the dialog
    // was opened.
    c.querySelectorAll(".rp-generated").forEach((e) => (e.textContent = fmtDateTime(Date.now())));
    return c.outerHTML;
  }
</script>

<div class="rp" bind:this={root}>
  {#if earlier.length}
    <p class="dim rp-noprint rp-ctl">Compare with
      <select value={compareAt} onchange={(e) => (compareAt = Number((e.target as HTMLSelectElement).value))}>
        <option value={0}>No comparison</option>
        {#each [...earlier].reverse() as s (s.at)}<option value={s.at}>{fmtDateTime(s.at)}</option>{/each}
      </select>
      {#if base && onDeleteRun}
        <button class="rp-link" title="Delete this stored run (a test run, a run on the wrong hosts); it also leaves the trends" onclick={() => { onDeleteRun(base.at); compareAt = 0; }}>delete this run</button>
      {/if}
      · {earlier.length + 1} runs stored for this folder (the last {fleet.catalog?.history_max ?? ""} are kept)
    </p>
  {/if}
  <div class="rp-head">
    <h1>Health report: {label}</h1>
    {#if logo}<img class="rp-logo" src={logo} alt="" />{/if}
  </div>
  <p class="rp-meta">
    Collected {fmtDateTime(snap.at)} · {snap.results.length} host{snap.results.length === 1 ? "" : "s"}
    {#if base} · compared with {fmtDateTime(base.at)}{/if}
  </p>
  <p class="rp-meta rp-sign">
    {author ? `Prepared by ${author} · ` : ""}Generated <span class="rp-generated">{fmtDateTime(Date.now())}</span>
  </p>

  <h2>Summary</h2>
  {#if snap.results.length}
    {@const total = snap.results.length}
    <div class="rp-bar" title="{count('green')} OK, {count('yellow')} attention, {count('red') + bad.length} action">
      {#if count("green")}<span class="g" style="flex:{count('green')}">{count("green")} OK</span>{/if}
      {#if count("yellow")}<span class="y" style="flex:{count('yellow')}">{count("yellow")} attention</span>{/if}
      {#if count("red") + bad.length}<span class="r" style="flex:{count('red') + bad.length}">{count("red") + bad.length} action</span>{/if}
    </div>
    <p class="rp-lead">
      {#if count("red") + bad.length}{count("red") + bad.length} of {total} {count("red") + bad.length === 1 ? "host needs" : "hosts need"} action{#if count("yellow")}, {count("yellow")} {count("yellow") === 1 ? "needs" : "need"} attention{/if}.
      {:else if count("yellow")}No host needs action; {count("yellow")} of {total} {count("yellow") === 1 ? "needs" : "need"} attention.
      {:else}All {total} hosts are OK.{/if}
    </p>
  {/if}
  <div class="rp-tiles">
    {#if has(snap, "failed")}<div class="rp-tile" class:red={failedRows.length > 0}><b>{failedRows.length}</b><span>with failed services</span></div>{/if}
    {#if has(snap, "disks") || has(snap, "inodes")}<div class="rp-tile" class:yellow={fullRows.length > 0} class:red={fullRows.some((x) => x.pct >= DISK_BAD)}><b>{fullRows.length}</b><span>filesystem{fullRows.length === 1 ? "" : "s"} over {DISK_WARN}%</span></div>{/if}
    {#if has(snap, "reboot") || has(snap, "kernelpending")}<div class="rp-tile" class:yellow={rebootFlaggedCount > 0}><b>{rebootFlaggedCount}</b><span>reboot overdue{rebootWithin.length ? ` · +${rebootWithin.length} within ${rs.rebootGraceDays} days` : ""}</span></div>{/if}
    {#if has(snap, "updates")}<div class="rp-tile" class:yellow={secFlagged > 0}><b>{secFlagged}</b><span>security updates overdue{secWithin.length ? ` · +${secWithin.length} since last patch day` : ""}</span></div>{/if}
    {#if ctOn}<div class="rp-tile" class:red={ctRows.length > 0} class:yellow={!ctRows.length && ctRestartRows.length > 0}><b>{ctRows.length}</b><span>container problem{ctRows.length === 1 ? "" : "s"} · {ctTotals.run} of {ctTotals.all} running</span></div>{/if}
    {#if has(snap, "lastpatch")}<div class="rp-tile" class:yellow={staleRows.length > 0}><b>{staleRows.length}</b><span>not updated in {PATCH_OLD_DAYS}+ days</span></div>{/if}
    {#if bad.length}<div class="rp-tile red"><b>{bad.length}</b><span>did not answer</span></div>{/if}
  </div>
  <div class="rp-hosts">
    {#each hostStatus as h (h.r.connection_id)}
      <span class="rp-host {h.level}" title={h.is.map((i) => i.text).join(", ") || "OK"}>{h.r.name}</span>
    {/each}
    {#each bad as r (r.connection_id)}<span class="rp-host red" title={r.error}>{r.name}</span>{/each}
  </div>

  <h2>Findings</h2>
  {#if !failedRows.length && !fullRows.length && !rebootFlaggedRows.length && !secFlaggedRows.length && !staleRows.length && !bad.length && !ctRows.length && !ctRestartRows.length}
    <p class="rp-none">Nothing needs action or attention.</p>
  {/if}
  {#if bad.length}
    <section class="rp-sec red">
      <h3>Did not answer</h3>
      <table><thead><tr><th>Host</th><th>Reason</th></tr></thead><tbody>
        {#each bad as r (r.connection_id)}<tr><td>{r.name}</td><td class="red">{r.error || r.state}</td></tr>{/each}
      </tbody></table>
    </section>
  {/if}
  {#if failedRows.length}
    <section class="rp-sec red">
      <h3>Failed services</h3>
      <table><thead><tr><th>Host</th><th>Units</th></tr></thead><tbody>
        {#each failedRows as r (r.connection_id)}
          <tr><td>{r.name}</td><td class="red">
            {#if r.facts.failed_units}
              {#each unexpectedUnits(r) as u, i (u)}{i ? ", " : ""}{u}{#if onExpectUnit} <button class="rp-noprint rp-link" title="Count {u} as expected on every host of this report: listed apart, does not colour a host" onclick={() => onExpectUnit(u)}>expected</button>{/if}{/each}
            {:else}{r.facts.failed} unit(s){/if}
          </td></tr>
        {/each}
      </tbody></table>
    </section>
  {/if}
  {#if ctRows.length}
    <section class="rp-sec red">
      <h3>Containers</h3>
      <table><thead><tr><th>Host</th><th>Container</th><th>Problem</th><th>Since</th><th>Project</th></tr></thead><tbody>
        {#each ctRows as x, i (i)}
          <tr><td>{x.r.name}</td>
            <td>{x.c.name}{#if onExpectContainer} <button class="rp-noprint rp-link" title="Count {x.c.name} as stopped on purpose on every host of this report: listed apart, does not colour a host" onclick={() => onExpectContainer(x.c.name)}>expected</button>{/if}</td>
            <td class="red">{containerProblem(x.c)}</td>
            <td>{x.c.started_at ? fmtDate(x.c.started_at * 1000) : "-"}</td>
            <td class="dim">{x.c.project || "-"}</td></tr>
        {/each}
      </tbody></table>
    </section>
  {/if}
  {#if ctRestartRows.length}
    <section class="rp-sec yellow">
      <h3>Containers restarted by the engine</h3>
      <p class="dim">Running again, but restarted by docker / podman (crash, out of memory) within the last {rs.containerRestartDays} days. A redeploy does not count.</p>
      <table><thead><tr><th>Host</th><th>Container</th><th class="num">Restarts</th><th>Running since</th><th>Project</th></tr></thead><tbody>
        {#each ctRestartRows as x, i (i)}
          <tr><td>{x.r.name}</td><td>{x.c.name}</td><td class="num yellow">{x.c.restarts}</td><td>{fmtDateTime(x.c.started_at * 1000)}</td><td class="dim">{x.c.project || "-"}</td></tr>
        {/each}
      </tbody></table>
    </section>
  {/if}
  {#if fullRows.length}
    <section class="rp-sec {fullRows.some((x) => x.pct >= DISK_BAD) ? 'red' : 'yellow'}">
      <h3>Filesystems over {DISK_WARN}%</h3>
      <table><thead><tr><th>Host</th><th>Filesystem</th><th class="num">Use</th><th>Trend</th><th>Full at this pace</th></tr></thead><tbody>
        {#each fullRows as x, i (i)}
          <tr>
            <td>{x.r.name}</td><td>{x.mount} <span class="dim">{x.kind}</span></td>
            <td class="num {x.pct >= DISK_BAD ? 'red' : 'yellow'}">{x.pct}%</td>
            <td>{#if x.trend}{x.trend.per30 >= 0 ? "+" : ""}{x.trend.per30.toFixed(1)} pts / 30 days{:else}<span class="dim">needs runs a day or more apart</span>{/if}</td>
            <td>{#if x.trend?.fullAt}{fmtDate(x.trend.fullAt * 1000)}{:else}<span class="dim">-</span>{/if}</td>
          </tr>
        {/each}
      </tbody></table>
    </section>
  {/if}
  {#if rebootFlaggedRows.length}
    <section class="rp-sec yellow">
      <h3>Reboot overdue</h3>
      <p class="dim">Waiting {rs.rebootGraceDays} days or more{kernelKnown ? ", counted from when the new kernel was installed" : ""}.</p>
      <table><thead><tr><th>Host</th>{#if kernelKnown}<th>Running kernel</th><th>Installed</th>{/if}<th>Waiting since</th><th class="num">Uptime</th></tr></thead><tbody>
        {#each rebootFlaggedRows as r (r.connection_id)}
          {@const since = rebootSince(r)}
          <tr><td>{r.name}</td>
            {#if kernelKnown}<td>{r.facts.kernel || "-"}</td><td>{r.facts.kernel_pending === "yes" ? r.facts.kernel_latest : "-"}</td>{/if}
            <td class="yellow">{since ? `${kernelKnown ? "" : "at least "}${fmtDate(since * 1000)} (${rebootDays(r)}d)` : "unknown"}</td>
            <td class="num">{r.facts.uptime_sec ? `${Math.floor(r.facts.uptime_sec / DAY)}d` : "-"}</td></tr>
        {/each}
      </tbody></table>
    </section>
  {/if}
  {#if secFlaggedRows.length}
    <section class="rp-sec yellow">
      <h3>Security updates overdue</h3>
      <p class="dim">Pending while the last update is {rs.securityGraceDays} or more days old.</p>
      <table><thead><tr><th>Host</th><th class="num">Security</th><th class="num">All updates</th><th>Last update</th></tr></thead><tbody>
        {#each secFlaggedRows as r (r.connection_id)}
          <tr><td>{r.name}</td><td class="num yellow">{r.facts.security}</td><td class="num">{r.facts.updates}</td><td>{(r.facts.last_patch ?? 0) > 0 ? fmtDate(r.facts.last_patch! * 1000) : "-"}</td></tr>
        {/each}
      </tbody></table>
    </section>
  {/if}
  {#if staleRows.length}
    <section class="rp-sec yellow">
      <h3>Not updated in {PATCH_OLD_DAYS}+ days</h3>
      <table><thead><tr><th>Host</th><th>Last update</th><th class="num">Days</th></tr></thead><tbody>
        {#each staleRows as r (r.connection_id)}
          <tr><td>{r.name}</td><td>{fmtDate(r.facts.last_patch! * 1000)}</td><td class="num yellow">{patchDays(r)}</td></tr>
        {/each}
      </tbody></table>
    </section>
  {/if}

  {#if rebootWithin.length || secWithin.length || expectedSeen.length || (rebootRows.length && !kernelKnown) || ctNoAccess.length || ctCleanExit.length || ctExpectedSeen.length || (ctOn && ctTotals.all)}
    <h3 class="rp-info-h">For information</h3>
    <ul class="rp-info">
      {#if rebootWithin.length}
        <li>Reboot pending, within the {rs.rebootGraceDays}-day window: {names(rebootWithin, (r) => `${rebootDays(r) ?? "?"}d`)}.</li>
      {/if}
      {#if secWithin.length}
        <li>Security updates released since the last patch day: {names(secWithin, (r) => String(r.facts.security))}.</li>
      {/if}
      {#if expectedSeen.length}
        <li>Known and expected, not counted: {expectedSeen.map(([u, hosts]) => `${u} on ${hosts.length} host${hosts.length === 1 ? "" : "s"}`).join("; ")}.</li>
      {/if}
      {#if ctOn && ctTotals.all}
        <li>Containers: {ctTotals.run} of {ctTotals.all} running on {ctTotals.hosts} host{ctTotals.hosts === 1 ? "" : "s"}.</li>
      {/if}
      {#if ctCleanExit.length}
        <li>Stopped cleanly (exit code 0): {ctCleanExit.join(", ")}.</li>
      {/if}
      {#if ctExpectedSeen.length}
        <li>Stopped on purpose, not counted: {ctExpectedSeen.map(([n, k]) => `${n}${k > 1 ? ` on ${k} hosts` : ""}`).join(", ")}.</li>
      {/if}
      {#if ctNoAccess.length}
        <li>Containers could not be read on {names(ctNoAccess)}: the login user has no access to docker (not in the docker group).</li>
      {/if}
      {#if rebootRows.length && !kernelKnown}
        <li>Newer kernel was not collected in this run; reboot waiting times count from the last upgrade and may be longer.</li>
      {/if}
    </ul>
  {/if}

  {#if base}
    <h2>Changes since {fmtDateTime(base.at)}</h2>
    {#if changes && changes.length}
      <table><thead><tr><th>Host</th><th>Change</th></tr></thead><tbody>
        {#each changes as c, i (i)}<tr><td>{c.host}</td><td class={c.level ?? ""}>{c.what}</td></tr>{/each}
      </tbody></table>
    {:else}
      <p class="dim">No changes.</p>
    {/if}
  {/if}

  {#if trendRows.length}
    <h2>Disk trends</h2>
    <p class="dim">Filesystems at 60% or more, or growing 2 points or more per 30 days, over the stored runs. Dashed line: {DISK_WARN}%.</p>
    <table><thead><tr><th>Host</th><th>Filesystem</th><th></th><th class="num">Now</th><th class="num">Per 30 days</th><th>Full at this pace</th></tr></thead><tbody>
      {#each trendRows as x, i (i)}
        <tr>
          <td>{x.r.name}</td><td>{x.mount}</td><td>{@html spark(x.trend!.pts)}</td>
          <td class="num" class:red={x.pct >= DISK_BAD} class:yellow={x.pct >= DISK_WARN && x.pct < DISK_BAD}>{x.pct}%</td>
          <td class="num">{x.trend!.per30 >= 0 ? "+" : ""}{x.trend!.per30.toFixed(1)}</td>
          <td>{x.trend!.fullAt ? fmtDate(x.trend!.fullAt * 1000) : "-"}</td>
        </tr>
      {/each}
    </tbody></table>
  {/if}

  <p class="rp-meta rp-sign rp-end">
    {author ? `Prepared by ${author} · ` : ""}Generated <span class="rp-generated">{fmtDateTime(Date.now())}</span>
  </p>

  <div class="rp-appendix-wrap">
  <h2>Appendix: all hosts</h2>
  <div class="rp-appendix">
    <table><thead><tr>{#each appendix.headers as h, i (i)}<th>{h}</th>{/each}</tr></thead><tbody>
      {#each appendix.rows as row, i (i)}<tr>{#each row as c, j (j)}<td>{c}</td>{/each}</tr>{/each}
    </tbody></table>
  </div>
  </div>
</div>

<svelte:head>{@html `<style>${REPORT_CSS}</style>`}</svelte:head>
