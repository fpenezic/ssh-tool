package bitwarden

import (
	"errors"
	"testing"

	"ssh-tool/internal/store"
)

// A credential synced from another machine names that machine's server id,
// which is not registered here: the registered server holding the item
// answers it.
func TestResolveServerOfAnotherMachine(t *testing.T) {
	fx := buildFixture(t)
	m, _, _ := newTestManager(t, fx, &fakeSyncer{payload: fx.sync})

	sec, _, err := m.Resolve(store.BitwardenRef{ServerID: "id-from-another-machine", CipherID: fx.personalCID, Field: FieldPassword})
	if err != nil || sec != "personal-pass" {
		t.Fatalf("resolve: %q %v", sec, err)
	}
	_, _, err = m.Resolve(store.BitwardenRef{ServerID: "id-from-another-machine", CipherID: "no-such-item", Field: FieldPassword})
	if !errors.Is(err, ErrCipherNotFound) {
		t.Fatalf("missing item: %v", err)
	}
}
