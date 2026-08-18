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
