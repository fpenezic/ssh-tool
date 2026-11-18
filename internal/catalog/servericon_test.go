package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"ssh-tool/internal/store"
)

// The catalog's own icon: fetched without a token only when its hash
// changes, kept as the root folder's image, refused when the bytes are
// not an image or not what meta named, and dropped when the server has
// none (or predates revision 5).
func TestSyncServerIcon(t *testing.T) {
	icon := pngOf(t, 16, 16)
	html := []byte("<html><script>alert(1)</script></html>")
	var serve atomic.Value // []byte, nil = no icon
	serve.Store(icon)
	var named atomic.Value // what meta names; "" = null, "-" = no field
	named.Store("sha256:" + ImageSHA256(icon))
	var fetches, authed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/meta":
			field := ""
			switch v := named.Load().(string); v {
			case "-":
			case "":
				field = `,"icon":null`
			default:
				field = fmt.Sprintf(`,"icon":%q`, v)
			}
			fmt.Fprintf(w, `{"server":{"name":"Fake"%s},"protocol":{"major":1,"revision":5},"auth":{"authorization_endpoint":"http://%[2]s/a","token_endpoint":"http://%[2]s/t","client_id":"ssh-tool"}}`, field, r.Host)
		case "/api/v1/server-icon":
			fetches.Add(1)
			if r.Header.Get("Authorization") != "" {
				authed.Add(1)
			}
			b, _ := serve.Load().([]byte)
			if b == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(b)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, err := db.CreateCatalog(store.NewCatalog{Name: "Team", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(srv.URL, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rootImage := func() string {
		f, err := db.GetFolder(cat.RootFolderID)
		if err != nil {
			t.Fatal(err)
		}
		if f.IconImageID == nil {
			return ""
		}
		return *f.IconImageID
	}

	if changed, err := SyncServerIcon(ctx, db, cat, c); err != nil || !changed || rootImage() == "" {
		t.Fatalf("first sync: changed %v err %v image %q", changed, err, rootImage())
	}
	if changed, _ := SyncServerIcon(ctx, db, cat, c); changed || fetches.Load() != 1 {
		t.Fatalf("same hash: changed %v, fetched %d times", changed, fetches.Load())
	}
	if authed.Load() != 0 {
		t.Fatal("the icon request carried a token")
	}

	// Not an image, served under an address that matches it: refused.
	serve.Store(html)
	named.Store("sha256:" + ImageSHA256(html))
	if changed, err := SyncServerIcon(ctx, db, cat, c); err != nil || changed {
		t.Fatalf("html: changed %v err %v", changed, err)
	}
	// Bytes that do not match what meta named: refused.
	other := pngOf(t, 8, 8)
	serve.Store(other)
	named.Store("sha256:" + ImageSHA256(icon[:len(icon)-1]))
	if changed, _ := SyncServerIcon(ctx, db, cat, c); changed {
		t.Fatal("mismatched bytes were used")
	}

	// An older server (no field) or no icon: dropped.
	named.Store("-")
	if changed, err := SyncServerIcon(ctx, db, cat, c); err != nil || !changed || rootImage() != "" {
		t.Fatalf("dropped: changed %v err %v image %q", changed, err, rootImage())
	}
	named.Store("")
	if changed, _ := SyncServerIcon(ctx, db, cat, c); changed {
		t.Fatal("null after none changed something")
	}
}
