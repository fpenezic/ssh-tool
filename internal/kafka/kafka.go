// Package kafka is the Kafka client behind a connection's Kafka tab: cluster
// overview, topics, topic detail and a buffered consumer.
//
// Every broker connection is opened by Config.Dial. The app hands in a dialer
// that opens a direct-tcpip channel on the connection's SSH session (or dials
// the way DialVia does), so the broker addresses the cluster advertises are
// resolved and reached on the far side. Nothing is bound on this machine and
// no hostname is mapped locally - the loopback-per-broker tunnels and the DNS
// override a standalone Kafka GUI needs to reach a cluster behind a bastion
// have no counterpart here.
//
// The package knows nothing of the store, the vault or the SSH layer; the app
// wires those into Config.
package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// DefaultPort is the broker port a bootstrap entry without one gets.
const DefaultPort = 9092

// DialFunc opens one broker connection. Same shape as kgo.Dialer.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config describes how to reach one cluster.
type Config struct {
	// Bootstrap is the seed broker list, host:port each, as seen from
	// wherever Dial dials from.
	Bootstrap []string
	// Dial opens broker connections. nil dials directly from this machine.
	Dial DialFunc
	// ClientID is sent to the brokers (shows up in their request logs).
	ClientID string
	// TLS, when set, wraps every broker connection (after Dial, so TLS runs
	// end to end through the tunnel). ServerName defaults to the broker's
	// advertised host.
	TLS *tls.Config
	// SASL authenticates every broker connection; nil = none.
	SASL Mechanism
}

// Mechanism is a SASL mechanism (re-exported so callers need not import
// franz-go).
type Mechanism = sasl.Mechanism

// SASL mechanism names, as stored in the kafka_sasl setting.
const (
	SASLPlain       = "plain"
	SASLScramSHA256 = "scram-sha-256"
	SASLScramSHA512 = "scram-sha-512"
)

// SASLMechanism builds a mechanism; "" means none (nil, nil).
func SASLMechanism(kind, user, pass string) (Mechanism, error) {
	switch kind {
	case "":
		return nil, nil
	case SASLPlain:
		return plain.Auth{User: user, Pass: pass}.AsMechanism(), nil
	case SASLScramSHA256:
		return scram.Auth{User: user, Pass: pass}.AsSha256Mechanism(), nil
	case SASLScramSHA512:
		return scram.Auth{User: user, Pass: pass}.AsSha512Mechanism(), nil
	}
	return nil, fmt.Errorf("unknown SASL mechanism %q", kind)
}

// dialer is Config.Dial (or a plain dial) with TLS layered on top.
func (cfg Config) dialer() DialFunc {
	base := cfg.Dial
	if base == nil {
		var d net.Dialer
		base = d.DialContext
	}
	if cfg.TLS == nil {
		return base
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := base(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tc := cfg.TLS.Clone()
		if tc.ServerName == "" {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				tc.ServerName = host
			}
		}
		conn := tls.Client(raw, tc)
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("tls handshake with %s: %w", addr, err)
		}
		return conn, nil
	}
}

// ParseBootstrap splits a bootstrap string ("a:9092,b:9092", spaces and
// newlines allowed) into host:port entries, adding DefaultPort where an entry
// has none. Duplicates are dropped, order is kept.
func ParseBootstrap(s string) ([]string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		addr, err := normalizeBroker(f)
		if err != nil {
			return nil, err
		}
		if !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no bootstrap brokers")
	}
	return out, nil
}

func normalizeBroker(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// No port: a bare host, or a bare IPv6 address in or out of brackets.
		host = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
		if host == "" || strings.ContainsAny(host, "[]") {
			return "", fmt.Errorf("bad broker address %q", s)
		}
		return net.JoinHostPort(host, strconv.Itoa(DefaultPort)), nil
	}
	n, err := strconv.Atoi(port)
	if host == "" || err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("bad broker address %q", s)
	}
	return net.JoinHostPort(host, port), nil
}

// Cluster is an open handle on one cluster. Creating it does no I/O; the
// first request dials. Safe for concurrent use.
type Cluster struct {
	cfg Config
	cl  *kgo.Client
	adm *kadm.Client

	mu        sync.Mutex
	consumers map[*Consumer]struct{}
	closed    bool
	// Producer clients, made on first Produce (see producer).
	autoProducer   *kgo.Client
	manualProducer *kgo.Client
}

// Open builds the client. It does not contact the cluster.
func Open(cfg Config) (*Cluster, error) {
	if len(cfg.Bootstrap) == 0 {
		return nil, errors.New("no bootstrap brokers")
	}
	cl, err := kgo.NewClient(clientOpts(cfg)...)
	if err != nil {
		return nil, err
	}
	return &Cluster{
		cfg:       cfg,
		cl:        cl,
		adm:       kadm.NewClient(cl),
		consumers: map[*Consumer]struct{}{},
	}, nil
}

func clientOpts(cfg Config, extra ...kgo.Opt) []kgo.Opt {
	id := cfg.ClientID
	if id == "" {
		id = "ssh-tool"
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Bootstrap...),
		kgo.ClientID(id),
		// The first dial through a cold SSH session pays for the whole
		// handshake (jump chain, auth, maybe a prompt); the dialer itself
		// does not hold kgo to this, it only bounds a plain TCP dial.
		kgo.DialTimeout(20 * time.Second),
	}
	if cfg.Dial != nil || cfg.TLS != nil {
		opts = append(opts, kgo.Dialer(cfg.dialer()))
	}
	if cfg.SASL != nil {
		opts = append(opts, kgo.SASL(cfg.SASL))
	}
	return append(opts, extra...)
}

// Close stops every consumer and closes the client.
func (c *Cluster) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	consumers := make([]*Consumer, 0, len(c.consumers))
	for k := range c.consumers {
		consumers = append(consumers, k)
	}
	c.consumers = nil
	producers := []*kgo.Client{c.autoProducer, c.manualProducer}
	c.mu.Unlock()
	for _, k := range consumers {
		k.Stop()
	}
	for _, p := range producers {
		if p != nil {
			p.Close()
		}
	}
	c.cl.Close()
}
