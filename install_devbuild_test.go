package main

import (
	"os"
	"testing"
)

func TestIsDevBuild(t *testing.T) {
	orig := appVersion
	t.Cleanup(func() { appVersion = orig })

	dev := []string{
		"dev",                      // go run .
		"unknown",                  // no stamp at all
		"",                         // nothing injected
		"v0.94.0-4-g271ae09",       // untagged build between releases
		"v0.94.0-4-g271ae09-dirty", // uncommitted changes
		"v0.94.0-dirty",            // tagged, but the tree was dirty
		"v1.0.0-rc1",               // pre-release, not a release
	}
	for _, v := range dev {
		appVersion = v
		if !isDevBuild() {
			t.Errorf("%q should count as a development build", v)
		}
	}

	releases := []string{"v0.94.0", "v1.0.0", "v0.100.3"}
	for _, v := range releases {
		appVersion = v
		if isDevBuild() {
			t.Errorf("%q is a release and must not be treated as a dev build", v)
		}
	}
}

// Running ./bin/ssh-tool from the source tree is a daily habit. The
// offer must not copy a dev build over the working install - but with
// nothing installed there is nothing to lose, and refusing there left
// the author unable to re-create an entry they had just removed.
func TestDevBuildMayCreateButNotReplace(t *testing.T) {
	orig := appVersion
	t.Cleanup(func() { appVersion = orig })
	appVersion = "v0.94.0-14-g76da8ca-dirty"

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("LOCALAPPDATA", home+"/Local")
	t.Setenv("APPDATA", home+"/Roaming")

	st := installStateFor(home + "/src/ssh-tool/bin/ssh-tool")
	// With nothing installed there is nothing to lose, so the offer
	// stands: refusing it left a dev build unable to re-create an entry
	// it had just removed.
	if !st.CanOffer {
		t.Error("with nothing installed, even a dev build may create the entry")
	}
	// The state itself stays truthful. Reporting a fake kind here hid
	// the uninstall option in Settings whenever a dev build was running,
	// even though there was a real install to remove.
	if st.Kind != "loose" {
		t.Errorf("Kind = %q, want the real location (loose)", st.Kind)
	}
	if st.ExePath == "" {
		t.Error("ExePath should still be reported for a dev build")
	}
}

// A dev build running while a real install exists must still see that
// install - Settings needs it to offer the uninstall.
func TestDevBuildStillSeesAnExistingInstall(t *testing.T) {
	orig := appVersion
	t.Cleanup(func() { appVersion = orig })
	appVersion = "v0.94.0-14-g76da8ca-dirty"

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	binDir := home + "/.local/bin"
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binDir+"/ssh-tool", []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := installStateFor(home + "/src/bin/ssh-tool")
	if !st.Replaces {
		t.Error("the existing install should be reported even from a dev build")
	}
	if st.CanOffer {
		t.Error("but installing it is still refused")
	}
}

// The UI check is not the only guard: a stale frontend, or a call from
// somewhere else, must not be able to overwrite a real install with a
// build from the source tree.
// A dev build must not overwrite a real install, even if the UI somehow
// asks it to.
func TestInstallFromRefusesDevBuildReplace(t *testing.T) {
	orig := appVersion
	t.Cleanup(func() { appVersion = orig })
	appVersion = "dev"

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("LOCALAPPDATA", home+"/Local")
	t.Setenv("APPDATA", home+"/Roaming")

	binDir := home + "/.local/bin"
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binDir+"/ssh-tool", []byte("the copy in use"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := installFrom(home + "/src/bin/ssh-tool"); err == nil {
		t.Fatal("a dev build must not replace an existing install")
	}
	// And the existing copy is untouched.
	data, err := os.ReadFile(binDir + "/ssh-tool")
	if err != nil || string(data) != "the copy in use" {
		t.Error("the installed copy should be exactly as it was")
	}
}
