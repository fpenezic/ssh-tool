package store

import (
	"reflect"
	"testing"
)

// TestCredentialUsageConfigs: a credential referenced only from a
// dynamic folder's provider config, a network profile, a Kafka or
// database login or the sync target still shows up in "Used by" (and so blocks Delete).
func TestCredentialUsageConfigs(t *testing.T) {
	db := openTestDB(t)
	const cred = "cred-1"

	f, err := db.CreateFolder(NewFolder{Name: "Cluster"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDynamicFolder(DynamicFolder{
		FolderID: f.ID, Provider: "proxmox", RefreshSeconds: 300,
		Config: map[string]any{
			"base_url":                "https://pve.example.com:8006",
			"api_token_credential_id": cred,
			"vnc_credential_id":       "other",
		},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := db.CreateNetworkProfile("Office VPN",
		`{"kind":"openconnect","server":"vpn.example.com","password_credential_id":"cred-1","client_cert_credential_id":"cred-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateNetworkProfile("Other", `{"kind":"tailscale","auth_key_credential_id":"other"}`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("sync_sftp_cred_id", cred); err != nil {
		t.Fatal(err)
	}

	kref := cred
	c, err := db.CreateConnection(NewConnection{
		Name: "broker-1", Hostname: "broker-1.example.com",
		Overrides: InheritableSettings{KafkaCredentialRef: &kref, DbCredentialRef: &kref,
			DbTargets: []DbTarget{{ID: "t1", Name: "stage", CredentialRef: &kref}, {ID: "t2", Name: "prod"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.CredentialUsage(cred)
	if err != nil {
		t.Fatal(err)
	}
	want := []UsageRef{
		{Kind: "connection", ID: c.ID, Name: "broker-1", Hostname: "broker-1.example.com", Role: "Kafka SASL"},
		{Kind: "connection", ID: c.ID, Name: "broker-1", Hostname: "broker-1.example.com", Role: "Database login"},
		{Kind: "connection", ID: c.ID, Name: "broker-1", Hostname: "broker-1.example.com", Role: "Database login (stage)"},
		{Kind: "folder", ID: f.ID, Name: "Cluster", Role: "API token"},
		{Kind: "network_profile", ID: p.ID, Name: "Office VPN", Role: "client cert"},
		{Kind: "network_profile", ID: p.ID, Name: "Office VPN", Role: "password"},
		{Kind: "sync", ID: "sync", Name: "Profile sync", Role: "SFTP login"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("usage:\n got %+v\nwant %+v", got, want)
	}
}
