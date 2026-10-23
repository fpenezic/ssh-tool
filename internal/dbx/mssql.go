package dbx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-sql/sqlexp"
	mssql "github.com/microsoft/go-mssqldb"
)

// msEngine is one small pool for the whole server: a connection reaches
// every database, names are qualified with it instead ([db].sys.tables).
// Named instances are not looked up (SQL Browser is UDP, which no tunnel
// carries): the address names the instance's port.
type msEngine struct {
	cfg Config

	once      sync.Once
	connector *mssql.Connector
	pool      *sql.DB
	err       error
}

func newMS(cfg Config) *msEngine { return &msEngine{cfg: cfg} }

// msDialer dials through the app's dialer. As a HostDialer it gets the
// host name unresolved: it resolves on the far side of the tunnel.
type msDialer struct {
	dial DialFunc
	host string
}

func (d msDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr)
}

func (d msDialer) HostName() string { return d.host }

func (e *msEngine) db() (*sql.DB, error) {
	e.once.Do(func() {
		host, _, err := net.SplitHostPort(e.cfg.Addr)
		if err != nil {
			e.err = err
			return
		}
		q := url.Values{}
		q.Set("app name", e.cfg.AppName)
		q.Set("dial timeout", "20")
		if e.cfg.Database != "" {
			q.Set("database", e.cfg.Database)
		}
		// "false" still encrypts the login packet, the password with it,
		// without checking the certificate (as Off works in SSMS); "disable"
		// would send it in the clear and most servers refuse it.
		switch e.cfg.TLS {
		case TLSRequire:
			q.Set("encrypt", "true")
			q.Set("TrustServerCertificate", "true")
		case TLSVerifyFull:
			q.Set("encrypt", "true")
			q.Set("hostNameInCertificate", host)
		default:
			q.Set("encrypt", "false")
			q.Set("TrustServerCertificate", "true")
		}
		u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(e.cfg.User, e.cfg.Password), Host: e.cfg.Addr, RawQuery: q.Encode()}
		c, err := mssql.NewConnector(u.String())
		if err != nil {
			e.err = err
			return
		}
		c.Dialer = msDialer{dial: e.cfg.dialer(), host: host}
		e.connector = c
		e.pool = sql.OpenDB(c)
		e.pool.SetMaxOpenConns(3)
		e.pool.SetMaxIdleConns(2)
		e.pool.SetConnMaxIdleTime(5 * time.Minute)
	})
	return e.pool, e.err
}

func (e *msEngine) info(ctx context.Context) (*ServerInfo, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	in := &ServerInfo{Engine: EngineMSSQL}
	var edition, ver string
	if err := db.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('Edition') AS nvarchar(128)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), SUSER_SNAME(), DB_NAME()`).Scan(&edition, &ver, &in.User, &in.Database); err != nil {
		return nil, msErr(err)
	}
	in.Version = "SQL Server " + ver + " " + edition
	return in, nil
}

func (e *msEngine) databases(ctx context.Context) ([]Database, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	// Sizes from sys.master_files need VIEW ANY DEFINITION; a login
	// without it sees -1. Databases the login cannot enter are left out.
	rows, err := db.QueryContext(ctx, `
		SELECT d.name, d.database_id,
		       COALESCE((SELECT SUM(CAST(f.size AS bigint)) * 8192 FROM sys.master_files f WHERE f.database_id = d.database_id), -1)
		FROM sys.databases d
		WHERE HAS_DBACCESS(d.name) = 1 AND d.state = 0
		ORDER BY d.name`)
	if err != nil {
		return nil, msErr(err)
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		var id int64
		if err := rows.Scan(&d.Name, &id, &d.SizeBytes); err != nil {
			return nil, msErr(err)
		}
		d.System = id <= 4 // master, tempdb, model, msdb
		out = append(out, d)
	}
	return out, msErr(rows.Err())
}

func msIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

var msSystemSchemas = map[string]bool{"sys": true, "INFORMATION_SCHEMA": true, "guest": true}

func (e *msEngine) objects(ctx context.Context, database string, system bool) ([]Object, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	d := msIdent(database)
	// Row counts from the partition stats (heap or clustered index), as
	// SSMS shows them; no table scan.
	rows, err := db.QueryContext(ctx, `
		SELECT s.name, o.name, o.type, o.is_ms_shipped,
		       COALESCE((SELECT SUM(p.rows) FROM `+d+`.sys.partitions p WHERE p.object_id = o.object_id AND p.index_id IN (0, 1)), -1),
		       COALESCE((SELECT SUM(a.total_pages) * 8192 FROM `+d+`.sys.partitions p JOIN `+d+`.sys.allocation_units a ON a.container_id = p.partition_id WHERE p.object_id = o.object_id), -1),
		       COALESCE(CAST(ep.value AS nvarchar(4000)), N'')
		FROM `+d+`.sys.objects o
		JOIN `+d+`.sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN `+d+`.sys.extended_properties ep ON ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.class = 1 AND ep.name = 'MS_Description'
		WHERE o.type IN ('U', 'V')
		ORDER BY s.name, o.name`)
	if err != nil {
		return nil, msErr(err)
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		var o Object
		var typ string
		var shipped bool
		if err := rows.Scan(&o.Schema, &o.Name, &typ, &shipped, &o.Rows, &o.SizeBytes, &o.Comment); err != nil {
			return nil, msErr(err)
		}
		o.Kind = KindTable
		if strings.TrimSpace(typ) == "V" {
			o.Kind = KindView
			o.Rows, o.SizeBytes = -1, -1
		}
		o.System = shipped || msSystemSchemas[o.Schema]
		if o.System && !system {
			continue
		}
		out = append(out, o)
	}
	return out, msErr(rows.Err())
}

// msType prints a column type the way SSMS scripts it.
func msType(typ string, maxLen, prec, scale int64) string {
	switch typ {
	case "varchar", "char", "varbinary", "binary":
		if maxLen < 0 {
			return typ + "(max)"
		}
		return fmt.Sprintf("%s(%d)", typ, maxLen)
	case "nvarchar", "nchar":
		if maxLen < 0 {
			return typ + "(max)"
		}
		return fmt.Sprintf("%s(%d)", typ, maxLen/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", typ, prec, scale)
	case "datetime2", "datetimeoffset", "time":
		return fmt.Sprintf("%s(%d)", typ, scale)
	}
	return typ
}

func (e *msEngine) describe(ctx context.Context, database, schema, name string) (*TableDetail, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	d := msIdent(database)
	var objID int64
	var typ string
	err = db.QueryRowContext(ctx, `
		SELECT o.object_id, o.type FROM `+d+`.sys.objects o JOIN `+d+`.sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name = @p1 AND o.name = @p2 AND o.type IN ('U', 'V')`, schema, name).Scan(&objID, &typ)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s.%s.%s not found", database, schema, name)
	}
	if err != nil {
		return nil, msErr(err)
	}
	det := &TableDetail{Schema: schema, Name: name, Kind: KindTable}
	if strings.TrimSpace(typ) == "V" {
		det.Kind = KindView
	}

	rows, err := db.QueryContext(ctx, `
		SELECT c.name, t.name, c.max_length, c.precision, c.scale, c.is_nullable, c.is_identity, c.is_computed,
		       dc.definition, cc.definition,
		       CASE WHEN EXISTS (SELECT 1 FROM `+d+`.sys.index_columns ic JOIN `+d+`.sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
		                         WHERE i.is_primary_key = 1 AND ic.object_id = c.object_id AND ic.column_id = c.column_id) THEN 1 ELSE 0 END,
		       COALESCE(CAST(ep.value AS nvarchar(4000)), N'')
		FROM `+d+`.sys.columns c
		JOIN `+d+`.sys.types t ON t.user_type_id = c.user_type_id
		LEFT JOIN `+d+`.sys.default_constraints dc ON dc.object_id = c.default_object_id
		LEFT JOIN `+d+`.sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		LEFT JOIN `+d+`.sys.extended_properties ep ON ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.class = 1 AND ep.name = 'MS_Description'
		WHERE c.object_id = @p1 ORDER BY c.column_id`, objID)
	if err != nil {
		return nil, msErr(err)
	}
	for rows.Next() {
		var c Column
		var typ string
		var maxLen, prec, scale int64
		var nullable, identity, computed, pk bool
		var def, comp sql.NullString
		if err := rows.Scan(&c.Name, &typ, &maxLen, &prec, &scale, &nullable, &identity, &computed, &def, &comp, &pk, &c.Comment); err != nil {
			rows.Close()
			return nil, msErr(err)
		}
		c.Type = msType(typ, maxLen, prec, scale)
		c.Nullable = nullable
		if def.Valid {
			c.Default = &def.String
		}
		if pk {
			c.Key = "PRI"
		}
		switch {
		case identity:
			c.Extra = "identity"
		case computed && comp.Valid:
			c.Extra = "computed " + comp.String
		}
		det.Columns = append(det.Columns, c)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `
		SELECT i.name, i.is_unique, i.is_primary_key, c.name, ic.is_included_column, i.type_desc
		FROM `+d+`.sys.indexes i
		JOIN `+d+`.sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN `+d+`.sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.object_id = @p1 AND i.name IS NOT NULL
		ORDER BY i.is_primary_key DESC, i.name, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, objID)
	if err != nil {
		return nil, msErr(err)
	}
	byName := map[string]int{}
	for rows.Next() {
		var n, col, kind string
		var uniq, primary, included bool
		if err := rows.Scan(&n, &uniq, &primary, &col, &included, &kind); err != nil {
			rows.Close()
			return nil, msErr(err)
		}
		i, ok := byName[n]
		if !ok {
			det.Indexes = append(det.Indexes, Index{Name: n, Unique: uniq, Primary: primary, Def: strings.ToLower(kind)})
			i = len(det.Indexes) - 1
			byName[n] = i
		}
		if included {
			col += " (included)"
		}
		det.Indexes[i].Columns = append(det.Indexes[i].Columns, col)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `
		SELECT fk.name, pc.name, rs.name, rt.name, rc.name
		FROM `+d+`.sys.foreign_keys fk
		JOIN `+d+`.sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN `+d+`.sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN `+d+`.sys.tables rt ON rt.object_id = fkc.referenced_object_id
		JOIN `+d+`.sys.schemas rs ON rs.schema_id = rt.schema_id
		JOIN `+d+`.sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE fk.parent_object_id = @p1
		ORDER BY fk.name, fkc.constraint_column_id`, objID)
	if err != nil {
		return nil, msErr(err)
	}
	type fk struct {
		cols, rcols []string
		ref         string
	}
	fks := map[string]*fk{}
	var order []string
	for rows.Next() {
		var n, col, rs, rt, rcol string
		if err := rows.Scan(&n, &col, &rs, &rt, &rcol); err != nil {
			rows.Close()
			return nil, msErr(err)
		}
		f := fks[n]
		if f == nil {
			f = &fk{ref: msIdent(rs) + "." + msIdent(rt)}
			fks[n] = f
			order = append(order, n)
		}
		f.cols = append(f.cols, msIdent(col))
		f.rcols = append(f.rcols, msIdent(rcol))
	}
	rows.Close()
	for _, n := range order {
		f := fks[n]
		det.ForeignKeys = append(det.ForeignKeys, ForeignKey{
			Name: n,
			Def:  fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", strings.Join(f.cols, ", "), f.ref, strings.Join(f.rcols, ", ")),
		})
	}

	if det.Kind == KindView {
		var def sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT definition FROM `+d+`.sys.sql_modules WHERE object_id = @p1`, objID).Scan(&def)
		det.DDL = strings.TrimSpace(def.String)
	} else {
		det.DDL = msRebuildDDL(schema, name, det)
		det.DDLNote = "Rebuilt from the catalog (SQL Server has no SHOW CREATE TABLE)."
	}
	return det, nil
}

func msRebuildDDL(schema, name string, det *TableDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s.%s (\n", msIdent(schema), msIdent(name))
	var lines []string
	for _, c := range det.Columns {
		if strings.HasPrefix(c.Extra, "computed ") {
			lines = append(lines, fmt.Sprintf("  %s AS %s", msIdent(c.Name), strings.TrimPrefix(c.Extra, "computed ")))
			continue
		}
		l := fmt.Sprintf("  %s %s", msIdent(c.Name), c.Type)
		if c.Extra == "identity" {
			l += " IDENTITY"
		}
		if !c.Nullable {
			l += " NOT NULL"
		}
		if c.Default != nil {
			l += " DEFAULT " + *c.Default
		}
		lines = append(lines, l)
	}
	for _, ix := range det.Indexes {
		if ix.Primary {
			q := make([]string, len(ix.Columns))
			for i, c := range ix.Columns {
				q[i] = msIdent(c)
			}
			lines = append(lines, fmt.Sprintf("  CONSTRAINT %s PRIMARY KEY (%s)", msIdent(ix.Name), strings.Join(q, ", ")))
		}
	}
	for _, f := range det.ForeignKeys {
		lines = append(lines, fmt.Sprintf("  CONSTRAINT %s %s", msIdent(f.Name), f.Def))
	}
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n);")
	return b.String()
}

func (e *msEngine) browse(ctx context.Context, database, schema, name string, offset, limit int) (*Result, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	d := msIdent(database)
	rows, err := db.QueryContext(ctx, `
		SELECT c.name FROM `+d+`.sys.indexes i
		JOIN `+d+`.sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN `+d+`.sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		JOIN `+d+`.sys.objects o ON o.object_id = i.object_id
		JOIN `+d+`.sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name = @p1 AND o.name = @p2 AND i.is_primary_key = 1 ORDER BY ic.key_ordinal`, schema, name)
	if err != nil {
		return nil, msErr(err)
	}
	var pk []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, msErr(err)
		}
		pk = append(pk, msIdent(c))
	}
	rows.Close()
	// OFFSET / FETCH needs an ORDER BY; without a key, any order will do.
	order := "(SELECT NULL)"
	if len(pk) > 0 {
		order = strings.Join(pk, ", ")
	}
	q := fmt.Sprintf("SELECT * FROM %s.%s.%s ORDER BY %s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY",
		d, msIdent(schema), msIdent(name), order, offset, limit)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, msErr(err)
	}
	defer conn.Close()
	start := time.Now()
	r := msRead(ctx, conn, q, limit, nil)
	r.Statement = q
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

// session opens a query tab's connection outside the tree pool (see
// mySession for why).
func (e *msEngine) session(ctx context.Context, database string) (session, error) {
	if _, err := e.db(); err != nil {
		return nil, err
	}
	own := sql.OpenDB(e.connector)
	own.SetMaxOpenConns(1)
	conn, err := own.Conn(ctx)
	if err != nil {
		_ = own.Close()
		return nil, msErr(err)
	}
	s := &msSession{conn: conn, own: own}
	if database != "" {
		if _, err := conn.ExecContext(ctx, "USE "+msIdent(database)); err != nil {
			s.close()
			return nil, msErr(err)
		}
	}
	return s, nil
}

func (e *msEngine) close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
}

// msSession is SQL Server's own: no autocommit to turn off, BEGIN TRAN
// holds a transaction across runs, and @@TRANCOUNT says whether one is
// open. A cancelled context sends an attention and keeps the connection.
type msSession struct {
	conn *sql.Conn
	own  *sql.DB

	mu    sync.Mutex
	stop  context.CancelFunc
	trans bool
}

func (s *msSession) run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result {
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
	var r *Result
	if readOnly {
		// SQL Server has no read-only transaction: the statement check
		// (msWriteWords) is the guard, the rollback a second net.
		bg := context.WithoutCancel(ctx)
		if _, err := s.conn.ExecContext(bg, "BEGIN TRANSACTION"); err != nil {
			return errResult(msErr(err))
		}
		r = msRead(ctx, s.conn, stmt, maxRows, s.cancel)
		_, _ = s.conn.ExecContext(bg, "IF @@TRANCOUNT > 0 ROLLBACK")
	} else {
		var onCut func()
		if IsRead(EngineMSSQL, stmt) {
			onCut = s.cancel
		}
		r = msRead(ctx, s.conn, stmt, maxRows, onCut)
	}
	var n int64
	if err := s.conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT @@TRANCOUNT").Scan(&n); err == nil {
		s.mu.Lock()
		s.trans = n > 0
		s.mu.Unlock()
	}
	return r
}

// msRead runs a batch and reads its first result set that has columns;
// later ones are counted in Note, PRINT output is added to it, and with no
// result set the affected counts add up to RowsAffected.
func msRead(ctx context.Context, conn *sql.Conn, stmt string, maxRows int, onCut func()) *Result {
	r := &Result{RowsAffected: -1}
	msg := &sqlexp.ReturnMessage{}
	rows, err := conn.QueryContext(ctx, stmt, msg)
	if err != nil {
		r.Error = msErr(err).Error()
		return r
	}
	defer rows.Close()
	var notices []string
	var affected int64 = -1
	more := 0
	var rr *rowReader
	var types []string
	var vals, ptrs []any
	for active := true; active; {
		switch m := msg.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			notices = append(notices, m.Message.String())
		case sqlexp.MsgError:
			if r.Error == "" {
				r.Error = msErr(m.Error).Error()
			}
		case sqlexp.MsgRowsAffected:
			if affected < 0 {
				affected = 0
			}
			affected += m.Count
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		case sqlexp.MsgNext:
			cts, err := rows.ColumnTypes()
			if err != nil || len(cts) == 0 {
				for rows.Next() {
				}
				continue
			}
			if r.HasRows {
				more++
				for rows.Next() {
				}
				continue
			}
			r.HasRows = true
			types = make([]string, len(cts))
			for i, ct := range cts {
				types[i] = ct.DatabaseTypeName()
				r.Columns = append(r.Columns, ResultColumn{Name: ct.Name(), Type: strings.ToLower(types[i])})
			}
			rr = &rowReader{r: r, maxRows: maxRows}
			vals = make([]any, len(cts))
			ptrs = make([]any, len(cts))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			for rows.Next() {
				if rr.stopped {
					continue
				}
				if err := rows.Scan(ptrs...); err != nil {
					r.Error = msErr(err).Error()
					return r
				}
				raw := make([][]byte, len(vals))
				for i, v := range vals {
					if v != nil {
						raw[i] = []byte(msCell(types[i], v))
					}
				}
				if !rr.add(raw) && onCut != nil {
					onCut()
					return r
				}
			}
		default:
			// The context ended (Stop): the driver has sent an attention.
			if ctx.Err() != nil {
				if r.Error == "" {
					r.Error = ctx.Err().Error()
				}
				active = false
			}
		}
	}
	if err := ctx.Err(); err != nil && r.Error == "" {
		// Stopped: the driver sent an attention and ended the batch
		// without an error of its own.
		r.Error = "Query cancelled: " + err.Error()
	}
	if r.HasRows {
		if !rr.stopped {
			r.RowsAffected = int64(len(r.Rows))
		}
	} else {
		r.RowsAffected = affected
	}
	var note []string
	if more > 0 {
		note = append(note, fmt.Sprintf("%d more result set%s not shown", more, plural(more)))
	}
	note = append(note, notices...)
	r.Note = strings.Join(note, "\n")
	return r
}

func (s *msSession) cancel() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *msSession) inTx() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trans
}

func (s *msSession) close() {
	_ = s.conn.Close()
	_ = s.own.Close()
}

// msCell prints a value as SSMS does: decimals and money exactly, a
// uniqueidentifier in its usual form, binary as 0x hex.
func msCell(dbType string, v any) string {
	switch dbType {
	case "DECIMAL", "MONEY", "SMALLMONEY":
		if b, ok := v.([]byte); ok {
			return string(b)
		}
	case "UNIQUEIDENTIFIER":
		if b, ok := v.([]byte); ok && len(b) == 16 {
			var u mssql.UniqueIdentifier
			if err := u.Scan(b); err == nil {
				return u.String()
			}
		}
	case "DATETIMEOFFSET":
		if t, ok := v.(time.Time); ok {
			return timeText(t, true)
		}
	}
	return cellText(v)
}

// msErr formats a server error with its number, state and line, as
// sqlcmd prints it.
func msErr(err error) error {
	if err == nil {
		return nil
	}
	var me mssql.Error
	if errors.As(err, &me) {
		return fmt.Errorf("Msg %d, Level %d, State %d, Line %d: %s", me.Number, me.Class, me.State, me.LineNo, me.Message)
	}
	return err
}
