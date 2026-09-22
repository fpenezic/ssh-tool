package main

import (
	"testing"

	"ssh-tool/internal/store"
)

// The open-tab snapshot is machine-local, and it is versioned. When the
// frontend moved from last_session_tabs_v1 to _v2 the routing table here
// was not updated, so the key stopped matching, fell through to the synced
// store, and every tab change started travelling between machines.
//
// Nothing failed: the key simply behaved like an ordinary setting. These
// tests pin the two lists that have to agree.
func TestLocalStateKeysCoverEveryTabSnapshotVersion(t *testing.T) {
	for _, k := range []string{"last_session_tabs_v1", "last_session_tabs_v2"} {
		if !localStateKeys[k] {
			t.Errorf("%s is not routed to local state - it would be written to the synced store", k)
		}
	}
}

// The mirror's exclusion list is the other half: localStateKeys keeps a key
// out of store.db, and the mirror list stops a pulled profile from writing
// one back in. A key in one but not the other is a half-fix.
func TestMirrorExclusionsMatchLocalStateKeys(t *testing.T) {
	for k := range localStateKeys {
		if !store.IsMachineLocalSetting(k) {
			t.Errorf("%s is machine-local but the mirror would import it from a pulled profile", k)
		}
	}
}
