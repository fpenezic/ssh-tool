package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

func TestAuthorizedKeyRe(t *testing.T) {
	good := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB7 laptop",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTI=",
		"sk-ssh-ed25519@openssh.com AAAAGnNr user@example.com",
	}
	bad := []string{
		"ssh-ed25519 AAAA'; rm -rf ~; echo '",
		"ssh-ed25519 AAAA\nssh-rsa BBBB",
		"command=\"/bin/sh\" ssh-ed25519 AAAA",
		"not a key",
	}
	for _, k := range good {
		if !authorizedKeyRe.MatchString(k) {
			t.Errorf("rejected %q", k)
		}
	}
	for _, k := range bad {
		if authorizedKeyRe.MatchString(k) {
			t.Errorf("accepted %q", k)
		}
	}
}

// A self-signed test server reached by name: the certificate is read and
// its expiry reported, and it is marked untrusted rather than failing.
func TestCheckOneTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	a := &App{}
	j := TLSCertResult{Hostname: "localhost", Port: port}
	a.checkOneTLS(&j, nil)
	if j.State != "ok" || j.NotAfter == 0 || j.DaysLeft <= 0 {
		t.Fatalf("result = %+v", j)
	}
	if j.Trusted || j.TrustError == "" {
		t.Errorf("a self-signed test cert must be untrusted: %+v", j)
	}

	// With resolved settings and no jump chain, DialVia dials directly.
	viaSettings := TLSCertResult{Hostname: "localhost", Port: port}
	a.checkOneTLS(&viaSettings, &store.ResolvedSettings{Hostname: "localhost", Port: 22})
	if viaSettings.State != "ok" || viaSettings.NotAfter != j.NotAfter {
		t.Errorf("settings path = %+v", viaSettings)
	}

	ip := TLSCertResult{Hostname: "127.0.0.1", Port: port}
	a.checkOneTLS(&ip, nil)
	if ip.State != "skipped" {
		t.Errorf("an IP host must be skipped, got %+v", ip)
	}
}

func TestKeyCommentRe(t *testing.T) {
	for _, ok := range []string{"", "jane@laptop", "Team - deploy-ci", "ops key 2026"} {
		if !keyCommentRe.MatchString(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"a'b", "x\ny", "$(id)", "a;b", "a`b"} {
		if keyCommentRe.MatchString(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Download folders: named after the connection, legal on every OS, and
// unique even when two hosts share a name (case-insensitively).
func TestFleetHostDirs(t *testing.T) {
	got := fleetHostDirs([]sshlayer.BatchHostInput{
		{ConnectionID: "a", Name: "web:01"},
		{ConnectionID: "b", Name: "Web_01"},
		{ConnectionID: "c", Name: "", Hostname: "db.example.com"},
	})
	if got["a"] != "web_01" || got["b"] != "Web_01-2" || got["c"] != "db.example.com" {
		t.Errorf("dirs = %v", got)
	}
}
