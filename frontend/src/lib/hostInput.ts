// Split what someone types or pastes as a host ("[user@]host[:port]") into
// its parts, so "db.example.com:2222" lands in Hostname + Port instead of
// becoming a hostname SSH cannot dial. A bare IPv6 address has colons but
// no port; one with a port is written "[2001:db8::1]:2222".
import type { InheritableSettings } from "./api";

export interface HostInput {
  host: string;
  port?: number;
  user?: string;
}

export function splitHostInput(raw: string): HostInput {
  let s = raw.trim();
  let user: string | undefined;
  const at = s.lastIndexOf("@");
  if (at > 0) {
    user = s.slice(0, at);
    s = s.slice(at + 1);
  }
  let host = s;
  let port: number | undefined;
  const bracket = /^\[([^\]]+)\](?::(\d+))?$/.exec(s);
  if (bracket) {
    host = bracket[1];
    if (bracket[2] !== undefined) port = Number(bracket[2]);
  } else if (s.indexOf(":") === s.lastIndexOf(":")) {
    // One colon at most: host:port. More is a bare IPv6 address.
    const m = /^(.*):(\d+)$/.exec(s);
    if (m && m[1] !== "") {
      host = m[1];
      port = Number(m[2]);
    }
  }
  if (port !== undefined && !(port >= 1 && port <= 65535)) {
    // Not a port: keep the text as typed.
    return { host: raw.trim() };
  }
  return { host, port, user };
}

// The overrides a new connection takes from what was typed: the port and
// user when given, nothing otherwise (so the folder's values inherit).
export function hostOverrides(t: HostInput): InheritableSettings | undefined {
  if (t.port === undefined && t.user === undefined) return undefined;
  const o: InheritableSettings = {};
  if (t.port !== undefined) o.port = t.port;
  if (t.user !== undefined) o.username = t.user;
  return o;
}
