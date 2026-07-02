// Package rdm imports a Devolutions Remote Desktop Manager JSON export
// into the ssh-tool store.
//
// Schema notes (reverse-engineered from real exports):
//
//	ConnectionType 25 - Two roles depending on whether Terminal.Host is set:
//	    • No Terminal.Host: group/folder template. Defines inherited settings
//	      (credentials, username, VPN) for all connections in the folder.
//	      NOT created as a standalone connection.
//	    • Terminal.Host present: SSH session reached via VPN/jump.
//	ConnectionType 26 - Two roles depending on whether Credentials is set:
//	    • Credentials field populated: a credential entry (password or SSH key).
//	    • Credentials field absent: a folder/group declaration.
//	ConnectionType 76 - SSH session with explicit SSHGateways jump list.
//	ConnectionType 77 - SSH session (plain or certificate-based).
//	All other types are non-SSH (RDP, web, file…). Skipped, counted.
//
//	Group is a backslash-separated path: "Work\\hetzner" = folder Work,
//	subfolder hetzner. Hierarchies are created on the fly.
package rdm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"ssh-tool/internal/store"
)

// File mirrors only the top-level fields we care about.
type File struct {
	Connections []Entry `json:"Connections"`
}

// Entry is the union of all RDM connection types we deal with.
type Entry struct {
	ConnectionType int    `json:"ConnectionType"`
	ID             string `json:"ID"`
	Name           string `json:"Name"`
	Group          string `json:"Group"`

	Image    string `json:"Image"`    // base64-encoded PNG (or empty)
	ImageMD5 string `json:"ImageMD5"` // useful for dedup

	// Top-level credential reference (used by type-76 connections).
	CredentialConnectionID        string `json:"CredentialConnectionID"`
	CredentialConnectionSavedPath string `json:"CredentialConnectionSavedPath"`

	Terminal    *Terminal    `json:"Terminal"`
	VPN         *VPN         `json:"VPN"`
	GroupDetails *GroupDetails `json:"GroupDetails"`
	Credentials  *Credentials  `json:"Credentials"`
}

// Terminal holds the SSH-specific configuration of an Entry.
type Terminal struct {
	Host         string    `json:"Host"`
	HostPort     int       `json:"HostPort"`
	Username     string    `json:"Username"`
	SSHGateways  []Gateway `json:"SSHGateways"`
	UseSSHGateway bool     `json:"UseSSHGateway"`
	PortForwards []PortForward `json:"PortForwards"`

	PrivateKeyCertificateFileName string `json:"PrivateKeyCertificateFileName"`
	PrivateKeyCertificateType     int    `json:"PrivateKeyCertificateType"`
	PrivateKeyType                int    `json:"PrivateKeyType"`
}

// Gateway is a single SSH jump host on an SSHGateways list.
type Gateway struct {
	Host                   string `json:"Host"`
	HostPort               int    `json:"HostPort"`
	Username               string `json:"Username"`
	CredentialConnectionID string `json:"CredentialConnectionID"`
}

// VPN holds the "use this other RDM connection as a tunnel" reference.
// VPNGroupName is the Name of another Entry; we resolve it in a second pass.
type VPN struct {
	VPNGroupName string `json:"VPNGroupName"`
	Mode         int    `json:"Mode"`
	Application  int    `json:"Application"`
	Enabled      bool   `json:"Enabled"`
}

// GroupDetails carries the inherited username / domain set on a folder template.
type GroupDetails struct {
	UserName string `json:"UserName"`
	Domain   string `json:"Domain"`
}

// Credentials is populated on ConnectionType-26 credential entries.
// SafePassword / SafePrivateKeyData are RDM-encrypted and cannot be decrypted
// without the RDM installation key; we import the plaintext metadata only.
type Credentials struct {
	UserName                string `json:"UserName"`
	CredentialType          int    `json:"CredentialType"`
	PrivateKeyType          int    `json:"PrivateKeyType"`             // 2 = SSH key
	PrivateKeyOverrideUsername string `json:"PrivateKeyOverrideUsername"` // username for key auth
	PublicKey               string `json:"PublicKey"`
	SafePassword            string `json:"SafePassword"`               // encrypted, not imported
	SafePrivateKeyData      string `json:"SafePrivateKeyData"`          // encrypted, not imported
}

// PortForward mirrors an RDM port-forward entry.
type PortForward struct {
	Source          string `json:"Source"`
	SourcePort      int    `json:"SourcePort"`
	Destination     string `json:"Destination"`
	DestinationPort int    `json:"DestinationPort"`
	Mode            int    `json:"Mode"`
}

// Summary is what the IPC layer hands back to the UI after an import.
type Summary struct {
	FoldersCreated        int                   `json:"folders_created"`
	ConnectionsCreated    int                   `json:"connections_created"`
	ImagesStored          int                   `json:"images_stored"`
	JumpResolved          int                   `json:"jump_resolved"`
	JumpUnresolved        int                   `json:"jump_unresolved"`
	SkippedNonSSH         int                   `json:"skipped_non_ssh"`
	CredentialsCreated    int                   `json:"credentials_created"`
	CredentialsNeedSecret int                   `json:"credentials_need_secret"`
	UnresolvedJumps       []string              `json:"unresolved_jumps"`
	UnresolvedCreds       []string              `json:"unresolved_creds"`
	NeedsAttention        []ConnectionAttention `json:"needs_attention"`
	Warnings              []string              `json:"warnings"`
}

// ConnectionAttention is one row of "post-import the user must touch this
// connection". Reasons we emit:
//
//   "inline-username":
//       RDM had a username typed into the entry but no credential
//       reference. We imported the username as an override; the user
//       must attach a credential.
//   "external-cred-ref":
//       RDM linked a credential by saved-path (vault somewhere), but we
//       don't copy the actual secret - credentials are imported into
//       our vault only on explicit user action. The connection has
//       overrides for username/host but no usable auth_ref.
//   "private-key-file":
//       RDM had a PrivateKeyCertificateFileName set (key on disk). Our
//       store has no concept of "filesystem key without a credential
//       row"; the user has to import the key + assign it.
type ConnectionAttention struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// isCredentialEntry returns true for entries that carry a Credentials payload.
// ConnectionType is intentionally not checked: different RDM versions may use
// different types for credential entries; the presence of meaningful Credentials
// data is the reliable signal.
func isCredentialEntry(e Entry) bool {
	if e.Credentials == nil {
		return false
	}
	c := e.Credentials
	return c.UserName != "" || c.SafePassword != "" ||
		c.PublicKey != "" || c.SafePrivateKeyData != "" ||
		c.PrivateKeyType != 0 || c.CredentialType != 0
}

// isGroupTemplate returns true for type-25 entries that define folder-level
// inherited settings (VPN, credentials, username) but are NOT actual SSH
// connections. The tell-tale sign is the absence of Terminal.Host.
func isGroupTemplate(e Entry) bool {
	return e.ConnectionType == 25 && (e.Terminal == nil || strings.TrimSpace(e.Terminal.Host) == "")
}

// IsSSHType reports whether a given RDM ConnectionType is one we treat as
// an SSH session.
func IsSSHType(t int) bool {
	switch t {
	case 25, 76, 77:
		return true
	}
	return false
}

// IsFolderType reports whether a given RDM ConnectionType is a folder/group
// (and not a credential entry).
func IsFolderType(t int) bool {
	return t == 26
}

// Importer holds context across the parse + write phases.
type Importer struct {
	db  *store.DB
	rdm File

	// rootFolderID, when non-empty, is the connection-tree folder under which
	// all top-level imported folders are created. Empty means the DB root.
	rootFolderID string

	// connByName: VPN.VPNGroupName -> Connectable. Populated during pass 3A.
	connByName map[string]*Connectable

	// rdmIDToConn: secondary lookup by RDM ID for SSHGateways[].CredentialConnectionID.
	rdmIDToConn map[string]*Connectable

	// folderPath -> created folder id; built during pass 1.
	folderIDs map[string]string

	// imageMD5 -> stored image id; populated during pass 2.
	imageIDs map[string]string

	// credByPath: RDM credential path / id -> credential_ref.id we created.
	credByPath map[string]string

	// credByRdmID: RDM entry ID -> credential_ref.id (populated in pass 0).
	credByRdmID map[string]string

	// credFolderIDs: credential folder path -> credential_folder.id.
	credFolderIDs map[string]string

	// jumpOnlyEntries[Group + "\" + Name] = true when the entry's name matches
	// a subfolder of its Group. Those entries act as the VPN-jump host for
	// every connection in that subfolder and should NOT be created as
	// standalone connection rows.
	jumpOnlyEntries map[string]bool

	summary Summary
}

// Connectable is the resolved per-entry record used for JumpHostSpec building.
type Connectable struct {
	Name     string
	Hostname string
	Port     int
	Username string
	CredRef  string // credential_refs.id or "" if unmapped
}

// Parse reads a JSON byte stream into a File.
func Parse(raw []byte) (File, error) {
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("parse RDM JSON: %w", err)
	}
	return f, nil
}

// NewImporter constructs an Importer; call Import to execute.
// rootFolderID, if non-empty, places all imported top-level folders under that
// existing connection-tree folder instead of at the DB root.
func NewImporter(db *store.DB, f File, rootFolderID string) *Importer {
	return &Importer{
		db:              db,
		rdm:             f,
		rootFolderID:    rootFolderID,
		connByName:      map[string]*Connectable{},
		rdmIDToConn:     map[string]*Connectable{},
		folderIDs:       map[string]string{},
		imageIDs:        map[string]string{},
		credByPath:      map[string]string{},
		credByRdmID:     map[string]string{},
		credFolderIDs:   map[string]string{},
		jumpOnlyEntries: map[string]bool{},
	}
}

// Import runs the whole pipeline. Idempotent within a single run: existing
// rows are never modified; only new rows are inserted.
func (im *Importer) Import() (Summary, error) {
	if err := im.pass0Credentials(); err != nil {
		return im.summary, fmt.Errorf("credentials: %w", err)
	}
	if err := im.pass1Folders(); err != nil {
		return im.summary, fmt.Errorf("folders: %w", err)
	}
	if err := im.pass2Images(); err != nil {
		return im.summary, fmt.Errorf("images: %w", err)
	}
	if err := im.pass3Connections(); err != nil {
		return im.summary, fmt.Errorf("connections: %w", err)
	}
	return im.summary, nil
}

// pass0Credentials imports type-26 entries that carry a Credentials payload
// as credential_refs. The vault secret cannot be recovered from the RDM export
// (it is encrypted with an RDM installation key), so only plaintext metadata
// is imported. The summary notes how many need their secret set manually.
func (im *Importer) pass0Credentials() error {
	for _, e := range im.rdm.Connections {
		if !isCredentialEntry(e) {
			continue
		}
		folderID, err := im.ensureCredentialFolderPath(e.Group)
		if err != nil {
			im.summary.Warnings = append(im.summary.Warnings,
				fmt.Sprintf("credential folder for %q: %v", e.Name, err))
		}

		// Determine kind from PrivateKeyType (2 = SSH key) or CredentialType (14 = key).
		kind := store.CredPassword
		var publicKey *string
		if e.Credentials.PrivateKeyType == 2 || e.Credentials.CredentialType == 14 {
			kind = store.CredKey
			if pk := strings.TrimSpace(e.Credentials.PublicKey); pk != "" {
				publicKey = &pk
			}
		}

		var defUser *string
		if u := e.Credentials.UserName; u != "" {
			defUser = &u
		} else if u := e.Credentials.PrivateKeyOverrideUsername; u != "" {
			defUser = &u
		}

		cred, err := im.db.CreateCredential(store.NewCredential{
			Name:            e.Name,
			Kind:            kind,
			StorageMode:     store.StorageManaged,
			FolderID:        folderID,
			Hint:            "Imported from RDM - set secret manually",
			Tags:            []string{"rdm-import"},
			DefaultUsername: defUser,
			PublicKey:       publicKey,
			Config:          map[string]any{},
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "conflict") {
				im.summary.Warnings = append(im.summary.Warnings,
					fmt.Sprintf("credential %q already exists, skipped", e.Name))
				continue
			}
			im.summary.Warnings = append(im.summary.Warnings,
				fmt.Sprintf("credential %q: %v", e.Name, err))
			continue
		}
		im.summary.CredentialsCreated++
		im.summary.CredentialsNeedSecret++

		// Register for lookup so connections can reference them.
		im.credByRdmID[e.ID] = cred.ID
		path := joinPath(e.Group, e.Name)
		im.credByPath[path] = cred.ID
	}
	return nil
}

// ensureCredentialFolderPath creates credential_folders along a backslash-
// separated path if they don't already exist. Returns the deepest folder's id
// pointer, or nil for the root.
func (im *Importer) ensureCredentialFolderPath(rdmPath string) (*string, error) {
	rdmPath = strings.TrimSpace(rdmPath)
	if rdmPath == "" {
		return nil, nil
	}
	if id, ok := im.credFolderIDs[rdmPath]; ok {
		return &id, nil
	}
	segments := strings.Split(rdmPath, `\`)
	var parentID *string
	var accum []string
	for _, seg := range segments {
		accum = append(accum, seg)
		key := strings.Join(accum, `\`)
		if id, ok := im.credFolderIDs[key]; ok {
			parentID = &id
			continue
		}
		f, err := im.db.CreateCredentialFolder(seg, parentID)
		if err != nil {
			// Already exists or other transient issue - try to continue.
			continue
		}
		im.credFolderIDs[key] = f.ID
		parentID = &f.ID
	}
	return parentID, nil
}

// pass1Folders walks every entry's Group string and ensures the folder
// hierarchy exists in the connection tree.
// Credential entries (type-26 with Credentials) are skipped - their group
// path belongs to the credential tree, handled in pass0.
// Group template entries (type-25 with no Terminal.Host) are used to apply
// inherited settings (username) to their parent folder.
func (im *Importer) pass1Folders() error {
	// Folders explicitly declared as ConnectionType 26 without credentials first.
	for _, e := range im.rdm.Connections {
		if !IsFolderType(e.ConnectionType) || isCredentialEntry(e) {
			continue
		}
		path := joinPath(e.Group, e.Name)
		if _, err := im.ensureFolderPath(path); err != nil {
			return err
		}
	}
	// Then any connection's parent Group path that isn't already created.
	for _, e := range im.rdm.Connections {
		if IsFolderType(e.ConnectionType) || isCredentialEntry(e) {
			continue
		}
		// Group templates: ensure the folder for e.Group exists so we can
		// apply settings to it below.
		if e.Group == "" {
			continue
		}
		if _, err := im.ensureFolderPath(e.Group); err != nil {
			return err
		}
	}

	// Apply group template settings (type-25, no Terminal.Host) to the folder.
	for _, e := range im.rdm.Connections {
		if !isGroupTemplate(e) || e.Group == "" {
			continue
		}
		folderID, ok := im.folderIDs[e.Group]
		if !ok {
			continue
		}
		settings := store.InheritableSettings{}
		changed := false
		if e.GroupDetails != nil && e.GroupDetails.UserName != "" {
			u := e.GroupDetails.UserName
			settings.Username = &u
			changed = true
		}
		// Resolve group-level credential reference.
		if e.CredentialConnectionSavedPath != "" {
			if credID, ok := im.credByPath[e.CredentialConnectionSavedPath]; ok {
				settings.AuthRef = &credID
				changed = true
			}
		}
		if changed {
			if _, err := im.db.UpdateFolder(store.UpdateFolder{
				ID:       folderID,
				Settings: &settings,
			}); err != nil {
				im.summary.Warnings = append(im.summary.Warnings,
					fmt.Sprintf("apply group settings for %q: %v", e.Group, err))
			}
		}
	}
	return nil
}

// ensureFolderPath creates folders along a backslash-separated RDM path if
// they don't already exist. Returns the deepest folder's id, or "" for root.
// When im.rootFolderID is set, all top-level segments are created under it.
func (im *Importer) ensureFolderPath(rdmPath string) (string, error) {
	rdmPath = strings.TrimSpace(rdmPath)
	if rdmPath == "" {
		return im.rootFolderID, nil
	}
	if id, ok := im.folderIDs[rdmPath]; ok {
		return id, nil
	}
	segments := strings.Split(rdmPath, `\`)
	parentID := im.rootFolderID
	var accum []string
	for _, seg := range segments {
		accum = append(accum, seg)
		key := strings.Join(accum, `\`)
		if id, ok := im.folderIDs[key]; ok {
			parentID = id
			continue
		}
		input := store.NewFolder{
			Name:     seg,
			Settings: store.InheritableSettings{},
		}
		if parentID != "" {
			p := parentID
			input.ParentID = &p
		}
		f, err := im.db.CreateFolder(input)
		if err != nil {
			return "", fmt.Errorf("create folder %q: %w", key, err)
		}
		im.folderIDs[key] = f.ID
		parentID = f.ID
		im.summary.FoldersCreated++
	}
	return parentID, nil
}

// pass2Images decodes inline base64 PNGs, stores them deduplicated by MD5,
// and then assigns icons to any credentials imported in pass0.
func (im *Importer) pass2Images() error {
	for _, e := range im.rdm.Connections {
		if e.Image == "" || e.ImageMD5 == "" {
			continue
		}
		if _, ok := im.imageIDs[e.ImageMD5]; ok {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(e.Image)
		if err != nil {
			im.summary.Warnings = append(im.summary.Warnings,
				fmt.Sprintf("image decode failed for %q: %v", e.Name, err))
			continue
		}
		imgID, err := im.db.PutImage(data, "image/png")
		if err != nil {
			im.summary.Warnings = append(im.summary.Warnings,
				fmt.Sprintf("image store failed for %q: %v", e.Name, err))
			continue
		}
		im.imageIDs[e.ImageMD5] = imgID
		im.summary.ImagesStored++
	}

	// Assign icons to credentials that were created in pass0.
	for _, e := range im.rdm.Connections {
		if e.ImageMD5 == "" || !isCredentialEntry(e) {
			continue
		}
		credID, ok := im.credByRdmID[e.ID]
		if !ok {
			continue
		}
		imgID, ok := im.imageIDs[e.ImageMD5]
		if !ok {
			continue
		}
		if err := im.db.SetCredentialIcon(credID, imgID); err != nil {
			im.summary.Warnings = append(im.summary.Warnings,
				fmt.Sprintf("credential icon for %q: %v", e.Name, err))
		}
	}
	return nil
}

// pass3Connections is two-phase internally: first index every SSH entry into
// connByName / rdmIDToConn; then create connection rows with resolved jump hosts.
//
// Group templates (type-25 no host) are skipped here - they are handled in
// pass1Folders.
//
// Jump-only entries (name matches a subfolder of their group) are indexed for
// VPN resolution but not created as standalone rows.
func (im *Importer) pass3Connections() error {
	// Detect jump-only entries up front.
	for _, e := range im.rdm.Connections {
		if !IsSSHType(e.ConnectionType) || isGroupTemplate(e) {
			continue
		}
		path := joinPath(e.Group, e.Name)
		if _, ok := im.folderIDs[path]; ok {
			im.jumpOnlyEntries[path] = true
		}
	}

	// Phase A: index connection metadata for VPN resolution.
	for _, e := range im.rdm.Connections {
		if !IsSSHType(e.ConnectionType) || isGroupTemplate(e) {
			continue
		}
		c := connectableFromEntry(e, im.credByRdmID, im.credByPath)
		im.connByName[e.Name] = c
		if e.ID != "" {
			im.rdmIDToConn[e.ID] = c
		}
	}

	// Phase B: create connection rows.
	for _, e := range im.rdm.Connections {
		if isGroupTemplate(e) {
			// Group template: settings already applied in pass1, not a connection.
			continue
		}
		if !IsSSHType(e.ConnectionType) {
			if !IsFolderType(e.ConnectionType) && !isCredentialEntry(e) {
				im.summary.SkippedNonSSH++
			}
			continue
		}
		if im.jumpOnlyEntries[joinPath(e.Group, e.Name)] {
			continue
		}
		if err := im.createConnection(e); err != nil {
			im.summary.Warnings = append(im.summary.Warnings,
				fmt.Sprintf("connection %q: %v", e.Name, err))
			continue
		}
		im.summary.ConnectionsCreated++
	}
	return nil
}

func connectableFromEntry(e Entry, credByRdmID, credByPath map[string]string) *Connectable {
	c := &Connectable{Name: e.Name}
	if e.Terminal != nil {
		c.Hostname = e.Terminal.Host
		c.Port = e.Terminal.HostPort
		c.Username = e.Terminal.Username
	}
	// Resolve credential reference: ID takes precedence, path is fallback.
	if e.CredentialConnectionID != "" {
		if id, ok := credByRdmID[e.CredentialConnectionID]; ok {
			c.CredRef = id
		}
	}
	if c.CredRef == "" && e.CredentialConnectionSavedPath != "" {
		if id, ok := credByPath[e.CredentialConnectionSavedPath]; ok {
			c.CredRef = id
		}
	}
	return c
}

func (im *Importer) createConnection(e Entry) error {
	folderID, _ := im.folderIDs[e.Group]
	host := ""
	port := 0
	username := ""
	if e.Terminal != nil {
		host = e.Terminal.Host
		port = e.Terminal.HostPort
		username = e.Terminal.Username
	}

	overrides := store.InheritableSettings{}
	if port > 0 && port != 22 {
		p := uint16(port)
		overrides.Port = &p
	}
	if username != "" {
		u := username
		overrides.Username = &u
	}

	// Resolve credential reference for auth_ref.
	authRef := ""
	if e.CredentialConnectionID != "" {
		if id, ok := im.credByRdmID[e.CredentialConnectionID]; ok {
			authRef = id
		}
	}
	if authRef == "" && e.CredentialConnectionSavedPath != "" {
		if id, ok := im.credByPath[e.CredentialConnectionSavedPath]; ok {
			authRef = id
		}
	}
	if authRef != "" {
		overrides.AuthRef = &authRef
	}

	// Resolve jump host.
	if jh := im.resolveJump(e); jh != nil {
		overrides.JumpHost = jh
		im.summary.JumpResolved++
	}

	in := store.NewConnection{
		Name:      e.Name,
		Hostname:  host,
		Overrides: overrides,
		Notes:     fmt.Sprintf("Imported from RDM. Original ID: %s", e.ID),
	}
	if folderID != "" {
		fid := folderID
		in.FolderID = &fid
	}
	conn, err := im.db.CreateConnection(in)
	if err != nil {
		return err
	}

	if e.ImageMD5 != "" {
		if imgID, ok := im.imageIDs[e.ImageMD5]; ok {
			if err := im.db.SetConnectionIcon(conn.ID, imgID); err != nil {
				return fmt.Errorf("set icon: %w", err)
			}
		}
	}

	// Per-connection "you need to look at this" guidance. Only fire when
	// pass0 didn't already resolve the auth_ref (otherwise the credential
	// was imported and no user action is needed).
	switch {
	case e.CredentialConnectionSavedPath != "" && authRef == "":
		// RDM linked an external vault entry that pass0 couldn't map.
		im.summary.NeedsAttention = append(im.summary.NeedsAttention, ConnectionAttention{
			Name:     e.Name,
			Hostname: host,
			Reason:   "external-cred-ref",
			Detail:   e.CredentialConnectionSavedPath,
		})
		im.summary.UnresolvedCreds = appendUnique(
			im.summary.UnresolvedCreds, e.CredentialConnectionSavedPath)
	case e.Terminal != nil && e.Terminal.PrivateKeyCertificateFileName != "":
		// Entry referenced a private key file on disk. We don't copy
		// files - user must import the key into the vault and bind it.
		im.summary.NeedsAttention = append(im.summary.NeedsAttention, ConnectionAttention{
			Name:     e.Name,
			Hostname: host,
			Reason:   "private-key-file",
			Detail:   e.Terminal.PrivateKeyCertificateFileName,
		})
	case username != "" && authRef == "":
		// Inline username, no credential reference. The username made it
		// across as an override; the password did not.
		im.summary.NeedsAttention = append(im.summary.NeedsAttention, ConnectionAttention{
			Name:     e.Name,
			Hostname: host,
			Reason:   "inline-username",
			Detail:   username,
		})
	}
	return nil
}

// resolveJump walks the RDM jump-host indirection and returns a JumpHostOverride.
func (im *Importer) resolveJump(e Entry) *store.JumpHostOverride {
	if e.Terminal != nil && e.Terminal.UseSSHGateway && len(e.Terminal.SSHGateways) > 0 {
		var head *store.JumpHostSpec
		for i := len(e.Terminal.SSHGateways) - 1; i >= 0; i-- {
			gw := e.Terminal.SSHGateways[i]
			spec := &store.JumpHostSpec{
				Hostname: gw.Host,
			}
			if gw.HostPort > 0 && gw.HostPort != 22 {
				p := uint16(gw.HostPort)
				spec.Port = &p
			}
			if gw.Username != "" {
				u := gw.Username
				spec.Username = &u
			}
			if head != nil {
				spec.Via = head
			}
			head = spec
		}
		if head != nil {
			return &store.JumpHostOverride{Kind: "chain", Chain: head}
		}
		return nil
	}

	if e.VPN != nil && e.VPN.VPNGroupName != "" {
		gw, ok := im.connByName[e.VPN.VPNGroupName]
		if !ok {
			im.summary.JumpUnresolved++
			im.summary.UnresolvedJumps = appendUnique(im.summary.UnresolvedJumps, e.VPN.VPNGroupName)
			return nil
		}
		spec := &store.JumpHostSpec{Hostname: gw.Hostname}
		if gw.Port > 0 && gw.Port != 22 {
			p := uint16(gw.Port)
			spec.Port = &p
		}
		if gw.Username != "" {
			u := gw.Username
			spec.Username = &u
		}
		if gw.CredRef != "" {
			spec.AuthRef = &gw.CredRef
		}
		return &store.JumpHostOverride{Kind: "chain", Chain: spec}
	}
	return nil
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// joinPath joins an RDM Group path with an additional segment using backslash.
func joinPath(group, name string) string {
	group = strings.TrimSpace(group)
	name = strings.TrimSpace(name)
	if group == "" {
		return name
	}
	if name == "" {
		return group
	}
	return group + `\` + name
}
