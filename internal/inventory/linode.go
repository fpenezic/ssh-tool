// Linode (Akamai Cloud Compute) dynamic inventory provider.
//
// Config:
//
//	{
//	  "api_token_credential_id": "<credential uuid>",
//	  "hostname_source": "label" | "public_ipv4" | "private_ipv4"
//	}
//
// The API field is "label" (Linode-speak for the instance's friendly
// name); we surface it via "label" rather than aliasing to "name" so
// the picker matches what users see in the Linode UI.

package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Linode struct{}

func (Linode) Name() string { return "linode" }

func (Linode) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	token, _ := cfg["api_token_secret"].(string)
	if token == "" {
		return nil, fmt.Errorf("linode: api_token credential required")
	}
	hostSource, _ := cfg["hostname_source"].(string)
	if hostSource == "" {
		hostSource = "label"
	}

	url := "https://api.linode.com/v4/linode/instances?page=1&page_size=500"
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
		page, perr := decodeLinodePage(resp)
		_ = resp.Body.Close()
		if perr != nil {
			return nil, perr
		}

		for _, l := range page.Data {
			raw, _ := json.Marshal(l)
			out = append(out, Entry{
				ExternalID: fmt.Sprintf("linode:%d", l.ID),
				Name:       l.Label,
				Hostname:   pickLinodeHostname(hostSource, l),
				Kind:       KindGuestVM,
				Status:     mapLinodeStatus(l.Status),
				Tags:       append([]string{"region=" + l.Region}, l.Tags...),
				Raw:        raw,
			})
		}

		if page.Page >= page.Pages {
			break
		}
		url = fmt.Sprintf("https://api.linode.com/v4/linode/instances?page=%d&page_size=500", page.Page+1)
	}
	return out, nil
}

type linodeInstance struct {
	ID     int64    `json:"id"`
	Label  string   `json:"label"`
	Status string   `json:"status"` // "running" | "offline" | "booting" | "shutting_down" | …
	Region string   `json:"region"`
	Tags   []string `json:"tags"`
	IPv4   []string `json:"ipv4"`
}

type linodePage struct {
	Data  []linodeInstance `json:"data"`
	Page  int              `json:"page"`
	Pages int              `json:"pages"`
}

func decodeLinodePage(resp *http.Response) (*linodePage, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("linode: GET %s returned %d", resp.Request.URL, resp.StatusCode)
	}
	var p linodePage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("linode: decode response: %w", err)
	}
	return &p, nil
}

func pickLinodeHostname(source string, l linodeInstance) string {
	switch source {
	case "public_ipv4":
		for _, ip := range l.IPv4 {
			if !isLinodePrivate(ip) {
				return ip
			}
		}
		return l.Label
	case "private_ipv4":
		for _, ip := range l.IPv4 {
			if isLinodePrivate(ip) {
				return ip
			}
		}
		return l.Label
	default:
		return l.Label
	}
}

// isLinodePrivate is a coarse RFC1918 + Linode 192.168.128/17 check.
// Linode hands out private IPv4 in its standard RFC1918 ranges plus
// 192.168.128.0/17 for the "private IP" feature; the rest of 10/8
// shows up on customers' VLAN nets.
func isLinodePrivate(ip string) bool {
	return strings.HasPrefix(ip, "10.") ||
		strings.HasPrefix(ip, "192.168.") ||
		strings.HasPrefix(ip, "172.16.") ||
		strings.HasPrefix(ip, "172.17.") ||
		strings.HasPrefix(ip, "172.18.") ||
		strings.HasPrefix(ip, "172.19.") ||
		strings.HasPrefix(ip, "172.20.") ||
		strings.HasPrefix(ip, "172.21.") ||
		strings.HasPrefix(ip, "172.22.") ||
		strings.HasPrefix(ip, "172.23.") ||
		strings.HasPrefix(ip, "172.24.") ||
		strings.HasPrefix(ip, "172.25.") ||
		strings.HasPrefix(ip, "172.26.") ||
		strings.HasPrefix(ip, "172.27.") ||
		strings.HasPrefix(ip, "172.28.") ||
		strings.HasPrefix(ip, "172.29.") ||
		strings.HasPrefix(ip, "172.30.") ||
		strings.HasPrefix(ip, "172.31.")
}

func mapLinodeStatus(s string) string {
	switch s {
	case "running":
		return "running"
	case "offline", "shutting_down":
		return "stopped"
	default:
		return s
	}
}
