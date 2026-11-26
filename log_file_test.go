package main

import (
	"bytes"
	"errors"
	"testing"
)

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, errors.New("the handle is invalid") }

// A Windows GUI build has no valid stderr: the log file still gets
// every line.
func TestLogTeeSkipsFailingWriter(t *testing.T) {
	var file bytes.Buffer
	n, err := logTee{failWriter{}, &file}.Write([]byte("line\n"))
	if err != nil || n != 5 || file.String() != "line\n" {
		t.Fatalf("n=%d err=%v file=%q", n, err, file.String())
	}
}
