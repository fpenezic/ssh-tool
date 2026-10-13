package ssh

import (
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestHostKeyAlgorithmsFor(t *testing.T) {
	cases := []struct {
		keyType string
		want    []string
	}{
		{ssh.KeyAlgoRSA, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}},
		{ssh.CertAlgoRSAv01, []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01}},
		{ssh.KeyAlgoED25519, []string{ssh.KeyAlgoED25519}},
		{ssh.KeyAlgoECDSA256, []string{ssh.KeyAlgoECDSA256}},
	}
	for _, c := range cases {
		if got := HostKeyAlgorithmsFor(c.keyType); !slices.Equal(got, c.want) {
			t.Errorf("HostKeyAlgorithmsFor(%q) = %v, want %v", c.keyType, got, c.want)
		}
	}
}
