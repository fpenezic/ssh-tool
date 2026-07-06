package tunnelhelper

import (
	"strings"
	"testing"
)

func TestCheckProtocol(t *testing.T) {
	cases := []struct {
		name    string
		v       int
		wantErr bool
		// wantMsg, when set, must appear in the error - so the message
		// points at the right side to update.
		wantMsg string
	}{
		{"in range", minProtocol, false, ""},
		{"max in range", maxProtocol, false, ""},
		// 0 = a pre-versioning helper (omitted field). Too old -> tell the
		// user to update the HELPER.
		{"missing field is too old", 0, true, "out of date"},
		{"below min", minProtocol - 1, true, "out of date"},
		// Newer than the app can drive -> tell the user to update the APP.
		{"above max", maxProtocol + 1, true, "newer than this app"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkProtocol(c.v)
			if c.wantErr && err == nil {
				t.Fatalf("checkProtocol(%d) = nil, want error", c.v)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("checkProtocol(%d) = %v, want nil", c.v, err)
			}
			if c.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), c.wantMsg)) {
				t.Fatalf("checkProtocol(%d) error = %v, want it to contain %q", c.v, err, c.wantMsg)
			}
		})
	}
}
