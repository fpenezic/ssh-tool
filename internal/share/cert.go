package share

// Self-signed TLS certificate for the browser-share HTTPS server.
//
// The app has never served TLS before this, and there is no CA, no revocation,
// and no DNS. The certificate exists to encrypt the transport; it does NOT by
// itself authenticate the host to the guest, because a browser buries the
// fingerprint five clicks deep behind an interstitial the guest has already
// clicked through. Authentication is the WORD-MNEMONIC fingerprint the host and
// guest compare out-of-band at approval time (see Fingerprint) - the same
// trust-on-first-use model the app already uses for SSH host keys.
//
// Consequences of that design, encoded here:
//   - The cert is LONG-LIVED (10 years). A fingerprint whose value is "compare
//     this every time" must be stable; a rotating fingerprint trains the user
//     to wave through changes, which is the exact failure we are guarding
//     against. Expiry buys nothing without revocation infrastructure.
//   - It lives in DataDir, not the vault. The vault is locked until the user
//     unlocks it; sharing your screen must not require unlocking your SSH keys.
//     Key file is 0600, matching the mcp-bridge.token precedent.
//   - SANs must cover whatever the guest types in the URL bar. The guest hits
//     an IP, so the cert needs IPAddresses set; a DNS/CN name does not satisfy
//     a browser for an IP URL. EnsureFor regenerates if the chosen bind IP
//     isn't already covered - and the caller is expected to tell the user the
//     fingerprint changed and why.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

const (
	certValidity = 10 * 365 * 24 * time.Hour // see the "long-lived" note above
	certOrg      = "ssh-tool session share"
)

// Fingerprint renders a certificate's SHA-256 (over the DER) three ways. Words
// is the one humans compare; the others are for logs and power users.
type Fingerprint struct {
	Hex   string // "a1b2c3...": full SHA-256, lowercase, colon-free
	Short string // "a1b2-c3d4-e5f6-7890": first 8 bytes, grouped
	Words string // "cobalt-otter-viola-medley": 4 words from the first 4 bytes
}

// fingerprintOf computes the Fingerprint of a DER-encoded certificate.
func fingerprintOf(der []byte) Fingerprint {
	sum := sha256.Sum256(der)
	hexStr := fmt.Sprintf("%x", sum)

	var groups []string
	for i := 0; i < 8; i += 2 {
		groups = append(groups, fmt.Sprintf("%02x%02x", sum[i], sum[i+1]))
	}

	words := make([]string, 4)
	for i := 0; i < 4; i++ {
		words[i] = fingerprintWords[sum[i]]
	}

	return Fingerprint{
		Hex:   hexStr,
		Short: strings.Join(groups, "-"),
		Words: strings.Join(words, "-"),
	}
}

// Cert bundles a loaded TLS certificate with its fingerprint and the SANs it
// was minted for, so a caller can decide whether the current bind IP is
// already covered without re-parsing.
type Cert struct {
	TLS         tls.Certificate
	Fingerprint Fingerprint
	IPs         []net.IP // IP SANs the cert carries
}

// coversIP reports whether the cert already lists ip as a SAN.
func (c *Cert) coversIP(ip net.IP) bool {
	for _, got := range c.IPs {
		if got.Equal(ip) {
			return true
		}
	}
	return false
}

// LoadOrCreate loads the cert/key from certPath/keyPath, or generates a fresh
// self-signed pair covering every local IP plus loopback and the hostname.
// A stored cert that is expired or unparseable is regenerated.
func LoadOrCreate(certPath, keyPath string) (*Cert, error) {
	if c, err := load(certPath, keyPath); err == nil {
		return c, nil
	}
	return generate(certPath, keyPath, nil)
}

// EnsureFor returns a cert valid for bindIP, regenerating (and rewriting the
// files) if the existing cert does not already cover it. regenerated reports
// whether a new cert was minted - the caller should surface a fingerprint-
// changed notice when it is true.
func EnsureFor(certPath, keyPath string, bindIP net.IP) (c *Cert, regenerated bool, err error) {
	existing, loadErr := load(certPath, keyPath)
	if loadErr == nil && (bindIP == nil || existing.coversIP(bindIP)) {
		return existing, false, nil
	}
	// Regenerate covering the current IP set plus the requested bind IP.
	extra := []net.IP{}
	if bindIP != nil {
		extra = append(extra, bindIP)
	}
	c, err = generate(certPath, keyPath, extra)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// Regenerate forces a fresh cert (the Settings -> Regenerate button). Every
// existing fingerprint the user's guests saved becomes invalid.
func Regenerate(certPath, keyPath string) (*Cert, error) {
	return generate(certPath, keyPath, nil)
}

func load(certPath, keyPath string) (*Cert, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, err
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, fmt.Errorf("share cert expired")
	}
	tlsCert.Leaf = leaf
	return &Cert{
		TLS:         tlsCert,
		Fingerprint: fingerprintOf(tlsCert.Certificate[0]),
		IPs:         leaf.IPAddresses,
	}, nil
}

// localIPs returns every non-loopback unicast IP currently assigned to an up
// interface, so the cert covers whatever interface the user later binds to.
func localIPs() []net.IP {
	var ips []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		ips = append(ips, ip)
	}
	return ips
}

func generate(certPath, keyPath string, extraIPs []net.IP) (*Cert, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("share cert keygen: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("share cert serial: %w", err)
	}

	// SANs: loopback + every current local IP + the requested bind IP, deduped.
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	ips = append(ips, localIPs()...)
	ips = append(ips, extraIPs...)
	ips = dedupeIPs(ips)

	host, _ := os.Hostname()
	dnsNames := []string{"localhost"}
	if host != "" && host != "localhost" {
		dnsNames = append(dnsNames, host)
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{certOrg}, CommonName: "ssh-tool-share"},
		NotBefore:             time.Now().Add(-1 * time.Hour), // clock skew
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
		DNSNames:              dnsNames,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("share cert create: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("share cert marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Key first, 0600, so a reader never sees a cert without its matching key
	// having been written under tight perms. Cert is public (0644).
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("share cert write key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("share cert write cert: %w", err)
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("share cert reload: %w", err)
	}
	tlsCert.Leaf, _ = x509.ParseCertificate(der)
	return &Cert{
		TLS:         tlsCert,
		Fingerprint: fingerprintOf(der),
		IPs:         ips,
	}, nil
}

func dedupeIPs(in []net.IP) []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	for _, ip := range in {
		k := ip.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ip)
	}
	return out
}
