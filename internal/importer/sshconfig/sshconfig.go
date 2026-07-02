// Package sshconfig parses an OpenSSH client config (~/.ssh/config or
// equivalent) and writes the resulting Host entries into the store as
// connections. Wildcard hosts ("*", "*.internal") are skipped - they
// describe defaults across many hosts, not a single connectable target.
//
// Mapping
// -------
//   Host alias        -> Connection.Name (and Hostname if no HostName)
//   HostName x        -> Connection.Hostname
//   User x            -> Overrides.Username
//   Port n            -> Overrides.Port
//   ProxyJump a,b,c   -> Overrides.JumpHost chain (linear: a -> b -> c)
//   IdentityFile p    -> Connection.Notes (informational; we never
//                        slurp private keys off disk automatically)
//
// All other keywords are recorded as Notes too so the user can decide
// whether to migrate them.

package sshconfig

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"ssh-tool/internal/store"
)

// Entry is a parsed Host block, accumulated from one or more `Host`
// lines that share the same alias. (OpenSSH lets you repeat `Host` -
// later directives layer on top.)
type Entry struct {
	Alias      string
	HostName   string
	User       string
	Port       int
	ProxyJump  []string // already split on commas, leftmost = first hop
	IdentFile  []string
	OtherLines []string
}

// Parse reads the config text and returns one Entry per (non-wildcard)
// Host block. Order matches the file so the resulting connections sit
// in a predictable spot.
func Parse(text string) ([]Entry, error) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	var entries []Entry
	var cur *Entry

	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val := splitKV(line)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "Host") {
			// One Host directive can name multiple aliases; we emit
			// one Entry per alias so each becomes its own connection.
			// Wildcard-only aliases are dropped entirely.
			aliases := strings.Fields(val)
			for _, a := range aliases {
				if strings.ContainsAny(a, "*?!") {
					continue
				}
				e := Entry{Alias: a}
				entries = append(entries, e)
			}
			if len(entries) == 0 {
				cur = nil
				continue
			}
			cur = &entries[len(entries)-1]
			continue
		}
		if cur == nil {
			// Directive before any Host - global default. Ignore for now.
			continue
		}
		switch strings.ToLower(key) {
		case "hostname":
			cur.HostName = val
		case "user":
			cur.User = val
		case "port":
			if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= 65535 {
				cur.Port = n
			}
		case "proxyjump":
			for _, hop := range strings.Split(val, ",") {
				hop = strings.TrimSpace(hop)
				if hop != "" && !strings.EqualFold(hop, "none") {
					cur.ProxyJump = append(cur.ProxyJump, hop)
				}
			}
		case "identityfile":
			cur.IdentFile = append(cur.IdentFile, val)
		default:
			cur.OtherLines = append(cur.OtherLines, raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// splitKV understands both "Key Value" and "Key = Value".
func splitKV(line string) (string, string) {
	if i := strings.IndexAny(line, " \t="); i >= 0 {
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(strings.TrimLeft(line[i:], " \t="))
		return k, v
	}
	return line, ""
}

// Summary mirrors the RDM import summary shape so the UI can reuse the
// same rendering pattern.
type Summary struct {
	ConnectionsCreated int      `json:"connections_created"`
	ConnectionsSkipped int      `json:"connections_skipped"`
	JumpResolved       int      `json:"jump_resolved"`
	JumpUnresolved     []string `json:"jump_unresolved"`
	IdentityFilesNoted int      `json:"identity_files_noted"`
	Warnings           []string `json:"warnings"`
}

// Apply persists the parsed entries into db. Connections land under
// rootFolderID (empty string = DB root). Existing connections with the
// same Name are left alone.
//
// ProxyJump is resolved against the same import batch: a hop name must
// match another entry's Alias, otherwise it's reported in
// Summary.JumpUnresolved and the connection lands with no jump chain.
func Apply(db *store.DB, entries []Entry, rootFolderID string) (*Summary, error) {
	sum := &Summary{}

	// Snapshot existing connection names so we can detect duplicates.
	existing, err := db.ListConnections(nil)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	nameTaken := map[string]bool{}
	for _, c := range existing {
		nameTaken[c.Name] = true
	}

	// Build a fast lookup so ProxyJump can resolve to a hop spec.
	byAlias := map[string]Entry{}
	for _, e := range entries {
		byAlias[e.Alias] = e
	}

	// Helper that walks a ProxyJump chain (leftmost-first) and returns
	// the head spec. Each unresolved hop falls back to using the hop
	// string verbatim as a hostname (preserves intent even when the
	// gateway isn't itself in the config).
	var buildChain func(hops []string) *store.JumpHostSpec
	buildChain = func(hops []string) *store.JumpHostSpec {
		if len(hops) == 0 {
			return nil
		}
		head := hops[0]
		tail := hops[1:]

		spec := &store.JumpHostSpec{}
		if e, ok := byAlias[head]; ok {
			sum.JumpResolved++
			hn := e.HostName
			if hn == "" {
				hn = e.Alias
			}
			spec.Hostname = hn
			if e.User != "" {
				spec.Username = strPtr(e.User)
			}
			if e.Port > 0 {
				p := uint16(e.Port)
				spec.Port = &p
			}
		} else {
			sum.JumpUnresolved = append(sum.JumpUnresolved, head)
			// Treat the hop as a raw hostname so we don't silently
			// drop it - better wrong port than no chain at all.
			spec.Hostname = head
		}
		spec.Via = buildChain(tail)
		return spec
	}

	for _, e := range entries {
		name := e.Alias
		if nameTaken[name] {
			sum.ConnectionsSkipped++
			continue
		}

		host := e.HostName
		if host == "" {
			host = e.Alias
		}

		overrides := store.InheritableSettings{}
		if e.User != "" {
			overrides.Username = strPtr(e.User)
		}
		if e.Port > 0 {
			p := uint16(e.Port)
			overrides.Port = &p
		}
		if chain := buildChain(e.ProxyJump); chain != nil {
			overrides.JumpHost = &store.JumpHostOverride{Kind: "chain", Chain: chain}
		}

		notes := buildNotes(e)
		if len(e.IdentFile) > 0 {
			sum.IdentityFilesNoted++
		}

		input := store.NewConnection{
			Name:      name,
			Hostname:  host,
			Overrides: overrides,
			Notes:     notes,
		}
		if rootFolderID != "" {
			input.FolderID = strPtr(rootFolderID)
		}
		if _, err := db.CreateConnection(input); err != nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("create %s: %v", name, err))
			continue
		}
		sum.ConnectionsCreated++
		nameTaken[name] = true
	}

	return sum, nil
}

// buildNotes assembles a human-readable block that records the bits we
// didn't fully translate - IdentityFile paths, any unknown directives.
// Users can copy the parts they actually need into the connection
// editor without us bypassing vault security by slurping keys.
func buildNotes(e Entry) string {
	var b strings.Builder
	if len(e.IdentFile) > 0 {
		b.WriteString("IdentityFile (not auto-imported - security):\n")
		for _, p := range e.IdentFile {
			b.WriteString("  - ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	if len(e.OtherLines) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Other ssh_config directives:\n")
		for _, l := range e.OtherLines {
			b.WriteString("  ")
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func strPtr(s string) *string { return &s }
