package main

import (
	"testing"
	"time"
)

// newTestApp returns an App with only the taskbar tracker wired, which is all
// the aggregation logic touches. applyTaskbarProgress is a no-op on the test
// platform (taskbar_other.go), so these exercise the shared half.
func newTestApp() *App {
	return &App{taskbar: newTaskbarTracker()}
}

// TestTaskbarAggregatesTransfers is the property the whole file exists for:
// one bar, many transfers, and the bar shows their sum rather than whichever
// one reported last.
func TestTaskbarAggregatesTransfers(t *testing.T) {
	a := newTestApp()
	tr := a.taskbar

	a.transferProgress("a", 50, 100)
	if tr.pct != 50 || tr.state != taskbarNormal {
		t.Fatalf("one transfer: pct=%d state=%v, want 50/normal", tr.pct, tr.state)
	}

	// A second transfer at 0/300 drags the aggregate down to 50/400.
	tr.lastAt = time.Time{} // defeat the rate limiter for the assertion
	a.transferProgress("b", 0, 300)
	if tr.pct != 12 {
		t.Fatalf("two transfers: pct=%d, want 12 (50 of 400)", tr.pct)
	}

	tr.lastAt = time.Time{}
	a.transferFinished("a", false)
	if tr.pct != 0 || tr.state != taskbarNormal {
		t.Fatalf("after one finishes: pct=%d state=%v, want 0/normal (b still running)",
			tr.pct, tr.state)
	}

	a.transferFinished("b", false)
	if tr.state != taskbarNoProgress {
		t.Fatalf("after the last finishes: state=%v, want no-progress", tr.state)
	}
}

// TestTaskbarUnknownTotalIsIndeterminate covers a directory transfer that is
// still walking the tree: reporting a percentage off a partial denominator
// would make the bar run backwards as more files are discovered.
func TestTaskbarUnknownTotalIsIndeterminate(t *testing.T) {
	a := newTestApp()
	tr := a.taskbar

	a.transferProgress("a", 10, 100)
	tr.lastAt = time.Time{}
	a.transferProgress("dir", 0, 0)
	if tr.state != taskbarIndeterminate {
		t.Fatalf("state=%v, want indeterminate while a total is unknown", tr.state)
	}

	tr.lastAt = time.Time{}
	a.transferProgress("dir", 0, 900)
	if tr.state != taskbarNormal {
		t.Fatalf("state=%v, want normal once the total is known", tr.state)
	}
}

// TestTaskbarRateLimit checks that a stream of tiny updates does not turn into
// a stream of UI-thread calls - the reason this indirection exists at all.
func TestTaskbarRateLimit(t *testing.T) {
	tr := newTaskbarTracker()
	tr.live["a"] = taskbarSnapshot{done: 1, total: 1000}
	tr.state = taskbarNormal
	tr.pct = 0
	tr.lastAt = time.Now()

	// Same state, percentage moved, but well inside the interval: suppressed.
	if push, _, _ := tr.shouldPushLocked(1, taskbarNormal, false); push {
		t.Error("update inside the rate-limit window was not suppressed")
	}
	// Unchanged percentage: suppressed regardless of timing.
	tr.lastAt = time.Time{}
	if push, _, _ := tr.shouldPushLocked(0, taskbarNormal, false); push {
		t.Error("unchanged percentage should not push")
	}
	// A state change ignores the rate limit.
	tr.lastAt = time.Now()
	if push, _, _ := tr.shouldPushLocked(0, taskbarError, false); !push {
		t.Error("state change must push even inside the rate-limit window")
	}
}

// TestTaskbarFinishAlwaysPushes pins the bug the force flag exists to prevent:
// the final update landing inside the rate-limit window and leaving the bar
// stuck just short of full.
func TestTaskbarFinishAlwaysPushes(t *testing.T) {
	tr := newTaskbarTracker()
	tr.live["a"] = taskbarSnapshot{done: 97, total: 100}
	tr.state, tr.pct, tr.lastAt = taskbarNormal, 97, time.Now()

	delete(tr.live, "a")
	pct, state := tr.aggregateLocked()
	push, _, _ := tr.shouldPushLocked(pct, state, true)
	if !push {
		t.Fatal("finish must push through the rate limiter")
	}
	if tr.state != taskbarNoProgress {
		t.Fatalf("state=%v, want no-progress after the last transfer", tr.state)
	}
}

// TestTaskbarFailureGoesRed covers the background-failure case: nothing else
// running, so the bar turns red rather than silently clearing.
func TestTaskbarFailureGoesRed(t *testing.T) {
	a := newTestApp()
	tr := a.taskbar

	a.transferProgress("a", 10, 100)
	a.transferFinished("a", true)
	if tr.state != taskbarError {
		t.Fatalf("state=%v, want error after a failed transfer", tr.state)
	}
	if tr.errorTimer == nil {
		t.Fatal("expected a timer to clear the error state")
	}
	tr.errorTimer.Stop()

	// A new transfer takes the bar back over immediately.
	tr.lastAt = time.Time{}
	a.transferProgress("b", 5, 10)
	if tr.state != taskbarNormal {
		t.Fatalf("state=%v, want normal once a new transfer starts", tr.state)
	}
	if tr.errorTimer != nil {
		t.Error("a new transfer must cancel the pending error-clear timer")
	}
}

// TestTaskbarFailureWithOthersRunningKeepsBar checks that one failure among
// several transfers does not paint the whole bar red - the others are fine.
func TestTaskbarFailureWithOthersRunningKeepsBar(t *testing.T) {
	a := newTestApp()
	tr := a.taskbar

	a.transferProgress("a", 10, 100)
	tr.lastAt = time.Time{}
	a.transferProgress("b", 20, 100)
	a.transferFinished("a", true)

	if tr.state != taskbarNormal {
		t.Fatalf("state=%v, want normal while another transfer is live", tr.state)
	}
}
