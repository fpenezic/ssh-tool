// A NetBird setup key as people paste it: the key alone, or the whole
// command the dashboard's "Add peer" shows
// (netbird up --management-url https://vpn.example.com --setup-key KEY).
// From the command the key is taken, and the management URL with it.

export interface PastedKey {
  key: string;
  management?: string;
}

// The last of the flags wins, as on NetBird's own command line
// (--setup-key OLD -k NEW uses NEW).
function flag(text: string, names: string[]): string | undefined {
  let last: { at: number; v: string } | undefined;
  for (const n of names) {
    for (const m of text.matchAll(new RegExp(`(?:^|\\s)${n}(?:=|\\s+)(["']?)([^\\s"']+)\\1`, "g"))) {
      if (!last || (m.index ?? 0) > last.at) last = { at: m.index ?? 0, v: m[2] };
    }
  }
  return last?.v;
}

// null: the text is not a command (a plain key, or anything else, stays
// as typed).
export function parseNetbirdKey(text: string): PastedKey | null {
  const t = text.trim();
  if (!/(^|\s)(--setup-key|-k)(=|\s)/.test(t)) return null;
  const key = flag(t, ["--setup-key", "-k"]);
  if (!key) return null;
  const management = flag(t, ["--management-url", "-m"]);
  return { key, ...(management ? { management } : {}) };
}
