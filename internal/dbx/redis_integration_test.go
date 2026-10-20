package dbx_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/dbx"
)

func TestIntegrationRedis(t *testing.T) {
	s := itSetupEnv(t, dbx.EngineRedis, "DBTEST_REDIS")
	h := s.open(t, false)
	ctx := ctx30(t)
	p := fmt.Sprintf("dbxit%d", time.Now().UnixNano()%1e9)

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Version, "Redis ") && !strings.HasPrefix(info.Version, "Valkey ") || info.User != s.cfg.User {
		t.Fatalf("info %+v", info)
	}
	tunneled := false
	for _, d := range s.srv.Dialed() {
		tunneled = tunneled || d == "db.internal:"+s.port
	}
	if !tunneled {
		t.Fatalf("not dialed through ssh: %v", s.srv.Dialed())
	}

	sess, err := h.OpenSession(ctx, "q1", "db3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if s2, err := h.OpenSession(c, "cleanup", "db3"); err == nil {
			s2.Run(c, "FLUSHDB", dbx.RunOptions{})
		}
	})
	var script strings.Builder
	script.WriteString("FLUSHDB\n")
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&script, "SET %s:user:%03d \"name %d\"\n", p, i, i)
	}
	fmt.Fprintf(&script, `HSET %[1]s:h f1 v1 f2 "two words"
RPUSH %[1]s:l a b c
SADD %[1]s:s x
ZADD %[1]s:z 1.5 m1 2 m2
XADD %[1]s:x * k v
SET plain "\xff\x00"
EXPIRE %[1]s:l 600
`, p)
	res := mustRun(t, sess, script.String())
	if last := res[len(res)-1]; last.Command != "(integer) 1" || !last.Write {
		t.Fatalf("expire %+v", last)
	}

	// Tree: db3 is listed, keys grouped by their first segment.
	dbs, err := h.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var saw3 bool
	for _, d := range dbs {
		saw3 = saw3 || d.Name == "db3"
	}
	if !saw3 {
		t.Fatalf("databases %+v", dbs)
	}
	objs, err := h.Objects(ctx, "db3", false)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]int64{}
	for _, o := range objs {
		groups[o.Name] = o.Rows
	}
	if groups[p+":*"] != 255 || groups["(no prefix)"] != 1 || len(groups) != 2 {
		t.Fatalf("objects %+v", objs)
	}
	d, err := h.Describe(ctx, "db3", "", p+":*")
	if err != nil || len(d.Columns) != 5 || !strings.Contains(d.DDL, "255 keys") || !strings.Contains(d.DDL, "string: 250") {
		t.Fatalf("describe %+v %v", d, err)
	}

	// Data: sorted pages with type, TTL, size and a preview.
	page, err := h.Browse(ctx, "db3", "", p+":*", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 10 || *page.Rows[0][0] != p+":h" || *page.Rows[0][1] != "hash" || *page.Rows[0][3] != "2" ||
		*page.Rows[0][4] != `{"f1":"v1","f2":"two words"}` {
		t.Fatalf("page %v", rowText(page.Rows[0]))
	}
	byKey := map[string][]string{}
	for _, r := range page.Rows {
		byKey[*r[0]] = rowText(r)
	}
	if l := byKey[p+":l"]; l[1] != "list" || l[2] == "-1" || l[3] != "3" || l[4] != `["a","b","c"]` {
		t.Fatalf("list %v", l)
	}
	if z := byKey[p+":s"]; z[4] != `["x"]` {
		t.Fatalf("set %v", z)
	}
	if page, err = h.Browse(ctx, "db3", "", p+":*", 250, 10); err != nil || len(page.Rows) != 5 {
		t.Fatalf("last page %v %v", page, err)
	}
	if page, err = h.Browse(ctx, "db3", "", "(no prefix)", 0, 10); err != nil || len(page.Rows) != 1 || *page.Rows[0][4] != "0xff00" {
		t.Fatalf("no prefix %+v %v", page, err)
	}

	// Query results.
	r := mustRun(t, sess, "HGETALL "+p+":h")[0]
	if r.Columns[0].Name != "field" || len(r.Rows) != 2 || r.Write {
		t.Fatalf("hgetall %+v", r)
	}
	r = mustRun(t, sess, "ZRANGE "+p+":z 0 -1 WITHSCORES")[0]
	if r.Columns[1].Name != "score" || *r.Rows[0][1] != "1.5" {
		t.Fatalf("zrange %+v", r)
	}
	r = sess.Run(ctx, "SCAN 0 MATCH "+p+":user:* COUNT 1000", dbx.RunOptions{MaxRows: 100})[0]
	if r.Error != "" || len(r.Rows) != 100 || !r.Truncated {
		t.Fatalf("scan %s rows=%d", r.Error, len(r.Rows))
	}
	r = mustRun(t, sess, "INFO keyspace")[0]
	if r.Columns[0].Name != "section" || !strings.Contains(rowLines(r), "db3") {
		t.Fatalf("info %+v", r)
	}
	if r = sess.Run(ctx, "HGET "+p+":l x", dbx.RunOptions{})[0]; !strings.HasPrefix(r.Error, "WRONGTYPE") {
		t.Fatalf("wrongtype %+v", r)
	}
	if r = sess.Run(ctx, "MONITOR", dbx.RunOptions{})[0]; !strings.Contains(r.Error, "redis-cli") {
		t.Fatalf("monitor %+v", r)
	}

	// SELECT carries to the next run, and across a Stop.
	mustRun(t, sess, "SELECT 4\nSET "+p+":in4 1")
	if r = mustRun(t, sess, "EXISTS "+p+":in4")[0]; r.Command != "(integer) 1" {
		t.Fatalf("select lost %+v", r)
	}
	done := make(chan []*dbx.Result)
	go func() { done <- sess.Run(context.Background(), "BLPOP "+p+":never 0", dbx.RunOptions{}) }()
	time.Sleep(300 * time.Millisecond)
	sess.Cancel()
	select {
	case rs := <-done:
		if rs[0].Error != "query stopped" {
			t.Fatalf("stopped %+v", rs[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not break off BLPOP")
	}
	if r = mustRun(t, sess, "EXISTS "+p+":in4")[0]; r.Command != "(integer) 1" {
		t.Fatalf("select lost after stop %+v", r)
	}
	mustRun(t, sess, "DEL "+p+":in4\nSELECT 3")

	// Read-only refuses writes before they leave.
	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "ro", "db3")
	if err != nil {
		t.Fatal(err)
	}
	if r = rs.Run(ctx, "SET "+p+":x 1", dbx.RunOptions{})[0]; !strings.Contains(r.Error, "read-only") {
		t.Fatalf("read-only set %+v", r)
	}
	if r = rs.Run(ctx, "GET "+p+":user:001", dbx.RunOptions{})[0]; r.Error != "" || *r.Rows[0][0] != "name 1" {
		t.Fatalf("read-only get %+v", r)
	}
}

// An ACL user limited to reads (+@read +@connection, which leaves out INFO
// and CONFIG): the tree and reads work, writes come back as NOPERM.
func TestIntegrationRedisReader(t *testing.T) {
	s := itSetupEnv(t, dbx.EngineRedis, "DBTEST_REDIS_READER")
	h := s.open(t, false)
	ctx := ctx30(t)
	if _, err := h.Info(ctx); err != nil {
		t.Fatal(err)
	}
	// Such a user may be refused INFO and CONFIG GET: db0 is still listed.
	if dbs, err := h.Databases(ctx); err != nil || len(dbs) == 0 || dbs[0].Name != "db0" {
		t.Fatalf("databases %+v %v", dbs, err)
	}
	if _, err := h.Objects(ctx, "db0", false); err != nil {
		t.Fatal(err)
	}
	sess, err := h.OpenSession(ctx, "q", "")
	if err != nil {
		t.Fatal(err)
	}
	if r := sess.Run(ctx, "SET x 1", dbx.RunOptions{})[0]; !strings.Contains(r.Error, "NOPERM") {
		t.Fatalf("%+v", r)
	}
}
