package share

// guestConn is one attached browser websocket. It has two goroutines: a read
// pump (guest -> host frames) and a write pump (host -> guest frames drained
// from a buffered channel). The write pump is what keeps Publish non-blocking:
// the hot path only does a non-blocking send to the channel; the actual
// websocket write happens here, off the PTY pump.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	outBufferFrames = 256              // per-conn send buffer; overflow => slow-kill
	writeTimeout    = 10 * time.Second // per-frame write deadline
	pingInterval    = 30 * time.Second
	pongTimeout     = 10 * time.Second
)

// errClose is returned by frame handlers to signal the read pump to tear down.
var errClose = errors.New("share: close connection")

// outMsg is a queued host->guest message: either a pre-marshalled binary frame
// or a JSON frame. Exactly one is set.
type outMsg struct {
	binary []byte
	json   *Frame
}

type guestConn struct {
	hub   *Hub
	share *shareSession
	ws    *websocket.Conn

	remoteIP string
	joinedAt time.Time

	ctx    context.Context
	cancel context.CancelFunc

	out chan outMsg

	// ready tracks which slots the guest has acknowledged (snapshot written).
	// Input for a slot is dropped until it is ready - this is what makes
	// replayed-scrollback query-response injection structurally impossible.
	readyMu sync.Mutex
	ready   map[string]bool

	killOnce sync.Once

	// auditInput / auditViolation are supplied by the App so the share package
	// doesn't import the audit store. May be nil in tests.
	auditInput     func(share ShareInfo, remoteIP, realID string, data []byte)
	auditViolation func(share ShareInfo, remoteIP, reason string)
	onGuestTab     func(shareID, remoteIP string, index int)
}

func newGuestConn(hub *Hub, share *shareSession, ws *websocket.Conn, remoteIP string) *guestConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &guestConn{
		hub:      hub,
		share:    share,
		ws:       ws,
		remoteIP: remoteIP,
		joinedAt: time.Now(),
		ctx:      ctx,
		cancel:   cancel,
		out:      make(chan outMsg, outBufferFrames),
		ready:    make(map[string]bool),
	}
}

// sendBinary queues a pre-marshalled binary frame. Non-blocking: a full buffer
// means the guest can't keep up, so it is slow-killed rather than backpressuring
// the PTY pump.
func (c *guestConn) sendBinary(frame []byte) {
	select {
	case c.out <- outMsg{binary: frame}:
	default:
		c.kill(ByeSlow)
	}
}

// sendJSON queues a JSON frame. Same non-blocking discipline.
func (c *guestConn) sendJSON(f Frame) {
	select {
	case c.out <- outMsg{json: &f}:
	default:
		c.kill(ByeSlow)
	}
}

// writePump drains c.out to the websocket and pings on an interval, reaping the
// conn if a pong doesn't return (a walked-away guest with a dead TCP must not
// read as attached).
func (c *guestConn) writePump() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case m := <-c.out:
			if err := c.writeMsg(m); err != nil {
				c.kill(ByeSlow)
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(c.ctx, pongTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.kill(ByeSlow)
				return
			}
		}
	}
}

func (c *guestConn) writeMsg(m outMsg) error {
	wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	if m.binary != nil {
		return c.ws.Write(wctx, websocket.MessageBinary, m.binary)
	}
	b, err := json.Marshal(m.json)
	if err != nil {
		return err
	}
	return c.ws.Write(wctx, websocket.MessageText, b)
}

// readPump reads guest->host frames until the connection ends. Only text frames
// are expected from the guest; a binary frame is a protocol violation.
func (c *guestConn) readPump() {
	defer c.kill(ByeRevoked) // if the loop exits for any non-kill reason
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			c.violation("binary frame from guest")
			return
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			c.violation("malformed frame")
			return
		}
		if err := c.dispatch(&f); err != nil {
			return
		}
	}
}

func (c *guestConn) dispatch(f *Frame) error {
	switch f.T {
	case TInput:
		if f.Input == nil {
			c.violation("input frame with no body")
			return errClose
		}
		return c.handleInput(f.Input)
	case TReady:
		if f.Ready != nil {
			c.markReady(f.Ready.Sid)
		}
		return nil
	case TGuestTab:
		if f.GuestTab != nil && c.onGuestTab != nil {
			c.onGuestTab(c.share.id, c.remoteIP, f.GuestTab.Index)
		}
		return nil
	case TPing:
		c.sendJSON(Frame{T: TPong, Pong: &struct{}{}})
		return nil
	default:
		// Unknown verb. Not fatal on its own (forward-compat), but a guest has
		// no legitimate reason to send anything else, so treat it as a
		// violation to keep the surface tight.
		c.violation("unknown frame type: " + f.T)
		return errClose
	}
}

// handleInput is the ONLY path from a websocket byte to a PTY. The checks run
// in this exact order; a read-only guest's input never gets decoded, resolved,
// or written, because the level check is the first statement.
func (c *guestConn) handleInput(in *Input) error {
	// 1. LEVEL. Fixed on the share, never read from the conn or the frame.
	if c.share.level != LevelControl {
		c.violation("input on a read-only share")
		return errClose
	}
	// 2. SLOT. An unknown slot is hostile - a guest probing for other sessions.
	realID, ok := c.share.resolveSlot(in.Sid)
	if !ok {
		c.violation("input for unshared session " + in.Sid)
		return errClose
	}
	// 3. READY GATE. Input before the guest acknowledged the snapshot is
	//    dropped - this blocks replayed-scrollback query-response injection.
	if !c.isReady(in.Sid) {
		return nil
	}
	// 4. DECODE + SIZE CAP.
	data, err := base64.StdEncoding.DecodeString(in.B64)
	if err != nil || len(data) > maxInputFrame {
		c.violation("bad or oversized input frame")
		return errClose
	}
	// 5. SESSION LIVENESS. Resolved every time; a session that died between
	//    frames must not be writable.
	sess, ok := c.hub.resolve(realID)
	if !ok {
		return nil // gone; the state frame already told the guest
	}
	if c.auditInput != nil {
		c.auditInput(c.share.info(), c.remoteIP, realID, data)
	}
	return sess.Write(data)
}

func (c *guestConn) markReady(slot string) {
	c.readyMu.Lock()
	c.ready[slot] = true
	c.readyMu.Unlock()
}

func (c *guestConn) isReady(slot string) bool {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	return c.ready[slot]
}

// violation records a protocol violation and closes the connection. A guest
// that violates the protocol is either malicious or broken; both should be
// visible to the host.
func (c *guestConn) violation(reason string) {
	if c.auditViolation != nil {
		c.auditViolation(c.share.info(), c.remoteIP, reason)
	}
	c.kill(ByeRevoked)
}

// kill tears the connection down exactly once: send bye, detach from every
// session, cancel the context so both pumps exit, close the ws.
func (c *guestConn) kill(reason string) {
	c.killOnce.Do(func() {
		if c.ws != nil {
			// Best-effort bye; the guest may already be gone.
			bctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if b, err := json.Marshal(Frame{T: TBye, Bye: &Bye{Reason: reason}}); err == nil {
				_ = c.ws.Write(bctx, websocket.MessageText, b)
			}
			cancel()
		}
		c.hub.detachAll(c)
		c.cancel()
		if c.ws != nil {
			_ = c.ws.Close(byeStatus(reason), reason)
		}
	})
}

// byeStatus maps a bye reason to a websocket close code so the guest can render
// the right message without racing the bye frame.
func byeStatus(reason string) websocket.StatusCode {
	switch reason {
	case ByeDenied:
		return 4403
	case ByeShareStopped, ByeAppClosing:
		return websocket.StatusGoingAway
	case ByeSlow:
		return websocket.StatusPolicyViolation
	default:
		return websocket.StatusNormalClosure
	}
}
