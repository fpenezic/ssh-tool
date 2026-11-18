package main

import (
	"context"
	"database/sql"
	"net"
	"strings"
	"testing"

	"ssh-tool/internal/inventory"
)

const (
	linkFolderUUID = "3f2b8c1e-9a4d-4e6f-8b2a-1c3d5e7f9a0b"
	linkConnUUID   = "7d1e2f3a-4b5c-4d6e-9f80-a1b2c3d4e5f6"
	linkSlotUUID   = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
)

func TestParseCatalogOpenLink(t *testing.T) {
	ok := "ssh-tool://catalog/open?url=https%3A%2F%2Fcatalog.example.com&kind=connections&uuid=" + strings.ToUpper(linkConnUUID)
	l := parseCatalogOpenLink([]string{"--x", ok})
	if l == nil || l.URL != "https://catalog.example.com" || l.Kind != "connections" || l.UUID != linkConnUUID {
		t.Fatalf("parsed %+v", l)
	}
	for _, bad := range []string{
		"ssh-tool://catalog/open?url=https%3A%2F%2Fc.example.com&kind=forwards&uuid=" + linkConnUUID,
		"ssh-tool://catalog/open?url=https%3A%2F%2Fc.example.com&kind=folders&uuid=not-a-uuid",
		"ssh-tool://catalog/open?url=https%3A%2F%2Fc.example.com&kind=folders&uuid={" + linkConnUUID + "}",
		"ssh-tool://catalog/open?kind=folders&uuid=" + linkConnUUID,
		"ssh-tool://catalog/add?url=https%3A%2F%2Fc.example.com&code=X",
	} {
		if l := parseCatalogOpenLink([]string{bad}); l != nil {
			t.Errorf("%s parsed as %+v", bad, l)
		}
	}
	// The enrollment parser does not take an open link either.
	if parseCatalogLink([]string{ok}) != nil {
		t.Error("an open link read as an enrollment")
	}
}

// A link selects: a held record resolves to its row, a slot to the slot,
// a record that left the catalog or was never shared is "not found" (after
// one sync try), and a server that is not attached only gets its /connect
// page offered.
func TestCatalogOpenLinkResolves(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	err := a.db.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE folders SET remote_uuid = ? WHERE id = 'cf'`, linkFolderUUID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE connections SET remote_uuid = ? WHERE id = 'cc'`, linkConnUUID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE catalog_slots SET uuid = ? WHERE uuid = 's1'`, linkSlotUUID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	base := ca.cat.BaseURL

	r, err := a.CatalogOpenLink(strings.ToUpper(base[:4])+base[4:]+"/", "connections", strings.ToUpper(linkConnUUID))
	if err != nil || r.Status != "ok" || r.Kind != "connection" || r.ID != "cc" || r.CatalogID != ca.cat.ID {
		t.Fatalf("connection: %+v %v", r, err)
	}
	if r, err := a.CatalogOpenLink(base, "folders", linkFolderUUID); err != nil || r.Status != "ok" || r.Kind != "folder" || r.ID != "cf" {
		t.Fatalf("folder: %+v %v", r, err)
	}
	if r, err := a.CatalogOpenLink(base, "slots", linkSlotUUID); err != nil || r.Status != "ok" || r.Kind != "slot" || r.ID != linkSlotUUID {
		t.Fatalf("slot: %+v %v", r, err)
	}
	// Never shared (or not synced yet): one sync try, then not found.
	if r, err := a.CatalogOpenLink(base, "connections", linkSlotUUID); err != nil || r.Status != "not_found" || r.SyncError == "" {
		t.Fatalf("unknown: %+v %v", r, err)
	}
	// Left the catalog: not found, not revealed.
	if err := a.db.WithCatalogWriter(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE connections SET catalog_state = 'removed' WHERE id = 'cc'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r, err := a.CatalogOpenLink(base, "connections", linkConnUUID); err != nil || r.Status != "not_found" {
		t.Fatalf("removed: %+v %v", r, err)
	}
	// Another server: only its connect page.
	r, err = a.CatalogOpenLink("https://other.example.com/base/", "folders", linkFolderUUID)
	if err != nil || r.Status != "not_connected" || r.ConnectURL != "https://other.example.com/base/connect" || r.CatalogID != "" {
		t.Fatalf("other server: %+v %v", r, err)
	}
	// Not a link at all.
	if _, err := a.CatalogOpenLink(base, "forwards", linkConnUUID); err == nil {
		t.Error("a forward kind was accepted")
	}
	if _, err := a.CatalogOpenLink("javascript:alert(1)", "folders", linkFolderUUID); err == nil {
		t.Error("a non-http catalog URL was accepted")
	}
}

// A catalog on a network profile dials through it: a background pass never
// starts the tunnel and waits (not an issue) when it is down; Sync now may
// start it.
func TestCatalogNetworkRouting(t *testing.T) {
	ca := newCatalogApp(t)
	a := ca.a
	if err := (vaultTokens{a: a, id: ca.cat.ID}).Save("stale"); err != nil {
		t.Fatal(err)
	}
	var calls []bool // background flag per tunnel dial
	old := catalogTunnelDial
	catalogTunnelDial = func(_ *App, profileID string, background bool) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
		if profileID != "vpn-1" {
			t.Errorf("dialed through %q", profileID)
		}
		calls = append(calls, background)
		if background {
			return nil, inventory.ErrTunnelWaiting
		}
		var d net.Dialer
		return d.DialContext, nil
	}
	t.Cleanup(func() { catalogTunnelDial = old })

	// Direct: no tunnel dial.
	_ = a.catalogSyncNow(ca.cat.ID, false)
	if len(calls) != 0 {
		t.Fatalf("a direct catalog used the tunnel: %v", calls)
	}
	if err := a.db.SetSetting(catalogNetworkKey(ca.cat.ID), "vpn-1"); err != nil {
		t.Fatal(err)
	}
	a.dropCatalogClient(ca.cat.ID)
	delete(a.catalogs.state, ca.cat.ID)
	// The direct pass above used up the token (the fake refuses it).
	if err := (vaultTokens{a: a, id: ca.cat.ID}).Save("stale"); err != nil {
		t.Fatal(err)
	}

	// Background with the tunnel down: waits, nothing reaches the server.
	refused := ca.tokenRefused
	err := a.catalogSyncNow(ca.cat.ID, true)
	if !catalogNetworkWaiting(err) {
		t.Fatalf("background: %v", err)
	}
	if len(calls) == 0 || !calls[len(calls)-1] || ca.tokenRefused != refused {
		t.Fatalf("background dials %v, refreshes %d -> %d", calls, refused, ca.tokenRefused)
	}
	list, _ := a.CatalogList()
	if list[0].State != catStateVPN || list[0].NetworkProfileID != "vpn-1" || !strings.Contains(list[0].Error, "waiting for") {
		t.Fatalf("background state: %+v", list[0])
	}
	// Sync now goes through the profile (and may start it).
	err = a.catalogSyncNow(ca.cat.ID, false)
	if calls[len(calls)-1] || ca.tokenRefused != refused+1 || catalogNetworkWaiting(err) {
		t.Fatalf("foreground: dials %v, refreshes %d, err %v", calls, ca.tokenRefused, err)
	}
}
