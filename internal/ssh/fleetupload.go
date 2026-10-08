package ssh

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// FleetUploadOptions is one file or directory sent to many hosts.
type FleetUploadOptions struct {
	LocalPath string `json:"local_path"`
	// RemoteDir is where the file or directory lands, as <dir>/<name>.
	// Empty or "~" is the login user's home; "~/x" and relative paths are
	// under it. Created when missing.
	RemoteDir string `json:"remote_dir"`
	// Existing decides what happens to a file already on the host:
	//   "skip"      - leave it (default)
	//   "overwrite" - always replace
	//   "changed"   - replace when the size differs or the local copy is
	//                 newer; uploaded files take the local mtime, so a
	//                 second run skips what the first one sent
	Existing string `json:"existing"`
	// Mode is an octal permission set on every uploaded file ("0755");
	// empty leaves the server's default.
	Mode string `json:"mode"`
}

// FleetTransferHost is one host's progress and, once State is final, result.
type FleetTransferHost struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	// State: "queued" | "connecting" | "transferring" | "done" | "error" | "cancelled".
	State        string `json:"state"`
	Target       string `json:"target,omitempty"`
	FilesDone    int    `json:"files_done"`
	FilesTotal   int    `json:"files_total"`
	FilesSkipped int    `json:"files_skipped"`
	// Unreadable counts remote entries a download could not list or read
	// as the login user (permission denied); they are left out.
	Unreadable int    `json:"unreadable"`
	Bytes      int64  `json:"bytes"`
	Total      int64  `json:"total"`
	Current    string `json:"current,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// ParseFleetMode checks an octal mode string; empty is "no chmod".
func ParseFleetMode(s string) (os.FileMode, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o7777 {
		return 0, false, fmt.Errorf("mode %q is not an octal permission like 0644", s)
	}
	return os.FileMode(n), true, nil
}

// FleetUpload sends opts.LocalPath to every host, batchParallelism at a
// time, over the same quiet chain as batch exec (shared bastions, no tab).
// onUpdate gets every state change and progress tick for a host; it is
// called from several goroutines. Results come back in input order.
func FleetUpload(
	db *store.DB,
	vault *creds.Vault,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	hosts []BatchHostInput,
	opts FleetUploadOptions,
	onUpdate func(FleetTransferHost),
	cancel <-chan struct{},
) []FleetTransferHost {
	if connectTimeout <= 0 {
		connectTimeout = 20 * time.Second
	}
	return fleetRun(hosts, onUpdate, cancel, func(h BatchHostInput, r FleetTransferHost) FleetTransferHost {
		return uploadOneHost(db, vault, hostKeyCB, algoLookup, connectTimeout, h, r, opts, onUpdate, cancel)
	})
}

// fleetRun runs one per-host job batchParallelism at a time; hosts still
// waiting when cancel closes are marked cancelled. Results in input order.
func fleetRun(hosts []BatchHostInput, onUpdate func(FleetTransferHost), cancel <-chan struct{},
	job func(BatchHostInput, FleetTransferHost) FleetTransferHost) []FleetTransferHost {
	out := make([]FleetTransferHost, len(hosts))
	for i, h := range hosts {
		out[i] = FleetTransferHost{ConnectionID: h.ConnectionID, Name: h.Name, Hostname: h.Hostname, State: "queued"}
	}
	sem := make(chan struct{}, batchParallelism)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-cancel:
				r := out[i]
				r.State = "cancelled"
				out[i] = r
				onUpdate(r)
				return
			}
			defer func() { <-sem }()
			out[i] = job(h, out[i])
		}()
	}
	wg.Wait()
	return out
}

// openFleetSFTP connects to one host the batch-exec way and opens an SFTP
// client on it. cleanup closes both.
func openFleetSFTP(db *store.DB, vault *creds.Vault, hostKeyCB ssh.HostKeyCallback, algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration, h BatchHostInput) (*sftp.Client, func(), error) {
	if h.Session != nil {
		// The session's cached client is shared with its SFTP browser and
		// closes with the session - never here.
		cli, err := h.Session.SFTPClient()
		return cli, func() {}, err
	}
	if strings.HasPrefix(h.ConnectionID, "session:") {
		return nil, func() {}, fmt.Errorf("the tab's session is closed")
	}
	if h.Settings == nil {
		return nil, func() {}, fmt.Errorf("could not resolve connection settings")
	}
	target, cleanup, err := buildChainQuiet(db, vault, h.Settings, hostKeyCB, algoLookup, connectTimeout)
	if err != nil {
		return nil, func() {}, err
	}
	cli, err := sftp.NewClient(target)
	if err != nil {
		cleanup()
		return nil, func() {}, explainSFTPOpenError(err)
	}
	return cli, func() { _ = cli.Close(); cleanup() }, nil
}

// fleetFail finishes a host's row with err (cancel is its own state).
func fleetFail(r FleetTransferHost, t0 time.Time, err error, onUpdate func(FleetTransferHost)) FleetTransferHost {
	r.State = "error"
	if errors.Is(err, ErrTransferCancelled) {
		r.State = "cancelled"
	} else {
		r.Error = err.Error()
	}
	r.Current = ""
	r.DurationMs = time.Since(t0).Milliseconds()
	onUpdate(r)
	return r
}

func uploadOneHost(
	db *store.DB,
	vault *creds.Vault,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	h BatchHostInput,
	r FleetTransferHost,
	opts FleetUploadOptions,
	onUpdate func(FleetTransferHost),
	cancel <-chan struct{},
) FleetTransferHost {
	t0 := time.Now()
	fail := func(err error) FleetTransferHost { return fleetFail(r, t0, err, onUpdate) }
	mode, chmod, err := ParseFleetMode(opts.Mode)
	if err != nil {
		return fail(err)
	}
	local, err := os.Stat(opts.LocalPath)
	if err != nil {
		return fail(err)
	}

	r.State = "connecting"
	onUpdate(r)
	cli, cleanup, err := openFleetSFTP(db, vault, hostKeyCB, algoLookup, connectTimeout, h)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	dir, err := fleetRemoteDir(cli, opts.RemoteDir)
	if err != nil {
		return fail(err)
	}
	if err := cli.MkdirAll(dir); err != nil && !strings.Contains(err.Error(), "exists") {
		return fail(fmt.Errorf("create %s: %w", dir, err))
	}
	r.Target = path.Join(dir, filepath.Base(filepath.Clean(opts.LocalPath)))
	r.State = "transferring"
	onUpdate(r)

	pol := &uploadPolicy{
		skip: func(fi os.FileInfo, remote string) bool {
			return skipExisting(cli, opts.Existing, fi, remote)
		},
		after: func(fi os.FileInfo, remote string) error {
			if chmod {
				if err := cli.Chmod(remote, mode); err != nil {
					return fmt.Errorf("chmod: %w", err)
				}
			}
			if opts.Existing == "changed" {
				// Best effort: without it "changed" re-sends every time.
				_ = cli.Chtimes(remote, time.Now(), fi.ModTime())
			}
			return nil
		},
	}

	if local.IsDir() {
		skipped, err := uploadDir(cli, opts.LocalPath, r.Target, func(p DirProgress) {
			r.FilesDone, r.FilesTotal = p.FilesDone, p.FilesTotal
			r.Bytes, r.Total, r.Current = p.BytesDone, p.BytesTotal, p.CurrentPath
			onUpdate(r)
		}, cancel, pol)
		r.FilesSkipped = skipped
		if err != nil {
			return fail(err)
		}
	} else {
		r.FilesTotal, r.Total = 1, local.Size()
		if pol.skip(local, r.Target) {
			r.FilesSkipped = 1
		} else {
			n, err := uploadFile(cli, opts.LocalPath, r.Target, func(w, total int64) {
				r.Bytes, r.Total = w, total
				onUpdate(r)
			}, cancel)
			r.Bytes = n
			if err != nil {
				return fail(err)
			}
			if err := pol.after(local, r.Target); err != nil {
				return fail(err)
			}
		}
		r.FilesDone, r.Bytes = 1, local.Size()
	}
	r.State = "done"
	r.Current = ""
	r.DurationMs = time.Since(t0).Milliseconds()
	onUpdate(r)
	return r
}

// fleetRemoteDir turns the user's target directory into an absolute path
// on this host: SFTP has no "~", and relative paths are taken from the
// server's working directory, which is the login user's home on OpenSSH.
func fleetRemoteDir(cli *sftp.Client, dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if strings.HasPrefix(dir, "/") {
		return path.Clean(dir), nil
	}
	home, err := cli.Getwd()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	switch {
	case dir == "" || dir == "~":
		return home, nil
	case strings.HasPrefix(dir, "~/"):
		return path.Join(home, dir[2:]), nil
	}
	return path.Join(home, dir), nil
}

// skipExisting applies FleetUploadOptions.Existing to one file.
func skipExisting(cli *sftp.Client, policy string, local os.FileInfo, remote string) bool {
	if policy == "overwrite" {
		return false
	}
	st, err := cli.Stat(remote)
	if err != nil {
		return false // not there (or unreadable): upload
	}
	if policy == "changed" {
		return st.Size() == local.Size() && !local.ModTime().Truncate(time.Second).After(st.ModTime())
	}
	return true // "skip" and anything unknown
}
