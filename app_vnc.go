//go:build !android && !ios

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
)

// VncSession is the IPC payload the frontend needs to open a noVNC RFB
// connection: the loopback ws URL (carrying a single-use token) plus an
// optional RFB password and a display title.
type VncSession struct {
	SessionID string `json:"session_id"`
	WsURL     string `json:"ws_url"`
	// Username + Password pre-fill the credential prompt for security
	// types that need them (Apple ARD wants username+password; plain VNC
	// auth wants password). For a generic connection these default to the
	// connection's resolved SSH username and, if set, its VNC or SSH
	// vault password - so a Mac console behaves like the SSH login. Empty
	// for Proxmox (it auths via the ticket).
	Username string `json:"username"`
	Password string `json:"password"`
	Title    string `json:"title"`
	// Transport tells the frontend HOW the RFB upstream is reached so it can
	// show a meaningful "connecting" message: "direct" (plain TCP dial),
	// "jump:<host>" (dialed from a bastion hop), or "tunnel" (over an SSH
	// loopback forward). Proxmox consoles report "proxmox".
	Transport string `json:"transport"`
}

// VncOpenProxmox opens a console for a Proxmox VM/LXC dynamic entry. It
// resolves the folder's base_url + API token, reads node/vmid/type from
// the entry, and registers a bridge upstream that runs the vncproxy POST
// + vncwebsocket on connect. Returns the ws URL the webview connects to.
func (a *App) VncOpenProxmox(folderID, entryID string) (*VncSession, error) {
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return nil, fmt.Errorf("vault is locked")
	}
	entry, err := a.db.GetDynamicEntry(entryID)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.FolderID != folderID {
		return nil, fmt.Errorf("dynamic entry not found")
	}
	df, err := a.db.GetDynamicFolder(folderID)
	if err != nil {
		return nil, err
	}
	if df == nil || df.Provider != "proxmox" {
		return nil, fmt.Errorf("folder is not a Proxmox dynamic folder")
	}

	// node / vmid / type live in the entry's Raw (the cluster/resources
	// row the provider cached).
	var raw struct {
		Type string `json:"type"`
		Node string `json:"node"`
		VMID int64  `json:"vmid"`
	}
	if err := json.Unmarshal(entry.Raw, &raw); err != nil {
		return nil, fmt.Errorf("decode entry: %w", err)
	}
	if raw.Type != "qemu" && raw.Type != "lxc" && raw.Type != "node" {
		return nil, fmt.Errorf("entry %s is not a VM, container or node console target", entry.Name)
	}
	if raw.Node == "" {
		return nil, fmt.Errorf("entry %s missing node", entry.Name)
	}
	if raw.Type != "node" && raw.VMID == 0 {
		return nil, fmt.Errorf("entry %s missing vmid", entry.Name)
	}

	return a.openProxmoxConsole(folderID, raw.Node, raw.Type, raw.VMID, entry.Name+" (console)")
}

// openProxmoxConsole is the shared core for opening a Proxmox guest/node
// console: resolves the folder's token (+ optional node-shell login),
// builds the upstream, mints the bridge token. Used by both the dynamic-
// entry path and the pinned-connection path.
func (a *App) openProxmoxConsole(folderID, node, kind string, vmid int64, title string) (*VncSession, error) {
	cfg, err := a.inventory.ResolveConfig(folderID)
	if err != nil {
		return nil, fmt.Errorf("resolve proxmox config: %w", err)
	}
	baseURL, _ := cfg["base_url"].(string)
	tokenID, _ := cfg["api_token_id"].(string)
	tokenSecret, _ := cfg["api_token_secret"].(string)
	insecure, _ := cfg["insecure_skip_verify"].(bool)

	// Node (host) consoles need a real PVE realm login (the API token is
	// rejected by vncshell). Resolve the optional vnc_credential_id: a
	// password-kind credential whose name is the PVE username
	// (user@realm) and whose vault secret is the password.
	vncUser, vncPass := "", ""
	if credID, _ := cfg["vnc_credential_id"].(string); credID != "" {
		if cred, e := a.db.GetCredential(credID); e == nil && cred != nil {
			vncUser = cred.Name
			if cred.DefaultUsername != nil && *cred.DefaultUsername != "" {
				vncUser = *cred.DefaultUsername
			}
			if cred.VaultKey != nil {
				if p, ok, _ := a.vault.Get(*cred.VaultKey); ok {
					vncPass = p
				}
			}
		}
	}

	open, password, err := sshlayer.NewProxmoxVncUpstream(sshlayer.ProxmoxVncTarget{
		BaseURL:     baseURL,
		Node:        node,
		Kind:        kind,
		VMID:        vmid,
		TokenID:     tokenID,
		TokenSecret: tokenSecret,
		Insecure:    insecure,
		Username:    vncUser,
		Password:    vncPass,
	})
	if err != nil {
		return nil, err
	}
	return a.registerVnc(open, "", password, title)
}

// VncOpenPinnedProxmox opens a Proxmox console for a PINNED connection -
// one promoted from a Proxmox dynamic entry. Pinning drops the cached
// entry (with its node/raw), so we recover the link from
// pinned_dynamic_entries (folder + external_id like "qemu:114"/"lxc:110")
// and look up the current node by vmid via /cluster/resources.
func (a *App) VncOpenPinnedProxmox(connectionID string) (*VncSession, error) {
	if a.vault.Status().Kind != creds.StatusUnlocked {
		return nil, fmt.Errorf("vault is locked")
	}
	pin, err := a.db.GetPinForConnection(connectionID)
	if err != nil {
		return nil, err
	}
	if pin == nil {
		return nil, fmt.Errorf("connection is not a pinned dynamic entry")
	}
	df, err := a.db.GetDynamicFolder(pin.FolderID)
	if err != nil {
		return nil, err
	}
	if df == nil || df.Provider != "proxmox" {
		return nil, fmt.Errorf("pinned connection did not come from a Proxmox folder")
	}
	// external_id is "qemu:<vmid>" or "lxc:<vmid>".
	parts := strings.SplitN(pin.ExternalID, ":", 2)
	if len(parts) != 2 || (parts[0] != "qemu" && parts[0] != "lxc") {
		return nil, fmt.Errorf("pinned entry %q is not a Proxmox guest", pin.ExternalID)
	}
	kind := parts[0]
	vmid, perr := strconv.ParseInt(parts[1], 10, 64)
	if perr != nil || vmid == 0 {
		return nil, fmt.Errorf("pinned entry %q has no vmid", pin.ExternalID)
	}

	cfg, err := a.inventory.ResolveConfig(pin.FolderID)
	if err != nil {
		return nil, fmt.Errorf("resolve proxmox config: %w", err)
	}
	node, err := sshlayer.ProxmoxNodeForVMID(
		cfg["base_url"].(string),
		cfg["api_token_id"].(string),
		cfg["api_token_secret"].(string),
		vmid,
		boolOf(cfg["insecure_skip_verify"]),
	)
	if err != nil {
		return nil, err
	}

	conn, _ := a.db.GetConnection(connectionID)
	title := "console"
	if conn != nil {
		title = conn.Name + " (console)"
	}
	return a.openProxmoxConsole(pin.FolderID, node, kind, vmid, title)
}

func boolOf(v any) bool { b, _ := v.(bool); return b }

// VncOpenConnection opens a console for a generic (non-Proxmox) saved
// connection. Resolves vnc_port + vnc_use_tunnel + the optional vault
// VNC password. When tunneling, it opens a dedicated SSH session the
// console owns (torn down on VncClose) and dials the RFB port through
// it; otherwise it dials host:vnc_port directly.
func (a *App) VncOpenConnection(connectionID string) (*VncSession, error) {
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, err
	}
	if settings.Hostname == "" {
		return nil, fmt.Errorf("connection has no hostname")
	}
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return nil, err
	}

	// Prefill the credential prompt like the SSH login does. Username is
	// the resolved SSH username (Apple ARD wants one, and a Mac's VNC
	// login is usually the same account). Password prefers a VNC-specific
	// vault entry, falling back to the SSH password so the common
	// "same login as SSH" case fills in automatically. Both are only
	// SUGGESTIONS in the prompt - the user can override.
	username := ""
	if settings.Username != nil {
		username = *settings.Username
	} else if settings.AuthRef != nil {
		if cred, e := a.db.GetCredential(*settings.AuthRef); e == nil && cred.DefaultUsername != nil {
			username = *cred.DefaultUsername
		}
	}

	password := ""
	vaultUnlocked := a.vault.Status().Kind == creds.StatusUnlocked
	if conn.VncPasswordVaultKey != nil {
		if !vaultUnlocked {
			return nil, fmt.Errorf("vault is locked")
		}
		if p, ok, _ := a.vault.Get(*conn.VncPasswordVaultKey); ok {
			password = p
		}
	}
	// No VNC-specific password: reuse the SSH password if one is stored.
	if password == "" && vaultUnlocked && conn.PasswordVaultKey != nil {
		if p, ok, _ := a.vault.Get(*conn.PasswordVaultKey); ok {
			password = p
		}
	}

	port := settings.VncPort
	if port == 0 {
		port = 5900
	}
	title := conn.Name + " (VNC)"

	// The SSH connect (tunnel or jump chain) must NOT happen here: this is
	// the VncOpenConnection IPC handler, and Wails serialises IPC calls -
	// a blocking SSH handshake (or a slow/failing jump dialing for the full
	// timeout) freezes every subsequent IPC, locking the whole UI. So the
	// dial is deferred into the lazy `open` factory, which the bridge calls
	// on a its own goroutine when the webview actually connects. The owned
	// session / chain cleanup is recorded so VncClose tears it down.
	var open func(ctx context.Context) (sshlayer.VncUpstream, error)
	// ownedRef lets the lazy factory hand its session/cleanup back to the
	// session meta for teardown, since the sessionID is minted only after
	// `open` is built. Guarded by vncMu via setVncOwned.
	connID := connectionID

	if settings.VncUseTunnel {
		// Dial the RFB port on the remote's loopback through SSH. Open a
		// dedicated session the console owns - we don't reuse a terminal
		// tab's session so closing the console can't kill a live shell,
		// and a terminal tab dropping can't kill the console.
		if a.vault.Status().Kind != creds.StatusUnlocked {
			return nil, fmt.Errorf("vault is locked")
		}
		if settings.AuthRef == nil && settings.PasswordOverride == nil {
			if conn.PasswordVaultKey != nil {
				if p, ok, _ := a.vault.Get(*conn.PasswordVaultKey); ok && p != "" {
					settings.PasswordOverride = &p
				}
			}
		}
		if settings.Username == nil && settings.AuthRef != nil {
			if cred, e := a.db.GetCredential(*settings.AuthRef); e == nil && cred.DefaultUsername != nil {
				settings.Username = cred.DefaultUsername
			}
		}
		if settings.AuthRef == nil && settings.PasswordOverride == nil {
			return nil, fmt.Errorf("VNC tunnel needs SSH credentials on the connection")
		}
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		open = func(ctx context.Context) (sshlayer.VncUpstream, error) {
			sink := vncNullSink{}
			sess, e := sshlayer.Connect(context.Background(), a.db, a.vault, settings, sink, a.makeHostKeyCallback(), a.makeAlgoLookup(), 0, nil)
			if e != nil {
				err := fmt.Errorf("vnc ssh tunnel connect: %w", e)
				a.setVncError(connID, err)
				return nil, err
			}
			client := sess.TargetClient()
			if client == nil {
				sess.Disconnect()
				err := fmt.Errorf("vnc tunnel: no live ssh client")
				a.setVncError(connID, err)
				return nil, err
			}
			a.setVncOwned(connID, sess, nil)
			up, derr := sshlayer.NewSSHClientUpstream(client, addr)(ctx)
			if derr != nil {
				sess.Disconnect()
				a.setVncError(connID, fmt.Errorf("vnc tunnel dial %s: %w", addr, derr))
				return nil, derr
			}
			return up, nil
		}
	} else {
		// Direct (non-loopback) RFB. If the connection has a jump host the
		// VNC target usually isn't routable from here - only from the
		// bastion - which is why a direct net.Dial failed and the user had
		// to be on the VPN. Route the RFB dial through the jump chain when
		// one exists (we SSH the bastion(s) and dial host:port from there;
		// the VNC host itself need not run sshd). No jump -> plain dial.
		addr := fmt.Sprintf("%s:%d", settings.Hostname, port)
		open = func(ctx context.Context) (sshlayer.VncUpstream, error) {
			jumpClient, jumpCleanup, jerr := sshlayer.BuildJumpChain(
				a.db, a.vault, settings, a.makeHostKeyCallback(), a.makeAlgoLookup(), 0)
			if jerr != nil {
				e := fmt.Errorf("vnc jump connect: %w", jerr)
				a.setVncError(connID, e)
				return nil, e
			}
			if jumpClient == nil {
				// No jump host - plain TCP dial.
				up, derr := sshlayer.NewTCPUpstream(addr)(ctx)
				if derr != nil {
					a.setVncError(connID, fmt.Errorf("vnc direct dial %s: %w", addr, derr))
				}
				return up, derr
			}
			a.setVncOwned(connID, nil, jumpCleanup)
			up, derr := sshlayer.NewSSHClientUpstream(jumpClient, addr)(ctx)
			if derr != nil {
				jumpCleanup()
				a.setVncError(connID, fmt.Errorf("vnc dial %s via jump: %w", addr, derr))
				return nil, derr
			}
			return up, nil
		}
	}

	// Describe the transport for the frontend's connecting message. The
	// jump-vs-direct branch above is decided at dial time inside `open`, but
	// it follows directly from whether the connection has a jump host, so we
	// can label it now.
	transport := "direct"
	if settings.VncUseTunnel {
		transport = "tunnel"
	} else if settings.JumpHost != nil {
		transport = "jump:" + settings.JumpHost.Hostname
	}
	return a.registerVncFor(connID, open, username, password, title, transport)
}

// setVncOwned records the SSH session / chain-cleanup a lazy VNC upstream
// created, keyed by connectionID, so VncClose can tear it down. The lazy
// factory runs after the session id is minted, so we match on connectionID
// (the title carries it) - look up the live VNC session for this connection
// and attach. Best-effort: if the console was already closed, we close the
// just-created resources immediately.
// setVncError records the most recent upstream-open failure for the live
// console of a connection, so VncLastError can surface it to the frontend
// (noVNC only delivers a bare close, not the reason). Keyed by connectionID
// like setVncOwned, since the sessionID is minted after the open factory.
func (a *App) setVncError(connectionID string, err error) {
	if err == nil {
		return
	}
	a.vncMu.Lock()
	defer a.vncMu.Unlock()
	for _, m := range a.vncSessions {
		if m.connectionID == connectionID {
			m.lastErr = err.Error()
			return
		}
	}
}

// VncLastError returns (and clears) the last upstream-open error for a
// console, by sessionID. The frontend calls this when a console
// disconnects/closes while still connecting, to show WHY (e.g. a jump-host
// auth failure) instead of a generic "connection closed". Empty if none.
func (a *App) VncLastError(sessionID string) string {
	a.vncMu.Lock()
	defer a.vncMu.Unlock()
	m := a.vncSessions[sessionID]
	if m == nil {
		return ""
	}
	e := m.lastErr
	m.lastErr = ""
	return e
}

func (a *App) setVncOwned(connectionID string, sess *sshlayer.Session, cleanup func()) {
	a.vncMu.Lock()
	for _, m := range a.vncSessions {
		if m.connectionID == connectionID {
			if sess != nil {
				m.ownedSession = sess
			}
			if cleanup != nil {
				m.ownedCleanup = cleanup
			}
			a.vncMu.Unlock()
			return
		}
	}
	a.vncMu.Unlock()
	// No live console for this connection (already closed) - don't leak.
	if sess != nil {
		sess.Disconnect()
	}
	if cleanup != nil {
		cleanup()
	}
}

// registerVnc stores the upstream factory under a new session id, mints
// the first ws token, and returns the payload. Shared by both open paths.
func (a *App) registerVnc(open func(ctx context.Context) (sshlayer.VncUpstream, error), username, password, title string) (*VncSession, error) {
	return a.registerVncFor("", open, username, password, title, "proxmox")
}

// registerVncFor is registerVnc with the owning connectionID, used by the
// generic-connection path so its lazy upstream factory can attach the
// session / cleanup it creates (see setVncOwned). Proxmox goes through
// registerVnc with an empty id.
func (a *App) registerVncFor(connectionID string, open func(ctx context.Context) (sshlayer.VncUpstream, error), username, password, title, transport string) (*VncSession, error) {
	sessionID := "vnc:" + uuid.New().String()
	wsURL, err := a.vncBridge.Mint(open)
	if err != nil {
		return nil, err
	}
	a.vncMu.Lock()
	a.vncSessions[sessionID] = &vncSessionMeta{
		title:        title,
		username:     username,
		password:     password,
		connectionID: connectionID,
		transport:    transport,
		open:         open,
	}
	a.vncMu.Unlock()
	return &VncSession{SessionID: sessionID, WsURL: wsURL, Username: username, Password: password, Title: title, Transport: transport}, nil
}

// VncClose tears down a console: drops the metadata and disconnects any
// SSH session the console owned (the tunnel). The bridge token is
// single-use and already consumed or GC'd.
func (a *App) VncClose(sessionID string) {
	a.vncMu.Lock()
	m := a.vncSessions[sessionID]
	delete(a.vncSessions, sessionID)
	a.vncMu.Unlock()
	if m != nil {
		if m.ownedSession != nil {
			m.ownedSession.Disconnect()
		}
		if m.ownedCleanup != nil {
			m.ownedCleanup()
		}
	}
}

// VncSessionList returns every live VNC console with a freshly-minted ws
// token. A detached window calls this after a tab tear-off to re-register
// its VNC tabs and reconnect noVNC - mirrors LocalShellList for PTYs.
func (a *App) VncSessionList() ([]VncSession, error) {
	a.vncMu.Lock()
	defer a.vncMu.Unlock()
	out := make([]VncSession, 0, len(a.vncSessions))
	for id, m := range a.vncSessions {
		wsURL, err := a.vncBridge.Mint(m.open)
		if err != nil {
			log.Printf("vnc list: mint %s: %v", id, err)
			continue
		}
		out = append(out, VncSession{
			SessionID: id,
			WsURL:     wsURL,
			Username:  m.username,
			Password:  m.password,
			Title:     m.title,
			Transport: m.transport,
		})
	}
	return out, nil
}

// SetConnectionVncPassword stores the RFB password for a connection in
// the vault and records the key on the connection row.
func (a *App) SetConnectionVncPassword(connectionID, password string) error {
	vaultKey := "conn_vnc_pass:" + connectionID
	if err := a.vault.Put(vaultKey, password); err != nil {
		return fmt.Errorf("vault put: %w", err)
	}
	return a.db.SetConnectionVncPasswordKey(connectionID, vaultKey)
}

// ClearConnectionVncPassword removes the stored VNC password.
func (a *App) ClearConnectionVncPassword(connectionID string) error {
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return err
	}
	if conn.VncPasswordVaultKey != nil {
		_ = a.vault.Delete(*conn.VncPasswordVaultKey)
	}
	return a.db.ClearConnectionVncPasswordKey(connectionID)
}

// GetConnectionHasVncPassword reports whether a VNC password is stored.
func (a *App) GetConnectionHasVncPassword(connectionID string) bool {
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return false
	}
	return conn.VncPasswordVaultKey != nil
}

// vncNullSink is an EventSink that drops everything - the VNC tunnel SSH
// session has no terminal, so its state/output events go nowhere.
type vncNullSink struct{}

func (vncNullSink) EmitState(string, sshlayer.SessionState) {}
func (vncNullSink) EmitOutput(string, []byte, uint64)       {}
func (vncNullSink) EmitExitStatus(string, uint32)           {}
func (vncNullSink) EmitDebug(string, string)                {}

// ClipboardGetText reads the OS clipboard via the native Wails clipboard.
// The webview's navigator.clipboard.readText() is blocked over a canvas
// (no secure context / user-gesture path), so the VNC console reads the
// clipboard through this instead to paste into the remote.
func (a *App) ClipboardGetText() string {
	if a.app == nil {
		return ""
	}
	text, _ := a.app.Clipboard.Text()
	return text
}

// ClipboardSetText writes text to the OS clipboard via Wails. The VNC
// console uses it to mirror the remote's RFB cut-text (copy from the
// remote desktop) into the local clipboard.
func (a *App) ClipboardSetText(text string) bool {
	if a.app == nil {
		return false
	}
	return a.app.Clipboard.SetText(text)
}
