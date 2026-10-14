package main

import (
	"strings"
	"testing"

	"ssh-tool/internal/store"
	"ssh-tool/internal/tunnelhelper"
)

func TestCheckpointNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"vpn.example.com":                   "vpn.example.com",
		"  vpn.example.com  ":               "vpn.example.com",
		"https://vpn.example.com/":          "vpn.example.com",
		"https://vpn.example.com/sslvpn/":   "vpn.example.com",
		"vpn.example.com:4433":              "vpn.example.com:4433",
		"https://192.0.2.10:443/Login/Home": "192.0.2.10:443",
	} {
		c := CheckpointConfig{Server: in}
		if err := c.normalize(); err != nil || c.Server != want {
			t.Errorf("%q -> %q (%v), want %q", in, c.Server, err, want)
		}
		if c.Kind != kindCheckpoint || c.SecondPassword != ocSecondPrompt {
			t.Errorf("%q: kind %q, second %q", in, c.Kind, c.SecondPassword)
		}
	}
	for _, bad := range []string{"", "https://", "vpn.example.com:0", "vpn.example.com:http", "vpn example.com"} {
		c := CheckpointConfig{Server: bad}
		if err := c.normalize(); err == nil {
			t.Errorf("%q accepted as %q", bad, c.Server)
		}
	}
	c := CheckpointConfig{Server: "vpn.example.com", SecondPassword: ocSecondTOTP}
	if err := c.normalize(); err == nil {
		t.Error("a TOTP source without its credential was accepted")
	}
	c = CheckpointConfig{Server: "vpn.example.com", SecondPassword: ocSecondPrompt, SecondCredentialID: "x"}
	if err := c.normalize(); err != nil || c.SecondCredentialID != "" {
		t.Errorf("prompt kept a credential: %q %v", c.SecondCredentialID, err)
	}
}

func TestCheckpointArgsAndHostPort(t *testing.T) {
	c := CheckpointConfig{Server: "vpn.example.com", LoginOption: "vpn_Radius"}
	got := strings.Join(cpArgs(&c, []string{"pin-sha256:AAA"}), " ")
	if got != "--server vpn.example.com --login-option vpn_Radius --pin pin-sha256:AAA" {
		t.Fatalf("args %q", got)
	}
	if h, p := c.hostPort(); h != "vpn.example.com" || p != 443 {
		t.Fatalf("hostPort %s %d", h, p)
	}
	c.Server = "vpn.example.com:4433"
	if h, p := c.hostPort(); h != "vpn.example.com" || p != 4433 {
		t.Fatalf("hostPort %s %d", h, p)
	}
}

// The plugin's login round: username from the profile, password asked
// (no credential); a refused login asks everything again, a forced
// re-login ("reconnecting") fills from storage again.
func TestLoginFillerRounds(t *testing.T) {
	f := &loginFiller{a: &App{}, row: &store.NetworkProfile{Name: "p"}, tag: kindCheckpoint,
		login: loginSources{Username: "alice", SecondPassword: ocSecondPrompt}}
	fields := []tunnelhelper.AuthField{
		{Key: "username", Name: "username", Label: "Username:", Kind: "text"},
		{Key: "password", Name: "password", Label: "Password:", Kind: "password"},
	}
	v, miss := f.fill(tunnelhelper.AuthChallenge{ID: "1", Fields: fields})
	if v["username"] != "alice" || len(miss) != 1 || miss[0] != 1 {
		t.Fatalf("first round %v missing %v", v, miss)
	}
	v, miss = f.fill(tunnelhelper.AuthChallenge{ID: "2", Error: "Access denied - wrong user name or password", Fields: fields})
	if len(v) != 0 || len(miss) != 2 {
		t.Fatalf("refused round must ask everything: %v missing %v", v, miss)
	}
	v, miss = f.fill(tunnelhelper.AuthChallenge{ID: "3", Error: "reconnecting", Fields: fields})
	if v["username"] != "alice" || len(miss) != 1 {
		t.Fatalf("reconnect round %v missing %v", v, miss)
	}
	_, miss = f.fill(tunnelhelper.AuthChallenge{ID: "4", Message: "Enter the code", Fields: []tunnelhelper.AuthField{
		{Key: "response", Name: "response", Label: "Code:", Kind: "password"},
	}})
	if len(miss) != 1 {
		t.Fatalf("a prompted one-time code must be asked: missing %v", miss)
	}
}
