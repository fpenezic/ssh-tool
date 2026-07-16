package bitwarden

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"ssh-tool/internal/store"
)

// fakeSecrets implements SecretReader over a fixed map; unlocked togglable.
type fakeSecrets struct {
	vals     map[string]string
	unlocked bool
}

func (s *fakeSecrets) Get(acct string) (string, bool, error) {
	v, ok := s.vals[acct]
	return v, ok, nil
}
func (s *fakeSecrets) Unlocked() bool { return s.unlocked }

// plainSealer is an identity sealer (the on-disk crypto is exercised elsewhere;
// here we only care about cache round-trip).
type plainSealer struct{}

func (plainSealer) Seal(b []byte) ([]byte, error) { return append([]byte{}, b...), nil }
func (plainSealer) Open(b []byte) ([]byte, error) { return append([]byte{}, b...), nil }

// fakeSyncer returns a fixed payload and counts calls; can be flipped offline.
type fakeSyncer struct {
	payload []byte
	calls   int32
	offline bool
}

func (f *fakeSyncer) Sync(_ store.BitwardenServer, _ Credentials) ([]byte, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.offline {
		return nil, errors.New("network down")
	}
	return f.payload, nil
}

func newTestManager(t *testing.T, fx fixture, syncer Syncer) (*Manager, *store.DB, store.BitwardenServer) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv, err := db.CreateBitwardenServer(store.BitwardenServer{
		Name:      "vw",
		ServerURL: "https://vault.example.com",
		APIKeyRef: "apikey-acct",
		MasterRef: "master-acct",
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecrets{
		vals:     map[string]string{"master-acct": testMaster},
		unlocked: true,
	}
	lookup := func(ref string) (Credentials, error) {
		return Credentials{ClientID: "id", ClientSecret: "sec"}, nil
	}
	m := newManager(db, secrets, plainSealer{}, lookup, syncer, t.TempDir())
	return m, db, *srv
}

func TestManagerFetchThenCache(t *testing.T) {
	fx := buildFixture(t)
	syncer := &fakeSyncer{payload: fx.sync}
	m, _, srv := newTestManager(t, fx, syncer)

	ref := store.BitwardenRef{ServerID: srv.ID, CipherID: fx.personalCID, Field: FieldPassword}

	sec, fresh, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if sec != "personal-pass" {
		t.Fatalf("got %q", sec)
	}
	if fresh != FreshFetched {
		t.Fatalf("first resolve freshness: %v", fresh)
	}

	// Second resolve within staleAfter must not re-sync (served from open state).
	_, fresh2, err := m.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	if fresh2 != FreshCached {
		t.Fatalf("second resolve freshness: %v", fresh2)
	}
	if got := atomic.LoadInt32(&syncer.calls); got != 1 {
		t.Fatalf("expected 1 sync call, got %d", got)
	}
}

func TestManagerSyncForcesFetch(t *testing.T) {
	fx := buildFixture(t)
	syncer := &fakeSyncer{payload: fx.sync}
	m, _, srv := newTestManager(t, fx, syncer)

	ref := store.BitwardenRef{ServerID: srv.ID, CipherID: fx.orgCID, Field: FieldPassword}
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Sync(srv.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := atomic.LoadInt32(&syncer.calls); got != 2 {
		t.Fatalf("expected 2 sync calls after forced Sync, got %d", got)
	}
}

func TestManagerStaleFallbackOnOffline(t *testing.T) {
	fx := buildFixture(t)
	syncer := &fakeSyncer{payload: fx.sync}
	m, _, srv := newTestManager(t, fx, syncer)

	ref := store.BitwardenRef{ServerID: srv.ID, CipherID: fx.personalCID, Field: FieldPassword}
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatal(err)
	}
	// Drop the in-memory open state so the next resolve must re-load, then go
	// offline: the sealed cache must serve the secret marked stale.
	m.Forget()
	syncer.offline = true

	sec, fresh, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("offline resolve: %v", err)
	}
	if sec != "personal-pass" {
		t.Fatalf("offline got %q", sec)
	}
	if fresh != FreshStale {
		t.Fatalf("offline freshness: %v", fresh)
	}
}

func TestManagerForgetOnLock(t *testing.T) {
	fx := buildFixture(t)
	syncer := &fakeSyncer{payload: fx.sync}
	m, _, srv := newTestManager(t, fx, syncer)

	ref := store.BitwardenRef{ServerID: srv.ID, CipherID: fx.personalCID, Field: FieldPassword}
	if _, _, err := m.Resolve(ref); err != nil {
		t.Fatal(err)
	}
	m.Forget()
	m.mu.Lock()
	n := len(m.open)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("Forget left %d open states", n)
	}
}

func TestManagerLockedRejects(t *testing.T) {
	fx := buildFixture(t)
	syncer := &fakeSyncer{payload: fx.sync}
	m, _, srv := newTestManager(t, fx, syncer)
	m.secrets.(*fakeSecrets).unlocked = false

	_, _, err := m.Resolve(store.BitwardenRef{ServerID: srv.ID, CipherID: fx.personalCID, Field: FieldPassword})
	if !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("want ErrVaultLocked, got %v", err)
	}
}
