package infisical

import (
	"testing"

	"ssh-tool/internal/store"
)

// A credential synced from another machine names that machine's server id,
// which is not registered here: a registered server answers it.
func TestResolveServerOfAnotherMachine(t *testing.T) {
	fake := &fakeInfisical{secretVal: "hunter2"}
	m, _ := newTestManager(t, fake)

	ref := store.InfisicalRef{ServerID: "id-from-another-machine", ProjectID: "proj1", Environment: "prod", SecretPath: "/", Key: "password"}
	val, fresh, err := m.Resolve(ref)
	if err != nil || val != "hunter2" || fresh != FreshFetched {
		t.Fatalf("resolve: %q %v %v", val, fresh, err)
	}

	// The server goes away: the value sealed under the reference is served.
	fake.offline.Store(true)
	m.Forget()
	if val, fresh, err = m.Resolve(ref); err != nil || val != "hunter2" || fresh != FreshStale {
		t.Fatalf("offline: %q %v %v", val, fresh, err)
	}
}
