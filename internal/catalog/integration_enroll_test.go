package catalog

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Enrollment codes against the dev server (PROTOCOL 10.5): bob mints a
// code over the API, a fresh client inspects it (not consumed), redeems
// it once with a device name, and the new session works on its own.
func TestIntegrationEnrollment(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	bob, _ := signedIn(t, base, "bob")
	ctx := context.Background()
	mint := func() (id, code string) {
		m := api(t, bob, "POST", "/api/v1/enrollments", "", nil)
		return m["id"].(string), m["code"].(string)
	}

	id, code := mint()
	toks := &memTokens{}
	c, err := NewClient(base, "ssh-tool/test", toks)
	if err != nil {
		t.Fatal(err)
	}
	c.DeviceName = "it-laptop"
	for i := 0; i < 2; i++ { // inspecting does not use it up
		e, err := c.InspectEnrollment(ctx, code)
		if err != nil || e.User.Name == "" || e.ExpiresIn <= 0 {
			t.Fatalf("inspect %d: %+v %v", i, e, err)
		}
	}
	if err := c.RedeemEnrollment(ctx, NormaliseCode(code)); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if rt, _ := toks.Load(); rt == "" {
		t.Fatal("no refresh token stored")
	}
	w, err := c.WhoAmI(ctx)
	if err != nil || w.Name == "" {
		t.Fatalf("whoami with the new session: %+v %v", w, err)
	}
	// Single use.
	other, _ := NewClient(base, "ssh-tool/test", &memTokens{})
	if err := other.RedeemEnrollment(ctx, code); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("second redemption: %v", err)
	}
	if _, err := other.InspectEnrollment(ctx, code); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("inspect after redemption: %v", err)
	}
	// The minter sees who redeemed it.
	st := api(t, bob, "GET", "/api/v1/enrollments/"+id, "", nil)
	if st["status"] != "redeemed" || st["device_name"] != "it-laptop" {
		t.Fatalf("status: %v", st)
	}
	// A revoked code is invalid.
	id2, code2 := mint()
	api(t, bob, "DELETE", "/api/v1/enrollments/"+id2, "", nil)
	if _, err := other.InspectEnrollment(ctx, code2); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("revoked code: %v", err)
	}
	// Ending the new session leaves bob's own running.
	_ = c.Revoke(ctx)
	if _, err := bob.WhoAmI(ctx); err != nil {
		t.Fatalf("bob's session ended with the enrolled one: %v", err)
	}
}
