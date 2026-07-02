// Package mobaxterm imports MobaXterm session exports (.mxtsessions).
//
// The format is INI-flavoured: every [Bookmarks] / [Bookmarks_N]
// section is one folder. SubRep carries the folder path (backslash
// separated, relative to the export root); every other key=value pair
// in the section (except ImgNum) is one session. The value packs the
// session type and its settings:
//
//	name=#109#0%host%port%username%...many more %-separated fields...
//
// #109# is the SSH session type; other numbers are telnet/RDP/VNC/...
// which we skip. Field positions past host/port/user vary between
// MobaXterm versions, so only those three are read. Passwords are not
// in the export at all (MobaXterm keeps them in a separate store).
package mobaxterm

import (
	"fmt"
	"strconv"
	"strings"

	"ssh-tool/internal/store"
)

// Entry is one parsed SSH session.
type Entry struct {
	Name   string
	Folder string // backslash-separated SubRep path; "" = root
	Host   string
	Port   uint16
	User   string
}

// Summary mirrors the other importers' shape so the UI renders all of
// them the same way.
type Summary struct {
	FoldersCreated     int      `json:"folders_created"`
	ConnectionsCreated int      `json:"connections_created"`
	ConnectionsSkipped int      `json:"connections_skipped"`
	SkippedNonSSH      int      `json:"skipped_non_ssh"`
	Warnings           []string `json:"warnings"`
}

// Parse extracts SSH sessions from .mxtsessions text. Non-SSH session
// types are counted in the summary, not returned.
func Parse(text string) ([]Entry, *Summary, error) {
	sum := &Summary{}
	var entries []Entry
	folder := ""
	inBookmarks := false

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section := strings.Trim(trimmed, "[]")
			inBookmarks = section == "Bookmarks" || strings.HasPrefix(section, "Bookmarks_")
			folder = ""
			continue
		}
		if !inBookmarks {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "SubRep":
			folder = strings.Trim(value, "\\")
			continue
		case "ImgNum":
			continue
		}
		if key == "" || value == "" {
			continue
		}

		// Session value: #<type>#<icon>%field%field%...
		if !strings.HasPrefix(value, "#") {
			continue
		}
		rest := value[1:]
		typeStr, rest, ok := strings.Cut(rest, "#")
		if !ok {
			continue
		}
		if typeStr != "109" {
			sum.SkippedNonSSH++
			continue
		}
		_, rest, ok = strings.Cut(rest, "%") // drop the icon field
		if !ok {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s: malformed session value", key))
			continue
		}
		fields := strings.Split(rest, "%")
		if len(fields) < 1 || strings.TrimSpace(fields[0]) == "" {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s: no hostname", key))
			continue
		}
		e := Entry{
			Name:   key,
			Folder: folder,
			Host:   strings.TrimSpace(fields[0]),
			Port:   22,
		}
		if len(fields) > 1 {
			if p, err := strconv.Atoi(strings.TrimSpace(fields[1])); err == nil && p > 0 && p <= 65535 {
				e.Port = uint16(p)
			}
		}
		if len(fields) > 2 {
			e.User = strings.TrimSpace(fields[2])
		}
		entries = append(entries, e)
	}

	if len(entries) == 0 && sum.SkippedNonSSH == 0 {
		return nil, nil, fmt.Errorf("no MobaXterm sessions found - is this a .mxtsessions export?")
	}
	return entries, sum, nil
}

// Apply creates the folder tree (SubRep paths) and connections under
// rootFolderID ("" = DB root). Existing connection names are skipped;
// existing folders along a path are reused, not duplicated.
func Apply(db *store.DB, entries []Entry, sum *Summary, rootFolderID string) (*Summary, error) {
	existing, err := db.ListConnections(nil)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	nameTaken := map[string]bool{}
	for _, c := range existing {
		nameTaken[c.Name] = true
	}

	// Existing folders keyed by (parentID, name) so re-imports reuse
	// the tree instead of stacking duplicates.
	folders, err := db.ListFolders()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	type fkey struct{ parent, name string }
	folderByKey := map[fkey]string{}
	for _, f := range folders {
		parent := ""
		if f.ParentID != nil {
			parent = *f.ParentID
		}
		folderByKey[fkey{parent, f.Name}] = f.ID
	}

	// ensurePath walks/creates one SubRep path and returns the leaf
	// folder ID. Paths are cached per import run.
	pathCache := map[string]string{}
	ensurePath := func(path string) (string, error) {
		if path == "" {
			return rootFolderID, nil
		}
		if id, ok := pathCache[path]; ok {
			return id, nil
		}
		parent := rootFolderID
		for _, seg := range strings.Split(path, "\\") {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			if id, ok := folderByKey[fkey{parent, seg}]; ok {
				parent = id
				continue
			}
			in := store.NewFolder{Name: seg}
			if parent != "" {
				p := parent
				in.ParentID = &p
			}
			f, err := db.CreateFolder(in)
			if err != nil {
				return "", fmt.Errorf("create folder %s: %w", seg, err)
			}
			folderByKey[fkey{parent, seg}] = f.ID
			parent = f.ID
			sum.FoldersCreated++
		}
		pathCache[path] = parent
		return parent, nil
	}

	for _, e := range entries {
		if nameTaken[e.Name] {
			sum.ConnectionsSkipped++
			continue
		}
		folderID, err := ensurePath(e.Folder)
		if err != nil {
			sum.Warnings = append(sum.Warnings, err.Error())
			continue
		}
		overrides := store.InheritableSettings{}
		if e.User != "" {
			u := e.User
			overrides.Username = &u
		}
		if e.Port != 22 {
			p := e.Port
			overrides.Port = &p
		}
		in := store.NewConnection{
			Name:      e.Name,
			Hostname:  e.Host,
			Overrides: overrides,
		}
		if folderID != "" {
			f := folderID
			in.FolderID = &f
		}
		if _, err := db.CreateConnection(in); err != nil {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("create %s: %v", e.Name, err))
			continue
		}
		sum.ConnectionsCreated++
		nameTaken[e.Name] = true
	}
	return sum, nil
}
