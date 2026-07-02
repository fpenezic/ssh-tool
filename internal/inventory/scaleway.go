// Scaleway dynamic inventory provider.
//
// Config:
//
//	{
//	  "api_token_credential_id": "<credential uuid>",
//	  "zone": "fr-par-1",                   // required, e.g. fr-par-1, nl-ams-1, pl-waw-1
//	  "hostname_source": "name" | "public_ipv4" | "private_ipv4"
//	}
//
// Scaleway scopes instance listings by zone, so the config carries one.
// To cover multiple zones, create one dynamic folder per zone - keeps
// the UI tree explicit instead of magicking cross-zone aggregation.

package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Scaleway struct{}

func (Scaleway) Name() string { return "scaleway" }

func (Scaleway) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	token, _ := cfg["api_token_secret"].(string)
	if token == "" {
		return nil, fmt.Errorf("scaleway: api_token credential required")
	}
	zone, _ := cfg["zone"].(string)
	if zone == "" {
		return nil, fmt.Errorf("scaleway: zone required (e.g. fr-par-1)")
	}
	hostSource, _ := cfg["hostname_source"].(string)
	if hostSource == "" {
		hostSource = "name"
	}

	url := fmt.Sprintf("https://api.scaleway.com/instance/v1/zones/%s/servers?per_page=100&page=1", zone)
	client := &http.Client{Timeout: 20 * time.Second}
	out := []Entry{}
	page := 1
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Auth-Token", token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, perr := decodeScalewayPage(resp)
		_ = resp.Body.Close()
		if perr != nil {
			return nil, perr
		}

		for _, s := range body.Servers {
			raw, _ := json.Marshal(s)
			tags := append([]string{"zone=" + zone}, s.Tags...)
			out = append(out, Entry{
				ExternalID: "scw:" + s.ID,
				Name:       s.Name,
				Hostname:   pickScalewayHostname(hostSource, s),
				Kind:       KindGuestVM,
				Status:     mapScalewayStatus(s.State),
				Tags:       tags,
				Raw:        raw,
			})
		}

		if len(body.Servers) < 100 {
			break
		}
		page++
		url = fmt.Sprintf("https://api.scaleway.com/instance/v1/zones/%s/servers?per_page=100&page=%d", zone, page)
	}
	return out, nil
}

type scalewayServer struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	State    string   `json:"state"` // "running" | "stopped" | "starting" | "stopping"
	Tags     []string `json:"tags"`
	PublicIP struct {
		Address string `json:"address"`
	} `json:"public_ip"`
	PrivateIP *string `json:"private_ip"`
}

type scalewayPage struct {
	Servers []scalewayServer `json:"servers"`
}

func decodeScalewayPage(resp *http.Response) (*scalewayPage, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("scaleway: GET %s returned %d", resp.Request.URL, resp.StatusCode)
	}
	var p scalewayPage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("scaleway: decode response: %w", err)
	}
	return &p, nil
}

func pickScalewayHostname(source string, s scalewayServer) string {
	switch source {
	case "public_ipv4":
		if s.PublicIP.Address != "" {
			return s.PublicIP.Address
		}
		return s.Name
	case "private_ipv4":
		if s.PrivateIP != nil && *s.PrivateIP != "" {
			return *s.PrivateIP
		}
		return s.Name
	default:
		return s.Name
	}
}

func mapScalewayStatus(s string) string {
	switch s {
	case "running":
		return "running"
	case "stopped", "stopped in place", "stopping":
		return "stopped"
	default:
		return s
	}
}
