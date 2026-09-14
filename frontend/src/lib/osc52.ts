// OSC 52 is how a program running inside the terminal puts text on the
// user's clipboard:
//
//   ESC ] 52 ; <targets> ; <base64 payload> BEL
//
// It is the only way a process on the far side of a PTY can reach the
// local clipboard - there is no other channel - so TUIs rely on it for
// "press c to copy". Claude Code's login prompt is one: without a
// handler it reports "copied" (it sent the sequence successfully) while
// nothing reaches the clipboard.
//
// <targets> selects which selection to write: "c" clipboard, "p" primary,
// "s" select, plus cut-buffers 0-7. We treat every write as a clipboard
// write; X11's primary selection has no browser equivalent.
//
// READS ARE DELIBERATELY NOT SUPPORTED. A payload of "?" asks the
// terminal to send the clipboard CONTENTS BACK to the process. That
// would let any command on any host the user is connected to exfiltrate
// whatever is on their clipboard - frequently a password, since this app
// has a copy-password button. Most emulators disable it by default for
// exactly this reason. parseOsc52 returns null for it, and the caller
// still marks the sequence handled so nothing is echoed to the screen.

export type Osc52Result =
  | { kind: "write"; text: string }
  // A read request ("?"), which we refuse. Distinguished from a parse
  // failure so the caller can still swallow the sequence.
  | { kind: "read" };

// Guard against a hostile or broken stream pushing a huge string into
// the clipboard. 1 MiB of base64 is far beyond any legitimate "copy this
// URL" use and well under anything that would stall the UI.
const MAX_B64 = 1024 * 1024;

/** parseOsc52 interprets an OSC 52 payload (everything between "52;" and
 *  the terminator, which xterm has already stripped).
 *
 *  Returns null when the payload is malformed, empty, or not decodable -
 *  callers should then do nothing.
 */
export function parseOsc52(payload: string): Osc52Result | null {
  // Split targets from data on the FIRST ";" only: base64 never contains
  // one, but splitting on all of them would corrupt a payload that does.
  const sep = payload.indexOf(";");
  if (sep < 0) return null;

  const data = payload.slice(sep + 1);
  if (data === "?") return { kind: "read" };
  if (data === "") return null;
  if (data.length > MAX_B64) return null;

  let text: string;
  try {
    text = decodeBase64Utf8(data);
  } catch {
    return null;
  }
  if (text === "") return null;
  return { kind: "write", text };
}

// atob yields one char per BYTE, so a UTF-8 payload (a path with
// diacritics, a CJK string) comes back mojibake unless the bytes are
// decoded as UTF-8. Round-trip through TextDecoder to get that right.
function decodeBase64Utf8(b64: string): string {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  // fatal:false so a stray invalid byte degrades to U+FFFD rather than
  // losing the whole copy.
  return new TextDecoder("utf-8").decode(bytes);
}
