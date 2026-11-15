package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ssh-tool/internal/syncer"
)

func TestAppWithoutCatalogs(t *testing.T) {
	for v, want := range map[string]bool{"v1.2.1": true, "1.2.9-rc1": true, "v0.118.0": true, "v1.3.0": false,
		"v1.3.0-rc1": false, "v2.0.0": false, "dev": false, "": false, "abc1234-dirty": false} {
		if got := appWithoutCatalogs(v); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

// Observed metas: old machines are listed for the publish dialog, this
// machine never is, and a newer remote generation makes catalog passes wait.
func TestSeenAppsAndCatalogWait(t *testing.T) {
	saved := localStateStore
	localStateStore = &localState{path: filepath.Join(t.TempDir(), "local-state.json")}
	t.Cleanup(func() { localStateStore = saved; remoteGenSeen.Store(0) })
	remoteGenSeen.Store(0)
	ca := newCatalogApp(t)
	a := ca.a

	a.noteRemoteMeta(&syncer.Meta{Generation: 3, Device: "LAPTOP", AppVersion: "v1.2.1"})
	a.noteRemoteMeta(&syncer.Meta{Generation: 4, Device: "DESK", AppVersion: "v1.3.0"})
	a.noteRemoteMeta(&syncer.Meta{Generation: 5, Device: syncer.DefaultDevice(), AppVersion: "v1.0.0"})
	old := a.CatalogPublishOldApps()
	if len(old) != 1 || old[0].Device != "LAPTOP" || old[0].Version != "v1.2.1" {
		t.Fatalf("old apps: %+v", old)
	}
	// Updated since: no longer listed.
	a.noteRemoteMeta(&syncer.Meta{Generation: 6, Device: "LAPTOP", AppVersion: "v1.3.0"})
	if old := a.CatalogPublishOldApps(); len(old) != 0 {
		t.Fatalf("updated machine still listed: %+v", old)
	}

	// Remote generation 6, local 0: the catalog waits, without touching
	// the network.
	if !a.profileRemoteAhead() {
		t.Fatal("remote ahead not seen")
	}
	_ = (vaultTokens{a: a, id: ca.cat.ID}).Save("rt")
	err := a.catalogSyncNow(ca.cat.ID, true)
	if err == nil || !strings.Contains(err.Error(), "pulled") || ca.tokenRefused != 0 {
		t.Fatalf("catalog did not wait: %v (refresh attempts %d)", err, ca.tokenRefused)
	}
	list, _ := a.CatalogList()
	if list[0].State != catStateWaiting {
		t.Fatalf("state %s", list[0].State)
	}
	// After the pull it runs again.
	if err := a.db.SetSetting(a.syncGenerationKey(""), "6"); err != nil {
		t.Fatal(err)
	}
	a.catalogAfterProfilePull()
	if err := a.catalogSyncNow(ca.cat.ID, true); errors.Is(err, nil) || strings.Contains(err.Error(), "pulled") {
		t.Fatalf("still waiting after the pull: %v", err)
	}
}
