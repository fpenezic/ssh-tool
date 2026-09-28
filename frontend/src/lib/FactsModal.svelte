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
      { key: "disks", label: "Disks (size per filesystem)" },
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
      { key: "reboot", label: "Reboot required" },
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
    patch: ["os", "kernel", "uptime", "updates", "reboot", "failed"],
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
  function diskLayout(r: FactsHostResult): string {
    return (r.facts.disks ?? []).map((d) => `${d.mount} ${gib(d.size_kb)}`).join(", ") || "-";
  }
  // Same mount points, each within 5% of the other's size: two "80 GB"
  // disks never report the same byte count, and rounding to whole GiB
  // would still split 79.4 from 80.2.
  function sameDisks(a: FactsHostResult, b: FactsHostResult): boolean {
    const da = a.facts.disks ?? [], db = b.facts.disks ?? [];
    if (da.length !== db.length) return false;
    const bm = new Map(db.map((d) => [d.mount, d.size_kb]));
    return da.every((d) => {
      const o = bm.get(d.mount);
      return o !== undefined && Math.abs(o - d.size_kb) <= 0.05 * Math.max(o, d.size_kb);
    });
  }
  function diskTotalKB(r: FactsHostResult): number {
    return (r.facts.disks ?? []).reduce((n, d) => n + d.size_kb, 0);
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
  const groups = $derived.by(() => {
    const key = (r: FactsHostResult) =>
      groupBy === "size" ? sizeKey(r) : groupBy === "os" ? (r.facts.os || "?") : groupBy === "kernel" ? (r.facts.kernel || "?") : "";
    const m = new Map<string, FactsHostResult[]>();
    for (const r of [...shown].sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }))) {
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

  // Every mount point seen in this report, "/" first. Up to MAX_MOUNT_COLS
  // of them each get a column (a host without that mount leaves it empty),
  // so Excel can sort and filter per mount; past that the table would get
  // too wide and they fold back into one list cell.
  const MAX_MOUNT_COLS = 6;
  const allMounts = $derived.by(() => {
    const set = new Set<string>();
    for (const r of okRows) for (const d of r.facts.disks ?? []) set.add(d.mount);
    return [...set].sort((a, b) => (a === "/" ? -1 : b === "/" ? 1 : a.localeCompare(b)));
  });

  // One place that decides the columns, used by the table, CSV and Markdown.
  const columns = $derived.by(() => {
    // n: the plain number for exports (CSV, Copy table), so a spreadsheet
    // sorts and sums it; the header then names the unit instead.
    const c: { h: string; v: (r: FactsHostResult) => string; n?: (r: FactsHostResult) => string; unit?: string }[] = [
      { h: "Host", v: (r) => r.name },
      { h: "Address", v: (r) => r.hostname },
    ];
    if (has("cpu")) {
      c.push({ h: "Cores", v: (r) => String(r.facts.cpu_cores || "-") });
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
          c.push({ h: m, v: (r) => { const k = kbOf(r); return k ? gib(k) : ""; }, n: (r) => { const k = kbOf(r); return k ? gibNum(k) : ""; } });
        }
      } else {
        c.push({ h: "Disks", v: diskLayout });
      }
      c.push({ h: "Disk total", v: (r) => gib(diskTotalKB(r)), n: (r) => gibNum(diskTotalKB(r)) });
    }
    if (has("virt")) c.push({ h: "Virt", v: (r) => r.facts.virt === "none" ? "bare metal" : r.facts.virt || "-" });
    if (has("os")) c.push({ h: "OS", v: (r) => r.facts.os || "-" });
    if (has("kernel")) c.push({ h: "Kernel", v: (r) => r.facts.kernel || "-" });
    if (has("uptime")) c.push({ h: "Uptime", v: (r) => uptime(r.facts.uptime_sec) });
    if (has("timesync")) c.push({ h: "NTP", v: (r) => r.facts.timesync || "-" });
    if (has("updates")) c.push({ h: "Updates", v: (r) => r.facts.updates < 0 ? "-" : `${r.facts.updates}${r.facts.security > 0 ? ` (${r.facts.security} sec)` : ""}` });
    if (has("reboot")) c.push({ h: "Reboot", v: (r) => r.facts.reboot || "-" });
    if (has("failed")) c.push({ h: "Failed", v: (r) => r.facts.failed < 0 ? "-" : String(r.facts.failed) });
    if (has("ips")) c.push({ h: "IPs", v: (r) => (r.facts.ips ?? []).join(" ") || "-" });
    if (has("gateway")) c.push({ h: "Gateway", v: (r) => r.facts.gateway || "-" });
    if (has("dns")) c.push({ h: "DNS", v: (r) => (r.facts.dns ?? []).join(" ") || "-" });
    if (has("ports")) c.push({ h: "Ports", v: (r) => (r.facts.ports ?? []).join(" ") || "-" });
    if (has("custom")) c.push({ h: "Custom", v: (r) => r.facts.custom || "-" });
    return c;
  });
  function cellClass(h: string, r: FactsHostResult): string {
    if (h === "Reboot" && r.facts.reboot === "yes") return "fwarn";
    if (h === "Updates" && r.facts.security > 0) return "ferr";
    if (h === "Failed" && r.facts.failed > 0) return "ferr";
    return "";
  }

  function csv(): string {
    const esc = (v: string) => /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
    const lines = [columns.map((c) => esc(exHead(c))).join(",")];
    for (const r of shown) lines.push(columns.map((c) => esc(exCell(c, r))).join(","));
    return lines.join("\n") + "\n";
  }
  function markdown(): string {
    const cell = (v: string) => v.replace(/\|/g, "\\|");
    const lines = [
      `| ${columns.map((c) => c.h).join(" | ")} |`,
      `| ${columns.map(() => "---").join(" | ")} |`,
      ...shown.map((r) => `| ${columns.map((c) => cell(c.v(r))).join(" | ")} |`),
    ];
    return lines.join("\n") + "\n";
  }
  // The same rows as a real table: Teams and Outlook paste the HTML as a
  // table, Excel splits it into cells. The tab-separated text is the plain
  // fallback, which Excel also splits whatever the locale's list separator.
  function tsv(): string {
    const cell = (v: string) => v.replace(/[\t\r\n]+/g, " ");
    return [columns.map(exHead), ...shown.map((r) => columns.map((c) => exCell(c, r)))]
      .map((row) => row.map(cell).join("\t")).join("\n") + "\n";
  }
  function html(): string {
    const esc = (v: string) => v.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    const th = columns.map((c) => `<th style="text-align:left;border:1px solid #999;padding:2px 6px">${esc(exHead(c))}</th>`).join("");
    const rows = shown.map((r) => `<tr>${columns.map((c) => `<td style="border:1px solid #999;padding:2px 6px">${esc(exCell(c, r))}</td>`).join("")}</tr>`).join("");
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
      <p class="fdim note">Update counts come from each host's cached package lists (apt -s, dnf -C), so they are as fresh as the host's last apt update / dnf makecache.</p>
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
        <span class="chip">Similar to {similarTo.name} <button class="linkish" onclick={() => (similarTo = null)} aria-label="Clear similar filter">×</button></span>
      {/if}
      <span class="spacer"></span>
      {#if has("reboot") && needReboot}<span class="chip warn">{needReboot} need reboot</span>{/if}
      {#if has("updates") && withSecurity}<span class="chip bad">{withSecurity} with security updates</span>{/if}
      {#if has("failed") && withFailed}<span class="chip bad">{withFailed} with failed units</span>{/if}
    </div>
    {#if has("cpu") || has("mem")}
      <div class="sizes"><span class="fdim">Sizes:</span>
        {#each sizeChips as [k, n] (k)}<span class="chip">{k} <span class="fdim">×{n}</span></span>{/each}
      </div>
    {/if}
    <div class="table-wrap">
      <table class="ftable">
        <thead><tr>{#each columns as c, i (i)}<th class:fmono={c.h.startsWith("/")}>{c.h}</th>{/each}<th></th></tr></thead>
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
                <td><button class="linkish" onclick={() => (similarTo = similarTo?.connection_id === r.connection_id ? null : r)}>Find similar</button></td>
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
  .table-wrap { overflow: auto; }
  .linkish { background: none; border: 0; padding: 0; color: var(--blue); font: inherit; cursor: pointer; }
  .linkish:hover { text-decoration: underline; }
</style>
