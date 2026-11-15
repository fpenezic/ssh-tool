package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"ssh-tool/internal/catalog"
	"ssh-tool/internal/store"
)

// The connect dialog's list: the unbound slots a connection needs, from
// its folder and through a jump host, in connect order, with how many
// connections of the catalog need each; a skipped one-time code slot stays
// out until the slot is bound.
func TestCatalogUnboundFor(t *testing.T) {
	saved := localStateStore
	localStateStore = &localState{path: filepath.Join(t.TempDir(), "local-state.json")}
	t.Cleanup(func() { localStateStore = saved })

	ca := newCatalogApp(t)
	a := ca.a
	catID := ca.cat.ID
	// The folder's credential (s1) plus a one-time code slot (s2) and a
	// second catalog connection in the same folder.
	err := a.db.WithCatalogWriter(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE folders SET catalog_json = ? WHERE id = 'cf'`,
			`{"uuid":"f-uuid","rev":1,"parent_uuid":null,"name":"Customer A","access":"read","settings":{"auth_ref":"s1","otp_ref":"s2","username":"deploy"}}`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO connections (id, folder_id, name, hostname, created_at, updated_at, catalog_id, remote_uuid, remote_rev, catalog_json)
			VALUES ('cc2', 'cf', 'db-1', 'db-1.example.com', 1, 1, ?, 'c2-uuid', 1, '{"uuid":"c2-uuid","rev":1,"folder_uuid":"f-uuid","name":"db-1","hostname":"db-1.example.com","settings":{"db_credential_ref":"s3"}}')`, catID); err != nil {
			return err
		}
		if err := a.db.UpsertCatalogSlotTx(tx, store.CatalogSlot{CatalogID: catID, UUID: "s3", Rev: 1, Type: "cred", Name: "db login", Kind: "slot"}); err != nil {
			return err
		}
		return a.db.UpsertCatalogSlotTx(tx, store.CatalogSlot{CatalogID: catID, UUID: "s2", Rev: 1, Type: "cred", Name: "otp", Kind: "slot"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Materialise(a.db, catID, nil); err != nil {
		t.Fatal(err)
	}

	got, err := a.CatalogUnboundFor([]string{ca.connID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SlotUUID != "s1" || got[0].Role != "cred" || got[1].SlotUUID != "s2" || got[1].Role != "otp" {
		t.Fatalf("pending: %+v", got)
	}
	if got[0].Connections != 2 || got[0].Name != "admin" || got[0].DocMD != "Ask ops." || got[0].CatalogName != "Team" {
		t.Fatalf("slot details: %+v", got[0])
	}

	// The database login is asked for when a Database tab opens, not for
	// a terminal.
	if got, _ = a.CatalogUnboundFor([]string{"cc2"}, "ssh"); len(got) != 2 {
		t.Fatalf("terminal asks for the database login: %+v", got)
	}
	if got, _ = a.CatalogUnboundFor([]string{"cc2"}, "db:"); len(got) != 3 || got[1].SlotUUID != "s3" || got[1].Connections != 1 || got[2].Role != "otp" {
		t.Fatalf("database tab: %+v", got)
	}

	// A local connection jumping through the catalog host needs the same
	// slots; the bastion's credential is asked for under "cred".
	cc := ca.connID
	local, err := a.db.CreateConnection(store.NewConnection{Name: "inner", Hostname: "10.0.0.5",
		Overrides: store.InheritableSettings{JumpHost: &store.JumpHostOverride{Kind: "chain", Chain: &store.JumpHostSpec{ConnectionID: &cc}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = a.CatalogUnboundFor([]string{local.ID}, "ssh"); len(got) != 2 || got[0].SlotUUID != "s1" {
		t.Fatalf("through the jump host: %+v", got)
	}

	// Skipping the code slot drops it; binding it clears the skip.
	if err := a.CatalogSkipOTP(catID, "s2"); err != nil {
		t.Fatal(err)
	}
	if got, _ = a.CatalogUnboundFor([]string{ca.connID}, ""); len(got) != 1 || got[0].SlotUUID != "s1" {
		t.Fatalf("after skip: %+v", got)
	}
	slots, _ := a.CatalogSlots(catID)
	for _, s := range slots {
		if s.UUID == "s2" && !s.Skipped {
			t.Fatalf("slot panel does not show the skip: %+v", s)
		}
	}
	cred, err := a.db.CreateCredential(store.NewCredential{Name: "my key", Kind: store.CredAgent})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CatalogBind(catID, "s2", cred.ID); err != nil {
		t.Fatal(err)
	}
	if a.otpSkipped()[catID+"/s2"] {
		t.Fatal("bind left the skip behind")
	}
	if err := a.CatalogBind(catID, "s1", cred.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = a.CatalogUnboundFor([]string{ca.connID, local.ID, "dyn:x", "nope"}, ""); len(got) != 0 {
		t.Fatalf("all bound: %+v", got)
	}
}
