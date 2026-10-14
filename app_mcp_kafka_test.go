//go:build !android && !ios

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"ssh-tool/internal/store"
)

// mcpKafkaApp opens a Kafka tab on an in-memory cluster ("events", 2
// partitions, 15 records each, every 7th carrying "needle") and
// returns the app and the tab's session id, not shared yet.
func mcpKafkaApp(t *testing.T, readOnly bool) (*App, string) {
	t.Helper()
	fc, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(2, "events"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	cl, err := kgo.NewClient(kgo.SeedBrokers(fc.ListenAddrs()...), kgo.DefaultProduceTopic("events"),
		kgo.RecordPartitioner(kgo.RoundRobinPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 30; i++ {
		v := "event-" + strings.Repeat("x", i%3)
		if i%7 == 0 {
			v += " needle"
		}
		if err := cl.ProduceSync(ctx, &kgo.Record{Value: []byte(v)}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	cl.Close()

	a := newKafkaTestApp(t)
	a.mcp = newMcpState()
	a.ctx = context.Background()
	on, off := true, false
	seed := fc.ListenAddrs()[0]
	id := kafkaTestConn(t, a, store.InheritableSettings{
		KafkaEnabled: &on, KafkaBootstrap: &seed, KafkaUseTunnel: &off, KafkaReadOnly: &readOnly,
	})
	info, err := a.KafkaOpen(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.KafkaClose(info.ID) })
	return a, info.ID
}

func TestMcpKafkaNeedsShare(t *testing.T) {
	a, sid := mcpKafkaApp(t, false)
	if _, err := a.mcpKafkaTopics(mcpKafkaTopicsArgs{SessionID: sid}); err == nil {
		t.Fatal("unshared Kafka session readable")
	}
	if err := a.McpShareSession(sid, "read"); err != nil {
		t.Fatal(err)
	}
	g := a.McpListGrants()
	if len(g) != 1 || g[0].Kind != "kafka" || !strings.HasSuffix(g[0].Name, "(Kafka)") {
		t.Fatalf("grants = %+v", g)
	}
	out, err := a.mcpKafkaTopics(mcpKafkaTopicsArgs{SessionID: sid})
	if err != nil || !strings.Contains(out, "events  partitions=2 rf=1 messages=30") {
		t.Fatalf("topics = %q, %v", out, err)
	}
	// Closing the tab drops the grant with it.
	a.KafkaClose(sid)
	if g := a.McpListGrants(); len(g) != 0 {
		t.Fatalf("grant outlived its tab: %+v", g)
	}
}

func TestMcpKafkaConsume(t *testing.T) {
	a, sid := mcpKafkaApp(t, false)
	if err := a.McpShareSession(sid, "read"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	out, err := a.mcpKafkaConsume(ctx, mcpKafkaConsumeArgs{SessionID: sid, Topic: "events", From: "earliest", MaxMessages: 5})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\np="); n != 5 || !strings.HasPrefix(out, "--- BEGIN UNTRUSTED KAFKA DATA ---\np=") || !strings.Contains(out, "5 record(s) shown") {
		t.Fatalf("earliest 5:\n%s", out)
	}

	out, err = a.mcpKafkaConsume(ctx, mcpKafkaConsumeArgs{SessionID: sid, Topic: "events", From: "earliest", Grep: "NEEDLE"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "5 record(s) shown, 30 read") || strings.Count(out, "needle") != 5 {
		t.Fatalf("grep:\n%s", out)
	}

	// Default: the last 10 of every partition, capped at 20 records.
	out, err = a.mcpKafkaConsume(ctx, mcpKafkaConsumeArgs{SessionID: sid, Topic: "events"})
	if err != nil || !strings.Contains(out, "20 record(s) shown") {
		t.Fatalf("default:\n%s %v", out, err)
	}

	// The byte budget wins over the count.
	out, err = a.mcpKafkaConsume(ctx, mcpKafkaConsumeArgs{SessionID: sid, Topic: "events", From: "earliest", MaxMessages: 30, MaxBytes: 300})
	if err != nil || !strings.Contains(out, "did not fit max_bytes") {
		t.Fatalf("max_bytes:\n%s %v", out, err)
	}

	if _, err := a.mcpKafkaConsume(ctx, mcpKafkaConsumeArgs{SessionID: sid, Topic: "events", From: "timestamp", Timestamp: "yesterday"}); err == nil {
		t.Fatal("bad timestamp: want error")
	}
	if _, err := a.mcpKafkaConsume(ctx, mcpKafkaConsumeArgs{SessionID: sid, Topic: "nope"}); err == nil {
		t.Fatal("unknown topic: want error")
	}
}

func TestMcpKafkaProduceGrants(t *testing.T) {
	a, sid := mcpKafkaApp(t, false)
	v := "hello"
	in := mcpKafkaProduceArgs{SessionID: sid, Topic: "events", Value: &v}

	_ = a.McpShareSession(sid, "read")
	if _, err := a.mcpKafkaProduce(in); err == nil || !strings.Contains(err.Error(), "read-run") {
		t.Fatalf("produce on a read share: %v", err)
	}
	// Yolo writes without a prompt.
	_ = a.McpShareSession(sid, "read-run-yolo")
	out, err := a.mcpKafkaProduce(in)
	if err != nil || !strings.HasPrefix(out, "produced to events") {
		t.Fatalf("yolo produce: %q %v", out, err)
	}
	acts := a.McpActivityList(sid)
	if len(acts) == 0 || acts[0].Kind != "kafka" || acts[0].Gate != "yolo" {
		t.Fatalf("activity = %+v", acts)
	}
}

func TestMcpKafkaProduceReadOnlyConnection(t *testing.T) {
	a, sid := mcpKafkaApp(t, true)
	_ = a.McpShareSession(sid, "read-run-yolo")
	v := "x"
	if _, err := a.mcpKafkaProduce(mcpKafkaProduceArgs{SessionID: sid, Topic: "events", Value: &v}); err != errKafkaReadOnly {
		t.Fatalf("err = %v, want read-only", err)
	}
}

func TestMcpKafkaToolsRefuseSSHSessions(t *testing.T) {
	a, _ := mcpKafkaApp(t, false)
	a.mcp.grants["ssh-session"] = mcpGrantReadOnly
	if _, err := a.mcpKafkaTopics(mcpKafkaTopicsArgs{SessionID: "ssh-session"}); err == nil || !strings.Contains(err.Error(), "not a Kafka session") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseMcpTime(t *testing.T) {
	if ms, err := parseMcpTime("1700000000000"); err != nil || ms != 1700000000000 {
		t.Fatalf("ms: %d %v", ms, err)
	}
	if ms, err := parseMcpTime("2026-01-02T03:04:05Z"); err != nil || ms != time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli() {
		t.Fatalf("rfc3339: %d %v", ms, err)
	}
}
