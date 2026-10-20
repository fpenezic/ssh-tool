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
		wait: func(*ssh.Client) error { select {} }, // fake clients never end
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

func TestJumpPoolPeek(t *testing.T) {
	p, built, _ := newTestPool()
	deps := sshlayer.JumpPrefixDeps{}
	s := jumpSettings("bastion", "10.0.0.1")
	key := sshlayer.JumpPrefixKey(s)

	// No prefix up yet -> peek returns nil and builds nothing.
	if c := p.peek(key); c != nil {
		t.Fatalf("peek returned a client with no live prefix")
	}
	if n := atomic.LoadInt32(built); n != 0 {
		t.Fatalf("peek built a prefix (built=%d), it must never build", n)
	}
	// Empty key -> nil.
	if c := p.peek(""); c != nil {
		t.Fatalf("peek(\"\") should be nil")
	}

	// Bring a prefix up via acquire; peek now returns the same client without
	// bumping refs (a later single release still tears it down on linger).
	client, release, _, err := p.acquire(context.Background(), s, deps)
	if err != nil {
		t.Fatal(err)
	}
	if pc := p.peek(key); pc != client {
		t.Fatalf("peek returned a different client than the live prefix")
	}
	// peek must not have bumped refs: the one release drops to zero and arms
	// the linger (no panic, entry still present until the timer fires).
	release()
	if p.entries[key] == nil {
		t.Fatalf("prefix removed immediately after single release; peek likely bumped refs")
	}
}

// A bastion whose connection ends is dropped, and the next acquire builds
// a new one instead of getting the dead client back.
func TestJumpPoolDeadBastionRebuilt(t *testing.T) {
	var built, cleaned int32
	var ends sync.Map // *ssh.Client -> chan struct{}
	p := &jumpPool{
		entries: map[string]*jumpEntry{},
		build: func(ctx context.Context, s *store.ResolvedSettings, d sshlayer.JumpPrefixDeps) (*ssh.Client, func(), string, error) {
			atomic.AddInt32(&built, 1)
			c := &ssh.Client{}
			ends.Store(c, make(chan struct{}))
			return c, func() { atomic.AddInt32(&cleaned, 1) }, "", nil
		},
		wait: func(c *ssh.Client) error {
			ch, _ := ends.Load(c)
			<-ch.(chan struct{})
			return nil
		},
	}
	deps := sshlayer.JumpPrefixDeps{}
	c1, r1, _, err := p.acquire(context.Background(), jumpSettings("bastion", "10.0.0.1"), deps)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := ends.Load(c1)
	close(ch.(chan struct{})) // the bastion drops
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&cleaned) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&cleaned) != 1 {
		t.Fatal("dead bastion not cleaned up")
	}
	r1()
	c2, r2, _, err := p.acquire(context.Background(), jumpSettings("bastion", "10.0.0.2"), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer r2()
	if c2 == c1 || atomic.LoadInt32(&built) != 2 {
		t.Fatalf("dead bastion handed out again (built %d)", atomic.LoadInt32(&built))
	}
	if atomic.LoadInt32(&cleaned) != 1 {
		t.Fatal("the dead bastion was cleaned up twice")
	}
}

// A slow build of one bastion does not hold up another bastion, and
// waiters for the slow one get its client.
func TestJumpPoolSlowBuildDoesNotBlockOthers(t *testing.T) {
	p, _, _ := newTestPool()
	gate := make(chan struct{})
	inner := p.build
	p.build = func(ctx context.Context, s *store.ResolvedSettings, d sshlayer.JumpPrefixDeps) (*ssh.Client, func(), string, error) {
		if s.JumpHost.Hostname == "slow" {
			<-gate
		}
		return inner(ctx, s, d)
	}
	deps := sshlayer.JumpPrefixDeps{}
	var wg sync.WaitGroup
	got := make([]*ssh.Client, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, r, _, err := p.acquire(context.Background(), jumpSettings("slow", "t"), deps)
			if err != nil {
				t.Error(err)
				return
			}
			defer r()
			got[i] = c
		}(i)
	}
	done := make(chan error, 1)
	go func() {
		_, r, _, err := p.acquire(context.Background(), jumpSettings("fast", "t"), deps)
		if err == nil {
			r()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a slow bastion build blocked another bastion")
	}
	close(gate)
	wg.Wait()
	if got[0] == nil || got[0] != got[1] {
		t.Fatal("waiters for one bastion got different clients")
	}
}
