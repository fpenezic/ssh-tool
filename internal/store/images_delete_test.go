package store

import "testing"

// A used icon can be deleted: the rows wearing it fall back to the default
// icon instead of the delete failing on the foreign key.
func TestDeleteImageClearsItsUsers(t *testing.T) {
	db := iconHintDB(t)
	img, err := db.PutImage([]byte("<svg/>"), "image/svg+xml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := db.CreateFolder(NewFolder{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CreateConnection(NewConnection{Name: "web-01", Hostname: "web-01.example.com", FolderID: &f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetFolderIcon(f.ID, img); err != nil {
		t.Fatal(err)
	}
	if err := db.SetConnectionIcon(c.ID, img); err != nil {
		t.Fatal(err)
	}

	n, err := db.DeleteImage(img)
	if err != nil {
		t.Fatalf("delete a used image: %v", err)
	}
	if n != 2 {
		t.Fatalf("cleared %d rows, want 2", n)
	}
	if gf, _ := db.GetFolder(f.ID); gf.IconImageID != nil {
		t.Fatalf("folder still points at the deleted image")
	}
	if gc, _ := db.GetConnection(c.ID); gc.IconImageID != nil {
		t.Fatalf("connection still points at the deleted image")
	}
	if _, _, ok, _ := db.GetImage(img); ok {
		t.Fatalf("image still in the store")
	}
	if _, err := db.DeleteImage(img); err == nil {
		t.Fatalf("deleting a missing image succeeded")
	}
}

func TestDeleteUnusedImagesKeepsUsedOnes(t *testing.T) {
	db := iconHintDB(t)
	used, _ := db.PutImage([]byte("<svg id='a'/>"), "image/svg+xml")
	spare1, _ := db.PutImage([]byte("<svg id='b'/>"), "image/svg+xml")
	spare2, _ := db.PutImage([]byte("<svg id='c'/>"), "image/svg+xml")
	f, err := db.CreateFolder(NewFolder{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetFolderIcon(f.ID, used); err != nil {
		t.Fatal(err)
	}

	n, err := db.DeleteUnusedImages()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	if _, _, ok, _ := db.GetImage(used); !ok {
		t.Fatalf("the used image was removed")
	}
	for _, id := range []string{spare1, spare2} {
		if _, _, ok, _ := db.GetImage(id); ok {
			t.Fatalf("unused image %s survived", id)
		}
	}
}
