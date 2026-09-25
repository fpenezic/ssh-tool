// Command timestamps: helpers kept free of xterm so they can be tested.

import type { CommandMark } from "./api";

export interface Segment {
  bytes: Uint8Array;
  // Set when a command was run at the END of this segment: the cursor is on
  // its command line once these bytes have been written.
  mark?: CommandMark;
}

// splitAtMarks cuts a replayed snapshot at each command mark. `endCum` is
// the output count at the snapshot's last byte, so the first byte sits at
// endCum - data.length. Marks outside the snapshot are ignored; two marks
// at one position (Enter pressed twice before any output) both land on the
// same line, so only the later one is kept.
export function splitAtMarks(data: Uint8Array, endCum: number, marks: CommandMark[] | null | undefined): Segment[] {
  const base = endCum - data.length;
  const inside = (marks ?? [])
    .filter((m) => m.cum >= base && m.cum <= endCum)
    .sort((a, b) => a.cum - b.cum);
  const out: Segment[] = [];
  let pos = 0;
  for (const m of inside) {
    const off = m.cum - base;
    if (off === pos && out.length > 0) {
      // Same position as the previous mark: same line, later time wins.
      out[out.length - 1].mark = m;
      continue;
    }
    out.push({ bytes: data.subarray(pos, off), mark: m });
    pos = off;
  }
  if (pos < data.length || out.length === 0) out.push({ bytes: data.subarray(pos) });
  return out;
}

// The terminal state that tells a shell prompt from a program running in
// the terminal. A subset of xterm's IModes plus the active buffer.
export interface TermState {
  altScreen: boolean;
  sendFocusMode: boolean;
  mouseTrackingMode: string;
}

// isCommandInput: does this keyboard input run a shell command line? Enter
// (CR) at a prompt. Not when a full-screen program owns the terminal:
//  - the alternate screen (vim, htop, less, nmtui);
//  - focus reporting (?1004) or mouse tracking switched on. TUIs that draw
//    in the normal buffer (Claude Code, other Ink apps) turn focus
//    reporting on, and a shell does not - measured, bash and a plain sh
//    both leave it off while Claude Code turns it on.
export function isCommandInput(data: string, st: TermState): boolean {
  if (!data.includes("\r")) return false;
  if (st.altScreen || st.sendFocusMode) return false;
  return st.mouseTrackingMode === "none";
}

export function fmtStamp(at: number): string {
  const d = new Date(at);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// blockRange: the buffer lines [start, end] (inclusive) of command idx -
// its command line through the line before the next command, or for the
// newest command, through the line before the cursor (the prompt waiting
// now). `lines` are the command lines, ascending. Never empty: a command
// whose output has not started yet still has its own line.
export function blockRange(lines: number[], idx: number, cursorLine: number): [number, number] {
  const start = lines[idx];
  const next = idx + 1 < lines.length ? lines[idx + 1] : cursorLine;
  return [start, Math.max(start, next - 1)];
}
