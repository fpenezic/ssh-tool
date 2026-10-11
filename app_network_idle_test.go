package main

import (
	"encoding/json"
	"testing"
)

// An edit rebuilds the config from a typed struct per kind; the shared
// idle_stop_minutes override must survive it, and its absence too.
func TestCarryIdleStop(t *testing.T) {
	got := carryIdleStop(`{"kind":"netbird","idle_stop_minutes":-1}`, `{"kind":"netbird","device_name":"x"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["idle_stop_minutes"] != float64(-1) || m["device_name"] != "x" {
		t.Fatalf("carried = %s", got)
	}
	if got := carryIdleStop(`{"kind":"netbird"}`, `{"kind":"netbird"}`); got != `{"kind":"netbird"}` {
		t.Fatalf("no override must stay absent, got %s", got)
	}
	if p := parsePolicy(`{"idle_stop_minutes":0}`); p.IdleStopMinutes == nil || *p.IdleStopMinutes != 0 {
		t.Fatalf("0 (stop at once) must parse as a set override, got %+v", p.IdleStopMinutes)
	}
}
