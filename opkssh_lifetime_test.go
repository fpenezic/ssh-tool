package main

import (
	"testing"

	sshlayer "ssh-tool/internal/ssh"
)

func TestCertLifetimeWindow(t *testing.T) {
	cases := []struct {
		name       string
		st         sshlayer.CertStatus
		start, end int64
		ok         bool
	}{
		{"finite cert, issue time recorded", sshlayer.CertStatus{HasCert: true, IssuedAt: 100, ValidAfter: 90, ValidBefore: 500, RenewAt: 440}, 100, 500, true},
		{"forever cert ends at the forced re-login", sshlayer.CertStatus{HasCert: true, IssuedAt: 100, RenewAt: 700}, 100, 700, true},
		{"no recorded issue time falls back to ValidAfter", sshlayer.CertStatus{HasCert: true, ValidAfter: 90, ValidBefore: 500}, 90, 500, true},
		{"no cert", sshlayer.CertStatus{}, 0, 0, false},
		{"forever cert with no issue time has no window", sshlayer.CertStatus{HasCert: true}, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := c.st
			start, end, ok := certLifetimeWindow(&st)
			if start != c.start || end != c.end || ok != c.ok {
				t.Errorf("got (%d, %d, %v), want (%d, %d, %v)", start, end, ok, c.start, c.end, c.ok)
			}
		})
	}
}
