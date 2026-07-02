// Package updater handles in-place binary replacement.
//
// On Windows the running .exe can't rename itself: Win32 holds an
// exclusive lock on the image file for the lifetime of the process.
// The pattern below sidesteps it by writing a small batch file that
// (1) waits for the current ssh-tool.exe handle to be released, then
// (2) overwrites it with the staged copy and (3) relaunches the app.
// The batch is invoked detached just before the Go process exits.
//
// On Linux / macOS we can rename the running binary in place, so the
// staged file is moved over the live one and the next launch happens
// implicitly (no auto-restart on those platforms yet - the user
// reopens the app).
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CleanupOldBinary removes the `<exe>.old` left behind by the Windows
// update swap. The apply script renames the running exe to `.old` to
// free the file lock, then tries to delete it after the swap - but the
// exiting process may still hold a handle at that instant, so the delete
// can silently fail and the `.old` lingers. By the next launch the old
// process is long gone, so deleting it here is reliable. No-op when
// there's nothing to clean (or off Windows, where no `.old` is made).
func CleanupOldBinary() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	old := exePath + ".old"
	if _, err := os.Stat(old); err == nil {
		_ = os.Remove(old)
	}
}

// DownloadResult is returned to the IPC layer after the staged binary
// is written. The caller can show the byte size + hash and offer the
// user a "Restart and update" button which calls Apply.
type DownloadResult struct {
	StagedPath   string `json:"staged_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Verified     bool   `json:"verified"`               // sha256 matched the manifest value
	ApplyScript  string `json:"apply_script,omitempty"` // populated on Windows
	NeedsRestart bool   `json:"needs_restart"`          // true → caller should exit after Apply
}

// ProgressFunc receives download progress. total is -1 when the
// server doesn't send Content-Length. Called from the download
// goroutine on every chunk - throttle on the receiving side.
type ProgressFunc func(read, total int64)

// progressWriter counts bytes flowing through a MultiWriter leg and
// reports the running total.
type progressWriter struct {
	read     int64
	total    int64
	onChange ProgressFunc
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.read += int64(len(p))
	if w.onChange != nil {
		w.onChange(w.read, w.total)
	}
	return len(p), nil
}

// Download streams the asset at url into <exeDir>/ssh-tool.exe.new
// (Windows) or <exeDir>/ssh-tool.new (Unix), verifying its sha256
// against wantSHA256 from the release manifest BEFORE the staged
// file gets anywhere near the live binary - a mismatch deletes the
// download and aborts. Empty wantSHA256 skips verification (manifest
// from an older / third-party release server); the result carries
// Verified=false so callers can surface that. onProgress may be nil.
func Download(url, wantSHA256 string, onProgress ProgressFunc) (*DownloadResult, error) {
	if url == "" {
		return nil, errors.New("updater: empty url")
	}
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("updater: locate exe: %w", err)
	}
	dir := filepath.Dir(exePath)
	stagedName := "ssh-tool.new"
	if runtime.GOOS == "windows" {
		stagedName = "ssh-tool.exe.new"
	}
	stagedPath := filepath.Join(dir, stagedName)

	// Stream + hash in one pass.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ssh-tool-updater")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("updater: HTTP %d from %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp(dir, "ssh-tool-dl-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	hasher := sha256.New()
	prog := &progressWriter{total: resp.ContentLength, onChange: onProgress}
	n, err := io.Copy(io.MultiWriter(tmp, hasher, prog), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("updater: download: %w", err)
	}
	if n < 1024 {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("updater: download too small (%d bytes)", n)
	}
	gotSHA := hex.EncodeToString(hasher.Sum(nil))
	verified := false
	if wantSHA256 != "" {
		if !strings.EqualFold(gotSHA, wantSHA256) {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("updater: checksum mismatch - the downloaded file does not match the release manifest (got %s, want %s); refusing to install", gotSHA, wantSHA256)
		}
		verified = true
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	// Replace any older staged file.
	_ = os.Remove(stagedPath)
	if err := os.Rename(tmpPath, stagedPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("updater: stage: %w", err)
	}

	out := &DownloadResult{
		StagedPath: stagedPath,
		Size:       n,
		SHA256:     gotSHA,
		Verified:   verified,
	}

	if runtime.GOOS == "windows" {
		scriptPath := filepath.Join(dir, "ssh-tool-apply-update.cmd")
		script := buildWindowsApplyScript(exePath, stagedPath)
		if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
			return nil, fmt.Errorf("updater: write apply script: %w", err)
		}
		out.ApplyScript = scriptPath
		out.NeedsRestart = true
	} else {
		// On Unix the rename is safe even while the binary runs.
		// We do it here so a subsequent restart picks up the new
		// version with no extra steps.
		if err := os.Rename(stagedPath, exePath); err != nil {
			return nil, fmt.Errorf("updater: swap: %w", err)
		}
		out.NeedsRestart = true
	}
	return out, nil
}

// Apply triggers the swap on Windows by spawning the apply script
// detached from the parent process. On Unix it's a no-op (Download
// already did the rename). After this returns successfully the caller
// MUST exit the current process within a few hundred milliseconds -
// the helper script polls for the binary handle to release.
//
// We deliberately avoid `cmd /c start ...` here. `start` resolves
// its target relative to the cmd CWD (often the user's Desktop on
// Windows), which fails with "The batch file cannot be found" if
// the script lives elsewhere. Calling cmd.exe /c with the absolute
// script path bypasses that lookup entirely. The DETACHED_PROCESS
// + CREATE_NEW_PROCESS_GROUP flags from detachedAttr() are what
// keep the child alive after the parent exits; `start` was never
// strictly required.
func Apply(scriptPath string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if scriptPath == "" {
		return errors.New("updater: empty script path")
	}
	abs, err := filepath.Abs(scriptPath)
	if err != nil {
		return fmt.Errorf("updater: resolve script path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("updater: script not found at %s: %w", abs, err)
	}
	cmd := exec.Command("cmd.exe", "/c", abs)
	cmd.Dir = filepath.Dir(abs) // anchor cwd so any relative ops in the .cmd resolve.
	cmd.SysProcAttr = detachedAttr()
	return cmd.Start()
}

// buildWindowsApplyScript composes the .cmd that survives parent exit.
// The script:
//  1. polls for the running .exe to release its file lock by
//     attempting an in-place rename (which fails with errorlevel 1
//     while the lock is held; loop until it succeeds).
//  2. replaces the live exe with the staged copy.
//  3. relaunches the app and self-deletes.
//
// We intentionally use timeout/PING for the wait so we don't depend
// on PowerShell being available on stripped-down Windows builds.
func buildWindowsApplyScript(exePath, stagedPath string) string {
	// Quote paths defensively - Program Files etc.
	q := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	return strings.Join([]string{
		`@echo off`,
		`setlocal`,
		`set TARGET=` + q(exePath),
		`set STAGED=` + q(stagedPath),
		`set OLD=` + q(exePath+".old"),
		`rem Wait up to 60s for the running process to release the binary.`,
		`set /a TRIES=0`,
		`:wait`,
		`if exist %OLD% del /q %OLD% 2>nul`,
		`ren %TARGET% "` + filepath.Base(exePath) + `.old" 2>nul`,
		`if not errorlevel 1 goto swap`,
		`set /a TRIES+=1`,
		`if %TRIES% GEQ 60 goto fail`,
		`ping -n 2 127.0.0.1 >nul`,
		`goto wait`,
		`:swap`,
		`move /y %STAGED% %TARGET% >nul`,
		`if errorlevel 1 goto rollback`,
		`rem Try a few times to delete the old binary - the just-exited`,
		`rem process can still hold the file handle for a moment. A leftover`,
		`rem .old is also swept on the next app launch, so this is best-effort.`,
		`set /a DTRIES=0`,
		`:delold`,
		`del /q %OLD% 2>nul`,
		`if not exist %OLD% goto relaunch`,
		`set /a DTRIES+=1`,
		`if %DTRIES% GEQ 5 goto relaunch`,
		`ping -n 2 127.0.0.1 >nul`,
		`goto delold`,
		`:relaunch`,
		`rem Relaunch detached so this script can exit + self-delete.`,
		`start "ssh-tool" /d "` + filepath.Dir(exePath) + `" %TARGET%`,
		`del /q "%~f0" 2>nul`,
		`exit /b 0`,
		`:rollback`,
		`rem Swap failed - restore the old binary so the user isn't left without one.`,
		`ren %OLD% "` + filepath.Base(exePath) + `" 2>nul`,
		`exit /b 1`,
		`:fail`,
		`exit /b 2`,
	}, "\r\n") + "\r\n"
}
