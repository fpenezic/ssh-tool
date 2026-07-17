package infisical

import "testing"

func TestSplitSecretRef(t *testing.T) {
	cases := []struct {
		base, rawKey string
		wantPath     string
		wantKey      string
	}{
		{"/", "cloudflare/password", "/cloudflare", "password"},
		{"/", "password", "/", "password"},
		{"/base", "sub/KEY", "/base/sub", "KEY"},
		{"/base/", "/leading/KEY", "/base/leading", "KEY"},
		{"", "a/b/c/KEY", "/a/b/c", "KEY"},
		{"/", "KEY", "/", "KEY"},
	}
	for _, c := range cases {
		gotPath, gotKey := splitSecretRef(c.base, c.rawKey)
		if gotPath != c.wantPath || gotKey != c.wantKey {
			t.Errorf("splitSecretRef(%q,%q) = (%q,%q), want (%q,%q)",
				c.base, c.rawKey, gotPath, gotKey, c.wantPath, c.wantKey)
		}
	}
}

func TestNormPath(t *testing.T) {
	cases := map[string]string{
		"":            "/",
		"/":           "/",
		"cloudflare":  "/cloudflare",
		"/cloudflare": "/cloudflare",
		"/a/b/":       "/a/b",
		"  /x  ":      "/x",
	}
	for in, want := range cases {
		if got := normPath(in); got != want {
			t.Errorf("normPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikePEMKey(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
	if !looksLikePEMKey(pem) {
		t.Error("PEM key not detected")
	}
	if looksLikePEMKey("just a password") {
		t.Error("plain password misdetected as key")
	}
}
