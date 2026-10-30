package dbx_test

// IBM Db2, the same way as integration_test.go:
//
//	DBTEST_DB2=db2://db2inst1:secret@127.0.0.1:50000/testdb
//
//	docker run -d --privileged -e LICENSE=accept -e DB2INST1_PASSWORD=secret -e DBNAME=testdb \
//	  -e ARCHIVE_LOGS=false -e AUTOCONFIG=false -p 127.0.0.1:50000:50000 icr.io/db2_community/db2
//
// (the first start takes several minutes: wait for "Setup has completed"
// in its log).

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/dbx"
)

func TestIntegrationDB2(t *testing.T) {
	s := itSetupEnv(t, dbx.EngineDB2, "DBTEST_DB2")
	h := s.open(t, false)
	ctx := ctx30(t)
	table := fmt.Sprintf("DBX_IT_%d", time.Now().UnixNano()%1e9)
	view := table + "_V"

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToUpper(s.cfg.User)
	if !strings.Contains(strings.ToUpper(info.Version), "DB2") || info.Database != schema {
		t.Fatalf("info %+v", info)
	}
	itTunneled(t, s)

	sess, err := h.OpenSession(ctx, "q1", schema)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, sess, "CREATE TABLE "+table+` (
		id INTEGER NOT NULL GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		name VARCHAR(50) NOT NULL, price DECIMAL(10,2), data VARBINARY(16),
		created TIMESTAMP DEFAULT CURRENT TIMESTAMP)`)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if cs, err := h.OpenSession(ctx, "cleanup", schema); err == nil {
			cs.Run(ctx, "DROP VIEW "+view, dbx.RunOptions{})
			cs.Run(ctx, "DROP TABLE "+table, dbx.RunOptions{})
		}
	})
	res := mustRun(t, sess, "INSERT INTO "+table+" (name, price, data) WITH n(i) AS (VALUES 1 UNION ALL SELECT i + 1 FROM n WHERE i < 1200) SELECT 'item ' || i, i * 1.5, BX'DEAD' FROM n;\n"+
		"CREATE VIEW "+view+" AS SELECT id, name FROM "+table+" WHERE id <= 10")
	// The real count: upstream go-db2 reports 1 (third_party/go-db2/PATCHES.md).
	if res[0].Error != "" || res[0].RowsAffected != 1200 || !res[0].Write {
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
	found, sysFound := false, false
	for _, d := range dbs {
		found = found || (d.Name == schema && !d.System)
		sysFound = sysFound || (d.Name == "SYSCAT" && d.System)
	}
	if !found || !sysFound {
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
	if len(d.Columns) != 5 || d.Columns[0].Key != "PRI" || d.Columns[0].Extra != "GENERATED ALWAYS AS IDENTITY" ||
		d.Columns[1].Nullable || d.Columns[1].Type != "VARCHAR(50)" || d.Columns[2].Type != "DECIMAL(10,2)" ||
		d.Columns[3].Type != "VARBINARY(16)" || d.Columns[4].Default == nil {
		t.Fatalf("columns %+v", d.Columns)
	}
	if len(d.Indexes) == 0 || !d.Indexes[0].Primary || d.Indexes[0].Columns[0] != "ID" {
		t.Fatalf("indexes %+v", d.Indexes)
	}
	if !strings.Contains(d.DDL, "CREATE TABLE") || !strings.Contains(d.DDL, `"PRICE" DECIMAL(10,2)`) || !strings.Contains(d.DDL, `PRIMARY KEY ("ID")`) {
		t.Fatalf("ddl %q (%s)", d.DDL, d.DDLNote)
	}
	if vd, err := h.Describe(ctx, schema, "", view); err != nil || !strings.Contains(strings.ToUpper(vd.DDL), "VIEW") || vd.Kind != dbx.KindView {
		t.Fatalf("view ddl %v %+v", err, vd)
	}

	page, err := h.Browse(ctx, schema, "", table, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 50 || *page.Rows[0][0] != "101" || *page.Rows[0][1] != "item 101" || *page.Rows[0][2] != "151.50" || *page.Rows[0][3] != "0xdead" {
		t.Fatalf("page: %d rows, first %v %v %v %v", len(page.Rows), *page.Rows[0][0], *page.Rows[0][1], *page.Rows[0][2], *page.Rows[0][3])
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`).MatchString(*page.Rows[0][4]) {
		t.Fatalf("timestamp %q", *page.Rows[0][4])
	}

	// The bound reaches the server (go-db2 reads a result whole), one row
	// past it so the cut shows.
	res = sess.Run(ctx, "SELECT * FROM "+table, dbx.RunOptions{MaxRows: 500})
	if res[0].Error != "" || len(res[0].Rows) != 500 || !res[0].Truncated {
		t.Fatalf("bounded read: err %q, %d rows, truncated %v", res[0].Error, len(res[0].Rows), res[0].Truncated)
	}
	if r := mustRun(t, sess, "SELECT id FROM "+table+" ORDER BY id FETCH FIRST 3 ROWS ONLY"); len(r[0].Rows) != 3 {
		t.Fatalf("own bound: %d rows", len(r[0].Rows))
	}

	res = sess.Run(ctx, "UPDATE "+table+" SET price = NULL WHERE id <= 3; SELECT price FROM "+table+" WHERE id = 1; SELECT nope FROM "+table+"; VALUES 1", dbx.RunOptions{})
	if len(res) != 3 || res[0].RowsAffected != 3 || res[1].Rows[0][0] != nil || !strings.Contains(res[2].Error, "SQL0206N") && !strings.Contains(res[2].Error, "-206") {
		t.Fatalf("script: %d results, %+v", len(res), res)
	}

	// SQL PL: semicolons inside the compound statement, "@" ends it.
	res = mustRun(t, sess, "BEGIN\n  DECLARE n INT;\n  SET n = 1;\n  UPDATE "+table+" SET name = 'first' WHERE id = n;\nEND\n@\nVALUES 'it''s'")
	if len(res) != 2 || *res[1].Rows[0][0] != "it's" {
		t.Fatalf("block: %+v / %+v", res[0], res[len(res)-1])
	}
	if r := mustRun(t, sess, "SELECT name FROM "+table+" WHERE id = 1"); *r[0].Rows[0][0] != "first" {
		t.Fatalf("block did not run: %v", *r[0].Rows[0][0])
	}

	itCancel(t, sess, "SELECT COUNT(*) FROM SYSCAT.COLUMNS a, SYSCAT.COLUMNS b, SYSCAT.COLUMNS c", "VALUES 1")

	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "q", schema)
	if err != nil {
		t.Fatal(err)
	}
	itReadOnlyRefused(t, rs,
		"DELETE FROM "+table, "DROP TABLE "+table, "BEGIN DELETE FROM "+table+"; END",
		"VALUES 1; DELETE FROM "+table,
		"SELECT id FROM FINAL TABLE (INSERT INTO "+table+" (name) VALUES ('x'))",
		"CALL SYSPROC.ADMIN_CMD('RUNSTATS ON TABLE "+schema+"."+table+"')",
	)
	if r := mustRun(t, rs, "SELECT COUNT(*) FROM "+table); *r[0].Rows[0][0] != "1200" {
		t.Fatalf("rows after read-only attempts: %s", *r[0].Rows[0][0])
	}
}
