package kafka

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Start modes of a consumer.
const (
	FromEarliest  = "earliest"  // the oldest retained record
	FromLatest    = "latest"    // only records produced from now on
	FromLast      = "last"      // the last N records of every partition
	FromOffset    = "offset"    // a given offset in every partition
	FromTimestamp = "timestamp" // the first record at or after a time
)

// Consumer states.
const (
	ConsumeRunning = "running"
	ConsumeDone    = "done"    // limit reached, or StopAtEnd and caught up
	ConsumeStopped = "stopped" // Stop
	ConsumeFailed  = "error"
)

// Buffer bounds. A consumer keeps the newest records within both; older ones
// are dropped and counted.
const (
	DefaultBufferMessages = 10_000
	DefaultBufferBytes    = 64 << 20
	maxSnapshot           = 1000
)

// ClusterBufferBudget is the buffer memory of all consumers of one cluster
// together: every consumer holding a buffer, running or finished, until
// Release drops it. Each gets an equal share, so listening to more topics
// keeps fewer records of each; a consumer started with its own smaller
// BufferBytes keeps that bound.
const ClusterBufferBudget = 256 << 20

// minShareMessages is the lowest record bound a shared buffer is squeezed to.
const minShareMessages = 1000

// Fetch bounds of a consumer's client. franz-go's defaults (50 MiB a fetch,
// 1 MiB a partition, plus as many fetches buffered as brokers answer) let a
// heavy topic hold hundreds of MiB in flight before the buffer evicts
// anything. A record larger than the partition limit still comes through:
// Kafka returns at least one record whatever the limit.
const (
	fetchMaxBytes          = 8 << 20
	fetchMaxPartitionBytes = 1 << 20
)

// stopAtEndIdle ends a StopAtEnd consumer that has had no record for this
// long. Offsets at the end of a partition that carry no record (transaction
// markers, compacted-away records) would otherwise keep it waiting forever.
var stopAtEndIdle = 5 * time.Second

// ConsumeRequest starts a consumer. The consumer is assign-only: no group, no
// committed offsets, nothing left behind on the cluster.
type ConsumeRequest struct {
	Topic string `json:"topic"`
	// Mode is one of the From* constants.
	Mode string `json:"mode"`
	// N is the record count for FromLast and the offset for FromOffset.
	N int64 `json:"n"`
	// Timestamp is unix milliseconds, for FromTimestamp.
	Timestamp int64 `json:"timestamp"`
	// Partition limits the consumer to one partition; -1 = all.
	Partition int32 `json:"partition"`
	// Limit stops after this many records; 0 = no limit.
	Limit int `json:"limit"`
	// StopAtEnd stops once every partition reaches the end offset it had
	// when the consumer started. Ignored for FromLatest.
	StopAtEnd bool `json:"stop_at_end"`
	// BufferMessages / BufferBytes bound the buffer; 0 = the defaults, which
	// shrink with the number of consumers holding a buffer on the cluster.
	BufferMessages int `json:"buffer_messages"`
	BufferBytes    int `json:"buffer_bytes"`
	// KeyContains, ValueContains and HeaderContains keep only the records
	// that match: a case-insensitive substring of the full key, of the full
	// value, of a header name or value. Several set = all must match. The
	// rest is counted (Scanned) but not buffered, and Limit counts matches.
	KeyContains    string `json:"key_contains"`
	ValueContains  string `json:"value_contains"`
	HeaderContains string `json:"header_contains"`
}

// filter is a ConsumeRequest's match conditions, lower case.
type filter struct{ key, value, header string }

func (f filter) active() bool { return f.key != "" || f.value != "" || f.header != "" }

func (f filter) match(r *kgo.Record) bool {
	if f.key != "" && !payloadHas(r.Key, f.key) {
		return false
	}
	if f.value != "" && !payloadHas(r.Value, f.value) {
		return false
	}
	if f.header != "" {
		hit := false
		for _, h := range r.Headers {
			if containsFold(h.Key, f.header) || payloadHas(h.Value, f.header) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// payloadHas reports whether lower occurs in b ignoring case. A binary
// payload matches on its raw bytes (text inside protobuf or Avro) and on the
// base64 text the UI shows for it.
func payloadHas[T string | []byte](b T, lower string) bool {
	if containsFold(b, lower) {
		return true
	}
	if len(b) == 0 || utf8.Valid([]byte(b)) {
		return false
	}
	return containsFold(base64.StdEncoding.EncodeToString([]byte(b)), lower)
}

// containsFold reports whether lower (already lower case) occurs in b,
// ignoring case. An ASCII needle is searched without copying b, so a filter
// on a heavy topic does not allocate a lower-case copy of every payload.
func containsFold[T string | []byte](b T, lower string) bool {
	if lower == "" {
		return true
	}
	if len(b) < len(lower) {
		return false
	}
	for i := 0; i < len(lower); i++ {
		if lower[i] >= utf8.RuneSelf {
			return strings.Contains(strings.ToLower(string(b)), lower)
		}
	}
	first, firstUp := lower[0], upperASCII(lower[0])
	last := len(b) - len(lower)
	for i := 0; i <= last; i++ {
		if c := b[i]; c != first && c != firstUp {
			continue
		}
		j := 1
		for ; j < len(lower); j++ {
			c := b[i+j]
			if c != lower[j] && c != upperASCII(lower[j]) {
				break
			}
		}
		if j == len(lower) {
			return true
		}
	}
	return false
}

func upperASCII(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// Payload is a key, value or header value. Text holds the bytes as is when
// they are valid UTF-8, else base64 with Encoding "base64".
type Payload struct {
	Text     string `json:"text"`
	Encoding string `json:"encoding"` // "utf8" | "base64"
	Size     int    `json:"size"`
	// Truncated: Text is a preview (see SnapshotOptions.Preview); Size is
	// still the full payload's.
	Truncated bool `json:"truncated,omitempty"`
}

// Header is one record header.
type Header struct {
	Key   string  `json:"key"`
	Value Payload `json:"value"`
}

// Message is one consumed record. Seq numbers the records of one consumer
// from 1 up, in arrival order.
type Message struct {
	Seq       uint64   `json:"seq"`
	Partition int32    `json:"partition"`
	Offset    int64    `json:"offset"`
	Timestamp int64    `json:"timestamp"` // unix milliseconds
	Key       *Payload `json:"key"`       // nil = null key
	Value     *Payload `json:"value"`     // nil = tombstone
	Headers   []Header `json:"headers"`
	Size      int      `json:"size"` // key + value + header bytes
}

// ConsumeSnapshot is the consumer state plus the records after a sequence
// number.
type ConsumeSnapshot struct {
	State string `json:"state"`
	// Error is the last fetch error. A running consumer can carry one: kgo
	// keeps retrying a partition whose leader is unreachable.
	Error    string    `json:"error,omitempty"`
	Messages []Message `json:"messages"`
	// Received counts the records kept so far (all of them without a
	// filter), Scanned every record read, Dropped the kept ones the buffer
	// evicted, Buffered what is in it now.
	Received uint64 `json:"received"`
	Scanned  uint64 `json:"scanned"`
	Dropped  uint64 `json:"dropped"`
	Buffered int    `json:"buffered"`
	// BufferMessages / BufferBytes are the bounds the buffer holds now; they
	// shrink while other consumers of the cluster hold buffers.
	BufferMessages int `json:"buffer_messages"`
	BufferBytes    int `json:"buffer_bytes"`
	// Filtering: the request has a match condition.
	Filtering bool `json:"filtering"`
	// Partitions is the partition set being read.
	Partitions []int32 `json:"partitions"`
}

// Consumer reads one topic into a bounded buffer until stopped.
type Consumer struct {
	req    ConsumeRequest
	filt   filter
	parent *Cluster
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	state string
	err   string
	// buf[head:] is the buffer, oldest first. Evicting moves head; the dead
	// prefix is compacted away once it is half the slice, so a full buffer
	// does not copy itself on every record.
	buf        []Message
	head       int
	bufBytes   int
	maxMsgs    int // current bounds, see ClusterBufferBudget
	maxBytes   int
	scanned    uint64
	received   uint64
	dropped    uint64
	partitions []int32
}

// Consume starts a consumer. Start offsets are resolved before it returns,
// so a bad topic or partition fails here rather than in the snapshot.
func (c *Cluster) Consume(ctx context.Context, req ConsumeRequest) (*Consumer, error) {
	if req.Topic == "" {
		return nil, errors.New("no topic")
	}
	starts, ends, err := c.startOffsets(ctx, req)
	if err != nil {
		return nil, err
	}
	parts := make([]int32, 0, len(starts))
	assign := make(map[int32]kgo.Offset, len(starts))
	for p, o := range starts {
		parts = append(parts, p)
		assign[p] = kgo.NewOffset().At(o)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })

	cl, err := kgo.NewClient(clientOpts(c.cfg,
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{req.Topic: assign}),
		kgo.FetchMaxWait(time.Second),
		kgo.FetchMaxBytes(fetchMaxBytes),
		kgo.FetchMaxPartitionBytes(fetchMaxPartitionBytes),
	)...)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	k := &Consumer{
		req:        req,
		filt:       filter{strings.ToLower(req.KeyContains), strings.ToLower(req.ValueContains), strings.ToLower(req.HeaderContains)},
		parent:     c,
		cancel:     cancel,
		done:       make(chan struct{}),
		state:      ConsumeRunning,
		partitions: parts,
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		cl.Close()
		return nil, errors.New("cluster closed")
	}
	c.consumers[k] = struct{}{}
	c.rebalanceLocked()
	c.mu.Unlock()

	var until map[int32]int64
	if req.StopAtEnd && req.Mode != FromLatest {
		until = map[int32]int64{}
		for p, s := range starts {
			if e := ends[p]; e > s {
				until[p] = e
			}
		}
	}
	go k.run(runCtx, cl, until)
	return k, nil
}

// startOffsets resolves the exact start offset of every partition read, and
// the end offsets at this moment.
func (c *Cluster) startOffsets(ctx context.Context, req ConsumeRequest) (map[int32]int64, map[int32]int64, error) {
	lo, err := c.adm.ListStartOffsets(ctx, req.Topic)
	if err != nil {
		return nil, nil, err
	}
	hi, err := c.adm.ListEndOffsets(ctx, req.Topic)
	if err != nil {
		return nil, nil, err
	}
	if err := lo.Error(); err != nil {
		return nil, nil, fmt.Errorf("topic %q: %w", req.Topic, err)
	}
	if err := hi.Error(); err != nil {
		return nil, nil, fmt.Errorf("topic %q: %w", req.Topic, err)
	}
	var at kadm.ListedOffsets
	if req.Mode == FromTimestamp {
		if at, err = c.adm.ListOffsetsAfterMilli(ctx, req.Timestamp, req.Topic); err != nil {
			return nil, nil, err
		}
	}
	starts := map[int32]int64{}
	ends := map[int32]int64{}
	for p, s := range lo[req.Topic] {
		if req.Partition >= 0 && p != req.Partition {
			continue
		}
		e, ok := hi.Lookup(req.Topic, p)
		if !ok {
			continue
		}
		first, end := s.Offset, e.Offset
		var o int64
		switch req.Mode {
		case FromEarliest, "":
			o = first
		case FromLatest:
			o = end
		case FromLast:
			o = max(first, end-max(req.N, 0))
		case FromOffset:
			o = min(max(req.N, first), end)
		case FromTimestamp:
			// No record at or after the time: Kafka answers -1, or the end
			// offset, depending on the version. Both mean "nothing yet".
			o = end
			if t, ok := at.Lookup(req.Topic, p); ok && t.Err == nil && t.Offset >= 0 {
				o = min(max(t.Offset, first), end)
			}
		default:
			return nil, nil, fmt.Errorf("unknown start mode %q", req.Mode)
		}
		starts[p] = o
		ends[p] = end
	}
	if len(starts) == 0 {
		if req.Partition >= 0 {
			return nil, nil, fmt.Errorf("topic %q has no partition %d", req.Topic, req.Partition)
		}
		return nil, nil, fmt.Errorf("topic %q not found", req.Topic)
	}
	return starts, ends, nil
}

func (k *Consumer) run(ctx context.Context, cl *kgo.Client, until map[int32]int64) {
	defer close(k.done)
	defer cl.Close()

	if until != nil && len(until) == 0 {
		k.finish(ConsumeDone, "")
		return
	}
	idleSince := time.Now()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, time.Second)
		fetches := cl.PollFetches(pollCtx)
		cancel()
		if ctx.Err() != nil || fetches.IsClientClosed() {
			k.finish(ConsumeStopped, "")
			return
		}
		got := false
		limitHit := false
		fetches.EachError(func(_ string, _ int32, err error) {
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				k.setErr(err)
			}
		})
		fetches.EachRecord(func(r *kgo.Record) {
			if limitHit {
				return
			}
			got = true
			if until != nil {
				if e, ok := until[r.Partition]; ok && r.Offset+1 >= e {
					delete(until, r.Partition)
				}
			}
			if k.add(r) {
				limitHit = true
			}
		})
		if limitHit {
			k.finish(ConsumeDone, "")
			return
		}
		if until != nil {
			if len(until) == 0 {
				k.finish(ConsumeDone, "")
				return
			}
			if got {
				idleSince = time.Now()
			} else if time.Since(idleSince) > stopAtEndIdle {
				k.finish(ConsumeDone, "")
				return
			}
		}
	}
}

// add buffers one record and reports whether the limit is reached.
func (k *Consumer) add(r *kgo.Record) bool {
	if k.filt.active() && !k.filt.match(r) {
		k.mu.Lock()
		k.scanned++
		k.mu.Unlock()
		return false
	}
	m := Message{
		Partition: r.Partition,
		Offset:    r.Offset,
		Timestamp: r.Timestamp.UnixMilli(),
		Key:       payloadOf(r.Key),
		Value:     payloadOf(r.Value),
		Headers:   make([]Header, 0, len(r.Headers)),
		Size:      len(r.Key) + len(r.Value),
	}
	for _, h := range r.Headers {
		m.Headers = append(m.Headers, Header{Key: h.Key, Value: *orEmpty(payloadOf(h.Value))})
		m.Size += len(h.Key) + len(h.Value)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.scanned++
	k.received++
	m.Seq = k.received
	k.buf = append(k.buf, m)
	k.bufBytes += m.Size
	k.trimLocked()
	return k.req.Limit > 0 && k.received >= uint64(k.req.Limit)
}

// trimLocked drops the oldest records until the buffer is within its bounds.
func (k *Consumer) trimLocked() {
	for n := len(k.buf) - k.head; n > k.maxMsgs || (k.bufBytes > k.maxBytes && n > 1); n-- {
		k.bufBytes -= k.buf[k.head].Size
		k.buf[k.head] = Message{} // let the payload go
		k.head++
		k.dropped++
	}
	if k.head > 0 && k.head >= len(k.buf)/2 {
		k.buf = append(k.buf[:0:0], k.buf[k.head:]...)
		k.head = 0
	}
}

// rebalanceLocked divides ClusterBufferBudget among the consumers holding a
// buffer. The caller holds c.mu.
func (c *Cluster) rebalanceLocked() {
	n := len(c.consumers)
	if n == 0 {
		return
	}
	for k := range c.consumers {
		msgs, bytes := DefaultBufferMessages, ClusterBufferBudget/n
		if n > 1 {
			msgs = max(minShareMessages, DefaultBufferMessages/n)
		}
		if k.req.BufferMessages > 0 {
			msgs = k.req.BufferMessages
		}
		if k.req.BufferBytes > 0 {
			bytes = min(bytes, k.req.BufferBytes)
		}
		k.mu.Lock()
		k.maxMsgs, k.maxBytes = msgs, bytes
		k.trimLocked()
		k.mu.Unlock()
	}
}

func payloadOf(b []byte) *Payload {
	if b == nil {
		return nil
	}
	if utf8.Valid(b) {
		return &Payload{Text: string(b), Encoding: "utf8", Size: len(b)}
	}
	return &Payload{Text: base64.StdEncoding.EncodeToString(b), Encoding: "base64", Size: len(b)}
}

func orEmpty(p *Payload) *Payload {
	if p == nil {
		return &Payload{Encoding: "utf8"}
	}
	return p
}

func (k *Consumer) setErr(err error) {
	k.mu.Lock()
	k.err = err.Error()
	k.mu.Unlock()
}

func (k *Consumer) finish(state, errMsg string) {
	k.mu.Lock()
	if k.state == ConsumeRunning {
		k.state = state
		if errMsg != "" {
			k.err = errMsg
		}
	}
	k.mu.Unlock()
}

// SnapshotOptions bounds one page of a snapshot.
type SnapshotOptions struct {
	// Max is the record count; <= 0 or above the cap means the cap.
	Max int
	// MaxBytes stops the page once the records in it add up to this many
	// payload bytes (the first record always fits); 0 = no byte bound.
	MaxBytes int
	// Preview cuts every key, value and header value to this many bytes
	// (Truncated set); 0 = full payloads. The UI lists previews and asks
	// for one full record with Message.
	Preview int
}

// Snapshot returns the state and a page of buffered records with a sequence
// number above after. Records the buffer already dropped are skipped;
// Dropped tells the caller about them.
func (k *Consumer) Snapshot(after uint64, o SnapshotOptions) ConsumeSnapshot {
	if o.Max <= 0 || o.Max > maxSnapshot {
		o.Max = maxSnapshot
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	live := k.buf[k.head:]
	s := ConsumeSnapshot{
		State:      k.state,
		Error:      k.err,
		Received:   k.received,
		Scanned:    k.scanned,
		Dropped:    k.dropped,
		Buffered:   len(live),
		Partitions: k.partitions,
		Messages:   []Message{},

		BufferMessages: k.maxMsgs,
		BufferBytes:    k.maxBytes,
		Filtering:      k.filt.active(),
	}
	if len(live) == 0 {
		return s
	}
	i := 0
	if first := live[0].Seq; after >= first {
		i = int(after - first + 1)
	}
	bytes := 0
	for ; i < len(live) && len(s.Messages) < o.Max; i++ {
		m := live[i]
		if o.Preview > 0 {
			m = previewOf(m, o.Preview)
		}
		// The bound is on what the page carries: previews, when cut.
		n := textBytes(m)
		if o.MaxBytes > 0 && len(s.Messages) > 0 && bytes+n > o.MaxBytes {
			break
		}
		bytes += n
		s.Messages = append(s.Messages, m)
	}
	return s
}

// Message returns one buffered record in full, false once the buffer has
// dropped it.
func (k *Consumer) Message(seq uint64) (Message, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	live := k.buf[k.head:]
	if len(live) == 0 || seq < live[0].Seq {
		return Message{}, false
	}
	i := int(seq - live[0].Seq)
	if i >= len(live) {
		return Message{}, false
	}
	return live[i], true
}

// Each calls fn for every buffered record, oldest first, in full. The buffer
// is copied first, so fn may be slow without holding up the consumer.
func (k *Consumer) Each(fn func(Message)) {
	k.mu.Lock()
	live := append([]Message(nil), k.buf[k.head:]...)
	k.mu.Unlock()
	for _, m := range live {
		fn(m)
	}
}

func textBytes(m Message) int {
	n := 0
	if m.Key != nil {
		n += len(m.Key.Text)
	}
	if m.Value != nil {
		n += len(m.Value.Text)
	}
	for _, h := range m.Headers {
		n += len(h.Key) + len(h.Value.Text)
	}
	return n
}

// Matches reports whether q (lower case) occurs in the key, the value, a
// header name or a header value, ignoring case. Base64 payloads are matched
// on their base64 text. Same rule as the Kafka tab's filter.
func (m Message) Matches(q string) bool {
	if q == "" {
		return true
	}
	has := func(p *Payload) bool { return p != nil && strings.Contains(strings.ToLower(p.Text), q) }
	if has(m.Key) || has(m.Value) {
		return true
	}
	for _, h := range m.Headers {
		if strings.Contains(strings.ToLower(h.Key), q) || has(&h.Value) {
			return true
		}
	}
	return false
}

func previewOf(m Message, n int) Message {
	m.Key = cut(m.Key, n)
	m.Value = cut(m.Value, n)
	if len(m.Headers) > 0 {
		hs := make([]Header, len(m.Headers))
		for i, h := range m.Headers {
			hs[i] = Header{Key: h.Key, Value: *cut(&h.Value, n)}
		}
		m.Headers = hs
	}
	return m
}

// cut shortens a payload's text to about n bytes: on a rune boundary for
// text, on a 4-character boundary for base64 (so the preview still decodes).
func cut(p *Payload, n int) *Payload {
	if p == nil || len(p.Text) <= n {
		return p
	}
	c := *p
	end := n
	if c.Encoding == "base64" {
		end -= end % 4
	} else {
		for end > 0 && !utf8.RuneStart(c.Text[end]) {
			end--
		}
	}
	c.Text = c.Text[:end]
	c.Truncated = true
	return &c
}

// Stop ends the consumer and waits for it to let go of its connections.
// Idempotent.
func (k *Consumer) Stop() {
	k.cancel()
	<-k.done
}

// Release stops the consumer, drops its buffer and gives its share of
// ClusterBufferBudget back to the others. The consumer reads as stopped and
// empty afterwards. Idempotent.
func (k *Consumer) Release() {
	k.Stop()
	k.parent.mu.Lock()
	if _, ok := k.parent.consumers[k]; ok {
		delete(k.parent.consumers, k)
		k.parent.rebalanceLocked()
	}
	k.parent.mu.Unlock()
	k.mu.Lock()
	k.buf, k.head, k.bufBytes = nil, 0, 0
	k.mu.Unlock()
}

// Request is the request the consumer was started with.
func (k *Consumer) Request() ConsumeRequest { return k.req }

// Done is closed when the consumer has stopped for any reason.
func (k *Consumer) Done() <-chan struct{} { return k.done }

// WriteNDJSON writes the buffered records matching filter (see Matches) as
// one JSON object a line: topic, partition, offset, timestamp, key, value
// and headers as text, with "_base64" naming the fields whose text is
// base64. Full payloads, not previews. Returns the record count.
func (k *Consumer) WriteNDJSON(w io.Writer, topic, filter string) (int, error) {
	q := strings.ToLower(filter)
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	n := 0
	var err error
	text := func(p *Payload) *string {
		if p == nil {
			return nil
		}
		return &p.Text
	}
	k.Each(func(m Message) {
		if err != nil || !m.Matches(q) {
			return
		}
		row := struct {
			Topic     string            `json:"topic"`
			Partition int32             `json:"partition"`
			Offset    int64             `json:"offset"`
			Timestamp int64             `json:"timestamp"`
			Key       *string           `json:"key"`
			Value     *string           `json:"value"`
			Headers   map[string]string `json:"headers"`
			Base64    []string          `json:"_base64,omitempty"`
		}{topic, m.Partition, m.Offset, m.Timestamp, text(m.Key), text(m.Value), map[string]string{}, nil}
		if m.Key != nil && m.Key.Encoding == "base64" {
			row.Base64 = append(row.Base64, "key")
		}
		if m.Value != nil && m.Value.Encoding == "base64" {
			row.Base64 = append(row.Base64, "value")
		}
		for _, h := range m.Headers {
			row.Headers[h.Key] = h.Value.Text
			if h.Value.Encoding == "base64" {
				row.Base64 = append(row.Base64, "header:"+h.Key)
			}
		}
		if err = enc.Encode(row); err == nil {
			n++
		}
	})
	if err != nil {
		return n, err
	}
	return n, bw.Flush()
}
