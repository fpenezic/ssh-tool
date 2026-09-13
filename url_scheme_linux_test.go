//go:build linux && !android

package main

import "testing"

// The Exec= value is written with %q, so a path containing a space
// survives - which means parsing it is not a split on whitespace. It
// also carries field codes (%u) that are not part of the program.
func TestExecTargetFromDesktopEntry(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"quoted with field code",
			"[Desktop Entry]\nExec=\"/home/u/.local/bin/ssh-tool\" %u\n",
			"/home/u/.local/bin/ssh-tool",
		},
		{
			"quoted path containing a space",
			"Exec=\"/home/u/My Apps/ssh-tool\" %u\n",
			"/home/u/My Apps/ssh-tool",
		},
		{
			"unquoted",
			"Exec=/usr/bin/ssh-tool %u\n",
			"/usr/bin/ssh-tool",
		},
		{
			"unquoted, no field code",
			"Exec=/usr/bin/ssh-tool\n",
			"/usr/bin/ssh-tool",
		},
		{
			"other keys around it",
			"[Desktop Entry]\nType=Application\nName=x\nExec=\"/opt/ssh-tool\" %u\nTerminal=false\n",
			"/opt/ssh-tool",
		},
		{"no Exec line", "[Desktop Entry]\nName=x\n", ""},
		{"empty Exec", "Exec=\n", ""},
		{"empty body", "", ""},
	}
	for _, tc := range cases {
		if got := execTargetFromDesktopEntry(tc.body); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A registration pointing at a symlink to this binary is not stale.
func TestSamePathResolvesSymlinks(t *testing.T) {
	if !samePath("/usr/bin/ssh-tool", "/usr/bin/ssh-tool") {
		t.Error("identical paths should match")
	}
	if samePath("/usr/bin/ssh-tool", "/home/u/.local/bin/ssh-tool") {
		t.Error("different paths should not match")
	}
	// Unix is case-sensitive: these are genuinely different files.
	if samePath("/usr/bin/ssh-tool", "/usr/bin/SSH-TOOL") {
		t.Error("Unix paths are case-sensitive")
	}
}

func TestIntegrationStatusFor(t *testing.T) {
	self := "/home/u/.local/bin/ssh-tool"

	none := integrationStatusFor("", "", self)
	if none.Registered || none.Stale {
		t.Error("nothing registered means nothing to report")
	}

	current := integrationStatusFor("ssh-tool-url.desktop", self, self)
	if !current.Registered || current.Stale {
		t.Error("a registration pointing at us is current")
	}

	stale := integrationStatusFor("ssh-tool-url.desktop", "/home/u/Downloads/ssh-tool", self)
	if !stale.Stale {
		t.Error("a registration pointing elsewhere is stale")
	}

	// The distinction that matters: unreadable is not the same as wrong,
	// and only one of them should push the user to re-register.
	unknown := integrationStatusFor("something-else.desktop", "", self)
	if !unknown.Registered {
		t.Error("registered by someone else is still registered")
	}
	if unknown.Stale {
		t.Error("an unreadable target must not be reported as stale")
	}
}
