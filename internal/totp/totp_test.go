package totp

import (
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B vectors (8 digits), one secret per algorithm.
func TestRFC6238Vectors(t *testing.T) {
	enc := func(s string) string { return base32.StdEncoding.EncodeToString([]byte(s)) }
	seeds := map[string]string{
		"SHA1":   enc("12345678901234567890"),
		"SHA256": enc("12345678901234567890123456789012"),
		"SHA512": enc("1234567890123456789012345678901234567890123456789012345678901234"),
	}
	cases := []struct {
		unix int64
		want map[string]string
	}{
		{59, map[string]string{"SHA1": "94287082", "SHA256": "46119246", "SHA512": "90693936"}},
		{1111111109, map[string]string{"SHA1": "07081804", "SHA256": "68084774", "SHA512": "25091201"}},
		{1234567890, map[string]string{"SHA1": "89005924", "SHA256": "91819424", "SHA512": "93441116"}},
		{20000000000, map[string]string{"SHA1": "65353130", "SHA256": "77737706", "SHA512": "47863826"}},
	}
	for alg, seed := range seeds {
		sp, err := Parse("otpauth://totp/x?secret=" + seed + "&digits=8&algorithm=" + alg)
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		for _, c := range cases {
			if got := sp.Code(time.Unix(c.unix, 0)); got != c.want[alg] {
				t.Errorf("%s @%d = %s, want %s", alg, c.unix, got, c.want[alg])
			}
		}
	}
}

func TestParseBareSecret(t *testing.T) {
	// Lower case, spaces and no padding, as people paste them.
	sp, err := Parse("gezd gnbv gy3t qojq gezd gnbv gy3t qojq")
	if err != nil {
		t.Fatal(err)
	}
	if sp.Digits != 6 || sp.Period != 30 || sp.Algorithm != "SHA1" {
		t.Fatalf("defaults = %+v", sp)
	}
	if got := sp.Code(time.Unix(59, 0)); got != "287082" {
		t.Fatalf("code = %s", got)
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"steam://ABC",
		"otpauth://hotp/x?secret=GEZDGNBV",
		"otpauth://totp/x?secret=GEZDGNBV&digits=4",
		"otpauth://totp/x?secret=GEZDGNBV&algorithm=MD5",
		"otpauth://totp/x",
		"not base32 !!",
	} {
		if _, err := Parse(s); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Parse(%q) err = %v, want ErrUnsupported", s, err)
		}
	}
}

func TestRemaining(t *testing.T) {
	sp := Spec{Period: 60}
	if r := sp.Remaining(time.Unix(125, 0)); r != 55 {
		t.Fatalf("remaining = %d", r)
	}
}

func TestFromKeePassFields(t *testing.T) {
	fields := map[string]string{
		"TimeOtp-Secret-Base32": "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
		"TimeOtp-Length":        "8",
		"TimeOtp-Algorithm":     "HMAC-SHA-1",
	}
	s := FromKeePassFields(func(n string) string { return fields[n] })
	sp, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := sp.Code(time.Unix(59, 0)); got != "94287082" {
		t.Fatalf("code = %s", got)
	}
	if FromKeePassFields(func(string) string { return "" }) != "" {
		t.Fatal("no secret should give no URI")
	}
}

// Parse errors end up in the app log, so none may carry the secret.
func TestParseErrorsDoNotQuoteTheSecret(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, in := range []string{
		"otpauth://totp/x?secret=" + secret + "&digits=12",
		"otpauth://totp/x?secret=" + secret + "&period=-5",
		"otpauth://totp/x?secret=" + secret + "&algorithm=MD5",
		"otpauth://hotp/" + secret + "?secret=" + secret,
		"otpauth://totp/%zz?secret=" + secret,
	} {
		_, err := Parse(in)
		if err == nil {
			t.Fatalf("%q parsed", in)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks the secret: %v", err)
		}
	}
}
