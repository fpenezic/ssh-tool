package resolver

import (
	"fmt"
	"strings"

	"ssh-tool/internal/store"
)

// A jump hop can reference a saved connection (JumpHostSpec.ConnectionID)
// instead of repeating its host, user and credential. ExpandJumpRefs turns
// those references into plain hops right before a connect, so the SSH
// layer never sees one:
//
//   - the referenced connection is resolved with its own folder
//     inheritance; when it is the FIRST hop its own jump chain comes first
//     (a bastion behind a bastion just works), otherwise the hops before
//     it in this chain are the route to it;
//   - its user and credential are written into each hop explicitly,
//     because a hop without them would otherwise borrow the TARGET's;
//   - a bastion that meets itself in its own (usually inherited) chain
//     keeps only the hops before itself, so "folder jumps through the
//     bastion that lives in it" works for the bastion too;
//   - any other chain that leads back to a connection already on the path
//     is an error, as is a reference to a connection that no longer exists.

// ConnLookup returns the connection a hop references, or an error saying
// why it cannot (deleted, gone from the inventory). An id with the
// DynRefPrefix names an inventory host; the returned connection is then the
// same synthetic one a direct connect to that host builds.
type ConnLookup func(id string) (*store.Connection, error)

// DynRefPrefix marks a hop reference to a dynamic-inventory host:
// "dyn:<folderId>/<externalId>". The provider's own id (vmid, droplet id,
// instance id, Ansible host name) survives refreshes, a host that drops
// out and comes back, and profile sync to another machine - the entry's
// local row id does none of that.
const DynRefPrefix = "dyn:"

// DynRef builds the reference for an inventory host.
func DynRef(folderID, externalID string) string {
	return DynRefPrefix + folderID + "/" + externalID
}

// ParseDynRef splits a reference built by DynRef.
func ParseDynRef(ref string) (folderID, externalID string, ok bool) {
	rest, ok := strings.CutPrefix(ref, DynRefPrefix)
	if !ok {
		return "", "", false
	}
	folderID, externalID, ok = strings.Cut(rest, "/")
	if !ok || folderID == "" || externalID == "" {
		return "", "", false
	}
	return folderID, externalID, true
}

// DynamicHostLookup resolves a DynRef to the synthetic connection a direct
// connect to that inventory host uses. The app wires it at startup (it
// owns the per-provider overrides); unset, inventory references fail.
var DynamicHostLookup func(ref string) (*store.Connection, error)

// DynamicSelfRef maps a directly connected dynamic host's id
// ("dyn:<entryId>") to the reference a jump hop would use for it, so a
// chain through itself is recognised. Wired by the app; "" when unknown.
var DynamicSelfRef func(connID string) string

// LookupFrom builds a ConnLookup over a loaded connection list.
func LookupFrom(conns []store.Connection) ConnLookup {
	idx := make(map[string]*store.Connection, len(conns))
	for i := range conns {
		idx[conns[i].ID] = &conns[i]
	}
	return func(id string) (*store.Connection, error) {
		if strings.HasPrefix(id, DynRefPrefix) {
			return lookupDynamic(id)
		}
		if c, ok := idx[id]; ok {
			return c, nil
		}
		return nil, errDeletedRef(id)
	}
}

func lookupDynamic(ref string) (*store.Connection, error) {
	if DynamicHostLookup == nil {
		return nil, fmt.Errorf("inventory host references are not available here")
	}
	return DynamicHostLookup(ref)
}

func errDeletedRef(id string) error {
	return fmt.Errorf("the jump host connection was deleted (id %s)", id)
}

// HasJumpRefs reports whether a chain references any saved connection, so
// callers can skip loading the connection list when it does not.
func HasJumpRefs(spec *store.JumpHostSpec) bool {
	for cur := spec; cur != nil; cur = cur.Via {
		if cur.ConnectionID != nil {
			return true
		}
	}
	return false
}

// maxJumpHops bounds an expanded chain. Real chains are 1-3 hops; this only
// stops a pathological nesting from building something absurd.
const maxJumpHops = 16

// ExpandJumpRefs replaces every connection reference in rs.JumpHost with
// the hops it stands for. selfID is the connection being connected ("" for
// a synthetic one), so a chain through itself is caught. rs is updated in
// place; the stored chain it pointed to is never modified.
//
// When the first hop comes from a reference and rs has no network profile
// of its own, the referenced connection's profile is used: the first hop
// is where the network profile applies, and a bastion that is only
// reachable over a VPN needs it whoever jumps through it.
func ExpandJumpRefs(rs *store.ResolvedSettings, selfID string, folders []store.Folder, lookup ConnLookup) error {
	if !HasJumpRefs(rs.JumpHost) {
		return nil
	}
	path := map[string]bool{}
	self := map[string]bool{}
	if selfID != "" {
		path[selfID] = true
		self[selfID] = true
		// A dynamic host connected directly carries its local row id
		// ("dyn:<entryId>"); a hop names it by folder + provider id.
		if DynamicSelfRef != nil {
			if alias := DynamicSelfRef(selfID); alias != "" {
				path[alias] = true
				self[alias] = true
			}
		}
	}
	hops, firstNet, err := expandChain(rs.JumpHost, path, self, folders, lookup)
	if err != nil {
		return err
	}
	if len(hops) > maxJumpHops {
		return fmt.Errorf("jump chain has %d hops after expanding bastion connections (max %d)", len(hops), maxJumpHops)
	}
	rs.JumpHost = relink(hops)
	if rs.NetworkProfileID == nil && firstNet != nil {
		rs.NetworkProfileID = firstNet
	}
	return nil
}

// expandChain flattens spec into plain hops, first-dialled first. firstNet
// is the network profile of the referenced connection that supplies the
// very first hop, if one does.
//
// owner is the connection whose chain this is. Reaching a hop that is the
// owner itself ends the chain there: a bastion that sits in the folder it
// is the jump host for inherits a chain through itself, and what it really
// needs is the hops BEFORE it (none, usually - it is dialled directly).
func expandChain(spec *store.JumpHostSpec, path, owner map[string]bool, folders []store.Folder, lookup ConnLookup) (hops []store.JumpHostSpec, firstNet *string, err error) {
	for cur := spec; cur != nil; cur = cur.Via {
		if cur.ConnectionID == nil {
			h := *cur
			h.Via = nil
			hops = append(hops, h)
			continue
		}
		id := *cur.ConnectionID
		if id == "" {
			return nil, nil, fmt.Errorf("a jump hop is set to a saved connection but none is picked")
		}
		if owner[id] {
			break
		}
		c, err := lookup(id)
		if err != nil {
			return nil, nil, err
		}
		if path[id] {
			return nil, nil, fmt.Errorf("jump chain loops back to %q", c.Name)
		}
		rs := ResolveWith(*c, folders)
		// The bastion's own route is used only when it is the first hop.
		// With hops before it in this chain, those ARE the route to it
		// (prepending its own chain would dial the same edge host twice,
		// or try to reach its route from the wrong side).
		var sub []store.JumpHostSpec
		var subNet *string
		if len(hops) == 0 {
			path[id] = true
			sub, subNet, err = expandChain(rs.JumpHost, path, map[string]bool{id: true}, folders, lookup)
			delete(path, id)
			if err != nil {
				return nil, nil, err
			}
		}
		// The referenced connection's own hops default to ITS user and
		// credential, the same way they do when it is connected directly.
		for i := range sub {
			if sub[i].Username == nil {
				sub[i].Username = rs.Username
			}
			if sub[i].AuthRef == nil {
				sub[i].AuthRef = rs.AuthRef
			}
		}
		port := rs.Port
		self := store.JumpHostSpec{
			Hostname: rs.Hostname,
			Port:     &port,
			Username: rs.Username,
			AuthRef:  rs.AuthRef,
			Name:     c.Name,
		}
		// Whatever network profile R itself would dial its first hop with
		// (its own, else the one its chain adopted) is what this chain's
		// first hop needs - but only if R supplies that first hop.
		if len(hops) == 0 {
			firstNet = rs.NetworkProfileID
			if firstNet == nil {
				firstNet = subNet
			}
		}
		hops = append(hops, sub...)
		hops = append(hops, self)
	}
	return hops, firstNet, nil
}

func relink(hops []store.JumpHostSpec) *store.JumpHostSpec {
	var next *store.JumpHostSpec
	for i := len(hops) - 1; i >= 0; i-- {
		h := hops[i]
		h.Via = next
		next = &h
	}
	return next
}
