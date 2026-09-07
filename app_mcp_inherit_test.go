//go:build !android && !ios

package main

import "testing"

// The case from a real session: two connections staged into one new folder,
// both carrying the same credential. That is exactly what belongs on the
// folder, so the approval modal should say so.
func TestRepeatedSettingsFlagsSharedCredential(t *testing.T) {
	got := repeatedSettings([]planConn{
		{Name: "prod", Folder: planRef{Temp: "f1"}, AuthRef: "cred-a"},
		{Name: "test", Folder: planRef{Temp: "f1"}, AuthRef: "cred-a"},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "credential" || got[0].ID != "cred-a" || got[0].Count != 2 {
		t.Errorf("unexpected finding: %+v", got[0])
	}
}

// A network profile (VPN) repeats for the same reason and gets the same
// treatment.
func TestRepeatedSettingsFlagsSharedProfile(t *testing.T) {
	got := repeatedSettings([]planConn{
		{Folder: planRef{Existing: "F"}, NetworkProfileID: "np-1"},
		{Folder: planRef{Existing: "F"}, NetworkProfileID: "np-1"},
		{Folder: planRef{Existing: "F"}, NetworkProfileID: "np-1"},
	})
	if len(got) != 1 || got[0].Kind != "profile" || got[0].Count != 3 {
		t.Fatalf("want one profile finding over 3 connections, got %+v", got)
	}
}

// Not unanimous: the two that differ are the point of the folder, and
// warning here would train the user to ignore the warnings that matter.
func TestRepeatedSettingsIgnoresPartialOverlap(t *testing.T) {
	got := repeatedSettings([]planConn{
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-a"},
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-a"},
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-b"},
	})
	if len(got) != 0 {
		t.Errorf("partial overlap must not warn, got %+v", got)
	}
}

// Same credential, different folders: nothing to hoist, because there is no
// single folder that could hold it.
func TestRepeatedSettingsIgnoresAcrossFolders(t *testing.T) {
	got := repeatedSettings([]planConn{
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-a"},
		{Folder: planRef{Temp: "f2"}, AuthRef: "cred-a"},
	})
	if len(got) != 0 {
		t.Errorf("different folders must not warn, got %+v", got)
	}
}

// A single connection carrying a credential is just a connection.
func TestRepeatedSettingsIgnoresSingleton(t *testing.T) {
	got := repeatedSettings([]planConn{
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-a"},
	})
	if len(got) != 0 {
		t.Errorf("a lone connection must not warn, got %+v", got)
	}
}

// Connections that already inherit (no credential set) are the good case and
// must stay silent.
func TestRepeatedSettingsSilentWhenInheriting(t *testing.T) {
	got := repeatedSettings([]planConn{
		{Folder: planRef{Temp: "f1"}},
		{Folder: planRef{Temp: "f1"}},
	})
	if len(got) != 0 {
		t.Errorf("already-inheriting connections must not warn, got %+v", got)
	}
}

// Both kinds repeat at once: report each, in a stable order.
func TestRepeatedSettingsStableOrder(t *testing.T) {
	conns := []planConn{
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-a", NetworkProfileID: "np-1"},
		{Folder: planRef{Temp: "f1"}, AuthRef: "cred-a", NetworkProfileID: "np-1"},
	}
	first := repeatedSettings(conns)
	if len(first) != 2 {
		t.Fatalf("want 2 findings, got %+v", first)
	}
	if first[0].Kind != "credential" || first[1].Kind != "profile" {
		t.Errorf("credential should precede profile: %+v", first)
	}
	// Repeated runs must not reorder - the findings become user-facing text.
	for i := 0; i < 20; i++ {
		again := repeatedSettings(conns)
		for j := range again {
			if again[j] != first[j] {
				t.Fatalf("unstable order on run %d: %+v vs %+v", i, again, first)
			}
		}
	}
}
