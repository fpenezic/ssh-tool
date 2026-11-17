package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Credentials the catalog client creates for external references (a slot
// that names a Bitwarden / Vaultwarden item): ordinary credential_refs rows
// so every credential consumer works unchanged, with ids derived from the
// catalog and slot. Two machines of one user therefore create the same row,
// and profile sync never duplicates it. They live in one credential folder
// per catalog. Both carry the catalogCredPrefix, which is how shadowing,
// the legacy mirror and the fingerprint recognise them.
const catalogCredPrefix = "catcred-"

// catalogCredWhere matches catalog-made credentials and their folders.
const catalogCredWhere = `id LIKE 'catcred-%'`

// CatalogCredentialFolderID is the credential folder of one catalog.
func CatalogCredentialFolderID(catalogID string) string { return catalogCredPrefix + catalogID }

// CatalogCredentialID is the credential made for one external slot.
func CatalogCredentialID(catalogID, slotUUID string) string {
	return catalogCredPrefix + catalogID + "-" + slotUUID
}

// IsCatalogCredential reports whether a credential (or credential folder)
// id is one the catalog client made.
func IsCatalogCredential(id string) bool { return strings.HasPrefix(id, catalogCredPrefix) }

// EnsureCatalogCredentialFolder creates or renames the catalog's
// credential folder.
func (d *DB) EnsureCatalogCredentialFolder(catalogID, name string) error {
	ts := now()
	_, err := d.conn.Exec(`INSERT INTO credential_folders (id, parent_id, name, sort_order, created_at, updated_at)
		VALUES (?, NULL, ?, 0, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at
		WHERE credential_folders.name != excluded.name`,
		CatalogCredentialFolderID(catalogID), name, ts, ts)
	return err
}

// UpsertCatalogCredential creates or updates a catalog-made credential.
// Its name is name, or name with the slot's short id when a credential of
// the user's own already has that name (names are unique).
func (d *DB) UpsertCatalogCredential(catalogID, slotUUID, name string, kind CredentialKind, config map[string]any) error {
	id := CatalogCredentialID(catalogID, slotUUID)
	cfg, err := json.Marshal(config)
	if err != nil {
		return err
	}
	var taken string
	err = d.conn.QueryRow(`SELECT id FROM credential_refs WHERE name = ? AND id != ?`, name, id).Scan(&taken)
	if err == nil {
		short := slotUUID
		if len(short) > 8 {
			short = short[:8]
		}
		// The suffixed name can be taken too (names are unique): widen it.
		base := name
		name = base + " (" + short + ")"
		for i := 2; i < 100; i++ {
			var x string
			e := d.conn.QueryRow(`SELECT id FROM credential_refs WHERE name = ? AND id != ?`, name, id).Scan(&x)
			if errors.Is(e, sql.ErrNoRows) {
				break
			}
			if e != nil {
				return e
			}
			name = fmt.Sprintf("%s (%s %d)", base, short, i)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ts := now()
	_, err = d.conn.Exec(`INSERT INTO credential_refs (id, folder_id, name, kind, storage_mode, hint, tags_json, config_json,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', '[]', ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET folder_id = excluded.folder_id, name = excluded.name, kind = excluded.kind,
			storage_mode = excluded.storage_mode, config_json = excluded.config_json, updated_at = excluded.updated_at
		WHERE credential_refs.name != excluded.name OR credential_refs.kind != excluded.kind
			OR credential_refs.config_json != excluded.config_json OR IFNULL(credential_refs.folder_id, '') != excluded.folder_id`,
		id, CatalogCredentialFolderID(catalogID), name, string(kind), string(StorageExternal), string(cfg), ts, ts)
	return err
}

// DeleteStaleCatalogCredentials removes a catalog's made credentials whose
// slot is no longer an external reference here (keep lists the live ids),
// and the folder when nothing is left in it.
func (d *DB) DeleteStaleCatalogCredentials(catalogID string, keep map[string]bool) error {
	ids, err := idSet(d.conn, `SELECT id FROM credential_refs WHERE id LIKE 'catcred-`+escapeLike(catalogID)+`-%' ESCAPE '\'`)
	if err != nil {
		return err
	}
	for id := range ids {
		if keep[id] {
			continue
		}
		if _, err := d.conn.Exec(`DELETE FROM credential_refs WHERE id = ?`, id); err != nil {
			return err
		}
	}
	if len(keep) == 0 {
		_, err = d.conn.Exec(`DELETE FROM credential_folders WHERE id = ?
			AND NOT EXISTS (SELECT 1 FROM credential_refs WHERE folder_id = ?)`,
			CatalogCredentialFolderID(catalogID), CatalogCredentialFolderID(catalogID))
	}
	return err
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`, `'`, `''`)
	return r.Replace(s)
}
