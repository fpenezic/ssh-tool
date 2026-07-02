// Package syncer implements encrypted profile sync over WebDAV.
//
// Free-tier personal sync: the whole profile (store.db snapshot +
// vault.enc, sealed into the same argon2id + XChaCha20-Poly1305
// envelope the backup feature uses) is pushed to / pulled from a
// user-provided WebDAV directory (Nextcloud, Apache mod_dav, rclone
// serve webdav, ...). The server only ever sees ciphertext plus a
// tiny plaintext meta file carrying a generation counter - a
// compromised WebDAV host learns nothing and can at worst serve a
// stale snapshot, which the generation guard surfaces.
//
// Concurrency model is git-like, not CRDT: push refuses when the
// remote generation isn't the one this machine last saw ("remote has
// changes - pull first"), pull replaces the local profile via the
// staged-restore path. No row-level merging, by design - snapshot
// semantics are predictable and cannot half-merge a vault.
package syncer

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound marks a 404 from the server (missing meta/snapshot).
var ErrNotFound = fmt.Errorf("not found")

// ValidateURL enforces the transport policy: https only, because
// WebDAV basic auth would otherwise cross the wire in plaintext (the
// snapshot itself is sealed, but the server credentials are not).
// Loopback is the one exception - local rclone/test servers and
// SSH-tunnelled endpoints terminate on this machine. Certificate
// verification is Go's default (full chain + hostname against the OS
// trust store, so private CAs installed system-wide just work); there
// is deliberately no skip-verify escape hatch - if demand appears,
// the right tool is TOFU cert pinning like host keys, not a toggle
// that silently allows MITM.
func ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("http is only allowed for localhost - use https (the WebDAV password would cross the network in plaintext)")
	}
	return fmt.Errorf("URL must start with https://")
}

// WebDAV is a minimal client - exactly the verbs sync needs (GET,
// PUT, MKCOL, MOVE), basic auth, no external dependency.
type WebDAV struct {
	// BaseURL is the sync directory, e.g.
	// https://cloud.example.com/remote.php/dav/files/user/ssh-tool/
	BaseURL  string
	Username string
	Password string
	Client   *http.Client
}

// Close satisfies Transport. WebDAV is stateless (a new HTTP request per
// call), so there's nothing to release.
func (w *WebDAV) Close() {}

func (w *WebDAV) http() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (w *WebDAV) url(name string) string {
	return strings.TrimRight(w.BaseURL, "/") + "/" + name
}

func (w *WebDAV) do(method, name string, body []byte, headers map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, w.url(name), rd)
	if err != nil {
		return nil, err
	}
	if w.Username != "" || w.Password != "" {
		req.SetBasicAuth(w.Username, w.Password)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return w.http().Do(req)
}

// EnsureDir creates the base directory if missing. 405 means "already
// exists" per RFC 4918 and is success; 409 means a parent is missing.
func (w *WebDAV) EnsureDir() error {
	resp, err := w.do("MKCOL", "", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode == 405:
		return nil
	case resp.StatusCode == 409:
		return fmt.Errorf("parent directory does not exist on the server (HTTP 409)")
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("authentication failed (HTTP %d)", resp.StatusCode)
	default:
		return fmt.Errorf("MKCOL: HTTP %d", resp.StatusCode)
	}
}

// Get downloads a file. ErrNotFound on 404.
func (w *WebDAV) Get(name string) ([]byte, error) {
	resp, err := w.do("GET", name, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d", name, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Put uploads a file (full overwrite).
func (w *WebDAV) Put(name string, data []byte) error {
	resp, err := w.do("PUT", name, data, map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PUT %s: HTTP %d", name, resp.StatusCode)
	}
	return nil
}

// Move renames within the sync dir, overwriting the destination -
// used for atomic snapshot replacement (PUT temp, MOVE over live).
// Servers without MOVE get a Put fallback from the caller.
func (w *WebDAV) Move(from, to string) error {
	resp, err := w.do("MOVE", from, nil, map[string]string{
		"Destination": w.url(to),
		"Overwrite":   "T",
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MOVE %s -> %s: HTTP %d", from, to, resp.StatusCode)
	}
	return nil
}
