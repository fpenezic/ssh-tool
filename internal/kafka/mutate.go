package kafka

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// ProduceRequest sends one record.
type ProduceRequest struct {
	Topic string `json:"topic"`
	// Partition picks the partition; -1 lets the partitioner choose (by key
	// hash when there is a key, like the Java client).
	Partition int32 `json:"partition"`
	// Key / Value: nil sends null (a null value is a tombstone).
	Key   *PayloadInput `json:"key"`
	Value *PayloadInput `json:"value"`
	// Headers are sent in order; duplicate keys are allowed.
	Headers []HeaderInput `json:"headers"`
}

// PayloadInput is a key, value or header value to send: Text as is, or
// base64-decoded when Encoding is "base64".
type PayloadInput struct {
	Text     string `json:"text"`
	Encoding string `json:"encoding"`
}

// HeaderInput is one header to send.
type HeaderInput struct {
	Key   string       `json:"key"`
	Value PayloadInput `json:"value"`
}

// ProduceResult is where the record landed.
type ProduceResult struct {
	Partition int32 `json:"partition"`
	Offset    int64 `json:"offset"`
	Timestamp int64 `json:"timestamp"`
}

func (p *PayloadInput) bytes() ([]byte, error) {
	if p == nil {
		return nil, nil
	}
	if p.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(p.Text)
		if err != nil {
			return nil, fmt.Errorf("bad base64: %w", err)
		}
		return b, nil
	}
	return []byte(p.Text), nil
}

// producer returns the client that sends records; manual picks the one whose
// partitioner honours Record.Partition. Both are made on first use and live
// as long as the cluster.
func (c *Cluster) producer(manual bool) (*kgo.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("cluster closed")
	}
	slot := &c.autoProducer
	part := kgo.StickyKeyPartitioner(nil)
	if manual {
		slot = &c.manualProducer
		part = kgo.ManualPartitioner()
	}
	if *slot == nil {
		cl, err := kgo.NewClient(clientOpts(c.cfg, kgo.RecordPartitioner(part))...)
		if err != nil {
			return nil, err
		}
		*slot = cl
	}
	return *slot, nil
}

// Produce sends one record and waits for the ack.
func (c *Cluster) Produce(ctx context.Context, req ProduceRequest) (*ProduceResult, error) {
	if req.Topic == "" {
		return nil, errors.New("no topic")
	}
	r := &kgo.Record{Topic: req.Topic, Partition: req.Partition}
	var err error
	if r.Key, err = req.Key.bytes(); err != nil {
		return nil, fmt.Errorf("key: %w", err)
	}
	if r.Value, err = req.Value.bytes(); err != nil {
		return nil, fmt.Errorf("value: %w", err)
	}
	for _, h := range req.Headers {
		v, err := h.Value.bytes()
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", h.Key, err)
		}
		if v == nil {
			v = []byte{}
		}
		r.Headers = append(r.Headers, kgo.RecordHeader{Key: h.Key, Value: v})
	}
	cl, err := c.producer(req.Partition >= 0)
	if err != nil {
		return nil, err
	}
	res := cl.ProduceSync(ctx, r)
	if err := res.FirstErr(); err != nil {
		return nil, err
	}
	out := res[0].Record
	return &ProduceResult{Partition: out.Partition, Offset: out.Offset, Timestamp: out.Timestamp.UnixMilli()}, nil
}

// CreateTopic creates a topic. replicationFactor -1 and partitions -1 take
// the broker defaults; configs are per-topic overrides.
func (c *Cluster) CreateTopic(ctx context.Context, name string, partitions int32, replicationFactor int16, configs map[string]string) error {
	if name == "" {
		return errors.New("no topic name")
	}
	var cfg map[string]*string
	if len(configs) > 0 {
		cfg = make(map[string]*string, len(configs))
		for k, v := range configs {
			cfg[k] = &v
		}
	}
	r, err := c.adm.CreateTopic(ctx, partitions, replicationFactor, cfg, name)
	if err != nil {
		return err
	}
	return withMessage(r.Err, r.ErrMessage)
}

// DeleteTopic deletes a topic and all its records.
func (c *Cluster) DeleteTopic(ctx context.Context, name string) error {
	r, err := c.adm.DeleteTopic(ctx, name)
	if err != nil {
		return err
	}
	return withMessage(r.Err, r.ErrMessage)
}

// AlterTopicConfig sets and removes topic config overrides in one request.
// A removed override falls back to the broker / default value.
func (c *Cluster) AlterTopicConfig(ctx context.Context, topic string, set map[string]string, remove []string) error {
	var ops []kadm.AlterConfig
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := set[k]
		ops = append(ops, kadm.AlterConfig{Op: kadm.SetConfig, Name: k, Value: &v})
	}
	for _, k := range remove {
		ops = append(ops, kadm.AlterConfig{Op: kadm.DeleteConfig, Name: k})
	}
	if len(ops) == 0 {
		return nil
	}
	rs, err := c.adm.AlterTopicConfigs(ctx, ops, topic)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := withMessage(r.Err, r.ErrMessage); err != nil {
			return err
		}
	}
	return nil
}

// SetPartitions raises a topic's partition count to total. Kafka cannot
// lower it.
func (c *Cluster) SetPartitions(ctx context.Context, topic string, total int) error {
	rs, err := c.adm.UpdatePartitions(ctx, total, topic)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := withMessage(r.Err, r.ErrMessage); err != nil {
			return err
		}
	}
	return nil
}

// SetReplicationFactor reassigns every partition of topic onto rf brokers,
// round robin from the partition number, keeping the current leader first
// when it is among them so the reassignment moves as little leadership as it
// can. The brokers copy the data in the background.
func (c *Cluster) SetReplicationFactor(ctx context.Context, topic string, rf int) error {
	m, err := c.adm.Metadata(ctx, topic)
	if err != nil {
		return err
	}
	t, ok := m.Topics[topic]
	if !ok || t.Err != nil {
		return fmt.Errorf("topic %q not found", topic)
	}
	brokers := make([]int32, 0, len(m.Brokers))
	for _, b := range m.Brokers {
		brokers = append(brokers, b.NodeID)
	}
	sort.Slice(brokers, func(i, j int) bool { return brokers[i] < brokers[j] })
	if rf < 1 || rf > len(brokers) {
		return fmt.Errorf("replication factor %d: the cluster has %d brokers", rf, len(brokers))
	}
	var req kadm.AlterPartitionAssignmentsReq
	for p, pd := range t.Partitions {
		req.Assign(topic, p, roundRobin(brokers, int(p), rf, pd.Leader))
	}
	rs, err := c.adm.AlterPartitionAssignments(ctx, req)
	if err != nil {
		return err
	}
	for _, ps := range rs {
		for _, r := range ps {
			if err := withMessage(r.Err, r.ErrMessage); err != nil {
				return err
			}
		}
	}
	return nil
}

func roundRobin(brokers []int32, start, n int, leader int32) []int32 {
	out := make([]int32, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, brokers[(start+i)%len(brokers)])
	}
	for i, b := range out {
		if b == leader && i > 0 {
			out[0], out[i] = out[i], out[0]
		}
	}
	return out
}

// Truncate deletes every record currently in topic (DeleteRecords up to the
// end offsets). The topic and its config stay.
func (c *Cluster) Truncate(ctx context.Context, topic string) error {
	ends, err := c.adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return err
	}
	if err := ends.Error(); err != nil {
		return err
	}
	var os kadm.Offsets
	ends.Each(func(o kadm.ListedOffset) {
		os.AddOffset(o.Topic, o.Partition, o.Offset, -1)
	})
	rs, err := c.adm.DeleteRecords(ctx, os)
	if err != nil {
		return err
	}
	for _, ps := range rs {
		for _, r := range ps {
			if r.Err != nil {
				return fmt.Errorf("partition %d: %w", r.Partition, r.Err)
			}
		}
	}
	return nil
}

// TopicMetrics is one sample of a topic's size. Rates come from two samples:
// (EndOffsets2 - EndOffsets1) / (SampledAt2 - SampledAt1).
type TopicMetrics struct {
	SampledAt int64 `json:"sampled_at"` // unix milliseconds
	// Messages is retained records (end - start), EndOffsets the sum of the
	// end offsets: every record ever written, retained or not.
	Messages   int64 `json:"messages"`
	EndOffsets int64 `json:"end_offsets"`
	// LeaderBytes is the log size on the partition leaders (the data once),
	// TotalBytes over every replica. -1 when the brokers would not say
	// (DescribeLogDirs denied).
	LeaderBytes int64  `json:"leader_bytes"`
	TotalBytes  int64  `json:"total_bytes"`
	SizeError   string `json:"size_error,omitempty"`
}

// TopicMetrics samples a topic's offsets and on-disk size.
func (c *Cluster) TopicMetrics(ctx context.Context, topic string) (*TopicMetrics, error) {
	m, err := c.adm.Metadata(ctx, topic)
	if err != nil {
		return nil, err
	}
	t, ok := m.Topics[topic]
	if !ok || t.Err != nil {
		return nil, fmt.Errorf("topic %q not found", topic)
	}
	starts, err := c.adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return nil, err
	}
	ends, err := c.adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return nil, err
	}
	out := &TopicMetrics{SampledAt: time.Now().UnixMilli(), LeaderBytes: -1, TotalBytes: -1}
	out.Messages = countMessages(topic, t.Partitions, starts, ends)
	ends.Each(func(o kadm.ListedOffset) {
		if o.Err == nil && o.Offset > 0 {
			out.EndOffsets += o.Offset
		}
	})

	var set kadm.TopicsSet
	set.Add(topic)
	dirs, err := c.adm.DescribeAllLogDirs(ctx, set)
	if err != nil {
		out.SizeError = err.Error()
		return out, nil
	}
	var leader, total int64
	dirs.Each(func(d kadm.DescribedLogDir) {
		for p, dp := range d.Topics[topic] {
			total += dp.Size
			if pd, ok := t.Partitions[p]; ok && pd.Leader == d.Broker {
				leader += dp.Size
			}
		}
	})
	out.LeaderBytes, out.TotalBytes = leader, total
	return out, nil
}

func withMessage(err error, msg string) error {
	if err == nil {
		return nil
	}
	if msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}
