package inventory

import "encoding/json"

// HostnameAuto is the hostname_source that picks the public IPv4 when the
// instance has one and the private one otherwise. It is what makes a
// bastion setup work without per-host config: hosts with a public address
// are dialled directly, private-only ones get their private address and are
// routed through the folder's bastion at connect time.
const HostnameAuto = "auto"

// Addresses returns the public and private IPv4 of a cloud entry, read back
// from the Raw JSON its provider stored at refresh time. Empty strings when
// the instance has no such address or the provider has no address model
// (proxmox, ansible). Reading from Raw keeps the addresses out of the
// dynamic_entries schema: they are recomputed from the same data the entry
// was built from, so they can never disagree with it.
func Addresses(provider string, raw []byte) (public, private string) {
	if len(raw) == 0 {
		return "", ""
	}
	switch provider {
	case "digitalocean":
		var d doDroplet
		if json.Unmarshal(raw, &d) == nil {
			return doAddrs(d)
		}
	case "hetzner":
		var s hetznerServer
		if json.Unmarshal(raw, &s) == nil {
			return hetznerAddrs(s)
		}
	case "scaleway":
		var s scalewayServer
		if json.Unmarshal(raw, &s) == nil {
			return scalewayAddrs(s)
		}
	case "linode":
		var l linodeInstance
		if json.Unmarshal(raw, &l) == nil {
			return linodeAddrs(l)
		}
	case "vultr":
		var v vultrInstance
		if json.Unmarshal(raw, &v) == nil {
			return vultrAddrs(v)
		}
	case "aws_ec2":
		var i ec2Instance
		if json.Unmarshal(raw, &i) == nil {
			return i.PublicIP, i.PrivateIP
		}
	}
	return "", ""
}

// SupportsAddresses reports whether a provider exposes public/private
// addresses, i.e. whether the bastion option applies to it.
func SupportsAddresses(provider string) bool {
	switch provider {
	case "digitalocean", "hetzner", "scaleway", "linode", "vultr", "aws_ec2":
		return true
	}
	return false
}

func doAddrs(d doDroplet) (pub, priv string) {
	for _, n := range d.Networks.V4 {
		if n.IPAddress == "" {
			continue
		}
		if n.Type == "public" && pub == "" {
			pub = n.IPAddress
		}
		if n.Type == "private" && priv == "" {
			priv = n.IPAddress
		}
	}
	return pub, priv
}

func hetznerAddrs(s hetznerServer) (pub, priv string) {
	pub = s.PublicNet.IPv4.IP
	if len(s.PrivateNet) > 0 {
		priv = s.PrivateNet[0].IP
	}
	return pub, priv
}

// scalewayAddrs reads the instance API's own private_ip, which Scaleway only
// fills for the legacy private address. Private Networks live in the
// separate IPAM API and are not fetched.
func scalewayAddrs(s scalewayServer) (pub, priv string) {
	pub = s.PublicIP.Address
	if s.PrivateIP != nil {
		priv = *s.PrivateIP
	}
	return pub, priv
}

func linodeAddrs(l linodeInstance) (pub, priv string) {
	for _, ip := range l.IPv4 {
		if isLinodePrivate(ip) {
			if priv == "" {
				priv = ip
			}
		} else if pub == "" {
			pub = ip
		}
	}
	return pub, priv
}

func vultrAddrs(v vultrInstance) (pub, priv string) {
	if v.MainIP != "0.0.0.0" {
		pub = v.MainIP
	}
	return pub, v.InternalIP
}
