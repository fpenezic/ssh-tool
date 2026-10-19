package main

import (
	"context"
	"fmt"
	"net"
	"sync"

	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/sshchan"
	"ssh-tool/internal/store"
)

// sshChanConnect builds the LazySSH connect of a tab that tunnels through
// its connection (Kafka, Database): a tunnel-only session to the
// connection's host, with the credentials a terminal connect would use.
// kind prefixes the errors and the audit action ("kafka.ssh_connect").
func (a *App) sshChanConnect(kind, connectionID string, conn *store.Connection, resolved *store.ResolvedSettings, lazy *sshchan.LazySSH) func(context.Context) (*gossh.Client, func(), error) {
	return func(ctx context.Context) (*gossh.Client, func(), error) {
		settings := *resolved
		settings.TunnelOnly = true
		if conn.PasswordVaultKey != nil && a.vault.Status().Kind == creds.StatusUnlocked {
			if p, ok, _ := a.vault.Get(*conn.PasswordVaultKey); ok && p != "" {
				settings.PasswordOverride = &p
			}
		}
		if settings.Username == nil && settings.AuthRef != nil {
			if cred, e := a.db.GetCredential(*settings.AuthRef); e == nil && cred.DefaultUsername != nil {
				settings.Username = cred.DefaultUsername
			}
		}
		sess, err := sshlayer.Connect(ctx, a.db, a.vault, &settings, sshChanNullSink{},
			a.makeHostKeyCallback(), a.makeAlgoLookup(), a.connectTimeout(), nil)
		if err != nil {
			return nil, nil, fmt.Errorf("%s ssh connect: %w", kind, err)
		}
		client := sess.TargetClient()
		if client == nil {
			sess.Disconnect()
			return nil, nil, fmt.Errorf("%s ssh connect: no live ssh client", kind)
		}
		// A dropped session clears itself so the next dial connects again.
		// In a goroutine: the close hook runs inside the session's
		// close-once, and Invalidate's Disconnect enters it again.
		sess.SetOnClose(func(string) { go lazy.Invalidate(client) })
		a.recordAudit(kind+".ssh_connect", connectionID, map[string]string{
			"host": settings.Hostname,
		})
		return client, sess.Disconnect, nil
	}
}

// sshChanNullSink drops the session events: a tunnelled tab has no
// terminal to render them. Its own type, not the VNC one - app_vnc.go is
// desktop-only and this file builds on android too.
type sshChanNullSink struct{}

func (sshChanNullSink) EmitState(string, sshlayer.SessionState) {}
func (sshChanNullSink) EmitOutput(string, []byte, uint64)       {}
func (sshChanNullSink) EmitExitStatus(string, uint32)           {}
func (sshChanNullSink) EmitDebug(string, string)                {}

// viaDial dials each connection the way DialVia does: from the last jump
// host through the shared bastion pool, else from here over the
// connection's network profile. The direct transport of the Kafka and
// Database tabs.
func (a *App) viaDial(settings *store.ResolvedSettings) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		c, cleanup, err := sshlayer.DialVia(ctx, a.db, a.vault, settings, addr,
			a.makeHostKeyCallback(), a.makeAlgoLookup(), a.connectTimeout())
		if err != nil {
			return nil, err
		}
		return &cleanupConn{Conn: c, cleanup: cleanup}, nil
	}
}

// cleanupConn runs DialVia's cleanup (which releases a pooled bastion) when
// the broker connection closes.
type cleanupConn struct {
	net.Conn
	once    sync.Once
	cleanup func()
}

func (c *cleanupConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.cleanup)
	return err
}
