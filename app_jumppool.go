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
	"errors"
	"log"
	"sync"
	"time"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"

	"golang.org/x/crypto/ssh"
)

// bastionLingerDefault is how long a shared prefix stays up after its last
// target session closes. Short by default: just enough to cover a quick
// disconnect/reconnect without holding the bastion open - a fresh connect
// + jump comes up fast anyway. Settings -> Connection overrides it.
const bastionLingerDefault = 10 * time.Second

// jumpEntry is one shared jump-prefix connection.
type jumpEntry struct {
	// ready is closed once the build is done: client set, or err (or
	// neither: the chain needs no jump after all).
	ready      chan struct{}
	err        error
	client     *ssh.Client // the bastion the target dials through
	cleanup    func()      // closes the whole prefix chain
	networkVia string      // first hop transport, propagated to riders
	refs       int         // live target sessions using this prefix
	stopTimer  *time.Timer // idle linger, armed when refs hits 0
	closeOnce  sync.Once
}

// close runs cleanup once, whoever gets there first (linger, the bastion
// dying, stopAll).
func (e *jumpEntry) close() {
	e.closeOnce.Do(func() {
		if e.cleanup != nil {
			e.cleanup()
		}
	})
}

// jumpPool owns the shared prefixes, keyed by ssh.JumpPrefixKey.
type jumpPool struct {
	mu      sync.Mutex
	entries map[string]*jumpEntry
	// build dials a jump prefix. A field so tests can inject a fake
	// without a real bastion; production wires BuildJumpChainVia.
	build func(ctx context.Context, settings *store.ResolvedSettings, deps sshlayer.JumpPrefixDeps) (*ssh.Client, func(), string, error)
	// linger returns the current idle linger; nil means the default.
	linger func() time.Duration
	// wait blocks until a bastion connection ends; nil means
	// client.Wait. A field so tests can run with fake clients.
	wait func(*ssh.Client) error
}

func (p *jumpPool) lingerFor() time.Duration {
	if p.linger == nil {
		return bastionLingerDefault
	}
	return p.linger()
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
// The first acquirer of a key builds it without holding the pool lock;
// later acquirers of that key wait for that build (no thundering herd on
// the bastion), while other keys, releases and peeks go on. A failed build
// fails its waiters with the same error rather than each retrying.
func (p *jumpPool) acquire(
	ctx context.Context,
	settings *store.ResolvedSettings,
	deps sshlayer.JumpPrefixDeps,
) (*ssh.Client, func(), string, error) {
	key := sshlayer.JumpPrefixKey(settings)
	if key == "" {
		return nil, nil, "", nil // no jump prefix; not pooled
	}

	var e *jumpEntry
	for {
		p.mu.Lock()
		e = p.entries[key]
		if e == nil {
			e = &jumpEntry{ready: make(chan struct{})}
			p.entries[key] = e
			p.mu.Unlock()
			client, cleanup, via, err := p.build(ctx, settings, deps)
			p.mu.Lock()
			if err != nil || client == nil {
				// client == nil: the chain resolved to no jump after
				// all; let Connect handle it.
				e.err = err
				delete(p.entries, key)
				close(e.ready)
				p.mu.Unlock()
				return nil, nil, "", err
			}
			if p.entries[key] != e {
				// stopAll ran during the build (shutdown).
				e.err = errors.New("jump pool: shutting down")
				close(e.ready)
				p.mu.Unlock()
				cleanup()
				return nil, nil, "", e.err
			}
			e.client, e.cleanup, e.networkVia = client, cleanup, via
			close(e.ready)
			go p.watch(key, e)
			log.Printf("jump pool: opened shared bastion for key %q", key)
			break
		}
		p.mu.Unlock()
		select {
		case <-e.ready:
		case <-ctx.Done():
			return nil, nil, "", ctx.Err()
		}
		p.mu.Lock()
		if e.client == nil {
			p.mu.Unlock()
			return nil, nil, "", e.err
		}
		if p.entries[key] != e {
			// It ended between the build and now: build anew.
			p.mu.Unlock()
			continue
		}
		if e.stopTimer != nil {
			e.stopTimer.Stop()
			e.stopTimer = nil
		}
		break
	}
	e.refs++
	p.mu.Unlock()

	var released bool
	release := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if released {
			return // defensive; release is called once
		}
		released = true
		e.refs--
		if e.refs > 0 || p.entries[key] != e {
			return // riders left, or the bastion already ended
		}
		// Last rider gone: arm the idle linger. If nobody re-acquires the
		// key within the linger, close the prefix and drop the entry.
		if e.stopTimer != nil {
			e.stopTimer.Stop()
		}
		linger := p.lingerFor()
		e.stopTimer = time.AfterFunc(linger, func() {
			p.mu.Lock()
			// Re-check under the lock: a reacquire may have bumped refs.
			if e.refs > 0 || p.entries[key] != e {
				p.mu.Unlock()
				return
			}
			delete(p.entries, key)
			p.mu.Unlock()
			log.Printf("jump pool: shared bastion for key %q idle %s, closing", key, linger)
			e.close()
		})
	}
	return e.client, release, e.networkVia, nil
}

// watch drops a shared bastion whose connection ended (restart, network
// loss), so the next acquire builds a new one instead of being handed the
// dead client for as long as reconnects keep re-arming the linger.
func (p *jumpPool) watch(key string, e *jumpEntry) {
	wait := p.wait
	if wait == nil {
		wait = (*ssh.Client).Wait
	}
	_ = wait(e.client)
	p.mu.Lock()
	if p.entries[key] == e {
		delete(p.entries, key)
		if e.stopTimer != nil {
			e.stopTimer.Stop()
			e.stopTimer = nil
		}
		log.Printf("jump pool: shared bastion for key %q ended", key)
	}
	p.mu.Unlock()
	e.close()
}

// peek returns the shared bastion client for a key IF one is already live,
// WITHOUT bumping refs or touching the idle-linger timer. It is for passive
// callers (the liveness probe) that want to ride an existing bastion but must
// never keep it alive or build a new one. Returns nil when no prefix is up.
func (p *jumpPool) peek(key string) *ssh.Client {
	if key == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[key]; e != nil {
		return e.client // nil while the first build runs
	}
	return nil
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
		e.close()
	}
}
