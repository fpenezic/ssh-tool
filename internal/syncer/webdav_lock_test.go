package syncer

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Nextcloud answers 423 while another client writes the file; callers
// retry on ErrLocked instead of logging every collision.
func TestWebDAVLockedIsErrLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusLocked)
	}))
	defer srv.Close()
	w := &WebDAV{BaseURL: srv.URL}
	if _, err := w.Get("presence.json"); !errors.Is(err, ErrLocked) {
		t.Fatalf("GET: %v", err)
	}
	if err := w.Put("presence.json", []byte("{}")); !errors.Is(err, ErrLocked) {
		t.Fatalf("PUT: %v", err)
	}
}
