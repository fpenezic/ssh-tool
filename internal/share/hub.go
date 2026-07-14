package share

// The Hub is the fan-out layer: it takes every PTY chunk the app already emits
// (tapped alongside the asciicast recorder in wailsSink.EmitOutput) and pushes
// it to every guest attached to that session. It is the ONE piece that runs on
// the hot path - Publish is called from the PTY pump goroutine while the
// session's scrollback lock is held - so it must never block.

import (
	"sync"
)

// Sourced is the minimal shape the hub needs from a session, satisfied verbatim
// by both *ssh.Session and *local.Session.
type Sourced interface {
	Scrollback() ([]byte, uint64)
	Write(data []byte) error
}

// SessionResolver hands the hub a session by its REAL id. The App wires this to
// look through both the SSH pool and the local pool, so the share package
// imports neither.
type SessionResolver func(realID string) (Sourced, bool)

// Hub fans PTY output to attached guest connections, keyed by real session id.
type Hub struct {
	resolve SessionResolver

	mu        sync.RWMutex
	bySession map[string][]*guestConn // realID -> conns (copy-on-write)
}

func newHub(resolve SessionResolver) *Hub {
	return &Hub{
		resolve:   resolve,
		bySession: make(map[string][]*guestConn),
	}
}

// attach registers a conn as an observer of realID. Rebuilds the slice under
// the write lock (attach/detach are rare; Publish reads are hot).
func (h *Hub) attach(realID string, c *guestConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.bySession[realID]
	next := make([]*guestConn, len(cur)+1)
	copy(next, cur)
	next[len(cur)] = c
	h.bySession[realID] = next
}

// detach removes a conn from realID.
func (h *Hub) detach(realID string, c *guestConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.bySession[realID]
	next := make([]*guestConn, 0, len(cur))
	for _, x := range cur {
		if x != c {
			next = append(next, x)
		}
	}
	if len(next) == 0 {
		delete(h.bySession, realID)
	} else {
		h.bySession[realID] = next
	}
}

// detachAll removes a conn from every session it observes (used when the conn
// dies for its own reasons - a slow buffer, a dead TCP).
func (h *Hub) detachAll(c *guestConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for realID, cur := range h.bySession {
		found := false
		for _, x := range cur {
			if x == c {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		next := make([]*guestConn, 0, len(cur))
		for _, x := range cur {
			if x != c {
				next = append(next, x)
			}
		}
		if len(next) == 0 {
			delete(h.bySession, realID)
		} else {
			h.bySession[realID] = next
		}
	}
}

// Publish fans one output chunk to every guest attached to realID. Called from
// the PTY pump WHILE the scrollback lock is held: it must never block. A guest
// whose buffered channel is full is killed (slow), never allowed to stall the
// pump or the other observers. The frame is marshalled once and the same []byte
// handed to every conn.
func (h *Hub) Publish(realID string, data []byte, cum uint64) {
	h.mu.RLock()
	conns := h.bySession[realID]
	h.mu.RUnlock()
	if len(conns) == 0 {
		return
	}
	// Marshal once, keyed by each conn's own slot for this share. Different
	// shares may name the same real session different slots, so we cannot
	// share one frame across shares - but within a share the slot is fixed, so
	// cache per slot.
	frameBySlot := make(map[string][]byte, 2)
	for _, c := range conns {
		slot := c.share.slotFor(realID)
		if slot == "" {
			continue
		}
		f, ok := frameBySlot[slot]
		if !ok {
			f = MarshalOutput(slot, cum, data)
			frameBySlot[slot] = f
		}
		c.sendBinary(f) // non-blocking; kills the conn on overflow
	}
}

// PublishSize tells every guest attached to realID that the host PTY resized.
func (h *Hub) PublishSize(realID string, cols, rows uint16) {
	h.mu.RLock()
	conns := h.bySession[realID]
	h.mu.RUnlock()
	for _, c := range conns {
		slot := c.share.slotFor(realID)
		if slot == "" {
			continue
		}
		c.sendJSON(Frame{T: TSize, Size: &Size{Sid: slot, Cols: cols, Rows: rows}})
	}
}

// PublishState tells every guest attached to realID of a session state change
// (disconnect, reconnect). reason is optional.
func (h *Hub) PublishState(realID, state, reason string) {
	h.mu.RLock()
	conns := h.bySession[realID]
	h.mu.RUnlock()
	for _, c := range conns {
		slot := c.share.slotFor(realID)
		if slot == "" {
			continue
		}
		c.sendJSON(Frame{T: TState, State: &State{Sid: slot, State: state, Reason: reason}})
	}
}
