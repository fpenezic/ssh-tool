package catalog

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"ssh-tool/internal/store"
)

// FetchImagesPerPass bounds the downloads of one sync pass; the rest come
// with the next passes.
const FetchImagesPerPass = 40

// FetchImages downloads the icon images this catalog's records name and
// this machine does not hold yet, checks them as the server does, and
// caches them (store.PutCatalogImage). Returns how many arrived; the
// caller materialises again when any did. An image the server does not
// serve (gone, not visible) or that fails the check is skipped and
// tried again on a later pass; a 429 ends the pass.
func FetchImages(ctx context.Context, db *store.DB, catalogID string, c *Client) (int, error) {
	refs, err := db.CatalogImageRefs(catalogID)
	if err != nil || len(refs) == 0 {
		return 0, err
	}
	limits := DefaultImageLimits
	if m, err := c.Meta(ctx); err == nil {
		limits = m.ImageLimitsOf()
	}
	got := 0
	tried := 0
	for _, r := range refs {
		h, ok := ImageHash(r)
		if !ok {
			continue
		}
		if id, err := db.ImageIDBySHA256(h); err != nil {
			return got, err
		} else if id != "" {
			continue
		}
		if tried >= FetchImagesPerPass {
			break
		}
		tried++
		mime, data, err := c.GetImage(ctx, h)
		if err != nil {
			var ra *RetryAfterError
			if errors.As(err, &ra) {
				return got, nil
			}
			var ae *APIError
			if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
				continue
			}
			return got, err
		}
		if ImageSHA256(data) != h {
			log.Printf("catalog: image %s: the bytes do not match their address; not used", h[:12])
			continue
		}
		typ, err := CheckImage(data, limits)
		if err != nil {
			log.Printf("catalog: image %s: %v; not used", h[:12], err)
			continue
		}
		if mime == "" || SniffImage(data) != mime {
			mime = typ
		}
		if _, err := db.PutCatalogImage(h, mime, data); err != nil {
			return got, fmt.Errorf("cache image: %w", err)
		}
		got++
	}
	return got, nil
}

// PruneImages drops cached catalog images no current catalog record and
// no row uses.
func PruneImages(db *store.DB) (int, error) {
	refs, err := db.CatalogImageRefs("")
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	for _, r := range refs {
		if h, ok := ImageHash(r); ok {
			keep[h] = true
		}
	}
	return db.PruneCatalogImages(keep)
}
