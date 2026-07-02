// Proxmox VNC upstream for the bridge.
//
// Proxmox exposes a VM/LXC console over noVNC via two API calls:
//
//   1. POST /api2/json/nodes/<node>/<type>/<vmid>/vncproxy
//        -> { ticket, port, user, cert, ... }
//   2. GET  /api2/json/nodes/<node>/<type>/<vmid>/vncwebsocket
//             ?port=<port>&vncticket=<ticket>
//        -> websocket carrying the RFB stream
//
// Auth on both is the API token (PVEAPIToken header), so no PVEAuthCookie
// dance is needed. The websocket uses base64-of-RFB inside each binary
// message (Proxmox's noVNC build runs in base64 mode), plus an initial
// "<user>:<ticket>\n" line the RFB proxy expects. proxmoxUpstream wraps
// the ws so the bridge sees a plain raw-RFB ReadWriteCloser and noVNC
// gets a clean binary stream.
package ssh

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ProxmoxVncTarget identifies one console (guest or node) plus the
// credentials to reach it. Built by the App from the dynamic folder
// config + entry.
type ProxmoxVncTarget struct {
	BaseURL     string // https://pve.example.com:8006
	Node        string // hosting node name (also the target for node consoles)
	Kind        string // "qemu" | "lxc" | "node"
	VMID        int64  // guest id; ignored for "node"
	TokenID     string // user@realm!tokenid
	TokenSecret string
	Insecure    bool // skip TLS verify (self-signed PVE)
	// User login, used ONLY for node (host) consoles - PVE's vncshell
	// rejects API tokens at a username-format check, so it needs a real
	// realm ticket from POST /access/ticket. Guest consoles ignore these
	// and use the token. Empty Username => node consoles unavailable.
	Username string // user@realm (e.g. fpenezic@ldap)
	Password string
}

// proxmoxTicketResp is the /access/ticket reply.
type proxmoxTicketResp struct {
	Data struct {
		Ticket              string `json:"ticket"`
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
		Username            string `json:"username"`
	} `json:"data"`
}

// proxmoxVncProxyResp is the subset of the vncproxy reply we use.
type proxmoxVncProxyResp struct {
	Data struct {
		Ticket string      `json:"ticket"`
		Port   json.Number `json:"port"`
		User   string      `json:"user"`
	} `json:"data"`
}

// NewProxmoxVncUpstream returns an upstream factory + the RFB password
// noVNC should present. For Proxmox the vncproxy ticket doubles as the
// RFB password in most PVE versions; we surface it so the webview can
// auto-auth. The factory performs the vncproxy POST lazily (on connect)
// so a stale tab doesn't burn a ticket early.
func NewProxmoxVncUpstream(t ProxmoxVncTarget) (open func(ctx context.Context) (VncUpstream, error), password string, err error) {
	if t.BaseURL == "" || t.Node == "" {
		return nil, "", fmt.Errorf("proxmox vnc: base_url and node required")
	}
	kind := t.Kind
	isNode := kind == "node"
	if !isNode && (t.TokenID == "" || t.TokenSecret == "") {
		return nil, "", fmt.Errorf("proxmox vnc: api token required for guest console")
	}
	if !isNode && kind != "qemu" && kind != "lxc" {
		kind = "qemu"
	}
	if !isNode && t.VMID == 0 {
		return nil, "", fmt.Errorf("proxmox vnc: vmid required for %s console", kind)
	}

	// Pin HTTP/1.1. PVE's vncproxy spawns its task but returns a 500 on
	// the HTTP/2 stream (Go's default transport negotiates h2 when the
	// cert is valid), and the vncwebsocket Upgrade needs HTTP/1.1
	// regardless. Setting TLSNextProto to an empty non-nil map disables
	// h2; an explicit transport is always used so behaviour doesn't flip
	// with insecure_skip_verify.
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: t.Insecure},
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	httpClient := &http.Client{Timeout: 15 * time.Second, Transport: tr}
	base := strings.TrimRight(t.BaseURL, "/")

	// Auth headers differ by console type:
	//  - guest (vncproxy): API token via the Authorization header.
	//  - node (vncshell): PVE rejects tokens, so we log in as a real
	//    user (POST /access/ticket) and use the PVEAuthCookie +
	//    CSRFPreventionToken. Requires a user+password credential.
	httpHeaders := http.Header{}
	wsHeaders := http.Header{}
	if isNode {
		if t.Username == "" || t.Password == "" {
			return nil, "", fmt.Errorf("proxmox node console needs a user+password login (set a VNC console login on the dynamic folder); API tokens can't open a node shell")
		}
		cookie, csrf, terr := proxmoxTicket(httpClient, base, t.Username, t.Password)
		if terr != nil {
			return nil, "", terr
		}
		httpHeaders.Set("Cookie", "PVEAuthCookie="+cookie)
		httpHeaders.Set("CSRFPreventionToken", csrf)
		wsHeaders.Set("Cookie", "PVEAuthCookie="+cookie)
	} else {
		authHeader := "PVEAPIToken=" + t.TokenID + "=" + t.TokenSecret
		httpHeaders.Set("Authorization", authHeader)
		wsHeaders.Set("Authorization", authHeader)
	}
	// Guests proxy via /nodes/<n>/<kind>/<vmid>/{vncproxy,vncwebsocket};
	// a node's own console uses /nodes/<n>/{vncshell,vncwebsocket}.
	var apiBase, proxyEndpoint string
	if isNode {
		apiBase = fmt.Sprintf("%s/api2/json/nodes/%s", base, t.Node)
		proxyEndpoint = "/vncshell"
	} else {
		apiBase = fmt.Sprintf("%s/api2/json/nodes/%s/%s/%d", base, t.Node, kind, t.VMID)
		proxyEndpoint = "/vncproxy"
	}

	// vncproxy / vncshell POST -> ticket + port. Done EAGERLY (not in the
	// lazy open) because Proxmox's guest VNC server uses VNC auth where
	// the vncticket IS the RFB password - we must surface it to noVNC up
	// front so it can auto-auth instead of prompting. noVNC connects
	// within ~1s of the tab opening, well inside the ticket lifetime.
	body := "websocket=1"
	if isNode {
		// A node shell needs an explicit cmd; "login" gives the standard
		// getty console.
		body = "websocket=1&cmd=login"
	}
	req, err := http.NewRequest("POST", apiBase+proxyEndpoint, strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	for k, v := range httpHeaders {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	log.Printf("vnc proxmox: POST %s body=%q node=%v", apiBase+proxyEndpoint, body, isNode)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("proxmox vncproxy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		rbody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// PVE often puts the real reason in the status line on 5xx (the
		// JSON body is just {"data":null}). Surface both.
		return nil, "", fmt.Errorf("proxmox vncproxy: HTTP %d %s: %s",
			resp.StatusCode, strings.TrimSpace(resp.Status), strings.TrimSpace(string(rbody)))
	}
	var pr proxmoxVncProxyResp
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, "", fmt.Errorf("proxmox vncproxy decode: %w", err)
	}
	if pr.Data.Ticket == "" || pr.Data.Port == "" {
		return nil, "", fmt.Errorf("proxmox vncproxy: empty ticket/port")
	}

	wsBase := strings.Replace(apiBase, "https://", "wss://", 1)
	wsBase = strings.Replace(wsBase, "http://", "ws://", 1)
	q := url.Values{}
	q.Set("port", pr.Data.Port.String())
	q.Set("vncticket", pr.Data.Ticket)
	fullWS := wsBase + "/vncwebsocket?" + q.Encode()

	open = func(ctx context.Context) (VncUpstream, error) {
		dialOpts := &websocket.DialOptions{
			HTTPClient:   httpClient,
			Subprotocols: []string{"binary"},
			HTTPHeader:   wsHeaders,
		}
		c, _, err := websocket.Dial(ctx, fullWS, dialOpts)
		if err != nil {
			return nil, fmt.Errorf("proxmox vncwebsocket dial: %w", err)
		}
		c.SetReadLimit(-1)
		// In binary subprotocol mode the stream is RAW RFB in both
		// directions - no base64, no in-stream auth line. Pass through.
		return websocket.NetConn(ctx, c, websocket.MessageBinary), nil
	}

	// The vncticket doubles as the RFB password (Proxmox sets VNC auth on
	// the guest's VNC server). noVNC presents it and connects with no
	// prompt.
	return open, pr.Data.Ticket, nil
}

// ProxmoxNodeForVMID finds which node currently hosts a guest, by vmid,
// via /cluster/resources. Used for pinned connections, which lost the
// cached node when they were promoted out of the inventory.
func ProxmoxNodeForVMID(baseURL, tokenID, tokenSecret string, vmid int64, insecure bool) (string, error) {
	if baseURL == "" || tokenID == "" || tokenSecret == "" {
		return "", fmt.Errorf("proxmox: base_url + api token required to locate the guest's node")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}}
	client := &http.Client{Timeout: 15 * time.Second, Transport: tr}
	u := strings.TrimRight(baseURL, "/") + "/api2/json/cluster/resources?type=vm"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+tokenID+"="+tokenSecret)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("proxmox cluster/resources: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("proxmox cluster/resources: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			VMID int64  `json:"vmid"`
			Node string `json:"node"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("proxmox cluster/resources decode: %w", err)
	}
	for _, r := range payload.Data {
		if r.VMID == vmid {
			if r.Node == "" {
				return "", fmt.Errorf("proxmox: guest %d has no node", vmid)
			}
			return r.Node, nil
		}
	}
	return "", fmt.Errorf("proxmox: guest %d not found in the cluster (running?)", vmid)
}

// proxmoxTicket logs in via POST /access/ticket and returns the
// PVEAuthCookie value + CSRFPreventionToken. Used for node consoles,
// which PVE won't open for API tokens. The username is a full realm
// login (user@realm).
func proxmoxTicket(client *http.Client, base, username, password string) (cookie, csrf string, err error) {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	req, err := http.NewRequest("POST", base+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("proxmox /access/ticket: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		rbody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("proxmox login (user %s): HTTP %d %s: %s",
			username, resp.StatusCode, strings.TrimSpace(resp.Status), strings.TrimSpace(string(rbody)))
	}
	var tr proxmoxTicketResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", "", fmt.Errorf("proxmox login decode: %w", err)
	}
	if tr.Data.Ticket == "" {
		return "", "", fmt.Errorf("proxmox login: empty ticket (wrong username/password?)")
	}
	return tr.Data.Ticket, tr.Data.CSRFPreventionToken, nil
}
