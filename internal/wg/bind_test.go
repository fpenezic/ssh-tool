package wg

import (
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// When the source address goes away, a failing send moves the socket to
// the new one and the receive function keeps working on it.
func TestSourceBindRoams(t *testing.T) {
	var mu sync.Mutex
	src := netip.MustParseAddr("127.0.0.2")
	b := newSourceBind(src)
	b.pick = func() (netip.Addr, bool) {
		mu.Lock()
		defer mu.Unlock()
		return src, true
	}
	fns, port, err := b.Open(0)
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2 here: %v", err)
	}
	defer b.Close()

	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	ep := &conn.StdNetEndpoint{AddrPort: peer.LocalAddr().(*net.UDPAddr).AddrPort()}
	from := func() netip.Addr {
		buf := make([]byte, 16)
		_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, ap, err := peer.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatal(err)
		}
		return ap.Addr()
	}
	if err := b.Send([][]byte{[]byte("a")}, ep); err != nil {
		t.Fatal(err)
	}
	if got := from(); got != netip.MustParseAddr("127.0.0.2") {
		t.Fatalf("sent from %s", got)
	}

	// The receiver is blocked on the old socket when the move happens.
	got := make(chan string, 1)
	go func() {
		bufs, sizes, eps := [][]byte{make([]byte, 16)}, []int{0}, []conn.Endpoint{nil}
		if n, err := fns[0](bufs, sizes, eps); err == nil && n == 1 {
			got <- string(bufs[0][:sizes[0]])
		} else {
			got <- "error: " + err.Error()
		}
	}()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	src = netip.MustParseAddr("127.0.0.3")
	mu.Unlock()
	if !b.rebind() {
		t.Fatal("no move to the new source")
	}
	if b.rebind() {
		t.Fatal("rebind not rate limited")
	}
	if err := b.Send([][]byte{[]byte("b")}, ep); err != nil {
		t.Fatal(err)
	}
	if got := from(); got != netip.MustParseAddr("127.0.0.3") {
		t.Fatalf("after the move sent from %s", got)
	}
	newAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 3), Port: int(port)}
	if b.v4.LocalAddr().(*net.UDPAddr).Port != int(port) {
		newAddr.Port = b.v4.LocalAddr().(*net.UDPAddr).Port
	}
	if _, err := peer.WriteToUDP([]byte("reply"), newAddr); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "reply" {
			t.Fatalf("receive after the move: %s", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receive function stuck on the old socket")
	}
}

func TestResolveEndpointPort(t *testing.T) {
	for _, ep := range []string{"192.0.2.1:abc", "192.0.2.1:70000", "192.0.2.1:0"} {
		if got, err := resolveEndpoint(ep); err == nil {
			t.Errorf("%s accepted as %s", ep, got)
		}
	}
	if got, err := resolveEndpoint("192.0.2.1:51820"); err != nil || got != "192.0.2.1:51820" {
		t.Errorf("valid endpoint: %s %v", got, err)
	}
}
