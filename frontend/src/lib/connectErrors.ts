// Maps raw Go/SSH error strings to plain-English explanations.
//
// The Connect button's failure path lands here. We look for known
// substrings in the raw error (case-insensitive), pick the friendliest
// rendering, and let the caller surface the raw text behind a toggle
// for the cases where the user (or the developer debugging) needs it.
//
// Order matters: more specific patterns first. The first match wins.

export interface FriendlyError {
  // Short one-liner, suitable for the red banner.
  summary: string;
  // Optional hint with possible causes / next steps. Rendered on a
  // second line in lighter text.
  hint?: string;
}

interface Rule {
  // Substring to look for inside the raw error (lower-cased compare).
  match: string | RegExp;
  // Either a static summary/hint or a function that builds one from
  // the raw text (for cases where the message wants to quote a host
  // or auth method back to the user).
  to: FriendlyError | ((raw: string) => FriendlyError);
}

// Try to lift the "jump bastion1: …" or "target host: …" prefix from
// the raw error and prepend "Failed at <hop>: " to the human summary.
// The backend wraps every hop error with `fmt.Errorf("%s: %w", h.Label, …)`,
// where Label is "jump <hostname>" or "target <hostname>".
function hopPrefix(raw: string): { hop: string; rest: string } | null {
  const m = /^(jump|target)\s+(\S+):\s*(.+)$/i.exec(raw);
  if (!m) return null;
  const kind = m[1].toLowerCase();
  const hostname = m[2];
  const rest = m[3];
  const hop = kind === "target" ? `target host (${hostname})` : `jump host (${hostname})`;
  return { hop, rest };
}

function withHop(raw: string, f: FriendlyError): FriendlyError {
  const p = hopPrefix(raw);
  if (!p) return f;
  return {
    summary: `${f.summary} at ${p.hop}`,
    hint: f.hint,
  };
}

const RULES: Rule[] = [
  {
    match: "no route to host",
    to: {
      summary: "Host unreachable",
      hint: "No network route to the target. Check VPN / firewall / wrong IP.",
    },
  },
  {
    match: "connection refused",
    to: {
      summary: "Connection refused",
      hint: "Reached the host, but nothing was listening on the SSH port. Wrong port or sshd not running?",
    },
  },
  {
    match: "i/o timeout",
    to: {
      summary: "Connection timed out",
      hint: "The host didn't respond. It might be down, behind a firewall, or the network is slow.",
    },
  },
  {
    match: "no such host",
    to: {
      summary: "Hostname could not be resolved",
      hint: "DNS lookup failed. Typo in the hostname, missing /etc/hosts entry, or VPN not connected?",
    },
  },
  {
    match: "unable to authenticate",
    to: (raw) => {
      // Go's SSH library tacks on the attempted methods list.
      const m = /attempted methods \[([^\]]+)\]/i.exec(raw);
      const methods = m ? m[1] : "";
      return {
        summary: "Authentication failed",
        hint: methods
          ? `The server rejected every method we tried (${methods}). Wrong password / key / username?`
          : "Wrong password, wrong key, or the server doesn't accept the credential type configured.",
      };
    },
  },
  {
    match: "ssh: handshake failed",
    to: {
      summary: "SSH handshake failed",
      hint: "Reached the host on the SSH port but the protocol negotiation failed. Server may not be sshd, or there's a version mismatch.",
    },
  },
  {
    match: "host key mismatch",
    to: {
      summary: "Host key mismatch",
      hint: "The server's host key doesn't match the one we trusted before. Could be a server reinstall, a port reuse - or a MITM. Review under Settings → Known hosts.",
    },
  },
  {
    match: "ssh: rejected: administratively prohibited",
    to: {
      summary: "Channel rejected by server",
      hint: "The server refused to open the requested channel (shell / port-forward). Check sshd config - AllowTcpForwarding, PermitOpen, ForceCommand.",
    },
  },
  {
    match: "permission denied",
    to: {
      summary: "Permission denied",
      hint: "Authentication succeeded but the server denied something - usually a forbidden command or restricted shell.",
    },
  },
  {
    match: "context canceled",
    to: {
      summary: "Connection cancelled",
      hint: "The connect attempt was aborted. Usually means the app closed the session before authentication finished.",
    },
  },
  {
    match: "context deadline exceeded",
    to: {
      summary: "Connection timed out",
      hint: "Took too long to connect. Raise the timeout in Settings, or check whether the host is reachable.",
    },
  },
];

// Wails v3 wraps backend errors as JSON: {"message":"…","cause":…,"kind":"RuntimeError"}.
// Peel the .message back out so substring rules + the hop prefix
// regex see the plain Go error string they expect.
export function unwrapRaw(raw: string): string {
  return unwrap(raw);
}

// errMsg turns whatever a Wails IPC rejection throws into a plain,
// human-readable string. The reject value may be:
//   - an Error/object whose .message is itself the JSON envelope
//     {"message":"...","cause":{},"kind":"RuntimeError"}
//   - a bare JSON-envelope string
//   - a plain string
// In every case we peel back to the Go error's own message. Use this at
// catch sites instead of `e?.message ?? String(e)`, which leaks the raw
// JSON to the UI.
export function errMsg(e: unknown): string {
  if (e == null) return "Unknown error";
  if (typeof e === "string") return unwrap(e);
  if (typeof e === "object") {
    const m = (e as { message?: unknown }).message;
    if (typeof m === "string") return unwrap(m);
  }
  return unwrap(String(e));
}
function unwrap(raw: string): string {
  const s = (raw ?? "").trim();
  if (!s.startsWith("{")) return s;
  try {
    const o = JSON.parse(s);
    if (o && typeof o.message === "string") return o.message;
  } catch { /* not JSON - fall through */ }
  return s;
}

export function explain(raw: string): FriendlyError {
  const r = unwrap((raw ?? "").toString());
  const lower = r.toLowerCase();
  for (const rule of RULES) {
    let hit = false;
    if (typeof rule.match === "string") {
      hit = lower.includes(rule.match);
    } else {
      hit = rule.match.test(r);
    }
    if (hit) {
      const f = typeof rule.to === "function" ? rule.to(r) : rule.to;
      return withHop(r, f);
    }
  }
  // Unknown error - surface the raw line as the summary so the user at
  // least sees what's wrong.
  return { summary: r || "Connect failed" };
}
