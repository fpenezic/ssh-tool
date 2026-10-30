package dbx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	godb2 "github.com/go-db2/go-db2"
)

// db2Engine is IBM Db2 (LUW) over DRDA, through go-db2 (pure Go). The
// connection goes to one database (Config.Database, which Db2 requires);
// the tree's databases are the schemas in it, and a session moves to one
// with SET SCHEMA.
//
// Sessions autocommit, as every other engine here does outside an
// explicit transaction. A read-only run is the exception: it runs with
// autocommit off and is rolled back (Db2 rolls back DDL too). go-db2 has
// no READ ONLY transaction, so the keyword check (CheckReadOnly) carries
// more of the weight than on Postgres.
//
// go-db2 reads a whole result before it returns the first row, so a
// query's row bound has to reach the server: db2Limit adds FETCH FIRST.
type db2Engine struct {
	cfg Config

	once      sync.Once
	connector driver.Connector
	pool      *sql.DB
	err       error
}

func newDB2(cfg Config) *db2Engine { return &db2Engine{cfg: cfg} }

// db2Connector opens each connection through the app's dialer. go-db2
// dials TCP itself and takes no dialer, so every connection gets a
// listener on this machine's loopback that takes one connection, which
// is piped to a connection the app's dialer opened (a direct-tcpip
// channel on the SSH session). The listener closes on that first accept,
// within the driver's connect; TLS still names the real server
// (Config.tlsConfig), so a certificate check is against it, not 127.0.0.1.
type db2Connector struct {
	cfg  Config
	base godb2.Config
}

func (c *db2Connector) Driver() driver.Driver { return godb2.NewConnector(&c.base).Driver() }

func (c *db2Connector) Connect(ctx context.Context) (driver.Conn, error) {
	up, err := c.cfg.dialer()(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = up.Close()
		return nil, err
	}
	// The pipe has to run while the driver connects: its handshake goes
	// over it. A connect that fails closes its socket, which ends the pipe
	// and closes up; one that never reached the listener leaves Accept to
	// fail on the Close below, which closes up too.
	go func() {
		local, err := ln.Accept()
		_ = ln.Close()
		if err != nil {
			_ = up.Close()
			return
		}
		db2Pipe(local, up)
	}()
	cfg := c.base
	cfg.Host = "127.0.0.1"
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	conn, err := godb2.NewConnector(&cfg).Connect(ctx)
	_ = ln.Close()
	if err != nil {
		return nil, db2Err(err)
	}
	return conn, nil
}

// db2Pipe copies both ways until either side ends, then closes both.
func db2Pipe(local, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, up); done <- struct{}{} }()
	<-done
	_ = local.Close()
	_ = up.Close()
}

func (e *db2Engine) db() (*sql.DB, error) {
	e.once.Do(func() {
		host, _, err := net.SplitHostPort(e.cfg.Addr)
		if err != nil {
			e.err = err
			return
		}
		database := strings.TrimSpace(e.cfg.Database)
		if database == "" {
			e.err = errors.New("Db2 needs a database: put its name in the Database field (e.g. SAMPLE)")
			return
		}
		base := godb2.Config{
			Host:           host,
			Database:       database,
			User:           e.cfg.User,
			Password:       e.cfg.Password,
			Timeout:        20 * time.Second, // connect only; go-db2 sets no I/O deadline
			ClientApplName: e.cfg.AppName,
		}
		if t := e.cfg.tlsConfig(); t != nil {
			base.UseSSL = true
			base.TLSConfig = t
		}
		e.connector = &db2Connector{cfg: e.cfg, base: base}
		e.pool = sql.OpenDB(e.connector)
		e.pool.SetMaxOpenConns(3)
		e.pool.SetMaxIdleConns(2)
		e.pool.SetConnMaxIdleTime(5 * time.Minute)
	})
	return e.pool, e.err
}

func (e *db2Engine) info(ctx context.Context) (*ServerInfo, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	in := &ServerInfo{Engine: EngineDB2}
	if err := db.QueryRowContext(ctx, `SELECT CURRENT USER, CURRENT SCHEMA FROM SYSIBM.SYSDUMMY1`).Scan(&in.User, &in.Database); err != nil {
		return nil, db2Err(err)
	}
	in.User, in.Database = strings.TrimSpace(in.User), strings.TrimSpace(in.Database)
	// SERVICE_LEVEL reads "DB2 v11.5.9.0"; the function needs EXECUTE on
	// it, which PUBLIC has by default.
	var lvl string
	if err := db.QueryRowContext(ctx, `SELECT SERVICE_LEVEL FROM TABLE(SYSPROC.ENV_GET_INST_INFO())`).Scan(&lvl); err == nil {
		in.Version = strings.TrimSpace(lvl)
	} else if err := db.QueryRowContext(ctx, `SELECT GETVARIABLE('SYSIBM.VERSION') FROM SYSIBM.SYSDUMMY1`).Scan(&lvl); err == nil {
		in.Version = "Db2 " + strings.TrimSpace(lvl)
	}
	return in, nil
}

// db2SystemSchema: the catalog and the schemas Db2 creates for itself.
func db2SystemSchema(name string) bool {
	return strings.HasPrefix(name, "SYS") || name == "NULLID" || name == "SQLJ"
}

// databases lists the schemas that hold a table or view, and the
// session's own.
func (e *db2Engine) databases(ctx context.Context) ([]Database, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.SCHEMANAME FROM SYSCAT.SCHEMATA s
		WHERE s.SCHEMANAME = CURRENT SCHEMA OR EXISTS (
			SELECT 1 FROM SYSCAT.TABLES t WHERE t.TABSCHEMA = s.SCHEMANAME AND t.TYPE IN ('T', 'V', 'S'))
		ORDER BY s.SCHEMANAME`)
	if err != nil {
		return nil, db2Err(err)
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.Name); err != nil {
			return nil, db2Err(err)
		}
		d.Name = strings.TrimSpace(d.Name)
		d.SizeBytes = -1
		d.System = db2SystemSchema(d.Name)
		out = append(out, d)
	}
	return out, db2Err(rows.Err())
}

func (e *db2Engine) objects(ctx context.Context, database string, _ bool) ([]Object, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	// CARD is the row count from the last RUNSTATS, -1 without one. S is
	// a materialized query table.
	rows, err := db.QueryContext(ctx, `
		SELECT TABNAME, TYPE, CARD, COALESCE(REMARKS, '') FROM SYSCAT.TABLES
		WHERE TABSCHEMA = ? AND TYPE IN ('T', 'V', 'S')
		ORDER BY TABNAME`, database)
	if err != nil {
		return nil, db2Err(err)
	}
	defer rows.Close()
	sys := db2SystemSchema(database)
	var out []Object
	for rows.Next() {
		var o Object
		var typ, comment string
		if err := rows.Scan(&o.Name, &typ, &o.Rows, &comment); err != nil {
			return nil, db2Err(err)
		}
		o.Name = strings.TrimSpace(o.Name)
		o.Comment = strings.TrimSpace(comment)
		o.SizeBytes = -1
		o.System = sys
		switch strings.TrimSpace(typ) {
		case "V":
			o.Kind, o.Rows = KindView, -1
		case "S":
			o.Kind = KindMatView
		default:
			o.Kind = KindTable
		}
		out = append(out, o)
	}
	return out, db2Err(rows.Err())
}

func db2Ident(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// db2Type prints a column type the way DESCRIBE TABLE does: LENGTH is the
// precision of a DECIMAL and the fraction digits of a TIMESTAMP (SCALE).
func db2Type(typ string, length, scale int64) string {
	typ = strings.TrimSpace(typ)
	switch typ {
	case "CHARACTER":
		return fmt.Sprintf("CHAR(%d)", length)
	case "VARCHAR", "GRAPHIC", "VARGRAPHIC", "BINARY", "VARBINARY":
		return fmt.Sprintf("%s(%d)", typ, length)
	case "DECIMAL":
		if scale == 0 {
			return fmt.Sprintf("DECIMAL(%d)", length)
		}
		return fmt.Sprintf("DECIMAL(%d,%d)", length, scale)
	case "DECFLOAT":
		if length == 8 {
			return "DECFLOAT(16)"
		}
		return "DECFLOAT(34)"
	case "TIMESTAMP":
		if scale != 6 {
			return fmt.Sprintf("TIMESTAMP(%d)", scale)
		}
	case "BLOB", "CLOB", "DBCLOB":
		return typ + "(" + db2Size(length) + ")"
	}
	return typ
}

// db2Size prints a LOB length as the DDL would (1M, 2G).
func db2Size(n int64) string {
	switch {
	case n > 0 && n%(1<<30) == 0:
		return strconv.FormatInt(n>>30, 10) + "G"
	case n > 0 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "M"
	case n > 0 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func (e *db2Engine) describe(ctx context.Context, database, _, name string) (*TableDetail, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	var typ string
	err = db.QueryRowContext(ctx, `SELECT TYPE FROM SYSCAT.TABLES WHERE TABSCHEMA = ? AND TABNAME = ?`, database, name).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s.%s not found", database, name)
	}
	if err != nil {
		return nil, db2Err(err)
	}
	d := &TableDetail{Name: name, Kind: KindTable}
	switch strings.TrimSpace(typ) {
	case "V":
		d.Kind = KindView
	case "S":
		d.Kind = KindMatView
	}

	// KEYSEQ is the column's place in the primary key, NULL outside it.
	rows, err := db.QueryContext(ctx, `
		SELECT COLNAME, TYPENAME, LENGTH, SCALE, NULLS, DEFAULT, COALESCE(KEYSEQ, 0),
		       COALESCE(IDENTITY, 'N'), COALESCE(GENERATED, ' '), COALESCE(REMARKS, '')
		FROM SYSCAT.COLUMNS WHERE TABSCHEMA = ? AND TABNAME = ? ORDER BY COLNO`, database, name)
	if err != nil {
		return nil, db2Err(err)
	}
	type pkCol struct {
		seq  int64
		name string
	}
	var pkCols []pkCol
	for rows.Next() {
		var c Column
		var typ, nulls, ident, gen, comment string
		var length, scale, keyseq int64
		var def sql.NullString
		if err := rows.Scan(&c.Name, &typ, &length, &scale, &nulls, &def, &keyseq, &ident, &gen, &comment); err != nil {
			rows.Close()
			return nil, db2Err(err)
		}
		c.Name = strings.TrimSpace(c.Name)
		c.Type = db2Type(typ, length, scale)
		c.Nullable = strings.TrimSpace(nulls) == "Y"
		if def.Valid && strings.TrimSpace(def.String) != "" {
			v := strings.TrimSpace(def.String)
			c.Default = &v
		}
		if keyseq > 0 {
			c.Key = "PRI"
			pkCols = append(pkCols, pkCol{keyseq, c.Name})
		}
		switch {
		case strings.TrimSpace(ident) == "Y" && strings.TrimSpace(gen) == "A":
			c.Extra = "GENERATED ALWAYS AS IDENTITY"
		case strings.TrimSpace(ident) == "Y":
			c.Extra = "GENERATED BY DEFAULT AS IDENTITY"
		}
		c.Comment = strings.TrimSpace(comment)
		d.Columns = append(d.Columns, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, db2Err(err)
	}
	rows.Close()
	pk := make([]string, len(pkCols))
	for _, p := range pkCols {
		if p.seq >= 1 && int(p.seq) <= len(pk) {
			pk[p.seq-1] = p.name
		}
	}

	rows, err = db.QueryContext(ctx, `
		SELECT i.INDNAME, i.UNIQUERULE, u.COLNAME, u.COLORDER
		FROM SYSCAT.INDEXES i
		JOIN SYSCAT.INDEXCOLUSE u ON u.INDSCHEMA = i.INDSCHEMA AND u.INDNAME = i.INDNAME
		WHERE i.TABSCHEMA = ? AND i.TABNAME = ?
		ORDER BY CASE i.UNIQUERULE WHEN 'P' THEN 0 ELSE 1 END, i.INDNAME, u.COLSEQ`, database, name)
	if err != nil {
		return nil, db2Err(err)
	}
	byName := map[string]int{}
	for rows.Next() {
		var n, rule, col, order string
		if err := rows.Scan(&n, &rule, &col, &order); err != nil {
			rows.Close()
			return nil, db2Err(err)
		}
		n, rule, col = strings.TrimSpace(n), strings.TrimSpace(rule), strings.TrimSpace(col)
		i, ok := byName[n]
		if !ok {
			d.Indexes = append(d.Indexes, Index{Name: n, Unique: rule == "P" || rule == "U", Primary: rule == "P"})
			i = len(d.Indexes) - 1
			byName[n] = i
		}
		if strings.TrimSpace(order) == "D" {
			col += " DESC"
		}
		d.Indexes[i].Columns = append(d.Indexes[i].Columns, col)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, db2Err(err)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `
		SELECT r.CONSTNAME, r.REFTABSCHEMA, r.REFTABNAME, k.COLNAME, p.COLNAME
		FROM SYSCAT.REFERENCES r
		JOIN SYSCAT.KEYCOLUSE k ON k.CONSTNAME = r.CONSTNAME AND k.TABSCHEMA = r.TABSCHEMA AND k.TABNAME = r.TABNAME
		JOIN SYSCAT.KEYCOLUSE p ON p.CONSTNAME = r.REFKEYNAME AND p.TABSCHEMA = r.REFTABSCHEMA
		  AND p.TABNAME = r.REFTABNAME AND p.COLSEQ = k.COLSEQ
		WHERE r.TABSCHEMA = ? AND r.TABNAME = ?
		ORDER BY r.CONSTNAME, k.COLSEQ`, database, name)
	if err != nil {
		return nil, db2Err(err)
	}
	type fk struct {
		cols, rcols []string
		rs, rt      string
	}
	fks := map[string]*fk{}
	var order []string
	for rows.Next() {
		var n, rs, rt, col, rcol string
		if err := rows.Scan(&n, &rs, &rt, &col, &rcol); err != nil {
			rows.Close()
			return nil, db2Err(err)
		}
		n = strings.TrimSpace(n)
		f := fks[n]
		if f == nil {
			f = &fk{rs: strings.TrimSpace(rs), rt: strings.TrimSpace(rt)}
			fks[n] = f
			order = append(order, n)
		}
		f.cols = append(f.cols, db2Ident(strings.TrimSpace(col)))
		f.rcols = append(f.rcols, db2Ident(strings.TrimSpace(rcol)))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, db2Err(err)
	}
	rows.Close()
	for _, n := range order {
		f := fks[n]
		ref := db2Ident(f.rt)
		if f.rs != database {
			ref = db2Ident(f.rs) + "." + ref
		}
		d.ForeignKeys = append(d.ForeignKeys, ForeignKey{
			Name: n,
			Def:  fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", strings.Join(f.cols, ", "), ref, strings.Join(f.rcols, ", ")),
		})
	}

	// A view's (and a materialized query table's) text is in the
	// catalog; a table's DDL comes from db2look, which SQL cannot reach.
	if d.Kind != KindTable {
		var text sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT TEXT FROM SYSCAT.VIEWS WHERE VIEWSCHEMA = ? AND VIEWNAME = ?`, database, name).Scan(&text); err == nil && text.Valid {
			d.DDL = strings.TrimSpace(text.String) + ";"
		}
	} else {
		d.DDL = db2RebuildDDL(database, name, d.Columns, pk)
		d.DDLNote = "Rebuilt from the catalog: the server's own DDL comes from db2look, which SQL cannot reach."
	}
	return d, nil
}

func db2RebuildDDL(schema, name string, cols []Column, pk []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s.%s (\n", db2Ident(schema), db2Ident(name))
	for i, c := range cols {
		fmt.Fprintf(&b, "  %s %s", db2Ident(c.Name), c.Type)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if c.Extra != "" {
			b.WriteString(" " + c.Extra)
		} else if c.Default != nil {
			b.WriteString(" WITH DEFAULT " + *c.Default)
		}
		if i < len(cols)-1 || len(pk) > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if len(pk) > 0 {
		q := make([]string, len(pk))
		for i, c := range pk {
			q[i] = db2Ident(c)
		}
		fmt.Fprintf(&b, "  PRIMARY KEY (%s)\n", strings.Join(q, ", "))
	}
	b.WriteString(");")
	return b.String()
}

func (e *db2Engine) browse(ctx context.Context, database, _, name string, offset, limit int) (*Result, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COLNAME FROM SYSCAT.COLUMNS
		WHERE TABSCHEMA = ? AND TABNAME = ? AND KEYSEQ IS NOT NULL ORDER BY KEYSEQ`, database, name)
	if err != nil {
		return nil, db2Err(err)
	}
	var pk []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, db2Err(err)
		}
		pk = append(pk, db2Ident(strings.TrimSpace(c)))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, db2Err(err)
	}
	rows.Close()
	q := "SELECT * FROM " + db2Ident(database) + "." + db2Ident(name)
	if len(pk) > 0 {
		q += " ORDER BY " + strings.Join(pk, ", ")
	}
	// OFFSET is Db2 11.1+; the first page needs none.
	if offset > 0 {
		q += fmt.Sprintf(" OFFSET %d ROWS", offset)
	}
	q += fmt.Sprintf(" FETCH FIRST %d ROWS ONLY", limit)
	start := time.Now()
	r := sqlRead(ctx, db, q, limit, db2Dialect, nil)
	r.Statement = q
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

// session opens a query tab's connection outside the tree pool (see
// mySession for why).
func (e *db2Engine) session(ctx context.Context, database string) (session, error) {
	if _, err := e.db(); err != nil {
		return nil, err
	}
	s := &db2Session{connector: e.connector, schema: database}
	if err := s.open(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (e *db2Engine) close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
}

// db2Session is one connection. A cancelled statement leaves go-db2's
// connection unusable (it stops the exchange by moving the socket
// deadline), so the next run opens a fresh one, back in the schema.
type db2Session struct {
	connector driver.Connector
	schema    string

	own  *sql.DB
	conn *sql.Conn

	mu     sync.Mutex
	stop   context.CancelFunc
	broken bool
}

func (s *db2Session) open(ctx context.Context) error {
	own := sql.OpenDB(s.connector)
	own.SetMaxOpenConns(1)
	conn, err := own.Conn(ctx)
	if err != nil {
		_ = own.Close()
		return db2Err(err)
	}
	if s.schema != "" {
		if _, err := conn.ExecContext(ctx, "SET SCHEMA "+db2Ident(s.schema)); err != nil {
			_ = conn.Close()
			_ = own.Close()
			return db2Err(err)
		}
	}
	s.own, s.conn = own, conn
	return nil
}

func (s *db2Session) run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result {
	// A reopen that failed leaves no connection: the next run tries again.
	s.mu.Lock()
	reopen := s.broken || s.conn == nil
	s.mu.Unlock()
	if reopen {
		s.closeConn()
		if err := s.open(ctx); err != nil {
			return errResult(err)
		}
		s.mu.Lock()
		s.broken = false
		s.mu.Unlock()
	}
	ctx, stop := context.WithCancel(ctx)
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.stop = nil
		if ctx.Err() != nil {
			s.broken = true
		}
		s.mu.Unlock()
		stop()
	}()
	if !readOnly {
		return s.exec(ctx, s.conn, stmt, maxRows)
	}
	// Autocommit off for the run, then everything it did is rolled back.
	tx, err := s.conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return errResult(db2Err(err))
	}
	defer func() { _ = tx.Rollback() }()
	return s.exec(ctx, tx, stmt, maxRows)
}

type db2Conn interface {
	sqlQuerier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *db2Session) exec(ctx context.Context, c db2Conn, stmt string, maxRows int) *Result {
	lead := leadKeyword(EngineDB2, stmt)
	if readLead(EngineDB2, lead) {
		return sqlRead(ctx, c, db2Limit(stmt, maxRows), maxRows, db2Dialect, nil)
	}
	if lead == "CALL" {
		// A procedure may return result sets.
		return sqlRead(ctx, c, stmt, maxRows, db2Dialect, nil)
	}
	// The affected count is right only with the vendored go-db2
	// (third_party/go-db2/PATCHES.md): upstream reads it from the wrong
	// place in the SQLCARD and reports 1 for everything.
	r := &Result{RowsAffected: -1}
	res, err := c.ExecContext(ctx, stmt)
	if err != nil {
		r.Error = db2Err(err).Error()
	} else if n, err := res.RowsAffected(); err == nil && !blockStmt(EngineDB2, stmt) && isDML(EngineDB2, stmt) {
		r.RowsAffected = n
	}
	return r
}

// db2Tail is a clause that has to stay last in a query: the row bound
// cannot follow it.
var db2Tail = regexp.MustCompile(`(?i)\b(FETCH\s+(FIRST|NEXT)|LIMIT|OFFSET|FOR\s+(READ|FETCH|UPDATE)|OPTIMIZE\s+FOR|WITH\s+(UR|CS|RS|RR)|SKIP\s+LOCKED)\b`)

// db2Limit bounds a query to one row past maxRows (so a cut shows as
// truncated) with FETCH FIRST: go-db2 reads the whole result before the
// first row comes back, and the bound otherwise works only on what has
// arrived. A query that already ends in a clause the bound cannot follow
// (its own FETCH FIRST or LIMIT, FOR READ ONLY, an isolation clause) runs
// as written.
func db2Limit(stmt string, maxRows int) string {
	if maxRows <= 0 {
		return stmt
	}
	s := strings.TrimRight(strings.TrimSpace(stmt), ";")
	s = strings.TrimSpace(s)
	if db2Tail.MatchString(db2TopLevel(codeOnly(EngineDB2, s))) {
		return stmt
	}
	// A trailing -- comment would swallow the clause.
	return s + fmt.Sprintf("\nFETCH FIRST %d ROWS ONLY", maxRows+1)
}

// db2TopLevel blanks what is inside parentheses, so a subquery's own
// FETCH FIRST does not count as the outer query's.
func db2TopLevel(s string) string {
	b := []byte(s)
	depth := 0
	for i, c := range b {
		switch {
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
			b[i] = ' '
		}
	}
	return string(b)
}

func (s *db2Session) cancel() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *db2Session) inTx() bool { return false }

func (s *db2Session) closeConn() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.own != nil {
		_ = s.own.Close()
	}
	s.conn, s.own = nil, nil
}

func (s *db2Session) close() { s.closeConn() }

var db2Dialect = sqlDialect{cell: db2Cell, errf: db2Err}

// db2Cell prints a value as the db2 CLP does, in ISO order for dates and
// timestamps; CHAR keeps its padding, as the server stores it.
func db2Cell(dbType string, v any) string {
	if t, ok := v.(time.Time); ok {
		switch strings.ToUpper(dbType) {
		case "DATE":
			return t.Format("2006-01-02")
		case "TIME":
			return t.Format("15:04:05")
		}
		return timeText(t, strings.Contains(strings.ToUpper(dbType), "ZONE"))
	}
	return cellText(v)
}

// db2Err keeps the server's SQLnnnnN / SQLSTATE text as go-db2 formats it.
func db2Err(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.TrimSpace(err.Error()))
}
