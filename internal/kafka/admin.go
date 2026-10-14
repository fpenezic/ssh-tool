package kafka

import (
	"context"
	"fmt"
	"sort"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Broker is one broker as the cluster advertises it. Host is the advertised
// name, which may only resolve on the far side of the tunnel.
type Broker struct {
	ID   int32  `json:"id"`
	Host string `json:"host"`
	Port int32  `json:"port"`
	Rack string `json:"rack,omitempty"`
}

// Overview is the cluster at a glance.
type Overview struct {
	ClusterID  string   `json:"cluster_id"`
	Controller int32    `json:"controller"`
	Brokers    []Broker `json:"brokers"`
	// Topics and Partitions count user topics only (no internal ones).
	Topics     int `json:"topics"`
	Partitions int `json:"partitions"`
}

// TopicSummary is one row of the topic list.
type TopicSummary struct {
	Name              string `json:"name"`
	Partitions        int    `json:"partitions"`
	ReplicationFactor int    `json:"replication_factor"`
	Internal          bool   `json:"internal"`
	// Messages is the sum of end - start offsets over the partitions: the
	// retained record count, give or take compaction and transaction
	// markers. -1 when the offsets could not be listed.
	Messages int64 `json:"messages"`
	// UnderReplicated counts partitions whose ISR is smaller than their
	// replica set; Offline counts partitions with no leader.
	UnderReplicated int    `json:"under_replicated"`
	Offline         int    `json:"offline"`
	Error           string `json:"error,omitempty"`
}

// PartitionInfo is one partition of a topic.
type PartitionInfo struct {
	Partition   int32   `json:"partition"`
	Leader      int32   `json:"leader"`
	Replicas    []int32 `json:"replicas"`
	ISR         []int32 `json:"isr"`
	Offline     []int32 `json:"offline"`
	StartOffset int64   `json:"start_offset"`
	EndOffset   int64   `json:"end_offset"`
	Error       string  `json:"error,omitempty"`
}

// ConfigEntry is one topic config value.
type ConfigEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Source is Kafka's config source, e.g. DYNAMIC_TOPIC_CONFIG (set on
	// the topic), STATIC_BROKER_CONFIG, DEFAULT_CONFIG.
	Source string `json:"source"`
	// Override: the value is set on the topic itself, not inherited.
	Override  bool `json:"override"`
	Sensitive bool `json:"sensitive"`
}

// TopicDetail is everything the topic view shows.
type TopicDetail struct {
	Name              string          `json:"name"`
	Internal          bool            `json:"internal"`
	ReplicationFactor int             `json:"replication_factor"`
	Messages          int64           `json:"messages"`
	Partitions        []PartitionInfo `json:"partitions"`
	Configs           []ConfigEntry   `json:"configs"`
	// ConfigError is set when the configs could not be read (an ACL that
	// allows Describe on the topic but not DescribeConfigs); the rest of the
	// detail is still valid.
	ConfigError string `json:"config_error,omitempty"`
}

// Overview reads the cluster metadata.
func (c *Cluster) Overview(ctx context.Context) (*Overview, error) {
	m, err := c.adm.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	ov := &Overview{ClusterID: m.Cluster, Controller: m.Controller}
	for _, b := range m.Brokers {
		ov.Brokers = append(ov.Brokers, brokerOf(b))
	}
	for _, t := range m.Topics {
		if t.IsInternal {
			continue
		}
		ov.Topics++
		ov.Partitions += len(t.Partitions)
	}
	return ov, nil
}

func brokerOf(b kadm.BrokerDetail) Broker {
	out := Broker{ID: b.NodeID, Host: b.Host, Port: b.Port}
	if b.Rack != nil {
		out.Rack = *b.Rack
	}
	return out
}

// Topics lists the topics, sorted by name. Internal topics (__consumer_offsets,
// __transaction_state) are left out unless includeInternal.
func (c *Cluster) Topics(ctx context.Context, includeInternal bool) ([]TopicSummary, error) {
	details, err := c.adm.ListTopicsWithInternal(ctx)
	if err != nil {
		return nil, err
	}
	if !includeInternal {
		details.FilterInternal()
	}
	names := details.Names()
	// Message counts are a second and third round trip. A failure there
	// leaves the counts at -1 rather than failing the list.
	var starts, ends kadm.ListedOffsets
	if len(names) > 0 {
		starts, _ = c.adm.ListStartOffsets(ctx, names...)
		ends, _ = c.adm.ListEndOffsets(ctx, names...)
	}
	out := make([]TopicSummary, 0, len(details))
	for _, t := range details.Sorted() {
		s := TopicSummary{
			Name:              t.Topic,
			Partitions:        len(t.Partitions),
			ReplicationFactor: replicationFactor(t.Partitions),
			Internal:          t.IsInternal,
			Messages:          countMessages(t.Topic, t.Partitions, starts, ends),
		}
		if t.Err != nil {
			s.Error = t.Err.Error()
		}
		for _, p := range t.Partitions {
			if p.Leader < 0 {
				s.Offline++
			}
			if len(p.ISR) < len(p.Replicas) {
				s.UnderReplicated++
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// replicationFactor is the largest replica set over the partitions (a
// reassignment in flight can leave partitions with different counts).
func replicationFactor(ps kadm.PartitionDetails) int {
	rf := 0
	for _, p := range ps {
		if len(p.Replicas) > rf {
			rf = len(p.Replicas)
		}
	}
	return rf
}

func countMessages(topic string, ps kadm.PartitionDetails, starts, ends kadm.ListedOffsets) int64 {
	if starts == nil || ends == nil {
		return -1
	}
	var n int64
	for p := range ps {
		s, ok1 := starts.Lookup(topic, p)
		e, ok2 := ends.Lookup(topic, p)
		if !ok1 || !ok2 || s.Err != nil || e.Err != nil || s.Offset < 0 || e.Offset < 0 {
			return -1
		}
		n += e.Offset - s.Offset
	}
	return n
}

// TopicDetail reads one topic: partitions with their offsets, and configs.
func (c *Cluster) TopicDetail(ctx context.Context, topic string) (*TopicDetail, error) {
	details, err := c.adm.ListTopicsWithInternal(ctx, topic)
	if err != nil {
		return nil, err
	}
	t, ok := details[topic]
	if !ok {
		return nil, fmt.Errorf("topic %q not found", topic)
	}
	if t.Err != nil {
		return nil, fmt.Errorf("topic %q: %w", topic, t.Err)
	}
	starts, serr := c.adm.ListStartOffsets(ctx, topic)
	ends, eerr := c.adm.ListEndOffsets(ctx, topic)

	d := &TopicDetail{
		Name:              topic,
		Internal:          t.IsInternal,
		ReplicationFactor: replicationFactor(t.Partitions),
		Messages:          -1,
	}
	if serr == nil && eerr == nil {
		d.Messages = countMessages(topic, t.Partitions, starts, ends)
	}
	for _, p := range t.Partitions.Sorted() {
		pi := PartitionInfo{
			Partition:   p.Partition,
			Leader:      p.Leader,
			Replicas:    nonNil(p.Replicas),
			ISR:         nonNil(p.ISR),
			Offline:     nonNil(p.OfflineReplicas),
			StartOffset: -1,
			EndOffset:   -1,
		}
		if p.Err != nil {
			pi.Error = p.Err.Error()
		}
		if o, ok := starts.Lookup(topic, p.Partition); ok && o.Err == nil {
			pi.StartOffset = o.Offset
		}
		if o, ok := ends.Lookup(topic, p.Partition); ok && o.Err == nil {
			pi.EndOffset = o.Offset
		}
		d.Partitions = append(d.Partitions, pi)
	}

	d.Configs = []ConfigEntry{}
	rcs, err := c.adm.DescribeTopicConfigs(ctx, topic)
	if err == nil {
		var rc kadm.ResourceConfig
		rc, err = rcs.On(topic, nil)
		if err == nil && rc.Err != nil {
			err = rc.Err
		}
		if err == nil {
			d.Configs = configEntries(rc.Configs)
		}
	}
	if err != nil {
		d.ConfigError = err.Error()
	}
	return d, nil
}

func configEntries(cs []kadm.Config) []ConfigEntry {
	out := make([]ConfigEntry, 0, len(cs))
	for _, c := range cs {
		out = append(out, ConfigEntry{
			Name:      c.Key,
			Value:     c.MaybeValue(),
			Source:    c.Source.String(),
			Override:  c.Source == kmsg.ConfigSourceDynamicTopicConfig,
			Sensitive: c.Sensitive,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func nonNil(s []int32) []int32 {
	if s == nil {
		return []int32{}
	}
	return s
}
