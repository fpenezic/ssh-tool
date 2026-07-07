package main

import "testing"

func TestWindowsPathToWSL(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Users\Administrator\Desktop\ssh-tool.exe`, "/mnt/c/Users/Administrator/Desktop/ssh-tool.exe"},
		{`D:\tools\ssh-tool.exe`, "/mnt/d/tools/ssh-tool.exe"},
		{`C:/already/forward.exe`, "/mnt/c/already/forward.exe"},
		{`/usr/local/bin/ssh-tool`, ""}, // not a drive path
		{`relative\path.exe`, ""},
		{``, ""},
		{`C:`, ""}, // too short / no separator
	}
	for _, c := range cases {
		if got := windowsPathToWSL(c.in); got != c.want {
			t.Errorf("windowsPathToWSL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
