//go:build android || ios

// Mobile event delivery (Go -> JS) via frontend long-poll.
//
// On desktop, Go pushes events into the WebView with execJS
// (window._wails.dispatchWailsEvent). On Android that push path is
// unavailable to app code: the native Activity owns the WebView, there is
// no Go-side WebviewWindow for the EventIPCTransport to dispatch to, and
// the JNI executeJavaScript helper is unexported. The result was that
// every Go-emitted event (host_key_challenge, session_state, transfer
// progress, ...) silently vanished, hanging any flow that waits on an
// event reply.
//
// Fix: our EventsEmit shim is the single choke point for all app events.
// On mobile it also enqueues each event here; the frontend drains the
// queue with the MobilePollEvents IPC (long-poll) and re-dispatches into
// the normal Wails event system, so existing EventsOn subscribers are
// unchanged. Uses only the IPC channel that already works - no execJS, no
// window, no Wails-internal patching.

package main

import (
	"encoding/json"
	"sync"
	"time"
)

// MobileEvent is one queued Go->JS event. Data is pre-marshalled so the
// poll response is a plain JSON array regardless of the payload type.
type MobileEvent struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

type mobileEventQueue struct {
	mu      sync.Mutex
	pending []MobileEvent
	signal  chan struct{} // buffered(1); pinged on enqueue to wake a waiting poll
}

var mobileEvents = &mobileEventQueue{signal: make(chan struct{}, 1)}

// mobileEnqueueEvent is called from EventsEmit on mobile. Best-effort:
// a payload that won't marshal is dropped rather than blocking emit.
func mobileEnqueueEvent(name string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = json.RawMessage("null")
	}
	mobileEvents.mu.Lock()
	mobileEvents.pending = append(mobileEvents.pending, MobileEvent{Name: name, Data: raw})
	mobileEvents.mu.Unlock()
	// Wake a waiting poll without blocking if none is parked.
	select {
	case mobileEvents.signal <- struct{}{}:
	default:
	}
}

// drain returns and clears all pending events.
func (q *mobileEventQueue) drain() []MobileEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	out := q.pending
	q.pending = nil
	return out
}

// MobilePollEvents long-polls for queued Go->JS events. Returns
// immediately with any pending events; otherwise blocks until an event
// arrives or the timeout elapses (returning an empty slice). The frontend
// calls this in a loop. Exposed as an IPC method only on mobile builds.
func (a *App) MobilePollEvents() []MobileEvent {
	if evs := mobileEvents.drain(); len(evs) > 0 {
		return evs
	}
	select {
	case <-mobileEvents.signal:
		// Drain whatever landed (may be more than one).
		if evs := mobileEvents.drain(); len(evs) > 0 {
			return evs
		}
		return []MobileEvent{}
	case <-time.After(25 * time.Second):
		return []MobileEvent{}
	}
}
