package syncer

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- OneDrive (Graph, path-addressed) ---

func newTestOneDrive(srvURL string, refresh func() (string, error)) *OneDrive {
	o := NewOneDrive("access-token-0", refresh)
	o.graphBase = srvURL
	return o
}

func TestOneDriveGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"itemNotFound","message":"not found"}}`)
	}))
	defer srv.Close()

	o := newTestOneDrive(srv.URL, nil)
	if _, err := o.Get("meta.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOneDrivePutGetRoundTrip(t *testing.T) {
	store := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// approot:/<name>:/content -> extract <name> between "approot:/" and ":".
		path := r.URL.Path
		i := strings.Index(path, "approot:/")
		if i < 0 {
			t.Fatalf("unexpected path %s", path)
		}
		rest := path[i+len("approot:/"):]
		name := strings.TrimSuffix(rest, "/content")
		name = strings.TrimSuffix(name, ":")
		switch r.Method {
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			store[name] = data
			io.WriteString(w, `{"id":"x"}`)
		case http.MethodGet:
			data, ok := store[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(data)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	o := newTestOneDrive(srv.URL, nil)
	want := []byte("sealed-onedrive")
	if err := o.Put("snapshot.stb", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := o.Get("snapshot.stb")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round-trip mismatch: %q vs %q", got, want)
	}
}

func TestOneDriveRefreshOn401(t *testing.T) {
	calls, refreshed := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer access-token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"id":"ok"}`)
	}))
	defer srv.Close()

	o := newTestOneDrive(srv.URL, func() (string, error) { refreshed = true; return "access-token-1", nil })
	if err := o.Put("snapshot.stb", []byte("x")); err != nil {
		t.Fatalf("Put after refresh: %v", err)
	}
	if !refreshed || calls != 2 {
		t.Fatalf("expected refresh + retry: refreshed=%v calls=%d", refreshed, calls)
	}
}

// --- Google Drive (id-addressed via appDataFolder) ---

func newTestGDrive(apiURL, uploadURL string, refresh func() (string, error)) *GoogleDrive {
	g := NewGoogleDrive("access-token-0", refresh)
	g.apiBase = apiURL
	g.uploadBase = uploadURL
	return g
}

func TestGDriveGetNotFound(t *testing.T) {
	// List returns no files -> Get should map to ErrNotFound without a media fetch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files") && r.Method == http.MethodGet && r.URL.Query().Get("q") != "" {
			io.WriteString(w, `{"files":[]}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL)
	}))
	defer srv.Close()

	g := newTestGDrive(srv.URL, srv.URL, nil)
	if _, err := g.Get("meta.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGDrivePutGetRoundTrip(t *testing.T) {
	// Minimal Drive emulation: name->id + id->content maps.
	nameToID := map[string]string{}
	idToContent := map[string][]byte{}
	nextID := 100

	handler := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case strings.HasPrefix(r.URL.Path, "/files") && r.Method == http.MethodGet && q.Get("q") != "":
			// list by name
			name := parseGDriveName(q.Get("q"))
			id, ok := nameToID[name]
			if !ok {
				io.WriteString(w, `{"files":[]}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"files": []map[string]string{{"id": id, "name": name}}})
		case strings.HasPrefix(r.URL.Path, "/files/") && r.Method == http.MethodGet && q.Get("alt") == "media":
			id := strings.TrimPrefix(r.URL.Path, "/files/")
			w.Write(idToContent[id])
		case strings.HasPrefix(r.URL.Path, "/files") && r.Method == http.MethodPost && q.Get("uploadType") == "multipart":
			body, _ := io.ReadAll(r.Body)
			name := parseMultipartName(body)
			id := "id" + itoa(nextID)
			nextID++
			nameToID[name] = id
			idToContent[id] = extractMultipartContent(body)
			io.WriteString(w, `{"id":"`+id+`"}`)
		case strings.HasPrefix(r.URL.Path, "/files/") && r.Method == http.MethodPatch && q.Get("uploadType") == "media":
			id := strings.TrimPrefix(r.URL.Path, "/files/")
			data, _ := io.ReadAll(r.Body)
			idToContent[id] = data
			io.WriteString(w, `{"id":"`+id+`"}`)
		default:
			t.Fatalf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	g := newTestGDrive(srv.URL, srv.URL, nil)
	want := []byte("sealed-gdrive")
	if err := g.Put("snapshot.stb", want); err != nil {
		t.Fatalf("Put(create): %v", err)
	}
	got, err := g.Get("snapshot.stb")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round-trip mismatch: %q vs %q", got, want)
	}
	// Second Put should update in place (same id), not create a duplicate.
	want2 := []byte("sealed-gdrive-v2")
	if err := g.Put("snapshot.stb", want2); err != nil {
		t.Fatalf("Put(update): %v", err)
	}
	if len(nameToID) != 1 {
		t.Fatalf("update created a duplicate: %d names", len(nameToID))
	}
	got2, _ := g.Get("snapshot.stb")
	if string(got2) != string(want2) {
		t.Fatalf("update not applied: %q", got2)
	}
}

// small test helpers (avoid pulling strconv/mime into the test's hot path)

func parseGDriveName(q string) string {
	// q looks like: name = 'meta.json'
	i := strings.Index(q, "'")
	j := strings.LastIndex(q, "'")
	if i < 0 || j <= i {
		return ""
	}
	return q[i+1 : j]
}

func parseMultipartName(body []byte) string {
	var meta struct {
		Name string `json:"name"`
	}
	// The first JSON object in the multipart body carries the metadata.
	start := strings.Index(string(body), "{")
	end := strings.Index(string(body), "}")
	if start < 0 || end <= start {
		return ""
	}
	json.Unmarshal(body[start:end+1], &meta)
	return meta.Name
}

func extractMultipartContent(body []byte) []byte {
	// Content follows the last blank line before the closing boundary.
	s := string(body)
	marker := "application/octet-stream\r\n\r\n"
	i := strings.Index(s, marker)
	if i < 0 {
		return nil
	}
	rest := s[i+len(marker):]
	if j := strings.LastIndex(rest, "\r\n--"); j >= 0 {
		rest = rest[:j]
	}
	return []byte(rest)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
