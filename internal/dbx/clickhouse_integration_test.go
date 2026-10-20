package dbx_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/dbx"
)

func TestIntegrationClickHouse(t *testing.T) {
	s := itSetup(t, dbx.EngineClickHouse)
	h := s.open(t, false)
	ctx := ctx30(t)
	db := s.cfg.Database
	table := fmt.Sprintf("dbx_it_%d", time.Now().UnixNano()%1e9)
	view := table + "_v"

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version == "" || info.Engine != dbx.EngineClickHouse {
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

	sess, err := h.OpenSession(ctx, "q1", db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if s2, err := h.OpenSession(c, "cleanup", db); err == nil {
			s2.Run(c, "DROP VIEW IF EXISTS "+view+"; DROP TABLE IF EXISTS "+table, dbx.RunOptions{})
		}
	})
	res := mustRun(t, sess, `
		CREATE TABLE `+table+` (
			id UInt64, name String, tags Array(String), price Nullable(Decimal(10,2)),
			raw String, note String DEFAULT 'none' COMMENT 'a note',
			INDEX name_idx name TYPE bloom_filter GRANULARITY 1
		) ENGINE = MergeTree ORDER BY (id) COMMENT 'it table';
		INSERT INTO `+table+` (id, name, tags, price, raw) VALUES
			(1, 'a', ['x','y'], 1.50, unhex('ff00')), (2, 'tab\there', [], NULL, 'b'), (3, 'line\nbreak', ['z'], 3, 'c');
		CREATE VIEW `+view+` AS SELECT id, name FROM `+table)
	if res[1].RowsAffected != 3 || !res[1].Write || res[1].HasRows {
		t.Fatalf("insert: %+v", res[1])
	}

	// Tree.
	dbs, err := h.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawDB, sawSystem bool
	for _, d := range dbs {
		sawDB = sawDB || d.Name == db
		sawSystem = sawSystem || (d.Name == "system" && d.System)
	}
	if !sawDB || !sawSystem {
		t.Fatalf("databases %+v", dbs)
	}
	objs, err := h.Objects(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
		if o.Name == table && (o.Rows != 3 || o.Comment != "it table") {
			t.Fatalf("table object %+v", o)
		}
	}
	if kinds[table] != dbx.KindTable || kinds[view] != dbx.KindView {
		t.Fatalf("objects %+v", objs)
	}

	d, err := h.Describe(ctx, db, "", table)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Columns) != 6 || d.Columns[0].Key != "PRI" || !d.Columns[3].Nullable ||
		d.Columns[5].Default == nil || *d.Columns[5].Default != "'none'" || d.Columns[5].Comment != "a note" {
		t.Fatalf("columns %+v", d.Columns)
	}
	var sawSkip bool
	for _, ix := range d.Indexes {
		sawSkip = sawSkip || (ix.Name == "name_idx" && strings.Contains(ix.Def, "bloom_filter"))
	}
	if !sawSkip || !d.Indexes[0].Primary || !strings.Contains(d.DDL, "CREATE TABLE") {
		t.Fatalf("detail %+v", d)
	}

	page, err := h.Browse(ctx, db, "", table, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || *page.Rows[0][0] != "2" || len(page.Columns) != 6 || page.Columns[2].Type != "Array(String)" {
		t.Fatalf("browse %+v", page)
	}

	// Values come back as text: NULL is nil, escapes undone, bytes that are
	// not UTF-8 as hex.
	r := mustRun(t, sess, "SELECT name, tags, price, raw FROM "+table+" ORDER BY id")[0]
	if !r.HasRows || len(r.Rows) != 3 || r.RowsAffected != 3 {
		t.Fatalf("select %+v", r)
	}
	if *r.Rows[0][1] != "['x','y']" || *r.Rows[0][2] != "1.5" || *r.Rows[0][3] != "0xff00" ||
		*r.Rows[1][0] != "tab\there" || r.Rows[1][2] != nil || *r.Rows[2][0] != "line\nbreak" {
		t.Fatalf("values %q %q %q", rowText(r.Rows[0]), rowText(r.Rows[1]), rowText(r.Rows[2]))
	}

	// Session state carries: SET and USE.
	r = mustRun(t, sess, "SET max_threads = 3; SELECT getSetting('max_threads')")[1]
	if *r.Rows[0][0] != "3" {
		t.Fatalf("SET did not carry: %+v", r.Rows)
	}
	r = mustRun(t, sess, "USE system; SELECT currentDatabase()")[1]
	if *r.Rows[0][0] != "system" {
		t.Fatalf("USE did not carry: %+v", r.Rows)
	}
	if r = mustRun(t, sess, "SELECT currentDatabase()")[0]; *r.Rows[0][0] != "system" {
		t.Fatalf("USE lost on the next run: %+v", r.Rows)
	}
	mustRun(t, sess, "USE "+db)

	// A statement with its own FORMAT comes back as lines.
	r = mustRun(t, sess, "SELECT id FROM "+table+" ORDER BY id FORMAT JSONEachRow")[0]
	if len(r.Columns) != 1 || r.Columns[0].Name != "output" || len(r.Rows) != 3 || *r.Rows[0][0] != `{"id":1}` {
		t.Fatalf("own format %+v %q", r, rowText(r.Rows[0]))
	}

	// Bounds stop a big read on the server.
	start := time.Now()
	r = sess.Run(ctx, "SELECT number FROM system.numbers", dbx.RunOptions{MaxRows: 100})[0]
	if r.Error != "" || !r.Truncated || len(r.Rows) != 100 || time.Since(start) > 10*time.Second {
		t.Fatalf("bounded read: %+v rows=%d", r.Error, len(r.Rows))
	}
	mustRun(t, sess, "SELECT 1")

	// An error after the result started.
	r = sess.Run(ctx, "SELECT number, throwIf(number = 3, 'boom') FROM numbers(10) SETTINGS max_block_size = 1", dbx.RunOptions{})[0]
	if !strings.Contains(r.Error, "boom") {
		t.Fatalf("mid-result error: %+v", r)
	}

	// Cancel stops a long query and keeps the session.
	done := make(chan []*dbx.Result)
	start = time.Now()
	go func() {
		done <- sess.Run(context.Background(), "SELECT count() FROM numbers_mt(1e13)", dbx.RunOptions{})
	}()
	time.Sleep(500 * time.Millisecond)
	sess.Cancel()
	select {
	case r := <-done:
		if r[0].Error == "" {
			t.Fatalf("cancelled query: %+v", r[0])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the query")
	}
	itEventually(t, func() error { return firstErr(sess.Run(ctx, "SELECT 1", dbx.RunOptions{})) })

	// A caller's context ending stops the query too.
	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	if r := sess.Run(short, "SELECT count() FROM numbers_mt(1e13)", dbx.RunOptions{}); r[0].Error == "" {
		t.Fatalf("timed-out query: %+v", r[0])
	}
	itEventually(t, func() error { return firstErr(sess.Run(ctx, "SELECT 1", dbx.RunOptions{})) })

	// Read-only: the keyword check refuses, and the server is told too.
	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "ro", db)
	if err != nil {
		t.Fatal(err)
	}
	r = rs.Run(ctx, "INSERT INTO "+table+" (id) VALUES (9)", dbx.RunOptions{})[0]
	if !strings.Contains(r.Error, "read-only") {
		t.Fatalf("read-only insert: %+v", r)
	}
	r = rs.Run(ctx, "SELECT getSetting('readonly')", dbx.RunOptions{})[0]
	if r.Error != "" || *r.Rows[0][0] != "2" {
		t.Fatalf("server readonly: %+v", r)
	}
	r = rs.Run(ctx, "SELECT 1 SETTINGS readonly = 0", dbx.RunOptions{})[0]
	if r.Error == "" {
		t.Fatal("readonly lifted from a query")
	}
	// The LLM's read-only runs on a writable handle get the same.
	r = sess.Run(ctx, "SELECT getSetting('readonly')", dbx.RunOptions{ReadOnly: true})[0]
	if r.Error != "" || *r.Rows[0][0] != "2" {
		t.Fatalf("run read-only: %+v", r)
	}
}

// A login whose profile is already read-only may not be sent readonly (or
// any setting): the tab works for it as it is.
func TestIntegrationClickHouseReadOnlyLogin(t *testing.T) {
	s := itSetupEnv(t, dbx.EngineClickHouse, "DBTEST_CH_READER")
	ctx := ctx30(t)
	for _, readOnly := range []bool{false, true} {
		h := s.open(t, readOnly)
		if _, err := h.Databases(ctx); err != nil {
			t.Fatal(err)
		}
		sess, err := h.OpenSession(ctx, "q", s.cfg.Database)
		if err != nil {
			t.Fatal(err)
		}
		r := sess.Run(ctx, "SELECT 1", dbx.RunOptions{ReadOnly: true})[0]
		if r.Error != "" {
			t.Fatalf("read-only=%v: %s", readOnly, r.Error)
		}
	}
}

func rowText(row []*string) []string {
	out := make([]string, len(row))
	for i, v := range row {
		if v == nil {
			out[i] = "NULL"
		} else {
			out[i] = *v
		}
	}
	return out
}

func firstErr(rs []*dbx.Result) error {
	for _, r := range rs {
		if r.Error != "" {
			return fmt.Errorf("%s", r.Error)
		}
	}
	return nil
}

// itEventually retries f for a few seconds: a cancelled query can hold its
// session on the server for a moment.
func itEventually(t *testing.T, f func() error) {
	t.Helper()
	var err error
	for i := 0; i < 20; i++ {
		if err = f(); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal(err)
}
