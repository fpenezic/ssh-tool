package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"ssh-tool/internal/store"
)

// Quick connect: an SSH session to a host typed in by hand, for the one-off
// box that is not worth a saved connection. The connection lives only in
// memory ("quick:<uuid>"), never in the store, so it neither syncs nor shows
// in the tree. It is kept for the app's lifetime because reconnect and pane
// splitting dial it again by id. An optional folder lends its inherited
// settings (jump host, credential, network profile), so a quick connect to a
// customer's box goes through that customer's bastion.

type quickConnStore struct {
	mu    sync.Mutex
	conns map[string]store.Connection
}

func (q *quickConnStore) get(id string) (store.Connection, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	c, ok := q.conns[id]
	return c, ok
}

func (q *quickConnStore) put(c store.Connection) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.conns == nil {
		q.conns = map[string]store.Connection{}
	}
	q.conns[c.ID] = c
}

// QuickConnectInput is what the quick connect dialog sends.
type QuickConnectInput struct {
	Target       string `json:"target"`        // [user@]host[:port], [user@][v6addr]:port
	CredentialID string `json:"credential_id"` // optional vault credential
	FolderID     string `json:"folder_id"`     // optional folder to inherit from
}

// QuickConnectResult carries the synthetic connection id and the display
// fields the frontend puts on the tab.
type QuickConnectResult struct {
	SessionID    string `json:"session_id"`
	NetworkVia   string `json:"network_via,omitempty"`
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
}

// parseQuickTarget splits "[user@]host[:port]". An IPv6 address takes a
// port only in brackets ("[2001:db8::1]:2222"); a bare one with colons is
// all host.
func parseQuickTarget(s string) (user, host string, port int, err error) {
	s = strings.TrimSpace(s)
	if at := strings.LastIndex(s, "@"); at >= 0 {
		user = s[:at]
		s = s[at+1:]
	}
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", 0, fmt.Errorf("missing ] in %q", s)
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", "", 0, fmt.Errorf("unexpected %q after ]", rest)
			}
			if port, err = parsePort(rest[1:]); err != nil {
				return "", "", 0, err
			}
		}
	case strings.Count(s, ":") == 1:
		i := strings.Index(s, ":")
		host = s[:i]
		if port, err = parsePort(s[i+1:]); err != nil {
			return "", "", 0, err
		}
	default:
		host = s
	}
	if host == "" || strings.ContainsAny(host, " \t/") {
		return "", "", 0, fmt.Errorf("not a host: %q", host)
	}
	return user, host, port, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	return n, nil
}

// SshQuickConnect opens a session to a typed-in target without saving it.
func (a *App) SshQuickConnect(in QuickConnectInput) (*QuickConnectResult, error) {
	user, host, port, err := parseQuickTarget(in.Target)
	if err != nil {
		return nil, err
	}
	name := host
	if user != "" {
		name = user + "@" + host
	}
	if port != 0 && port != 22 {
		name += ":" + strconv.Itoa(port)
	}
	c := store.Connection{
		ID:       "quick:" + uuid.NewString(),
		Name:     name,
		Hostname: host,
	}
	if in.FolderID != "" {
		fid := in.FolderID
		c.FolderID = &fid
	}
	if user != "" {
		c.Overrides.Username = &user
	}
	if port != 0 {
		p := uint16(port)
		c.Overrides.Port = &p
	}
	if in.CredentialID != "" {
		cred := in.CredentialID
		c.Overrides.AuthRef = &cred
	}
	a.quick.put(c)
	res, err := a.connectQuick(c)
	if err != nil {
		return nil, err
	}
	return &QuickConnectResult{
		SessionID:    res.SessionID,
		NetworkVia:   res.NetworkVia,
		ConnectionID: c.ID,
		Name:         c.Name,
		Hostname:     c.Hostname,
	}, nil
}

func (a *App) connectQuick(c store.Connection) (*SshConnectResult, error) {
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	return a.connectSynthetic(c, folders, c.Name, c.Hostname, "", "", "",
		func(sessionID string, settings *store.ResolvedSettings) {
			u := ""
			if settings.Username != nil {
				u = *settings.Username
			}
			folder := ""
			if c.FolderID != nil {
				folder = *c.FolderID
			}
			a.recordAudit("ssh.connect.quick", c.ID, map[string]string{
				"session_id": sessionID,
				"folder_id":  folder,
				"host":       settings.Hostname,
				"port":       strconv.Itoa(int(settings.Port)),
				"user":       u,
			})
		})
}

// QuickConnectionSave turns a quick connect into a saved connection with the
// same target, folder and credential, and returns it. Only what the quick
// connect set itself is written; everything else keeps inheriting.
func (a *App) QuickConnectionSave(quickID, name string) (*store.Connection, error) {
	c, ok := a.quick.get(quickID)
	if !ok {
		return nil, fmt.Errorf("quick connection not found")
	}
	if strings.TrimSpace(name) == "" {
		name = c.Name
	}
	return a.db.CreateConnection(store.NewConnection{
		FolderID:  c.FolderID,
		Name:      strings.TrimSpace(name),
		Hostname:  c.Hostname,
		Overrides: c.Overrides,
	})
}
