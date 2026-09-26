// Package mcpprompt holds the one text that tells an LLM how to use the
// ssh-tool MCP bridge. The bridge sends it as the MCP `instructions` field
// on every connect, and the app's "Copy system prompt" buttons copy the
// same bytes for clients that ignore server instructions. Edit prompt.md;
// there is no other copy to keep in sync.
//
// It is NOT a security control. Anything here is text the model may ignore,
// and host output that tries to talk its way past it is doing exactly that.
// The real boundaries are structural and live in the app: the per-command
// approval modal, the read / read-run / read-run-yolo grant split, the
// dangerous-command check, and commit_plan's approval step. Never relax one
// of those on the grounds that this text warns about it.
package mcpprompt

import (
	_ "embed"
	"strings"
)

//go:embed prompt.md
var text string

// Text returns the prompt.
func Text() string { return strings.TrimSpace(text) }
