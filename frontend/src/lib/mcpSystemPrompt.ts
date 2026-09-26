// The "Copy system prompt" text is NOT kept here: it is the same text the MCP
// bridge sends as its instructions, embedded in the binary from
// internal/mcpprompt/prompt.md and fetched with api.mcpSystemPrompt(), so
// the two can never drift apart. Only the where-to-paste hint lives here.

export const MCP_SYSTEM_PROMPT_HINT =
  "Paste it where your client keeps instructions - Claude Desktop: Project -> " +
  "Instructions (then chat inside that project); Claude Code: ~/.claude/CLAUDE.md " +
  "or a project CLAUDE.md; others: the system-prompt field. Pasting it as a chat " +
  "message may not take effect.";
