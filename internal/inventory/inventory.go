// Package inventory powers dynamic-inventory folders: folders whose
// children are pulled from an external source (proxmox, hetzner, …)
// rather than stored by the user. The folder row itself lives in the
// regular `folders` table so the inherit cascade keeps working; the
// `dynamic_folders` side table carries provider config and refresh
// state, and `dynamic_entries` is a refreshable cache the frontend
// reads.
//
// This file defines the shape every provider implements. Concrete
// providers live in their own files (proxmox.go, hetzner.go later).
package inventory

import "context"

// EntryKind buckets a fetched entry into one of the two
// pseudo-sub-folders the tree renders for a dynamic folder.
type EntryKind string

const (
	KindHost     EntryKind = "host"      // PVE / hypervisor node
	KindGuestVM  EntryKind = "guest_vm"  // qemu VM
	KindGuestLXC EntryKind = "guest_lxc" // LXC container
	// KindServer is the generic shape used by providers that don't
	// have a hypervisor/guest distinction (Ansible static inventory,
	// flat cloud listings). Renders as "host" in the tree without
	// the "VM" / "LXC" badge that misleads on static inventories.
	KindServer EntryKind = "server"
)

// Entry is the normalised shape every provider returns. Provider-
// specific extras can ride along in Raw (JSON) for the UI to surface
// later without schema changes.
type Entry struct {
	ExternalID string    `json:"external_id"`
	Name       string    `json:"name"`
	Hostname   string    `json:"hostname"`
	Kind       EntryKind `json:"kind"`
	Status     string    `json:"status"` // "running" | "stopped" | ""
	Tags       []string  `json:"tags"`
	Raw        []byte    `json:"raw"`
}

// Provider is what every dynamic source implements.
type Provider interface {
	// Name returns the canonical id ("proxmox" / "hetzner" / …).
	Name() string
	// Fetch pulls the current state of the source. Implementations
	// should respect ctx for cancellation and timeout.
	Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error)
}

// Filter narrows a raw entry list before it lands in the cache.
// All fields are optional; empty slice = "no filter on this axis".
type Filter struct {
	// IncludeKinds restricts the result to the listed entry kinds.
	// Empty = include all.
	IncludeKinds []EntryKind `json:"include_kinds"`
	// TagWhitelist matches an entry if any of its tags is present
	// here. Empty disables whitelist filtering.
	TagWhitelist []string `json:"tag_whitelist"`
	// TagBlacklist excludes any entry whose tags overlap this set.
	// Always applied after the whitelist.
	TagBlacklist []string `json:"tag_blacklist"`
	// HideStopped drops entries reporting Status == "stopped". Applied
	// to guests; hosts are typically always "online" and the proxmox
	// status field for them isn't meaningful for this filter.
	HideStopped bool `json:"hide_stopped"`
}

// Apply walks entries and returns the subset matching the filter.
func (f Filter) Apply(entries []Entry) []Entry {
	if len(f.IncludeKinds) == 0 && len(f.TagWhitelist) == 0 && len(f.TagBlacklist) == 0 && !f.HideStopped {
		return entries
	}
	allowed := map[EntryKind]bool{}
	for _, k := range f.IncludeKinds {
		allowed[k] = true
	}
	whitelist := map[string]bool{}
	for _, t := range f.TagWhitelist {
		whitelist[t] = true
	}
	blacklist := map[string]bool{}
	for _, t := range f.TagBlacklist {
		blacklist[t] = true
	}
	out := make([]Entry, 0, len(entries))
EntryLoop:
	for _, e := range entries {
		if len(allowed) > 0 && !allowed[e.Kind] {
			continue
		}
		if len(whitelist) > 0 {
			ok := false
			for _, t := range e.Tags {
				if whitelist[t] {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		for _, t := range e.Tags {
			if blacklist[t] {
				continue EntryLoop
			}
		}
		if f.HideStopped && e.Kind != KindHost && e.Status == "stopped" {
			continue
		}
		out = append(out, e)
	}
	return out
}
