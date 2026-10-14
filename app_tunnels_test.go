package main

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"ssh-tool/internal/store"
)

// A tunnel keeps retrying only what can heal on its own; a login or host
// key failure retried every minute would just repeat its prompt.
func TestTunnelRetryable(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	cases := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("target example.com: dial: %w", dial), true},
		{errors.New("target example.com: dial: dial tcp 192.0.2.1:22: connect: no route to host"), true},
		{errors.New("target example.com: ssh handshake: EOF"), true},
		{errors.New("target example.com: ssh: handshake failed: EOF"), true},
		{errors.New("dial tcp: lookup example.com: no such host"), true},
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain"), false},
		{errors.New("ssh: handshake failed: host key rejected by user"), false},
		{fmt.Errorf("resolve: %w", store.ErrNotFound), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := tunnelRetryable(c.err); got != c.want {
			t.Errorf("tunnelRetryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
