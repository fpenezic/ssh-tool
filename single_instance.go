//go:build !android && !ios

// Single-instance handling.
//
// First launch binds a small TCP listener on 127.0.0.1 and writes
// the chosen port into a side file in DataDir. Subsequent launches
// read that file, try to dial the first instance, and if successful
// hand off their argv (so the deep-link URL gets through) and exit.
// The running instance pops a `deep_link_import` event for the
// frontend the same way the cold-start path does, then refocuses
// its main window.
//
// Falls back to "act as the primary" if the lock file is stale
// (port unreachable) - fresh listener, overwrite the lock.
//
// Loopback-only, no auth on the wire - we trust anything that can
// already write to %APPDATA%\ssh-tool\.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ssh-tool/internal/store"
)

type instanceMsg struct {
	Argv []string `json:"argv"`
}

// trySendToRunning is called BEFORE we initialise the application
// bits. If a primary instance is already up, hand off our argv and
// return true (caller exits). On any error (no lock file, stale
// port, write fail) return false so this process becomes the
// primary.
func trySendToRunning(argv []string) bool {
	port := readInstanceLockPort()
	if port == 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 800*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	enc := json.NewEncoder(conn)
	if err := enc.Encode(instanceMsg{Argv: argv}); err != nil {
		return false
	}
	// Read a single byte ack so we know the primary actually saw it
	// before this process exits. Best-effort - timeout = treat as
	// success since the message left our socket either way.
	br := bufio.NewReader(conn)
	_, _ = br.ReadByte()
	return true
}

// startInstanceServer brings up the loopback listener and writes
// its port into the lock file. handler runs in a goroutine per
// connection. Returned cancel func tears it down on shutdown.
func startInstanceServer(handler func(argv []string)) (cancel func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := writeInstanceLockPort(port); err != nil {
		_ = ln.Close()
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveInstance(conn, handler)
		}
	}()
	return func() {
		_ = ln.Close()
		_ = os.Remove(instanceLockPath())
	}, nil
}

func serveInstance(conn net.Conn, handler func(argv []string)) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var msg instanceMsg
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		return
	}
	// Ack first so the secondary can exit fast; then dispatch.
	_, _ = conn.Write([]byte{1})
	handler(msg.Argv)
}

func instanceLockPath() string {
	return filepath.Join(store.DataDir(), "instance.lock")
}

func writeInstanceLockPort(port int) error {
	return os.WriteFile(instanceLockPath(), []byte(strconv.Itoa(port)), 0o600)
}

func readInstanceLockPort() int {
	b, err := os.ReadFile(instanceLockPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}

// waitForParentExit blocks (capped) until the relaunch parent is
// gone. Set by AppRelaunch on the child so the fresh instance doesn't
// race the dying one for store.db / the WebView2 user-data dir.
//
// Liveness probe is the parent's single-instance PORT, not its PID:
// polling a Windows PID via os.FindProcess opens a fresh process
// handle every iteration and never releases it, which keeps the
// exited process object alive - the probe then reads "alive" until
// the cap expires (field report: every relaunch stalled the full
// cap). The TCP listener dies with the process, no handles involved.
func waitForParentExit() {
	if os.Getenv("SSH_TOOL_WAIT_PID") == "" {
		return
	}
	port := readInstanceLockPort()
	if port == 0 {
		time.Sleep(500 * time.Millisecond)
		return
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 250*time.Millisecond)
		if err != nil {
			// Listener gone = parent dead. One extra beat for the OS
			// to release file handles.
			time.Sleep(300 * time.Millisecond)
			return
		}
		_ = conn.Close()
		time.Sleep(200 * time.Millisecond)
	}
}
