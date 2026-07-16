package bitwarden

import (
	"sort"
	"strings"
)

// CipherInfo describes one item for the picker (decrypted metadata only, never
// the secret itself).
type CipherInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Username    string   `json:"username"`
	Type        int      `json:"type"`
	IsSSHKey    bool     `json:"is_ssh_key"`
	HasPassword bool     `json:"has_password"`
	HasTotp     bool     `json:"has_totp"`
	CustomKeys  []string `json:"custom_keys"`
	Attachments []string `json:"attachments"`
}

// GroupInfo is a browse node: an organization (or the personal vault) holding
// collections, each holding ciphers. Personal items have OrgID "".
type GroupInfo struct {
	OrgID       string           `json:"org_id"`
	Name        string           `json:"name"`
	Collections []CollectionInfo `json:"collections"`
	Ciphers     []CipherInfo     `json:"ciphers"` // items with no collection (personal, or org-uncollected)
}

// CollectionInfo groups ciphers within an organization.
type CollectionInfo struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Ciphers []CipherInfo `json:"ciphers"`
}

// Browse returns the decrypted tree for the picker: a personal group plus one
// group per organization, each with its collections and items. Names that fail
// to decrypt are shown as "<locked>" rather than dropped.
func (v *Vault) Browse() []GroupInfo {
	// Index collections by org and id.
	collByOrg := map[string][]collection{}
	collName := map[string]string{}
	for _, c := range v.collections {
		collByOrg[c.OrganizationID] = append(collByOrg[c.OrganizationID], c)
		if key, ok := v.orgKeys[c.OrganizationID]; ok {
			if n, err := decStr(c.Name, key); err == nil {
				collName[c.ID] = n
			} else {
				collName[c.ID] = "<locked>"
			}
		} else {
			collName[c.ID] = "<locked>"
		}
	}

	personal := GroupInfo{OrgID: "", Name: "My Vault"}
	orgGroups := map[string]*GroupInfo{}
	for _, org := range v.orgs {
		orgGroups[org.ID] = &GroupInfo{OrgID: org.ID, Name: v.orgDisplayName(org)}
	}

	// Bucket ciphers into personal / org-collection / org-uncollected.
	orgColCiphers := map[string]map[string][]CipherInfo{} // orgID -> collID -> items
	for i := range v.ciphers {
		c := &v.ciphers[i]
		info := v.cipherInfo(c)
		if c.OrganizationID == "" {
			personal.Ciphers = append(personal.Ciphers, info)
			continue
		}
		g := orgGroups[c.OrganizationID]
		if g == nil {
			// org we couldn't unwrap; still show under a placeholder group
			g = &GroupInfo{OrgID: c.OrganizationID, Name: "<locked organization>"}
			orgGroups[c.OrganizationID] = g
		}
		if len(c.CollectionIDs) == 0 {
			g.Ciphers = append(g.Ciphers, info)
			continue
		}
		if orgColCiphers[c.OrganizationID] == nil {
			orgColCiphers[c.OrganizationID] = map[string][]CipherInfo{}
		}
		for _, cid := range c.CollectionIDs {
			orgColCiphers[c.OrganizationID][cid] = append(orgColCiphers[c.OrganizationID][cid], info)
		}
	}

	// Attach collections (only those that have items) to their org groups.
	for orgID, g := range orgGroups {
		for _, col := range collByOrg[orgID] {
			items := orgColCiphers[orgID][col.ID]
			if len(items) == 0 {
				continue
			}
			sortCiphers(items)
			g.Collections = append(g.Collections, CollectionInfo{
				ID:      col.ID,
				Name:    collName[col.ID],
				Ciphers: items,
			})
		}
		sort.Slice(g.Collections, func(i, j int) bool {
			return strings.ToLower(g.Collections[i].Name) < strings.ToLower(g.Collections[j].Name)
		})
		sortCiphers(g.Ciphers)
	}

	sortCiphers(personal.Ciphers)

	out := []GroupInfo{personal}
	orgIDs := make([]string, 0, len(orgGroups))
	for id := range orgGroups {
		orgIDs = append(orgIDs, id)
	}
	sort.Slice(orgIDs, func(i, j int) bool {
		return strings.ToLower(orgGroups[orgIDs[i]].Name) < strings.ToLower(orgGroups[orgIDs[j]].Name)
	})
	for _, id := range orgIDs {
		out = append(out, *orgGroups[id])
	}
	return out
}

func (v *Vault) orgDisplayName(org organization) string {
	// Org names in Profile.Organizations are plaintext on Vaultwarden.
	if org.Name != "" {
		return org.Name
	}
	return "Organization"
}

// cipherInfo decrypts the display metadata of one cipher.
func (v *Vault) cipherInfo(c *cipherItem) CipherInfo {
	key, err := v.keyFor(c)
	info := CipherInfo{ID: c.ID, Type: c.Type}
	if err != nil {
		info.Name = "<locked>"
		return info
	}
	if n, e := decStr(c.Name, key); e == nil {
		info.Name = n
	} else {
		info.Name = "<locked>"
	}
	if c.Login != nil {
		if u, e := decStr(c.Login.Username, key); e == nil {
			info.Username = u
		}
		info.HasPassword = c.Login.Password != ""
		info.HasTotp = c.Login.Totp != ""
	}
	if c.SSHKey != nil {
		info.IsSSHKey = true
	}
	for _, f := range c.Fields {
		if n, e := decStr(f.Name, key); e == nil && n != "" {
			info.CustomKeys = append(info.CustomKeys, n)
		}
	}
	for _, a := range c.Attachments {
		info.Attachments = append(info.Attachments, a.FileName)
	}
	return info
}

func sortCiphers(items []CipherInfo) {
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}

func normalizeField(field string) string {
	return strings.ToLower(strings.TrimSpace(field))
}

func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
