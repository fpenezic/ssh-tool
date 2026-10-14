package kafka

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
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
	// BufferMessages / BufferBytes bound the buffer; 0 = the defaults.
	BufferMessages int `json:"buffer_messages"`
	BufferBytes    int `json:"buffer_bytes"`
}

// Payload is a key, value or header value. Text holds the bytes as is when
// they are valid UTF-8, else base64 with Encoding "base64".
type Payload struct {
	Text     string `json:"text"`
	Encoding string `json:"encoding"` // "utf8" | "base64"
	Size     int    `json:"size"`
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
	// Received counts every record so far, Dropped the ones the buffer
	// evicted, Buffered what is in it now.
	Received uint64 `json:"received"`
	Dropped  uint64 `json:"dropped"`
	Buffered int    `json:"buffered"`
	// Partitions is the partition set being read.
	Partitions []int32 `json:"partitions"`
}

// Consumer reads one topic into a bounded buffer until stopped.
type Consumer struct {
	req    ConsumeRequest
	parent *Cluster
	cancel context.CancelFunc
	done   chan struct{}

	mu         sync.Mutex
	state      string
	err        string
	buf        []Message // oldest first
	bufBytes   int
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
	if req.BufferMessages <= 0 {
		req.BufferMessages = DefaultBufferMessages
	}
	if req.BufferBytes <= 0 {
		req.BufferBytes = DefaultBufferBytes
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
	)...)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	k := &Consumer{
		req:        req,
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
	k.received++
	m.Seq = k.received
	k.buf = append(k.buf, m)
	k.bufBytes += m.Size
	evict := 0
	for len(k.buf)-evict > k.req.BufferMessages || (k.bufBytes > k.req.BufferBytes && len(k.buf)-evict > 1) {
		k.bufBytes -= k.buf[evict].Size
		evict++
	}
	if evict > 0 {
		k.dropped += uint64(evict)
		k.buf = append(k.buf[:0:0], k.buf[evict:]...)
	}
	return k.req.Limit > 0 && k.received >= uint64(k.req.Limit)
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
	k.parent.mu.Lock()
	if k.parent.consumers != nil {
		delete(k.parent.consumers, k)
	}
	k.parent.mu.Unlock()
}

// Snapshot returns the state and up to max buffered records with a sequence
// number above after (max <= 0 or above the cap means the cap). Records the
// buffer already dropped are skipped; Dropped tells the caller about them.
func (k *Consumer) Snapshot(after uint64, max int) ConsumeSnapshot {
	if max <= 0 || max > maxSnapshot {
		max = maxSnapshot
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	s := ConsumeSnapshot{
		State:      k.state,
		Error:      k.err,
		Received:   k.received,
		Dropped:    k.dropped,
		Buffered:   len(k.buf),
		Partitions: k.partitions,
		Messages:   []Message{},
	}
	if len(k.buf) == 0 {
		return s
	}
	i := 0
	if first := k.buf[0].Seq; after >= first {
		i = int(after - first + 1)
	}
	if i >= len(k.buf) {
		return s
	}
	end := min(i+max, len(k.buf))
	s.Messages = append(s.Messages, k.buf[i:end]...)
	return s
}

// Stop ends the consumer and waits for it to let go of its connections.
// Idempotent.
func (k *Consumer) Stop() {
	k.cancel()
	<-k.done
}

// Done is closed when the consumer has stopped for any reason.
func (k *Consumer) Done() <-chan struct{} { return k.done }
