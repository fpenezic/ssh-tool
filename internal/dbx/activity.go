package dbx

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Activity is the server's other sessions as a grid: who runs what, for
// how long, what it waits on and which session blocks it. Kill stops one
// by the id in the grid's first column. Reading another user's sessions
// takes a monitoring grant on most servers; without one the server's
// error comes back, or only the login's own sessions.

// ErrNoActivity is an engine without a sessions view here.
var ErrNoActivity = errors.New("this engine has no sessions view")

type monitor interface {
	activity(ctx context.Context) *Result
	kill(ctx context.Context, id string, session bool) error
}

// Activity lists the server's sessions, the one asking left out.
func (h *Handle) Activity(ctx context.Context) (*Result, error) {
	m, ok := h.eng.(monitor)
	if !ok {
		return nil, ErrNoActivity
	}
	start := time.Now()
	r := m.activity(ctx)
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	return r, nil
}

var (
	numericID = regexp.MustCompile(`^[0-9]{1,19}$`)
	oraID     = regexp.MustCompile(`^[0-9]{1,10},[0-9]{1,10}$`)
)

// Kill stops what a session runs (session false) or ends the session
// (true). Engines that can only end one (SQL Server, Db2) end it either
// way. Refused on a read-only handle: it changes the server.
func (h *Handle) Kill(ctx context.Context, id string, session bool) error {
	if h.cfg.ReadOnly {
		return errors.New("read-only connection: stopping sessions is refused")
	}
	m, ok := h.eng.(monitor)
	if !ok {
		return ErrNoActivity
	}
	re := numericID
	if h.cfg.Engine == EngineOracle {
		re = oraID
	}
	if !re.MatchString(id) {
		return fmt.Errorf("session id %q is not one of this server's", id)
	}
	return m.kill(ctx, id, session)
}

func (e *pgEngine) activity(ctx context.Context) *Result {
	p, err := e.pool(ctx, "")
	if err != nil {
		return &Result{Error: err.Error()}
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		return &Result{Error: pgErr(err).Error()}
	}
	defer conn.Release()
	return pgRead(ctx, conn.Conn(), conn.Conn(), `
		SELECT pid AS id, usename AS "user", datname AS "database", client_addr::text AS client, state,
		       date_trunc('second', now() - query_start)::text AS running,
		       wait_event_type || ': ' || wait_event AS waiting,
		       array_to_string(pg_blocking_pids(pid), ', ') AS blocked_by, query
		FROM pg_stat_activity
		WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()
		ORDER BY state = 'active' DESC, query_start`, MaxRowsCeiling, true)
}

func (e *pgEngine) kill(ctx context.Context, id string, session bool) error {
	p, err := e.pool(ctx, "")
	if err != nil {
		return err
	}
	pid, _ := strconv.ParseInt(id, 10, 64)
	fn := "pg_cancel_backend"
	if session {
		fn = "pg_terminate_backend"
	}
	var ok bool
	if err := p.QueryRow(ctx, "SELECT "+fn+"($1)", pid).Scan(&ok); err != nil {
		return pgErr(err)
	}
	if !ok {
		return fmt.Errorf("session %s is gone or not yours to stop", id)
	}
	return nil
}

func (e *myEngine) activity(ctx context.Context) *Result {
	db, err := e.db()
	if err != nil {
		return &Result{Error: err.Error()}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return &Result{Error: myErr(err).Error()}
	}
	defer conn.Close()
	return myRead(ctx, conn, "SELECT ID AS id, USER AS user, DB AS `database`, HOST AS client, COMMAND AS state, "+
		"TIME AS running_s, STATE AS waiting, INFO AS query FROM information_schema.PROCESSLIST "+
		"WHERE ID <> CONNECTION_ID() ORDER BY COMMAND = 'Sleep', TIME DESC", MaxRowsCeiling, nil)
}

func (e *myEngine) kill(ctx context.Context, id string, session bool) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	q := "KILL QUERY " + id // id is digits (Kill checked it)
	if session {
		q = "KILL " + id
	}
	_, err = db.ExecContext(ctx, q)
	return myErr(err)
}

func (e *msEngine) activity(ctx context.Context) *Result {
	db, err := e.db()
	if err != nil {
		return &Result{Error: err.Error()}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return &Result{Error: msErr(err).Error()}
	}
	defer conn.Close()
	return msRead(ctx, conn, `
		SELECT s.session_id AS id, s.login_name AS [user], DB_NAME(s.database_id) AS [database], s.host_name AS client,
		       COALESCE(r.status, s.status) AS state, DATEDIFF(SECOND, r.start_time, SYSDATETIME()) AS running_s,
		       r.wait_type AS waiting, NULLIF(r.blocking_session_id, 0) AS blocked_by, t.text AS query
		FROM sys.dm_exec_sessions s
		LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
		OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
		WHERE s.is_user_process = 1 AND s.session_id <> @@SPID
		ORDER BY CASE WHEN r.session_id IS NULL THEN 1 ELSE 0 END, r.start_time`, MaxRowsCeiling, nil)
}

func (e *msEngine) kill(ctx context.Context, id string, _ bool) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "KILL "+id) // digits (Kill checked it); SQL Server ends the session
	return msErr(err)
}

func (e *oraEngine) activity(ctx context.Context) *Result {
	db, err := e.db()
	if err != nil {
		return &Result{Error: err.Error()}
	}
	return sqlRead(ctx, db, `
		SELECT s.SID || ',' || s.SERIAL# AS id, s.USERNAME AS "user", s.SCHEMANAME AS "schema", s.MACHINE AS client,
		       s.STATUS AS state, s.LAST_CALL_ET AS running_s, s.EVENT AS waiting, s.BLOCKING_SESSION AS blocked_by,
		       q.SQL_TEXT AS query
		FROM V$SESSION s
		LEFT JOIN V$SQL q ON q.SQL_ID = s.SQL_ID AND q.CHILD_NUMBER = s.SQL_CHILD_NUMBER
		WHERE s.TYPE = 'USER' AND s.SID <> SYS_CONTEXT('USERENV', 'SID')
		ORDER BY CASE s.STATUS WHEN 'ACTIVE' THEN 0 ELSE 1 END, s.LAST_CALL_ET DESC`, MaxRowsCeiling, oraDialect, nil)
}

func (e *oraEngine) kill(ctx context.Context, id string, session bool) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	q := "ALTER SYSTEM CANCEL SQL '" + id + "'" // sid,serial digits (Kill checked it)
	if session {
		q = "ALTER SYSTEM KILL SESSION '" + id + "' IMMEDIATE"
	}
	_, err = db.ExecContext(ctx, q)
	return oraErr(err)
}

func (e *db2Engine) activity(ctx context.Context) *Result {
	db, err := e.db()
	if err != nil {
		return &Result{Error: err.Error()}
	}
	return sqlRead(ctx, db, `
		SELECT c.APPLICATION_HANDLE AS ID, c.SESSION_AUTH_ID AS "USER", c.CLIENT_HOSTNAME AS CLIENT,
		       COALESCE(a.ACTIVITY_STATE, 'IDLE') AS STATE,
		       TIMESTAMPDIFF(2, CHAR(CURRENT TIMESTAMP - a.LOCAL_START_TIME)) AS RUNNING_S,
		       a.ACTIVITY_TYPE AS WAITING, CAST(NULL AS BIGINT) AS BLOCKED_BY,
		       CAST(SUBSTR(a.STMT_TEXT, 1, 4000) AS VARCHAR(4000)) AS QUERY
		FROM TABLE(MON_GET_CONNECTION(NULL, -2)) c
		LEFT JOIN TABLE(MON_GET_ACTIVITY(NULL, -2)) a ON a.APPLICATION_HANDLE = c.APPLICATION_HANDLE
		WHERE c.APPLICATION_HANDLE <> MON_GET_APPLICATION_HANDLE()
		ORDER BY CASE WHEN a.ACTIVITY_STATE IS NULL THEN 1 ELSE 0 END, a.LOCAL_START_TIME`, MaxRowsCeiling, db2Dialect, nil)
}

func (e *db2Engine) kill(ctx context.Context, id string, _ bool) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	// Db2 can only force the whole connection off.
	_, err = db.ExecContext(ctx, "CALL SYSPROC.ADMIN_CMD('FORCE APPLICATION ("+id+")')") // digits (Kill checked it)
	return db2Err(err)
}
