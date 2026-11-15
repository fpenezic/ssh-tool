package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"runtime"
	"strings"
	"time"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
	"ssh-tool/internal/syncer"
)

// Enrollment codes (PROTOCOL 10.5): the catalog's web UI mints a one-time
// code and opens ssh-tool://catalog/add?url=<base URL>&code=<code>, or the
// user types the code in Settings - Catalogs. Either way, before anything
// is created: the server's protocol is checked, the code is inspected
// (which does not use it up) and the user confirms what is being
// connected - the server's name, the URL exactly as received, whose code
// it is. Only the confirmation redeems it. A URL scheme can be claimed by
// another local program, so a link never redeems on its own.
//
// This is the one sign-in that works on Android: no loopback browser flow.

// catalogDeviceName is what the catalog's device list shows for this
// machine.
func catalogDeviceName() string {
	n := strings.TrimSpace(syncer.DefaultDevice())
	if n == "" || n == "localhost" {
		if runtime.GOOS == "android" {
			return "ssh-tool on Android"
		}
		return "ssh-tool"
	}
	if len(n) > 100 {
		n = n[:100]
	}
	return n
}

// CatalogEnrollPreview is what the confirmation shows.
type CatalogEnrollPreview struct {
	// BaseURL is the URL exactly as it arrived (link or typed);
	// NormalisedURL what the app will use, when that differs.
	BaseURL       string `json:"base_url"`
	NormalisedURL string `json:"normalised_url"`
	ServerName    string `json:"server_name"`
	UserName      string `json:"user_name"`
	UserEmail     string `json:"user_email"`
	ExpiresIn     int    `json:"expires_in"`
	DeviceName    string `json:"device_name"`
	// Already attached: that catalog, and who is signed in there now.
	ExistingID      string `json:"existing_id,omitempty"`
	ExistingName    string `json:"existing_name,omitempty"`
	ExistingAccount string `json:"existing_account,omitempty"`
}

// memTokens holds a refresh token until the catalog row exists.
type memTokens struct{ rt string }

func (m *memTokens) Load() (string, error) { return m.rt, nil }
func (m *memTokens) Save(rt string) error  { m.rt = rt; return nil }

func (a *App) enrollClient(baseURL string, tokens catalog.TokenStore) (*catalog.Client, string, error) {
	u, err := catalog.ValidateBaseURL(baseURL)
	if err != nil {
		return nil, "", err
	}
	c, err := catalog.NewClient(u, "ssh-tool/"+appVersion, tokens)
	if err != nil {
		return nil, "", err
	}
	c.DeviceName = catalogDeviceName()
	return c, u, nil
}

// enrollError turns the 10.5 failures into what the user can act on.
func enrollError(err error) error {
	var ra *catalog.RetryAfterError
	switch {
	case errors.Is(err, catalog.ErrEnrollmentInvalid):
		return err
	case errors.As(err, &ra):
		secs := int(ra.Wait.Round(time.Second) / time.Second)
		return fmt.Errorf("the catalog is limiting attempts from this address; try again in %d s", secs)
	}
	return err
}

// CatalogEnrollInspect checks the server and the code and returns what the
// confirmation shows. Nothing is created or used up.
func (a *App) CatalogEnrollInspect(baseURL, code string) (*CatalogEnrollPreview, error) {
	baseURL, code = strings.TrimSpace(baseURL), strings.TrimSpace(code)
	if baseURL == "" || code == "" {
		return nil, fmt.Errorf("a catalog URL and a code are both needed")
	}
	c, u, err := a.enrollClient(baseURL, &memTokens{})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	meta, err := c.Meta(ctx)
	if err != nil {
		return nil, err
	}
	e, err := c.InspectEnrollment(ctx, code)
	if err != nil {
		return nil, enrollError(err)
	}
	p := &CatalogEnrollPreview{BaseURL: baseURL, ServerName: meta.Server.Name, UserName: e.User.Name,
		ExpiresIn: e.ExpiresIn, DeviceName: catalogDeviceName()}
	if u != baseURL {
		p.NormalisedURL = u
	}
	if e.User.Email != nil {
		p.UserEmail = *e.User.Email
	}
	if cats, err := a.db.ListCatalogs(); err == nil {
		for _, cat := range cats {
			if catalog.SameServer(cat.BaseURL, u) {
				p.ExistingID, p.ExistingName, p.ExistingAccount = cat.ID, cat.Name, cat.AccountName
			}
		}
	}
	return p, nil
}

// CatalogEnrollConfirm redeems the code after the user confirmed. When the
// server is already attached, replace says to end this machine's session
// there and use the new one (the rows stay); without it, nothing happens.
func (a *App) CatalogEnrollConfirm(baseURL, code string, replace bool) (*CatalogInfo, error) {
	if a.vault == nil || a.vault.Status().Kind != creds.StatusUnlocked {
		return nil, fmt.Errorf("unlock the vault first: the catalog sign-in is kept there")
	}
	baseURL, code = strings.TrimSpace(baseURL), strings.TrimSpace(code)
	tmp := &memTokens{}
	c, u, err := a.enrollClient(baseURL, tmp)
	if err != nil {
		return nil, err
	}
	var existing *store.Catalog
	if cats, err := a.db.ListCatalogs(); err == nil {
		for i := range cats {
			if catalog.SameServer(cats[i].BaseURL, u) {
				existing = &cats[i]
			}
		}
	}
	if existing != nil && !replace {
		return nil, fmt.Errorf("this catalog server is already attached as %q", existing.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	meta, err := c.Meta(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.RedeemEnrollment(ctx, code); err != nil {
		return nil, enrollError(err)
	}

	cat := existing
	if cat == nil {
		name := strings.TrimSpace(meta.Server.Name)
		if name == "" {
			name = "Catalog"
		}
		cat, err = a.db.CreateCatalog(store.NewCatalog{Name: name, BaseURL: u, ServerName: meta.Server.Name, ProtocolRevision: int64(meta.Protocol.Revision)})
		if err != nil {
			a.revokeTemp(c)
			return nil, err
		}
		a.recordAudit("catalog.add", cat.ID, map[string]string{"base_url": u, "via": "enrollment"})
	} else {
		// End this machine's previous session there first.
		if old, err := a.catalogClient(existing); err == nil {
			rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := old.Revoke(rctx); err != nil {
				log.Printf("catalog %s: revoke the replaced session: %v", existing.Name, err)
			}
			rcancel()
		}
	}
	if err := (vaultTokens{a: a, id: cat.ID}).Save(tmp.rt); err != nil {
		a.revokeTemp(c)
		return nil, fmt.Errorf("store the catalog sign-in: %w", err)
	}
	rt := a.catRT()
	rt.mu.Lock()
	delete(rt.clients, cat.ID)
	delete(rt.state, cat.ID)
	rt.mu.Unlock()
	EventsEmit("catalog_changed", map[string]any{"catalog_id": cat.ID})
	live, err := a.catalogClient(cat)
	if err != nil {
		return nil, err
	}
	if err := a.catalogSignedIn(cat, live, "enrollment"); err != nil {
		return nil, err
	}
	info := a.catalogInfo(*cat)
	return &info, nil
}

// revokeTemp ends a session redeemed into memory that could not be kept.
func (a *App) revokeTemp(c *catalog.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = c.Revoke(ctx)
}

// CatalogLink is an ssh-tool://catalog/add link's content.
type CatalogLink struct {
	URL  string `json:"url"`
	Code string `json:"code"`
}

// parseCatalogLink finds ssh-tool://catalog/add?url=..&code=.. in argv.
// Only the parse: the frontend shows the confirmation, nothing redeems.
func parseCatalogLink(args []string) *CatalogLink {
	for _, a := range args {
		if !strings.HasPrefix(strings.ToLower(a), "ssh-tool://") {
			continue
		}
		u, err := url.Parse(a)
		if err != nil || !strings.EqualFold(u.Host, "catalog") || strings.TrimRight(u.Path, "/") != "/add" {
			continue
		}
		q := u.Query()
		if q.Get("url") == "" || q.Get("code") == "" {
			continue
		}
		return &CatalogLink{URL: q.Get("url"), Code: q.Get("code")}
	}
	return nil
}
