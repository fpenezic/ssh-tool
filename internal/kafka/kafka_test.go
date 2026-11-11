package kafka

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestParseBootstrap(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"a:9093", []string{"a:9093"}, false},
		{"a", []string{"a:9092"}, false},
		{" a:1, b ,a:1\nc:3;d:4 ", []string{"a:1", "b:9092", "c:3", "d:4"}, false},
		{"[::1]:9093", []string{"[::1]:9093"}, false},
		{"::1", []string{"[::1]:9092"}, false},
		{"[fe80::1]", []string{"[fe80::1]:9092"}, false},
		{"", nil, true},
		{" , ", nil, true},
		{"a:0", nil, true},
		{"a:x", nil, true},
		{":9092", nil, true},
	}
	for _, c := range cases {
		got, err := ParseBootstrap(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseBootstrap(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("ParseBootstrap(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

// t0 is the timestamp of the first seeded record; record i of a partition is
// t0 + i seconds.
var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newFake starts an in-memory cluster with topic "orders" (3 partitions,
// perPart records each: key k<p>-<i>, value v<i>, a header) and an empty
// topic "empty".
func newFake(t *testing.T, perPart int) *kfake.Cluster {
	t.Helper()
	fc, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, "orders", "empty"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	produce(t, fc.ListenAddrs(), perPart, 0)
	return fc
}

// produce writes perPart records into each partition of "orders", numbering
// them from first.
func produce(t *testing.T, seeds []string, perPart, first int) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var recs []*kgo.Record
	for p := int32(0); p < 3; p++ {
		for i := first; i < first+perPart; i++ {
			recs = append(recs, &kgo.Record{
				Topic:     "orders",
				Partition: p,
				Key:       []byte(fmt.Sprintf("k%d-%d", p, i)),
				Value:     []byte(fmt.Sprintf("v%d", i)),
				Headers:   []kgo.RecordHeader{{Key: "h", Value: []byte{0xff, 0x00}}},
				Timestamp: t0.Add(time.Duration(i) * time.Second),
			})
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

func openFake(t *testing.T, fc *kfake.Cluster) *Cluster {
	t.Helper()
	c, err := Open(Config{Bootstrap: fc.ListenAddrs()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func ctx10(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestOverviewAndTopics(t *testing.T) {
	fc := newFake(t, 4)
	c := openFake(t, fc)
	ctx := ctx10(t)

	ov, err := c.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Brokers) != 1 || ov.Topics != 2 || ov.Partitions != 6 {
		t.Fatalf("overview = %+v", ov)
	}

	topics, err := c.Topics(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 || topics[0].Name != "empty" || topics[1].Name != "orders" {
		t.Fatalf("topics = %+v", topics)
	}
	o := topics[1]
	if o.Partitions != 3 || o.ReplicationFactor != 1 || o.Messages != 12 || o.Offline != 0 || o.UnderReplicated != 0 {
		t.Fatalf("orders = %+v", o)
	}
	if topics[0].Messages != 0 {
		t.Fatalf("empty = %+v", topics[0])
	}
}

func TestTopicDetail(t *testing.T) {
	fc := newFake(t, 5)
	c := openFake(t, fc)
	ctx := ctx10(t)

	d, err := c.TopicDetail(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if d.Messages != 15 || len(d.Partitions) != 3 {
		t.Fatalf("detail = %+v", d)
	}
	for i, p := range d.Partitions {
		if p.Partition != int32(i) || p.StartOffset != 0 || p.EndOffset != 5 || p.Leader < 0 || len(p.Replicas) != 1 {
			t.Fatalf("partition %d = %+v", i, p)
		}
	}
	if d.ConfigError != "" {
		t.Fatalf("config error: %s", d.ConfigError)
	}

	if _, err := c.TopicDetail(ctx, "nope"); err == nil {
		t.Fatal("unknown topic: want error")
	}
}

// drain polls a consumer until it leaves the running state and returns
// every record it buffered.
func drain(t *testing.T, k *Consumer) ([]Message, ConsumeSnapshot) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var all []Message
	var after uint64
	for {
		s := k.Snapshot(after, SnapshotOptions{})
		all = append(all, s.Messages...)
		if n := len(s.Messages); n > 0 {
			after = s.Messages[n-1].Seq
		}
		if s.State != ConsumeRunning && len(s.Messages) == 0 {
			return all, s
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer still running after 15s: %+v (got %d)", s, len(all))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConsumeModes(t *testing.T) {
	fc := newFake(t, 10)
	c := openFake(t, fc)

	cases := []struct {
		name string
		req  ConsumeRequest
		want int
		// first is the expected first offset of every partition read.
		first int64
	}{
		{"earliest", ConsumeRequest{Mode: FromEarliest, Partition: -1, StopAtEnd: true}, 30, 0},
		{"last 3", ConsumeRequest{Mode: FromLast, N: 3, Partition: -1, StopAtEnd: true}, 9, 7},
		{"last more than there is", ConsumeRequest{Mode: FromLast, N: 50, Partition: -1, StopAtEnd: true}, 30, 0},
		{"offset 6", ConsumeRequest{Mode: FromOffset, N: 6, Partition: -1, StopAtEnd: true}, 12, 6},
		{"offset past the end", ConsumeRequest{Mode: FromOffset, N: 99, Partition: -1, StopAtEnd: true}, 0, 0},
		{"one partition", ConsumeRequest{Mode: FromEarliest, Partition: 1, StopAtEnd: true}, 10, 0},
		{"limit", ConsumeRequest{Mode: FromEarliest, Partition: -1, Limit: 7}, 7, 0},
		{"timestamp", ConsumeRequest{Mode: FromTimestamp, Timestamp: t0.Add(8 * time.Second).UnixMilli(), Partition: -1, StopAtEnd: true}, 6, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Topic = "orders"
			k, err := c.Consume(ctx10(t), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			defer k.Stop()
			msgs, s := drain(t, k)
			if s.State != ConsumeDone || len(msgs) != tc.want {
				t.Fatalf("state %s, %d records; want done, %d (err %q)", s.State, len(msgs), tc.want, s.Error)
			}
			firsts := map[int32]int64{}
			for i, m := range msgs {
				if m.Seq != uint64(i+1) {
					t.Fatalf("record %d has seq %d", i, m.Seq)
				}
				if f, ok := firsts[m.Partition]; !ok || m.Offset < f {
					firsts[m.Partition] = m.Offset
				}
				if tc.req.Partition >= 0 && m.Partition != tc.req.Partition {
					t.Fatalf("record from partition %d", m.Partition)
				}
			}
			if tc.req.Limit == 0 {
				for p, f := range firsts {
					if f != tc.first {
						t.Fatalf("partition %d starts at %d, want %d", p, f, tc.first)
					}
				}
			}
		})
	}
}

func TestConsumePayloads(t *testing.T) {
	fc := newFake(t, 1)
	c := openFake(t, fc)
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: 2, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	msgs, _ := drain(t, k)
	if len(msgs) != 1 {
		t.Fatalf("got %d records", len(msgs))
	}
	m := msgs[0]
	if m.Key == nil || m.Key.Text != "k2-0" || m.Key.Encoding != "utf8" {
		t.Fatalf("key = %+v", m.Key)
	}
	if m.Value == nil || m.Value.Text != "v0" {
		t.Fatalf("value = %+v", m.Value)
	}
	// 0xff 0x00 is not UTF-8: base64.
	if len(m.Headers) != 1 || m.Headers[0].Value.Encoding != "base64" || m.Headers[0].Value.Text != "/wA=" || m.Headers[0].Value.Size != 2 {
		t.Fatalf("headers = %+v", m.Headers)
	}
	if m.Timestamp != t0.UnixMilli() {
		t.Fatalf("timestamp = %d", m.Timestamp)
	}
	if m.Size != len("k2-0")+len("v0")+len("h")+2 {
		t.Fatalf("size = %d", m.Size)
	}
}

func TestConsumeLatestFollows(t *testing.T) {
	fc := newFake(t, 3)
	c := openFake(t, fc)
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromLatest, Partition: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()

	produce(t, fc.ListenAddrs(), 2, 100)
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := k.Snapshot(0, SnapshotOptions{})
		if s.Received == 6 {
			for _, m := range s.Messages {
				if m.Value.Text != "v100" && m.Value.Text != "v101" {
					t.Fatalf("old record %q", m.Value.Text)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("received %d, want 6", s.Received)
		}
		time.Sleep(20 * time.Millisecond)
	}
	k.Stop()
	if s := k.Snapshot(0, SnapshotOptions{}); s.State != ConsumeStopped {
		t.Fatalf("after Stop: %s", s.State)
	}
}

func TestConsumeBufferAndPaging(t *testing.T) {
	fc := newFake(t, 10)
	c := openFake(t, fc)
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: -1, StopAtEnd: true, BufferMessages: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	select {
	case <-k.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("consumer did not finish")
	}
	s := k.Snapshot(0, SnapshotOptions{Max: 3})
	if s.Received != 30 || s.Dropped != 22 || s.Buffered != 8 || len(s.Messages) != 3 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Messages[0].Seq != 23 {
		t.Fatalf("oldest buffered seq = %d, want 23", s.Messages[0].Seq)
	}
	if s := k.Snapshot(28, SnapshotOptions{}); len(s.Messages) != 2 || s.Messages[0].Seq != 29 {
		t.Fatalf("after 28: %+v", s.Messages)
	}
	if s := k.Snapshot(30, SnapshotOptions{}); len(s.Messages) != 0 {
		t.Fatalf("after the last: %+v", s.Messages)
	}
}

func TestConsumeErrors(t *testing.T) {
	fc := newFake(t, 1)
	c := openFake(t, fc)
	ctx := ctx10(t)
	if _, err := c.Consume(ctx, ConsumeRequest{Topic: "nope", Mode: FromEarliest, Partition: -1}); err == nil {
		t.Fatal("unknown topic: want error")
	}
	if _, err := c.Consume(ctx, ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: 7}); err == nil {
		t.Fatal("unknown partition: want error")
	}
	if _, err := c.Consume(ctx, ConsumeRequest{Topic: "orders", Mode: "sideways", Partition: -1}); err == nil {
		t.Fatal("unknown mode: want error")
	}
}

func TestConsumeEmptyStopAtEnd(t *testing.T) {
	fc := newFake(t, 1)
	c := openFake(t, fc)
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "empty", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-k.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("consumer of an empty topic did not finish")
	}
	if s := k.Snapshot(0, SnapshotOptions{}); s.State != ConsumeDone || s.Received != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestCloseStopsConsumers(t *testing.T) {
	fc := newFake(t, 1)
	c, err := Open(Config{Bootstrap: fc.ListenAddrs()})
	if err != nil {
		t.Fatal(err)
	}
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromLatest, Partition: -1})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case <-k.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the consumer")
	}
	if _, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromLatest, Partition: -1}); err == nil {
		t.Fatal("Consume after Close: want error")
	}
}

// Large records: snapshot pages carry previews within a byte bound, the
// full record comes from Message, and the buffer keeps its byte bound while
// evicting without losing order.
func TestConsumeLargeRecords(t *testing.T) {
	fc, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "big"))
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	cl, err := kgo.NewClient(kgo.SeedBrokers(fc.ListenAddrs()...), kgo.DefaultProduceTopic("big"),
		kgo.ProducerBatchMaxBytes(4<<20))
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctx10(t)
	const n, size = 40, 300 << 10 // 40 records of 300 KiB = 12 MiB
	for i := 0; i < n; i++ {
		v := []byte(fmt.Sprintf("%04d:", i) + strings.Repeat("é", size/2))
		if err := cl.ProduceSync(ctx, &kgo.Record{Value: v}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	cl.Close()

	c := openFake(t, fc)
	k, err := c.Consume(ctx, ConsumeRequest{Topic: "big", Mode: FromEarliest, Partition: -1, StopAtEnd: true, BufferBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	select {
	case <-k.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("consumer did not finish")
	}

	full := k.Snapshot(0, SnapshotOptions{})
	if full.Received != n || full.Buffered*(size+5) > 4<<20+size+5 || full.Dropped != uint64(n-full.Buffered) {
		t.Fatalf("buffer: received %d buffered %d dropped %d", full.Received, full.Buffered, full.Dropped)
	}
	for i, m := range full.Messages {
		if m.Seq != full.Messages[0].Seq+uint64(i) || m.Value.Truncated {
			t.Fatalf("record %d: seq %d truncated %v", i, m.Seq, m.Value.Truncated)
		}
	}
	last := full.Messages[len(full.Messages)-1]
	if !strings.HasPrefix(last.Value.Text, fmt.Sprintf("%04d:", n-1)) {
		t.Fatalf("newest record lost: %q", last.Value.Text[:5])
	}

	page := k.Snapshot(0, SnapshotOptions{Preview: 1001, MaxBytes: 3000})
	if len(page.Messages) != 2 {
		t.Fatalf("page of %d previews, want 2 (1001 bytes each within 3000)", len(page.Messages))
	}
	p := page.Messages[0].Value
	if !p.Truncated || p.Size != size+5 || len(p.Text) != 1001 || !utf8.ValidString(p.Text) {
		t.Fatalf("preview: truncated %v size %d len %d", p.Truncated, p.Size, len(p.Text))
	}
	// One byte less lands inside an "é": the cut steps back to its start.
	if q := k.Snapshot(0, SnapshotOptions{Preview: 1002, Max: 1}).Messages[0].Value; len(q.Text) != 1001 {
		t.Fatalf("rune boundary: len %d", len(q.Text))
	}

	m, ok := k.Message(page.Messages[1].Seq)
	if !ok || len(m.Value.Text) != size+5 || m.Value.Truncated {
		t.Fatalf("full record: ok %v len %d", ok, len(m.Value.Text))
	}
	if _, ok := k.Message(1); ok && full.Dropped > 0 {
		t.Fatal("a dropped record is still served")
	}
	if _, ok := k.Message(n + 1); ok {
		t.Fatal("a record past the end is served")
	}
}

func TestCutBase64(t *testing.T) {
	p := cut(&Payload{Text: "QUJDREVGR0hJSg==", Encoding: "base64", Size: 10}, 7)
	if p.Text != "QUJD" || !p.Truncated {
		t.Fatalf("cut = %+v", p)
	}
	if q := cut(&Payload{Text: "abc", Encoding: "utf8"}, 7); q.Truncated {
		t.Fatal("short payload cut")
	}
	if cut(nil, 3) != nil {
		t.Fatal("nil payload")
	}
}

func TestWriteNDJSON(t *testing.T) {
	fc := newFake(t, 2)
	c := openFake(t, fc)
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	drain(t, k)
	var b strings.Builder
	n, err := k.WriteNDJSON(&b, "orders", "K1-")
	if err != nil || n != 2 {
		t.Fatalf("n = %d, %v", n, err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row["topic"] != "orders" || !strings.HasPrefix(row["key"].(string), "k1-") || row["_base64"] == nil {
		t.Fatalf("row = %v", row)
	}
	if n, _ := k.WriteNDJSON(io.Discard, "orders", ""); n != 6 {
		t.Fatalf("unfiltered: %d", n)
	}
}

func TestConsumeFilter(t *testing.T) {
	fc := newFake(t, 10)
	c := openFake(t, fc)
	run := func(req ConsumeRequest) ([]Message, ConsumeSnapshot) {
		t.Helper()
		req.Topic, req.Mode, req.Partition, req.StopAtEnd = "orders", FromEarliest, -1, true
		k, err := c.Consume(ctx10(t), req)
		if err != nil {
			t.Fatal(err)
		}
		defer k.Stop()
		return drain(t, k)
	}

	// Case-insensitive, over the key: partition 1 only.
	msgs, s := run(ConsumeRequest{KeyContains: "K1-"})
	if len(msgs) != 10 || s.Received != 10 || s.Scanned != 30 || !s.Filtering {
		t.Fatalf("key filter: %d msgs, %+v", len(msgs), s)
	}
	for i, m := range msgs {
		if m.Partition != 1 || m.Seq != uint64(i+1) {
			t.Fatalf("record %d = p%d seq %d", i, m.Partition, m.Seq)
		}
	}

	// Fields are ANDed.
	msgs, s = run(ConsumeRequest{KeyContains: "k2-", ValueContains: "V3"})
	if len(msgs) != 1 || msgs[0].Key.Text != "k2-3" || s.Scanned != 30 {
		t.Fatalf("key+value: %+v %+v", msgs, s)
	}

	// A header matches by name or by value; binary values match as text.
	if msgs, _ = run(ConsumeRequest{HeaderContains: "H"}); len(msgs) != 30 {
		t.Fatalf("header name: %d", len(msgs))
	}
	if msgs, _ = run(ConsumeRequest{HeaderContains: "nope"}); len(msgs) != 0 {
		t.Fatalf("header miss: %d", len(msgs))
	}

	// The limit counts matches: 1 stops at the first one.
	msgs, s = run(ConsumeRequest{ValueContains: "v7", Limit: 1})
	if len(msgs) != 1 || s.State != ConsumeDone || s.Scanned > 30 {
		t.Fatalf("limit 1: %d, %+v", len(msgs), s)
	}

	// No filter: scanned equals received.
	if _, s = run(ConsumeRequest{}); s.Received != 30 || s.Scanned != 30 || s.Filtering {
		t.Fatalf("no filter: %+v", s)
	}
}

func TestConsumeBudgetShared(t *testing.T) {
	fc := newFake(t, 1)
	c := openFake(t, fc)
	start := func() *Consumer {
		t.Helper()
		k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromLatest, Partition: -1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k.Stop)
		return k
	}
	a := start()
	if s := a.Snapshot(0, SnapshotOptions{}); s.BufferBytes != ClusterBufferBudget || s.BufferMessages != DefaultBufferMessages {
		t.Fatalf("alone: %+v", s)
	}
	b := start()
	for _, k := range []*Consumer{a, b} {
		if s := k.Snapshot(0, SnapshotOptions{}); s.BufferBytes != ClusterBufferBudget/2 || s.BufferMessages != DefaultBufferMessages/2 {
			t.Fatalf("two: %+v", s)
		}
	}
	// A finished consumer keeps its buffer, so it keeps its share.
	b.Stop()
	if s := a.Snapshot(0, SnapshotOptions{}); s.BufferBytes != ClusterBufferBudget/2 {
		t.Fatalf("after Stop: %+v", s)
	}
	// Release drops the buffer and returns the share.
	b.Release()
	if s := a.Snapshot(0, SnapshotOptions{}); s.BufferBytes != ClusterBufferBudget {
		t.Fatalf("after Release: %+v", s)
	}
	if s := b.Snapshot(0, SnapshotOptions{}); s.Buffered != 0 || s.State != ConsumeStopped {
		t.Fatalf("released: %+v", s)
	}
	b.Release() // idempotent
}

func TestConsumeBudgetCountsFinished(t *testing.T) {
	fc := newFake(t, 10)
	c := openFake(t, fc)
	var ks []*Consumer
	for i := 0; i < 3; i++ {
		k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "orders", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k.Release)
		drain(t, k)
		ks = append(ks, k)
	}
	// Three finished consumers still hold buffers: each has a third.
	for i, k := range ks {
		if s := k.Snapshot(0, SnapshotOptions{}); s.State != ConsumeDone || s.BufferBytes != ClusterBufferBudget/3 || s.Buffered != 30 {
			t.Fatalf("consumer %d: %+v", i, s)
		}
	}
}

func TestContainsFold(t *testing.T) {
	for _, tc := range []struct {
		b, q string
		want bool
	}{
		{"Hello World", "world", true},
		{"Hello World", "o w", true},
		{"Hello", "hello!", false},
		{"", "a", false},
		{"abc", "", true},
		{"xxABCxx", "abc", true},
		{"aaab", "aab", true},
		{"[1]", "[1]", true},
		{"ŠIBENIK", "šib", true},
		{"čaša", "ČAŠ", false}, // the needle is lower case by contract
	} {
		if got := containsFold(tc.b, tc.q); got != tc.want {
			t.Errorf("containsFold(%q, %q) = %v", tc.b, tc.q, got)
		}
		if got := containsFold([]byte(tc.b), tc.q); got != tc.want {
			t.Errorf("containsFold([]byte %q, %q) = %v", tc.b, tc.q, got)
		}
	}
	// Binary: raw text inside it, and the base64 the UI shows.
	bin := []byte{0xff, 0x00, 'O', 'r', 'd', 'e', 'r', '-', '7', 0xfe}
	if !payloadHas(bin, "order-7") {
		t.Error("raw text in binary")
	}
	if !payloadHas(bin, strings.ToLower(base64.StdEncoding.EncodeToString(bin)[:8])) {
		t.Error("base64 of binary")
	}
	if payloadHas([]byte("plain"), "cgxhaw") { // base64 of "plain" is cGxhaW4=
		t.Error("base64 matched on a text payload")
	}
}

// produceAt writes records into one partition of "empty" with the given
// timestamps (seconds after t0), keyed p<partition>-<second>.
func produceAt(t *testing.T, seeds []string, p int32, secs ...int) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.RecordPartitioner(kgo.ManualPartitioner()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var recs []*kgo.Record
	for _, s := range secs {
		recs = append(recs, &kgo.Record{
			Topic:     "empty",
			Partition: p,
			Key:       []byte(fmt.Sprintf("p%d-%d", p, s)),
			Value:     []byte(fmt.Sprintf("v%d", s)),
			Timestamp: t0.Add(time.Duration(s) * time.Second),
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

func keysOf(msgs []Message) string {
	var ks []string
	for _, m := range msgs {
		ks = append(ks, m.Key.Text)
	}
	return strings.Join(ks, " ")
}

func TestConsumeNewest(t *testing.T) {
	fc := newFake(t, 0)
	c := openFake(t, fc)
	seeds := fc.ListenAddrs()
	// Partition 0 holds the newest records, 1 the oldest, 2 some in between:
	// the newest 5 of the topic are not the last N of each partition.
	produceAt(t, seeds, 0, 100, 101, 102, 103)
	produceAt(t, seeds, 1, 1, 2, 3, 4, 5, 6)
	produceAt(t, seeds, 2, 50, 51, 52)

	run := func(t *testing.T, req ConsumeRequest) ([]Message, ConsumeSnapshot) {
		t.Helper()
		req.Topic, req.Mode, req.Partition = "empty", FromNewest, -1
		k, err := c.Consume(ctx10(t), req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k.Stop)
		return drain(t, k)
	}

	t.Run("pick", func(t *testing.T) {
		msgs, s := run(t, ConsumeRequest{N: 5, StopAtEnd: true})
		if got, want := keysOf(msgs), "p2-52 p0-100 p0-101 p0-102 p0-103"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		for i, m := range msgs {
			if m.Seq != uint64(i+1) {
				t.Fatalf("record %d has seq %d", i, m.Seq)
			}
		}
		if s.State != ConsumeDone || s.Gathering || s.Scanned != 12 || s.Received != 5 {
			t.Fatalf("snapshot %+v", s)
		}
	})
	t.Run("more than there is", func(t *testing.T) {
		msgs, _ := run(t, ConsumeRequest{N: 50, StopAtEnd: true})
		if len(msgs) != 13 || msgs[0].Key.Text != "p1-1" || msgs[12].Key.Text != "p0-103" {
			t.Fatalf("got %q", keysOf(msgs))
		}
	})
	t.Run("limit keeps the newest", func(t *testing.T) {
		msgs, s := run(t, ConsumeRequest{N: 5, Limit: 2})
		if got, want := keysOf(msgs), "p0-102 p0-103"; got != want || s.State != ConsumeDone {
			t.Fatalf("got %q (%s), want %q", got, s.State, want)
		}
	})
	t.Run("filter", func(t *testing.T) {
		// The pick is the newest N that match, among the last N read of
		// each partition.
		msgs, s := run(t, ConsumeRequest{N: 3, StopAtEnd: true, KeyContains: "P1-"})
		if got, want := keysOf(msgs), "p1-4 p1-5 p1-6"; got != want || s.Scanned != 9 {
			t.Fatalf("got %q scanned %d, want %q", got, s.Scanned, want)
		}
	})
	t.Run("then follows", func(t *testing.T) {
		req := ConsumeRequest{Topic: "empty", Mode: FromNewest, N: 2, Partition: -1}
		k, err := c.Consume(ctx10(t), req)
		if err != nil {
			t.Fatal(err)
		}
		defer k.Stop()
		deadline := time.Now().Add(10 * time.Second)
		for k.Snapshot(0, SnapshotOptions{}).Received < 2 {
			if time.Now().After(deadline) {
				t.Fatal("no pick")
			}
			time.Sleep(20 * time.Millisecond)
		}
		// A record older than the pick still comes after it: from here on it
		// is arrival order, like every other mode.
		produceAt(t, seeds, 1, 7)
		for {
			s := k.Snapshot(0, SnapshotOptions{})
			if s.Received == 3 {
				if got, want := keysOf(s.Messages), "p0-102 p0-103 p1-7"; got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("new record not read: %+v", s)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// produceTx writes records into one partition of "empty" in a committed
// transaction, so the partition's end offset is the commit marker, which a
// consumer never sees.
func produceTx(t *testing.T, seeds []string, p int32, secs ...int) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.RecordPartitioner(kgo.ManualPartitioner()), kgo.TransactionalID(fmt.Sprintf("tx-%d", p)))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cl.BeginTransaction(); err != nil {
		t.Fatal(err)
	}
	for _, s := range secs {
		r := &kgo.Record{Topic: "empty", Partition: p, Key: []byte(fmt.Sprintf("p%d-%d", p, s)), Timestamp: t0.Add(time.Duration(s) * time.Second)}
		if err := cl.ProduceSync(ctx, r).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	if err := cl.EndTransaction(ctx, kgo.TryCommit); err != nil {
		t.Fatal(err)
	}
}

// keepBusy produces into partition 1 of "empty" every 100 ms until the
// returned func is called; that func waits for the producer to stop, so it
// never touches t after the test.
func keepBusy(t *testing.T, seeds []string) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for i := 100; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				produceAt(t, seeds, 1, i)
			}
		}
	}()
	return func() { close(stop); <-done }
}

// A partition ending in a transaction marker never delivers its last
// offset. The pick must still show: on idle while other partitions stay
// busy, and as soon as a record past the end arrives.
func TestConsumeNewestTxMarker(t *testing.T) {
	waitPick := func(t *testing.T, k *Consumer, within time.Duration) ConsumeSnapshot {
		t.Helper()
		deadline := time.Now().Add(within)
		for {
			s := k.Snapshot(0, SnapshotOptions{})
			if !s.Gathering {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("still gathering after %v: %+v", within, s)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	t.Run("busy other partition", func(t *testing.T) {
		old := stopAtEndIdle
		stopAtEndIdle = 500 * time.Millisecond
		defer func() { stopAtEndIdle = old }()
		fc := newFake(t, 0)
		c := openFake(t, fc)
		seeds := fc.ListenAddrs()
		produceTx(t, seeds, 0, 10, 11, 12)
		k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "empty", Mode: FromNewest, N: 5, Partition: -1})
		if err != nil {
			t.Fatal(err)
		}
		defer k.Stop()
		defer keepBusy(t, seeds)()
		s := waitPick(t, k, 3*time.Second)
		if len(s.Messages) < 3 {
			t.Fatalf("pick of %d records after gathering: %+v", len(s.Messages), s)
		}
		if got := keysOf(s.Messages[:3]); got != "p0-10 p0-11 p0-12" {
			t.Fatalf("pick %q", got)
		}
	})

	t.Run("record past the end", func(t *testing.T) {
		fc := newFake(t, 0)
		c := openFake(t, fc)
		seeds := fc.ListenAddrs()
		produceTx(t, seeds, 0, 10, 11, 12)
		k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "empty", Mode: FromNewest, N: 5, Partition: -1})
		if err != nil {
			t.Fatal(err)
		}
		defer k.Stop()
		time.Sleep(300 * time.Millisecond)
		produceAt(t, seeds, 0, 20)
		// Well under the 5 s idle rule.
		s := waitPick(t, k, 2*time.Second)
		deadline := time.Now().Add(2 * time.Second)
		for s.Received < 4 {
			if time.Now().After(deadline) {
				t.Fatalf("record past the end not buffered: %+v", s)
			}
			time.Sleep(20 * time.Millisecond)
			s = k.Snapshot(0, SnapshotOptions{})
		}
		if got := keysOf(s.Messages); got != "p0-10 p0-11 p0-12 p0-20" {
			t.Fatalf("got %q", got)
		}
	})
}

// Stop at end over a partition that ends in a commit marker, while another
// partition keeps getting records: the idle rule must still end it.
func TestStopAtEndTxMarkerBusyTopic(t *testing.T) {
	old := stopAtEndIdle
	stopAtEndIdle = 500 * time.Millisecond
	defer func() { stopAtEndIdle = old }()
	fc := newFake(t, 0)
	c := openFake(t, fc)
	seeds := fc.ListenAddrs()
	produceTx(t, seeds, 0, 10, 11, 12)
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "empty", Mode: FromEarliest, Partition: -1, StopAtEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	defer keepBusy(t, seeds)()
	select {
	case <-k.Done():
	case <-time.After(3 * time.Second):
		t.Fatalf("still running: %+v", k.Snapshot(0, SnapshotOptions{}))
	}
}

// Records past the ends wait for the flush in a queue bounded like the
// buffer: a busy partition during a long gather cannot grow it past what
// the buffer would keep, and the counts match a plain buffer eviction.
func TestConsumeNewestLateQueueBounded(t *testing.T) {
	old := stopAtEndIdle
	stopAtEndIdle = time.Second
	defer func() { stopAtEndIdle = old }()
	fc := newFake(t, 0)
	c := openFake(t, fc)
	seeds := fc.ListenAddrs()
	produceTx(t, seeds, 0, 10, 11, 12) // gathers until the idle rule
	k, err := c.Consume(ctx10(t), ConsumeRequest{Topic: "empty", Mode: FromNewest, N: 3, Partition: -1, BufferMessages: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	secs := make([]int, 20)
	for i := range secs {
		secs[i] = 100 + i
	}
	produceAt(t, seeds, 1, secs...)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := k.Snapshot(0, SnapshotOptions{})
		// The last 3 offsets of partition 0 hold 2 records (the third is the
		// commit marker), so 2 picked + 20 late.
		if !s.Gathering && s.Received == 22 {
			if got := keysOf(s.Messages); got != "p1-117 p1-118 p1-119" || s.Dropped != 19 || s.Buffered != 3 {
				t.Fatalf("got %q, %+v", got, s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no flush: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestQueueLateBounds(t *testing.T) {
	rec := func(i int) *kgo.Record { return &kgo.Record{Key: []byte(fmt.Sprintf("k%02d", i)), Value: []byte("0123456789")} }
	k := &Consumer{maxMsgs: 3, maxBytes: 1 << 20}
	g := &gather{}
	for i := 0; i < 20; i++ {
		k.queueLate(g, rec(i))
	}
	if len(g.late) != 3 || g.lateDropped != 17 || string(g.late[0].Key) != "k17" || g.lateBytes != 39 {
		t.Fatalf("by count: %d kept, %d dropped, first %s, %d bytes", len(g.late), g.lateDropped, g.late[0].Key, g.lateBytes)
	}
	k = &Consumer{maxMsgs: 100, maxBytes: 30}
	g = &gather{}
	for i := 0; i < 20; i++ {
		k.queueLate(g, rec(i))
	}
	if len(g.late) != 2 || g.lateBytes != 26 {
		t.Fatalf("by bytes: %d kept, %d bytes", len(g.late), g.lateBytes)
	}
	// With a limit, records past it are never shown: not even queued.
	k = &Consumer{maxMsgs: 100, maxBytes: 1 << 20, req: ConsumeRequest{Limit: 5}}
	g = &gather{}
	for i := 0; i < 20; i++ {
		k.queueLate(g, rec(i))
	}
	if len(g.late) != 5 || g.lateDropped != 0 || string(g.late[4].Key) != "k04" {
		t.Fatalf("limit: %d kept, %d dropped", len(g.late), g.lateDropped)
	}
	// A limit above the buffer bound: evicted records count toward it.
	k = &Consumer{maxMsgs: 3, maxBytes: 1 << 20, req: ConsumeRequest{Limit: 8}}
	g = &gather{}
	for i := 0; i < 20; i++ {
		k.queueLate(g, rec(i))
	}
	if len(g.late) != 3 || g.lateDropped != 5 || string(g.late[2].Key) != "k07" {
		t.Fatalf("limit over the bound: %d kept, %d dropped, last %s", len(g.late), g.lateDropped, g.late[len(g.late)-1].Key)
	}
	// The filter runs before queueing.
	k = &Consumer{maxMsgs: 100, maxBytes: 1 << 20, filt: filter{key: "k1"}}
	g = &gather{}
	for i := 0; i < 20; i++ {
		k.queueLate(g, rec(i))
	}
	if len(g.late) != 10 || k.scanned != 10 {
		t.Fatalf("filter: %d kept, %d scanned", len(g.late), k.scanned)
	}
}
