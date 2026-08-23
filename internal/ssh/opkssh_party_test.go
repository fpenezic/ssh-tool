package ssh

import (
	"context"
	"sync"
	"testing"
	"time"
)

// The login runs on the party's context rather than the winning connect's,
// because the user cannot tell which host owns the browser tab. They see a
// row of hosts on "Connecting..." and press Cancel on whichever one they are
// looking at, which is almost never the one performing the login.
//
// So: one member leaving must not abort a login the others still want.
func TestLoginPartySurvivesOneMemberLeaving(t *testing.T) {
	const cred = "party-survives"

	ctxA, leaveA := joinLoginParty(cred)
	_, leaveB := joinLoginParty(cred)
	defer leaveB()

	leaveA()

	select {
	case <-ctxA.Done():
		t.Fatal("login context cancelled while another connect was still waiting for the cert")
	case <-time.After(50 * time.Millisecond):
	}
}

// ...and the other half: when everyone gives up, the browser flow must be
// abandoned immediately. Before this, cancelling every host left the login
// running to its five-minute ceiling, holding the lock and the OIDC callback
// port, so retrying was silently queued behind a flow nobody wanted.
func TestLoginPartyCancelsWhenLastMemberLeaves(t *testing.T) {
	const cred = "party-cancels"

	ctx, leaveA := joinLoginParty(cred)
	_, leaveB := joinLoginParty(cred)

	leaveA()
	leaveB()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("login context still live after every member left; the browser flow would run to its timeout")
	}
}

// A party formed after the previous one emptied must be independent - a new
// connect attempt cannot inherit the cancelled context of the attempt the
// user just abandoned, or it would fail instantly.
func TestLoginPartyIsFreshAfterEveryoneLeft(t *testing.T) {
	const cred = "party-fresh"

	ctx1, leave1 := joinLoginParty(cred)
	leave1()
	<-ctx1.Done() // previous attempt abandoned

	ctx2, leave2 := joinLoginParty(cred)
	defer leave2()

	if ctx2.Err() != nil {
		t.Fatal("a new login party inherited the cancelled context of the abandoned one")
	}
}

// leave() is deferred on paths that can also return early; calling it twice
// must not double-decrement and cancel a party other connects are using.
func TestLoginPartyLeaveIsIdempotent(t *testing.T) {
	const cred = "party-idempotent"

	_, leaveA := joinLoginParty(cred)
	ctxB, leaveB := joinLoginParty(cred)
	defer leaveB()

	leaveA()
	leaveA()

	select {
	case <-ctxB.Done():
		t.Fatal("a repeated leave() cancelled a party that still had a member")
	case <-time.After(50 * time.Millisecond):
	}
}

// Different credentials must not share a party: abandoning one credential's
// login cannot abort an unrelated one.
func TestLoginPartyIsPerCredential(t *testing.T) {
	ctxA, leaveA := joinLoginParty("cred-one")
	defer leaveA()

	_, leaveB := joinLoginParty("cred-two")
	leaveB()

	if ctxA.Err() != nil {
		t.Fatal("leaving one credential's party cancelled another credential's login")
	}
}

// Concurrent joins and leaves must not corrupt the refcount. Run with -race.
func TestLoginPartyConcurrentJoinLeave(t *testing.T) {
	const cred = "party-concurrent"

	// One member held for the duration, so the party should still be alive
	// at the end no matter how the others interleave.
	ctx, leaveHeld := joinLoginParty(cred)
	defer leaveHeld()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, leave := joinLoginParty(cred)
			leave()
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		t.Fatalf("party cancelled despite a member still holding it: %v", err)
	}
}

// tryLock has to distinguish an uncontended acquire from one that will wait,
// so the wait can be reported. Getting this backwards would either report a
// wait that is not happening or stay silent through a real one.
func TestTryLock(t *testing.T) {
	l := newLockChan()

	if !l.tryLock() {
		t.Fatal("tryLock failed on a free lock")
	}
	if l.tryLock() {
		t.Fatal("tryLock succeeded on a held lock")
	}
	l.Unlock()
	if !l.tryLock() {
		t.Fatal("tryLock failed after the lock was released")
	}
	l.Unlock()
}

// A context already cancelled before joining still gets a live party context:
// the party's lifetime is about collective interest, not any one member's
// context. The caller checks its own ctx separately.
func TestLoginPartyIgnoresMemberContexts(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = cancelledCtx

	ctx, leave := joinLoginParty("party-independent")
	defer leave()

	if ctx.Err() != nil {
		t.Fatal("party context was born cancelled")
	}
}

// The connect performing the login must be able to cancel it. Its own
// context is not the party context, so without a bridge, cancelling the very
// host that opened the browser did nothing - the most natural thing for a
// user to try, since that is the host they were looking at when the tab
// appeared.
func TestBridgeCancelEndsLoginWhenPerformerCancels(t *testing.T) {
	partyCtx, partyCancel := context.WithCancel(context.Background())
	defer partyCancel()
	ownCtx, ownCancel := context.WithCancel(context.Background())

	var left bool
	var leftMu sync.Mutex
	loginCtx, stop := bridgeCancel(partyCtx, ownCtx, func() {
		leftMu.Lock()
		left = true
		leftMu.Unlock()
	})
	defer stop()

	ownCancel()

	select {
	case <-loginCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the performer did not end the login")
	}

	leftMu.Lock()
	defer leftMu.Unlock()
	if !left {
		t.Fatal("the performer cancelled without leaving the party; the refcount would never reach zero")
	}
}

// Cancelling the party (everyone gave up) must also end the login.
func TestBridgeCancelEndsLoginWhenPartyCancels(t *testing.T) {
	partyCtx, partyCancel := context.WithCancel(context.Background())
	ownCtx, ownCancel := context.WithCancel(context.Background())
	defer ownCancel()

	loginCtx, stop := bridgeCancel(partyCtx, ownCtx, func() {})
	defer stop()

	partyCancel()

	select {
	case <-loginCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the party did not end the login")
	}
}

// A login that completes normally must not be reported as cancelled, and the
// watcher goroutine must not outlive it.
func TestBridgeCancelStopIsClean(t *testing.T) {
	partyCtx, partyCancel := context.WithCancel(context.Background())
	defer partyCancel()
	ownCtx, ownCancel := context.WithCancel(context.Background())
	defer ownCancel()

	var leaveCalls int
	var mu sync.Mutex
	loginCtx, stop := bridgeCancel(partyCtx, ownCtx, func() {
		mu.Lock()
		leaveCalls++
		mu.Unlock()
	})

	if loginCtx.Err() != nil {
		t.Fatal("login context was born cancelled")
	}
	stop()

	// Give the watcher a moment to exit down the done branch.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if leaveCalls != 0 {
		t.Fatalf("a cleanly finished login called leave() %d times via the bridge", leaveCalls)
	}
}
