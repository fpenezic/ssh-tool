package ssh

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The reason the lock is a channel and not a sync.Mutex: a goroutine blocked
// in Mutex.Lock() cannot be released by cancelling its context. With the
// per-credential login lock that meant a user who closed the OIDC browser
// tab left every queued connect stuck on "Connecting..." behind a Cancel
// button that could not do anything, until the winner's five-minute timeout
// expired.
func TestLockCtxReleasesOnCancel(t *testing.T) {
	l := newLockChan()
	l.Lock() // stand in for an in-flight OIDC login

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- lockCtx(ctx, l) }()

	// The waiter must still be waiting: nothing has released the lock.
	select {
	case err := <-errCh:
		t.Fatalf("lockCtx returned %v while the lock was held", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want an error wrapping context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lockCtx ignored its cancelled context; a queued connect would hang until the login times out")
	}

	// The cancelled waiter must not have taken the lock on its way out.
	l.Unlock()
	if err := lockCtx(context.Background(), l); err != nil {
		t.Fatalf("lock was not free after the holder released it: %v", err)
	}
}

// An already-cancelled connect must not jump a free lock and go on to open a
// browser tab of its own.
func TestLockCtxRefusesAnAlreadyCancelledContext(t *testing.T) {
	l := newLockChan()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := lockCtx(ctx, l); err == nil {
		t.Fatal("lockCtx acquired the lock for an already-cancelled context")
	}

	// And it must have left the lock free for a live caller.
	if err := lockCtx(context.Background(), l); err != nil {
		t.Fatalf("a refused acquire consumed the lock: %v", err)
	}
}

// The normal path: an uncontended lock is acquired immediately.
func TestLockCtxAcquiresWhenFree(t *testing.T) {
	l := newLockChan()
	if err := lockCtx(context.Background(), l); err != nil {
		t.Fatalf("lockCtx failed on a free lock: %v", err)
	}
	l.Unlock()
}

// A waiter that is not cancelled proceeds as soon as the holder releases,
// which is the case that makes 25 connects share one login.
func TestLockCtxProceedsAfterRelease(t *testing.T) {
	l := newLockChan()
	l.Lock()

	errCh := make(chan error, 1)
	go func() { errCh <- lockCtx(context.Background(), l) }()

	time.Sleep(20 * time.Millisecond)
	l.Unlock()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("waiter failed after the lock was released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not wake after the lock was released")
	}
}

// Every path out of the locked section must release the lock. An early
// version returned "sign-in was cancelled" from inside the critical section
// before the deferred Unlock was registered, leaking the lock: every later
// connect then queued forever on a lock nobody would release, logging
// "waiting for an in-flight login" with no login running. Nothing in the
// unit tests caught it because they exercised the lock, not the function
// that uses it.
//
// This models the acquire-check-return shape and asserts the lock is free
// afterwards, which is the property that actually matters.
func TestLockIsReleasedOnEveryReturnPath(t *testing.T) {
	l := newLockChan()

	// Stand-in for EnsureFreshCert's structure: acquire, then take an early
	// return through a condition, with the release deferred first.
	attempt := func(bailOut bool) error {
		if err := lockCtx(context.Background(), l); err != nil {
			return err
		}
		defer l.Unlock()
		if bailOut {
			return errors.New("sign-in was cancelled")
		}
		return nil
	}

	if err := attempt(true); err == nil {
		t.Fatal("expected the bail-out path to return an error")
	}
	if !l.tryLock() {
		t.Fatal("the bail-out path leaked the lock; every later connect would queue forever")
	}
	l.Unlock()

	if err := attempt(false); err != nil {
		t.Fatalf("normal path: %v", err)
	}
	if !l.tryLock() {
		t.Fatal("the normal path leaked the lock")
	}
	l.Unlock()
}
