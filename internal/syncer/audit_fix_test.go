package syncer

import (
	"context"
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
