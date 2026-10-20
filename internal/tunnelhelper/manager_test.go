package tunnelhelper

import (
	"os"
	"strings"
	"testing"
	"time"
)

func fakeArgs(mode string) (string, []string, []string) {
	exe, _ := os.Executable()
	return exe, []string{"-test.run=^$"}, []string{"TUNNELHELPER_FAKE=" + mode}
}

// A login waiting for a one-time code does not hold up other profiles or
// status reads, and Stop ends it.
func TestManagerStartingDoesNotBlock(t *testing.T) {
	m := NewManager(nil)
	defer m.StopAll()
	exe, args, env := fakeArgs("hang")
	errc := make(chan error, 1)
	go func() {
		_, err := m.EnsureWith("slow", "slow", exe, args, env, Options{ReadyTimeout: time.Minute, OnAuth: func(*Proc, AuthChallenge) {}})
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond) // the helper is up and waiting

	done := make(chan error, 1)
	go func() {
		_ = m.Status("slow")
		_ = m.Get("other")
		exe, args, env := fakeArgs("session")
		env = append(env, "TUNNELHELPER_MARK="+t.TempDir()+"/mark")
		_, err := m.EnsureWith("other", "other", exe, args, env, Options{ReadyTimeout: 20 * time.Second})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a starting helper blocked the manager")
	}

	start := time.Now()
	m.Stop("slow")
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a stopped login came up")
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("stop took %s", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not end the starting helper")
	}
}

// An info line past 64 KiB does not stop the reader.
func TestLongHelperLine(t *testing.T) {
	p, err := fakeSpawn(t, "bigline", Options{ReadyTimeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer closeFake(p)
	if !strings.Contains(string(p.Info()), strings.Repeat("x", 1000)) {
		t.Fatal("the long info line was lost")
	}
}
