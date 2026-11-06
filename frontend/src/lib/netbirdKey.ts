// A NetBird setup key as people paste it: the key alone, or the whole
// command the dashboard's "Add peer" shows
// (netbird up --management-url https://vpn.example.com --setup-key KEY).
// From the command the key is taken, and the management URL with it.

export interface PastedKey {
  key: string;
  management?: string;
}

function flag(text: string, names: string[]): string | undefined {
  for (const n of names) {
    const m = text.match(new RegExp(`(?:^|\\s)${n}(?:=|\\s+)(["']?)([^\\s"']+)\\1`));
    if (m) return m[2];
  }
  return undefined;
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
