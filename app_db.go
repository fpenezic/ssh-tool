package main

// Database tab backend: a connection with db_enabled opens a server handle
// (internal/dbx) whose connections ride the connection's SSH path, the same
// two transports as the Kafka tab (see app_kafka.go): channels on one
// shell-less session to the host (default), or dialed like DialVia.
//
// Each query tab inside the Database tab gets its own server session, keyed
// by the tab's key, so SET, temp tables and an open transaction carry from
// one run to the next and survive the tab being split, moved, detached or
// the window reloaded. The tabs themselves (which are open, their SQL) are
// kept here as an opaque blob for the same reason.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"ssh-tool/internal/dbx"
	"ssh-tool/internal/resolver"
	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/sshchan"
	"ssh-tool/internal/store"
)

// dbCallTimeout bounds a tree or structure call; like kafkaCallTimeout it
// covers a cold SSH connect with prompts. Query runs are not bounded: Stop
// ends them.
const dbCallTimeout = 90 * time.Second

// DbHandleInfo describes an open database handle.
type DbHandleInfo struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Engine       string `json:"engine"`
	Addr         string `json:"addr"`
	Database     string `json:"database"`
	User         string `json:"user"`
	// Transport is "tunnel", "jump:<bastion host>" or "direct".
	Transport string `json:"transport"`
	// State is the SSH session state of a tunnel transport
	// (sshchan.Transport*), "" for the others; Error the last connect
	// error.
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	ReadOnly  bool   `json:"read_only"`
	Sensitive bool   `json:"sensitive"`
	TLS       string `json:"tls"`
}

type dbHandle struct {
	info DbHandleInfo
	h    *dbx.Handle
	lazy *sshchan.LazySSH // nil unless Transport == "tunnel"
	// tabState is the UI's tab list (JSON), kept for a window that did not
	// open the tab (detach, redock, reload). Guarded by App.dbMu.
	tabState string
}

var errDbClosed = errors.New("database tab is not open")

// DbOpen opens a database handle for a connection. No I/O beyond warming
// the SSH session of a tunnel transport.
func (a *App) DbOpen(connectionID string) (*DbHandleInfo, error) {
	settings, err := resolver.ResolveConnection(a.db, connectionID)
	if err != nil {
		return nil, err
	}
	if !settings.DbEnabled {
		return nil, fmt.Errorf("the database is not enabled on this connection")
	}
	conn, err := a.db.GetConnection(connectionID)
	if err != nil {
		return nil, err
	}
	addr, err := dbx.ParseAddr(settings.DbEngine, settings.DbAddr)
	if err != nil {
		return nil, err
	}
	user, password, err := a.dbLogin(settings)
	if err != nil {
		return nil, err
	}
	dh := &dbHandle{info: DbHandleInfo{
		ID:           "db:" + uuid.New().String(),
		ConnectionID: connectionID,
		Name:         conn.Name,
		Engine:       settings.DbEngine,
		Addr:         addr,
		Database:     settings.DbName,
		User:         user,
		ReadOnly:     settings.DbReadOnly,
		Sensitive:    conn.Sensitive,
		TLS:          settings.DbTLS,
	}}
	cfg := dbx.Config{
		Engine: settings.DbEngine, Addr: addr, Database: settings.DbName,
		User: user, Password: password, TLS: settings.DbTLS, ReadOnly: settings.DbReadOnly,
	}
	if settings.DbUseTunnel {
		if settings.Hostname == "" {
			return nil, fmt.Errorf("connection has no hostname")
		}
		dh.info.Transport = "tunnel"
		dh.lazy = &sshchan.LazySSH{}
		dh.lazy.Connect = a.sshChanConnect("db", connectionID, conn, settings, dh.lazy)
		cfg.Dial = dh.lazy.Dial
	} else {
		dh.info.Transport = "direct"
		if settings.JumpHost != nil {
			dh.info.Transport = "jump:" + settings.JumpHost.Hostname
		}
		cfg.Dial = a.viaDial(settings)
	}
	h, err := dbx.Open(cfg)
	if err != nil {
		if dh.lazy != nil {
			dh.lazy.Close()
		}
		return nil, err
	}
	dh.h = h

	a.dbMu.Lock()
	a.dbHandles[dh.info.ID] = dh
	a.dbMu.Unlock()
	if dh.lazy != nil {
		go func() { _, _ = dh.lazy.Client(context.Background()) }()
	}
	a.recordAudit("db.open", connectionID, map[string]string{
		"handle_id": dh.info.ID,
		"engine":    dh.info.Engine,
		"addr":      addr,
		"database":  settings.DbName,
		"user":      user,
		"transport": dh.info.Transport,
		"read_only": fmt.Sprint(settings.DbReadOnly),
	})
	info := dh.status()
	return &info, nil
}

// dbLogin resolves the login from the connection's database credential:
// its default username, and its password through the same path SSH uses
// (vault, KeePass, Bitwarden, Infisical). A credential without a password
// is a login for trust / peer authentication.
func (a *App) dbLogin(settings *store.ResolvedSettings) (string, string, error) {
	if settings.DbCredentialRef == nil {
		return "", "", fmt.Errorf("the database needs a login: pick a credential in the connection's Database section")
	}
	cred, err := a.db.GetCredential(*settings.DbCredentialRef)
	if err != nil {
		return "", "", fmt.Errorf("database credential: %w", err)
	}
	if cred == nil {
		return "", "", fmt.Errorf("database credential not found (was it deleted?)")
	}
	user := ""
	if cred.DefaultUsername != nil {
		user = *cred.DefaultUsername
	}
	if user == "" {
		return "", "", fmt.Errorf("database credential %q has no username", cred.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer cancel()
	m, err := sshlayer.ResolveAuth(ctx, cred, a.vault, nil)
	if err != nil {
		return "", "", fmt.Errorf("database credential %q: %w", cred.Name, err)
	}
	return user, m.Password, nil
}

func (dh *dbHandle) status() DbHandleInfo {
	info := dh.info
	if dh.lazy != nil {
		info.State, info.Error = dh.lazy.State()
	}
	return info
}

func (a *App) dbGet(id string) (*dbHandle, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	dh, ok := a.dbHandles[id]
	if !ok {
		return nil, errDbClosed
	}
	return dh, nil
}

func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dbCallTimeout)
}

// DbStatus reports an open handle and its transport state.
func (a *App) DbStatus(id string) (*DbHandleInfo, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	info := dh.status()
	return &info, nil
}

// DbList returns every open handle (a detached window rebuilds its tabs'
// sessions from it).
func (a *App) DbList() []DbHandleInfo {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	out := make([]DbHandleInfo, 0, len(a.dbHandles))
	for _, dh := range a.dbHandles {
		out = append(out, dh.status())
	}
	return out
}

// DbClose closes a handle: its sessions (a running statement is
// cancelled, an open transaction rolled back by the server), its pool and
// its SSH session. Unknown ids are ignored.
func (a *App) DbClose(id string) {
	a.dbMu.Lock()
	dh, ok := a.dbHandles[id]
	delete(a.dbHandles, id)
	a.dbMu.Unlock()
	if !ok {
		return
	}
	if a.mcp != nil {
		a.clearMcpGrant(id)
	}
	go func() {
		dh.h.Close()
		if dh.lazy != nil {
			// Give the sessions a moment to close cleanly first.
			time.Sleep(time.Second)
			dh.lazy.Close()
		}
	}()
}

// DbInfo reports the server version and the login.
func (a *App) DbInfo(id string) (*dbx.ServerInfo, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return dh.h.Info(ctx)
}

// DbDatabases lists the server's databases.
func (a *App) DbDatabases(id string) ([]dbx.Database, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return dh.h.Databases(ctx)
}

// DbObjects lists a database's tables and views.
func (a *App) DbObjects(id, database string, system bool) ([]dbx.Object, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return dh.h.Objects(ctx, database, system)
}

// DbDescribe returns a table's structure and DDL.
func (a *App) DbDescribe(id, database, schema, name string) (*dbx.TableDetail, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return dh.h.Describe(ctx, database, schema, name)
}

// DbBrowse reads a page of a table's rows.
func (a *App) DbBrowse(id, database, schema, name string, offset, limit int) (*dbx.Result, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return dh.h.Browse(ctx, database, schema, name, offset, limit)
}

// DbRunResult is what a run returned: one result per statement run (it
// stops at the first error), and the session state after it.
type DbRunResult struct {
	Results  []*dbx.Result `json:"results"`
	InTx     bool          `json:"in_tx"`
	Database string        `json:"database"`
}

// DbRun runs a script on the query tab's session (opened on database,
// reopened when the tab switches database). Writes are audited.
func (a *App) DbRun(id, tabKey, database, script string, maxRows int) (*DbRunResult, error) {
	return a.dbRun(id, tabKey, database, script, dbx.RunOptions{MaxRows: maxRows}, "")
}

// dbRun is DbRun with the run options and the audit source ("" = the user,
// "mcp" = the LLM).
func (a *App) dbRun(id, tabKey, database, script string, opts dbx.RunOptions, source string) (*DbRunResult, error) {
	dh, err := a.dbGet(id)
	if err != nil {
		return nil, err
	}
	sess, err := a.dbSession(dh, tabKey, database)
	if err != nil {
		return nil, err
	}
	results := sess.Run(context.Background(), script, opts)
	for _, r := range results {
		if !r.Write {
			continue
		}
		meta := map[string]string{
			"handle_id": id,
			"database":  database,
			"statement": clip(r.Statement, 300),
		}
		if source != "" {
			meta["source"] = source
		}
		if r.Error != "" {
			meta["error"] = clip(r.Error, 300)
		} else if r.RowsAffected >= 0 {
			meta["rows_affected"] = fmt.Sprint(r.RowsAffected)
		}
		a.recordAudit("db.write", dh.info.ConnectionID, meta)
	}
	return &DbRunResult{Results: results, InTx: sess.InTx(), Database: sess.Database}, nil
}

// dbSession returns the tab's session on database, opening it (or
// reopening it on another database) as needed. A session holding an open
// transaction is not swapped out from under it.
func (a *App) dbSession(dh *dbHandle, tabKey, database string) (*dbx.Session, error) {
	if s := dh.h.Session(tabKey); s != nil {
		if s.Database == database {
			return s, nil
		}
		if s.InTx() {
			return nil, fmt.Errorf("this tab has an open transaction on %s: commit or roll back before switching database", s.Database)
		}
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return dh.h.OpenSession(ctx, tabKey, database)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// DbCancel stops the statement a query tab is running.
func (a *App) DbCancel(id, tabKey string) {
	dh, err := a.dbGet(id)
	if err != nil {
		return
	}
	if s := dh.h.Session(tabKey); s != nil {
		go s.Cancel()
	}
}

// DbCloseTab closes a query tab's session; an open transaction is rolled
// back by the server.
func (a *App) DbCloseTab(id, tabKey string) {
	if dh, err := a.dbGet(id); err == nil {
		dh.h.CloseSession(tabKey)
	}
}

// DbTabInTx reports whether a query tab's session holds an open
// transaction (Postgres only), so closing the tab can ask first.
func (a *App) DbTabInTx(id, tabKey string) bool {
	dh, err := a.dbGet(id)
	if err != nil {
		return false
	}
	s := dh.h.Session(tabKey)
	return s != nil && s.InTx()
}

// DbStatementAt returns the statement of script under the byte offset, nil
// for a script with none. The editor runs it on Ctrl+Enter.
func (a *App) DbStatementAt(engine, script string, offset int) *dbx.Stmt {
	s, ok := dbx.StatementAt(engine, script, offset)
	if !ok {
		return nil
	}
	return &s
}

// DbIsWrite reports which statements of a script are writes, so the UI can
// confirm them on a sensitive connection before running.
func (a *App) DbIsWrite(engine, script string) []string {
	var out []string
	for _, s := range dbx.Split(engine, script) {
		if !dbx.IsRead(engine, s.Text) {
			out = append(out, s.Text)
		}
	}
	return out
}

// DbSetTabState stores the UI's tab list of a handle. Unknown ids are
// ignored.
func (a *App) DbSetTabState(id, state string) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	if dh, ok := a.dbHandles[id]; ok {
		dh.tabState = state
	}
}

// DbGetTabState returns the tab list DbSetTabState stored ("" = none).
func (a *App) DbGetTabState(id string) (string, error) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	dh, ok := a.dbHandles[id]
	if !ok {
		return "", errDbClosed
	}
	return dh.tabState, nil
}
