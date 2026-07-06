package main

// Auto-sync engine for the WebDAV profile sync.
//
// Push-on-change: a 60s ticker compares a content fingerprint of the
// profile (store.ContentFingerprint + vault mtime, see syncFingerprint)
// against the fingerprint stamped at the last successful push. After a
// quiet period the profile is pushed silently. The same ticker drives
// the periodic remote check (every N minutes, configurable) so a
// laptop waking from standby learns within a tick that another machine
// pushed while it slept - the frontend gets a "sync_remote_ahead"
// event and shows a pull hint. Pull itself stays manual: applying a
// snapshot requires a restart and silently scheduling that is not
// acceptable.

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ssh-tool/internal/backup"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
	"ssh-tool/internal/syncer"
)

const (
	// autoSyncQuiet is how long the data must sit unchanged before a
	// dirty profile is pushed - batches a burst of edits into one
	// snapshot instead of uploading on every keystroke of a rename.
	autoSyncQuiet = 90 * time.Second
	// autoSyncTickEvery drives both the dirty check and (counted) the
	// remote check. Also bounds the post-standby detection latency.
	autoSyncTickEvery = 60 * time.Second
)

func (a *App) syncAutoEnabled() bool {
	v, ok, _ := a.db.GetSetting("sync_auto")
	return ok && v == "1"
}

func (a *App) syncCheckMinutes() int {
	if v, ok, _ := a.db.GetSetting("sync_check_minutes"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 5
}

// syncAutoApplyEnabled gates automatic background pull. Off by default:
// a pull replaces the whole profile, so applying it unattended is opt
// in. Even when on, the backend only fires when local is clean and the
// frontend confirms the UI is idle (nothing being edited).
func (a *App) syncAutoApplyEnabled() bool {
	v, ok, _ := a.db.GetSetting("sync_auto_apply")
	return ok && v == "1"
}

// syncFingerprint is the change signal: a content signature of the
// profile tables (store.ContentFingerprint - stable across restarts,
// moves only on a real committed mutation) joined with the vault
// file's mtime (vault.enc is a plain file, rewritten only on a real
// secret change, never checkpointed). Earlier attempts keyed off
// process/file state (mtime, data_version) and either never settled
// or re-fired on every launch; see ContentFingerprint for the why.
// syncFingerprintFormat tags the fingerprint string. Bump it whenever
// the body composition changes (new table, different field) so an
// upgrade re-baselines silently instead of pushing an unchanged
// profile. The body after the "|" is the content; the tag before it
// is metadata only.
const syncFingerprintFormat = "fp3"

func (a *App) syncFingerprint() string {
	return syncFingerprintFormat + "|" + a.syncFingerprintBodyNow()
}

func (a *App) syncFingerprintBodyNow() string {
	var vaultM int64
	if st, err := os.Stat(creds.DefaultPath()); err == nil {
		vaultM = st.ModTime().UnixNano()
	}
	return a.db.ContentFingerprint() + "vault=" + strconv.FormatInt(vaultM, 10)
}

// syncFingerprintBody returns the content portion of a fingerprint
// string (everything after the format tag), so two fingerprints can
// be compared for content equality regardless of format version.
func syncFingerprintBody(fp string) string {
	if i := strings.IndexByte(fp, '|'); i >= 0 {
		return fp[i+1:]
	}
	return fp // pre-tag stamp (older build) - whole string is the body
}

// syncDirty reports whether the profile changed since the last push.
func (a *App) syncDirty() bool {
	return a.syncFingerprint() != a.syncPushedFingerprint()
}

func syncStampPath() string {
	return filepath.Join(filepath.Dir(store.DefaultPath()), "sync-pushed.stamp")
}

func (a *App) syncPushedFingerprint() string {
	if b, err := os.ReadFile(syncStampPath()); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// recordSyncPushedFingerprint stamps the current fingerprint after a
// successful push. app_settings is excluded from the fingerprint,
// so push's own sync_generation / sync_last_at writes don't re-dirty
// the profile; the stamp simply records the post-push content state.
func (a *App) recordSyncPushedFingerprint() {
	writeSyncStamp(a.syncFingerprint())
}

func writeSyncStamp(fp string) {
	_ = os.WriteFile(syncStampPath(), []byte(fp), 0o600)
}

// startAutoSync launches the background loop. Called from initialise
// once db + vault exist; the loop itself stays dormant until the
// sync_auto setting turns on.
func (a *App) startAutoSync() {
	a.reconcileSyncStampOnStart()
	go func() {
		startup := time.After(10 * time.Second)
		tick := time.NewTicker(autoSyncTickEvery)
		defer tick.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-startup:
				a.autoSyncRemoteCheck()
			case <-tick.C:
				a.autoSyncTick()
			}
		}
	}()
}

// reconcileSyncStampOnStart prevents a spurious push on the first
// launch after an upgrade that changed the fingerprint FORMAT (a new
// table in the signature, a different separator). The stamp carries a
// format version (see fingerprint format below); when only that
// version differs from the running binary's, the content is unchanged
// and the new binary just re-stamps in the new format - no push, so
// the other machine never sees a phantom update.
//
// A genuine content difference (offline edit) is preserved: it shows
// up as a different content body, not just a different format tag, and
// the normal dirty path pushes it.
func (a *App) reconcileSyncStampOnStart() {
	if a == nil || a.db == nil {
		return
	}
	stamped := a.syncPushedFingerprint()
	if stamped == "" {
		return // never synced
	}
	cur := a.syncFingerprint()
	if cur == stamped {
		return // exact match, nothing to do
	}
	// Compare content bodies with the format tag stripped. If only the
	// tag changed, re-baseline silently; if the body changed too, it's
	// a real edit - leave the stamp so the dirty path pushes it.
	//
	// fp3 added the network_profiles segment to the body. On an upgrade
	// from fp2 a machine with NO profiles gains only an empty
	// "network_profiles=0;" segment - that is a format change, not new
	// content, so canonicalise it out of both sides before comparing.
	// A machine that HAS profiles carries "network_profiles=N/T;" (N>0),
	// which does NOT cancel out, so its body genuinely differs and the
	// dirty path pushes it - which is exactly what finally ships the
	// profiles to the other machine.
	if canonSyncBody(syncFingerprintBody(cur)) == canonSyncBody(syncFingerprintBody(stamped)) {
		writeSyncStamp(cur)
		log.Printf("auto-sync: re-baselined stamp (fingerprint format change, no content push)")
	}
}

// canonSyncBody removes segments that carry no content when empty, so a
// pure format upgrade (a new zero-valued segment) compares equal to the
// old body. Only the empty network_profiles segment is stripped; a
// populated one (network_profiles=N/T with N>0) is real content and
// stays.
func canonSyncBody(body string) string {
	return strings.Replace(body, "network_profiles=0;", "", 1)
}

// autoSyncReady gates every background action: feature on, URL set,
// vault unlocked (the WebDAV password and passphrase live in it),
// and no staged pull waiting for a restart - the pull's own
// bookkeeping dirties the OLD profile, and pushing that would
// overwrite the snapshot the user just pulled.
func (a *App) autoSyncReady() bool {
	if a == nil || a.db == nil || !a.syncAutoEnabled() {
		return false
	}
	// Transport must be configured: a WebDAV URL, or an SFTP host. The full
	// validity (credential, dir, ...) is checked when syncClient builds the
	// transport; this is just the "is anything set up" gate.
	transport, _, _ := a.db.GetSetting("sync_transport")
	if transport == "sftp" {
		if v, ok, _ := a.db.GetSetting("sync_sftp_host"); !ok || v == "" {
			return false
		}
	} else {
		if v, ok, _ := a.db.GetSetting("sync_webdav_url"); !ok || v == "" {
			return false
		}
	}
	if a.syncPendingRestore() {
		return false
	}
	return a.vault.Status().Kind == creds.StatusUnlocked
}

// syncPendingRestore reports whether a pulled snapshot is staged and
// waiting for the next start.
func (a *App) syncPendingRestore() bool {
	ready := filepath.Join(filepath.Dir(store.DefaultPath()), backup.PendingDir, "READY")
	_, err := os.Stat(ready)
	return err == nil
}

func (a *App) autoSyncTick() {
	if !a.autoSyncReady() {
		return
	}
	// Dirty check via the content fingerprint (see syncFingerprint).
	// The quiet period batches a burst of edits into one push: we
	// remember the fingerprint at the moment it first went dirty and
	// only push once it has stopped changing for autoSyncQuiet. A new
	// edit mid-wait resets the timer.
	fp := a.syncFingerprint()
	if fp == a.syncPushedFingerprint() {
		// Clean - clear any pending dirty marker, then maybe poll.
		a.syncDirtyFP = ""
		if time.Since(a.syncLastRemoteCheck) >= time.Duration(a.syncCheckMinutes())*time.Minute {
			a.autoSyncRemoteCheck()
		}
		return
	}
	// Dirty. Track when this exact state settled.
	if fp != a.syncDirtyFP {
		a.syncDirtyFP = fp
		a.syncDirtySince = time.Now()
		return // wait for quiet
	}
	if time.Since(a.syncDirtySince) < autoSyncQuiet {
		return
	}
	if res, err := a.SyncPush(false); err != nil {
		// Generation guard = both sides changed; surface it as a
		// remote-ahead situation for the user to resolve. Other
		// errors (network, server) just log - next tick retries.
		log.Printf("auto-sync push: %v", err)
		a.autoSyncRemoteCheck()
	} else {
		log.Printf("auto-sync: pushed generation %d", res.Generation)
		EventsEmit("sync_auto_pushed", res.Generation)
	}
	a.syncDirtyFP = ""
	a.syncLastRemoteCheck = time.Now()
}

// autoSyncRemoteCheck fetches the remote meta and notifies the
// frontend once per new generation when the remote is ahead.
func (a *App) autoSyncRemoteCheck() {
	if !a.autoSyncReady() {
		return
	}
	a.syncLastRemoteCheck = time.Now()
	dav, _, err := a.syncClient()
	if err != nil {
		return
	}
	defer dav.Close()
	meta, err := syncer.FetchMeta(dav)
	if err != nil {
		if err != syncer.ErrNotFound {
			log.Printf("auto-sync check: %v", err)
		}
		return
	}
	if meta.Generation <= a.syncGeneration() {
		return
	}
	if meta.Generation == a.syncNotifiedGen {
		return // already told the user about this one
	}
	a.syncNotifiedGen = meta.Generation
	log.Printf("auto-sync: remote generation %d ahead of local %d (device %s)",
		meta.Generation, a.syncGeneration(), meta.Device)

	// Auto-apply path: when enabled and local is clean (no unsynced
	// edits to conflict with), ask the frontend to apply it. The
	// frontend gates on UI idleness (nothing being edited, no modal
	// open) and calls SyncPullLive itself, so the backend never yanks
	// the profile out from under an open editor. Falls through to the
	// normal notification - if the UI isn't idle, the pill/toast still
	// lets the user pull manually.
	autoApply := a.syncAutoApplyEnabled() && !a.syncDirty()
	EventsEmit("sync_remote_ahead", map[string]any{
		"generation": meta.Generation,
		"device":     meta.Device,
		"updated_at": meta.UpdatedAt,
		"auto_apply": autoApply,
	})
}

// syncFlushOnQuit pushes a dirty profile on the way out, best-effort
// with a hard cap so a dead network can't hold the quit hostage.
// Skipped when the remote is ahead - quitting is not the moment to
// resolve a conflict.
func (a *App) syncFlushOnQuit() {
	if !a.autoSyncReady() {
		return
	}
	if !a.syncDirty() {
		return // clean - nothing to flush
	}
	dav, phrase, err := a.syncClient()
	if err != nil {
		return
	}
	defer dav.Close()
	// syncClient now returns a transport-agnostic syncer.Transport; the
	// WebDAV impl carries its own default timeout and SFTP its own, so there
	// is no per-call http client to tune here (the old quit-path 10s
	// override only applied to WebDAV).
	prevGen := a.syncGeneration()
	res, err := syncer.Push(dav, store.DefaultPath(), creds.DefaultPath(), phrase,
		appVersion, syncer.DefaultDevice(), prevGen, false,
		func(gen int64) error {
			return a.db.SetSetting("sync_generation", strconv.FormatInt(gen, 10))
		})
	if err != nil {
		_ = a.db.SetSetting("sync_generation", strconv.FormatInt(prevGen, 10))
		log.Printf("sync flush on quit: %v", err)
		return
	}
	_ = a.db.SetSetting("sync_last_at", strconv.FormatInt(time.Now().Unix(), 10))
	a.recordSyncPushedFingerprint()
	a.recordAudit("sync.push", "", map[string]string{
		"generation": strconv.FormatInt(res.Generation, 10),
		"trigger":    "quit",
	})
	log.Printf("sync flush on quit: pushed generation %d", res.Generation)
}
