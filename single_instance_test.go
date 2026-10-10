package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// A handoff is acted on only with the primary's token: loopback is shared by
// every local user, and an argv can open a shell or fetch an import URL.
func TestServeInstanceNeedsToken(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  bool
	}{{"secret", true}, {"", false}, {"wrong", false}} {
		srv, cli := net.Pipe()
		got := make(chan bool, 1)
		go serveInstance(srv, "secret", func(instanceMsg) { got <- true })
		_ = cli.SetDeadline(time.Now().Add(time.Second))
		_ = json.NewEncoder(cli).Encode(instanceMsg{Argv: []string{"--open-dir", "/tmp"}, Token: tc.token})
		_, _ = cli.Read(make([]byte, 1))
		_ = cli.Close()
		select {
		case <-got:
			if !tc.want {
				t.Errorf("token %q: handler ran", tc.token)
			}
		case <-time.After(300 * time.Millisecond):
			if tc.want {
				t.Errorf("token %q: handler did not run", tc.token)
			}
		}
	}
}
