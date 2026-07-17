// Package superputty imports SuperPuTTY session exports (Sessions.xml).
//
// SuperPuTTY stores its sessions as an XML document:
//
//	<ArrayOfSessionData>
//	  <SessionData SessionId="prod/web/app1" SessionName="app1"
//	    Host="10.0.0.1" Port="22" Username="deploy" Proto="SSH" .../>
//	  ...
//	</ArrayOfSessionData>
//
// SessionId is a slash-separated path whose last segment is the session; the
// folder is everything before it (SessionName is the leaf-name fallback). Only
// Proto="SSH" sessions are imported; RDP/Telnet/... are counted and skipped.
// SuperPuTTY stores no passwords, so nothing secret is lost.
package superputty

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"ssh-tool/internal/store"
)

// Entry is one parsed SSH session (shape shared with the other importers).
type Entry struct {
	Name   string
	Folder string // backslash-separated path, "" = root (matches Apply's ensurePath)
	Host   string
	Port   uint16
	User   string
}

// Summary mirrors the other importers' shape so the UI renders them the same.
type Summary struct {
	FoldersCreated     int      `json:"folders_created"`
	ConnectionsCreated int      `json:"connections_created"`
	ConnectionsSkipped int      `json:"connections_skipped"`
	SkippedNonSSH      int      `json:"skipped_non_ssh"`
	Warnings           []string `json:"warnings"`
}

// xmlSessionData maps the SessionData element's attributes.
type xmlSessionData struct {
	SessionID   string `xml:"SessionId,attr"`
	SessionName string `xml:"SessionName,attr"`
	Host        string `xml:"Host,attr"`
	Port        string `xml:"Port,attr"`
	Username    string `xml:"Username,attr"`
	Proto       string `xml:"Proto,attr"`
}

type xmlSessions struct {
	Sessions []xmlSessionData `xml:"SessionData"`
}

// Parse extracts SSH sessions from a Sessions.xml document. Non-SSH session
// types are counted in the summary, not returned.
func Parse(text string) ([]Entry, *Summary, error) {
	sum := &Summary{}
	var doc xmlSessions
	if err := xml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, nil, fmt.Errorf("parse Sessions.xml: %w", err)
	}

	var entries []Entry
	for _, s := range doc.Sessions {
		if !strings.EqualFold(strings.TrimSpace(s.Proto), "SSH") {
			sum.SkippedNonSSH++
			continue
		}
		folder, name := splitSessionID(s.SessionID, s.SessionName)
		if name == "" {
			// A session with no usable name is not worth importing.
			sum.Warnings = append(sum.Warnings, "skipped a session with no name")
			continue
		}
		port := uint16(22)
		if p := strings.TrimSpace(s.Port); p != "" {
			if n, err := strconv.ParseUint(p, 10, 16); err == nil && n > 0 {
				port = uint16(n)
			}
		}
		entries = append(entries, Entry{
			Name:   name,
			Folder: folder,
			Host:   strings.TrimSpace(s.Host),
			Port:   port,
			User:   strings.TrimSpace(s.Username),
		})
	}
	return entries, sum, nil
}

// splitSessionID turns a slash-separated SessionId into a backslash folder path
// (for Apply's ensurePath) and a leaf name. The last path segment is the name
// unless a SessionName is given, which wins. "prod/web/app1" -> ("prod\\web",
// "app1").
func splitSessionID(sessionID, sessionName string) (folder, name string) {
	segs := []string{}
	for _, s := range strings.Split(sessionID, "/") {
		if s = strings.TrimSpace(s); s != "" {
			segs = append(segs, s)
		}
	}
	name = strings.TrimSpace(sessionName)
	if len(segs) > 0 {
		if name == "" {
			name = segs[len(segs)-1]
		}
		folder = strings.Join(segs[:len(segs)-1], "\\")
	}
	return folder, name
}

// Apply materialises the parsed entries into the store: it walks/creates the
// folder tree under rootFolderID and creates one connection per entry, skipping
// name collisions. Mirrors the other importers' Apply.
func Apply(db *store.DB, entries []Entry, sum *Summary, rootFolderID string) (*Summary, error) {
	existing, err := db.ListConnections(nil)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	nameTaken := map[string]bool{}
	for _, c := range existing {
		nameTaken[c.Name] = true
	}

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
