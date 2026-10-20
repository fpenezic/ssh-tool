package dbx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Redis (and Valkey, KeyDB) over a small RESP2 client of our own through
// Config.Dial. The numbered databases (db0, db1, ...) are the tree's
// databases; the keys of one, grouped by the part before their first ":",
// are its objects ("user:*"), sampled with SCAN. A query tab is one
// connection and runs one command per line, redis-cli style; SELECT
// carries to the next run. There are no transactions to speak of and no
// read-only session: read-only is a command allowlist (rdReadArgs), so an
// ACL user limited to +@read is the real guard.

// Sampling bounds: the tree and Data never walk a keyspace of millions.
const (
	rdSampleKeys = 10000
	rdScanBudget = 3 * time.Second
	rdPreview    = 20 // elements of a collection shown in its value
)

type rdEngine struct {
	cfg Config

	mu   sync.Mutex
	idle []*rdConn // per database, at most one each
}

func newRD(cfg Config) *rdEngine { return &rdEngine{cfg: cfg} }

// rdDB reads a database name: "db3", "3"; "" is the configured one (or 0).
func (e *rdEngine) rdDB(name string) (int, error) {
	if name == "" {
		name = e.cfg.Database
	}
	n := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "db")
	if n == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(n)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("redis database %q: a number (db0, db1, ...)", name)
	}
	return v, nil
}

// conn takes an idle connection on db or dials one; put hands it back.
func (e *rdEngine) conn(ctx context.Context, db int) (*rdConn, error) {
	e.mu.Lock()
	for i, c := range e.idle {
		if c.db == db {
			e.idle = append(e.idle[:i], e.idle[i+1:]...)
			e.mu.Unlock()
			return c, nil
		}
	}
	e.mu.Unlock()
	return rdDial(ctx, e.cfg, db)
}

func (e *rdEngine) put(c *rdConn, err error) {
	if err != nil {
		c.close()
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.idle) >= 4 {
		e.idle[0].close()
		e.idle = e.idle[1:]
	}
	e.idle = append(e.idle, c)
}

// with runs f on a pooled connection; a connection that failed is dropped.
func (e *rdEngine) with(ctx context.Context, db int, f func(c *rdConn) error) error {
	c, err := e.conn(ctx, db)
	if err != nil {
		return err
	}
	err = f(c)
	var re rdError
	if errors.As(err, &re) {
		e.put(c, nil) // a server error leaves the connection fine
	} else {
		e.put(c, err)
	}
	return err
}

// rdError is an error reply.
type rdError string

func (e rdError) Error() string { return string(e) }

func rdOK(r *rdReply) error {
	if r.kind == '-' {
		return rdError(r.text())
	}
	return nil
}

func (e *rdEngine) info(ctx context.Context) (*ServerInfo, error) {
	db, err := e.rdDB("")
	if err != nil {
		return nil, err
	}
	si := &ServerInfo{Engine: EngineRedis, Database: "db" + strconv.Itoa(db)}
	err = e.with(ctx, db, func(c *rdConn) error {
		rs, err := c.do(ctx, []string{"INFO", "server"}, []string{"ACL", "WHOAMI"})
		if err != nil {
			return err
		}
		if err := rdOK(rs[0]); err != nil {
			return err
		}
		fields := rdInfo(rs[0].text())
		name := "Redis"
		if fields["valkey_version"] != "" {
			name = "Valkey"
		}
		v := fields["valkey_version"]
		if v == "" {
			v = fields["redis_version"]
		}
		si.Version = name + " " + v
		if fields["redis_mode"] == "cluster" {
			si.Version += " (cluster)"
		}
		si.User = e.cfg.User
		if rs[1].kind != '-' && !rs[1].null {
			si.User = rs[1].text()
		}
		if si.User == "" {
			si.User = "default"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return si, nil
}

// rdInfo reads INFO's key:value lines.
func rdInfo(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if k, v, ok := strings.Cut(l, ":"); ok && !strings.HasPrefix(l, "#") {
			out[k] = v
		}
	}
	return out
}

func (e *rdEngine) databases(ctx context.Context) ([]Database, error) {
	own, err := e.rdDB("")
	if err != nil {
		return nil, err
	}
	keys := map[int]int64{}
	count := 0
	err = e.with(ctx, own, func(c *rdConn) error {
		rs, err := c.do(ctx, []string{"INFO", "keyspace"}, []string{"CONFIG", "GET", "databases"})
		if err != nil {
			return err
		}
		if err := rdOK(rs[0]); err != nil {
			return err
		}
		for k, v := range rdInfo(rs[0].text()) {
			n, err := strconv.Atoi(strings.TrimPrefix(k, "db"))
			if err != nil || !strings.HasPrefix(k, "db") {
				continue
			}
			f := strings.Split(v, ",")
			if kv := strings.TrimPrefix(f[0], "keys="); kv != f[0] {
				keys[n], _ = strconv.ParseInt(kv, 10, 64)
			}
		}
		// CONFIG GET is often refused (an ACL user, a managed service): the
		// databases with keys are listed then.
		if r := rs[1]; r.kind == '*' && len(r.elems) == 2 {
			count, _ = strconv.Atoi(r.elems[1].text())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var nums []int
	add := func(n int) {
		if !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	add(own)
	for i := 0; i < min(count, 256); i++ {
		add(i)
	}
	for n := range keys {
		add(n)
	}
	sort.Ints(nums)
	out := make([]Database, 0, len(nums))
	for _, n := range nums {
		out = append(out, Database{Name: "db" + strconv.Itoa(n), SizeBytes: -1})
	}
	return out, nil
}

// rdScan walks SCAN with an optional MATCH until max keys, the end, or the
// time budget; done is false when it stopped early.
func rdScan(ctx context.Context, c *rdConn, match string, max int) (keys []string, done bool, err error) {
	seen := map[string]bool{}
	cursor := "0"
	deadline := time.Now().Add(rdScanBudget)
	for {
		cmd := []string{"SCAN", cursor, "COUNT", "1000"}
		if match != "" {
			cmd = append(cmd, "MATCH", match)
		}
		r, err := c.one(ctx, cmd...)
		if err != nil {
			return nil, false, err
		}
		if err := rdOK(r); err != nil {
			return nil, false, err
		}
		if r.kind != '*' || len(r.elems) != 2 {
			return nil, false, errors.New("unexpected SCAN reply")
		}
		cursor = r.elems[0].text()
		for _, k := range r.elems[1].elems {
			if s := k.text(); !seen[s] {
				seen[s] = true
				keys = append(keys, s)
			}
		}
		if cursor == "0" {
			return keys, true, nil
		}
		if len(keys) >= max || time.Now().After(deadline) {
			return keys, false, nil
		}
	}
}

// rdNoPrefix names the group of keys without a ":".
const rdNoPrefix = "(no prefix)"

// rdGroup is the object a key belongs to: "user:*" for "user:42".
func rdGroup(key string) string {
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[:i+1] + "*"
	}
	return rdNoPrefix
}

// rdMatch is the SCAN pattern of a group: the prefix with glob characters
// escaped. The no-prefix group scans everything and filters.
func rdMatch(group string) string {
	if group == rdNoPrefix || !strings.HasSuffix(group, ":*") {
		return ""
	}
	p := strings.TrimSuffix(group, "*")
	var b strings.Builder
	for _, c := range []byte(p) {
		if strings.IndexByte(`*?[]\`, c) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String() + "*"
}

func (e *rdEngine) objects(ctx context.Context, database string, _ bool) ([]Object, error) {
	db, err := e.rdDB(database)
	if err != nil {
		return nil, err
	}
	var keys []string
	done := false
	err = e.with(ctx, db, func(c *rdConn) error {
		keys, done, err = rdScan(ctx, c, "", rdSampleKeys)
		return err
	})
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, k := range keys {
		counts[rdGroup(k)]++
	}
	out := make([]Object, 0, len(counts))
	for g, n := range counts {
		o := Object{Name: g, Kind: KindTable, Rows: n, SizeBytes: -1}
		if !done {
			o.Comment = fmt.Sprintf("in a sample of %d keys", len(keys))
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// rdColumns are the columns of a key listing.
var rdColumns = []ResultColumn{
	{Name: "key", Type: "text"}, {Name: "type", Type: "text"},
	{Name: "ttl", Type: "long"}, {Name: "size", Type: "long"}, {Name: "value", Type: "text"},
}

func (e *rdEngine) describe(ctx context.Context, database, _, name string) (*TableDetail, error) {
	db, err := e.rdDB(database)
	if err != nil {
		return nil, err
	}
	d := &TableDetail{Name: name, Kind: KindTable}
	for _, c := range rdColumns {
		col := Column{Name: c.Name, Type: c.Type}
		switch c.Name {
		case "key":
			col.Key = "PRI"
		case "ttl":
			col.Comment = "seconds; -1 no expiry"
		case "size":
			col.Comment = "bytes of a string, elements of a collection"
		case "value":
			col.Comment = fmt.Sprintf("a string's start, a collection's first %d elements", rdPreview)
		}
		d.Columns = append(d.Columns, col)
	}
	match := rdMatch(name)
	var keys []string
	done := false
	err = e.with(ctx, db, func(c *rdConn) error {
		keys, done, err = rdScan(ctx, c, match, rdSampleKeys)
		return err
	})
	if err != nil {
		return nil, err
	}
	n := 0
	types := map[string]int{}
	var sample []string
	for _, k := range keys {
		if rdGroup(k) == name {
			n++
			if len(sample) < 1000 {
				sample = append(sample, k)
			}
		}
	}
	err = e.with(ctx, db, func(c *rdConn) error {
		cmds := make([][]string, len(sample))
		for i, k := range sample {
			cmds[i] = []string{"TYPE", k}
		}
		if len(cmds) == 0 {
			return nil
		}
		rs, err := c.do(ctx, cmds...)
		if err != nil {
			return err
		}
		for _, r := range rs {
			types[r.text()]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	pattern := match
	if pattern == "" {
		pattern = "* (keys without \":\")"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# SCAN 0 MATCH %s\n", pattern)
	if done {
		fmt.Fprintf(&b, "# %d keys\n", n)
	} else {
		fmt.Fprintf(&b, "# %d keys in a sample of %d\n", n, len(keys))
	}
	tn := make([]string, 0, len(types))
	for t := range types {
		tn = append(tn, t)
	}
	sort.Strings(tn)
	for _, t := range tn {
		if len(sample) == n {
			fmt.Fprintf(&b, "# %s: %d\n", t, types[t])
		} else {
			fmt.Fprintf(&b, "# %s: %d of %d looked at\n", t, types[t], len(sample))
		}
	}
	d.DDL = b.String()
	d.DDLNote = "Redis has no schema: the key pattern and the types seen"
	return d, nil
}

func (e *rdEngine) browse(ctx context.Context, database, _, name string, offset, limit int) (*Result, error) {
	db, err := e.rdDB(database)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	match := rdMatch(name)
	r := &Result{RowsAffected: -1, HasRows: true, Columns: rdColumns}
	err = e.with(ctx, db, func(c *rdConn) error {
		// The whole sample is read and sorted, so pages follow one order.
		keys, done, err := rdScan(ctx, c, match, max(rdSampleKeys, offset+limit))
		if err != nil {
			return err
		}
		var mine []string
		for _, k := range keys {
			if rdGroup(k) == name {
				mine = append(mine, k)
			}
		}
		// SCAN has no order of its own; sorted, a page is the same page
		// on the next read of the same sample.
		sort.Strings(mine)
		if offset >= len(mine) {
			mine = nil
		} else {
			mine = mine[offset:min(len(mine), offset+limit)]
		}
		rows, err := rdDescribeKeys(ctx, c, mine)
		if err != nil {
			return err
		}
		rr := &rowReader{r: r, maxRows: limit}
		for _, row := range rows {
			rr.add(row)
		}
		if !done {
			r.Note = "keys from a sample of the keyspace"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.Statement = "SCAN 0 MATCH " + rdQuote(orStar(match))
	r.DurationMs = time.Since(start).Milliseconds()
	r.RowsAffected = int64(len(r.Rows))
	return r, nil
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// rdDescribeKeys reads type, TTL, size and a value preview of each key, in
// two pipelined rounds.
func rdDescribeKeys(ctx context.Context, c *rdConn, keys []string) ([][][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var cmds [][]string
	for _, k := range keys {
		cmds = append(cmds, []string{"TYPE", k}, []string{"TTL", k})
	}
	rs, err := c.do(ctx, cmds...)
	if err != nil {
		return nil, err
	}
	types := make([]string, len(keys))
	ttls := make([]string, len(keys))
	for i := range keys {
		types[i] = rs[2*i].text()
		if r := rs[2*i+1]; r.kind == ':' {
			ttls[i] = strconv.FormatInt(r.num, 10)
		}
	}
	n := strconv.Itoa(rdPreview)
	cmds = cmds[:0]
	for i, k := range keys {
		switch types[i] {
		case "string":
			cmds = append(cmds, []string{"STRLEN", k}, []string{"GETRANGE", k, "0", "511"})
		case "hash":
			cmds = append(cmds, []string{"HLEN", k}, []string{"HSCAN", k, "0", "COUNT", n})
		case "list":
			cmds = append(cmds, []string{"LLEN", k}, []string{"LRANGE", k, "0", strconv.Itoa(rdPreview - 1)})
		case "set":
			cmds = append(cmds, []string{"SCARD", k}, []string{"SSCAN", k, "0", "COUNT", n})
		case "zset":
			cmds = append(cmds, []string{"ZCARD", k}, []string{"ZRANGE", k, "0", strconv.Itoa(rdPreview - 1), "WITHSCORES"})
		case "stream":
			cmds = append(cmds, []string{"XLEN", k}, []string{"XREVRANGE", k, "+", "-", "COUNT", "1"})
		default:
			cmds = append(cmds, []string{"PING"}, []string{"PING"})
		}
	}
	if rs, err = c.do(ctx, cmds...); err != nil {
		return nil, err
	}
	rows := make([][][]byte, len(keys))
	for i, k := range keys {
		size, val := rs[2*i], rs[2*i+1]
		row := [][]byte{rdCell([]byte(k)), []byte(types[i]), nil, nil, nil}
		if ttls[i] != "" {
			row[2] = []byte(ttls[i])
		}
		if size.kind == ':' {
			row[3] = []byte(strconv.FormatInt(size.num, 10))
		}
		if val.kind != '-' && !val.null {
			row[4] = []byte(rdPreviewText(types[i], val, size.num))
		}
		rows[i] = row
	}
	return rows, nil
}

// rdPreviewText shows a value: a string as itself (cut, binary as hex),
// a collection as JSON.
func rdPreviewText(typ string, r *rdReply, size int64) string {
	switch typ {
	case "string":
		s := string(rdCell(r.str))
		if size > int64(len(r.str)) {
			s += "..."
		}
		return s
	case "hash", "set":
		// HSCAN / SSCAN: cursor and the elements.
		if len(r.elems) == 2 {
			r = r.elems[1]
		}
	case "stream":
		return rdJSON(r)
	}
	if typ == "hash" || typ == "zset" {
		m := make([]string, 0, len(r.elems)/2)
		for i := 0; i+1 < len(r.elems); i += 2 {
			k, _ := json.Marshal(string(rdCell(r.elems[i].str)))
			v, _ := json.Marshal(string(rdCell(r.elems[i+1].str)))
			m = append(m, string(k)+":"+string(v))
			if len(m) == rdPreview {
				break
			}
		}
		s := "{" + strings.Join(m, ",") + "}"
		if size > int64(len(m)) {
			s += "..."
		}
		return s
	}
	elems := r.elems
	if len(elems) > rdPreview {
		elems = elems[:rdPreview]
	}
	parts := make([]string, len(elems))
	for i, e := range elems {
		b, _ := json.Marshal(string(rdCell(e.str)))
		parts[i] = string(b)
	}
	s := "[" + strings.Join(parts, ",") + "]"
	if size > int64(len(elems)) {
		s += "..."
	}
	return s
}

// rdCell is a value as cell text: bytes that are not UTF-8 as 0x hex.
func rdCell(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	return []byte("0x" + hexString(b))
}

// rdJSON renders a reply as JSON (nested arrays, nil as null).
func rdJSON(r *rdReply) string {
	b, _ := json.Marshal(rdAny(r))
	return string(b)
}

func rdAny(r *rdReply) any {
	switch {
	case r.null:
		return nil
	case r.kind == ':':
		return r.num
	case r.kind == '*':
		out := make([]any, len(r.elems))
		for i, e := range r.elems {
			out[i] = rdAny(e)
		}
		return out
	}
	return string(rdCell(r.str))
}

func (e *rdEngine) session(ctx context.Context, database string) (session, error) {
	db, err := e.rdDB(database)
	if err != nil {
		return nil, err
	}
	c, err := rdDial(ctx, e.cfg, db)
	if err != nil {
		return nil, err
	}
	return &rdSession{e: e, c: c, db: db}, nil
}

func (e *rdEngine) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.idle {
		c.close()
	}
	e.idle = nil
}

// rdSession is a query tab: its own connection. Stop closes it (the only
// way to break off a blocking command); the next run dials again, on the
// database the tab had SELECTed.
type rdSession struct {
	e *rdEngine

	mu   sync.Mutex
	c    *rdConn // nil after Stop or a broken connection
	db   int
	stop context.CancelFunc
}

func (s *rdSession) run(ctx context.Context, stmt string, _ bool, maxRows int) *Result {
	args, err := rdArgs(stmt)
	if err != nil {
		return errResult(err)
	}
	if len(args) == 0 {
		return errResult(errors.New("no command"))
	}
	if why := rdRefuse(args); why != "" {
		return errResult(errors.New(why))
	}
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	s.mu.Lock()
	c, db := s.c, s.db
	s.c, s.stop = nil, stop
	s.mu.Unlock()
	if c == nil {
		if c, err = rdDial(rctx, s.e.cfg, db); err != nil {
			s.clearStop()
			return errResult(s.stopped(ctx, err))
		}
	}
	r, err := c.one(rctx, args...)
	if err != nil {
		c.close()
		s.clearStop()
		return errResult(s.stopped(ctx, err))
	}
	s.mu.Lock()
	s.stop = nil
	if rctx.Err() != nil {
		// Stopped as the reply came: the connection may be mid-way.
		c.close()
	} else {
		s.c = c
		if strings.EqualFold(args[0], "SELECT") && r.kind == '+' && len(args) == 2 {
			s.db, _ = strconv.Atoi(args[1])
			c.db = s.db
		}
	}
	s.mu.Unlock()
	return rdResult(args, r, maxRows)
}

func (s *rdSession) clearStop() {
	s.mu.Lock()
	s.stop = nil
	s.mu.Unlock()
}

// stopped words an error after Stop the way the other engines do.
func (s *rdSession) stopped(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return errors.New("query stopped")
	}
	return err
}

func (s *rdSession) cancel() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *rdSession) inTx() bool { return false }

func (s *rdSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		s.c.close()
		s.c = nil
	}
}

// rdPairs: commands whose flat array reply is field / value pairs.
func rdPairs(args []string) (string, string, bool) {
	cmd := strings.ToUpper(args[0])
	with := func(word string) bool {
		for _, a := range args[1:] {
			if strings.EqualFold(a, word) {
				return true
			}
		}
		return false
	}
	switch {
	case cmd == "HGETALL":
		return "field", "value", true
	case cmd == "CONFIG" && len(args) > 1 && strings.EqualFold(args[1], "GET"):
		return "name", "value", true
	case cmd == "HRANDFIELD" && with("WITHVALUES"):
		return "field", "value", true
	case strings.HasPrefix(cmd, "Z") && with("WITHSCORES"):
		return "member", "score", true
	}
	return "", "", false
}

// rdResult shows a reply. Status and integer replies are the result's
// message; strings, arrays and INFO come back as rows.
func rdResult(args []string, r *rdReply, maxRows int) *Result {
	res := &Result{RowsAffected: -1}
	cmd := strings.ToUpper(args[0])
	switch {
	case r.kind == '-':
		res.Error = r.text()
		return res
	case r.kind == '+':
		res.Command = r.text()
		return res
	case r.kind == ':':
		res.Command = "(integer) " + strconv.FormatInt(r.num, 10)
		return res
	case r.null:
		res.Command = "(nil)"
		return res
	}
	res.HasRows = true
	rr := &rowReader{r: res, maxRows: maxRows}
	if r.kind == '$' {
		if cmd == "INFO" {
			res.Columns = []ResultColumn{{Name: "section", Type: "text"}, {Name: "name", Type: "text"}, {Name: "value", Type: "text"}}
			section := ""
			for _, l := range strings.Split(r.text(), "\n") {
				l = strings.TrimRight(l, "\r")
				if strings.HasPrefix(l, "# ") {
					section = l[2:]
					continue
				}
				if k, v, ok := strings.Cut(l, ":"); ok {
					if !rr.add([][]byte{[]byte(section), []byte(k), []byte(v)}) {
						break
					}
				}
			}
		} else if strings.Contains(r.text(), "\n") {
			res.Columns = []ResultColumn{{Name: "output", Type: "text"}}
			for _, l := range strings.Split(strings.TrimRight(r.text(), "\r\n"), "\n") {
				if !rr.add([][]byte{rdCell([]byte(strings.TrimRight(l, "\r")))}) {
					break
				}
			}
		} else {
			res.Columns = []ResultColumn{{Name: "value", Type: "text"}}
			rr.add([][]byte{rdCell(r.str)})
		}
		res.RowsAffected = int64(len(res.Rows))
		return res
	}
	elems := r.elems
	// SCAN family: a cursor and the elements.
	if strings.HasSuffix(cmd, "SCAN") && len(elems) == 2 && elems[1].kind == '*' {
		res.Note = "next cursor " + elems[0].text()
		elems = elems[1].elems
		if cmd == "HSCAN" || cmd == "ZSCAN" {
			args = []string{"HGETALL"}
			if cmd == "ZSCAN" {
				args = []string{"ZRANGE", "WITHSCORES"}
			}
		}
	}
	if a, b, ok := rdPairs(args); ok && len(elems)%2 == 0 {
		res.Columns = []ResultColumn{{Name: a, Type: "text"}, {Name: b, Type: "text"}}
		if b == "score" {
			res.Columns[1].Type = "double"
		}
		for i := 0; i+1 < len(elems); i += 2 {
			if !rr.add([][]byte{rdText(elems[i]), rdText(elems[i+1])}) {
				break
			}
		}
	} else {
		res.Columns = []ResultColumn{{Name: "value", Type: "text"}}
		for _, e := range elems {
			if !rr.add([][]byte{rdText(e)}) {
				break
			}
		}
	}
	if !res.Truncated {
		res.RowsAffected = int64(len(res.Rows))
	}
	return res
}

// rdText is an element as cell text; nil stays NULL, a nested array is
// JSON.
func rdText(r *rdReply) []byte {
	switch {
	case r.null:
		return nil
	case r.kind == ':':
		return []byte(strconv.FormatInt(r.num, 10))
	case r.kind == '*':
		return []byte(rdJSON(r))
	case r.kind == '-':
		return []byte("(error) " + r.text())
	}
	return rdCell(r.str)
}
