package dbx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
)

// oraEngine is one small pool for the whole server. The connection goes
// to one service (Config.Database: a service name, or "sid:NAME" for a
// SID); the tree's databases are the schemas in it, and a session moves
// to one with ALTER SESSION SET CURRENT_SCHEMA.
//
// Sessions autocommit, as every other engine here does outside an
// explicit transaction: Oracle has no BEGIN, so a unit of work that must
// commit together goes in a PL/SQL block. A read-only run is the
// exception: it runs in a SET TRANSACTION READ ONLY that is rolled back.
type oraEngine struct {
	cfg Config

	once      sync.Once
	connector driver.Connector
	pool      *sql.DB
	err       error
}

func newOra(cfg Config) *oraEngine { return &oraEngine{cfg: cfg} }

// oraDialer adapts the app's dialer to go-ora's.
type oraDialer struct{ dial DialFunc }

func (d oraDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := d.dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return noDeadlineConn{c}, nil
}

// noDeadlineConn ignores a deadline the connection cannot keep: an SSH
// channel refuses them, and go-ora gives up the connection on that
// error. A stuck statement is stopped through the context instead (the
// driver sends a break).
type noDeadlineConn struct{ net.Conn }

func (c noDeadlineConn) SetDeadline(t time.Time) error {
	_ = c.Conn.SetDeadline(t)
	return nil
}

func (c noDeadlineConn) SetReadDeadline(t time.Time) error {
	_ = c.Conn.SetReadDeadline(t)
	return nil
}

func (c noDeadlineConn) SetWriteDeadline(t time.Time) error {
	_ = c.Conn.SetWriteDeadline(t)
	return nil
}

// oraTarget splits Config.Database into the service name or SID.
func oraTarget(database string) (service, sid string) {
	database = strings.TrimSpace(database)
	if len(database) > 4 && strings.EqualFold(database[:4], "sid:") {
		return "", strings.TrimSpace(database[4:])
	}
	return database, ""
}

func (e *oraEngine) db() (*sql.DB, error) {
	e.once.Do(func() {
		host, portStr, err := net.SplitHostPort(e.cfg.Addr)
		if err != nil {
			e.err = err
			return
		}
		port, _ := strconv.Atoi(portStr)
		service, sid := oraTarget(e.cfg.Database)
		if service == "" && sid == "" {
			e.err = errors.New("Oracle needs a service name: put it in the Database field (or sid:NAME for a SID)")
			return
		}
		opts := map[string]string{
			"CONNECTION TIMEOUT": "20",
			"PROGRAM":            e.cfg.AppName,
		}
		if sid != "" {
			opts["SID"] = sid
		}
		tlsCfg := e.cfg.tlsConfig()
		if tlsCfg != nil {
			opts["SSL"] = "true"
			opts["SSL VERIFY"] = strconv.FormatBool(e.cfg.TLS == TLSVerifyFull)
		}
		c := go_ora.NewConnector(go_ora.BuildUrl(host, port, service, e.cfg.User, e.cfg.Password, opts))
		oc, ok := c.(*go_ora.OracleConnector)
		if !ok {
			e.err = errors.New("unexpected Oracle connector")
			return
		}
		oc.Dialer(oraDialer{e.cfg.dialer()})
		if tlsCfg != nil {
			oc.WithTLSConfig(tlsCfg)
		}
		e.connector = c
		e.pool = sql.OpenDB(c)
		e.pool.SetMaxOpenConns(3)
		e.pool.SetMaxIdleConns(2)
		e.pool.SetConnMaxIdleTime(5 * time.Minute)
	})
	return e.pool, e.err
}

func (e *oraEngine) info(ctx context.Context) (*ServerInfo, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	in := &ServerInfo{Engine: EngineOracle}
	if err := db.QueryRowContext(ctx, `SELECT USER, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL`).Scan(&in.User, &in.Database); err != nil {
		return nil, err
	}
	// PRODUCT_COMPONENT_VERSION is readable by every login; VERSION_FULL
	// is 18c+, VERSION before it.
	var prod, ver string
	if err := db.QueryRowContext(ctx, `SELECT PRODUCT, VERSION_FULL FROM PRODUCT_COMPONENT_VERSION WHERE PRODUCT LIKE 'Oracle%' AND ROWNUM = 1`).Scan(&prod, &ver); err != nil {
		if err := db.QueryRowContext(ctx, `SELECT PRODUCT, VERSION FROM PRODUCT_COMPONENT_VERSION WHERE PRODUCT LIKE 'Oracle%' AND ROWNUM = 1`).Scan(&prod, &ver); err != nil {
			return in, nil //nolint:nilerr // the version is a nicety; the login works
		}
	}
	in.Version = strings.TrimSpace(prod) + " " + ver
	return in, nil
}

// databases lists the schemas that own a table or view, and the login's
// own. Oracle-maintained ones (SYS, SYSTEM, MDSYS, ...) are system.
func (e *oraEngine) databases(ctx context.Context) ([]Database, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT u.USERNAME, u.ORACLE_MAINTAINED
		FROM ALL_USERS u
		WHERE u.USERNAME = USER OR EXISTS (
			SELECT 1 FROM ALL_OBJECTS o
			WHERE o.OWNER = u.USERNAME AND o.OBJECT_TYPE IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW'))
		ORDER BY u.USERNAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		var maint sql.NullString
		if err := rows.Scan(&d.Name, &maint); err != nil {
			return nil, err
		}
		d.SizeBytes = -1
		d.System = maint.String == "Y"
		out = append(out, d)
	}
	return out, rows.Err()
}

func (e *oraEngine) isSystemSchema(ctx context.Context, db *sql.DB, schema string) bool {
	var maint sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT ORACLE_MAINTAINED FROM ALL_USERS WHERE USERNAME = :1`, schema).Scan(&maint)
	return maint.String == "Y"
}

func (e *oraEngine) objects(ctx context.Context, database string, _ bool) ([]Object, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	// A materialized view also lists as the TABLE that holds its rows;
	// the view wins. Dropped tables in the recycle bin are left out.
	rows, err := db.QueryContext(ctx, `
		SELECT o.OBJECT_NAME, o.OBJECT_TYPE, NVL(t.NUM_ROWS, -1), NVL(c.COMMENTS, ' ')
		FROM ALL_OBJECTS o
		LEFT JOIN ALL_TABLES t ON t.OWNER = o.OWNER AND t.TABLE_NAME = o.OBJECT_NAME AND o.OBJECT_TYPE = 'TABLE'
		LEFT JOIN ALL_TAB_COMMENTS c ON c.OWNER = o.OWNER AND c.TABLE_NAME = o.OBJECT_NAME
		WHERE o.OWNER = :1 AND o.OBJECT_TYPE IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW')
		  AND o.OBJECT_NAME NOT LIKE 'BIN$%' AND o.GENERATED = 'N'
		  AND NOT (o.OBJECT_TYPE = 'TABLE' AND EXISTS (
			SELECT 1 FROM ALL_MVIEWS m WHERE m.OWNER = o.OWNER AND m.MVIEW_NAME = o.OBJECT_NAME))
		ORDER BY o.OBJECT_NAME`, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sys := e.isSystemSchema(ctx, db, database)
	var out []Object
	for rows.Next() {
		var o Object
		var typ, comment string
		if err := rows.Scan(&o.Name, &typ, &o.Rows, &comment); err != nil {
			return nil, err
		}
		o.Comment = strings.TrimSpace(comment)
		o.SizeBytes = -1
		o.System = sys
		switch typ {
		case "VIEW":
			o.Kind, o.Rows = KindView, -1
		case "MATERIALIZED VIEW":
			o.Kind, o.Rows = KindMatView, -1
		default:
			o.Kind = KindTable
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func oraIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// oraType prints a column type the way DESCRIBE does.
func oraType(typ string, length, charLen int64, charUsed string, prec, scale sql.NullInt64) string {
	switch typ {
	case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR":
		if typ[0] == 'N' || charUsed == "C" {
			unit := ""
			if typ[0] != 'N' {
				unit = " CHAR"
			}
			return fmt.Sprintf("%s(%d%s)", typ, charLen, unit)
		}
		return fmt.Sprintf("%s(%d)", typ, length)
	case "RAW":
		return fmt.Sprintf("RAW(%d)", length)
	case "NUMBER":
		switch {
		case !prec.Valid && scale.Valid && scale.Int64 == 0:
			return "INTEGER"
		case !prec.Valid:
			return "NUMBER"
		case scale.Valid && scale.Int64 != 0:
			return fmt.Sprintf("NUMBER(%d,%d)", prec.Int64, scale.Int64)
		}
		return fmt.Sprintf("NUMBER(%d)", prec.Int64)
	case "FLOAT":
		if prec.Valid {
			return fmt.Sprintf("FLOAT(%d)", prec.Int64)
		}
	}
	return typ
}

func (e *oraEngine) describe(ctx context.Context, database, _, name string) (*TableDetail, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	var typ string
	err = db.QueryRowContext(ctx, `
		SELECT OBJECT_TYPE FROM ALL_OBJECTS WHERE OWNER = :1 AND OBJECT_NAME = :2
		  AND OBJECT_TYPE IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW')
		ORDER BY DECODE(OBJECT_TYPE, 'MATERIALIZED VIEW', 0, 1)`, database, name).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s.%s not found", database, name)
	}
	if err != nil {
		return nil, err
	}
	d := &TableDetail{Name: name, Kind: KindTable}
	switch typ {
	case "VIEW":
		d.Kind = KindView
	case "MATERIALIZED VIEW":
		d.Kind = KindMatView
	}

	pk := map[string]bool{}
	pkName := ""
	rows, err := db.QueryContext(ctx, `
		SELECT c.CONSTRAINT_NAME, cc.COLUMN_NAME FROM ALL_CONSTRAINTS c
		JOIN ALL_CONS_COLUMNS cc ON cc.OWNER = c.OWNER AND cc.CONSTRAINT_NAME = c.CONSTRAINT_NAME
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2 AND c.CONSTRAINT_TYPE = 'P'
		ORDER BY cc.POSITION`, database, name)
	if err != nil {
		return nil, err
	}
	var pkCols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&pkName, &col); err != nil {
			rows.Close()
			return nil, err
		}
		pk[col] = true
		pkCols = append(pkCols, col)
	}
	rows.Close()

	// DATA_DEFAULT is a LONG: read as text, it carries the default's
	// source with trailing whitespace.
	rows, err = db.QueryContext(ctx, `
		SELECT c.COLUMN_NAME, c.DATA_TYPE, c.DATA_LENGTH, NVL(c.CHAR_LENGTH, 0), NVL(c.CHAR_USED, ' '),
		       c.DATA_PRECISION, c.DATA_SCALE, c.NULLABLE, c.DATA_DEFAULT, NVL(m.COMMENTS, ' ')
		FROM ALL_TAB_COLUMNS c
		LEFT JOIN ALL_COL_COMMENTS m ON m.OWNER = c.OWNER AND m.TABLE_NAME = c.TABLE_NAME AND m.COLUMN_NAME = c.COLUMN_NAME
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2 ORDER BY c.COLUMN_ID`, database, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c Column
		var typ, charUsed, nullable, comment string
		var length, charLen int64
		var prec, scale sql.NullInt64
		var def sql.NullString
		if err := rows.Scan(&c.Name, &typ, &length, &charLen, &charUsed, &prec, &scale, &nullable, &def, &comment); err != nil {
			rows.Close()
			return nil, err
		}
		c.Type = oraType(typ, length, charLen, strings.TrimSpace(charUsed), prec, scale)
		c.Nullable = nullable == "Y"
		if def.Valid {
			v := strings.TrimSpace(def.String)
			c.Default = &v
		}
		if pk[c.Name] {
			c.Key = "PRI"
		}
		c.Comment = strings.TrimSpace(comment)
		d.Columns = append(d.Columns, c)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `
		SELECT i.INDEX_NAME, i.UNIQUENESS, ic.COLUMN_NAME, ie.COLUMN_EXPRESSION
		FROM ALL_INDEXES i
		JOIN ALL_IND_COLUMNS ic ON ic.INDEX_OWNER = i.OWNER AND ic.INDEX_NAME = i.INDEX_NAME
		LEFT JOIN ALL_IND_EXPRESSIONS ie ON ie.INDEX_OWNER = ic.INDEX_OWNER AND ie.INDEX_NAME = ic.INDEX_NAME
		  AND ie.COLUMN_POSITION = ic.COLUMN_POSITION
		WHERE i.TABLE_OWNER = :1 AND i.TABLE_NAME = :2 AND i.INDEX_TYPE <> 'LOB'
		ORDER BY i.INDEX_NAME, ic.COLUMN_POSITION`, database, name)
	if err != nil {
		return nil, err
	}
	byName := map[string]int{}
	for rows.Next() {
		var n, uniq, col string
		var expr sql.NullString
		if err := rows.Scan(&n, &uniq, &col, &expr); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := byName[n]
		if !ok {
			d.Indexes = append(d.Indexes, Index{Name: n, Unique: uniq == "UNIQUE", Primary: n == pkName})
			i = len(d.Indexes) - 1
			byName[n] = i
		}
		if expr.Valid && expr.String != "" {
			col = strings.TrimSpace(expr.String)
		}
		d.Indexes[i].Columns = append(d.Indexes[i].Columns, col)
	}
	rows.Close()
	// The primary key's index first, as the other engines list it.
	for i, ix := range d.Indexes {
		if ix.Primary && i > 0 {
			d.Indexes = append([]Index{ix}, append(d.Indexes[:i:i], d.Indexes[i+1:]...)...)
			break
		}
	}

	rows, err = db.QueryContext(ctx, `
		SELECT c.CONSTRAINT_NAME, cc.COLUMN_NAME, r.OWNER, r.TABLE_NAME, rc.COLUMN_NAME
		FROM ALL_CONSTRAINTS c
		JOIN ALL_CONS_COLUMNS cc ON cc.OWNER = c.OWNER AND cc.CONSTRAINT_NAME = c.CONSTRAINT_NAME
		JOIN ALL_CONSTRAINTS r ON r.OWNER = c.R_OWNER AND r.CONSTRAINT_NAME = c.R_CONSTRAINT_NAME
		JOIN ALL_CONS_COLUMNS rc ON rc.OWNER = r.OWNER AND rc.CONSTRAINT_NAME = r.CONSTRAINT_NAME AND rc.POSITION = cc.POSITION
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2 AND c.CONSTRAINT_TYPE = 'R'
		ORDER BY c.CONSTRAINT_NAME, cc.POSITION`, database, name)
	if err != nil {
		return nil, err
	}
	type fk struct {
		cols, rcols []string
		rs, rt      string
	}
	fks := map[string]*fk{}
	var order []string
	for rows.Next() {
		var n, col, rs, rt, rcol string
		if err := rows.Scan(&n, &col, &rs, &rt, &rcol); err != nil {
			rows.Close()
			return nil, err
		}
		f := fks[n]
		if f == nil {
			f = &fk{rs: rs, rt: rt}
			fks[n] = f
			order = append(order, n)
		}
		f.cols = append(f.cols, oraIdent(col))
		f.rcols = append(f.rcols, oraIdent(rcol))
	}
	rows.Close()
	for _, n := range order {
		f := fks[n]
		ref := oraIdent(f.rt)
		if f.rs != database {
			ref = oraIdent(f.rs) + "." + ref
		}
		d.ForeignKeys = append(d.ForeignKeys, ForeignKey{
			Name: n,
			Def:  fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", strings.Join(f.cols, ", "), ref, strings.Join(f.rcols, ", ")),
		})
	}

	// DBMS_METADATA needs SELECT_CATALOG_ROLE for another schema's
	// objects; without it the DDL is rebuilt from the columns.
	kind := "TABLE"
	switch d.Kind {
	case KindView:
		kind = "VIEW"
	case KindMatView:
		kind = "MATERIALIZED_VIEW"
	}
	var ddl sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT DBMS_METADATA.GET_DDL(:1, :2, :3) FROM DUAL`, kind, name, database).Scan(&ddl); err == nil && ddl.Valid {
		d.DDL = strings.TrimSpace(ddl.String) + ";"
	} else if d.Kind == KindTable {
		d.DDL = oraRebuildDDL(database, name, d.Columns, pkCols)
		d.DDLNote = "Rebuilt from the catalog: DBMS_METADATA refused (it needs SELECT_CATALOG_ROLE for another schema's tables)."
	}
	return d, nil
}

func oraRebuildDDL(schema, name string, cols []Column, pk []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s.%s (\n", oraIdent(schema), oraIdent(name))
	for i, c := range cols {
		fmt.Fprintf(&b, "  %s %s", oraIdent(c.Name), c.Type)
		if c.Default != nil {
			b.WriteString(" DEFAULT " + *c.Default)
		}
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if i < len(cols)-1 || len(pk) > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if len(pk) > 0 {
		q := make([]string, len(pk))
		for i, c := range pk {
			q[i] = oraIdent(c)
		}
		fmt.Fprintf(&b, "  PRIMARY KEY (%s)\n", strings.Join(q, ", "))
	}
	b.WriteString(");")
	return b.String()
}

func (e *oraEngine) browse(ctx context.Context, database, _, name string, offset, limit int) (*Result, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT cc.COLUMN_NAME FROM ALL_CONSTRAINTS c
		JOIN ALL_CONS_COLUMNS cc ON cc.OWNER = c.OWNER AND cc.CONSTRAINT_NAME = c.CONSTRAINT_NAME
		WHERE c.OWNER = :1 AND c.TABLE_NAME = :2 AND c.CONSTRAINT_TYPE = 'P' ORDER BY cc.POSITION`, database, name)
	if err != nil {
		return nil, err
	}
	var pk []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		pk = append(pk, oraIdent(c))
	}
	rows.Close()
	// OFFSET / FETCH is 12c+; 11g is long out of support.
	q := "SELECT * FROM " + oraIdent(database) + "." + oraIdent(name)
	if len(pk) > 0 {
		q += " ORDER BY " + strings.Join(pk, ", ")
	}
	q += fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	start := time.Now()
	r := sqlRead(ctx, db, q, limit, oraDialect, nil)
	r.Statement = q
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

// session opens a query tab's connection outside the tree pool (see
// mySession for why).
func (e *oraEngine) session(ctx context.Context, database string) (session, error) {
	if _, err := e.db(); err != nil {
		return nil, err
	}
	own := sql.OpenDB(e.connector)
	own.SetMaxOpenConns(1)
	conn, err := own.Conn(ctx)
	if err != nil {
		_ = own.Close()
		return nil, oraErr(err)
	}
	s := &oraSession{conn: conn, own: own}
	if database != "" {
		if _, err := conn.ExecContext(ctx, "ALTER SESSION SET CURRENT_SCHEMA = "+oraIdent(database)); err != nil {
			s.close()
			return nil, oraErr(err)
		}
	}
	// DBMS_OUTPUT is collected after each block (oraSession.output).
	_, _ = conn.ExecContext(ctx, "BEGIN DBMS_OUTPUT.ENABLE(NULL); END;")
	return s, nil
}

func (e *oraEngine) close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
}

type oraSession struct {
	conn *sql.Conn
	own  *sql.DB

	mu   sync.Mutex
	stop context.CancelFunc
}

func (s *oraSession) run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result {
	ctx, stop := context.WithCancel(ctx)
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.stop = nil
		s.mu.Unlock()
		stop()
	}()
	if !readOnly {
		return s.exec(ctx, s.conn, stmt, maxRows)
	}
	// A transaction holds the connection out of autocommit; READ ONLY has
	// to be its first statement.
	tx, err := s.conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return errResult(oraErr(err))
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return errResult(oraErr(err))
	}
	return s.exec(ctx, tx, stmt, maxRows)
}

type oraConn interface {
	sqlQuerier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *oraSession) exec(ctx context.Context, c oraConn, stmt string, maxRows int) *Result {
	if readLead(EngineOracle, leadKeyword(EngineOracle, stmt)) {
		return sqlRead(ctx, c, stmt, maxRows, oraDialect, s.cancel)
	}
	// No Command: the grid then shows the affected count, which Oracle
	// does not put in a command tag the way Postgres does.
	r := &Result{RowsAffected: -1}
	block := blockStmt(EngineOracle, stmt)
	res, err := c.ExecContext(ctx, stmt)
	if err != nil {
		r.Error = oraErr(err).Error()
	} else if n, err := res.RowsAffected(); err == nil && !block && isDML(EngineOracle, stmt) {
		r.RowsAffected = n
	}
	if block {
		if out := s.output(context.WithoutCancel(ctx), c); out != "" {
			r.Note = out
		}
	}
	return r
}

// output collects what a block wrote with DBMS_OUTPUT.PUT_LINE.
func (s *oraSession) output(ctx context.Context, c oraConn) string {
	const maxLines = 1000
	var lines []string
	for len(lines) < maxLines {
		var line sql.NullString
		var status int64
		// An OUT string needs its size up front; a line is at most 32767.
		if _, err := c.ExecContext(ctx, "BEGIN DBMS_OUTPUT.GET_LINE(:1, :2); END;",
			go_ora.Out{Dest: &line, Size: 32767}, go_ora.Out{Dest: &status}); err != nil || status != 0 {
			break
		}
		lines = append(lines, line.String)
	}
	return strings.Join(lines, "\n")
}

func (s *oraSession) cancel() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *oraSession) inTx() bool { return false }

func (s *oraSession) close() {
	_ = s.conn.Close()
	_ = s.own.Close()
}

// isDML: a statement whose affected count means something (DDL and
// blocks report 0 or 1 of nothing).
func isDML(engine, stmt string) bool {
	switch leadKeyword(engine, stmt) {
	case "INSERT", "UPDATE", "DELETE", "MERGE":
		return true
	}
	return false
}

var oraDialect = sqlDialect{cell: oraCell, typeName: oraTypeName, errf: oraErr}

// oraTypeNames maps go-ora's wire type names to the SQL names: the wire
// calls VARCHAR2 "NCHAR" (type 1), and a CLOB a locator.
var oraTypeNames = map[string]string{
	"NCHAR": "VARCHAR2", "CHAR": "CHAR", "LongVarChar": "LONG", "LONG": "LONG",
	"RAW": "RAW", "LongRaw": "LONG RAW", "LongVarRaw": "LONG RAW",
	"IBFloat": "BINARY_FLOAT", "IBDouble": "BINARY_DOUBLE", "BFloat": "BINARY_FLOAT", "BDouble": "BINARY_DOUBLE",
	"OCIClobLocator": "CLOB", "OCIBlobLocator": "BLOB", "OCIFileLocator": "BFILE",
	"TimeStampDTY": "TIMESTAMP", "TIMESTAMP": "TIMESTAMP",
	"TimeStampTZ_DTY": "TIMESTAMP WITH TIME ZONE", "TIMESTAMPTZ": "TIMESTAMP WITH TIME ZONE",
	"TimeStampLTZ_DTY": "TIMESTAMP WITH LOCAL TIME ZONE", "TimeStampeLTZ": "TIMESTAMP WITH LOCAL TIME ZONE",
	"IntervalYM_DTY": "INTERVAL YEAR TO MONTH", "IntervalYM": "INTERVAL YEAR TO MONTH",
	"IntervalDS_DTY": "INTERVAL DAY TO SECOND", "IntervalDS": "INTERVAL DAY TO SECOND",
	"XMLType": "XMLTYPE", "OCIXMLType": "XMLTYPE", "Boolean": "BOOLEAN", "REFCURSOR": "REF CURSOR",
}

func oraTypeName(t string) string {
	if n, ok := oraTypeNames[t]; ok {
		return n
	}
	return t
}

// oraCell prints a value as SQL*Plus would, without its NLS formatting:
// DATE and TIMESTAMP in ISO order, RAW and BLOB as hex.
func oraCell(dbType string, v any) string {
	if t, ok := v.(time.Time); ok {
		return timeText(t, strings.Contains(dbType, "TZ") || strings.Contains(dbType, "TIME ZONE"))
	}
	return cellText(v)
}

// oraErr keeps the server's ORA-nnnnn text; go-ora already formats it.
func oraErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.TrimSpace(err.Error()))
}
