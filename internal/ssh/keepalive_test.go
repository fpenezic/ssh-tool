package ssh

import (
	"testing"
	"time"
)

func TestKeepaliveInterval(t *testing.T) {
	cases := []struct {
		name    string
		setting uint32
		want    time.Duration
	}{
		// 0 is the user saying "don't hold the link open with traffic".
		// The probe still has to run - it is the only thing that can see a
		// dead chain - so it falls back to the slow detection-only tick.
		{"off falls back to the idle probe", 0, keepaliveIdleProbe},
		{"explicit 15s", 15, 15 * time.Second},
		{"explicit 60s", 60, 60 * time.Second},
		// A hand-typed 1 is floored: pointless traffic, and it would leave
		// no room for a probe timeout shorter than the tick.
		{"1s is floored", 1, keepaliveMinInterval},
		{"4s is floored", 4, keepaliveMinInterval},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keepaliveInterval(c.setting); got != c.want {
				t.Fatalf("keepaliveInterval(%d) = %s, want %s", c.setting, got, c.want)
			}
		})
	}
}

func TestKeepaliveProbeTimeout(t *testing.T) {
	cases := []struct {
		name  string
		every time.Duration
		want  time.Duration
	}{
		// Half the interval, so a probe can never still be in flight when
		// the next tick fires.
		{"half the interval", 20 * time.Second, 10 * time.Second},
		{"the floored minimum still halves", keepaliveMinInterval, keepaliveMinInterval / 2},
		{"capped on a long interval", 10 * time.Minute, 30 * time.Second},
		{"the idle probe lands on the cap", keepaliveIdleProbe, 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keepaliveProbeTimeout(c.every); got != c.want {
				t.Fatalf("keepaliveProbeTimeout(%s) = %s, want %s", c.every, got, c.want)
			}
		})
	}
}

// The timeout must always be strictly less than the tick, or a slow probe
// would still be outstanding when the next one starts.
func TestKeepaliveProbeTimeoutIsShorterThanInterval(t *testing.T) {
	for _, setting := range []uint32{0, 1, 5, 15, 30, 60, 300, 3600} {
		every := keepaliveInterval(setting)
		if to := keepaliveProbeTimeout(every); to >= every {
			t.Fatalf("setting=%d: probe timeout %s >= interval %s", setting, to, every)
		}
	}
}
