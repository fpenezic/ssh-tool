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

// Claude Desktop's local agent mode drops an argument named session_id:
// the session tools take "session", and the old name still works. A call
// naming neither gets past the schema and is answered "session is required".
func TestMcpSessionParamNames(t *testing.T) {
	a := &App{mcp: newMcpState()}
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
	call := func(args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String()
	}
	for _, args := range []map[string]any{
		{"session": "s1", "command": "uptime"},
		{"session_id": "s1", "command": "uptime"},
	} {
		// Both reach the handler, which finds no such shared session.
		if got := call(args); !strings.Contains(got, "not shared for running commands") {
			t.Fatalf("%v: want the handler's answer, got %q", args, got)
		}
	}
	if got := call(map[string]any{"command": "uptime"}); !strings.Contains(got, "session is required") {
		t.Fatalf("no session: got %q", got)
	}
	// The schema advertises "session" and requires neither name.
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name != "run" {
			continue
		}
		raw, _ := json.Marshal(tl.InputSchema)
		s := string(raw)
		if !strings.Contains(s, `"session"`) || strings.Contains(s, `"required":["session`) || strings.Contains(s, `"session_id"]`) {
			t.Fatalf("run schema: %s", s)
		}
	}
}
