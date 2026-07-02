// Proxmox VE dynamic inventory provider.
//
// Config (`config_json` on `dynamic_folders`):
//
//   {
//     "base_url": "https://pve.example.com:8006",
//     "api_token_id": "user@pam!sshtool",    // user@realm!tokenid
//     "api_token_secret": "...",             // raw UUID secret
//     "insecure_skip_verify": false,
//     "include_hosts": true,
//     "include_guests": true,
//     "tag_whitelist": ["prod","staging"],
//     "tag_blacklist": ["deprecated"]
//   }
//
// The cluster /cluster/resources endpoint returns every VM, LXC and
// node from the whole cluster in one request. With proxmox sitting
// behind a load balancer the user only needs the LB URL - wherever
// the request lands, the response is cluster-wide. No per-node
// failover logic required in the app.

package inventory

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Proxmox struct{}

func (Proxmox) Name() string { return "proxmox" }

func (Proxmox) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	baseURL, _ := cfg["base_url"].(string)
	tokenID, _ := cfg["api_token_id"].(string)
	tokenSecret, _ := cfg["api_token_secret"].(string)
	insecure, _ := cfg["insecure_skip_verify"].(bool)
	includeHosts, _ := cfg["include_hosts"].(bool)
	includeGuests, _ := cfg["include_guests"].(bool)
	if baseURL == "" || tokenID == "" || tokenSecret == "" {
		return nil, fmt.Errorf("proxmox: base_url + api_token_id + api_token_secret required")
	}

	url := strings.TrimRight(baseURL, "/") + "/api2/json/cluster/resources"

	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Proxmox token auth: "PVEAPIToken=USER@REALM!TOKENID=SECRET".
	req.Header.Set("Authorization", "PVEAPIToken="+tokenID+"="+tokenSecret)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("proxmox: GET %s returned %d", url, resp.StatusCode)
	}

	var payload struct {
		Data []struct {
			Type    string  `json:"type"` // "node" | "qemu" | "lxc" | "storage" | "pool"
			Name    string  `json:"name"` // node uses name; guests use this too for VM name
			Node    string  `json:"node"` // hosting node (guests only)
			VMID    int64   `json:"vmid"` // guest id
			Status  string  `json:"status"`
			Tags    string  `json:"tags"`    // semicolon-separated
			MaxCPU  int64   `json:"maxcpu"`  // configured vCPUs
			MaxMem  int64   `json:"maxmem"`  // bytes
			MaxDisk int64   `json:"maxdisk"` // bytes
			CPU     float64 `json:"cpu"`     // current load 0..1
			Mem     int64   `json:"mem"`     // bytes in use
			Disk    int64   `json:"disk"`    // bytes in use
			Uptime  int64   `json:"uptime"`  // seconds
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("proxmox: decode response: %w", err)
	}

	out := make([]Entry, 0, len(payload.Data))
	for _, r := range payload.Data {
		switch r.Type {
		case "node":
			if !includeHosts {
				continue
			}
			raw, _ := json.Marshal(r)
			out = append(out, Entry{
				ExternalID: "node:" + r.Node,
				Name:       r.Node,
				Hostname:   r.Node, // FQDN per user's DNS setup
				Kind:       KindHost,
				Status:     r.Status,
				Tags:       splitProxmoxTags(r.Tags),
				Raw:        raw,
			})
		case "qemu", "lxc":
			if !includeGuests {
				continue
			}
			raw, _ := json.Marshal(r)
			kind := KindGuestVM
			if r.Type == "lxc" {
				kind = KindGuestLXC
			}
			id := fmt.Sprintf("%s:%d", r.Type, r.VMID)
			name := r.Name
			if name == "" {
				name = fmt.Sprintf("%s-%d", r.Type, r.VMID)
			}
			out = append(out, Entry{
				ExternalID: id,
				Name:       name,
				Hostname:   name, // user confirmed: VM name == FQDN in DNS
				Kind:       kind,
				Status:     r.Status,
				Tags:       splitProxmoxTags(r.Tags),
				Raw:        raw,
			})
		}
	}
	return out, nil
}

// splitProxmoxTags parses the semicolon-separated tag string proxmox
// returns. Empty input → empty slice (not nil) so JSON serialises to
// [] not null.
func splitProxmoxTags(s string) []string {
	out := []string{}
	if s == "" {
		return out
	}
	for _, t := range strings.Split(s, ";") {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
