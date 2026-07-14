package share

import (
	"crypto/tls"
	"net"
	"path/filepath"
	"testing"
)

func TestGenerateAndLoadStable(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "share-cert.pem")
	keyPath := filepath.Join(dir, "share-key.pem")

	c1, err := LoadOrCreate(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreate (generate): %v", err)
	}
	if c1.Fingerprint.Words == "" || c1.Fingerprint.Hex == "" {
		t.Fatal("empty fingerprint")
	}

	// Loading again must return the SAME fingerprint - this is the whole
	// point of persistence. A rotating fingerprint would defeat the words.
	c2, err := LoadOrCreate(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreate (load): %v", err)
	}
	if c2.Fingerprint.Hex != c1.Fingerprint.Hex {
		t.Fatalf("fingerprint changed on reload: %s -> %s", c1.Fingerprint.Hex, c2.Fingerprint.Hex)
	}
	if c2.Fingerprint.Words != c1.Fingerprint.Words {
		t.Fatalf("words changed on reload: %q -> %q", c1.Fingerprint.Words, c2.Fingerprint.Words)
	}
}

func TestFingerprintShapes(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadOrCreate(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"))
	if err != nil {
		t.Fatal(err)
	}
	fp := c.Fingerprint
	if len(fp.Hex) != 64 {
		t.Fatalf("hex len = %d, want 64", len(fp.Hex))
	}
	// Short is 4 groups of 4 hex chars joined by "-": "aabb-ccdd-eeff-0011".
	if len(fp.Short) != 19 {
		t.Fatalf("short = %q (len %d), want len 19", fp.Short, len(fp.Short))
	}
	// Words: 4 words joined by "-".
	wordCount := 1
	for _, r := range fp.Words {
		if r == '-' {
			wordCount++
		}
	}
	if wordCount != 4 {
		t.Fatalf("words = %q, want 4 words", fp.Words)
	}
}

func TestEnsureForCoversBindIP(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "c.pem")
	keyPath := filepath.Join(dir, "k.pem")

	// A public-ish IP the machine almost certainly does not have, so the
	// initial cert won't already cover it and EnsureFor must regenerate.
	bind := net.ParseIP("203.0.113.7") // TEST-NET-3, reserved for docs
	c, regen, err := EnsureFor(certPath, keyPath, bind)
	if err != nil {
		t.Fatalf("EnsureFor: %v", err)
	}
	if !regen {
		t.Fatal("expected regeneration for an uncovered bind IP")
	}
	if !c.coversIP(bind) {
		t.Fatal("regenerated cert does not cover the bind IP")
	}

	// A second call for the same IP must NOT regenerate (fingerprint stable).
	c2, regen2, err := EnsureFor(certPath, keyPath, bind)
	if err != nil {
		t.Fatal(err)
	}
	if regen2 {
		t.Fatal("unexpected regeneration for an already-covered bind IP")
	}
	if c2.Fingerprint.Hex != c.Fingerprint.Hex {
		t.Fatal("fingerprint changed despite no regeneration")
	}

	// Loopback is always covered, so it never regenerates.
	if _, regen3, _ := EnsureFor(certPath, keyPath, net.IPv4(127, 0, 0, 1)); regen3 {
		t.Fatal("loopback should already be covered")
	}
}

func TestCertUsableForTLS(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadOrCreate(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"))
	if err != nil {
		t.Fatal(err)
	}
	// The tls.Certificate must be complete enough to serve.
	cfg := &tls.Config{Certificates: []tls.Certificate{c.TLS}}
	if len(cfg.Certificates[0].Certificate) == 0 {
		t.Fatal("no DER in tls.Certificate")
	}
	if cfg.Certificates[0].PrivateKey == nil {
		t.Fatal("no private key in tls.Certificate")
	}
}
