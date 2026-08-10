// Aggregate file-transfer progress, surfaced on the OS taskbar button.
//
// Windows fills the taskbar icon behind the app the way Chrome does during a
// download. Everything below is the platform-independent half: track what is in
// flight, reduce it to one percentage, and rate-limit how often the platform
// layer hears about it. applyTaskbarProgress is the seam
// (taskbar_windows.go / taskbar_other.go).
//
// Two things this has to get right:
//
//   - Rate limiting is not optional. SftpDownload's progress callback fires
//     every 100 ms OR every 256 KB per transfer, whichever comes first, and the
//     Windows call has to be marshalled onto the UI thread. Forwarding every
//     tick would push UI-thread work proportional to transfer speed.
//   - There is ONE taskbar button and there can be many transfers. The bar
//     shows their sum, not whichever one reported last, and it only clears when
//     the last of them finishes.

package main

import (
	"sync"
	"time"
)

// taskbarState mirrors the Windows TBPFLAG values. The other platforms ignore
// them, but the vocabulary is the same everywhere so the shared code can talk
// about intent rather than about Windows.
type taskbarState uint32

const (
	taskbarNoProgress    taskbarState = 0x0
	taskbarIndeterminate taskbarState = 0x1
	taskbarNormal        taskbarState = 0x2
	taskbarError         taskbarState = 0x4
)

// taskbarMinInterval bounds how often the platform layer is touched.
const taskbarMinInterval = 100 * time.Millisecond

// taskbarErrorLinger is how long a failed transfer keeps the bar red once
// nothing is running anymore. Long enough to notice, short enough not to be
// mistaken for a stuck transfer.
const taskbarErrorLinger = 4 * time.Second

type taskbarSnapshot struct {
	done  int64
	total int64
}

// taskbarTracker holds the live transfer set and the last thing we told the OS.
type taskbarTracker struct {
	mu     sync.Mutex
	live   map[string]taskbarSnapshot
	pct    int
	state  taskbarState
	lastAt time.Time
	// errorTimer clears a lingering error state. Held so a transfer that
	// starts while the red bar is showing can cancel it.
	errorTimer *time.Timer
}

func newTaskbarTracker() *taskbarTracker {
	return &taskbarTracker{live: map[string]taskbarSnapshot{}}
}

// transferProgress records one transfer's position and refreshes the taskbar.
// Called from the SFTP progress callbacks; safe from any goroutine.
func (a *App) transferProgress(transferID string, done, total int64) {
	t := a.taskbar
	if t == nil {
		return
	}
	t.mu.Lock()
	t.live[transferID] = taskbarSnapshot{done: done, total: total}
	if t.errorTimer != nil {
		t.errorTimer.Stop()
		t.errorTimer = nil
	}
	pct, state := t.aggregateLocked()
	push, p, s := t.shouldPushLocked(pct, state, false)
	t.mu.Unlock()

	if push {
		a.applyTaskbarProgress(p, s)
	}
}

// transferFinished drops a transfer from the aggregate. When it was the last
// one the bar clears - or goes red for a few seconds first if this transfer
// failed, so a failure that happens while the window is in the background
// still leaves a trace.
func (a *App) transferFinished(transferID string, failed bool) {
	t := a.taskbar
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.live, transferID)
	pct, state := t.aggregateLocked()
	if len(t.live) == 0 && failed {
		state = taskbarError
		pct = 100
		if t.errorTimer != nil {
			t.errorTimer.Stop()
		}
		t.errorTimer = time.AfterFunc(taskbarErrorLinger, func() {
			t.mu.Lock()
			// Another transfer may have started in the meantime; that one owns
			// the bar now and this timer has nothing to clear.
			stillIdle := len(t.live) == 0 && t.state == taskbarError
			t.errorTimer = nil
			t.mu.Unlock()
			if stillIdle {
				a.setTaskbarIdle()
			}
		})
	}
	// A finish always pushes: the transition to 100% or to cleared must not be
	// swallowed by the rate limiter, or the bar sticks at 97% forever.
	_, p, s := t.shouldPushLocked(pct, state, true)
	t.mu.Unlock()

	a.applyTaskbarProgress(p, s)
}

// setTaskbarIdle clears the bar unconditionally.
func (a *App) setTaskbarIdle() {
	t := a.taskbar
	if t == nil {
		return
	}
	t.mu.Lock()
	t.pct, t.state, t.lastAt = 0, taskbarNoProgress, time.Now()
	t.mu.Unlock()
	a.applyTaskbarProgress(0, taskbarNoProgress)
}

// aggregateLocked reduces every live transfer to one percentage. A transfer
// whose total isn't known yet (a directory transfer still walking the tree)
// makes the whole thing indeterminate rather than reporting a percentage
// computed from a partial denominator, which would run backwards as the walk
// discovers more files.
func (t *taskbarTracker) aggregateLocked() (int, taskbarState) {
	if len(t.live) == 0 {
		return 0, taskbarNoProgress
	}
	var done, total int64
	for _, s := range t.live {
		if s.total <= 0 {
			return 0, taskbarIndeterminate
		}
		done += s.done
		total += s.total
	}
	if total <= 0 {
		return 0, taskbarIndeterminate
	}
	pct := int(done * 100 / total)
	if pct > 100 {
		pct = 100
	}
	return pct, taskbarNormal
}

// shouldPushLocked applies the rate limit. A state change or a forced push
// always goes through; otherwise the percentage must have actually moved and
// the minimum interval must have elapsed. Returns the values to send.
func (t *taskbarTracker) shouldPushLocked(pct int, state taskbarState, force bool) (bool, int, taskbarState) {
	changed := state != t.state || pct != t.pct
	if !force {
		if !changed {
			return false, pct, state
		}
		if state == t.state && time.Since(t.lastAt) < taskbarMinInterval {
			return false, pct, state
		}
	}
	t.pct, t.state, t.lastAt = pct, state, time.Now()
	return true, pct, state
}
