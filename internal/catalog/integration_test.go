package catalog

// Against a real catalog server in development mode. Skipped unless
// CATALOGTEST_URL names one, started with development users alice (group
// "ops") and bob (admin), passwords equal to the names, for example (in the
// catalog server's repository, on a scratch DATA_DIR):
//
//	LISTEN_ADDR=127.0.0.1:18080 BASE_URL=http://127.0.0.1:18080 DATA_DIR=<scratch> \
//	  ADMIN_USER=admin ADMIN_PASSWORD=admin go run . -dev -dev-users <users.json>
//
// The sign-in is the real browser flow with the browser played by an HTTP
// client: GET the authorize URL, POST the local sign-in form, follow the
// redirect to the loopback listener.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/store"
)

var reqField = regexp.MustCompile(`name="req" value="([^"]+)"`)

// browserLogin returns an openBrowser func that signs in as user.
func browserLogin(t *testing.T, user string) func(string) {
	return func(authURL string) {
		go func() {
			jar, _ := cookiejar.New(nil)
			hc := &http.Client{Jar: jar, Timeout: 30 * time.Second}
			resp, err := hc.Get(authURL)
			if err != nil {
				t.Errorf("authorize: %v", err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			m := reqField.FindSubmatch(body)
			if m == nil {
				t.Errorf("no sign-in form in %s", body)
				return
			}
			u, _ := url.Parse(authURL)
			form := url.Values{"req": {string(m[1])}, "user": {user}, "password": {user}}
			resp, err = hc.PostForm(u.Scheme+"://"+u.Host+"/oauth/login/local", form)
			if err != nil {
				t.Errorf("sign-in form: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
}

func signedIn(t *testing.T, base, user string) (*Client, *memTokens) {
	toks := &memTokens{}
	c, err := NewClient(base, "ssh-tool/test", toks)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.SignIn(ctx, browserLogin(t, user)); err != nil {
		t.Fatalf("sign in as %s: %v", user, err)
	}
	return c, toks
}

// api is an authorized call for seeding (the client's writes come in a
// later phase). ifMatch "" sends none.
func api(t *testing.T, c *Client, method, path, ifMatch string, body any) map[string]any {
	t.Helper()
	tok, _, err := c.token(context.Background(), -1)
	if err != nil {
		t.Fatal(err)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.BaseURL+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		ct := "application/json"
		if method == http.MethodPatch {
			ct = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func newUUID(t *testing.T) string {
	b := make([]byte, 16)
	f, _ := os.Open("/dev/urandom")
	defer f.Close()
	_, _ = io.ReadFull(f, b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestIntegrationDevServer(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	bob, _ := signedIn(t, base, "bob")
	alice, aliceTokens := signedIn(t, base, "alice")

	w, err := alice.WhoAmI(context.Background())
	if err != nil || w.Name == "" {
		t.Fatalf("whoami: %+v %v", w, err)
	}
	t.Logf("alice: subject %s groups %v", w.Subject, w.Groups)

	// bob (admin) builds a space with a folder alice's group may read, a
	// slot on the space, a connection and a forward.
	space, folder, slot, conn, fwd := newUUID(t), newUUID(t), newUUID(t), newUUID(t), newUUID(t)
	api(t, bob, "POST", "/api/v1/folders", "", map[string]any{"uuid": space, "parent_uuid": nil, "name": "IT space " + space[:8], "settings": map[string]any{"username": "deploy"}})
	api(t, bob, "POST", "/api/v1/slots", "", map[string]any{"uuid": slot, "folder_uuid": space, "type": "cred", "name": "admin-" + slot[:8], "kind": "slot", "doc_md": "Ask ops."})
	api(t, bob, "POST", "/api/v1/folders", "", map[string]any{"uuid": folder, "parent_uuid": space, "name": "Customer A", "settings": map[string]any{"auth_ref": slot}})
	api(t, bob, "POST", "/api/v1/connections", "", map[string]any{"uuid": conn, "folder_uuid": folder, "name": "web-1", "hostname": "web-1.example.com", "settings": map[string]any{"port": 2222}})
	api(t, bob, "POST", "/api/v1/forwards", "", map[string]any{"uuid": fwd, "connection_uuid": conn, "kind": "local", "local_port": 15432, "remote_host": "127.0.0.1", "remote_port": 5432, "auto_start": true})
	api(t, bob, "PUT", "/api/v1/folders/"+folder+"/acl", "0", map[string]any{"entries": []any{map[string]any{"group": "ops", "role": "read"}}})

	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, err := db.CreateCatalog(store.NewCatalog{Name: "Dev", BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	env := &syncEnv{t: t, db: db, cat: cat, c: alice}

	// First sync: a snapshot with the space as a skeleton, the folder, the
	// connection, the forward and the space's slot.
	res := env.sync()
	t.Logf("first sync: %+v", res)
	if !res.Reset {
		t.Error("first sync was not a snapshot")
	}
	sp, fo := env.folder(space), env.folder(folder)
	if sp == nil || fo == nil || *fo.ParentID != sp.ID || *sp.ParentID != cat.RootFolderID {
		t.Fatalf("folders: %+v %+v", sp, fo)
	}
	var raw string
	_ = db.Conn().QueryRow(`SELECT catalog_json FROM folders WHERE id = ?`, sp.ID).Scan(&raw)
	var spRec Folder
	_ = json.Unmarshal([]byte(raw), &spRec)
	if !spRec.Skeleton() || spRec.DocMD != nil {
		t.Errorf("space should reach alice as a skeleton: %s", raw)
	}
	c := env.conn(conn)
	if c == nil || *c.FolderID != fo.ID {
		t.Fatalf("connection: %+v", c)
	}
	if f := env.fwd(fwd); f == nil || f.AutoStart {
		t.Fatalf("forward (auto start must stay off): %+v", f)
	}
	// Other spaces of earlier runs may be readable too: look for ours.
	slots, _ := db.ListCatalogSlots(cat.ID)
	var got *store.CatalogSlot
	for i := range slots {
		if slots[i].UUID == slot {
			got = &slots[i]
		}
	}
	if got == nil || got.DocMD != "Ask ops." {
		t.Fatalf("skeleton's slot: %+v", slots)
	}

	// Incremental: a rename and a deleted forward.
	api(t, bob, "PATCH", "/api/v1/connections/"+conn, "1", map[string]any{"name": "web-1-renamed"})
	api(t, bob, "DELETE", "/api/v1/forwards/"+fwd, "1", nil)
	res = env.sync()
	t.Logf("incremental sync: %+v", res)
	if res.Reset {
		t.Error("incremental sync came back as a reset")
	}
	if c2 := env.conn(conn); c2 == nil || c2.Name != "web-1-renamed" || c2.ID != c.ID {
		t.Fatalf("rename: %+v", c2)
	}
	if env.fwd(fwd) != nil {
		t.Error("deleted forward still held")
	}

	// Access lost: the grant goes, the next sync is a reset and the rows
	// leave (no local data on them).
	api(t, bob, "PUT", "/api/v1/folders/"+folder+"/acl", "1", map[string]any{"entries": []any{}})
	res = env.sync()
	t.Logf("after the grant was removed: %+v", res)
	if !res.Reset {
		t.Error("a grant change did not reset the feed")
	}
	if env.conn(conn) != nil || env.folder(folder) != nil || env.folder(space) != nil {
		t.Error("rows the user lost access to are still held")
	}

	// Refresh rotation: a reused refresh token ends the session.
	old, _ := aliceTokens.Load()
	alice.SetAccessToken("", time.Time{})
	if _, err := alice.WhoAmI(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	reuse, _ := NewClient(base, "ssh-tool/test", &memTokens{rt: old})
	if _, err := reuse.WhoAmI(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("reused refresh token accepted: %v", err)
	}
	alice.SetAccessToken("", time.Time{})
	if _, err := alice.WhoAmI(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("session survived a refresh token reuse: %v", err)
	}
}
