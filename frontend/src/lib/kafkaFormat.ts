// Pure helpers for the Kafka tab: payload decoding, JSON pretty printing,
// message filtering and export. No Svelte, no IPC - unit-tested.

import type { KafkaMessage, KafkaPayload } from "./api";

export function formatCount(n: number): string {
  if (n < 0) return "?";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + "k";
  if (n < 1_000_000_000) return (n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0) + "M";
  return (n / 1_000_000_000).toFixed(1) + "G";
}

export function formatBytes(n: number): string {
  if (n < 0) return "?";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${v} B` : `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

export function formatTime(ms: number): string {
  const d = new Date(ms);
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

// The bytes of a base64 payload.
export function base64Bytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// Hex dump: 16 bytes a line, offset, hex, printable ASCII.
export function hexDump(bytes: Uint8Array, max = 4096): string {
  const lines: string[] = [];
  const n = Math.min(bytes.length, max);
  for (let off = 0; off < n; off += 16) {
    const row = bytes.slice(off, Math.min(off + 16, n));
    const hex = Array.from(row, (b) => b.toString(16).padStart(2, "0")).join(" ");
    const ascii = Array.from(row, (b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : ".")).join("");
    lines.push(`${off.toString(16).padStart(8, "0")}  ${hex.padEnd(47)}  ${ascii}`);
  }
  if (bytes.length > max) lines.push(`... ${bytes.length - max} more bytes`);
  return lines.join("\n");
}

// One-line preview for the message list.
export function preview(p: KafkaPayload | null, max = 200): string {
  if (!p) return "null";
  if (p.encoding === "base64") return `<${p.size} bytes binary>`;
  const s = p.text.replace(/\s+/g, " ");
  return s.length > max ? s.slice(0, max) + "…" : s;
}

// The payload pretty-printed when it is JSON, else as is. Binary payloads
// come back as a hex dump.
export function pretty(p: KafkaPayload | null): { text: string; kind: "null" | "json" | "text" | "binary" } {
  if (!p) return { text: "null", kind: "null" };
  if (p.encoding === "base64") return { text: hexDump(base64Bytes(p.text)), kind: "binary" };
  const t = p.text.trim();
  if ((t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))) {
    try {
      return { text: JSON.stringify(JSON.parse(t), null, 2), kind: "json" };
    } catch {
      // not JSON after all
    }
  }
  return { text: p.text, kind: "text" };
}

// Case-insensitive match over key, value and headers (binary payloads are
// matched on their base64 text, which is useless but harmless).
export function matches(m: KafkaMessage, q: string): boolean {
  if (!q) return true;
  const n = q.toLowerCase();
  const has = (p: KafkaPayload | null) => !!p && p.text.toLowerCase().includes(n);
  if (has(m.key) || has(m.value)) return true;
  return m.headers.some((h) => h.key.toLowerCase().includes(n) || has(h.value));
}

// NDJSON export: one JSON object a line. Text payloads stay text (parsed
// JSON stays a string so the export round-trips the exact bytes); binary
// ones are base64 with an "_encoding" marker.
export function toNDJSON(msgs: KafkaMessage[], topic: string): string {
  const enc = (p: KafkaPayload | null) => (p ? p.text : null);
  return msgs
    .map((m) => {
      const o: Record<string, unknown> = {
        topic,
        partition: m.partition,
        offset: m.offset,
        timestamp: m.timestamp,
        key: enc(m.key),
        value: enc(m.value),
        headers: Object.fromEntries(m.headers.map((h) => [h.key, h.value.text])),
      };
      const bin = [
        m.key?.encoding === "base64" ? "key" : "",
        m.value?.encoding === "base64" ? "value" : "",
        ...m.headers.filter((h) => h.value.encoding === "base64").map((h) => "header:" + h.key),
      ].filter(Boolean);
      if (bin.length) o._base64 = bin;
      return JSON.stringify(o);
    })
    .join("\n") + (msgs.length ? "\n" : "");
}

// Parses "key=value" lines (the config editor's text form). Blank lines and
// lines starting with # are skipped; a line without "=" is an error.
export function parseConfigLines(text: string): { set: Record<string, string>; error?: string } {
  const set: Record<string, string> = {};
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) return { set, error: `line ${i + 1}: expected key=value` };
    set[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  return { set };
}

// A rate between two samples, per second; null when it cannot be computed.
export function ratePerSec(prev: { at: number; v: number } | null, cur: { at: number; v: number }): number | null {
  if (!prev || cur.at <= prev.at || prev.v < 0 || cur.v < 0) return null;
  return ((cur.v - prev.v) * 1000) / (cur.at - prev.at);
}
