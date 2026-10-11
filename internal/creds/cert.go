package creds

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"software.sslmate.com/src/go-pkcs12"

	"ssh-tool/internal/store"
)

// CertMeta is the public side of a client certificate credential, kept
// in the credential config so the UI can show it without the vault.
type CertMeta struct {
	Subject     string
	Issuer      string
	NotAfter    time.Time
	Fingerprint string // SHA-256 of the leaf, hex
	ChainLen    int
}

func (m CertMeta) config() map[string]any {
	return map[string]any{
		"subject":     m.Subject,
		"issuer":      m.Issuer,
		"not_after":   m.NotAfter.Unix(),
		"fingerprint": m.Fingerprint,
		"chain_len":   m.ChainLen,
	}
}

// ParseClientCert turns an imported client certificate - PKCS#12
// (.p12/.pfx) or PEM with the certificate(s) and the private key - into
// the bundle the vault keeps: the leaf first, then any chain, then an
// unencrypted PKCS#8 key, all PEM. The password only opens the file at
// import; the vault's own encryption protects the stored key, so a
// connect never asks for it again.
//
// PKCS#12 goes through go-pkcs12, not x/crypto/pkcs12: the latter only
// reads the legacy RC2/3DES bags and rejects the AES-256 (PBES2) files
// that OpenSSL 3 and current Windows exports produce.
func ParseClientCert(data []byte, password string) (string, CertMeta, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return "", CertMeta{}, errors.New("validation: certificate file is empty")
	}
	var (
		leaf  *x509.Certificate
		chain []*x509.Certificate
		key   any
	)
	if bytes.Contains(data, []byte("-----BEGIN")) {
		var certs []*x509.Certificate
		for rest := data; ; {
			var b *pem.Block
			b, rest = pem.Decode(rest)
			if b == nil {
				break
			}
			switch {
			case b.Type == "CERTIFICATE":
				c, err := x509.ParseCertificate(b.Bytes)
				if err != nil {
					return "", CertMeta{}, fmt.Errorf("certificate: %w", err)
				}
				certs = append(certs, c)
			case b.Type == "ENCRYPTED PRIVATE KEY":
				return "", CertMeta{}, errors.New("encrypted PKCS#8 keys are not supported - export as .pfx / .p12, or decrypt the key first")
			case strings.HasSuffix(b.Type, "PRIVATE KEY"):
				der := b.Bytes
				//lint:ignore SA1019 legacy "Proc-Type: 4,ENCRYPTED" PEM keys still show up in the wild.
				if x509.IsEncryptedPEMBlock(b) { //nolint:staticcheck
					if password == "" {
						return "", CertMeta{}, errors.New("the private key is encrypted - enter its password")
					}
					var err error
					//lint:ignore SA1019 see above
					if der, err = x509.DecryptPEMBlock(b, []byte(password)); err != nil { //nolint:staticcheck
						return "", CertMeta{}, errors.New("wrong password for the private key")
					}
				}
				k, err := parsePrivateKey(der)
				if err != nil {
					return "", CertMeta{}, err
				}
				key = k
			}
		}
		if key == nil {
			return "", CertMeta{}, errors.New("no private key in the PEM - include the key, or import a .pfx / .p12")
		}
		if len(certs) == 0 {
			return "", CertMeta{}, errors.New("no certificate in the PEM")
		}
		// The leaf is the certificate that matches the key, wherever
		// it sits in the file.
		for i, c := range certs {
			if publicKeyMatches(c, key) {
				leaf = c
				chain = append(append(chain, certs[:i]...), certs[i+1:]...)
				break
			}
		}
		if leaf == nil {
			return "", CertMeta{}, errors.New("the private key does not belong to any certificate in the file")
		}
	} else {
		var err error
		key, leaf, chain, err = pkcs12.DecodeChain(data, password)
		if err != nil {
			if errors.Is(err, pkcs12.ErrIncorrectPassword) {
				return "", CertMeta{}, errors.New("wrong password for the certificate file")
			}
			return "", CertMeta{}, fmt.Errorf("PKCS#12: %w", err)
		}
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", CertMeta{}, fmt.Errorf("private key: %w", err)
	}
	var buf bytes.Buffer
	for _, c := range append([]*x509.Certificate{leaf}, chain...) {
		_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	_ = pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sum := sha256.Sum256(leaf.Raw)
	return buf.String(), CertMeta{
		Subject:     leaf.Subject.String(),
		Issuer:      leaf.Issuer.String(),
		NotAfter:    leaf.NotAfter,
		Fingerprint: hex.EncodeToString(sum[:]),
		ChainLen:    len(chain),
	}, nil
}

// SplitCertBundle separates a stored bundle into the certificate chain
// PEM and the key PEM, checking they still form a pair.
func SplitCertBundle(bundle string) (certPEM, keyPEM []byte, err error) {
	for rest := []byte(bundle); ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			certPEM = append(certPEM, pem.EncodeToMemory(b)...)
		} else if strings.HasSuffix(b.Type, "PRIVATE KEY") {
			keyPEM = append(keyPEM, pem.EncodeToMemory(b)...)
		}
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, nil, fmt.Errorf("stored client certificate is damaged: %w", err)
	}
	return certPEM, keyPEM, nil
}

// joinCertKey appends a separately picked key file to the certificate
// file, so a cert.pem + key.pem pair parses as one PEM.
func joinCertKey(cert, key []byte) []byte {
	if len(bytes.TrimSpace(key)) == 0 {
		return cert
	}
	out := append(append([]byte{}, cert...), '\n')
	return append(out, key...)
}

func parsePrivateKey(der []byte) (any, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("unsupported private key format")
}

func publicKeyMatches(c *x509.Certificate, key any) bool {
	k, ok := key.(interface{ Public() crypto.PublicKey })
	if !ok {
		return false
	}
	pub, ok := k.Public().(interface{ Equal(crypto.PublicKey) bool })
	return ok && pub.Equal(c.PublicKey)
}

// createCertificate imports a client TLS certificate (VPN client-cert
// auth) into the vault as a bundle; see ParseClientCert. The leaf's
// expiry becomes the credential expiry so the usual expiry warning
// covers certificate renewal.
func (s *Service) createCertificate(in CreateInput) (*CreateResult, error) {
	bundle, meta, err := ParseClientCert(joinCertKey(in.CertData, in.CertKeyData), in.CertPassword)
	if err != nil {
		return nil, err
	}
	exp := meta.NotAfter.Unix()
	cred, err := s.DB.CreateCredential(store.NewCredential{
		FolderID:             in.FolderID,
		Name:                 in.Name,
		Kind:                 store.CredCertificate,
		StorageMode:          store.StorageManaged,
		Hint:                 hintStr(in.Hint),
		Tags:                 in.Tags,
		Config:               meta.config(),
		RotationReminderDays: in.RotationReminderDays,
		ExpiresAt:            &exp,
	})
	if err != nil {
		return nil, err
	}
	vk := VaultAccountKey(cred.ID)
	if err := s.Vault.Put(vk, bundle); err != nil {
		_ = s.DB.DeleteCredential(cred.ID)
		return nil, err
	}
	if err := s.DB.SetCredentialVaultKey(cred.ID, vk); err != nil {
		return nil, err
	}
	_, _ = s.DB.AppendHistory(cred.ID, "created ("+meta.Subject+")", "user", false)
	cred, err = s.DB.GetCredential(cred.ID)
	if err != nil {
		return nil, err
	}
	return &CreateResult{Credential: cred}, nil
}

// ReplaceCertificate swaps a certificate credential's bundle for a newly
// imported one (a renewed certificate) and refreshes its metadata and
// expiry. Every profile that references the credential picks it up.
func (s *Service) ReplaceCertificate(id string, data, keyData []byte, password string) (*store.CredentialRef, error) {
	cred, err := s.DB.GetCredential(id)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, fmt.Errorf("credential %s not found", id)
	}
	if cred.Kind != store.CredCertificate {
		return nil, errors.New("replace_certificate only works on certificate credentials")
	}
	bundle, meta, err := ParseClientCert(joinCertKey(data, keyData), password)
	if err != nil {
		return nil, err
	}
	vk := VaultAccountKey(cred.ID)
	if cred.VaultKey != nil {
		vk = *cred.VaultKey
	}
	if err := s.Vault.Put(vk, bundle); err != nil {
		return nil, err
	}
	cfg := meta.config()
	exp := meta.NotAfter.Unix()
	if _, err := s.DB.UpdateCredential(store.UpdateCredential{ID: id, Config: &cfg, ExpiresAt: &exp}); err != nil {
		return nil, err
	}
	_ = s.DB.TouchCredentialRotated(id)
	if cred.VaultKey == nil {
		if err := s.DB.SetCredentialVaultKey(cred.ID, vk); err != nil {
			return nil, err
		}
	}
	_, _ = s.DB.AppendHistory(id, "certificate replaced ("+meta.Subject+")", "user", false)
	return s.DB.GetCredential(id)
}
