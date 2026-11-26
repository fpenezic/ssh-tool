package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/inventory"
	"ssh-tool/internal/store"
)

// A catalog server behind a VPN: each catalog may name a network profile
// its HTTP traffic goes through (sync, sign-in token calls, publish,
// edits, images, the icon). The choice is a synced setting per catalog,
// like the catalog row itself; "" is direct.
//
// Starting a tunnel is the same rule as for inventory API calls: a user
// action (Sync now, sign-in, publish, an edit) may bring the tunnel up;
// a background pass (the scheduler, the sync after a sign-in or a
// publish) only rides a tunnel that is already up, or goes direct in the
// profile's auto mode, and otherwise waits (catStateVPN, not an
// issue). A passive app must not start a VPN, perhaps with a login
// prompt, every few minutes just to poll a catalog.

const catalogNetworkKeyPrefix = "catalog_network:"

func catalogNetworkKey(catalogID string) string { return catalogNetworkKeyPrefix + catalogID }

// catalogNetwork is the network profile a catalog's traffic goes
// through, "" for direct (or when it cannot be read; a dial uses
// catalogNetworkErr so a failed read never turns into a direct dial).
func (a *App) catalogNetwork(catalogID string) string {
	id, _ := a.catalogNetworkErr(catalogID)
	return id
}

func (a *App) catalogNetworkErr(catalogID string) (string, error) {
	if a.db == nil || catalogID == "" {
		return "", nil
	}
	v, ok, err := a.db.GetSetting(catalogNetworkKey(catalogID))
	if err != nil {
		return "", fmt.Errorf("read the catalog's network setting: %w", err)
	}
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(v), nil
}

// CatalogSetNetwork routes a catalog's traffic through a network profile,
// or direct with "". The cached client is dropped so the next call dials
// the new way.
func (a *App) CatalogSetNetwork(catalogID, profileID string) error {
	if _, err := a.db.GetCatalog(catalogID); err != nil {
		return err
	}
	profileID = strings.TrimSpace(profileID)
	if profileID != "" {
		if _, err := a.db.GetNetworkProfile(profileID); err != nil {
			return fmt.Errorf("network profile: %w", err)
		}
	}
	if err := a.db.SetSetting(catalogNetworkKey(catalogID), profileID); err != nil {
		return err
	}
	a.dropCatalogClient(catalogID)
	a.recordAudit("catalog.network", catalogID, map[string]string{"network_profile_id": profileID})
	EventsEmit("catalog_changed", map[string]any{"catalog_id": catalogID})
	return nil
}

func (a *App) dropCatalogClient(catalogID string) {
	rt := a.catRT()
	rt.mu.Lock()
	c := rt.clients[catalogID]
	delete(rt.clients, catalogID)
	rt.mu.Unlock()
	if c != nil {
		c.HTTP.CloseIdleConnections()
	}
}

type catalogBgKey struct{}

// catalogBackground marks a context as a background pass: its dials never
// start a tunnel.
func catalogBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, catalogBgKey{}, true)
}

func isCatalogBackground(ctx context.Context) bool {
	v, _ := ctx.Value(catalogBgKey{}).(bool)
	return v
}

// catalogTunnelDial is the profile dialer, swappable in tests.
var catalogTunnelDial = func(a *App, profileID string, background bool) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if background {
		return a.wgBackgroundDialerFor(profileID)
	}
	return a.wgDialerFor(profileID)
}

// catalogHTTPClient is the HTTP client for a catalog's calls. profile
// says which network profile to use at dial time ("" = direct), so a
// change of the setting needs no new client for dials, only for kept
// connections (dropCatalogClient closes those).
func (a *App) catalogHTTPClient(profile func() (string, error)) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	direct := tr.DialContext
	envProxy := tr.Proxy
	// Through a tunnel the profile is the route: an environment proxy
	// would take the request (and its token) somewhere else.
	tr.Proxy = func(r *http.Request) (*url.URL, error) {
		if id, err := profile(); err != nil || id != "" {
			return nil, err
		}
		if envProxy == nil {
			return nil, nil
		}
		return envProxy(r)
	}
	tr.IdleConnTimeout = 30 * time.Second
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		id, err := profile()
		if err != nil {
			return nil, err
		}
		if id == "" {
			return direct(ctx, network, addr)
		}
		dial, err := catalogTunnelDial(a, id, isCatalogBackground(ctx))
		if err != nil {
			return nil, err
		}
		return dial(ctx, network, addr)
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: tr}
}

// catalogNetworkWaiting: a background pass needed a tunnel that is not up.
func catalogNetworkWaiting(err error) bool {
	return errors.Is(err, inventory.ErrTunnelWaiting)
}

// catalogWaitingVPNMsg is what the catalog shows while it waits.
func (a *App) catalogWaitingVPNMsg(catalogID string) string {
	return "waiting for " + a.catalogNetworkName(catalogID) + ": Sync now or a connection through it brings it up"
}

// catalogNetworkName names the catalog's network profile for a message.
func (a *App) catalogNetworkName(catalogID string) string {
	if row, err := a.db.GetNetworkProfile(a.catalogNetwork(catalogID)); err == nil && row != nil {
		return row.Name
	}
	return "its VPN"
}

// useCatalogNetwork puts a new (not yet saved) catalog's client on a
// chosen profile; Add and the enrollment use it before the row exists.
func (a *App) useCatalogNetwork(c *catalog.Client, profileID string) {
	c.HTTP = a.catalogHTTPClient(func() (string, error) { return profileID, nil })
}

// saveCatalogNetwork stores the profile chosen while adding a catalog (or
// replacing its session with a code). "" keeps what is stored.
func (a *App) saveCatalogNetwork(cat *store.Catalog, profileID string) error {
	if profileID == "" || cat == nil {
		return nil
	}
	if err := a.db.SetSetting(catalogNetworkKey(cat.ID), profileID); err != nil {
		return fmt.Errorf("save the catalog's network: %w", err)
	}
	a.dropCatalogClient(cat.ID)
	return nil
}
