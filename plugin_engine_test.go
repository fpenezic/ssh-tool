package main

import (
	"os"
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
