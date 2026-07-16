package bitwarden

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// symKey is a Bitwarden symmetric key: a 32-byte encryption key and an optional
// 32-byte MAC key (present on stretched / 64-byte keys, absent on raw 32-byte).
type symKey struct {
	enc []byte
	mac []byte
}

// zero wipes the key material. Called when a vault is forgotten.
func (k *symKey) zero() {
	for i := range k.enc {
		k.enc[i] = 0
	}
	for i := range k.mac {
		k.mac[i] = 0
	}
}

// parseEncString splits a Bitwarden encrypted string "TYPE.base64|base64|..."
// into its type and raw components. iv/mac are nil for the types that lack them.
func parseEncString(s string) (typ int, iv, ct, mac []byte, err error) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0, nil, nil, nil, errors.New("bitwarden: enc string has no type prefix")
	}
	typ, err = strconv.Atoi(s[:dot])
	if err != nil {
		return 0, nil, nil, nil, fmt.Errorf("bitwarden: bad enc type: %w", err)
	}
	parts := strings.Split(s[dot+1:], "|")
	dec := func(i int) ([]byte, error) {
		if i >= len(parts) {
			return nil, fmt.Errorf("bitwarden: enc string missing part %d", i)
		}
		return base64.StdEncoding.DecodeString(parts[i])
	}
	switch typ {
	case encAesCbc256_B64:
		if iv, err = dec(0); err != nil {
			return
		}
		ct, err = dec(1)
		return
	case encAesCbc128_HmacSha256_B64, encAesCbc256_HmacSha256_B64:
		if iv, err = dec(0); err != nil {
			return
		}
		if ct, err = dec(1); err != nil {
			return
		}
		mac, err = dec(2)
		return
	case encRsa2048_OaepSha256_B64, encRsa2048_OaepSha1_B64:
		ct, err = dec(0)
		return
	case encRsa2048_OaepSha256_HmacB64, encRsa2048_OaepSha1_HmacB64:
		if ct, err = dec(0); err != nil {
			return
		}
		mac, err = dec(1)
		return
	default:
		return typ, nil, nil, nil, fmt.Errorf("bitwarden: unsupported enc type %d", typ)
	}
}

// decryptSym decrypts an AES-CBC(+HMAC) EncString with a symmetric key.
func decryptSym(s string, key symKey) ([]byte, error) {
	typ, iv, ct, mac, err := parseEncString(s)
	if err != nil {
		return nil, err
	}
	switch typ {
	case encAesCbc256_B64, encAesCbc128_HmacSha256_B64, encAesCbc256_HmacSha256_B64:
	default:
		return nil, fmt.Errorf("bitwarden: decryptSym on non-symmetric type %d", typ)
	}
	if mac != nil {
		if key.mac == nil {
			return nil, errors.New("bitwarden: MAC present but key has no MAC key")
		}
		h := hmac.New(sha256.New, key.mac)
		h.Write(iv)
		h.Write(ct)
		if !hmac.Equal(h.Sum(nil), mac) {
			return nil, errors.New("bitwarden: HMAC mismatch (wrong key or tampered data)")
		}
	}
	block, err := aes.NewCipher(key.enc)
	if err != nil {
		return nil, err
	}
	if len(ct) == 0 || len(ct)%block.BlockSize() != 0 || len(iv) != block.BlockSize() {
		return nil, errors.New("bitwarden: ciphertext not block-aligned")
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return pkcs7Unpad(out, block.BlockSize())
}

// decryptToSymKey decrypts an EncString whose plaintext is itself a key.
func decryptToSymKey(s string, key symKey) (symKey, error) {
	raw, err := decryptSym(s, key)
	if err != nil {
		return symKey{}, err
	}
	return bytesToSymKey(raw)
}

func bytesToSymKey(raw []byte) (symKey, error) {
	switch len(raw) {
	case 32:
		return symKey{enc: raw}, nil
	case 64:
		return symKey{enc: raw[:32], mac: raw[32:]}, nil
	default:
		return symKey{}, fmt.Errorf("bitwarden: unexpected key length %d", len(raw))
	}
}

// stretchMasterKey HKDF-Expands the 32-byte master key into a 64-byte stretched
// key (32 enc + 32 mac) that decrypts profile.Key.
func stretchMasterKey(mk []byte) symKey {
	return symKey{
		enc: hkdfExpand(mk, "enc", 32),
		mac: hkdfExpand(mk, "mac", 32),
	}
}

func hkdfExpand(prk []byte, info string, length int) []byte {
	r := hkdf.Expand(sha256.New, prk, []byte(info))
	out := make([]byte, length)
	_, _ = io.ReadFull(r, out)
	return out
}

// decryptRSAPrivateKey decrypts the user's RSA private key (enc by userKey) and
// parses it (PKCS8 DER).
func decryptRSAPrivateKey(encPriv string, userKey symKey) (*rsa.PrivateKey, error) {
	der, err := decryptSym(encPriv, userKey)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("bitwarden: profile private key is not RSA")
	}
	return rk, nil
}

// unwrapOrgKey RSA-OAEP-decrypts an org key with the user's private key.
func unwrapOrgKey(encOrgKey string, priv *rsa.PrivateKey) (symKey, error) {
	typ, _, ct, _, err := parseEncString(encOrgKey)
	if err != nil {
		return symKey{}, err
	}
	var h hash.Hash
	switch typ {
	case encRsa2048_OaepSha256_B64, encRsa2048_OaepSha256_HmacB64:
		h = sha256.New()
	case encRsa2048_OaepSha1_B64, encRsa2048_OaepSha1_HmacB64:
		h = sha1.New()
	default:
		return symKey{}, fmt.Errorf("bitwarden: org key is not an RSA enc type (%d)", typ)
	}
	raw, err := rsa.DecryptOAEP(h, nil, priv, ct, nil)
	if err != nil {
		return symKey{}, err
	}
	return bytesToSymKey(raw)
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("bitwarden: empty plaintext")
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > blockSize || pad > len(b) {
		return nil, errors.New("bitwarden: bad PKCS7 padding")
	}
	if !bytes.Equal(b[len(b)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("bitwarden: bad PKCS7 padding bytes")
	}
	return b[:len(b)-pad], nil
}
