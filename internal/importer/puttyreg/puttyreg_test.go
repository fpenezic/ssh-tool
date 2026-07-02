package puttyreg

import (
	"path/filepath"
	"testing"
	"unicode/utf16"

	"ssh-tool/internal/store"
)

const sample = `Windows Registry Editor Version 5.00

[HKEY_CURRENT_USER\Software\SimonTatham\PuTTY\Sessions\Default%20Settings]
"HostName"=""
"Protocol"="ssh"

[HKEY_CURRENT_USER\Software\SimonTatham\PuTTY\Sessions\web-01]
"HostName"="192.0.2.10"
"PortNumber"=dword:00000016
"UserName"="root"
"Protocol"="ssh"

[HKEY_CURRENT_USER\Software\SimonTatham\PuTTY\Sessions\prod%20bastion]
"HostName"="ops@bastion.example"
"PortNumber"=dword:000008ae
"Protocol"="ssh"

[HKEY_CURRENT_USER\Software\SimonTatham\PuTTY\Sessions\old-serial]
"HostName"="COM1"
"Protocol"="serial"
`

func TestParse(t *testing.T) {
	entries, sum, err := Parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 ssh entries, got %d: %+v", len(entries), entries)
	}
	if sum.SkippedNonSSH != 1 {
		t.Fatalf("want 1 non-ssh (serial), got %d", sum.SkippedNonSSH)
	}
	e := entries[0]
	if e.Name != "web-01" || e.Host != "192.0.2.10" || e.Port != 22 || e.User != "root" {
		t.Fatalf("bad entry 0: %+v", e)
	}
	// %20 decodes, dword:08ae = 2222, user@host splits.
	e = entries[1]
	if e.Name != "prod bastion" || e.Host != "bastion.example" || e.Port != 2222 || e.User != "ops" {
		t.Fatalf("bad entry 1: %+v", e)
	}
}

func TestDecodeUTF16(t *testing.T) {
	// reg.exe writes UTF-16LE with BOM - the common real-world case.
	u := utf16.Encode([]rune(sample))
	raw := []byte{0xFF, 0xFE}
	for _, v := range u {
		raw = append(raw, byte(v), byte(v>>8))
	}
	entries, _, err := Parse(Decode(raw))
	if err != nil {
		t.Fatalf("parse utf16: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("utf16 roundtrip lost entries: %d", len(entries))
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, _, err := Parse("Windows Registry Editor Version 5.00\n\n[HKEY_CURRENT_USER\\Software\\Foo]\n\"Bar\"=\"baz\"\n"); err == nil {
		t.Fatalf("non-putty reg should error")
	}
}

func TestApply(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	entries, sum, err := Parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sum, err = Apply(db, entries, sum, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sum.ConnectionsCreated != 2 {
		t.Fatalf("want 2 created, got %+v", sum)
	}

	conns, _ := db.ListConnections(nil)
	for _, c := range conns {
		if c.Name == "prod bastion" {
			if c.Overrides.Username == nil || *c.Overrides.Username != "ops" {
				t.Fatalf("user@host split lost: %+v", c.Overrides)
			}
			if c.Overrides.Port == nil || *c.Overrides.Port != 2222 {
				t.Fatalf("port lost: %+v", c.Overrides)
			}
		}
	}

	// Re-import skips by name.
	entries2, sum2, _ := Parse(sample)
	sum2, err = Apply(db, entries2, sum2, "")
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if sum2.ConnectionsCreated != 0 || sum2.ConnectionsSkipped != 2 {
		t.Fatalf("re-import should skip, got %+v", sum2)
	}
}
