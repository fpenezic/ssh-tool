package wg

// sourceBind is a minimal wireguard-go conn.Bind that binds its UDP socket to a
// SPECIFIC local source address instead of 0.0.0.0. This is what lets the
// userspace tunnel keep working when another VPN (e.g. a UniFi Identity "TUN
// Mode" split tunnel) installs a 0.0.0.0/0 default route with a lower metric
// than the physical NIC: the default conn.NewDefaultBind() listens on all
// interfaces and lets the OS route the outbound handshake, which then leaks into
// the other VPN's TUN and is dropped. Pinning the socket to the physical NIC's
// address forces the WireGuard handshake and data out that interface directly,
// bypassing the hijacking default route.
//
// It is deliberately a BatchSize()==1 bind using the plain UDPConn API (no
// GSO/segmentation offload, no per-packet control messages). WireGuard-go
// supports single-packet binds (that is the Windows path of the stock bind
// anyway), and correctness matters more than throughput for a client that mostly
// carries an SSH first hop plus provider API calls.

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// PhysicalSourceIP returns the best physical-NIC IPv4 address to source direct
// dials from (exported wrapper over pickPhysicalSource), so callers outside the
// package (the auto-mode direct probe) can bind a plain net.Dialer to the same
// physical NIC the tunnel uses. ok=false when none is found.
func PhysicalSourceIP() (netip.Addr, bool) {
	return pickPhysicalSource()
}

// pickPhysicalSource returns the local IPv4 address of the best physical NIC to
// source WireGuard traffic from: a private-LAN (RFC1918) address on an up,
// non-loopback interface that is NOT a virtual host-only adapter (Hyper-V / WSL
// vEthernet / VMware / VirtualBox / docker) and NOT a VPN tunnel (WireGuard /
// OpenVPN TAP-Windows / Fortinet / Check Point / UniFi "Split VPN" / generic
// utun/tun/tap/ppp). Returns ok=false if none is found (caller then falls back
// to the default all-interfaces bind).
//
// The intent is to bind the WG socket to the real NIC so the handshake leaves
// the machine directly instead of following a hijacking 0.0.0.0/0 default route
// another VPN installed. We only consider IPv4 because our endpoints are IPv4
// public addresses; extend if IPv6 endpoints become common.
func pickPhysicalSource() (netip.Addr, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return netip.Addr{}, false
	}
	var best netip.Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualOrVPNIface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			ad, ok := netip.AddrFromSlice(ip4)
			if !ok {
				continue
			}
			ad = ad.Unmap()
			if ad.IsLoopback() || ad.IsLinkLocalUnicast() {
				continue
			}
			// Prefer a private LAN address; that is the home/office NIC that
			// reaches the internet directly. Take the first such one.
			if ad.IsPrivate() {
				return ad, true
			}
			if !best.IsValid() {
				best = ad // fall back to any routable non-virtual address
			}
		}
	}
	if best.IsValid() {
		return best, true
	}
	return netip.Addr{}, false
}

// isVirtualOrVPNIface reports whether the interface name looks like a host-only
// virtual adapter or a VPN tunnel whose address must not be used to source
// physical-NIC WireGuard traffic.
func isVirtualOrVPNIface(name string) bool {
	n := strings.ToLower(name)
	for _, marker := range []string{
		// host-only virtual adapters
		"vethernet", "hyper-v", "wsl", "vmware", "virtualbox", "vboxnet",
		"docker", "br-", "veth",
		// VPN tunnels (the whole point: never source from one of these)
		"wireguard", "wg", "openvpn", "tap-windows", "tap-", "tun",
		"utun", "ppp", "fortinet", "forticlient", "check point", "checkpoint",
		"split vpn", "identity", "zerotier", "tailscale", "nordlynx",
	} {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

// sourceBind implements conn.Bind, binding to a fixed source IP.
type sourceBind struct {
	src netip.Addr // the physical-NIC local address to bind to

	mu sync.Mutex
	v4 *net.UDPConn
	v6 *net.UDPConn
}

var _ conn.Bind = (*sourceBind)(nil)

// newSourceBind returns a conn.Bind pinned to src.
func newSourceBind(src netip.Addr) *sourceBind {
	return &sourceBind{src: src.Unmap()}
}

func (b *sourceBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.v4 != nil || b.v6 != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}

	// Bind the family matching the source address. WireGuard only needs the
	// family the endpoint uses; our source is a single physical-NIC address, so
	// one socket on that family is enough. We still open the other family on the
	// wildcard so peers reachable over it (rare for this client) keep working.
	var (
		fns []conn.ReceiveFunc
		err error
	)

	if b.src.Is4() {
		laddr := &net.UDPAddr{IP: b.src.AsSlice(), Port: int(port)}
		b.v4, err = net.ListenUDP("udp4", laddr)
		if err != nil {
			return nil, 0, err
		}
		// Adopt the actually-bound port for a v6 companion + the return value.
		port = uint16(b.v4.LocalAddr().(*net.UDPAddr).Port)
		fns = append(fns, b.makeReceive(b.v4))
	} else {
		laddr := &net.UDPAddr{IP: b.src.AsSlice(), Port: int(port)}
		b.v6, err = net.ListenUDP("udp6", laddr)
		if err != nil {
			return nil, 0, err
		}
		port = uint16(b.v6.LocalAddr().(*net.UDPAddr).Port)
		fns = append(fns, b.makeReceive(b.v6))
	}

	if len(fns) == 0 {
		return nil, 0, errors.New("wg source bind: no socket opened")
	}
	return fns, port, nil
}

func (b *sourceBind) makeReceive(c *net.UDPConn) conn.ReceiveFunc {
	return func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, ap, err := c.ReadFromUDPAddrPort(bufs[0])
		if err != nil {
			return 0, err
		}
		sizes[0] = n
		eps[0] = &conn.StdNetEndpoint{AddrPort: ap}
		return 1, nil
	}
}

func (b *sourceBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	se, ok := ep.(*conn.StdNetEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	dst := se.AddrPort

	b.mu.Lock()
	c := b.v4
	if dst.Addr().Is6() {
		c = b.v6
	}
	b.mu.Unlock()
	if c == nil {
		// No socket for this family (we only pinned one). Nothing we can do.
		return net.ErrClosed
	}

	for _, buf := range bufs {
		if _, err := c.WriteToUDPAddrPort(buf, dst); err != nil {
			return err
		}
	}
	return nil
}

func (b *sourceBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &conn.StdNetEndpoint{AddrPort: ap}, nil
}

// SetMark is a no-op: SO_MARK is Linux-only and irrelevant to the source-bind
// workaround (which fixes routing by choosing the outbound interface directly).
func (b *sourceBind) SetMark(mark uint32) error { return nil }

func (b *sourceBind) BatchSize() int { return 1 }

func (b *sourceBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var err error
	if b.v4 != nil {
		err = b.v4.Close()
		b.v4 = nil
	}
	if b.v6 != nil {
		if e := b.v6.Close(); e != nil && err == nil {
			err = e
		}
		b.v6 = nil
	}
	return err
}
