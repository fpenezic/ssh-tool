//go:build !android

package creds

import (
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// What an older Windows build sealed under the previous id derivation
// (HOSTNAME first) still opens after COMPUTERNAME took precedence.
func TestOpenForMachineLegacyWindowsID(t *testing.T) {
	old := machineGOOS
	machineGOOS = "windows"
	t.Cleanup(func() { machineGOOS = old })
	t.Setenv("HOSTNAME", "Legacy-Host")
	t.Setenv("COMPUTERNAME", "CURRENT-HOST")

	legacy := legacyMachineID()
	if legacy == "" {
		t.Fatal("legacy id equals the current one; the test setup did not change the derivation")
	}
	key, err := machineKeyFor(legacy)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	blob := append(append([]byte{}, nonce...), aead.Seal(nil, nonce, []byte("pending-pass"), nil)...)

	got, err := OpenForMachine(blob)
	if err != nil || string(got) != "pending-pass" {
		t.Fatalf("OpenForMachine = %q, %v", got, err)
	}
	if id, _ := machineID(); id != "CURRENT-HOST" {
		t.Errorf("current id = %q, want COMPUTERNAME", id)
	}

	// Sealing uses the current id only.
	sealed, err := SealForMachine([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	ck, _ := machineKeyFor("CURRENT-HOST")
	ca, _ := chacha20poly1305.NewX(ck)
	if _, err := ca.Open(nil, sealed[:ca.NonceSize()], sealed[ca.NonceSize():], nil); err != nil {
		t.Errorf("SealForMachine did not use the current id: %v", err)
	}
}
