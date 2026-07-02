// Package exporter serialises a portion of the connections tree
// (folders + connections + optional credentials) into a portable
// archive. Two formats are supported, sharing one canonical shape:
//
//   - TOML - diff-friendly, intended for hand-editable backups
//   - JSON - programmatic exchange + smaller payloads
//
// The companion ImportArchive does the reverse: dry-run report + apply.
//
// Credentials handling
// --------------------
// By default credentials are referenced by id only, never serialised
// with their secret material. Set Options.IncludeCredentials = true to
// also write a credentials section. Secret material (passwords,
// private keys stored as managed in the vault) is wrapped using the
// same XChaCha20-Poly1305 + argon2id pattern the file vault uses, with
// a passphrase provided by the caller. The export key is independent
// of the running vault - restore on another machine works with the
// passphrase alone.
package exporter

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"ssh-tool/internal/store"
)

// Format is the on-disk format choice.
type Format string

const (
	FormatTOML Format = "toml"
	FormatJSON Format = "json"
)

// Archive is the canonical serialised shape. Same structure regardless
// of TOML/JSON - only the encoding differs.
type Archive struct {
	// SchemaVersion lets future imports detect old archives and apply
	// migrations if needed. Bump on any breaking change.
	SchemaVersion int    `json:"schema_version" toml:"schema_version"`
	GeneratedAt   int64  `json:"generated_at" toml:"generated_at"`
	GeneratedBy   string `json:"generated_by" toml:"generated_by"`

	Folders     []ArchiveFolder     `json:"folders" toml:"folders"`
	Connections []ArchiveConnection `json:"connections" toml:"connections"`
	Credentials []ArchiveCredential `json:"credentials,omitempty" toml:"credentials,omitempty"`
	// CredentialFolders carries the credential tree's own folder
	// hierarchy (separate from connection folders) so import can
	// rebuild it - without this section every imported credential
	// lands flat. Only folders on the ancestry path of an exported
	// credential are included. Optional: archives written before this
	// section existed import fine, just flat.
	CredentialFolders []ArchiveCredFolder `json:"credential_folders,omitempty" toml:"credential_folders,omitempty"`
	// Images carries the custom icon blobs referenced by exported
	// folders / connections / credentials (icon_image_id), base64 in
	// the archive, content-addressed (md5) on import so re-imports and
	// shared logos never duplicate rows. Optional + skipped entirely
	// by StripIcon; old archives without it import fine.
	Images []ArchiveImage `json:"images,omitempty" toml:"images,omitempty"`
	// Forwards carries port-forward definitions (local / remote /
	// dynamic-SOCKS5) attached to the exported connections. Each row
	// references its parent via ConnectionID. SOCKS bookmarks travel
	// inline on the forward - they're the most useful part for the
	// reader.
	Forwards []ArchivePortForward `json:"forwards,omitempty" toml:"forwards,omitempty"`

	// EncryptedSecrets present when Options.IncludeCredentials is set.
	// Maps credentialID -> base64(nonce || ciphertext).
	EncryptedSecrets *EncryptedSecretBlock `json:"encrypted_secrets,omitempty" toml:"encrypted_secrets,omitempty"`
}

// ArchiveImage is one custom icon blob. ID is the source DB's image
// id - import remaps it (PutImage dedupes by md5, so the same logo
// arriving twice resolves to one row).
type ArchiveImage struct {
	ID   string `json:"id" toml:"id"`
	MIME string `json:"mime" toml:"mime"`
	B64  string `json:"b64" toml:"b64"`
}

type ArchiveFolder struct {
	ID          string                    `json:"id" toml:"id"`
	ParentID    *string                   `json:"parent_id,omitempty" toml:"parent_id,omitempty"`
	Name        string                    `json:"name" toml:"name"`
	IconImageID *string                   `json:"icon_image_id,omitempty" toml:"icon_image_id,omitempty"`
	Settings    store.InheritableSettings `json:"settings" toml:"settings"`
	// Dynamic carries the provider config when this folder is a
	// dynamic-inventory folder (Proxmox, Hetzner, ...). Without it the
	// folder imported as a plain empty folder. Cached entries are NOT
	// exported - the importing side refreshes from the provider.
	Dynamic *ArchiveDynamicFolder `json:"dynamic,omitempty" toml:"dynamic,omitempty"`
}

// ArchiveDynamicFolder mirrors store.DynamicFolder minus the runtime
// fields (last_pulled_at, last_error). Config may reference credentials
// (api_token_credential_id, jump_credential_id, override_credential_id);
// those ids are remapped on import like every other credential ref.
type ArchiveDynamicFolder struct {
	Provider       string         `json:"provider" toml:"provider"`
	Config         map[string]any `json:"config" toml:"config"`
	RefreshSeconds int            `json:"refresh_seconds" toml:"refresh_seconds"`
}

// dynCredConfigKeys lists dynamic-folder config keys whose value is a
// credential id. Shared by the exporter (collect referenced creds) and
// the importer (remap to the freshly minted ids).
var dynCredConfigKeys = []string{
	"api_token_credential_id",
	"jump_credential_id",
	"override_credential_id",
}

type ArchiveConnection struct {
	ID          string                    `json:"id" toml:"id"`
	FolderID    *string                   `json:"folder_id,omitempty" toml:"folder_id,omitempty"`
	Name        string                    `json:"name" toml:"name"`
	Hostname    string                    `json:"hostname" toml:"hostname"`
	Overrides   store.InheritableSettings `json:"overrides" toml:"overrides"`
	Tags        []string                  `json:"tags,omitempty" toml:"tags,omitempty"`
	Notes       string                    `json:"notes,omitempty" toml:"notes,omitempty"`
	Favorite    bool                      `json:"favorite,omitempty" toml:"favorite,omitempty"`
	IconImageID *string                   `json:"icon_image_id,omitempty" toml:"icon_image_id,omitempty"`
}

type ArchivePortForward struct {
	ID           string                `json:"id" toml:"id"`
	ConnectionID string                `json:"connection_id" toml:"connection_id"`
	Kind         string                `json:"kind" toml:"kind"` // local|remote|dynamic
	LocalAddr    *string               `json:"local_addr,omitempty" toml:"local_addr,omitempty"`
	LocalPort    *uint16               `json:"local_port,omitempty" toml:"local_port,omitempty"`
	RemoteHost   *string               `json:"remote_host,omitempty" toml:"remote_host,omitempty"`
	RemotePort   *uint16               `json:"remote_port,omitempty" toml:"remote_port,omitempty"`
	AutoStart    bool                  `json:"auto_start,omitempty" toml:"auto_start,omitempty"`
	Description  string                `json:"description,omitempty" toml:"description,omitempty"`
	Bookmarks    []store.ProxyBookmark `json:"bookmarks,omitempty" toml:"bookmarks,omitempty"`
}

type ArchiveCredFolder struct {
	ID       string  `json:"id" toml:"id"`
	ParentID *string `json:"parent_id,omitempty" toml:"parent_id,omitempty"`
	Name     string  `json:"name" toml:"name"`
}

type ArchiveCredential struct {
	ID              string               `json:"id" toml:"id"`
	FolderID        *string              `json:"folder_id,omitempty" toml:"folder_id,omitempty"`
	Name            string               `json:"name" toml:"name"`
	Kind            store.CredentialKind `json:"kind" toml:"kind"`
	StorageMode     store.StorageMode    `json:"storage_mode" toml:"storage_mode"`
	Hint            string               `json:"hint,omitempty" toml:"hint,omitempty"`
	Tags            []string             `json:"tags,omitempty" toml:"tags,omitempty"`
	Config          map[string]any       `json:"config,omitempty" toml:"config,omitempty"`
	PublicKey       *string              `json:"public_key,omitempty" toml:"public_key,omitempty"`
	DefaultUsername *string              `json:"default_username,omitempty" toml:"default_username,omitempty"`
	IconImageID     *string              `json:"icon_image_id,omitempty" toml:"icon_image_id,omitempty"`
}

// EncryptedSecretBlock carries the KDF parameters used + the ciphertexts.
// Re-deriving the key on import only needs the passphrase + this struct.
type EncryptedSecretBlock struct {
	KDF        string `json:"kdf" toml:"kdf"`   // "argon2id"
	Salt       string `json:"salt" toml:"salt"` // base64
	MemoryKiB  uint32 `json:"memory_kib" toml:"memory_kib"`
	Iterations uint32 `json:"iterations" toml:"iterations"`
	Parallel   uint8  `json:"parallel" toml:"parallel"`
	// CipherBy is keyed by credential id; value = base64(nonce || ciphertext).
	CipherBy map[string]string `json:"cipher_by" toml:"cipher_by"`
	// ConnCipherBy carries per-connection password overrides (the
	// "password without a credential entry" path), keyed by the
	// archive's connection id. Same sealing as CipherBy. Optional -
	// archives from before this field import fine without it.
	ConnCipherBy map[string]string `json:"conn_cipher_by,omitempty" toml:"conn_cipher_by,omitempty"`
}

const currentSchemaVersion = 1

// Argon2 parameters - match the file vault defaults (interactive-grade,
// see CLAUDE.md gotcha #9 for the rationale).
const (
	argonMemKiB     uint32 = 19 * 1024
	argonIterations uint32 = 2
	argonParallel   uint8  = 1
	keyLen          uint32 = 32
)

// Options drives Build.
type Options struct {
	// IncludeCredentials writes a credentials section + encrypted secret
	// block. Requires Passphrase to be non-empty.
	IncludeCredentials bool
	Passphrase         string

	// StripNotes drops the free-form Notes field from every exported
	// connection. Use when sharing externally - notes commonly hold
	// internal docs, ticket numbers, owner contacts.
	StripNotes bool
	// StripTags clears Tags on connections and credentials so the
	// recipient doesn't inherit your local taxonomy.
	StripTags bool
	// StripColor clears ColorTag overrides on folders + connections
	// so the export uses your recipient's defaults.
	StripColor bool
	// StripIcon clears IconImageID on folders + connections and drops
	// the icon Images section. Re-imports get default Lucide icons.
	StripIcon bool
	// ConvertAuthRefToInherit rewrites every connection's AuthRef
	// override to nil so the connection inherits credentials from
	// its folder ancestry on import. Useful when sharing connections
	// without their credentials - the recipient supplies their own
	// folder-level credential. Has no effect when IncludeCredentials
	// is true (the credential references survive on purpose).
	ConvertAuthRefToInherit bool
}

// SecretFetcher is supplied by the caller (app.go) so the exporter
// doesn't need vault.Vault wiring of its own. Returns (plaintext, true)
// on hit, (nil, false) if the credential exists but has no managed
// secret (e.g. agent / opkssh that lives entirely in vault config).
type SecretFetcher func(credID, vaultKey string) ([]byte, bool, error)

// Build assembles an Archive for the given root selection. Folders +
// connections beneath each rootFolderID (or root if nil) are included,
// plus any standalone connections passed in extraConnIDs.
//
// db is read-only here; the caller still owns the transaction.
func Build(
	db *store.DB,
	rootFolderIDs []string,
	extraConnIDs []string,
	opts Options,
	fetchSecret SecretFetcher,
) (*Archive, error) {
	allFolders, err := db.ListFolders()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	allConns, err := db.ListConnections(nil)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}

	keepFolder := map[string]bool{}
	keepConn := map[string]bool{}

	// Empty selection = export everything.
	exportAll := len(rootFolderIDs) == 0 && len(extraConnIDs) == 0
	if exportAll {
		for _, f := range allFolders {
			keepFolder[f.ID] = true
		}
		for _, c := range allConns {
			keepConn[c.ID] = true
		}
	} else {
		// Build child-of-folder index for descent.
		childrenOf := map[string][]string{}
		for _, f := range allFolders {
			pid := ""
			if f.ParentID != nil {
				pid = *f.ParentID
			}
			childrenOf[pid] = append(childrenOf[pid], f.ID)
		}
		var visit func(string)
		visit = func(id string) {
			if keepFolder[id] {
				return
			}
			keepFolder[id] = true
			for _, c := range childrenOf[id] {
				visit(c)
			}
		}
		for _, id := range rootFolderIDs {
			visit(id)
		}
		// Connections inside any kept folder + extras.
		for _, c := range allConns {
			if c.FolderID != nil && keepFolder[*c.FolderID] {
				keepConn[c.ID] = true
			}
		}
		for _, id := range extraConnIDs {
			keepConn[id] = true
		}
	}

	// Stable order so diffs are clean run-to-run.
	sort.Slice(allFolders, func(i, j int) bool { return allFolders[i].ID < allFolders[j].ID })
	sort.Slice(allConns, func(i, j int) bool { return allConns[i].ID < allConns[j].ID })

	arc := &Archive{
		SchemaVersion: currentSchemaVersion,
		GeneratedAt:   time.Now().Unix(),
		GeneratedBy:   "ssh-tool",
	}
	for _, f := range allFolders {
		if !keepFolder[f.ID] {
			continue
		}
		// If the parent isn't part of this export (subtree export
		// rooted at this folder, or quick-share from the tree
		// context menu), drop ParentID so the importer treats this
		// folder as a root. Otherwise the import would warn "parent
		// missing from archive, placing at root" and lose the
		// inheritance chain for every top-level entry.
		parentID := f.ParentID
		if parentID != nil && !keepFolder[*parentID] {
			parentID = nil
		}
		arc.Folders = append(arc.Folders, ArchiveFolder{
			ID:          f.ID,
			ParentID:    parentID,
			Name:        f.Name,
			IconImageID: f.IconImageID,
			Settings:    f.Settings,
		})
	}

	// Attach provider config to dynamic folders in the kept set.
	dynFolders, err := db.ListDynamicFolders()
	if err != nil {
		return nil, fmt.Errorf("list dynamic folders: %w", err)
	}
	dynByFolder := map[string]*store.DynamicFolder{}
	for i := range dynFolders {
		dynByFolder[dynFolders[i].FolderID] = &dynFolders[i]
	}
	for i := range arc.Folders {
		if d, ok := dynByFolder[arc.Folders[i].ID]; ok {
			arc.Folders[i].Dynamic = &ArchiveDynamicFolder{
				Provider:       d.Provider,
				Config:         d.Config,
				RefreshSeconds: d.RefreshSeconds,
			}
		}
	}
	// Per-connection password overrides live in the vault under the
	// key recorded on the row; remember them so the secrets block can
	// seal them alongside credential secrets.
	connPassKeys := map[string]string{}
	for _, c := range allConns {
		if !keepConn[c.ID] {
			continue
		}
		if c.PasswordVaultKey != nil && *c.PasswordVaultKey != "" {
			connPassKeys[c.ID] = *c.PasswordVaultKey
		}
		// Same orphan-FK guard as folders above: a connection
		// included via `extraConnIDs` (quick-share of individual
		// rows) may sit in a folder that isn't in the archive.
		// Strip FolderID so it imports at root rather than warning.
		folderID := c.FolderID
		if folderID != nil && !keepFolder[*folderID] {
			folderID = nil
		}
		arc.Connections = append(arc.Connections, ArchiveConnection{
			ID:          c.ID,
			FolderID:    folderID,
			Name:        c.Name,
			Hostname:    c.Hostname,
			Overrides:   c.Overrides,
			Tags:        c.Tags,
			Notes:       c.Notes,
			Favorite:    c.Favorite,
			IconImageID: c.IconImageID,
		})

		// Forwards (incl. SOCKS bookmarks) per kept connection.
		fwds, err := db.ListPortForwards(c.ID)
		if err != nil {
			return nil, fmt.Errorf("list forwards for %s: %w", c.ID, err)
		}
		for _, f := range fwds {
			arc.Forwards = append(arc.Forwards, ArchivePortForward{
				ID:           f.ID,
				ConnectionID: f.ConnectionID,
				Kind:         f.Kind,
				LocalAddr:    f.LocalAddr,
				LocalPort:    f.LocalPort,
				RemoteHost:   f.RemoteHost,
				RemotePort:   f.RemotePort,
				AutoStart:    f.AutoStart,
				Description:  f.Description,
				Bookmarks:    f.Bookmarks,
			})
		}
	}

	// Credentials referenced by any kept connection / folder.
	if opts.IncludeCredentials {
		if opts.Passphrase == "" {
			return nil, fmt.Errorf("credentials export requires a passphrase")
		}
		referenced := map[string]bool{}
		collectAuthRef := func(s store.InheritableSettings) {
			if s.AuthRef != nil && *s.AuthRef != "" {
				referenced[*s.AuthRef] = true
			}
		}
		for _, f := range arc.Folders {
			collectAuthRef(f.Settings)
			// Dynamic-folder configs reference credentials outside the
			// AuthRef mechanism (API tokens, jump/override creds) -
			// without this they were never exported and the imported
			// folder pointed at a credential id that didn't exist.
			if f.Dynamic != nil {
				for _, key := range dynCredConfigKeys {
					if id, _ := f.Dynamic.Config[key].(string); id != "" {
						referenced[id] = true
					}
				}
			}
		}
		for _, c := range arc.Connections {
			collectAuthRef(c.Overrides)
		}

		allCreds, err := db.ListCredentials()
		if err != nil {
			return nil, fmt.Errorf("list credentials: %w", err)
		}
		sort.Slice(allCreds, func(i, j int) bool { return allCreds[i].ID < allCreds[j].ID })

		// Derive export key from passphrase + fresh salt.
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("rand salt: %w", err)
		}
		key := argon2.IDKey([]byte(opts.Passphrase), salt, argonIterations, argonMemKiB, argonParallel, keyLen)
		aead, err := chacha20poly1305.NewX(key)
		if err != nil {
			return nil, fmt.Errorf("init aead: %w", err)
		}

		block := &EncryptedSecretBlock{
			KDF:        "argon2id",
			Salt:       base64.StdEncoding.EncodeToString(salt),
			MemoryKiB:  argonMemKiB,
			Iterations: argonIterations,
			Parallel:   argonParallel,
			CipherBy:   map[string]string{},
		}

		for _, c := range allCreds {
			if !referenced[c.ID] {
				continue
			}
			arc.Credentials = append(arc.Credentials, ArchiveCredential{
				ID:              c.ID,
				FolderID:        c.FolderID,
				Name:            c.Name,
				Kind:            c.Kind,
				StorageMode:     c.StorageMode,
				Hint:            c.Hint,
				Tags:            c.Tags,
				Config:          c.Config,
				PublicKey:       c.PublicKey,
				DefaultUsername: c.DefaultUsername,
				IconImageID:     c.IconImageID,
			})
			if c.VaultKey == nil || *c.VaultKey == "" {
				continue // no managed secret to wrap
			}
			plain, ok, err := fetchSecret(c.ID, *c.VaultKey)
			if err != nil {
				return nil, fmt.Errorf("fetch secret %s: %w", c.ID, err)
			}
			if !ok || len(plain) == 0 {
				continue
			}
			nonce := make([]byte, aead.NonceSize())
			if _, err := rand.Read(nonce); err != nil {
				return nil, fmt.Errorf("rand nonce: %w", err)
			}
			ct := aead.Seal(nil, nonce, plain, nil)
			block.CipherBy[c.ID] = base64.StdEncoding.EncodeToString(append(nonce, ct...))
		}

		// Per-connection password overrides ride in the same block
		// under the same key. They're exactly the secrets the user
		// opted into sharing with "include credentials".
		for connID, vaultKey := range connPassKeys {
			plain, ok, err := fetchSecret(connID, vaultKey)
			if err != nil {
				return nil, fmt.Errorf("fetch conn password %s: %w", connID, err)
			}
			if !ok || len(plain) == 0 {
				continue
			}
			nonce := make([]byte, aead.NonceSize())
			if _, err := rand.Read(nonce); err != nil {
				return nil, fmt.Errorf("rand nonce: %w", err)
			}
			ct := aead.Seal(nil, nonce, plain, nil)
			if block.ConnCipherBy == nil {
				block.ConnCipherBy = map[string]string{}
			}
			block.ConnCipherBy[connID] = base64.StdEncoding.EncodeToString(append(nonce, ct...))
		}
		arc.EncryptedSecrets = block

		// Credential folders: ancestry of every exported credential so
		// import can rebuild the tree. Walk parents to the root - a
		// child without its parent in the archive would import at root.
		credFolders, err := db.ListCredentialFolders()
		if err != nil {
			return nil, fmt.Errorf("list credential folders: %w", err)
		}
		cfByID := map[string]*store.CredentialFolder{}
		for i := range credFolders {
			cfByID[credFolders[i].ID] = &credFolders[i]
		}
		keepCF := map[string]bool{}
		for _, ac := range arc.Credentials {
			if ac.FolderID == nil {
				continue
			}
			for id := *ac.FolderID; id != "" && !keepCF[id]; {
				f, ok := cfByID[id]
				if !ok {
					break // dangling reference; credential will land at root
				}
				keepCF[id] = true
				if f.ParentID == nil {
					break
				}
				id = *f.ParentID
			}
		}
		for _, f := range credFolders { // ListCredentialFolders order = stable output
			if !keepCF[f.ID] {
				continue
			}
			arc.CredentialFolders = append(arc.CredentialFolders, ArchiveCredFolder{
				ID:       f.ID,
				ParentID: f.ParentID,
				Name:     f.Name,
			})
		}
	}

	applyStrips(arc, opts)

	// Icon images: collected AFTER strips so StripIcon also drops the
	// blobs, not just the references. Deduped here by id; the import
	// side dedupes content by md5 (PutImage), so shared logos never
	// multiply. A dangling icon_image_id (image row deleted) clears
	// the reference rather than failing the export.
	if !opts.StripIcon {
		seen := map[string]bool{}
		collect := func(ref **string) error {
			id := *ref
			if id == nil || *id == "" {
				return nil
			}
			if seen[*id] {
				return nil
			}
			mime, data, ok, err := db.GetImage(*id)
			if err != nil {
				return fmt.Errorf("get image %s: %w", *id, err)
			}
			if !ok || len(data) == 0 {
				*ref = nil
				return nil
			}
			seen[*id] = true
			arc.Images = append(arc.Images, ArchiveImage{
				ID:   *id,
				MIME: mime,
				B64:  base64.StdEncoding.EncodeToString(data),
			})
			return nil
		}
		for i := range arc.Folders {
			if err := collect(&arc.Folders[i].IconImageID); err != nil {
				return nil, err
			}
		}
		for i := range arc.Connections {
			if err := collect(&arc.Connections[i].IconImageID); err != nil {
				return nil, err
			}
		}
		for i := range arc.Credentials {
			if err := collect(&arc.Credentials[i].IconImageID); err != nil {
				return nil, err
			}
		}
	}

	return arc, nil
}

// applyStrips mutates arc according to the strip / convert flags
// from Options. Runs after everything is collected so the strip
// rules can't accidentally skip dependencies (referenced creds).
func applyStrips(arc *Archive, opts Options) {
	for i := range arc.Folders {
		f := &arc.Folders[i]
		if opts.StripColor {
			f.Settings.ColorTag = nil
		}
	}
	for i := range arc.Connections {
		c := &arc.Connections[i]
		if opts.StripNotes {
			c.Notes = ""
		}
		if opts.StripTags {
			c.Tags = nil
		}
		if opts.StripColor {
			c.Overrides.ColorTag = nil
		}
		if opts.ConvertAuthRefToInherit && !opts.IncludeCredentials {
			c.Overrides.AuthRef = nil
		}
	}
	// StripIcon clears the icon references; Build skips the image
	// blob collection entirely when the flag is set (it runs after
	// this), so neither refs nor data leave the machine.
	if opts.StripIcon {
		for i := range arc.Folders {
			arc.Folders[i].IconImageID = nil
		}
		for i := range arc.Connections {
			arc.Connections[i].IconImageID = nil
		}
		for i := range arc.Credentials {
			arc.Credentials[i].IconImageID = nil
		}
	}
	if opts.StripTags {
		for i := range arc.Credentials {
			arc.Credentials[i].Tags = nil
		}
	}
}

// Encode writes the archive in the requested format.
func Encode(arc *Archive, format Format) (string, error) {
	switch format {
	case FormatJSON:
		b, err := json.MarshalIndent(arc, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	case FormatTOML:
		b, err := toml.Marshal(arc)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("unknown format %q", format)
	}
}

// DecryptSecret reverses what Build does for one credential, given the
// import passphrase and the block.
func DecryptSecret(passphrase string, block *EncryptedSecretBlock, credID string) ([]byte, error) {
	cipherB64, ok := block.CipherBy[credID]
	if !ok {
		return nil, fmt.Errorf("no ciphertext for %s", credID)
	}
	return decryptBlob(passphrase, block, cipherB64)
}

// DecryptConnPassword unwraps a per-connection password override
// (ConnCipherBy, keyed by the archive's connection id).
func DecryptConnPassword(passphrase string, block *EncryptedSecretBlock, connID string) ([]byte, error) {
	cipherB64, ok := block.ConnCipherBy[connID]
	if !ok {
		return nil, fmt.Errorf("no ciphertext for connection %s", connID)
	}
	return decryptBlob(passphrase, block, cipherB64)
}

func decryptBlob(passphrase string, block *EncryptedSecretBlock, cipherB64 string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(block.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("decode cipher: %w", err)
	}
	key := argon2.IDKey([]byte(passphrase), salt, block.Iterations, block.MemoryKiB, block.Parallel, keyLen)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, fmt.Errorf("cipher too short")
	}
	nonce := raw[:aead.NonceSize()]
	ct := raw[aead.NonceSize():]
	return aead.Open(nil, nonce, ct, nil)
}
