package main

import (
	"encoding/base64"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/dbx"
	"ssh-tool/internal/store"
)

func newDbTestApp(t *testing.T) *App {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	v := creds.NewVault()
	v.SetPath(filepath.Join(t.TempDir(), "vault.enc"))
	if err := v.Init("lab passphrase", false); err != nil {
		t.Fatal(err)
	}
	return &App{db: db, vault: v, credSvc: &creds.Service{DB: db, Vault: v}, dbHandles: map[string]*dbHandle{}}
}

func dbTestCred(t *testing.T, a *App, user, password string) string {
	t.Helper()
	res, err := a.CredentialsCreate(creds.CreateInput{Kind: "password", Name: "db login", DefaultUsername: &user, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	return res.Credential.ID
}

func TestDbOpenRefuses(t *testing.T) {
	a := newDbTestApp(t)
	on, off := true, false
	bad := "db:notaport"
	nouser := dbTestCred(t, a, "", "pw")
	cases := []struct {
		name string
		over store.InheritableSettings
		want string
	}{
		{"not enabled", store.InheritableSettings{}, "not enabled"},
		{"disabled", store.InheritableSettings{DbEnabled: &off}, "not enabled"},
		{"bad address", store.InheritableSettings{DbEnabled: &on, DbAddr: &bad}, "bad port"},
		{"no login", store.InheritableSettings{DbEnabled: &on}, "needs a login"},
		{"no username", store.InheritableSettings{DbEnabled: &on, DbCredentialRef: &nouser}, "no username"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := a.db.CreateConnection(store.NewConnection{Name: "db", Hostname: "db.example.com", Overrides: tc.over})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.DbOpen(c.ID, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if len(a.dbHandles) != 0 {
		t.Fatalf("%d handles left open", len(a.dbHandles))
	}
}

func TestDbIPCTunnelPostgres(t *testing.T) { dbIPCTunnel(t, dbx.EnginePostgres, "DBTEST_PG") }
func TestDbIPCTunnelMySQL(t *testing.T)    { dbIPCTunnel(t, dbx.EngineMySQL, "DBTEST_MYSQL") }

// The default transport end to end against a real server (see
// internal/dbx/integration_test.go for the DBTEST_* variables): DbOpen on
// a connection whose SSH password and database login are in the vault,
// the database named by a host only the SSH server resolves, query tabs
// with their own sessions, read-only refusals.
func dbIPCTunnel(t *testing.T, engine, env string) {
	raw := os.Getenv(env)
	if raw == "" {
		t.Skipf("%s not set", env)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, dbPort, _ := net.SplitHostPort(u.Host)
	dbPass, _ := u.User.Password()
	dbName := strings.TrimPrefix(u.Path, "/")
	lab := startKafkaSSHLab(t) // resolves kafka.internal to 127.0.0.1

	a := newDbTestApp(t)
	if err := a.db.UpsertKnownHost(lab.host, lab.port, lab.key.Type(),
		base64.StdEncoding.EncodeToString(lab.key.Marshal()), gossh.FingerprintSHA256(lab.key)); err != nil {
		t.Fatal(err)
	}
	credID := dbTestCred(t, a, u.User.Username(), dbPass)
	on, sshUser := true, "kafka"
	port := uint16(lab.port)
	addr := "kafka.internal:" + dbPort
	newConn := func(name string, readOnly bool) string {
		ro := readOnly
		c, err := a.db.CreateConnection(store.NewConnection{Name: name, Hostname: lab.host, Overrides: store.InheritableSettings{
			Username: &sshUser, Port: &port, DbEnabled: &on, DbEngine: &engine, DbAddr: &addr, DbName: &dbName,
			DbCredentialRef: &credID, DbReadOnly: &ro,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.SetConnectionPassword(c.ID, "secret"); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}

	info, err := a.DbOpen(newConn("rw", false), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.DbClose(info.ID)
	if info.Transport != "tunnel" || info.Engine != engine || info.User != u.User.Username() {
		t.Fatalf("info = %+v", info)
	}
	si, err := a.DbInfo(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if si.Database != dbName {
		t.Fatalf("server info %+v", si)
	}
	dbs, err := a.DbDatabases(info.ID)
	if err != nil || len(dbs) == 0 {
		t.Fatalf("databases %v %v", dbs, err)
	}

	table := "dbx_ipc_" + strings.ToLower(engine)
	run := func(id, tab, script string) *DbRunResult {
		t.Helper()
		r, err := a.DbRun(id, tab, dbName, script, 0)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := run(info.ID, "q1", "DROP TABLE IF EXISTS "+table+"; CREATE TABLE "+table+" (id int PRIMARY KEY, v text); INSERT INTO "+table+" VALUES (1, 'a'), (2, NULL)")
	for _, res := range r.Results {
		if res.Error != "" {
			t.Fatalf("%s: %s", res.Statement, res.Error)
		}
	}
	defer run(info.ID, "cleanup", "DROP TABLE IF EXISTS "+table)
	if r.Results[2].RowsAffected != 2 {
		t.Fatalf("insert: %+v", r.Results[2])
	}

	// Session state is per tab.
	if engine == dbx.EnginePostgres {
		run(info.ID, "q1", "BEGIN; UPDATE "+table+" SET v = 'b' WHERE id = 1")
		if !a.DbTabInTx(info.ID, "q1") || a.DbTabInTx(info.ID, "q2") {
			t.Fatal("transaction state should be per tab")
		}
		if got := *run(info.ID, "q2", "SELECT v FROM "+table+" WHERE id = 1").Results[0].Rows[0][0]; got != "a" {
			t.Fatalf("other tab saw the uncommitted update: %q", got)
		}
		if _, err := a.DbRun(info.ID, "q1", "postgres", "SELECT 1", 0); err == nil || !strings.Contains(err.Error(), "open transaction") {
			t.Fatalf("switching database inside a transaction: %v", err)
		}
		run(info.ID, "q1", "ROLLBACK")
	}

	objs, err := a.DbObjects(info.ID, dbName, false)
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	for _, o := range objs {
		if o.Name == table {
			found = o.Schema
		}
	}
	page, err := a.DbBrowse(info.ID, dbName, found, table, 0, 10)
	if err != nil || len(page.Rows) != 2 || page.Rows[1][1] != nil {
		t.Fatalf("browse %v %+v", err, page)
	}

	// Read-only connection: refused in the backend.
	roInfo, err := a.DbOpen(newConn("ro", true), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.DbClose(roInfo.ID)
	r = run(roInfo.ID, "q1", "DELETE FROM "+table)
	if !strings.Contains(r.Results[0].Error, "read-only") {
		t.Fatalf("read-only delete: %+v", r.Results[0])
	}
	if got := *run(roInfo.ID, "q1", "SELECT COUNT(*) FROM "+table).Results[0].Rows[0][0]; got != "2" {
		t.Fatalf("rows after refused delete: %s", got)
	}

	if w := a.DbIsWrite(engine, "SELECT 1; update t set a = 1; -- x\nDROP TABLE t"); len(w) != 2 {
		t.Fatalf("writes: %q", w)
	}
	if s := a.DbStatementAt(engine, "SELECT 1;\nSELECT 2", 12); s == nil || s.Text != "SELECT 2" {
		t.Fatalf("statement at: %+v", s)
	}

	a.DbSetTabState(info.ID, `{"tabs":["q1"]}`)
	if st, _ := a.DbGetTabState(info.ID); st != `{"tabs":["q1"]}` {
		t.Fatalf("tab state %q", st)
	}
	if lab.loginCount() != 2 {
		// One SSH login per handle, however many sessions and pools.
		t.Fatalf("%d ssh logins, want 2", lab.loginCount())
	}
}

// A database listed under the connection takes its own fields over the
// connection's defaults; an unknown one is refused.
func TestDbTargetOverrides(t *testing.T) {
	pg, mysql, ro, cred, empty := "postgres", "mysql", true, "c-stage", ""
	appdb, stage := "app", "app_stage"
	r := &store.ResolvedSettings{
		DbEnabled: true, DbEngine: pg, DbAddr: "", DbName: appdb, DbTLS: "disable", DbUseTunnel: true,
		DbCredentialRef: &cred,
		DbTargets: []store.DbTarget{
			{ID: "t1", Name: "stage", Database: &stage, ReadOnly: &ro, CredentialRef: &empty},
			{ID: "t2", Engine: &mysql, Database: &appdb},
		},
	}
	s, name, err := dbTarget(r, "t1")
	if err != nil || name != "stage" || s.DbName != "app_stage" || !s.DbReadOnly || s.DbCredentialRef != nil || s.DbEngine != "postgres" {
		t.Fatalf("t1: %v %q %+v", err, name, s)
	}
	if r.DbName != "app" || r.DbReadOnly {
		t.Fatal("the connection's settings were changed")
	}
	if s, name, _ = dbTarget(r, "t2"); name != "app" || s.DbEngine != "mysql" || *s.DbCredentialRef != "c-stage" {
		t.Fatalf("t2: %q %+v", name, s)
	}
	if _, _, err := dbTarget(r, "gone"); err == nil {
		t.Fatal("unknown target accepted")
	}
}

// DbSaveLogin stores the login in the vault and points the database at it.
func TestDbSaveLogin(t *testing.T) {
	a := newDbTestApp(t)
	on := true
	c, err := a.db.CreateConnection(store.NewConnection{Name: "db1", Hostname: "db1.example.com", Overrides: store.InheritableSettings{
		DbEnabled: &on, DbTargets: []store.DbTarget{{ID: "t1", Name: "stage"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DbOpen(c.ID, "t1"); err == nil || !strings.Contains(err.Error(), "needs a login") {
		t.Fatalf("open without a login: %v", err)
	}
	id, err := a.DbSaveLogin(c.ID, "t1", "", "stage_user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := a.db.GetConnection(c.ID)
	if tg := got.Overrides.DbTargets[0]; tg.CredentialRef == nil || *tg.CredentialRef != id || got.Overrides.DbCredentialRef != nil {
		t.Fatalf("target login not set: %+v", got.Overrides)
	}
	cred, _ := a.db.GetCredential(id)
	if cred == nil || cred.Name != "db1 database" || *cred.DefaultUsername != "stage_user" {
		t.Fatalf("credential %+v", cred)
	}
	if _, err := a.DbSaveLogin(c.ID, "", "own", "u", "p"); err != nil {
		t.Fatal(err)
	}
	got, _ = a.db.GetConnection(c.ID)
	if got.Overrides.DbCredentialRef == nil {
		t.Fatal("the connection's own login not set")
	}
}
