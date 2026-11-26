package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestProfileBusyOwner(t *testing.T) {
	busy := fmt.Errorf("%spid-1|pc-1", errProfileBusyPrefix)
	cases := map[string]struct {
		err  error
		want string
	}{
		"plain":   {busy, "pc-1"},
		"wrapped": {fmt.Errorf("token request: direct dial failed (timeout) and tunnel failed: %w", busy), "pc-1"},
		"trailer": {fmt.Errorf("via tunnel wg: %w (direct dial failed too: timeout)", busy), "pc-1"},
		"other":   {errors.New("dial tcp: i/o timeout"), ""},
		"nil":     {nil, ""},
	}
	for name, c := range cases {
		if got := profileBusyOwner(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
