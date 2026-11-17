package main

// "Remove catalog" keeping its tree as a local folder, and "Copy to my
// connections" (store.KeepCatalogLocal / store.CopyCatalogRows). Neither
// talks to the catalog server; MCP has neither.

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

// CatalogRemoveKeepLocal detaches a catalog like CatalogRemove, but its
// folders, connections and forwards stay as the user's own local folder,
// working as before; slots this user had not bound become empty fields,
// listed in the result and noted on each connection.
func (a *App) CatalogRemoveKeepLocal(id string) (*store.LocaliseResult, error) {
	cat, err := a.db.GetCatalog(id)
	if err != nil {
		return nil, err
	}
	_ = a.CatalogSignOut(id)
	rt := a.catRT()
	rt.apply.Lock()
	// Materialise once more so the rows hold the current translation and
	// the user's own layer.
	ext, _ := catalog.PrepareExternal(a.db, cat, nil)
	if err := catalog.Materialise(a.db, id, ext); err != nil {
		log.Printf("catalog %s: materialise before keep: %v", cat.Name, err)
	}
	res, err := a.db.KeepCatalogLocal(id)
	rt.apply.Unlock()
	if err != nil {
		return nil, err
	}
	a.forgetCatalog(id)
	a.recordAudit("catalog.remove", id, map[string]string{"kept_local": "true",
		"connections": strconv.Itoa(res.Connections), "emptied": strconv.Itoa(len(res.Emptied))})
	EventsEmit("catalog_changed", map[string]any{"catalog_id": id})
	return res, nil
}

// forgetCatalog drops a removed catalog's runtime state and cached images.
func (a *App) forgetCatalog(id string) {
	rt := a.catRT()
	rt.mu.Lock()
	delete(rt.clients, id)
	delete(rt.state, id)
	delete(rt.next, id)
	rt.mu.Unlock()
	if _, err := catalog.PruneImages(a.db); err != nil {
		log.Printf("catalog: prune images: %v", err)
	}
}

// CatalogCopyToLocal copies a catalog folder (with everything below it) or
// connection under one of the user's own folders ("" = top level). The
// copy is independent: it does not follow the catalog. What the top row
// inherits from catalog folders above it is folded in, so it behaves the
// same; stored passwords are copied too.
func (a *App) CatalogCopyToLocal(kind, id, targetFolderID string) (*store.LocaliseResult, error) {
	if kind != "folders" && kind != "connections" {
		return nil, fmt.Errorf("only folders and connections can be copied")
	}
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	var inherited any
	if kind == "folders" {
		inherited = resolver.EffectiveFolderSettings(id, folders)
	} else {
		c, err := a.db.GetConnection(id)
		if err != nil {
			return nil, err
		}
		inherited = resolver.EffectiveOverrides(*c, folders)
	}
	b, err := json.Marshal(inherited)
	if err != nil {
		return nil, err
	}
	res, copied, err := a.db.CopyCatalogRows(store.CopySource{Kind: kind, ID: id, Inherited: string(b)}, targetFolderID)
	if err != nil {
		return nil, err
	}
	for _, cc := range copied {
		res.PasswordsSkipped += a.copyConnectionSecrets(cc.From, cc.To)
	}
	a.recordAudit("catalog.copy_local", id, map[string]string{"kind": kind, "connections": strconv.Itoa(res.Connections)})
	EventsEmit("catalog_changed", map[string]any{})
	return res, nil
}

// copyConnectionSecrets copies a connection's stored SSH and VNC passwords
// to its copy. A missing or locked vault leaves the copy without them.
// It returns how many stored passwords could not be copied (a locked
// vault), so the result can say so.
func (a *App) copyConnectionSecrets(from, to string) int {
	c, err := a.db.GetConnection(from)
	if err != nil {
		return 0
	}
	skipped := 0
	one := func(key *string, set func(string, string) error, what string) {
		if key == nil {
			return
		}
		if a.vault == nil {
			skipped++
			return
		}
		v, ok, err := a.vault.Get(*key)
		if err != nil || !ok {
			skipped++
			return
		}
		if err := set(to, v); err != nil {
			log.Printf("copy %s: %v", what, err)
			skipped++
		}
	}
	one(c.PasswordVaultKey, a.SetConnectionPassword, "connection password")
	one(c.VncPasswordVaultKey, a.SetConnectionVncPassword, "VNC password")
	return skipped
}
