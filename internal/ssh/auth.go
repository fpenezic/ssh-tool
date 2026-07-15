package ssh

import (
	"context"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// ErrVaultLocked is returned by ResolveAuth (via resolvePassword /
// resolveKey) when the credential's secret is missing because the
// vault is currently locked. The caller (the SSH connect path)
// turns this into a typed event the frontend can act on: re-open
// the unlock gate, then offer to retry the connect.
type ErrVaultLockedT struct {
	CredentialName string
}

func (e *ErrVaultLockedT) Error() string {
	if e.CredentialName != "" {
		return "vault is locked - credential " + e.CredentialName + " can't be decrypted"
	}
	return "vault is locked"
}

// IsVaultLocked reports whether err in its chain is an
// ErrVaultLockedT, so the SSH layer can fail-fast with a typed
// signal instead of treating it as a generic resolve error.
func IsVaultLocked(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*ErrVaultLockedT)
	return ok
}

// ContainsVaultLocked walks the error chain (errors.Is would also
// work but ErrVaultLockedT isn't a sentinel - it carries the
// credential name as state) to find an ErrVaultLockedT anywhere
// in the wrap stack. Used by app.go after Connect() returns to
// decide whether to emit the typed "vault locked during connect"
// event.
func ContainsVaultLocked(err error) bool {
	for err != nil {
		if _, ok := err.(*ErrVaultLockedT); ok {
			return true
		}
		// Standard library unwrap.
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// AuthMaterial is the resolved per-hop authentication state, ready to be
// turned into ssh.AuthMethod values.
type AuthMaterial struct {
	Password string
	Signers  []ssh.Signer // public-key / cert auth
	Agent    agent.ExtendedAgent
}

// ToAuthMethods turns the resolved material into the slice of AuthMethods
// that ssh.ClientConfig wants. Order matters - we try the strongest method
// first.
func (m *AuthMaterial) ToAuthMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	if len(m.Signers) > 0 {
		methods = append(methods, ssh.PublicKeys(m.Signers...))
	}
	if m.Agent != nil {
		methods = append(methods, ssh.PublicKeysCallback(m.Agent.Signers))
	}
	if m.Password != "" {
		methods = append(methods, ssh.Password(m.Password))
	}
	return methods
}

// InlineAuthMethods builds SSH auth methods from raw secret material that
// isn't backed by a tree credential - used by SFTP sync, where the auth has
// to be typed in directly so a fresh machine can bootstrap (the tree
// credential it would otherwise reference doesn't exist until after the
// first pull). Exactly one of password / keyPEM is expected; an encrypted
// key needs keyPassphrase. Returns an error if neither is usable.
func InlineAuthMethods(password, keyPEM, keyPassphrase string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if keyPEM != "" {
		var signer ssh.Signer
		var err error
		if keyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(keyPEM), []byte(keyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(keyPEM))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no usable auth material (need a password or a private key)")
	}
	return methods, nil
}

// KeepassResolveHook, when set by the host (app.go), resolves a credential's
// KeePass reference to its secret. Wired as a package var - like BrowserOpenHook
// (gotcha 28) - so the ssh layer routes through the KeePass manager without
// importing it or the manager reaching into every ResolveAuth call site.
// Returns the secret and whether the credential actually carried a keepass_ref
// (handled=false => not a KeePass credential, fall through to the normal path).
var KeepassResolveHook func(cred *store.CredentialRef) (secret string, handled bool, err error)

// ResolveAuth turns a credential reference into AuthMaterial. Side-effecting
// for `opkssh` (may run the OIDC login) and `agent` (opens UDS connection).
// ctx cancels an in-flight opkssh OIDC login; the other kinds ignore it.
func ResolveAuth(ctx context.Context, cred *store.CredentialRef, vault *creds.Vault) (*AuthMaterial, error) {
	// A credential carrying a KeePass reference resolves through the hook
	// regardless of its kind: a "password" cred yields a password auth method,
	// a "key" cred yields a signer. The secret is pulled from the .kdbx in
	// memory and never persisted.
	if KeepassResolveHook != nil {
		secret, handled, err := KeepassResolveHook(cred)
		if err != nil {
			return nil, err
		}
		if handled {
			return keepassAuthMaterial(cred, secret)
		}
	}
	switch cred.Kind {
	case store.CredPassword:
		return resolvePassword(cred, vault)
	case store.CredKey:
		return resolveKey(cred, vault)
	case store.CredAgent:
		return resolveAgent(cred)
	case store.CredOpkssh:
		return resolveOpkssh(ctx, cred, vault)
	case store.CredVault:
		return nil, fmt.Errorf("vault credential type not yet supported")
	default:
		return nil, fmt.Errorf("unknown credential kind: %s", cred.Kind)
	}
}

// keepassAuthMaterial turns a secret pulled from a KeePass entry into auth
// material. For a key credential the secret is a PEM private key; for anything
// else it is treated as a password.
func keepassAuthMaterial(cred *store.CredentialRef, secret string) (*AuthMaterial, error) {
	if secret == "" {
		return nil, fmt.Errorf("keepass credential %s resolved to an empty secret", cred.Name)
	}
	if cred.Kind == store.CredKey {
		signer, err := ssh.ParsePrivateKey([]byte(secret))
		if err != nil {
			if _, missing := err.(*ssh.PassphraseMissingError); missing {
				return nil, fmt.Errorf("keepass key %s is passphrase-protected; store an unencrypted key or the passphrase separately", cred.Name)
			}
			return nil, fmt.Errorf("parse keepass key %s: %w", cred.Name, err)
		}
		return &AuthMaterial{Signers: []ssh.Signer{signer}}, nil
	}
	return &AuthMaterial{Password: secret}, nil
}

func resolvePassword(cred *store.CredentialRef, vault *creds.Vault) (*AuthMaterial, error) {
	if cred.VaultKey == nil {
		return nil, fmt.Errorf("password has no vault_key")
	}
	pw, ok, err := vault.Get(*cred.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("vault get: %w", err)
	}
	if !ok {
		// Distinguish vault-locked from secret-missing - the locked
		// case is recoverable (unlock + retry) while a genuinely
		// absent secret needs a credential edit.
		if vault.Status().Kind == creds.StatusLocked {
			return nil, &ErrVaultLockedT{CredentialName: cred.Name}
		}
		return nil, fmt.Errorf("password missing in vault")
	}
	return &AuthMaterial{Password: pw}, nil
}

func resolveKey(cred *store.CredentialRef, vault *creds.Vault) (*AuthMaterial, error) {
	switch cred.StorageMode {
	case store.StorageManaged:
		if cred.VaultKey == nil {
			return nil, fmt.Errorf("managed key has no vault_key")
		}
		keyText, ok, err := vault.Get(*cred.VaultKey)
		if err != nil {
			return nil, fmt.Errorf("vault get: %w", err)
		}
		if !ok {
			if vault.Status().Kind == creds.StatusLocked {
				return nil, &ErrVaultLockedT{CredentialName: cred.Name}
			}
			return nil, fmt.Errorf("key missing in vault")
		}
		signer, err := ssh.ParsePrivateKey([]byte(keyText))
		if err != nil {
			// Managed keys are typically unencrypted because we either
			// generated them without a passphrase or imported after
			// decrypt. If it IS encrypted somehow, surface a clear msg.
			if _, missing := err.(*ssh.PassphraseMissingError); missing {
				return nil, fmt.Errorf("managed key is encrypted but passphrase storage not yet wired")
			}
			return nil, fmt.Errorf("parse managed key: %w", err)
		}
		return &AuthMaterial{Signers: []ssh.Signer{signer}}, nil

	case store.StorageFileRef:
		path, _ := cred.Config["key_path"].(string)
		if path == "" {
			return nil, fmt.Errorf("file_ref key has no key_path in config")
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		// Try unencrypted first; fall back to passphrase from vault.
		signer, err := ssh.ParsePrivateKey(bytes)
		if err != nil {
			if _, missing := err.(*ssh.PassphraseMissingError); missing {
				if cred.VaultKey == nil {
					return nil, fmt.Errorf("encrypted key %s needs passphrase in vault", path)
				}
				pass, ok, vErr := vault.Get(*cred.VaultKey)
				if vErr != nil {
					return nil, fmt.Errorf("vault get passphrase: %w", vErr)
				}
				if !ok {
					return nil, fmt.Errorf("passphrase missing in vault for %s", path)
				}
				signer, err = ssh.ParsePrivateKeyWithPassphrase(bytes, []byte(pass))
				if err != nil {
					return nil, fmt.Errorf("decrypt %s: %w", path, err)
				}
			} else {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
		}
		return &AuthMaterial{Signers: []ssh.Signer{signer}}, nil

	case store.StorageExternal:
		return nil, fmt.Errorf("key credential in 'external' mode is unsupported")
	}
	return nil, fmt.Errorf("unknown storage mode: %s", cred.StorageMode)
}

func resolveAgent(cred *store.CredentialRef) (*AuthMaterial, error) {
	sockPath, _ := cred.Config["socket_path"].(string)
	if sockPath == "" {
		sockPath = os.Getenv("SSH_AUTH_SOCK")
	}
	if sockPath == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set and no socket_path configured")
	}
	// Refuse to sign through a socket that isn't owned by us or sits
	// in a world-writable parent - see agent_validate_unix.go for the
	// rationale and the exact ruleset. No-op on Windows where the
	// agent is a named pipe and ACLs are OS-managed.
	if err := validateAgentSocket(sockPath); err != nil {
		return nil, fmt.Errorf("agent socket rejected: %w", err)
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("agent dial %s: %w", sockPath, err)
	}
	return &AuthMaterial{Agent: agent.NewClient(conn)}, nil
}

func resolveOpkssh(ctx context.Context, cred *store.CredentialRef, vault *creds.Vault) (*AuthMaterial, error) {
	cfg, err := ParseOpksshConfig(cred)
	if err != nil {
		return nil, err
	}
	auth, err := EnsureFreshCert(ctx, cfg, vault)
	if err != nil {
		return nil, err
	}
	return &AuthMaterial{Signers: []ssh.Signer{auth.Signer}}, nil
}
