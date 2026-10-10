package main

import "testing"

func TestSemverGreaterKnowsReleaseCandidates(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.111.0", "v0.111.0-rc1", true},  // the release follows its RC
		{"v0.111.0-rc1", "v0.111.0", false}, // and an RC never undercuts it
		{"v0.111.0-rc2", "v0.111.0-rc1", true},
		{"v0.111.0-rc10", "v0.111.0-rc9", true},
		{"v0.111.0-rc1", "v0.110.0", true},
		{"v0.110.1", "v0.111.0-rc1", false},
		{"v0.111.0-rc2", "v0.111.0-rc1-3-gabc1234", true},
		{"v0.111.0", "v0.111.0-3-gabc1234", false}, // a dev build after a tag is not behind it
		{"v0.112.0", "v0.111.0-3-gabc1234", true},
		{"v0.111.0", "v0.111.0", false},
	} {
		if got := semverGreater(c.a, c.b); got != c.want {
			t.Errorf("semverGreater(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsRCTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.111.0-rc1": true, "v1.2.3-rc12": true,
		"v0.111.0": false, "v0.111.0-test": false, "v0.111.0-rc": false,
		"v0.111.0-rc1-3-gabc1234": false, "helper-v1": false, "0.111.0-rc1": false,
	} {
		if got := isRCTag(tag); got != want {
			t.Errorf("isRCTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
