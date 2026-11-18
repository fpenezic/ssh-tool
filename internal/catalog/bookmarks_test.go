package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"ssh-tool/internal/store"
)

func TestSharedBookmarks(t *testing.T) {
	got := SharedBookmarks([]store.ProxyBookmark{
		{Name: " ui ", URL: " https://grafana.example.com/d/1 ", InTree: true},
		{Name: "", URL: "https://a.example.com"},
		{Name: "port", URL: "http://127.0.0.1:{port}/"},
		{Name: "js", URL: "javascript:alert(1)"},
		{Name: "file", URL: "file:///etc/passwd"},
		{Name: "nohost", URL: "http:///x"},
		{Name: strings.Repeat("n", 201), URL: "https://b.example.com"},
	}, 9)
	b, _ := json.Marshal(got)
	if string(b) != `[{"name":"ui","url":"https://grafana.example.com/d/1"},{"name":"port","url":"http://127.0.0.1:{port}/"}]` {
		t.Fatalf("shared %s", b)
	}
	// {port}: from revision 9, once, as the authority's port only.
	for u, want := range map[string]bool{
		"http://127.0.0.1:{port}/admin":       true,
		"http://[::1]:{port}":                 true,
		"https://host.example.com:{port}#x":   true,
		"http://{port}/":                      false,
		"http://127.0.0.1:8080/{port}":        false,
		"http://127.0.0.1:8080/?p={port}":     false,
		"http://127.0.0.1:{port}/{port}":      false,
		"http://127.0.0.1:{port}x/":           false,
		"javascript://127.0.0.1:{port}/alert": false,
	} {
		if got := len(SharedBookmarks([]store.ProxyBookmark{{Name: "n", URL: u}}, 9)) == 1; got != want {
			t.Errorf("rev 9 %s: kept %v", u, got)
		}
		if len(SharedBookmarks([]store.ProxyBookmark{{Name: "n", URL: u}}, 8)) != 0 {
			t.Errorf("rev 8 kept %s", u)
		}
	}
	if SharedBookmarks(nil, 6) == nil {
		t.Fatal("nil instead of an empty list")
	}
	var many []store.ProxyBookmark
	for i := 0; i < 60; i++ {
		many = append(many, store.ProxyBookmark{Name: "x", URL: "https://x.example.com"})
	}
	if n := len(SharedBookmarks(many, 6)); n != 50 {
		t.Fatalf("%d kept", n)
	}
}

// A forward record's shared bookmarks: unchanged (absent or empty) is no
// edit, an edit is a whole-array patch without in_tree, an invalid one or
// an older server is refused.
func TestPlanEditForwardBookmarks(t *testing.T) {
	e := newEditEnv(t)
	raw := json.RawMessage(`{"uuid":"p1","rev":2,"connection_uuid":"c1","kind":"dynamic","local_port":1080,"description":"socks"}`)
	form, err := FormOf(e.db, e.cat.ID, KindForwards, raw)
	if err != nil {
		t.Fatal(err)
	}
	if form.Bookmarks == nil {
		t.Fatal("absent bookmarks read as nil")
	}
	plan := func(rev int64, edit func(*EditForm)) *EditPlan {
		t.Helper()
		edited := form
		edited.Bookmarks = append([]store.ProxyBookmark(nil), form.Bookmarks...)
		edit(&edited)
		cat := *e.cat
		cat.ProtocolRevision = rev
		p, err := PlanEdit(e.db, &cat, e.scope, EditInput{Kind: KindForwards, LocalID: "x", Rev: 2, Loaded: form, Edited: edited}, raw, 2)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := plan(6, func(f *EditForm) { f.Bookmarks = nil }); len(p.Changed) != 0 {
		t.Fatalf("nil vs empty is an edit: %+v", p.Changed)
	}
	add := func(f *EditForm) {
		f.Bookmarks = []store.ProxyBookmark{{Name: "grafana", URL: "https://grafana.example.com", InTree: true}}
	}
	p := plan(6, add)
	b, _ := json.Marshal(p.Patch)
	if len(p.Refusals) != 0 || string(b) != `{"bookmarks":[{"name":"grafana","url":"https://grafana.example.com"}]}` {
		t.Fatalf("patch %s refusals %v", b, p.Refusals)
	}
	if p := plan(5, add); len(p.Refusals) == 0 || p.Patch != nil {
		t.Fatalf("revision 5 took bookmarks: %+v", p)
	}
	port := func(f *EditForm) {
		f.Bookmarks = []store.ProxyBookmark{{Name: "local", URL: "http://127.0.0.1:{port}/"}}
	}
	if p := plan(8, port); len(p.Refusals) == 0 || !strings.Contains(p.Refusals[0], "revision 9") {
		t.Fatalf("a {port} bookmark below revision 9: %+v", p.Refusals)
	}
	if p := plan(9, port); len(p.Refusals) != 0 || p.Patch == nil {
		t.Fatalf("a {port} bookmark at revision 9: %+v", p.Refusals)
	}
	if p := plan(9, func(f *EditForm) {
		f.Bookmarks = []store.ProxyBookmark{{Name: "bad", URL: "http://127.0.0.1:8080/{port}"}}
	}); len(p.Refusals) == 0 || !strings.Contains(p.Refusals[0], "only once, as the port") {
		t.Fatalf("{port} in the path: %+v", p.Refusals)
	}
}

// Publishing sends a forward's bookmarks only from revision 6 and only
// when the "bookmarks" category is ticked; in_tree never goes.
func TestPublishForwardBookmarks(t *testing.T) {
	e := newSyncEnv(t)
	x := publishFixture(t, e.db)
	if err := e.db.SetPortForwardBookmarks(x.fwd, []store.ProxyBookmark{{Name: "ui", URL: "http://pg-ui.example.com/", InTree: true}}); err != nil {
		t.Fatal(err)
	}
	bookmarksOf := func(opts PublishOptions) ([]Bookmark, *PublishPlan) {
		t.Helper()
		opts.CatalogID, opts.RootID, opts.AccountName = e.cat.ID, x.root, "bob"
		plan, err := BuildPublish(e.db, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Request.Forwards) != 1 {
			t.Fatalf("%d forwards", len(plan.Request.Forwards))
		}
		return plan.Request.Forwards[0].Bookmarks, plan
	}
	if bs, _ := bookmarksOf(PublishOptions{Revision: 6, Unsent: map[string]bool{"bookmarks": true}}); len(bs) != 0 {
		t.Fatalf("unticked: %+v", bs)
	}
	if bs, _ := bookmarksOf(PublishOptions{Revision: 5}); len(bs) != 0 {
		t.Fatalf("revision 5: %+v", bs)
	}
	bs, plan := bookmarksOf(PublishOptions{Revision: 6})
	body, _ := json.Marshal(plan.Request.Forwards[0])
	if len(bs) != 1 || bs[0].URL != "http://pg-ui.example.com/" || strings.Contains(string(body), "in_tree") {
		t.Fatalf("sent %s", body)
	}
	found := false
	for _, ft := range plan.FreeText {
		if ft.Value == "http://pg-ui.example.com/" {
			found = true
		}
	}
	if !found {
		t.Fatal("the bookmark URL is not in the free-text review")
	}
}
