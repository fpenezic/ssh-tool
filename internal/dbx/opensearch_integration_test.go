package dbx_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ssh-tool/internal/dbx"
)

func TestIntegrationOpenSearch(t *testing.T) {
	itSearch(t, itSetupEnv(t, dbx.EngineOpenSearch, "DBTEST_OS"), "OpenSearch")
}

func TestIntegrationElasticsearch(t *testing.T) {
	itSearch(t, itSetupEnv(t, dbx.EngineOpenSearch, "DBTEST_ES"), "Elasticsearch")
}

func itSearch(t *testing.T, s *itServer, flavor string) {
	h := s.open(t, false)
	ctx := ctx30(t)
	index := fmt.Sprintf("dbx_it_%d", time.Now().UnixNano()%1e9)
	alias := index + "_a"

	info, err := h.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Version, flavor+" ") || info.Database == "" || info.User != s.cfg.User {
		t.Fatalf("info %+v", info)
	}
	tunneled := false
	for _, d := range s.srv.Dialed() {
		tunneled = tunneled || d == "db.internal:"+s.port
	}
	if !tunneled {
		t.Fatalf("not dialed through ssh: %v", s.srv.Dialed())
	}

	sess, err := h.OpenSession(ctx, "q1", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sess.Run(c, "DELETE /"+index, dbx.RunOptions{})
	})
	var bulk strings.Builder
	for i := 1; i <= 1200; i++ {
		fmt.Fprintf(&bulk, "{\"index\":{\"_id\":\"%d\"}}\n{\"name\":\"item %d\",\"n\":%d,\"meta\":{\"color\":\"%s\"},\"tags\":[\"t%d\"]}\n",
			i, i, i, []string{"red", "blue"}[i%2], i%3)
	}
	res := mustRun(t, sess, `PUT /`+index+`
{
  "mappings": { "properties": {
    "name": { "type": "text", "fields": { "raw": { "type": "keyword" } } },
    "n": { "type": "long" },
    "meta": { "properties": { "color": { "type": "keyword" } } },
    "tags": { "type": "keyword" }
  } }
}

POST /`+index+`/_bulk?refresh=true
`+bulk.String()+`
POST /_aliases
{ "actions": [ { "add": { "index": "`+index+`", "alias": "`+alias+`" } } ] }`)
	if !strings.Contains(rowLines(res[0]), `"acknowledged": true`) || !res[0].Write {
		t.Fatalf("create: %s", rowLines(res[0]))
	}
	if strings.Contains(rowLines(res[1]), `"errors": true`) {
		t.Fatalf("bulk: %s", rowLines(res[1]))
	}

	// Tree.
	dbs, err := h.Databases(ctx)
	if err != nil || len(dbs) != 1 || dbs[0].Name != info.Database {
		t.Fatalf("databases %+v %v", dbs, err)
	}
	objs, err := h.Objects(ctx, dbs[0].Name, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]dbx.Object{}
	for _, o := range objs {
		kinds[o.Name] = o
		if o.System {
			t.Fatalf("system object without system: %+v", o)
		}
	}
	if o := kinds[index]; o.Kind != dbx.KindTable || o.Rows != 1200 || o.SizeBytes <= 0 {
		t.Fatalf("index %+v", o)
	}
	if o := kinds[alias]; o.Kind != dbx.KindView || o.Comment != "alias of "+index {
		t.Fatalf("alias %+v", o)
	}

	d, err := h.Describe(ctx, "", "", index)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for _, c := range d.Columns {
		cols = append(cols, c.Name+":"+c.Type)
	}
	if strings.Join(cols, " ") != "_id:keyword meta:object meta.color:keyword n:long name:text name.raw:keyword tags:keyword" ||
		!strings.Contains(d.DDL, `"mappings"`) || d.Kind != dbx.KindTable {
		t.Fatalf("describe %v %+v", cols, d)
	}
	if d, err = h.Describe(ctx, "", "", alias); err != nil || d.Kind != dbx.KindView || len(d.Columns) != 7 {
		t.Fatalf("describe alias %+v %v", d, err)
	}

	page, err := h.Browse(ctx, "", "", index, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	cols = cols[:0]
	for _, c := range page.Columns {
		cols = append(cols, c.Name)
	}
	if strings.Join(cols, " ") != "_id meta.color n name tags" || len(page.Rows) != 5 {
		t.Fatalf("browse %v rows=%d", cols, len(page.Rows))
	}
	if page, err = h.Browse(ctx, "", "", alias, 0, 2); err != nil || page.Columns[1].Name != "_index" || *page.Rows[0][1] != index {
		t.Fatalf("browse alias %+v %v", page, err)
	}

	// Searches come back as a grid, aggregations as JSON.
	r := mustRun(t, sess, `GET /`+index+`/_search
{ "query": { "term": { "meta.color": "red" } }, "size": 3, "sort": [ { "n": "asc" } ] }`)[0]
	if r.Note != "600 hits" || len(r.Rows) != 3 || r.Columns[0].Name != "_index" || r.Columns[1].Name != "_id" ||
		*r.Rows[0][1] != "2" || r.Write {
		t.Fatalf("search %+v %v", r, rowText(r.Rows[0]))
	}
	r = mustRun(t, sess, `POST /`+index+`/_search
{ "size": 0, "aggs": { "c": { "terms": { "field": "meta.color" } } } }`)[0]
	if r.Columns[0].Name != "output" || !strings.Contains(rowLines(r), `"key": "blue"`) || r.Write {
		t.Fatalf("aggs %+v", r)
	}
	r = mustRun(t, sess, "GET _cat/indices/"+index)[0]
	if len(r.Rows) != 1 || r.Columns[0].Name != "health" {
		t.Fatalf("cat %+v", r)
	}
	r = mustRun(t, sess, "HEAD /"+index)[0]
	if *r.Rows[0][0] != "200 OK" {
		t.Fatalf("head %+v", r)
	}
	r = sess.Run(ctx, "GET /"+index+"_missing/_search", dbx.RunOptions{})[0]
	if !strings.Contains(r.Error, "index_not_found_exception") {
		t.Fatalf("missing index %+v", r)
	}

	// SQL pages through the cursor and stops at the bound.
	r = sess.Run(ctx, "SELECT n, name FROM "+index+" WHERE n > 0", dbx.RunOptions{MaxRows: 5000})[0]
	if r.Error != "" || len(r.Rows) != 1200 || r.Truncated || r.Columns[0].Name != "n" {
		t.Fatalf("sql all: %s rows=%d truncated=%v", r.Error, len(r.Rows), r.Truncated)
	}
	r = sess.Run(ctx, "SELECT n FROM "+index, dbx.RunOptions{MaxRows: 150})[0]
	if r.Error != "" || len(r.Rows) != 150 || !r.Truncated {
		t.Fatalf("sql bounded: %s rows=%d truncated=%v", r.Error, len(r.Rows), r.Truncated)
	}
	r = sess.Run(ctx, "SELECT meta.color, count(*) AS c FROM "+index+" GROUP BY meta.color", dbx.RunOptions{})[0]
	if r.Error != "" || len(r.Rows) != 2 {
		t.Fatalf("sql group: %s %+v", r.Error, r.Rows)
	}
	r = sess.Run(ctx, "SELEC 1", dbx.RunOptions{})[0]
	if r.Error == "" {
		t.Fatal("bad SQL ran")
	}

	// Read-only refuses writes before they leave, and lets reads through.
	ro := s.open(t, true)
	rs, err := ro.OpenSession(ctx, "ro", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"DELETE /" + index, "POST /" + index + "/_doc\n{}", "POST /" + index + "/_delete_by_query\n{\"query\":{\"match_all\":{}}}"} {
		if r := rs.Run(ctx, w, dbx.RunOptions{})[0]; !strings.Contains(r.Error, "read-only") {
			t.Fatalf("read-only %q: %+v", w, r)
		}
	}
	if r := rs.Run(ctx, "GET /"+index+"/_count", dbx.RunOptions{})[0]; r.Error != "" || !strings.Contains(rowLines(r), "1200") {
		t.Fatalf("read-only count: %+v", r)
	}
	if r := rs.Run(ctx, "SELECT count(*) FROM "+index, dbx.RunOptions{})[0]; r.Error != "" || *r.Rows[0][0] != "1200" {
		t.Fatalf("read-only sql: %+v", r)
	}
}

func rowLines(r *dbx.Result) string {
	var b strings.Builder
	for _, row := range r.Rows {
		b.WriteString(strings.Join(rowText(row), "\t"))
		b.WriteByte('\n')
	}
	return b.String()
}
