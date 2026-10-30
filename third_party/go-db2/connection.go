package db2

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"

	"github.com/go-db2/go-db2/network"
)

// Conn implements the database/sql/driver.Conn interface for IBM Db2.
type Conn struct {
	session *network.Session
	cfg     *Config
	mu      sync.Mutex
	closed  bool
}

// NewConn wraps a connected network session into a database/sql driver.Conn.
func NewConn(session *network.Session, cfg *Config) *Conn {
	return &Conn{
		session: session,
		cfg:     cfg,
	}
}

// Prepare returns a prepared statement, bound to this connection.
func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

// applyContextMetadata applies session-level user switching (WithUser) and client info
// audit registers (WithClientInfo) propagated in ctx to ensure proper multi-tenant
// identity isolation and auditability across all execution paths.
func (c *Conn) applyContextMetadata(ctx context.Context) error {
	if targetUser := UserFromContext(ctx); targetUser != "" {
		if err := validateSQLIdentifier(targetUser, "context user"); err != nil {
			return err
		}
		if targetUser != c.session.CurrentUser() {
			if err := c.session.SwitchUser(ctx, targetUser); err != nil {
				return err
			}
		}
	}

	if clientInfo := ClientInfoFromContext(ctx); !clientInfo.IsEmpty() {
		if err := c.session.SetClientInfo(ctx, clientInfo.ApplicationName, clientInfo.WorkstationName, clientInfo.UserID, clientInfo.Accounting, clientInfo.CorrelationToken); err != nil {
			return err
		}
	}
	return nil
}

// PrepareContext returns a prepared statement, bound to this connection with context support.
func (c *Conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return nil, ErrConnectionClosed
	}

	if err := c.applyContextMetadata(ctx); err != nil {
		return nil, err
	}

	outCols, paramCols, err := c.session.PrepareAndDescribe(ctx, query)
	if err != nil {
		return nil, err
	}

	return NewStmt(c, query, outCols, paramCols), nil
}

// Exec executes a query that doesn't return rows (legacy interface).
func (c *Conn) Exec(query string, args []driver.Value) (driver.Result, error) {
	namedArgs := make([]driver.NamedValue, len(args))
	for i, v := range args {
		namedArgs[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return c.ExecContext(context.Background(), query, namedArgs)
}

// ExecContext executes a query without returning rows.
func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return nil, ErrConnectionClosed
	}

	if err := c.applyContextMetadata(ctx); err != nil {
		return nil, err
	}

	if len(args) == 0 {
		affected, err := c.session.ExecDirect(ctx, query)
		if err != nil {
			return nil, err
		}
		return NewResultWithConn(c, affected, isInsertQuery(query)), nil
	}

	// Prepare and execute with parameters
	outCols, paramCols, err := c.session.PrepareAndDescribe(ctx, query)
	if err != nil {
		return nil, err
	}

	stmt := NewStmt(c, query, outCols, paramCols)
	return stmt.execContextLocked(ctx, args)
}

// Query executes a query that may return rows (legacy interface).
func (c *Conn) Query(query string, args []driver.Value) (driver.Rows, error) {
	namedArgs := make([]driver.NamedValue, len(args))
	for i, v := range args {
		namedArgs[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return c.QueryContext(context.Background(), query, namedArgs)
}

// QueryContext executes a query that may return rows.
func (c *Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return nil, ErrConnectionClosed
	}

	if err := c.applyContextMetadata(ctx); err != nil {
		return nil, err
	}

	if len(args) == 0 {
		if !isQueryStatement(query) {
			_, err := c.session.ExecDirect(ctx, query)
			if err != nil {
				return nil, err
			}
			return NewRows(nil, nil), nil
		}

		cols, rowsData, err := c.session.QueryDirect(ctx, query)
		if err != nil {
			return nil, err
		}

		return NewRows(cols, rowsData), nil
	}

	// Prepare and query with parameters
	outCols, paramCols, err := c.session.PrepareAndDescribe(ctx, query)
	if err != nil {
		return nil, err
	}

	stmt := NewStmt(c, query, outCols, paramCols)
	return stmt.queryContextLocked(ctx, args)
}

func stripLeadingCommentsAndSpaces(sql string) string {
	s := strings.TrimSpace(sql)
	for {
		if strings.HasPrefix(s, "--") {
			idx := strings.IndexByte(s, '\n')
			if idx == -1 {
				return ""
			}
			s = strings.TrimSpace(s[idx+1:])
			continue
		}
		if strings.HasPrefix(s, "/*") {
			idx := strings.Index(s, "*/")
			if idx == -1 {
				return ""
			}
			s = strings.TrimSpace(s[idx+2:])
			continue
		}
		if strings.HasPrefix(s, "(") {
			s = strings.TrimSpace(s[1:])
			continue
		}
		break
	}
	return s
}

// hasPrefixFold reports whether s begins with prefix (expected to be uppercase ASCII),
// ignoring ASCII case differences.
// Optimization: Checks bytes inline without converting s to upper case via strings.ToUpper,
// eliminating heap allocations and full-string scanning overhead (0 allocs/op, O(1) prefix time).
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c != prefix[i] {
			return false
		}
	}
	return true
}

func isQueryStatement(sql string) bool {
	s := stripLeadingCommentsAndSpaces(sql)
	return hasPrefixFold(s, "SELECT") ||
		hasPrefixFold(s, "WITH") ||
		hasPrefixFold(s, "VALUES") ||
		hasPrefixFold(s, "TABLE")
}

// CurrentUser returns the currently active user on this connection.
func (c *Conn) CurrentUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.session == nil {
		return ""
	}
	return c.session.CurrentUser()
}

// SwitchUser transitions the active user identity on this connection within a trusted context or session.
func (c *Conn) SwitchUser(ctx context.Context, newUser string, password ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return ErrConnectionClosed
	}

	return c.session.SwitchUser(ctx, newUser, password...)
}

// SetClientInfo updates client info registers on this connection.
func (c *Conn) SetClientInfo(ctx context.Context, info ClientInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return ErrConnectionClosed
	}

	return c.session.SetClientInfo(ctx, info.ApplicationName, info.WorkstationName, info.UserID, info.Accounting, info.CorrelationToken)
}

// Close invalidates and closes the database connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	if c.session != nil {
		return c.session.Close()
	}
	return nil
}

// Begin starts and returns a new transaction (legacy interface).
func (c *Conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx starts and returns a new transaction with the provided context and options.
func (c *Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return nil, ErrConnectionClosed
	}

	if opts.ReadOnly {
		return nil, errors.New("db2: read-only transactions are not supported")
	}

	// Disable auto-commit for explicit transaction scope
	c.session.SetAutoCommit(false)

	return &Tx{
		conn: c,
		ctx:  ctx,
	}, nil
}

// Ping implements driver.Pinger interface to verify connection liveness.
func (c *Conn) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil {
		return ErrConnectionClosed
	}

	return c.session.Ping(ctx)
}

// IsValid implements driver.Validator. database/sql calls it before returning
// the connection to the pool, and discards the connection when it is false.
func (c *Conn) IsValid() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.session != nil && !c.session.Broken()
}

// ResetSession implements driver.SessionResetter. It is called before a pooled
// connection is reused; driver.ErrBadConn makes database/sql discard it.
func (c *Conn) ResetSession(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.session == nil || c.session.Broken() {
		return driver.ErrBadConn
	}

	if c.cfg != nil && c.cfg.User != "" && c.session.CurrentUser() != c.cfg.User {
		if err := c.session.SwitchUser(ctx, c.cfg.User); err != nil {
			return driver.ErrBadConn
		}
	}

	if c.cfg != nil && !c.session.ClientInfoMatches(c.cfg.ClientApplName, c.cfg.ClientWrkstnName, c.cfg.ClientUserid, c.cfg.ClientAcctng, c.cfg.ClientCorrToken) {
		if err := c.session.ResetClientInfo(ctx, c.cfg.ClientApplName, c.cfg.ClientWrkstnName, c.cfg.ClientUserid, c.cfg.ClientAcctng, c.cfg.ClientCorrToken); err != nil {
			return driver.ErrBadConn
		}
	}

	c.session.SetAutoCommit(true)
	return nil
}

// Interface assertions
// CheckNamedValue implements driver.NamedValueChecker interface.
func (c *Conn) CheckNamedValue(nv *driver.NamedValue) error {
	return nil
}

var (
	_ driver.Conn               = (*Conn)(nil)
	_ driver.ConnPrepareContext = (*Conn)(nil)
	_ driver.ConnBeginTx        = (*Conn)(nil)
	_ driver.Pinger             = (*Conn)(nil)
	_ driver.ExecerContext      = (*Conn)(nil)
	_ driver.QueryerContext     = (*Conn)(nil)
	_ driver.NamedValueChecker  = (*Conn)(nil)
	_ driver.Validator          = (*Conn)(nil)
	_ driver.SessionResetter    = (*Conn)(nil)
)
