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
//
// e.g. with docker:
//
//	docker run -d -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=appdb -p 127.0.0.1:5432:5432 postgres:17
//	docker run -d -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=appdb -p 127.0.0.1:3306:3306 mysql:8.4
//	docker run -d -e CLICKHOUSE_USER=app -e CLICKHOUSE_PASSWORD=secret -e CLICKHOUSE_DB=appdb -p 127.0.0.1:8123:8123 clickhouse/clickhouse-server:25.8
//	docker run -d -e discovery.type=single-node -e OPENSEARCH_INITIAL_ADMIN_PASSWORD=<strong> -p 127.0.0.1:9200:9200 opensearchproject/opensearch:2.19.3
//	docker run -d -e discovery.type=single-node -e ELASTIC_PASSWORD=secret -e xpack.security.http.ssl.enabled=false -p 127.0.0.1:9201:9200 docker.elastic.co/elasticsearch/elasticsearch:8.19.4
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
