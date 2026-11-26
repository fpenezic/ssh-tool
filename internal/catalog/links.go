package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Config is GET /api/v1/config (PROTOCOL 4.3): operator settings a client
// may use. Admins can change them at run time (revision 14), so callers
// read it again on every sync instead of keeping it for the process.
type Config struct {
	// ExternalLinks maps an external_ref backend to an https URL template
	// whose {name} placeholders are the same-named external_ref fields.
	ExternalLinks map[string]string `json:"external_links"`
}

// Config reads the operator settings. A server without the endpoint
// answers 404; that counts as no settings.
func (c *Client) Config(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := c.do(ctx, http.MethodGet, "/api/v1/config", nil, &cfg, true); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return &Config{}, nil
		}
		return nil, err
	}
	return &cfg, nil
}

var linkField = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// ExternalLink fills the template for backend from an external_ref (its
// JSON as stored). Each {name} takes the same-named field, URL-encoded.
// It returns "" when the backend has no template, a field is missing or
// not a string, or the result is not an https URL with a host: the link
// only ever opens the item in the backend's own web vault.
func ExternalLink(links map[string]string, backend, refJSON string) string {
	tmpl := links[strings.ToLower(backend)]
	if tmpl == "" || refJSON == "" {
		return ""
	}
	var ref map[string]any
	if json.Unmarshal([]byte(refJSON), &ref) != nil {
		return ""
	}
	ok := true
	out := linkField.ReplaceAllStringFunc(tmpl, func(m string) string {
		v, isStr := ref[m[1:len(m)-1]].(string)
		if !isStr || v == "" {
			ok = false
			return ""
		}
		// QueryEscape covers the query and fragment positions templates
		// use; a space as %20 reads the same in a path and a fragment.
		return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
	})
	if !ok {
		return ""
	}
	u, err := url.Parse(out)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return out
}
