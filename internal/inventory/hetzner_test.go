package inventory

import (
	"reflect"
	"testing"
)

// hetznerLabels must return a STABLE order regardless of Go map
// iteration randomisation - an unstable order made the dynamic-entry
// change detector fire every refresh, which auto-pushed every refresh
// interval with the user idle. Run enough times that a randomised
// map order would almost certainly diverge if unsorted.
func TestHetznerLabelsStableOrder(t *testing.T) {
	m := map[string]string{
		"env": "prod", "role": "web", "team": "infra", "zone": "eu", "tier": "1",
	}
	want := hetznerLabels(m)
	for i := 0; i < 200; i++ {
		if got := hetznerLabels(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("unstable label order on iter %d: %v != %v", i, got, want)
		}
	}
	// And it is actually sorted.
	for i := 1; i < len(want); i++ {
		if want[i-1] > want[i] {
			t.Fatalf("labels not sorted: %v", want)
		}
	}
}
