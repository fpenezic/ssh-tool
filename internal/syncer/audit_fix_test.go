package syncer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A callback without our state cannot end the flow, errors included, and
// repeated callbacks do not hang their handlers.
func TestAuthorizeStateBeforeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	get := func(q string) int {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/callback?" + q)
		if err != nil {
			return 0 // the flow already closed the listener
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	open := func(authURL string) {
		u, _ := url.Parse(authURL)
		state := u.Query().Get("state")
		if code := get("error=forged"); code != http.StatusBadRequest {
			t.Errorf("stateless error callback: HTTP %d", code)
		}
		// Several real callbacks at once (a reloaded tab): one is delivered,
		// none may hang.
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				get("state=" + url.QueryEscape(state) + "&error=access_denied")
			}()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = Authorize(ctx, OAuthEndpoints{AuthURL: "https://auth.example.com/authorize", TokenURL: "https://auth.example.com/token", RedirectPort: port}, "id", "", open)
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("want the real denial, got %v", err)
	}
}


// A server that accepts TCP and never speaks SSH fails the connect within
// the timeout instead of hanging it.
func TestSFTPHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold it open, say nothing
		}
	}()
	s := &SFTP{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "u",
		HostKey: ssh.InsecureIgnoreHostKey(), Timeout: 500 * time.Millisecond}
	done := make(chan error, 1)
	go func() { _, err := s.connect(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("connected to a silent server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handshake hung past the timeout")
	}
}

// The trust dialog may outlast the handshake timeout: the clock stops at
// the host-key callback.
func TestSFTPHostKeyPromptOutlastsTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	key, err := ssh.ParsePrivateKey(testHostKeyPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	srvCfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	srvCfg.AddHostKey(key)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, chans, reqs, err := ssh.NewServerConn(c, srvCfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for ch := range chans {
			_ = ch.Reject(ssh.Prohibited, "no")
		}
	}()
	s := &SFTP{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "u",
		Auth: []ssh.AuthMethod{ssh.Password("x")},
		HostKey: func(string, net.Addr, ssh.PublicKey) error {
			time.Sleep(800 * time.Millisecond) // the user reading the fingerprint
			return nil
		},
		Timeout: 300 * time.Millisecond}
	_, err = s.connect()
	// The SFTP subsystem is refused by this server; what matters is that
	// the handshake got past the slow host-key check.
	if err != nil && strings.Contains(err.Error(), "handshake") {
		t.Fatalf("handshake cut off by the timeout: %v", err)
	}
}

func testHostKeyPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(b)
}
