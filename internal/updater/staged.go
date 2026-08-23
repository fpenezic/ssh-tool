package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// stagedManifestName is the sidecar written next to the staged binary
// so a staged-but-not-applied update survives an app restart. Before
// it existed, the apply-script path lived only in App memory: if the
// user hit Download and then closed the app instead of Restart, the
// next launch knew nothing about ssh-tool.exe.new, offered the same
// update again, and re-downloaded the whole binary.
const stagedManifestName = "ssh-tool-update.json"

// StagedUpdate describes a download that has been verified and written
// to disk but not yet swapped over the live binary. Only Windows can
// be in this state - on Unix Download renames the staged file over the
// running binary immediately, so there is nothing left pending.
type StagedUpdate struct {
	Version     string `json:"version"`      // release version the staged binary is
	StagedPath  string `json:"staged_path"`  // absolute path to <exe>.new
	ApplyScript string `json:"apply_script"` // absolute path to the .cmd that swaps it in
	SHA256      string `json:"sha256"`       // digest of the staged file, re-verified before apply
	Size        int64  `json:"size"`
}

// stagedManifestPath is the sidecar location: alongside the executable,
// the same directory the staged binary and apply script already live in,
// so removing that directory removes the whole pending update at once.
func stagedManifestPath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exePath), stagedManifestName), nil
}

// writeStagedManifest records a pending update. Best-effort: a failure
// here only costs the user a re-download, so callers ignore the error
// rather than failing an otherwise-good download.
func writeStagedManifest(s StagedUpdate) error {
	path, err := stagedManifestPath()
	if err != nil {
		return err
	}
	return writeStagedManifestAt(path, s)
}

func writeStagedManifestAt(path string, s StagedUpdate) error {
	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o644)
}

// ClearStaged removes the manifest, the staged binary and the apply
// script. Called when the staged update turns out to be unusable
// (corrupt, or superseded by a newer release) and after a successful
// apply hand-off. Every step is best-effort - a leftover file is
// harmless, and the next Download overwrites it anyway.
func ClearStaged() {
	path, err := stagedManifestPath()
	if err != nil {
		return
	}
	clearStagedAt(path)
}

func clearStagedAt(path string) {
	if s, err := readStagedManifest(path); err == nil {
		if s.StagedPath != "" {
			_ = os.Remove(s.StagedPath)
		}
		if s.ApplyScript != "" {
			_ = os.Remove(s.ApplyScript)
		}
	}
	_ = os.Remove(path)
}

func readStagedManifest(path string) (StagedUpdate, error) {
	var s StagedUpdate
	blob, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(blob, &s); err != nil {
		return s, err
	}
	return s, nil
}

// PendingStaged returns the update staged by an earlier run, if one is
// still valid, plus whether there is anything at all. It re-hashes the
// staged file rather than trusting the manifest: the file may have been
// truncated by a crash mid-write, replaced, or partially synced, and
// swapping a corrupt binary over a working one is the one failure mode
// this whole pipeline must never have.
//
// Anything inconsistent (missing file, missing script, digest mismatch)
// is cleaned up and reported as "nothing staged", which degrades to the
// old behaviour: the update check offers the release again and the user
// downloads it once more.
func PendingStaged() (StagedUpdate, bool) {
	var zero StagedUpdate
	if runtime.GOOS != "windows" {
		return zero, false
	}
	path, err := stagedManifestPath()
	if err != nil {
		return zero, false
	}
	return pendingStagedAt(path)
}

// pendingStagedAt is PendingStaged with the manifest location injected, so
// the validation rules can be tested on every platform instead of only on
// the one that produces staged updates.
func pendingStagedAt(path string) (StagedUpdate, bool) {
	var zero StagedUpdate
	s, err := readStagedManifest(path)
	if err != nil {
		return zero, false
	}
	if s.StagedPath == "" || s.ApplyScript == "" || s.Version == "" || s.SHA256 == "" {
		clearStagedAt(path)
		return zero, false
	}
	if _, err := os.Stat(s.ApplyScript); err != nil {
		clearStagedAt(path)
		return zero, false
	}
	sum, err := fileSHA256(s.StagedPath)
	if err != nil || !strings.EqualFold(sum, s.SHA256) {
		clearStagedAt(path)
		return zero, false
	}
	return s, true
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
