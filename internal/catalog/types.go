// Package catalog is the client half of a shared connection catalog: the
// wire types and HTTP client for the catalog protocol (v1), the sign-in,
// and (in later files) the engine that applies the change feed to the
// local store.
//
// Clients ignore unknown fields in every response and skip feed items of
// a kind they do not know, so a newer server can add things within v1.
// Record fields this client does not interpret stay in Raw, which is what
// the store keeps as catalog_json.
package catalog

import (
	"encoding/json"
	"net/url"
	"strings"

	"ssh-tool/internal/store"
)

// ProtocolMajor is the protocol major version this client speaks; it is
// also the /api/v1 path segment.
const ProtocolMajor = 1

// Record kinds, as in URLs and feed items.
const (
	KindFolders     = "folders"
	KindConnections = "connections"
	KindForwards    = "forwards"
	KindSlots       = "slots"
)

// Meta is GET /api/v1/meta.
type Meta struct {
	Server struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"server"`
	Protocol struct {
		Major    int `json:"major"`
		Revision int `json:"revision"`
	} `json:"protocol"`
	Auth struct {
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RevocationEndpoint    string   `json:"revocation_endpoint"`
		ClientID              string   `json:"client_id"`
		Methods               []string `json:"methods"`
	} `json:"auth"`
	// Images: the icon image store's limits (PROTOCOL 3.6), from
	// revision 2. Nil on an older server.
	Images *ImageLimits `json:"images"`
}

// WhoAmI is GET /api/v1/whoami.
type WhoAmI struct {
	Subject string   `json:"subject"`
	Name    string   `json:"name"`
	Email   *string  `json:"email"`
	Groups  []string `json:"groups"`
	Admin   bool     `json:"admin"`
	Via     string   `json:"via"`
}

// Feed is one page of GET /api/v1/changes.
type Feed struct {
	Reset  bool   `json:"reset"`
	Items  []Item `json:"items"`
	Cursor string `json:"cursor"`
	More   bool   `json:"more"`
}

// Item is one feed entry: an upsert carrying the record, or a tombstone.
type Item struct {
	Kind   string          `json:"kind"`
	Op     string          `json:"op"` // upsert | delete
	Record json.RawMessage `json:"record,omitempty"`
	UUID   string          `json:"uuid,omitempty"`
	Rev    int64           `json:"rev,omitempty"`
	Reason string          `json:"reason,omitempty"` // deleted | access_lost
}

// Common holds the fields every record carries.
type Common struct {
	UUID      string `json:"uuid"`
	Rev       int64  `json:"rev"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// Folder is a folders record. Access "skeleton" marks an ancestor the
// caller sees only so that inheritance is complete (PROTOCOL 9.1); its
// DocMD is null.
type Folder struct {
	Common
	ParentUUID *string         `json:"parent_uuid"`
	Name       string          `json:"name"`
	SortOrder  int64           `json:"sort_order"`
	Settings   json.RawMessage `json:"settings"`
	IconName   *string         `json:"icon_name"`
	IconColor  *string         `json:"icon_color"`
	IconImage  *string         `json:"icon_image"`
	DocMD      *string         `json:"doc_md"`
	Access     string          `json:"access"`
}

// Skeleton reports whether the folder is an ancestor the caller cannot
// open (PROTOCOL 9.1).
func (f *Folder) Skeleton() bool { return f.Access == "skeleton" }

// Connection is a connections record. Settings is the app's per-connection
// overrides with catalog references in place of local ids.
type Connection struct {
	Common
	FolderUUID string          `json:"folder_uuid"`
	Name       string          `json:"name"`
	Hostname   string          `json:"hostname"`
	Protocol   string          `json:"protocol"`
	SortOrder  int64           `json:"sort_order"`
	Settings   json.RawMessage `json:"settings"`
	Tags       []string        `json:"tags"`
	Notes      string          `json:"notes"`
	IconName   *string         `json:"icon_name"`
	IconColor  *string         `json:"icon_color"`
	IconImage  *string         `json:"icon_image"`
	DocMD      *string         `json:"doc_md"`
}

// ProtocolOrDefault is the connection's protocol, "ssh" when empty, trimmed
// and lowercased for comparisons.
func (c *Connection) ProtocolOrDefault() string {
	p := strings.ToLower(strings.TrimSpace(c.Protocol))
	if p == "" {
		return "ssh"
	}
	return p
}

// Forward is a forwards record. AutoStart is only a suggestion: nothing
// starts on a user's machine because of it (PROTOCOL 3.4).
type Forward struct {
	Common
	ConnectionUUID string  `json:"connection_uuid"`
	Kind           string  `json:"kind"`
	LocalAddr      *string `json:"local_addr"`
	LocalPort      *uint16 `json:"local_port"`
	RemoteHost     *string `json:"remote_host"`
	RemotePort     *uint16 `json:"remote_port"`
	AutoStart      bool    `json:"auto_start"`
	Description    string  `json:"description"`
}

// Slot is a slots record: a named placeholder each user binds locally.
type Slot struct {
	Common
	FolderUUID  string          `json:"folder_uuid"`
	Type        string          `json:"type"` // cred | net | jump
	Name        string          `json:"name"`
	Kind        *string         `json:"kind"`    // cred: slot | external
	Backend     *string         `json:"backend"` // external only
	ExternalRef json.RawMessage `json:"external_ref"`
	DocMD       *string         `json:"doc_md"`
}

// KnownKind reports whether this client applies feed items of kind.
func KnownKind(kind string) bool {
	switch kind {
	case KindFolders, KindConnections, KindForwards, KindSlots:
		return true
	}
	return false
}

// BookmarksRevision is the protocol revision from which a forward record
// carries shared bookmarks (PROTOCOL 3.4); PortPlaceholderRevision the one
// from which such a URL may hold "{port}" as the port of its authority.
const (
	BookmarksRevision       = 6
	PortPlaceholderRevision = 9
	// CatalogSlotsRevision: from it a slot may sit at the catalog level
	// (folder_uuid null): in scope for every record, written by admins
	// only (PROTOCOL 3.5).
	CatalogSlotsRevision = 12
)

// Bookmark is a shared forward bookmark: a name and an http(s) URL. The
// user's own bookmarks (and their in_tree flag) stay local.
type Bookmark struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// SharedBookmarks turns local bookmarks into the shared shape, dropping
// what a server at revision would refuse (no name, not http/https with a
// host, too long, a "{port}" placeholder below revision 9 or anywhere but
// the authority's port) and keeping at most 50. Never nil.
func SharedBookmarks(in []store.ProxyBookmark, revision int) []Bookmark {
	out := []Bookmark{}
	for _, b := range in {
		name, u := strings.TrimSpace(b.Name), strings.TrimSpace(b.URL)
		if name == "" || len([]rune(name)) > 200 || len(u) > 2048 {
			continue
		}
		// "{port}" (a local forward whose port is picked at start or set
		// per user) is taken from revision 9, once, as the authority's
		// port; it is checked with a port in its place. Before that such
		// a bookmark stays local.
		check := u
		if strings.Contains(u, "{port}") {
			if revision < PortPlaceholderRevision || !portPlaceholderInAuthority(u) {
				continue
			}
			check = strings.Replace(u, "{port}", "1", 1)
		}
		pu, err := url.Parse(check)
		if err != nil || pu.Hostname() == "" || (!strings.EqualFold(pu.Scheme, "http") && !strings.EqualFold(pu.Scheme, "https")) {
			continue
		}
		out = append(out, Bookmark{Name: name, URL: u})
		if len(out) == 50 {
			break
		}
	}
	return out
}

// portPlaceholderInAuthority: "{port}" occurs once, as the port of the
// URL's authority ("http://127.0.0.1:{port}/x", "http://[::1]:{port}").
func portPlaceholderInAuthority(u string) bool {
	if strings.Count(u, "{port}") != 1 {
		return false
	}
	_, rest, ok := strings.Cut(u, "://")
	if !ok {
		return false
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	return strings.HasSuffix(authority, ":{port}") && len(authority) > len(":{port}")
}
