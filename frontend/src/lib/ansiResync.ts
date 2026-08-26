/**
 * Find a safe point to start writing a truncated terminal stream.
 *
 * The backend scrollback ring is capped and trims from the front on a line
 * boundary. That is right for ordinary shell output, but a full-screen TUI
 * (Claude Code, htop, vim) does not write lines - it writes escape sequences
 * that move the cursor, switch to the alternate screen and set scroll
 * regions. Cutting such a stream at "the first \n" lands in the middle of a
 * sequence as often as not.
 *
 * Replaying that into a freshly reset xterm feeds it half an escape: the
 * parser consumes the remainder as parameters or text, and the screen ends up
 * with content landing mid-row and box-drawing broken apart - the reported
 * corruption, which showed up on a tab left in the background long enough for
 * its scrollback to be dropped and later replayed.
 *
 * So: skip forward past any partial sequence at the head of the buffer.
 */

/** Bytes that can begin an escape sequence. */
const ESC = 0x1b;
const CSI_8BIT = 0x9b;
const OSC_8BIT = 0x9d;

/**
 * Return the offset of the first byte that is safe to write, i.e. the start
 * of a complete sequence or of plain text - never the middle of one.
 *
 * Conservative by design: when the head looks like it could be the tail of a
 * sequence, drop bytes until something unambiguous is found. Losing a few
 * bytes off the top of a replay is invisible; writing a half sequence is not.
 */
export function safeResyncOffset(data: Uint8Array): number {
  if (data.length === 0) return 0;

  // A buffer that starts with ESC (or an 8-bit introducer) is already at a
  // sequence boundary - nothing to skip.
  const first = data[0];
  if (first === ESC || first === CSI_8BIT || first === OSC_8BIT) return 0;

  // Only two shapes are worth acting on, because only they are unambiguous.
  // Everything else stays untouched: eating real output is a worse failure
  // than replaying one stray escape, and most bytes that COULD terminate a
  // sequence (letters, punctuation) are overwhelmingly just text.
  //
  // 1. A BEL within the first few bytes: the tail of a chopped OSC string
  //    ("\x1b]0;title\x07"). Plain output essentially never contains BEL.
  const bellWindow = Math.min(data.length, 64);
  for (let i = 0; i < bellWindow; i++) {
    const b = data[i];
    if (b === ESC || b === CSI_8BIT || b === OSC_8BIT) break;
    if (b === 0x07) return i + 1;
    // A newline ends the candidate region - past it we are certainly in
    // ordinary output.
    if (b === 0x0a) break;
  }

  // 2. A run of CSI parameter/intermediate bytes followed by a final byte,
  //    with nothing else before it: the tail of a chopped control sequence
  //    ("\x1b[12;40H" cut to "12;40H"). Requiring the ENTIRE head to be
  //    parameter bytes is what keeps ordinary text safe - "hello" fails at
  //    'h', "2026-08-26 log" fails at the space.
  const end = findSequenceTail(data);
  return end;
}

/**
 * If the buffer opens with the tail of a control sequence, return the offset
 * just past its final byte; otherwise 0.
 *
 * Requires at least one parameter/intermediate byte before the final one. A
 * bare final byte at offset 0 is also accepted, since "\x1b[2J" chopped to
 * "J" is exactly the reported case - but only when the rest of the buffer
 * does not read as ordinary text starting with that letter, which is why the
 * caller limits this to a chopped-replay context.
 */
function findSequenceTail(data: Uint8Array): number {
  const limit = Math.min(data.length, 32);
  for (let i = 0; i < limit; i++) {
    const b = data[i];
    if (b >= 0x40 && b <= 0x7e) {
      // Final byte. Only treat it as a sequence tail when everything before
      // it was parameter/intermediate AND there was at least one such byte -
      // a lone letter at position 0 is far more likely to be text.
      return i > 0 && looksLikeSequenceBody(data, i) ? i + 1 : 0;
    }
    // 0x20 (space) is technically an intermediate byte, but in practice a
    // space this early means ordinary text - "2026-08-26 log line" would
    // otherwise read as a sequence body all the way to the 'l'.
    if (b === 0x20) return 0;
    const isParam = b >= 0x30 && b <= 0x3f;
    const isIntermediate = b >= 0x21 && b <= 0x2f;
    if (!isParam && !isIntermediate) return 0;
  }
  return 0;
}

/**
 * Whether every byte before index `end` could be the parameter/intermediate
 * body of a control sequence. Empty (end === 0) counts as yes - a lone final
 * byte at position 0 is exactly what a chopped "ESC [ 2 J" leaves behind.
 */
function looksLikeSequenceBody(data: Uint8Array, end: number): boolean {
  for (let i = 0; i < end; i++) {
    const b = data[i];
    const isParam = b >= 0x30 && b <= 0x3f;
    const isIntermediate = b >= 0x20 && b <= 0x2f;
    if (!isParam && !isIntermediate) return false;
  }
  return true;
}

/**
 * Trim a truncated stream to a safe starting point.
 *
 * Returns the input unchanged when it already starts cleanly, so the common
 * case costs one comparison.
 */
export function resyncAnsi(data: Uint8Array): Uint8Array {
  const off = safeResyncOffset(data);
  return off === 0 ? data : data.subarray(off);
}
