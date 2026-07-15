// Package keepass reads secrets out of a KeePass .kdbx database, read-only.
//
// KeePass stays the source of truth: ssh-tool never writes to the file. A
// credential in the tree carries a reference (db id + entry UUID + field name),
// and the resolver pulls the secret at connect time, in memory only, for the
// lifetime of that one connection. The decrypted database is held in memory and
// wiped when the vault locks - same lifecycle and threat model as the vault.
//
// The parser is pure Go (github.com/tobischo/gokeepasslib/v3), so this builds
// on the CGO_ENABLED=0 desktop stack and on android (gotcha 19) with no binary
// dependency and no platform branch.
package keepass

import (
	"bytes"
	"errors"
	"fmt"

	kp "github.com/tobischo/gokeepasslib/v3"
)

// ErrEntryNotFound is returned by Resolve when the referenced UUID no longer
// exists in the database (the entry was deleted, or the file was swapped for a
// different database). The resolver turns this into an actionable message
// rather than a generic auth failure. Rename/move keep the UUID stable; only
// deletion drops it.
var ErrEntryNotFound = errors.New("keepass: entry not found")

// ErrFieldEmpty is returned when the referenced field (password or an
// attachment) exists on the entry but is empty - a misconfigured reference we
// surface rather than silently authenticating with "".
var ErrFieldEmpty = errors.New("keepass: referenced field is empty")

// FieldPassword is the sentinel field name meaning "the entry's Password
// value". Any other field name is looked up first as a String value, then as
// an attachment (binary) key - covering both the "private key pasted into a
// custom field" and the "private key added as an attachment" KeePass patterns.
const FieldPassword = "password"

// DB is an unlocked, in-memory KeePass database. It is read-only: no method
// mutates the file. Safe for concurrent Resolve calls (the underlying tree is
// never mutated after Open).
type DB struct {
	db *kp.Database
}

// Open decrypts a .kdbx from its raw bytes using a master password and/or a key
// file. Either may be empty, but not both. The decrypted content lives only in
// the returned DB (and the caller's original blob, which the caller owns and
// should keep encrypted at rest).
func Open(raw, keyFile []byte, master string) (*DB, error) {
	if master == "" && len(keyFile) == 0 {
		return nil, errors.New("keepass: need a master password or a key file")
	}

	var creds *kp.DBCredentials
	var err error
	switch {
	case len(keyFile) > 0 && master != "":
		creds, err = kp.NewPasswordAndKeyDataCredentials(master, keyFile)
	case len(keyFile) > 0:
		creds, err = kp.NewKeyDataCredentials(keyFile)
	default:
		creds = kp.NewPasswordCredentials(master)
	}
	if err != nil {
		return nil, fmt.Errorf("keepass: build credentials: %w", err)
	}

	database := kp.NewDatabase()
	database.Credentials = creds
	if err := kp.NewDecoder(bytes.NewReader(raw)).Decode(database); err != nil {
		// A wrong master password surfaces here as an HMAC / decrypt failure.
		return nil, fmt.Errorf("keepass: decrypt (wrong password or key file?): %w", err)
	}
	if err := database.UnlockProtectedEntries(); err != nil {
		return nil, fmt.Errorf("keepass: unlock protected entries: %w", err)
	}
	return &DB{db: database}, nil
}

// Resolve looks up an entry by UUID and returns the requested field's secret.
// field == FieldPassword returns the entry's Password. Any other field is tried
// as a String value first, then as an attachment (binary) key.
func (d *DB) Resolve(entryUUID, field string) (string, error) {
	e := d.findEntry(entryUUID)
	if e == nil {
		return "", ErrEntryNotFound
	}
	if field == "" || field == FieldPassword {
		pw := e.GetPassword()
		if pw == "" {
			return "", ErrFieldEmpty
		}
		return pw, nil
	}
	// A custom String value (e.g. a PEM pasted into a field named "PrivateKey").
	if v := e.Get(field); v != nil {
		if v.Value.Content == "" {
			return "", ErrFieldEmpty
		}
		return v.Value.Content, nil
	}
	// An attachment whose Key matches the field name.
	for _, ref := range e.Binaries {
		if ref.Name != field {
			continue
		}
		bin := d.db.FindBinary(ref.Value.ID)
		if bin == nil {
			return "", ErrEntryNotFound
		}
		content, err := bin.GetContentBytes()
		if err != nil {
			return "", fmt.Errorf("keepass: read attachment %q: %w", field, err)
		}
		if len(content) == 0 {
			return "", ErrFieldEmpty
		}
		return string(content), nil
	}
	return "", fmt.Errorf("keepass: entry has no field or attachment named %q", field)
}

// findEntry walks every group depth-first and returns the entry matching the
// base64-encoded UUID, or nil. gokeepasslib's UUID marshals to base64 text, so
// the reference we store (and the picker surfaces) is that base64 string.
func (d *DB) findEntry(b64 string) *kp.Entry {
	want, err := uuidFromB64(b64)
	if err != nil {
		return nil
	}
	var walk func(groups []kp.Group) *kp.Entry
	walk = func(groups []kp.Group) *kp.Entry {
		for gi := range groups {
			g := &groups[gi]
			for ei := range g.Entries {
				if g.Entries[ei].UUID.Compare(want) {
					return &g.Entries[ei]
				}
			}
			if hit := walk(g.Groups); hit != nil {
				return hit
			}
		}
		return nil
	}
	return walk(d.db.Content.Root.Groups)
}

func uuidFromB64(b64 string) (kp.UUID, error) {
	var u kp.UUID
	err := u.UnmarshalText([]byte(b64))
	return u, err
}
