/**
 * Whether a terminal's scrollback may be released while its tab sits in the
 * background, and whether a release may be undone by reset-and-replay.
 *
 * Releasing works by resetting xterm and replaying the backend ring. That
 * ring is a byte HISTORY, not a screen state, which is fine for line-based
 * output but wrong for a full-screen TUI: the ring is capped and trims from
 * the front, so the `?1049h` that entered the alternate screen scrolls out of
 * it, and the replay then runs the TUI's cursor positioning against the
 * normal buffer. The screen comes apart.
 *
 * The rule is therefore simple, and cheap: leave the alternate screen alone.
 * A TUI keeps no scrollback of its own - that is what the alternate screen
 * is - so the memory this feature reclaims is zero in exactly the case that
 * breaks.
 */

export type BufferType = "normal" | "alternate";

/** Whether the scrollback may be dropped for a backgrounded terminal. */
export function mayDropScrollback(buffer: BufferType | undefined): boolean {
  return buffer !== "alternate";
}

/**
 * Whether a restore may reset the terminal and replay the ring.
 *
 * False when the terminal is now in the alternate screen: a TUI can start
 * while the tab is backgrounded, so a drop that was legitimate at the time
 * can be followed by a restore that is not. The caller re-arms the buffer
 * size without the replay.
 */
export function mayReplayOnRestore(buffer: BufferType | undefined): boolean {
  return buffer !== "alternate";
}


/**
 * Whether dropping the scrollback is worth it at all for this terminal.
 *
 * The drop is only ever undone by replaying the backend ring, and that ring
 * is capped in BYTES (1 MB) while xterm's scrollback is capped in LINES
 * (5000 by default). For plain output the ring comfortably covers the line
 * budget, so the round trip is lossless. For dense output - a TUI's colour,
 * box-drawing and cursor positioning easily runs 3-5x the bytes per rendered
 * line - 1 MB holds far fewer than 5000 lines, and every background round
 * trip silently SHORTENS the user's history.
 *
 * That is invisible until you scroll up and find earlier output missing,
 * which is exactly how it was reported.
 *
 * We cannot count the ring's lines from here, so use the conservative
 * proxy: only drop when the terminal is not holding more history than a
 * 1 MB replay can be relied on to restore.
 */
/**
 * Whether a terminal is accumulating scrollback the user could scroll back to.
 *
 * Measured with the app's own logging, on a real Claude Code session:
 *
 *   drop    terminal=826 lines, ring=1162
 *   restore terminal=70  lines, ring=1162, replaying
 *
 * and, over four minutes on another tab:
 *
 *   terminal=69 lines while the ring grew 303 -> 439 -> 575 -> 711
 *
 * Two things follow, and both killed the earlier line-count comparison:
 *
 * 1. Ring lines are NOT terminal lines. The ring counts newlines in the raw
 *    byte stream; a TUI redrawing in place emits newlines constantly and adds
 *    nothing to the scrollback. Replaying 711 ring lines does not reconstruct
 *    69 screen lines - it replays the redraw history.
 *
 * 2. Checking again at restore time is useless: setting scrollback = 0 makes
 *    xterm evict immediately, so by then the buffer is back to the viewport
 *    height (70) and any guard sees a tiny terminal. The measurement happens
 *    after the damage.
 *
 * So the decision has to be made at drop time on a property that is actually
 * knowable: does this terminal hold history BEYOND its viewport? If it does,
 * dropping risks losing something a replay cannot faithfully rebuild, and the
 * memory is not worth it. If it does not - a TUI redrawing in place, which is
 * the case the memory feature was measured against - there is nothing to lose.
 */
export function terminalHasScrollback(usedLines: number, viewportRows: number): boolean {
  if (viewportRows <= 0) return usedLines > 0;
  return usedLines > viewportRows;
}
