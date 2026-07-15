package share

import (
	"net"
	"testing"
)

// TestListenPreferredFallsBack: when the preferred port is taken, the listener
// binds a nearby or ephemeral port instead of failing - so a busy 8443 never
// blocks a share.
func TestListenPreferredFallsBack(t *testing.T) {
	// Occupy a port on loopback.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	taken := uint16(blocker.Addr().(*net.TCPAddr).Port)

	ln, err := listenPreferred("127.0.0.1", taken)
	if err != nil {
		t.Fatalf("listenPreferred: %v", err)
	}
	defer ln.Close()

	got := uint16(ln.Addr().(*net.TCPAddr).Port)
	if got == taken {
		t.Fatalf("bound the taken port %d", taken)
	}
	// It should be within the +10 window or an ephemeral port; either way, a
	// real, different, listenable port.
	if got == 0 {
		t.Fatal("bound port 0")
	}
}

// TestListenPreferredUsesPreferredWhenFree: a free preferred port is honoured.
func TestListenPreferredUsesPreferredWhenFree(t *testing.T) {
	// Grab a free port, release it, then ask for exactly it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := uint16(probe.Addr().(*net.TCPAddr).Port)
	probe.Close()

	ln, err := listenPreferred("127.0.0.1", free)
	if err != nil {
		t.Fatalf("listenPreferred: %v", err)
	}
	defer ln.Close()
	if got := uint16(ln.Addr().(*net.TCPAddr).Port); got != free {
		t.Fatalf("got port %d, wanted the free preferred %d", got, free)
	}
}
