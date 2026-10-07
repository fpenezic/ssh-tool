package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// handshakeWith runs a client handshake against an in-memory server that
// accepts only the password "right", returning the passwords it was offered.
func handshakeWith(t *testing.T, methods []ssh.AuthMethod) (bool, []string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	scfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			seen = append(seen, string(pw))
			if string(pw) == "right" {
				return nil, nil
			}
			return nil, errNoMorePasswords
		},
	}
	scfg.AddHostKey(signer)
	// Loopback TCP, not net.Pipe: the unbuffered pipe deadlocks the
	// simultaneous version exchange.
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
	return ok, seen
}

// The bug this guards: a folder credential's password and the connection's
// own were two ssh.Password methods, and x/crypto skipped the second.
func TestTargetAuthTriesConnectionPasswordWithCredentialPassword(t *testing.T) {
	over := "right"
	ok, seen := handshakeWith(t, targetAuthMethods(&AuthMaterial{Password: "folder"}, &over, false, "t", "h", 22, nil))
	if !ok {
		t.Fatalf("auth failed, server saw %v", seen)
	}
	if len(seen) != 1 || seen[0] != "right" {
		t.Errorf("connection password must go first, server saw %v", seen)
	}

	// Wrong connection password: the credential's is still tried.
	wrong := "stale"
	ok, seen = handshakeWith(t, targetAuthMethods(&AuthMaterial{Password: "right"}, &wrong, false, "t", "h", 22, nil))
	if !ok || len(seen) != 2 {
		t.Errorf("fallback to credential password: ok=%v seen=%v", ok, seen)
	}
}

// A wrong stored password used to leave the password prompt dead.
func TestTargetAuthPromptsAfterWrongStoredPassword(t *testing.T) {
	old := InteractiveAuthHook
	defer func() { InteractiveAuthHook = old }()
	InteractiveAuthHook = func(_, _ string, _ int, _, _, _ string, _ []InteractiveAuthPrompt) ([]string, error) {
		return []string{"right"}, nil
	}
	ok, seen := handshakeWith(t, targetAuthMethods(&AuthMaterial{Password: "wrong"}, nil, true, "t", "h", 22, nil))
	if !ok || len(seen) != 2 || seen[1] != "right" {
		t.Errorf("prompt after a wrong stored password: ok=%v seen=%v", ok, seen)
	}
}

func TestPasswordSequenceDropsEmptyAndRepeats(t *testing.T) {
	if passwordSequence([]string{"", ""}, nil) != nil {
		t.Error("no passwords must give no method")
	}
	ok, seen := handshakeWith(t, []ssh.AuthMethod{passwordSequence([]string{"x", "x", "", "right"}, nil)})
	if !ok || len(seen) != 2 {
		t.Errorf("ok=%v seen=%v", ok, seen)
	}
}
