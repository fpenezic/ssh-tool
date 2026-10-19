package dbx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgEngine keeps one small pool per database: a Postgres connection is
// bound to one database, so the tree opens a pool for each it expands.
type pgEngine struct {
	cfg Config

	mu     sync.Mutex
	pools  map[string]*pgxpool.Pool
	closed bool
}

func newPG(cfg Config) *pgEngine {
	return &pgEngine{cfg: cfg, pools: map[string]*pgxpool.Pool{}}
}

// connConfig builds the pgx config for database. It starts from a
// key=value string with only the fields we set, then replaces dialing,
// name lookup and TLS: the host is resolved by the far side (LookupFunc
// passes it through to Dial), TLS has no plaintext fallback.
func (e *pgEngine) connConfig(database string) (*pgx.ConnConfig, error) {
	host, port, _ := net.SplitHostPort(e.cfg.Addr)
	if database == "" {
		database = e.cfg.Database
	}
	parts := []string{
		"host=" + pgQuoteValue(host),
		"port=" + port,
		"user=" + pgQuoteValue(e.cfg.User),
		"sslmode=disable",
		"connect_timeout=20",
	}
	if database != "" {
		parts = append(parts, "dbname="+pgQuoteValue(database))
	}
	cc, err := pgx.ParseConfig(strings.Join(parts, " "))
	if err != nil {
		return nil, err
	}
	cc.Password = e.cfg.Password
	cc.TLSConfig = e.cfg.tlsConfig()
	cc.Fallbacks = nil
	dial := e.cfg.dialer()
	cc.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// A cancel request dials RemoteAddr(); an SSH channel reports
		// 0.0.0.0:0 there, so report the address it was opened to.
		return &addrConn{Conn: c, remote: dialedAddr{network: network, addr: addr}}, nil
	}
	cc.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		return []string{host}, nil
	}
	// A cancelled run sends a cancel request and keeps the connection, so
	// a stopped query does not cost the session (its temp tables, its
	// open transaction).
	cc.BuildContextWatcherHandler = func(pc *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: pc, DeadlineDelay: 10 * time.Second}
	}
	cc.RuntimeParams["application_name"] = e.cfg.AppName
	if e.cfg.ReadOnly {
		cc.RuntimeParams["default_transaction_read_only"] = "on"
	}
	// Text results (exact numerics, timestamps as the server prints them)
	// and no statement cache, which DDL in a session would invalidate.
	cc.DefaultQueryExecMode = pgx.QueryExecModeExec
	return cc, nil
}

type dialedAddr struct{ network, addr string }

func (a dialedAddr) Network() string { return a.network }
func (a dialedAddr) String() string  { return a.addr }

type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c *addrConn) RemoteAddr() net.Addr { return c.remote }

// pgQuoteValue quotes a libpq key=value value.
func pgQuoteValue(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func (e *pgEngine) pool(ctx context.Context, database string) (*pgxpool.Pool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrClosed
	}
	if p := e.pools[database]; p != nil {
		return p, nil
	}
	cc, err := e.connConfig(database)
	if err != nil {
		return nil, err
	}
	pc, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	pc.ConnConfig = cc
	pc.MaxConns = 2
	pc.MinConns = 0
	pc.MaxConnIdleTime = 5 * time.Minute
	p, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	e.pools[database] = p
	return p, nil
}

func (e *pgEngine) info(ctx context.Context) (*ServerInfo, error) {
	p, err := e.pool(ctx, "")
	if err != nil {
		return nil, err
	}
	in := &ServerInfo{Engine: EnginePostgres}
	err = p.QueryRow(ctx, `SELECT version(), current_user, current_database()`).Scan(&in.Version, &in.User, &in.Database)
	if err != nil {
		return nil, pgErr(err)
	}
	return in, nil
}

func (e *pgEngine) databases(ctx context.Context) ([]Database, error) {
	p, err := e.pool(ctx, "")
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `
		SELECT datname,
		       CASE WHEN has_database_privilege(datname, 'CONNECT') THEN pg_database_size(datname) ELSE -1 END,
		       datistemplate
		FROM pg_database WHERE datallowconn ORDER BY datname`)
	if err != nil {
		return nil, pgErr(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Database, error) {
		var d Database
		err := r.Scan(&d.Name, &d.SizeBytes, &d.System)
		return d, err
	})
	return out, pgErr(err)
}

const pgSystemSchema = `(n.nspname IN ('pg_catalog', 'information_schema') OR n.nspname LIKE 'pg\_toast%' OR n.nspname LIKE 'pg\_temp\_%')`

func (e *pgEngine) objects(ctx context.Context, database string, system bool) ([]Object, error) {
	p, err := e.pool(ctx, database)
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `
		SELECT n.nspname, c.relname, c.relkind::text,
		       CASE WHEN c.relkind IN ('r', 'm', 'p') THEN c.reltuples::bigint ELSE -1 END,
		       CASE WHEN c.relkind IN ('r', 'm', 'p') THEN pg_total_relation_size(c.oid) ELSE -1 END,
		       `+pgSystemSchema+`,
		       COALESCE(obj_description(c.oid, 'pg_class'), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
		  AND ($1 OR NOT `+pgSystemSchema+`)
		ORDER BY 1, 2`, system)
	if err != nil {
		return nil, pgErr(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Object, error) {
		var o Object
		var kind string
		err := r.Scan(&o.Schema, &o.Name, &kind, &o.Rows, &o.SizeBytes, &o.System, &o.Comment)
		o.Kind = pgKind(kind)
		if o.Rows < 0 {
			o.Rows = -1
		}
		return o, err
	})
	return out, pgErr(err)
}

func pgKind(relkind string) string {
	switch relkind {
	case "v":
		return KindView
	case "m":
		return KindMatView
	case "f":
		return KindForeign
	case "S":
		return KindSequence
	}
	return KindTable
}

func (e *pgEngine) describe(ctx context.Context, database, schema, name string) (*TableDetail, error) {
	p, err := e.pool(ctx, database)
	if err != nil {
		return nil, err
	}
	var oid uint32
	var relkind string
	err = p.QueryRow(ctx, `
		SELECT c.oid, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&oid, &relkind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%s.%s not found", schema, name)
	}
	if err != nil {
		return nil, pgErr(err)
	}
	d := &TableDetail{Schema: schema, Name: name, Kind: pgKind(relkind)}

	rows, err := p.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
		       pg_get_expr(ad.adbin, ad.adrelid),
		       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY (i.indkey)),
		       CASE WHEN a.attidentity <> '' THEN 'identity' WHEN a.attgenerated <> '' THEN 'generated' ELSE '' END,
		       COALESCE(col_description(a.attrelid, a.attnum), '')
		FROM pg_attribute a LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, pgErr(err)
	}
	d.Columns, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Column, error) {
		var c Column
		var pk bool
		err := r.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &pk, &c.Extra, &c.Comment)
		if pk {
			c.Key = "PRI"
		}
		return c, err
	})
	if err != nil {
		return nil, pgErr(err)
	}

	rows, err = p.Query(ctx, `
		SELECT i.relname, ix.indisunique, ix.indisprimary, pg_get_indexdef(ix.indexrelid),
		       ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k, true) FROM generate_series(1, ix.indnkeyatts) k ORDER BY k)
		FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = $1 ORDER BY ix.indisprimary DESC, i.relname`, oid)
	if err != nil {
		return nil, pgErr(err)
	}
	d.Indexes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Index, error) {
		var ix Index
		err := r.Scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Def, &ix.Columns)
		return ix, err
	})
	if err != nil {
		return nil, pgErr(err)
	}

	rows, err = p.Query(ctx, `
		SELECT conname, pg_get_constraintdef(oid, true) FROM pg_constraint
		WHERE conrelid = $1 AND contype = 'f' ORDER BY conname`, oid)
	if err != nil {
		return nil, pgErr(err)
	}
	d.ForeignKeys, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ForeignKey, error) {
		var fk ForeignKey
		err := r.Scan(&fk.Name, &fk.Def)
		return fk, err
	})
	if err != nil {
		return nil, pgErr(err)
	}

	d.DDL, err = e.ddl(ctx, p, oid, d)
	if err != nil {
		return nil, err
	}
	d.DDLNote = "Rebuilt from the catalog: partitioning, storage options, ownership and grants are left out."
	return d, nil
}

// ddl rebuilds a CREATE statement from the catalog (Postgres has no SHOW
// CREATE TABLE): columns, constraints, and the indexes no constraint owns.
func (e *pgEngine) ddl(ctx context.Context, p *pgxpool.Pool, oid uint32, d *TableDetail) (string, error) {
	qname := pgIdent(d.Schema) + "." + pgIdent(d.Name)
	if d.Kind == KindView || d.Kind == KindMatView {
		var def string
		if err := p.QueryRow(ctx, `SELECT pg_get_viewdef($1::oid, true)`, oid).Scan(&def); err != nil {
			return "", pgErr(err)
		}
		kw := "VIEW"
		if d.Kind == KindMatView {
			kw = "MATERIALIZED VIEW"
		}
		return fmt.Sprintf("CREATE %s %s AS\n%s", kw, qname, strings.TrimRight(def, "\n")), nil
	}
	var lines []string
	for _, c := range d.Columns {
		l := "    " + pgIdent(c.Name) + " " + c.Type
		if c.Default != nil {
			l += " DEFAULT " + *c.Default
		}
		if !c.Nullable {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	rows, err := p.Query(ctx, `
		SELECT conname, pg_get_constraintdef(oid, true) FROM pg_constraint
		WHERE conrelid = $1 AND contype IN ('p', 'u', 'c', 'f', 'x')
		ORDER BY CASE contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'c' THEN 2 WHEN 'f' THEN 3 ELSE 4 END, conname`, oid)
	if err != nil {
		return "", pgErr(err)
	}
	cons, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var c [2]string
		err := r.Scan(&c[0], &c[1])
		return c, err
	})
	if err != nil {
		return "", pgErr(err)
	}
	for _, c := range cons {
		lines = append(lines, "    CONSTRAINT "+pgIdent(c[0])+" "+c[1])
	}
	out := "CREATE TABLE " + qname + " (\n" + strings.Join(lines, ",\n") + "\n);"
	rows, err = p.Query(ctx, `
		SELECT pg_get_indexdef(ix.indexrelid) FROM pg_index ix
		WHERE ix.indrelid = $1 AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = ix.indexrelid)
		ORDER BY 1`, oid)
	if err != nil {
		return "", pgErr(err)
	}
	idx, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", pgErr(err)
	}
	for _, s := range idx {
		out += "\n" + s + ";"
	}
	return out, nil
}

func pgIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (e *pgEngine) browse(ctx context.Context, database, schema, name string, offset, limit int) (*Result, error) {
	p, err := e.pool(ctx, database)
	if err != nil {
		return nil, err
	}
	var pk []string
	rows, err := p.Query(ctx, `
		SELECT a.attname FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE n.nspname = $1 AND c.relname = $2 AND i.indisprimary ORDER BY k.ord`, schema, name)
	if err != nil {
		return nil, pgErr(err)
	}
	if pk, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, pgErr(err)
	}
	q := "SELECT * FROM " + pgIdent(schema) + "." + pgIdent(name)
	if len(pk) > 0 {
		for i := range pk {
			pk[i] = pgIdent(pk[i])
		}
		q += " ORDER BY " + strings.Join(pk, ", ")
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	conn, err := p.Acquire(ctx)
	if err != nil {
		return nil, pgErr(err)
	}
	defer conn.Release()
	start := time.Now()
	r := pgRead(ctx, conn.Conn(), conn.Conn(), q, limit, true)
	r.Statement = q
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

func (e *pgEngine) session(ctx context.Context, database string) (session, error) {
	cc, err := e.connConfig(database)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return nil, pgErr(err)
	}
	return &pgSession{conn: conn}, nil
}

func (e *pgEngine) close() {
	e.mu.Lock()
	pools := e.pools
	e.pools = map[string]*pgxpool.Pool{}
	e.closed = true
	e.mu.Unlock()
	for _, p := range pools {
		go p.Close() // waits for acquired connections; never block the caller
	}
}

type pgSession struct {
	conn *pgx.Conn

	mu   sync.Mutex
	stop context.CancelFunc
}

type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *pgSession) run(ctx context.Context, stmt string, readOnly bool, maxRows int) *Result {
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
		// Cancelling a truncated read is only safe outside the user's own
		// transaction: a cancelled statement aborts the transaction it is in.
		idle := s.conn.PgConn().TxStatus() == 'I'
		return pgRead(ctx, s.conn, s.conn, stmt, maxRows, idle && IsRead(EnginePostgres, stmt))
	}
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return errResult(pgErr(err))
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	return pgRead(ctx, tx, s.conn, stmt, maxRows, true)
}

// pgRead runs stmt and reads its rows under the bounds. cancelOnCut stops
// the server once the bound is hit; otherwise the rest is read and dropped
// (a write with RETURNING must not be cancelled half way).
func pgRead(ctx context.Context, q pgQuerier, conn *pgx.Conn, stmt string, maxRows int, cancelOnCut bool) *Result {
	r := &Result{RowsAffected: -1}
	rctx, cut := context.WithCancel(ctx)
	defer cut()
	rows, err := q.Query(rctx, stmt)
	if err != nil {
		r.Error = pgErr(err).Error()
		return r
	}
	fds := rows.FieldDescriptions()
	r.HasRows = len(fds) > 0
	for _, fd := range fds {
		typ := strconv.FormatUint(uint64(fd.DataTypeOID), 10)
		if t, ok := conn.TypeMap().TypeForOID(fd.DataTypeOID); ok {
			typ = t.Name
		}
		r.Columns = append(r.Columns, ResultColumn{Name: fd.Name, Type: typ})
	}
	rr := &rowReader{r: r, maxRows: maxRows}
	for rows.Next() {
		if rr.stopped {
			continue // drain
		}
		if !rr.add(rows.RawValues()) && cancelOnCut {
			cut()
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil && !(rr.stopped && cancelOnCut) {
		r.Error = pgErr(err).Error()
		return r
	}
	if rr.stopped {
		if cancelOnCut {
			return r
		}
	}
	tag := rows.CommandTag()
	r.Command = tag.String()
	r.RowsAffected = tag.RowsAffected()
	return r
}

func (s *pgSession) cancel() {
	s.mu.Lock()
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *pgSession) inTx() bool { return s.conn.PgConn().TxStatus() != 'I' }

func (s *pgSession) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.conn.Close(ctx)
}

// pgErr formats a server error the way psql does, with the SQLSTATE and
// the detail / hint lines.
func pgErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	msg := fmt.Sprintf("%s %s: %s", pe.Severity, pe.Code, pe.Message)
	if pe.Position > 0 {
		msg += fmt.Sprintf(" (at character %d)", pe.Position)
	}
	if pe.Detail != "" {
		msg += "\nDETAIL: " + pe.Detail
	}
	if pe.Hint != "" {
		msg += "\nHINT: " + pe.Hint
	}
	return errors.New(msg)
}
