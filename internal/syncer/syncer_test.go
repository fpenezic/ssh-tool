package syncer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ssh-tool/internal/store"
)

// fakeDAV is an in-memory WebDAV-enough server: GET/PUT/MKCOL/MOVE
// with basic auth. moveBroken simulates minimal servers without MOVE.
type fakeDAV struct {
	mu         sync.Mutex
	files      map[string][]byte
	moveBroken bool
}

func (f *fakeDAV) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "u" || pass != "p" {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/dav/")
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(201)
		case "GET":
			b, ok := f.files[name]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(b)
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			f.files[name] = b
			w.WriteHeader(201)
		case "MOVE":
			if f.moveBroken {
				w.WriteHeader(405)
				return
			}
			dst := r.Header.Get("Destination")
			i := strings.Index(dst, "/dav/")
			if i < 0 {
				w.WriteHeader(400)
				return
			}
			dstName := dst[i+len("/dav/"):]
			b, ok := f.files[name]
			if !ok {
				w.WriteHeader(404)
				return
			}
			f.files[dstName] = b
			delete(f.files, name)
			w.WriteHeader(201)
		default:
			w.WriteHeader(405)
		}
	})
}

func newEnv(t *testing.T) (dav *WebDAV, fake *fakeDAV, dataDir string) {
	t.Helper()
	fake = &fakeDAV{files: map[string][]byte{}}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	dav = &WebDAV{BaseURL: srv.URL + "/dav/", Username: "u", Password: "p"}

	dataDir = t.TempDir()
	db, err := store.Open(filepath.Join(dataDir, "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := db.CreateConnection(store.NewConnection{Name: "web-01", Hostname: "h"}); err != nil {
		t.Fatalf("seed conn: %v", err)
	}
	_ = db.Close()
	if err := os.WriteFile(filepath.Join(dataDir, "vault.enc"), []byte("sealed-vault-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dav, fake, dataDir
}

func TestPushPullRoundTrip(t *testing.T) {
	dav, fake, dataDir := newEnv(t)
	storePath := filepath.Join(dataDir, "store.db")
	vaultPath := filepath.Join(dataDir, "vault.enc")

	res, err := Push(dav, storePath, vaultPath, "pw", "test", "laptop", 0, false, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Generation != 1 {
		t.Fatalf("first push should be gen 1, got %d", res.Generation)
	}
	if _, ok := fake.files[snapshotName]; !ok {
		t.Fatalf("snapshot missing on server: %v", keys(fake.files))
	}
	// Server must hold ciphertext only - the connection name from the
	// seeded store must not appear anywhere in the uploaded bytes.
	if strings.Contains(string(fake.files[snapshotName]), "web-01") {
		t.Fatalf("snapshot leaks plaintext")
	}
	if strings.Contains(string(fake.files[snapshotName]), "sealed-vault-bytes") {
		t.Fatalf("snapshot leaks vault bytes verbatim (not sealed)")
	}

	meta, err := FetchMeta(dav)
	if err != nil || meta.Generation != 1 || meta.Device != "laptop" {
		t.Fatalf("meta: %+v %v", meta, err)
	}

	// Pull onto a "second machine" (fresh data dir, no vault yet).
	dataDir2 := t.TempDir()
	store2 := filepath.Join(dataDir2, "store.db")
	vault2 := filepath.Join(dataDir2, "vault.enc")
	db2, err := store.Open(store2)
	if err != nil {
		t.Fatal(err)
	}
	_ = db2.Close()

	pr, err := Pull(dav, "pw", store2, vault2)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pr.Generation != 1 || pr.Device != "laptop" {
		t.Fatalf("pull result: %+v", pr)
	}
	// Staged, not applied: pending-restore dir with READY flag.
	pending := filepath.Join(dataDir2, "pending-restore")
	if _, err := os.Stat(filepath.Join(pending, "READY")); err != nil {
		t.Fatalf("restore not staged: %v", err)
	}
}

func TestPushGenerationGuard(t *testing.T) {
	dav, _, dataDir := newEnv(t)
	storePath := filepath.Join(dataDir, "store.db")
	vaultPath := filepath.Join(dataDir, "vault.enc")

	if _, err := Push(dav, storePath, vaultPath, "pw", "test", "laptop", 0, false, nil); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	// Second machine that never pulled (knownGen 0) must be refused.
	if _, err := Push(dav, storePath, vaultPath, "pw", "test", "desktop", 0, false, nil); err == nil {
		t.Fatalf("push over unseen remote changes should fail")
	}
	// Force overwrites and bumps past the remote generation.
	res, err := Push(dav, storePath, vaultPath, "pw", "test", "desktop", 0, true, nil)
	if err != nil {
		t.Fatalf("force push: %v", err)
	}
	if res.Generation != 2 {
		t.Fatalf("force push should land gen 2, got %d", res.Generation)
	}
	// In-sync machine pushes fine.
	if _, err := Push(dav, storePath, vaultPath, "pw", "test", "desktop", 2, false, nil); err != nil {
		t.Fatalf("in-sync push: %v", err)
	}
}

func TestPushMoveFallback(t *testing.T) {
	dav, fake, dataDir := newEnv(t)
	fake.moveBroken = true
	res, err := Push(dav, filepath.Join(dataDir, "store.db"), filepath.Join(dataDir, "vault.enc"), "pw", "test", "laptop", 0, false, nil)
	if err != nil {
		t.Fatalf("push without MOVE support: %v", err)
	}
	if res.Generation != 1 {
		t.Fatalf("gen: %d", res.Generation)
	}
	if _, ok := fake.files[snapshotName]; !ok {
		t.Fatalf("snapshot missing after PUT fallback")
	}
}

func TestPullWrongPassphrase(t *testing.T) {
	dav, _, dataDir := newEnv(t)
	storePath := filepath.Join(dataDir, "store.db")
	vaultPath := filepath.Join(dataDir, "vault.enc")
	if _, err := Push(dav, storePath, vaultPath, "pw", "test", "laptop", 0, false, nil); err != nil {
		t.Fatal(err)
	}
	dataDir2 := t.TempDir()
	db2, _ := store.Open(filepath.Join(dataDir2, "store.db"))
	_ = db2.Close()
	if _, err := Pull(dav, "WRONG", filepath.Join(dataDir2, "store.db"), filepath.Join(dataDir2, "vault.enc")); err == nil {
		t.Fatalf("wrong passphrase must fail")
	}
}

func TestPullEmptyRemote(t *testing.T) {
	dav, _, dataDir := newEnv(t)
	if _, err := Pull(dav, "pw", filepath.Join(dataDir, "store.db"), filepath.Join(dataDir, "vault.enc")); err == nil {
		t.Fatalf("pull from empty dir must error cleanly")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestValidateURL(t *testing.T) {
	ok := []string{
		"https://cloud.example.com/dav/",
		"http://localhost:8080/dav/",
		"http://127.0.0.1/dav/",
		"http://[::1]:9000/x",
	}
	for _, u := range ok {
		if err := ValidateURL(u); err != nil {
			t.Errorf("ValidateURL(%q) should pass: %v", u, err)
		}
	}
	bad := []string{
		"http://cloud.example.com/dav/", // basic auth in plaintext
		"http://192.168.1.10/dav/",      // LAN is still a network
		"ftp://x/",
		"cloud.example.com/dav",
	}
	for _, u := range bad {
		if err := ValidateURL(u); err == nil {
			t.Errorf("ValidateURL(%q) should fail", u)
		}
	}
}
