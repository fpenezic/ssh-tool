package syncer

// GoogleDrive implements Transport over the Google Drive v3 API, syncing the
// encrypted profile snapshot to the hidden per-app "appDataFolder" space. Auth
// is the same PKCE flow as Dropbox/OneDrive (oauthpkce.go): the caller holds a
// refresh token in the vault and hands us a live access token plus a refresh
// closure; a mid-sync 401 self-heals.
//
// Unlike Dropbox (path RPC) and OneDrive (path-addressed URLs), Drive is
// ID-addressed: a file has an opaque fileId, and you find it by querying the
// appDataFolder space for name = '<x>'. So Get/Put/Move all begin by resolving
// the name to an id. Uploads for a new name POST to the upload endpoint with an
// appDataFolder parent; updates PATCH the existing id's media. There is no
// directory to create - the appDataFolder space exists per app implicitly, so
// EnsureDir is a no-op.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GoogleDriveEndpoints is the PKCE config for Google Drive. The
// drive.appdata scope restricts access to the app's own hidden folder;
// access_type=offline + prompt=consent make Google return a refresh token.
var GoogleDriveEndpoints = OAuthEndpoints{
	AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
	Scopes:   []string{"https://www.googleapis.com/auth/drive.appdata"},
	ExtraAuthParams: map[string]string{
		"access_type": "offline",
		// Google only returns a refresh token on the FIRST consent unless
		// prompt=consent is forced, so a reconnect always yields one.
		"prompt": "consent",
	},
}

const (
	gdriveAPIDefault    = "https://www.googleapis.com/drive/v3"
	gdriveUploadDefault = "https://www.googleapis.com/upload/drive/v3"
)

// GoogleDrive is a Transport backed by the app-data folder. Names are flat
// (meta.json, snapshot.stb) inside the appDataFolder space.
type GoogleDrive struct {
	token   string
	refresh func() (string, error)
	Client  *http.Client
	// apiBase/uploadBase default to the live hosts; tests point them at httptest.
	apiBase    string
	uploadBase string
}

// NewGoogleDrive builds a Drive transport from a live access token and a
// refresh closure (used to recover from a 401).
func NewGoogleDrive(accessToken string, refresh func() (string, error)) *GoogleDrive {
	return &GoogleDrive{token: accessToken, refresh: refresh}
}

func (g *GoogleDrive) http() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (g *GoogleDrive) api() string {
	if g.apiBase != "" {
		return g.apiBase
	}
	return gdriveAPIDefault
}

func (g *GoogleDrive) upload() string {
	if g.uploadBase != "" {
		return g.uploadBase
	}
	return gdriveUploadDefault
}

// Close satisfies Transport. Drive is stateless HTTP - nothing to release.
func (g *GoogleDrive) Close() {}

// do issues one Drive request, refreshing the access token and retrying once on
// a 401. build produces a fresh request each attempt (rewound body on retry).
func (g *GoogleDrive) do(build func(token string) (*http.Request, error)) (*http.Response, error) {
	req, err := build(g.token)
	if err != nil {
		return nil, err
	}
	resp, err := g.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || g.refresh == nil {
		return resp, nil
	}
	resp.Body.Close()
	newToken, rerr := g.refresh()
	if rerr != nil {
		return nil, fmt.Errorf("gdrive token refresh: %w", rerr)
	}
	g.token = newToken
	req, err = build(g.token)
	if err != nil {
		return nil, err
	}
	return g.http().Do(req)
}

// resolveID returns the fileId of a name in the appDataFolder space, or "" if
// it does not exist yet.
func (g *GoogleDrive) resolveID(name string) (string, error) {
	// q escapes single quotes per Drive query syntax.
	q := fmt.Sprintf("name = '%s'", strings.ReplaceAll(name, "'", `\'`))
	params := url.Values{
		"q":        {q},
		"spaces":   {"appDataFolder"},
		"fields":   {"files(id,name)"},
		"pageSize": {"1"},
	}
	u := g.api() + "/files?" + params.Encode()
	resp, err := g.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodGet, u, nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gdrive list %s: HTTP %d: %s", name, resp.StatusCode, summarizeGraphErr(raw))
	}
	var body struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("gdrive list decode: %w", err)
	}
	if len(body.Files) == 0 {
		return "", nil
	}
	return body.Files[0].ID, nil
}

// EnsureDir is a no-op: the appDataFolder space exists per app implicitly.
func (g *GoogleDrive) EnsureDir() error { return nil }

// Get downloads a file by resolving its id then fetching alt=media. A missing
// name maps to ErrNotFound (parity with the other transports).
func (g *GoogleDrive) Get(name string) ([]byte, error) {
	id, err := g.resolveID(name)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, ErrNotFound
	}
	u := g.api() + "/files/" + id + "?alt=media"
	resp, err := g.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodGet, u, nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return io.ReadAll(resp.Body)
	}
	raw, _ := io.ReadAll(resp.Body)
	return nil, fmt.Errorf("gdrive download %s: HTTP %d: %s", name, resp.StatusCode, summarizeGraphErr(raw))
}

// Put creates or overwrites a file. If the name already exists, PATCH its media
// (keeps the id stable); otherwise multipart-POST new metadata + content with an
// appDataFolder parent.
func (g *GoogleDrive) Put(name string, data []byte) error {
	id, err := g.resolveID(name)
	if err != nil {
		return err
	}
	if id != "" {
		return g.updateMedia(id, data)
	}
	return g.createMultipart(name, data)
}

// updateMedia overwrites an existing file's content by id (media upload PATCH).
func (g *GoogleDrive) updateMedia(id string, data []byte) error {
	u := g.upload() + "/files/" + id + "?uploadType=media"
	resp, err := g.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPatch, u, bytes.NewReader(data))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
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
	return fmt.Errorf("gdrive update: HTTP %d: %s", resp.StatusCode, summarizeGraphErr(raw))
}

// createMultipart uploads a new file with metadata (name + appDataFolder parent)
// and content in one multipart/related request.
func (g *GoogleDrive) createMultipart(name string, data []byte) error {
	meta, _ := json.Marshal(map[string]any{
		"name":    name,
		"parents": []string{"appDataFolder"},
	})
	const boundary = "sshtool-gdrive-boundary"
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Type: application/json; charset=UTF-8\r\n\r\n")
	body.Write(meta)
	body.WriteString("\r\n--" + boundary + "\r\n")
	body.WriteString("Content-Type: application/octet-stream\r\n\r\n")
	body.Write(data)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	u := g.upload() + "/files?uploadType=multipart"
	payload := body.Bytes()
	resp, err := g.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPost, u, bytes.NewReader(payload))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)
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
	return fmt.Errorf("gdrive create %s: HTTP %d: %s", name, resp.StatusCode, summarizeGraphErr(raw))
}

// Move renames from -> to, overwriting the destination - the atomic snapshot
// swap. Drive rename is a metadata PATCH of the name. Since two files can share
// a name in Drive, we first delete any existing destination, then rename the
// source onto it, so a subsequent resolveID finds exactly one.
func (g *GoogleDrive) Move(from, to string) error {
	fromID, err := g.resolveID(from)
	if err != nil {
		return err
	}
	if fromID == "" {
		return fmt.Errorf("gdrive move: source %s not found", from)
	}
	// Drop any existing destination so the rename doesn't create a duplicate name.
	if toID, err := g.resolveID(to); err == nil && toID != "" {
		if derr := g.deleteID(toID); derr != nil {
			return fmt.Errorf("gdrive move clear dest %s: %w", to, derr)
		}
	}
	patch, _ := json.Marshal(map[string]any{"name": to})
	u := g.api() + "/files/" + fromID
	resp, err := g.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPatch, u, bytes.NewReader(patch))
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
	return fmt.Errorf("gdrive move %s -> %s: HTTP %d: %s", from, to, resp.StatusCode, summarizeGraphErr(raw))
}

func (g *GoogleDrive) deleteID(id string) error {
	u := g.api() + "/files/" + id
	resp, err := g.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodDelete, u, nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("gdrive delete: HTTP %d: %s", resp.StatusCode, summarizeGraphErr(raw))
}
