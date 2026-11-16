package catalog

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// Icon images against the dev server (PROTOCOL 3.6): bob publishes a
// folder whose connection wears an uploaded image; alice syncs, fetches
// and caches it; the server's refusals carry the reasons the client
// check uses; when the record drops the image, alice's cache is pruned.
func TestIntegrationIconImages(t *testing.T) {
	base := os.Getenv("CATALOGTEST_URL")
	if base == "" {
		t.Skip("CATALOGTEST_URL not set")
	}
	bob, _ := signedIn(t, base, "bob")
	alice, _ := signedIn(t, base, "alice")
	ctx := context.Background()
	m, err := bob.Meta(ctx)
	if err != nil || !m.ImagesSupported() {
		t.Skipf("server without the image store: %v", err)
	}
	lim := m.ImageLimitsOf()

	space := newUUID(t)
	api(t, bob, "POST", "/api/v1/folders", "", map[string]any{"uuid": space, "parent_uuid": nil, "name": "Images space " + space[:8], "settings": map[string]any{}})
	api(t, bob, "PUT", "/api/v1/folders/"+space+"/acl", "0", map[string]any{"entries": []any{map[string]any{"group": "ops", "role": "read"}}})

	// A picture unique to this run, so the store does not have it yet.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i, b := range []byte(space[:8]) {
		img.Set(i, 0, color.RGBA{R: b, G: byte(i), B: 7, A: 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	pic := buf.Bytes()
	hash := ImageSHA256(pic)

	// Refusals name their reason.
	if err := bob.PutImage(ctx, hash, append(append([]byte{}, pic...), "tail"...)); ServerImageRefusal(err) == nil || ServerImageRefusal(err).Reason != "hash_mismatch" {
		t.Fatalf("wrong hash: %v", err)
	}
	tail := append(append([]byte{}, pic...), "tail"...)
	if err := bob.PutImage(ctx, ImageSHA256(tail), tail); ServerImageRefusal(err) == nil || ServerImageRefusal(err).Reason != "trailing_data" {
		t.Fatalf("trailing data: %v", err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	if err := bob.PutImage(ctx, ImageSHA256(svg), svg); ServerImageRefusal(err) == nil || ServerImageRefusal(err).Reason != "unsupported_type" {
		t.Fatalf("svg: %v", err)
	}

	bdb, err := store.Open(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	bcat, _ := bdb.CreateCatalog(store.NewCatalog{Name: "Dev", BaseURL: base})
	benv := &syncEnv{t: t, db: bdb, cat: bcat, c: bob}
	benv.sync()
	mine, _ := bdb.CreateFolder(store.NewFolder{Name: "Customer I"})
	web, _ := bdb.CreateConnection(store.NewConnection{Name: "web-i", Hostname: "web-i.example.com", FolderID: &mine.ID})
	iid, err := bdb.PutImage(pic, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := bdb.SetConnectionIcon(web.ID, iid); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPublish(bdb, PublishOptions{CatalogID: bcat.ID, RootID: mine.ID, TargetUUID: space, AccountName: "bob", Images: &lim})
	if err != nil || len(plan.SentImages()) != 1 {
		t.Fatalf("plan: %v %+v", err, plan.Images)
	}

	// Publishing before the upload: the missing image is a record error.
	if _, err := bob.Publish(ctx, &plan.Request); err == nil || len(PublishRecordErrors(err)) == 0 {
		t.Fatalf("publish without the image: %v", err)
	}
	if has, err := bob.HeadImage(ctx, hash); err != nil || has {
		t.Fatalf("head before upload: %v %v", has, err)
	}
	if err := bob.PutImage(ctx, hash, pic); err != nil {
		t.Fatal(err)
	}
	if err := bob.PutImage(ctx, hash, pic); err != nil {
		t.Fatalf("second put is not idempotent: %v", err)
	}
	resp, err := bob.Publish(ctx, &plan.Request)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompletePublish(bdb, plan, resp.Records, "bob", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := bdb.GetConnection(web.ID); c.IconImageID == nil || *c.IconImageID != iid {
		t.Fatalf("bob's row lost its image: %v", c.IconImageID)
	}

	// alice: sync, fetch, cache, materialise.
	adb, err := store.Open(filepath.Join(t.TempDir(), "alice.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer adb.Close()
	acat, _ := adb.CreateCatalog(store.NewCatalog{Name: "Dev", BaseURL: base})
	aenv := &syncEnv{t: t, db: adb, cat: acat, c: alice}
	aenv.sync()
	n, err := FetchImages(ctx, adb, acat.ID, alice)
	if err != nil || n < 1 {
		t.Fatalf("fetch: %d %v", n, err)
	}
	if err := Materialise(adb, acat.ID, nil); err != nil {
		t.Fatal(err)
	}
	uuid := ""
	for _, r := range plan.Records {
		if r.LocalID == web.ID {
			uuid = r.UUID
		}
	}
	ac := aenv.conn(uuid)
	if ac.IconImageID == nil || *ac.IconImageID != store.CatalogImagePrefix+hash {
		t.Fatalf("alice's row: %v", ac.IconImageID)
	}
	if _, data, ok, _ := adb.GetImage(*ac.IconImageID); !ok || !bytes.Equal(data, pic) {
		t.Fatal("cached bytes differ")
	}
	if n, _ := FetchImages(ctx, adb, acat.ID, alice); n != 0 {
		t.Fatalf("fetched again: %d", n)
	}

	// The record goes back to a built-in icon: alice prunes the image.
	cur := api(t, bob, "GET", "/api/v1/connections/"+uuid, "", nil)
	rev := int64(cur["rev"].(float64))
	if _, err := bob.Patch(ctx, KindConnections, uuid, rev, map[string]any{"icon_image": nil, "icon_name": "server", "icon_color": nil}); err != nil {
		t.Fatal(err)
	}
	aenv.sync()
	if err := Materialise(adb, acat.ID, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := PruneImages(adb); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if ac := aenv.conn(uuid); ac.IconImageID != nil || ac.IconName == nil || *ac.IconName != "server" {
		t.Fatalf("alice's row after the change: %v %v", ac.IconImageID, ac.IconName)
	}
}
