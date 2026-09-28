package ssh

import (
	"net"
	"testing"
)

// An auto-port forward restarted after a stop lands on the port it had,
// and moves on when that port was taken meanwhile.
func TestListenSticky(t *testing.T) {
	p := NewForwardPool()
	l1, port1, err := p.listenSticky("fwd-a", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	l1.Close()
	l2, port2, err := p.listenSticky("fwd-a", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if port2 != port1 {
		t.Errorf("restart got port %d, want the sticky %d", port2, port1)
	}
	// l2 still holds the port: a third bind must fall back, not fail.
	l3, port3, err := p.listenSticky("fwd-a", "127.0.0.1", 0)
	if err != nil {
		t.Fatalf("taken sticky port must fall back to a new one: %v", err)
	}
	if port3 == port2 {
		t.Error("fell back to the port that is still held")
	}
	l2.Close()
	l3.Close()
	var _ net.Listener = l3
}
