package share

// A shareSession is one active share: a fixed set of sessions (behind
// guest-scoped slots), an access level, a scrollback policy, a bind address,
// and a token. It owns the mapping between guest slots ("s1") and real pool
// session ids - that mapping lives ONLY here, never on the wire, so a guest can
// never address a session that wasn't shared with it, even given a backend bug.

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// SharedSession is one session offered inside a share, before slot assignment.
// The App builds these from the tabs the host picked.
type SharedSession struct {
	RealID string // the real pool session id
	Name   string
	Cols   uint16
	Rows   uint16
	State  string
}

// shareSession is the live server-side state of one share.
type shareSession struct {
	id         string
	token      string
	level      Level
	scrollback bool // include existing history on join
	bind       string
	hostName   string
	tabsBlob   []byte // the projected manifest tabs (opaque JSON from the frontend)
	created    time.Time

	// slot <-> real id, the security-critical mapping. Immutable after
	// ShareStart, so no lock is needed for reads.
	realBySlot map[string]string
	slotByReal map[string]string
	meta       map[string]SharedSession // slot -> metadata for the manifest

	// used flips true on the first successful guest attach; an unused share's
	// token expires (GC), a used one lives until the share stops.
	usedMu sync.Mutex
	used   bool

	// activeTab is the host's currently-focused tab index, updated live so
	// passive guests can follow. Guarded by its own mutex (read on attach,
	// written on host tab switch).
	activeTabMu sync.Mutex
	activeTab   int
}

func (s *shareSession) setActiveTab(i int) {
	s.activeTabMu.Lock()
	s.activeTab = i
	s.activeTabMu.Unlock()
}

func (s *shareSession) getActiveTab() int {
	s.activeTabMu.Lock()
	defer s.activeTabMu.Unlock()
	return s.activeTab
}

// newShareSession assigns sequential guest slots (s1, s2, ...) to the given
// sessions and mints a token.
func newShareSession(id, bind, hostName string, level Level, scrollback bool,
	activeTab int, tabsBlob []byte, sessions []SharedSession) *shareSession {
	s := &shareSession{
		id:         id,
		token:      randToken(),
		level:      level,
		scrollback: scrollback,
		activeTab:  activeTab,
		bind:       bind,
		hostName:   hostName,
		tabsBlob:   tabsBlob,
		created:    time.Now(),
		realBySlot: make(map[string]string, len(sessions)),
		slotByReal: make(map[string]string, len(sessions)),
		meta:       make(map[string]SharedSession, len(sessions)),
	}
	for i, sess := range sessions {
		slot := slotName(i)
		s.realBySlot[slot] = sess.RealID
		s.slotByReal[sess.RealID] = slot
		sess.RealID = "" // do not keep the real id in the guest-facing meta copy
		s.meta[slot] = sess
	}
	return s
}

// resolveSlot maps a guest slot to a real session id. ok=false for any slot not
// in this share - the caller treats that as a protocol violation.
func (s *shareSession) resolveSlot(slot string) (realID string, ok bool) {
	realID, ok = s.realBySlot[slot]
	return
}

// slotFor maps a real session id to this share's slot, or "" if the session
// isn't part of this share.
func (s *shareSession) slotFor(realID string) string {
	return s.slotByReal[realID]
}

func (s *shareSession) markUsed() {
	s.usedMu.Lock()
	s.used = true
	s.usedMu.Unlock()
}

func (s *shareSession) isUsed() bool {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	return s.used
}

func slotName(i int) string {
	return "s" + itoa(i+1)
}

// itoa avoids pulling strconv for a tiny non-negative int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func randToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
