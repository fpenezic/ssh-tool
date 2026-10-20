package bitwarden

import (
	"testing"
	"time"

	"ssh-tool/internal/store"
)

// gatedSyncer blocks each sync until released.
type gatedSyncer struct {
	payload []byte
	entered chan struct{}
	release chan struct{}
}

func (g *gatedSyncer) Sync(store.BitwardenServer, Credentials) ([]byte, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.payload, nil
}

// A sync in progress neither blocks Forget nor survives it: the keys it
// decrypts after a vault lock are not stored.
func TestManagerForgetDuringSync(t *testing.T) {
	fx := buildFixture(t)
	g := &gatedSyncer{payload: fx.sync, entered: make(chan struct{}, 1), release: make(chan struct{})}
	m, _, srv := newTestManager(t, fx, g)
	ref := store.BitwardenRef{ServerID: srv.ID, CipherID: fx.personalCID, Field: FieldPassword}

	errc := make(chan error, 1)
	go func() { _, _, err := m.Resolve(ref); errc <- err }()
	<-g.entered

	forgot := make(chan struct{})
	go func() { m.Forget(); close(forgot) }()
	select {
	case <-forgot:
	case <-time.After(2 * time.Second):
		t.Fatal("Forget waited for the sync")
	}
	close(g.release)
	if err := <-errc; err == nil {
		t.Fatal("a resolve that straddled a vault lock returned a secret")
	}
	m.mu.Lock()
	n := len(m.open)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d vaults stored after the lock", n)
	}
}

// A server with no saved master password says so instead of asking for
// an unlock that cannot help.
func TestManagerNoMasterRef(t *testing.T) {
	fx := buildFixture(t)
	m, db, srv := newTestManager(t, fx, &fakeSyncer{payload: fx.sync})
	srv.MasterRef = ""
	if _, err := db.UpdateBitwardenServer(srv); err != nil {
		t.Skipf("cannot clear the master ref here: %v", err)
	}
	_, _, err := m.Resolve(store.BitwardenRef{ServerID: srv.ID, CipherID: fx.personalCID, Field: FieldPassword})
	if err == nil || err == ErrVaultLocked {
		t.Fatalf("got %v", err)
	}
}
