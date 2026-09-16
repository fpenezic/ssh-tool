package main

import "testing"

// A forward configured with port 0 gets its port from the OS at start,
// so a bookmark cannot contain the number: it must carry {port} and be
// resolved against the live listener at click time.
func TestExpandForwardURLPort(t *testing.T) {
	got := expandForwardURL("http://127.0.0.1:{port}/grafana", "127.0.0.1", 54832)
	if want := "http://127.0.0.1:54832/grafana"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// 0.0.0.0 is a listen address, not a dialable one. A browser on this
// machine has to be sent to loopback instead.
func TestExpandForwardURLHostWildcard(t *testing.T) {
	got := expandForwardURL("http://{host}:{port}/", "0.0.0.0", 8080)
	if want := "http://127.0.0.1:8080/"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	got = expandForwardURL("http://{host}:{port}/", "", 8080)
	if want := "http://127.0.0.1:8080/"; got != want {
		t.Fatalf("empty addr: got %q want %q", got, want)
	}
	got = expandForwardURL("http://{host}:{port}/", "::", 8080)
	if want := "http://127.0.0.1:8080/"; got != want {
		t.Fatalf("v6 wildcard: got %q want %q", got, want)
	}
}

// An explicit bind address is what the user chose to dial; keep it.
func TestExpandForwardURLHostExplicit(t *testing.T) {
	got := expandForwardURL("http://{host}:{port}/", "192.168.1.5", 9090)
	if want := "http://192.168.1.5:9090/"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Bookmarks saved before placeholders existed must keep working
// untouched - including ones whose URL happens to contain no brace.
func TestExpandForwardURLLeavesPlainURLsAlone(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:8080/grafana",
		"https://example.com",
		"",
	} {
		if got := expandForwardURL(u, "127.0.0.1", 54832); got != u {
			t.Fatalf("plain URL rewritten: %q -> %q", u, got)
		}
	}
}

// Several occurrences resolve, not just the first.
func TestExpandForwardURLRepeated(t *testing.T) {
	got := expandForwardURL("http://{host}:{port}/r?to={host}:{port}", "127.0.0.1", 1234)
	if want := "http://127.0.0.1:1234/r?to=127.0.0.1:1234"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
