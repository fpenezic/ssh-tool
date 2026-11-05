// Package dbx is the SQL client behind a connection's Database tab:
// databases, tables and their structure, browsing rows, and query sessions
// for PostgreSQL, MySQL / MariaDB, ClickHouse, Oracle, SQL Server and Db2, and
// indices and queries
// for OpenSearch / Elasticsearch, keys and commands for Redis.
//
// Every database connection is opened by Config.Dial. The app hands in a
// dialer that opens a direct-tcpip channel on the connection's SSH session
// (or dials the way DialVia does), so the database address is resolved
// and reached on the far side: a server that only listens on the host's
// loopback works with no local forward. Host names are never resolved on
// this machine (pgconn's LookupFunc is a pass-through).
//
// Values come back as text, the way psql and the mysql CLI print them:
// numerics keep their precision, NULL is nil, binary is hex (\x... on
// Postgres, 0x... on MySQL).
//
// The package knows nothing of the store, the vault or the SSH layer; the
// app wires those into Config.
package dbx

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Engines, as stored in the db_engine setting.
const (
	EnginePostgres   = "postgres"
	EngineMySQL      = "mysql"
	EngineClickHouse = "clickhouse"
	// EngineOpenSearch is OpenSearch or Elasticsearch (the flavor is read
	// from the server).
	EngineOpenSearch = "opensearch"
	// EngineRedis is Redis or a protocol-compatible server (Valkey, KeyDB).
	EngineRedis = "redis"
	// EngineOracle is Oracle Database; Config.Database is the service name
	// and the tree's databases are its schemas.
	EngineOracle = "oracle"
	// EngineMSSQL is Microsoft SQL Server (and Azure SQL).
	EngineMSSQL = "mssql"
	// EngineDB2 is IBM Db2 (LUW); Config.Database is the database and the
	// tree's databases are its schemas.
	EngineDB2 = "db2"
)

// DefaultPort is the port an address without one gets. tls matters to
// ClickHouse only, whose HTTPS interface has a port of its own.
func DefaultPort(engine, tls string) int {
	switch engine {
	case EngineMySQL:
		return 3306
	case EngineClickHouse:
		if tls == TLSRequire || tls == TLSVerifyFull {
			return 8443
		}
		return 8123 // the HTTP interface
	case EngineOpenSearch:
		return 9200
	case EngineRedis:
		return 6379
	case EngineOracle:
		return 1521
	case EngineMSSQL:
		return 1433
	case EngineDB2:
		return 50000
	}
	return 5432
}

// TLS modes, as stored in the db_tls setting.
const (
	TLSDisable    = "disable"
	TLSRequire    = "require"     // encrypted, certificate not checked
	TLSVerifyFull = "verify-full" // encrypted, certificate checked against the host name
)

// Limits on what one run brings back. A result past either stops reading
// and is marked Truncated; a cell past MaxCellBytes is cut and marked.
const (
	DefaultMaxRows = 500
	MaxRowsCeiling = 100000
	MaxCellBytes   = 256 << 10
	MaxResultBytes = 64 << 20
)

// DialFunc opens one database connection.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config describes how to reach one database server.
type Config struct {
	Engine string
	// Addr is host:port as seen from wherever Dial dials from.
	Addr string
	// Database is the one to connect to first; "" takes the engine's
	// default (the user name on Postgres, none on MySQL).
	Database string
	User     string
	Password string
	TLS      string // TLSDisable (default), TLSRequire, TLSVerifyFull
	// ReadOnly opens every session read-only and refuses writes (see
	// CheckReadOnly).
	ReadOnly bool
	// QueryTimeout stops a query tab's statement that runs longer (the
	// same cancel as the Stop button). 0 = no limit.
	QueryTimeout time.Duration
	// Dial opens database connections. nil dials directly.
	Dial DialFunc
	// AppName shows up in the server's connection list.
	AppName string
}

// ParseAddr fills in the engine's default port (see DefaultPort); "" means
// localhost.
func ParseAddr(engine, tls, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = "127.0.0.1"
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			return net.JoinHostPort(strings.Trim(s, "[]"), strconv.Itoa(DefaultPort(engine, tls))), nil
		}
		return "", fmt.Errorf("database address %q: %w", s, err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("database address %q: bad port", s)
	}
	return net.JoinHostPort(host, port), nil
}

func (cfg Config) dialer() DialFunc {
	if cfg.Dial != nil {
		return cfg.Dial
	}
	var d net.Dialer
	return d.DialContext
}

func (cfg Config) tlsConfig() *tls.Config {
	host, _, _ := net.SplitHostPort(cfg.Addr)
	switch cfg.TLS {
	case TLSRequire:
		return &tls.Config{InsecureSkipVerify: true, ServerName: host} //nolint:gosec // "require" is encrypt-only by definition
	case TLSVerifyFull:
		return &tls.Config{ServerName: host}
	}
	return nil
}

// ServerInfo describes the server and the login.
type ServerInfo struct {
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	User     string `json:"user"`
	Database string `json:"database"`
}

// Database is one database on the server.
type Database struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"` // -1 unknown
	System    bool   `json:"system"`
}

// Object kinds.
const (
	KindTable    = "table"
	KindView     = "view"
	KindMatView  = "matview"
	KindForeign  = "foreign"
	KindSequence = "sequence"
)

// Object is a table or view. Schema is "" on MySQL (its databases are
// its schemas).
type Object struct {
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Rows      int64  `json:"rows"`       // estimate; -1 unknown
	SizeBytes int64  `json:"size_bytes"` // -1 unknown
	System    bool   `json:"system"`
	Comment   string `json:"comment,omitempty"`
}

// Column describes a table column.
type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	Key      string  `json:"key,omitempty"` // "PRI" for primary key members
	Extra    string  `json:"extra,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

// Index describes an index.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Def     string   `json:"def,omitempty"`
}

// ForeignKey describes a foreign key. Columns pairs with RefColumns, in
// key order; RefDatabase / RefSchema / RefTable name the referenced table
// the way Browse takes it (the database is the schema on MySQL, Oracle
// and Db2).
type ForeignKey struct {
	Name string `json:"name"`
	Def  string `json:"def"`
	// Database / Schema / Table: the table that holds the key, set when it
	// is not the one described (ReferencedBy).
	Database    string   `json:"database,omitempty"`
	Schema      string   `json:"schema,omitempty"`
	Table       string   `json:"table,omitempty"`
	Columns     []string `json:"columns,omitempty"`
	RefDatabase string   `json:"ref_database,omitempty"`
	RefSchema   string   `json:"ref_schema,omitempty"`
	RefTable    string   `json:"ref_table,omitempty"`
	RefColumns  []string `json:"ref_columns,omitempty"`
}

// TableDetail is a table's (or view's) structure.
type TableDetail struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
	DDL         string       `json:"ddl"`
	// DDLNote says how DDL was obtained when it is not the server's own
	// (Postgres has no SHOW CREATE TABLE; it is rebuilt from the catalog).
	DDLNote string `json:"ddl_note,omitempty"`
}

// ResultColumn names a result column and its type.
type ResultColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Result is what one statement returned.
type Result struct {
	Statement string         `json:"statement"`
	Columns   []ResultColumn `json:"columns"`
	Rows      [][]*string    `json:"rows"`
	// HasRows: the statement returned a result set (possibly empty), as
	// opposed to a command with an affected count.
	HasRows      bool   `json:"has_rows"`
	RowsAffected int64  `json:"rows_affected"` // -1 not reported
	Command      string `json:"command,omitempty"`
	// Note is shown beside a result set (a search's total hits).
	Note       string `json:"note,omitempty"`
	Truncated  bool   `json:"truncated"`
	CutCells   int    `json:"cut_cells"` // cells cut at MaxCellBytes
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Write      bool   `json:"write"` // not a read statement (see IsRead)
}

// RunOptions bound a run.
type RunOptions struct {
	MaxRows int `json:"max_rows"`
	// ReadOnly applies the read-only guards to this run even on a writable
	// handle (the LLM's queries).
	ReadOnly bool `json:"read_only"`
}

func (o RunOptions) maxRows() int {
	switch {
	case o.MaxRows <= 0:
		return DefaultMaxRows
	case o.MaxRows > MaxRowsCeiling:
		return MaxRowsCeiling
	}
	return o.MaxRows
}

// engine is what each server kind implements.
type engine interface {
	info(ctx context.Context) (*ServerInfo, error)
	databases(ctx context.Context) ([]Database, error)
	objects(ctx context.Context, database string, system bool) ([]Object, error)
	describe(ctx context.Context, database, schema, name string) (*TableDetail, error)
	browse(ctx context.Context, database, schema, name string, where []Cond, order []Order, offset, limit int) (*Result, error)
	session(ctx context.Context, database string) (session, error)
	close()
}

// session is one dedicated server connection a query tab runs on.
type session interface {
	run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result
	cancel()
	inTx() bool
	close()
}

// Handle is an open server: a small pool for the tree and table views, and
// the query sessions of the tab.
type Handle struct {
	cfg Config
	eng engine

	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("database handle closed")

// Open prepares a handle. No I/O: the first call connects.
func Open(cfg Config) (*Handle, error) {
	addr, err := ParseAddr(cfg.Engine, cfg.TLS, cfg.Addr)
	if err != nil {
		return nil, err
	}
	cfg.Addr = addr
	if cfg.AppName == "" {
		cfg.AppName = "ssh-tool"
	}
	h := &Handle{cfg: cfg, sessions: map[string]*Session{}}
	switch cfg.Engine {
	case EnginePostgres:
		h.eng = newPG(cfg)
	case EngineMySQL:
		h.eng = newMy(cfg)
	case EngineClickHouse:
		h.eng = newCH(cfg)
	case EngineOpenSearch:
		h.eng = newOS(cfg)
	case EngineRedis:
		h.eng = newRD(cfg)
	case EngineOracle:
		h.eng = newOra(cfg)
	case EngineMSSQL:
		h.eng = newMS(cfg)
	case EngineDB2:
		h.eng = newDB2(cfg)
	default:
		return nil, fmt.Errorf("unknown database engine %q", cfg.Engine)
	}
	return h, nil
}

// Engine is the handle's engine name.
func (h *Handle) Engine() string { return h.cfg.Engine }

// ReadOnly reports whether the handle refuses writes.
func (h *Handle) ReadOnly() bool { return h.cfg.ReadOnly }

// Info reports the server version and the login.
func (h *Handle) Info(ctx context.Context) (*ServerInfo, error) { return h.eng.info(ctx) }

// Databases lists the databases the login can see.
func (h *Handle) Databases(ctx context.Context) ([]Database, error) { return h.eng.databases(ctx) }

// Objects lists a database's tables and views (system schemas only with
// system).
func (h *Handle) Objects(ctx context.Context, database string, system bool) ([]Object, error) {
	return h.eng.objects(ctx, database, system)
}

// Describe returns a table's or view's columns, indexes, foreign keys and
// DDL.
func (h *Handle) Describe(ctx context.Context, database, schema, name string) (*TableDetail, error) {
	return h.eng.describe(ctx, database, schema, name)
}

// Cond is one equality a filtered browse keeps rows by: Column = Value,
// or Column IS NULL for a nil Value. The column is quoted as an
// identifier and the value sent as a bind parameter, never spliced in.
type Cond struct {
	Column string  `json:"column"`
	Value  *string `json:"value"`
	// Op is the comparison ("" is =): = <> < > <= >= LIKE "NOT LIKE".
	// A nil Value takes IS NULL, or IS NOT NULL with Op <>.
	Op string `json:"op,omitempty"`
	// Hex: Value is a binary cell as the grid shows it (0x... or \x...),
	// bound as its bytes.
	Hex bool `json:"hex,omitempty"`
}

// bindValue is what a condition binds: the bytes of a binary cell, the
// text otherwise.
func (c Cond) bindValue() (any, error) {
	if !c.Hex {
		return *c.Value, nil
	}
	h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(*c.Value, "0x"), "0X"), `\x`)
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("filter on %s: %q is not a hex value", c.Column, *c.Value)
	}
	return b, nil
}

// ErrNoFilter is a filtered browse on an engine without one.
var ErrNoFilter = errors.New("this engine cannot filter or sort a table's rows on the server")

// whereClause is " WHERE a = $1 AND b IS NULL" for where, with the bind
// values in order; ph(n, c) is the engine's n-th placeholder (1-based)
// for condition c, bind (may be nil) turns a condition's value into what
// the engine binds for it.
func whereClause(where []Cond, ident func(string) string, ph func(int, Cond) string, bind func(Cond, any) any) (string, []any, error) {
	if len(where) == 0 {
		return "", nil, nil
	}
	var parts []string
	var args []any
	for _, c := range where {
		op := c.Op
		if op == "" {
			op = "="
		}
		if !condOps[op] {
			return "", nil, fmt.Errorf("filter on %s: unknown comparison %q", c.Column, c.Op)
		}
		if c.Value == nil {
			switch op {
			case "=":
				parts = append(parts, ident(c.Column)+" IS NULL")
			case "<>":
				parts = append(parts, ident(c.Column)+" IS NOT NULL")
			default:
				return "", nil, fmt.Errorf("filter on %s: %s needs a value", c.Column, op)
			}
			continue
		}
		if c.Hex && strings.Contains(op, "LIKE") {
			return "", nil, fmt.Errorf("filter on %s: LIKE does not apply to a binary column", c.Column)
		}
		v, err := c.bindValue()
		if err != nil {
			return "", nil, err
		}
		if bind != nil {
			v = bind(c, v)
		}
		args = append(args, v)
		parts = append(parts, ident(c.Column)+" "+op+" "+ph(len(args), c))
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

var condOps = map[string]bool{"=": true, "<>": true, "<": true, ">": true, "<=": true, ">=": true, "LIKE": true, "NOT LIKE": true}

// Order is one sort key of a browse, server side.
type Order struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// orderList is the ORDER BY list: the asked keys first, then the primary
// key (already quoted) so pages stay stable; "" when there is neither.
func orderList(order []Order, pk []string, ident func(string) string) string {
	var parts []string
	seen := map[string]bool{}
	for _, o := range order {
		q := ident(o.Column)
		seen[q] = true
		if o.Desc {
			q += " DESC"
		}
		parts = append(parts, q)
	}
	for _, k := range pk {
		if !seen[k] {
			parts = append(parts, k)
		}
	}
	return strings.Join(parts, ", ")
}

// numbered is a placeholder style with the position in it ($1, :1, @p1).
func numbered(prefix string) func(int, Cond) string {
	return func(n int, _ Cond) string { return prefix + strconv.Itoa(n) }
}

// qmark is the ? placeholder.
func qmark(int, Cond) string { return "?" }

// Browse reads a page of a table's rows, in primary-key order when it has
// one.
func (h *Handle) Browse(ctx context.Context, database, schema, name string, offset, limit int) (*Result, error) {
	return h.BrowseWhere(ctx, database, schema, name, nil, offset, limit)
}

// BrowseWhere is Browse of the rows matching every condition in where (a
// foreign key followed from another table's row).
func (h *Handle) BrowseWhere(ctx context.Context, database, schema, name string, where []Cond, offset, limit int) (*Result, error) {
	return h.BrowseQuery(ctx, database, schema, name, where, nil, offset, limit)
}

// BrowseQuery is BrowseWhere sorted by order on the server (the primary
// key after it, so pages stay stable).
func (h *Handle) BrowseQuery(ctx context.Context, database, schema, name string, where []Cond, order []Order, offset, limit int) (*Result, error) {
	if limit <= 0 {
		limit = DefaultMaxRows
	}
	if limit > MaxRowsCeiling {
		limit = MaxRowsCeiling
	}
	if offset < 0 {
		offset = 0
	}
	for _, c := range where {
		if c.Column == "" {
			return nil, errors.New("filter: empty column name")
		}
	}
	for _, o := range order {
		if o.Column == "" {
			return nil, errors.New("sort: empty column name")
		}
	}
	return h.eng.browse(ctx, database, schema, name, where, order, offset, limit)
}

// Session is a query tab's own server connection: session state (SET,
// temp tables, an open transaction) carries from one run to the next.
type Session struct {
	ID       string
	Database string

	h   *Handle
	s   session
	mu  sync.Mutex // one run at a time
	run sync.Mutex // guards running
	// running is true while a run is in progress (Cancel is a no-op
	// otherwise).
	running bool
}

// OpenSession opens a session on database under id, replacing one that
// id already had.
func (h *Handle) OpenSession(ctx context.Context, id, database string) (*Session, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	h.mu.Unlock()
	s, err := h.eng.session(ctx, database)
	if err != nil {
		return nil, err
	}
	sess := &Session{ID: id, Database: database, h: h, s: s}
	h.mu.Lock()
	old := h.sessions[id]
	h.sessions[id] = sess
	closed := h.closed
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	if closed {
		s.close()
		return nil, ErrClosed
	}
	return sess, nil
}

// Session returns the session open under id.
func (h *Handle) Session(id string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

// CloseSession closes the session open under id.
func (h *Handle) CloseSession(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// close stops a running statement and closes the connection once the run
// has let go of it: a connection is not safe for concurrent use.
func (s *Session) close() {
	s.Cancel()
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.s.close()
	}()
}

// Run runs a script statement by statement and stops at the first error,
// whose Result carries it. In read-only mode a statement CheckReadOnly
// refuses is reported as an error without reaching the server.
func (s *Session) Run(ctx context.Context, script string, opts RunOptions) []*Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.Lock()
	s.running = true
	s.run.Unlock()
	defer func() {
		s.run.Lock()
		s.running = false
		s.run.Unlock()
	}()
	engine := s.h.cfg.Engine
	readOnly := s.h.cfg.ReadOnly || opts.ReadOnly
	var out []*Result
	for _, st := range Split(engine, script) {
		var r *Result
		if readOnly {
			if err := CheckReadOnly(engine, st.Text); err != nil {
				r = &Result{Statement: st.Text, Error: err.Error(), RowsAffected: -1, Write: true}
			}
		}
		if r == nil {
			start := time.Now()
			// The timeout cancels on the server, as Stop does: closing the
			// client's context alone leaves some engines' statement running.
			var timedOut atomic.Bool
			var timer *time.Timer
			if d := s.h.cfg.QueryTimeout; d > 0 {
				timer = time.AfterFunc(d, func() {
					timedOut.Store(true)
					s.s.cancel()
				})
			}
			r = s.s.run(ctx, st.Text, readOnly, opts.maxRows())
			if timer != nil {
				timer.Stop()
			}
			r.Statement = st.Text
			r.DurationMs = time.Since(start).Milliseconds()
			r.Write = !IsRead(engine, st.Text)
			if timedOut.Load() {
				msg := fmt.Sprintf("stopped after the query timeout (%s)", s.h.cfg.QueryTimeout)
				if r.Error != "" {
					msg += ": " + r.Error
				}
				r.Error = msg
			}
		}
		out = append(out, r)
		if r.Error != "" || ctx.Err() != nil {
			break
		}
	}
	return out
}

// Cancel asks the server to stop the running statement.
func (s *Session) Cancel() {
	s.run.Lock()
	running := s.running
	s.run.Unlock()
	if running {
		s.s.cancel()
	}
}

// InTx reports whether the session holds an open transaction (Postgres
// and SQL Server; MySQL does not report it, Oracle sessions autocommit).
func (s *Session) InTx() bool { return s.s.inTx() }

// Close closes every session and the pool.
func (h *Handle) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	sessions := h.sessions
	h.sessions = map[string]*Session{}
	h.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
	h.eng.close()
}

// rowReader accumulates rows under the result bounds.
type rowReader struct {
	r        *Result
	maxRows  int
	bytes    int
	stopped  bool
	isBinary []bool
	hexPref  string
}

// add appends one row of raw text values (nil = NULL). It reports false
// once a bound is hit; the caller stops reading.
func (rr *rowReader) add(raw [][]byte) bool {
	if len(rr.r.Rows) >= rr.maxRows || rr.bytes >= MaxResultBytes {
		rr.r.Truncated = true
		rr.stopped = true
		return false
	}
	row := make([]*string, len(raw))
	for i, v := range raw {
		if v == nil {
			continue
		}
		var s string
		if i < len(rr.isBinary) && rr.isBinary[i] {
			s = rr.hexPref + hexString(v)
		} else {
			s = string(v)
		}
		if len(s) > MaxCellBytes {
			s = truncUTF8(s, MaxCellBytes) + "..."
			rr.r.CutCells++
		}
		rr.bytes += len(s)
		row[i] = &s
	}
	rr.r.Rows = append(rr.r.Rows, row)
	return true
}

// hexString writes b as hex. Callers pass a cell's value; past
// MaxResultBytes it is cut (a result is cut there anyway).
func hexString(b []byte) string {
	if len(b) > MaxResultBytes {
		b = b[:MaxResultBytes]
	}
	return hex.EncodeToString(b)
}

func truncUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// errResult reports err as a statement result.
func errResult(err error) *Result {
	return &Result{Error: err.Error(), RowsAffected: -1}
}
