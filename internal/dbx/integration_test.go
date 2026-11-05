package dbx_test

// Integration tests against real servers, reached through an in-process SSH
// server the way the Database tab reaches them. They run only when the
// server is named in the environment:
//
//	DBTEST_PG=postgres://postgres:secret@127.0.0.1:5432/appdb
//	DBTEST_MYSQL=mysql://root:secret@127.0.0.1:3306/appdb
//	DBTEST_CH=clickhouse://app:secret@127.0.0.1:8123/appdb
//	DBTEST_CH_READER=clickhouse://reader:secret@127.0.0.1:8123/appdb (optional:
//	  a login whose profile has readonly = 1)
//	DBTEST_OS=opensearch://admin:secret@127.0.0.1:9200?tls=require
//	DBTEST_ES=opensearch://elastic:secret@127.0.0.1:9201 (Elasticsearch)
//	DBTEST_REDIS=redis://app:secret@127.0.0.1:6379 (uses db3 and db4, flushed)
//	DBTEST_REDIS_READER=redis://reader:secret@127.0.0.1:6379 (optional: an ACL
//	  user limited to reads)
//
// e.g. with docker:
//
//	docker run -d -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=appdb -p 127.0.0.1:5432:5432 postgres:17
//	docker run -d -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=appdb -p 127.0.0.1:3306:3306 mysql:8.4
//	docker run -d -e CLICKHOUSE_USER=app -e CLICKHOUSE_PASSWORD=secret -e CLICKHOUSE_DB=appdb -p 127.0.0.1:8123:8123 clickhouse/clickhouse-server:25.8
//	docker run -d -e discovery.type=single-node -e OPENSEARCH_INITIAL_ADMIN_PASSWORD=<strong> -p 127.0.0.1:9200:9200 opensearchproject/opensearch:2.19.3
//	docker run -d -e discovery.type=single-node -e ELASTIC_PASSWORD=secret -e xpack.security.http.ssl.enabled=false -p 127.0.0.1:9201:9200 docker.elastic.co/elasticsearch/elasticsearch:8.19.4
//	docker run -d -p 127.0.0.1:6379:6379 redis:7.4 redis-server --user default off --user app on '>secret' '~*' '&*' '+@all' --user reader on '>secret' '~*' '+@read' '+@connection' '-@dangerous'
//
// Each run creates and drops its own tables (dbx_it_*; indices on
// OpenSearch).

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ssh-tool/internal/dbx"
	"ssh-tool/internal/sshchan/sshchantest"
)

type itServer struct {
	engine string
	cfg    dbx.Config
	srv    *sshchantest.Server
	port   string
}

func itSetup(t *testing.T, engine string) *itServer {
	env := map[string]string{dbx.EnginePostgres: "DBTEST_PG", dbx.EngineMySQL: "DBTEST_MYSQL", dbx.EngineClickHouse: "DBTEST_CH"}[engine]
	return itSetupEnv(t, engine, env)
}

func itSetupEnv(t *testing.T, engine, env string) *itServer {
	raw := os.Getenv(env)
	if raw == "" {
		t.Skipf("%s not set", env)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(u.Host)
	pw, _ := u.User.Password()
	// The database host name only resolves on the SSH server.
	srv := sshchantest.Start(t, map[string]string{"db.internal": host})
	var connects atomic.Int32
	lazy := srv.Lazy(&connects)
	t.Cleanup(lazy.Close)
	return &itServer{
		engine: engine, srv: srv, port: port,
		cfg: dbx.Config{
			Engine: engine, Addr: "db.internal:" + port, Database: strings.TrimPrefix(u.Path, "/"),
			User: u.User.Username(), Password: pw, Dial: lazy.Dial, TLS: u.Query().Get("tls"),
		},
	}
}

func (s *itServer) open(t *testing.T, readOnly bool) *dbx.Handle {
	cfg := s.cfg
	cfg.ReadOnly = readOnly
	h, err := dbx.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

// itTimeout: a handle with a query timeout stops a long statement by
// itself, says so, and its session runs the next one.
func itTimeout(t *testing.T, s *itServer, database, long, after string) {
	t.Helper()
	cfg := s.cfg
	cfg.QueryTimeout = time.Second
	h, err := dbx.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	sess, err := h.OpenSession(ctx30(t), "timeout", database)
	if err != nil {
		t.Fatal(err)
	}
	// Bounded here too, so a timeout that never fires fails the test
	// instead of hanging it.
	rctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	r := sess.Run(rctx, long, dbx.RunOptions{})
	if time.Since(start) > 15*time.Second {
		t.Fatalf("the timeout did not stop %q", long)
	}
	if !strings.Contains(r[0].Error, "query timeout") {
		t.Fatalf("timed out statement: %+v", r[0])
	}
	mustRun(t, sess, after)
}

// itActivity: a long statement on another session shows in Activity, and
// Kill stops it (the statement, then the session). A server that keeps
// its sessions view behind a grant answers with that error (allowed names
// it), which is fine for a login without one.
func itActivity(t *testing.T, s *itServer, h *dbx.Handle, database, long, match, after, allowed string) {
	t.Helper()
	other, err := h.OpenSession(ctx30(t), "busy", database)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []*dbx.Result, 1)
	go func() { done <- other.Run(context.Background(), long, dbx.RunOptions{}) }()
	time.Sleep(1500 * time.Millisecond)
	act, err := h.Activity(ctx30(t))
	if err != nil {
		if allowed != "" && strings.Contains(err.Error(), allowed) {
			other.Cancel()
			<-done
			t.Logf("activity needs a grant here: %v", err)
			return
		}
		t.Fatal(err)
	}
	id := ""
	qi := len(act.Columns) - 1
	for _, r := range act.Rows {
		if r[qi] != nil && strings.Contains(strings.ToLower(*r[qi]), strings.ToLower(match)) {
			id = *r[0]
		}
	}
	if id == "" {
		other.Cancel()
		<-done
		t.Fatalf("activity does not show %q: %d rows", match, len(act.Rows))
	}
	if err := h.Kill(ctx30(t), id, false); err != nil {
		other.Cancel()
		<-done
		t.Fatalf("kill %s: %v", id, err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("kill did not stop the statement")
	}
	// Stopping the statement keeps the session where the engine can do
	// that (SQL Server and Db2 can only end it).
	if e := h.Engine(); e != dbx.EngineMSSQL && e != dbx.EngineDB2 {
		mustRun(t, other, after)
	}
	if err := h.Kill(ctx30(t), "1; DROP TABLE x", false); err == nil {
		t.Fatal("a malformed session id was accepted")
	}
}

func ctx30(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// mustRun runs a script and fails on any statement error.
func mustRun(t *testing.T, s *dbx.Session, script string) []*dbx.Result {
	t.Helper()
	res := s.Run(ctx30(t), script, dbx.RunOptions{MaxRows: 5000})
	for _, r := range res {
		if r.Error != "" {
			t.Fatalf("%s: %s", r.Statement, r.Error)
		}
	}
	return res
}

func TestIntegrationPostgres(t *testing.T) { itEngine(t, dbx.EnginePostgres) }
func TestIntegrationMySQL(t *testing.T)    { itEngine(t, dbx.EngineMySQL) }

func itEngine(t *testing.T, engine string) {
	s := itSetup(t, engine)
	h := s.open(t, false)
	ctx := ctx30(t)
	pg := engine == dbx.EnginePostgres
	table := fmt.Sprintf("dbx_it_%d", time.Now().UnixNano()%1e9)
	view := table + "_v"

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version == "" || info.Database != s.cfg.Database {
		t.Fatalf("info %+v", info)
	}
	tunneled := false
	for _, d := range s.srv.Dialed() {
		if d == "db.internal:"+s.port {
			tunneled = true
		}
	}
	if !tunneled {
		t.Fatalf("not dialed through ssh: %v", s.srv.Dialed())
	}

	sess, err := h.OpenSession(ctx, "q1", s.cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	ddl := fmt.Sprintf(`CREATE TABLE %s (id serial PRIMARY KEY, name text NOT NULL, price numeric(10,2), data bytea)`, table)
	fill := fmt.Sprintf(`INSERT INTO %s (name, price, data) SELECT 'item ' || g, g * 1.5, '\xdead'::bytea FROM generate_series(1, 1200) g`, table)
	if !pg {
		ddl = fmt.Sprintf("CREATE TABLE %s (id int AUTO_INCREMENT PRIMARY KEY, name varchar(50) NOT NULL, price decimal(10,2), data blob, KEY name_idx (name))", table)
		fill = fmt.Sprintf(`INSERT INTO %s (name, price, data)
			WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 1200)
			SELECT CONCAT('item ', n), n * 1.5, 0xdead FROM g`, table)
	}
	mustRun(t, sess, ddl)
	if !pg {
		// Both cap recursion at 1000, each under its own variable.
		sess.Run(ctx, "SET SESSION cte_max_recursion_depth = 5000", dbx.RunOptions{})
		sess.Run(ctx, "SET SESSION max_recursive_iterations = 5000", dbx.RunOptions{})
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cs, err := h.OpenSession(ctx, "cleanup", s.cfg.Database)
		if err == nil {
			cs.Run(ctx, "DROP VIEW IF EXISTS "+view+"; DROP TABLE IF EXISTS "+table, dbx.RunOptions{})
		}
	})
	res := mustRun(t, sess, fill+"; CREATE VIEW "+view+" AS SELECT id, name FROM "+table+" WHERE id <= 10")
	if res[0].RowsAffected != 1200 {
		t.Fatalf("insert affected %d", res[0].RowsAffected)
	}

	// Tree.
	dbs, err := h.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range dbs {
		found = found || d.Name == s.cfg.Database
	}
	if !found {
		t.Fatalf("databases %+v", dbs)
	}
	objs, err := h.Objects(ctx, s.cfg.Database, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	schema := ""
	for _, o := range objs {
		kinds[o.Name] = o.Kind
		if o.Name == table {
			schema = o.Schema
		}
	}
	if kinds[table] != dbx.KindTable || kinds[view] != dbx.KindView {
		t.Fatalf("objects: %v", kinds)
	}
	if pg && schema != "public" {
		t.Fatalf("schema %q", schema)
	}

	// Structure.
	d, err := h.Describe(ctx, s.cfg.Database, schema, table)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Columns) != 4 || d.Columns[0].Key != "PRI" || d.Columns[1].Nullable {
		t.Fatalf("columns %+v", d.Columns)
	}
	if len(d.Indexes) == 0 || !d.Indexes[0].Primary || d.Indexes[0].Columns[0] != "id" {
		t.Fatalf("indexes %+v", d.Indexes)
	}
	if !strings.Contains(d.DDL, "CREATE TABLE") || !strings.Contains(d.DDL, "price") {
		t.Fatalf("ddl %q", d.DDL)
	}
	if vd, err := h.Describe(ctx, s.cfg.Database, schema, view); err != nil || !strings.Contains(strings.ToUpper(vd.DDL), "VIEW") {
		t.Fatalf("view ddl %v %+v", err, vd)
	}

	// Browse pages in key order.
	page, err := h.Browse(ctx, s.cfg.Database, schema, table, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 50 || *page.Rows[0][0] != "101" || page.Truncated {
		t.Fatalf("page: %d rows, first %v", len(page.Rows), *page.Rows[0][0])
	}
	if *page.Rows[0][2] != "151.50" {
		t.Fatalf("numeric as text: %q", *page.Rows[0][2])
	}
	wantBin := map[bool]string{true: `\xdead`, false: "0xdead"}[pg]
	if *page.Rows[0][3] != wantBin {
		t.Fatalf("binary: %q, want %q", *page.Rows[0][3], wantBin)
	}

	itForeignKeys(t, h, sess, s.cfg.Database, schema, table)

	// A read past the bound stops, and the session still works after it.
	res = sess.Run(ctx, "SELECT * FROM "+table, dbx.RunOptions{MaxRows: 500})
	if res[0].Error != "" || len(res[0].Rows) != 500 || !res[0].Truncated {
		t.Fatalf("bounded read: err %q, %d rows, truncated %v", res[0].Error, len(res[0].Rows), res[0].Truncated)
	}
	res = mustRun(t, sess, "SELECT COUNT(*) FROM "+table+" WHERE price IS NULL")
	if *res[0].Rows[0][0] != "0" {
		t.Fatalf("after bounded read: %v", *res[0].Rows[0][0])
	}

	// A script: results per statement, stops at the first error.
	res = sess.Run(ctx, "UPDATE "+table+" SET price = NULL WHERE id <= 3; SELECT price FROM "+table+" WHERE id = 1; SELECT nope FROM "+table+"; SELECT 1", dbx.RunOptions{})
	if len(res) != 3 || res[0].RowsAffected != 3 || !res[0].Write || res[1].Rows[0][0] != nil || res[2].Error == "" {
		t.Fatalf("script: %d results, %+v", len(res), res)
	}

	// Postgres sessions keep a transaction across runs.
	if pg {
		mustRun(t, sess, "BEGIN")
		if !sess.InTx() {
			t.Fatal("not in a transaction after BEGIN")
		}
		mustRun(t, sess, "DELETE FROM "+table+"; ROLLBACK")
		if sess.InTx() {
			t.Fatal("still in a transaction")
		}
	}

	// Cancel stops a long statement and keeps the session.
	sleep := map[bool]string{true: "SELECT pg_sleep(30)", false: "SELECT SLEEP(30)"}[pg]
	itTimeout(t, s, s.cfg.Database, sleep, "SELECT 1")
	itActivity(t, s, h, s.cfg.Database, map[bool]string{true: "SELECT pg_sleep(20)", false: "SELECT SLEEP(20)"}[pg], "sleep(20)", "SELECT 1", "")
	done := make(chan []*dbx.Result)
	start := time.Now()
	go func() { done <- sess.Run(context.Background(), sleep, dbx.RunOptions{}) }()
	time.Sleep(500 * time.Millisecond)
	sess.Cancel()
	select {
	case r := <-done:
		// MySQL's SLEEP returns 1 when killed instead of failing.
		if pg && r[0].Error == "" {
			t.Fatalf("cancelled sleep: %+v", r[0])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the statement")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel too slow")
	}
	mustRun(t, sess, "SELECT 1")

	// Query tabs hold their own connections: four more tabs open, and the
	// tree and Stop still get through.
	for i := 0; i < 4; i++ {
		if _, err := h.OpenSession(ctx, fmt.Sprintf("extra%d", i), s.cfg.Database); err != nil {
			t.Fatal(err)
		}
	}
	quick, cancelQuick := context.WithTimeout(ctx, 5*time.Second)
	if _, err := h.Databases(quick); err != nil {
		t.Fatalf("tree with five tabs open: %v", err)
	}
	cancelQuick()
	go func() { done <- sess.Run(context.Background(), sleep, dbx.RunOptions{}) }()
	time.Sleep(500 * time.Millisecond)
	start = time.Now()
	sess.Cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel with five tabs open did not stop the statement")
	}

	// A caller's context ending stops the statement too (the LLM's calls).
	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	start = time.Now()
	sess.Run(short, sleep, dbx.RunOptions{})
	cancelShort()
	if time.Since(start) > 10*time.Second {
		t.Fatal("an ended context did not stop the statement")
	}
	mustRun(t, sess, "SELECT 1")

	// Read-only: refused before the server, and by the server.
	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "q", s.cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DELETE FROM " + table, "DROP TABLE " + table, "SET SESSION TRANSACTION READ WRITE",
		"SELECT 1; DELETE FROM " + table,
	} {
		r := rs.Run(ctx, stmt, dbx.RunOptions{})
		if last := r[len(r)-1]; !strings.Contains(last.Error, "read-only") {
			t.Fatalf("%q in read-only: %+v", stmt, last)
		}
	}
	// A write hidden in a read statement is the server's to refuse.
	sneaky := "WITH d AS (DELETE FROM " + table + " RETURNING id) SELECT COUNT(*) FROM d"
	if !pg {
		sneaky = "SELECT * FROM " + table + " FOR UPDATE"
	}
	if r := rs.Run(ctx, sneaky, dbx.RunOptions{}); pg && r[0].Error == "" {
		t.Fatalf("server let %q through: %+v", sneaky, r[0])
	}
	if !pg {
		if r := rs.Run(ctx, "SELECT * FROM "+table+" INTO OUTFILE '/tmp/x'", dbx.RunOptions{}); !strings.Contains(r[0].Error, "read-only") {
			t.Fatalf("outfile: %+v", r[0])
		}
	}
	res = mustRun(t, rs, "SELECT COUNT(*) FROM "+table)
	if *res[0].Rows[0][0] != "1200" {
		t.Fatalf("rows after read-only attempts: %s", *res[0].Rows[0][0])
	}
	// Sessions opened read-only stay so: the server refuses a write the
	// keyword check cannot see (a function call).
	if pg {
		if r := rs.Run(ctx, "SELECT nextval('"+table+"_id_seq')", dbx.RunOptions{}); !strings.Contains(r[0].Error, "read-only") {
			t.Fatalf("nextval in read-only: %+v", r[0])
		}
	}
}

// itForeignKeys: a child table's foreign key comes back with its columns
// and the referenced table named the way BrowseWhere takes it, and
// following it reads the referenced row (a bound value: one that looks
// like SQL matches nothing and errors nothing). The same SQL on every
// engine with foreign keys; names are compared without case (Oracle and
// Db2 fold unquoted ones up).
// itBinKey is how an engine writes a binary key column and the value
// 0xDEAD in SQL, and how its grid shows that value.
type itBinKey struct{ typ, literal, shown string }

var itBinKeys = map[string]itBinKey{
	dbx.EnginePostgres: {"bytea", `'\xdead'::bytea`, `\xdead`},
	dbx.EngineMySQL:    {"VARBINARY(8)", "X'DEAD'", "0xdead"},
	dbx.EngineMSSQL:    {"VARBINARY(8)", "0xDEAD", "0xdead"},
	dbx.EngineOracle:   {"RAW(8)", "HEXTORAW('DEAD')", "0xdead"},
	dbx.EngineDB2:      {"VARBINARY(8)", "BX'DEAD'", "0xdead"},
}

func itForeignKeys(t *testing.T, h *dbx.Handle, sess *dbx.Session, database, schema, prefix string) {
	t.Helper()
	ctx := ctx30(t)
	parent, child := prefix+"_p", prefix+"_c"
	if prefix == strings.ToUpper(prefix) { // Oracle, Db2: the catalog holds unquoted names upper case
		parent, child = prefix+"_P", prefix+"_C"
	}
	mustRun(t, sess, "CREATE TABLE "+parent+" (id INT NOT NULL PRIMARY KEY, code VARCHAR(10) NOT NULL, CONSTRAINT "+parent+"_uq UNIQUE (id, code))")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		sess.Run(ctx, "DROP TABLE "+child, dbx.RunOptions{})
		sess.Run(ctx, "DROP TABLE "+parent, dbx.RunOptions{})
	})
	mustRun(t, sess, "CREATE TABLE "+child+" (id INT NOT NULL PRIMARY KEY, parent_id INT, pcode VARCHAR(10), "+
		"CONSTRAINT "+child+"_fk FOREIGN KEY (parent_id) REFERENCES "+parent+" (id), "+
		"CONSTRAINT "+child+"_fk2 FOREIGN KEY (parent_id, pcode) REFERENCES "+parent+" (id, code))")
	for _, q := range []string{
		"INSERT INTO " + parent + " (id, code) VALUES (1, 'one')",
		"INSERT INTO " + parent + " (id, code) VALUES (2, 'two')",
		"INSERT INTO " + child + " (id, parent_id, pcode) VALUES (10, 2, 'two')",
		"INSERT INTO " + child + " (id, parent_id, pcode) VALUES (11, NULL, NULL)",
	} {
		mustRun(t, sess, q)
	}

	d, err := h.Describe(ctx, database, schema, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ForeignKeys) != 2 {
		t.Fatalf("foreign keys %+v", d.ForeignKeys)
	}
	fk, fk2 := d.ForeignKeys[0], d.ForeignKeys[1]
	if len(fk.Columns) == 2 {
		fk, fk2 = fk2, fk
	}
	eq := strings.EqualFold
	if len(fk.Columns) != 1 || !eq(fk.Columns[0], "parent_id") || len(fk.RefColumns) != 1 || !eq(fk.RefColumns[0], "id") ||
		!eq(fk.RefTable, parent) || !eq(fk.RefDatabase, database) || !eq(fk.RefSchema, schema) {
		t.Fatalf("foreign key %+v", fk)
	}

	if len(fk2.Columns) != 2 || !eq(fk2.Columns[0], "parent_id") || !eq(fk2.Columns[1], "pcode") ||
		len(fk2.RefColumns) != 2 || !eq(fk2.RefColumns[0], "id") || !eq(fk2.RefColumns[1], "code") {
		t.Fatalf("composite foreign key %+v", fk2)
	}

	// The other way: the parent lists both keys of the child.
	in, err := h.ReferencedBy(ctx, database, schema, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 2 {
		t.Fatalf("referenced by: %+v", in)
	}
	for _, k := range in {
		if !eq(k.Table, child) || !eq(k.Database, database) || !eq(k.Schema, schema) || !eq(k.RefTable, parent) ||
			len(k.Columns) != len(k.RefColumns) || !eq(k.Columns[0], "parent_id") || !eq(k.RefColumns[0], "id") {
			t.Fatalf("referenced by: %+v", k)
		}
	}
	comp := in[0]
	if len(comp.Columns) != 2 {
		comp = in[1]
	}
	if len(comp.Columns) != 2 || !eq(comp.Columns[1], "pcode") || !eq(comp.RefColumns[1], "code") {
		t.Fatalf("referenced by: composite key %+v", in)
	}

	two, twoCode := "2", "two"
	page, err := h.BrowseWhere(ctx, fk2.RefDatabase, fk2.RefSchema, fk2.RefTable,
		[]dbx.Cond{{Column: fk2.RefColumns[0], Value: &two}, {Column: fk2.RefColumns[1], Value: &twoCode}}, 0, 50)
	if err != nil {
		t.Fatalf("followed composite: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("followed composite: %d rows", len(page.Rows))
	}
	// Both members count: a pair no parent has matches nothing.
	one := "one"
	page, err = h.BrowseWhere(ctx, fk2.RefDatabase, fk2.RefSchema, fk2.RefTable,
		[]dbx.Cond{{Column: fk2.RefColumns[0], Value: &two}, {Column: fk2.RefColumns[1], Value: &one}}, 0, 50)
	if err != nil || len(page.Rows) != 0 {
		t.Fatalf("mismatched composite: err %v, rows %v", err, page)
	}
	page, err = h.BrowseWhere(ctx, fk.RefDatabase, fk.RefSchema, fk.RefTable, []dbx.Cond{{Column: fk.RefColumns[0], Value: &two}}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || *page.Rows[0][0] != "2" || *page.Rows[0][1] != "two" {
		t.Fatalf("followed: %d rows %v", len(page.Rows), page.Rows)
	}
	odd := "2' OR '1'='1"
	page, err = h.BrowseWhere(ctx, fk.RefDatabase, fk.RefSchema, fk.RefTable, []dbx.Cond{{Column: fk2.RefColumns[1], Value: &odd}}, 0, 50)
	if err != nil {
		t.Fatalf("bound value: %v", err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("bound value: %d rows", len(page.Rows))
	}
	page, err = h.BrowseWhere(ctx, database, schema, child, []dbx.Cond{{Column: fk.Columns[0], Value: nil}}, 0, 50)
	if err != nil {
		t.Fatalf("IS NULL: %v", err)
	}
	if len(page.Rows) != 1 || *page.Rows[0][0] != "11" {
		t.Fatalf("IS NULL: %d rows", len(page.Rows))
	}

	// The Data view's filter and sort, on the server.
	codeCol := fk2.RefColumns[1]
	tpat, one1 := "t%", "1"
	page, err = h.BrowseQuery(ctx, fk.RefDatabase, fk.RefSchema, fk.RefTable, []dbx.Cond{{Column: codeCol, Value: &tpat, Op: "LIKE"}}, nil, 0, 50)
	if err != nil || len(page.Rows) != 1 || *page.Rows[0][1] != "two" {
		t.Fatalf("LIKE: err %v, rows %v", err, page)
	}
	page, err = h.BrowseQuery(ctx, fk.RefDatabase, fk.RefSchema, fk.RefTable, []dbx.Cond{{Column: fk.RefColumns[0], Value: &one1, Op: ">="}},
		[]dbx.Order{{Column: codeCol, Desc: true}}, 0, 50)
	if err != nil || len(page.Rows) != 2 || *page.Rows[0][1] != "two" || *page.Rows[1][1] != "one" {
		t.Fatalf(">= sorted desc: err %v, rows %v", err, page)
	}

	// A binary key, followed with the value as the grid shows it.
	bk, ok := itBinKeys[h.Engine()]
	if !ok {
		t.Fatalf("no binary key type for %s", h.Engine())
	}
	bparent, bchild := parent+"b", child+"b"
	if parent == strings.ToUpper(parent) {
		bparent, bchild = parent+"B", child+"B"
	}
	mustRun(t, sess, "CREATE TABLE "+bparent+" (k "+bk.typ+" NOT NULL PRIMARY KEY, label VARCHAR(10))")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		sess.Run(ctx, "DROP TABLE "+bchild, dbx.RunOptions{})
		sess.Run(ctx, "DROP TABLE "+bparent, dbx.RunOptions{})
	})
	mustRun(t, sess, "CREATE TABLE "+bchild+" (id INT NOT NULL PRIMARY KEY, k "+bk.typ+", CONSTRAINT "+bchild+"_fk FOREIGN KEY (k) REFERENCES "+bparent+" (k))")
	mustRun(t, sess, "INSERT INTO "+bparent+" (k, label) VALUES ("+bk.literal+", 'dead')")
	mustRun(t, sess, "INSERT INTO "+bchild+" (id, k) VALUES (1, "+bk.literal+")")
	bd, err := h.Describe(ctx, database, schema, bchild)
	if err != nil || len(bd.ForeignKeys) != 1 {
		t.Fatalf("binary foreign key: %v %+v", err, bd)
	}
	bf := bd.ForeignKeys[0]
	cp, err := h.Browse(ctx, database, schema, bchild, 0, 5)
	if err != nil || len(cp.Rows) != 1 || *cp.Rows[0][1] != bk.shown {
		t.Fatalf("binary cell: %v %v (want %q)", err, cp, bk.shown)
	}
	shown := *cp.Rows[0][1]
	page, err = h.BrowseWhere(ctx, bf.RefDatabase, bf.RefSchema, bf.RefTable, []dbx.Cond{{Column: bf.RefColumns[0], Value: &shown, Hex: true}}, 0, 5)
	if err != nil {
		t.Fatalf("followed binary key: %v", err)
	}
	if len(page.Rows) != 1 || *page.Rows[0][1] != "dead" {
		t.Fatalf("followed binary key: %d rows", len(page.Rows))
	}
}
