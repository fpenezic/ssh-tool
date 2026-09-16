package local

import (
	"os"
	"testing"
)

// The WSL cases are the reason resolveStartDir exists: wsl.exe inherits
// the Windows cwd, so the directory has to travel as an argument.
func TestResolveStartDirWSLDefaultsToLinuxHome(t *testing.T) {
	dir, args := resolveStartDir("wsl", "")
	if dir != "" {
		t.Fatalf("wsl must not set cmd.Dir, got %q", dir)
	}
	if len(args) != 2 || args[0] != "--cd" || args[1] != "~" {
		t.Fatalf("want [--cd ~], got %v", args)
	}
}

func TestResolveStartDirWSLPassesRequestedThrough(t *testing.T) {
	// A Linux path cannot be os.Stat'd from the Windows side, so it is
	// forwarded unchecked rather than silently dropped.
	dir, args := resolveStartDir("wsl", "/srv/app")
	if dir != "" {
		t.Fatalf("wsl must not set cmd.Dir, got %q", dir)
	}
	if len(args) != 2 || args[1] != "/srv/app" {
		t.Fatalf("want [--cd /srv/app], got %v", args)
	}
}

func TestResolveStartDirDefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this machine")
	}
	dir, args := resolveStartDir("bash", "")
	if dir != home {
		t.Fatalf("want home %q, got %q", home, dir)
	}
	if args != nil {
		t.Fatalf("non-wsl shells take cmd.Dir, not args: %v", args)
	}
}

func TestResolveStartDirHonoursRequested(t *testing.T) {
	tmp := t.TempDir()
	dir, args := resolveStartDir("bash", tmp)
	if dir != tmp {
		t.Fatalf("want %q, got %q", tmp, dir)
	}
	if args != nil {
		t.Fatalf("unexpected args: %v", args)
	}
}

// A path that has gone away must not fail the spawn: the shell opens in
// the default location instead.
func TestResolveStartDirInvalidFallsBackToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this machine")
	}
	dir, _ := resolveStartDir("bash", "/definitely/not/a/real/directory")
	if dir != home {
		t.Fatalf("invalid dir should fall back to home %q, got %q", home, dir)
	}
}

// A file is not a directory; same fallback as a missing path.
func TestResolveStartDirFileFallsBack(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this machine")
	}
	f := t.TempDir() + "/afile"
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := resolveStartDir("bash", f)
	if dir != home {
		t.Fatalf("a file should fall back to home %q, got %q", home, dir)
	}
}
