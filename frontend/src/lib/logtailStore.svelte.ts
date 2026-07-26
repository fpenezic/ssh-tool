// Window-local registry of open log tails, keyed by sessionId. Mirrors
// tcpdumpStore exactly: the tail view is mounted once per session up in
// TerminalArea (stable sessionId key, survives pane-tree remounts), and the
// backend stream (a Go goroutine keyed by session) is unaffected by frontend
// view lifetime. "detached" = viewed in its own window; the main window keeps
// the entry (chip) but must not also mount a modal for it.

export type LogTailMode = "open" | "minimized" | "detached";

export interface LogTailStats {
  source: string; // unit or path, for the chip label
  lines: number;
  running: boolean;
}

interface Entry {
  sessionId: string;
  mode: LogTailMode;
  stats: LogTailStats | null;
}

class LogTailStore {
  private entries = new Map<string, Entry>();
  // Same two-counter split as tcpdumpStore: membershipVersion for
  // mount/mode, statsVersion for the chip, kept separate to avoid a
  // stats-push -> host-rerender -> stats-push reactive loop.
  membershipVersion = $state(0);
  statsVersion = $state(0);

  open(sessionId: string) {
    const e = this.entries.get(sessionId);
    if (e) {
      e.mode = "open";
    } else {
      this.entries.set(sessionId, { sessionId, mode: "open", stats: null });
    }
    this.membershipVersion++;
  }

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

  detach(sessionId: string) {
    const e = this.entries.get(sessionId);
    if (e) {
      e.mode = "detached";
    } else {
      this.entries.set(sessionId, { sessionId, mode: "detached", stats: null });
    }
    this.membershipVersion++;
  }

  close(sessionId: string) {
    if (this.entries.delete(sessionId)) this.membershipVersion++;
  }

  setStats(sessionId: string, stats: LogTailStats) {
    const e = this.entries.get(sessionId);
    if (!e) return;
    const p = e.stats;
    if (p && p.source === stats.source && p.lines === stats.lines && p.running === stats.running) {
      return;
    }
    e.stats = stats;
    this.statsVersion++;
  }

  modeOf(sessionId: string): LogTailMode | null {
    void this.membershipVersion;
    return this.entries.get(sessionId)?.mode ?? null;
  }

  statsOf(sessionId: string): LogTailStats | null {
    void this.statsVersion;
    return this.entries.get(sessionId)?.stats ?? null;
  }

  list(): Entry[] {
    void this.membershipVersion;
    return [...this.entries.values()];
  }
}

export const logtail = new LogTailStore();
