<script lang="ts">
  // Live tcpdump capture against the active session's target host.
  //
  // Auth path is chosen by `TcpdumpProbe` (root? sudo cache? prompt?).
  // The frontend collects a password only when needed and pipes it
  // through `TcpdumpProvidePassword` to the backend, which feeds it
  // into sudo's stdin once. Cap is hard-stopped at 5000 packets on
  // the backend regardless of the input.
  //
  // Output is brief by design (no payload bytes - tcpdump -q). If you
  // want payload, run tcpdump in a normal shell - this modal is the
  // "what's happening on this interface right now" view, not a forensic
  // capture tool.

  import { onMount, onDestroy } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api, type TcpdumpProbeResult, type Insight, type RouteResult } from "./api";
  import { EventsOn } from "./wailsRuntime";
  import { clickOutside } from "./clickOutside";

  interface TcpdumpStats {
    iface: string;
    packets: number;
    insights: number;
    running: boolean;
  }
  interface Props {
    sessionId: string;
    onClose: () => void;
    // When hidden, the overlay is display:none but the component stays
    // mounted so the capture, event subscriptions and Insights keep
    // running in the background. Restored by flipping hidden back off.
    hidden?: boolean;
    // Minimise sends the capture to the background (toolbar chip) without
    // stopping it. When absent the modal shows no minimise control.
    onMinimize?: () => void;
    // Periodic stats push so the parent's toolbar chip can show live
    // counts while minimised.
    onStats?: (s: TcpdumpStats) => void;
  }
  let { sessionId, onClose, hidden = false, onMinimize, onStats }: Props = $props();

  let probe = $state<TcpdumpProbeResult | null>(null);
  let interfaces = $state<string[]>([]);
  let probeErr = $state<string | null>(null);

  let iface = $state<string>("");
  let bpf = $state<string>("");
  let maxCount = $state<number>(500);
  // Continuous = no packet cap; the capture runs until stopped. Sends
  // max_count -1 (the backend sentinel for "drop -c"). Required for a
  // long-lived capture that survives a tab detach.
  let continuous = $state(false);
  // When the connection has a stored password we can offer to feed it
  // into sudo automatically - defaults on, but the user can flip off
  // if the sudo password is different from the login password.
  let useSavedPassword = $state(true);
  // Verbose toggle = tcpdump -v + per-protocol decode (DHCP/DNS/ARP).
  // Cheap perf-wise; default off because most users want the brief
  // header-only stream.
  let verbose = $state(false);
  // Insights toggle = live network-health analyzer. Flags routing /
  // wrong-interface anomalies (UDP reply from wrong source IP, half-open
  // TCP, ICMP unreachable/redirect/TTL-exceeded, ARP off-subnet, RST
  // storms) off the parsed stream. TCP flag-based checks (half-open,
  // RST) need verbose to see the flags; UDP/ICMP/ARP work in brief mode
  // too. Default on - it's the reason you reach for a quick capture.
  let insights = $state(true);

  // Custom port → protocol map for the decoder. Use when the
  // expected traffic runs on a non-standard port (HTTP on 9000,
  // MQTT bridge on 1885, etc). Keys are ports as strings; values
  // are decoder names. Only consulted when verbose is on.
  const KNOWN_PROTOS = [
    "http", "tls", "dns", "dhcp", "ntp", "snmp",
    "ldap", "smb", "mqtt", "ssh", "cwmp",
  ];
  let portOverrides = $state<Array<{ port: string; proto: string }>>([]);
  let newOverridePort = $state("");
  let newOverrideProto = $state("http");

  function addPortOverride() {
    // newOverridePort is typed string but bound to <input type="number">, so
    // Svelte hands back a number once the user types - .trim() threw and the
    // add button did nothing. Coerce before parsing.
    const portNum = parseInt(String(newOverridePort ?? "").trim(), 10);
    if (!Number.isFinite(portNum) || portNum <= 0 || portNum > 65535) return;
    const portStr = String(portNum);
    if (portOverrides.some((o) => o.port === portStr)) return;
    portOverrides = [...portOverrides, { port: portStr, proto: newOverrideProto }];
    newOverridePort = "";
  }
  function removePortOverride(port: string) {
    portOverrides = portOverrides.filter((o) => o.port !== port);
  }
  function portOverridesAsMap(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const o of portOverrides) out[o.port] = o.proto;
    return out;
  }

  type Decoded = {
    type: string;
    summary: string;
    fields: Record<string, string>;
  };
  type Packet = {
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
    decoded?: Decoded;
    seq?: number;
  };

  let running = $state(false);
  let dumpId = $state<string | null>(null);
  let packets = $state<Packet[]>([]);
  // Cumulative packet count for the whole capture, reported by the
  // backend. `packets` only holds the rendered tail, so this is the true
  // number shown in the count + the toolbar chip.
  let totalPackets = $state(0);
  // Rate guard: if a flood arrives faster than you can read, nudge the
  // user to add a BPF filter. Tracks packets in a short rolling window.
  let floodNudge = $state(false);
  let rateWindowStart = 0;
  let rateWindowCount = 0;
  // Live network-health findings, newest last. De-duped backend-side so
  // each distinct (kind, flow) lands once.
  let insightList = $state<Insight[]>([]);
  // Route-check results keyed by flow_key (the active "Check route"
  // confirmation behind an insight). null = in flight.
  let routeChecks = $state<Record<string, RouteResult[] | null>>({});
  const RATE_LIMIT = 2000;     // packets within the window before nudging
  const RATE_WINDOW_MS = 4000; // rolling window for the rate guard
  // View mode: flat list (per-packet) or grouped by flow (4-tuple).
  let viewMode = $state<"flat" | "flows" | "decode" | "insights">("flat");
  let textFilter = $state<string>("");
  let errorMsg = $state<string | null>(null);
  let needsPassword = $state(false);
  let passwordInput = $state<string>("");
  let passwordError = $state<string | null>(null);
  let pwInputEl: HTMLInputElement | undefined = $state();

  $effect(() => {
    if (needsPassword) setTimeout(() => pwInputEl?.focus(), 0);
  });

  // Push live stats up so the parent toolbar chip stays current while
  // minimised. Reads reactive deps (packets.length, insightList.length,
  // running, iface) so it re-fires as the capture progresses.
  $effect(() => {
    onStats?.({
      iface,
      packets: totalPackets,
      insights: insightList.length,
      running,
    });
  });

  let unsubLine: null | (() => void) = null;
  let unsubEvent: null | (() => void) = null;
  let unsubInsight: null | (() => void) = null;

  // Cap on rows kept in memory - 5000 is what tcpdump itself caps at,
  // but if we render every one xterm-style the DOM gets sluggish. Trim
  // from the front when we cross 2000. Verbose mode payloads can run
  // ~5x larger (hex dump + decoded fields) so the cap drops to keep
  // memory and Svelte's $derived chain from blowing up on a burst.
  const RENDER_CAP = $derived(verbose ? 800 : 2000);

  // Queue incoming packets and flush via rAF so a 1000 pkt/s burst
  // triggers ~60 reactive updates per second instead of 1000.
  // Without this, every event hand-rolls a fresh packets array
  // (slice + spread) and re-derives filteredPackets + flows +
  // dhcpTransactions, which is O(n) per packet x O(n) consumers.
  let packetQueue: Packet[] = [];
  let flushPending = false;
  let flushTimer: ReturnType<typeof setTimeout> | null = null;
  function flushPackets() {
    flushPending = false;
    if (flushTimer) { clearTimeout(flushTimer); flushTimer = null; }
    if (packetQueue.length === 0) return;
    const drained = packetQueue;
    packetQueue = [];
    let next = packets.concat(drained);
    if (next.length > RENDER_CAP) {
      next = next.slice(next.length - RENDER_CAP);
    }
    packets = next;
  }
  function enqueuePacket(pkt: Packet) {
    packetQueue.push(pkt);
    // Bound the pre-flush queue independently of the flush cadence. If a
    // flush is somehow delayed (paused rAF + slow timer under load), the
    // queue must not grow without limit. We only ever render the last
    // RENDER_CAP rows anyway, so anything older than that in the queue is
    // already destined to be sliced off - drop it now.
    if (packetQueue.length > RENDER_CAP) {
      packetQueue = packetQueue.slice(packetQueue.length - RENDER_CAP);
    }
    if (flushPending) return;
    flushPending = true;
    // Render at a FIXED, modest cadence - a single timer, NOT
    // requestAnimationFrame. rAF runs at up to 60fps when the main
    // thread is free, which meant up to 60 packets-array reassignments
    // per second, each re-deriving the list + flows and triggering a
    // scroll + repaint. That kept the WebView2 GPU compositor
    // re-rasterizing the scroll layer continuously and its backing
    // store grew unbounded (RSS climbing in msedgewebview2.exe even
    // though the JS heap stayed flat). The tell: with DevTools open
    // rAF is throttled and the growth stopped; closed, it resumed.
    // A 150ms timer caps repaints at ~6-7/s regardless. The backend
    // already batches at 100ms, so this adds no perceptible latency.
    flushTimer = setTimeout(flushPackets, 150);
  }

  onMount(async () => {
    try {
      probe = await api.tcpdumpProbe(sessionId);
    } catch (e: any) {
      probeErr = `Probe failed: ${e?.message ?? e}`;
      return;
    }
    try {
      interfaces = await api.tcpdumpListInterfaces(sessionId);
      // Default to "any" - it captures across every device, the safest
      // starting point when you don't yet know which interface the
      // traffic rides. Fall back to the first real NIC, then whatever's
      // first, if "any" isn't offered.
      const pick = interfaces.includes("any")
        ? "any"
        : (interfaces.find((i) => i !== "lo") ?? interfaces[0]);
      if (pick) iface = pick;
    } catch (e: any) {
      probeErr = `List interfaces failed: ${e?.message ?? e}`;
    }
    // If a capture is already running for this session (this window just
    // received the session via a detach/redock), re-attach to it instead
    // of showing the Start form.
    try {
      const existing = await api.tcpdumpActiveForSession(sessionId);
      if (existing.dump_id) await attach(existing);
    } catch { /* no active capture - show the Start form */ }
  });

  // Distinguishes a deliberate close (user hit ✕ → stop the backend
  // capture) from an incidental unmount (the session moved to another
  // window on detach, or the host list re-rendered). On an incidental
  // unmount we must NOT stop the capture - it's session-scoped on the
  // backend and another window will re-attach to it. Only a real close
  // tears the capture down.
  function closeCapture() {
    if (dumpId) api.tcpdumpStop(dumpId).catch(() => {});
    // Tear down listeners and release the captured data on a real close.
    // stop() deliberately keeps the data (you stop to inspect what you
    // caught), but closing the capture means you're done - drop the
    // packet/insight/flow buffers so the WebView can reclaim the few
    // hundred MB they hold instead of carrying it until the whole modal
    // is torn down.
    cleanupSubs();
    clearState();
    dumpId = null;
    running = false;
    onClose();
  }

  onDestroy(() => {
    cleanupSubs();
    clearState();
    if (flushTimer) { clearTimeout(flushTimer); flushTimer = null; }
    // Deliberately does NOT stop the backend capture: an unmount here is
    // usually the session being detached to another window, which must
    // keep the capture alive. closeCapture() handles the real close.
  });

  function cleanupSubs() {
    unsubLine?.(); unsubLine = null;
    unsubEvent?.(); unsubEvent = null;
    unsubInsight?.(); unsubInsight = null;
  }

  // Drop every large buffer so its memory is collectable. Reassign to
  // fresh empties (not splice) so Svelte's reactive consumers and the
  // derived chain (flows / dhcpTransactions / flatVisible) collapse to
  // empty and stop retaining the old packet objects.
  function clearState() {
    packets = [];
    packetQueue = [];
    insightList = [];
    routeChecks = {};
    flushPending = false;
    attachWatermark = 0;
    totalPackets = 0;
    floodNudge = false;
    rateWindowStart = 0;
    rateWindowCount = 0;
  }

  async function start() {
    if (!iface) return;
    if (!probe) return;
    errorMsg = null;
    clearState();
    attachWatermark = 0; // fresh capture, not an attach
    needsPassword = false;
    passwordError = null;
    try {
      const id = await api.tcpdumpStart({
        session_id: sessionId,
        iface,
        bpf_filter: bpf.trim(),
        max_count: continuous ? -1 : maxCount,
        root_user: probe.root_user,
        sudo_no_pwd: probe.sudo_no_pwd,
        use_saved_password: useSavedPassword && probe.has_candidate_password,
        verbose,
        insights,
        port_overrides: portOverridesAsMap(),
      });
      dumpId = id;
      running = true;
      subscribe(id, insights);
    } catch (e: any) {
      errorMsg = errMsg(e);
    }
  }

  // Wire the three event streams for a dumpID. Shared by start() (fresh
  // capture) and attach() (re-binding to a capture another window
  // started, e.g. after a tab detach). withInsights only gates the
  // insight stream subscription.
  function subscribe(id: string, withInsights: boolean) {
    // Idempotent: tear down any existing listeners first. Without this a
    // second start()/attach() on the same modal instance overwrote the
    // unsub handles, orphaning the old listeners - and each orphaned
    // listener's closure retained the whole component scope (packets et
    // al), so memory never recovered across capture restarts. This was
    // the WebView-side leak that survived stop and disconnect.
    cleanupSubs();
    // The backend coalesces the live stream into batches (one event per
    // ~100ms) so a high-rate continuous capture can't flood the IPC
    // queue. Each batch is an array; enqueue every packet, applying the
    // same attach-watermark dedupe per packet.
    unsubLine = EventsOn(`tcpdump_line_batch:${id}`, (b: { packets: Packet[]; skipped: number; total: number }) => {
      // The backend now tail-caps each batch to ~250 packets and tells us
      // the cumulative total + how many it skipped, so the UI shows the
      // true count while only ever handling the renderable tail.
      if (typeof b?.total === "number") {
        totalPackets = b.total;
        // Rate guard: more than RATE_LIMIT packets within RATE_WINDOW_MS
        // means the feed is faster than anyone can read - surface a
        // one-time nudge to add a BPF filter. We don't auto-stop: the
        // pipeline is bounded now, so a flood is annoying, not dangerous.
        const now = Date.now();
        if (now - rateWindowStart > RATE_WINDOW_MS) {
          rateWindowStart = now;
          rateWindowCount = b.total;
        } else if (!floodNudge && b.total - rateWindowCount > RATE_LIMIT) {
          floodNudge = true;
        }
      }
      const pkts = b?.packets;
      if (!pkts || pkts.length === 0) return;
      for (const pkt of pkts) {
        // During an attach, drop live packets the snapshot already holds
        // (seq <= watermark). Packets straddling the snapshot boundary
        // (seq > watermark) flow through normally.
        if (attachWatermark > 0 && (pkt.seq ?? 0) <= attachWatermark) continue;
        enqueuePacket(pkt);
      }
    });
    if (withInsights) {
      unsubInsight = EventsOn(`tcpdump_insight:${id}`, (ins: Insight) => {
        // Cap the insight list. The backend de-dupes, but its de-dupe
        // set resets under pathological volume, so the same findings can
        // re-fire on a very long busy capture - keep this list bounded so
        // it can't grow without limit.
        const next = [...insightList, ins];
        insightList = next.length > 500 ? next.slice(next.length - 500) : next;
      });
    }
    unsubEvent = EventsOn(`tcpdump_event:${id}`, (p: { event: string; msg: string }) => {
      switch (p.event) {
        case "needs_password":
          needsPassword = true;
          break;
        case "started":
          needsPassword = false;
          break;
        case "password_rejected":
          passwordError = "Password rejected. Try again.";
          needsPassword = true;
          passwordInput = "";
          break;
        case "error":
          errorMsg = p.msg || "Capture error";
          running = false;
          break;
        case "ended":
          running = false;
          if (p.msg) {
            errorMsg = p.msg;
          } else if (!continuous && packets.length > 0 && packets.length >= maxCount) {
            // Clean exit with the buffer full = tcpdump hit its -c packet
            // cap. Say so, so a fast capture finishing on its own doesn't
            // look like it crashed. (This is the usual "capture died on
            // detach" red herring: a small cap fills in seconds.)
            errorMsg = `Reached the ${maxCount}-packet limit. Raise "Max packets" and start again to capture longer.`;
          }
          cleanupSubs();
          break;
      }
    });
  }

  // Watermark for snapshot/live dedupe during an attach. Live packets
  // with seq <= this were already in the snapshot we loaded, so they're
  // dropped to avoid duplicates. 0 = no attach in progress (normal
  // capture started in this window).
  let attachWatermark = 0;

  // Re-attach to a capture already running for this session (started by
  // another window before a detach/redock moved the session here). Uses
  // the same snapshot-then-subscribe race fix as the PTY: subscribe
  // FIRST so no live packet is lost between snapshot and subscribe, then
  // load the server-side history, then dedupe live chunks whose seq is
  // already covered by the snapshot's cum watermark. This recovers the
  // packets captured before this window existed - they live on the
  // backend ring, not in the old window's heap.
  async function attach(info: import("./api").TcpdumpActiveInfo) {
    const id = info.dump_id;
    dumpId = id;
    running = true;
    // Restore the capture context so the header/controls show what's
    // actually running instead of stale defaults after a detach.
    if (info.iface) iface = info.iface;
    bpf = info.bpf_filter;
    verbose = info.verbose;
    insights = info.insights;
    continuous = info.continuous;
    if (!info.continuous && info.max_count > 0) maxCount = info.max_count;
    // Subscribe first; enqueued packets are held by the rAF/timer flush
    // and filtered against the watermark once we have it.
    subscribe(id, info.insights);
    try {
      const snap = await api.tcpdumpSnapshot(id);
      attachWatermark = snap.cum;
      // Drop anything already queued that the snapshot covers, then seed
      // packets with the snapshot history.
      packetQueue = packetQueue.filter((p) => (p.seq ?? 0) > attachWatermark);
      packets = snap.packets as Packet[];
    } catch {
      // No snapshot (capture vanished) - fall back to live-only.
    }
  }

  async function stop() {
    if (!dumpId) return;
    try { await api.tcpdumpStop(dumpId); } catch { /* ignore */ }
    running = false;
    cleanupSubs();
  }

  async function submitPassword() {
    if (!dumpId || !passwordInput) return;
    passwordError = null;
    try {
      await api.tcpdumpProvidePassword(dumpId, passwordInput);
      passwordInput = "";
      needsPassword = false;
    } catch (e: any) {
      passwordError = errMsg(e);
    }
  }

  // Lightweight client-side text filter. Matches the raw line, the
  // parsed info, AND the decoded summary + field values. Matching only
  // p.raw missed two things: (1) decode output like the CWMP method or a
  // ParameterValueStruct value lives in p.decoded, not the header line;
  // (2) in verbose mode p.raw is the hex dump, where the ASCII gloss is
  // wrapped every 16 bytes, so a search term longer than one gloss
  // segment never matched as a contiguous substring. Searching the
  // decoded fields fixes both.
  function packetHaystack(p: Packet): string {
    let s = p.raw + " " + (p.info ?? "");
    const d = p.decoded;
    if (d) {
      s += " " + (d.type ?? "") + " " + (d.summary ?? "");
      if (d.fields) {
        for (const k in d.fields) s += " " + k + " " + d.fields[k];
      }
    }
    return s.toLowerCase();
  }
  const filteredPackets = $derived(
    textFilter.trim()
      ? packets.filter((p) => packetHaystack(p).includes(textFilter.toLowerCase()))
      : packets
  );

  // --- Flat live list ----------------------------------------------
  // A plain, scrollable, newest-at-bottom list. No virtualization, no
  // column-reverse: the backend now tail-caps the live stream to ~250
  // packets per capture, so the DOM here is small and bounded no matter
  // the packet rate. (That backend cap - not anything in this view - is
  // what fixed the WebView2 memory growth; the earlier
  // virtualization/column-reverse attempts made the view cramped and
  // killed scrolling for no benefit.)
  const TAIL_ROWS = 250; // matches the backend tail cap
  const flatRows = $derived(
    filteredPackets.length > TAIL_ROWS
      ? filteredPackets.slice(filteredPackets.length - TAIL_ROWS)
      : filteredPackets,
  );

  // Auto-follow the tail like a terminal, but only when the user is
  // already at the bottom - if they've scrolled up to read, don't yank
  // them back. Cheap: one scrollTop write per flush at most, on a
  // 250-row list, only when pinned.
  let rowsEl: HTMLDivElement | undefined = $state();
  let pinnedToBottom = true;
  function onRowsScroll() {
    if (!rowsEl) return;
    const dist = rowsEl.scrollHeight - rowsEl.scrollTop - rowsEl.clientHeight;
    pinnedToBottom = dist < 24;
  }
  $effect(() => {
    void flatRows.length; // re-run as rows arrive
    if (viewMode !== "flat" || !pinnedToBottom || !rowsEl) return;
    rowsEl.scrollTop = rowsEl.scrollHeight;
  });

  // Flows view: group packets by flow_key, keep order by first-seen.
  // Each group renders as a collapsible row with count + endpoints
  // + last-seen timestamp.
  type Flow = {
    key: string;
    proto: string;
    a: string; // endpoint 1
    b: string; // endpoint 2
    packets: Packet[];
    firstSeen: string;
    lastSeen: string;
    bytes: number;
  };

  // Decoded packets (DHCP / DNS / ARP) for the Decode tab.
  //
  // IMPORTANT: every grouping below (decodedPackets, dhcpTransactions,
  // otherDecoded, flows) is gated on viewMode. They each rebuild a new
  // Map + arrays over the whole packet buffer every time filteredPackets
  // changes (~6x/s on a busy capture). When you're on the flat tab -
  // the common case - computing them is pure waste: large short-lived
  // allocations that pile up as garbage between GCs and, on the
  // production WebView2 where GC runs less eagerly, outpace collection
  // (the "puni i puni" growth that only sawtoothed under DevTools).
  // Returning [] when the view isn't active stops the churn at the
  // source. Each is recomputed instantly when you switch to its tab.
  const decodedPackets = $derived(
    viewMode === "decode"
      ? filteredPackets.filter((p) => p.decoded && p.decoded.type)
      : [],
  );

  // DHCP transactions grouped by xid. A standard DORA sequence shares
  // an xid across all four packets, so this turns
  //   DHCPDISCOVER · DHCPOFFER · DHCPREQUEST · DHCPACK
  // into a single timeline row. Non-DHCP packets stay in the flat list.
  type DhcpTx = {
    xid: string;
    packets: Packet[];
    stages: string[];   // ordered msg_types or BOOTP ops seen
    clientMAC: string;
    assignedIP: string;
    firstSeen: string;
    lastSeen: string;
  };
  const dhcpTransactions = $derived.by<DhcpTx[]>(() => {
    const m = new Map<string, DhcpTx>();
    const order: string[] = [];
    for (const p of decodedPackets) {
      const d = p.decoded!;
      if (d.type !== "dhcp") continue;
      const xid = d.fields.xid || "(no xid)";
      let tx = m.get(xid);
      if (!tx) {
        tx = {
          xid,
          packets: [],
          stages: [],
          clientMAC: "",
          assignedIP: "",
          firstSeen: p.timestamp,
          lastSeen: p.timestamp,
        };
        m.set(xid, tx);
        order.push(xid);
      }
      tx.packets.push(p);
      tx.lastSeen = p.timestamp || tx.lastSeen;
      const stage = d.fields.msg_type || d.fields.bootp_op || d.fields.direction || "?";
      tx.stages.push(stage);
      if (!tx.clientMAC && d.fields.client_mac) tx.clientMAC = d.fields.client_mac;
      if (!tx.assignedIP && d.fields.assigned_ip) tx.assignedIP = d.fields.assigned_ip;
    }
    return order.map((k) => m.get(k)!);
  });

  // Non-DHCP decoded packets (DNS / ARP) flow into the regular list
  // since they don't share xid semantics.
  const otherDecoded = $derived(
    decodedPackets.filter((p) => p.decoded!.type !== "dhcp"),
  );

  const flows = $derived.by<Flow[]>(() => {
    if (viewMode !== "flows") return []; // see decodedPackets note
    const m = new Map<string, Flow>();
    const order: string[] = [];
    for (const p of filteredPackets) {
      const key = p.flow_key || `_unparsed:${p.raw.slice(0, 40)}`;
      let f = m.get(key);
      if (!f) {
        const aStr = p.src_port
          ? `${p.src_ip}:${p.src_port}`
          : p.src_ip || "?";
        const bStr = p.dst_port
          ? `${p.dst_ip}:${p.dst_port}`
          : p.dst_ip || "?";
        f = {
          key,
          proto: p.proto || "?",
          a: aStr,
          b: bStr,
          packets: [],
          firstSeen: p.timestamp,
          lastSeen: p.timestamp,
          bytes: 0,
        };
        m.set(key, f);
        order.push(key);
      }
      f.packets.push(p);
      f.lastSeen = p.timestamp || f.lastSeen;
      f.bytes += p.length || 0;
    }
    return order.map((k) => m.get(k)!);
  });

  // Insights ordered error → warn → info, preserving arrival order
  // within a severity tier so the live feed reads chronologically.
  const sevRank: Record<string, number> = { error: 0, warn: 1, info: 2 };
  const sortedInsights = $derived(
    [...insightList].sort((a, b) => (sevRank[a.severity] ?? 9) - (sevRank[b.severity] ?? 9)),
  );

  // Run `ip route get` for the endpoints an insight points at. For a
  // wrong-source-IP finding we ask "how does the host reach the client,
  // sourced from the address it should have answered with" - the kernel
  // reply shows whether that egress is the expected interface/source.
  async function runRouteCheck(ins: Insight) {
    if (!ins.dst_ip) return;
    routeChecks = { ...routeChecks, [ins.flow_key]: null };
    try {
      const queries = [{ dst: ins.dst_ip, from: ins.src_ip || "" }];
      // Also ask the plain forward route (no `from`) so the user sees
      // the source IP the kernel picks by default - the 0.0.0.0-bind tell.
      if (ins.src_ip) queries.push({ dst: ins.dst_ip, from: "" });
      const res = await api.tcpdumpCheckRoute(sessionId, queries);
      routeChecks = { ...routeChecks, [ins.flow_key]: res };
    } catch (e: any) {
      routeChecks = {
        ...routeChecks,
        [ins.flow_key]: [{ dst: ins.dst_ip, from: ins.src_ip || "", dev: "", src: "", via: "", raw: "", error: errMsg(e) }],
      };
    }
  }

  // Escape / click-outside handler. When already hidden (minimised) the
  // overlay is display:none but use:clickOutside is still listening -
  // any click anywhere counts as "outside" and would fire this. Bail in
  // that case, otherwise a click on the restore chip (or the terminal)
  // tears the capture down via onClose. Also: a *finished* capture
  // (running=false) must still minimise rather than close, so the
  // history survives until the user explicitly closes it.
  function dismiss() {
    if (hidden) return; // already minimised - ignore stray outside-clicks
    if (onMinimize) onMinimize();
    else closeCapture();
  }

  function sevColor(sev: string): string {
    switch (sev) {
      case "error": return "var(--red)";
      case "warn": return "var(--yellow)";
      default: return "var(--blue)";
    }
  }

  function protoColor(proto: string): string {
    switch (proto) {
      case "tcp": return "var(--blue)";
      case "udp": return "var(--green)";
      case "icmp": return "var(--yellow)";
      case "icmpv6": return "var(--yellow)";
      case "arp": return "var(--mauve)";
      case "dhcp": return "var(--sapphire)";
      case "dns": return "var(--teal)";
      case "tls": return "var(--pink)";
      case "http": return "var(--peach)";
      case "ssh": return "var(--lavender)";
      case "ntp": return "var(--rosewater)";
      case "snmp": return "var(--maroon)";
      case "ldap": return "var(--flamingo)";
      case "smb": return "var(--red)";
      case "mqtt": return "var(--sky)";
      case "cwmp": return "var(--mauve)";
      default: return "var(--subtext0)";
    }
  }
</script>

<!-- Escape minimises when a capture is running (keeps it alive in the
     background) and otherwise closes. clickOutside likewise minimises
     rather than tearing down a live capture. Both fall back to onClose
     when there's nothing to keep (no minimise handler or not running). -->
<div class="overlay" class:hidden role="dialog" aria-modal="true" tabindex="-1"
     onkeydown={(e) => { if (e.key === "Escape") dismiss(); }}>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document"
       use:clickOutside={{ onOutside: dismiss }}
       onkeydown={(e) => e.stopPropagation()}>
    <header>
      <strong>tcpdump</strong>
      <span class="auth-state">
        {#if probe?.root_user}
          <span class="ok">running as root</span>
        {:else if probe?.sudo_no_pwd}
          <span class="ok">sudo (no password)</span>
        {:else if probe?.has_candidate_password && useSavedPassword}
          <span class="ok">sudo (saved password)</span>
        {:else if probe}
          <span class="warn">sudo will prompt</span>
        {/if}
      </span>
      {#if onMinimize}
        <button
          class="minimize"
          onclick={onMinimize}
          title={running ? "Minimise - capture keeps running in the background" : "Minimise"}
        >-</button>
      {/if}
      <button class="close" onclick={closeCapture} title={running ? "Stop capture and close" : "Close"}>✕</button>
    </header>

    {#if running}
      <!-- Capture context line: survives detach/redock (the attaching
           window restores these from the backend), so you can always
           see what interface / filter / mode the live capture is on. -->
      <!-- Capture context only (interface / filter / mode). The packet
           and insight counts live in the footer + tab badges, so they're
           not repeated here. -->
      <div class="capture-status">
        <span class="cs-iface">{iface}</span>
        {#if bpf.trim()}<span class="cs-bpf" title={bpf}>{bpf}</span>{/if}
        {#if verbose}<span class="cs-flag">verbose</span>{/if}
        {#if insights}<span class="cs-flag">insights</span>{/if}
        <span class="cs-flag">{continuous ? "continuous" : `cap ${maxCount}`}</span>
      </div>
    {/if}

    {#if probeErr}
      <div class="err">{probeErr}</div>
    {/if}

    <div class="controls">
      <label>
        <span>Interface</span>
        <select bind:value={iface} disabled={running}>
          {#each interfaces as i (i)}
            <option value={i}>{i}</option>
          {/each}
        </select>
      </label>
      <label class="grow">
        <span>BPF filter</span>
        <input
          bind:value={bpf}
          placeholder="e.g. host 10.0.0.1 and port 443"
          disabled={running}
        />
      </label>
      <label class="num">
        <span>Max packets</span>
        <input type="number" min="10" max="5000" step="50" bind:value={maxCount} disabled={running || continuous} />
      </label>
      <label class="chk" title="Run until you stop it - no packet cap. Needed for a long-lived capture that should keep running while you switch tabs or detach the window.">
        <input type="checkbox" bind:checked={continuous} disabled={running} />
        <span>Continuous</span>
      </label>
      {#if probe?.has_candidate_password && !probe.root_user && !probe.sudo_no_pwd}
        <label class="chk" title="Use the saved connection password for sudo. Turn off if the sudo password differs from the login password.">
          <input type="checkbox" bind:checked={useSavedPassword} disabled={running} />
          <span>Use saved password</span>
        </label>
      {/if}
      <label class="chk" title="Capture full payload - gives the Decode tab DHCP / DNS / ARP field-level dissection.">
        <input type="checkbox" bind:checked={verbose} disabled={running} />
        <span>Verbose (decode)</span>
      </label>
      <label class="chk" title="Flag routing / wrong-interface anomalies live: UDP replies from the wrong source IP (0.0.0.0-bound services), SYNs with no reply, ICMP unreachable/redirect/TTL-exceeded, ARP for off-subnet hosts, RST storms. TCP flag checks need Verbose; UDP/ICMP/ARP work either way.">
        <input type="checkbox" bind:checked={insights} disabled={running} />
        <span>Insights</span>
      </label>
      {#if running}
        <button class="danger" onclick={stop}>Stop</button>
      {:else}
        <button class="primary" onclick={start} disabled={!iface || !probe}>Start</button>
      {/if}
    </div>

    {#if verbose}
      <div class="port-overrides" title="Tell the decoder to treat a custom port as a known protocol - for HTTP on 9000, MQTT bridge on 1885, etc.">
        <span class="po-label">Custom port → proto</span>
        {#each portOverrides as o (o.port)}
          <span class="po-chip">
            {o.port} → {o.proto}
            <button
              class="po-remove"
              type="button"
              onclick={() => removePortOverride(o.port)}
              disabled={running}
              title="Remove"
            >×</button>
          </span>
        {/each}
        <input
          class="po-port"
          type="number"
          min="1"
          max="65535"
          placeholder="port"
          bind:value={newOverridePort}
          disabled={running}
          onkeydown={(e) => { if (e.key === "Enter") { e.preventDefault(); addPortOverride(); } }}
        />
        <select bind:value={newOverrideProto} disabled={running}>
          {#each KNOWN_PROTOS as p (p)}
            <option value={p}>{p}</option>
          {/each}
        </select>
        <button
          type="button"
          class="po-add"
          onclick={addPortOverride}
          disabled={running || !newOverridePort.trim()}
        >Add</button>
        {#if running}
          <span class="po-note">Stop the capture to change decode ports.</span>
        {/if}
      </div>
    {/if}

    {#if needsPassword}
      <div class="pw-prompt">
        <span class="pw-label">sudo password:</span>
        <input
          type="password"
          bind:value={passwordInput}
          bind:this={pwInputEl}
          onkeydown={(e) => { if (e.key === "Enter") submitPassword(); }}
        />
        <button class="primary" onclick={submitPassword}>Send</button>
        {#if passwordError}<span class="err inline">{passwordError}</span>{/if}
      </div>
    {/if}

    {#if errorMsg}
      <div class="err">{errorMsg}</div>
    {/if}

    <div class="row-filter">
      <input
        placeholder="Filter captured rows (client-side substring)…"
        bind:value={textFilter}
      />
      <div class="view-toggle">
        <button class:active={viewMode === "flat"} onclick={() => (viewMode = "flat")}>Flat</button>
        <button class:active={viewMode === "flows"} onclick={() => (viewMode = "flows")}>
          Flows {#if flows.length > 0}<span class="badge">{flows.length}</span>{/if}
        </button>
        <button class:active={viewMode === "decode"} onclick={() => (viewMode = "decode")}>
          Decode {#if decodedPackets.length > 0}<span class="badge">{decodedPackets.length}</span>{/if}
        </button>
        <button class:active={viewMode === "insights"} onclick={() => (viewMode = "insights")}>
          Insights {#if insightList.length > 0}<span class="badge alert">{insightList.length}</span>{/if}
        </button>
      </div>
      <span class="count">
        {#if viewMode === "flat"}
          <!-- Always the same format so it doesn't flicker between "N"
               and "N shown / M total" as totalPackets crosses the tail
               cap. The list is a tail; total is the real count, and we
               note the cap once it's actually exceeded. -->
          {totalPackets.toLocaleString()} pkt{totalPackets === 1 ? "" : "s"}{#if totalPackets > TAIL_ROWS} · last {TAIL_ROWS}{/if}
        {:else if viewMode === "flows"}
          {flows.length} flows
        {:else}
          {totalPackets.toLocaleString()} pkts
        {/if}
      </span>
    </div>

    {#if floodNudge}
      <div class="flood-nudge">
        High packet rate - the live view only shows the most recent
        {TAIL_ROWS}. Add a BPF filter (e.g. <code>host 1.2.3.4</code> or
        <code>port 443</code>) to narrow what tcpdump captures.
        <button class="x" title="Dismiss" onclick={() => (floodNudge = false)}>✕</button>
      </div>
    {/if}

    <div class="rows" bind:this={rowsEl} onscroll={onRowsScroll}>
      {#if packets.length === 0}
        <div class="empty">
          {running ? "Listening…" : "Pick an interface and press Start."}
        </div>
      {:else if viewMode === "flat"}
        <!-- Plain scrollable list, newest at the bottom. Bounded to the
             last TAIL_ROWS (the backend caps the live stream there too),
             so the DOM stays small. Auto-follows the tail unless you
             scroll up. -->
        {#each flatRows as p (p.seq)}
          <div class="row flat">
            <span class="ts">{p.timestamp}</span>
            {#if p.proto}<span class="proto" style:color={protoColor(p.proto)}>{p.proto}</span>{/if}
            <span class="raw">{p.raw}</span>
          </div>
        {/each}
      {:else if viewMode === "flows"}
        {#each flows as f (f.key)}
          <details class="flow">
            <summary>
              <span class="proto" style:color={protoColor(f.proto)}>{f.proto}</span>
              <span class="endpoints">{f.a} ↔ {f.b}</span>
              <span class="flow-meta">
                {f.packets.length} pkt{f.packets.length === 1 ? "" : "s"}
                {#if f.bytes > 0} · {f.bytes}B{/if}
                · {f.firstSeen}{f.lastSeen !== f.firstSeen ? "..." + f.lastSeen : ""}
              </span>
            </summary>
            <div class="flow-packets">
              {#each f.packets as p (p.seq)}
                <div class="row sub">
                  <span class="ts">{p.timestamp}</span>
                  <span class="dir">
                    {#if p.src_ip === f.a.split(":")[0] || `${p.src_ip}:${p.src_port}` === f.a}→{:else}←{/if}
                  </span>
                  {#if p.length > 0}<span class="len">{p.length}B</span>{/if}
                  <span class="raw">{p.info || p.raw}</span>
                </div>
              {/each}
            </div>
          </details>
        {/each}
      {:else if viewMode === "decode"}
        <!-- Decode tab: DHCP transactions (xid-grouped) + flat DNS/ARP -->
        {#if decodedPackets.length === 0}
          <div class="empty">
            {verbose
              ? "No decoded packets yet - DHCP / DNS / ARP traffic will land here."
              : "Verbose mode is off. Stop, tick \"Verbose (decode)\", and Start again to see protocol decode."}
          </div>
        {/if}

        {#each dhcpTransactions as tx (tx.xid)}
          <details class="decode tx" open>
            <summary>
              <span class="proto" style:color={protoColor("dhcp")}>dhcp</span>
              <span class="tx-xid">xid {tx.xid}</span>
              <span class="tx-stages">
                {#each tx.stages as s, i (i)}
                  {#if i > 0}<span class="tx-arrow">→</span>{/if}
                  <span class="tx-stage {s.toLowerCase()}">{s}</span>
                {/each}
              </span>
              <span class="flow-meta">
                {tx.packets.length} pkt{tx.packets.length === 1 ? "" : "s"}
                · {tx.firstSeen}{tx.lastSeen !== tx.firstSeen ? "..." + tx.lastSeen : ""}
                {#if tx.assignedIP} · {tx.assignedIP}{/if}
              </span>
            </summary>
            {#each tx.packets as p (p.seq)}
              {@const d = p.decoded!}
              <details class="decode sub" open={tx.packets.length <= 4}>
                <summary>
                  <span class="ts">{p.timestamp}</span>
                  <span class="decode-sum">{d.summary}</span>
                </summary>
                <table class="decode-fields">
                  <tbody>
                    {#each Object.entries(d.fields) as [k, v] (k)}
                      <tr><td class="dk">{k}</td><td class="dv">{v}</td></tr>
                    {/each}
                  </tbody>
                </table>
              </details>
            {/each}
          </details>
        {/each}

        {#each otherDecoded as p (p.seq)}
          {@const d = p.decoded!}
          <details class="decode" open>
            <summary>
              <span class="proto" style:color={protoColor(d.type)}>{d.type}</span>
              <span class="ts">{p.timestamp}</span>
              <span class="decode-sum">{d.summary}</span>
            </summary>
            <table class="decode-fields">
              <tbody>
                {#each Object.entries(d.fields) as [k, v] (k)}
                  <tr><td class="dk">{k}</td><td class="dv">{v}</td></tr>
                {/each}
              </tbody>
            </table>
          </details>
        {/each}
      {:else}
        <!-- Insights tab: live network-health findings -->
        {#if !insights}
          <div class="empty">
            Insights are off for this capture. Stop, tick "Insights", and Start again.
          </div>
        {:else if insightList.length === 0}
          <div class="empty">
            No anomalies so far. Routing / wrong-interface problems (UDP replies
            from the wrong source IP, SYNs with no reply, ICMP unreachable, ARP
            for off-subnet hosts) will appear here as they happen.
          </div>
        {/if}

        {#each sortedInsights as ins (ins.kind + "|" + ins.flow_key + "|" + ins.src_ip + "|" + ins.dst_ip)}
          <div class="insight" style:border-left-color={sevColor(ins.severity)}>
            <div class="insight-head">
              <span class="sev" style:color={sevColor(ins.severity)}>{ins.severity}</span>
              <span class="insight-title">{ins.title}</span>
              {#if ins.suggest_route_check && ins.dst_ip}
                <button
                  class="route-btn"
                  onclick={() => runRouteCheck(ins)}
                  disabled={routeChecks[ins.flow_key] === null}
                  title="Run `ip route get` on the host to see the actual egress interface and source IP for this flow."
                >
                  {routeChecks[ins.flow_key] === null ? "Checking…" : "Check route"}
                </button>
              {/if}
            </div>
            <div class="insight-detail">{ins.detail}</div>
            {#if routeChecks[ins.flow_key]}
              <div class="route-result">
                {#each routeChecks[ins.flow_key]! as r (r.dst + "|" + r.from)}
                  <div class="route-line">
                    <span class="route-q">
                      route to {r.dst}{r.from ? ` from ${r.from}` : " (default source)"}:
                    </span>
                    {#if r.error}
                      <span class="route-err">{r.error}</span>
                    {:else}
                      {#if r.dev}<span class="route-dev">dev {r.dev}</span>{/if}
                      {#if r.src}<span class="route-src">src {r.src}</span>{/if}
                      {#if r.via}<span class="route-via">via {r.via}</span>{/if}
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/each}
      {/if}
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: flex-start; justify-content: center;
    z-index: 320;
    padding-top: 6vh;
  }
  /* Minimised: hide the overlay entirely but keep the component mounted
     so the capture + subscriptions + Insights keep running. */
  .overlay.hidden { display: none; }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 8px;
    width: min(960px, 95vw);
    max-height: 85vh;
    display: flex; flex-direction: column;
    overflow: hidden;
    box-shadow: 0 20px 60px rgba(0,0,0,0.6);
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.8rem;
    padding: 0.55rem 0.9rem;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.9rem;
  }
  .auth-state { flex: 1; font-size: 0.78rem; }
  .auth-state .ok { color: var(--green); }
  .auth-state .warn { color: var(--yellow); }
  .capture-status {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.4rem;
    padding: 0.35rem 0.9rem;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.74rem;
    color: var(--subtext0);
  }
  .capture-status .cs-iface {
    font-weight: 600;
    color: var(--pink);
  }
  .capture-status .cs-bpf {
    font-family: var(--mono, monospace);
    color: var(--text);
    background: var(--surface0);
    padding: 0 0.35rem;
    border-radius: 3px;
    max-width: 22rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .capture-status .cs-flag {
    color: var(--overlay1);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    padding: 0 0.3rem;
    font-size: 0.68rem;
  }
  .minimize, .close {
    background: transparent; color: var(--subtext0); border: 0;
    cursor: pointer; font: inherit; padding: 0 0.4rem;
    line-height: 1;
  }
  .minimize { font-size: 1.2rem; }
  .minimize:hover { color: var(--text); }
  .close:hover { color: var(--red); }
  .controls {
    display: flex;
    gap: 0.5rem;
    padding: 0.5rem 0.8rem;
    align-items: end;
    border-bottom: 1px solid var(--surface0);
    flex-wrap: wrap;
  }
  .controls label {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.72rem;
    color: var(--subtext0);
  }
  .controls label.grow { flex: 1; min-width: 200px; }
  .controls label.num input { width: 5rem; }
  .controls label.chk {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
    color: var(--text);
    font-size: 0.82rem;
    padding-bottom: 0.1rem;
  }
  .controls input,
  .controls select {
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.3rem 0.45rem;
    font: inherit;
    font-size: 0.85rem;
  }
  .controls button { font-size: 0.85rem; padding: 0.35rem 0.8rem; }
  .port-overrides {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem;
    padding: 0.45rem 0.8rem;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.82rem;
  }
  .po-label { color: var(--subtext0); margin-right: 0.2rem; }
  .po-note { color: var(--overlay0); font-size: 0.72rem; font-style: italic; margin-left: 0.3rem; }
  .po-chip {
    display: inline-flex; align-items: center; gap: 0.3rem;
    background: var(--surface0); color: var(--text);
    padding: 0.15rem 0.45rem; border-radius: 12px;
    font-size: 0.78rem;
    font-family: ui-monospace, monospace;
  }
  .po-remove {
    background: transparent; border: 0; color: var(--overlay0);
    cursor: pointer; padding: 0 0.1rem; font-size: 0.95rem;
    line-height: 1;
  }
  .po-remove:hover:not(:disabled) { color: var(--red); }
  .po-port {
    width: 5.5rem;
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.25rem 0.4rem;
    font: inherit;
    font-size: 0.8rem;
  }
  .port-overrides select {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.2rem 0.4rem;
    font: inherit;
    font-size: 0.8rem;
  }
  .po-add {
    background: var(--surface0); color: var(--text); border: 0;
    padding: 0.25rem 0.65rem; border-radius: 3px;
    cursor: pointer; font: inherit; font-size: 0.78rem;
  }
  .po-add:hover:not(:disabled) { background: var(--surface1); }
  .po-add:disabled { opacity: 0.5; cursor: not-allowed; }
  .pw-prompt {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.5rem 0.8rem;
    background: var(--crust);
    border-bottom: 1px solid var(--surface0);
  }
  .pw-label { color: var(--yellow); font-size: 0.85rem; }
  .pw-prompt input[type="password"] {
    flex: 1; max-width: 220px;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.3rem 0.45rem;
    font: inherit;
  }
  .row-filter {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.4rem 0.8rem;
    border-bottom: 1px solid var(--surface0);
  }
  .row-filter input {
    flex: 1;
    background: var(--mantle); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 3px;
    padding: 0.3rem 0.45rem;
    font: inherit;
    font-size: 0.85rem;
  }
  .count { color: var(--overlay0); font-size: 0.75rem; }
  .rows {
    flex: 1;
    overflow-y: auto;
    background: var(--mantle);
    padding: 0.4rem 0.8rem;
    font-family: ui-monospace, "JetBrains Mono", monospace;
    font-size: 0.8rem;
  }
  .flood-nudge {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0 0.8rem 0.4rem;
    padding: 0.4rem 0.6rem;
    background: color-mix(in oklab, var(--yellow) 14%, var(--bg-panel));
    border: 1px solid color-mix(in oklab, var(--yellow) 35%, var(--bg-panel));
    border-radius: 4px;
    color: var(--text);
    font-size: 0.78rem;
  }
  .flood-nudge code {
    background: var(--crust); padding: 0 0.25rem; border-radius: 2px;
    font-size: 0.74rem;
  }
  .flood-nudge .x {
    margin-left: auto; background: transparent; border: 0;
    color: var(--subtext0); cursor: pointer; padding: 0 0.25rem;
  }
  .flood-nudge .x:hover { color: var(--text); }
  .row {
    color: var(--text);
    white-space: pre-wrap;
    padding: 0.05rem 0;
    border-bottom: 1px solid var(--crust);
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
  }
  .row .ts { color: var(--overlay0); font-size: 0.72rem; flex-shrink: 0; }
  .row .proto {
    font-weight: 600;
    font-size: 0.7rem;
    text-transform: uppercase;
    flex-shrink: 0;
    min-width: 2.5rem;
  }
  .row .len { color: var(--overlay1); font-size: 0.72rem; flex-shrink: 0; }
  .row .dir { color: var(--overlay0); flex-shrink: 0; }
  .row .raw { flex: 1; min-width: 0; word-break: break-all; }
  .row.sub { padding-left: 1rem; border-bottom: 0; }
  /* Virtualized flat rows: fixed height + single line so the
     scroll-window math (FLAT_ROW_H) stays exact. Long lines truncate
     with ellipsis instead of wrapping; full content is in the Decode
     tab and the flows view. */
  /* Single-line rows for the live feed: one packet per line, ellipsis on
     overflow so the list scans cleanly. Normal line height - readable,
     not crammed. */
  .row.flat {
    padding: 0.1rem 0;
    align-items: baseline;
    white-space: nowrap;
    overflow: hidden;
  }
  .row.flat .raw {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    word-break: normal;
  }
  .view-toggle {
    display: flex;
    gap: 1px;
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    overflow: hidden;
  }
  .view-toggle button {
    background: transparent;
    color: var(--subtext0);
    border: 0;
    padding: 0.25rem 0.6rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.78rem;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  .view-toggle button:hover { background: var(--surface0); color: var(--text); }
  .view-toggle button.active {
    background: var(--surface0);
    color: var(--text);
  }
  .view-toggle .badge {
    background: var(--surface1);
    color: var(--text);
    border-radius: 999px;
    padding: 0 0.35rem;
    font-size: 0.65rem;
  }
  .flow {
    border-bottom: 1px solid var(--crust);
  }
  .flow summary {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    padding: 0.25rem 0;
    cursor: pointer;
    list-style: none;
  }
  .flow summary::-webkit-details-marker { display: none; }
  .flow summary:hover { background: var(--base); }
  .flow .endpoints {
    color: var(--text);
    flex: 1;
    min-width: 0;
    word-break: break-all;
  }
  .flow .flow-meta {
    color: var(--overlay0);
    font-size: 0.72rem;
    flex-shrink: 0;
  }
  .flow-packets {
    padding: 0.2rem 0 0.3rem;
    background: var(--crust);
  }
  .decode {
    border-bottom: 1px solid var(--crust);
    padding: 0.2rem 0;
  }
  .decode summary {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    cursor: pointer;
    list-style: none;
    padding: 0.25rem 0;
  }
  .decode summary::-webkit-details-marker { display: none; }
  .decode summary:hover { background: var(--base); }
  .decode-sum {
    color: var(--text);
    flex: 1;
    word-break: break-all;
  }
  .decode-fields {
    margin: 0.3rem 0 0.5rem 2rem;
    border-collapse: collapse;
    font-size: 0.78rem;
  }
  .decode-fields .dk {
    color: var(--subtext0);
    padding: 0.1rem 0.6rem 0.1rem 0;
    text-align: right;
    vertical-align: top;
    font-family: ui-monospace, monospace;
    white-space: nowrap;
  }
  .decode-fields .dv {
    color: var(--text);
    padding: 0.1rem 0;
    font-family: ui-monospace, monospace;
    word-break: break-all;
  }
  .tx { background: var(--crust); }
  .tx > summary { padding: 0.4rem 0.4rem; flex-wrap: wrap; }
  .tx-xid {
    color: var(--yellow);
    font-family: ui-monospace, monospace;
    font-size: 0.74rem;
  }
  .tx-stages {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    flex: 1;
    flex-wrap: wrap;
  }
  .tx-arrow { color: var(--overlay0); }
  .tx-stage {
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    background: var(--surface0);
    color: var(--text);
    font-size: 0.72rem;
    font-weight: 600;
    text-transform: uppercase;
  }
  .tx-stage.discover { background: var(--sapphire); color: var(--on-accent); }
  .tx-stage.offer    { background: var(--teal); color: var(--on-accent); }
  .tx-stage.request  { background: var(--yellow); color: var(--on-accent); }
  .tx-stage.ack      { background: var(--green); color: var(--on-accent); }
  .tx-stage.nak      { background: var(--red); color: var(--on-accent); }
  .tx-stage.bootrequest, .tx-stage.\?  { background: var(--surface1); }
  .tx-stage.bootreply  { background: var(--surface1); }
  .decode.sub {
    margin-left: 1.5rem;
    border-bottom: 0;
    background: var(--mantle);
    padding: 0.15rem 0.4rem;
    margin-top: 2px;
    border-radius: 3px;
  }
  .decode.sub > summary { padding: 0.15rem 0; }
  .empty { color: var(--overlay0); font-size: 0.85rem; padding: 0.8rem 0; font-family: inherit; }
  .err {
    padding: 0.4rem 0.8rem;
    background: color-mix(in oklab, var(--red) 14%, var(--bg-panel));
    color: var(--red);
    font-size: 0.82rem;
    border-bottom: 1px solid var(--surface0);
  }
  .err.inline { padding: 0; background: transparent; border: 0; }

  /* Insights tab */
  .view-toggle .badge.alert {
    background: var(--red);
    color: var(--crust);
    font-weight: 700;
  }
  .insight {
    border-left: 3px solid var(--overlay0);
    background: var(--mantle);
    border-radius: 0 5px 5px 0;
    padding: 0.5rem 0.7rem;
    margin: 0.4rem 0;
  }
  .insight-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .insight .sev {
    text-transform: uppercase;
    font-size: 0.62rem;
    font-weight: 700;
    letter-spacing: 0.04em;
  }
  .insight-title {
    flex: 1;
    color: var(--text);
    font-weight: 600;
    font-size: 0.84rem;
  }
  .route-btn {
    background: var(--surface0);
    color: var(--text);
    border: 1px solid var(--surface1);
    border-radius: 4px;
    padding: 0.15rem 0.55rem;
    font-size: 0.72rem;
    cursor: pointer;
    white-space: nowrap;
  }
  .route-btn:hover:not(:disabled) { background: var(--surface1); }
  .route-btn:disabled { opacity: 0.6; cursor: default; }
  .insight-detail {
    color: var(--subtext0);
    font-size: 0.78rem;
    line-height: 1.45;
    margin-top: 0.3rem;
  }
  .route-result {
    margin-top: 0.45rem;
    padding-top: 0.4rem;
    border-top: 1px dashed var(--surface0);
    font-family: var(--mono, monospace);
    font-size: 0.76rem;
  }
  .route-line { display: flex; flex-wrap: wrap; gap: 0.45rem; padding: 0.12rem 0; }
  .route-q { color: var(--overlay1); }
  .route-dev { color: var(--blue); }
  .route-src { color: var(--green); }
  .route-via { color: var(--mauve); }
  .route-err { color: var(--red); }
</style>
