//go:build !android && !ios

package main

import (
	"os"
	"strings"
	"testing"
)

// The tree walk must find this process. Anything less means the endpoint
// reports a total that silently omits part of the app.
func TestProcTreeIncludesSelf(t *testing.T) {
	procs, err := procTree()
	if err != nil {
		t.Fatalf("procTree: %v", err)
	}
	if len(procs) == 0 {
		t.Fatal("procTree returned nothing; it must at least find this process")
	}
	self := os.Getpid()
	var found bool
	for _, p := range procs {
		if p.PID == self {
			found = true
			if p.Name == "" {
				t.Error("own process has no name")
			}
		}
	}
	if !found {
		t.Fatalf("procTree did not include self (pid %d)", self)
	}
}

// Sorted loudest-first, so the process actually responsible is the first row
// rather than something to be found by scanning.
func TestProcTreeSortedByMemory(t *testing.T) {
	procs, err := procTree()
	if err != nil {
		t.Fatalf("procTree: %v", err)
	}
	for i := 1; i < len(procs); i++ {
		if procs[i-1].WorkingSet < procs[i].WorkingSet {
			t.Fatalf("row %d (%d bytes) sorts above row %d (%d bytes)",
				i-1, procs[i-1].WorkingSet, i, procs[i].WorkingSet)
		}
	}
}

// A PID must never appear twice: the total is a sum, so a duplicated child
// would inflate it.
func TestProcTreeHasNoDuplicates(t *testing.T) {
	procs, err := procTree()
	if err != nil {
		t.Fatalf("procTree: %v", err)
	}
	seen := map[int]bool{}
	for _, p := range procs {
		if seen[p.PID] {
			t.Fatalf("pid %d listed twice", p.PID)
		}
		seen[p.PID] = true
	}
}

func TestFormatProcTree(t *testing.T) {
	out := formatProcTree([]procInfo{
		{PID: 100, PPID: 1, Name: "ssh-tool.exe", WorkingSet: 50 * 1024 * 1024},
		{PID: 200, PPID: 100, Name: "msedgewebview2.exe", WorkingSet: 150 * 1024 * 1024},
	})
	for _, want := range []string{"ssh-tool.exe", "msedgewebview2.exe", "2 processes", "200.0 MB"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A long process name must not push the RSS column out of alignment - the
// whole point of the plain-text format is that it is readable in a terminal.
func TestFormatProcTreeTruncatesLongNames(t *testing.T) {
	out := formatProcTree([]procInfo{
		{PID: 1, PPID: 0, Name: strings.Repeat("x", 80), WorkingSet: 1024 * 1024},
	})
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 70 {
			t.Fatalf("line too wide (%d chars): %q", len(line), line)
		}
	}
}
