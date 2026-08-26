package local

import (
	"regexp"
	"os"
	"testing"
)

// The local PTY pump's read size is load-bearing, not arbitrary.
//
// Wails beta.8 routes an event one of two ways depending on its JSON size
// (maxInlineEventPayload, 8192 bytes): larger payloads are parked host-side
// and fetched over HTTP, chained on window._wails.__eq so ordering is
// guaranteed; smaller ones are spliced into the JS-eval queue.
//
// A 4 KiB read base64-encodes to ~5.5 KB and took the inline path, while the
// SSH pump's 8 KiB read (~10.9 KB) took the fetch path - the only structural
// difference between the two, and it matched a report where a local tab lost
// scrollback after being backgrounded while SSH to the same host did not.
//
// If this ever needs to shrink, read gotcha 49 first and measure.
func TestLocalPumpReadSizeCrossesTheInlinePayloadLimit(t *testing.T) {
	src, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatalf("read local.go: %v", err)
	}
	m := regexp.MustCompile(`func \(s \*Session\) pumpOutput\(\)[\s\S]*?buf := make\(\[\]byte, (\d+)\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find the pump's read buffer size")
	}
	got := string(m[1])
	if got != "8192" {
		t.Errorf("pump reads %s bytes, want 8192 - a smaller read base64-encodes "+
			"below Wails' 8192-byte inline limit and takes the unchained "+
			"delivery path (see gotcha 49)", got)
	}
}

// base64 inflates by 4/3; the check above is only meaningful if 8192 bytes
// actually clears the limit once encoded.
func TestEightKiBClearsTheInlineLimitOnceEncoded(t *testing.T) {
	const raw = 8192
	const inlineLimit = 8192
	encoded := (raw + 2) / 3 * 4
	if encoded <= inlineLimit {
		t.Fatalf("%d raw bytes encode to %d, which does not clear the %d-byte "+
			"inline limit - the reasoning behind the read size is wrong",
			raw, encoded, inlineLimit)
	}
}
