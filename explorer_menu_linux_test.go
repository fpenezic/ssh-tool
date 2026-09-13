//go:build linux && !android

package main

import "testing"

// The generated script has quoted strings on several lines before the
// one that runs the binary, so finding "the first quoted thing" is not
// enough - it has to be the exec line.
func TestScriptTarget(t *testing.T) {
	script := `#!/bin/sh
# Installed by ssh-tool (Settings -> Integration). Opens the selected
# directory as a local shell tab in ssh-tool.
dir="$(printf '%s' "$NAUTILUS_SCRIPT_SELECTED_FILE_PATHS" | head -n1)"
[ -z "$dir" ] && dir="$PWD"
exec "/home/u/.local/bin/ssh-tool" --open-dir "$dir"
`
	if got := scriptTarget(script); got != "/home/u/.local/bin/ssh-tool" {
		t.Errorf("got %q, want the exec target", got)
	}
}

func TestScriptTargetHandlesSpaces(t *testing.T) {
	script := "#!/bin/sh\nexec \"/home/u/My Apps/ssh-tool\" --open-dir \"$dir\"\n"
	if got := scriptTarget(script); got != "/home/u/My Apps/ssh-tool" {
		t.Errorf("got %q, want the path with a space intact", got)
	}
}

func TestScriptTargetRejectsNonsense(t *testing.T) {
	for _, body := range []string{"", "#!/bin/sh\n", "#!/bin/sh\necho hi\n"} {
		if got := scriptTarget(body); got != "" {
			t.Errorf("body %q: got %q, want empty", body, got)
		}
	}
}
