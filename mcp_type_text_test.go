package main

import "testing"

func TestMcpTypeTextOK(t *testing.T) {
	ok := []string{"", "ls -la", "echo 'a\tb'", "grep -r \"x\" /etc", "čšž ünïcode"}
	for _, s := range ok {
		if err := mcpTypeTextOK(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	bad := []string{"id\r", "id\n", "a\r\nb", "\x15rm -rf /", "\x1b[200~x", "x\x7f", "x\u0085", "x\x9b", "\x00", "x\x03"}
	for _, s := range bad {
		if err := mcpTypeTextOK(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}
