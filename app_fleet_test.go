package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
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

	j := TLSCertResult{Hostname: "localhost", Port: port}
	checkOneTLS(&j)
	if j.State != "ok" || j.NotAfter == 0 || j.DaysLeft <= 0 {
		t.Fatalf("result = %+v", j)
	}
	if j.Trusted || j.TrustError == "" {
		t.Errorf("a self-signed test cert must be untrusted: %+v", j)
	}

	ip := TLSCertResult{Hostname: "127.0.0.1", Port: port}
	checkOneTLS(&ip)
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
