package ssh

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// pipeConn feeds request bytes into handleHTTPProxy and captures what the
// handler writes back (the error/reply), using a net.Pipe so the handler sees
// a real net.Conn.
func runProxyParse(t *testing.T, request string) (*httpProxyTarget, string, error) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Feed the request from a goroutine and collect anything the handler
	// writes back to the server side.
	go func() {
		_, _ = client.Write([]byte(request))
	}()

	var reply strings.Builder
	replyDone := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		for {
			n, err := client.Read(buf)
			if n > 0 {
				reply.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		close(replyDone)
	}()

	br := bufio.NewReader(server)
	tgt, err := handleHTTPProxy(server, br)
	// Give the reply collector a moment, then unblock it by closing.
	_ = server.Close()
	<-replyDone
	return tgt, reply.String(), err
}

func TestHTTPProxyConnect(t *testing.T) {
	tgt, _, err := runProxyParse(t, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tgt.connect {
		t.Fatalf("expected connect=true")
	}
	if tgt.addr != "example.com:443" {
		t.Fatalf("addr = %q, want example.com:443", tgt.addr)
	}
	if tgt.replay != nil {
		t.Fatalf("replay should be nil for CONNECT")
	}
}

func TestHTTPProxyConnectDefaultPort(t *testing.T) {
	// A CONNECT without an explicit port defaults to 443.
	tgt, _, err := runProxyParse(t, "CONNECT example.com HTTP/1.1\r\n\r\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.addr != "example.com:443" {
		t.Fatalf("addr = %q, want example.com:443", tgt.addr)
	}
}

func TestHTTPProxyPlainGET(t *testing.T) {
	req := "GET http://deb.example.com/dists/stable/Release HTTP/1.1\r\n" +
		"Host: deb.example.com\r\n" +
		"User-Agent: apt\r\n\r\n"
	tgt, _, err := runProxyParse(t, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.connect {
		t.Fatalf("plain GET should not be connect")
	}
	if tgt.addr != "deb.example.com:80" {
		t.Fatalf("addr = %q, want deb.example.com:80", tgt.addr)
	}
	replay := string(tgt.replay)
	// Request line must be rewritten to origin-relative form.
	if !strings.HasPrefix(replay, "GET /dists/stable/Release HTTP/1.1\r\n") {
		t.Fatalf("rewritten request line wrong: %q", replay)
	}
	// Original headers must survive.
	if !strings.Contains(replay, "Host: deb.example.com\r\n") {
		t.Fatalf("Host header lost: %q", replay)
	}
	if !strings.HasSuffix(replay, "\r\n\r\n") {
		t.Fatalf("replay must end with blank line: %q", replay)
	}
}

func TestHTTPProxyMalformedLine(t *testing.T) {
	tgt, reply, err := runProxyParse(t, "GARBAGE\r\n\r\n")
	if err == nil {
		t.Fatalf("expected error for malformed request line")
	}
	if tgt != nil {
		t.Fatalf("target should be nil on error")
	}
	if !strings.Contains(reply, "400") {
		t.Fatalf("expected 400 reply, got %q", reply)
	}
}

func TestHTTPProxyBadAbsoluteURI(t *testing.T) {
	// A non-CONNECT method whose target isn't an http:// absolute URI.
	_, reply, err := runProxyParse(t, "GET /relative/path HTTP/1.1\r\nHost: x\r\n\r\n")
	if err == nil {
		t.Fatalf("expected error for relative-URI proxy request")
	}
	if !strings.Contains(reply, "400") {
		t.Fatalf("expected 400 reply, got %q", reply)
	}
}

func TestHTTPProxyOversizedHeaders(t *testing.T) {
	// Header block that never terminates and exceeds the cap.
	var b strings.Builder
	b.WriteString("CONNECT example.com:443 HTTP/1.1\r\n")
	for b.Len() < maxProxyHeaderBytes+1024 {
		b.WriteString("X-Filler: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n")
	}
	_, _, err := runProxyParse(t, b.String())
	if err == nil {
		t.Fatalf("expected error for oversized headers")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("expected 'too long' error, got %v", err)
	}
}

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct{ in, def, want string }{
		{"example.com", "80", "example.com:80"},
		{"example.com:8080", "80", "example.com:8080"},
		{"1.2.3.4", "443", "1.2.3.4:443"},
		{"1.2.3.4:22", "443", "1.2.3.4:22"},
		{"", "80", ""},
	}
	for _, c := range cases {
		if got := normalizeHostPort(c.in, c.def); got != c.want {
			t.Errorf("normalizeHostPort(%q,%q) = %q, want %q", c.in, c.def, got, c.want)
		}
	}
}
