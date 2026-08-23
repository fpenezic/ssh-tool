// Shared presentation for an MCP grant level. The robot indicator appears in
// two places (the pane header button and the collapsed tab badge) and both must
// agree on what a colour means, so the tooltip text lives here rather than
// being spelled out at each site.
//
// Colour mapping (CSS side, keyed off the same strings):
//   ""              not shared      - icon hidden
//   "read"          blue            - scrollback + allowlisted reads
//   "read-run"      yellow          - adds exec/type, every command gated
//   "read-run-yolo" red             - auto-runs; only catastrophic commands prompt
// Severity order, loudest last. A counter that stands for several sessions
// shows the loudest grant among them: seeing "3" in calm blue while one of
// those three is auto-running commands would be the wrong summary.
const LEVEL_RANK: Record<string, number> = {
  "": 0,
  read: 1,
  "read-run": 2,
  "read-run-yolo": 3,
};

// Returns whichever of the two levels is louder. Unknown strings rank 0 so a
// level this build does not recognise never masks one it does.
export function loudestMcpLevel(a: string, b: string): string {
  return (LEVEL_RANK[b] ?? 0) > (LEVEL_RANK[a] ?? 0) ? b : a;
}

export function mcpLevelTitle(level: string): string {
  switch (level) {
    case "read":
      return "Shared with an LLM: read only";
    case "read-run":
      return "Shared with an LLM: read + run (each command needs approval)";
    case "read-run-yolo":
      return "Shared with an LLM: AUTO-RUN (YOLO) - commands run without approval";
    default:
      return "Share this session with an LLM (MCP)";
  }
}

// Tooltip for a counter that stands for n sessions, coloured by the loudest
// grant among them. Distinct from mcpLevelTitle, which describes exactly one
// session's own grant.
export function mcpCounterTitle(n: number, level: string): string {
  const plural = `${n} session${n === 1 ? "" : "s"} shared with an LLM`;
  switch (level) {
    case "read-run":
      return `${plural} - read + run, each command needs approval - click for activity`;
    case "read-run-yolo":
      return `${plural} - AUTO-RUN (YOLO), commands run without approval - click for activity`;
    default:
      return `${plural} - click for activity`;
  }
}
