package ssh

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// DialVia opens a TCP connection to addr the way a connect to settings
// would reach that host: through its jump chain when it has one (the
// address is dialed FROM the last bastion, as a direct-tcpip channel), else
// from this machine over the connection's network profile, else directly.
// For "talk to a non-SSH port on that host" callers such as the TLS check.
//
// The jump prefix comes from the shared-bastion pool when it is wired, so
// checking many hosts behind one bastion opens one connection to it. The
// returned cleanup closes the connection and releases the bastion; call it
// exactly once.
func DialVia(
	ctx context.Context,
	db *store.DB,
	vault *creds.Vault,
	settings *store.ResolvedSettings,
	addr string,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	timeout time.Duration,
) (net.Conn, func(), error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if len(buildHopChain(settings)) <= 1 {
		conn, _, err := firstHopDial(ctx, settings, addr, timeout)
		if err != nil {
			return nil, func() {}, err
		}
		return conn, func() { _ = conn.Close() }, nil
	}

	var (
		client  *ssh.Client
		release func()
	)
	if JumpPrefixHook != nil {
		shared, rel, _, err := JumpPrefixHook(ctx, settings, JumpPrefixDeps{
			DB: db, Vault: vault, HostKeyCB: hostKeyCB, AlgoLookup: algoLookup, ConnectTimeout: timeout,
		})
		if err != nil {
			return nil, func() {}, fmt.Errorf("jump host: %w", err)
		}
		client, release = shared, rel
	}
	if client == nil {
		c, cleanup, _, err := BuildJumpChainVia(ctx, db, vault, settings, hostKeyCB, algoLookup, timeout)
		if err != nil {
			return nil, func() {}, fmt.Errorf("jump host: %w", err)
		}
		if c == nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("jump host: chain resolved to nothing")
		}
		client, release = c, cleanup
	}

	c, err := dialThrough(client, addr, timeout)
	if err != nil {
		release()
		return nil, func() {}, fmt.Errorf("dial %s from the jump host: %w", addr, err)
	}
	return c, func() { _ = c.Close(); release() }, nil
}

// dialThrough opens a direct-tcpip channel to addr through client, bounded
// by timeout. ssh.Client.Dial takes no context, and the bastion only gives
// up on an unreachable target after its OWN TCP timeout (about two minutes
// on Linux) - so without this a stopped host behind a bastion sat on
// "connecting" long past the user's connect timeout.
func dialThrough(client *ssh.Client, addr string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 {
		return client.Dial("tcp", addr)
	}
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := client.Dial("tcp", addr)
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(timeout):
		// The late result, if any, is closed when it arrives.
		go func() {
			if r := <-ch; r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, fmt.Errorf("no answer within %s", timeout)
	}
}
