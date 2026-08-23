package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
)

func newBroadcastTestApp() *App {
	return &App{
		broadcastGroups: map[string]map[string]bool{
			"": {},
		},
	}
}

func TestEvictFromBroadcastGroupsRemovesFromEveryGroup(t *testing.T) {
	a := newBroadcastTestApp()
	a.broadcastGroups[""] = map[string]bool{"s1": true, "s2": true}
	a.broadcastGroups["ops"] = map[string]bool{"s1": true, "s3": true}

	a.evictFromBroadcastGroups("s1")

	if a.broadcastGroups[""]["s1"] {
		t.Fatal("s1 still in the default group")
	}
	if a.broadcastGroups["ops"]["s1"] {
		t.Fatal("s1 still in the ops group")
	}
	if !a.broadcastGroups[""]["s2"] || !a.broadcastGroups["ops"]["s3"] {
		t.Fatal("eviction removed a session it was not asked to remove")
	}
}

func TestEvictFromBroadcastGroupsIsIdempotent(t *testing.T) {
	a := newBroadcastTestApp()
	a.broadcastGroups[""] = map[string]bool{"s1": true}

	a.evictFromBroadcastGroups("s1")
	a.evictFromBroadcastGroups("s1")
	a.evictFromBroadcastGroups("never-a-member")

	if len(a.broadcastGroups[""]) != 0 {
		t.Fatalf("default group should be empty, has %d", len(a.broadcastGroups[""]))
	}
}

func TestEvictFromBroadcastGroupsIsConcurrencySafe(t *testing.T) {
	// The close hooks fire from per-session goroutines; a broadcast of
	// Ctrl-D ends every member at once, so eviction races itself.
	a := newBroadcastTestApp()
	ids := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		id := "s" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		ids = append(ids, id)
		a.broadcastGroups[""][id] = true
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			a.evictFromBroadcastGroups(id)
		}(id)
	}
	wg.Wait()

	if len(a.broadcastGroups[""]) != 0 {
		t.Fatalf("group should be empty after evicting every member, has %d", len(a.broadcastGroups[""]))
	}
}

// TestEverySessionCloseHookEvictsFromBroadcast is the regression guard for
// the bug this helper was extracted for: the dynamic-inventory connect path
// registered a SetOnClose hook that did every other teardown step but never
// dropped the session from the broadcast groups, so ending 24 dynamic
// sessions with Ctrl-D left 24 ghost members behind ("24 of 1 selected").
//
// It parses app.go and asserts that every SetOnClose callback literal calls
// evictFromBroadcastGroups. A new close path that forgets it fails here
// rather than in the user's status bar.
func TestEverySessionCloseHookEvictsFromBroadcast(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "SetOnClose" || len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.FuncLit)
		if !ok {
			return true
		}
		found++
		if !callsEvict(lit) {
			t.Errorf("SetOnClose hook at %s does not call evictFromBroadcastGroups; "+
				"a session that closes while in a broadcast group would stay a ghost member",
				fset.Position(call.Pos()))
		}
		return true
	})

	if found == 0 {
		t.Fatal("found no SetOnClose hooks in app.go - did the close wiring move?")
	}
}

func callsEvict(lit *ast.FuncLit) bool {
	hit := false
	ast.Inspect(lit, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
			sel.Sel.Name == "evictFromBroadcastGroups" {
			hit = true
			return false
		}
		return true
	})
	return hit
}

// TestNoInlineBroadcastEvictionOutsideHelper keeps the eviction from being
// copy-pasted back into a close hook. Three near-identical inline blocks are
// what let one of them drift out of sync in the first place.
func TestNoInlineBroadcastEvictionOutsideHelper(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		name := fn.Name.Name
		// The helper itself, and the group-editing IPC, legitimately
		// delete from broadcastGroups.
		if name == "evictFromBroadcastGroups" || strings.HasPrefix(name, "Broadcast") {
			return false
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "delete" || len(call.Args) != 2 {
				return true
			}
			// delete(g, id) where g ranges over a.broadcastGroups.
			if rangesOverBroadcastGroups(fn) {
				t.Errorf("%s at %s deletes from a broadcast group inline; "+
					"call evictFromBroadcastGroups instead so every close path stays in sync",
					name, fset.Position(call.Pos()))
			}
			return true
		})
		return true
	})
}

func rangesOverBroadcastGroups(fn *ast.FuncDecl) bool {
	hit := false
	ast.Inspect(fn, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if sel, ok := rng.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "broadcastGroups" {
			hit = true
			return false
		}
		return true
	})
	return hit
}
