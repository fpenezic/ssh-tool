package main

import (
	"testing"

	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
)

func ocField(key, name, kind string) tunnelhelper.AuthField {
	return tunnelhelper.AuthField{Key: key, Name: name, Label: name + ":", Kind: kind}
}

// ocserv asks user, password and the one-time code in three rounds, the
// code under the same field name as the password.
func TestOCFillerSequentialRounds(t *testing.T) {
	f := &ocAuthFiller{a: &App{}, row: &store.NetworkProfile{Name: "p"}, cfg: &OpenConnectConfig{Username: "alice", SecondPassword: ocSecondPrompt}}
	v, miss := f.fill(tunnelhelper.AuthChallenge{ID: "1", Message: "Please enter your username.", Fields: []tunnelhelper.AuthField{ocField("main:username:1", "username", "text")}})
	if v["main:username:1"] != "alice" || len(miss) != 0 {
		t.Fatalf("round 1: %v missing %v", v, miss)
	}
	// No password credential: asked, and remembered as the first password.
	_, miss = f.fill(tunnelhelper.AuthChallenge{ID: "2", Message: "Please enter your password.", Fields: []tunnelhelper.AuthField{ocField("main:password:1", "password", "password")}})
	if len(miss) != 1 || !f.pass1 {
		t.Fatalf("round 2 missing %v pass1=%v", miss, f.pass1)
	}
	_, miss = f.fill(tunnelhelper.AuthChallenge{ID: "3", Message: "Please enter your OTP password.", Fields: []tunnelhelper.AuthField{ocField("main:password:1", "password", "password")}})
	if len(miss) != 1 || !f.pass2 {
		t.Fatalf("round 3 must be the second password: missing %v pass2=%v", miss, f.pass2)
	}
}

// Cisco ASA double authentication: one form, named secondary fields; the
// second user defaults to the first; a single-option group is picked.
func TestOCFillerASAForm(t *testing.T) {
	f := &ocAuthFiller{a: &App{}, row: &store.NetworkProfile{Name: "p"}, cfg: &OpenConnectConfig{Username: "alice", SecondPassword: ocSecondPrompt}}
	group := tunnelhelper.AuthField{Key: "group_list", Name: "group_list", Kind: "select", Options: []tunnelhelper.AuthOption{{Value: "staff"}}}
	v, miss := f.fill(tunnelhelper.AuthChallenge{ID: "1", Message: "Please enter your username and password.", Fields: []tunnelhelper.AuthField{
		group,
		ocField("username", "username", "text"),
		ocField("password", "password", "password"),
		ocField("secondary_username", "secondary_username", "text"),
		ocField("secondary_password", "secondary_password", "password"),
	}})
	if v["group_list"] != "staff" || v["username"] != "alice" {
		t.Fatalf("values = %v", v)
	}
	// A blank second username is asked, never copied from the first.
	if len(miss) != 3 || miss[0] != 2 || miss[1] != 3 || miss[2] != 4 {
		t.Fatalf("missing = %v, want password, secondary username, secondary password", miss)
	}
	want := map[string]string{"username": ocRoleUser1, "password": ocRolePass1, "secondary_username": ocRoleUser2, "secondary_password": ocRolePass2}
	for k, r := range want {
		if f.roles[k] != r {
			t.Fatalf("role of %s = %q, want %q", k, f.roles[k], r)
		}
	}
}

// A rejected answer brings the same form back with an error: nothing is
// autofilled again (the second password must not land in the first
// one's field) and each field keeps the role it had.
func TestOCFillerRejectedRoundAsksAll(t *testing.T) {
	f := &ocAuthFiller{a: &App{}, row: &store.NetworkProfile{Name: "p"}, cfg: &OpenConnectConfig{Username: "alice", SecondUsername: "bob", SecondPassword: ocSecondPrompt}}
	form := []tunnelhelper.AuthField{
		ocField("username", "username", "text"),
		ocField("password", "password", "password"),
		ocField("secondary_username", "secondary_username", "text"),
		ocField("secondary_password", "secondary_password", "password"),
	}
	v, _ := f.fill(tunnelhelper.AuthChallenge{ID: "1", Message: "Please enter your username and password.", Fields: form})
	if v["secondary_username"] != "bob" {
		t.Fatalf("round 1 = %v", v)
	}
	again := make([]tunnelhelper.AuthField, len(form))
	for i, fl := range form {
		fl.Key = fl.Key + ":2"
		again[i] = fl
	}
	v, miss := f.fill(tunnelhelper.AuthChallenge{ID: "2", Error: "Login failed.", Message: "Please enter your username and password.", Fields: again})
	if len(v) != 0 || len(miss) != 4 {
		t.Fatalf("rejected round: values %v missing %v", v, miss)
	}
	if f.roles["password:2"] != ocRolePass1 || f.roles["secondary_password:2"] != ocRolePass2 {
		t.Fatalf("roles = %v", f.roles)
	}
}
