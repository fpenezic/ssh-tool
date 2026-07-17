package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/store"
)

// genKeyPEM returns a real, parseable ed25519 private key in OpenSSH PEM form.
func genKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestExternalAuthMaterialPassword(t *testing.T) {
	cred := &store.CredentialRef{Name: "kp", Kind: store.CredPassword}
	m, err := externalAuthMaterial(cred, "s3cret")
	if err != nil {
		t.Fatalf("externalAuthMaterial: %v", err)
	}
	if m.Password != "s3cret" {
		t.Fatalf("password: got %q", m.Password)
	}
	if len(m.Signers) != 0 {
		t.Fatalf("password cred should have no signers")
	}
	// ToAuthMethods should yield exactly one password method.
	if got := len(m.ToAuthMethods()); got != 1 {
		t.Fatalf("ToAuthMethods: got %d methods, want 1", got)
	}
}

func TestExternalAuthMaterialKey(t *testing.T) {
	pemKey := genKeyPEM(t)
	cred := &store.CredentialRef{Name: "kp-key", Kind: store.CredKey}
	m, err := externalAuthMaterial(cred, pemKey)
	if err != nil {
		t.Fatalf("externalAuthMaterial(key): %v", err)
	}
	if len(m.Signers) != 1 {
		t.Fatalf("key cred should have exactly one signer, got %d", len(m.Signers))
	}
	if m.Password != "" {
		t.Fatalf("key cred should have no password")
	}
	if got := len(m.ToAuthMethods()); got != 1 {
		t.Fatalf("ToAuthMethods: got %d methods, want 1", got)
	}
}

func TestExternalAuthMaterialEmptySecret(t *testing.T) {
	cred := &store.CredentialRef{Name: "kp", Kind: store.CredPassword}
	if _, err := externalAuthMaterial(cred, ""); err == nil {
		t.Fatal("expected an error for an empty secret")
	}
}

func TestExternalAuthMaterialBadKey(t *testing.T) {
	cred := &store.CredentialRef{Name: "kp-key", Kind: store.CredKey}
	if _, err := externalAuthMaterial(cred, "not a pem key"); err == nil {
		t.Fatal("expected an error parsing a non-PEM key")
	}
}

func TestToAuthMethodsOrderAndShape(t *testing.T) {
	// A material with a signer AND a password yields two methods, signer first.
	pemKey := genKeyPEM(t)
	signer, err := ssh.ParsePrivateKey([]byte(pemKey))
	if err != nil {
		t.Fatal(err)
	}
	m := &AuthMaterial{Signers: []ssh.Signer{signer}, Password: "pw"}
	methods := m.ToAuthMethods()
	if len(methods) != 2 {
		t.Fatalf("want 2 methods (publickey, password), got %d", len(methods))
	}

	// Empty material yields no methods.
	if got := len((&AuthMaterial{}).ToAuthMethods()); got != 0 {
		t.Fatalf("empty material: want 0 methods, got %d", got)
	}
}

func TestInteractiveAuthMethodsGating(t *testing.T) {
	// No hook set -> no interactive methods.
	InteractiveAuthHook = nil
	if got := interactiveAuthMethods("t", "h", 22); got != nil {
		t.Fatalf("no hook: want nil, got %d methods", len(got))
	}

	// Hook set -> keyboard-interactive + password callback.
	InteractiveAuthHook = func(label, host string, port int, name, instruction string, prompts []InteractiveAuthPrompt) ([]string, error) {
		return []string{"answer"}, nil
	}
	defer func() { InteractiveAuthHook = nil }()
	methods := interactiveAuthMethods("t", "h", 22)
	if len(methods) != 2 {
		t.Fatalf("with hook: want 2 methods, got %d", len(methods))
	}
}

// TestResolveAuthHookRouting verifies the resolver routes an external-ref
// credential through its hook (before the kind switch) and does NOT touch the
// vault for it. The KeePass hook is tried first, then Bitwarden.
func TestResolveAuthHookRouting(t *testing.T) {
	// Clean slate.
	KeepassResolveHook = nil
	BitwardenResolveHook = nil
	InfisicalResolveHook = nil
	defer func() { KeepassResolveHook = nil; BitwardenResolveHook = nil; InfisicalResolveHook = nil }()

	var keepassCalled, bitwardenCalled, infisicalCalled bool

	// KeePass hook handles a cred whose config carries a keepass marker.
	KeepassResolveHook = func(cred *store.CredentialRef) (string, bool, error) {
		keepassCalled = true
		if _, ok := cred.Config["keepass_ref"]; ok {
			return "kp-secret", true, nil
		}
		return "", false, nil
	}
	BitwardenResolveHook = func(cred *store.CredentialRef) (string, bool, error) {
		bitwardenCalled = true
		if _, ok := cred.Config["bitwarden_ref"]; ok {
			return "bw-secret", true, nil
		}
		return "", false, nil
	}
	InfisicalResolveHook = func(cred *store.CredentialRef) (string, bool, error) {
		infisicalCalled = true
		if _, ok := cred.Config["infisical_ref"]; ok {
			return "inf-secret", true, nil
		}
		return "", false, nil
	}

	// A KeePass-backed password credential (nil vault is safe: the hook handles
	// it before the kind switch reaches resolvePassword).
	kpCred := &store.CredentialRef{
		Name:   "kp",
		Kind:   store.CredPassword,
		Config: map[string]any{"keepass_ref": map[string]any{"db_id": "d", "entry_uuid": "e"}},
	}
	m, err := ResolveAuth(nil, kpCred, nil)
	if err != nil {
		t.Fatalf("resolve keepass: %v", err)
	}
	if m.Password != "kp-secret" {
		t.Fatalf("keepass secret: got %q", m.Password)
	}
	if !keepassCalled {
		t.Fatal("keepass hook was not called")
	}

	// A Bitwarden-backed credential falls through the KeePass hook (handled=false)
	// then the Bitwarden hook resolves it.
	keepassCalled, bitwardenCalled = false, false
	bwCred := &store.CredentialRef{
		Name:   "bw",
		Kind:   store.CredPassword,
		Config: map[string]any{"bitwarden_ref": map[string]any{"server_id": "s", "cipher_id": "c"}},
	}
	m, err = ResolveAuth(nil, bwCred, nil)
	if err != nil {
		t.Fatalf("resolve bitwarden: %v", err)
	}
	if m.Password != "bw-secret" {
		t.Fatalf("bitwarden secret: got %q", m.Password)
	}
	if !keepassCalled || !bitwardenCalled {
		t.Fatalf("expected both hooks tried: keepass=%v bitwarden=%v", keepassCalled, bitwardenCalled)
	}

	// An Infisical-backed credential falls through KeePass + Bitwarden
	// (handled=false) and is resolved by the Infisical hook.
	keepassCalled, bitwardenCalled, infisicalCalled = false, false, false
	infCred := &store.CredentialRef{
		Name:   "inf",
		Kind:   store.CredPassword,
		Config: map[string]any{"infisical_ref": map[string]any{"server_id": "s", "project_id": "p", "environment": "prod", "key": "k"}},
	}
	m, err = ResolveAuth(nil, infCred, nil)
	if err != nil {
		t.Fatalf("resolve infisical: %v", err)
	}
	if m.Password != "inf-secret" {
		t.Fatalf("infisical secret: got %q", m.Password)
	}
	if !keepassCalled || !bitwardenCalled || !infisicalCalled {
		t.Fatalf("expected all hooks tried: keepass=%v bitwarden=%v infisical=%v", keepassCalled, bitwardenCalled, infisicalCalled)
	}
}
