// Package bitwarden reads secrets live out of a Vaultwarden / Bitwarden server.
//
// It is the HTTP-backed sibling of internal/keepass (gotcha 33): a credential
// stays password/key with StorageMode=external and a config_json.bitwarden_ref,
// and the secret is decrypted from the server's vault at connect time, never
// copied into the local vault. Login uses an API key (client_credentials); the
// master password is used only as the KDF input to decrypt the vault contents
// and is never sent to the server.
//
// The package has no app-internal imports so it stays unit-testable. The crypto
// implements the Bitwarden EncString scheme: AES-256-CBC with an HMAC-SHA256
// tag, HKDF key stretching, RSA-OAEP org-key unwrap, and PBKDF2 / Argon2id KDFs.
package bitwarden

// KDF types (the server's Profile.Kdf field).
const (
	kdfPBKDF2   = 0
	kdfArgon2id = 1
)

// EncString types (the leading "N." in a Bitwarden encrypted string).
const (
	encAesCbc256_B64              = 0 // iv|ct, no MAC (legacy)
	encAesCbc128_HmacSha256_B64   = 1
	encAesCbc256_HmacSha256_B64   = 2 // the common symmetric type
	encRsa2048_OaepSha256_B64     = 3
	encRsa2048_OaepSha1_B64       = 4 // org-key unwrap in practice
	encRsa2048_OaepSha256_HmacB64 = 5
	encRsa2048_OaepSha1_HmacB64   = 6
)

// Bitwarden cipher item types.
const (
	cipherTypeLogin      = 1
	cipherTypeSecureNote = 2
	cipherTypeCard       = 3
	cipherTypeIdentity   = 4
	cipherTypeSSHKey     = 5
)

// ---- wire types (subset of /api/sync and /identity/connect/token) ----

type tokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// syncResp is the decoded /api/sync payload. Bitwarden capitalizes JSON keys.
type syncResp struct {
	Profile     profile       `json:"Profile"`
	Ciphers     []cipherItem  `json:"Ciphers"`
	Collections []collection  `json:"Collections"`
}

type profile struct {
	Email          string         `json:"Email"`
	Key            string         `json:"Key"`        // userKey, enc by stretched master key
	PrivateKey     string         `json:"PrivateKey"` // RSA private key, enc by userKey
	Kdf            int            `json:"Kdf"`
	KdfIterations  int            `json:"KdfIterations"`
	KdfMemory      int            `json:"KdfMemory"`      // MiB
	KdfParallelism int            `json:"KdfParallelism"`
	Organizations  []organization `json:"Organizations"`
}

type organization struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
	Key  string `json:"Key"` // org key, RSA-enc to the user's public key
}

type collection struct {
	ID             string `json:"Id"`
	OrganizationID string `json:"OrganizationId"`
	Name           string `json:"Name"` // enc by org key
}

type cipherItem struct {
	ID              string          `json:"Id"`
	OrganizationID  string          `json:"OrganizationId"`
	Type            int             `json:"Type"`
	Name            string          `json:"Name"`
	Login           *loginData      `json:"Login"`
	SSHKey          *sshKeyData     `json:"SshKey"`
	Fields          []fieldData     `json:"Fields"`
	Attachments     []attachmentRef `json:"Attachments"`
	CollectionIDs   []string        `json:"CollectionIds"`
}

type loginData struct {
	Username string `json:"Username"`
	Password string `json:"Password"`
	Totp     string `json:"Totp"`
}

type sshKeyData struct {
	PrivateKey     string `json:"PrivateKey"`
	PublicKey      string `json:"PublicKey"`
	KeyFingerprint string `json:"KeyFingerprint"`
}

type fieldData struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
	Type  int    `json:"Type"`
}

type attachmentRef struct {
	ID       string `json:"Id"`
	FileName string `json:"FileName"`
	Key      string `json:"Key"`
	URL      string `json:"Url"`
}
