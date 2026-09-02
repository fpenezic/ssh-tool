// Shared current-directory state, keyed by session id.
//
// The terminal reports where the shell is via OSC 7 (the same sequence
// terminal emulators use to open a new tab in the same directory), and the
// SFTP pane can follow it. The other direction is explicit: browsing to a
// folder does not move the shell on its own, because that would inject a
// command into whatever the user is typing - the SFTP pane offers a "cd
// here" button instead.
//
// This store is the only thing the two panes share; neither imports the
// other.

class SessionCwd {
  // sessionId -> last directory reported by that session's shell.
  private cwds = $state<Record<string, string>>({});
  // sessionId -> whether the SFTP pane should follow. Off by default: a pane
  // that jumps around while you are working in it is worse than one that
  // stays put, so following is opt-in per pane.
  private following = $state<Record<string, boolean>>({});

  /** report records a directory seen from the shell. Called by the terminal's
   *  OSC 7 handler. */
  report(sessionId: string, dir: string) {
    if (!sessionId || !dir) return;
    if (this.cwds[sessionId] === dir) return;
    this.cwds[sessionId] = dir;
  }

  /** get returns the last known shell directory for a session, or "". */
  get(sessionId: string): string {
    return this.cwds[sessionId] ?? "";
  }

  isFollowing(sessionId: string): boolean {
    return this.following[sessionId] ?? false;
  }

  setFollowing(sessionId: string, on: boolean) {
    this.following[sessionId] = on;
  }

  /** forget drops a session's state when its pane goes away. */
  forget(sessionId: string) {
    delete this.cwds[sessionId];
    delete this.following[sessionId];
  }
}

export const sessionCwd = new SessionCwd();

/** parseOsc7 extracts the path from an OSC 7 payload. The sequence carries a
 *  file:// URL - `file://host/path` - and the path is percent-encoded, so a
 *  directory with a space or a non-ASCII name arrives escaped.
 *
 *  Returns "" for anything that is not a usable local path: a malformed URL,
 *  a non-file scheme, or a host that is not this machine. We do not verify
 *  the host beyond that it parses, because over SSH the reported host is the
 *  remote's own name and checking it against anything here would be wrong.
 */
export function parseOsc7(payload: string): string {
  const raw = payload.trim();
  if (!raw.startsWith("file://")) return "";
  // Strip the scheme, then everything up to the first "/" is the authority.
  const rest = raw.slice("file://".length);
  const slash = rest.indexOf("/");
  if (slash < 0) return "";
  const path = rest.slice(slash);
  try {
    const decoded = decodeURIComponent(path);
    // A shell that emits OSC 7 for a relative path is broken; ignore it
    // rather than resolving it against a guess.
    return decoded.startsWith("/") ? decoded : "";
  } catch {
    // Malformed percent-encoding - better no update than a wrong path.
    return "";
  }
}
