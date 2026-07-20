package share

import (
	"sort"
	"testing"
)

func TestIfaceRankOrdersRealLANFirst(t *testing.T) {
	// The picker defaults to the first entry, so a host-only virtual adapter
	// (WSL/Hyper-V vEthernet) must never sort ahead of the real NIC.
	in := []Interface{
		{Name: "vEthernet (WSL (Hyper-V firewall))", IP: "172.17.240.1"},
		{Name: "Ethernet", IP: "192.168.107.94"},
		{Name: "wg0", IP: "10.8.0.2"},
	}
	sort.SliceStable(in, func(i, j int) bool {
		return ifaceRank(in[i]) < ifaceRank(in[j])
	})
	if in[0].IP != "192.168.107.94" {
		t.Fatalf("first = %+v, want the private Ethernet NIC", in[0])
	}
	// The WSL vEthernet is virtual and must sort last even though it is private.
	last := in[len(in)-1]
	if last.IP != "172.17.240.1" {
		t.Fatalf("last = %+v, want the WSL vEthernet", last)
	}
}

func TestIsVirtualIface(t *testing.T) {
	virtual := []string{
		"vEthernet (WSL)", "vEthernet (Default Switch)", "Hyper-V Virtual Ethernet",
		"VMware Network Adapter VMnet8", "VirtualBox Host-Only", "vboxnet0",
		"docker0", "br-1a2b3c", "veth0aa11",
	}
	for _, n := range virtual {
		if !isVirtualIface(n) {
			t.Errorf("%q should be virtual", n)
		}
	}
	// Real and overlay adapters a guest can route to must NOT be flagged.
	real := []string{"Ethernet", "eth0", "en0", "wlan0", "wg0", "tailscale0", "utun3", "ztabcd1234"}
	for _, n := range real {
		if isVirtualIface(n) {
			t.Errorf("%q should not be virtual", n)
		}
	}
}
