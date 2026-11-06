// A NetBird setup key as people paste it: the key alone, or the whole
// command the dashboard's "Add peer" shows
// (netbird up --management-url https://vpn.example.com --setup-key KEY).
// From the command the key is taken, and the management URL with it.

export interface PastedKey {
  key: string;
  management?: string;
}

// The command without a shell comment: from a # that starts a word,
// outside quotes, to the end of the line.
function uncomment(text: string): string {
  let q = "";
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (q) {
      if (c === q) q = "";
    } else if (c === '"' || c === "'") {
      q = c;
    } else if (c === "\\") {
      i++;
    } else if (c === "#" && (i === 0 || /\s/.test(text[i - 1]))) {
      return text.slice(0, i);
    }
  }
  return text;
}

// The last of the flags wins, as on NetBird's own command line
// (--setup-key OLD -k NEW uses NEW); a last flag with no value gives "",
// not an earlier value.
function flag(text: string, names: string[]): string | undefined {
  let last: { at: number; v: string } | undefined;
  for (const n of names) {
    for (const m of text.matchAll(new RegExp(`(?:^|\\s)${n}(?:=(["']?)([^\\s"']*)\\1|\\s+(["']?)([^\\s"']*)\\3|(?=\\s|$))`, "g"))) {
      let v = m[2] ?? m[4] ?? "";
      if (v.startsWith("-")) v = "";
      if (!last || (m.index ?? 0) > last.at) last = { at: m.index ?? 0, v };
    }
  }
  return last?.v;
}

// null: the text is not a command (a plain key, or anything else, stays
// as typed).
export function parseNetbirdKey(text: string): PastedKey | null {
  const t = uncomment(text).trim();
  if (!/(^|\s)(--setup-key|-k)(=|\s)/.test(t)) return null;
  const key = flag(t, ["--setup-key", "-k"]);
  if (!key) return null;
  const management = flag(t, ["--management-url", "-m"]);
  return { key, ...(management ? { management } : {}) };
}
