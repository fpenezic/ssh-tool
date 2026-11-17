package catalog

import (
	"encoding/json"
	"log"
	"strings"

	"ssh-tool/internal/store"
)

// External references: a credential slot of kind "external" names an item
// in a secret backend each user reads with their own account. Phase 2
// handles Bitwarden / Vaultwarden. Any other backend behaves like an
// ordinary slot the user binds by hand.
//
// The reference names only the item (PROTOCOL 4.3), never a server: each
// user's server registrations are local, and the app picks which of them
// holds the item. The pick is an ordinary credential (a catalog-made one,
// store.CatalogCredentialID) carrying the usual bitwarden_ref, so every
// credential consumer (SSH, TOTP, Kafka, Database) works unchanged.

// BackendBitwarden is the external_ref backend this client resolves.
const BackendBitwarden = "bitwarden"

// ItemProbe reports whether a registered Bitwarden server's vault holds an
// item. A probe error (vault not reachable) counts as "not known": that
// server is not picked on this pass.
type ItemProbe func(serverID, cipherID string) (bool, error)

type bitwardenRef struct {
	CipherID string `json:"cipher_id"`
	Field    string `json:"field"`
}

// PrepareExternal creates or updates the catalog-made credential for each
// Bitwarden external slot this user has not bound by hand, and returns the
// resolver materialisation uses. Server choice:
//   - none registered: the slot stays unbound (the user can add a server,
//     or bind the slot to anything of their own);
//   - one: that server;
//   - several: the one whose vault holds the item, when exactly one does;
//     otherwise unbound, and the user picks by binding the slot.
//
// It reads and writes the store outside any catalog transaction (the probe
// may reach the network), so it runs before Sync or Materialise. Credentials
// for slots that are gone, or no longer external, are removed.
func PrepareExternal(db *store.DB, cat *store.Catalog, probe ItemProbe) (ExternalResolver, error) {
	slots, err := db.ListCatalogSlots(cat.ID)
	if err != nil {
		return nil, err
	}
	bindings, err := db.ListCatalogBindings(cat.ID)
	if err != nil {
		return nil, err
	}
	bound := map[string]bool{}
	for _, b := range bindings {
		bound[b.SlotUUID] = true
	}
	servers, err := db.ListBitwardenServers()
	if err != nil {
		return nil, err
	}
	picked := map[string]string{} // slot uuid -> credential id
	keep := map[string]bool{}
	folderReady := false
	for _, s := range slots {
		if s.Type != "cred" || s.Kind != "external" || !strings.EqualFold(s.Backend, BackendBitwarden) || bound[s.UUID] {
			continue
		}
		var ref bitwardenRef
		if err := json.Unmarshal([]byte(s.ExternalRefJSON), &ref); err != nil || ref.CipherID == "" {
			log.Printf("catalog %s: slot %s has an unreadable bitwarden reference", cat.Name, s.Name)
			continue
		}
		serverID := ""
		switch len(servers) {
		case 0:
		case 1:
			serverID = servers[0].ID
		default:
			if probe == nil {
				// No probing on this path (a rematerialise after a local
				// edit): keep the server an earlier pass found, when it is
				// still registered, rather than unbinding a working slot.
				if cr, err := db.GetCredential(store.CatalogCredentialID(cat.ID, s.UUID)); err == nil && cr != nil {
					if prev := store.ParseBitwardenRef(cr.Config); prev != nil {
						for _, srv := range servers {
							if srv.ID == prev.ServerID {
								serverID = srv.ID
							}
						}
					}
				}
				break
			}
			var hits []string
			for _, srv := range servers {
				ok, err := probe(srv.ID, ref.CipherID)
				if err != nil {
					log.Printf("catalog %s: looking for slot %s in bitwarden server %s: %v", cat.Name, s.Name, srv.Name, err)
					continue
				}
				if ok {
					hits = append(hits, srv.ID)
				}
			}
			if len(hits) == 1 {
				serverID = hits[0]
			}
		}
		if serverID == "" {
			continue
		}
		if !folderReady {
			if err := db.EnsureCatalogCredentialFolder(cat.ID, cat.Name); err != nil {
				return nil, err
			}
			folderReady = true
		}
		field := ref.Field
		if field == "" {
			field = "password"
		}
		kind := store.CredPassword
		if strings.EqualFold(field, "privatekey") {
			kind = store.CredKey
		}
		cfg := map[string]any{
			"bitwarden_ref": map[string]any{"server_id": serverID, "cipher_id": ref.CipherID, "field": field},
			"catalog_slot":  map[string]any{"catalog_id": cat.ID, "slot_uuid": s.UUID},
		}
		if err := db.UpsertCatalogCredential(cat.ID, s.UUID, cat.Name+": "+s.Name, kind, cfg); err != nil {
			return nil, err
		}
		id := store.CatalogCredentialID(cat.ID, s.UUID)
		picked[s.UUID] = id
		keep[id] = true
	}
	if err := db.DeleteStaleCatalogCredentials(cat.ID, keep); err != nil {
		return nil, err
	}
	return func(s store.CatalogSlot) string { return picked[s.UUID] }, nil
}
