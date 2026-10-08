package ssh

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// FleetDownloadOptions fetches the same remote path from many hosts into
// one local folder, each host under its own subfolder.
type FleetDownloadOptions struct {
	// RemotePath is a file, a directory (fetched recursively) or a glob
	// such as /var/log/nginx/*.log. "~" and relative paths are taken
	// from the login user's home. The glob is matched by the SFTP
	// client, not a shell: * ? [..] work, ** and {a,b} do not.
	RemotePath string `json:"remote_path"`
	LocalDir   string `json:"local_dir"`
	// SkipCompressed leaves out rotated archives (.gz, .xz, .zst, ...).
	SkipCompressed bool `json:"skip_compressed"`
}

// remoteItem is one regular file to fetch; Rel is where it goes under the
// host's folder: the matched file's name, or the matched directory's name
// followed by the path inside it.
type remoteItem struct {
	Remote string
	Rel    string
	Size   int64
}

var compressedExts = []string{".gz", ".tgz", ".bz2", ".xz", ".zst", ".lz4", ".zip", ".7z"}

func isCompressed(name string) bool {
	low := strings.ToLower(name)
	for _, e := range compressedExts {
		if strings.HasSuffix(low, e) {
			return true
		}
	}
	return false
}

// scanRemote lists what a download of opts.RemotePath would fetch. The
// second value counts entries that could not be read (permission denied
// inside a directory, an unreadable glob match); they are left out
// rather than failing the host.
func scanRemote(cli *sftp.Client, remotePath string, skipCompressed bool) ([]remoteItem, int, error) {
	if strings.TrimSpace(remotePath) == "" {
		return nil, 0, fmt.Errorf("no remote path given")
	}
	full, err := fleetRemoteDir(cli, remotePath)
	if err != nil {
		return nil, 0, err
	}
	glob := strings.ContainsAny(full, "*?[")
	roots := []string{full}
	if glob {
		if roots, err = cli.Glob(full); err != nil {
			return nil, 0, fmt.Errorf("match %s: %w", full, err)
		}
		if len(roots) == 0 {
			return nil, 0, fmt.Errorf("nothing matches %s", full)
		}
	}
	var items []remoteItem
	unreadable := 0
	seen := map[string]bool{}
	add := func(remote, rel string, size int64) {
		if seen[remote] || (skipCompressed && isCompressed(remote)) {
			return
		}
		seen[remote] = true
		items = append(items, remoteItem{Remote: remote, Rel: rel, Size: size})
	}
	for _, root := range roots {
		st, err := cli.Stat(root) // follows a symlinked root on purpose
		if err != nil {
			if !glob {
				return nil, 0, err
			}
			unreadable++
			continue
		}
		if !st.IsDir() {
			if st.Mode().IsRegular() {
				add(root, path.Base(root), st.Size())
			}
			continue
		}
		w := cli.Walk(root)
		for w.Step() {
			if w.Err() != nil {
				unreadable++
				continue
			}
			fi := w.Stat()
			if !fi.Mode().IsRegular() {
				continue // dirs, and symlinks are not followed inside a tree
			}
			inner := strings.TrimPrefix(strings.TrimPrefix(w.Path(), root), "/")
			add(w.Path(), path.Join(path.Base(root), inner), fi.Size())
		}
	}
	return items, unreadable, nil
}

// FleetDownloadScan reports per host how many files a download would
// fetch and how big they are, without fetching anything.
func FleetDownloadScan(
	db *store.DB,
	vault *creds.Vault,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	hosts []BatchHostInput,
	opts FleetDownloadOptions,
) []FleetTransferHost {
	if connectTimeout <= 0 {
		connectTimeout = 20 * time.Second
	}
	noop := func(FleetTransferHost) {}
	return fleetRun(hosts, noop, nil, func(h BatchHostInput, r FleetTransferHost) FleetTransferHost {
		t0 := time.Now()
		cli, cleanup, err := openFleetSFTP(db, vault, hostKeyCB, algoLookup, connectTimeout, h)
		if err != nil {
			return fleetFail(r, t0, err, noop)
		}
		defer cleanup()
		items, unreadable, err := scanRemote(cli, opts.RemotePath, opts.SkipCompressed)
		if err != nil {
			return fleetFail(r, t0, err, noop)
		}
		r.FilesTotal, r.Unreadable = len(items), unreadable
		for _, it := range items {
			r.Total += it.Size
		}
		r.State = "done"
		r.DurationMs = time.Since(t0).Milliseconds()
		return r
	})
}

// FleetDownload fetches opts.RemotePath from every host into
// opts.LocalDir/<dirs[connection id]>/. A file that fails (rotated away,
// unreadable) does not stop the rest of that host; the host ends in
// "error" naming how many failed. Existing local files are replaced.
func FleetDownload(
	db *store.DB,
	vault *creds.Vault,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	hosts []BatchHostInput,
	dirs map[string]string,
	opts FleetDownloadOptions,
	onUpdate func(FleetTransferHost),
	cancel <-chan struct{},
) []FleetTransferHost {
	if connectTimeout <= 0 {
		connectTimeout = 20 * time.Second
	}
	return fleetRun(hosts, onUpdate, cancel, func(h BatchHostInput, r FleetTransferHost) FleetTransferHost {
		t0 := time.Now()
		fail := func(err error) FleetTransferHost { return fleetFail(r, t0, err, onUpdate) }
		r.State = "connecting"
		onUpdate(r)
		cli, cleanup, err := openFleetSFTP(db, vault, hostKeyCB, algoLookup, connectTimeout, h)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		items, unreadable, err := scanRemote(cli, opts.RemotePath, opts.SkipCompressed)
		if err != nil {
			return fail(err)
		}
		hostDir := filepath.Join(opts.LocalDir, dirs[h.ConnectionID])
		r.Target = hostDir
		r.FilesTotal, r.Unreadable = len(items), unreadable
		for _, it := range items {
			r.Total += it.Size
		}
		r.State = "transferring"
		onUpdate(r)

		failed := 0
		var firstErr error
		for i, it := range items {
			select {
			case <-cancel:
				return fail(ErrTransferCancelled)
			default:
			}
			r.FilesDone, r.Current = i, it.Rel
			local, err := safeLocalPath(hostDir, it.Rel)
			if err == nil {
				err = os.MkdirAll(filepath.Dir(local), 0o755)
			}
			base := r.Bytes
			if err == nil {
				_, err = downloadFile(cli, it.Remote, local, func(w, _ int64) {
					r.Bytes = base + w
					onUpdate(r)
				}, cancel)
			}
			r.Bytes = base + it.Size
			if err != nil {
				if isCancelErr(err) {
					return fail(ErrTransferCancelled)
				}
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", it.Rel, err)
				}
			}
		}
		r.FilesDone, r.Current = len(items), ""
		r.DurationMs = time.Since(t0).Milliseconds()
		if failed > 0 {
			r.State = "error"
			r.Error = fmt.Sprintf("%d of %d files failed - first: %v", failed, len(items), firstErr)
		} else {
			r.State = "done"
		}
		onUpdate(r)
		return r
	})
}

// isCancelErr: pkg/sftp can hand the writer's cancel error back wrapped
// or flattened into its own, so match the text too.
func isCancelErr(err error) bool {
	return errors.Is(err, ErrTransferCancelled) || strings.Contains(err.Error(), ErrTransferCancelled.Error())
}

// safeLocalPath joins a remote-derived relative path under base. Each
// part is made a legal file name on every OS (a Linux log may well be
// called "a:b"), and nothing can climb out of base.
func safeLocalPath(base, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		clean = append(clean, SafeFileName(p))
	}
	if len(clean) == 0 {
		return "", fmt.Errorf("empty file name")
	}
	out := filepath.Join(append([]string{base}, clean...)...)
	if r, err := filepath.Rel(base, out); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the download folder", rel)
	}
	return out, nil
}

// SafeFileName makes one path component legal on Windows, macOS and
// Linux: reserved characters and controls become "_", trailing dots and
// spaces go (Windows drops them), ".." and reserved device names are
// prefixed so they stay ordinary names.
func SafeFileName(name string) string {
	var b strings.Builder
	for _, c := range name {
		if c < 0x20 || strings.ContainsRune(`<>:"/\|?*`, c) {
			b.WriteRune('_')
		} else {
			b.WriteRune(c)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	if out == "" {
		return "_"
	}
	stem := strings.ToUpper(strings.SplitN(out, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		out = "_" + out
	}
	return out
}
