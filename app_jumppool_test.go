package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"

	"golang.org/x/crypto/ssh"
)

// jumpSettings builds resolved settings with a single jump host, so
// JumpPrefixKey returns a non-empty key. target varies the final hop
// (which must NOT affect the key).
func jumpSettings(jumpHost, target string) *store.ResolvedSettings {
	return &store.ResolvedSettings{
		Hostname: target,
		Port:     22,
		JumpHost: &store.JumpHostSpec{Hostname: jumpHost, Port: ptrU16(22)},
	}
}

func ptrU16(v uint16) *uint16 { return &v }

// newTestPool returns a pool whose build returns a distinct sentinel
// client per call and counts how many times each prefix was actually
// built and cleaned up.
func newTestPool() (*jumpPool, *int32, *int32) {
	var built, cleaned int32
	p := &jumpPool{
		entries: map[string]*jumpEntry{},
		build: func(ctx context.Context, s *store.ResolvedSettings, deps sshlayer.JumpPrefixDeps) (*ssh.Client, func(), string, error) {
			atomic.AddInt32(&built, 1)
			return &ssh.Client{}, func() { atomic.AddInt32(&cleaned, 1) }, "tunnel", nil
		},
	}
	return p, &built, &cleaned
}

func TestJumpPoolSharesOnePrefix(t *testing.T) {
	p, built, cleaned := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}

	// Three targets behind the SAME bastion. Same key -> built once.
	c1, r1, _, err := p.acquire(context.Background(), jumpSettings("bastion", "10.0.0.1"), deps)
	if err != nil {
		t.Fatal(err)
	}
	c2, r2, _, err := p.acquire(context.Background(), jumpSettings("bastion", "10.0.0.2"), deps)
	if err != nil {
		t.Fatal(err)
	}
	c3, r3, _, err := p.acquire(context.Background(), jumpSettings("bastion", "10.0.0.3"), deps)
	if err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(built); n != 1 {
		t.Fatalf("built %d times, want 1 (shared bastion)", n)
	}
	if c1 != c2 || c2 != c3 {
		t.Fatal("acquires returned different clients for the same bastion")
	}

	// Releasing the first two must NOT close the shared prefix.
	r1()
	r2()
	if n := atomic.LoadInt32(cleaned); n != 0 {
		t.Fatalf("prefix cleaned up while a rider is live (cleaned=%d)", n)
	}

	// Releasing the last arms the linger; the prefix survives until it fires.
	r3()
	if n := atomic.LoadInt32(cleaned); n != 0 {
		t.Fatalf("prefix cleaned up before linger elapsed (cleaned=%d)", n)
	}
}

func TestJumpPoolReacquireCancelsLinger(t *testing.T) {
	p, built, cleaned := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}
	s := jumpSettings("bastion", "10.0.0.1")

	_, r1, _, _ := p.acquire(context.Background(), s, deps)
	r1() // refs -> 0, linger armed

	// Reacquire before the (10s) linger fires: same entry, no rebuild.
	_, r2, _, _ := p.acquire(context.Background(), jumpSettings("bastion", "10.0.0.2"), deps)
	if n := atomic.LoadInt32(built); n != 1 {
		t.Fatalf("rebuilt during linger (built=%d, want 1)", n)
	}
	if n := atomic.LoadInt32(cleaned); n != 0 {
		t.Fatalf("prefix closed despite reacquire (cleaned=%d)", n)
	}
	r2()
}

func TestJumpPoolDifferentBastionsDistinct(t *testing.T) {
	p, built, _ := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}

	_, ra, _, _ := p.acquire(context.Background(), jumpSettings("bastionA", "10.0.0.1"), deps)
	_, rb, _, _ := p.acquire(context.Background(), jumpSettings("bastionB", "10.0.0.2"), deps)
	if n := atomic.LoadInt32(built); n != 2 {
		t.Fatalf("built %d times, want 2 (distinct bastions)", n)
	}
	ra()
	rb()
}

func TestJumpPoolNoJumpNotPooled(t *testing.T) {
	p, built, _ := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}

	// No jump host -> empty key -> acquire declines, nothing built.
	c, rel, _, err := p.acquire(context.Background(),
		&store.ResolvedSettings{Hostname: "10.0.0.1", Port: 22}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil || rel != nil {
		t.Fatal("direct connection should not be pooled")
	}
	if n := atomic.LoadInt32(built); n != 0 {
		t.Fatalf("built %d times for a no-jump connection, want 0", n)
	}
}

func TestJumpPoolStopAllClosesPrefixes(t *testing.T) {
	p, _, cleaned := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}

	// Two live prefixes.
	p.acquire(context.Background(), jumpSettings("bastionA", "10.0.0.1"), deps)
	p.acquire(context.Background(), jumpSettings("bastionB", "10.0.0.2"), deps)
	p.stopAll()
	if n := atomic.LoadInt32(cleaned); n != 2 {
		t.Fatalf("stopAll cleaned %d prefixes, want 2", n)
	}
}

// Concurrent acquires of the same key must still build exactly once
// (the pool lock serialises the build).
func TestJumpPoolConcurrentAcquireBuildsOnce(t *testing.T) {
	p, built, _ := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}
	var wg sync.WaitGroup
	releases := make([]func(), 20)
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, rel, _, err := p.acquire(context.Background(), jumpSettings("bastion", "t"), deps)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			releases[i] = rel
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if n := atomic.LoadInt32(built); n != 1 {
		t.Fatalf("concurrent acquire built %d times, want 1", n)
	}
	for _, r := range releases {
		if r != nil {
			r()
		}
	}
	_ = time.Now
}
