package syncer

import (
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP is a sync Transport backed by an SSH/SFTP server. It reuses the
// same SSH machinery as connections (auth methods resolved from the vault,
// host-key verification through the known-hosts store) - the app builds the
// auth + host-key callback and hands them in, so this package stays free of
// the store/vault dependencies.
//
// The whole snapshot is still sealed locally before upload (same envelope as
// WebDAV); the SFTP server only ever sees ciphertext. The win over WebDAV is
// that a rename on a POSIX server is genuinely atomic, so the temp -> live
// snapshot swap has no torn-read window, and most people already run an SSH
// server.
type SFTP struct {
	// Host / Port / User identify the server; Dir is the remote sync
	// directory (created on first push). AuthMethods + HostKeyCallback come
	// from the app's connect layer.
	Host    string
	Port    int
	User    string
	Dir     string
	Auth    []ssh.AuthMethod
	HostKey ssh.HostKeyCallback
	// HostKeyAlgorithms optionally pins the offered host-key algorithms, the
	// same way the connection layer does, so a known host with an ed25519
	// key isn't met with an rsa offer.
	HostKeyAlgorithms []string
	Timeout           time.Duration

	client *sftp.Client
	ssh    *ssh.Client
}

func (s *SFTP) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 30 * time.Second
}

// connect lazily dials SSH + opens an SFTP session, caching both. The
// transport is short-lived (one push/pull), so a single connection for the
// handful of operations is fine; Close releases it.
func (s *SFTP) connect() (*sftp.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	addr := net.JoinHostPort(s.Host, fmt.Sprintf("%d", s.Port))
	cfg := &ssh.ClientConfig{
		User:              s.User,
		Auth:              s.Auth,
		HostKeyCallback:   s.HostKey,
		HostKeyAlgorithms: s.HostKeyAlgorithms,
		Timeout:           s.timeout(),
	}
	conn, err := net.DialTimeout("tcp", addr, s.timeout())
	if err != nil {
		return nil, fmt.Errorf("sftp sync: dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp sync: ssh handshake: %w", err)
	}
	cli := ssh.NewClient(sshConn, chans, reqs)
	sc, err := sftp.NewClient(cli)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("sftp sync: open sftp: %w", err)
	}
	s.ssh = cli
	s.client = sc
	return sc, nil
}

// Close releases the SSH + SFTP connection. Safe to call more than once.
func (s *SFTP) Close() {
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
	if s.ssh != nil {
		_ = s.ssh.Close()
		s.ssh = nil
	}
}

// remote joins the configured Dir with a sync file name. Always forward
// slashes - SFTP paths are POSIX regardless of the local OS.
func (s *SFTP) remote(name string) string {
	return path.Join(s.Dir, name)
}

// EnsureDir creates the remote sync directory (and parents) if missing.
func (s *SFTP) EnsureDir() error {
	sc, err := s.connect()
	if err != nil {
		return err
	}
	// MkdirAll is idempotent and creates intermediate dirs. A pre-existing
	// directory is not an error.
	if err := sc.MkdirAll(s.Dir); err != nil {
		return fmt.Errorf("sftp sync: mkdir %s: %w", s.Dir, err)
	}
	return nil
}

// Get reads a sync file. ErrNotFound when it doesn't exist, matching the
// WebDAV transport's 404 contract.
func (s *SFTP) Get(name string) ([]byte, error) {
	sc, err := s.connect()
	if err != nil {
		return nil, err
	}
	f, err := sc.Open(s.remote(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sftp sync: open %s: %w", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("sftp sync: read %s: %w", name, err)
	}
	return data, nil
}

// Put writes a sync file, overwriting any existing content (truncate).
func (s *SFTP) Put(name string, data []byte) error {
	sc, err := s.connect()
	if err != nil {
		return err
	}
	f, err := sc.OpenFile(s.remote(name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sftp sync: create %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("sftp sync: write %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sftp sync: close %s: %w", name, err)
	}
	return nil
}

// Move renames within the sync dir, overwriting the destination. On a POSIX
// server this is atomic - the whole point of preferring SFTP for the
// temp -> live snapshot swap. PosixRename is used when the server advertises
// the posix-rename@openssh.com extension (overwrites atomically); otherwise
// fall back to a remove-then-rename, matching the WebDAV "overwrite" Move.
func (s *SFTP) Move(from, to string) error {
	sc, err := s.connect()
	if err != nil {
		return err
	}
	src, dst := s.remote(from), s.remote(to)
	if err := sc.PosixRename(src, dst); err == nil {
		return nil
	}
	// Fallback: plain Rename fails if the destination exists, so clear it
	// first. There's a brief window here with no live snapshot, but meta is
	// still committed last, so a concurrent reader keyed on meta is safe.
	_ = sc.Remove(dst)
	if err := sc.Rename(src, dst); err != nil {
		return fmt.Errorf("sftp sync: rename %s -> %s: %w", from, to, err)
	}
	return nil
}
