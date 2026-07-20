package share

// Network-interface enumeration for the share dialog's bind picker, plus a
// couple of tiny codec helpers the server uses.

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"sort"
	"strings"
)

// Interface is one bindable network interface, for the UI dropdown.
type Interface struct {
	Name string `json:"name"` // "eth0", "wg0", ...
	IP   string `json:"ip"`   // "10.0.4.7"
}

// Interfaces returns the up, non-loopback interfaces with a usable unicast IPv4
// (and global IPv6) address. The bind picker offers these instead of 0.0.0.0 so
// a share isn't accidentally exposed on an interface the user forgot about.
//
// The list is sorted so the first entry is the interface a guest can most
// likely reach: real LAN adapters first, virtual host-only ones (Hyper-V /
// WSL vEthernet, VMware, VirtualBox) last. The frontend defaults to the first
// entry, so on Windows the WSL/Hyper-V adapter must not sort ahead of the real
// NIC or the share URL comes out on an IP no phone/laptop on the LAN can hit.
func Interfaces() []Interface {
	var out []Interface
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
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
			ip := ipNet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			out = append(out, Interface{Name: iface.Name, IP: ip.String()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return ifaceRank(out[i]) < ifaceRank(out[j])
	})
	return out
}

// ifaceRank orders bindable interfaces for the picker: lower sorts first.
// Real, routable adapters come before host-only virtual ones so the default
// selection lands on an interface a LAN guest can actually reach.
func ifaceRank(i Interface) int {
	if isVirtualIface(i.Name) {
		return 2
	}
	// Prefer a private LAN address (RFC1918 / ULA) over anything else so an
	// ordinary home/office NIC wins the default even if the OS enumerates a
	// less useful adapter first.
	if ip := net.ParseIP(i.IP); ip != nil && ip.IsPrivate() {
		return 0
	}
	return 1
}

// isVirtualIface reports whether the interface name looks like a host-only
// virtual adapter (Hyper-V / WSL vEthernet, VMware, VirtualBox, docker) whose
// address a guest on the physical LAN cannot route to.
func isVirtualIface(name string) bool {
	n := strings.ToLower(name)
	// Host-only virtual adapters only. Overlay adapters a guest may share the
	// network with (tailscale/wg/zerotier) are deliberately NOT here - binding
	// to them is a valid choice.
	for _, marker := range []string{
		"vethernet", "hyper-v", "wsl", "vmware", "virtualbox", "vboxnet",
		"docker", "br-", "veth",
	} {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// unmarshalTabs decodes the frontend's projected {tabs:[...]} blob into the
// manifest tab list. The frontend owns the pane-tree schema; the backend treats
// each tab's root as opaque JSON.
func unmarshalTabs(blob []byte, out *[]ManifestTab) error {
	if len(blob) == 0 {
		*out = nil
		return nil
	}
	var wrap struct {
		Tabs []ManifestTab `json:"tabs"`
	}
	if err := json.Unmarshal(blob, &wrap); err != nil {
		return err
	}
	*out = wrap.Tabs
	return nil
}
