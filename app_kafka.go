package main

// Kafka tab backend: a connection with kafka_enabled opens a cluster handle
// (internal/kafka) whose broker connections ride the connection's SSH path.
//
// Two transports, picked by the resolved kafka_use_tunnel:
//   - tunnel (default): one shell-less SSH session to the connection's host,
//     opened on the first broker dial and again after it drops; every broker
//     connection is a direct-tcpip channel on it, so broker names the cluster
//     advertises resolve on that host.
//   - direct: each broker connection is dialed like DialVia - from the last
//     jump host through the shared bastion pool, else from here over the
//     connection's network profile. The host itself is never SSHed.
//
// The session is the cluster's own, like a VNC console's: it is not in the
// pool, has no tab and no forwards, and closing a terminal on the same host
// cannot take the Kafka tab down (or the reverse).
//
// Nothing here dials inside an IPC call that only opens or closes a handle.
// The data calls (topics, detail, consume start) do block, up to
// kafkaCallTimeout, which covers a cold SSH connect with an auth prompt.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/importer/kafkatool"
	"ssh-tool/internal/kafka"
	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/sshchan"
	"ssh-tool/internal/store"
)

// kafkaCallTimeout bounds one data call. Generous: the first call of a tab
// may wait for the SSH handshake, a host-key prompt or a password prompt.
const kafkaCallTimeout = 90 * time.Second

// KafkaClusterInfo describes an open cluster handle.
type KafkaClusterInfo struct {
	ID           string   `json:"id"`
	ConnectionID string   `json:"connection_id"`
	Name         string   `json:"name"`
	Bootstrap    []string `json:"bootstrap"`
	// Transport is "tunnel", "jump:<bastion host>" or "direct".
	Transport string `json:"transport"`
	// State is the SSH session state of a tunnel transport (kafka.Transport*),
	// "" for the others; Error the last connect error.
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// ReadOnly: the backend refuses every write on this handle.
	// Sensitive mirrors the connection's flag (destructive actions ask for
	// the topic name to be typed). Security is "plaintext", "tls", or the
	// SASL mechanism with "+tls" when both.
	ReadOnly  bool   `json:"read_only"`
	Sensitive bool   `json:"sensitive"`
	Security  string `json:"security"`
}

type kafkaCluster struct {
	info    KafkaClusterInfo
	cluster *kafka.Cluster
	lazy    *sshchan.LazySSH // nil unless Transport == "tunnel"
	// openTopics are the topic tabs the UI has open on this handle, so a
	// window that did not open them (detach, redock, reload) shows the same
	// tabs and adopts their consumers. Guarded by App.kafkaMu.
	openTopics []string
}

// errKafkaReadOnly refuses a write on a read-only cluster.
var errKafkaReadOnly = errors.New("this Kafka connection is read-only (kafka_read_only)")

type kafkaConsumer struct {
	clusterID string
	consumer  *kafka.Consumer
}

// KafkaOpen opens a cluster handle for a connection. No I/O beyond starting
// the SSH connect in the background for a tunnel transport.
func (a *App) KafkaOpen(connectionID string) (*KafkaClusterInfo, error) {
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, err
	}
	if !settings.KafkaEnabled {
		return nil, fmt.Errorf("Kafka is not enabled on this connection")
	}
	if settings.KafkaBootstrap == "" {
		return nil, fmt.Errorf("no Kafka bootstrap servers set on this connection")
	}
	bootstrap, err := kafka.ParseBootstrap(settings.KafkaBootstrap)
	if err != nil {
		return nil, err
	}
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return nil, err
	}

	kc := &kafkaCluster{info: KafkaClusterInfo{
		ID:           "kafka:" + uuid.New().String(),
		ConnectionID: connectionID,
		Name:         conn.Name,
		Bootstrap:    bootstrap,
		ReadOnly:     settings.KafkaReadOnly,
		Sensitive:    settings.Sensitive,
		Security:     "plaintext",
	}}
	cfg := kafka.Config{Bootstrap: bootstrap, ClientID: "ssh-tool"}
	if settings.KafkaTLS {
		cfg.TLS = &tls.Config{InsecureSkipVerify: settings.KafkaTLSSkipVerify} //nolint:gosec // opt-in per connection
		kc.info.Security = "tls"
	}
	if settings.KafkaSASL != "" {
		mech, err := a.kafkaSASL(settings)
		if err != nil {
			return nil, err
		}
		cfg.SASL = mech
		kc.info.Security = settings.KafkaSASL
		if settings.KafkaTLS {
			kc.info.Security += "+tls"
		}
	}
	var dial kafka.DialFunc
	if settings.KafkaUseTunnel {
		if settings.Hostname == "" {
			return nil, fmt.Errorf("connection has no hostname")
		}
		kc.info.Transport = "tunnel"
		kc.lazy = &sshchan.LazySSH{}
		kc.lazy.Connect = a.sshChanConnect("kafka", connectionID, conn, settings, kc.lazy)
		dial = kc.lazy.Dial
	} else {
		kc.info.Transport = "direct"
		if settings.JumpHost != nil {
			kc.info.Transport = "jump:" + settings.JumpHost.Hostname
		}
		dial = a.viaDial(settings)
	}

	cfg.Dial = dial
	cluster, err := kafka.Open(cfg)
	if err != nil {
		if kc.lazy != nil {
			kc.lazy.Close()
		}
		return nil, err
	}
	kc.cluster = cluster

	a.kafkaMu.Lock()
	a.kafkaClusters[kc.info.ID] = kc
	a.kafkaMu.Unlock()

	if kc.lazy != nil {
		// Warm the session up while the tab renders; the first data call
		// joins this connect instead of starting its own.
		go func() { _, _ = kc.lazy.Client(context.Background()) }()
	}
	a.recordAudit("kafka.open", connectionID, map[string]string{
		"cluster_id": kc.info.ID,
		"bootstrap":  strings.Join(bootstrap, ","),
		"transport":  kc.info.Transport,
		"security":   kc.info.Security,
		"read_only":  strconv.FormatBool(kc.info.ReadOnly),
	})
	info := kc.status()
	return &info, nil
}

// kafkaSASL resolves the SASL login from the connection's Kafka credential:
// its default username, and its password through the same path SSH uses, so
// vault, KeePass, Bitwarden and Infisical credentials all work.
func (a *App) kafkaSASL(settings *store.ResolvedSettings) (kafka.Mechanism, error) {
	if settings.KafkaCredentialRef == nil {
		return nil, fmt.Errorf("Kafka SASL %s needs a credential (Kafka section of the connection)", settings.KafkaSASL)
	}
	cred, err := a.db.GetCredential(*settings.KafkaCredentialRef)
	if err != nil {
		return nil, fmt.Errorf("Kafka credential: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf("Kafka credential not found (was it deleted?)")
	}
	user := ""
	if cred.DefaultUsername != nil {
		user = *cred.DefaultUsername
	}
	if user == "" {
		return nil, fmt.Errorf("Kafka credential %q has no username", cred.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	m, err := sshlayer.ResolveAuth(ctx, cred, a.vault, nil)
	if err != nil {
		return nil, fmt.Errorf("Kafka credential %q: %w", cred.Name, err)
	}
	if m.Password == "" {
		return nil, fmt.Errorf("Kafka credential %q holds no password", cred.Name)
	}
	return kafka.SASLMechanism(settings.KafkaSASL, user, m.Password)
}

func (kc *kafkaCluster) status() KafkaClusterInfo {
	info := kc.info
	if kc.lazy != nil {
		info.State, info.Error = kc.lazy.State()
	}
	return info
}

func (a *App) kafkaGet(clusterID string) (*kafkaCluster, error) {
	a.kafkaMu.Lock()
	defer a.kafkaMu.Unlock()
	kc, ok := a.kafkaClusters[clusterID]
	if !ok {
		return nil, fmt.Errorf("Kafka cluster %s is not open", clusterID)
	}
	return kc, nil
}

// KafkaStatus reports an open handle and its transport state.
func (a *App) KafkaStatus(clusterID string) (*KafkaClusterInfo, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	info := kc.status()
	return &info, nil
}

// KafkaClose closes a handle: its consumers, its client and its SSH session.
// Unknown ids are ignored. The teardown runs in the background so closing a
// tab never waits for a consumer's poll to return.
func (a *App) KafkaClose(clusterID string) {
	a.kafkaMu.Lock()
	kc, ok := a.kafkaClusters[clusterID]
	delete(a.kafkaClusters, clusterID)
	for id, c := range a.kafkaConsumers {
		if c.clusterID == clusterID {
			delete(a.kafkaConsumers, id)
		}
	}
	a.kafkaMu.Unlock()
	if !ok {
		return
	}
	if a.mcp != nil {
		a.clearMcpGrant(clusterID)
	}
	go func() {
		kc.cluster.Close()
		if kc.lazy != nil {
			kc.lazy.Close()
		}
	}()
}

// KafkaOverview reads the brokers and the topic / partition counts.
func (a *App) KafkaOverview(clusterID string) (*kafka.Overview, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	return kc.cluster.Overview(ctx)
}

// KafkaTopics lists the topics with partition, replica and message counts.
func (a *App) KafkaTopics(clusterID string, includeInternal bool) ([]kafka.TopicSummary, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	return kc.cluster.Topics(ctx, includeInternal)
}

// KafkaTopicDetail reads one topic's partitions, offsets and configs.
func (a *App) KafkaTopicDetail(clusterID, topic string) (*kafka.TopicDetail, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	return kc.cluster.TopicDetail(ctx, topic)
}

// KafkaConsumeStart starts a consumer and returns its id. It never joins a
// group or commits an offset.
func (a *App) KafkaConsumeStart(clusterID string, req kafka.ConsumeRequest) (string, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	k, err := kc.cluster.Consume(ctx, req)
	if err != nil {
		return "", err
	}
	id := "kc:" + uuid.New().String()
	a.kafkaMu.Lock()
	if _, open := a.kafkaClusters[clusterID]; !open {
		// Closed while the start offsets were being listed.
		a.kafkaMu.Unlock()
		k.Stop()
		return "", fmt.Errorf("Kafka cluster %s is not open", clusterID)
	}
	a.kafkaConsumers[id] = &kafkaConsumer{clusterID: clusterID, consumer: k}
	a.kafkaMu.Unlock()
	a.recordAudit("kafka.consume", kc.info.ConnectionID, map[string]string{
		"cluster_id": clusterID,
		"topic":      req.Topic,
		"mode":       req.Mode,
	})
	return id, nil
}

// Page bounds of KafkaConsumeSnapshot. The UI lists previews: a heavy topic
// with large records must not push hundreds of MiB through one IPC call or
// keep them in the webview. The full record comes from KafkaConsumeMessage.
const (
	kafkaPreviewBytes = 2 << 10
	kafkaPageBytes    = 2 << 20
)

func (a *App) kafkaConsumer(consumerID string) (*kafkaConsumer, error) {
	a.kafkaMu.Lock()
	defer a.kafkaMu.Unlock()
	c, ok := a.kafkaConsumers[consumerID]
	if !ok {
		return nil, fmt.Errorf("Kafka consumer %s is not running", consumerID)
	}
	return c, nil
}

// KafkaConsumeSnapshot returns a consumer's state and a page of records after
// the sequence number after: at most max records and about 2 MiB, every key,
// value and header cut to a 2 KiB preview. The buffer stays until
// KafkaConsumeStop, also after the consumer finished or was halted.
func (a *App) KafkaConsumeSnapshot(consumerID string, after uint64, max int) (*kafka.ConsumeSnapshot, error) {
	c, err := a.kafkaConsumer(consumerID)
	if err != nil {
		return nil, err
	}
	s := c.consumer.Snapshot(after, kafka.SnapshotOptions{Max: max, MaxBytes: kafkaPageBytes, Preview: kafkaPreviewBytes})
	return &s, nil
}

// KafkaConsumeMessage returns one buffered record in full; an error once the
// buffer has dropped it.
func (a *App) KafkaConsumeMessage(consumerID string, seq uint64) (*kafka.Message, error) {
	c, err := a.kafkaConsumer(consumerID)
	if err != nil {
		return nil, err
	}
	m, ok := c.consumer.Message(seq)
	if !ok {
		return nil, fmt.Errorf("record %d is no longer in the buffer", seq)
	}
	return &m, nil
}

// KafkaConsumeHalt stops fetching but keeps the buffer, so the records read
// so far can still be opened and exported.
func (a *App) KafkaConsumeHalt(consumerID string) {
	c, err := a.kafkaConsumer(consumerID)
	if err == nil {
		go c.consumer.Stop()
	}
}

// KafkaConsumeExport saves the buffered records matching filter as NDJSON,
// full payloads, through a save dialog. Returns the path ("" = cancelled)
// and the record count.
func (a *App) KafkaConsumeExport(consumerID, topic, filter string) (*KafkaExportResult, error) {
	c, err := a.kafkaConsumer(consumerID)
	if err != nil {
		return nil, err
	}
	path, err := SaveFileDialog(SaveFileDialogOptions{DefaultFilename: topic + ".ndjson", Title: "Export records"})
	if err != nil || path == "" {
		return &KafkaExportResult{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	n, werr := c.consumer.WriteNDJSON(f, topic, filter)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("export: %w", werr)
	}
	return &KafkaExportResult{Path: path, Records: n}, nil
}

// KafkaExportResult is where an export went.
type KafkaExportResult struct {
	Path    string `json:"path"`
	Records int    `json:"records"`
}

// KafkaConsumeStop stops a consumer and drops its buffer. Unknown ids are
// ignored.
func (a *App) KafkaConsumeStop(consumerID string) {
	a.kafkaMu.Lock()
	c, ok := a.kafkaConsumers[consumerID]
	delete(a.kafkaConsumers, consumerID)
	a.kafkaMu.Unlock()
	if ok {
		go c.consumer.Release()
	}
}

// KafkaTabState is what a Kafka tab shows on a handle, kept by the backend so
// it outlives the webview component: the open topic tabs and the consumers
// that still hold a buffer (running or finished, until KafkaConsumeStop).
type KafkaTabState struct {
	Topics    []string           `json:"topics"`
	Consumers []KafkaConsumerRef `json:"consumers"`
}

// KafkaConsumerRef is one consumer of a handle and what started it.
type KafkaConsumerRef struct {
	ID      string               `json:"id"`
	Request kafka.ConsumeRequest `json:"request"`
}

// KafkaGetTabState returns the open topic tabs and consumers of a handle.
func (a *App) KafkaGetTabState(clusterID string) (*KafkaTabState, error) {
	a.kafkaMu.Lock()
	defer a.kafkaMu.Unlock()
	kc, ok := a.kafkaClusters[clusterID]
	if !ok {
		return nil, fmt.Errorf("Kafka cluster %s is not open", clusterID)
	}
	st := &KafkaTabState{Topics: append([]string{}, kc.openTopics...), Consumers: []KafkaConsumerRef{}}
	for id, c := range a.kafkaConsumers {
		if c.clusterID == clusterID {
			st.Consumers = append(st.Consumers, KafkaConsumerRef{ID: id, Request: c.consumer.Request()})
		}
	}
	return st, nil
}

// KafkaSetOpenTopics records the topic tabs open on a handle. Unknown ids
// are ignored.
func (a *App) KafkaSetOpenTopics(clusterID string, topics []string) {
	a.kafkaMu.Lock()
	defer a.kafkaMu.Unlock()
	if kc, ok := a.kafkaClusters[clusterID]; ok {
		kc.openTopics = append([]string{}, topics...)
	}
}

// KafkaList returns every open handle. A detached window uses it to rebuild
// the session entries of the Kafka tabs it received in its layout.
func (a *App) KafkaList() []KafkaClusterInfo {
	a.kafkaMu.Lock()
	defer a.kafkaMu.Unlock()
	out := make([]KafkaClusterInfo, 0, len(a.kafkaClusters))
	for _, kc := range a.kafkaClusters {
		out = append(out, kc.status())
	}
	return out
}

// KafkaTopicMetrics samples a topic's offsets and on-disk size.
func (a *App) KafkaTopicMetrics(clusterID, topic string) (*kafka.TopicMetrics, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	return kc.cluster.TopicMetrics(ctx, topic)
}

// KafkaGroups lists the consumer groups with their lag.
func (a *App) KafkaGroups(clusterID string) ([]kafka.GroupSummary, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	return kc.cluster.Groups(ctx)
}

// KafkaGroup describes one consumer group: members and per-partition lag.
func (a *App) KafkaGroup(clusterID, group string) (*kafka.GroupDetail, error) {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	return kc.cluster.Group(ctx, group)
}

// kafkaWrite runs one write on a writable handle and records it in the audit
// log, successful or not.
func (a *App) kafkaWrite(clusterID, action string, meta map[string]string, fn func(context.Context, *kafka.Cluster) error) error {
	kc, err := a.kafkaGet(clusterID)
	if err != nil {
		return err
	}
	if kc.info.ReadOnly {
		return errKafkaReadOnly
	}
	ctx, cancel := context.WithTimeout(context.Background(), kafkaCallTimeout)
	defer cancel()
	err = fn(ctx, kc.cluster)
	meta["cluster_id"] = clusterID
	if err != nil {
		meta["error"] = err.Error()
	}
	a.recordAudit("kafka."+action, kc.info.ConnectionID, meta)
	return err
}

// KafkaProduce sends one record.
func (a *App) KafkaProduce(clusterID string, req kafka.ProduceRequest) (*kafka.ProduceResult, error) {
	var res *kafka.ProduceResult
	meta := map[string]string{"topic": req.Topic, "partition": strconv.Itoa(int(req.Partition))}
	err := a.kafkaWrite(clusterID, "produce", meta, func(ctx context.Context, c *kafka.Cluster) error {
		var err error
		res, err = c.Produce(ctx, req)
		return err
	})
	return res, err
}

// KafkaCreateTopicInput creates a topic. Partitions / ReplicationFactor -1
// take the broker defaults.
type KafkaCreateTopicInput struct {
	Name              string            `json:"name"`
	Partitions        int32             `json:"partitions"`
	ReplicationFactor int16             `json:"replication_factor"`
	Configs           map[string]string `json:"configs"`
}

// KafkaCreateTopic creates a topic.
func (a *App) KafkaCreateTopic(clusterID string, in KafkaCreateTopicInput) error {
	meta := map[string]string{
		"topic":              in.Name,
		"partitions":         strconv.Itoa(int(in.Partitions)),
		"replication_factor": strconv.Itoa(int(in.ReplicationFactor)),
	}
	return a.kafkaWrite(clusterID, "create_topic", meta, func(ctx context.Context, c *kafka.Cluster) error {
		return c.CreateTopic(ctx, strings.TrimSpace(in.Name), in.Partitions, in.ReplicationFactor, in.Configs)
	})
}

// KafkaDeleteTopic deletes a topic and its records.
func (a *App) KafkaDeleteTopic(clusterID, topic string) error {
	return a.kafkaWrite(clusterID, "delete_topic", map[string]string{"topic": topic}, func(ctx context.Context, c *kafka.Cluster) error {
		return c.DeleteTopic(ctx, topic)
	})
}

// KafkaAlterTopicConfig sets and removes topic config overrides.
func (a *App) KafkaAlterTopicConfig(clusterID, topic string, set map[string]string, remove []string) error {
	keys := make([]string, 0, len(set)+len(remove))
	for k := range set {
		keys = append(keys, k)
	}
	meta := map[string]string{"topic": topic, "set": strings.Join(keys, ","), "remove": strings.Join(remove, ",")}
	return a.kafkaWrite(clusterID, "alter_topic_config", meta, func(ctx context.Context, c *kafka.Cluster) error {
		return c.AlterTopicConfig(ctx, topic, set, remove)
	})
}

// KafkaSetPartitions raises a topic's partition count.
func (a *App) KafkaSetPartitions(clusterID, topic string, total int) error {
	meta := map[string]string{"topic": topic, "partitions": strconv.Itoa(total)}
	return a.kafkaWrite(clusterID, "set_partitions", meta, func(ctx context.Context, c *kafka.Cluster) error {
		return c.SetPartitions(ctx, topic, total)
	})
}

// KafkaSetReplicationFactor reassigns a topic's partitions onto rf brokers.
func (a *App) KafkaSetReplicationFactor(clusterID, topic string, rf int) error {
	meta := map[string]string{"topic": topic, "replication_factor": strconv.Itoa(rf)}
	return a.kafkaWrite(clusterID, "set_replication_factor", meta, func(ctx context.Context, c *kafka.Cluster) error {
		return c.SetReplicationFactor(ctx, topic, rf)
	})
}

// KafkaTruncate deletes every record currently in a topic.
func (a *App) KafkaTruncate(clusterID, topic string) error {
	return a.kafkaWrite(clusterID, "truncate", map[string]string{"topic": topic}, func(ctx context.Context, c *kafka.Cluster) error {
		return c.Truncate(ctx, topic)
	})
}

// KafkaResetGroupOffsets moves a consumer group's offsets on one topic.
func (a *App) KafkaResetGroupOffsets(clusterID string, req kafka.ResetGroupRequest) error {
	meta := map[string]string{"group": req.Group, "topic": req.Topic, "mode": req.Mode}
	return a.kafkaWrite(clusterID, "reset_group_offsets", meta, func(ctx context.Context, c *kafka.Cluster) error {
		return c.ResetGroupOffsets(ctx, req)
	})
}

// KafkaDeleteGroup deletes a consumer group and its committed offsets.
func (a *App) KafkaDeleteGroup(clusterID, group string) error {
	return a.kafkaWrite(clusterID, "delete_group", map[string]string{"group": group}, func(ctx context.Context, c *kafka.Cluster) error {
		return c.DeleteGroup(ctx, group)
	})
}

// KafkaToolImport imports kafka-tool's profiles.json: one connection with
// Kafka enabled per profile, in a "kafka-tool" folder under rootFolderID
// ("" = the root). SSH passwords go to the vault, never to the store.
func (a *App) KafkaToolImport(text string, rootFolderID string) (*kafkatool.Summary, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty import payload")
	}
	ps, err := kafkatool.Parse(text)
	if err != nil {
		return nil, err
	}
	var save func(string, string) error
	if a.vault != nil && a.vault.Status().Kind == creds.StatusUnlocked {
		save = a.SetConnectionPassword
	}
	return kafkatool.Apply(a.db, ps, rootFolderID, save)
}
