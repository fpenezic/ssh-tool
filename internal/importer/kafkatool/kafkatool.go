// Package kafkatool imports cluster profiles from kafka-tool, a standalone
// Kafka GUI (~/.config/kafka-tool/profiles.json):
//
//	[{"id": "...", "name": "staging", "bootstrapServers": "127.0.0.1:9092",
//	  "sshTunnelEnabled": true, "sshHost": "10.0.0.5", "sshPort": 22,
//	  "sshUsername": "deploy", "sshAuthType": "KEY_FILE", "sshKeyPath": "",
//	  "sshPassword": "", "sshProxyJumpEnabled": true,
//	  "sshProxyJumpHost": "bastion.example.com", "sshProxyJumpPort": 22,
//	  "sshProxyJumpUsername": "jump", "sshProxyJumpKeyPath": ""}, ...]
//
// Each profile becomes one connection with Kafka enabled:
//   - with an SSH tunnel: the connection is the SSH host (its bootstrap is
//     what that host sees), the proxy jump becomes an inline jump host;
//   - without: the brokers are dialed directly, the connection's hostname is
//     the first broker (for the tree only) and the Kafka tab is its default
//     action, since there is no SSH host to open.
//
// kafka-tool keeps the SSH password in plain text. A password login is
// handed to the caller (savePassword) for the vault and never written to
// the store; a key file is not read - the user assigns a key credential.
package kafkatool

import (
	"encoding/json"
	"fmt"
	"strings"

	"ssh-tool/internal/store"
)

// Profile is one kafka-tool cluster profile. Field names are kafka-tool's.
type Profile struct {
	Name                 string `json:"name"`
	BootstrapServers     string `json:"bootstrapServers"`
	SSHTunnelEnabled     bool   `json:"sshTunnelEnabled"`
	SSHHost              string `json:"sshHost"`
	SSHPort              int    `json:"sshPort"`
	SSHUsername          string `json:"sshUsername"`
	SSHAuthType          string `json:"sshAuthType"` // KEY_FILE | PASSWORD
	SSHKeyPath           string `json:"sshKeyPath"`
	SSHPassword          string `json:"sshPassword"`
	SSHProxyJumpEnabled  bool   `json:"sshProxyJumpEnabled"`
	SSHProxyJumpHost     string `json:"sshProxyJumpHost"`
	SSHProxyJumpPort     int    `json:"sshProxyJumpPort"`
	SSHProxyJumpUsername string `json:"sshProxyJumpUsername"`
	SSHProxyJumpKeyPath  string `json:"sshProxyJumpKeyPath"`
}

// Summary mirrors the other importers' shape.
type Summary struct {
	FoldersCreated     int      `json:"folders_created"`
	ConnectionsCreated int      `json:"connections_created"`
	ConnectionsSkipped int      `json:"connections_skipped"`
	PasswordsStored    int      `json:"passwords_stored"`
	Warnings           []string `json:"warnings"`
}

// FolderName is the folder the profiles land in, under the chosen root.
const FolderName = "kafka-tool"

// Parse reads profiles.json.
func Parse(text string) ([]Profile, error) {
	var ps []Profile
	if err := json.Unmarshal([]byte(text), &ps); err != nil {
		return nil, fmt.Errorf("parse kafka-tool profiles.json: %w", err)
	}
	return ps, nil
}

// Planned is one profile turned into a connection, before it is written.
type Planned struct {
	Conn store.NewConnection
	// Password is the SSH login password to put in the vault, "" for none.
	Password string
	Warnings []string
}

// Plan maps a profile to a connection. It touches nothing.
func Plan(p Profile) (*Planned, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("a profile has no name")
	}
	bootstrap := strings.TrimSpace(p.BootstrapServers)
	if bootstrap == "" {
		return nil, fmt.Errorf("profile %s has no bootstrap servers", name)
	}
	on, off := true, false
	out := &Planned{Conn: store.NewConnection{Name: name, Overrides: store.InheritableSettings{
		KafkaEnabled:   &on,
		KafkaBootstrap: &bootstrap,
	}}}
	o := &out.Conn.Overrides

	if !p.SSHTunnelEnabled {
		first := strings.TrimSpace(strings.Split(bootstrap, ",")[0])
		host := first
		if i := strings.LastIndex(first, ":"); i > 0 && !strings.HasSuffix(first, "]") {
			host = first[:i]
		}
		out.Conn.Hostname = strings.Trim(host, "[]")
		o.KafkaUseTunnel = &off
		o.KafkaDefault = &on
		return out, nil
	}

	if strings.TrimSpace(p.SSHHost) == "" {
		return nil, fmt.Errorf("profile %s has an SSH tunnel but no SSH host", name)
	}
	out.Conn.Hostname = strings.TrimSpace(p.SSHHost)
	o.KafkaUseTunnel = &on
	if u := strings.TrimSpace(p.SSHUsername); u != "" {
		o.Username = &u
	}
	if p.SSHPort > 0 && p.SSHPort != 22 && p.SSHPort <= 65535 {
		port := uint16(p.SSHPort)
		o.Port = &port
	}
	switch strings.ToUpper(p.SSHAuthType) {
	case "PASSWORD":
		out.Password = p.SSHPassword
	default:
		key := strings.TrimSpace(p.SSHKeyPath)
		if key == "" {
			key = "the default ~/.ssh key"
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s: logged in with %s - assign a key credential (or use the SSH agent)", name, key))
	}
	if p.SSHProxyJumpEnabled && strings.TrimSpace(p.SSHProxyJumpHost) != "" {
		jump := &store.JumpHostSpec{Hostname: strings.TrimSpace(p.SSHProxyJumpHost)}
		if p.SSHProxyJumpPort > 0 && p.SSHProxyJumpPort != 22 && p.SSHProxyJumpPort <= 65535 {
			port := uint16(p.SSHProxyJumpPort)
			jump.Port = &port
		}
		if u := strings.TrimSpace(p.SSHProxyJumpUsername); u != "" {
			jump.Username = &u
		}
		o.JumpHost = &store.JumpHostOverride{Kind: "chain", Chain: jump}
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s: jump host %s needs a credential (kafka-tool used a key file)", name, jump.Hostname))
	}
	return out, nil
}

// Apply writes the profiles as connections in a "kafka-tool" folder under
// rootFolderID ("" = the root). A name already in the tree is skipped.
// savePassword stores an SSH password for a created connection (the vault);
// nil skips passwords with a warning.
func Apply(db *store.DB, ps []Profile, rootFolderID string, savePassword func(connID, password string) error) (*Summary, error) {
	sum := &Summary{Warnings: []string{}}
	existing, err := db.ListConnections(nil)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	taken := map[string]bool{}
	for _, c := range existing {
		taken[c.Name] = true
	}

	folderID := ""
	ensureFolder := func() (string, error) {
		if folderID != "" {
			return folderID, nil
		}
		folders, err := db.ListFolders()
		if err != nil {
			return "", err
		}
		for _, f := range folders {
			parent := ""
			if f.ParentID != nil {
				parent = *f.ParentID
			}
			if parent == rootFolderID && f.Name == FolderName {
				folderID = f.ID
				return folderID, nil
			}
		}
		in := store.NewFolder{Name: FolderName}
		if rootFolderID != "" {
			r := rootFolderID
			in.ParentID = &r
		}
		f, err := db.CreateFolder(in)
		if err != nil {
			return "", err
		}
		sum.FoldersCreated++
		folderID = f.ID
		return folderID, nil
	}

	for _, p := range ps {
		plan, err := Plan(p)
		if err != nil {
			sum.Warnings = append(sum.Warnings, err.Error())
			sum.ConnectionsSkipped++
			continue
		}
		if taken[plan.Conn.Name] {
			sum.ConnectionsSkipped++
			continue
		}
		fid, err := ensureFolder()
		if err != nil {
			return nil, fmt.Errorf("create folder: %w", err)
		}
		plan.Conn.FolderID = &fid
		c, err := db.CreateConnection(plan.Conn)
		if err != nil {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("create %s: %v", plan.Conn.Name, err))
			continue
		}
		sum.ConnectionsCreated++
		taken[plan.Conn.Name] = true
		sum.Warnings = append(sum.Warnings, plan.Warnings...)
		if plan.Password != "" {
			if savePassword == nil {
				sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s: SSH password not imported", plan.Conn.Name))
			} else if err := savePassword(c.ID, plan.Password); err != nil {
				sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s: SSH password not stored: %v", plan.Conn.Name, err))
			} else {
				sum.PasswordsStored++
			}
		}
	}
	return sum, nil
}
