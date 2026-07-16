package bitwarden

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"testing"

	"golang.org/x/crypto/pbkdf2"
	cssh "golang.org/x/crypto/ssh"
)

// This file builds a synthetic Bitwarden sync payload with known keys so the
// decrypt path can be tested without a live server. It mirrors the server's own
// encryption: PBKDF2 master key -> stretched key -> userKey; RSA keypair whose
// public key wraps an org key; ciphers encrypted with the right key.

const (
	testEmail    = "user@example.com"
	testMaster   = "correct horse battery staple"
	testPBKDF2It = 600000
)

// encSym encrypts plaintext as an AES-256-CBC + HMAC-SHA256 EncString (type 2).
func encSym(t *testing.T, plaintext []byte, key symKey) string {
	t.Helper()
	block, err := aes.NewCipher(key.enc)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, block.BlockSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	padded := pkcs7Pad(plaintext, block.BlockSize())
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	h := hmac.New(sha256.New, key.mac)
	h.Write(iv)
	h.Write(ct)
	mac := h.Sum(nil)
	return fmt.Sprintf("2.%s|%s|%s",
		base64.StdEncoding.EncodeToString(iv),
		base64.StdEncoding.EncodeToString(ct),
		base64.StdEncoding.EncodeToString(mac),
	)
}

func encStr(t *testing.T, s string, key symKey) string { return encSym(t, []byte(s), key) }

// encRSA wraps plaintext (a key) to a public key as an RSA-OAEP-SHA1 EncString
// (type 4, the org-key form Vaultwarden uses).
func encRSA(t *testing.T, plaintext []byte, pub *rsa.PublicKey) string {
	t.Helper()
	ct, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}
	return "4." + base64.StdEncoding.EncodeToString(ct)
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	pad := blockSize - len(b)%blockSize
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func randKey(t *testing.T) symKey {
	t.Helper()
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return symKey{enc: raw[:32], mac: raw[32:]}
}

// fixture holds the synthetic payload plus the ids/values a test asserts on.
type fixture struct {
	sync        []byte
	orgID       string
	collID      string
	personalCID string // login item, personal
	orgCID      string // login item, org+collection
	sshCID      string // ssh-key item, org
	customField string
	customValue string
	sshKeyPEM   string // the PEM the ssh item resolves to
}

// genSSHKeyPEM returns a real, parseable ed25519 private key in PEM form.
func genSSHKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := cssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

// buildFixture creates a sync payload: one personal login, one org login in a
// collection, one org SSH-key item, all decryptable with testMaster.
func buildFixture(t *testing.T) fixture {
	t.Helper()

	// Master key -> stretched -> userKey (random 64B, wrapped by stretched key).
	mk := pbkdf2.Key([]byte(testMaster), []byte(testEmail), testPBKDF2It, 32, sha256.New)
	stretched := stretchMasterKey(mk)
	userKey := randKey(t)
	userKeyRaw := append(append([]byte{}, userKey.enc...), userKey.mac...)
	profileKey := encSym(t, userKeyRaw, stretched)

	// RSA keypair for the account; private key wrapped by userKey.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	encPriv := encSym(t, pkcs8, userKey)

	// Org key (random 64B) wrapped to the account public key.
	orgKey := randKey(t)
	orgKeyRaw := append(append([]byte{}, orgKey.enc...), orgKey.mac...)
	encOrgKey := encRSA(t, orgKeyRaw, &rsaKey.PublicKey)

	orgID := "org-1"
	collID := "coll-1"

	f := fixture{
		orgID:       orgID,
		collID:      collID,
		personalCID: "cipher-personal",
		orgCID:      "cipher-org",
		sshCID:      "cipher-ssh",
		customField: "API-KEY",
		customValue: "custom-secret-value",
		sshKeyPEM:   genSSHKeyPEM(t),
	}

	sr := syncResp{
		Profile: profile{
			Email:         testEmail,
			Key:           profileKey,
			PrivateKey:    encPriv,
			Kdf:           kdfPBKDF2,
			KdfIterations: testPBKDF2It,
			Organizations: []organization{{ID: orgID, Name: "Acme", Key: encOrgKey}},
		},
		Collections: []collection{
			{ID: collID, OrganizationID: orgID, Name: encStr(t, "Infra", orgKey)},
		},
		Ciphers: []cipherItem{
			{
				ID:   f.personalCID,
				Type: cipherTypeLogin,
				Name: encStr(t, "Personal Host", userKey),
				Login: &loginData{
					Username: encStr(t, "root", userKey),
					Password: encStr(t, "personal-pass", userKey),
				},
				Fields: []fieldData{
					{Name: encStr(t, f.customField, userKey), Value: encStr(t, f.customValue, userKey)},
				},
			},
			{
				ID:             f.orgCID,
				OrganizationID: orgID,
				CollectionIDs:  []string{collID},
				Type:           cipherTypeLogin,
				Name:           encStr(t, "Org Host", orgKey),
				Login: &loginData{
					Username: encStr(t, "deploy", orgKey),
					Password: encStr(t, "org-pass", orgKey),
				},
			},
			{
				ID:             f.sshCID,
				OrganizationID: orgID,
				Type:           cipherTypeSSHKey,
				Name:           encStr(t, "Org SSH Key", orgKey),
				SSHKey: &sshKeyData{
					PrivateKey: encStr(t, f.sshKeyPEM, orgKey),
					PublicKey:  encStr(t, "ssh-ed25519 AAAA...", orgKey),
				},
			},
		},
	}

	raw, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	f.sync = raw
	return f
}
