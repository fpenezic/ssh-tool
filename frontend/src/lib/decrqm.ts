// Strips DECRQM / DECRPM queries out of a PTY byte stream.
//
//   ESC [ ? <params> $ p      (DECRQM)
//   ESC [ ? <params> $ y      (DECRPM, less common but same shape)
//
// The 8-bit CSI introducer (0x9b) is handled too.
//
// Reason for the strip: xterm 6.x has an open bug in its requestMode
// handler that throws when certain params land. The throw fires from
// inside an async parser callback, so a try/catch around term.write does
// NOT catch it - it kills the parser mid-stream, and the rest of the
// chunk lands as garbage (overlapping rows, characters swallowed into a
// half-parsed sequence). The sequence is only a feature-detection query;
// dropping it makes the remote fall back to safe defaults.
//
// Why this is a stateful scanner and not a pure function:
//
// The PTY pump reads fixed 8 KiB blocks, so a sequence is split across
// two chunks whenever one happens to straddle the boundary. A stateless
// strip sees an unterminated sequence at the end of chunk A, gives up,
// and emits it verbatim; chunk B then starts mid-sequence and its tail
// ("2026$p") passes through as ordinary text. The net effect is the
// exact corruption the strip exists to prevent, and it is WORSE than no
// strip at all because it only misfires under load.
//
// Measured on "HELLO\x1b[?2026$pWORLD": 8 of 18 possible split points
// leaked the sequence through intact.
//
// Claude Code and other TUIs poll DECRQM 2026 (synchronized output)
// continuously, so a long-lived session hits a bad boundary routinely
// rather than rarely.
//
// A scanner therefore holds any partial sequence back and resumes on the
// next chunk. Callers must keep one instance per session for the life of
// that stream.

// Largest partial sequence to hold. A real DECRQM is under a dozen
// bytes; the cap bounds how much a malformed or hostile stream can make
// us buffer, and how long output can be withheld waiting for a
// terminator that never comes.
const MAX_PARTIAL = 64;

type ScanState =
  // Not inside a candidate sequence.
  | { kind: "idle" }
  // Saw ESC at the very end of a chunk; need the next byte to know
  // whether it is a CSI introducer.
  | { kind: "esc" }
  // Inside "CSI ? <params>", collecting digits and semicolons while
  // looking for the "$p" / "$y" terminator. `bytes` is everything from
  // the introducer onward, replayed verbatim if this turns out not to
  // be a DECRQM after all.
  | { kind: "params"; bytes: number[] }
  // Saw "$" and need one more byte to tell a terminator from a false
  // alarm.
  | { kind: "dollar"; bytes: number[] };

/** DecrqmStripper removes DECRQM/DECRPM queries from a byte stream,
 *  correctly handling sequences split across chunk boundaries.
 *
 *  One instance per stream. `push` returns the bytes safe to write now;
 *  a trailing partial sequence is withheld until the next `push`
 *  resolves it.
 */
export class DecrqmStripper {
  private state: ScanState = { kind: "idle" };

  push(data: Uint8Array): Uint8Array {
    // Fast path: no carry-over and no introducer anywhere means there is
    // nothing this scanner can act on. Avoids a copy per chunk for the
    // overwhelming majority of terminal output.
    if (this.state.kind === "idle") {
      let found = false;
      for (let i = 0; i < data.length; i++) {
        const b = data[i];
        if (b === 0x1b || b === 0x9b) { found = true; break; }
      }
      if (!found) return data;
    }

    // Worst case every carried byte plus every input byte is emitted.
    const carried = this.state.kind === "idle" || this.state.kind === "esc"
      ? (this.state.kind === "esc" ? 1 : 0)
      : this.state.bytes.length;
    const out = new Uint8Array(carried + data.length);
    let oi = 0;

    const emit = (b: number) => { out[oi++] = b; };
    const emitAll = (bs: number[]) => { for (const b of bs) out[oi++] = b; };

    for (let i = 0; i < data.length; i++) {
      const b = data[i];

      switch (this.state.kind) {
        case "idle":
          if (b === 0x1b) {
            // Defer: only "ESC [" starts a CSI, and the "[" may be in
            // the next chunk.
            this.state = { kind: "esc" };
          } else if (b === 0x9b) {
            this.state = { kind: "params", bytes: [b] };
          } else {
            emit(b);
          }
          break;

        case "esc":
          if (b === 0x5b /* [ */) {
            this.state = { kind: "params", bytes: [0x1b, b] };
          } else {
            // Not a CSI. Replay the ESC and re-handle this byte from
            // idle, so an "ESC ESC" pair is not swallowed.
            emit(0x1b);
            this.state = { kind: "idle" };
            i--;
          }
          break;

        case "params": {
          const bytes = this.state.bytes;
          // The introducer must be followed by "?" for DECRQM/DECRPM.
          // bytes holds the introducer (1 or 2 bytes) and nothing else
          // at this point iff we have not yet seen the "?".
          const seenQuestion = bytes.some((x) => x === 0x3f);
          if (!seenQuestion) {
            if (b === 0x3f /* ? */) {
              bytes.push(b);
            } else {
              // Some other CSI - not ours. Replay and re-handle.
              emitAll(bytes);
              this.state = { kind: "idle" };
              i--;
            }
            break;
          }
          if (b === 0x24 /* $ */) {
            bytes.push(b);
            this.state = { kind: "dollar", bytes };
            break;
          }
          if ((b >= 0x30 && b <= 0x39) || b === 0x3b /* digits, ; */) {
            bytes.push(b);
            if (bytes.length > MAX_PARTIAL) {
              // Malformed or not actually a DECRQM. Give the bytes back
              // rather than buffering without bound.
              emitAll(bytes);
              this.state = { kind: "idle" };
            }
            break;
          }
          // Any other byte means this CSI is not a DECRQM.
          emitAll(bytes);
          this.state = { kind: "idle" };
          i--;
          break;
        }

        case "dollar": {
          const bytes = this.state.bytes;
          if (b === 0x70 /* p */ || b === 0x79 /* y */) {
            // Complete DECRQM/DECRPM - drop the whole thing.
            this.state = { kind: "idle" };
          } else {
            // "$" was not a terminator after all.
            emitAll(bytes);
            this.state = { kind: "idle" };
            i--;
          }
          break;
        }
      }
    }

    return out.subarray(0, oi);
  }

  /** flush releases any partial sequence still held, for when the stream
   *  ends (or a caller needs to guarantee nothing is withheld). Leaves
   *  the scanner ready for reuse.
   */
  flush(): Uint8Array {
    const s = this.state;
    this.state = { kind: "idle" };
    if (s.kind === "idle") return new Uint8Array(0);
    if (s.kind === "esc") return new Uint8Array([0x1b]);
    return new Uint8Array(s.bytes);
  }
}

/** stripSnapshot removes DECRQM/DECRPM from a self-contained buffer (a
 *  scrollback replay), where there is no "next chunk" to complete a
 *  partial sequence. Anything still pending at the end is emitted rather
 *  than dropped, so no byte is lost.
 *
 *  Do NOT use this on a live stream: each call starts fresh, which is
 *  exactly the boundary bug this module exists to fix.
 */
export function stripSnapshot(data: Uint8Array): Uint8Array {
  const s = new DecrqmStripper();
  const head = s.push(data);
  const tail = s.flush();
  if (tail.length === 0) return head;
  const out = new Uint8Array(head.length + tail.length);
  out.set(head, 0);
  out.set(tail, head.length);
  return out;
}
