package dbx_test

// Oracle and SQL Server, the same way as integration_test.go:
//
//	DBTEST_ORA=oracle://app:secret@127.0.0.1:1521/FREEPDB1
//	DBTEST_MSSQL=sqlserver://sa:Secret_123@127.0.0.1:1433/appdb (the database
//	  is created when missing)
//
//	docker run -d -e ORACLE_PASSWORD=secret -e APP_USER=app -e APP_USER_PASSWORD=secret -p 127.0.0.1:1521:1521 gvenzl/oracle-free:23-slim-faststart
//	docker run -d -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=Secret_123 -p 127.0.0.1:1433:1433 mcr.microsoft.com/mssql/server:2022-latest

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/dbx"
)

func itTunneled(t *testing.T, s *itServer) {
	t.Helper()
	for _, d := range s.srv.Dialed() {
		if d == "db.internal:"+s.port {
			return
		}
	}
	t.Fatalf("not dialed through ssh: %v", s.srv.Dialed())
}

// itCancel runs a long statement, stops it and checks the session lives on.
func itCancel(t *testing.T, sess *dbx.Session, long, after string) {
	t.Helper()
	done := make(chan []*dbx.Result)
	go func() { done <- sess.Run(context.Background(), long, dbx.RunOptions{}) }()
	time.Sleep(time.Second)
	sess.Cancel()
	select {
	case r := <-done:
		if r[0].Error == "" {
			t.Fatalf("cancelled statement: %+v", r[0])
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancel did not stop the statement")
	}
	mustRun(t, sess, after)

	// A caller's context ending stops it too.
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	sess.Run(short, long, dbx.RunOptions{})
	if time.Since(start) > 15*time.Second {
		t.Fatal("an ended context did not stop the statement")
	}
	mustRun(t, sess, after)
}

func itReadOnlyRefused(t *testing.T, sess *dbx.Session, stmts ...string) {
	t.Helper()
	ctx := ctx30(t)
	for _, stmt := range stmts {
		r := sess.Run(ctx, stmt, dbx.RunOptions{})
		if last := r[len(r)-1]; !strings.Contains(last.Error, "read-only") {
			t.Fatalf("%q in read-only: %+v", stmt, last)
		}
	}
}

func TestIntegrationOracle(t *testing.T) {
	s := itSetupEnv(t, dbx.EngineOracle, "DBTEST_ORA")
	h := s.open(t, false)
	ctx := ctx30(t)
	table := fmt.Sprintf("DBX_IT_%d", time.Now().UnixNano()%1e9)
	view := table + "_V"

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToUpper(s.cfg.User)
	if !strings.HasPrefix(info.Version, "Oracle") || info.Database != schema {
		t.Fatalf("info %+v", info)
	}
	itTunneled(t, s)

	sess, err := h.OpenSession(ctx, "q1", schema)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, sess, "CREATE TABLE "+table+` (
		id NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		name VARCHAR2(50 CHAR) NOT NULL, price NUMBER(10,2), data RAW(16),
		created TIMESTAMP DEFAULT SYSTIMESTAMP)`)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if cs, err := h.OpenSession(ctx, "cleanup", schema); err == nil {
			cs.Run(ctx, "DROP VIEW "+view, dbx.RunOptions{})
			cs.Run(ctx, "DROP TABLE "+table+" PURGE", dbx.RunOptions{})
		}
	})
	res := mustRun(t, sess, "INSERT INTO "+table+" (name, price, data) SELECT 'item ' || LEVEL, LEVEL * 1.5, HEXTORAW('DEAD') FROM DUAL CONNECT BY LEVEL <= 1200;\n"+
		"CREATE VIEW "+view+" AS SELECT id, name FROM "+table+" WHERE id <= 10")
	if res[0].RowsAffected != 1200 || !res[0].Write {
		t.Fatalf("insert %+v", res[0])
	}

	// Committed on its own: another connection sees the rows.
	other, err := h.OpenSession(ctx, "q2", schema)
	if err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, other, "SELECT COUNT(*) FROM "+table); *r[0].Rows[0][0] != "1200" {
		t.Fatalf("autocommit: %v", *r[0].Rows[0][0])
	}

	dbs, err := h.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range dbs {
		found = found || (d.Name == schema && !d.System)
	}
	if !found {
		t.Fatalf("databases %+v", dbs)
	}
	objs, err := h.Objects(ctx, schema, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
	}
	if kinds[table] != dbx.KindTable || kinds[view] != dbx.KindView {
		t.Fatalf("objects: %v", kinds)
	}

	d, err := h.Describe(ctx, schema, "", table)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Columns) != 5 || d.Columns[0].Key != "PRI" || d.Columns[1].Nullable || d.Columns[1].Type != "VARCHAR2(50 CHAR)" || d.Columns[2].Type != "NUMBER(10,2)" {
		t.Fatalf("columns %+v", d.Columns)
	}
	if len(d.Indexes) == 0 || !d.Indexes[0].Primary || d.Indexes[0].Columns[0] != "ID" {
		t.Fatalf("indexes %+v", d.Indexes)
	}
	if !strings.Contains(d.DDL, "CREATE TABLE") || !strings.Contains(d.DDL, "PRICE") {
		t.Fatalf("ddl %q (%s)", d.DDL, d.DDLNote)
	}
	if vd, err := h.Describe(ctx, schema, "", view); err != nil || !strings.Contains(strings.ToUpper(vd.DDL), "VIEW") {
		t.Fatalf("view ddl %v %+v", err, vd)
	}

	page, err := h.Browse(ctx, schema, "", table, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 50 || *page.Rows[0][0] != "101" || *page.Rows[0][2] != "151.5" || *page.Rows[0][3] != "0xdead" {
		t.Fatalf("page: %d rows, first %v %v %v", len(page.Rows), *page.Rows[0][0], *page.Rows[0][2], *page.Rows[0][3])
	}
	// The wire's type names (VARCHAR2 is "NCHAR") read as SQL's.
	if page.Columns[1].Type != "varchar2" || page.Columns[3].Type != "raw" || page.Columns[4].Type != "timestamp" {
		t.Fatalf("column types %+v", page.Columns)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`).MatchString(*page.Rows[0][4]) {
		t.Fatalf("timestamp %q", *page.Rows[0][4])
	}

	res = sess.Run(ctx, "SELECT * FROM "+table, dbx.RunOptions{MaxRows: 500})
	if res[0].Error != "" || len(res[0].Rows) != 500 || !res[0].Truncated {
		t.Fatalf("bounded read: err %q, %d rows, truncated %v", res[0].Error, len(res[0].Rows), res[0].Truncated)
	}
	mustRun(t, sess, "SELECT 1 FROM DUAL")

	res = sess.Run(ctx, "UPDATE "+table+" SET price = NULL WHERE id <= 3; SELECT price FROM "+table+" WHERE id = 1; SELECT nope FROM "+table+"; SELECT 1 FROM DUAL", dbx.RunOptions{})
	if len(res) != 3 || res[0].RowsAffected != 3 || res[1].Rows[0][0] != nil || !strings.Contains(res[2].Error, "ORA-00904") {
		t.Fatalf("script: %d results, %+v", len(res), res)
	}

	// PL/SQL: semicolons inside a block, "/" ends it, DBMS_OUTPUT comes back.
	res = mustRun(t, sess, "DECLARE\n  n NUMBER;\nBEGIN\n  SELECT COUNT(*) INTO n FROM "+table+";\n  DBMS_OUTPUT.PUT_LINE('rows: ' || n);\nEND;\n/\nSELECT q'[it's]' FROM DUAL")
	if len(res) != 2 || res[0].Note != "rows: 1200" || *res[1].Rows[0][0] != "it's" {
		t.Fatalf("block: note %q, %+v / %+v", res[0].Note, res[0], res[len(res)-1])
	}

	// A break reaches the server in band (no TCP urgent data through a
	// tunnel): a running query stops, DBMS_SESSION.SLEEP would not.
	itCancel(t, sess, "SELECT COUNT(*) FROM ALL_OBJECTS a, ALL_OBJECTS b, ALL_OBJECTS c", "SELECT 1 FROM DUAL")

	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "q", schema)
	if err != nil {
		t.Fatal(err)
	}
	itReadOnlyRefused(t, rs,
		"DELETE FROM "+table, "DROP TABLE "+table, "BEGIN DELETE FROM "+table+"; END;",
		"SELECT 1 FROM DUAL; DELETE FROM "+table,
		"WITH FUNCTION f RETURN NUMBER IS BEGIN RETURN 1; END; SELECT f FROM DUAL",
	)
	if r := rs.Run(ctx, "SELECT * FROM "+table+" FOR UPDATE", dbx.RunOptions{}); r[0].Error == "" {
		t.Fatalf("server let FOR UPDATE through a read-only transaction: %+v", r[0])
	}
	if r := mustRun(t, rs, "SELECT COUNT(*) FROM "+table); *r[0].Rows[0][0] != "1200" {
		t.Fatalf("rows after read-only attempts: %s", *r[0].Rows[0][0])
	}
}

func TestIntegrationMSSQL(t *testing.T) {
	s := itSetupEnv(t, dbx.EngineMSSQL, "DBTEST_MSSQL")
	ctx := ctx30(t)
	dbName := s.cfg.Database
	{
		cfg := s.cfg
		cfg.Database = ""
		boot, err := dbx.Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		bs, err := boot.OpenSession(ctx, "boot", "master")
		if err != nil {
			t.Fatal(err)
		}
		mustRun(t, bs, "IF DB_ID('"+dbName+"') IS NULL CREATE DATABASE "+dbName)
		boot.Close()
	}
	h := s.open(t, false)
	table := fmt.Sprintf("dbx_it_%d", time.Now().UnixNano()%1e9)
	view := table + "_v"

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Version, "SQL Server") || info.Database != dbName {
		t.Fatalf("info %+v", info)
	}
	itTunneled(t, s)

	sess, err := h.OpenSession(ctx, "q1", dbName)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, sess, "CREATE TABLE "+table+` (
		id int IDENTITY PRIMARY KEY, name nvarchar(50) NOT NULL, price decimal(10,2),
		data varbinary(16), gid uniqueidentifier DEFAULT NEWID(), at datetimeoffset DEFAULT SYSDATETIMEOFFSET())`)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if cs, err := h.OpenSession(ctx, "cleanup", dbName); err == nil {
			cs.Run(ctx, "DROP VIEW IF EXISTS "+view+"; DROP TABLE IF EXISTS "+table, dbx.RunOptions{})
		}
	})
	res := mustRun(t, sess, "WITH n AS (SELECT TOP 1200 ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS i FROM sys.all_objects a CROSS JOIN sys.all_objects b)\n"+
		"INSERT INTO "+table+" (name, price, data) SELECT CONCAT('item ', i), i * 1.5, 0xDEAD FROM n\nGO\n"+
		"CREATE VIEW "+view+" AS SELECT id, name FROM "+table+" WHERE id <= 10")
	if res[0].RowsAffected != 1200 || !res[0].Write {
		t.Fatalf("insert %+v", res[0])
	}

	dbs, err := h.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range dbs {
		found = found || (d.Name == dbName && !d.System)
	}
	if !found {
		t.Fatalf("databases %+v", dbs)
	}
	objs, err := h.Objects(ctx, dbName, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Schema+"."+o.Name] = o.Kind
	}
	if kinds["dbo."+table] != dbx.KindTable || kinds["dbo."+view] != dbx.KindView {
		t.Fatalf("objects: %v", kinds)
	}

	d, err := h.Describe(ctx, dbName, "dbo", table)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Columns) != 6 || d.Columns[0].Key != "PRI" || d.Columns[0].Extra != "identity" || d.Columns[1].Nullable || d.Columns[1].Type != "nvarchar(50)" || d.Columns[2].Type != "decimal(10,2)" {
		t.Fatalf("columns %+v", d.Columns)
	}
	if len(d.Indexes) == 0 || !d.Indexes[0].Primary || d.Indexes[0].Columns[0] != "id" {
		t.Fatalf("indexes %+v", d.Indexes)
	}
	if !strings.Contains(d.DDL, "CREATE TABLE") || !strings.Contains(d.DDL, "[price]") {
		t.Fatalf("ddl %q", d.DDL)
	}
	if vd, err := h.Describe(ctx, dbName, "dbo", view); err != nil || !strings.Contains(strings.ToUpper(vd.DDL), "VIEW") {
		t.Fatalf("view ddl %v %+v", err, vd)
	}

	page, err := h.Browse(ctx, dbName, "dbo", table, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	r0 := page.Rows[0]
	if len(page.Rows) != 50 || *r0[0] != "101" || *r0[2] != "151.50" || *r0[3] != "0xdead" {
		t.Fatalf("page: %d rows, first %v %v %v", len(page.Rows), *r0[0], *r0[2], *r0[3])
	}
	if !regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`).MatchString(*r0[4]) {
		t.Fatalf("uniqueidentifier %q", *r0[4])
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d[.\d]* [+-]\d\d:\d\d$`).MatchString(*r0[5]) {
		t.Fatalf("datetimeoffset %q", *r0[5])
	}

	res = sess.Run(ctx, "SELECT * FROM "+table, dbx.RunOptions{MaxRows: 500})
	if res[0].Error != "" || len(res[0].Rows) != 500 || !res[0].Truncated {
		t.Fatalf("bounded read: err %q, %d rows, truncated %v", res[0].Error, len(res[0].Rows), res[0].Truncated)
	}
	mustRun(t, sess, "SELECT 1")

	res = sess.Run(ctx, "UPDATE "+table+" SET price = NULL WHERE id <= 3; SELECT price FROM "+table+" WHERE id = 1; SELECT nope FROM "+table+"; SELECT 1", dbx.RunOptions{})
	if len(res) != 3 || res[0].RowsAffected != 3 || res[1].Rows[0][0] != nil || !strings.Contains(res[2].Error, "Msg 207") {
		t.Fatalf("script: %d results, %+v", len(res), res)
	}

	// A DECLARE batch runs to GO; PRINT comes back in the note.
	res = mustRun(t, sess, "DECLARE @n int; SELECT @n = COUNT(*) FROM "+table+"; PRINT CONCAT('rows: ', @n); SELECT @n AS n\nGO\nSELECT N'it''s' AS s")
	if len(res) != 2 || *res[0].Rows[0][0] != "1200" || !strings.Contains(res[0].Note, "rows: 1200") || *res[1].Rows[0][0] != "it's" {
		t.Fatalf("batch: %+v", res)
	}

	// Transactions across runs.
	mustRun(t, sess, "BEGIN TRAN")
	if !sess.InTx() {
		t.Fatal("not in a transaction after BEGIN TRAN")
	}
	mustRun(t, sess, "DELETE FROM "+table+"; ROLLBACK")
	if sess.InTx() {
		t.Fatal("still in a transaction")
	}

	itCancel(t, sess, "WAITFOR DELAY '00:00:30'", "SELECT 1")

	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "q", dbName)
	if err != nil {
		t.Fatal(err)
	}
	itReadOnlyRefused(t, rs,
		"DELETE FROM "+table, "DROP TABLE "+table, "EXEC sp_who",
		"SELECT 1 DELETE FROM "+table, "SELECT * INTO dbx_copy FROM "+table,
		"SELECT * FROM OPENROWSET(BULK '/etc/passwd', SINGLE_CLOB) x",
		"WITH x AS (SELECT 1 AS a) SELECT a FROM x; UPDATE "+table+" SET price = 1",
	)
	// A write word as a quoted name or in a string is not a write.
	mustRun(t, rs, "SELECT TOP 1 name AS [delete], 'drop table x' AS s FROM "+table)
	if r := mustRun(t, rs, "SELECT COUNT(*) FROM "+table); *r[0].Rows[0][0] != "1200" {
		t.Fatalf("rows after read-only attempts: %s", *r[0].Rows[0][0])
	}
}
