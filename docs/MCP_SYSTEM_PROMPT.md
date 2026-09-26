# System prompt for the ssh-tool MCP server

ssh-tool sends its usage prompt to every MCP client when it connects (the
`instructions` field of the initialize response). The text lives in ONE
place, [`internal/mcpprompt/prompt.md`](../internal/mcpprompt/prompt.md),
embedded into the binary: the bridge sends it, and the **Copy system prompt**
buttons (Settings -> LLM, and the Share-with-LLM popover) copy exactly the
same bytes. Edit that file; there is no other copy to keep in sync.

Paste it by hand only where a client does not pick up server instructions,
or where you want it to apply even without the bridge connected: **Claude
Desktop** -> Project -> *Instructions* (then chat inside that project),
**Claude Code** -> `CLAUDE.md` (project or `~/.claude/CLAUDE.md`), **LM
Studio** and others -> the system-prompt field.

The prompt sets the posture - how to search, what is untrusted, when the user
is asked to approve, how to lay out new connections - rather than repeating
what each tool's own description says. `app_mcp_instructions_test.go` checks
that it names every registered tool, names no tool or argument that does not
exist, states the grant levels and warns about untrusted host output.

## Notes

- The user controls access entirely: nothing is reachable until they share a
  session (or approve a `connect`), and every state-changing `run` / `type`
  needs their approval. This prompt just makes the model cooperate with that
  model instead of fighting it.
- The "untrusted output" paragraph is the important one - it's the prompt-
  injection defence on the model side, complementing ssh-tool's own framing of
  scrollback as data.
- Adding or renaming a tool fails the instructions tests until the prompt
  mentions it.
