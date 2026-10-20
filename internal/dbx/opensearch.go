package dbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// OpenSearch and Elasticsearch are spoken over their REST API with plain
// net/http through Config.Dial. The cluster is the one "database" of the
// tree, its indices, aliases and data streams the objects. A query tab runs
// Kibana console blocks ("GET /logs/_search" and a JSON body) and SQL
// statements, which go to the cluster's SQL endpoint (_plugins/_sql on
// OpenSearch, _sql on Elasticsearch) with cursor paging. There is no server
// session and no transaction: read-only rests on the method and endpoint
// check (osRead), so a read-only role on the cluster is the real guard.

// osSQLPage is the fetch size of a SQL page.
const osSQLPage = 1000

type osEngine struct {
	cfg    Config
	base   *url.URL
	client *http.Client

	mu sync.Mutex
	// flavor is "opensearch" or "elasticsearch", "" until GET / answered.
	flavor  string
	cluster string
}

func newOS(cfg Config) *osEngine {
	scheme := "http"
	tlsCfg := cfg.tlsConfig()
	if tlsCfg != nil {
		scheme = "https"
	}
	dial := cfg.dialer()
	tr := &http.Transport{
		Proxy: nil, // the address is reached through Dial, never a proxy
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
		TLSClientConfig:     tlsCfg,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 20 * time.Second,
	}
	return &osEngine{
		cfg:    cfg,
		base:   &url.URL{Scheme: scheme, Host: cfg.Addr},
		client: &http.Client{Transport: tr, CheckRedirect: noRedirect},
	}
}

// noRedirect returns a 3xx as it is: every request goes to cfg.Addr only,
// the read-only check saw this method and path, and credentials stay with
// the server they were meant for.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// osResp is a response read whole (up to MaxResultBytes).
type osResp struct {
	status int
	ctype  string
	body   []byte
}

func (r *osResp) json() bool { return strings.Contains(r.ctype, "json") }

// do sends one request. path may carry a query string; it must be a path,
// never another host.
func (e *osEngine) do(ctx context.Context, method, path string, body []byte, ctype string) (*osResp, error) {
	ref, err := url.Parse(path)
	if err != nil || ref.Scheme != "" || ref.Host != "" || ref.User != nil {
		return nil, fmt.Errorf("bad request path %q", path)
	}
	u := *e.base
	u.Path = ref.Path
	u.RawPath = ref.RawPath
	u.RawQuery = ref.RawQuery
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	switch {
	case e.cfg.User != "":
		req.SetBasicAuth(e.cfg.User, e.cfg.Password)
	case e.cfg.Password != "":
		// No user: the password is an API key (Elasticsearch's encoded
		// id:key form).
		req.Header.Set("Authorization", "ApiKey "+e.cfg.Password)
	}
	if body != nil {
		if ctype == "" {
			ctype = "application/json"
		}
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", e.cfg.AppName)
	resp, err := e.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// Not the request URL in the message.
			err = ue.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResultBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxResultBytes {
		return nil, fmt.Errorf("the response is over %d MB", MaxResultBytes>>20)
	}
	return &osResp{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: b}, nil
}

// call sends a request of ours and decodes a JSON answer into out; a
// status past 299 is the server's error.
func (e *osEngine) call(ctx context.Context, method, path string, body any, out any) error {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	r, err := e.do(ctx, method, path, b, "")
	if err != nil {
		return err
	}
	if r.status > 299 {
		return osError(r)
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(r.body))
	dec.UseNumber()
	return dec.Decode(out)
}

// osError is the server's error: type and reason (OpenSearch SQL adds
// details), or the body itself.
func osError(r *osResp) error {
	var v struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(r.body, &v) == nil && len(v.Error) > 0 {
		var e struct {
			Type    string `json:"type"`
			Reason  string `json:"reason"`
			Details string `json:"details"`
			Caused  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"caused_by"`
		}
		if json.Unmarshal(v.Error, &e) == nil && (e.Type != "" || e.Reason != "") {
			msg := e.Reason
			if e.Type != "" {
				msg = e.Type + ": " + msg
			}
			if e.Details != "" && e.Details != e.Reason {
				msg += " - " + e.Details
			}
			if e.Caused != nil && e.Caused.Reason != "" && e.Caused.Reason != e.Reason {
				msg += " (" + e.Caused.Reason + ")"
			}
			return errors.New(msg)
		}
		var s string
		if json.Unmarshal(v.Error, &s) == nil && s != "" {
			return errors.New(s)
		}
	}
	msg := strings.TrimSpace(string(r.body))
	if len(msg) > 2000 {
		msg = truncUTF8(msg, 2000) + "..."
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d %s", r.status, http.StatusText(r.status))
	}
	return errors.New(msg)
}

// root reads GET / once: the flavor and the cluster name.
func (e *osEngine) root(ctx context.Context) (version string, err error) {
	var v struct {
		Cluster string `json:"cluster_name"`
		Version struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
		} `json:"version"`
	}
	if err := e.call(ctx, http.MethodGet, "/", nil, &v); err != nil {
		return "", err
	}
	flavor, name := "elasticsearch", "Elasticsearch"
	if v.Version.Distribution != "" {
		flavor, name = v.Version.Distribution, "OpenSearch"
	}
	e.mu.Lock()
	e.flavor, e.cluster = flavor, v.Cluster
	e.mu.Unlock()
	return name + " " + v.Version.Number, nil
}

func (e *osEngine) known(ctx context.Context) (flavor, cluster string, err error) {
	e.mu.Lock()
	flavor, cluster = e.flavor, e.cluster
	e.mu.Unlock()
	if flavor != "" {
		return flavor, cluster, nil
	}
	if _, err := e.root(ctx); err != nil {
		return "", "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.flavor, e.cluster, nil
}

func (e *osEngine) info(ctx context.Context) (*ServerInfo, error) {
	version, err := e.root(ctx)
	if err != nil {
		return nil, err
	}
	flavor, cluster, _ := e.known(ctx)
	user := e.cfg.User
	// Who the cluster says we are; a login without the right to ask keeps
	// the configured name.
	if flavor == "elasticsearch" {
		var a struct {
			Username string `json:"username"`
		}
		if e.call(ctx, http.MethodGet, "/_security/_authenticate", nil, &a) == nil && a.Username != "" {
			user = a.Username
		}
	} else {
		var a struct {
			UserName string `json:"user_name"`
		}
		if e.call(ctx, http.MethodGet, "/_plugins/_security/authinfo", nil, &a) == nil && a.UserName != "" {
			user = a.UserName
		}
	}
	return &ServerInfo{Engine: EngineOpenSearch, Version: version, User: user, Database: cluster}, nil
}

func (e *osEngine) databases(ctx context.Context) ([]Database, error) {
	_, cluster, err := e.known(ctx)
	if err != nil {
		return nil, err
	}
	if cluster == "" {
		cluster = "cluster"
	}
	return []Database{{Name: cluster, SizeBytes: -1}}, nil
}

func (e *osEngine) objects(ctx context.Context, _ string, system bool) ([]Object, error) {
	var idx []struct {
		Index  string `json:"index"`
		Docs   string `json:"docs.count"`
		Size   string `json:"store.size"`
		Status string `json:"status"`
		Health string `json:"health"`
	}
	if err := e.call(ctx, http.MethodGet, "/_cat/indices?format=json&bytes=b&expand_wildcards=all&h=index,docs.count,store.size,status,health", nil, &idx); err != nil {
		return nil, err
	}
	var out []Object
	for _, i := range idx {
		sys := strings.HasPrefix(i.Index, ".")
		if sys && !system {
			continue
		}
		o := Object{Name: i.Index, Kind: KindTable, Rows: num(&i.Docs), SizeBytes: num(&i.Size), System: sys}
		switch {
		case i.Status == "close":
			o.Comment = "closed"
		case i.Health != "" && i.Health != "green":
			o.Comment = "health " + i.Health
		}
		out = append(out, o)
	}
	// Aliases and data streams are best effort: a login may see indices
	// without the right to list either.
	var al []struct {
		Alias string `json:"alias"`
		Index string `json:"index"`
	}
	if e.call(ctx, http.MethodGet, "/_cat/aliases?format=json&expand_wildcards=all&h=alias,index", nil, &al) == nil {
		targets := map[string][]string{}
		var names []string
		for _, a := range al {
			if _, ok := targets[a.Alias]; !ok {
				names = append(names, a.Alias)
			}
			targets[a.Alias] = append(targets[a.Alias], a.Index)
		}
		for _, n := range names {
			sys := strings.HasPrefix(n, ".")
			if sys && !system {
				continue
			}
			out = append(out, Object{Name: n, Kind: KindView, Rows: -1, SizeBytes: -1, System: sys,
				Comment: "alias of " + strings.Join(targets[n], ", ")})
		}
	}
	var ds struct {
		Streams []struct {
			Name string `json:"name"`
		} `json:"data_streams"`
	}
	if e.call(ctx, http.MethodGet, "/_data_stream?expand_wildcards=all", nil, &ds) == nil {
		for _, s := range ds.Streams {
			sys := strings.HasPrefix(s.Name, ".")
			if sys && !system {
				continue
			}
			out = append(out, Object{Name: s.Name, Kind: KindTable, Rows: -1, SizeBytes: -1, System: sys, Comment: "data stream"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// osObjectType: mapping types whose _source value is an object, which the
// grid shows field by field.
var osObjectType = map[string]bool{"object": true, "flattened": true, "flat_object": true}

// osField is one field of a mapping, flattened to its dotted path.
type osField struct {
	path  string
	typ   string
	extra string
	leaf  bool // holds values in _source (not an object, not a multi-field)
}

// mapping reads the merged mapping of an index, alias or data stream (the
// first index that has a field wins) and whether name is an index itself.
func (e *osEngine) mapping(ctx context.Context, name string) ([]osField, bool, error) {
	var m map[string]struct {
		Mappings struct {
			Properties json.RawMessage `json:"properties"`
		} `json:"mappings"`
	}
	if err := e.call(ctx, http.MethodGet, "/"+url.PathEscape(name)+"/_mapping", nil, &m); err != nil {
		return nil, false, err
	}
	_, isIndex := m[name]
	idx := make([]string, 0, len(m))
	for k := range m {
		idx = append(idx, k)
	}
	sort.Strings(idx)
	seen := map[string]bool{}
	var out []osField
	for _, k := range idx {
		for _, f := range osProps(m[k].Mappings.Properties, "") {
			if !seen[f.path] {
				seen[f.path] = true
				out = append(out, f)
			}
		}
	}
	return out, isIndex, nil
}

// osProps flattens a mapping's properties; objects come before their
// fields, multi-fields after theirs.
func osProps(raw json.RawMessage, prefix string) []osField { return osPropsIn(raw, prefix, false) }

// osPropsIn: inNested, the fields are inside a nested object, whose
// _source value is shown whole.
func osPropsIn(raw json.RawMessage, prefix string, inNested bool) []osField {
	if len(raw) == 0 {
		return nil
	}
	var props map[string]struct {
		Type       string                     `json:"type"`
		Properties json.RawMessage            `json:"properties"`
		Fields     map[string]json.RawMessage `json:"fields"`
		Index      *bool                      `json:"index"`
		Enabled    *bool                      `json:"enabled"`
		Format     string                     `json:"format"`
	}
	if json.Unmarshal(raw, &props) != nil {
		return nil
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []osField
	for _, n := range names {
		p := props[n]
		f := osField{path: prefix + n, typ: p.Type}
		var notes []string
		if p.Format != "" {
			notes = append(notes, "format "+p.Format)
		}
		if (p.Index != nil && !*p.Index) || (p.Enabled != nil && !*p.Enabled) {
			notes = append(notes, "not indexed")
		}
		if f.typ == "" {
			f.typ = "object"
		}
		// An object's values live in its fields (a document's own fields
		// for one without a mapping); a nested one is an array, kept whole.
		f.leaf = !inNested && (f.typ == "nested" || (len(p.Properties) == 0 && !osObjectType[f.typ]))
		f.extra = strings.Join(notes, ", ")
		out = append(out, f)
		if len(p.Properties) > 0 {
			out = append(out, osPropsIn(p.Properties, f.path+".", inNested || f.typ == "nested")...)
		}
		subs := make([]string, 0, len(p.Fields))
		for s := range p.Fields {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		for _, s := range subs {
			var sub struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(p.Fields[s], &sub)
			out = append(out, osField{path: f.path + "." + s, typ: sub.Type, extra: "multi-field of " + f.path})
		}
	}
	return out
}

func (e *osEngine) describe(ctx context.Context, _, _, name string) (*TableDetail, error) {
	fields, isIndex, err := e.mapping(ctx, name)
	if err != nil {
		return nil, err
	}
	d := &TableDetail{Name: name, Kind: KindTable, DDLNote: "GET /" + name}
	if !isIndex {
		d.Kind = KindView
	}
	d.Columns = append(d.Columns, Column{Name: "_id", Type: "keyword", Key: "PRI"})
	for _, f := range fields {
		d.Columns = append(d.Columns, Column{Name: f.path, Type: f.typ, Nullable: true, Extra: f.extra})
	}
	r, err := e.do(ctx, http.MethodGet, "/"+url.PathEscape(name), nil, "")
	if err == nil && r.status < 300 {
		d.DDL = osPretty(r.body)
	}
	return d, nil
}

func (e *osEngine) browse(ctx context.Context, _, _, name string, offset, limit int) (*Result, error) {
	fields, isIndex, err := e.mapping(ctx, name)
	if err != nil {
		return nil, err
	}
	q := map[string]any{"from": offset, "size": limit, "sort": []string{"_doc"}}
	stmt := "POST /" + name + "/_search\n" + osPretty(mustJSON(q))
	start := time.Now()
	r, err := e.do(ctx, http.MethodPost, "/"+url.PathEscape(name)+"/_search", mustJSON(q), "")
	if err != nil {
		return nil, err
	}
	if r.status > 299 {
		return nil, osError(r)
	}
	g := newOSGrid()
	g.col("_id", "keyword")
	if !isIndex {
		g.col("_index", "keyword")
	}
	for _, f := range fields {
		if f.leaf {
			g.col(f.path, f.typ)
		}
	}
	res, ok := g.hits(r.body, limit, !isIndex)
	if !ok {
		return nil, fmt.Errorf("unexpected search response")
	}
	res.Statement = stmt
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

func (e *osEngine) session(ctx context.Context, _ string) (session, error) {
	// One round trip up front, so a wrong address or login fails the open
	// rather than the first run.
	if _, _, err := e.known(ctx); err != nil {
		return nil, err
	}
	return &osSession{e: e}, nil
}

func (e *osEngine) close() { e.client.CloseIdleConnections() }

// osSession is a query tab. The cluster keeps no state for it: a run is
// one request (a SQL statement may page), stopped by closing it, which
// cancels a search on the server.
type osSession struct {
	e *osEngine

	mu   sync.Mutex
	stop context.CancelFunc
}

func (s *osSession) run(ctx context.Context, stmt string, _ bool, maxRows int) *Result {
	if err := ctx.Err(); err != nil {
		return errResult(err)
	}
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.stop = nil
		s.mu.Unlock()
	}()
	var r *Result
	var err error
	if req, ok := osParseREST(stmt); ok {
		r, err = s.e.runREST(rctx, req, maxRows)
	} else {
		r, err = s.e.runSQL(rctx, stmt, maxRows)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			err = errors.New("query stopped")
		}
		return errResult(err)
	}
	return r
}

func (s *osSession) cancel() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *osSession) inTx() bool { return false }

func (s *osSession) close() {}

// osREST is a console block: method, path (with its query string) and
// body (comments dropped, """ strings turned into JSON strings).
type osREST struct {
	method string
	path   string
	body   string
}

// endpoint is the path's first "_" segment past the index name (_all
// skipped) and the one after it: "/logs/_search" is _search, "/_sql/close"
// is _sql close, "/_plugins/_sql" is _plugins _sql.
func (r osREST) endpoint() (string, string) {
	p := r.path
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	segs := strings.FieldsFunc(p, func(c rune) bool { return c == '/' })
	for i, s := range segs {
		if strings.HasPrefix(s, "_") && s != "_all" {
			next := ""
			if i+1 < len(segs) {
				next = segs[i+1]
			}
			return s, next
		}
	}
	return "", ""
}

func (e *osEngine) runREST(ctx context.Context, req osREST, maxRows int) (*Result, error) {
	ep, _ := req.endpoint()
	path := req.path
	if ep == "_cat" && !strings.Contains(path, "format=") {
		// The grid rather than the text table.
		if strings.Contains(path, "?") {
			path += "&format=json"
		} else {
			path += "?format=json"
		}
	}
	var body []byte
	ctype := ""
	if strings.TrimSpace(req.body) != "" {
		if osNDJSON(ep) {
			b, err := osLines(req.body)
			if err != nil {
				return nil, err
			}
			body, ctype = b, "application/x-ndjson"
		} else {
			body = []byte(req.body)
		}
	}
	resp, err := e.do(ctx, req.method, path, body, ctype)
	if err != nil {
		return nil, err
	}
	if req.method == http.MethodHead {
		return osOutput([]string{fmt.Sprintf("%d %s", resp.status, http.StatusText(resp.status))}, maxRows), nil
	}
	if resp.status > 299 {
		var v struct {
			Error json.RawMessage `json:"error"`
		}
		// A 404 that is an answer ({"found": false}) is shown as one.
		if !resp.json() || json.Unmarshal(resp.body, &v) != nil || len(v.Error) > 0 {
			return nil, osError(resp)
		}
	}
	if !resp.json() {
		return osOutput(strings.Split(strings.TrimRight(string(resp.body), "\n"), "\n"), maxRows), nil
	}
	if r, ok := osTable(resp.body, maxRows); ok {
		return r, nil
	}
	return osOutput(strings.Split(osPretty(resp.body), "\n"), maxRows), nil
}

// osNDJSON: endpoints whose body is one JSON document per line.
func osNDJSON(ep string) bool {
	return ep == "_bulk" || ep == "_msearch"
}

// osLines re-encodes a body of JSON documents (pretty-printed ones too)
// one per line.
func osLines(body string) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(body))
	var b bytes.Buffer
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("body: %w", err)
		}
		if err := json.Compact(&b, raw); err != nil {
			return nil, err
		}
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// osTable turns a response into a grid where it has one: search hits (no
// aggregations), a SQL result, or an array of objects (_cat with
// format=json).
func osTable(body []byte, maxRows int) (*Result, bool) {
	t := bytes.TrimSpace(body)
	if len(t) > 0 && t[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(t, &arr) != nil {
			return nil, false
		}
		g := newOSGrid()
		for _, a := range arr {
			if a = bytes.TrimSpace(a); len(a) == 0 || a[0] != '{' {
				return nil, false
			}
		}
		for _, a := range arr {
			if !g.row(nil, a, maxRows) {
				break
			}
		}
		return g.result(), true
	}
	var probe struct {
		Hits *struct {
			Hits json.RawMessage `json:"hits"`
		} `json:"hits"`
		Aggs     json.RawMessage `json:"aggregations"`
		Schema   json.RawMessage `json:"schema"`
		Columns  json.RawMessage `json:"columns"`
		Datarows json.RawMessage `json:"datarows"`
		Rows     json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(t, &probe) != nil {
		return nil, false
	}
	switch {
	case probe.Hits != nil && len(probe.Hits.Hits) > 2 && len(probe.Aggs) == 0:
		return newOSGrid().hits(t, maxRows, true)
	case len(probe.Schema) > 0 && len(probe.Datarows) > 0, len(probe.Columns) > 0 && len(probe.Rows) > 0:
		p, err := osSQLParse(t)
		if err != nil {
			return nil, false
		}
		r := &Result{RowsAffected: -1, HasRows: true, Columns: p.cols}
		rr := &rowReader{r: r, maxRows: maxRows}
		for _, row := range p.rows {
			if !rr.add(row) {
				break
			}
		}
		if !r.Truncated {
			r.RowsAffected = int64(len(r.Rows))
		}
		return r, true
	}
	return nil, false
}

// osOutput is text shown a line per row.
func osOutput(lines []string, maxRows int) *Result {
	r := &Result{RowsAffected: -1, HasRows: true, Columns: []ResultColumn{{Name: "output", Type: "text"}}}
	rr := &rowReader{r: r, maxRows: maxRows}
	for _, l := range lines {
		if !rr.add([][]byte{[]byte(l)}) {
			break
		}
	}
	return r
}

// osGrid builds rows of flattened documents; columns appear in the order
// they are first met.
type osGrid struct {
	r     *Result
	rr    *rowReader
	index map[string]int
	typed map[int]bool
}

func newOSGrid() *osGrid {
	r := &Result{RowsAffected: -1, HasRows: true}
	return &osGrid{r: r, rr: &rowReader{r: r}, index: map[string]int{}, typed: map[int]bool{}}
}

// col adds a column with a known type.
func (g *osGrid) col(name, typ string) int {
	if i, ok := g.index[name]; ok {
		return i
	}
	g.index[name] = len(g.r.Columns)
	g.r.Columns = append(g.r.Columns, ResultColumn{Name: name, Type: typ})
	if typ != "" {
		g.typed[len(g.r.Columns)-1] = true
	}
	return len(g.r.Columns) - 1
}

// row adds a document (meta fields first); false once the bound is hit.
func (g *osGrid) row(meta []osKV, doc json.RawMessage, maxRows int) bool {
	g.rr.maxRows = maxRows
	vals := map[int][]byte{}
	set := func(path string, v *string, kind string) {
		i := g.col(path, "")
		if v == nil {
			return
		}
		if !g.typed[i] && kind != "" {
			g.r.Columns[i].Type = kind
			g.typed[i] = true
		}
		vals[i] = []byte(*v)
	}
	for _, m := range meta {
		set(m.path, m.val, m.kind)
	}
	osFlatten(doc, "", func(path string, v *string, kind string) { set(path, v, kind) })
	raw := make([][]byte, len(g.r.Columns))
	for i, v := range vals {
		raw[i] = v
	}
	return g.rr.add(raw)
}

// result pads rows to the final column count.
func (g *osGrid) result() *Result {
	for i, row := range g.r.Rows {
		for len(row) < len(g.r.Columns) {
			row = append(row, nil)
		}
		g.r.Rows[i] = row
	}
	if !g.r.Truncated {
		g.r.RowsAffected = int64(len(g.r.Rows))
	}
	return g.r
}

// hits reads a search response's hits: _index (withIndex), _id, _score
// when there is one, then the flattened _source.
func (g *osGrid) hits(body []byte, maxRows int, withIndex bool) (*Result, bool) {
	var sr struct {
		Hits struct {
			Total json.RawMessage `json:"total"`
			Hits  []struct {
				Index  string          `json:"_index"`
				ID     string          `json:"_id"`
				Score  json.RawMessage `json:"_score"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if json.Unmarshal(body, &sr) != nil {
		return nil, false
	}
	for _, h := range sr.Hits.Hits {
		id, ix := h.ID, h.Index
		var meta []osKV
		if withIndex {
			meta = append(meta, osKV{path: "_index", val: &ix, kind: "keyword"})
		}
		meta = append(meta, osKV{path: "_id", val: &id, kind: "keyword"})
		if s := strings.TrimSpace(string(h.Score)); s != "" && s != "null" {
			meta = append(meta, osKV{path: "_score", val: &s, kind: "float"})
		}
		if !g.row(meta, h.Source, maxRows) {
			break
		}
	}
	r := g.result()
	if n := osTotal(sr.Hits.Total); n != "" {
		r.Note = n + " hits"
	}
	return r, true
}

// osTotal reads hits.total: a number, or {"value": n, "relation": "gte"}.
func osTotal(raw json.RawMessage) string {
	var t struct {
		Value    json.Number `json:"value"`
		Relation string      `json:"relation"`
	}
	if json.Unmarshal(raw, &t) == nil && t.Value != "" {
		if t.Relation == "gte" {
			return string(t.Value) + "+"
		}
		return string(t.Value)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return string(n)
	}
	return ""
}

type osKV struct {
	path string
	val  *string
	kind string
}

// osFlatten walks a JSON object in document order and reports each value
// under its dotted path. Objects are walked into; an array is one value
// (its JSON), as is anything that is not an object at the top.
func osFlatten(raw json.RawMessage, prefix string, add func(path string, v *string, kind string)) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}
	if raw[0] != '{' {
		name := strings.TrimSuffix(prefix, ".")
		if name == "" {
			name = "value"
		}
		v, kind := osValue(raw)
		add(name, v, kind)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return
		}
		if t := bytes.TrimSpace(val); len(t) > 0 && t[0] == '{' && len(t) > 2 {
			osFlatten(t, prefix+key+".", add)
			continue
		}
		v, kind := osValue(val)
		add(prefix+key, v, kind)
	}
}

// osValue is a JSON value as cell text and the type it suggests.
func osValue(raw json.RawMessage) (*string, string) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil, ""
	}
	var s string
	switch t[0] {
	case '"':
		if json.Unmarshal(t, &s) != nil {
			s = string(t)
		}
		return &s, "text"
	case '[', '{':
		var b bytes.Buffer
		if json.Compact(&b, t) == nil {
			s = b.String()
		} else {
			s = string(t)
		}
		return &s, "json"
	case 't', 'f':
		s = string(t)
		return &s, "boolean"
	}
	s = string(t)
	return &s, "number"
}

// osSQLResult is one page of a SQL answer, in either flavor's shape.
type osSQLResult struct {
	cols   []ResultColumn
	rows   [][][]byte
	cursor string
	total  int64
	size   int64
}

func osSQLParse(body []byte) (*osSQLResult, error) {
	var v struct {
		Schema []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
			Type  string `json:"type"`
		} `json:"schema"`
		Columns []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"columns"`
		Datarows []json.RawMessage `json:"datarows"`
		Rows     []json.RawMessage `json:"rows"`
		Cursor   string            `json:"cursor"`
		Total    int64             `json:"total"`
		Size     int64             `json:"size"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	p := &osSQLResult{cursor: v.Cursor, total: v.Total, size: v.Size}
	for _, c := range v.Schema {
		name := c.Name
		if c.Alias != "" {
			name = c.Alias
		}
		p.cols = append(p.cols, ResultColumn{Name: name, Type: c.Type})
	}
	for _, c := range v.Columns {
		p.cols = append(p.cols, ResultColumn{Name: c.Name, Type: c.Type})
	}
	rows := v.Datarows
	if rows == nil {
		rows = v.Rows
	}
	for _, row := range rows {
		var cells []json.RawMessage
		if err := json.Unmarshal(row, &cells); err != nil {
			return nil, err
		}
		raw := make([][]byte, len(cells))
		for i, c := range cells {
			if s, _ := osValue(c); s != nil {
				raw[i] = []byte(*s)
			}
		}
		p.rows = append(p.rows, raw)
	}
	return p, nil
}

// runSQL sends a statement to the cluster's SQL endpoint and follows the
// cursor until the rows run out or the bound is hit; a cursor left open is
// closed.
func (e *osEngine) runSQL(ctx context.Context, stmt string, maxRows int) (*Result, error) {
	flavor, _, err := e.known(ctx)
	if err != nil {
		return nil, err
	}
	path, closePath := "/_sql?format=json", "/_sql/close"
	if flavor != "elasticsearch" {
		path, closePath = "/_plugins/_sql?format=jdbc", "/_plugins/_sql/close"
	}
	q := strings.TrimSpace(stripComments(EngineOpenSearch, stmt))
	page := min(osSQLPage, maxRows)
	req := map[string]any{"query": q}
	if leadKeyword(EngineOpenSearch, q) == "SELECT" {
		req["fetch_size"] = page
	}
	p, err := e.sqlPage(ctx, path, req)
	if err != nil && req["fetch_size"] != nil {
		// OpenSearch pages only simple queries; an aggregation asked for a
		// page fails, and is asked again whole.
		delete(req, "fetch_size")
		var err2 error
		if p, err2 = e.sqlPage(ctx, path, req); err2 != nil {
			return nil, err
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	r := &Result{RowsAffected: -1, HasRows: true, Columns: p.cols}
	rr := &rowReader{r: r, maxRows: maxRows}
	for {
		for _, row := range p.rows {
			for len(row) < len(r.Columns) {
				row = append(row, nil)
			}
			if !rr.add(row) {
				break
			}
		}
		if p.cursor == "" || rr.stopped {
			break
		}
		cursor := p.cursor
		if p, err = e.sqlPage(ctx, path, map[string]any{"cursor": cursor}); err != nil {
			e.sqlClose(closePath, cursor)
			return nil, err
		}
		if rr.stopped || len(p.rows) == 0 {
			break
		}
	}
	if p.cursor != "" {
		r.Truncated = true
		e.sqlClose(closePath, p.cursor)
	}
	if !r.Truncated && p.total > 0 && len(r.Rows) < int(p.total) && p.cursor == "" && req["fetch_size"] == nil {
		// OpenSearch's size limit (plugins.query.size_limit) cut it.
		r.Truncated = true
	}
	if !r.Truncated {
		r.RowsAffected = int64(len(r.Rows))
	}
	return r, nil
}

func (e *osEngine) sqlPage(ctx context.Context, path string, req map[string]any) (*osSQLResult, error) {
	r, err := e.do(ctx, http.MethodPost, path, mustJSON(req), "")
	if err != nil {
		return nil, err
	}
	if r.status > 299 {
		return nil, osError(r)
	}
	return osSQLParse(r.body)
}

// sqlClose frees a cursor the result did not read to the end.
func (e *osEngine) sqlClose(path, cursor string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = e.do(ctx, http.MethodPost, path, mustJSON(map[string]string{"cursor": cursor}), "")
}

func osPretty(b []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, bytes.TrimSpace(b), "", "  ") != nil {
		return string(b)
	}
	return out.String()
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
