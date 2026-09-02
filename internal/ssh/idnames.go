package ssh

// uid/gid -> name resolution for the SFTP browser.
//
// SFTP protocol v3 carries numeric ids only; there is no name lookup in the
// protocol itself. Rather than shell out per listing, we read /etc/passwd and
// /etc/group once per session over the SFTP channel we already have open and
// cache the maps. That works on hosts where an exec channel is refused
// (ForceCommand=internal-sftp, restricted shells), which is exactly the sort
// of host people browse over SFTP.
//
// Directory-backed accounts (LDAP/SSSD/AD) do not appear in those files, so
// some ids will stay unresolved. That is reported honestly by returning no
// name rather than guessing - the UI falls back to the number.

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"sync"
)

// idNames holds one session's resolved id -> name maps. A nil map means the
// file could not be read; an empty (non-nil) map means it was read and simply
// had nothing useful, so we don't retry on every listing.
type idNames struct {
	mu     sync.Mutex
	loaded bool
	users  map[int64]string
	groups map[int64]string
}

// Maximum bytes we will read from either file. Large enough for a very big
// /etc/passwd, small enough that a hostile or broken server cannot make us
// buffer a whole disk.
const maxIDFileBytes = 4 << 20

// parseIDFile parses a colon-separated passwd/group file: name in field 0,
// numeric id in field idField. Malformed lines are skipped rather than
// failing the whole parse - one bad line should not cost every other name.
func parseIDFile(r io.Reader, idField int) map[int64]string {
	out := make(map[int64]string)
	sc := bufio.NewScanner(io.LimitReader(r, maxIDFileBytes))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) <= idField {
			continue
		}
		name := parts[0]
		if name == "" {
			continue
		}
		id, err := strconv.ParseInt(parts[idField], 10, 64)
		if err != nil {
			continue
		}
		// First definition wins: that matches getpwuid() on a file with
		// duplicate ids, and keeps "root" from being shadowed by an alias.
		if _, seen := out[id]; !seen {
			out[id] = name
		}
	}
	return out
}

// loadIDNames reads /etc/passwd and /etc/group over SFTP. Errors are not
// propagated: an unreadable file (permissions, a non-POSIX server, a Windows
// SFTP server) just means ids stay numeric, which the UI already handles.
func (s *Session) loadIDNames() {
	if s.idNames == nil {
		s.idNames = &idNames{}
	}
	c := s.idNames
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return
	}
	c.loaded = true

	cli, err := s.SFTPClient()
	if err != nil {
		return
	}
	read := func(p string, idField int) map[int64]string {
		f, err := cli.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		return parseIDFile(f, idField)
	}
	// passwd: name:x:uid:gid:...   group: name:x:gid:members
	c.users = read("/etc/passwd", 2)
	c.groups = read("/etc/group", 2)
}

// ResolveIDNames fills Owner/Group on the given entries, in place. Safe to
// call on any host: when the files are unreadable the names stay empty and
// the frontend renders the numeric ids it already has.
func (s *Session) ResolveIDNames(entries []SftpEntry) {
	if len(entries) == 0 {
		return
	}
	s.loadIDNames()
	c := s.idNames
	c.mu.Lock()
	users, groups := c.users, c.groups
	c.mu.Unlock()
	if users == nil && groups == nil {
		return
	}
	for i := range entries {
		if entries[i].UID >= 0 {
			if n, ok := users[entries[i].UID]; ok {
				entries[i].Owner = n
			}
		}
		if entries[i].GID >= 0 {
			if n, ok := groups[entries[i].GID]; ok {
				entries[i].Group = n
			}
		}
	}
}
