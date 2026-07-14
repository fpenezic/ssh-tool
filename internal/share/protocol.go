package share

// The websocket wire contract between the host (this app) and a browser guest.
//
// One websocket per guest at wss://<bind>/s/<token>/ws. Everything is a JSON
// text frame EXCEPT PTY output, which is a binary frame (see OutputFrame) -
// base64-in-JSON would cost 33% plus an escape pass on every chunk, and the
// websocket, unlike a Wails event, has no JSON-only constraint.
//
// The guest->host surface is deliberately tiny: three verbs (input, ready,
// ping). Every additional verb is attack surface. There is no resize frame
// (the host owns the PTY size; the guest letterboxes), no tab-switch frame
// (tab switching is client-side; the guest already streams every shared
// session), and no level-upgrade frame (changing a share's level is stop +
// restart).

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

// Level is a share's access level, fixed at ShareStart and never re-read from
// the connection or a frame.
type Level string

const (
	LevelRead    Level = "read"    // guest sees output, cannot type
	LevelControl Level = "control" // guest types into the same PTY (tmux-style)
)

// Frame is the JSON envelope for every non-output message in either direction.
// Exactly one payload field is set, selected by T.
type Frame struct {
	T string `json:"t"`

	// host -> guest
	Pending  *Pending  `json:"pending,omitempty"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Snap     *Snap     `json:"snap,omitempty"`
	Size     *Size     `json:"size,omitempty"`
	State    *State    `json:"state,omitempty"`
	Bye      *Bye      `json:"bye,omitempty"`
	Pong     *struct{} `json:"pong,omitempty"`

	// guest -> host
	Input *Input    `json:"input,omitempty"`
	Ready *Ready    `json:"ready,omitempty"`
	Ping  *struct{} `json:"ping,omitempty"`
}

// Frame type discriminators.
const (
	TPending  = "pending"
	TManifest = "manifest"
	TSnap     = "snap"
	TSize     = "size"
	TState    = "state"
	TBye      = "bye"
	TPong     = "pong"

	TInput = "input"
	TReady = "ready"
	TPing  = "ping"
)

// Pending is sent immediately on WS accept, before the host approves. The guest
// shows "waiting for <Host>" and, prominently, the fingerprint words - the host
// shows the SAME words in the approval modal, and they compare out-of-band.
type Pending struct {
	Host    string `json:"host"`     // "alice@thinkpad"
	FpHex   string `json:"fp_hex"`   // full SHA-256, for power users
	FpShort string `json:"fp_short"` // grouped hex
	FpWords string `json:"fp_words"` // the four words to compare
}

// Manifest is sent once, on approval. It is the single source of truth for what
// the guest may see: the projected tab trees (with guest-scoped session slots)
// and the per-session metadata. Real pool UUIDs never appear here.
type Manifest struct {
	ShareID  string            `json:"share_id"`
	Level    Level             `json:"level"`
	HostName string            `json:"host_name"`
	Tabs     []ManifestTab     `json:"tabs"`
	Sessions []ManifestSession `json:"sessions"`
}

// ManifestTab mirrors SerializedPaneTab (frontend panetypes.ts) with sessionIds
// rewritten to guest slots. Root is shipped as opaque JSON: the projection is
// built frontend-side (it owns the pane-tree schema) and handed to the backend
// as a raw blob, exactly like the detach/redock layout string.
type ManifestTab struct {
	Title      string          `json:"title"`
	GroupName  string          `json:"group_name,omitempty"`
	GroupColor string          `json:"group_color,omitempty"`
	Root       json.RawMessage `json:"root"`
}

// ManifestSession is the per-slot metadata a guest needs for tab titles, the
// initial terminal size, and the connected/disconnected badge.
type ManifestSession struct {
	ID    string `json:"id"` // guest slot, e.g. "s1"
	Name  string `json:"name"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
	State string `json:"state"` // "connected" | "disconnected" | ...
}

// Snap carries a session's scrollback snapshot plus its watermark, sent once
// per session right after the manifest. When the share's scrollback policy is
// false B64 is empty but Cum still carries the current watermark: the guest
// needs it to know which live chunks predate its join and must be dropped.
type Snap struct {
	Sid string `json:"sid"`
	B64 string `json:"b64"`
	Cum uint64 `json:"cum"`
}

// Size tells the guest the host PTY's dimensions changed. The guest sets its
// xterm to exactly these and CSS-scales; it never resizes the host PTY.
type Size struct {
	Sid  string `json:"sid"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// State mirrors a session_state change (sshlayer.SessionState). The guest greys
// the pane and shows Reason.
type State struct {
	Sid    string `json:"sid"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Bye is the host ending the connection. Reason is one of the byeReason values;
// the WS then closes with a matching status code.
type Bye struct {
	Reason string `json:"reason"`
}

const (
	ByeRevoked      = "revoked"       // host kicked this guest
	ByeShareStopped = "share_stopped" // the whole share ended
	ByeAppClosing   = "app_closing"   // app shutting down
	ByeSlow         = "slow"          // guest couldn't keep up, dropped
	ByeDenied       = "denied"        // approval denied / timed out
)

// Input is the ONLY guest->host frame that can affect the host. Sid is a guest
// slot; B64 is the base64 keystrokes. Kept as JSON base64 (not binary) so the
// entire enforcement path in handleInput reads plainly.
type Input struct {
	Sid string `json:"sid"`
	B64 string `json:"b64"`
}

// Ready is sent by the guest after its snapshot write callback fires for a
// session. The hub drops input for a session until Ready arrives -> a browser
// xterm's answers to queries embedded in the replayed scrollback (DA1/DSR/OSC)
// can never be injected into the host PTY. Structural, not client-trusted.
type Ready struct {
	Sid string `json:"sid"`
}

// --- binary output frame -------------------------------------------------

// Output frames are binary, not JSON. Layout (all big-endian):
//
//	byte  0       : outputFrameKind (0x01)
//	bytes 1..2    : uint16 length of sid
//	bytes 3..3+L  : sid bytes
//	next  8 bytes : uint64 cum
//	remaining     : raw PTY bytes (NOT base64)
//
// Marshalled once per chunk in Hub.Publish and the same []byte handed to every
// attached conn.
const outputFrameKind byte = 0x01

// maxInputFrame caps a single guest input frame. A guest cannot paste more than
// this in one frame; anything larger is a protocol violation.
const maxInputFrame = 4 * 1024

// MarshalOutput builds a binary output frame for sid+cum+data.
func MarshalOutput(sid string, cum uint64, data []byte) []byte {
	out := make([]byte, 0, 1+2+len(sid)+8+len(data))
	out = append(out, outputFrameKind)
	out = binary.BigEndian.AppendUint16(out, uint16(len(sid)))
	out = append(out, sid...)
	out = binary.BigEndian.AppendUint64(out, cum)
	out = append(out, data...)
	return out
}

var errBadOutputFrame = errors.New("share: malformed output frame")

// ParseOutput decodes a binary output frame. Used by tests and by the guest
// client (ported to TS); kept here so the codec has one authority.
func ParseOutput(b []byte) (sid string, cum uint64, data []byte, err error) {
	if len(b) < 1+2+8 || b[0] != outputFrameKind {
		return "", 0, nil, errBadOutputFrame
	}
	sidLen := int(binary.BigEndian.Uint16(b[1:3]))
	if len(b) < 3+sidLen+8 {
		return "", 0, nil, errBadOutputFrame
	}
	sid = string(b[3 : 3+sidLen])
	off := 3 + sidLen
	cum = binary.BigEndian.Uint64(b[off : off+8])
	data = b[off+8:]
	return sid, cum, data, nil
}
