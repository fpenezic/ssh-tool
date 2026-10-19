package kafka

import (
	"net"
	"sync/atomic"
	"testing"

	"ssh-tool/internal/sshchan"
	"ssh-tool/internal/sshchan/sshchantest"
)

// The seed broker name only resolves on the SSH server: the cluster must be
// reachable through the tunnel anyway, and every broker connection must ride
// the one session.
func TestClusterThroughSSH(t *testing.T) {
	fc := newFake(t, 2)
	_, port, _ := net.SplitHostPort(fc.ListenAddrs()[0])
	srv := sshchantest.Start(t, map[string]string{"kafka.internal": "127.0.0.1"})
	var connects atomic.Int32
	lazy := srv.Lazy(&connects)
	defer lazy.Close()

	c, err := Open(Config{Bootstrap: []string{"kafka.internal:" + port}, Dial: lazy.Dial})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := ctx10(t)
	topics, err := c.Topics(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 || topics[1].Messages != 6 {
		t.Fatalf("topics = %+v", topics)
	}
	k, err := c.Consume(ctx, ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	if msgs, _ := drain(t, k); len(msgs) != 6 {
		t.Fatalf("consumed %d through ssh, want 6", len(msgs))
	}
	if n := connects.Load(); n != 1 {
		t.Fatalf("%d ssh connects, want 1", n)
	}
	if st, _ := lazy.State(); st != sshchan.TransportConnected {
		t.Fatalf("state %s", st)
	}
	seed := false
	for _, d := range srv.Dialed() {
		if d == "kafka.internal:"+port {
			seed = true
		}
	}
	if !seed {
		t.Fatalf("seed not dialed through ssh: %v", srv.Dialed())
	}
}
