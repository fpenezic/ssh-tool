package recorder

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	m := NewManager()
	path := filepath.Join(t.TempDir(), "rec.cast")

	got, err := m.Start("s1", path, 120, 30, "host - test")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if got != path {
		t.Fatalf("path mismatch: %s", got)
	}
	if _, err := m.Start("s1", path+"2", 80, 24, ""); err == nil {
		t.Fatalf("double start should fail")
	}

	m.Write("s1", []byte("hello\r\n"))
	m.Resize("s1", 100, 40)
	m.Write("s1", []byte{0xff, 0xfe, 'o', 'k'}) // invalid UTF-8 must not break the file
	m.Write("nope", []byte("ignored"))          // not recording - no-op

	p, ok := m.Stop("s1")
	if !ok || p != path {
		t.Fatalf("stop: ok=%v path=%s", ok, p)
	}
	if _, ok := m.Stop("s1"); ok {
		t.Fatalf("second stop should be a no-op")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)

	if !sc.Scan() {
		t.Fatalf("missing header line")
	}
	var h map[string]any
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		t.Fatalf("header not JSON: %v", err)
	}
	if h["version"].(float64) != 2 || h["width"].(float64) != 120 || h["height"].(float64) != 30 {
		t.Fatalf("bad header: %v", h)
	}

	var events [][3]any
	for sc.Scan() {
		var ev [3]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("event not JSON: %v (%s)", err, sc.Text())
		}
		events = append(events, ev)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	if events[0][1] != "o" || events[0][2] != "hello\r\n" {
		t.Fatalf("bad output event: %v", events[0])
	}
	if events[1][1] != "r" || events[1][2] != "100x40" {
		t.Fatalf("bad resize event: %v", events[1])
	}
	if events[2][1] != "o" {
		t.Fatalf("bad third event: %v", events[2])
	}
	// Timestamps monotonic non-decreasing.
	if events[1][0].(float64) < events[0][0].(float64) {
		t.Fatalf("timestamps went backwards")
	}
}

func TestStartRefusesExistingFile(t *testing.T) {
	m := NewManager()
	path := filepath.Join(t.TempDir(), "rec.cast")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("s1", path, 80, 24, ""); err == nil {
		t.Fatalf("start over existing file should fail (O_EXCL)")
	}
}

func TestSuggestedFilename(t *testing.T) {
	ts := time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC)
	cases := map[string]string{
		"web-01":           "web-01-20260611-103000.cast",
		"db / prod (eu)":   "db-prod-eu-20260611-103000.cast",
		"../../etc/passwd": "etc-passwd-20260611-103000.cast",
		"":                 "session-20260611-103000.cast",
	}
	for in, want := range cases {
		if got := SuggestedFilename(in, ts); got != want {
			t.Errorf("SuggestedFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadInfoAndListDir(t *testing.T) {
	dir := t.TempDir()
	m := NewManager()
	path := filepath.Join(dir, "a.cast")
	if _, err := m.Start("s1", path, 100, 30, "host-a"); err != nil {
		t.Fatal(err)
	}
	m.Write("s1", []byte("one"))
	m.Resize("s1", 90, 28)
	m.Write("s1", []byte("two"))
	m.Stop("s1")

	info, err := ReadInfo(path)
	if err != nil {
		t.Fatalf("readinfo: %v", err)
	}
	if info.Width != 100 || info.Height != 30 || info.Title != "host-a" {
		t.Fatalf("bad header info: %+v", info)
	}
	if info.Duration <= 0 {
		t.Fatalf("duration should be > 0, got %v", info.Duration)
	}
	if info.Size <= 0 || info.Name != "a.cast" {
		t.Fatalf("bad stat info: %+v", info)
	}

	// Non-cast files and subdirs are skipped; list sorts newest first.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, err := ListDir(dir)
	if err != nil {
		t.Fatalf("listdir: %v", err)
	}
	if len(list) != 1 || list[0].Name != "a.cast" {
		t.Fatalf("want only a.cast, got %+v", list)
	}

	// Missing dir is an empty list, not an error.
	empty, err := ListDir(filepath.Join(dir, "missing"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing dir: %v %v", empty, err)
	}
}
