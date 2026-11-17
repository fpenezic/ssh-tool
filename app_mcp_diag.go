//go:build !android && !ios

package main

// Diagnostics for tool calls whose arguments do not fit the tool's input
// schema. One MCP client (Claude Desktop) was reported to lose the
// arguments of tools with a required parameter: run and read_terminal
// said session_id was missing although the model sent it, while the same
// calls through another client worked. What actually arrived is not
// visible from the model's side, so a call refused at schema validation
// logs the shape of what it got (argument keys and JSON types, never the
// values: a command or a typed text can hold a secret) and names the keys
// it received in the error the model sees, so the user can paste either.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// argsShape describes raw tool arguments without their values:
// `{"command":string, "session_id":string}`, or what they were instead
// of an object.
func argsShape(raw json.RawMessage) string {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "no arguments"
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return fmt.Sprintf("a JSON string of %d bytes instead of an object", len(s))
		}
		return "not a JSON object"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%q:%s", k, jsonKind(m[k])))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func jsonKind(v json.RawMessage) string {
	t := strings.TrimSpace(string(v))
	switch {
	case t == "":
		return "empty"
	case t == "null":
		return "null"
	case t[0] == '"':
		return "string"
	case t[0] == '{':
		return "object"
	case t[0] == '[':
		return "array"
	case t == "true" || t == "false":
		return "bool"
	}
	return "number"
}

// mcpArgsDiag is receiving middleware: a tools/call refused because its
// arguments do not match the schema is logged with the arguments' shape,
// and the shape is appended to the error the client gets.
func mcpArgsDiag(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if method != "tools/call" || err != nil {
			return res, err
		}
		cr, ok := res.(*mcp.CallToolResult)
		call, ok2 := req.(*mcp.CallToolRequest)
		if !ok || !ok2 || cr == nil || !cr.IsError || call.Params == nil || len(cr.Content) == 0 {
			return res, err
		}
		tc, ok := cr.Content[0].(*mcp.TextContent)
		if !ok || !strings.HasPrefix(tc.Text, `validating "arguments"`) {
			return res, err
		}
		shape := argsShape(call.Params.Arguments)
		client := ""
		if s := call.Session; s != nil {
			if p := s.InitializeParams(); p != nil && p.ClientInfo != nil {
				client = p.ClientInfo.Name + " " + p.ClientInfo.Version
			}
		}
		log.Printf("mcp: %s refused (%s), client %q sent %s", call.Params.Name, tc.Text, client, shape)
		tc.Text += " (received: " + shape + ")"
		return res, err
	}
}
