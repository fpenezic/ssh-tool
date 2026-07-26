package syncer

// Dropbox implements Transport over the Dropbox v2 HTTP API, so the encrypted
// profile snapshot can sync to a user's Dropbox app folder. Auth is PKCE (see
// oauthpkce.go): the caller holds a refresh token in the vault and hands us a
// live access token plus a refresh closure. A mid-sync 401 (access token
// expired) self-heals - we refresh once and retry - so a long-idle app doesn't
// force the user to reconnect.
//
// The server, exactly as with WebDAV, only ever sees the sealed snapshot
// ciphertext + the tiny plaintext meta.json.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DropboxEndpoints is the PKCE config for Dropbox. scope files.content.write +
// .read are enough for the app-folder blob store; token_access_type=offline is
// what makes Dropbox issue a refresh token at all.
var DropboxEndpoints = OAuthEndpoints{
	AuthURL:         "https://www.dropbox.com/oauth2/authorize",
	TokenURL:        "https://api.dropboxapi.com/oauth2/token",
	Scopes:          []string{"files.content.write", "files.content.read"},
	ExtraAuthParams: map[string]string{"token_access_type": "offline"},
}

// Dropbox is a Transport backed by a Dropbox app folder. folder is the path
// prefix (e.g. "/ssh-tool"); names are joined under it. refresh returns a fresh
// access token on demand (used to recover from a 401).
type Dropbox struct {
	token   string
	refresh func() (string, error)
	folder  string
	Client  *http.Client
	// rpcBase/contentBase default to the live Dropbox hosts; tests point them
	// at an httptest server. Empty means the default.
	rpcBase     string
	contentBase string
}

const (
	dropboxRPCBase     = "https://api.dropboxapi.com"
	dropboxContentBase = "https://content.dropboxapi.com"
)

// NewDropbox builds a Dropbox transport. folder is normalized to a leading
// slash, no trailing slash (Dropbox paths are absolute; "" means app root).
func NewDropbox(accessToken, folder string, refresh func() (string, error)) *Dropbox {
	return &Dropbox{
		token:   accessToken,
		folder:  normalizeDropboxFolder(folder),
		refresh: refresh,
	}
}

func (d *Dropbox) rpc(path string) string {
	if d.rpcBase != "" {
		return d.rpcBase + path
	}
	return dropboxRPCBase + path
}

func (d *Dropbox) content(path string) string {
	if d.contentBase != "" {
		return d.contentBase + path
	}
	return dropboxContentBase + path
}

func normalizeDropboxFolder(folder string) string {
	folder = strings.TrimRight(strings.TrimSpace(folder), "/")
	if folder == "" {
		return ""
	}
	if !strings.HasPrefix(folder, "/") {
		folder = "/" + folder
	}
	return folder
}

func (d *Dropbox) http() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (d *Dropbox) path(name string) string {
	return d.folder + "/" + strings.TrimLeft(name, "/")
}

// Close satisfies Transport. Dropbox is stateless HTTP - nothing to release.
func (d *Dropbox) Close() {}

// do issues one Dropbox request, refreshing the access token and retrying once
// on a 401 (expired access token). The build closure produces a fresh request
// each attempt because a retry needs a rewound body.
func (d *Dropbox) do(build func(token string) (*http.Request, error)) (*http.Response, error) {
	req, err := build(d.token)
	if err != nil {
		return nil, err
	}
	resp, err := d.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || d.refresh == nil {
		return resp, nil
	}
	// Expired access token: refresh once and retry.
	resp.Body.Close()
	newToken, rerr := d.refresh()
	if rerr != nil {
		return nil, fmt.Errorf("dropbox token refresh: %w", rerr)
	}
	d.token = newToken
	req, err = build(d.token)
	if err != nil {
		return nil, err
	}
	return d.http().Do(req)
}

// EnsureDir creates the sync folder if missing. An already-existing folder
// (path/conflict) is success, mirroring WebDAV's treatment of MKCOL 405.
func (d *Dropbox) EnsureDir() error {
	if d.folder == "" {
		return nil // app root always exists
	}
	arg := map[string]any{"path": d.folder, "autorename": false}
	body, _ := json.Marshal(arg)
	resp, err := d.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPost, d.rpc("/2/files/create_folder_v2"), bytes.NewReader(body))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict && bytes.Contains(raw, []byte("path/conflict")) {
		return nil // folder already exists
	}
	return fmt.Errorf("dropbox create_folder: HTTP %d: %s", resp.StatusCode, summarizeDropboxErr(raw))
}

// Get downloads a file. A path/not_found maps to ErrNotFound, parity with the
// WebDAV 404 path so FetchMeta/Pull treat a never-pushed dir the same way.
func (d *Dropbox) Get(name string) ([]byte, error) {
	apiArg, _ := json.Marshal(map[string]string{"path": d.path(name)})
	resp, err := d.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPost, d.content("/2/files/download"), nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Dropbox-API-Arg", string(apiArg))
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return io.ReadAll(resp.Body)
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict && bytes.Contains(raw, []byte("path/not_found")) {
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf("dropbox download %s: HTTP %d: %s", name, resp.StatusCode, summarizeDropboxErr(raw))
}

// Put uploads a file, overwriting any existing content at the path.
func (d *Dropbox) Put(name string, data []byte) error {
	apiArg, _ := json.Marshal(map[string]any{
		"path": d.path(name),
		"mode": "overwrite",
		"mute": true,
	})
	resp, err := d.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPost, d.content("/2/files/upload"), bytes.NewReader(data))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Dropbox-API-Arg", string(apiArg))
		req.Header.Set("Content-Type", "application/octet-stream")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("dropbox upload %s: HTTP %d: %s", name, resp.StatusCode, summarizeDropboxErr(raw))
}

// Move renames within the sync folder, overwriting the destination - the atomic
// snapshot swap (PUT temp, MOVE over live). move_v2 does not overwrite, so on a
// destination conflict we fall back to copy-over-Put + delete, which the
// Transport contract explicitly permits.
func (d *Dropbox) Move(from, to string) error {
	arg, _ := json.Marshal(map[string]any{
		"from_path":  d.path(from),
		"to_path":    d.path(to),
		"autorename": false,
	})
	resp, err := d.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPost, d.rpc("/2/files/move_v2"), bytes.NewReader(arg))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	// Destination exists: Dropbox refuses to overwrite on move. Fall back to a
	// read + overwrite-Put on the destination, then delete the source.
	if resp.StatusCode == http.StatusConflict && bytes.Contains(raw, []byte("to")) {
		data, gerr := d.Get(from)
		if gerr != nil {
			return fmt.Errorf("dropbox move fallback read %s: %w", from, gerr)
		}
		if perr := d.Put(to, data); perr != nil {
			return fmt.Errorf("dropbox move fallback write %s: %w", to, perr)
		}
		return d.delete(from)
	}
	return fmt.Errorf("dropbox move %s -> %s: HTTP %d: %s", from, to, resp.StatusCode, summarizeDropboxErr(raw))
}

func (d *Dropbox) delete(name string) error {
	arg, _ := json.Marshal(map[string]string{"path": d.path(name)})
	resp, err := d.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPost, d.rpc("/2/files/delete_v2"), bytes.NewReader(arg))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("dropbox delete %s: HTTP %d: %s", name, resp.StatusCode, summarizeDropboxErr(raw))
}

// summarizeDropboxErr pulls the error_summary out of a Dropbox error body when
// present, else returns a trimmed snippet. Keeps messages user-actionable
// without dumping the full JSON.
func summarizeDropboxErr(raw []byte) string {
	var e struct {
		ErrorSummary string `json:"error_summary"`
	}
	if json.Unmarshal(raw, &e) == nil && e.ErrorSummary != "" {
		return e.ErrorSummary
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
