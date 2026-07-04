// ssh-tool-netbird: sidecar that joins a NetBird network as an
// embedded userspace peer and exposes it to ssh-tool through a
// loopback SOCKS5 proxy.
//
// Why a separate binary: netbirdio/netbird requires seven go.mod
// replace directives (its own wireguard-go fork among them) that
// would otherwise contaminate the main application's dependency
// tree, and it adds tens of MB that only NetBird users need. The
// main app spawns this helper per profile, dials through the SOCKS5
// port, and closes stdin to shut it down.
//
// Protocol (line-delimited JSON on stdout):
//
//	{"event":"ready","socks":"127.0.0.1:PORT"}   peer up, proxy listening
//	{"event":"status","peers":N}                 every 15s
//	{"event":"error","error":"..."}              fatal startup problem
//
// The setup key arrives via the SSHTOOL_NB_SETUP_KEY environment
// variable, never argv (argv is world-readable in the process list).
// After the first successful registration the config file under
// --state-dir carries device credentials and the key may be absent.
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
	"path/filepath"
	"time"

	"github.com/netbirdio/netbird/client/embed"
)

func emit(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fatal(format string, args ...any) {
	emit(map[string]string{"event": "error", "error": fmt.Sprintf(format, args...)})
	os.Exit(1)
}

func main() {
	management := flag.String("management", "", "management URL (empty = netbird.io cloud)")
	device := flag.String("device", "ssh-tool", "device name this peer registers under")
	stateDir := flag.String("state-dir", "", "directory for netbird config + state (required)")
	logLevel := flag.String("log-level", "warn", "netbird client log level")
	flag.Parse()

	if *stateDir == "" {
		fatal("--state-dir is required")
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		fatal("state dir: %v", err)
	}

	setupKey := os.Getenv("SSHTOOL_NB_SETUP_KEY")
	configPath := filepath.Join(*stateDir, "config.json")
	if setupKey == "" {
		if _, err := os.Stat(configPath); err != nil {
			fatal("no setup key (SSHTOOL_NB_SETUP_KEY) and no prior registration in %s", *stateDir)
		}
	}

	// The netbird client logs through logrus to stderr; keep our
	// protocol stream (stdout) clean.
	log.SetOutput(os.Stderr)

	client, err := embed.New(embed.Options{
		DeviceName:    *device,
		SetupKey:      setupKey,
		ManagementURL: *management,
		ConfigPath:    configPath,
		StatePath:     filepath.Join(*stateDir, "state.json"),
		LogOutput:     os.Stderr,
		LogLevel:      *logLevel,
	})
	if err != nil {
		fatal("netbird client: %v", err)
	}

	startCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := client.Start(startCtx); err != nil {
		cancel()
		fatal("netbird start: %v", err)
	}
	cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("socks listen: %v", err)
	}
	go serveSocks(ln, client)

	emit(map[string]string{"event": "ready", "socks": ln.Addr().String()})

	// Status heartbeat: connected peer count. Also proves liveness.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			peers := 0
			if st, err := client.Status(); err == nil {
				for _, p := range st.Peers {
					if p.ConnStatus.String() == "Connected" {
						peers++
					}
				}
			}
			emit(map[string]any{"event": "status", "peers": peers})
		}
	}()

	// Parent closes our stdin (or dies, which closes it too) ->
	// graceful shutdown. This is the only shutdown path besides
	// signals, and it works identically on Windows and Unix.
	_, _ = io.Copy(io.Discard, os.Stdin)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = client.Stop(stopCtx)
}
