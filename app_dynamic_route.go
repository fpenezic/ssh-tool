package main

import (
	"ssh-tool/internal/inventory"
	"ssh-tool/internal/store"
)

// Bastion routing for cloud dynamic folders.
//
// A folder can name one of its own instances as the bastion
// (config bastion_external_id, with bastion_name as the fallback when the
// instance was rebuilt under a new id). Every entry that has a private
// address but no public one is then reached through that bastion's public
// address; entries with a public address, and the bastion itself, dial
// directly. bastion_credential_id optionally gives the bastion its own
// login - on the bastion's own entry and on the jump hop - while the
// private hosts keep the folder's credential.
//
// Everything that turns a dynamic entry into a connection (connect, batch,
// probe, pin, convert-to-static, copy-ssh-command) goes through
// dynamicConnection, so none of them can disagree about the route.

// dynamicConnection builds the synthetic connection for a dynamic entry:
// the entry's host plus the per-host overrides it carries on top of its
// folder. df may be nil (then only the entry itself is used).
// jumpCredOverride replaces the folder's Ansible jump credential when set.
func (a *App) dynamicConnection(entry *store.DynamicEntry, df *store.DynamicFolder, jumpCredOverride string) store.Connection {
	folderRef := entry.FolderID
	c := store.Connection{
		ID:        "dyn:" + entry.ID,
		FolderID:  &folderRef,
		Name:      entry.Name,
		Hostname:  entry.Hostname,
		Overrides: store.InheritableSettings{},
	}
	// Ansible per-host vars (ansible_user / port / jump hops) come first;
	// the jump credential is the folder's, unless the caller overrides it.
	jumpCred := jumpCredOverride
	if jumpCred == "" && df != nil {
		jumpCred, _ = df.Config["jump_credential_id"].(string)
	}
	applyAnsibleVarsToConnection(&c, entry.Raw, jumpCred)
	if df != nil {
		a.applyBastion(&c, entry, df)
	}
	return c
}

// dynamicConnectionFor is dynamicConnection when the caller has not loaded
// the folder row.
func (a *App) dynamicConnectionFor(entry *store.DynamicEntry, jumpCredOverride string) store.Connection {
	df, _ := a.db.GetDynamicFolder(entry.FolderID)
	return a.dynamicConnection(entry, df, jumpCredOverride)
}

// applyBastion routes a private-only entry through the folder's bastion and
// gives the bastion its own credential. A jump host already set on the entry
// (Ansible vars) wins - it is more specific than the folder rule.
func (a *App) applyBastion(c *store.Connection, entry *store.DynamicEntry, df *store.DynamicFolder) {
	bastionID, _ := df.Config["bastion_external_id"].(string)
	bastionName, _ := df.Config["bastion_name"].(string)
	if bastionID == "" && bastionName == "" {
		return
	}
	bastionCred, _ := df.Config["bastion_credential_id"].(string)
	bastion := a.findBastion(df, bastionID, bastionName)
	if bastion == nil {
		return // bastion gone from the inventory; the direct dial fails visibly
	}
	if bastion.ID == entry.ID {
		if bastionCred != "" {
			cred := bastionCred
			c.Overrides.AuthRef = &cred
		}
		return
	}
	pub, priv := inventory.Addresses(df.Provider, entry.Raw)
	if pub != "" || priv == "" || c.Overrides.JumpHost != nil {
		return
	}
	bastionAddr, _ := inventory.Addresses(df.Provider, bastion.Raw)
	if bastionAddr == "" {
		return
	}
	hop := &store.JumpHostSpec{Hostname: bastionAddr}
	if bastionCred != "" {
		cred := bastionCred
		hop.AuthRef = &cred
		// The hop would otherwise inherit the TARGET's username; a
		// bastion with its own login usually has its own user too.
		if cr, err := a.db.GetCredential(bastionCred); err == nil && cr != nil && cr.DefaultUsername != nil {
			u := *cr.DefaultUsername
			hop.Username = &u
		}
	}
	c.Hostname = priv
	c.Overrides.JumpHost = &store.JumpHostOverride{Kind: "chain", Chain: hop}
}

// findBastion looks the bastion up among the folder's cached entries: by
// external id, else by name (an instance rebuilt under the same name).
func (a *App) findBastion(df *store.DynamicFolder, id, name string) *store.DynamicEntry {
	entries, err := a.db.ListDynamicEntries(df.FolderID)
	if err != nil {
		return nil
	}
	var byName *store.DynamicEntry
	for i := range entries {
		e := &entries[i]
		if id != "" && e.ExternalID == id {
			return e
		}
		if name != "" && e.Name == name && byName == nil {
			byName = e
		}
	}
	return byName
}
