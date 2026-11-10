package wg

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// A short name is tried in the search domains, as resolv.conf does: only
// a name that does not exist moves on. db1.a.test resolves (10.9.0.2,
// nothing listens), db1.b.test too (10.9.0.3 listens): db1 must fail on
// a.test's host, never reach b.test's. db2 exists only in b.test.
func TestDialSearchDomains(t *testing.T) {
	_, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("10.9.0.1"), netip.MustParseAddr("10.9.0.2"), netip.MustParseAddr("10.9.0.3")},
		[]netip.Addr{netip.MustParseAddr("10.9.0.1")}, 1420)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	pc, err := tnet.ListenUDP(&net.UDPAddr{IP: net.ParseIP("10.9.0.1"), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		ip := map[string][4]byte{"db1.a.test.": {10, 9, 0, 2}, "db1.b.test.": {10, 9, 0, 3}, "db2.b.test.": {10, 9, 0, 3}}
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) == 0 {
				continue
			}
			q := m.Questions[0]
			name := q.Name.String()
			if q.Type == dnsmessage.TypeA {
				mu.Lock()
				asked = append(asked, name)
				mu.Unlock()
			}
			m.Response = true
			if a, ok := ip[name]; ok && q.Type == dnsmessage.TypeA {
				m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: a}}}
			} else if !ok {
				m.RCode = dnsmessage.RCodeNameError
			}
			out, _ := m.Pack()
			_, _ = pc.WriteTo(out, from)
		}
	}()
	ln, err := tnet.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.9.0.3"), Port: 2222})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	names := func() string {
		mu.Lock()
		defer mu.Unlock()
		s := strings.Join(asked, ",")
		asked = nil
		return s
	}
	tun := &Tunnel{tnet: tnet, search: []string{"a.test", "b.test"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if c, err := tun.DialContext(ctx, "tcp", "db1:2222"); err == nil {
		c.Close()
		t.Fatalf("db1 reached %v: the refused host in a.test moved on to b.test", c.RemoteAddr())
	}
	if got := names(); got != "db1.a.test." {
		t.Errorf("db1 asked %s", got)
	}
	c, err := tun.DialContext(ctx, "tcp", "db2:2222")
	if err != nil {
		t.Fatalf("db2: %v", err)
	}
	if c.RemoteAddr().String() != "10.9.0.3:2222" {
		t.Errorf("db2 reached %v", c.RemoteAddr())
	}
	c.Close()
	if got := names(); got != "db2.a.test.,db2.b.test." {
		t.Errorf("db2 asked %s", got)
	}
	// Nowhere: the error is the name as written.
	if _, err := tun.DialContext(ctx, "tcp", "nope:22"); err == nil || !strings.Contains(err.Error(), "lookup nope.") {
		t.Errorf("nope: %v", err)
	}
}
