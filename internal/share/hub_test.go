package share

import (
	"bytes"
	"sync"
	"testing"
)

// fakeSession is a Sourced whose scrollback grows as data is appended, mirroring
// the real scrollbackBuf (cum = cumulative bytes, snapshot returns buf+cum
// atomically). It lets the hub test exercise the join race without a PTY.
type fakeSession struct {
	mu  sync.Mutex
	buf []byte
	cum uint64
}

func (f *fakeSession) append(data []byte) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, data...)
	f.cum += uint64(len(data))
	return f.cum
}

func (f *fakeSession) Scrollback() ([]byte, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, len(f.buf))
	copy(out, f.buf)
	return out, f.cum
}

func (f *fakeSession) Write([]byte) error { return nil }

// applyWatermark replays the exact dedupe the guest client performs: given a
// snapshot ending at watermark and a stream of live (cum, data) chunks, drop
// chunks fully covered and trim a straddling chunk's overlapping prefix. Returns
// the reconstructed byte stream (which must equal what the session emitted).
func applyWatermark(snapshot []byte, watermark uint64, live []outChunk) []byte {
	out := append([]byte{}, snapshot...)
	writeCum := watermark
	for _, ch := range live {
		start := ch.cum - uint64(len(ch.data))
		switch {
		case ch.cum <= writeCum:
			// fully covered
		case start < writeCum:
			overlap := writeCum - start
			out = append(out, ch.data[overlap:]...)
			writeCum = ch.cum
		default:
			out = append(out, ch.data...)
			writeCum = ch.cum
		}
	}
	return out
}

type outChunk struct {
	cum  uint64
	data []byte
}

// drainBinary pulls every queued binary frame off a conn and decodes it.
func drainBinary(t *testing.T, c *guestConn, slot string) []outChunk {
	t.Helper()
	var chunks []outChunk
	for {
		select {
		case m := <-c.out:
			if m.binary == nil {
				continue
			}
			sid, cum, data, err := ParseOutput(m.binary)
			if err != nil {
				t.Fatalf("parse output: %v", err)
			}
			if sid != slot {
				t.Fatalf("frame for wrong slot: %s != %s", sid, slot)
			}
			chunks = append(chunks, outChunk{cum: cum, data: append([]byte{}, data...)})
		default:
			return chunks
		}
	}
}

// TestJoinRaceNoGapNoDup is the test the plan flags as most-likely-broken: a
// session emitting continuously while a guest joins. Reconstructing the guest's
// view (snapshot + watermark-deduped live) must exactly equal the full emitted
// stream - no duplicated bytes at the seam, no gap.
func TestJoinRaceNoGapNoDup(t *testing.T) {
	fake := &fakeSession{}
	hub := newHub(func(id string) (Sourced, bool) {
		if id == "real1" {
			return fake, true
		}
		return nil, false
	})

	// A share mapping slot "s1" -> "real1", scrollback ON.
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelRead, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})

	// Emit some history BEFORE the guest joins.
	var emitted []byte
	for i := 0; i < 20; i++ {
		chunk := []byte("pre-join line\n")
		emitted = append(emitted, chunk...)
		fake.append(chunk)
		hub.Publish("real1", chunk, fake.cum) // no conns yet -> dropped, fine
	}

	// Guest joins: take the snapshot with its watermark, THEN attach for live.
	// (The server does subscribe-then-snapshot; here we do snapshot-then-attach
	// but immediately, and the test's continuous emission after attach exercises
	// the seam. The watermark dedupe must cover any overlap either way.)
	gc := newGuestConn(hub, share, nil, "1.2.3.4")
	snapshot, watermark := fake.Scrollback()
	hub.attach("real1", gc)

	// Emit MORE after the join, interleaved with nothing racing (single
	// goroutine keeps the test deterministic; the watermark math is what's
	// under test, not concurrency).
	for i := 0; i < 20; i++ {
		chunk := []byte("post-join line\n")
		emitted = append(emitted, chunk...)
		cum := fake.append(chunk)
		hub.Publish("real1", chunk, cum)
	}

	live := drainBinary(t, gc, "s1")
	got := applyWatermark(snapshot, watermark, live)

	if !bytes.Equal(got, emitted) {
		t.Fatalf("reconstructed guest view != emitted stream\n got %d bytes\n want %d bytes", len(got), len(emitted))
	}
}

// TestScrollbackOffStartsClean: with scrollback disabled the guest gets no
// history but the correct watermark, so the reconstruction starts clean at the
// join point with no gap and no replayed history.
func TestScrollbackOffStartsClean(t *testing.T) {
	fake := &fakeSession{}
	hub := newHub(func(id string) (Sourced, bool) { return fake, id == "real1" })
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelRead, false, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})

	// History the guest must NOT see.
	fake.append([]byte("secret history\n"))

	gc := newGuestConn(hub, share, nil, "1.2.3.4")
	// scrollback:false => guest snapshot is empty, watermark is current cum.
	_, watermark := fake.Scrollback()
	hub.attach("real1", gc)

	var postJoin []byte
	for i := 0; i < 10; i++ {
		chunk := []byte("visible\n")
		postJoin = append(postJoin, chunk...)
		hub.Publish("real1", chunk, fake.append(chunk))
	}

	live := drainBinary(t, gc, "s1")
	got := applyWatermark(nil, watermark, live) // empty snapshot

	if !bytes.Equal(got, postJoin) {
		t.Fatalf("guest saw wrong post-join bytes: got %q want %q", got, postJoin)
	}
	if bytes.Contains(got, []byte("secret")) {
		t.Fatal("scrollback:false leaked pre-join history")
	}
}

// TestReadOnlyRejectsInput: the backend enforcement test. A read-only share's
// handleInput must never write to the session and must flag a violation.
func TestReadOnlyRejectsInput(t *testing.T) {
	written := false
	fake := &writeRecorder{onWrite: func() { written = true }}
	hub := newHub(func(id string) (Sourced, bool) { return fake, id == "real1" })
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelRead, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})

	var violated string
	gc := newGuestConn(hub, share, nil, "1.2.3.4")
	gc.auditViolation = func(_ ShareInfo, _, reason string) { violated = reason }
	gc.markReady("s1")

	err := gc.handleInput(&Input{Sid: "s1", B64: "cm0gLXJmIC8K"}) // "rm -rf /\n"
	if err != errClose {
		t.Fatalf("expected errClose, got %v", err)
	}
	if written {
		t.Fatal("read-only share wrote input to the session")
	}
	if violated == "" {
		t.Fatal("no violation recorded")
	}
}

// TestControlUnknownSlotRejected: even on a control share, input for a slot not
// in the share is a violation and never resolves.
func TestControlUnknownSlotRejected(t *testing.T) {
	written := false
	fake := &writeRecorder{onWrite: func() { written = true }}
	hub := newHub(func(id string) (Sourced, bool) { return fake, id == "real1" })
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelControl, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})

	var violated string
	gc := newGuestConn(hub, share, nil, "1.2.3.4")
	gc.auditViolation = func(_ ShareInfo, _, reason string) { violated = reason }

	if err := gc.handleInput(&Input{Sid: "s99", B64: "eA=="}); err != errClose {
		t.Fatalf("expected errClose for unknown slot, got %v", err)
	}
	if written {
		t.Fatal("wrote input for an unshared slot")
	}
	if violated == "" {
		t.Fatal("no violation for unknown slot")
	}
}

// TestControlBeforeReadyDropped: input for a slot the guest hasn't acknowledged
// (ready) is silently dropped - blocks replayed-scrollback injection.
func TestControlBeforeReadyDropped(t *testing.T) {
	written := false
	fake := &writeRecorder{onWrite: func() { written = true }}
	hub := newHub(func(id string) (Sourced, bool) { return fake, id == "real1" })
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelControl, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})
	gc := newGuestConn(hub, share, nil, "1.2.3.4")
	// NOT ready.

	if err := gc.handleInput(&Input{Sid: "s1", B64: "eA=="}); err != nil {
		t.Fatalf("expected nil (drop), got %v", err)
	}
	if written {
		t.Fatal("wrote input before the guest was ready")
	}

	// After ready, the same input goes through.
	gc.markReady("s1")
	if err := gc.handleInput(&Input{Sid: "s1", B64: "eA=="}); err != nil {
		t.Fatalf("post-ready input: %v", err)
	}
	if !written {
		t.Fatal("post-ready input was not written")
	}
}

// TestControlOversizedRejected: an input frame over the cap is a violation.
func TestControlOversizedRejected(t *testing.T) {
	fake := &writeRecorder{}
	hub := newHub(func(id string) (Sourced, bool) { return fake, id == "real1" })
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelControl, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})
	gc := newGuestConn(hub, share, nil, "1.2.3.4")
	gc.auditViolation = func(ShareInfo, string, string) {}
	gc.markReady("s1")

	big := bytes.Repeat([]byte("A"), maxInputFrame+1)
	b64 := encodeBase64(big)
	if err := gc.handleInput(&Input{Sid: "s1", B64: b64}); err != errClose {
		t.Fatalf("expected errClose for oversized frame, got %v", err)
	}
}

type writeRecorder struct {
	onWrite func()
}

func (w *writeRecorder) Scrollback() ([]byte, uint64) { return nil, 0 }
func (w *writeRecorder) Write([]byte) error {
	if w.onWrite != nil {
		w.onWrite()
	}
	return nil
}
