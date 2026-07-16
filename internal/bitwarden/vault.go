package bitwarden

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Field names a resolvable value inside a cipher.
const (
	FieldPassword   = "password"
	FieldUsername   = "username"
	FieldTotp       = "totp"
	FieldPrivateKey = "privatekey" // SSH-key item private key
)

var (
	// ErrCipherNotFound means the referenced item is not in the synced vault.
	ErrCipherNotFound = errors.New("bitwarden: cipher not found")
	// ErrFieldEmpty means the referenced field exists but has no value.
	ErrFieldEmpty = errors.New("bitwarden: field is empty")
	// ErrWrongMaster means the master password failed to decrypt the user key.
	ErrWrongMaster = errors.New("bitwarden: wrong master password")
)

// Vault is a decrypted-in-memory view of one server's sync payload. It holds the
// user key and every unwrapped org key, plus the raw ciphers/collections/orgs so
// Resolve and Browse work without re-decrypting keys per call. Never persisted.
type Vault struct {
	userKey     symKey
	orgKeys     map[string]symKey // orgID -> key
	ciphers     []cipherItem
	collections []collection
	orgs        []organization
	kdf         int
}

// OpenVault derives keys from the master password and the sync payload, then
// returns a decrypted Vault. rawSync is the JSON body of /api/sync.
func OpenVault(rawSync []byte, master string) (*Vault, error) {
	var sr syncResp
	if err := json.Unmarshal(rawSync, &sr); err != nil {
		return nil, fmt.Errorf("bitwarden: decode sync: %w", err)
	}
	return openVaultFrom(&sr, master)
}

func openVaultFrom(sr *syncResp, master string) (*Vault, error) {
	mk, err := deriveMasterKey(master, sr.Profile.Email, sr.Profile)
	if err != nil {
		return nil, err
	}
	stretched := stretchMasterKey(mk)
	userKey, err := decryptToSymKey(sr.Profile.Key, stretched)
	if err != nil {
		// A HMAC/padding failure here almost always means a wrong master pass.
		return nil, ErrWrongMaster
	}

	v := &Vault{
		userKey:     userKey,
		orgKeys:     map[string]symKey{},
		ciphers:     sr.Ciphers,
		collections: sr.Collections,
		orgs:        sr.Profile.Organizations,
		kdf:         sr.Profile.Kdf,
	}

	if len(sr.Profile.Organizations) > 0 && sr.Profile.PrivateKey != "" {
		priv, err := decryptRSAPrivateKey(sr.Profile.PrivateKey, userKey)
		if err != nil {
			return nil, fmt.Errorf("bitwarden: decrypt account private key: %w", err)
		}
		for _, org := range sr.Profile.Organizations {
			ok, err := unwrapOrgKey(org.Key, priv)
			if err != nil {
				// Skip an org we can't unwrap rather than failing the whole open;
				// its ciphers will report a missing key at resolve/browse time.
				continue
			}
			v.orgKeys[org.ID] = ok
		}
	}
	return v, nil
}

// Forget zeroes all key material.
func (v *Vault) Forget() {
	v.userKey.zero()
	for id, k := range v.orgKeys {
		k.zero()
		delete(v.orgKeys, id)
	}
}

// keyFor returns the symmetric key that decrypts a cipher: the org key when the
// cipher belongs to an organization, else the user key.
func (v *Vault) keyFor(c *cipherItem) (symKey, error) {
	if c.OrganizationID == "" {
		return v.userKey, nil
	}
	k, ok := v.orgKeys[c.OrganizationID]
	if !ok {
		return symKey{}, fmt.Errorf("bitwarden: no key for organization %s", c.OrganizationID)
	}
	return k, nil
}

// decStr decrypts an EncString with a given key, tolerating an empty input.
func decStr(s string, key symKey) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := decryptSym(s, key)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (v *Vault) findCipher(id string) *cipherItem {
	for i := range v.ciphers {
		if v.ciphers[i].ID == id {
			return &v.ciphers[i]
		}
	}
	return nil
}

// Resolve decrypts the requested field of a cipher. field is one of the Field
// constants, or a custom field name (matched case-insensitively against the
// cipher's Fields), or an attachment file name (not yet fetched - see manager).
func (v *Vault) Resolve(cipherID, field string) (string, error) {
	c := v.findCipher(cipherID)
	if c == nil {
		return "", ErrCipherNotFound
	}
	key, err := v.keyFor(c)
	if err != nil {
		return "", err
	}
	if field == "" {
		field = FieldPassword
	}

	var enc string
	switch normalizeField(field) {
	case FieldPassword:
		if c.Login != nil {
			enc = c.Login.Password
		}
	case FieldUsername:
		if c.Login != nil {
			enc = c.Login.Username
		}
	case FieldTotp:
		if c.Login != nil {
			enc = c.Login.Totp
		}
	case FieldPrivateKey:
		if c.SSHKey != nil {
			enc = c.SSHKey.PrivateKey
		}
	default:
		// Custom field by name.
		for _, f := range c.Fields {
			name, derr := decStr(f.Name, key)
			if derr != nil {
				continue
			}
			if equalFold(name, field) {
				enc = f.Value
				break
			}
		}
	}
	if enc == "" {
		return "", ErrFieldEmpty
	}
	out, err := decStr(enc, key)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", ErrFieldEmpty
	}
	return out, nil
}
