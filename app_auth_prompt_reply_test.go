package main

import (
	"testing"
	"time"
)

// A second reply to a prompt returns an error instead of blocking.
func TestAuthPromptSecondReply(t *testing.T) {
	a := &App{pendingAuthPrompts: map[string]chan authPromptReply{"p": make(chan authPromptReply, 1)}}
	if err := a.SshRespondAuthPrompt("p", []string{"x"}, false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.SshRespondAuthPrompt("p", nil, true) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second reply accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second reply blocked")
	}
}
