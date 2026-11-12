package main

import (
	"fmt"
	"unicode/utf8"
)

// mcpTypeTextOK refuses text that type_into_terminal must not send. The tool
// promises the text lands at the prompt unsubmitted, and under the YOLO grant
// that review step is the only control (no approval, no IsDangerous check). A
// CR or LF in the text would press Enter for the user; other C0 bytes, DEL,
// ESC and C1 code points can edit or replace the line (Ctrl-U, Ctrl-W, escape
// sequences). Tab is allowed: it completes, it does not submit. Invalid UTF-8
// is refused so raw C1 bytes cannot slip past the rune check.
func mcpTypeTextOK(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("text is not valid UTF-8")
	}
	for _, r := range text {
		if (r < 0x20 && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return fmt.Errorf("text must not contain newlines or control characters (found U+%04X); type_into_terminal never presses Enter - use run for a command that should execute", r)
		}
	}
	return nil
}
