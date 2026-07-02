package creds

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// KeyType is the user-facing key kind.
type KeyType string

const (
	KeyEd25519 KeyType = "ed25519"
	KeyRSA     KeyType = "rsa"
	KeyECDSA   KeyType = "ecdsa"
)

// GenerateParams collects the user-supplied options for fresh key gen.
type GenerateParams struct {
	KeyType    KeyType `json:"key_type"`
	Bits       *uint32 `json:"bits"` // RSA: 2048/3072/4096. ECDSA: 256/384/521.
	Comment    string  `json:"comment"`
	Passphrase *string `json:"passphrase"` // nil => unencrypted
}

// GeneratedKey is the result handed to the service layer.
type GeneratedKey struct {
	PrivateOpenSSH    string // PEM-encoded OpenSSH private key
	PublicOpenSSH     string // "ssh-... AAAA... comment"
	Algorithm         string // ssh wire algo, e.g. "ssh-ed25519"
	FingerprintSha256 string // "SHA256:..."
}

// Generate creates a fresh key pair per params. Encrypts the private side
// with the passphrase if provided.
//
// Implementation note: x/crypto/ssh has MarshalPrivateKey /
// MarshalPrivateKeyWithPassphrase which emit canonical OpenSSH format
// directly. We get to skip the Go-big.Int padding pitfall that ate days on
// the Rust path.
func Generate(p GenerateParams) (*GeneratedKey, error) {
	var (
		priv any
		pub  ssh.PublicKey
		err  error
	)
	switch p.KeyType {
	case KeyEd25519:
		var pubKey ed25519.PublicKey
		var privKey ed25519.PrivateKey
		pubKey, privKey, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		priv = privKey
		pub, err = ssh.NewPublicKey(pubKey)
		if err != nil {
			return nil, err
		}
	case KeyRSA:
		bits := uint32(3072)
		if p.Bits != nil {
			bits = *p.Bits
		}
		if !(bits == 2048 || bits == 3072 || bits == 4096) {
			return nil, fmt.Errorf("rsa bits must be 2048/3072/4096, got %d", bits)
		}
		rsaKey, err := rsa.GenerateKey(rand.Reader, int(bits))
		if err != nil {
			return nil, err
		}
		priv = rsaKey
		pub, err = ssh.NewPublicKey(&rsaKey.PublicKey)
		if err != nil {
			return nil, err
		}
	case KeyECDSA:
		bits := uint32(256)
		if p.Bits != nil {
			bits = *p.Bits
		}
		var curve elliptic.Curve
		switch bits {
		case 256:
			curve = elliptic.P256()
		case 384:
			curve = elliptic.P384()
		case 521:
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("ecdsa bits must be 256/384/521, got %d", bits)
		}
		ecKey, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, err
		}
		priv = ecKey
		pub, err = ssh.NewPublicKey(&ecKey.PublicKey)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported key type: %s", p.KeyType)
	}

	var pemBlock []byte
	if p.Passphrase != nil && *p.Passphrase != "" {
		pemBlock, err = marshalEncrypted(priv, *p.Passphrase, p.Comment)
		if err != nil {
			return nil, err
		}
	} else {
		pemBlock, err = marshalUnencrypted(priv, p.Comment)
		if err != nil {
			return nil, err
		}
	}

	return &GeneratedKey{
		PrivateOpenSSH:    string(pemBlock),
		PublicOpenSSH:     string(ssh.MarshalAuthorizedKey(pub)),
		Algorithm:         pub.Type(),
		FingerprintSha256: ssh.FingerprintSHA256(pub),
	}, nil
}

func marshalUnencrypted(priv any, comment string) ([]byte, error) {
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	return pemEncode(block), nil
}

func marshalEncrypted(priv any, passphrase, comment string) ([]byte, error) {
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, comment, []byte(passphrase))
	if err != nil {
		return nil, err
	}
	return pemEncode(block), nil
}

// ParsePrivate parses an OpenSSH-format private key. Returns metadata + the
// re-emitted public key. Used during import flow.
func ParsePrivate(openssh string, passphrase *string) (*ParsedPrivate, error) {
	var (
		signer ssh.Signer
		err    error
	)
	if passphrase != nil && *passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(openssh), []byte(*passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(openssh))
		if err != nil {
			if _, isMissing := err.(*ssh.PassphraseMissingError); isMissing {
				return nil, errors.New("key is encrypted; passphrase required")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("parse private: %w", err)
	}
	pub := signer.PublicKey()
	return &ParsedPrivate{
		Algorithm:         pub.Type(),
		PublicOpenSSH:     string(ssh.MarshalAuthorizedKey(pub)),
		FingerprintSha256: ssh.FingerprintSHA256(pub),
	}, nil
}

type ParsedPrivate struct {
	Algorithm         string
	PublicOpenSSH     string
	FingerprintSha256 string
	Comment           string // ssh package doesn't expose it; left empty here
}
