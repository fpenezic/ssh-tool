package ssh

import (
	"errors"
	"io"
	"net"
	"testing"
)

func TestSocks5ReplyForDialErr(t *testing.T) {
	cases := []struct {
		err  string
		want byte
	}{
		// What x/crypto/ssh surfaces for a closed port on the far side.
		{"ssh: rejected: connect failed (Connection refused)", replyConnRefused},
		{"dial tcp: lookup nope.example.com: no such host", replyHostUnreach},
		{"dial tcp 10.0.0.9:80: i/o timeout", replyHostUnreach},
		{"dial tcp 10.0.0.9:80: connect: no route to host", replyHostUnreach},
		{"dial tcp: connect: network is unreachable", replyNetUnreach},
		{"something else entirely", replyGeneralFail},
	}
	for _, c := range cases {
		if got := socks5ReplyForDialErr(errors.New(c.err)); got != c.want {
			t.Errorf("socks5ReplyForDialErr(%q) = %d, want %d", c.err, got, c.want)
		}
	}
}

// TestHandleSocks5NoEarlyReply is the regression guard for the bug this
// replaced: handleSocks5 used to write "granted" before the caller had dialed
// anything, so a failed dial reached the client as a bare connection reset.
func TestHandleSocks5NoEarlyReply(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		// Greeting: version 5, one method, "no auth".
		_, _ = client.Write([]byte{0x05, 0x01, 0x00})
		ack := make([]byte, 2)
		_, _ = io.ReadFull(client, ack)
		// CONNECT example.com:443 by domain name.
		host := "example.com"
		req := []byte{0x05, 0x01, 0x00, atypDomain, byte(len(host))}
		req = append(req, host...)
		req = append(req, 0x01, 0xbb)
		_, _ = client.Write(req)
	}()

	dest, err := handleSocks5(server)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if dest != "example.com:443" {
		t.Fatalf("dest = %q, want example.com:443", dest)
	}

	// The reply is the caller's job, after the dial - and it must carry the
	// real outcome, not a blanket "granted".
	reply := make([]byte, 10)
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(client, reply)
		done <- err
	}()
	if err := writeReply(server, replyConnRefused); err != nil {
		t.Fatalf("write reply: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[0] != socks5Version || reply[1] != replyConnRefused {
		t.Fatalf("reply = %v, want version 5 + code %d", reply[:2], replyConnRefused)
	}
}
