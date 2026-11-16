package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ssh-tool/internal/catalog"
)

// fakeImageServer: an image store that answers 429 while limited, and
// connection patches that echo the record back with the patch applied.
type fakeImageServer struct {
	mu      sync.Mutex
	limited bool
	stored  map[string]bool
	patches []map[string]any
}

func (f *fakeImageServer) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v1/meta":
		fmt.Fprintf(w, `{"protocol":{"major":1,"revision":2},"auth":{"authorization_endpoint":"http://%[1]s/a","token_endpoint":"http://%[1]s/t","client_id":"ssh-tool"},"images":{"max_bytes":262144,"max_width":1024,"max_height":1024,"types":["image/png"]}}`, r.Host)
	case strings.HasPrefix(r.URL.Path, "/api/v1/images/"):
		h := strings.TrimPrefix(r.URL.Path, "/api/v1/images/")
		switch r.Method {
		case http.MethodHead:
			if f.stored[h] {
				w.WriteHeader(200)
			} else {
				w.WriteHeader(404)
			}
		case http.MethodPut:
			_, _ = io.ReadAll(r.Body)
			if f.limited {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(429)
				return
			}
			f.stored[h] = true
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		}
	case strings.HasPrefix(r.URL.Path, "/api/v1/connections/") && r.Method == http.MethodPatch:
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.patches = append(f.patches, p)
		rec := map[string]any{"uuid": "c-uuid", "rev": 2, "folder_uuid": "f-uuid", "name": "web-1", "hostname": "web-1.example.com", "settings": map[string]any{}}
		for k, v := range p {
			rec[k] = v
		}
		json.NewEncoder(w).Encode(map[string]any{"record": rec})
	default:
		w.WriteHeader(404)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The upload limit at publish: the publish waits briefly, then goes
// without the image and marks it; the background job uploads it once the
// limit lifts, sets icon_image on the record (built-in fields cleared in
// the same patch) and drops the mark and the layer's copy.
func TestCatalogIconUploadsAfterTheLimit(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	fs := &fakeImageServer{limited: true, stored: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(fs.handler))
	defer srv.Close()
	c, err := catalog.NewClient(srv.URL, "test", vaultTokens{a: a, id: ca.cat.ID})
	if err != nil {
		t.Fatal(err)
	}
	c.SetAccessToken("tok", time.Now().Add(time.Hour))
	a.catRT().clients[ca.cat.ID] = c
	cat := *ca.cat
	cat.BaseURL = srv.URL

	pic := testPNG(t)
	img, err := a.db.PutImage(pic, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	hash := catalog.ImageSHA256(pic)

	// Publish side: a plan with one image, limited past the wait.
	saved := publishImageWait
	publishImageWait = 200 * time.Millisecond
	defer func() { publishImageWait = saved }()
	ref := "sha256:" + hash
	plan := &catalog.PublishPlan{Layers: map[string]catalog.PlanLayer{},
		Records: []catalog.PlanRecord{{Kind: catalog.KindConnections, UUID: "c-uuid", LocalID: "cc", Name: "web-1"}},
		Images:  []catalog.PlanImage{{SHA256: hash, ImageID: img, Records: []string{"web-1"}}}}
	plan.Request.Connections = []catalog.PublishConnection{{UUID: "c-uuid", Name: "web-1", IconImage: &ref}}
	catalog.SetPlanImageUses(plan, hash, catalog.KindConnections, "c-uuid")
	if err := a.uploadPublishImages(context.Background(), cat.ID, c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.Request.Connections[0].IconImage != nil || !plan.Images[0].Later {
		t.Fatalf("limited image still sent: %+v", plan.Images[0])
	}
	if err := a.db.SetCatalogOverride(cat.ID, "c-uuid", "connections", plan.Layers["c-uuid"].JSON); err != nil {
		t.Fatal(err)
	}
	pend, _ := a.pendingIcons(cat.ID)
	if len(pend) != 1 || pend[0].imageID != img {
		t.Fatalf("pending: %+v", pend)
	}

	// Background: the limit lifts.
	fs.mu.Lock()
	fs.limited = false
	fs.mu.Unlock()
	a.runIconUploads(context.Background(), &cat, c)
	if !fs.stored[hash] || len(fs.patches) != 1 {
		t.Fatalf("stored %v patches %v", fs.stored, fs.patches)
	}
	p := fs.patches[0]
	if p["icon_image"] != ref || p["icon_name"] != nil || p["icon_color"] != nil {
		t.Fatalf("patch: %v", p)
	}
	if _, has := p["icon_name"]; !has {
		t.Fatal("patch does not clear icon_name")
	}
	if pend, _ := a.pendingIcons(cat.ID); len(pend) != 0 {
		t.Fatalf("still pending: %+v", pend)
	}
	if strings.Contains(a.catalogLayerJSON(cat.ID, "c-uuid"), img) {
		t.Fatalf("layer still keeps the image: %s", a.catalogLayerJSON(cat.ID, "c-uuid"))
	}
	conn, _ := a.db.GetConnection("cc")
	if conn.IconImageID == nil || *conn.IconImageID != img {
		t.Fatalf("row icon: %v", conn.IconImageID)
	}

	// Stopping: a cancelled job returns at once while limited.
	fs.mu.Lock()
	fs.limited = true
	fs.mu.Unlock()
	if err := a.db.SetCatalogOverride(cat.ID, "c-uuid", "connections", `{"_record":{"icon_image_id":"`+img+`","icon_pending":true}}`); err != nil {
		t.Fatal(err)
	}
	delete(fs.stored, hash)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.runIconUploads(ctx, &cat, c); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled upload did not stop")
	}
}
