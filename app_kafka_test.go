package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/kafka"
	"ssh-tool/internal/store"
)

func newKafkaTestApp(t *testing.T) *App {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{
		db:             db,
		kafkaClusters:  map[string]*kafkaCluster{},
		kafkaConsumers: map[string]*kafkaConsumer{},
	}
}

func kafkaTestConn(t *testing.T, a *App, over store.InheritableSettings) string {
	t.Helper()
	c, err := a.db.CreateConnection(store.NewConnection{Name: "broker", Hostname: "broker.example.com", Overrides: over})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestKafkaOpenRefuses(t *testing.T) {
	a := newKafkaTestApp(t)
	on, off := true, false
	empty, bad := "  ", "host:notaport"
	cases := []struct {
		name string
		over store.InheritableSettings
		want string
	}{
		{"not enabled", store.InheritableSettings{}, "not enabled"},
		{"disabled", store.InheritableSettings{KafkaEnabled: &off}, "not enabled"},
		{"no bootstrap", store.InheritableSettings{KafkaEnabled: &on, KafkaBootstrap: &empty}, "no Kafka bootstrap"},
		{"bad bootstrap", store.InheritableSettings{KafkaEnabled: &on, KafkaBootstrap: &bad}, "bad broker address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.KafkaOpen(kafkaTestConn(t, a, tc.over))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if len(a.kafkaClusters) != 0 {
		t.Fatalf("%d handles left open", len(a.kafkaClusters))
	}
}

// The whole IPC path against an in-memory cluster, direct transport.
func TestKafkaIPCDirect(t *testing.T) {
	fc, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(2, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	cl, err := kgo.NewClient(kgo.SeedBrokers(fc.ListenAddrs()...), kgo.DefaultProduceTopic("events"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		if err := cl.ProduceSync(ctx, &kgo.Record{Value: []byte("e")}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	cl.Close()

	a := newKafkaTestApp(t)
	on, off := true, false
	seed := fc.ListenAddrs()[0]
	id := kafkaTestConn(t, a, store.InheritableSettings{KafkaEnabled: &on, KafkaBootstrap: &seed, KafkaUseTunnel: &off})

	info, err := a.KafkaOpen(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Transport != "direct" || info.State != "" || info.Name != "broker" || len(info.Bootstrap) != 1 {
		t.Fatalf("info = %+v", info)
	}

	topics, err := a.KafkaTopics(info.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Name != "events" || topics[0].Messages != 5 {
		t.Fatalf("topics = %+v", topics)
	}
	if d, err := a.KafkaTopicDetail(info.ID, "events"); err != nil || len(d.Partitions) != 2 {
		t.Fatalf("detail = %+v, %v", d, err)
	}

	cid, err := a.KafkaConsumeStart(info.ID, kafka.ConsumeRequest{Topic: "events", Mode: kafka.FromEarliest, Partition: -1, StopAtEnd: true, Tab: "events#2"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := a.KafkaConsumeSnapshot(cid, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == kafka.ConsumeDone {
			if s.Received != 5 || len(s.Messages) != 5 {
				t.Fatalf("snapshot = %+v", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer did not finish: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The tab state outlives the UI component: open topics and the finished
	// consumer (still holding its buffer) come back to another window, the
	// consumer with the tab it belongs to (a second tab on its topic here).
	a.KafkaSetOpenTopics(info.ID, []string{"events", "events#2"})
	st, err := a.KafkaGetTabState(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Topics) != 2 || st.Topics[0] != "events" || len(st.Consumers) != 1 || st.Consumers[0].ID != cid || st.Consumers[0].Request.Topic != "events" || st.Consumers[0].Request.Tab != "events#2" {
		t.Fatalf("tab state = %+v", st)
	}

	a.KafkaClose(info.ID)
	if _, err := a.KafkaGetTabState(info.ID); err == nil {
		t.Fatal("tab state after close: want error")
	}
	if _, err := a.KafkaTopics(info.ID, false); err == nil {
		t.Fatal("topics after close: want error")
	}
	if _, err := a.KafkaConsumeSnapshot(cid, 0, 0); err == nil {
		t.Fatal("consumer survived its cluster")
	}
	evs, err := a.db.ListAudit(store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.Action] = true
	}
	if !seen["kafka.open"] || !seen["kafka.consume"] {
		t.Fatalf("audit = %+v", evs)
	}
}

// A read-only connection reads but every write is refused in the backend.
func TestKafkaReadOnlyRefusesWrites(t *testing.T) {
	fc, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	a := newKafkaTestApp(t)
	on, off := true, false
	seed := fc.ListenAddrs()[0]
	id := kafkaTestConn(t, a, store.InheritableSettings{
		KafkaEnabled: &on, KafkaBootstrap: &seed, KafkaUseTunnel: &off, KafkaReadOnly: &on,
	})
	info, err := a.KafkaOpen(id)
	if err != nil {
		t.Fatal(err)
	}
	defer a.KafkaClose(info.ID)
	if !info.ReadOnly || info.Security != "plaintext" {
		t.Fatalf("info = %+v", info)
	}
	if _, err := a.KafkaTopics(info.ID, false); err != nil {
		t.Fatalf("read on a read-only cluster: %v", err)
	}
	writes := map[string]error{
		"produce": func() error {
			_, err := a.KafkaProduce(info.ID, kafka.ProduceRequest{Topic: "events", Partition: -1})
			return err
		}(),
		"create":       a.KafkaCreateTopic(info.ID, KafkaCreateTopicInput{Name: "x", Partitions: 1, ReplicationFactor: 1}),
		"delete":       a.KafkaDeleteTopic(info.ID, "events"),
		"config":       a.KafkaAlterTopicConfig(info.ID, "events", map[string]string{"retention.ms": "1"}, nil),
		"partitions":   a.KafkaSetPartitions(info.ID, "events", 3),
		"rf":           a.KafkaSetReplicationFactor(info.ID, "events", 1),
		"truncate":     a.KafkaTruncate(info.ID, "events"),
		"reset group":  a.KafkaResetGroupOffsets(info.ID, kafka.ResetGroupRequest{Group: "g", Topic: "events", Mode: kafka.FromEarliest}),
		"delete group": a.KafkaDeleteGroup(info.ID, "g"),
	}
	for name, err := range writes {
		if err != errKafkaReadOnly {
			t.Errorf("%s: err = %v, want read-only", name, err)
		}
	}
	if topics, _ := a.KafkaTopics(info.ID, false); len(topics) != 1 || topics[0].Partitions != 1 {
		t.Fatalf("a refused write changed the cluster: %+v", topics)
	}
}

// SASL without a credential fails at open, with a message that says where to
// set one.
func TestKafkaSASLNeedsCredential(t *testing.T) {
	a := newKafkaTestApp(t)
	on := true
	seed, mech := "127.0.0.1:9092", "scram-sha-512"
	id := kafkaTestConn(t, a, store.InheritableSettings{KafkaEnabled: &on, KafkaBootstrap: &seed, KafkaSASL: &mech})
	if _, err := a.KafkaOpen(id); err == nil || !strings.Contains(err.Error(), "needs a credential") {
		t.Fatalf("err = %v", err)
	}
}

// kafkaSSHLab is a password-only SSH server ("kafka" / "secret") serving
// direct-tcpip, resolving "kafka.internal" itself - a name this machine does
// not know - the way a bastion resolves advertised broker names.
type kafkaSSHLab struct {
	host string
	port int
	key  gossh.PublicKey

	mu     sync.Mutex
	conns  []net.Conn
	logins int
}

func startKafkaSSHLab(t *testing.T) *kafkaSSHLab {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := gossh.NewSignerFromKey(priv)
	lab := &kafkaSSHLab{key: signer.PublicKey()}
	cfg := &gossh.ServerConfig{PasswordCallback: func(c gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
		if c.User() == "kafka" && string(pw) == "secret" {
			lab.mu.Lock()
			lab.logins++
			lab.mu.Unlock()
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	lab.host = "127.0.0.1"
	lab.port = ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			lab.mu.Lock()
			lab.conns = append(lab.conns, c)
			lab.mu.Unlock()
			go func() {
				_, chans, reqs, err := gossh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go gossh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "direct-tcpip" {
						_ = nc.Reject(gossh.Prohibited, "no")
						continue
					}
					var req struct {
						Host string
						Port uint32
						OH   string
						OP   uint32
					}
					if err := gossh.Unmarshal(nc.ExtraData(), &req); err != nil {
						_ = nc.Reject(gossh.ConnectionFailed, "bad request")
						continue
					}
					host := req.Host
					if host == "kafka.internal" {
						host = "127.0.0.1"
					}
					up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(req.Port))))
					if err != nil {
						_ = nc.Reject(gossh.ConnectionFailed, err.Error())
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						_ = up.Close()
						continue
					}
					go gossh.DiscardRequests(creqs)
					go func() { _, _ = io.Copy(ch, up); _ = ch.CloseWrite() }()
					go func() { _, _ = io.Copy(up, ch); _ = up.Close() }()
				}
			}()
		}
	}()
	t.Cleanup(lab.dropAll)
	return lab
}

func (l *kafkaSSHLab) dropAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
	l.conns = nil
}

func (l *kafkaSSHLab) loginCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.logins
}

// The default transport end to end: KafkaOpen on a connection whose
// password is in the vault, brokers named by a host only the SSH server
// resolves, one SSH login for every broker connection, and a dropped
// session healing on the next call.
func TestKafkaIPCTunnel(t *testing.T) {
	fc, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(2, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	_, kport, _ := net.SplitHostPort(fc.ListenAddrs()[0])
	lab := startKafkaSSHLab(t)

	a := newKafkaTestApp(t)
	v := creds.NewVault()
	v.SetPath(filepath.Join(t.TempDir(), "vault.enc"))
	if err := v.Init("lab passphrase", false); err != nil {
		t.Fatal(err)
	}
	a.vault = v
	if err := a.db.UpsertKnownHost(lab.host, lab.port, lab.key.Type(),
		base64.StdEncoding.EncodeToString(lab.key.Marshal()), gossh.FingerprintSHA256(lab.key)); err != nil {
		t.Fatal(err)
	}
	on, user := true, "kafka"
	port := uint16(lab.port)
	seed := "kafka.internal:" + kport
	c, err := a.db.CreateConnection(store.NewConnection{Name: "broker", Hostname: lab.host, Overrides: store.InheritableSettings{
		Username: &user, Port: &port, KafkaEnabled: &on, KafkaBootstrap: &seed,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetConnectionPassword(c.ID, "secret"); err != nil {
		t.Fatal(err)
	}

	info, err := a.KafkaOpen(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.KafkaClose(info.ID)
	if info.Transport != "tunnel" {
		t.Fatalf("transport = %s", info.Transport)
	}
	topics, err := a.KafkaTopics(info.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Name != "events" {
		t.Fatalf("topics = %+v", topics)
	}
	if _, err := a.KafkaProduce(info.ID, kafka.ProduceRequest{Topic: "events", Partition: 1, Value: &kafka.PayloadInput{Text: "through ssh"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := a.KafkaStatus(info.ID); st.State != kafka.TransportConnected {
		t.Fatalf("state = %+v", st)
	}
	if n := lab.loginCount(); n != 1 {
		t.Fatalf("%d ssh logins, want 1", n)
	}

	// Kill the session: the next calls reconnect on their own.
	lab.dropAll()
	deadline := time.Now().Add(20 * time.Second)
	for {
		d, err := a.KafkaTopicDetail(info.ID, "events")
		if err == nil && d.Messages == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recovery after the SSH drop: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := lab.loginCount(); n != 2 {
		t.Fatalf("%d ssh logins after the drop, want 2", n)
	}
}
