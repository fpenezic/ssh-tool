package dbx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ClickHouse is spoken over its HTTP interface (8123, 8443 with TLS): plain
// net/http through Config.Dial, results in TabSeparatedWithNamesAndTypes,
// which is text the way clickhouse-client prints it. A query tab is an
// HTTP session (session_id): SET and USE carry from one run to the next.
// ClickHouse has no transactions, so read-only rests on the keyword check
// and the server's own readonly=2 for the request.

// chSessionTimeout is how long the server keeps an idle query tab's session
// (seconds). Servers cap it at max_session_timeout (3600 by default); past
// it the session starts over with the server's defaults.
const chSessionTimeout = "3600"

// chEngine sends every request through one client: HTTP needs no pool of
// our own, and the tree never waits behind a query tab.
type chEngine struct {
	cfg    Config
	base   string
	client *http.Client

	mu sync.Mutex
	// serverRO is the login's own readonly setting, -1 until known. A login
	// whose profile is already read-only may not be sent readonly or any
	// other setting at all.
	serverRO int
}

func newCH(cfg Config) *chEngine {
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
		// Under the server's keep_alive_timeout (10 s by default): a POST is
		// not retried on a connection the server has just closed.
		IdleConnTimeout:     8 * time.Second,
		TLSHandshakeTimeout: 20 * time.Second,
	}
	return &chEngine{
		cfg:      cfg,
		base:     scheme + "://" + cfg.Addr + "/",
		client:   &http.Client{Transport: tr, CheckRedirect: noRedirect},
		serverRO: -1,
	}
}

// chReq is one HTTP request.
type chReq struct {
	query    string
	database string
	session  string
	queryID  string
	readOnly bool
}

// readOnlyLevel learns the login's readonly setting once.
func (e *chEngine) readOnlyLevel(ctx context.Context) (int, error) {
	e.mu.Lock()
	ro := e.serverRO
	e.mu.Unlock()
	if ro >= 0 {
		return ro, nil
	}
	rows, err := e.rows(ctx, `SELECT getSetting('readonly')`)
	if err != nil {
		return 0, err
	}
	if len(rows) == 1 && len(rows[0]) == 1 && rows[0][0] != nil {
		ro, _ = strconv.Atoi(*rows[0][0])
	}
	e.mu.Lock()
	e.serverRO = ro
	e.mu.Unlock()
	return ro, nil
}

// do sends one request and returns the response of a 200; any other status
// is the server's error text.
func (e *chEngine) do(ctx context.Context, r chReq) (*http.Response, error) {
	v := url.Values{}
	if r.database == "" && r.session == "" {
		// Outside a session, the connection's database (Info reports it).
		r.database = e.cfg.Database
	}
	if r.database != "" {
		v.Set("database", r.database)
	}
	if r.session != "" {
		v.Set("session_id", r.session)
		v.Set("session_timeout", chSessionTimeout)
	}
	if r.queryID != "" {
		v.Set("query_id", r.queryID)
	}
	if r.readOnly {
		ro, err := e.readOnlyLevel(ctx)
		if err != nil {
			return nil, err
		}
		if ro == 0 && r.readOnly {
			// 2, not 1: the query may still carry its own SETTINGS, but not
			// readonly itself.
			v.Set("readonly", "2")
		}
	}
	u := e.base
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(r.query))
	if err != nil {
		return nil, err
	}
	// Credentials go in headers, never the URL.
	req.Header.Set("X-ClickHouse-User", e.cfg.User)
	req.Header.Set("X-ClickHouse-Key", e.cfg.Password)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", e.cfg.AppName)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = resp.Status
		}
		return nil, chErr(msg)
	}
	return resp, nil
}

// rows runs one of our own reads and returns every row as text. The query
// must not carry a FORMAT clause.
func (e *chEngine) rows(ctx context.Context, q string) ([][]*string, error) {
	resp, err := e.do(ctx, chReq{query: q + "\nFORMAT TabSeparated"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r := chRead(resp.Body, MaxRowsCeiling, false, nil)
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r.Rows, nil
}

func (e *chEngine) info(ctx context.Context) (*ServerInfo, error) {
	rows, err := e.rows(ctx, `SELECT version(), currentUser(), currentDatabase(), getSetting('readonly')`)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 4 {
		return nil, fmt.Errorf("unexpected server reply")
	}
	ro, _ := strconv.Atoi(str(rows[0][3]))
	e.mu.Lock()
	e.serverRO = ro
	e.mu.Unlock()
	return &ServerInfo{Engine: EngineClickHouse, Version: str(rows[0][0]), User: str(rows[0][1]), Database: str(rows[0][2])}, nil
}

var chSystemDBs = map[string]bool{"system": true, "information_schema": true}

func (e *chEngine) databases(ctx context.Context) ([]Database, error) {
	rows, err := e.rows(ctx, `
		SELECT d.name, toInt64(p.b)
		FROM system.databases d
		LEFT JOIN (SELECT database, sum(bytes_on_disk) AS b FROM system.parts WHERE active GROUP BY database) p
		ON p.database = d.name ORDER BY d.name`)
	if err != nil {
		// A login without access to system.parts still sees its databases,
		// without sizes.
		if rows, err = e.rows(ctx, `SELECT name, -1 FROM system.databases ORDER BY name`); err != nil {
			return nil, err
		}
	}
	out := make([]Database, 0, len(rows))
	for _, r := range rows {
		d := Database{Name: str(r[0]), SizeBytes: num(r[1])}
		d.System = chSystemDBs[strings.ToLower(d.Name)]
		out = append(out, d)
	}
	return out, nil
}

func (e *chEngine) objects(ctx context.Context, database string, _ bool) ([]Object, error) {
	rows, err := e.rows(ctx, `
		SELECT name, engine,
		       if(isNull(total_rows), -1, toInt64(total_rows)),
		       if(isNull(total_bytes), -1, toInt64(total_bytes)),
		       comment
		FROM system.tables WHERE database = `+chString(database)+` AND NOT is_temporary ORDER BY name`)
	if err != nil {
		return nil, err
	}
	sys := chSystemDBs[strings.ToLower(database)]
	out := make([]Object, 0, len(rows))
	for _, r := range rows {
		o := Object{Name: str(r[0]), Kind: chKind(str(r[1])), Rows: num(r[2]), SizeBytes: num(r[3]), Comment: str(r[4]), System: sys}
		out = append(out, o)
	}
	return out, nil
}

func chKind(engine string) string {
	switch engine {
	case "View", "LiveView", "WindowView":
		return KindView
	case "MaterializedView":
		return KindMatView
	}
	return KindTable
}

func (e *chEngine) describe(ctx context.Context, database, _, name string) (*TableDetail, error) {
	where := `database = ` + chString(database) + ` AND name = ` + chString(name)
	rows, err := e.rows(ctx, `SELECT engine, create_table_query, primary_key, sorting_key FROM system.tables WHERE `+where)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s.%s not found", database, name)
	}
	t := rows[0]
	d := &TableDetail{Name: name, Kind: chKind(str(t[0])), DDL: str(t[1])}
	if pretty, err := e.rows(ctx, `SELECT formatQuery(`+chString(d.DDL)+`)`); err == nil && len(pretty) == 1 {
		// formatQuery came with 23.x; older servers keep the one-line text.
		d.DDL = str(pretty[0][0])
	}
	if d.DDL != "" {
		d.DDL += ";"
	}
	pk, sk := str(t[2]), str(t[3])

	cols, err := e.rows(ctx, `
		SELECT name, type, default_kind, default_expression, comment, is_in_primary_key
		FROM system.columns WHERE database = `+chString(database)+` AND table = `+chString(name)+` ORDER BY position`)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		col := Column{Name: str(c[0]), Type: str(c[1]), Comment: str(c[4])}
		col.Nullable = strings.HasPrefix(col.Type, "Nullable(")
		switch kind := str(c[2]); kind {
		case "":
		case "DEFAULT":
			def := str(c[3])
			col.Default = &def
		default:
			// MATERIALIZED, ALIAS, EPHEMERAL: computed, not a default.
			col.Extra = kind + " " + str(c[3])
		}
		if str(c[5]) == "1" {
			col.Key = "PRI"
		}
		d.Columns = append(d.Columns, col)
	}

	if pk != "" {
		d.Indexes = append(d.Indexes, Index{Name: "PRIMARY KEY", Columns: splitTop(pk), Primary: true})
	}
	if sk != "" && sk != pk {
		d.Indexes = append(d.Indexes, Index{Name: "ORDER BY", Columns: splitTop(sk)})
	}
	skip, err := e.rows(ctx, `
		SELECT name, type, expr, granularity FROM system.data_skipping_indices
		WHERE database = `+chString(database)+` AND table = `+chString(name)+` ORDER BY name`)
	if err == nil {
		for _, s := range skip {
			d.Indexes = append(d.Indexes, Index{
				Name:    str(s[0]),
				Columns: []string{str(s[2])},
				Def:     "TYPE " + str(s[1]) + " GRANULARITY " + str(s[3]),
			})
		}
	}
	return d, nil
}

// splitTop splits a key expression ("kind, toDate(ts)") on its top-level
// commas.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '\'', '`', '"':
			i = quoted(s, i, s[i], true) - 1
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

func (e *chEngine) browse(ctx context.Context, database, _, name string, offset, limit int) (*Result, error) {
	rows, err := e.rows(ctx, `SELECT sorting_key FROM system.tables WHERE database = `+chString(database)+` AND name = `+chString(name))
	if err != nil {
		return nil, err
	}
	q := "SELECT * FROM " + chIdent(database) + "." + chIdent(name)
	if len(rows) == 1 && str(rows[0][0]) != "" {
		// The server's own expression, so it reads in key order.
		q += " ORDER BY " + str(rows[0][0])
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	start := time.Now()
	resp, err := e.do(ctx, chReq{query: q + "\nFORMAT TabSeparatedWithNamesAndTypes"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r := chRead(resp.Body, limit, true, nil)
	r.Statement = q
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

func (e *chEngine) session(ctx context.Context, database string) (session, error) {
	s := &chSession{e: e, id: "ssh-tool-" + uuid.NewString(), db: database}
	// One round trip up front, so a wrong database or login fails the open
	// rather than the first run.
	resp, err := e.do(ctx, chReq{query: "SELECT 1", database: database, session: s.id})
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return s, nil
}

func (e *chEngine) close() { e.client.CloseIdleConnections() }

// chSession is a query tab: an HTTP session on the server. A run is tied to
// its own context: ending it closes the request, which the server answers by
// stopping the query, and the session survives.
type chSession struct {
	e  *chEngine
	id string

	mu     sync.Mutex
	db     string // the current database, as USE leaves it
	stop   context.CancelFunc
	runQID string
}

func (s *chSession) run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result {
	if err := ctx.Err(); err != nil {
		return errResult(err)
	}
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	qid := uuid.NewString()
	s.mu.Lock()
	db := s.db
	s.stop, s.runQID = stop, qid
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.stop, s.runQID = nil, ""
		s.mu.Unlock()
	}()

	lead := leadKeyword(EngineClickHouse, stmt)
	// A statement with its own FORMAT gets its output back as lines.
	table := returnsRows(EngineClickHouse, stmt) && !chHasFormat(stmt)
	q := stmt
	if table {
		q += "\nFORMAT TabSeparatedWithNamesAndTypes"
	}
	// The caller giving up (an MCP call, a timeout) stops the query on the
	// server as Stop does.
	unhook := context.AfterFunc(ctx, func() { s.kill(qid) })
	defer unhook()
	resp, err := s.doLocked(rctx, chReq{query: q, database: db, session: s.id, queryID: qid, readOnly: readOnly})
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// Not the request URL (session and query ids) in the message.
			err = ue.Err
		}
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			err = errors.New("query stopped")
		}
		return errResult(err)
	}
	defer resp.Body.Close()
	var onCut func()
	if IsRead(EngineClickHouse, stmt) {
		onCut = func() { stop(); s.kill(qid) }
	}
	var r *Result
	if table {
		r = chRead(resp.Body, maxRows, true, onCut)
	} else {
		r = chReadLines(resp.Body, maxRows, onCut)
		if !r.HasRows && lead == "INSERT" {
			r.RowsAffected = chWritten(resp.Header.Get("X-ClickHouse-Summary"))
		}
	}
	if r.Error == "" && lead == "USE" {
		s.followUse(ctx)
	}
	return r
}

// chFormatRE is a FORMAT clause: the name of an output format, at the end
// of the statement or before its SETTINGS. format('{}', x) is a function.
var chFormatRE = regexp.MustCompile(`(?i)\bFORMAT\s+[A-Za-z_][A-Za-z0-9_]*\s*(SETTINGS\b|$)`)

func chHasFormat(stmt string) bool {
	return chFormatRE.MatchString(strings.TrimSpace(codeOnly(EngineClickHouse, stmt)))
}

// followUse reads back the database a USE switched to: every request names
// the session's database, which would otherwise undo the USE.
func (s *chSession) followUse(ctx context.Context) {
	resp, err := s.e.do(ctx, chReq{query: "SELECT currentDatabase()\nFORMAT TabSeparated", session: s.id})
	if err != nil {
		return
	}
	defer resp.Body.Close()
	r := chRead(resp.Body, 1, false, nil)
	if r.Error == "" && len(r.Rows) == 1 && r.Rows[0][0] != nil {
		s.mu.Lock()
		s.db = *r.Rows[0][0]
		s.mu.Unlock()
	}
}

// doLocked sends a session request, waiting out a session the server still
// holds for a query that was stopped a moment ago (the kill is
// asynchronous). Runs are serialised on our side, so a locked session is
// never another run of ours.
func (s *chSession) doLocked(ctx context.Context, r chReq) (*http.Response, error) {
	for i := 0; ; i++ {
		resp, err := s.e.do(ctx, r)
		if err == nil || i == 25 || !strings.Contains(err.Error(), "SESSION_IS_LOCKED") {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// cancel closes the running request and kills the query.
func (s *chSession) cancel() {
	s.mu.Lock()
	stop, qid := s.stop, s.runQID
	s.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	s.kill(qid)
}

// kill asks the server to stop a query. Closing the request is not enough:
// a query that has not started sending rows (an aggregation) runs on after
// the client is gone, and holds its session. A login without the grant to
// kill gets an error here, which is dropped; its query runs out.
func (s *chSession) kill(qid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if resp, err := s.e.do(ctx, chReq{query: "KILL QUERY WHERE query_id = " + chString(qid) + " ASYNC"}); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func (s *chSession) inTx() bool { return false }

func (s *chSession) close() {}

// chRead reads a TabSeparated result; with header, the first two lines are
// the column names and types. Past the bounds onCut stops the query (the
// request is closed); with no onCut the rest is read and dropped.
func chRead(body io.Reader, maxRows int, header bool, onCut func()) *Result {
	r := &Result{RowsAffected: -1}
	br := bufio.NewReaderSize(body, 64<<10)
	rr := &rowReader{r: r, maxRows: maxRows}
	ncols := -1
	if header {
		names, err := chLine(br)
		if err == io.EOF && len(names) == 0 {
			return r
		}
		if err != nil && err != io.EOF {
			r.Error = err.Error()
			return r
		}
		if msg, ok := chException(br, names, -1); ok {
			r.Error = msg
			return r
		}
		types, err := chLine(br)
		if err != nil && err != io.EOF {
			r.Error = err.Error()
			return r
		}
		nf, tf := chFields(names), chFields(types)
		for i, n := range nf {
			c := ResultColumn{Name: str(n)}
			if i < len(tf) {
				c.Type = str(tf[i])
			}
			r.Columns = append(r.Columns, c)
		}
		ncols = len(nf)
		r.HasRows = true
	}
	for {
		line, err := chLine(br)
		if err == nil || len(line) > 0 {
			if msg, ok := chException(br, line, ncols); ok {
				r.Error = msg
				return r
			}
			if rr.stopped {
				if onCut != nil {
					break
				}
			} else {
				f := chFields(line)
				raw := make([][]byte, len(f))
				for i, v := range f {
					if v != nil {
						raw[i] = []byte(chText(*v))
					}
				}
				if !rr.add(raw) && onCut != nil {
					onCut()
					break
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if rr.stopped {
				break
			}
			if errors.Is(err, context.Canceled) {
				// Stop while rows were arriving: what came is not the result.
				err = errors.New("query stopped")
			}
			r.Error = err.Error()
			return r
		}
	}
	if !rr.stopped {
		r.RowsAffected = int64(len(r.Rows))
	}
	return r
}

// chReadLines reads the output of a statement with its own FORMAT (or of a
// command) as one "output" column, a row per line. No output is no result
// set.
func chReadLines(body io.Reader, maxRows int, onCut func()) *Result {
	r := &Result{RowsAffected: -1}
	br := bufio.NewReaderSize(body, 64<<10)
	rr := &rowReader{r: r, maxRows: maxRows}
	for {
		line, err := chLine(br)
		if err == nil || len(line) > 0 {
			if msg, ok := chException(br, line, -1); ok {
				r.Error = msg
				return r
			}
			if !r.HasRows {
				r.HasRows = true
				r.Columns = []ResultColumn{{Name: "output", Type: "String"}}
			}
			if !rr.stopped && !rr.add([][]byte{[]byte(chText(string(line)))}) && onCut != nil {
				onCut()
				break
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if rr.stopped {
				break
			}
			if errors.Is(err, context.Canceled) {
				// Stop while rows were arriving: what came is not the result.
				err = errors.New("query stopped")
			}
			r.Error = err.Error()
			return r
		}
	}
	if r.HasRows && !rr.stopped {
		r.RowsAffected = int64(len(r.Rows))
	}
	return r
}

// chException recognises an error the server wrote into a result it had
// already started: a "__exception__" line followed by the message (24.x
// and later), or, on older servers, a "Code: N. DB::Exception" line where a
// row of ncols fields was due. The rest of the body is the message. With
// ncols < 0 (output lines, our own reads) the width says nothing, and a
// log's exception column holds such lines as data: there the bare line
// counts only as the last one of the body, where an old server leaves it.
func chException(br *bufio.Reader, line []byte, ncols int) (string, bool) {
	s := string(line)
	if s == "__exception__" {
		rest, err := io.ReadAll(io.LimitReader(br, 64<<10))
		if errors.Is(err, context.Canceled) {
			return "query stopped", true
		}
		return chErr(strings.TrimSpace(string(rest))).Error(), true
	}
	if !strings.HasPrefix(s, "Code: ") || !strings.Contains(s, "DB::Exception") {
		return "", false
	}
	last := false
	if ncols < 0 {
		_, err := br.Peek(1)
		// A Stop while peeking is not "the last line": it is the stop.
		if errors.Is(err, context.Canceled) {
			return "query stopped", true
		}
		last = err != nil
	}
	if (ncols >= 0 && len(chFields(line)) != ncols) || last {
		rest, err := io.ReadAll(io.LimitReader(br, 64<<10))
		if errors.Is(err, context.Canceled) {
			return "query stopped", true
		}
		return chErr(strings.TrimSpace(s + "\n" + string(rest))).Error(), true
	}
	return "", false
}

// chLine reads one line without its newline. A line past MaxResultBytes is
// an error: the result would be cut anyway.
func chLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		part, err := br.ReadSlice('\n')
		buf = append(buf, part...)
		if len(buf) > MaxResultBytes {
			return nil, fmt.Errorf("a result line is over %d MB", MaxResultBytes>>20)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if n := len(buf); n > 0 && buf[n-1] == '\n' {
			buf = buf[:n-1]
		}
		return buf, err
	}
}

// chFields splits a TabSeparated line and unescapes each field; \N alone is
// NULL.
func chFields(line []byte) []*string {
	parts := bytes.Split(line, []byte{'\t'})
	out := make([]*string, len(parts))
	for i, p := range parts {
		if string(p) == `\N` {
			continue
		}
		v := chUnescape(p)
		out[i] = &v
	}
	return out
}

func chUnescape(p []byte) string {
	if bytes.IndexByte(p, '\\') < 0 {
		return string(p)
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c != '\\' || i+1 == len(p) {
			b.WriteByte(c)
			continue
		}
		i++
		switch p[i] {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '0':
			b.WriteByte(0)
		case 'a':
			b.WriteByte('\a')
		case 'v':
			b.WriteByte('\v')
		default:
			b.WriteByte(p[i])
		}
	}
	return b.String()
}

// chText shows a String holding bytes that are not UTF-8 as hex, the way the
// other engines show binary.
func chText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return "0x" + hexString([]byte(s))
}

// chWritten reads written_rows from the X-ClickHouse-Summary header; -1
// when it is not there.
func chWritten(summary string) int64 {
	var sum struct {
		WrittenRows string `json:"written_rows"`
	}
	if summary == "" || json.Unmarshal([]byte(summary), &sum) != nil {
		return -1
	}
	n, err := strconv.ParseInt(sum.WrittenRows, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// chErr trims the server's version tail off an error.
func chErr(msg string) error {
	if i := strings.LastIndex(msg, " (version "); i > 0 && strings.HasSuffix(msg, ")") {
		msg = msg[:i]
	}
	return errors.New(msg)
}

// chString quotes a string literal.
func chString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// chIdent quotes an identifier.
func chIdent(s string) string {
	return "`" + strings.NewReplacer(`\`, `\\`, "`", "\\`").Replace(s) + "`"
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func num(p *string) int64 {
	if p == nil {
		return -1
	}
	n, err := strconv.ParseInt(*p, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
