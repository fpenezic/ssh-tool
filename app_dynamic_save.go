package main

// "Save as connection" on a dynamic inventory host: a fixed connection
// with what the host resolves to now (address, port, user, credential,
// route), so it can be used where an inventory host cannot: as a bastion
// a catalog publish can send (a dynamic host means nothing to others).
// The copy does not follow the inventory any more.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// DynamicSaveResult is what a save made.
type DynamicSaveResult struct {
	ConnectionID string `json:"connection_id"`
	// Repointed: rows whose jump host now goes through the new connection.
	Repointed int `json:"repointed"`
}

// DynamicEntrySaveAsConnection saves inventory host dynID ("dyn:<entry>")
// as a connection in folderID ("" = top level). repoint also points every
// jump host of the user's own rows that goes through the host at it.
func (a *App) DynamicEntrySaveAsConnection(dynID, folderID string, repoint bool) (*DynamicSaveResult, error) {
	entryID, ok := strings.CutPrefix(dynID, "dyn:")
	if !ok {
		return nil, fmt.Errorf("not an inventory host")
	}
	entry, err := a.db.GetDynamicEntry(entryID)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("the host is gone from the inventory - refresh the folder")
	}
	if folderID != "" {
		if f, err := a.db.GetFolder(folderID); err != nil || f == nil || f.CatalogID != nil {
			return nil, fmt.Errorf("pick one of your own folders")
		}
	}
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	synthetic := a.dynamicConnectionFor(entry, "")
	synthetic.ID = dynID
	eff := resolver.EffectiveOverrides(synthetic, folders)
	var fid *string
	if folderID != "" {
		fid = &folderID
	}
	c, err := a.db.CreateConnection(store.NewConnection{FolderID: fid, Name: entry.Name, Hostname: synthetic.Hostname, Overrides: eff})
	if err != nil {
		return nil, err
	}
	res := &DynamicSaveResult{ConnectionID: c.ID}
	if repoint {
		res.Repointed, err = a.repointJumpHosts(dynID, c.ID)
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// repointJumpHosts replaces jump-host references to from with to in the
// user's own folders and connections (catalog rows are the catalog's).
func (a *App) repointJumpHosts(from, to string) (int, error) {
	q, _ := json.Marshal(from)
	r, _ := json.Marshal(to)
	ts := time.Now().Unix()
	n := 0
	for _, t := range []struct{ table, col string }{{"connections", "overrides_json"}, {"folders", "settings_json"}} {
		res, err := a.db.Conn().Exec(`UPDATE `+t.table+` SET `+t.col+` = replace(`+t.col+`, ?, ?), updated_at = ?
			WHERE catalog_id IS NULL AND instr(`+t.col+`, ?) > 0`, `"connection_id":`+string(q), `"connection_id":`+string(r), ts, `"connection_id":`+string(q))
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, nil
}
