package creds

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

func testCert(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: parent == nil, BasicConstraintsValid: true}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func certPEM(cs ...*x509.Certificate) string {
	var b strings.Builder
	for _, c := range cs {
		b.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	return b.String()
}

// Both the AES-256 (OpenSSL 3 / current Windows export) and the legacy
// RC2 PKCS#12 encodings import, and a wrong password says so.
func TestParseClientCertPKCS12(t *testing.T) {
	ca, caKey := testCert(t, "ca.example.com", nil, nil)
	leaf, key := testCert(t, "client.example.com", ca, caKey)
	for name, enc := range map[string]*pkcs12.Encoder{"modern": pkcs12.Modern, "legacy": pkcs12.LegacyRC2} {
		pfx, err := enc.Encode(key, leaf, []*x509.Certificate{ca}, "secret")
		if err != nil {
			t.Fatalf("%s encode: %v", name, err)
		}
		bundle, meta, err := ParseClientCert(pfx, "secret")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if meta.Subject != "CN=client.example.com" || meta.ChainLen != 1 {
			t.Fatalf("%s: meta = %+v", name, meta)
		}
		if _, _, err := SplitCertBundle(bundle); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, _, err := ParseClientCert(pfx, "wrong"); err == nil || !strings.Contains(err.Error(), "wrong password") {
			t.Fatalf("%s: wrong password: %v", name, err)
		}
	}
}

// In a PEM the leaf is found by its key, not by position, and is put
// first in the stored bundle.
func TestParseClientCertPEMLeafByKey(t *testing.T) {
	ca, caKey := testCert(t, "ca.example.com", nil, nil)
	leaf, key := testCert(t, "client.example.com", ca, caKey)
	der, _ := x509.MarshalECPrivateKey(key)
	in := certPEM(ca, leaf) + string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	bundle, meta, err := ParseClientCert([]byte(in), "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Subject != "CN=client.example.com" {
		t.Fatalf("leaf = %s", meta.Subject)
	}
	first, _ := pem.Decode([]byte(bundle))
	if c, _ := x509.ParseCertificate(first.Bytes); c.Subject.CommonName != "client.example.com" {
		t.Fatalf("bundle starts with %s", c.Subject.CommonName)
	}
	if _, _, err := ParseClientCert([]byte(certPEM(leaf)), ""); err == nil {
		t.Fatal("a PEM without a key must be rejected")
	}
}

// A cert.pem + key.pem pair picked as two files imports as one.
func TestJoinCertKey(t *testing.T) {
	leaf, key := testCert(t, "client.example.com", nil, nil)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	k := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, _, err := ParseClientCert(joinCertKey([]byte(certPEM(leaf)), k), ""); err != nil {
		t.Fatal(err)
	}
}
