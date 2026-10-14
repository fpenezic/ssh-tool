package kafkatool

import (
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/resolver"
	"ssh-tool/internal/store"
)

const sample = `[
  {"id": "1", "name": "staging", "bootstrapServers": "127.0.0.1:9092",
   "sshTunnelEnabled": true, "sshHost": "10.0.0.5", "sshPort": 2222,
   "sshUsername": "deploy", "sshAuthType": "PASSWORD", "sshKeyPath": "",
   "sshPassword": "pw1", "sshProxyJumpEnabled": true,
   "sshProxyJumpHost": "bastion.example.com", "sshProxyJumpPort": 22,
   "sshProxyJumpUsername": "jump", "sshProxyJumpKeyPath": ""},
  {"id": "2", "name": "local", "bootstrapServers": "broker1.example.com:9093,broker2.example.com:9093",
   "sshTunnelEnabled": false},
  {"id": "3", "name": "keyed", "bootstrapServers": "kafka:9092",
   "sshTunnelEnabled": true, "sshHost": "10.0.0.6", "sshAuthType": "KEY_FILE",
   "sshKeyPath": "~/.ssh/id_ed25519", "sshPassword": "passphrase"},
  {"id": "4", "name": "", "bootstrapServers": "x:1"},
  {"id": "5", "name": "broken", "bootstrapServers": "x:1", "sshTunnelEnabled": true}
]`

func TestPlan(t *testing.T) {
	ps, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Plan(ps[0])
	if err != nil {
		t.Fatal(err)
	}
	o := p.Conn.Overrides
	if p.Conn.Hostname != "10.0.0.5" || *o.Port != 2222 || *o.Username != "deploy" || p.Password != "pw1" {
		t.Fatalf("ssh fields: %+v %q", p.Conn, p.Password)
	}
	if !*o.KafkaEnabled || *o.KafkaBootstrap != "127.0.0.1:9092" || !*o.KafkaUseTunnel || o.KafkaDefault != nil {
		t.Fatalf("kafka fields: %+v", o)
	}
	if o.JumpHost == nil || o.JumpHost.Chain.Hostname != "bastion.example.com" || *o.JumpHost.Chain.Username != "jump" || o.JumpHost.Chain.Port != nil {
		t.Fatalf("jump: %+v", o.JumpHost)
	}

	p, _ = Plan(ps[1])
	o = p.Conn.Overrides
	if p.Conn.Hostname != "broker1.example.com" || *o.KafkaUseTunnel || !*o.KafkaDefault || p.Password != "" {
		t.Fatalf("direct: %+v", p.Conn)
	}

	// A key-file login keeps no secret (the passphrase is not a password)
	// and says so.
	p, _ = Plan(ps[2])
	if p.Password != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "id_ed25519") {
		t.Fatalf("keyed: %q %v", p.Password, p.Warnings)
	}

	if _, err := Plan(ps[3]); err == nil {
		t.Fatal("no name: want error")
	}
	if _, err := Plan(ps[4]); err == nil {
		t.Fatal("tunnel without host: want error")
	}
}

func TestApply(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ps, _ := Parse(sample)
	saved := map[string]string{}
	save := func(id, pw string) error { saved[id] = pw; return nil }

	sum, err := Apply(db, ps, "", save)
	if err != nil {
		t.Fatal(err)
	}
	if sum.FoldersCreated != 1 || sum.ConnectionsCreated != 3 || sum.ConnectionsSkipped != 2 || sum.PasswordsStored != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	conns, _ := db.ListConnections(nil)
	folders, _ := db.ListFolders()
	for _, c := range conns {
		if c.FolderID == nil || len(folders) != 1 || *c.FolderID != folders[0].ID || folders[0].Name != FolderName {
			t.Fatalf("%s not in the kafka-tool folder", c.Name)
		}
		r := resolver.ResolveWith(c, folders)
		if !r.KafkaEnabled || r.KafkaBootstrap == "" {
			t.Fatalf("%s: kafka not enabled", c.Name)
		}
		if c.Name == "staging" && saved[c.ID] != "pw1" {
			t.Fatalf("staging password not handed over")
		}
	}

	// Again: the names are taken, the folder is reused.
	sum, err = Apply(db, ps, "", save)
	if err != nil {
		t.Fatal(err)
	}
	if sum.FoldersCreated != 0 || sum.ConnectionsCreated != 0 || sum.ConnectionsSkipped != 5 {
		t.Fatalf("second run = %+v", sum)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse("{not json"); err == nil {
		t.Fatal("want error")
	}
}
