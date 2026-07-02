package creds

import (
	"encoding/pem"
)

// pemEncode serialises an OpenSSH pem.Block back to PEM text.
func pemEncode(block *pem.Block) []byte {
	return pem.EncodeToMemory(block)
}
