package ssh

import (
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// An otp_ref that is an unbound catalog slot must not leave the auth
// credential's own TOTP in place: the user types the code instead.
func TestApplyOTPRefCatalogMarkerDropsBorrowedCode(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := TOTPSecretHook
	TOTPSecretHook = func(*store.CredentialRef) string { return "from-hook" }
	defer func() { TOTPSecretHook = old }()
	ref := store.CatalogUnboundRef("cat", "otp-slot")
	m := applyOTPRef(db, &store.ResolvedSettings{OTPRef: &ref}, &AuthMaterial{TOTP: "auth-credential-item"})
	if m.TOTP != "" {
		t.Fatalf("borrowed one-time code kept: %q", m.TOTP)
	}
}
