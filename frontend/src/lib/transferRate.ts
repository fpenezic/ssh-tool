// Transfer speed and ETA from the sftp_progress event stream.
//
// The backend emits progress every 100 ms or 256 KB, whichever comes
// first. The rate is measured over a sliding window rather than between
// two consecutive events: SFTP writes arrive in bursts, and the gap
// between two events swings the instantaneous figure by an order of
// magnitude. Three seconds is long enough to be readable and short
// enough to follow a real change (a link that slows down mid-transfer).

export type RateSample = { t: number; bytes: number };

const WINDOW_MS = 3000;
// Below this span the figure is noise; show nothing rather than a
// number that jumps on every event.
const MIN_SPAN_MS = 500;

// pushSample appends a sample and drops the ones that fell out of the
// window. The newest sample older than the window is kept as the anchor,
// so the measured span always covers the full window once it exists.
export function pushSample(samples: RateSample[], s: RateSample, windowMs = WINDOW_MS): RateSample[] {
  const out = [...samples, s];
  while (out.length > 2 && out[1].t <= s.t - windowMs) out.shift();
  return out;
}

// rateBps returns bytes per second, or null while there is too little
// history to say. `now` rather than the last sample's time, so a stalled
// transfer (no events at all) reads 0 instead of freezing on the last
// good figure.
export function rateBps(samples: RateSample[], now: number, windowMs = WINDOW_MS): number | null {
  if (samples.length === 0) return null;
  const first = samples[0];
  const last = samples[samples.length - 1];
  if (now - last.t > windowMs) return 0;
  const span = now - first.t;
  if (span < MIN_SPAN_MS) return null;
  return ((last.bytes - first.bytes) * 1000) / span;
}

// etaSeconds is the time left at the current rate, or null when the
// rate is unknown or zero (a stall has no meaningful ETA).
export function etaSeconds(bytes: number, total: number, bps: number | null): number | null {
  if (!bps || bps <= 0 || total <= 0 || bytes >= total) return null;
  return Math.ceil((total - bytes) / bps);
}

export function fmtEta(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`;
}

// A transfer as the batch summary sees it. `running` is false once it
// finished, failed or was cancelled.
export type BatchItem = { bytes: number; total: number; samples: RateSample[]; running: boolean; failed: boolean };

export type BatchSummary = { running: number; bytes: number; total: number; bps: number | null; eta: number | null };

// summarizeBatch folds the visible transfers into one line. Completed
// transfers stay in the byte totals (they are part of the batch the user
// started - dropping them would move the percentage backwards); failed and
// cancelled ones leave it, since their remaining bytes will never come.
// The rate is the sum of the running transfers' own rates: they share one
// link, so the sum is what the link is doing.
export function summarizeBatch(items: BatchItem[], now: number): BatchSummary {
  let running = 0, bytes = 0, total = 0, bps = 0, known = false;
  for (const it of items) {
    if (it.failed) continue;
    bytes += Math.min(it.bytes, it.total || it.bytes);
    total += it.total;
    if (!it.running) continue;
    running++;
    const r = rateBps(it.samples, now);
    if (r !== null) { bps += r; known = true; }
  }
  const rate = known ? bps : null;
  return { running, bytes, total, bps: rate, eta: etaSeconds(bytes, total, rate) };
}
