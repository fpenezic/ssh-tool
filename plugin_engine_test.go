package main

import (
	"os"
	"runtime/debug"
	"testing"
)

// The test binary itself is a Go binary: its build info lists this
// module's dependencies, which is enough to exercise the lookup without a
// real helper on disk.
func TestPluginEngineVersionReadsBuildInfo(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if v := pluginEngineVersion("netbird", exe); v != "" {
		t.Errorf("the app does not embed NetBird, got %q", v)
	}
	if v := pluginEngineVersion("unknown", exe); v != "" {
		t.Errorf("unknown plugin got %q", v)
	}
	pluginEngineModule["probe"] = "golang.org/x/crypto"
	defer delete(pluginEngineModule, "probe")
	if v := pluginEngineVersion("probe", exe); v == "" || v[0] == 'v' {
		t.Errorf("x/crypto version from build info = %q, want a bare version", v)
	}
}

func TestEngineDepVersion(t *testing.T) {
	cases := []struct {
		d    debug.Module
		want string
	}{
		{debug.Module{Version: "v0.30.2"}, "0.30.2"},
		{debug.Module{Version: "v1.0.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v1.0.1"}}, "1.0.1"},
		// A local directory replace: build info says (devel).
		{debug.Module{Version: "v0.1.0", Replace: &debug.Module{Path: "./third_party/x", Version: "(devel)"}}, "0.1.0 (patched)"},
		{debug.Module{Version: "v0.1.0", Replace: &debug.Module{Path: "./third_party/x"}}, "0.1.0 (patched)"},
		{debug.Module{Version: "(devel)", Replace: &debug.Module{Path: "./third_party/x", Version: "(devel)"}}, "patched"},
	}
	for _, c := range cases {
		if got := engineDepVersion(&c.d); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.d, got, c.want)
		}
	}
}
