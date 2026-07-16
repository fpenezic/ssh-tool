package bitwarden

import (
	"errors"
	"testing"

	cssh "golang.org/x/crypto/ssh"
)

func TestOpenVaultResolvesPersonalAndOrg(t *testing.T) {
	f := buildFixture(t)
	v, err := OpenVault(f.sync, testMaster)
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}

	cases := []struct {
		name, cid, field, want string
	}{
		{"personal password", f.personalCID, FieldPassword, "personal-pass"},
		{"personal username", f.personalCID, FieldUsername, "root"},
		{"personal custom field", f.personalCID, f.customField, f.customValue},
		{"org password (org key)", f.orgCID, FieldPassword, "org-pass"},
		{"org username", f.orgCID, FieldUsername, "deploy"},
	}
	for _, c := range cases {
		got, err := v.Resolve(c.cid, c.field)
		if err != nil {
			t.Errorf("%s: Resolve error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestOpenVaultSSHKeyParses(t *testing.T) {
	f := buildFixture(t)
	v, err := OpenVault(f.sync, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := v.Resolve(f.sshCID, FieldPrivateKey)
	if err != nil {
		t.Fatalf("resolve ssh key: %v", err)
	}
	if pem != f.sshKeyPEM {
		t.Fatalf("ssh key PEM mismatch")
	}
	// The whole point: the resolved secret must parse as an SSH signer.
	if _, err := cssh.ParsePrivateKey([]byte(pem)); err != nil {
		t.Fatalf("resolved ssh key does not parse: %v", err)
	}
}

func TestOpenVaultWrongMaster(t *testing.T) {
	f := buildFixture(t)
	_, err := OpenVault(f.sync, "wrong password")
	if !errors.Is(err, ErrWrongMaster) {
		t.Fatalf("want ErrWrongMaster, got %v", err)
	}
}

func TestResolveUnknownCipher(t *testing.T) {
	f := buildFixture(t)
	v, _ := OpenVault(f.sync, testMaster)
	_, err := v.Resolve("does-not-exist", FieldPassword)
	if !errors.Is(err, ErrCipherNotFound) {
		t.Fatalf("want ErrCipherNotFound, got %v", err)
	}
}

func TestResolveEmptyField(t *testing.T) {
	f := buildFixture(t)
	v, _ := OpenVault(f.sync, testMaster)
	// The org login has no TOTP set.
	_, err := v.Resolve(f.orgCID, FieldTotp)
	if !errors.Is(err, ErrFieldEmpty) {
		t.Fatalf("want ErrFieldEmpty, got %v", err)
	}
}

func TestBrowseTree(t *testing.T) {
	f := buildFixture(t)
	v, _ := OpenVault(f.sync, testMaster)
	groups := v.Browse()

	// Expect a personal group + one org group.
	var personal, org *GroupInfo
	for i := range groups {
		switch groups[i].OrgID {
		case "":
			personal = &groups[i]
		case f.orgID:
			org = &groups[i]
		}
	}
	if personal == nil || org == nil {
		t.Fatalf("missing groups: personal=%v org=%v", personal != nil, org != nil)
	}
	if len(personal.Ciphers) != 1 || personal.Ciphers[0].Name != "Personal Host" {
		t.Fatalf("personal group wrong: %+v", personal.Ciphers)
	}
	if personal.Ciphers[0].Username != "root" {
		t.Errorf("personal username: got %q", personal.Ciphers[0].Username)
	}
	if len(personal.Ciphers[0].CustomKeys) != 1 || personal.Ciphers[0].CustomKeys[0] != f.customField {
		t.Errorf("custom keys: got %v", personal.Ciphers[0].CustomKeys)
	}

	// Org: the login sits in the "Infra" collection, the SSH key is uncollected.
	if len(org.Collections) != 1 || org.Collections[0].Name != "Infra" {
		t.Fatalf("org collections wrong: %+v", org.Collections)
	}
	if len(org.Collections[0].Ciphers) != 1 || org.Collections[0].Ciphers[0].Name != "Org Host" {
		t.Errorf("collection ciphers wrong: %+v", org.Collections[0].Ciphers)
	}
	var sawSSH bool
	for _, c := range org.Ciphers {
		if c.IsSSHKey && c.Name == "Org SSH Key" {
			sawSSH = true
		}
	}
	if !sawSSH {
		t.Errorf("org SSH key not in uncollected items: %+v", org.Ciphers)
	}
}
