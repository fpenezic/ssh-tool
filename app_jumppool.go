package main

// Shared bastion (jump) connection multiplexing.
//
// A bulk Connect-all onto N connections behind the SAME jump host used to
// open N separate SSH connections to that bastion - N simultaneous
// handshakes that trip the bastion's MaxStartups (handshakes fail with
// EOF) and stall the batch. This pool dials a shared jump prefix ONCE and
// hands the same *ssh.Client to every target behind it, so the bastion
// sees one connection and each target rides a direct-tcpip channel
// through it (the ControlMaster model).
//
// Wiring: ssh.JumpPrefixHook (set in initialise) calls acquire; Connect
// then dials the target through the returned client and skips building
// the prefix itself. The session calls the returned release on teardown.

import (
	"context"
	"log"
	"sync"
	"time"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"

	"golang.org/x/crypto/ssh"
)

// bastionLinger is how long a shared prefix stays up after its last
// target session closes. Short on purpose: just enough to cover a quick
// disconnect/reconnect without holding the bastion open - a fresh connect
// + jump comes up fast anyway.
const bastionLinger = 10 * time.Second

// jumpEntry is one shared jump-prefix connection.
type jumpEntry struct {
	client     *ssh.Client // the bastion the target dials through
	cleanup    func()      // closes the whole prefix chain
	networkVia string      // first hop transport, propagated to riders
	refs       int         // live target sessions using this prefix
	stopTimer  *time.Timer // idle linger, armed when refs hits 0
}

// jumpPool owns the shared prefixes, keyed by ssh.JumpPrefixKey.
type jumpPool struct {
	mu      sync.Mutex
	entries map[string]*jumpEntry
	// build dials a jump prefix. A field so tests can inject a fake
	// without a real bastion; production wires BuildJumpChainVia.
	build func(ctx context.Context, settings *store.ResolvedSettings, deps sshlayer.JumpPrefixDeps) (*ssh.Client, func(), string, error)
}

func newJumpPool() *jumpPool {
	return &jumpPool{
		entries: map[string]*jumpEntry{},
		build: func(ctx context.Context, settings *store.ResolvedSettings, deps sshlayer.JumpPrefixDeps) (*ssh.Client, func(), string, error) {
			return sshlayer.BuildJumpChainVia(
				ctx, deps.DB, deps.Vault, settings, deps.HostKeyCB, deps.AlgoLookup, deps.ConnectTimeout)
		},
	}
}

// acquire returns a shared bastion client for the connection's jump
// prefix, building it once and reusing it for later callers. release must
// be called exactly once when the caller's session tears down. Returns
// (nil, nil, "", nil) when the connection has no jump prefix (nothing to
// share) so Connect falls back to building its own chain.
//
// The pool lock is held across the build: the bastion handshake is short
// and the batch is staggered, so serialising the first acquirer of a key
// (and making everyone after it wait for that one bastion to come up) is
// exactly the behaviour we want - no thundering herd on the bastion.
func (p *jumpPool) acquire(
	ctx context.Context,
	settings *store.ResolvedSettings,
	deps sshlayer.JumpPrefixDeps,
) (*ssh.Client, func(), string, error) {
	key := sshlayer.JumpPrefixKey(settings)
	if key == "" {
		return nil, nil, "", nil // no jump prefix; not pooled
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	e := p.entries[key]
	if e == nil {
		client, cleanup, via, err := p.build(ctx, settings, deps)
		if err != nil {
			return nil, nil, "", err
		}
		if client == nil {
			// Chain resolved to no jump after all; let Connect handle it.
			return nil, nil, "", nil
		}
		e = &jumpEntry{client: client, cleanup: cleanup, networkVia: via}
		p.entries[key] = e
		log.Printf("jump pool: opened shared bastion for key %q", key)
	} else if e.stopTimer != nil {
		e.stopTimer.Stop()
		e.stopTimer = nil
	}
	e.refs++

	var released bool
	release := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if released {
			return // defensive; release is called once
		}
		released = true
		e.refs--
		if e.refs > 0 {
			return
		}
		// Last rider gone: arm the idle linger. If nobody re-acquires the
		// key within bastionLinger, close the prefix and drop the entry.
		if e.stopTimer != nil {
			e.stopTimer.Stop()
		}
		e.stopTimer = time.AfterFunc(bastionLinger, func() {
			p.mu.Lock()
			// Re-check under the lock: a reacquire may have bumped refs.
			if e.refs > 0 || p.entries[key] != e {
				p.mu.Unlock()
				return
			}
			delete(p.entries, key)
			p.mu.Unlock()
			log.Printf("jump pool: shared bastion for key %q idle %s, closing", key, bastionLinger)
			e.cleanup()
		})
	}
	return e.client, release, e.networkVia, nil
}

// stopAll closes every shared prefix. Called on app shutdown AFTER
// sessions are torn down (a prefix may itself ride a WG tunnel).
func (p *jumpPool) stopAll() {
	p.mu.Lock()
	entries := p.entries
	p.entries = map[string]*jumpEntry{}
	p.mu.Unlock()
	for _, e := range entries {
		if e.stopTimer != nil {
			e.stopTimer.Stop()
		}
		e.cleanup()
	}
}
