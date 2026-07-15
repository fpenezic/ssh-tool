package keepass

import (
	kp "github.com/tobischo/gokeepasslib/v3"
)

// EntryInfo is one entry as surfaced to the credential picker: enough to
// display and reference it, never the secret. UUID is the base64 string the
// credential reference stores.
type EntryInfo struct {
	UUID       string   `json:"uuid"`
	Title      string   `json:"title"`
	Username   string   `json:"username"`
	HasPass    bool     `json:"has_pass"`
	Attachments []string `json:"attachments"` // attachment keys, for key-file auth
	CustomKeys []string `json:"custom_keys"`  // non-standard String keys
	GroupPath  string   `json:"group_path"`   // "Root/Servers/prod" for display
}

// GroupInfo is one group node in the browse tree.
type GroupInfo struct {
	Name    string      `json:"name"`
	Path    string      `json:"path"`
	Groups  []GroupInfo `json:"groups"`
	Entries []EntryInfo `json:"entries"`
}

// standardKeys are the built-in String keys KeePass ships; we don't offer them
// as "custom" fields in the picker (Password is offered explicitly).
var standardKeys = map[string]bool{
	"Title": true, "UserName": true, "Password": true,
	"URL": true, "Notes": true,
}

// Browse returns the full group/entry tree for the picker. Secrets are never
// included - only presence flags and field names.
func (d *DB) Browse() []GroupInfo {
	return browseGroups(d.db.Content.Root.Groups, "")
}

func browseGroups(groups []kp.Group, parentPath string) []GroupInfo {
	out := make([]GroupInfo, 0, len(groups))
	for gi := range groups {
		g := &groups[gi]
		path := g.Name
		if parentPath != "" {
			path = parentPath + "/" + g.Name
		}
		gi := GroupInfo{
			Name:    g.Name,
			Path:    path,
			Groups:  browseGroups(g.Groups, path),
			Entries: make([]EntryInfo, 0, len(g.Entries)),
		}
		for ei := range g.Entries {
			gi.Entries = append(gi.Entries, entryInfo(&g.Entries[ei], path))
		}
		out = append(out, gi)
	}
	return out
}

func entryInfo(e *kp.Entry, groupPath string) EntryInfo {
	b64, _ := e.UUID.MarshalText()
	info := EntryInfo{
		UUID:      string(b64),
		Title:     e.GetTitle(),
		Username:  e.GetContent("UserName"),
		HasPass:   e.GetPassword() != "",
		GroupPath: groupPath,
	}
	for _, ref := range e.Binaries {
		info.Attachments = append(info.Attachments, ref.Name)
	}
	for _, v := range e.Values {
		if !standardKeys[v.Key] {
			info.CustomKeys = append(info.CustomKeys, v.Key)
		}
	}
	return info
}
