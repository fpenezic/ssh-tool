// Package totp generates RFC 6238 time-based one-time codes from the secret
// forms the external password managers store: an otpauth:// URI (Bitwarden,
// KeePassXC's "otp" attribute) or a bare base32 secret (Bitwarden accepts
// both). Steam's variant is not supported.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed TOTP configuration.
type Spec struct {
	Secret    []byte
	Digits    int
	Period    int    // seconds
	Algorithm string // "SHA1" | "SHA256" | "SHA512"
}

// ErrUnsupported marks a secret form this package cannot turn into codes.
// Parse errors are constant text built on ErrUnsupported: they never quote
// the input, which carries the secret, because callers log them.
var ErrUnsupported = errors.New("totp: unsupported secret")

// Parse accepts an otpauth://totp/ URI or a bare base32 secret (spaces,
// dashes, lower case and missing padding tolerated).
func Parse(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, fmt.Errorf("%w: empty", ErrUnsupported)
	}
	if strings.HasPrefix(strings.ToLower(s), "otpauth://") {
		return parseURI(s)
	}
	if strings.HasPrefix(strings.ToLower(s), "steam://") {
		return Spec{}, fmt.Errorf("%w: steam codes", ErrUnsupported)
	}
	key, err := decodeBase32(s)
	if err != nil {
		return Spec{}, err
	}
	return Spec{Secret: key, Digits: 6, Period: 30, Algorithm: "SHA1"}, nil
}

func parseURI(s string) (Spec, error) {
	u, err := url.Parse(s)
	if err != nil {
		// Not the url error: it quotes the whole URI, secret included,
		// and callers log Parse errors.
		return Spec{}, fmt.Errorf("%w: malformed otpauth URI", ErrUnsupported)
	}
	if !strings.EqualFold(u.Host, "totp") {
		return Spec{}, fmt.Errorf("%w: otpauth type is not totp", ErrUnsupported)
	}
	q := u.Query()
	key, err := decodeBase32(q.Get("secret"))
	if err != nil {
		return Spec{}, err
	}
	sp := Spec{Secret: key, Digits: 6, Period: 30, Algorithm: "SHA1"}
	if d := q.Get("digits"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 6 || n > 10 {
			return Spec{}, fmt.Errorf("%w: digits out of range", ErrUnsupported)
		}
		sp.Digits = n
	}
	if p := q.Get("period"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 3600 {
			return Spec{}, fmt.Errorf("%w: period out of range", ErrUnsupported)
		}
		sp.Period = n
	}
	if a := q.Get("algorithm"); a != "" {
		a = strings.ToUpper(a)
		if a != "SHA1" && a != "SHA256" && a != "SHA512" {
			return Spec{}, fmt.Errorf("%w: unknown algorithm", ErrUnsupported)
		}
		sp.Algorithm = a
	}
	return sp, nil
}

func decodeBase32(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '\t', '=':
			return -1
		}
		return r
	}, strings.ToUpper(s))
	if clean == "" {
		return nil, fmt.Errorf("%w: no secret", ErrUnsupported)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: secret is not base32", ErrUnsupported)
	}
	return key, nil
}

// Code returns the code valid at t.
func (sp Spec) Code(t time.Time) string {
	var h func() hash.Hash
	switch sp.Algorithm {
	case "SHA256":
		h = sha256.New
	case "SHA512":
		h = sha512.New
	default:
		h = sha1.New
	}
	period := sp.Period
	if period <= 0 {
		period = 30
	}
	digits := sp.Digits
	if digits <= 0 {
		digits = 6
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix())/uint64(period))
	m := hmac.New(h, sp.Secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint64(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, uint64(bin)%mod)
}

// Remaining is how many seconds the code at t stays valid.
func (sp Spec) Remaining(t time.Time) int {
	period := sp.Period
	if period <= 0 {
		period = 30
	}
	return period - int(t.Unix()%int64(period))
}

// FromKeePassFields builds a secret string from KeePass 2's native TOTP
// fields (TimeOtp-Secret-Base32, -Length, -Period, -Algorithm). get returns
// "" for a missing field. The result feeds Parse; "" means none configured.
func FromKeePassFields(get func(name string) string) string {
	secret := get("TimeOtp-Secret-Base32")
	if secret == "" {
		return ""
	}
	q := url.Values{}
	q.Set("secret", secret)
	if v := get("TimeOtp-Length"); v != "" {
		q.Set("digits", v)
	}
	if v := get("TimeOtp-Period"); v != "" {
		q.Set("period", v)
	}
	if v := get("TimeOtp-Algorithm"); v != "" {
		// KeePass writes HMAC-SHA-1 / HMAC-SHA-256 / HMAC-SHA-512.
		q.Set("algorithm", strings.ReplaceAll(strings.TrimPrefix(strings.ToUpper(v), "HMAC-"), "-", ""))
	}
	return "otpauth://totp/keepass?" + q.Encode()
}
