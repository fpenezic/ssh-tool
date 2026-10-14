package kafka

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestProduce(t *testing.T) {
	fc := newFake(t, 0)
	c := openFake(t, fc)
	ctx := ctx10(t)

	res, err := c.Produce(ctx, ProduceRequest{
		Topic:     "orders",
		Partition: 2,
		Key:       &PayloadInput{Text: "k"},
		Value:     &PayloadInput{Text: "/wA=", Encoding: "base64"},
		Headers:   []HeaderInput{{Key: "trace", Value: PayloadInput{Text: "abc"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Partition != 2 || res.Offset != 0 {
		t.Fatalf("result = %+v", res)
	}
	// Auto partition, null value (a tombstone).
	if _, err := c.Produce(ctx, ProduceRequest{Topic: "orders", Partition: -1, Key: &PayloadInput{Text: "k"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Produce(ctx, ProduceRequest{Topic: "orders", Partition: -1, Value: &PayloadInput{Text: "%%", Encoding: "base64"}}); err == nil {
		t.Fatal("bad base64: want error")
	}

	k, err := c.Consume(ctx, ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	msgs, _ := drain(t, k)
	if len(msgs) != 2 {
		t.Fatalf("consumed %d, want 2", len(msgs))
	}
	var bin, tomb *Message
	for i := range msgs {
		if msgs[i].Value == nil {
			tomb = &msgs[i]
		} else {
			bin = &msgs[i]
		}
	}
	if bin == nil || bin.Value.Encoding != "base64" || bin.Value.Text != "/wA=" || len(bin.Headers) != 1 || bin.Headers[0].Value.Text != "abc" {
		t.Fatalf("binary record = %+v", bin)
	}
	if tomb == nil || tomb.Key == nil || tomb.Key.Text != "k" {
		t.Fatalf("tombstone = %+v", tomb)
	}
}

func TestTopicLifecycle(t *testing.T) {
	fc := newFake(t, 4)
	c := openFake(t, fc)
	ctx := ctx10(t)

	if err := c.CreateTopic(ctx, "fresh", 2, 1, map[string]string{"retention.ms": "60000"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateTopic(ctx, "fresh", 2, 1, nil); err == nil {
		t.Fatal("duplicate create: want error")
	}
	d, err := c.TopicDetail(ctx, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Partitions) != 2 || configValue(d, "retention.ms") != "60000" || !configOverride(d, "retention.ms") {
		t.Fatalf("fresh = %d partitions, retention %q", len(d.Partitions), configValue(d, "retention.ms"))
	}

	if err := c.AlterTopicConfig(ctx, "fresh", map[string]string{"cleanup.policy": "compact"}, []string{"retention.ms"}); err != nil {
		t.Fatal(err)
	}
	d, _ = c.TopicDetail(ctx, "fresh")
	if configValue(d, "cleanup.policy") != "compact" || configOverride(d, "retention.ms") {
		t.Fatalf("after alter: cleanup %q, retention override %v", configValue(d, "cleanup.policy"), configOverride(d, "retention.ms"))
	}

	if err := c.SetPartitions(ctx, "fresh", 5); err != nil {
		t.Fatal(err)
	}
	if d, _ = c.TopicDetail(ctx, "fresh"); len(d.Partitions) != 5 {
		t.Fatalf("partitions = %d, want 5", len(d.Partitions))
	}
	if err := c.SetReplicationFactor(ctx, "fresh", 3); err == nil {
		t.Fatal("rf above the broker count: want error")
	}
	if err := c.SetReplicationFactor(ctx, "fresh", 1); err != nil {
		t.Fatal(err)
	}

	if err := c.Truncate(ctx, "orders"); err != nil {
		t.Fatal(err)
	}
	if d, _ = c.TopicDetail(ctx, "orders"); d.Messages != 0 || d.Partitions[0].StartOffset != 4 {
		t.Fatalf("after truncate: %d messages, start %d", d.Messages, d.Partitions[0].StartOffset)
	}

	// A topic of its own: kfake keeps reporting a topic that had a
	// reassignment after it is deleted.
	if err := c.CreateTopic(ctx, "doomed", 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTopic(ctx, "doomed"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TopicDetail(ctx, "doomed"); err == nil {
		t.Fatal("deleted topic still described")
	}
}

func configValue(d *TopicDetail, name string) string {
	for _, c := range d.Configs {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func configOverride(d *TopicDetail, name string) bool {
	for _, c := range d.Configs {
		if c.Name == name {
			return c.Override
		}
	}
	return false
}

func TestRoundRobin(t *testing.T) {
	b := []int32{1, 2, 3, 4}
	if got := roundRobin(b, 2, 3, -1); got[0] != 3 || got[1] != 4 || got[2] != 1 {
		t.Fatalf("got %v", got)
	}
	// The current leader moves to the front.
	if got := roundRobin(b, 0, 3, 3); got[0] != 3 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestTopicMetrics(t *testing.T) {
	fc := newFake(t, 3)
	c := openFake(t, fc)
	m, err := c.TopicMetrics(ctx10(t), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if m.Messages != 9 || m.EndOffsets != 9 || m.SampledAt == 0 {
		t.Fatalf("metrics = %+v", m)
	}
	// kfake reports log dirs with zero sizes; the sums still have to be
	// there (not -1) and consistent.
	if m.SizeError == "" && (m.TotalBytes < 0 || m.LeaderBytes != m.TotalBytes) {
		t.Fatalf("sizes = %+v", m)
	}
}

// groupConsume reads n records of "orders" in group g and commits.
func groupConsume(t *testing.T, seeds []string, g string, n int) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.ConsumerGroup(g), kgo.ConsumeTopics("orders"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), kgo.DisableAutoCommit())
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx := ctx10(t)
	got := 0
	for got < n {
		fs := cl.PollRecords(ctx, n-got)
		if err := ctx.Err(); err != nil {
			t.Fatalf("group consume: %v (got %d)", err, got)
		}
		got += fs.NumRecords()
	}
	if err := cl.CommitUncommittedOffsets(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGroups(t *testing.T) {
	fc := newFake(t, 5) // 15 records
	groupConsume(t, fc.ListenAddrs(), "billing", 15)
	c := openFake(t, fc)
	ctx := ctx10(t)

	gs, err := c.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Name != "billing" || gs[0].Lag != 0 || len(gs[0].Topics) != 1 {
		t.Fatalf("groups = %+v", gs)
	}

	if err := c.ResetGroupOffsets(ctx, ResetGroupRequest{Group: "billing", Topic: "orders", Mode: FromEarliest}); err != nil {
		t.Fatal(err)
	}
	d, err := c.Group(ctx, "billing")
	if err != nil {
		t.Fatal(err)
	}
	if d.Lag != 15 || len(d.Partitions) != 3 || d.State != "Empty" {
		t.Fatalf("after reset: %+v", d)
	}
	if err := c.ResetGroupOffsets(ctx, ResetGroupRequest{Group: "billing", Topic: "orders", Mode: FromLast, N: 1}); err != nil {
		t.Fatal(err)
	}
	if d, _ = c.Group(ctx, "billing"); d.Lag != 3 {
		t.Fatalf("after last-1 reset: lag %d", d.Lag)
	}

	if err := c.DeleteGroup(ctx, "billing"); err != nil {
		t.Fatal(err)
	}
	if gs, _ = c.Groups(ctx); len(gs) != 0 {
		t.Fatalf("after delete: %+v", gs)
	}
}

func TestResetRefusesActiveGroup(t *testing.T) {
	fc := newFake(t, 2)
	cl, err := kgo.NewClient(kgo.SeedBrokers(fc.ListenAddrs()...), kgo.ConsumerGroup("live"), kgo.ConsumeTopics("orders"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx := ctx10(t)
	for n := 0; n < 6; {
		n += cl.PollRecords(ctx, 6).NumRecords()
		if ctx.Err() != nil {
			t.Fatal("group never joined")
		}
	}
	c := openFake(t, fc)
	err = c.ResetGroupOffsets(ctx, ResetGroupRequest{Group: "live", Topic: "orders", Mode: FromEarliest})
	if !errors.Is(err, ErrGroupActive) {
		t.Fatalf("err = %v, want ErrGroupActive", err)
	}
}

func TestSASLMechanism(t *testing.T) {
	for _, k := range []string{SASLPlain, SASLScramSHA256, SASLScramSHA512} {
		m, err := SASLMechanism(k, "u", "p")
		if err != nil || m == nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if m, err := SASLMechanism("", "u", "p"); m != nil || err != nil {
		t.Fatalf("none: %v %v", m, err)
	}
	if _, err := SASLMechanism("gssapi", "u", "p"); err == nil {
		t.Fatal("unknown: want error")
	}
}

func TestSASLAndTLS(t *testing.T) {
	cert, pool := selfSigned(t)
	fc, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.SeedTopics(1, "secure"),
		kfake.EnableSASL(),
		kfake.Superuser("SCRAM-SHA-256", "admin", "s3cret"),
		kfake.TLS(&tls.Config{Certificates: []tls.Certificate{cert}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()

	open := func(pass string, roots bool) *Cluster {
		mech, _ := SASLMechanism(SASLScramSHA256, "admin", pass)
		cfg := &tls.Config{}
		if roots {
			cfg.RootCAs = pool
		}
		c, err := Open(Config{Bootstrap: fc.ListenAddrs(), TLS: cfg, SASL: mech})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		return ctx
	}
	if _, err := open("s3cret", true).Topics(ctx10(t), false); err != nil {
		t.Fatalf("good login: %v", err)
	}
	if _, err := open("wrong", true).Topics(short(), false); err == nil {
		t.Fatal("bad password: want error")
	}
	if _, err := open("s3cret", false).Topics(short(), false); err == nil {
		t.Fatal("untrusted certificate: want error")
	}
}

// selfSigned makes a certificate for 127.0.0.1 and a pool trusting it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kafka.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
