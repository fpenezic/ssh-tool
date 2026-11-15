package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ssh-tool/internal/syncer"
)

// What this machine has seen of the other machines on its sync profile,
// from the meta.json files it already fetches (auto-sync polls, status,
// pulls): the remote generation, and each pushing device's app version.
// Machine-local (local-state.json), never synced, no format change.
//
// Two uses:
//   - a catalog pass waits while the profile remote is ahead of this
//     machine (catalogWaitsForProfile): syncing the catalog before
//     pulling a profile in which another machine of this user published
//     a folder would insert the published records next to the local
//     originals this machine still holds;
//   - the publish dialog names the machines last seen on an app without
//     catalog support (they stop showing a published folder).

const syncSeenAppsKey = "sync_seen_apps"

// remoteGenSeen is the highest remote generation observed this run.
var remoteGenSeen atomic.Int64

// SeenApp is one device as last seen in the profile's meta.
type SeenApp struct {
	Device   string `json:"device"`
	Version  string `json:"version"`
	LastSeen int64  `json:"last_seen"`
}

// noteRemoteMeta records an observed meta.json.
func (a *App) noteRemoteMeta(m *syncer.Meta) {
	if m == nil {
		return
	}
	for {
		cur := remoteGenSeen.Load()
		if m.Generation <= cur || remoteGenSeen.CompareAndSwap(cur, m.Generation) {
			break
		}
	}
	if m.Device == "" || m.Device == syncer.DefaultDevice() {
		return
	}
	seen := a.seenApps()
	seen[m.Device] = SeenApp{Device: m.Device, Version: m.AppVersion, LastSeen: time.Now().Unix()}
	b, err := json.Marshal(seen)
	if err == nil {
		_ = a.localSettingSet(syncSeenAppsKey, string(b))
	}
}

func (a *App) seenApps() map[string]SeenApp {
	out := map[string]SeenApp{}
	if v, ok := a.localSettingGet(syncSeenAppsKey); ok {
		_ = json.Unmarshal([]byte(v), &out)
	}
	return out
}

// profileRemoteAhead: a newer profile generation was seen on the remote
// than this machine has applied.
func (a *App) profileRemoteAhead() bool {
	return remoteGenSeen.Load() > a.syncGeneration()
}

// appWithoutCatalogs reports whether an app version predates catalog
// support (1.3). Development builds ("dev", a commit id) count as current.
func appWithoutCatalogs(v string) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(strings.SplitN(v, "-", 2)[0], ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major < 1 || (major == 1 && minor < 3)
}

// seenOldAppsWindow: evidence older than this is not shown.
const seenOldAppsWindow = 90 * 24 * time.Hour

// CatalogPublishOldApps lists the machines on this profile last seen on
// an app without catalog support. The publish dialog warns with it; a
// block would refuse CatalogPublish when it is not empty.
func (a *App) CatalogPublishOldApps() []SeenApp {
	out := []SeenApp{}
	cut := time.Now().Add(-seenOldAppsWindow).Unix()
	for _, s := range a.seenApps() {
		if s.LastSeen >= cut && appWithoutCatalogs(s.Version) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}
