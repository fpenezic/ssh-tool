package main

// Profile sync and catalogs across app versions (store/shadow.go has the
// data side). Two things live here:
//
//   - stagePrepare: the PrepareStore step for restart-time restores, which
//     never lets a snapshot that cannot carry catalog data replace it.
//   - The repush marker. After a pull from a snapshot an app without
//     catalog support pushed, this machine kept its catalog data and the
//     remote lacks it. Pushing once puts it back for this user's other
//     up-to-date machines; the older app ignores it. The marker is
//     machine-local (local-state.json), never the reason for a push when
//     there is no catalog data here, and dropped as soon as the remote
//     has moved on, so it can never cause a push conflict.

import (
	"log"

	"ssh-tool/internal/backup"
	"ssh-tool/internal/store"
	"ssh-tool/internal/syncer"
)

const catalogRepushKey = "sync_catalog_repush_v1"

func init() { localStateKeys[catalogRepushKey] = true }

func (a *App) catalogRepushPending() bool {
	v, ok := a.localSettingGet(catalogRepushKey)
	return ok && v != ""
}

// setCatalogRepush records the generation the marker belongs to: the one
// just pulled. on=false clears it.
func (a *App) setCatalogRepush(on bool) {
	if !on {
		_ = a.localSettingDelete(catalogRepushKey)
		return
	}
	_ = a.localSettingSet(catalogRepushKey, "1")
}

// stagePrepare returns the PrepareStore step for one restart-time restore.
// A sync pull that had to merge (the snapshot could not carry this
// machine's catalog data) sets the repush marker, which survives the
// restart.
func (a *App) stagePrepare(trust store.ShadowTrust) backup.PrepareStore {
	return func(path string) (string, func(), error) {
		out, cleanup, err := a.db.PrepareStagedStore(path, trust)
		if err == nil && trust.FromSync && out != path {
			a.setCatalogRepush(true)
		}
		return out, cleanup, err
	}
}

// catalogRepushTick runs the marker on a clean tick. Returns true when it
// handled the tick (pushed or tried to).
func (a *App) catalogRepushTick() bool {
	pending := a.catalogRepushPending()
	if !pending {
		return false
	}
	remoteGen := int64(-1)
	if dav, _, err := a.syncClient(); err == nil {
		if meta, err := syncer.FetchMeta(dav); err == nil {
			a.noteRemoteMeta(meta)
			remoteGen = meta.Generation
		} else if err == syncer.ErrNotFound {
			remoteGen = 0
		}
		dav.Close()
	}
	push, drop := syncer.CatalogRepushAction(pending, a.db.HasCatalogData(), remoteGen, a.syncGeneration())
	if drop {
		a.setCatalogRepush(false)
		return false
	}
	if !push {
		return false
	}
	res, err := a.SyncPush(false)
	if err != nil {
		log.Printf("auto-sync: catalog repush: %v", err)
		if syncer.IsGenerationConflict(err) {
			// Someone pushed in between: the marker must not turn
			// into a conflict. Drop it; the remote-ahead path takes
			// over. Any other error (offline) keeps it for later.
			a.setCatalogRepush(false)
			a.autoSyncRemoteCheck()
		}
		return true
	}
	log.Printf("auto-sync: pushed generation %d to restore catalog data an older app dropped", res.Generation)
	EventsEmit("sync_auto_pushed", res.Generation)
	return true
}
