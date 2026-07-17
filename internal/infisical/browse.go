package infisical

import (
	"sort"
	"strings"
)

// EntryInfo is one selectable secret in the picker: a key at a folder path
// inside an environment. The value is never carried here - only metadata.
type EntryInfo struct {
	Key      string `json:"key"`
	Path     string `json:"path"`      // absolute Infisical folder path, e.g. "/cloudflare"
	HasValue bool   `json:"has_value"` // false when the value came back empty
	Comment  string `json:"comment"`
	// IsKey is a heuristic: the value looks like a PEM private key, so the
	// picker can suggest wiring it as an SSH key rather than a password.
	IsKey bool `json:"is_key"`
}

// EnvInfo is one environment (dev / prod ...) with its secrets, grouped by path.
type EnvInfo struct {
	Name    string      `json:"name"`
	Slug    string      `json:"slug"`
	Entries []EntryInfo `json:"entries"`
}

// GroupInfo is a browse node: one project (workspace) holding its environments.
// The picker renders Project -> Environment -> (path-prefixed) key.
type GroupInfo struct {
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	Environments []EnvInfo `json:"environments"`
}

// buildEnvEntries turns the raw secrets of one environment into sorted
// EntryInfos. base is the path that was queried (used when a secret carries no
// SecretPath of its own, i.e. a non-recursive list).
func buildEnvEntries(base string, secrets []rawSecret) []EntryInfo {
	base = normPath(base)
	out := make([]EntryInfo, 0, len(secrets))
	for _, s := range secrets {
		path := normPath(s.SecretPath)
		if path == "" {
			path = base
		}
		out = append(out, EntryInfo{
			Key:      s.SecretKey,
			Path:     path,
			HasValue: s.SecretValue != "",
			Comment:  s.SecretComment,
			IsKey:    looksLikePEMKey(s.SecretValue),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return strings.ToLower(out[i].Key) < strings.ToLower(out[j].Key)
	})
	return out
}

// normPath normalises an Infisical folder path to a leading-slash, no-trailing-
// slash form. "" and "/" both map to "/".
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/"
	}
	p = "/" + strings.Trim(p, "/")
	return p
}

// splitSecretRef maps a base path + a key that may itself contain folder
// segments ("cloudflare/password") into an absolute Infisical folder path and a
// bare key. base "/" + "cloudflare/password" -> ("/cloudflare", "password").
// Proven in the spike.
func splitSecretRef(base, rawKey string) (path, key string) {
	base = normPath(base)
	rawKey = strings.Trim(rawKey, "/")
	if i := strings.LastIndex(rawKey, "/"); i >= 0 {
		folder := rawKey[:i]
		key = rawKey[i+1:]
		if base == "/" {
			path = "/" + folder
		} else {
			path = base + "/" + folder
		}
		return normPath(path), key
	}
	return base, rawKey
}

// looksLikePEMKey reports whether a secret value is a PEM-encoded private key,
// used to suggest SSH-key wiring in the picker.
func looksLikePEMKey(v string) bool {
	return strings.Contains(v, "-----BEGIN") && strings.Contains(v, "PRIVATE KEY-----")
}
