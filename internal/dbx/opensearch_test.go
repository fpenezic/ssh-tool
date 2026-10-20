package dbx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOSSplit(t *testing.T) {
	script := `# logs first
GET /logs/_search
{
  "query": { "match": { "msg": "a; b" } }
}

SELECT host FROM logs; SHOW TABLES LIKE %

post _plugins/_sql
{
  "query": """
SELECT 1;
DELETE FROM x
"""
}
DELETE FROM logs WHERE a = 1
HEAD /logs
`
	got := Split(EngineOpenSearch, script)
	var texts []string
	for _, s := range got {
		texts = append(texts, s.Text)
		if script[s.Start:s.End] != s.Text {
			t.Fatalf("range %d-%d is %q, not %q", s.Start, s.End, script[s.Start:s.End], s.Text)
		}
	}
	want := []string{
		"GET /logs/_search\n{\n  \"query\": { \"match\": { \"msg\": \"a; b\" } }\n}",
		"SELECT host FROM logs",
		"SHOW TABLES LIKE %",
		"post _plugins/_sql\n{\n  \"query\": \"\"\"\nSELECT 1;\nDELETE FROM x\n\"\"\"\n}",
		"DELETE FROM logs WHERE a = 1",
		"HEAD /logs",
	}
	if strings.Join(texts, "\n|\n") != strings.Join(want, "\n|\n") {
		t.Fatalf("got\n%s", strings.Join(texts, "\n|\n"))
	}
	// A block body keeps literals and NDJSON lines.
	got = Split(EngineOpenSearch, "POST /_bulk\n{\"index\":{}}\n{\"ok\":true}\n[\n  null,\n  false\n]")
	if len(got) != 1 {
		t.Fatalf("bulk %+v", got)
	}
}

func TestOSParseREST(t *testing.T) {
	r, ok := osParseREST("get logs/_search?size=1\n# note\n{\"query\": \"\"\"a \"b\"\nc\"\"\"}")
	if !ok || r.method != "GET" || r.path != "/logs/_search?size=1" || r.body != "{\"query\": \"a \\\"b\\\"\\nc\"}" {
		t.Fatalf("%+v", r)
	}
	if _, ok := osParseREST("DELETE FROM logs"); ok {
		t.Fatal("SQL taken as a request")
	}
	if ep, next := (osREST{path: "/_all/_search/scroll"}).endpoint(); ep != "_search" || next != "scroll" {
		t.Fatal(ep, next)
	}
}

func TestOSRead(t *testing.T) {
	read := []string{
		"GET /_cat/indices",
		"HEAD /logs",
		"POST /logs/_search\n{}",
		"POST _search/scroll\n{}",
		"POST /logs/_count",
		"POST /_msearch\n{}\n{}",
		"POST /logs/_explain/1\n{}",
		"POST /_plugins/_sql\n{\"query\": \"SELECT * FROM logs\"}",
		"POST /_sql?format=txt\n{\"query\": \"\"\"\nSHOW TABLES\n\"\"\"}",
		"POST /_sql\n{\"cursor\": \"abc\"}",
		"POST /_plugins/_sql/close\n{\"cursor\": \"abc\"}",
		"POST /_plugins/_ppl\n{\"query\": \"source=logs | head 5\"}",
		"SELECT * FROM logs",
		"SHOW TABLES LIKE %",
		"DESCRIBE TABLES LIKE logs",
	}
	for _, s := range read {
		if !IsRead(EngineOpenSearch, s) || CheckReadOnly(EngineOpenSearch, s) != nil {
			t.Errorf("not read: %q (%v)", s, CheckReadOnly(EngineOpenSearch, s))
		}
	}
	write := []string{
		"PUT /logs",
		"DELETE /logs",
		"PATCH /logs",
		"POST /logs/_doc\n{}",
		"POST /logs/_doc/_search\n{}",
		"POST /_bulk\n{}",
		"POST /logs/_update_by_query\n{}",
		"POST /logs/_delete_by_query\n{}",
		"POST /_reindex\n{}",
		"POST /logs/_close",
		"POST /_plugins/_sql\n{\"query\": \"DELETE FROM logs\"}",
		"POST /_plugins/_sql\n{\"query\": \"SELECT 1\"",
		"POST /_sql\n{}",
		"POST /",
		"DELETE FROM logs WHERE a = 1",
		"SELECT * INTO x FROM logs",
	}
	for _, s := range write {
		if IsRead(EngineOpenSearch, s) || CheckReadOnly(EngineOpenSearch, s) == nil {
			t.Errorf("read: %q", s)
		}
	}
	if err := CheckReadOnly(EngineOpenSearch, "POST /logs/_doc\n{}"); err == nil || !strings.Contains(err.Error(), "POST _doc") {
		t.Fatal(err)
	}
}

func TestOSRedact(t *testing.T) {
	got := RedactSecrets(EngineOpenSearch, `PUT _plugins/_security/api/internalusers/bob
{"password": "s3\"cret", "hash": "$2y$x", "backend_roles": ["a"], "Secret_Key": "k"}`)
	if strings.Contains(got, "s3") || strings.Contains(got, "$2y") || strings.Contains(got, `"k"`) || !strings.Contains(got, `"backend_roles": ["a"]`) {
		t.Fatal(got)
	}
	if got := RedactSecrets(EngineOpenSearch, "SELECT 'PASSWORD' FROM x"); got != "SELECT 'PASSWORD' FROM x" {
		t.Fatal(got)
	}
}

func TestOSTable(t *testing.T) {
	// Search hits: _index, _id, _score, then the source flattened in
	// document order; a later document adds its own columns.
	body := `{"took":1,"hits":{"total":{"value":2,"relation":"eq"},"max_score":1.0,"hits":[
	 {"_index":"shop","_id":"1","_score":1.0,"_source":{"name":"a","meta":{"color":"red","size":{"w":1}},"tags":["x","y"],"n":null}},
	 {"_index":"shop","_id":"2","_score":1.0,"_source":{"name":"b","price":2.50,"ok":true}}]}}`
	r, ok := osTable([]byte(body), 10)
	if !ok {
		t.Fatal("no table")
	}
	var cols []string
	for _, c := range r.Columns {
		cols = append(cols, c.Name+":"+c.Type)
	}
	if strings.Join(cols, " ") != "_index:keyword _id:keyword _score:float name:text meta.color:text meta.size.w:number tags:json n: price:number ok:boolean" {
		t.Fatal(cols)
	}
	if len(r.Rows) != 2 || len(r.Rows[0]) != len(r.Columns) || *r.Rows[0][6] != `["x","y"]` || r.Rows[0][7] != nil ||
		*r.Rows[1][8] != "2.50" || r.Rows[1][4] != nil || r.Note != "2 hits" || r.RowsAffected != 2 {
		t.Fatalf("%+v", r)
	}
	// Bounded.
	r, _ = osTable([]byte(body), 1)
	if !r.Truncated || len(r.Rows) != 1 || r.RowsAffected != -1 {
		t.Fatalf("%+v", r)
	}
	// Aggregations are shown as JSON.
	if _, ok := osTable([]byte(`{"hits":{"hits":[{"_id":"1"}]},"aggregations":{}}`), 10); ok {
		t.Fatal("aggregations as a table")
	}
	// _cat with format=json.
	r, ok = osTable([]byte(`[{"index":"a","docs.count":"3"},{"index":"b","docs.count":"10"}]`), 10)
	if !ok || len(r.Rows) != 2 || r.Columns[1].Name != "docs.count" || *r.Rows[1][1] != "10" {
		t.Fatalf("%+v", r)
	}
	// Both SQL shapes.
	for _, b := range []string{
		`{"schema":[{"name":"n","type":"long"},{"name":"x","alias":"y","type":"text"}],"datarows":[[1,"a"],[2,null]],"total":2,"size":2,"status":200}`,
		`{"columns":[{"name":"n","type":"long"},{"name":"y","type":"text"}],"rows":[[1,"a"],[2,null]]}`,
	} {
		r, ok = osTable([]byte(b), 10)
		if !ok || len(r.Rows) != 2 || r.Columns[1].Name != "y" || r.Columns[0].Type != "long" || *r.Rows[0][0] != "1" || r.Rows[1][1] != nil {
			t.Fatalf("%s: %+v", b, r)
		}
	}
	if _, ok := osTable([]byte(`{"acknowledged":true}`), 10); ok {
		t.Fatal("ack as a table")
	}
}

func TestOSProps(t *testing.T) {
	raw := `{"name":{"type":"text","fields":{"raw":{"type":"keyword"}}},"meta":{"properties":{"color":{"type":"keyword"}}},
	"ts":{"type":"date","format":"epoch_millis"},"blob":{"type":"object","enabled":false},"items":{"type":"nested","properties":{"a":{"type":"long"}}}}`
	var got []string
	for _, f := range osProps([]byte(raw), "") {
		s := f.path + ":" + f.typ
		if f.leaf {
			s += "*"
		}
		if f.extra != "" {
			s += "(" + f.extra + ")"
		}
		got = append(got, s)
	}
	want := "blob:object(not indexed) items:nested* items.a:long meta:object meta.color:keyword* name:text* name.raw:keyword(multi-field of name) ts:date*(format epoch_millis)"
	if strings.Join(got, " ") != want {
		t.Fatal(strings.Join(got, " "))
	}
}

// A redirect is shown, not followed: it could send the request (and its
// credentials) elsewhere, past the read-only check.
func TestOSNoRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed to %s %s", r.Method, r.URL)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/idx/_doc", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	e := newOS(Config{Addr: strings.TrimPrefix(srv.URL, "http://"), User: "u", Password: "p"})
	r, err := e.do(context.Background(), http.MethodPost, "/idx/_search", []byte("{}"), "")
	if err != nil || r.status != http.StatusTemporaryRedirect {
		t.Fatalf("%v %+v", err, r)
	}
}
