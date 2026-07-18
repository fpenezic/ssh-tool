package store

import (
	"encoding/json"
	"time"
)

// Folder is a node in the connection tree. ParentID == nil means root.
type Folder struct {
	ID          string              `json:"id"`
	ParentID    *string             `json:"parent_id"`
	Name        string              `json:"name"`
	SortOrder   int64               `json:"sort_order"`
	Settings    InheritableSettings `json:"settings"`
	IconImageID *string             `json:"icon_image_id"`
	// IconName is a built-in (lucide) icon key; IconColor is a palette
	// name (see frontend palette.ts) tinting it. Mutually exclusive with
	// IconImageID - setting one clears the other. Both nil = default icon.
	IconName  *string `json:"icon_name"`
	IconColor *string `json:"icon_color"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// Connection is a leaf in the tree.
type Connection struct {
	ID        string  `json:"id"`
	FolderID  *string `json:"folder_id"`
	Name      string  `json:"name"`
	Hostname  string  `json:"hostname"`
	SortOrder int64   `json:"sort_order"`
	// Protocol selects how a connect is dialed: "ssh" (default - the
	// full SSH chain) or "local" (spawn a local PTY and run
	// InitialCommand). A local connection ignores hostname/port/auth/
	// jump; it is a saved local shell (telnet client, serial console,
	// "claude", a REPL, ...). Real column, defaulting to "ssh" so every
	// existing connection is unchanged.
	Protocol string `json:"protocol"`
	// LocalShellKind picks the shell for a "local" connection: nil / ""
	// = auto (per-platform default), else one of the kinds resolveShell
	// accepts ("bash"/"zsh"/"sh"/"powershell"/"cmd"/"wsl"). Ignored for
	// SSH connections.
	LocalShellKind *string             `json:"local_shell_kind,omitempty"`
	Overrides      InheritableSettings `json:"overrides"`
	Tags           []string            `json:"tags"`
	Notes          string              `json:"notes"`
	Favorite       bool                `json:"favorite"`
	Sensitive      bool                `json:"sensitive"`
	IconImageID    *string             `json:"icon_image_id"`
	// IconName / IconColor: built-in lucide icon + palette colour, same
	// semantics as on Folder. Mutually exclusive with IconImageID.
	IconName         *string `json:"icon_name"`
	IconColor        *string `json:"icon_color"`
	LastUsedAt       *int64  `json:"last_used_at"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	PasswordVaultKey *string `json:"password_vault_key,omitempty"`
	// VncPasswordVaultKey points at the vault entry holding this
	// connection's VNC (RFB) password, when set. Same lifecycle as
	// PasswordVaultKey - a real column, not part of overrides_json,
	// because the secret itself lives in the vault and only the key is
	// stored here. nil means "no stored VNC password" (noVNC prompts if
	// the server demands auth).
	VncPasswordVaultKey *string `json:"vnc_password_vault_key,omitempty"`
}

// InheritableSettings: every field is a pointer (or empty map / nil pointer
// helper struct) so we can distinguish "unset, inherit from parent" from
// "explicitly set to zero value".
//
// JumpHost uses the JumpHostOverride wrapper because there's a meaningful
// "explicit no-jump" case that's distinct from "inherit".
type InheritableSettings struct {
	Username          *string           `json:"username,omitempty"`
	Port              *uint16           `json:"port,omitempty"`
	AuthRef           *string           `json:"auth_ref,omitempty"`
	JumpHost          *JumpHostOverride `json:"jump_host,omitempty"`
	SSHOptions        map[string]string `json:"ssh_options,omitempty"`
	EnvVars           map[string]string `json:"env_vars,omitempty"`
	ColorTag          *string           `json:"color_tag,omitempty"`
	BroadcastGroupID  *string           `json:"broadcast_group_id,omitempty"`
	KeepaliveInterval *uint32           `json:"keepalive_interval,omitempty"`
	TerminalType      *string           `json:"terminal_type,omitempty"`
	// InitialCommand is run in the shell right after connect (e.g.
	// "cd /var/www", "tmux new -A -s main"). nil means inherit from
	// folder ancestry; "" means explicitly none (breaks an inherited
	// command). Sent to the TARGET hop's PTY only, with a trailing
	// newline, so it runs and lands in the user's own scrollback.
	InitialCommand *string `json:"initial_command,omitempty"`
	// AutoReconnect: when true, sessions that drop without a user
	// Disconnect trigger an exponential-backoff reconnect loop (capped).
	// nil means "inherit from folder ancestry"; false means "explicitly
	// don't reconnect even if the parent folder says yes".
	AutoReconnect *bool `json:"auto_reconnect,omitempty"`
	// Verbose: when true, the SSH layer emits diagnostic events
	// (resolved settings, TCP dial, handshake, auth attempts) to the
	// frontend so the user can see why a connect attempt fails. Same
	// tri-state inheritance as AutoReconnect.
	Verbose *bool `json:"verbose,omitempty"`
	// VncEnabled gates the "Open VNC console" action on a connection.
	// Most SSH hosts have no VNC server, so the console is opt-in: the
	// action and the editor's VNC fields only show when this resolves
	// true. Tri-state inheritance so a folder of desktops can enable it
	// for all children.
	VncEnabled *bool `json:"vnc_enabled,omitempty"`
	// VncPort is the RFB port for the "Open VNC console" action on a
	// generic (non-Proxmox) connection. nil means inherit; when unset
	// all the way up the chain the open path defaults to 5900.
	VncPort *uint16 `json:"vnc_port,omitempty"`
	// VncUseTunnel: when true, the VNC console dials the RFB port
	// through the connection's SSH session (so vnc_port is reached on
	// the remote's loopback, like a localhost-bound x11vnc). When false
	// the bridge dials host:vnc_port directly. Tri-state inheritance.
	VncUseTunnel *bool `json:"vnc_use_tunnel,omitempty"`
	// NetworkProfileID routes the FIRST SSH hop through a userspace
	// WireGuard tunnel (internal/wg). nil = inherit from ancestry;
	// "" = explicitly direct (breaks an inherited profile); otherwise
	// the id of a network_profiles row. Jump-chain hops after the
	// first ride the previous hop's SSH channel and need no network
	// of their own.
	NetworkProfileID *string `json:"network_profile_id,omitempty"`
}

// JumpHostOverride is tagged-union with two variants: "none" (explicit
// strip-inherited-chain) or "chain" with the full spec at .Chain.
//
// JSON shape: {"kind":"none"} or {"kind":"chain", "chain": {...JumpHostSpec...}}
type JumpHostOverride struct {
	Kind  string        `json:"kind"`            // "none" | "chain"
	Chain *JumpHostSpec `json:"chain,omitempty"` // present when Kind == "chain"
}

func (j JumpHostOverride) MarshalJSON() ([]byte, error) {
	if j.Kind == "none" {
		return json.Marshal(map[string]string{"kind": "none"})
	}
	if j.Kind == "chain" && j.Chain != nil {
		return json.Marshal(struct {
			Kind  string        `json:"kind"`
			Chain *JumpHostSpec `json:"chain"`
		}{Kind: "chain", Chain: j.Chain})
	}
	return json.Marshal(nil)
}

func (j *JumpHostOverride) UnmarshalJSON(b []byte) error {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	j.Kind = probe.Kind
	if probe.Kind == "chain" {
		// Two accepted shapes: {kind, chain:{...}} (new) and {kind, ...fields} (legacy).
		var nested struct {
			Chain *JumpHostSpec `json:"chain"`
		}
		if err := json.Unmarshal(b, &nested); err == nil && nested.Chain != nil {
			j.Chain = nested.Chain
		} else {
			// Fallback: try flat shape so any previously-saved rows still load.
			var spec JumpHostSpec
			if err := json.Unmarshal(b, &spec); err != nil {
				return err
			}
			if spec.Hostname != "" || spec.Port != nil || spec.Username != nil ||
				spec.AuthRef != nil || spec.Via != nil {
				j.Chain = &spec
			}
		}
	}
	return nil
}

// JumpHostSpec describes one hop. Recursive via.
type JumpHostSpec struct {
	Hostname string        `json:"hostname"`
	Port     *uint16       `json:"port,omitempty"`
	Username *string       `json:"username,omitempty"`
	AuthRef  *string       `json:"auth_ref,omitempty"`
	Via      *JumpHostSpec `json:"via,omitempty"`
}

// ResolvedSettings is the merged result the SSH layer consumes.
type ResolvedSettings struct {
	Hostname          string            `json:"hostname"`
	Username          *string           `json:"username"`
	Port              uint16            `json:"port"`
	AuthRef           *string           `json:"auth_ref"`
	JumpHost          *JumpHostSpec     `json:"jump_host"`
	SSHOptions        map[string]string `json:"ssh_options"`
	EnvVars           map[string]string `json:"env_vars"`
	ColorTag          *string           `json:"color_tag"`
	BroadcastGroupID  *string           `json:"broadcast_group_id"`
	KeepaliveInterval uint32            `json:"keepalive_interval"`
	TerminalType      string            `json:"terminal_type"`
	// InitialCommand is run in the shell right after connect ("" = none).
	InitialCommand string `json:"initial_command"`
	AutoReconnect  bool   `json:"auto_reconnect"`
	Verbose        bool   `json:"verbose"`
	// VncPort is the resolved RFB port for the VNC console action,
	// defaulting to 5900 when unset anywhere in the chain. VncUseTunnel
	// says whether to reach it through the connection's SSH session.
	VncEnabled   bool   `json:"vnc_enabled"`
	VncPort      uint16 `json:"vnc_port"`
	VncUseTunnel bool   `json:"vnc_use_tunnel"`
	// NetworkProfileID: non-nil when the first hop should dial through
	// the userspace WireGuard tunnel of this network profile. An
	// explicit "" override normalizes to nil here.
	NetworkProfileID *string `json:"network_profile_id"`
	// PasswordOverride carries a per-connection plaintext password resolved
	// from the vault by app.go before passing to the SSH layer. Never
	// serialised to JSON (not shown in the resolved-settings preview).
	PasswordOverride *string `json:"-"`
}

// CredentialKind / StorageMode mirror the Rust enums.
type CredentialKind string

const (
	CredPassword CredentialKind = "password"
	CredKey      CredentialKind = "key"
	CredAgent    CredentialKind = "agent"
	CredOpkssh   CredentialKind = "opkssh"
	CredVault    CredentialKind = "vault"
	// CredAPIToken stores an opaque token paired with an identifier
	// (e.g. proxmox `user@realm!tokenid` + secret). Not usable for
	// SSH auth - referenced by external integrations like the dynamic
	// inventory providers.
	CredAPIToken CredentialKind = "api_token"
)

type StorageMode string

const (
	StorageManaged  StorageMode = "managed"
	StorageFileRef  StorageMode = "file_ref"
	StorageExternal StorageMode = "external"
)

// CredentialFolder groups credentials (mirrors connection Folder).
type CredentialFolder struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	SortOrder int64   `json:"sort_order"`
	// Built-in (lucide) icon + palette colour. Credential folders never
	// had an uploaded-image icon, so these are the only icon fields.
	IconName  *string `json:"icon_name"`
	IconColor *string `json:"icon_color"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// CredentialRef holds metadata only. Secrets live in vault under VaultKey.
type CredentialRef struct {
	ID                   string         `json:"id"`
	FolderID             *string        `json:"folder_id"`
	Name                 string         `json:"name"`
	Kind                 CredentialKind `json:"kind"`
	StorageMode          StorageMode    `json:"storage_mode"`
	Hint                 string         `json:"hint"`
	Tags                 []string       `json:"tags"`
	Config               map[string]any `json:"config"`
	PublicKey            *string        `json:"public_key"`
	VaultKey             *string        `json:"vault_key"`
	DefaultUsername      *string        `json:"default_username"`
	LastRotatedAt        *int64         `json:"last_rotated_at"`
	ExpiresAt            *int64         `json:"expires_at"`
	RotationReminderDays *int64         `json:"rotation_reminder_days"`
	RetainHistory        bool           `json:"retain_history"`
	IconImageID          *string        `json:"icon_image_id"`
	// IconName / IconColor: built-in lucide icon + palette colour, same
	// semantics as on Connection. Mutually exclusive with IconImageID.
	IconName  *string `json:"icon_name"`
	IconColor *string `json:"icon_color"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// CredentialHistoryEntry is a metadata-only audit record (or pointer to a
// retained-history entry in the vault).
type CredentialHistoryEntry struct {
	ID           string `json:"id"`
	CredentialID string `json:"credential_id"`
	ChangedAt    int64  `json:"changed_at"`
	Note         string `json:"note"`
	RotatedBy    string `json:"rotated_by"`
	HasValue     bool   `json:"has_value"`
}

// CredentialSecretHistoryEntry is one snapshot of a previous secret
// value, sealed in the file vault under VaultAccount. The plaintext
// is never returned by ListSecretHistory - callers fetch it on
// demand via RevealSecretHistory (which also resets the clipboard
// auto-clear timer, same as a live reveal).
type CredentialSecretHistoryEntry struct {
	ID           string `json:"id"`
	CredentialID string `json:"credential_id"`
	RotatedAt    int64  `json:"rotated_at"`
	VaultAccount string `json:"vault_account"`
	Note         string `json:"note"`
	RotatedBy    string `json:"rotated_by"`
}

// Helpers ---------------------------------------------------------------

func now() int64        { return time.Now().Unix() }
func ptr[T any](v T) *T { return &v }
