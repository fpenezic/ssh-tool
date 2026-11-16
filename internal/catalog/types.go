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
	"strings"
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
