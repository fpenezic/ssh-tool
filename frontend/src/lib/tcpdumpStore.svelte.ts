// Window-local registry of open tcpdump captures, keyed by sessionId.
//
// Why this exists: the capture UI used to be mounted inside PaneNode,
// whose subtree is rebuilt on every layout mutation (split, SFTP-split,
// drag, redock - all call replaceLeaf which creates fresh split nodes).
// That remounted the modal and tore down its subscriptions + packet
// buffer, killing a running capture the moment you touched the layout.
//
// The fix: the capture overlay is mounted once per session up in
// TerminalArea (above the pane tree), keyed by sessionId - a stable id
// the layout never rewrites. PaneNode just toggles entries here. The
// backend capture (a Go goroutine on tcpdump_*:<dumpId>) is unaffected
// by any of this; only the frontend view lifetime is at stake.
//
// State is window-local on purpose: separate windows are separate JS
// heaps, so the packet buffer can't cross a detach. On detach the new
// window mounts a fresh view and starts accumulating from that point;
// the capture itself keeps running because it lives on the backend.

export type TcpdumpMode = "open" | "minimized";

export interface TcpdumpStats {
  iface: string;
  packets: number;
  insights: number;
  running: boolean;
}

interface Entry {
  sessionId: string;
  mode: TcpdumpMode;
  stats: TcpdumpStats | null;
}

class TcpdumpStore {
  // Keyed by sessionId. A bare version counter forces $derived consumers
  // to re-evaluate when we mutate an entry in place (Svelte's deep
  // tracking doesn't see field writes on objects inside a Map) - same
  // pattern broadcast.svelte.ts uses for its groups.
  private entries = new Map<string, Entry>();
  // TWO independent counters, deliberately. membershipVersion changes on
  // open/minimize/close - what the TerminalArea host's mount list and
  // PaneNode's mode chip depend on. statsVersion changes ONLY on
  // setStats. They MUST stay separate: the mounted modal pushes stats via
  // an $effect every time packets/insights tick, and if that bump also
  // invalidated the host's mount list, the host would re-render the modal,
  // re-running its $effect, calling setStats again - an infinite reactive
  // loop (effect_update_depth_exceeded). Stats consumers (the chip) read
  // statsVersion; mount/mode consumers read membershipVersion; neither
  // crosses over.
  membershipVersion = $state(0);
  statsVersion = $state(0);

  // open() either creates a new entry (first time the user hits the
  // tcpdump button for this session) or restores a minimised one.
  open(sessionId: string) {
    const e = this.entries.get(sessionId);
    if (e) {
      e.mode = "open";
    } else {
      this.entries.set(sessionId, { sessionId, mode: "open", stats: null });
    }
    this.membershipVersion++;
  }

  // ensureMinimized registers a capture this window didn't start (it's
  // running on the backend for this session, e.g. after a detach moved
  // the session here) as a background/minimized entry, so the toolbar
  // chip appears and the hidden modal mounts + attaches. No-op if an
  // entry already exists (don't clobber an open one).
  ensureMinimized(sessionId: string) {
    if (this.entries.has(sessionId)) return;
    this.entries.set(sessionId, { sessionId, mode: "minimized", stats: null });
    this.membershipVersion++;
  }

  minimize(sessionId: string) {
    const e = this.entries.get(sessionId);
    if (e) {
      e.mode = "minimized";
      this.membershipVersion++;
    }
  }

  // close() removes the entry entirely - the mounted view unmounts,
  // which stops the backend capture via its onDestroy.
  close(sessionId: string) {
    if (this.entries.delete(sessionId)) this.membershipVersion++;
  }

  setStats(sessionId: string, stats: TcpdumpStats) {
    const e = this.entries.get(sessionId);
    if (!e) return;
    // Skip no-op updates. The modal pushes stats from an $effect that
    // re-runs on every render; without this equality guard each render
    // bumps statsVersion, which re-renders stats consumers, which can
    // feed back into another render - a reactive loop. Only bump when a
    // value actually changed.
    const p = e.stats;
    if (p && p.iface === stats.iface && p.packets === stats.packets &&
        p.insights === stats.insights && p.running === stats.running) {
      return;
    }
    e.stats = stats;
    this.statsVersion++; // stats only - must NOT touch membershipVersion
  }

  modeOf(sessionId: string): TcpdumpMode | null {
    void this.membershipVersion;
    return this.entries.get(sessionId)?.mode ?? null;
  }

  statsOf(sessionId: string): TcpdumpStats | null {
    void this.statsVersion;
    return this.entries.get(sessionId)?.stats ?? null;
  }

  // All open/minimised captures, for the TerminalArea host to mount.
  // Reads membershipVersion ONLY - never statsVersion - so a stats push
  // can't re-render the host and retrigger the modal's stats $effect.
  list(): Entry[] {
    void this.membershipVersion;
    return [...this.entries.values()];
  }
}

export const tcpdump = new TcpdumpStore();
