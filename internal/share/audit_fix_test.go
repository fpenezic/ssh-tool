package share

import (
	"bytes"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"
)

// A guest approved after its share stopped is not attached.
func TestAttachAfterStopRefused(t *testing.T) {
	srv := NewServer(Config{Resolve: func(string) (Sourced, bool) { return nil, false }})
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelControl, true, 0, nil,
		[]SharedSession{{Slot: "s1", RealID: "real1", Name: "n"}})
	srv.mu.Lock()
	srv.byID[share.id] = share
	srv.mu.Unlock()
	gc := newGuestConn(srv.hub, share, nil, "192.0.2.1")
	if !srv.attachConn(share, gc) {
		t.Fatal("refused on a running share")
	}
	srv.mu.Lock()
	delete(srv.byID, share.id)
	delete(srv.conns, share.id)
	srv.mu.Unlock()
	late := newGuestConn(srv.hub, share, nil, "192.0.2.2")
	if srv.attachConn(share, late) {
		t.Fatal("attached to a stopped share")
	}
	srv.mu.Lock()
	n := len(srv.conns[share.id])
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d conns left on a stopped share", n)
	}
}

// A guest whose buffer is full is dropped without blocking the sender.
func TestOverflowDoesNotBlock(t *testing.T) {
	srv := NewServer(Config{Resolve: func(string) (Sourced, bool) { return nil, false }})
	share := newShareSession("sh1", "127.0.0.1:0", "host", LevelRead, true, 0, nil, nil)
	gc := newGuestConn(srv.hub, share, nil, "192.0.2.1")
	for i := 0; i < outBufferFrames; i++ {
		gc.sendBinary([]byte{1})
	}
	start := time.Now()
	for i := 0; i < 10; i++ {
		gc.sendBinary([]byte{1})
		gc.sendJSON(Frame{T: TBye})
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("overflow blocked for %s", d)
	}
	select {
	case <-gc.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("overflowing guest not dropped")
	}
}

// Regenerate reaches the running listener: the next handshake serves the
// new certificate.
func TestRegenerateCertServedLive(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(Config{Resolve: func(string) (Sourced, bool) { return nil, false },
		CertPath: filepath.Join(dir, "c.pem"), KeyPath: filepath.Join(dir, "k.pem")})
	cert, err := LoadOrCreate(srv.certPath, srv.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	err = srv.ensureListening("127.0.0.1", 0, cert)
	bind := srv.bind
	srv.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.srv.Close()
	served := func() []byte {
		c, err := tls.Dial("tcp", bind, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.ConnectionState().PeerCertificates[0].Raw
	}
	before := served()
	if _, err := srv.RegenerateCert(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, served()) {
		t.Fatal("the listener still serves the old certificate")
	}
}
