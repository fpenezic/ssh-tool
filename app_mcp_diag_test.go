//go:build !android && !ios

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestArgsShape(t *testing.T) {
	for raw, want := range map[string]string{
		``:                                 "no arguments",
		`null`:                             "no arguments",
		`{"session_id":"x","max_bytes":5}`: `{"max_bytes":number, "session_id":string}`,
		`"{\"session_id\":\"x\"}"`:         "a JSON string of 18 bytes instead of an object",
		`[1]`:                              "not a JSON object",
		`{"sessionId":"x","command":null,"o":{}}`: `{"command":null, "o":object, "sessionId":string}`,
	} {
		if got := argsShape(json.RawMessage(raw)); got != want {
			t.Errorf("argsShape(%s) = %s, want %s", raw, got, want)
		}
	}
}

// A call refused at schema validation names what arrived (keys and types,
// never values) in the error the client gets.
func TestMcpArgsDiagNamesWhatArrived(t *testing.T) {
	a := &App{}
	server := a.buildMcpServer()
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run", Arguments: map[string]any{"sessionId": "s1", "command": "secret-value"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("want a refused call, got %+v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `received: {"command":string, "sessionId":string}`) {
		t.Fatalf("error does not name what arrived: %s", text)
	}
	if strings.Contains(text, "secret-value") {
		t.Fatalf("error leaks an argument value: %s", text)
	}
}

// The log line never carries the validator's text: a type mismatch quotes
// the offending value, which can be a secret.
func TestMcpArgsDiagLogHasNoValues(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	a := &App{}
	server := a.buildMcpServer()
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run", Arguments: map[string]any{"session_id": "s1", "command": []any{"secret-value"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("want a refused call, got %+v", res)
	}
	out := buf.String()
	if !strings.Contains(out, "mcp: run refused") {
		t.Fatalf("no refusal logged: %q", out)
	}
	if strings.Contains(out, "secret-value") {
		t.Fatalf("log leaks an argument value: %s", out)
	}
}
