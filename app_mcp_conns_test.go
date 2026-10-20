//go:build !android && !ios

package main

import (
	"net"
	"testing"
	"time"
)

// Turning the bridge off ends the sessions it accepted, not just the
// listener.
func TestMcpStopClosesConns(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("APPDATA", tmp)
	t.Setenv("HOME", tmp)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{mcpListener: ln, mcpTCPListener: tcp}
	local, localPeer := net.Pipe()
	remote, remotePeer := net.Pipe()
	defer localPeer.Close()
	defer remotePeer.Close()
	if !a.mcpTrack(false, ln, local, true) || !a.mcpTrack(true, tcp, remote, true) {
		t.Fatal("track refused on running listeners")
	}
	a.stopMcpListener()
	for name, c := range map[string]net.Conn{"local": local, "tcp": remote} {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil || isTimeout(err) {
			t.Fatalf("%s connection still open after stop: %v", name, err)
		}
	}
	// A connection accepted by the old listener after the stop is refused.
	late, latePeer := net.Pipe()
	defer late.Close()
	defer latePeer.Close()
	if a.mcpTrack(false, ln, late, true) {
		t.Fatal("tracked a connection of a stopped listener")
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// A capped read ending inside a multi-byte character is still text.
func TestMcpTextPrefix(t *testing.T) {
	data := []byte("ab\xc5") // "ab" + first byte of "š"
	if got, ok := mcpTextPrefix(data, true); !ok || string(got) != "ab" {
		t.Fatalf("truncated: %q %v", got, ok)
	}
	if _, ok := mcpTextPrefix(data, false); ok {
		t.Fatal("a complete file with a broken rune counted as text")
	}
	if _, ok := mcpTextPrefix([]byte{0xff, 0xfe, 0xfd, 0xfc, 0x00}, true); ok {
		t.Fatal("binary counted as text")
	}
}

// A second answer to one approval returns an error instead of blocking.
func TestMcpApprovalAnsweredTwice(t *testing.T) {
	a := &App{mcp: &mcpState{approvals: map[string]chan mcpDecision{"x": make(chan mcpDecision, 1)}}}
	if err := a.McpApprovalRespond("x", "run"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.McpApprovalRespond("x", "deny") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second answer accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second answer blocked")
	}
}

// A late second answer to a facts approval neither blocks nor replaces
// the hosts the first answer picked.
func TestMcpFactsSecondAnswerKeepsPicks(t *testing.T) {
	a := &App{mcp: &mcpState{
		approvals:  map[string]chan mcpDecision{"f": make(chan mcpDecision, 1)},
		factsPicks: map[string][]string{},
	}}
	if err := a.McpFactsApprovalRespond("f", true, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := a.McpFactsApprovalRespond("f", true, []string{"c"}); err == nil {
		t.Fatal("second answer accepted")
	}
	if got := a.mcp.factsPicks["f"]; len(got) != 2 || got[0] != "a" {
		t.Fatalf("picks = %v", got)
	}
}
