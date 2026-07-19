package exporter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"ssh-tool/internal/store"
)

// ConflictMode controls how the importer reacts to an existing row.
type ConflictMode string

const (
	// ConflictSkip leaves the existing row in place and reports the
	// archived one in the summary's Skipped slice.
	ConflictSkip ConflictMode = "skip"
	// ConflictRename appends " (imported)" to the new row's name and
	// inserts it alongside the existing one. Useful for migrating from
	// a sibling laptop without losing either side.
	ConflictRename ConflictMode = "rename"
	// ConflictOverwrite replaces the existing row by id. Most invasive;
	// keep it opt-in.
	ConflictOverwrite ConflictMode = "overwrite"
)

// ImportOptions tunes apply behaviour.
type ImportOptions struct {
	Conflict ConflictMode
	// DryRun = true reports what would happen without touching the DB.
	DryRun bool
	// SecretPassphrase is required to unwrap credential secrets if the
	// archive includes them.
	SecretPassphrase string
	// TargetFolderID, when non-empty, places every root folder and
	// every root-level connection from the archive underneath this
	// existing folder. Nested folders / connections inside the
	// archive keep their relative structure. Empty = import at root
	// (legacy behaviour).
	TargetFolderID string
}

// ImportSummary lists what the apply did (or would do, in dry-run).
type ImportSummary struct {
	FoldersCreated     []string `json:"folders_created"`
	FoldersUpdated     []string `json:"folders_updated"`
	FoldersSkipped     []string `json:"folders_skipped"`
	ConnsCreated       []string `json:"conns_created"`
	ConnsUpdated       []string `json:"conns_updated"`
	ConnsSkipped       []string `json:"conns_skipped"`
	CredsCreated       []string `json:"creds_created"`
	CredsSkipped       []string `json:"creds_skipped"`
	CredFoldersCreated []string `json:"cred_folders_created,omitempty"`
	CredFoldersSkipped []string `json:"cred_folders_skipped,omitempty"`
	SecretsImported    int      `json:"secrets_imported"`
	// ImagesImported counts custom icon blobs landed (or matched to
	// an existing content-addressed row).
	ImagesImported int `json:"images_imported"`
	// ConnPasswordsImported counts restored per-connection password
	// overrides (password stored on the connection row itself, no
	// credential entry involved).
	ConnPasswordsImported int      `json:"conn_passwords_imported"`
	ForwardsCreated       int      `json:"forwards_created"`
	Warnings              []string `json:"warnings"`
}

// Decode parses an archive from raw bytes. Format auto-detect: any input
// whose first non-comment, non-whitespace character is `{` is JSON,
// otherwise TOML. The catalog's bundle endpoint can prepend header
// comments (`# catalog-bundle generated at …`) so we skip past those
// before deciding.
func Decode(text string) (*Archive, error) {
	t := skipLeadingComments(text)
	var arc Archive
	if strings.HasPrefix(t, "{") {
		// JSON parser doesn't tolerate the prepended # lines, so feed
		// it the comment-stripped tail. TOML does tolerate them so
		// the original `text` is fine in the else branch.
		if err := json.Unmarshal([]byte(t), &arc); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	} else {
		if err := toml.Unmarshal([]byte(text), &arc); err != nil {
			return nil, fmt.Errorf("parse toml: %w", err)
		}
	}
	if arc.SchemaVersion == 0 {
		return nil, fmt.Errorf("missing schema_version")
	}
	if arc.SchemaVersion > currentSchemaVersion {
		return nil, fmt.Errorf("archive schema %d is newer than supported %d",
			arc.SchemaVersion, currentSchemaVersion)
	}
	return &arc, nil
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

// skipLeadingComments walks past any number of leading lines that
// start with `#` (after whitespace), plus the whitespace itself.
// Used so the JSON branch of Decode tolerates the catalog's
// `# catalog-bundle …` headers.
func skipLeadingComments(text string) string {
	for {
		text = strings.TrimLeftFunc(text, isSpace)
		if !strings.HasPrefix(text, "#") {
			return text
		}
		// Drop the comment line.
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			return ""
		}
		text = text[nl+1:]
	}
}

// SecretWriter stores a freshly imported secret payload under the given
// vault key, returning the key actually used (caller may rewrite it).
type SecretWriter func(credID string, plain []byte) (vaultKey string, err error)

// Apply walks the archive and writes its contents through db. With
// DryRun set, no writes happen - the summary still reflects what the
// run would do.
func Apply(
	db *store.DB,
	arc *Archive,
	opts ImportOptions,
	storeSecret SecretWriter,
) (*ImportSummary, error) {
	sum := &ImportSummary{}

	// Snapshot existing rows so we can detect conflicts by id without
	// round-tripping the DB inside hot loops.
	existingFolders, _ := db.ListFolders()
	existingConns, _ := db.ListConnections(nil)
	existingCreds, _ := db.ListCredentials()
	folderByID := map[string]*store.Folder{}
	connByID := map[string]*store.Connection{}
	credByID := map[string]*store.CredentialRef{}
	for i := range existingFolders {
		folderByID[existingFolders[i].ID] = &existingFolders[i]
	}
	for i := range existingConns {
		connByID[existingConns[i].ID] = &existingConns[i]
	}
	for i := range existingCreds {
		credByID[existingCreds[i].ID] = &existingCreds[i]
	}

	// CreateFolder / CreateConnection / CreateCredential each mint a
	// fresh uuid, so the import has to maintain a OLD -> NEW id map
	// for every entity. Connections + jump chains + folder settings
	// reference auth_ref by id; folders themselves reference parent
	// folders by id. Without remapping, every child gets the archive's
	// original id which doesn't exist in this DB (FK constraint fails).
	folderIDMap := map[string]string{}
	credIDMap := map[string]string{}

	// resolveAuthRef maps an archive credential id to its final local id.
	// Resolvable when the credential is in this archive (credIDMap, filled
	// by the credentials pass below) or already exists locally by that id
	// (credByID - e.g. re-importing onto the same machine). Anything else
	// is unknown and gets dropped to "inherit" by the remap helpers.
	resolveAuthRef := func(id string) (string, bool) {
		if newID, ok := credIDMap[id]; ok {
			return newID, true
		}
		if _, ok := credByID[id]; ok {
			return id, true
		}
		return "", false
	}
	// Collect dropped (dangling) auth_refs into one deduped warning so the
	// user sees that some connections fell back to inheriting a credential.
	droppedAuthRefs := map[string]bool{}
	noteDropped := func(ids []string) {
		for _, id := range ids {
			droppedAuthRefs[id] = true
		}
	}

	// ----- Icon images -----
	// Inserted first so folder / connection / credential passes can
	// link remapped icon ids as they create rows. PutImage is
	// content-addressed (md5): re-imports and logos shared across
	// archives resolve to the existing row instead of duplicating.
	imageIDMap := map[string]string{}
	for _, img := range arc.Images {
		data, err := base64.StdEncoding.DecodeString(img.B64)
		if err != nil || len(data) == 0 {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("image %s: bad base64, icon dropped", img.ID))
			continue
		}
		sum.ImagesImported++
		if opts.DryRun {
			imageIDMap[img.ID] = img.ID // synthetic for counting
			continue
		}
		newID, err := db.PutImage(data, img.MIME)
		if err != nil {
			sum.ImagesImported--
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("image %s: store: %v", img.ID, err))
			continue
		}
		imageIDMap[img.ID] = newID
	}
	// setIcon links a remapped icon onto a freshly written row;
	// shared by the three entity passes below.
	setIcon := func(oldIconID *string, link func(imageID string) error, what, name string) {
		if opts.DryRun || oldIconID == nil {
			return
		}
		newImgID, ok := imageIDMap[*oldIconID]
		if !ok {
			return // image missing from archive (or failed) - default icon
		}
		if err := link(newImgID); err != nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("%s %s: icon link: %v", what, name, err))
		}
	}

	// setNamedIcon restores a built-in (lucide) icon + colour. Mutually
	// exclusive with an uploaded image, so it's only applied when the
	// archive carried a name and no image link succeeded above; the store
	// setters clear icon_image_id anyway. No-op on dry-run / no name.
	setNamedIcon := func(iconName, iconColor *string, link func(name, color string) error, what, name string) {
		if opts.DryRun || iconName == nil || *iconName == "" {
			return
		}
		color := ""
		if iconColor != nil {
			color = *iconColor
		}
		if err := link(*iconName, color); err != nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("%s %s: named icon: %v", what, name, err))
		}
	}

	// ----- Credential folders first -----
	// The credential tree has its own folder hierarchy. Rebuild it
	// before the credentials so their FolderID can be remapped instead
	// of dropped (pre-section archives have none and import flat).
	// Reuse an existing folder when the id matches (re-import into the
	// same DB) or when name + remapped parent match (import on another
	// machine, repeated imports) - otherwise create.
	credFolderIDMap := map[string]string{}
	{
		existingCF, _ := db.ListCredentialFolders()
		cfByID := map[string]bool{}
		type cfKey struct{ parent, name string }
		cfByParentName := map[cfKey]string{}
		for _, f := range existingCF {
			cfByID[f.ID] = true
			p := ""
			if f.ParentID != nil {
				p = *f.ParentID
			}
			cfByParentName[cfKey{p, f.Name}] = f.ID
		}
		for _, f := range topoSortCredFolders(arc.CredentialFolders) {
			if cfByID[f.ID] {
				credFolderIDMap[f.ID] = f.ID
				sum.CredFoldersSkipped = append(sum.CredFoldersSkipped, f.Name)
				continue
			}
			newParent := ""
			var newParentPtr *string
			if f.ParentID != nil {
				if mapped, ok := credFolderIDMap[*f.ParentID]; ok {
					newParent = mapped
					newParentPtr = &mapped
				} else {
					sum.Warnings = append(sum.Warnings,
						fmt.Sprintf("credential folder %s: parent missing from archive, placing at root", f.Name))
				}
			}
			if id, ok := cfByParentName[cfKey{newParent, f.Name}]; ok {
				credFolderIDMap[f.ID] = id
				sum.CredFoldersSkipped = append(sum.CredFoldersSkipped, f.Name)
				continue
			}
			sum.CredFoldersCreated = append(sum.CredFoldersCreated, f.Name)
			if opts.DryRun {
				credFolderIDMap[f.ID] = f.ID // synthetic so children remap cleanly
				continue
			}
			created, err := db.CreateCredentialFolder(f.Name, newParentPtr)
			if err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("credential folder %s create: %v", f.Name, err))
				continue
			}
			credFolderIDMap[f.ID] = created.ID
			cfByParentName[cfKey{newParent, f.Name}] = created.ID
			setNamedIcon(f.IconName, f.IconColor, func(n, c string) error {
				return db.SetCredentialFolderNamedIcon(created.ID, n, c)
			}, "credential folder", f.Name)
		}
	}

	// ----- Credentials -----
	// Credentials need to land before connections so the auth_ref
	// remap has values when we walk connections. (The archive carries
	// only credentials that connections / folders actually reference,
	// so we'll have what we need.)
	for _, c := range arc.Credentials {
		if _, exists := credByID[c.ID]; exists {
			sum.CredsSkipped = append(sum.CredsSkipped, c.Name)
			// Existing id stays valid - remap to self so later
			// auth_ref rewrites point at the existing credential.
			credIDMap[c.ID] = c.ID
			continue
		}
		sum.CredsCreated = append(sum.CredsCreated, c.Name)
		if opts.DryRun {
			// Pretend the id stays so dry-run reporting (auth_ref
			// resolution) doesn't claim everything is unresolved.
			credIDMap[c.ID] = c.ID
			continue
		}
		// Credentials reference credential_folders, NOT connection
		// folders. Remap through the credential-folder pass above;
		// unmapped (pre-section archive, missing parent) lands at
		// root rather than risking an FK fail.
		var credFolderID *string
		if c.FolderID != nil {
			if mapped, ok := credFolderIDMap[*c.FolderID]; ok {
				credFolderID = &mapped
			}
		}
		newCred, err := db.CreateCredential(store.NewCredential{
			Name:            c.Name,
			Kind:            c.Kind,
			StorageMode:     c.StorageMode,
			FolderID:        credFolderID,
			Hint:            c.Hint,
			Tags:            c.Tags,
			Config:          c.Config,
			PublicKey:       c.PublicKey,
			DefaultUsername: c.DefaultUsername,
		})
		if err != nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("cred %s create: %v", c.Name, err))
			continue
		}
		credIDMap[c.ID] = newCred.ID
		setIcon(c.IconImageID, func(imgID string) error {
			return db.SetCredentialIcon(newCred.ID, imgID)
		}, "cred", c.Name)
		setNamedIcon(c.IconName, c.IconColor, func(n, col string) error {
			return db.SetCredentialNamedIcon(newCred.ID, n, col)
		}, "cred", c.Name)
		// Unwrap + restore secret if archive carries one.
		if arc.EncryptedSecrets != nil {
			if _, present := arc.EncryptedSecrets.CipherBy[c.ID]; !present {
				continue
			}
			if opts.SecretPassphrase == "" {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("cred %s: encrypted secret skipped (no passphrase)", c.Name))
				continue
			}
			plain, err := DecryptSecret(opts.SecretPassphrase, arc.EncryptedSecrets, c.ID)
			if err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("cred %s: decrypt: %v", c.Name, err))
				continue
			}
			vaultKey, err := storeSecret(newCred.ID, plain)
			if err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("cred %s: vault write: %v", c.Name, err))
				continue
			}
			if err := db.SetCredentialVaultKey(newCred.ID, vaultKey); err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("cred %s: vault_key link: %v", c.Name, err))
				continue
			}
			sum.SecretsImported++
		}
	}

	// ----- Folders, topologically sorted -----
	// Sort so a folder's parent is processed first; a child's ParentID
	// can then be remapped to the parent's freshly-minted id.
	sortedFolders, err := topoSortFolders(arc.Folders)
	if err != nil {
		// Cycle detected - give up rather than partial-write. Caller
		// gets a clear error rather than confusing FK failures.
		return nil, err
	}
	for _, f := range sortedFolders {
		if existing, exists := folderByID[f.ID]; exists {
			switch opts.Conflict {
			case ConflictSkip, "":
				sum.FoldersSkipped = append(sum.FoldersSkipped, f.Name)
				folderIDMap[f.ID] = existing.ID
				continue
			case ConflictOverwrite:
				sum.FoldersUpdated = append(sum.FoldersUpdated, f.Name)
				folderIDMap[f.ID] = existing.ID
				if !opts.DryRun {
					settings, dropped := remapAuthRefInSettings(f.Settings, resolveAuthRef)
					noteDropped(dropped)
					if _, err := db.UpdateFolder(store.UpdateFolder{
						ID:       existing.ID,
						Name:     &f.Name,
						Settings: &settings,
					}); err != nil {
						sum.Warnings = append(sum.Warnings,
							fmt.Sprintf("folder %s overwrite: %v", f.Name, err))
					}
					setIcon(f.IconImageID, func(imgID string) error {
						return db.SetFolderIcon(existing.ID, imgID)
					}, "folder", f.Name)
					setNamedIcon(f.IconName, f.IconColor, func(n, col string) error {
						return db.SetFolderNamedIcon(existing.ID, n, col)
					}, "folder", f.Name)
					if f.Dynamic != nil {
						df := store.DynamicFolder{
							FolderID:       existing.ID,
							Provider:       f.Dynamic.Provider,
							Config:         remapDynCredRefs(f.Dynamic.Config, credIDMap, sum, f.Name),
							RefreshSeconds: f.Dynamic.RefreshSeconds,
						}
						var derr error
						if cur, _ := db.GetDynamicFolder(existing.ID); cur != nil {
							derr = db.UpdateDynamicFolder(df)
						} else {
							derr = db.CreateDynamicFolder(df)
						}
						if derr != nil {
							sum.Warnings = append(sum.Warnings,
								fmt.Sprintf("folder %s: dynamic config: %v", f.Name, derr))
						}
					}
				}
				continue
			case ConflictRename:
				f.Name = f.Name + " (imported)"
			}
		}
		sum.FoldersCreated = append(sum.FoldersCreated, f.Name)
		// Remap ParentID through the map built up so far. A nil
		// parent (root folder) stays nil; an old parent id that
		// hasn't been mapped (parent is missing from the archive)
		// also becomes nil with a warning so the import doesn't FK-fail.
		newParentID := remapFolderID(f.ParentID, folderIDMap)
		if f.ParentID != nil && newParentID == nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("folder %s: parent %s missing from archive, placing at root",
					f.Name, *f.ParentID))
		}
		if newParentID == nil && opts.TargetFolderID != "" {
			t := opts.TargetFolderID
			newParentID = &t
		}
		settings, droppedF := remapAuthRefInSettings(f.Settings, resolveAuthRef)
		noteDropped(droppedF)
		if opts.DryRun {
			folderIDMap[f.ID] = f.ID // synthetic so children remap cleanly
			continue
		}
		created, err := db.CreateFolder(store.NewFolder{
			ParentID: newParentID,
			Name:     f.Name,
			Settings: settings,
		})
		if err != nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("folder %s create: %v", f.Name, err))
			continue
		}
		folderIDMap[f.ID] = created.ID
		setIcon(f.IconImageID, func(imgID string) error {
			return db.SetFolderIcon(created.ID, imgID)
		}, "folder", f.Name)
		setNamedIcon(f.IconName, f.IconColor, func(n, col string) error {
			return db.SetFolderNamedIcon(created.ID, n, col)
		}, "folder", f.Name)
		// Dynamic-inventory side table. Cached entries are not part of
		// the archive - the first refresh on this side repopulates them.
		if f.Dynamic != nil {
			cfg := remapDynCredRefs(f.Dynamic.Config, credIDMap, sum, f.Name)
			if err := db.CreateDynamicFolder(store.DynamicFolder{
				FolderID:       created.ID,
				Provider:       f.Dynamic.Provider,
				Config:         cfg,
				RefreshSeconds: f.Dynamic.RefreshSeconds,
			}); err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("folder %s: dynamic config: %v", f.Name, err))
			}
		}
	}

	// ----- Connections -----
	// connIDMap: archive id → DB id, used to attach forwards in the
	// pass below. Existing connections (skip / overwrite paths) map
	// to their already-present row; newly created ones to the freshly
	// minted id.
	connIDMap := map[string]string{}
	// connPassEligible: archive ids whose row we created or overwrote.
	// Skipped connections keep their existing row untouched, so an
	// archived password override must NOT be written onto them.
	connPassEligible := map[string]bool{}
	for _, c := range arc.Connections {
		if existing, exists := connByID[c.ID]; exists {
			switch opts.Conflict {
			case ConflictSkip, "":
				sum.ConnsSkipped = append(sum.ConnsSkipped, c.Name)
				connIDMap[c.ID] = existing.ID
				continue
			case ConflictOverwrite:
				connPassEligible[c.ID] = true
				sum.ConnsUpdated = append(sum.ConnsUpdated, c.Name)
				if !opts.DryRun {
					newFolderID := remapFolderID(c.FolderID, folderIDMap)
					if newFolderID == nil && opts.TargetFolderID != "" {
						t := opts.TargetFolderID
						newFolderID = &t
					}
					overrides, droppedC := remapAuthRefInSettings(c.Overrides, resolveAuthRef)
					noteDropped(droppedC)
					protocol := c.Protocol
					if _, err := db.UpdateConnection(store.UpdateConnection{
						ID:                  existing.ID,
						FolderID:            newFolderID,
						Name:                &c.Name,
						Hostname:            &c.Hostname,
						Protocol:            &protocol,
						LocalShellKind:      c.LocalShellKind,
						ClearLocalShellKind: c.LocalShellKind == nil,
						Overrides:           &overrides,
						Tags:                &c.Tags,
						Notes:               &c.Notes,
						Favorite:            &c.Favorite,
					}); err != nil {
						sum.Warnings = append(sum.Warnings,
							fmt.Sprintf("conn %s overwrite: %v", c.Name, err))
					}
					setIcon(c.IconImageID, func(imgID string) error {
						return db.SetConnectionIcon(existing.ID, imgID)
					}, "conn", c.Name)
					setNamedIcon(c.IconName, c.IconColor, func(n, col string) error {
						return db.SetConnectionNamedIcon(existing.ID, n, col)
					}, "conn", c.Name)
				}
				connIDMap[c.ID] = existing.ID
				continue
			case ConflictRename:
				c.Name = c.Name + " (imported)"
			}
		}
		sum.ConnsCreated = append(sum.ConnsCreated, c.Name)
		connPassEligible[c.ID] = true
		if !opts.DryRun {
			newFolderID := remapFolderID(c.FolderID, folderIDMap)
			if c.FolderID != nil && newFolderID == nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("conn %s: folder %s missing from archive, placing at root",
						c.Name, *c.FolderID))
			}
			if newFolderID == nil && opts.TargetFolderID != "" {
				t := opts.TargetFolderID
				newFolderID = &t
			}
			overrides, droppedC := remapAuthRefInSettings(c.Overrides, resolveAuthRef)
			noteDropped(droppedC)
			created, err := db.CreateConnection(store.NewConnection{
				FolderID:       newFolderID,
				Name:           c.Name,
				Hostname:       c.Hostname,
				Protocol:       c.Protocol,
				LocalShellKind: c.LocalShellKind,
				Overrides:      overrides,
				Tags:           c.Tags,
				Notes:          c.Notes,
			})
			if err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("conn %s create: %v", c.Name, err))
				continue
			}
			connIDMap[c.ID] = created.ID
			setIcon(c.IconImageID, func(imgID string) error {
				return db.SetConnectionIcon(created.ID, imgID)
			}, "conn", c.Name)
			setNamedIcon(c.IconName, c.IconColor, func(n, col string) error {
				return db.SetConnectionNamedIcon(created.ID, n, col)
			}, "conn", c.Name)
		}
	}

	// ----- Per-connection password overrides -----
	// Restored only onto rows this import created or overwrote (see
	// connPassEligible above). Stored via the same SecretWriter as
	// credential secrets, then linked on the connection row.
	if arc.EncryptedSecrets != nil && len(arc.EncryptedSecrets.ConnCipherBy) > 0 {
		oldIDs := make([]string, 0, len(arc.EncryptedSecrets.ConnCipherBy))
		for id := range arc.EncryptedSecrets.ConnCipherBy {
			oldIDs = append(oldIDs, id)
		}
		sort.Strings(oldIDs)
		for _, oldID := range oldIDs {
			if !connPassEligible[oldID] {
				continue
			}
			if opts.SecretPassphrase == "" {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("conn %s: encrypted password skipped (no passphrase)", oldID))
				continue
			}
			sum.ConnPasswordsImported++
			if opts.DryRun {
				continue
			}
			newID, ok := connIDMap[oldID]
			if !ok {
				sum.ConnPasswordsImported--
				continue // creation failed above; already warned
			}
			plain, err := DecryptConnPassword(opts.SecretPassphrase, arc.EncryptedSecrets, oldID)
			if err != nil {
				sum.ConnPasswordsImported--
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("conn %s: password decrypt: %v", oldID, err))
				continue
			}
			vaultKey, err := storeSecret(newID, plain)
			if err != nil {
				sum.ConnPasswordsImported--
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("conn %s: password vault write: %v", oldID, err))
				continue
			}
			if err := db.SetConnectionPasswordKey(newID, vaultKey); err != nil {
				sum.ConnPasswordsImported--
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("conn %s: password link: %v", oldID, err))
			}
		}
	}

	// ----- Forwards (incl. SOCKS bookmarks) -----
	for _, f := range arc.Forwards {
		newConnID, ok := connIDMap[f.ConnectionID]
		if !ok {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("forward %s: parent connection %s not imported, skipping",
					f.ID, f.ConnectionID))
			continue
		}
		if opts.DryRun {
			sum.ForwardsCreated++
			continue
		}
		created, err := db.CreatePortForward(store.NewPortForward{
			ConnectionID: newConnID,
			Kind:         f.Kind,
			LocalAddr:    f.LocalAddr,
			LocalPort:    f.LocalPort,
			RemoteHost:   f.RemoteHost,
			RemotePort:   f.RemotePort,
			AutoStart:    f.AutoStart,
			Description:  f.Description,
		})
		if err != nil {
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("forward on %s: %v", f.ConnectionID, err))
			continue
		}
		if len(f.Bookmarks) > 0 {
			if err := db.SetPortForwardBookmarks(created.ID, f.Bookmarks); err != nil {
				sum.Warnings = append(sum.Warnings,
					fmt.Sprintf("forward bookmarks on %s: %v", f.ConnectionID, err))
			}
		}
		sum.ForwardsCreated++
	}

	// Surface dropped dangling credential references as one warning. These
	// are auth_refs that pointed at a credential not in the archive (and
	// not present locally) - typically a credential-less share; the
	// affected connections/folders now inherit their folder's credential
	// instead of carrying a broken id.
	if n := len(droppedAuthRefs); n > 0 {
		sum.Warnings = append(sum.Warnings,
			fmt.Sprintf("%d credential reference(s) not in the archive were dropped; affected connections now inherit their folder's credential", n))
	}

	return sum, nil
}

// remapFolderID returns the new id for an old folder id via the map,
// or nil if the input is nil or the old id has no mapping (parent
// missing - caller decides what to do).
func remapFolderID(oldID *string, m map[string]string) *string {
	if oldID == nil {
		return nil
	}
	if newID, ok := m[*oldID]; ok {
		s := newID
		return &s
	}
	return nil
}

// remapAuthRefInSettings walks an InheritableSettings and rewrites
// AuthRef (and AuthRefs inside any JumpHost chain) to the new
// credential id. Returns a copy - never mutates the original.
//
// resolve reports the final local id for an archive credential id and
// whether it is known at all. An AuthRef that resolves is rewritten; one
// that doesn't (credential wasn't in the archive and doesn't exist
// locally - the common case when the archive was shared without
// credentials, or a credential subtree was stripped) is DROPPED to nil so
// the connection cleanly falls back to inheriting its folder's
// credential, instead of pointing at a dangling uuid the user then has to
// hunt down and replace by hand. Dropped ids are appended to dropped.
func remapAuthRefInSettings(s store.InheritableSettings, resolve func(id string) (string, bool)) (store.InheritableSettings, []string) {
	var dropped []string
	out := s
	if out.AuthRef != nil {
		if newID, ok := resolve(*out.AuthRef); ok {
			out.AuthRef = &newID
		} else {
			dropped = append(dropped, *out.AuthRef)
			out.AuthRef = nil
		}
	}
	if out.JumpHost != nil && out.JumpHost.Chain != nil {
		newChain, d := remapJumpChain(*out.JumpHost.Chain, resolve)
		dropped = append(dropped, d...)
		out.JumpHost = &store.JumpHostOverride{Kind: out.JumpHost.Kind, Chain: &newChain}
	}
	return out, dropped
}

func remapJumpChain(spec store.JumpHostSpec, resolve func(id string) (string, bool)) (store.JumpHostSpec, []string) {
	var dropped []string
	out := spec
	if out.AuthRef != nil {
		if newID, ok := resolve(*out.AuthRef); ok {
			out.AuthRef = &newID
		} else {
			dropped = append(dropped, *out.AuthRef)
			out.AuthRef = nil
		}
	}
	if out.Via != nil {
		via, d := remapJumpChain(*out.Via, resolve)
		dropped = append(dropped, d...)
		out.Via = &via
	}
	return out, dropped
}

// topoSortFolders orders the slice so every entry's parent appears
// before it. Roots (nil ParentID) lead. Returns an error if a cycle
// is detected - the archive is malformed and we'd rather fail loud
// than write half a tree.
func topoSortFolders(folders []ArchiveFolder) ([]ArchiveFolder, error) {
	// Build adjacency: parent -> children. Roots get an empty-string
	// key so the BFS picks them up first.
	byParent := map[string][]ArchiveFolder{}
	for _, f := range folders {
		key := ""
		if f.ParentID != nil {
			key = *f.ParentID
		}
		byParent[key] = append(byParent[key], f)
	}

	visited := map[string]bool{}
	out := make([]ArchiveFolder, 0, len(folders))

	// Roots first.
	var visit func(parentKey string)
	visit = func(parentKey string) {
		for _, f := range byParent[parentKey] {
			if visited[f.ID] {
				continue
			}
			visited[f.ID] = true
			out = append(out, f)
			visit(f.ID)
		}
	}
	visit("")

	// Anything left has a parent not in the archive (orphan). Append
	// them at the end so they at least get attempted (will land at
	// root via the remap-to-nil fallback). Cycles never resolve and
	// stay unvisited.
	for _, f := range folders {
		if visited[f.ID] {
			continue
		}
		// Self-reference or cycle detection: if the folder's parent
		// chain leads back to itself in the archive, refuse the
		// whole import.
		if hasCycle(f.ID, folders) {
			return nil, fmt.Errorf("folder cycle detected involving %s", f.ID)
		}
		visited[f.ID] = true
		out = append(out, f)
	}
	return out, nil
}

// remapDynCredRefs returns a copy of a dynamic folder's config with
// every credential-id key (dynCredConfigKeys) rewritten through the
// import's OLD -> NEW credential map. A reference to a credential the
// archive doesn't carry is dropped with a warning - the folder imports
// working except for that one setting, instead of pointing at an id
// that doesn't exist on this side.
func remapDynCredRefs(cfg map[string]any, credIDMap map[string]string, sum *ImportSummary, folderName string) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, key := range dynCredConfigKeys {
		id, _ := out[key].(string)
		if id == "" {
			continue
		}
		if mapped, ok := credIDMap[id]; ok {
			out[key] = mapped
		} else {
			delete(out, key)
			sum.Warnings = append(sum.Warnings,
				fmt.Sprintf("folder %s: %s references a credential not in the archive - cleared", folderName, key))
		}
	}
	return out
}

// topoSortCredFolders orders credential folders parent-first, same
// approach as topoSortFolders. Orphans (parent absent from the
// archive) and cycle members append at the end - the import loop's
// missing-parent fallback places them at root with a warning, so a
// damaged archive degrades to a flat import instead of failing.
func topoSortCredFolders(folders []ArchiveCredFolder) []ArchiveCredFolder {
	byParent := map[string][]ArchiveCredFolder{}
	for _, f := range folders {
		key := ""
		if f.ParentID != nil {
			key = *f.ParentID
		}
		byParent[key] = append(byParent[key], f)
	}
	visited := map[string]bool{}
	out := make([]ArchiveCredFolder, 0, len(folders))
	var visit func(parentKey string)
	visit = func(parentKey string) {
		for _, f := range byParent[parentKey] {
			if visited[f.ID] {
				continue
			}
			visited[f.ID] = true
			out = append(out, f)
			visit(f.ID)
		}
	}
	visit("")
	for _, f := range folders {
		if !visited[f.ID] {
			out = append(out, f)
		}
	}
	return out
}

func hasCycle(startID string, folders []ArchiveFolder) bool {
	parent := map[string]*string{}
	for _, f := range folders {
		parent[f.ID] = f.ParentID
	}
	seen := map[string]bool{startID: true}
	cur := parent[startID]
	for cur != nil {
		if seen[*cur] {
			return true
		}
		seen[*cur] = true
		cur = parent[*cur]
	}
	return false
}
