package store

import (
	"fmt"
	"strings"
)

// A catalog record names slots and other catalog records where the app
// has local ids. Materialisation writes local ids where it can, and one of
// these markers where it cannot, so a connect never falls back to an
// inherited or default credential, network route or jump host in their
// place (nil would mean "inherit"):
//
//	cat-unbound:<catalog id>/<slot uuid>   a slot this user has not bound
//	                                       (or bound to something since deleted)
//	cat-missing:<catalog id>/<uuid>        a reference to a record this
//	                                       user does not hold (deleted, or
//	                                       not readable for them)
//
// Every lookup that can receive one (GetCredential, GetNetworkProfile, the
// jump-host lookups in the resolver) answers with a *CatalogRefError, so
// the user is told which slot to bind instead of "not found".
const (
	catalogUnboundPrefix = "cat-unbound:"
	catalogMissingPrefix = "cat-missing:"
)

// CatalogUnboundRef is the marker for an unbound slot.
func CatalogUnboundRef(catalogID, slotUUID string) string {
	return catalogUnboundPrefix + catalogID + "/" + slotUUID
}

// CatalogMissingRef is the marker for a reference to a record not held.
func CatalogMissingRef(catalogID, uuid string) string {
	return catalogMissingPrefix + catalogID + "/" + uuid
}

// CatalogRefError says a reference is a catalog marker, not a local id.
type CatalogRefError struct {
	Unbound   bool // true: an unbound slot; false: a record not held
	CatalogID string
	UUID      string
	// What looked it up: "credential", "network profile", "jump host".
	What string
	// Name is the slot's name when known (the store fills it in).
	Name string
}

func (e *CatalogRefError) Error() string {
	if e.Unbound {
		slot := e.UUID
		if e.Name != "" {
			slot = fmt.Sprintf("%q", e.Name)
		}
		return fmt.Sprintf("the catalog %s slot %s is not bound on this machine - bind it to one of your own", e.What, slot)
	}
	return fmt.Sprintf("the catalog %s %s is not available to you (deleted, or no access) - set a local one on this connection", e.What, e.UUID)
}

// ParseCatalogRef recognises a marker. ok is false for an ordinary id.
func ParseCatalogRef(ref, what string) (*CatalogRefError, bool) {
	var unbound bool
	var rest string
	switch {
	case strings.HasPrefix(ref, catalogUnboundPrefix):
		unbound, rest = true, ref[len(catalogUnboundPrefix):]
	case strings.HasPrefix(ref, catalogMissingPrefix):
		rest = ref[len(catalogMissingPrefix):]
	default:
		return nil, false
	}
	cat, uuid, _ := strings.Cut(rest, "/")
	return &CatalogRefError{Unbound: unbound, CatalogID: cat, UUID: uuid, What: what}, true
}

// IsCatalogRef reports whether ref is a marker.
func IsCatalogRef(ref string) bool {
	return strings.HasPrefix(ref, catalogUnboundPrefix) || strings.HasPrefix(ref, catalogMissingPrefix)
}

// catalogRefError recognises a marker and fills in the slot's name.
func (d *DB) catalogRefError(ref, what string) (*CatalogRefError, bool) {
	e, ok := ParseCatalogRef(ref, what)
	if ok && e.Unbound {
		_ = d.conn.QueryRow(`SELECT name FROM catalog_slots WHERE catalog_id = ? AND uuid = ?`, e.CatalogID, e.UUID).Scan(&e.Name)
	}
	return e, ok
}
