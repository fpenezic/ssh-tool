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
// Loopback-only, and loopback is shared by every user on the machine
// (RDS, a multi-user Linux box), so a message must carry the token from
// instance.token - a 0600 file in the data dir that only this user can
// read. Without it any local process could make the app open a shell in a
// directory of its choosing (--open-dir) or fetch an import URL. The port
// stays alone in instance.lock so an older build still finds the primary.

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	// Version and ExePath describe the process that is handing off.
	// Without them a newer build launched while an older one is running
	// hands over its argv and exits silently, so the user double-clicks
	// the update and keeps looking at the old version with nothing to
	// suggest otherwise. Empty when the sender predates this field.
	Version string `json:"version,omitempty"`
	ExePath string `json:"exe_path,omitempty"`
	Token   string `json:"token,omitempty"`
}

// instanceMsgMax caps what serveInstance reads: an argv is a handful of
// short strings.
const instanceMsgMax = 64 << 10

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
	exePath, _ := os.Executable()
	enc := json.NewEncoder(conn)
	if err := enc.Encode(instanceMsg{
		Argv:    argv,
		Version: appVersion,
		ExePath: exePath,
		Token:   readInstanceToken(),
	}); err != nil {
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
func startInstanceServer(handler func(msg instanceMsg)) (cancel func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	token, err := writeInstanceToken()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
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
			go serveInstance(conn, token, handler)
		}
	}()
	return func() {
		_ = ln.Close()
		_ = os.Remove(instanceLockPath())
		_ = os.Remove(instanceTokenPath())
	}, nil
}

func serveInstance(conn net.Conn, token string, handler func(msg instanceMsg)) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var msg instanceMsg
	if err := json.NewDecoder(io.LimitReader(conn, instanceMsgMax)).Decode(&msg); err != nil {
		return
	}
	if subtle.ConstantTimeCompare([]byte(msg.Token), []byte(token)) != 1 {
		// No ack: an older build handing off without a token treats the
		// missing ack as delivered and exits, which is the old behaviour
		// for a message the primary did not act on.
		log.Printf("instance handoff refused: missing or wrong token")
		return
	}
	// Ack first so the secondary can exit fast; then dispatch.
	_, _ = conn.Write([]byte{1})
	handler(msg)
}

func instanceLockPath() string {
	return filepath.Join(store.DataDir(), "instance.lock")
}

func instanceTokenPath() string {
	return filepath.Join(store.DataDir(), "instance.token")
}

// writeInstanceToken makes a fresh per-run token. Written before the port
// so a secondary that sees the port also finds the token.
func writeInstanceToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	t := hex.EncodeToString(b)
	if err := os.WriteFile(instanceTokenPath(), []byte(t), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

func readInstanceToken() string {
	b, err := os.ReadFile(instanceTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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
