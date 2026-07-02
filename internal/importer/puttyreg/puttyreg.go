// Package puttyreg imports PuTTY sessions from a Windows registry
// export (.reg). PuTTY has no export UI of its own; the standard
// migration path is:
//
//	reg export HKCU\Software\SimonTatham\PuTTY\Sessions putty.reg
//
// Each [...\Sessions\<name>] section is one session; the name is
// PuTTY-%XX-encoded (space = %20). Only Protocol=ssh sessions are
// imported. KiTTY exports (9bis.com\KiTTY\Sessions) use the same
// layout and parse identically. PuTTY never stores passwords, so
// there are no credentials to carry.
//
// reg.exe writes .reg files as UTF-16LE; the frontend's FileReader
// decodes that via the BOM before the text reaches us, but Decode()
// also handles raw UTF-16 bytes for safety.
package puttyreg

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"ssh-tool/internal/store"
)

// Entry is one parsed SSH session.
type Entry struct {
	Name string
	Host string
	Port uint16
	User string
}

// Summary mirrors the other importers' shape.
type Summary struct {
	ConnectionsCreated int      `json:"connections_created"`
	ConnectionsSkipped int      `json:"connections_skipped"`
	SkippedNonSSH      int      `json:"skipped_non_ssh"`
	Warnings           []string `json:"warnings"`
}

// Decode normalises raw .reg bytes to text: UTF-16LE/BE (reg.exe
// default, BOM-detected) or UTF-8 (with or without BOM).
func Decode(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return decodeUTF16(raw[2:], false)
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return decodeUTF16(raw[2:], true)
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return string(raw[3:])
	}
	return string(raw)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
	}
	return string(utf16.Decode(u))
}

// unescapeName reverses PuTTY's %XX session-name encoding.
func unescapeName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unquoteValue strips the quotes of a .reg string value and undoes
// its escaping (\\ and \").
func unquoteValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, `\\`, "\x00")
	s = strings.ReplaceAll(s, `\"`, `"`)
	return strings.ReplaceAll(s, "\x00", `\`)
}

// Parse extracts SSH sessions from .reg text.
func Parse(text string) ([]Entry, *Summary, error) {
	sum := &Summary{}
	var entries []Entry

	type session struct {
		name     string
		host     string
		port     uint16
		user     string
		protocol string
	}
	var cur *session
	flush := func() {
		if cur == nil {
			return
		}
		s := *cur
		cur = nil
		if s.name == "Default Settings" {
			return
		}
		if s.protocol != "ssh" {
			if s.protocol != "" || s.host != "" {
				sum.SkippedNonSSH++
			}
			return
		}
		if s.host == "" {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s: no HostName, skipped", s.name))
			return
		}
		// PuTTY allows user@host in the HostName box.
		if u, h, ok := strings.Cut(s.host, "@"); ok && s.user == "" && u != "" {
			s.user, s.host = u, h
		}
		if s.port == 0 {
			s.port = 22
		}
		entries = append(entries, Entry{Name: s.name, Host: s.host, Port: s.port, User: s.user})
	}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			path := strings.Trim(line, "[]")
			// Any hive path ending in \Sessions\<name> qualifies -
			// covers PuTTY, KiTTY and portable variants.
			idx := strings.LastIndex(path, `\Sessions\`)
			if idx < 0 {
				continue
			}
			name := path[idx+len(`\Sessions\`):]
			if name == "" || strings.Contains(name, `\`) {
				continue
			}
			cur = &session{name: unescapeName(name)}
			continue
		}
		if cur == nil {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"`)
		switch key {
		case "HostName":
			cur.host = unquoteValue(value)
		case "UserName":
			cur.user = unquoteValue(value)
		case "Protocol":
			cur.protocol = strings.ToLower(unquoteValue(value))
		case "PortNumber":
			v := strings.TrimSpace(value)
			if hexStr, ok := strings.CutPrefix(v, "dword:"); ok {
				if p, err := strconv.ParseUint(hexStr, 16, 32); err == nil && p > 0 && p <= 65535 {
					cur.port = uint16(p)
				}
			}
		}
	}
	flush()

	if len(entries) == 0 && sum.SkippedNonSSH == 0 {
		return nil, nil, fmt.Errorf("no PuTTY sessions found - export with: reg export \"HKCU\\Software\\SimonTatham\\PuTTY\\Sessions\" putty.reg")
	}
	return entries, sum, nil
}

// Apply creates the connections flat under rootFolderID ("" = DB
// root). PuTTY has no folder concept, so there is no tree to rebuild.
// Existing connection names are skipped.
func Apply(db *store.DB, entries []Entry, sum *Summary, rootFolderID string) (*Summary, error) {
	existing, err := db.ListConnections(nil)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	nameTaken := map[string]bool{}
	for _, c := range existing {
		nameTaken[c.Name] = true
	}

	for _, e := range entries {
		if nameTaken[e.Name] {
			sum.ConnectionsSkipped++
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
		if rootFolderID != "" {
			f := rootFolderID
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
