//go:build !android && !ios

package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"ssh-tool/internal/dbx"
	"ssh-tool/internal/store"
)

// mcpDbApp opens a Database tab, direct transport, on the server in
// DBTEST_PG (see internal/dbx/integration_test.go), not shared yet.
func mcpDbApp(t *testing.T, readOnly bool) (*App, string) {
	t.Helper()
	raw := os.Getenv("DBTEST_PG")
	if raw == "" {
		t.Skip("DBTEST_PG not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	a := newDbTestApp(t)
	a.mcp = newMcpState()
	a.ctx = context.Background()
	cred := dbTestCred(t, a, u.User.Username(), pw)
	on, off := true, false
	engine, addr, name := dbx.EnginePostgres, u.Host, strings.TrimPrefix(u.Path, "/")
	c, err := a.db.CreateConnection(store.NewConnection{Name: "pg", Hostname: "pg.example.com", Overrides: store.InheritableSettings{
		DbEnabled: &on, DbEngine: &engine, DbAddr: &addr, DbName: &name, DbUseTunnel: &off,
		DbCredentialRef: &cred, DbReadOnly: &readOnly,
	}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.DbOpen(c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.DbClose(info.ID) })
	return a, info.ID
}

func TestMcpDbNeedsShare(t *testing.T) {
	a, sid := mcpDbApp(t, false)
	if _, err := a.mcpDbQuery(mcpDbQueryArgs{SessionID: sid, SQL: "SELECT 1"}); err == nil {
		t.Fatal("unshared Database session readable")
	}
	if err := a.McpShareSession(sid, "read"); err != nil {
		t.Fatal(err)
	}
	g := a.McpListGrants()
	if len(g) != 1 || g[0].Kind != "db" || !strings.HasSuffix(g[0].Name, "(Database)") {
		t.Fatalf("grants = %+v", g)
	}
	a.DbClose(sid)
	if g := a.McpListGrants(); len(g) != 0 {
		t.Fatalf("grant outlived its tab: %+v", g)
	}
}

// db_query is read-only on a writable connection too; db_execute needs
// read-run, refuses transaction control, and runs.
func TestMcpDbQueryAndExecute(t *testing.T) {
	a, sid := mcpDbApp(t, false)
	if err := a.McpShareSession(sid, "read"); err != nil {
		t.Fatal(err)
	}
	out, err := a.mcpDbQuery(mcpDbQueryArgs{SessionID: sid, SQL: "SELECT 1 AS one, NULL AS nothing, 'a\tb' AS tab; DELETE FROM pg_class"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "one\tnothing\ttab\n1\tNULL\ta\\tb\n(1 row)") || !strings.Contains(out, "read-only connection: delete") {
		t.Fatalf("query output:\n%s", out)
	}
	// A write a keyword check cannot see is the server's to refuse.
	out, _ = a.mcpDbQuery(mcpDbQueryArgs{SessionID: sid, SQL: "CREATE TEMP SEQUENCE s; SELECT nextval('s')"})
	if !strings.Contains(out, "read-only connection: create") {
		t.Fatalf("create in db_query:\n%s", out)
	}
	if _, err := a.mcpDbExecute(mcpDbExecuteArgs{SessionID: sid, SQL: "CREATE TEMP TABLE t (a int)"}); err == nil || !strings.Contains(err.Error(), "read-run") {
		t.Fatalf("execute on a read share: %v", err)
	}
	if err := a.McpShareSession(sid, "read-run-yolo"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mcpDbExecute(mcpDbExecuteArgs{SessionID: sid, SQL: "BEGIN; CREATE TEMP TABLE t (a int)"}); err == nil || !strings.Contains(err.Error(), "transaction control") {
		t.Fatalf("BEGIN in db_execute: %v", err)
	}
	out, err = a.mcpDbExecute(mcpDbExecuteArgs{SessionID: sid, SQL: "CREATE TEMP TABLE mcp_t (a int); INSERT INTO mcp_t VALUES (1), (2)"})
	if err != nil || !strings.Contains(out, "INSERT 0 2") {
		t.Fatalf("execute: %q %v", out, err)
	}
	// Same session: the temp table is there for the next query.
	out, err = a.mcpDbQuery(mcpDbQueryArgs{SessionID: sid, SQL: "SELECT count(*) FROM mcp_t"})
	if err != nil || !strings.Contains(out, "count\n2\n") {
		t.Fatalf("query after execute: %q %v", out, err)
	}
}

func TestMcpDbExecuteReadOnlyConnection(t *testing.T) {
	a, sid := mcpDbApp(t, true)
	if err := a.McpShareSession(sid, "read-run-yolo"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mcpDbExecute(mcpDbExecuteArgs{SessionID: sid, SQL: "CREATE TEMP TABLE t (a int)"}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("execute on a read-only connection: %v", err)
	}
}

func TestFormatDbResultsBudget(t *testing.T) {
	v := strings.Repeat("x", 100)
	r := &dbx.Result{Statement: "SELECT", HasRows: true, Columns: []dbx.ResultColumn{{Name: "c"}}}
	for i := 0; i < 50; i++ {
		r.Rows = append(r.Rows, []*string{&v})
	}
	out := formatDbResults([]*dbx.Result{r}, 1000)
	if len(out) > 1200 || !strings.Contains(out, "more exist") || !strings.Contains(out, "cut at the byte budget") {
		t.Fatalf("budget not kept (%d bytes):\n%s", len(out), out)
	}
}
