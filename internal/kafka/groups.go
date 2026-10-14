package kafka

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/twmb/franz-go/pkg/kadm"
)

// GroupSummary is one row of the consumer group list.
type GroupSummary struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Protocol string `json:"protocol_type"` // "consumer", "connect", ...
	Members  int    `json:"members"`
	// Lag is the summed lag over the group's committed partitions, -1 when
	// it could not be computed; Topics the topics it has commits for.
	Lag    int64    `json:"lag"`
	Topics []string `json:"topics"`
	Error  string   `json:"error,omitempty"`
}

// GroupMember is one member of a group.
type GroupMember struct {
	ID       string             `json:"id"`
	ClientID string             `json:"client_id"`
	Host     string             `json:"host"`
	Assigned map[string][]int32 `json:"assigned"`
}

// GroupPartition is a group's position on one partition.
type GroupPartition struct {
	Topic     string `json:"topic"`
	Partition int32  `json:"partition"`
	Committed int64  `json:"committed"` // -1 = no commit
	End       int64  `json:"end"`
	Lag       int64  `json:"lag"` // -1 = unknown
	Member    string `json:"member,omitempty"`
	Error     string `json:"error,omitempty"`
}

// GroupDetail is a group with its members and per-partition lag.
type GroupDetail struct {
	GroupSummary
	Assignor   string           `json:"assignor"`
	Members    []GroupMember    `json:"member_list"`
	Partitions []GroupPartition `json:"partitions"`
}

// Groups lists the consumer groups with their lag, sorted by name.
func (c *Cluster) Groups(ctx context.Context) ([]GroupSummary, error) {
	listed, err := c.adm.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(listed))
	for n := range listed {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]GroupSummary, 0, len(names))
	if len(names) == 0 {
		return out, nil
	}
	lags, err := c.adm.Lag(ctx, names...)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		l, ok := lags[n]
		if !ok {
			lg := listed[n]
			out = append(out, GroupSummary{Name: n, State: lg.State, Protocol: lg.ProtocolType, Lag: -1, Topics: []string{}})
			continue
		}
		out = append(out, summaryOf(l))
	}
	return out, nil
}

func summaryOf(l kadm.DescribedGroupLag) GroupSummary {
	s := GroupSummary{
		Name:     l.Group,
		State:    l.State,
		Protocol: l.ProtocolType,
		Members:  len(l.Members),
		Lag:      0,
		Topics:   []string{},
	}
	if err := l.Error(); err != nil {
		s.Error = err.Error()
	}
	for t := range l.Lag {
		s.Topics = append(s.Topics, t)
	}
	sort.Strings(s.Topics)
	for _, ml := range l.Lag.Sorted() {
		if ml.Lag < 0 {
			s.Lag = -1
			break
		}
		s.Lag += ml.Lag
	}
	return s
}

// Group describes one group.
func (c *Cluster) Group(ctx context.Context, name string) (*GroupDetail, error) {
	lags, err := c.adm.Lag(ctx, name)
	if err != nil {
		return nil, err
	}
	l, ok := lags[name]
	if !ok {
		return nil, fmt.Errorf("group %q not found", name)
	}
	if l.DescribeErr != nil {
		return nil, l.DescribeErr
	}
	d := &GroupDetail{GroupSummary: summaryOf(l), Assignor: l.Protocol, Members: []GroupMember{}, Partitions: []GroupPartition{}}
	for _, m := range l.Members {
		gm := GroupMember{ID: m.MemberID, ClientID: m.ClientID, Host: m.ClientHost, Assigned: map[string][]int32{}}
		if ca, ok := m.Assigned.AsConsumer(); ok {
			for _, t := range ca.Topics {
				gm.Assigned[t.Topic] = append(gm.Assigned[t.Topic], t.Partitions...)
			}
		}
		d.Members = append(d.Members, gm)
	}
	for _, ml := range l.Lag.Sorted() {
		gp := GroupPartition{
			Topic:     ml.Topic,
			Partition: ml.Partition,
			Committed: ml.Commit.At,
			End:       ml.End.Offset,
			Lag:       ml.Lag,
		}
		if ml.Member != nil {
			gp.Member = ml.Member.ClientID
		}
		if ml.Err != nil {
			gp.Error = ml.Err.Error()
		}
		d.Partitions = append(d.Partitions, gp)
	}
	return d, nil
}

// ResetGroupRequest moves a group's committed offsets on one topic.
type ResetGroupRequest struct {
	Group string `json:"group"`
	Topic string `json:"topic"`
	// Mode: FromEarliest, FromLatest, FromOffset (N), FromTimestamp.
	Mode      string `json:"mode"`
	N         int64  `json:"n"`
	Timestamp int64  `json:"timestamp"`
}

// ErrGroupActive refuses a reset while the group has members: the broker
// would reject the commit, and the members would overwrite it anyway.
var ErrGroupActive = errors.New("the group has active members - stop its consumers first")

// ResetGroupOffsets commits new offsets for every partition of a topic in a
// group, like kafka-consumer-groups --reset-offsets --execute.
func (c *Cluster) ResetGroupOffsets(ctx context.Context, req ResetGroupRequest) error {
	described, err := c.adm.DescribeGroups(ctx, req.Group)
	if err != nil {
		return err
	}
	g, ok := described[req.Group]
	if !ok {
		return fmt.Errorf("group %q not found", req.Group)
	}
	if g.Err != nil {
		return g.Err
	}
	if len(g.Members) > 0 {
		return ErrGroupActive
	}
	starts, _, err := c.startOffsets(ctx, ConsumeRequest{
		Topic: req.Topic, Mode: req.Mode, N: req.N, Timestamp: req.Timestamp, Partition: -1,
	})
	if err != nil {
		return err
	}
	var os kadm.Offsets
	for p, o := range starts {
		os.AddOffset(req.Topic, p, o, -1)
	}
	return c.adm.CommitAllOffsets(ctx, req.Group, os)
}

// DeleteGroup deletes a group and its committed offsets. Kafka refuses while
// the group has members.
func (c *Cluster) DeleteGroup(ctx context.Context, name string) error {
	r, err := c.adm.DeleteGroup(ctx, name)
	if err != nil {
		return err
	}
	return r.Err
}
