// Hetzner Cloud dynamic inventory provider.
//
// Config (`config_json` on `dynamic_folders`):
//
//   {
//     "api_token_credential_id": "<credential uuid>",
//     "hostname_source": "name" | "public_ipv4" | "private_ipv4",
//     "label_whitelist": ["env=prod"],
//     "label_blacklist": ["env=dev"]
//   }
//
// Manager.resolveSecrets inlines the credential's secret into the
// config as `api_token_secret` (and re-uses `api_token_id` if the
// credential has a token_id - Hetzner doesn't, leaves it empty).
//
// All Hetzner servers are guests (KindGuestVM) - Hetzner Cloud
// doesn't expose physical-host inventory through this API, so the
// "Hosts" pseudo-folder stays empty for hetzner sources.

package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Hetzner struct{}

func (Hetzner) Name() string { return "hetzner" }

func (Hetzner) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	token, _ := cfg["api_token_secret"].(string)
	if token == "" {
		return nil, fmt.Errorf("hetzner: api_token credential required")
	}
	hostSource, _ := cfg["hostname_source"].(string)
	if hostSource == "" {
		hostSource = "name"
	}

	// Hetzner paginates at 50/page by default; bump to max.
	url := "https://api.hetzner.cloud/v1/servers?per_page=50&page=1"

	client := &http.Client{Timeout: 20 * time.Second}
	out := []Entry{}
	for url != "" {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, perr := decodeHetznerPage(resp)
		_ = resp.Body.Close()
		if perr != nil {
			return nil, perr
		}

		for _, s := range body.Servers {
			raw, _ := json.Marshal(s)
			out = append(out, Entry{
				ExternalID: fmt.Sprintf("server:%d", s.ID),
				Name:       s.Name,
				Hostname:   pickHetznerHostname(hostSource, s),
				Kind:       KindGuestVM,
				Status:     mapHetznerStatus(s.Status),
				Tags:       hetznerLabels(s.Labels),
				Raw:        raw,
			})
		}

		// Next page link, if any.
		if body.Meta.Pagination.NextPage == 0 {
			url = ""
		} else {
			url = fmt.Sprintf("https://api.hetzner.cloud/v1/servers?per_page=50&page=%d",
				body.Meta.Pagination.NextPage)
		}
	}
	return out, nil
}

type hetznerServer struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Status    string            `json:"status"` // "running" | "off" | "starting" | …
	Labels    map[string]string `json:"labels"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
		IPv6 struct {
			IP string `json:"ip"`
		} `json:"ipv6"`
	} `json:"public_net"`
	PrivateNet []struct {
		IP string `json:"ip"`
	} `json:"private_net"`
}

type hetznerPage struct {
	Servers []hetznerServer `json:"servers"`
	Meta    struct {
		Pagination struct {
			NextPage int `json:"next_page"`
		} `json:"pagination"`
	} `json:"meta"`
}

func decodeHetznerPage(resp *http.Response) (*hetznerPage, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("hetzner: GET %s returned %d", resp.Request.URL, resp.StatusCode)
	}
	var page hetznerPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("hetzner: decode response: %w", err)
	}
	return &page, nil
}

func pickHetznerHostname(source string, s hetznerServer) string {
	switch source {
	case "public_ipv4":
		if s.PublicNet.IPv4.IP != "" {
			return s.PublicNet.IPv4.IP
		}
		return s.Name
	case "private_ipv4":
		if len(s.PrivateNet) > 0 && s.PrivateNet[0].IP != "" {
			return s.PrivateNet[0].IP
		}
		return s.Name
	default:
		return s.Name
	}
}

// mapHetznerStatus normalises Hetzner's status strings to the shared
// "running" / "stopped" buckets the rest of the app expects.
func mapHetznerStatus(s string) string {
	switch s {
	case "running":
		return "running"
	case "off", "stopping":
		return "stopped"
	default:
		return s
	}
}

// hetznerLabels flattens the {key:value} label map into the "key=value"
// tag strings the rest of the filter machinery handles.
//
// Output is SORTED: Go map iteration is randomised, so an unsorted
// slice here produced a different tag order on every refresh, which
// the dynamic-entry change detector read as "changed" - rewriting the
// cache every cycle and (with sync on) auto-pushing every refresh
// interval while the user was idle.
func hetznerLabels(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v == "" {
			out = append(out, k)
			continue
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// ensure unused-import noop for strings is gone if Go compiler complains;
// strings is used below if we add normalisers later.
var _ = strings.TrimSpace
