package main

import (
	"testing"

	sshlayer "ssh-tool/internal/ssh"
)

// TestCanRun covers the grant levels that authorise exec + type.
func TestCanRun(t *testing.T) {
	if canRun(mcpGrantNone) || canRun(mcpGrantReadOnly) {
		t.Fatal("none / read-only must not authorise run")
	}
	if !canRun(mcpGrantReadRun) || !canRun(mcpGrantReadRunYolo) {
		t.Fatal("read-run and yolo must authorise run")
	}
}

// TestYoloGateDecision documents the gate mcpRun picks per level, using the same
// two classifiers mcpRun consults (IsReadOnly, IsDangerous). It guards the
// invariant that YOLO auto-runs writes but a catastrophic command still routes
// to the approval prompt.
func TestYoloGateDecision(t *testing.T) {
	// gateFor mirrors the decision in mcpRun (without the live session), so the
	// branch is testable in isolation. Returns "auto" (read-only, any level),
	// "yolo" (auto-approved write under yolo), or "prompt" (needs the modal).
	gateFor := func(lvl mcpGrantLevel, cmd string) string {
		if sshlayer.IsReadOnly(cmd, nil) {
			return "auto"
		}
		if lvl == mcpGrantReadRunYolo && !sshlayer.IsDangerous(cmd) {
			return "yolo"
		}
		return "prompt"
	}

	cases := []struct {
		lvl  mcpGrantLevel
		cmd  string
		want string
	}{
		{mcpGrantReadRun, "cat /etc/hosts", "auto"},        // read-only, gated level
		{mcpGrantReadRunYolo, "cat /etc/hosts", "auto"},    // read-only, yolo
		{mcpGrantReadRun, "systemctl restart nginx", "prompt"},   // write, gated -> modal
		{mcpGrantReadRunYolo, "systemctl restart nginx", "yolo"}, // write, yolo -> auto
		{mcpGrantReadRunYolo, "echo hi > /tmp/x", "yolo"},        // benign write, yolo
		{mcpGrantReadRunYolo, "rm -rf /", "prompt"},              // catastrophic -> modal even in yolo
		{mcpGrantReadRunYolo, "mkfs.ext4 /dev/sda1", "prompt"},   // catastrophic -> modal
		{mcpGrantReadRun, "rm -rf /", "prompt"},                  // catastrophic, gated
	}
	for _, c := range cases {
		if got := gateFor(c.lvl, c.cmd); got != c.want {
			t.Errorf("gateFor(%s, %q) = %q, want %q", c.lvl, c.cmd, got, c.want)
		}
	}
}

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
