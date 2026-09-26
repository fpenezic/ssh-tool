package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/cmdpolicy"
	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// host is one target in scope: a saved connection or a dynamic-inventory
// entry ("dyn:<entry id>", the same id form the app's batch runner uses).
type host struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Folder   string `json:"folder"`
}

// folderPaths maps folder id -> "Parent/Child" path.
func folderPaths(folders []store.Folder) map[string]string {
	byID := map[string]store.Folder{}
	for _, f := range folders {
		byID[f.ID] = f
	}
	out := map[string]string{}
	var path func(id string, depth int) string
	path = func(id string, depth int) string {
		if p, ok := out[id]; ok {
			return p
		}
		f, ok := byID[id]
		if !ok || depth > 64 {
			return ""
		}
		p := f.Name
		if f.ParentID != nil {
			if pp := path(*f.ParentID, depth+1); pp != "" {
				p = pp + "/" + p
			}
		}
		out[id] = p
		return p
	}
	for _, f := range folders {
		path(f.ID, 0)
	}
	return out
}

// inFolders reports whether path is one of roots or below one of them.
func inFolders(path string, roots []string) bool {
	for _, r := range roots {
		r = strings.Trim(r, "/")
		if r == "" {
			return true // "/" = the whole profile
		}
		if strings.EqualFold(path, r) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(r)+"/") {
			return true
		}
	}
	return false
}

// scope lists every host the config allows, sorted by folder then name.
func (a *agent) scope() ([]host, error) {
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	paths := folderPaths(folders)
	conns, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	var out []host
	for _, c := range conns {
		if c.Protocol != "" && c.Protocol != "ssh" {
			continue
		}
		fp := ""
		if c.FolderID != nil {
			fp = paths[*c.FolderID]
		}
		named := false
		for _, n := range a.cfg.Connections {
			if strings.EqualFold(n, c.Name) || n == c.ID {
				named = true
			}
		}
		if named || inFolders(fp, a.cfg.Folders) {
			out = append(out, host{ID: c.ID, Name: c.Name, Hostname: c.Hostname, Folder: fp})
		}
	}
	dyn, err := a.db.ListDynamicFolders()
	if err != nil {
		return nil, err
	}
	for _, df := range dyn {
		fp := paths[df.FolderID]
		if !inFolders(fp, a.cfg.Folders) {
			continue
		}
		entries, err := a.db.ListDynamicEntries(df.FolderID)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Hostname == "" {
				continue
			}
			out = append(out, host{ID: "dyn:" + e.ID, Name: e.Name, Hostname: e.Hostname, Folder: fp})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Folder != out[j].Folder {
			return out[i].Folder < out[j].Folder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// pick selects hosts by name or id (case-insensitive name), or by folder.
// With neither, every host in scope.
func pick(all []host, names []string, folder string) ([]host, error) {
	if len(names) == 0 && folder == "" {
		return all, nil
	}
	var out []host
	seen := map[string]bool{}
	add := func(h host) {
		if !seen[h.ID] {
			seen[h.ID] = true
			out = append(out, h)
		}
	}
	if folder != "" {
		for _, h := range all {
			if inFolders(h.Folder, []string{folder}) {
				add(h)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no hosts in scope under folder %q", folder)
		}
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		var hits []host
		for _, h := range all {
			if h.ID == n || strings.EqualFold(h.Name, n) {
				hits = append(hits, h)
			}
		}
		switch len(hits) {
		case 0:
			return nil, fmt.Errorf("host %q is not in scope", n)
		case 1:
			add(hits[0])
		default:
			ids := make([]string, len(hits))
			for i, h := range hits {
				ids[i] = h.ID + " (" + h.Folder + ")"
			}
			return nil, fmt.Errorf("host name %q is ambiguous: %s", n, strings.Join(ids, ", "))
		}
	}
	return out, nil
}

// settings resolves a host the way the app's batch runner does: folder
// inheritance, per-connection password override, credential default user.
func (a *agent) settings(h host) (*store.ResolvedSettings, error) {
	var s *store.ResolvedSettings
	if strings.HasPrefix(h.ID, "dyn:") {
		e, err := a.db.GetDynamicEntry(strings.TrimPrefix(h.ID, "dyn:"))
		if err != nil || e == nil {
			return nil, fmt.Errorf("dynamic entry %s not found", h.ID)
		}
		folders, err := a.db.ListFolders()
		if err != nil {
			return nil, err
		}
		fid := e.FolderID
		r := resolver.ResolveWith(store.Connection{
			ID: h.ID, FolderID: &fid, Name: e.Name, Hostname: e.Hostname,
		}, folders)
		s = &r
	} else {
		r, err := resolver.ResolveConnection(a.db, h.ID)
		if err != nil {
			return nil, err
		}
		s = r
		if c, err := a.db.GetConnection(h.ID); err == nil && c.PasswordVaultKey != nil {
			if pass, ok, _ := a.vault.Get(*c.PasswordVaultKey); ok && pass != "" {
				s.PasswordOverride = &pass
			}
		}
	}
	if s.Username == nil && s.AuthRef != nil {
		if cred, err := a.db.GetCredential(*s.AuthRef); err == nil && cred.DefaultUsername != nil {
			s.Username = cred.DefaultUsername
		}
	}
	return s, nil
}

// hostKeyCallback accepts only keys already pinned in the profile's
// known_hosts. There is no one to ask about a new or changed key, so both
// fail closed; connect once from the app to pin a host.
func (a *agent) hostKeyCallback() gossh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key gossh.PublicKey) error {
		host, portStr, err := net.SplitHostPort(hostname)
		if err != nil {
			host, portStr = hostname, "22"
		}
		port, _ := strconv.Atoi(portStr)
		if port == 0 {
			port = 22
		}
		stored, err := a.db.GetKnownHost(host, port)
		if err != nil {
			return fmt.Errorf("known_hosts lookup failed: %w", err)
		}
		if stored == nil {
			return fmt.Errorf("host key for %s:%d is not pinned in this profile", host, port)
		}
		if stored.KeyType != key.Type() || stored.KeyB64 != encodeKey(key) {
			return fmt.Errorf("host key for %s:%d does not match the pinned key", host, port)
		}
		return nil
	}
}

func (a *agent) algoLookup() sshlayer.HostKeyAlgoLookup {
	return func(host string, port int) []string {
		if k, err := a.db.GetKnownHost(host, port); err == nil && k != nil {
			return []string{k.KeyType}
		}
		return nil
	}
}

var errRefused = errors.New("refused")

// exec classifies command, then runs it on the picked hosts in parallel.
func (a *agent) exec(command string, names []string, folder string, timeoutSeconds int) (string, error) {
	v := cmdpolicy.Classify(command, a.cfg.policy())
	if !v.ReadOnly {
		log.Printf("refused %q: %s", command, v.Reason)
		return "", fmt.Errorf("%w: not read-only - %s", errRefused, v.Reason)
	}
	all, err := a.scope()
	if err != nil {
		return "", err
	}
	targets, err := pick(all, names, folder)
	if err != nil {
		return "", err
	}
	if timeoutSeconds <= 0 || timeoutSeconds > a.cfg.TimeoutSeconds {
		timeoutSeconds = a.cfg.TimeoutSeconds
	}
	inputs := make([]sshlayer.BatchHostInput, len(targets))
	for i, h := range targets {
		s, err := a.settings(h)
		if err != nil {
			log.Printf("%s: %v", h.Name, err)
			s = nil // BatchExec reports it per host
		}
		inputs[i] = sshlayer.BatchHostInput{ConnectionID: h.ID, Settings: s, Name: h.Name, Hostname: h.Hostname}
	}
	log.Printf("exec %q on %d host(s)", command, len(inputs))
	t0 := time.Now()
	res := sshlayer.BatchExec(a.db, a.vault, a.hostKeyCallback(), a.algoLookup(), 0, inputs, command, timeoutSeconds)
	log.Printf("exec done in %s", time.Since(t0).Round(time.Millisecond))
	return formatResults(res, a.cfg.MaxOutputBytes), nil
}

func formatHosts(hs []host) string {
	if len(hs) == 0 {
		return "No hosts in scope.\n"
	}
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", h.Name, h.Hostname, h.Folder, h.ID)
	}
	return b.String()
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n[... %d more bytes cut]", len(s)-max)
}

func formatResults(res []sshlayer.BatchHostResult, max int) string {
	var b strings.Builder
	b.WriteString("--- BEGIN UNTRUSTED HOST OUTPUT ---\n")
	for _, r := range res {
		fmt.Fprintf(&b, "=== %s (%s) state=%s exit=%d %dms\n", r.Name, r.Hostname, r.State, r.ExitCode, r.DurationMs)
		if r.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", r.Error)
		}
		if r.Stdout != "" {
			b.WriteString(clip(r.Stdout, max))
			if !strings.HasSuffix(r.Stdout, "\n") {
				b.WriteByte('\n')
			}
		}
		if r.Stderr != "" {
			b.WriteString("[stderr]\n")
			b.WriteString(clip(r.Stderr, max))
			if !strings.HasSuffix(r.Stderr, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("--- END UNTRUSTED HOST OUTPUT ---\n")
	return b.String()
}
