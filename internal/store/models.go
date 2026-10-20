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
	LocalShellKind *string `json:"local_shell_kind,omitempty"`
	// LocalShellDir is the working directory this connection's shell
	// starts in. nil / "" defers to the app-wide `local_shell_dir`
	// setting, which itself falls back to the user's home directory.
	// Ignored for SSH connections.
	LocalShellDir *string             `json:"local_shell_dir,omitempty"`
	Overrides     InheritableSettings `json:"overrides"`
	Tags          []string            `json:"tags"`
	Notes         string              `json:"notes"`
	Favorite      bool                `json:"favorite"`
	// OpenHidden starts this connection's tab hidden - the session runs
	// normally, it just stays out of the tab bar. For background helpers
	// whose output nobody watches.
	OpenHidden  bool    `json:"open_hidden"`
	IconImageID *string `json:"icon_image_id"`
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
	Username *string `json:"username,omitempty"`
	Port     *uint16 `json:"port,omitempty"`
	AuthRef  *string `json:"auth_ref,omitempty"`
	// OTPRef names a Bitwarden / KeePass credential whose item TOTP answers
	// the server's verification-code question, next to whatever AuthRef
	// authenticates with (a key plus a code from the password manager).
	// "" stops an inherited one.
	OTPRef            *string           `json:"otp_ref,omitempty"`
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
	// InitialCommandLineDelayMs pauses between the lines of a multi-line
	// InitialCommand. 0 (the default) sends them back to back, which is
	// what every connection did before this existed. A pause is needed
	// when a line replaces the shell that reads the next one - "sudo su -
	// user" is the case that motivated it: without a wait, line 2 is
	// swallowed by the outgoing shell instead of the new one. nil means
	// inherit from folder ancestry.
	InitialCommandLineDelayMs *uint32 `json:"initial_command_line_delay_ms,omitempty"`
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
	// ProbeLiveness gates the tree liveness probe (a TCP connect to the SSH
	// port that paints a green/red/unknown dot on connection rows inside
	// expanded folders). Tri-state inheritance so a folder can be opted out
	// (e.g. hosts that alarm on port scans, or a folder you never want probed).
	// Unset resolves true - probing happens wherever the global switch is on
	// unless a folder turns it off.
	ProbeLiveness *bool `json:"probe_liveness,omitempty"`
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
	// VncDefault makes the VNC console the DEFAULT connect action for a
	// connection (double-click / Enter open the console instead of an SSH
	// terminal). For a host reached only over VNC (a Windows box, a
	// KVM-over-IP / IPMI console) this avoids a pointless SSH attempt.
	// SSH stays available via the right-click menu. Only meaningful when
	// VncEnabled resolves true. Tri-state inheritance.
	VncDefault *bool `json:"vnc_default,omitempty"`
	// NetworkProfileID routes the FIRST SSH hop through a userspace
	// WireGuard tunnel (internal/wg). nil = inherit from ancestry;
	// "" = explicitly direct (breaks an inherited profile); otherwise
	// the id of a network_profiles row. Jump-chain hops after the
	// first ride the previous hop's SSH channel and need no network
	// of their own.
	NetworkProfileID *string `json:"network_profile_id,omitempty"`
	// TunnelOnly marks a connection that exists for its port forwards (the
	// `ssh -N` case): the default action starts it in the tunnel manager -
	// no shell, no PTY, no tab - instead of opening a terminal. A terminal
	// is still one right-click away. Tri-state inheritance so a folder of
	// tunnels sets it once.
	TunnelOnly *bool `json:"tunnel_only,omitempty"`
	// TunnelAutostart starts the tunnel when the app starts (once the vault
	// is open). Only meaningful when TunnelOnly resolves true. Tri-state
	// inheritance.
	TunnelAutostart *bool `json:"tunnel_autostart,omitempty"`
	// KafkaEnabled shows the "Open Kafka" action and the editor's Kafka
	// section. Opt-in like VNC: most hosts have no broker behind them.
	// Tri-state inheritance so a folder holding a cluster's hosts enables
	// it once.
	KafkaEnabled *bool `json:"kafka_enabled,omitempty"`
	// KafkaBootstrap is the seed broker list ("a:9092,b:9092"; a missing
	// port means 9092), as seen from wherever the brokers are dialed from:
	// the connection's host when KafkaUseTunnel, else this machine or the
	// last jump host. Often "127.0.0.1:9092" on a broker host itself.
	KafkaBootstrap *string `json:"kafka_bootstrap,omitempty"`
	// KafkaUseTunnel: broker connections are opened as channels on an SSH
	// session to this connection's host (so broker names resolve there).
	// When false they are dialed like DialVia does - from the last jump
	// host, else from here over the network profile. nil resolves to true:
	// a cluster behind the host is the usual case.
	KafkaUseTunnel *bool `json:"kafka_use_tunnel,omitempty"`
	// KafkaDefault makes the Kafka tab the default connect action
	// (double-click / Enter), like VncDefault. Only honoured when
	// KafkaEnabled resolves true.
	KafkaDefault *bool `json:"kafka_default,omitempty"`
	// KafkaReadOnly refuses every write: produce, topic create / delete /
	// config / partitions / truncate, group offset reset / delete. Enforced
	// in the backend, not only hidden in the UI. For production clusters.
	KafkaReadOnly *bool `json:"kafka_read_only,omitempty"`
	// KafkaTLS wraps every broker connection in TLS (end to end, inside the
	// tunnel); KafkaTLSSkipVerify accepts any broker certificate.
	KafkaTLS           *bool `json:"kafka_tls,omitempty"`
	KafkaTLSSkipVerify *bool `json:"kafka_tls_skip_verify,omitempty"`
	// KafkaSASL is the SASL mechanism: "" none, "plain", "scram-sha-256",
	// "scram-sha-512". The login comes from KafkaCredentialRef: its default
	// username and its password (vault, KeePass, Bitwarden, Infisical).
	// An explicit "" credential stops an inherited one.
	KafkaSASL          *string `json:"kafka_sasl,omitempty"`
	KafkaCredentialRef *string `json:"kafka_credential_ref,omitempty"`
	// DbEnabled shows the "Open database" action and the editor's
	// Database section. Opt-in like Kafka. Tri-state inheritance, so a
	// folder of database hosts sets engine, port and login once.
	DbEnabled *bool `json:"db_enabled,omitempty"`
	// DbEngine is "postgres", "mysql" (MySQL and MariaDB) or "clickhouse".
	DbEngine *string `json:"db_engine,omitempty"`
	// DbAddr is host[:port] as seen from wherever the database is dialed
	// from: the connection's host when DbUseTunnel, else this machine or
	// the last jump host. "" means 127.0.0.1 and the engine's port.
	DbAddr *string `json:"db_addr,omitempty"`
	// DbName is the database to open first ("" = the server's default).
	DbName *string `json:"db_name,omitempty"`
	// DbUseTunnel: database connections are channels on an SSH session to
	// this connection's host. nil resolves to true.
	DbUseTunnel *bool `json:"db_use_tunnel,omitempty"`
	// DbDefault makes the Database tab the default connect action, like
	// KafkaDefault. Only honoured when DbEnabled resolves true.
	DbDefault *bool `json:"db_default,omitempty"`
	// DbReadOnly refuses writes in the backend (see dbx.CheckReadOnly).
	DbReadOnly *bool `json:"db_read_only,omitempty"`
	// DbTLS is "disable" (nil), "require" or "verify-full".
	DbTLS *string `json:"db_tls,omitempty"`
	// DbCredentialRef is the database login: the credential's default
	// username and its password. An explicit "" stops an inherited one.
	DbCredentialRef *string `json:"db_credential_ref,omitempty"`
	// DbLabel names the connection's own database in the tree and on its
	// tab ("" = its database name). Connection-only, like DbTargets.
	DbLabel *string `json:"db_label,omitempty"`
	// DbInTree shows the connection's own database as a row under it in
	// the tree (nil = shown). Connection-only.
	DbInTree *bool `json:"db_in_tree,omitempty"`
	// DbDefaultTarget is the database "Double-click opens: Database"
	// opens and the tree lists first: a DbTargets id, "" / nil = the
	// connection's own. Connection-only.
	DbDefaultTarget *string `json:"db_default_target,omitempty"`
	// DbTargets are the databases listed under a connection (prod, stage,
	// another engine on the same host), each opening its own tab. A field
	// a target leaves nil takes the Db* value above. Set on connections;
	// not inherited from folders (the Db* defaults are). Empty: the
	// connection itself is the one database.
	DbTargets []DbTarget `json:"db_targets,omitempty"`
	// Sensitive marks production or otherwise critical hosts: the LLM
	// (MCP) cannot list, open, change or route through them, a Database
	// tab confirms statements that change data and Kafka asks for the
	// topic or group name before deleting it. Set on a folder it covers
	// everything under it; false on a connection opts it out.
	Sensitive *bool `json:"sensitive,omitempty"`
}

// DbTarget is one database under a connection.
type DbTarget struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Engine        *string `json:"engine,omitempty"`
	Addr          *string `json:"addr,omitempty"`
	Database      *string `json:"database,omitempty"`
	CredentialRef *string `json:"credential_ref,omitempty"`
	ReadOnly      *bool   `json:"read_only,omitempty"`
	TLS           *string `json:"tls,omitempty"`
	UseTunnel     *bool   `json:"use_tunnel,omitempty"`
	// InTree: a row under the connection in the tree (nil = shown).
	InTree *bool `json:"in_tree,omitempty"`
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
				spec.AuthRef != nil || spec.ConnectionID != nil || spec.Via != nil {
				j.Chain = &spec
			}
		}
	}
	return nil
}

// JumpHostSpec describes one hop. Recursive via.
//
// ConnectionID makes the hop a reference to a saved connection instead of a
// typed-in host: at connect time resolver.ExpandJumpRefs replaces it with
// that connection's resolved host, port, user and credential, preceded by
// the connection's own jump chain. The other fields of a reference hop are
// ignored, so the bastion has one source of truth.
type JumpHostSpec struct {
	Hostname     string        `json:"hostname"`
	Port         *uint16       `json:"port,omitempty"`
	Username     *string       `json:"username,omitempty"`
	AuthRef      *string       `json:"auth_ref,omitempty"`
	ConnectionID *string       `json:"connection_id,omitempty"`
	Via          *JumpHostSpec `json:"via,omitempty"`
	// Name is the referenced connection's name, filled in by the expansion
	// only (never saved) so connect progress and errors can say which
	// bastion failed.
	Name string `json:"name,omitempty"`
}

// ResolvedSettings is the merged result the SSH layer consumes.
type ResolvedSettings struct {
	Hostname          string            `json:"hostname"`
	Username          *string           `json:"username"`
	Port              uint16            `json:"port"`
	AuthRef           *string           `json:"auth_ref"`
	OTPRef            *string           `json:"otp_ref"` // TOTP source credential, nil = none
	JumpHost          *JumpHostSpec     `json:"jump_host"`
	SSHOptions        map[string]string `json:"ssh_options"`
	EnvVars           map[string]string `json:"env_vars"`
	ColorTag          *string           `json:"color_tag"`
	BroadcastGroupID  *string           `json:"broadcast_group_id"`
	KeepaliveInterval uint32            `json:"keepalive_interval"`
	TerminalType      string            `json:"terminal_type"`
	// InitialCommand is run in the shell right after connect ("" = none).
	InitialCommand string `json:"initial_command"`
	// InitialCommandLineDelayMs is the pause between lines of a multi-line
	// InitialCommand; 0 sends them back to back.
	InitialCommandLineDelayMs uint32 `json:"initial_command_line_delay_ms"`
	AutoReconnect             bool   `json:"auto_reconnect"`
	Verbose                   bool   `json:"verbose"`
	ProbeLiveness             bool   `json:"probe_liveness"`
	// VncPort is the resolved RFB port for the VNC console action,
	// defaulting to 5900 when unset anywhere in the chain. VncUseTunnel
	// says whether to reach it through the connection's SSH session.
	VncEnabled   bool   `json:"vnc_enabled"`
	VncPort      uint16 `json:"vnc_port"`
	VncUseTunnel bool   `json:"vnc_use_tunnel"`
	// VncDefault: the VNC console is the default connect action (double-
	// click opens it instead of an SSH terminal). Only honoured when
	// VncEnabled is true.
	VncDefault bool `json:"vnc_default"`
	// NetworkProfileID: non-nil when the first hop should dial through
	// the userspace WireGuard tunnel of this network profile. An
	// explicit "" override normalizes to nil here.
	NetworkProfileID *string `json:"network_profile_id"`
	// TunnelOnly / TunnelAutostart: see InheritableSettings. The SSH layer
	// reads TunnelOnly as "open no shell"; the app sets it per connect (a
	// tunnel-only connection can still be opened as a terminal), so the
	// resolved value is what the connection is, not how this session runs.
	TunnelOnly      bool `json:"tunnel_only"`
	TunnelAutostart bool `json:"tunnel_autostart"`
	// Kafka*: see InheritableSettings. KafkaBootstrap is trimmed, "" when
	// unset; KafkaUseTunnel defaults to true.
	KafkaEnabled       bool    `json:"kafka_enabled"`
	KafkaBootstrap     string  `json:"kafka_bootstrap"`
	KafkaUseTunnel     bool    `json:"kafka_use_tunnel"`
	KafkaDefault       bool    `json:"kafka_default"`
	KafkaReadOnly      bool    `json:"kafka_read_only"`
	KafkaTLS           bool    `json:"kafka_tls"`
	KafkaTLSSkipVerify bool    `json:"kafka_tls_skip_verify"`
	KafkaSASL          string  `json:"kafka_sasl"`
	KafkaCredentialRef *string `json:"kafka_credential_ref"`
	// Db*: see InheritableSettings. DbAddr and DbName are trimmed, ""
	// when unset; DbUseTunnel defaults to true, DbTLS to "disable".
	DbEnabled       bool       `json:"db_enabled"`
	DbEngine        string     `json:"db_engine"`
	DbAddr          string     `json:"db_addr"`
	DbName          string     `json:"db_name"`
	DbUseTunnel     bool       `json:"db_use_tunnel"`
	DbDefault       bool       `json:"db_default"`
	DbReadOnly      bool       `json:"db_read_only"`
	DbTLS           string     `json:"db_tls"`
	DbCredentialRef *string    `json:"db_credential_ref"`
	DbTargets       []DbTarget `json:"db_targets"`
	DbLabel         string     `json:"db_label"`
	DbDefaultTarget string     `json:"db_default_target"`
	// Sensitive: see InheritableSettings. Defaults to false.
	Sensitive bool `json:"sensitive"`
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
	// CredCertificate is a client TLS certificate + key (VPN client-cert
	// auth), stored in the vault as a PEM bundle. Not SSH auth material.
	CredCertificate CredentialKind = "certificate"
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
	// Uploaded image (since schema v27) or built-in (lucide) icon +
	// palette colour; one source at a time.
	IconImageID *string `json:"icon_image_id"`
	IconName    *string `json:"icon_name"`
	IconColor   *string `json:"icon_color"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
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
