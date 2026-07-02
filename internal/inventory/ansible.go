// Ansible static-inventory provider.
//
// Config (`config_json` on `dynamic_folders`):
//
//	{
//	  "path": "/abs/path/to/hosts.ini",   // or hosts.yml
//	  "host_pattern": "web*",              // optional fnmatch on host name
//	  "group_pattern": "prod_*",           // optional fnmatch on group name
//	  "name_from": "inventory_hostname" | "ansible_host" | "var:custom_name"
//	}
//
// Parses both the classic INI form (`[group]` sections with
// `host k=v` lines) and the YAML form (nested `children:` /
// `hosts:`). group_vars / host_vars side directories are NOT
// honoured - keep it simple. Ansible host variables that
// translate to SSH settings (ansible_user, ansible_port,
// ansible_host, ansible_ssh_common_args) survive in the entry's
// Raw payload so the connect flow can lift them into overrides.
//
// Folder structure is intentionally flat: every host becomes a
// single entry under the dynamic folder, with the group names as
// tags. Matches the user's mental model "I have N hosts; tag
// them for filter" better than mirroring Ansible's DAG.
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Ansible struct{}

func (Ansible) Name() string { return "ansible" }

// ansibleHost is the merged view of one host after walking the
// inventory file: vars from every group it belongs to, with host-
// level vars winning over group-level. Marshaled into Entry.Raw as
// the "raw" payload the UI / connect flow can read later.
type ansibleHost struct {
	Name   string            `json:"name"`
	Groups []string          `json:"groups"`
	Vars   map[string]string `json:"vars"`
}

func (Ansible) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	path, _ := cfg["path"].(string)
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("ansible: path is required")
	}
	hostPattern, _ := cfg["host_pattern"].(string)
	groupPattern, _ := cfg["group_pattern"].(string)
	nameFrom, _ := cfg["name_from"].(string)
	if nameFrom == "" {
		nameFrom = "inventory_hostname"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ansible: read %s: %w", path, err)
	}

	hosts, err := parseAnsibleInventory(data, filepath.Ext(path))
	if err != nil {
		return nil, fmt.Errorf("ansible: parse %s: %w", path, err)
	}

	out := make([]Entry, 0, len(hosts))
	for _, h := range hosts {
		if !matchAnsiblePattern(hostPattern, h.Name) {
			continue
		}
		if groupPattern != "" {
			any := false
			for _, g := range h.Groups {
				if matchAnsiblePattern(groupPattern, g) {
					any = true
					break
				}
			}
			if !any {
				continue
			}
		}
		entry, err := buildAnsibleEntry(h, nameFrom)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	// Stable order by display name for diff-friendly refreshes.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func buildAnsibleEntry(h ansibleHost, nameFrom string) (Entry, error) {
	display := pickAnsibleName(h, nameFrom)
	hostname := h.Vars["ansible_host"]
	if hostname == "" {
		hostname = h.Name
	}
	tags := append([]string{}, h.Groups...)
	sort.Strings(tags)

	raw, err := json.Marshal(h)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		ExternalID: h.Name,
		Name:       display,
		Hostname:   hostname,
		Kind:       KindServer,
		Status:     "",
		Tags:       tags,
		Raw:        raw,
	}, nil
}

func pickAnsibleName(h ansibleHost, nameFrom string) string {
	switch {
	case nameFrom == "inventory_hostname":
		return h.Name
	case nameFrom == "ansible_host":
		if v := h.Vars["ansible_host"]; v != "" {
			return v
		}
		return h.Name
	case strings.HasPrefix(nameFrom, "var:"):
		key := strings.TrimPrefix(nameFrom, "var:")
		if v := h.Vars[key]; v != "" {
			return v
		}
		return h.Name
	default:
		return h.Name
	}
}

// matchAnsiblePattern compares pattern to s using fnmatch-style
// globs (*, ?, [chars]). Empty pattern matches everything. Used for
// both host and group filters.
func matchAnsiblePattern(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	// filepath.Match implements fnmatch semantics, which is exactly
	// what Ansible's host/group patterns use for the simple cases
	// we support.
	ok, err := filepath.Match(pattern, s)
	if err != nil {
		// Bad pattern - fall back to substring match so the user
		// gets some hits instead of an empty list and no feedback.
		return strings.Contains(s, pattern)
	}
	return ok
}

// ---------- parsers ----------

// parseAnsibleInventory dispatches on file extension. Anything other
// than .yml / .yaml falls through to the INI parser, which mirrors
// Ansible's own behaviour (the YAML parser is opt-in via extension).
func parseAnsibleInventory(data []byte, ext string) ([]ansibleHost, error) {
	switch strings.ToLower(ext) {
	case ".yml", ".yaml":
		return parseAnsibleYAML(data)
	default:
		return parseAnsibleINI(data)
	}
}

// ---------- INI parser ----------
//
// Format:
//
//   [webservers]
//   web1.example.com ansible_user=deploy ansible_port=2222
//   web2.example.com
//
//   [webservers:vars]
//   ansible_user=deploy
//
//   [all:children]
//   webservers
//   dbservers
//
// We intentionally ignore include/import directives and the
// "implicit all/ungrouped" semantics - Ansible's own behaviour is
// quirky and we'd rather under-parse than wrong-parse.

var iniSection = regexp.MustCompile(`^\[([^\]]+)\]\s*$`)

// groupBlob is the intermediate per-group state the INI parser
// builds before materialiseAnsibleHosts walks it into host records.
type groupBlob struct {
	hosts    map[string]map[string]string // host -> per-host vars
	vars     map[string]string            // group:vars block
	children []string                     // group:children members
}

func parseAnsibleINI(data []byte) ([]ansibleHost, error) {
	groups := map[string]*groupBlob{}
	ensureGroup := func(name string) *groupBlob {
		if g, ok := groups[name]; ok {
			return g
		}
		g := &groupBlob{
			hosts: map[string]map[string]string{},
			vars:  map[string]string{},
		}
		groups[name] = g
		return g
	}

	var (
		current = "ungrouped"
		mode    = "hosts" // hosts | vars | children
	)
	ensureGroup(current)

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if m := iniSection.FindStringSubmatch(line); m != nil {
			name := strings.TrimSpace(m[1])
			switch {
			case strings.HasSuffix(name, ":vars"):
				current = strings.TrimSuffix(name, ":vars")
				mode = "vars"
			case strings.HasSuffix(name, ":children"):
				current = strings.TrimSuffix(name, ":children")
				mode = "children"
			default:
				current = name
				mode = "hosts"
			}
			ensureGroup(current)
			continue
		}
		g := groups[current]
		switch mode {
		case "hosts":
			host, vars := parseINIHostLine(line)
			if host == "" {
				continue
			}
			if existing, ok := g.hosts[host]; ok {
				for k, v := range vars {
					existing[k] = v
				}
			} else {
				g.hosts[host] = vars
			}
		case "vars":
			if k, v, ok := splitKV(line); ok {
				g.vars[k] = v
			}
		case "children":
			g.children = append(g.children, line)
		}
	}

	return materialiseAnsibleHosts(groups), nil
}

func parseINIHostLine(line string) (string, map[string]string) {
	// Host line: "name[ k=v[ k=v...]]". Quoted values can hold spaces.
	tokens, err := splitFields(line)
	if err != nil || len(tokens) == 0 {
		return "", nil
	}
	host := tokens[0]
	vars := map[string]string{}
	for _, t := range tokens[1:] {
		if k, v, ok := splitKV(t); ok {
			vars[k] = v
		}
	}
	return host, vars
}

func splitKV(s string) (string, string, bool) {
	idx := strings.Index(s, "=")
	if idx < 1 {
		return "", "", false
	}
	k := strings.TrimSpace(s[:idx])
	v := strings.TrimSpace(s[idx+1:])
	// Strip matching single or double quotes - Ansible vars often
	// quote values with spaces (ProxyCommand="ssh -W %h:%p bastion").
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			v = v[1 : len(v)-1]
		}
	}
	return k, v, true
}

// splitFields is a tiny shlex: splits on whitespace but treats
// quoted runs as one token. Enough for typical inventory lines.
func splitFields(s string) ([]string, error) {
	out := []string{}
	var (
		cur   strings.Builder
		quote byte = 0
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

// materialiseAnsibleHosts walks the parsed group structure and emits
// one ansibleHost per unique host, with vars merged from every
// ancestor group it transitively belongs to. Host-level vars win
// over group vars; deeper groups (child) win over their parents.
// `all` is implicit; we treat any group not appearing as a child of
// another as a root.
func materialiseAnsibleHosts(groups map[string]*groupBlob) []ansibleHost {
	type host struct {
		name       string
		groupChain []string
		vars       map[string]string
	}
	hosts := map[string]*host{}
	upsertHost := func(name, group string) *host {
		h, ok := hosts[name]
		if !ok {
			h = &host{name: name, vars: map[string]string{}}
			hosts[name] = h
		}
		// Track membership (preserve insertion order; we sort later).
		for _, g := range h.groupChain {
			if g == group {
				return h
			}
		}
		h.groupChain = append(h.groupChain, group)
		return h
	}

	// Recursively expand children. Cap depth to defang cycles.
	var expand func(group string, ancestors []string, depth int)
	expand = func(group string, ancestors []string, depth int) {
		if depth > 32 {
			return
		}
		g, ok := groups[group]
		if !ok {
			return
		}
		chain := append(ancestors, group)
		// Direct host members.
		for hName, hVars := range g.hosts {
			h := upsertHost(hName, group)
			// Walk chain root-first so group_vars cascade and host
			// vars come last (winning).
			for _, ancestor := range chain {
				ag := groups[ancestor]
				if ag == nil {
					continue
				}
				for k, v := range ag.vars {
					h.vars[k] = v
				}
			}
			for k, v := range hVars {
				h.vars[k] = v
			}
			// Make every ancestor a membership too so tags reflect
			// the full Ansible group set, not just the leaf.
			for _, ancestor := range ancestors {
				upsertHost(hName, ancestor)
			}
		}
		for _, child := range g.children {
			expand(child, chain, depth+1)
		}
	}
	for name := range groups {
		expand(name, nil, 0)
	}

	out := make([]ansibleHost, 0, len(hosts))
	for _, h := range hosts {
		groupsCopy := append([]string{}, h.groupChain...)
		sort.Strings(groupsCopy)
		out = append(out, ansibleHost{
			Name:   h.name,
			Groups: groupsCopy,
			Vars:   h.vars,
		})
	}
	return out
}

// ---------- YAML parser ----------
//
// Format (recursive):
//
//   all:
//     children:
//       webservers:
//         hosts:
//           web1.example.com:
//             ansible_user: deploy
//             ansible_port: 2222
//         vars:
//           env: prod
//       dbservers:
//         hosts:
//           db1.example.com: {}
//
// We ignore the top-level key (`all` by convention) and walk
// children/hosts recursively. Vars cascade the same as INI.

func parseAnsibleYAML(data []byte) ([]ansibleHost, error) {
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	hosts := map[string]*ansibleHost{}
	for groupName, raw := range root {
		walkYAMLGroup(groupName, raw, []string{}, map[string]string{}, hosts, 0)
	}
	out := make([]ansibleHost, 0, len(hosts))
	for _, h := range hosts {
		sort.Strings(h.Groups)
		out = append(out, *h)
	}
	return out, nil
}

func walkYAMLGroup(name string, raw any, ancestors []string, inheritedVars map[string]string, hosts map[string]*ansibleHost, depth int) {
	if depth > 32 {
		return
	}
	chain := append(append([]string{}, ancestors...), name)
	body, ok := raw.(map[string]any)
	if !ok {
		return
	}
	// Merge vars from this level into a fresh map so siblings don't
	// see each other's vars.
	merged := map[string]string{}
	for k, v := range inheritedVars {
		merged[k] = v
	}
	if vars, ok := body["vars"].(map[string]any); ok {
		for k, v := range vars {
			merged[k] = stringifyYAML(v)
		}
	}

	if children, ok := body["children"].(map[string]any); ok {
		for childName, childRaw := range children {
			walkYAMLGroup(childName, childRaw, chain, merged, hosts, depth+1)
		}
	}

	if hostMap, ok := body["hosts"].(map[string]any); ok {
		for hName, hRaw := range hostMap {
			h := hosts[hName]
			if h == nil {
				h = &ansibleHost{Name: hName, Vars: map[string]string{}}
				hosts[hName] = h
			}
			for k, v := range merged {
				h.Vars[k] = v
			}
			if hVars, ok := hRaw.(map[string]any); ok {
				for k, v := range hVars {
					h.Vars[k] = stringifyYAML(v)
				}
			}
			// Membership in every level of the chain so tags reflect
			// the whole nesting, matching INI behaviour.
			for _, g := range chain {
				addUnique(&h.Groups, g)
			}
		}
	}
}

func stringifyYAML(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int, int64, float64, bool:
		return fmt.Sprint(t)
	default:
		// Complex value (list, nested map). JSON-encode so the raw
		// shape survives in the var map without us inventing a
		// flattening convention.
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func addUnique(slice *[]string, s string) {
	for _, existing := range *slice {
		if existing == s {
			return
		}
	}
	*slice = append(*slice, s)
}

// ---------- ssh_common_args parser (exposed for the connect flow) ----------
//
// Ansible inventories smuggle jump hosts and other ssh client knobs
// through `ansible_ssh_common_args` (or _extra_args). Common form:
//
//     ansible_ssh_common_args: "-o ProxyJump=bastion.example.com"
//     ansible_ssh_common_args: '-o "ProxyCommand=ssh -W %h:%p bastion"'
//
// AnsibleParseJumpHosts walks an arg string and returns ProxyJump
// hops in chain order. ProxyCommand is best-effort: we recognise the
// `ssh -W %h:%p host` and `ssh ... host` shapes that ssh itself
// uses for inventory_proxy and surface them as a single hop;
// anything weirder returns ("", false) so the caller can warn the
// user to set the jump host manually.
//
// Exported so app.go's connect path can read it from Entry.Raw and
// fold into ResolvedSettings overrides.

var (
	reProxyJump    = regexp.MustCompile(`(?i)\bProxyJump\s*=\s*([^\s"']+)`)
	reProxyCommand = regexp.MustCompile(`(?i)\bProxyCommand\s*=\s*(.+?)(?:"|'|$)`)
	// -J is the short form of -o ProxyJump=. Ansible inventories
	// often use the shorter spelling (`-J root@bastion`); the
	// regex above only matches the long form.
	reDashJ = regexp.MustCompile(`(?:^|\s)-J\s+([^\s"']+)`)
)

// AnsibleParseJumpHosts pulls a comma-separated ProxyJump list out
// of the given args string. Returns hop strings ("user@host:port"
// or just "host") in chain order, or nil if no recognizable jump
// directive is present.
func AnsibleParseJumpHosts(args string) []string {
	if m := reDashJ.FindStringSubmatch(args); len(m) >= 2 {
		return splitJumpList(m[1])
	}
	if m := reProxyJump.FindStringSubmatch(args); len(m) >= 2 {
		return splitJumpList(m[1])
	}
	// reProxyCommand grabs everything after `ProxyCommand=` up to the
	// next closing quote (single or double) or end of string. Works
	// because Ansible always wraps the -o value in matching quotes
	// when the ProxyCommand contains spaces, so the closing quote
	// reliably delimits the command.
	if m := reProxyCommand.FindStringSubmatch(args); len(m) >= 2 {
		cmd := strings.TrimSpace(m[1])
		if hop, ok := parseProxyCommandSSH(cmd); ok {
			return []string{hop}
		}
	}
	return nil
}

func splitJumpList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseProxyCommandSSH recognises the canonical `ssh -W %h:%p HOST`
// form (and its `ssh user@host -W ...` variants). Anything else
// returns false - we don't want to misparse an exotic command and
// silently route the user through the wrong host.
func parseProxyCommandSSH(cmd string) (string, bool) {
	tokens, err := splitFields(cmd)
	if err != nil || len(tokens) == 0 {
		return "", false
	}
	// Reject anything that isn't literally `ssh ...`.
	if base := filepath.Base(tokens[0]); base != "ssh" {
		return "", false
	}
	// Known flag set: -W takes one arg (%h:%p). -o takes one arg
	// (option=value). -i, -p, -l, -F, -L, -R, -D each take one arg.
	// Anything else with `-` prefix we treat as a value-less flag -
	// guessing wrong here is safer than eating the host token.
	takesArg := map[string]bool{
		"-W": true, "-o": true, "-i": true, "-p": true, "-l": true,
		"-F": true, "-L": true, "-R": true, "-D": true, "-J": true,
		"-B": true, "-b": true, "-c": true, "-E": true, "-e": true,
		"-I": true, "-m": true, "-Q": true, "-S": true, "-w": true,
	}
	hasWFlag := false
	host := ""
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t == "-W":
			hasWFlag = true
			i++ // skip the %h:%p arg
		case strings.HasPrefix(t, "-") && takesArg[t]:
			i++ // skip its arg
		case strings.HasPrefix(t, "-"):
			// Bare flag, no arg.
		default:
			// First non-flag positional is the host (user@host[:port]).
			if host == "" {
				host = t
			}
		}
	}
	if !hasWFlag || host == "" {
		return "", false
	}
	// Reject anything that looks like a URL or absolute path -
	// those aren't valid SSH host arguments and likely indicate a
	// weird ProxyCommand we don't want to misroute the user through.
	if strings.HasPrefix(host, "/") || strings.Contains(host, "://") {
		return "", false
	}
	return host, true
}
