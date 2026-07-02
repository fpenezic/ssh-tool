package backup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// PassphraseProvider returns the live vault passphrase when one is
// recoverable without prompting the user (e.g. via the machine-bound
// sidecar). Returns ("", nil) when no passphrase is available; an
// error escapes only on hard failures.
type PassphraseProvider func() (string, error)

// SchedulerConfig captures everything the periodic worker needs.
type SchedulerConfig struct {
	Enabled        bool
	IntervalHours  int // gap between successful runs, default 24
	KeepLast       int // backups + pre-restore snapshots to retain
	StoreDBPath    string
	VaultEncPath   string
	AppVersion     string
	GetPassphrase  PassphraseProvider
	OnEvent        func(kind, msg string) // optional: kind = "run"|"skip"|"error"
}

// Scheduler runs auto-backups on a sat-vremeni tick. Live config is
// swapped atomically via SetConfig; the worker rereads it each tick.
type Scheduler struct {
	cfg    atomic.Pointer[SchedulerConfig]
	cancel context.CancelFunc
	done   chan struct{}
}

const (
	defaultIntervalHours = 24
	defaultKeepLast      = 7
	tickInterval         = time.Hour
)

// New constructs an idle Scheduler. Call Start to launch the goroutine.
func New() *Scheduler { return &Scheduler{} }

func (s *Scheduler) SetConfig(c SchedulerConfig) {
	if c.IntervalHours <= 0 {
		c.IntervalHours = defaultIntervalHours
	}
	if c.KeepLast <= 0 {
		c.KeepLast = defaultKeepLast
	}
	s.cfg.Store(&c)
}

func (s *Scheduler) Start() {
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.loop(ctx)
}

func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.done)
	// Tick once immediately, then hourly. The interval gate inside Run
	// keeps us from oversampling.
	s.runOnce()
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce()
		}
	}
}

func (s *Scheduler) runOnce() {
	cp := s.cfg.Load()
	if cp == nil {
		return
	}
	c := *cp
	if !c.Enabled {
		return
	}
	now := time.Now().UTC()
	dataDir := filepath.Dir(c.StoreDBPath)
	bDir := DefaultDir(dataDir)

	last := lastAutoBackupAt(bDir)
	gap := time.Duration(c.IntervalHours) * time.Hour
	if !last.IsZero() && now.Sub(last) < gap {
		return
	}

	pp, err := c.GetPassphrase()
	if err != nil {
		emit(c.OnEvent, "error", fmt.Sprintf("passphrase provider: %v", err))
		return
	}
	if pp == "" {
		emit(c.OnEvent, "skip", "vault locked and no sidecar - skipping auto-backup")
		return
	}

	dest := filepath.Join(bDir, autoBackupFilename(now))
	if err := Create(dest, c.StoreDBPath, c.VaultEncPath, pp, c.AppVersion); err != nil {
		emit(c.OnEvent, "error", fmt.Sprintf("auto-backup create: %v", err))
		return
	}
	emit(c.OnEvent, "run", "auto-backup created: "+filepath.Base(dest))

	if err := pruneOld(bDir, c.KeepLast); err != nil {
		emit(c.OnEvent, "error", fmt.Sprintf("prune: %v", err))
	}
}

func emit(fn func(kind, msg string), kind, msg string) {
	if fn != nil {
		fn(kind, msg)
		return
	}
	log.Printf("backup: %s: %s", kind, msg)
}

const autoBackupPrefix = "ssh-tool-auto-"

func autoBackupFilename(t time.Time) string {
	return fmt.Sprintf("%s%s%s", autoBackupPrefix, t.UTC().Format("20060102-150405"), backupExt)
}

func lastAutoBackupAt(bDir string) time.Time {
	entries, err := os.ReadDir(bDir)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), autoBackupPrefix) || !strings.HasSuffix(e.Name(), backupExt) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

// pruneOld keeps the newest `keep` auto-backups and the newest `keep`
// pre-restore safety snapshots, deleting the rest. Manual backups
// (anything that doesn't start with the auto prefix) are left alone.
func pruneOld(bDir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	if err := pruneFiles(bDir, autoBackupPrefix, backupExt, keep); err != nil {
		return err
	}
	return prunePreRestoreDirs(bDir, keep)
}

func pruneFiles(dir, prefix, suffix string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type item struct {
		name string
		mod  time.Time
	}
	var matches []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		matches = append(matches, item{e.Name(), fi.ModTime()})
	}
	if len(matches) <= keep {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].mod.After(matches[j].mod) })
	var firstErr error
	for _, m := range matches[keep:] {
		if err := os.Remove(filepath.Join(dir, m.name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func prunePreRestoreDirs(bDir string, keep int) error {
	entries, err := os.ReadDir(bDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type item struct {
		name string
		mod  time.Time
	}
	var matches []item
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), snapshotDir+"-") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		matches = append(matches, item{e.Name(), fi.ModTime()})
	}
	if len(matches) <= keep {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].mod.After(matches[j].mod) })
	var firstErr error
	for _, m := range matches[keep:] {
		if err := os.RemoveAll(filepath.Join(bDir, m.name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// silence unused-import warnings if errors helpers ever change.
var _ = errors.New
