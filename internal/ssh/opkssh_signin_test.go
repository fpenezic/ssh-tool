package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
)

// fakeLogin stands in for the browser sign-in: it issues a real key and a
// cert valid for a day, and counts how often it was asked.
func fakeLogin(t *testing.T, calls *atomic.Int32, delay time.Duration) func(context.Context, *OpksshConfig) ([]byte, []byte, error) {
	t.Helper()
	_, caKey, _ := ed25519.GenerateKey(rand.Reader)
	ca, err := ssh.NewSignerFromKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, _ *OpksshConfig) ([]byte, []byte, error) {
		calls.Add(1)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		sshPub, _ := ssh.NewPublicKey(pub)
		now := time.Now()
		cert := &ssh.Certificate{
			Key:             sshPub,
			CertType:        ssh.UserCert,
			ValidPrincipals: []string{"user"},
			ValidAfter:      uint64(now.Add(-time.Minute).Unix()),
			ValidBefore:     uint64(now.Add(24 * time.Hour).Unix()),
		}
		if err := cert.SignCert(rand.Reader, ca); err != nil {
			return nil, nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "")
		if err != nil {
			return nil, nil, err
		}
		return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(cert), nil
	}
}

func signInFixture(t *testing.T, delay time.Duration) (*OpksshConfig, *creds.Vault, *atomic.Int32) {
	t.Helper()
	v := creds.NewVault()
	v.SetPath(filepath.Join(t.TempDir(), "vault.enc"))
	if err := v.Init("pass", false); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	prev := opksshLogin
	opksshLogin = fakeLogin(t, calls, delay)
	t.Cleanup(func() { opksshLogin = prev })
	cfg := &OpksshConfig{CredentialID: "signin-" + t.Name(), MaxCertAgeSeconds: 3600, MinRemainingBeforeRefreshMinutes: 60}
	return cfg, v, calls
}

func TestSignInNowSignsInOverAGoodCert(t *testing.T) {
	cfg, v, calls := signInFixture(t, 0)
	ctx := context.Background()
	if _, err := EnsureFreshCert(ctx, cfg, v, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureFreshCert(ctx, cfg, v, nil); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a connect with a good cert signed in again: %d logins", n)
	}
	if err := SignInNow(ctx, cfg, v); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("Sign in now did not sign in over a good cert: %d logins", n)
	}
}

// Two Sign in now clicks (or a click racing a connect's own login) must
// not open two browser tabs: the one that queued inherits the result.
func TestSignInNowQueuedBehindALoginDoesNotRepeatIt(t *testing.T) {
	cfg, v, calls := signInFixture(t, 200*time.Millisecond)
	ctx := context.Background()
	done := make(chan error, 2)
	go func() { done <- SignInNow(ctx, cfg, v) }()
	time.Sleep(50 * time.Millisecond)
	go func() { done <- SignInNow(ctx, cfg, v) }()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("queued sign-in repeated the login: %d logins", n)
	}
}

func TestCancelSignInEndsIt(t *testing.T) {
	cfg, v, _ := signInFixture(t, 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- SignInNow(context.Background(), cfg, v) }()
	time.Sleep(50 * time.Millisecond)
	CancelSignIn(cfg.CredentialID)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled sign-in reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CancelSignIn did not end the sign-in")
	}
}
