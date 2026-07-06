// ssh-tool-tailscale: sidecar that joins a Tailscale tailnet as an
// embedded userspace node (tsnet) and exposes it to ssh-tool through a
// loopback SOCKS5 proxy.
//
// Why a separate binary: like netbird-helper, the Tailscale client pulls
// a large dependency tree that only Tailscale users need, kept out of
// the main app. tsnet runs entirely in userspace (no TUN device, no
// admin), so the same spawn / SOCKS5 / stdin-close-to-stop contract as
// the NetBird helper applies. The app is provider-agnostic
// (internal/tunnelhelper); this binary just has to speak the protocol.
//
// Protocol (line-delimited JSON on stdout), identical to netbird-helper:
//
//	{"event":"ready","socks":"127.0.0.1:PORT","protocol":1}  node up, proxy listening
//	{"event":"status","peers":N}                 every 15s
//	{"event":"error","error":"..."}              fatal startup problem
//
// The auth key arrives via the SSHTOOL_TS_AUTHKEY environment variable,
// never argv (argv is world-readable in the process list). After the
// first successful registration the state dir carries node credentials
// and the key may be absent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"tailscale.com/tsnet"
)

// tsnetDialer adapts tsnet.Server (which exposes Dial, not DialContext)
// to the dialer interface serveSocks needs. The signatures are
// otherwise identical.
type tsnetDialer struct{ srv *tsnet.Server }

func (d tsnetDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.srv.Dial(ctx, network, address)
}

func emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fatal(format string, args ...any) {
	emit(map[string]string{"event": "error", "error": fmt.Sprintf(format, args...)})
	os.Exit(1)
}

// version is stamped at build time via -X main.version=<helper tag>.
// The app compares it against the newest helper release to flag updates.
// "dev" for un-stamped builds.
var version = "dev"

// protocolVersion is the helper wire-protocol version, announced in the
// ready event. Must match a version the app speaks (tunnelhelper.go
// minProtocol/maxProtocol). Bump only on a breaking protocol change;
// kept in lockstep with netbird-helper so one app protocol range covers
// every helper.
const protocolVersion = 1

func main() {
	controlURL := flag.String("control", "", "control server URL (empty = Tailscale's; set for Headscale/self-hosted)")
	hostname := flag.String("hostname", "ssh-tool", "hostname this node registers under in the tailnet")
	stateDir := flag.String("state-dir", "", "directory for tailscale node state (required)")
	showVersion := flag.Bool("version", false, "print helper version and exit")
	flag.Parse()

	// --version prints one line and exits, so the app can read the
	// installed helper's version without a full connect.
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *stateDir == "" {
		fatal("--state-dir is required")
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		fatal("state dir: %v", err)
	}

	authKey := os.Getenv("SSHTOOL_TS_AUTHKEY")
	// After the first registration the state dir holds node creds, so a
	// blank key on a re-run is fine - tsnet reuses the stored identity.
	// A brand-new state dir with no key can't register; tsnet will block
	// on Up until it times out, surfaced as a startup error below.

	// tsnet logs verbosely; keep our protocol stream (stdout) clean by
	// sending everything to stderr, which the app forwards into its log.
	log.SetOutput(os.Stderr)

	srv := &tsnet.Server{
		Dir:       *stateDir,
		Hostname:  *hostname,
		AuthKey:   authKey,
		Ephemeral: false, // multi-machine sync: a stable node per machine
		Logf:      func(format string, args ...any) { log.Printf(format, args...) },
	}
	if *controlURL != "" {
		srv.ControlURL = *controlURL // self-hosted Headscale / custom control
	}

	// Up blocks until the node is registered and has a usable netmap, or
	// the context expires. A first-time registration with a bad/missing
	// key fails here with a clear error rather than hanging forever.
	startCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if _, err := srv.Up(startCtx); err != nil {
		cancel()
		_ = srv.Close()
		fatal("tailscale up: %v", err)
	}
	cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = srv.Close()
		fatal("socks listen: %v", err)
	}
	// srv.Dial dials INTO the tailnet, resolving MagicDNS names inside
	// the overlay - the same role NetBird's embed dialer plays. tsnet
	// spells it Dial (not DialContext), so wrap it to the dialer
	// interface the shared SOCKS server expects.
	go serveSocks(ln, tsnetDialer{srv})

	emit(map[string]any{"event": "ready", "socks": ln.Addr().String(), "protocol": protocolVersion})

	// Status heartbeat: connected peer count via the local client.
	go func() {
		lc, err := srv.LocalClient()
		if err != nil {
			return // no local client -> just skip status, ready already sent
		}
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			peers := 0
			if st, err := lc.Status(context.Background()); err == nil {
				for _, p := range st.Peer {
					if p.Online {
						peers++
					}
				}
			}
			emit(map[string]any{"event": "status", "peers": peers})
		}
	}()

	// Parent closes our stdin (or dies, which closes it) -> graceful
	// shutdown. Same single shutdown path as netbird-helper.
	_, _ = io.Copy(io.Discard, os.Stdin)

	_ = srv.Close()
}
