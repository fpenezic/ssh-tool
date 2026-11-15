package main

// Binding catalog slots at the first connect.
//
// A catalog connection whose credential, network or jump host comes from
// a slot this user has not bound cannot connect. Instead of warning about
// every such slot up front (a toast after sync, a mark on every row), the
// app asks when the user clicks to connect: CatalogUnboundFor lists the
// unbound slots the connections about to open need, and the frontend
// shows one dialog to bind them all before it goes on. Connects without a
// click (workspace restore, tunnel autostart, MCP, the CI runner) never
// ask; they fail with the slot's error and the catalog root's chip.
//
// A one-time code slot can be skipped ("I type the code"): the skip is
// remembered per slot on this machine, so the dialog does not come back on
// every connect, and binding the slot clears it.

import (
	"encoding/json"
	"sort"
	"strings"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

const catalogOTPSkippedKey = "catalog_otp_skipped"

func init() { localStateKeys[catalogOTPSkippedKey] = true }

// CatalogPendingSlot is one unbound slot a connect needs.
type CatalogPendingSlot struct {
	CatalogID   string `json:"catalog_id"`
	CatalogName string `json:"catalog_name"`
	SlotUUID    string `json:"slot_uuid"`
	Name        string `json:"name"`
	// Type is the slot's (cred, net, jump); Role is what the connect uses
	// it for: "jump", "net", "cred" or "otp" (a credential slot used only
	// for the one-time code, which the user may skip).
	Type       string `json:"type"`
	Role       string `json:"role"`
	DocMD      string `json:"doc_md"`
	FolderName string `json:"folder_name"`
	// Connections: how many connections of this catalog on this machine
	// use the slot for anything (directly or through a bastion).
	Connections int `json:"connections"`
}

// Connect order: the bastion is dialled first, then the network, then the
// target's credential and its one-time code.
var slotRoleOrder = map[string]int{"jump": 0, "net": 1, "cred": 2, "otp": 3}

type slotUse struct{ catalogID, uuid, role string }

// What a connect opens decides which references it needs: a terminal,
// VNC or tunnel needs the SSH login and route; a Database tab also the
// database login (of the database it opens); a Kafka tab the Kafka login.
// "all" counts every use (the dialog's "used by N connections").
type slotPurpose struct {
	db, kafka, all bool
	dbTarget       string
}

func parsePurpose(p string) slotPurpose {
	switch {
	case p == "all":
		return slotPurpose{db: true, kafka: true, all: true}
	case p == "kafka":
		return slotPurpose{kafka: true}
	case p == "db" || strings.HasPrefix(p, "db:"):
		return slotPurpose{db: true, dbTarget: strings.TrimPrefix(strings.TrimPrefix(p, "db"), ":")}
	}
	return slotPurpose{}
}

// roleOf is what a field holding a slot marker is used for, or "" when
// this purpose does not need it.
func (p slotPurpose) roleOf(key string) string {
	switch key {
	case "auth_ref":
		return "cred"
	case "otp_ref":
		return "otp"
	case "network_profile_id":
		return "net"
	case "connection_id":
		return "jump"
	case "db_credential_ref", "credential_ref":
		if p.db {
			return "cred"
		}
	case "kafka_credential_ref":
		if p.kafka {
			return "cred"
		}
	}
	return ""
}

// unboundIn collects the unbound slot markers in a settings value, keyed
// by the field that holds them; follow is called for jump hosts that are
// connections.
func unboundIn(v any, key string, p slotPurpose, out map[string]slotUse, follow func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if k == "db_targets" {
				// Only the database being opened (every one for "all").
				list, _ := x.([]any)
				for _, it := range list {
					tg, _ := it.(map[string]any)
					if id, _ := tg["id"].(string); p.all || (p.db && p.dbTarget != "" && id == p.dbTarget) {
						unboundIn(tg, "", p, out, follow)
					}
				}
				continue
			}
			unboundIn(x, k, p, out, follow)
		}
	case []any:
		for _, x := range t {
			unboundIn(x, key, p, out, follow)
		}
	case string:
		e, ok := store.ParseCatalogRef(t, "")
		if !ok {
			if key == "connection_id" && t != "" && follow != nil {
				follow(t)
			}
			return
		}
		role := p.roleOf(key)
		if !e.Unbound || role == "" {
			return
		}
		k := e.CatalogID + "/" + e.UUID
		if prev, ok := out[k]; ok && slotRoleOrder[prev.role] <= slotRoleOrder[role] {
			return
		}
		out[k] = slotUse{catalogID: e.CatalogID, uuid: e.UUID, role: role}
	}
}

// slotsNeededBy returns the unbound slots a connection needs for a
// purpose: in its effective settings and, through jump hosts that are
// connections, in theirs (a bastion is only ever an SSH hop).
func (a *App) slotsNeededBy(conn store.Connection, folders []store.Folder, p slotPurpose) map[string]slotUse {
	out := map[string]slotUse{}
	seen := map[string]bool{conn.ID: true}
	var visit func(c store.Connection, p slotPurpose, depth int)
	visit = func(c store.Connection, p slotPurpose, depth int) {
		eff := resolver.EffectiveOverrides(c, folders)
		b, err := json.Marshal(eff)
		if err != nil {
			return
		}
		var m any
		if json.Unmarshal(b, &m) != nil {
			return
		}
		unboundIn(m, "", p, out, func(id string) {
			if depth >= 8 || seen[id] || strings.HasPrefix(id, resolver.DynRefPrefix) {
				return
			}
			seen[id] = true
			if bc, err := a.db.GetConnection(id); err == nil && bc != nil {
				visit(*bc, slotPurpose{}, depth+1)
			}
		})
	}
	visit(conn, p, 0)
	return out
}

func (a *App) otpSkipped() map[string]bool {
	out := map[string]bool{}
	if v, ok := a.localSettingGet(catalogOTPSkippedKey); ok {
		_ = json.Unmarshal([]byte(v), &out)
	}
	return out
}

func (a *App) setOTPSkipped(catalogID, slotUUID string, on bool) error {
	m := a.otpSkipped()
	k := catalogID + "/" + slotUUID
	if m[k] == on {
		return nil
	}
	if on {
		m[k] = true
	} else {
		delete(m, k)
	}
	if len(m) == 0 {
		return a.localSettingDelete(catalogOTPSkippedKey)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return a.localSettingSet(catalogOTPSkippedKey, string(b))
}

// CatalogUnboundFor lists the unbound catalog slots the given connections
// need for what is being opened (purpose: "" or "ssh" for a terminal, VNC
// or tunnel, "db:<target id>" for a Database tab, "kafka"), in connect
// order. Ids of dynamic hosts and unknown ids are passed over; a one-time
// code slot the user skipped on this machine is left out.
func (a *App) CatalogUnboundFor(connIDs []string, purpose string) ([]CatalogPendingSlot, error) {
	p := parsePurpose(purpose)
	folders, err := a.db.ListFolders()
	if err != nil {
		return nil, err
	}
	need := map[string]slotUse{}
	for _, id := range connIDs {
		if strings.HasPrefix(id, "dyn:") {
			continue
		}
		c, err := a.db.GetConnection(id)
		if err != nil || c == nil {
			continue
		}
		for k, u := range a.slotsNeededBy(*c, folders, p) {
			if prev, ok := need[k]; !ok || slotRoleOrder[u.role] < slotRoleOrder[prev.role] {
				need[k] = u
			}
		}
	}
	skipped := a.otpSkipped()
	for k, u := range need {
		if u.role == "otp" && skipped[k] {
			delete(need, k)
		}
	}
	if len(need) == 0 {
		return []CatalogPendingSlot{}, nil
	}

	// Per catalog: its name, slots, and how many of its connections need
	// each slot.
	out := []CatalogPendingSlot{}
	byCat := map[string][]slotUse{}
	for _, u := range need {
		byCat[u.catalogID] = append(byCat[u.catalogID], u)
	}
	all, err := a.db.ListConnections(nil)
	if err != nil {
		return nil, err
	}
	for catID, uses := range byCat {
		cat, err := a.db.GetCatalog(catID)
		if err != nil || cat == nil {
			continue
		}
		slots, err := a.db.ListCatalogSlots(catID)
		if err != nil {
			return nil, err
		}
		byUUID := map[string]store.CatalogSlot{}
		for _, s := range slots {
			byUUID[s.UUID] = s
		}
		counts := map[string]int{}
		for _, c := range all {
			if c.CatalogID == nil || *c.CatalogID != catID {
				continue
			}
			for _, u := range a.slotsNeededBy(c, folders, slotPurpose{db: true, kafka: true, all: true}) {
				counts[u.catalogID+"/"+u.uuid]++
			}
		}
		for _, u := range uses {
			s, ok := byUUID[u.uuid]
			if !ok {
				continue
			}
			p := CatalogPendingSlot{CatalogID: catID, CatalogName: cat.Name, SlotUUID: u.uuid, Name: s.Name,
				Type: s.Type, Role: u.role, DocMD: s.DocMD, Connections: counts[catID+"/"+u.uuid]}
			if _, lid := a.slotFolderWritable(catID, s.FolderUUID); lid != "" {
				if f, err := a.db.GetFolder(lid); err == nil {
					p.FolderName = f.Name
				}
			}
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return slotRoleOrder[out[i].Role] < slotRoleOrder[out[j].Role]
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// CatalogSkipOTP remembers on this machine that the user types the
// one-time code for this slot themselves; binding the slot clears it.
func (a *App) CatalogSkipOTP(catalogID, slotUUID string) error {
	return a.setOTPSkipped(catalogID, slotUUID, true)
}
