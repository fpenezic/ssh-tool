package share

import (
	"bytes"
	"testing"
)

func TestOutputFrameRoundTrip(t *testing.T) {
	cases := []struct {
		sid  string
		cum  uint64
		data []byte
	}{
		{"s1", 0, []byte("hello")},
		{"s12", 184320, []byte{0x1b, '[', '3', '1', 'm'}}, // an ANSI sequence
		{"s3", 1 << 40, nil},                              // empty payload (size-only-ish)
		{"session-with-a-longer-slot", 42, bytes.Repeat([]byte("x"), 9000)},
	}
	for _, c := range cases {
		frame := MarshalOutput(c.sid, c.cum, c.data)
		sid, cum, data, err := ParseOutput(frame)
		if err != nil {
			t.Fatalf("ParseOutput(%q): %v", c.sid, err)
		}
		if sid != c.sid {
			t.Errorf("sid = %q, want %q", sid, c.sid)
		}
		if cum != c.cum {
			t.Errorf("cum = %d, want %d", cum, c.cum)
		}
		if !bytes.Equal(data, c.data) {
			t.Errorf("data = %v, want %v", data, c.data)
		}
	}
}

func TestParseOutputRejectsGarbage(t *testing.T) {
	bad := [][]byte{
		nil,
		{},
		{0x02, 0, 0},                     // wrong kind byte
		{outputFrameKind},                // too short for the header
		{outputFrameKind, 0, 5, 's'},     // claims sid len 5 but only 1 byte
		{outputFrameKind, 0, 0, 0, 0, 0}, // sid len 0 but no room for cum
	}
	for i, b := range bad {
		if _, _, _, err := ParseOutput(b); err == nil {
			t.Errorf("case %d: expected error for %v", i, b)
		}
	}
}

func TestOutputBinaryNotText(t *testing.T) {
	// The first byte is the kind discriminator, not JSON. A JSON parser would
	// choke, which is the point: output never goes through the JSON path.
	frame := MarshalOutput("s1", 1, []byte("x"))
	if frame[0] != outputFrameKind {
		t.Fatalf("first byte = %#x, want %#x", frame[0], outputFrameKind)
	}
}
