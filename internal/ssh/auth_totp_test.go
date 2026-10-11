package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/totp"
)

const testTOTP = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// kiHandshake runs a client handshake against a PAM-style server that only
// speaks keyboard-interactive and asks for a password and a verification
// code in one round (accepts "right" plus the current code of testTOTP).
func kiHandshake(t *testing.T, methods []ssh.AuthMethod) bool {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := totp.Parse(testTOTP)
	if err != nil {
		t.Fatal(err)
	}
	scfg := &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := ch("", "", []string{"Password: ", "Verification code: "}, []bool{false, false})
			if err != nil {
				return nil, err
			}
			now := time.Now()
			if len(ans) == 2 && ans[0] == "right" &&
				(ans[1] == sp.Code(now) || ans[1] == sp.Code(now.Add(-30*time.Second))) {
				return nil, nil
			}
			return nil, errNoMorePasswords
		},
	}
	scfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c2, err := ln.Accept()
		if err != nil {
			return
		}
		defer c2.Close()
		if sc, _, _, err := ssh.NewServerConn(c2, scfg); err == nil {
			sc.Close()
		}
	}()
	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	ccfg := &ssh.ClientConfig{User: "u", Auth: methods, HostKeyCallback: ssh.InsecureIgnoreHostKey()}
	cc, _, _, err := ssh.NewClientConn(c1, "pipe", ccfg)
	ok := err == nil
	if ok {
		cc.Close()
	}
	c1.Close()
	<-done
	return ok
}

// Stored password plus the item's TOTP answer a PAM 2FA server with nobody
// to ask (the batch / fleet path).
func TestTargetAuthAnswersTOTPWithoutPrompt(t *testing.T) {
	old := InteractiveAuthHook
	defer func() { InteractiveAuthHook = old }()
	InteractiveAuthHook = nil
	m := &AuthMaterial{Password: "right", TOTP: testTOTP}
	if !kiHandshake(t, targetAuthMethods(m, nil, false, "t", "h", 22, nil)) {
		t.Fatal("password + TOTP autofill should pass keyboard-interactive")
	}
}

// Without a TOTP secret only the code question reaches the user; the
// stored password is filled in.
func TestTargetAuthPromptsOnlyForCode(t *testing.T) {
	old := InteractiveAuthHook
	defer func() { InteractiveAuthHook = old }()
	sp, _ := totp.Parse(testTOTP)
	var asked []string
	InteractiveAuthHook = func(_, _ string, _ int, _, _, _ string, ps []InteractiveAuthPrompt) ([]string, error) {
		for _, p := range ps {
			asked = append(asked, p.Text)
		}
		return []string{sp.Code(time.Now())}, nil
	}
	if !kiHandshake(t, targetAuthMethods(&AuthMaterial{Password: "right"}, nil, true, "t", "h", 22, nil)) {
		t.Fatal("auth failed")
	}
	if len(asked) != 1 || asked[0] != "Verification code: " {
		t.Fatalf("user was asked %q, want only the code", asked)
	}
}

func TestKIAutofillRules(t *testing.T) {
	f := &kiAutofill{password: "pw", totp: testTOTP, now: func() time.Time { return time.Unix(59, 0) }}
	ans, missing := f.answer(
		[]string{"Username: ", "Password: ", "New password: ", "Verification code: "},
		[]bool{true, false, false, false})
	if ans[1] != "pw" || ans[3] != "287082" {
		t.Fatalf("answers = %q", ans)
	}
	if len(missing) != 2 || missing[0] != 0 || missing[1] != 2 {
		t.Fatalf("missing = %v, want the echoed username and the password change", missing)
	}
	// Once used, nothing is offered again: a rejected code must not loop.
	_, missing = f.answer([]string{"Password: ", "Verification code: "}, nil)
	if len(missing) != 2 {
		t.Fatalf("second round filled %d answers, want none", 2-len(missing))
	}
	// A nil filler asks for everything.
	var none *kiAutofill
	if _, m := none.answer([]string{"a", "b"}, nil); len(m) != 2 {
		t.Fatalf("nil filler missing = %v", m)
	}
}
