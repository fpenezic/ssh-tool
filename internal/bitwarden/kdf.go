package bitwarden

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

// deriveMasterKey turns a master password + email into the 32-byte master key,
// using the KDF the server records for this account (PBKDF2 or Argon2id). The
// email (lowercased, trimmed) is the salt.
func deriveMasterKey(password, email string, p profile) ([]byte, error) {
	salt := strings.ToLower(strings.TrimSpace(email))
	switch p.Kdf {
	case kdfPBKDF2:
		iter := p.KdfIterations
		if iter <= 0 {
			iter = 600000
		}
		return pbkdf2.Key([]byte(password), []byte(salt), iter, 32, sha256.New), nil
	case kdfArgon2id:
		iter := uint32(p.KdfIterations)
		if iter == 0 {
			iter = 3
		}
		mem := uint32(p.KdfMemory) * 1024 // server stores MiB; argon2 wants KiB
		if mem == 0 {
			mem = 64 * 1024
		}
		par := uint8(p.KdfParallelism)
		if par == 0 {
			par = 4
		}
		// Bitwarden salts Argon2 with SHA256(email), not the raw email.
		saltHash := sha256.Sum256([]byte(salt))
		return argon2.IDKey([]byte(password), saltHash[:], iter, mem, par, 32), nil
	default:
		return nil, fmt.Errorf("bitwarden: unknown KDF type %d", p.Kdf)
	}
}

func kdfName(k int) string {
	if k == kdfArgon2id {
		return "argon2id"
	}
	return "pbkdf2"
}
