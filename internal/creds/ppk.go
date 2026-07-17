package creds

// PuTTY .ppk private-key support. ssh-tool stores and uses keys in OpenSSH / PEM
// form (x/crypto/ssh parses those natively); PuTTY's own .ppk format is not
// understood downstream. Rather than teach the resolve/connect paths about .ppk,
// we CONVERT a .ppk to an OpenSSH PEM at import time and store that - one format
// in the vault, no .ppk residue anywhere else.

import (
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/kayrus/putty"
	"golang.org/x/crypto/ssh"
)

// ppkHeader is the first line marker of both PPK v2 and v3.
const ppkHeader = "PuTTY-User-Key-File-"

// IsPPK reports whether data looks like a PuTTY .ppk private key (v2 or v3), by
// its leading header. Cheap sniff used by the import paths to branch.
func IsPPK(data []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(data)), ppkHeader)
}

// ConvertPPKToOpenSSH parses a PuTTY .ppk key and re-marshals it to an
// unencrypted OpenSSH PEM string. passphrase is required for an encrypted .ppk
// and ignored for an unencrypted one. The resulting key is UNENCRYPTED: the
// vault is the at-rest protection, matching how paste-imported managed keys are
// already stored.
func ConvertPPKToOpenSSH(data []byte, passphrase string) (string, error) {
	key, err := putty.New(data)
	if err != nil {
		return "", fmt.Errorf("parse .ppk: %w", err)
	}

	encrypted := key.Encryption != "" && key.Encryption != "none"
	if encrypted && passphrase == "" {
		return "", fmt.Errorf("this .ppk is encrypted; a passphrase is required")
	}

	var pw []byte
	if encrypted {
		pw = []byte(passphrase)
	}
	raw, err := key.ParseRawPrivateKey(pw)
	if err != nil {
		if encrypted {
			return "", fmt.Errorf("could not decrypt .ppk (wrong passphrase?): %w", err)
		}
		return "", fmt.Errorf("parse .ppk private key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(raw, key.Comment)
	if err != nil {
		return "", fmt.Errorf("re-marshal .ppk to OpenSSH: %w", err)
	}
	return string(pem.EncodeToMemory(block)), nil
}
