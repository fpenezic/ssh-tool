package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"ssh-tool/internal/catalog"
)

// ssh-tool://catalog/open?url=<base>&kind=<connections|folders|slots>&uuid=<uuid>
// is the catalog web page's "Open in ssh-tool" (catalog PROTOCOL 8.3). Any
// page can open such a URL, so the link only ever selects and reveals: it
// never connects, starts a forward or runs anything, and a server that is
// not attached only gets an offer to open its own /connect page.

// CatalogOpenLinkArgs is an ssh-tool://catalog/open link's content.
type CatalogOpenLinkArgs struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
	UUID string `json:"uuid"`
}

var catalogOpenKinds = map[string]bool{"connections": true, "folders": true, "slots": true}

// parseCatalogOpenLink finds a well-formed ssh-tool://catalog/open link in
// argv: an unknown kind or a uuid that is not one makes it no link at all.
func parseCatalogOpenLink(args []string) *CatalogOpenLinkArgs {
	for _, a := range args {
		if !strings.HasPrefix(strings.ToLower(a), "ssh-tool://") {
			continue
		}
		u, err := url.Parse(a)
		if err != nil || !strings.EqualFold(u.Host, "catalog") || strings.TrimRight(u.Path, "/") != "/open" {
			continue
		}
		q := u.Query()
		l := &CatalogOpenLinkArgs{URL: strings.TrimSpace(q.Get("url")), Kind: q.Get("kind"), UUID: strings.ToLower(q.Get("uuid"))}
		if l.URL == "" || !catalogOpenKinds[l.Kind] {
			continue
		}
		if _, err := uuid.Parse(l.UUID); err != nil || len(l.UUID) != 36 {
			continue
		}
		return l
	}
	return nil
}

// CatalogOpenResult tells the frontend what to show for a link.
type CatalogOpenResult struct {
	// Status: "ok" (Kind + ID name the row to reveal), "not_connected"
	// (ConnectURL is the server's /connect page to offer), "not_found"
	// (not shared with this account, or no longer in the catalog).
	Status      string `json:"status"`
	CatalogID   string `json:"catalog_id,omitempty"`
	CatalogName string `json:"catalog_name,omitempty"`
	// Kind: "folder", "connection" or "slot"; ID the local row id (the
	// slot's uuid for a slot).
	Kind       string `json:"kind,omitempty"`
	ID         string `json:"id,omitempty"`
	ConnectURL string `json:"connect_url,omitempty"`
	// SyncError: the sync tried for a record not held yet failed, so
	// "not found" may only mean "not reachable now".
	SyncError string `json:"sync_error,omitempty"`
}

// CatalogOpenLink resolves a catalog/open link to a row in the tree,
// syncing first when the record is not held yet. Read-only apart from
// that sync.
func (a *App) CatalogOpenLink(rawURL, kind, id string) (*CatalogOpenResult, error) {
	l := parseCatalogOpenLink([]string{"ssh-tool://catalog/open?" + url.Values{"url": {rawURL}, "kind": {kind}, "uuid": {id}}.Encode()})
	if l == nil {
		return nil, fmt.Errorf("not a catalog link")
	}
	base, err := catalog.ValidateBaseURL(l.URL)
	if err != nil {
		return nil, err
	}
	cats, err := a.db.ListCatalogs()
	if err != nil {
		return nil, err
	}
	var catID, catName string
	for _, c := range cats {
		if catalog.SameServer(c.BaseURL, base) {
			catID, catName = c.ID, c.Name
			break
		}
	}
	if catID == "" {
		return &CatalogOpenResult{Status: "not_connected", ConnectURL: strings.TrimRight(base, "/") + "/connect"}, nil
	}
	res := &CatalogOpenResult{CatalogID: catID, CatalogName: catName}
	find := func() (bool, error) {
		if l.Kind == "slots" {
			slots, err := a.db.ListCatalogSlots(catID)
			if err != nil {
				return false, err
			}
			for _, s := range slots {
				if strings.EqualFold(s.UUID, l.UUID) {
					res.Status, res.Kind, res.ID = "ok", "slot", s.UUID
					return true, nil
				}
			}
			return false, nil
		}
		rowID, state, err := a.db.CatalogLocalRowState(catID, l.Kind, l.UUID)
		if err != nil || rowID == "" || state == "removed" {
			return false, err
		}
		res.Status, res.ID = "ok", rowID
		res.Kind = strings.TrimSuffix(l.Kind, "s")
		return true, nil
	}
	if ok, err := find(); err != nil || ok {
		return res, err
	}
	// Not held: shared since the last pass, perhaps. One sync (a user
	// action, so it may bring the catalog's VPN up), then look again.
	if err := a.catalogSyncNow(catID, false); err != nil {
		res.SyncError = catalogShortError(err)
	}
	if ok, err := find(); err != nil || ok {
		return res, err
	}
	res.Status = "not_found"
	return res, nil
}
