// SFTP client wrapper. We keep one *sftp.Client per session, lazily
// initialised on the first SFTP call. The underlying transport is the
// existing target ssh.Client at the end of the jump chain.
//
// Concurrent SFTP calls on the same session share the client (pkg/sftp
// is safe for concurrent use). Closing the client is deferred to session
// teardown; SetOnClose in session.go would normally take care of that,
// but we expose CloseSFTP for explicit cleanup paths too.

package ssh

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// SftpEntry is the wire format we hand back to the frontend. Mirrors
// sftp.FileInfo just enough to render a row.
type SftpEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`         // absolute path on the remote
	IsDir   bool   `json:"is_dir"`
	IsLink  bool   `json:"is_link"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`         // unix-style permission bits
	ModeStr string `json:"mode_str"`     // e.g. "-rw-r--r--"
	ModTime int64  `json:"mod_time"`     // unix seconds
	Target  string `json:"target,omitempty"` // symlink target if IsLink
	// UID/GID come from the raw SFTP attrs. Protocol v3 carries numbers
	// only - there is no name lookup over SFTP - so the UI shows the ids.
	// -1 means the server did not report them (some non-POSIX servers).
	UID int64 `json:"uid"`
	GID int64 `json:"gid"`
	// Owner/Group are resolved from the host's /etc/passwd and /etc/group
	// (see idnames.go) and are empty when the id is not in those files -
	// LDAP/SSSD accounts, or a server that refuses to serve them.
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// sftpClient lazily creates and caches the *sftp.Client. Held as a value
// inside Session so callers can reuse one open SFTP session across many
// IPC calls.
type sftpHolder struct {
	mu     sync.Mutex
	client *sftp.Client
}

// SFTPClient returns a *sftp.Client for this session, creating one on
// first use. The client is closed when the session closes (the wait
// goroutine in session.go calls CloseSFTP via the onClose hook installed
// by app startup; see app.go).
func (s *Session) SFTPClient() (*sftp.Client, error) {
	if s.sftp == nil {
		s.sftp = &sftpHolder{}
	}
	s.sftp.mu.Lock()
	defer s.sftp.mu.Unlock()
	if s.sftp.client != nil {
		return s.sftp.client, nil
	}
	tgt := s.TargetClient()
	if tgt == nil {
		return nil, errors.New("session has no target client")
	}
	cli, err := sftp.NewClient(tgt)
	if err != nil {
		return nil, fmt.Errorf("open sftp: %w", err)
	}
	s.sftp.client = cli
	return cli, nil
}

// CloseSFTP releases the cached SFTP client if one exists. Safe to call
// multiple times. Called from the session-close path.
func (s *Session) CloseSFTP() {
	if s.sftp == nil {
		return
	}
	s.sftp.mu.Lock()
	defer s.sftp.mu.Unlock()
	if s.sftp.client != nil {
		_ = s.sftp.client.Close()
		s.sftp.client = nil
	}
}

// SftpList returns the directory at remotePath. If remotePath is empty,
// the user's home directory is listed. Symlinks are not followed for
// the entries themselves (so the user sees them as links), but the
// target is resolved into Target for display.
func (s *Session) SftpList(remotePath string) (string, []SftpEntry, error) {
	cli, err := s.SFTPClient()
	if err != nil {
		return "", nil, err
	}
	// pkg/sftp doesn't expand ~; Getwd returns the CWD which is the user's
	// home after a default OpenSSH login. "~/sub" has to be rewritten too,
	// not just a bare "~" - the path bar lets one be pasted straight in.
	if remotePath == "" || remotePath == "~" || strings.HasPrefix(remotePath, "~/") {
		home, err := cli.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("getwd: %w", err)
		}
		if strings.HasPrefix(remotePath, "~/") {
			remotePath = path.Join(home, remotePath[2:])
		} else {
			remotePath = home
		}
	}
	infos, err := cli.ReadDir(remotePath)
	if err != nil {
		return remotePath, nil, fmt.Errorf("readdir %s: %w", remotePath, err)
	}
	out := make([]SftpEntry, 0, len(infos))
	for _, fi := range infos {
		entry := fileInfoToEntry(fi, path.Join(remotePath, fi.Name()))
		if entry.IsLink {
			if tgt, err := cli.ReadLink(entry.Path); err == nil {
				entry.Target = tgt
			}
		}
		out = append(out, entry)
	}
	s.ResolveIDNames(out)
	return remotePath, out, nil
}

func fileInfoToEntry(fi os.FileInfo, fullPath string) SftpEntry {
	mode := fi.Mode()
	// Sys() is *sftp.FileStat for entries that came off the wire; anything
	// else (or a server that omitted the attrs) leaves the ids unknown.
	uid, gid := int64(-1), int64(-1)
	if st, ok := fi.Sys().(*sftp.FileStat); ok && st != nil {
		uid, gid = int64(st.UID), int64(st.GID)
	}
	return SftpEntry{
		Name:    fi.Name(),
		Path:    fullPath,
		IsDir:   fi.IsDir(),
		IsLink:  mode&os.ModeSymlink != 0,
		Size:    fi.Size(),
		Mode:    uint32(mode.Perm()),
		ModeStr: mode.String(),
		ModTime: fi.ModTime().Unix(),
		UID:     uid,
		GID:     gid,
	}
}

// SftpStat stat()s a single path. Used to refresh one row after a rename
// or upload without re-reading the whole directory.
func (s *Session) SftpStat(remotePath string) (*SftpEntry, error) {
	cli, err := s.SFTPClient()
	if err != nil {
		return nil, err
	}
	fi, err := cli.Stat(remotePath)
	if err != nil {
		return nil, err
	}
	e := fileInfoToEntry(fi, remotePath)
	return &e, nil
}

// SftpMkdir creates a directory; parents must already exist (matches
// `mkdir`, not `mkdir -p`).
func (s *Session) SftpMkdir(remotePath string) error {
	cli, err := s.SFTPClient()
	if err != nil {
		return err
	}
	return cli.Mkdir(remotePath)
}

// SftpRemove removes a file or empty directory. For non-empty directories
// the caller must walk children first; we don't recurse by accident.
func (s *Session) SftpRemove(remotePath string) error {
	cli, err := s.SFTPClient()
	if err != nil {
		return err
	}
	fi, err := cli.Stat(remotePath)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return cli.RemoveDirectory(remotePath)
	}
	return cli.Remove(remotePath)
}

// SftpRename moves / renames a remote path.
func (s *Session) SftpRename(oldPath, newPath string) error {
	cli, err := s.SFTPClient()
	if err != nil {
		return err
	}
	return cli.Rename(oldPath, newPath)
}

// SftpReadAll reads the entire remote file into memory. Capped by the
// caller - small previews only. Use SftpDownload for large files.
func (s *Session) SftpReadAll(remotePath string, maxBytes int64) ([]byte, error) {
	cli, err := s.SFTPClient()
	if err != nil {
		return nil, err
	}
	f, err := cli.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if maxBytes <= 0 {
		return io.ReadAll(f)
	}
	return io.ReadAll(io.LimitReader(f, maxBytes))
}

// SftpWriteFile replaces a remote file's contents in place, preserving its
// permission bits. Used by the quick-view editor, which only ever saves a
// file the user already opened.
//
// Why not reuse SftpUpload: it calls Create(), which truncates and applies
// the server's default mode. Saving /etc/nginx/nginx.conf that way would
// silently relax it to 0644. Here we stat first, write, then restore the
// mode we saw.
//
// expectedModTime guards against a lost update: the caller passes the
// mod-time it read the file at, and a mismatch aborts rather than
// overwriting whatever changed underneath. Pass 0 to skip the check.
//
// The write is not atomic - there is no rename dance, because a temp file
// next to the target would need the same ownership to be safe, and SFTP
// gives us no way to set that as a non-root user. A failed write mid-way
// therefore leaves a truncated file, same as any editor writing in place
// over SFTP.
func (s *Session) SftpWriteFile(remotePath string, data []byte, expectedModTime int64) error {
	cli, err := s.SFTPClient()
	if err != nil {
		return err
	}
	fi, statErr := cli.Stat(remotePath)
	if statErr != nil {
		return fmt.Errorf("stat %s: %w", remotePath, statErr)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", remotePath)
	}
	if expectedModTime != 0 && fi.ModTime().Unix() != expectedModTime {
		return fmt.Errorf(
			"file changed on the server since it was opened (modified %s) - reopen it before saving",
			fi.ModTime().Format(time.RFC3339),
		)
	}
	mode := fi.Mode().Perm()

	f, err := cli.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open %s for writing: %w", remotePath, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", remotePath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", remotePath, err)
	}
	// Restore the mode we found. OpenFile above does not change it on an
	// existing file, but a server that created it fresh would apply its own
	// default, so set it back explicitly.
	if err := cli.Chmod(remotePath, mode); err != nil {
		return fmt.Errorf("restore mode on %s: %w", remotePath, err)
	}
	return nil
}

// TransferProgress is a chunk of progress info emitted during up/down.
type TransferProgress struct {
	TransferID  string `json:"transfer_id"`
	Bytes       int64  `json:"bytes"`
	Total       int64  `json:"total"`
	Done        bool   `json:"done"`
	Err         string `json:"err,omitempty"`
	// Recursive transfer fields (zero for single-file transfers).
	FilesDone   int    `json:"files_done,omitempty"`
	FilesTotal  int    `json:"files_total,omitempty"`
	CurrentPath string `json:"current_path,omitempty"`
}

// progressWriter wraps an io.Writer and calls onChunk every flushInterval
// or every chunkBytes, whichever comes first. Caller passes the running
// total so we can include Total in the emit.
type progressWriter struct {
	w             io.Writer
	bytes         int64
	total         int64
	onChunk       func(written, total int64)
	lastEmit      time.Time
	emitEvery     time.Duration
	emitEveryByte int64
	emittedAt     int64
}

func newProgressWriter(w io.Writer, total int64, onChunk func(written, total int64)) *progressWriter {
	return &progressWriter{
		w:             w,
		total:         total,
		onChunk:       onChunk,
		emitEvery:     100 * time.Millisecond,
		emitEveryByte: 256 * 1024,
		lastEmit:      time.Now(),
	}
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.bytes += int64(n)
	now := time.Now()
	if now.Sub(p.lastEmit) >= p.emitEvery || p.bytes-p.emittedAt >= p.emitEveryByte {
		p.onChunk(p.bytes, p.total)
		p.lastEmit = now
		p.emittedAt = p.bytes
	}
	return n, err
}

// SftpDownload streams a remote file to a local path. Progress is
// reported via onProgress; the caller is responsible for routing those
// to the frontend. cancel may be closed to abort the transfer mid-way.
func (s *Session) SftpDownload(remotePath, localPath string, onProgress func(written, total int64), cancel <-chan struct{}) (int64, error) {
	cli, err := s.SFTPClient()
	if err != nil {
		return 0, err
	}
	fi, err := cli.Stat(remotePath)
	if err != nil {
		return 0, err
	}
	if fi.IsDir() {
		return 0, errors.New("download: source is a directory")
	}
	src, err := cli.Open(remotePath)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	pw := newProgressWriter(dst, fi.Size(), onProgress)
	return copyWithCancel(pw, src, cancel)
}

// SftpUpload streams a local file to the remote. Progress + cancel behave
// the same as SftpDownload.
func (s *Session) SftpUpload(localPath, remotePath string, onProgress func(written, total int64), cancel <-chan struct{}) (int64, error) {
	cli, err := s.SFTPClient()
	if err != nil {
		return 0, err
	}
	src, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return 0, err
	}
	dst, err := cli.Create(remotePath)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	pw := newProgressWriter(dst, fi.Size(), onProgress)
	return copyWithCancel(pw, src, cancel)
}

// copyWithCancel is io.Copy with a cancel channel checked between chunks.
// Cancelled transfers return ErrTransferCancelled and leave the partial
// destination behind for the caller to clean up.
func copyWithCancel(dst io.Writer, src io.Reader, cancel <-chan struct{}) (int64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	for {
		select {
		case <-cancel:
			return total, ErrTransferCancelled
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			nw, werr := dst.Write(buf[:n])
			total += int64(nw)
			if werr != nil {
				return total, werr
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// ErrTransferCancelled is returned when the cancel channel fires mid-copy.
var ErrTransferCancelled = errors.New("transfer cancelled")

// DirProgress is the aggregate state of a recursive transfer.
type DirProgress struct {
	FilesDone   int    `json:"files_done"`
	FilesTotal  int    `json:"files_total"`
	BytesDone   int64  `json:"bytes_done"`
	BytesTotal  int64  `json:"bytes_total"`
	CurrentPath string `json:"current_path"`
}

// SftpDownloadDir mirrors a remote directory tree into a local one.
// Walks the remote tree first to learn the total file count + byte
// count for accurate progress, then downloads sequentially. Symlinks
// are skipped (we don't recreate them locally). cancel aborts mid-
// transfer; partial files are left as-is for the caller to clean.
func (s *Session) SftpDownloadDir(remoteRoot, localRoot string, onProgress func(DirProgress), cancel <-chan struct{}) error {
	cli, err := s.SFTPClient()
	if err != nil {
		return err
	}
	// Canonicalise localRoot so the boundary check below compares
	// against an absolute, lexical form. EvalSymlinks would be nicer
	// but fails if the dir doesn't exist yet (it usually does - the
	// user picked it - but be conservative).
	rootAbs, err := filepath.Abs(localRoot)
	if err != nil {
		return fmt.Errorf("resolve local root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)

	// safeJoin returns the local path for `rel` only if the joined
	// result stays underneath localRoot. A hostile SFTP server could
	// otherwise feed a Walk that emits paths whose Rel-to-remoteRoot
	// contains `..` (or absolute paths altogether), and Join would
	// happily resolve to ~/.ssh/authorized_keys, ~/.bashrc, etc.
	// This is the C2 fix from the security audit.
	safeJoin := func(rel string) (string, bool) {
		if rel == "" || rel == "." {
			return rootAbs, true
		}
		// Reject absolute paths outright - Rel between two unrelated
		// paths can return them. The downloaded tree only ever uses
		// paths relative to remoteRoot.
		if filepath.IsAbs(rel) {
			return "", false
		}
		joined := filepath.Clean(filepath.Join(rootAbs, rel))
		// joined must be rootAbs itself OR be inside it. Boundary
		// check uses the trailing separator to avoid the classic
		// prefix-match false positive (`/foo` vs `/foobar`).
		if joined == rootAbs {
			return joined, true
		}
		sep := string(filepath.Separator)
		if !strings.HasPrefix(joined, rootAbs+sep) {
			return "", false
		}
		return joined, true
	}

	// Walk + plan.
	type item struct {
		Remote string
		Local  string
		Size   int64
	}
	var items []item
	var dirs []string
	var totalBytes int64

	walker := cli.Walk(remoteRoot)
	for walker.Step() {
		if werr := walker.Err(); werr != nil {
			return fmt.Errorf("walk %s: %w", walker.Path(), werr)
		}
		fi := walker.Stat()
		if fi == nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		rel, _ := filepath.Rel(remoteRoot, walker.Path())
		if rel == "." {
			rel = ""
		}
		local, ok := safeJoin(rel)
		if !ok {
			return fmt.Errorf("refusing %q: resolves outside download root", walker.Path())
		}
		if fi.IsDir() {
			dirs = append(dirs, local)
		} else {
			items = append(items, item{Remote: walker.Path(), Local: local, Size: fi.Size()})
			totalBytes += fi.Size()
		}
	}
	// Create local directory skeleton up front.
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	prog := DirProgress{FilesTotal: len(items), BytesTotal: totalBytes}
	for i, it := range items {
		select {
		case <-cancel:
			return ErrTransferCancelled
		default:
		}
		// Use the path validated at walk-time, not a re-derived one.
		rel, _ := filepath.Rel(rootAbs, it.Local)
		prog.CurrentPath = rel
		prog.FilesDone = i
		onProgress(prog)
		_ = os.MkdirAll(filepath.Dir(it.Local), 0o755)
		n, derr := s.SftpDownload(it.Remote, it.Local, func(_, _ int64) {}, cancel)
		prog.BytesDone += n
		if derr != nil {
			return fmt.Errorf("download %s: %w", rel, derr)
		}
	}
	prog.FilesDone = len(items)
	prog.CurrentPath = ""
	onProgress(prog)
	return nil
}

// SftpUploadDir mirrors a local directory tree into a remote one.
// Mirror logic + symlink rules match SftpDownloadDir.
func (s *Session) SftpUploadDir(localRoot, remoteRoot string, onProgress func(DirProgress), cancel <-chan struct{}) error {
	cli, err := s.SFTPClient()
	if err != nil {
		return err
	}
	type item struct {
		Local string
		Size  int64
	}
	var items []item
	var dirs []string
	var totalBytes int64

	werr := filepath.Walk(localRoot, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(localRoot, p)
		if rel == "." {
			rel = ""
		}
		remote := path.Join(remoteRoot, filepath.ToSlash(rel))
		if info.IsDir() {
			dirs = append(dirs, remote)
		} else {
			items = append(items, item{Local: p, Size: info.Size()})
			totalBytes += info.Size()
		}
		return nil
	})
	if werr != nil {
		return werr
	}
	// Make remote dirs (parents before children - Walk returns parents
	// first so the order is already correct).
	if err := cli.MkdirAll(remoteRoot); err != nil && !strings.Contains(err.Error(), "exists") {
		return err
	}
	for _, d := range dirs {
		if err := cli.MkdirAll(d); err != nil && !strings.Contains(err.Error(), "exists") {
			return err
		}
	}

	prog := DirProgress{FilesTotal: len(items), BytesTotal: totalBytes}
	for i, it := range items {
		select {
		case <-cancel:
			return ErrTransferCancelled
		default:
		}
		rel, _ := filepath.Rel(localRoot, it.Local)
		remotePath := path.Join(remoteRoot, filepath.ToSlash(rel))
		prog.CurrentPath = rel
		prog.FilesDone = i
		onProgress(prog)
		// Parent might not exist if a stray top-level file landed first;
		// safe to attempt.
		_ = cli.MkdirAll(path.Dir(remotePath))
		n, uerr := s.SftpUpload(it.Local, remotePath, func(_, _ int64) {}, cancel)
		prog.BytesDone += n
		if uerr != nil {
			return fmt.Errorf("upload %s: %w", rel, uerr)
		}
	}
	prog.FilesDone = len(items)
	prog.CurrentPath = ""
	onProgress(prog)
	return nil
}
