// Batch command execution across N connections - fan out a one-off
// command to a multi-select set of hosts, capture stdout/stderr/exit
// per host, hand the result back to the UI.
//
// No PTY, no scrollback, no interactive auth. Each host opens its
// own quiet SSH chain (mirrors session.Connect minus the emit-state /
// banner-callback / scrollback machinery), runs the command via
// session.Output, tears down. Concurrency is capped at 8 in flight
// to avoid file-descriptor exhaustion on big batches.

package ssh

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

const batchParallelism = 8

// BatchHostResult is what the frontend renders per host.
type BatchHostResult struct {
	ConnectionID string `json:"connection_id"`
	Hostname     string `json:"hostname"`
	Name         string `json:"name"`
	State        string `json:"state"` // "ok" | "error" | "skipped"
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	ExitCode     int    `json:"exit_code"`
	DurationMs   int64  `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

// BatchExec runs the command on every (connectionID, settings) pair in
// hosts. settings must already be resolved + password-override populated
// by the caller (mirrors what app.SshConnect does). hostKeyCB is the
// same callback shared with interactive connects - for batch we let it
// auto-accept unknown keys silently if it's nil; the caller decides
// the policy.
func BatchExec(
	db *store.DB,
	vault *creds.Vault,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	hosts []BatchHostInput,
	command string,
	timeoutSeconds int,
) []BatchHostResult {
	if connectTimeout <= 0 {
		connectTimeout = 20 * time.Second
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	out := make([]BatchHostResult, len(hosts))
	sem := make(chan struct{}, batchParallelism)
	var wg sync.WaitGroup
	for i, h := range hosts {
		i, h := i, h
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = runOneBatch(db, vault, hostKeyCB, algoLookup, connectTimeout, h, command, timeoutSeconds)
		}()
	}
	wg.Wait()
	return out
}

// BatchHostInput is the per-host payload the IPC layer assembles.
type BatchHostInput struct {
	ConnectionID string
	Settings     *store.ResolvedSettings
	Name         string
	Hostname     string
}

func runOneBatch(
	db *store.DB,
	vault *creds.Vault,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
	h BatchHostInput,
	command string,
	timeoutSeconds int,
) BatchHostResult {
	r := BatchHostResult{
		ConnectionID: h.ConnectionID,
		Hostname:     h.Hostname,
		Name:         h.Name,
		State:        "error",
	}
	if h.Settings == nil {
		r.Error = "could not resolve connection settings"
		return r
	}
	t0 := time.Now()
	target, cleanupFn, err := buildChainQuiet(db, vault, h.Settings, hostKeyCB, algoLookup, connectTimeout)
	if err != nil {
		r.Error = err.Error()
		r.DurationMs = time.Since(t0).Milliseconds()
		return r
	}
	defer cleanupFn()

	sess, err := target.NewSession()
	if err != nil {
		r.Error = fmt.Sprintf("new session: %v", err)
		r.DurationMs = time.Since(t0).Milliseconds()
		return r
	}
	defer sess.Close()

	// Capture stdout / stderr separately so the UI can render them
	// distinctly (and surface stderr-only commands like `ls foo`
	// with their meaningful output).
	var stdout, stderr captureBuf
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	// Wrap the run in a context-deadline so a single bad host can't
	// stall the whole batch.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- sess.Run(command)
	}()

	select {
	case err = <-done:
		// fall through
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGINT)
		_ = sess.Close()
		r.Error = "command timed out"
		r.Stdout = stdout.String()
		r.Stderr = stderr.String()
		r.DurationMs = time.Since(t0).Milliseconds()
		return r
	}

	r.Stdout = stdout.String()
	r.Stderr = stderr.String()
	r.DurationMs = time.Since(t0).Milliseconds()
	if err == nil {
		r.State = "ok"
		r.ExitCode = 0
		return r
	}
	if xe, ok := err.(*ssh.ExitError); ok {
		r.State = "ok" // non-zero exit is still "ran cleanly"; the UI colours by code
		r.ExitCode = xe.ExitStatus()
		return r
	}
	r.Error = err.Error()
	return r
}

// captureBuf is a small bounded buffer so a runaway `cat /dev/urandom`
// doesn't OOM the app. Caps at 1 MiB per stream.
type captureBuf struct {
	buf [1 << 20]byte
	n   int
}

func (b *captureBuf) Write(p []byte) (int, error) {
	if b.n >= len(b.buf) {
		// silently drop after cap
		return len(p), nil
	}
	space := len(b.buf) - b.n
	if len(p) > space {
		copy(b.buf[b.n:], p[:space])
		b.n = len(b.buf)
		return len(p), nil
	}
	copy(b.buf[b.n:], p)
	b.n += len(p)
	return len(p), nil
}

func (b *captureBuf) String() string {
	return string(b.buf[:b.n])
}

// buildChainQuiet is the quiet variant of session.Connect's chain
// walk - no sink, no banner forwarding, no session pool registration.
// Returns the target *ssh.Client and a cleanup that closes the whole
// chain. Mirrors the auth fallback for password overrides at the
// last hop.
func buildChainQuiet(
	db *store.DB,
	vault *creds.Vault,
	settings *store.ResolvedSettings,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
) (*ssh.Client, func(), error) {
	return dialChain(db, vault, buildHopChain(settings), settings, hostKeyCB, algoLookup, connectTimeout)
}

// BuildJumpChain connects ONLY the jump hops of settings (everything before
// the final target hop) and returns the last jump's *ssh.Client plus a
// cleanup. Used by VNC (and other "dial a non-SSH port behind a bastion"
// callers): the VNC host isn't an SSH server, so we can't SSH into it - we
// SSH to the jump(s) and dial the VNC host:port FROM there. Returns
// (nil, noop, nil) when there is no jump host, so the caller dials directly.
func BuildJumpChain(
	db *store.DB,
	vault *creds.Vault,
	settings *store.ResolvedSettings,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
) (*ssh.Client, func(), error) {
	full := buildHopChain(settings)
	// The last hop is the target itself; drop it - we want the bastion(s).
	if len(full) <= 1 {
		return nil, func() {}, nil // no jump host
	}
	return dialChain(db, vault, full[:len(full)-1], settings, hostKeyCB, algoLookup, connectTimeout)
}

func dialChain(
	db *store.DB,
	vault *creds.Vault,
	chain []hop,
	settings *store.ResolvedSettings,
	hostKeyCB ssh.HostKeyCallback,
	algoLookup HostKeyAlgoLookup,
	connectTimeout time.Duration,
) (*ssh.Client, func(), error) {
	if len(chain) == 0 {
		return nil, func() {}, fmt.Errorf("empty chain")
	}

	var (
		clients []*ssh.Client
		prev    *ssh.Client
	)
	closeAll := func() { cleanup(clients) }

	for i, h := range chain {
		var methods []ssh.AuthMethod
		if h.AuthRef != nil {
			cred, err := db.GetCredential(*h.AuthRef)
			if err != nil {
				closeAll()
				return nil, func() {}, fmt.Errorf("%s: get credential %s: %w", h.Label, *h.AuthRef, err)
			}
			if h.Username == "" && cred.DefaultUsername != nil {
				h.Username = *cred.DefaultUsername
			}
			// dialChain backs batch exec, jump chains and VNC - non-
			// interactive paths where an opkssh login isn't user-cancelable.
			// Background ctx: the opkssh 5-min timeout still bounds it.
			auth, err := ResolveAuth(context.Background(), cred, vault)
			if err != nil {
				isLastHop := i == len(chain)-1
				if !(isLastHop && settings.PasswordOverride != nil) {
					closeAll()
					return nil, func() {}, fmt.Errorf("%s: %w", h.Label, err)
				}
			} else {
				methods = auth.ToAuthMethods()
			}
		}
		if i == len(chain)-1 && settings.PasswordOverride != nil {
			methods = append(methods, ssh.Password(*settings.PasswordOverride))
		}
		if h.Username == "" {
			closeAll()
			return nil, func() {}, fmt.Errorf("%s: no username", h.Label)
		}
		if len(methods) == 0 {
			closeAll()
			return nil, func() {}, fmt.Errorf("%s: no credential assigned and no password set", h.Label)
		}
		var pinnedAlgos []string
		if algoLookup != nil {
			pinnedAlgos = algoLookup(h.Hostname, int(h.Port))
		}
		cfg := &ssh.ClientConfig{
			User:              h.Username,
			Auth:              methods,
			HostKeyCallback:   hostKeyCB,
			HostKeyAlgorithms: pinnedAlgos,
			Timeout:           connectTimeout,
		}
		addr := fmt.Sprintf("%s:%d", h.Hostname, h.Port)
		var client *ssh.Client
		if i == 0 {
			conn, err := firstHopDial(context.Background(), settings, addr, connectTimeout)
			if err != nil {
				closeAll()
				return nil, func() {}, fmt.Errorf("%s: dial: %w", h.Label, err)
			}
			sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
			if err != nil {
				_ = conn.Close()
				closeAll()
				return nil, func() {}, fmt.Errorf("%s: ssh handshake: %w", h.Label, err)
			}
			client = ssh.NewClient(sshConn, chans, reqs)
		} else {
			netConn, err := prev.Dial("tcp", addr)
			if err != nil {
				closeAll()
				return nil, func() {}, fmt.Errorf("%s: dial through jump: %w", h.Label, err)
			}
			sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
			if err != nil {
				_ = netConn.Close()
				closeAll()
				return nil, func() {}, fmt.Errorf("%s: ssh handshake: %w", h.Label, err)
			}
			client = ssh.NewClient(sshConn, chans, reqs)
		}
		clients = append(clients, client)
		prev = client
	}
	return clients[len(clients)-1], closeAll, nil
}
