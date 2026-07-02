// DigitalOcean dynamic inventory provider.
//
// Config (`config_json` on `dynamic_folders`):
//
//	{
//	  "api_token_credential_id": "<credential uuid>",
//	  "hostname_source": "name" | "public_ipv4" | "private_ipv4"
//	}
//
// Manager.resolveSecrets inlines the credential secret as
// `api_token_secret` before passing the config to Fetch.
//
// All DigitalOcean droplets are guests (KindGuestVM) - DO exposes no
// physical-host inventory through the public API.

package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type DigitalOcean struct{}

func (DigitalOcean) Name() string { return "digitalocean" }

func (DigitalOcean) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	token, _ := cfg["api_token_secret"].(string)
	if token == "" {
		return nil, fmt.Errorf("digitalocean: api_token credential required")
	}
	hostSource, _ := cfg["hostname_source"].(string)
	if hostSource == "" {
		hostSource = "name"
	}

	url := "https://api.digitalocean.com/v2/droplets?per_page=200&page=1"
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
		page, perr := decodeDOPage(resp)
		_ = resp.Body.Close()
		if perr != nil {
			return nil, perr
		}

		for _, d := range page.Droplets {
			raw, _ := json.Marshal(d)
			out = append(out, Entry{
				ExternalID: fmt.Sprintf("droplet:%d", d.ID),
				Name:       d.Name,
				Hostname:   pickDOHostname(hostSource, d),
				Kind:       KindGuestVM,
				Status:     mapDOStatus(d.Status),
				Tags:       d.Tags,
				Raw:        raw,
			})
		}

		url = page.Links.Pages.Next
	}
	return out, nil
}

type doNetwork struct {
	IPAddress string `json:"ip_address"`
	Type      string `json:"type"` // "public" | "private"
}

type doDroplet struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"` // "new" | "active" | "off" | "archive"
	Tags     []string `json:"tags"`
	Networks struct {
		V4 []doNetwork `json:"v4"`
	} `json:"networks"`
}

type doPage struct {
	Droplets []doDroplet `json:"droplets"`
	Links    struct {
		Pages struct {
			Next string `json:"next"`
		} `json:"pages"`
	} `json:"links"`
}

func decodeDOPage(resp *http.Response) (*doPage, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("digitalocean: GET %s returned %d", resp.Request.URL, resp.StatusCode)
	}
	var p doPage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("digitalocean: decode response: %w", err)
	}
	return &p, nil
}

func pickDOHostname(source string, d doDroplet) string {
	switch source {
	case "public_ipv4":
		for _, n := range d.Networks.V4 {
			if n.Type == "public" && n.IPAddress != "" {
				return n.IPAddress
			}
		}
		return d.Name
	case "private_ipv4":
		for _, n := range d.Networks.V4 {
			if n.Type == "private" && n.IPAddress != "" {
				return n.IPAddress
			}
		}
		return d.Name
	default:
		return d.Name
	}
}

func mapDOStatus(s string) string {
	switch s {
	case "active":
		return "running"
	case "off", "archive":
		return "stopped"
	default:
		return s
	}
}
