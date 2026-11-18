package catalog

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"ssh-tool/internal/store"
)

// A catalog's own icon (PROTOCOL revision 5): meta's server.icon names it
// ("sha256:<hex>" or null) and GET /api/v1/server-icon serves the bytes,
// both anonymous, so the icon shows before a sign-in. The app keeps it as
// the image of the catalog's root folder: the tree draws a folder image
// already, and an image a row uses is never pruned.

// ServerIconRef reads server.icon from a fresh meta (the cached Meta is
// read once per client and would miss a change). "" when the server has
// none or predates revision 5.
func (c *Client) ServerIconRef(ctx context.Context) (string, error) {
	var m struct {
		Server struct {
			Icon *string `json:"icon"`
		} `json:"server"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/meta", nil, &m, false); err != nil {
		return "", err
	}
	if m.Server.Icon == nil || *m.Server.Icon == "" {
		return "", nil
	}
	h, ok := ImageHash(*m.Server.Icon)
	if !ok {
		return "", fmt.Errorf("catalog: server.icon is not a sha256 image address")
	}
	return h, nil
}

// maxServerIconRead bounds the download whatever the server claims; the
// image check then applies the real limits.
const maxServerIconRead = 2 << 20

// GetServerIcon downloads the catalog's icon without a token. A 404 (no
// icon set) is nil bytes and no error.
func (c *Client) GetServerIcon(ctx context.Context, hexHash string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/server-icon?v="+hexHash, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/png, image/jpeg, image/webp")
	c.setUA(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, parseAPIError(resp.StatusCode, body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxServerIconRead+1))
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if len(data) > maxServerIconRead {
		return nil, fmt.Errorf("catalog: the server icon is larger than %d bytes", maxServerIconRead)
	}
	return data, nil
}

// SyncServerIcon brings the catalog root folder's image in line with the
// server's icon: fetched only when the hash differs from the one held,
// checked like any catalog image (PNG, JPEG or WebP within the limits,
// bytes matching their address), cleared when the server has none.
// Reports whether the root's image changed.
func SyncServerIcon(ctx context.Context, db *store.DB, cat *store.Catalog, c *Client) (bool, error) {
	want, err := c.ServerIconRef(ctx)
	if err != nil {
		return false, err
	}
	have, err := db.CatalogRootIconSHA256(cat.RootFolderID)
	if err != nil {
		return false, err
	}
	if want == have {
		return false, nil
	}
	if want == "" {
		return true, db.SetCatalogRootIcon(cat.RootFolderID, nil)
	}
	id, err := db.ImageIDBySHA256(want)
	if err != nil {
		return false, err
	}
	if id == "" {
		data, err := c.GetServerIcon(ctx, want)
		if err != nil {
			return false, err
		}
		if data == nil {
			// Meta named an icon the endpoint does not serve (changed in
			// between): leave things as they are for the next pass.
			return false, nil
		}
		if ImageSHA256(data) != want {
			log.Printf("catalog %s: server icon: the bytes do not match their address; not used", cat.Name)
			return false, nil
		}
		limits := DefaultImageLimits
		if m, err := c.Meta(ctx); err == nil {
			limits = m.ImageLimitsOf()
		}
		typ, err := CheckImage(data, limits)
		if err != nil {
			log.Printf("catalog %s: server icon: %v; not used", cat.Name, err)
			return false, nil
		}
		if id, err = db.PutCatalogImage(want, typ, data); err != nil {
			return false, fmt.Errorf("cache server icon: %w", err)
		}
	}
	return true, db.SetCatalogRootIcon(cat.RootFolderID, &id)
}

// FreshRevision reads protocol.revision from a fresh meta: the cached
// Meta is read once per client and would miss a server upgrade, and a
// revision gate (shared bookmarks, "{port}") must see the server as it is.
func (c *Client) FreshRevision(ctx context.Context) (int, error) {
	var m struct {
		Protocol struct {
			Revision int `json:"revision"`
		} `json:"protocol"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/meta", nil, &m, false); err != nil {
		return 0, err
	}
	return m.Protocol.Revision, nil
}
