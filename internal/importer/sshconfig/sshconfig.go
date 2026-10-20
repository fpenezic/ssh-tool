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
	"net"
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
	// cur is the entries the current block applies to: every alias of
	// its Host line (a repeated alias layers onto its first entry). A
	// wildcard-only Host or a Match block applies to none of them; their
	// directives used to land on the host above.
	var cur []int
	byAlias := map[string]int{}

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
		if strings.EqualFold(key, "Match") {
			cur = nil // conditional block: not imported
			continue
		}
		if strings.EqualFold(key, "Host") {
			// One Host directive can name multiple aliases; we emit
			// one Entry per alias so each becomes its own connection.
			// Wildcard aliases are dropped.
			cur = nil
			for _, a := range strings.Fields(val) {
				if strings.ContainsAny(a, "*?!") {
					continue
				}
				i, ok := byAlias[a]
				if !ok {
					entries = append(entries, Entry{Alias: a})
					i = len(entries) - 1
					byAlias[a] = i
				}
				cur = append(cur, i)
			}
			continue
		}
		// Before any Host (global defaults) cur is empty: ignored for now.
		for _, i := range cur {
			applyDirective(&entries[i], key, val, raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// applyDirective sets one directive on an entry. As in OpenSSH the first
// value read wins for single-valued keys; IdentityFile accumulates.
func applyDirective(e *Entry, key, val, raw string) {
	switch strings.ToLower(key) {
	case "hostname":
		if e.HostName == "" {
			e.HostName = val
		}
	case "user":
		if e.User == "" {
			e.User = val
		}
	case "port":
		if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= 65535 && e.Port == 0 {
			e.Port = n
		}
	case "proxyjump":
		if len(e.ProxyJump) > 0 {
			return
		}
		for _, hop := range strings.Split(val, ",") {
			hop = strings.TrimSpace(hop)
			if hop != "" && !strings.EqualFold(hop, "none") {
				e.ProxyJump = append(e.ProxyJump, hop)
			}
		}
	case "identityfile":
		e.IdentFile = append(e.IdentFile, val)
	default:
		e.OtherLines = append(e.OtherLines, raw)
	}
}

// splitHop reads a ProxyJump hop, [user@]host[:port] (host may be a
// bracketed IPv6 address).
func splitHop(hop string) (user, host string, port uint16) {
	host = hop
	if i := strings.LastIndex(host, "@"); i > 0 {
		user, host = host[:i], host[i+1:]
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			return user, h, uint16(n)
		}
		return user, h, 0
	}
	return user, strings.Trim(host, "[]"), 0
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
			// Not an alias: take it as ProxyJump's [user@]host[:port]
			// rather than drop the hop.
			user, host, port := splitHop(head)
			spec.Hostname = host
			if user != "" {
				spec.Username = strPtr(user)
			}
			if port > 0 {
				spec.Port = &port
			}
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
