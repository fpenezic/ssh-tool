package main

import "testing"

func TestOpenableURL(t *testing.T) {
	for in, want := range map[string]bool{
		"https://example.com/x":       true,
		"HTTP://example.com":          true,
		"mailto:someone@example.com":  true,
		"file:///C:/Windows/system32": false,
		"ms-settings:":                false,
		"search-ms:query=x":           false,
		"javascript:alert(1)":         false,
		"vbscript:x":                  false,
		"data:text/html,x":            false,
		"https://":                    false,
		"\\\\server\\share\\x.exe":    false,
		"C:\\Windows\\notepad.exe":    false,
	} {
		if got := openableURL(in); got != want {
			t.Errorf("openableURL(%q) = %v, want %v", in, got, want)
		}
	}
}
