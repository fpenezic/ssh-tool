package ssh

import "golang.org/x/crypto/ssh"

// HostKeyAlgorithmsFor maps a stored host key type (as returned by
// ssh.PublicKey.Type()) to the host key algorithms the client should
// offer when pinning that host.
//
// For most key types the key type and the signature algorithm share a
// name, but RSA is the exception: an "ssh-rsa" key can sign with
// rsa-sha2-512, rsa-sha2-256 or legacy SHA-1 "ssh-rsa". Modern OpenSSH
// servers refuse the SHA-1 variant, so offering only the key type
// makes the handshake fail with "no common algorithm for host key"
// even though the stored key is valid. The SHA-2 variants are offered
// first; the key itself is still pinned by the host key callback.
func HostKeyAlgorithmsFor(keyType string) []string {
	switch keyType {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	case ssh.CertAlgoRSAv01:
		return []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01}
	default:
		return []string{keyType}
	}
}
