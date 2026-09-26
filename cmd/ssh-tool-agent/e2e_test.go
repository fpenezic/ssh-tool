package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// testSSHServer accepts password "pw" and runs exec requests with sh -c.
// It counts exec requests so a test can prove a refused command never
// reached the host.
type testSSHServer struct {
	addr  string
	port  int
	key   gossh.PublicKey
	execs atomic.Int32
}

func startSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			if string(pw) == "pw" {
				return nil, nil
			}
			return nil, gossh.ErrNoAuth
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &testSSHServer{addr: ln.Addr().String(), port: ln.Addr().(*net.TCPAddr).Port, key: signer.PublicKey()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, cfg)
		}
	}()
	return s
}

func (s *testSSHServer) serve(c net.Conn, cfg *gossh.ServerConfig) {
	_, chans, reqs, err := gossh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go gossh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(gossh.UnknownChannelType, "")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					r.Reply(false, nil)
					continue
				}
				s.execs.Add(1)
				n := binary.BigEndian.Uint32(r.Payload)
				cmd := exec.Command("sh", "-c", string(r.Payload[4:4+n]))
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				r.Reply(true, nil)
				code := 0
				if err := cmd.Run(); err != nil {
					code = 1
					if ee, ok := err.(*exec.ExitError); ok {
						code = ee.ExitCode()
					}
				}
				st := make([]byte, 4)
				binary.BigEndian.PutUint32(st, uint32(code))
				ch.SendRequest("exit-status", false, st)
				return
			}
		}()
	}
}

// newTestProfile builds a throwaway profile: folder Lab with a pinned host
// "web" and a host "stranger" whose key is not pinned, folder Other with a
// host outside the agent's scope. All three point at the same test server.
func newTestProfile(t *testing.T, srv *testSSHServer) string {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := creds.NewVault()
	v.SetPath(filepath.Join(dir, "vault.enc"))
	if err := v.Init("test-passphrase", false); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("conn-pw", "pw"); err != nil {
		t.Fatal(err)
	}

	user := "agent"
	port := uint16(srv.port)
	lab, err := db.CreateFolder(store.NewFolder{Name: "Lab",
		Settings: store.InheritableSettings{Username: &user, Port: &port}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateFolder(store.NewFolder{Name: "Other",
		Settings: store.InheritableSettings{Username: &user, Port: &port}})
	if err != nil {
		t.Fatal(err)
	}
	add := func(folder *store.Folder, name, hostname string) {
		c, err := db.CreateConnection(store.NewConnection{FolderID: &folder.ID, Name: name, Hostname: hostname})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetConnectionPasswordKey(c.ID, "conn-pw"); err != nil {
			t.Fatal(err)
		}
	}
	// Two names for one server: the key is pinned for 127.0.0.1 only, so
	// "stranger" (dialled as localhost) meets an unpinned host key.
	add(lab, "web", "127.0.0.1")
	add(lab, "stranger", "localhost")
	add(other, "outside", "127.0.0.1")
	if err := db.UpsertKnownHost("127.0.0.1", srv.port, srv.key.Type(), encodeKey(srv.key),
		gossh.FingerprintSHA256(srv.key)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

// TestAgentOverMCP drives the agent the way an LLM client does: over MCP,
// against a real SSH server, with a profile and vault on disk.
func TestAgentOverMCP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test server runs commands with sh")
	}
	srv := startSSHServer(t)
	dir := newTestProfile(t, srv)
	t.Setenv("SSH_TOOL_VAULT_PASSPHRASE", "test-passphrase")

	a, err := open(&Config{DataDir: dir, Folders: []string{"Lab"}, TimeoutSeconds: 10, MaxOutputBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()

	ct, st := mcp.NewInMemoryTransports()
	ss, err := a.server().Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	t.Run("list_hosts is scoped", func(t *testing.T) {
		out, isErr := callTool(t, cs, "list_hosts", nil)
		if isErr || !strings.Contains(out, "web") || !strings.Contains(out, "stranger") || strings.Contains(out, "outside") {
			t.Fatalf("list_hosts:\n%s", out)
		}
	})

	t.Run("read-only pipeline runs", func(t *testing.T) {
		out, isErr := callTool(t, cs, "exec", map[string]any{
			"command": "echo hello-from-host 2>&1 | tr a-z A-Z; ls /nonexistent 2>/dev/null || true",
			"hosts":   []string{"web"},
		})
		if isErr || !strings.Contains(out, "HELLO-FROM-HOST") || !strings.Contains(out, "state=ok exit=0") {
			t.Fatalf("exec:\n%s", out)
		}
		if !strings.Contains(out, "BEGIN UNTRUSTED HOST OUTPUT") {
			t.Fatalf("output not fenced as untrusted:\n%s", out)
		}
	})

	t.Run("exit code and stderr come back", func(t *testing.T) {
		out, _ := callTool(t, cs, "exec", map[string]any{"command": "ls /nonexistent-dir", "hosts": []string{"web"}})
		if strings.Contains(out, "exit=0") || !strings.Contains(out, "[stderr]") {
			t.Fatalf("exec:\n%s", out)
		}
	})

	t.Run("refused command never reaches the host", func(t *testing.T) {
		before := srv.execs.Load()
		out, isErr := callTool(t, cs, "exec", map[string]any{"command": "echo x > /tmp/agent-test", "hosts": []string{"web"}})
		if !isErr || !strings.Contains(out, "writes a file") {
			t.Fatalf("exec:\n%s", out)
		}
		if srv.execs.Load() != before {
			t.Fatal("refused command was sent to the host")
		}
	})

	t.Run("host outside scope", func(t *testing.T) {
		out, isErr := callTool(t, cs, "exec", map[string]any{"command": "uptime", "hosts": []string{"outside"}})
		if !isErr || !strings.Contains(out, "not in scope") {
			t.Fatalf("exec:\n%s", out)
		}
	})

	t.Run("unpinned host key fails closed", func(t *testing.T) {
		before := srv.execs.Load()
		out, _ := callTool(t, cs, "exec", map[string]any{"command": "uptime", "hosts": []string{"stranger"}})
		if !strings.Contains(out, "not pinned") || srv.execs.Load() != before {
			t.Fatalf("exec:\n%s", out)
		}
	})

	t.Run("folder fan-out", func(t *testing.T) {
		out, _ := callTool(t, cs, "exec", map[string]any{"command": "echo ok", "folder": "Lab"})
		if strings.Count(out, "=== ") != 2 || !strings.Contains(out, "=== web") {
			t.Fatalf("exec:\n%s", out)
		}
	})

	t.Run("check_command", func(t *testing.T) {
		out, _ := callTool(t, cs, "check_command", map[string]any{"command": "systemctl restart nginx"})
		if !strings.HasPrefix(out, "refused:") {
			t.Fatalf("check_command: %s", out)
		}
	})
}
