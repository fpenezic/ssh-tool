// Vultr dynamic inventory provider.
//
// Config:
//
//	{
//	  "api_token_credential_id": "<credential uuid>",
//	  "hostname_source": "label" | "public_ipv4" | "private_ipv4"
//	}
//
// Vultr exposes "instances" (the Cloud Compute product); bare-metal
// and Object Storage live behind their own endpoints and are out of
// scope.

package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Vultr struct{}

func (Vultr) Name() string { return "vultr" }

func (Vultr) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	token, _ := cfg["api_token_secret"].(string)
	if token == "" {
		return nil, fmt.Errorf("vultr: api_token credential required")
	}
	hostSource, _ := cfg["hostname_source"].(string)
	if hostSource == "" {
		hostSource = "label"
	}

	url := "https://api.vultr.com/v2/instances?per_page=500"
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
		page, perr := decodeVultrPage(resp)
		_ = resp.Body.Close()
		if perr != nil {
			return nil, perr
		}

		for _, v := range page.Instances {
			raw, _ := json.Marshal(v)
			tags := append([]string{"region=" + v.Region}, v.Tags...)
			out = append(out, Entry{
				ExternalID: "vultr:" + v.ID,
				Name:       firstNonEmpty(v.Label, v.Hostname, v.ID),
				Hostname:   pickVultrHostname(hostSource, v),
				Kind:       KindGuestVM,
				Status:     mapVultrStatus(v.PowerStatus, v.Status),
				Tags:       tags,
				Raw:        raw,
			})
		}

		next := page.Meta.Links.Next
		if next == "" {
			break
		}
		url = "https://api.vultr.com/v2/instances?per_page=500&cursor=" + next
	}
	return out, nil
}

type vultrInstance struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Hostname    string   `json:"hostname"`
	Status      string   `json:"status"`       // "active" | "pending" | "suspended"
	PowerStatus string   `json:"power_status"` // "running" | "stopped" | "starting"
	MainIP      string   `json:"main_ip"`
	InternalIP  string   `json:"internal_ip"`
	Region      string   `json:"region"`
	Tags        []string `json:"tags"`
}

type vultrPage struct {
	Instances []vultrInstance `json:"instances"`
	Meta      struct {
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	} `json:"meta"`
}

func decodeVultrPage(resp *http.Response) (*vultrPage, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("vultr: GET %s returned %d", resp.Request.URL, resp.StatusCode)
	}
	var p vultrPage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("vultr: decode response: %w", err)
	}
	return &p, nil
}

func pickVultrHostname(source string, v vultrInstance) string {
	switch source {
	case "public_ipv4":
		if v.MainIP != "" && v.MainIP != "0.0.0.0" {
			return v.MainIP
		}
	case "private_ipv4":
		if v.InternalIP != "" {
			return v.InternalIP
		}
	}
	return firstNonEmpty(v.Label, v.Hostname, v.ID)
}

func mapVultrStatus(power, status string) string {
	switch power {
	case "running":
		return "running"
	case "stopped":
		return "stopped"
	}
	if status == "active" {
		return "running"
	}
	return status
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
