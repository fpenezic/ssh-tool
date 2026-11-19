package catalog

import (
	"testing"

	"ssh-tool/internal/store"
)

// "No colour for me" on a catalog row: an empty colour over a catalog
// colour stays in the layer (it clears the catalog's), an empty one over
// no catalog colour is no change.
func TestLayerFromEditEmptyColour(t *testing.T) {
	red, empty := "red", ""
	l := LayerFromEdit(store.InheritableSettings{ColorTag: &red}, store.InheritableSettings{ColorTag: &empty})
	if l.ColorTag == nil || *l.ColorTag != "" {
		t.Fatalf("clearing the catalog colour: %v", l.ColorTag)
	}
	if l := LayerFromEdit(store.InheritableSettings{}, store.InheritableSettings{ColorTag: &empty}); l.ColorTag != nil {
		t.Fatalf("empty over nothing kept: %q", *l.ColorTag)
	}
	if l := LayerFromEdit(store.InheritableSettings{ColorTag: &red}, store.InheritableSettings{ColorTag: &red}); l.ColorTag != nil {
		t.Fatal("unchanged colour kept")
	}
}
