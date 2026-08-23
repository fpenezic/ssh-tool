package ssh

import (
	"sync"
	"testing"
)

// The lock is what stops N concurrent connects sharing one opkssh
// credential from starting N browser logins. Only the first can bind the
// OIDC callback port, so the rest fail with "address already in use" after
// each has opened a browser tab - 25 dynamic hosts meant 25 tabs and 24
// failures.
func TestCertLoginLockIsPerCredential(t *testing.T) {
	a1 := certLoginLock("cred-a")
	a2 := certLoginLock("cred-a")
	b := certLoginLock("cred-b")

	if a1 != a2 {
		t.Fatal("same credential handed out two different locks; concurrent connects would both log in")
	}
	if a1 == b {
		t.Fatal("different credentials share a lock; an unrelated credential would serialise behind this one")
	}
}

// Simulates the failing scenario: many connects for one credential racing
// through the read-decide-login-write sequence. With the lock covering the
// whole sequence, exactly one of them should observe an empty store and do
// the "login"; the rest must find what it wrote.
//
// This is why the lock cannot be released between the login and the vault
// write: a waiter admitted in that window re-reads an empty vault and logs
// in again, which is the original bug with extra steps.
func TestCertLoginLockSerialisesReadAndWrite(t *testing.T) {
	const connects = 25

	var (
		storeMu  sync.Mutex
		haveCert bool
		logins   int
	)

	var wg sync.WaitGroup
	for i := 0; i < connects; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu := certLoginLock("shared-cred")
			mu.Lock()
			defer mu.Unlock()

			// Read: is there already a cert? (vault.Get in the real code)
			storeMu.Lock()
			need := !haveCert
			storeMu.Unlock()

			if need {
				// Login, then write the result back (vault.Put).
				storeMu.Lock()
				logins++
				haveCert = true
				storeMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if logins != 1 {
		t.Fatalf("got %d logins for one credential across %d connects, want exactly 1", logins, connects)
	}
}

// Two credentials must not block each other - a slow browser login for one
// should not hold up connects using a different credential.
func TestCertLoginLockAllowsOtherCredentialsThrough(t *testing.T) {
	held := certLoginLock("busy-cred")
	held.Lock()
	defer held.Unlock()

	done := make(chan struct{})
	go func() {
		other := certLoginLock("idle-cred")
		other.Lock()
		other.Unlock()
		close(done)
	}()

	<-done // hangs (and fails the test by timeout) if the locks are shared
}
