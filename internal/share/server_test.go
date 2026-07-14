package share

import (
	"sync"
	"testing"
)

// TestSessionClosedEndsShareWhenLast verifies that when a share's last live
// session closes, the whole share is dropped. Regression: the app used to call
// SessionClosed BEFORE removing the session from the pool, so the resolver
// still reported it live and the share lingered with a connected guest.
func TestSessionClosedEndsLastShare(t *testing.T) {
	var mu sync.Mutex
	live := map[string]bool{"real1": true}
	resolve := func(id string) (Sourced, bool) {
		mu.Lock()
		defer mu.Unlock()
		if live[id] {
			return &writeRecorder{}, true
		}
		return nil, false
	}

	srv := NewServer(Config{Resolve: resolve})
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelControl, true, 0, nil,
		[]SharedSession{{RealID: "real1", Name: "n"}})
	srv.mu.Lock()
	srv.byID[share.id] = share
	srv.byToken[share.token] = share
	srv.mu.Unlock()

	// While the session is live, closing an UNRELATED session must not touch it.
	srv.SessionClosed("other")
	srv.mu.Lock()
	stillThere := srv.byID["sh1"] != nil
	srv.mu.Unlock()
	if !stillThere {
		t.Fatal("share dropped on an unrelated session close")
	}

	// Now the session dies: mark it gone (as pool.Remove would), THEN notify.
	mu.Lock()
	delete(live, "real1")
	mu.Unlock()
	srv.SessionClosed("real1")

	srv.mu.Lock()
	gone := srv.byID["sh1"] == nil && srv.byToken[share.token] == nil
	srv.mu.Unlock()
	if !gone {
		t.Fatal("share was not ended after its last session closed")
	}
}

// TestSessionClosedKeepsMultiSessionShare: a share with two sessions survives
// one of them closing.
func TestSessionClosedKeepsMultiSessionShare(t *testing.T) {
	var mu sync.Mutex
	live := map[string]bool{"real1": true, "real2": true}
	resolve := func(id string) (Sourced, bool) {
		mu.Lock()
		defer mu.Unlock()
		if live[id] {
			return &writeRecorder{}, true
		}
		return nil, false
	}
	srv := NewServer(Config{Resolve: resolve})
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelRead, true, 0, nil,
		[]SharedSession{{RealID: "real1", Name: "a"}, {RealID: "real2", Name: "b"}})
	srv.mu.Lock()
	srv.byID[share.id] = share
	srv.byToken[share.token] = share
	srv.mu.Unlock()

	mu.Lock()
	delete(live, "real1")
	mu.Unlock()
	srv.SessionClosed("real1")

	srv.mu.Lock()
	stillThere := srv.byID["sh1"] != nil
	srv.mu.Unlock()
	if !stillThere {
		t.Fatal("share ended while a second session was still live")
	}
}
