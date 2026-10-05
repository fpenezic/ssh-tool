package ssh

import (
	"errors"
	"net"
	"testing"
)

// An auto-port forward restarted after a stop lands on the port it had,
// and moves on when that port was taken meanwhile.
func TestListenSticky(t *testing.T) {
	p := NewForwardPool()
	l1, port1, moved, err := p.listenSticky("fwd-a", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Errorf("first bind reported a move from %d", moved)
	}
	l1.Close()
	l2, port2, moved, err := p.listenSticky("fwd-a", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Errorf("sticky rebind reported a move from %d", moved)
	}
	if port2 != port1 {
		t.Errorf("restart got port %d, want the sticky %d", port2, port1)
	}
	// l2 still holds the port: a third bind must fall back, not fail.
	l3, port3, moved, err := p.listenSticky("fwd-a", "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("taken sticky port must fall back to a new one: %v", err)
	}
	if moved != port2 {
		t.Errorf("fallback reported moved=%d, want %d", moved, port2)
	}
	if port3 == port2 {
		t.Error("fell back to the port that is still held")
	}
	l2.Close()
	l3.Close()
	var _ net.Listener = l3
}

// A fixed port that is taken comes back as *PortInUseError: naming our own
// forward when one of ours holds it, "another program" otherwise.
func TestListenStickyPortInUse(t *testing.T) {
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	taken := uint16(other.Addr().(*net.TCPAddr).Port)

	p := NewForwardPool()
	_, _, _, err = p.listenSticky("fwd-b", "127.0.0.1", taken)
	var pe *PortInUseError
	if !errors.As(err, &pe) {
		t.Fatalf("want *PortInUseError, got %T %v", err, err)
	}
	if pe.HolderID != "" || pe.Port != taken {
		t.Errorf("foreign holder: got %+v", pe)
	}

	// Register a fake running forward of ours on a second port.
	mine, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Close()
	minePort := uint16(mine.Addr().(*net.TCPAddr).Port)
	p.register(&activeForward{id: "fwd-held", kind: ForwardLocal, session: &Session{ID: "s1"},
		localAddr: "127.0.0.1", localPort: minePort})
	_, _, _, err = p.listenSticky("fwd-c", "0.0.0.0", minePort)
	if !errors.As(err, &pe) || pe.HolderID != "fwd-held" {
		t.Fatalf("own holder: want fwd-held, got %v", err)
	}
	if got := p.CheckLocalPort("127.0.0.1", minePort); !got.InUse || got.HolderID != "fwd-held" {
		t.Errorf("CheckLocalPort own: %+v", got)
	}
	if got := p.CheckLocalPort("127.0.0.1", taken); !got.InUse || got.HolderID != "" {
		t.Errorf("CheckLocalPort foreign: %+v", got)
	}
}
