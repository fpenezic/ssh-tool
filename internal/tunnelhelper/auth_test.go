package tunnelhelper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

// The test binary doubles as a fake helper: TestMain hands control to
// fakeHelper when the env var names a mode.
func TestMain(m *testing.M) {
	if mode := os.Getenv("TUNNELHELPER_FAKE"); mode != "" {
		fakeHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeHelper(mode string) {
	out := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	switch mode {
	case "cert":
		out(map[string]string{"event": "error", "error": "not trusted", "cert_pin": "pin-sha256:AAAA", "cert_subject": "CN=vpn.example.com"})
		return
	case "auth":
		out(map[string]any{"event": "auth", "id": "1", "message": "Please enter your OTP password.",
			"fields": []map[string]string{{"key": "main:password:1", "name": "password", "label": "Password:", "kind": "password"}}})
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			return
		}
		var c struct {
			Cmd    string            `json:"cmd"`
			ID     string            `json:"id"`
			Values map[string]string `json:"values"`
		}
		_ = json.Unmarshal(sc.Bytes(), &c)
		if c.Cmd != "auth" || c.Values["main:password:1"] != "123456" {
			out(map[string]string{"event": "error", "error": "authentication failed"})
			return
		}
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		out(map[string]any{"event": "info", "info": map[string]any{"mtu": 1340}})
		out(map[string]any{"event": "ready", "socks": ln.Addr().String(), "protocol": 1})
		for sc.Scan() { // until stdin closes
		}
	}
}

func fakeSpawn(t *testing.T, mode string, opts Options) (*Proc, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return spawn("p1", "fake", exe, []string{"-test.run=^$"}, []string{"TUNNELHELPER_FAKE=" + mode}, opts)
}

// closeFake is the stdin-EOF shutdown without the manager's reaper.
func closeFake(p *Proc) {
	_ = p.stdin.Close()
	_ = p.cmd.Wait()
}

func TestAuthChallengeRelayed(t *testing.T) {
	var got AuthChallenge
	p, err := fakeSpawn(t, "auth", Options{
		ReadyTimeout: 20 * time.Second,
		OnAuth: func(p *Proc, ch AuthChallenge) {
			got = ch
			_ = p.Answer(ch.ID, map[string]string{"main:password:1": "123456"})
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer closeFake(p)
	if got.Message != "Please enter your OTP password." || len(got.Fields) != 1 || got.Fields[0].Kind != "password" {
		t.Fatalf("challenge = %+v", got)
	}
	var info struct{ MTU int }
	if err := json.Unmarshal(p.Info(), &info); err != nil || info.MTU != 1340 {
		t.Fatalf("info = %s (%v)", p.Info(), err)
	}
}

func TestAuthWithoutHandlerFails(t *testing.T) {
	if p, err := fakeSpawn(t, "auth", Options{ReadyTimeout: 20 * time.Second}); err == nil {
		closeFake(p)
		t.Fatal("a challenge with no handler must fail the spawn")
	}
}

func TestUntrustedCertCarriesPin(t *testing.T) {
	_, err := fakeSpawn(t, "cert", Options{})
	var ce *CertUntrustedError
	if !errors.As(err, &ce) || ce.Pin != "pin-sha256:AAAA" || ce.Subject != "CN=vpn.example.com" {
		t.Fatalf("err = %v", err)
	}
}
