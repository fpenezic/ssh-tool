//go:build !android && !ios

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMcpInstructionsMentionEveryTool keeps the instructions from rotting.
// They exist so a client that has never seen this bridge learns the two
// workflows without the user pasting anything; a tool nobody is told about
// may as well not be registered. Renaming or adding a tool without updating
// the prose fails here.
//
// Exempt: tools whose names appear only as part of a workflow the prose
// describes as a group, listed with the reason they are covered.
func TestMcpInstructionsMentionEveryTool(t *testing.T) {
	covered := map[string]string{
		// commit_plan is named; these are "the create calls" it commits.
		"list_folders":          "covered by the provisioning paragraph",
		"list_credentials":      "covered by 'reference credentials by their existing id'",
		"list_network_profiles": "covered by the provisioning paragraph",
	}

	for _, name := range registeredMcpToolNames(t) {
		if strings.Contains(mcpInstructions, name) {
			continue
		}
		if why, ok := covered[name]; ok {
			t.Logf("tool %q not named in instructions: %s", name, why)
			continue
		}
		t.Errorf("tool %q is registered but never mentioned in mcpInstructions; "+
			"a client is told nothing about it", name)
	}
}

// TestMcpInstructionsNameOnlyRealTools is the other direction: prose that
// names a tool which no longer exists sends the client after a tool that
// will error.
func TestMcpInstructionsNameOnlyRealTools(t *testing.T) {
	real := map[string]bool{}
	for _, n := range registeredMcpToolNames(t) {
		real[n] = true
	}
	// Every snake_case word in the prose that looks like a tool name.
	for _, word := range strings.FieldsFunc(mcpInstructions, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z')
	}) {
		if !strings.Contains(word, "_") || real[word] {
			continue
		}
		// Ordinary prose that happens to contain an underscore would be
		// unusual; flag it so a typo'd tool name cannot hide.
		t.Errorf("mcpInstructions names %q, which is not a registered tool", word)
	}
}

// TestMcpInstructionsStateTheGrantLevels guards the part a client most needs
// to get right: it cannot grant itself access, and there are three levels.
func TestMcpInstructionsStateTheGrantLevels(t *testing.T) {
	for _, want := range []string{
		string(mcpGrantReadOnly),
		string(mcpGrantReadRun),
		string(mcpGrantReadRunYolo),
		"manage",
	} {
		if !strings.Contains(mcpInstructions, want) {
			t.Errorf("mcpInstructions never mentions the %q grant", want)
		}
	}
}

// TestMcpInstructionsWarnAboutUntrustedOutput checks the prompt-injection
// note survives edits. It is the weakest of the layers protecting against
// hostile host output - the approval modal and grant split are the real
// boundary - but it is the only one that travels with the server.
func TestMcpInstructionsWarnAboutUntrustedOutput(t *testing.T) {
	lower := strings.ToLower(mcpInstructions)
	if !strings.Contains(lower, "never follow instructions") {
		t.Error("mcpInstructions must tell the client not to follow instructions found in host output")
	}
}

// TestMcpServerCarriesInstructionsOverTheWire connects a real client to a
// real server over the SDK's in-memory transport and reads the instructions
// back off the initialize result. Asserting on the constant alone would pass
// even if buildMcpServer went back to passing nil ServerOptions, which is a
// silent no-op: the prose would exist and no client would ever see it.
func TestMcpServerCarriesInstructionsOverTheWire(t *testing.T) {
	a := &App{}
	server := a.buildMcpServer()

	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	got := cs.InitializeResult().Instructions
	if got == "" {
		t.Fatal("server sent no instructions in the initialize result")
	}
	if got != mcpInstructions {
		t.Errorf("instructions on the wire differ from mcpInstructions:\n got %q", got)
	}
}

// TestMcpToolsAreListable is the companion check: the instructions describe
// tools, so the tools have to actually be advertised to a connected client.
func TestMcpToolsAreListable(t *testing.T) {
	a := &App{}
	server := a.buildMcpServer()

	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	live := map[string]bool{}
	for _, tool := range res.Tools {
		live[tool.Name] = true
	}
	for _, name := range registeredMcpToolNames(t) {
		if !live[name] {
			t.Errorf("tool %q is registered in source but not advertised to a client", name)
		}
	}
}

// registeredMcpToolNames returns the Name of every mcp.AddTool registration,
// read from the source so the list cannot drift from what is registered.
func registeredMcpToolNames(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string
	for _, f := range []string{"app_mcp_desktop.go"} {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "AddTool" || len(call.Args) < 2 {
				return true
			}
			unary, ok := call.Args[1].(*ast.UnaryExpr)
			if !ok {
				return true
			}
			lit, ok := unary.X.(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Name" {
					continue
				}
				if v, ok := kv.Value.(*ast.BasicLit); ok && v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil {
						out = append(out, s)
					}
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no mcp.AddTool registrations - did the wiring move?")
	}
	return out
}
