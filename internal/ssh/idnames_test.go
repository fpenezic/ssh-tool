package ssh

import (
	"strings"
	"testing"
)

func TestParseIDFilePasswd(t *testing.T) {
	const passwd = `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
filip:x:1000:1000:Filip,,,:/home/filip:/bin/bash

# a comment line
broken-line-without-enough-fields
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
badid:x:notanumber:5::/:/bin/false
`
	got := parseIDFile(strings.NewReader(passwd), 2)

	want := map[int64]string{
		0:     "root",
		1:     "daemon",
		1000:  "filip",
		65534: "nobody",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for id, name := range want {
		if got[id] != name {
			t.Errorf("uid %d: got %q, want %q", id, got[id], name)
		}
	}
}

func TestParseIDFileGroup(t *testing.T) {
	const group = `root:x:0:
sudo:x:27:filip,other
filip:x:1000:
`
	got := parseIDFile(strings.NewReader(group), 2)
	if got[27] != "sudo" {
		t.Errorf("gid 27: got %q, want sudo", got[27])
	}
	if got[1000] != "filip" {
		t.Errorf("gid 1000: got %q, want filip", got[1000])
	}
}

// A duplicate id must keep the first definition, matching getpwuid() on a
// file with aliases - otherwise "root" could be shadowed by a later entry.
func TestParseIDFileFirstWins(t *testing.T) {
	const passwd = `root:x:0:0::/root:/bin/sh
toor:x:0:0::/root:/bin/sh
`
	got := parseIDFile(strings.NewReader(passwd), 2)
	if got[0] != "root" {
		t.Errorf("uid 0: got %q, want root (first definition)", got[0])
	}
}

func TestParseIDFileEmpty(t *testing.T) {
	got := parseIDFile(strings.NewReader(""), 2)
	if len(got) != 0 {
		t.Errorf("empty input: got %v, want no entries", got)
	}
	if got == nil {
		t.Error("empty input must still return a non-nil map")
	}
}

// A file that is all garbage must not panic and must yield nothing.
func TestParseIDFileGarbage(t *testing.T) {
	const junk = "\x00\x01binary\nnot:a:passwd\n::::\n"
	got := parseIDFile(strings.NewReader(junk), 2)
	if len(got) != 0 {
		t.Errorf("garbage input: got %v, want no entries", got)
	}
}
