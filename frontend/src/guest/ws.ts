// The guest websocket client - the TS mirror of internal/share/protocol.go.
//
// It speaks a single websocket to the host's share server: receives the
// handshake, manifest, per-session snapshots, and live binary output frames;
// sends input (control shares only), ready acks, and pings. It performs the
// same cum-watermark dedupe the desktop Terminal does, but WITHOUT the
// reorder/gap machinery: a websocket preserves order, so a straddle trim at the
// snapshot boundary is the only correction needed.
//
// Zero Wails: this runs in a plain browser. No api.ts, no stores, no runtime.

import type { SerializedPaneTab } from "../lib/panetypes";

export type Level = "read" | "control";

export interface ManifestSession {
  id: string; // guest slot "s1"
  name: string;
  cols: number;
  rows: number;
  state: string;
}

export interface Manifest {
  share_id: string;
  level: Level;
  host_name: string;
  active_tab: number;
  tabs: SerializedPaneTab[];
  sessions: ManifestSession[];
}

export interface Pending {
  host: string;
  fp_hex: string;
  fp_short: string;
  fp_words: string;
}

// Phase of the guest connection, surfaced to the UI.
export type Phase =
  | { kind: "connecting" }
  | { kind: "pending"; info: Pending }
  | { kind: "live"; manifest: Manifest }
  | { kind: "denied" }
  | { kind: "closed"; reason: string }
  | { kind: "error"; message: string };

// A per-session sink the UI wires up: the client calls these as frames arrive.
export interface SessionSink {
  // write appends decoded PTY bytes (already watermark-deduped) to the terminal.
  write(data: Uint8Array): void;
  // clear resets the terminal (before writing a fresh snapshot on re-manifest,
  // so a re-sync doesn't append a duplicate copy of the scrollback).
  clear(): void;
  // resize sets the host PTY dimensions (guest letterboxes to these).
  resize(cols: number, rows: number): void;
  // state marks a session connected/disconnected.
  state(state: string, reason: string): void;
}

const OUTPUT_KIND = 0x01;

export class GuestClient {
  private ws: WebSocket | null = null;
  private sinks = new Map<string, SessionSink>();
  // Per-slot watermark: live chunks with cum <= watermark are already covered
  // by the snapshot and are dropped; a straddling chunk is trimmed.
  private watermark = new Map<string, number>();
  private manifest: Manifest | null = null;

  // onPhase is the single UI subscription point.
  onPhase: (p: Phase) => void = () => {};
  // onActiveTab fires when the host switches tabs, so a following guest can too.
  onActiveTab: (index: number) => void = () => {};

  constructor(private url: string) {}

  connect() {
    this.onPhase({ kind: "connecting" });
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch (e) {
      this.onPhase({ kind: "error", message: String(e) });
      return;
    }
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onmessage = (ev) => this.onMessage(ev);
    ws.onerror = () => this.onPhase({ kind: "error", message: "connection error" });
    ws.onclose = (ev) => {
      // If the phase already moved to denied/closed via a bye frame, keep it.
      this.onPhase({ kind: "closed", reason: ev.reason || closeCodeReason(ev.code) });
    };
  }

  // registerSink is called by the UI once it has an xterm for a slot. Any
  // buffered watermark is already tracked; the sink starts receiving live
  // output immediately. The UI must call ready() after writing the snapshot.
  registerSink(slot: string, sink: SessionSink) {
    this.sinks.set(slot, sink);
  }

  // ready tells the host the guest has written slot's snapshot, unblocking input
  // for it (the replay-injection gate). Harmless on read-only shares.
  ready(slot: string) {
    this.send({ t: "ready", ready: { sid: slot } });
  }

  // reportTab tells the host which tab the guest is looking at (informational).
  reportTab(index: number) {
    this.send({ t: "guest_tab", guest_tab: { index } });
  }

  // sendInput forwards a keystroke (control shares only; the host enforces).
  sendInput(slot: string, data: Uint8Array) {
    this.send({ t: "input", input: { sid: slot, b64: toB64(data) } });
  }

  close() {
    this.ws?.close();
  }

  private send(frame: unknown) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(frame));
    }
  }

  private onMessage(ev: MessageEvent) {
    if (ev.data instanceof ArrayBuffer) {
      this.onBinary(new Uint8Array(ev.data));
      return;
    }
    let frame: any;
    try {
      frame = JSON.parse(ev.data as string);
    } catch {
      return;
    }
    switch (frame.t) {
      case "pending":
        this.onPhase({ kind: "pending", info: frame.pending });
        break;
      case "manifest":
        this.manifest = frame.manifest as Manifest;
        this.onPhase({ kind: "live", manifest: this.manifest });
        break;
      case "snap":
        this.onSnap(frame.snap);
        break;
      case "size":
        this.sinks.get(frame.size.sid)?.resize(frame.size.cols, frame.size.rows);
        break;
      case "state":
        this.sinks.get(frame.state.sid)?.state(frame.state.state, frame.state.reason ?? "");
        break;
      case "active_tab":
        this.onActiveTab(frame.active_tab?.index ?? 0);
        break;
      case "bye":
        this.onBye(frame.bye?.reason ?? "");
        break;
      case "pong":
        break;
    }
  }

  private onSnap(snap: { sid: string; b64: string; cum: number }) {
    this.watermark.set(snap.sid, snap.cum);
    const sink = this.sinks.get(snap.sid);
    // Clear first so a re-manifest snapshot replaces the scrollback rather than
    // appending a second copy of it (the duplicated "Last login" lines).
    sink?.clear();
    if (snap.b64) {
      sink?.write(fromB64(snap.b64));
    }
    // Acknowledge as soon as the snapshot is applied. If the sink isn't
    // registered yet (UI still mounting), the UI calls ready() itself after
    // registerSink; sending here too is idempotent on the host side.
    if (this.sinks.has(snap.sid)) {
      this.ready(snap.sid);
    }
  }

  private onBinary(buf: Uint8Array) {
    const parsed = parseOutput(buf);
    if (!parsed) return;
    const { sid, cum, data } = parsed;
    const sink = this.sinks.get(sid);
    if (!sink) return;

    const wm = this.watermark.get(sid);
    if (wm === undefined) {
      // No snapshot yet - shouldn't happen (snap precedes live), but be safe.
      sink.write(data);
      return;
    }
    const start = cum - data.length;
    if (cum <= wm) {
      return; // fully covered by the snapshot
    }
    if (start < wm) {
      sink.write(data.subarray(wm - start)); // trim the overlapping prefix
    } else {
      sink.write(data);
    }
    this.watermark.set(sid, cum);
  }

  private onBye(reason: string) {
    if (reason === "denied") {
      this.onPhase({ kind: "denied" });
    } else {
      this.onPhase({ kind: "closed", reason });
    }
    this.ws?.close();
  }
}

// parseOutput mirrors internal/share/protocol.go ParseOutput.
function parseOutput(b: Uint8Array): { sid: string; cum: number; data: Uint8Array } | null {
  if (b.length < 1 + 2 + 8 || b[0] !== OUTPUT_KIND) return null;
  const view = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const sidLen = view.getUint16(1);
  if (b.length < 3 + sidLen + 8) return null;
  const sid = new TextDecoder().decode(b.subarray(3, 3 + sidLen));
  const off = 3 + sidLen;
  // cum is uint64; terminal offsets never exceed 2^53 in practice.
  const hi = view.getUint32(off);
  const lo = view.getUint32(off + 4);
  const cum = hi * 2 ** 32 + lo;
  const data = b.subarray(off + 8);
  return { sid, cum, data };
}

function toB64(data: Uint8Array): string {
  let s = "";
  for (let i = 0; i < data.length; i++) s += String.fromCharCode(data[i]);
  return btoa(s);
}

function fromB64(b64: string): Uint8Array {
  const s = atob(b64);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

function closeCodeReason(code: number): string {
  switch (code) {
    case 4403:
      return "The host denied access.";
    case 1001:
      return "The host ended the session.";
    case 1008:
      return "Disconnected: the connection couldn't keep up.";
    default:
      return "Connection closed.";
  }
}
