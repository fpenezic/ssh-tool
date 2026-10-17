package inventory

import (
	"testing"
	"time"
)

func TestNextRetryBacksOffToTheInterval(t *testing.T) {
	interval := 5 * time.Minute
	retry := failedRetryFirst
	var got []time.Duration
	for i := 0; i < 8; i++ {
		got = append(got, nextRetry(retry, interval))
		if retry < interval {
			retry *= 2
		}
	}
	want := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, interval, interval, interval}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("retry %d = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	// A short interval is never exceeded by the first retry either.
	if d := nextRetry(failedRetryFirst, 10*time.Second); d != 10*time.Second {
		t.Fatalf("short interval: %v", d)
	}
}
