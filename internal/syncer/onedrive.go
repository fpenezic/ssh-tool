package syncer

// OneDrive implements Transport over the Microsoft Graph API, syncing the
// encrypted profile snapshot to the app's dedicated OneDrive folder (the
// "approot" special folder, scoped by Files.ReadWrite.AppFolder). Auth is the
// same PKCE flow as Dropbox (see oauthpkce.go): the caller holds a refresh
// token in the vault and hands us a live access token plus a refresh closure;
// a mid-sync 401 self-heals.
//
// Unlike Dropbox's RPC style, Graph is path-addressed: the file path is part of
// the URL (/special/approot:/<path>:/content). A missing file is a plain 404,
// which maps straight to ErrNotFound. There is no folder to create - the
// approot exists implicitly the first time the app writes to it, so EnsureDir
// is a no-op.

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

// OneDriveEndpoints is the PKCE config for personal OneDrive (the default when
// no account type is chosen). Kept for back-compat; new callers should use
// OneDriveEndpointsFor to pick the authority that matches the user's account.
var OneDriveEndpoints = OneDriveEndpointsFor("")

// OneDriveEndpointsFor returns the PKCE config for the given Microsoft account
// type, which selects the authority segment of the login URL:
//   - "personal" (or "") -> /consumers : personal Microsoft accounts only
//     (outlook.com, hotmail.com, live.com, ...).
//   - "work"             -> /organizations : work / school (Entra ID) accounts.
//   - "both"             -> /common : either kind.
//
// offline_access is required to get a refresh token; Files.ReadWrite.AppFolder
// scopes access to just the app's own folder. The user's Azure app registration
// "Supported account types" must be compatible with the authority they pick.
func OneDriveEndpointsFor(accountType string) OAuthEndpoints {
	authority := "consumers"
	switch accountType {
	case "work":
		authority = "organizations"
	case "both":
		authority = "common"
	}
	return OAuthEndpoints{
		AuthURL:  "https://login.microsoftonline.com/" + authority + "/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/" + authority + "/oauth2/v2.0/token",
		Scopes:   []string{"Files.ReadWrite.AppFolder", "offline_access"},
	}
}

const graphBaseDefault = "https://graph.microsoft.com/v1.0"

// OneDrive is a Transport backed by the app's OneDrive folder. Names live
// directly under approot (a flat blob store - meta.json, snapshot.stb).
type OneDrive struct {
	token   string
	refresh func() (string, error)
	Client  *http.Client
	// graphBase defaults to the live Graph host; tests point it at httptest.
	graphBase string
}

// NewOneDrive builds a OneDrive transport from a live access token and a
// refresh closure (used to recover from a 401).
func NewOneDrive(accessToken string, refresh func() (string, error)) *OneDrive {
	return &OneDrive{token: accessToken, refresh: refresh}
}

func (o *OneDrive) http() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (o *OneDrive) base() string {
	if o.graphBase != "" {
		return o.graphBase
	}
	return graphBaseDefault
}

// itemURL builds a path-addressed approot URL for a name, with a trailing Graph
// verb (":/content", or "" for the item's metadata endpoint).
func (o *OneDrive) itemURL(name, suffix string) string {
	// approot:/<escaped name>:<suffix> ; PathEscape keeps any odd chars safe.
	p := url.PathEscape(strings.TrimLeft(name, "/"))
	return fmt.Sprintf("%s/me/drive/special/approot:/%s:%s", o.base(), p, suffix)
}

// Close satisfies Transport. Graph is stateless HTTP - nothing to release.
func (o *OneDrive) Close() {}

// do issues one Graph request, refreshing the access token and retrying once on
// a 401 (expired token). build produces a fresh request each attempt so a retry
// has a rewound body.
func (o *OneDrive) do(build func(token string) (*http.Request, error)) (*http.Response, error) {
	req, err := build(o.token)
	if err != nil {
		return nil, err
	}
	resp, err := o.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || o.refresh == nil {
		return resp, nil
	}
	resp.Body.Close()
	newToken, rerr := o.refresh()
	if rerr != nil {
		return nil, fmt.Errorf("onedrive token refresh: %w", rerr)
	}
	o.token = newToken
	req, err = build(o.token)
	if err != nil {
		return nil, err
	}
	return o.http().Do(req)
}

// EnsureDir is a no-op: the approot special folder is created implicitly on
// first write, so there is nothing to provision.
func (o *OneDrive) EnsureDir() error { return nil }

// Get downloads a file. A 404 maps to ErrNotFound (parity with WebDAV/Dropbox),
// so FetchMeta/Pull treat a never-pushed folder the same way.
func (o *OneDrive) Get(name string) ([]byte, error) {
	resp, err := o.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodGet, o.itemURL(name, "/content"), nil)
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
	return nil, fmt.Errorf("onedrive download %s: HTTP %d: %s", name, resp.StatusCode, summarizeGraphErr(raw))
}

// Put uploads a file with a simple PUT to :/content, overwriting any existing
// content. Good for files up to 250 MB - far above our sealed snapshot.
func (o *OneDrive) Put(name string, data []byte) error {
	resp, err := o.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPut, o.itemURL(name, "/content"), bytes.NewReader(data))
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
	return fmt.Errorf("onedrive upload %s: HTTP %d: %s", name, resp.StatusCode, summarizeGraphErr(raw))
}

// Move renames within the app folder, overwriting the destination - the atomic
// snapshot swap (PUT temp, MOVE over live). Graph move is a PATCH that sets the
// new name, and it will NOT overwrite an existing destination (409 nameAlready
// Exists), so on conflict we fall back to copy-over-Put + delete, which the
// Transport contract permits.
func (o *OneDrive) Move(from, to string) error {
	patch, _ := json.Marshal(map[string]any{"name": strings.TrimLeft(to, "/")})
	resp, err := o.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodPatch, o.itemURL(from, ""), bytes.NewReader(patch))
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
	// Destination exists: Graph refuses to overwrite on rename. Fall back to a
	// read + overwrite-Put on the destination, then delete the source.
	if resp.StatusCode == http.StatusConflict {
		data, gerr := o.Get(from)
		if gerr != nil {
			return fmt.Errorf("onedrive move fallback read %s: %w", from, gerr)
		}
		if perr := o.Put(to, data); perr != nil {
			return fmt.Errorf("onedrive move fallback write %s: %w", to, perr)
		}
		return o.delete(from)
	}
	return fmt.Errorf("onedrive move %s -> %s: HTTP %d: %s", from, to, resp.StatusCode, summarizeGraphErr(raw))
}

func (o *OneDrive) delete(name string) error {
	resp, err := o.do(func(token string) (*http.Request, error) {
		req, e := http.NewRequest(http.MethodDelete, o.itemURL(name, ""), nil)
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
	return fmt.Errorf("onedrive delete %s: HTTP %d: %s", name, resp.StatusCode, summarizeGraphErr(raw))
}

// summarizeGraphErr pulls the message out of a Graph error body ({"error":
// {"code","message"}}) when present, else a trimmed snippet.
func summarizeGraphErr(raw []byte) string {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		if e.Error.Code != "" {
			return e.Error.Code + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
