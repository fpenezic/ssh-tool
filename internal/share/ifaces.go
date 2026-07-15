package share

// Network-interface enumeration for the share dialog's bind picker, plus a
// couple of tiny codec helpers the server uses.

import (
	"encoding/base64"
	"encoding/json"
	"net"
)

// Interface is one bindable network interface, for the UI dropdown.
type Interface struct {
	Name string `json:"name"` // "eth0", "wg0", ...
	IP   string `json:"ip"`   // "10.0.4.7"
}

// Interfaces returns the up, non-loopback interfaces with a usable unicast IPv4
// (and global IPv6) address. The bind picker offers these instead of 0.0.0.0 so
// a share isn't accidentally exposed on an interface the user forgot about.
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
	return out
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
