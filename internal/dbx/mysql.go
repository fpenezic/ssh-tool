package dbx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

func init() {
	// The driver logs every connection it drops (a closed tunnel, a
	// session closed mid-read) on its own; the errors that matter reach
	// the caller anyway.
	_ = mysql.SetLogger(log.New(io.Discard, "", 0))
}

// myEngine is one small pool for the whole server: a MySQL connection
// reaches every database, names are qualified instead.
type myEngine struct {
	cfg Config

	once      sync.Once
	connector driver.Connector
	pool      *sql.DB
	err       error
}

func newMy(cfg Config) *myEngine { return &myEngine{cfg: cfg} }

func (e *myEngine) driverConfig() *mysql.Config {
	mc := mysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = e.cfg.Addr
	mc.User = e.cfg.User
	mc.Passwd = e.cfg.Password
	mc.DBName = e.cfg.Database
	mc.Timeout = 20 * time.Second
	mc.ParseTime = false // values stay text, as the server prints them
	mc.MultiStatements = false
	mc.InterpolateParams = false
	mc.ConnectionAttributes = "program_name:" + e.cfg.AppName
	// Without TLS, caching_sha2_password (the MySQL 8 default) fetches the
	// server's RSA key for a full login; the tunnel is what protects it.
	mc.TLS = e.cfg.tlsConfig()
	dial := e.cfg.dialer()
	mc.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dial(ctx, network, addr)
	}
	return mc
}

func (e *myEngine) db() (*sql.DB, error) {
	e.once.Do(func() {
		c, err := mysql.NewConnector(e.driverConfig())
		if err != nil {
			e.err = err
			return
		}
		e.connector = c
		e.pool = sql.OpenDB(c)
		e.pool.SetMaxOpenConns(3)
		e.pool.SetMaxIdleConns(2)
		e.pool.SetConnMaxIdleTime(5 * time.Minute)
	})
	return e.pool, e.err
}

func (e *myEngine) info(ctx context.Context) (*ServerInfo, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	in := &ServerInfo{Engine: EngineMySQL}
	var dbName sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT VERSION(), CURRENT_USER(), DATABASE()`).Scan(&in.Version, &in.User, &dbName); err != nil {
		return nil, myErr(err)
	}
	in.Database = dbName.String
	return in, nil
}

var mySystemDBs = map[string]bool{"information_schema": true, "mysql": true, "performance_schema": true, "sys": true}

func (e *myEngine) databases(ctx context.Context) ([]Database, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.SCHEMA_NAME, COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), -1)
		FROM information_schema.SCHEMATA s
		LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
		GROUP BY s.SCHEMA_NAME ORDER BY s.SCHEMA_NAME`)
	if err != nil {
		return nil, myErr(err)
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.Name, &d.SizeBytes); err != nil {
			return nil, myErr(err)
		}
		d.System = mySystemDBs[strings.ToLower(d.Name)]
		out = append(out, d)
	}
	return out, myErr(rows.Err())
}

func (e *myEngine) objects(ctx context.Context, database string, system bool) ([]Object, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT TABLE_NAME, TABLE_TYPE, COALESCE(TABLE_ROWS, -1),
		       COALESCE(DATA_LENGTH + INDEX_LENGTH, -1), COALESCE(TABLE_COMMENT, '')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, database)
	if err != nil {
		return nil, myErr(err)
	}
	defer rows.Close()
	sys := mySystemDBs[strings.ToLower(database)]
	var out []Object
	for rows.Next() {
		var o Object
		var typ string
		if err := rows.Scan(&o.Name, &typ, &o.Rows, &o.SizeBytes, &o.Comment); err != nil {
			return nil, myErr(err)
		}
		o.Kind = KindTable
		if strings.Contains(typ, "VIEW") {
			o.Kind = KindView
			o.Rows, o.SizeBytes = -1, -1
			if strings.HasPrefix(o.Comment, "VIEW") {
				o.Comment = ""
			}
		}
		o.System = sys || typ == "SYSTEM VIEW"
		out = append(out, o)
	}
	return out, myErr(rows.Err())
}

func (e *myEngine) describe(ctx context.Context, database, _, name string) (*TableDetail, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	var typ string
	err = db.QueryRowContext(ctx, `SELECT TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, database, name).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s.%s not found", database, name)
	}
	if err != nil {
		return nil, myErr(err)
	}
	d := &TableDetail{Name: name, Kind: KindTable}
	if strings.Contains(typ, "VIEW") {
		d.Kind = KindView
	}

	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE = 'YES', COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COLUMN_COMMENT
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, database, name)
	if err != nil {
		return nil, myErr(err)
	}
	for rows.Next() {
		var c Column
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &def, &c.Key, &c.Extra, &c.Comment); err != nil {
			rows.Close()
			return nil, myErr(err)
		}
		if def.Valid {
			c.Default = &def.String
		}
		d.Columns = append(d.Columns, c)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE = 0, COLUMN_NAME FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME, SEQ_IN_INDEX`, database, name)
	if err != nil {
		return nil, myErr(err)
	}
	byName := map[string]int{}
	for rows.Next() {
		var n string
		var uniq bool
		var col sql.NullString
		if err := rows.Scan(&n, &uniq, &col); err != nil {
			rows.Close()
			return nil, myErr(err)
		}
		i, ok := byName[n]
		if !ok {
			d.Indexes = append(d.Indexes, Index{Name: n, Unique: uniq, Primary: n == "PRIMARY"})
			i = len(d.Indexes) - 1
			byName[n] = i
		}
		ix := &d.Indexes[i]
		if col.Valid {
			ix.Columns = append(ix.Columns, col.String)
		} else {
			ix.Columns = append(ix.Columns, "(expression)")
		}
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `
		SELECT k.CONSTRAINT_NAME,
		       GROUP_CONCAT(k.COLUMN_NAME ORDER BY k.ORDINAL_POSITION),
		       k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME,
		       GROUP_CONCAT(k.REFERENCED_COLUMN_NAME ORDER BY k.ORDINAL_POSITION)
		FROM information_schema.KEY_COLUMN_USAGE k
		WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		GROUP BY k.CONSTRAINT_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME
		ORDER BY k.CONSTRAINT_NAME`, database, name)
	if err != nil {
		return nil, myErr(err)
	}
	for rows.Next() {
		var n, cols, rs, rt, rcols string
		if err := rows.Scan(&n, &cols, &rs, &rt, &rcols); err != nil {
			rows.Close()
			return nil, myErr(err)
		}
		ref := myIdent(rt)
		if rs != database {
			ref = myIdent(rs) + "." + ref
		}
		d.ForeignKeys = append(d.ForeignKeys, ForeignKey{
			Name: n,
			Def:  fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", cols, ref, rcols),
		})
	}
	rows.Close()

	q := "SHOW CREATE TABLE "
	if d.Kind == KindView {
		q = "SHOW CREATE VIEW "
	}
	rows, err = db.QueryContext(ctx, q+myIdent(database)+"."+myIdent(name))
	if err != nil {
		return nil, myErr(err)
	}
	defer rows.Close()
	if rows.Next() {
		cols, _ := rows.Columns()
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, myErr(err)
		}
		if len(vals) > 1 {
			d.DDL = string(vals[1]) + ";"
		}
	}
	return d, myErr(rows.Err())
}

func myIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func (e *myEngine) browse(ctx context.Context, database, _, name string, offset, limit int) (*Result, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = 'PRIMARY' ORDER BY SEQ_IN_INDEX`, database, name)
	if err != nil {
		return nil, myErr(err)
	}
	var pk []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, myErr(err)
		}
		pk = append(pk, myIdent(c))
	}
	rows.Close()
	q := "SELECT * FROM " + myIdent(database) + "." + myIdent(name)
	if len(pk) > 0 {
		q += " ORDER BY " + strings.Join(pk, ", ")
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, myErr(err)
	}
	defer conn.Close()
	start := time.Now()
	r := myRead(ctx, conn, q, limit, nil)
	r.Statement = q
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

// session opens a query tab's connection outside the tree pool: the pool
// is small, and a tab holds its connection for as long as it is open - a
// few tabs would otherwise leave the tree, browsing and KILL QUERY (Stop)
// waiting for a free connection.
func (e *myEngine) session(ctx context.Context, database string) (session, error) {
	if _, err := e.db(); err != nil {
		return nil, err
	}
	own := sql.OpenDB(e.connector)
	own.SetMaxOpenConns(1)
	conn, err := own.Conn(ctx)
	if err != nil {
		_ = own.Close()
		return nil, myErr(err)
	}
	s := &mySession{e: e, conn: conn, own: own}
	fail := func(err error) (session, error) {
		s.close()
		return nil, myErr(err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT CONNECTION_ID()`).Scan(&s.id); err != nil {
		return fail(err)
	}
	if database != "" {
		if _, err := conn.ExecContext(ctx, "USE "+myIdent(database)); err != nil {
			return fail(err)
		}
	}
	if e.cfg.ReadOnly {
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			return fail(err)
		}
	}
	return s, nil
}

func (e *myEngine) close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
}

// mySession runs on one pooled connection held for the tab. A run is NOT
// tied to the caller's context: the driver closes the connection when a
// context ends mid-query, which would cost the session. Cancel sends KILL
// QUERY from another connection instead.
type mySession struct {
	e    *myEngine
	conn *sql.Conn
	own  *sql.DB // the session's own one-connection pool
	id   int64
}

func (s *mySession) run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result {
	bg := context.WithoutCancel(ctx)
	// The caller's context ending (an MCP call given up, a timeout) stops
	// the statement the only way MySQL can without losing the session.
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	if !readOnly {
		return s.exec(bg, stmt, maxRows)
	}
	if _, err := s.conn.ExecContext(bg, "START TRANSACTION READ ONLY"); err != nil {
		return errResult(myErr(err))
	}
	defer func() { _, _ = s.conn.ExecContext(bg, "ROLLBACK") }()
	return s.exec(bg, stmt, maxRows)
}

func (s *mySession) exec(ctx context.Context, stmt string, maxRows int) *Result {
	if !returnsRows(EngineMySQL, stmt) {
		r := &Result{RowsAffected: -1}
		res, err := s.conn.ExecContext(ctx, stmt)
		if err != nil {
			r.Error = myErr(err).Error()
			return r
		}
		if n, err := res.RowsAffected(); err == nil {
			r.RowsAffected = n
		}
		return r
	}
	var onCut func()
	if IsRead(EngineMySQL, stmt) {
		// Past the bound a read is stopped on the server; a write that
		// returns rows (CALL) is read to the end instead.
		onCut = s.cancel
	}
	return myRead(ctx, s.conn, stmt, maxRows, onCut)
}

// myRead runs a statement that returns rows. Past the bounds onCut (KILL
// QUERY) stops the server, whose "interrupted" error is then expected;
// with no onCut the rest is read and dropped. The protocol has no other
// way to end a result early.
func myRead(ctx context.Context, conn *sql.Conn, stmt string, maxRows int, onCut func()) *Result {
	r := &Result{RowsAffected: -1}
	rows, err := conn.QueryContext(ctx, stmt)
	if err != nil {
		r.Error = myErr(err).Error()
		return r
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		r.Error = myErr(err).Error()
		return r
	}
	r.HasRows = len(cts) > 0
	bin := make([]bool, len(cts))
	for i, ct := range cts {
		t := ct.DatabaseTypeName()
		r.Columns = append(r.Columns, ResultColumn{Name: ct.Name(), Type: strings.ToLower(t)})
		switch t {
		case "BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "GEOMETRY", "BIT":
			bin[i] = true
		}
	}
	rr := &rowReader{r: r, maxRows: maxRows, isBinary: bin, hexPref: "0x"}
	vals := make([]sql.RawBytes, len(cts))
	ptrs := make([]any, len(cts))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if rr.stopped {
			continue
		}
		if err := rows.Scan(ptrs...); err != nil {
			r.Error = myErr(err).Error()
			return r
		}
		raw := make([][]byte, len(vals))
		for i, v := range vals {
			if v != nil {
				raw[i] = v
			}
		}
		if !rr.add(raw) && onCut != nil {
			onCut()
			break
		}
	}
	if err := rows.Err(); err != nil && !(rr.stopped && onCut != nil) {
		r.Error = myErr(err).Error()
		return r
	}
	if !rr.stopped {
		r.RowsAffected = int64(len(r.Rows))
	}
	return r
}

func (s *mySession) cancel() {
	db, err := s.e.db()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", s.id))
}

func (s *mySession) inTx() bool { return false }

func (s *mySession) close() {
	_ = s.conn.Close()
	_ = s.own.Close()
}

// myErr formats a server error with its number and SQLSTATE.
func myErr(err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		state := ""
		if me.SQLState != [5]byte{} {
			state = " (" + string(me.SQLState[:]) + ")"
		}
		return fmt.Errorf("ERROR %d%s: %s", me.Number, state, me.Message)
	}
	return err
}
