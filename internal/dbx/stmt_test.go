package dbx

import (
	"reflect"
	"strings"
	"testing"
)

func texts(st []Stmt) []string {
	out := make([]string, len(st))
	for i, s := range st {
		out[i] = s.Text
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		engine, script string
		want           []string
	}{
		{EnginePostgres, "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{EnginePostgres, "SELECT 'a;b'; SELECT \"x;y\" FROM t", []string{"SELECT 'a;b'", "SELECT \"x;y\" FROM t"}},
		{EnginePostgres, "SELECT 'it''s; fine'", []string{"SELECT 'it''s; fine'"}},
		{EnginePostgres, "-- a; comment\nSELECT 1; /* b; */ SELECT 2", []string{"-- a; comment\nSELECT 1", "/* b; */ SELECT 2"}},
		{EnginePostgres, "CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql; SELECT f()",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql", "SELECT f()"}},
		{EnginePostgres, "DO $body$ BEGIN PERFORM 1; END $body$; SELECT 2",
			[]string{"DO $body$ BEGIN PERFORM 1; END $body$", "SELECT 2"}},
		// $1 is a parameter, not a dollar quote.
		{EnginePostgres, "SELECT $1; SELECT 2", []string{"SELECT $1", "SELECT 2"}},
		{EnginePostgres, "SELECT E'a\\';b'; SELECT 2", []string{"SELECT E'a\\';b'", "SELECT 2"}},
		{EngineMySQL, "SELECT 'a\\';b'; SELECT `c;d` FROM t", []string{"SELECT 'a\\';b'", "SELECT `c;d` FROM t"}},
		{EngineMySQL, "# x; y\nSELECT 1", []string{"# x; y\nSELECT 1"}},
		{EnginePostgres, " ; ;-- only a comment\n", nil},
		{EnginePostgres, "SELECT 'unterminated; still one", []string{"SELECT 'unterminated; still one"}},
		{EnginePostgres, "/* a /* b; */ c; */ SELECT 1; SELECT 2", []string{"/* a /* b; */ c; */ SELECT 1", "SELECT 2"}},
		{EngineMySQL, "/* a /* b */ SELECT 1; SELECT 2", []string{"/* a /* b */ SELECT 1", "SELECT 2"}},
	}
	for _, c := range cases {
		if got := texts(Split(c.engine, c.script)); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.engine, c.script, got, c.want)
		}
	}
}

func TestSplitOffsets(t *testing.T) {
	script := "SELECT 1;\n\n  UPDATE t SET a = 1 ;"
	for _, s := range Split(EnginePostgres, script) {
		if script[s.Start:s.End] != s.Text {
			t.Fatalf("range %d-%d = %q, text %q", s.Start, s.End, script[s.Start:s.End], s.Text)
		}
	}
	if s, ok := StatementAt(EnginePostgres, script, strings.Index(script, "SET")); !ok || !strings.HasPrefix(s.Text, "UPDATE") {
		t.Fatalf("at SET: %q", s.Text)
	}
	// Between statements: the one before.
	if s, _ := StatementAt(EnginePostgres, script, 10); s.Text != "SELECT 1" {
		t.Fatalf("between: %q", s.Text)
	}
}

func TestCheckReadOnly(t *testing.T) {
	allowed := []string{
		"SELECT 1", "  (SELECT 1) UNION (SELECT 2)", "-- why\nselect * from t", "WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW search_path", "EXPLAIN SELECT 1", "VALUES (1)", "TABLE t", "/* c */ DESCRIBE t",
		"SELECT 'INTO OUTFILE' AS s", "SELECT update_time, delete_flag FROM t", "EXPLAIN SELECT 1",
	}
	for _, s := range allowed {
		for _, e := range []string{EnginePostgres, EngineMySQL} {
			if err := CheckReadOnly(e, s); err != nil {
				t.Errorf("%s %q refused: %v", e, s, err)
			}
		}
	}
	refused := []string{
		"DELETE FROM t", "update t set a=1", "DROP TABLE t", "CREATE TABLE x (a int)", "TRUNCATE t",
		"SET transaction_read_only = off", "SET TRANSACTION READ WRITE", "BEGIN", "COMMIT", "CALL p()",
		"DO $$ BEGIN END $$", "COPY t FROM STDIN", "GRANT ALL ON t TO x", "LOCK TABLE t", "", "-- only",
		"WITH d AS (DELETE FROM t RETURNING id) SELECT count(*) FROM d",
		"with u as (update t set a = 1 returning *) select * from u",
		"EXPLAIN ANALYZE DELETE FROM t", "EXPLAIN (ANALYZE, BUFFERS) SELECT 1",
		"SELECT * INTO t2 FROM t",
	}
	for _, s := range refused {
		if err := CheckReadOnly(EnginePostgres, s); err == nil {
			t.Errorf("%q allowed", s)
		}
	}
	for _, s := range []string{
		"SELECT * FROM t INTO OUTFILE '/tmp/x'", "select 1 into dumpfile '/tmp/y'",
		// Executable comments are code to MySQL.
		"SELECT * FROM t /*!INTO OUTFILE '/tmp/x'*/", "/*!50000 DROP TABLE t */", "SELECT 1 INTO @v",
		// "--" is a MySQL comment only before whitespace.
		"SELECT 1 --1 INTO OUTFILE '/tmp/x'",
	} {
		if err := CheckReadOnly(EngineMySQL, s); err == nil {
			t.Errorf("mysql %q allowed", s)
		}
	}
}

func TestParseAddr(t *testing.T) {
	cases := map[string]string{
		"":              "127.0.0.1:5432",
		"db":            "db:5432",
		"db:6432":       "db:6432",
		"[::1]":         "[::1]:5432",
		" 10.0.0.5:15 ": "10.0.0.5:15",
	}
	for in, want := range cases {
		got, err := ParseAddr(EnginePostgres, in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	if got, _ := ParseAddr(EngineMySQL, "db"); got != "db:3306" {
		t.Errorf("mysql default port: %q", got)
	}
	if _, err := ParseAddr(EnginePostgres, "db:x"); err == nil {
		t.Error("bad port accepted")
	}
}

func TestRowReaderBounds(t *testing.T) {
	r := &Result{}
	rr := &rowReader{r: r, maxRows: 2, isBinary: []bool{false, true}, hexPref: "0x"}
	if !rr.add([][]byte{[]byte("a"), {0xde, 0xad}}) || !rr.add([][]byte{nil, nil}) {
		t.Fatal("rows within the bound refused")
	}
	if rr.add([][]byte{[]byte("c"), nil}) || !r.Truncated {
		t.Fatal("third row accepted")
	}
	if *r.Rows[0][1] != "0xdead" || r.Rows[1][0] != nil {
		t.Fatalf("values: %v %v", *r.Rows[0][1], r.Rows[1][0])
	}
	big := strings.Repeat("\u00e9", MaxCellBytes) // 2 bytes each
	r2 := &Result{}
	rr2 := &rowReader{r: r2, maxRows: 10}
	rr2.add([][]byte{[]byte(big)})
	if r2.CutCells != 1 || len(*r2.Rows[0][0]) > MaxCellBytes+3 || !strings.HasSuffix(*r2.Rows[0][0], "...") {
		t.Fatalf("cell not cut: %d cut, len %d", r2.CutCells, len(*r2.Rows[0][0]))
	}
}

func TestIsTxControl(t *testing.T) {
	for _, s := range []string{"BEGIN", "start transaction", "COMMIT;", "rollback to savepoint a", "-- x\nEND"} {
		if !IsTxControl(EnginePostgres, s) {
			t.Errorf("%q not transaction control", s)
		}
	}
	if IsTxControl(EnginePostgres, "SELECT 'BEGIN'") || IsTxControl(EngineMySQL, "UPDATE t SET a = 1") {
		t.Error("ordinary statement taken for transaction control")
	}
}

// The writes under a read keyword count as writes (audit, confirmation).
func TestIsReadWriteForms(t *testing.T) {
	for _, s := range []string{
		"WITH d AS (DELETE FROM t RETURNING id) SELECT count(*) FROM d",
		"EXPLAIN ANALYZE UPDATE t SET a = 1",
		"SELECT * INTO new_t FROM t",
	} {
		if IsRead(EnginePostgres, s) {
			t.Errorf("%q taken for a read", s)
		}
	}
	if IsRead(EngineMySQL, "SELECT 1 /*!INTO OUTFILE '/x'*/") {
		t.Error("MySQL executable comment taken for a comment")
	}
	if IsRead(EngineMySQL, "SELECT 1 --1 INTO OUTFILE '/x'") || !IsRead(EngineMySQL, "SELECT 1 -- into later") {
		t.Error("MySQL -- comment rule")
	}
	if !IsRead(EnginePostgres, "WITH x AS (SELECT 1) SELECT * FROM x -- delete later") || !IsRead(EngineMySQL, "SELECT '/*!' AS s") {
		t.Error("plain reads taken for writes")
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := map[string]string{
		"ALTER ROLE app PASSWORD 'se''cret'":                           "ALTER ROLE app PASSWORD '***'",
		"CREATE USER u WITH ENCRYPTED PASSWORD 'x' VALID UNTIL 'y'":    "CREATE USER u WITH ENCRYPTED PASSWORD '***' VALID UNTIL 'y'",
		"create user 'u'@'%' identified by 'pw'":                       "create user 'u'@'%' identified by '***'",
		"ALTER USER u IDENTIFIED WITH caching_sha2_password BY \"pw\"": "ALTER USER u IDENTIFIED WITH caching_sha2_password BY '***'",
		"SET PASSWORD = 'pw'":                                          "SET PASSWORD = '***'",
		"SET PASSWORD FOR 'u'@'%' = 'pw'":                              "SET PASSWORD FOR 'u'@'%' = '***'",
		"ALTER USER u IDENTIFIED BY 'new' REPLACE 'old'":               "ALTER USER u IDENTIFIED BY '***' REPLACE '***'",
		"ALTER USER u IDENTIFIED BY 'p\\'w' ACCOUNT LOCK":              "ALTER USER u IDENTIFIED BY '***' ACCOUNT LOCK",
		"SELECT REPLACE(name, 'a', 'b') FROM t":                        "SELECT REPLACE(name, 'a', 'b') FROM t",
		"SELECT password FROM users":                                   "SELECT password FROM users",
	}
	for in, want := range cases {
		if got := RedactSecrets(EngineMySQL, in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// Comments between the tokens do not hide a password.
func TestRedactSecretsAcrossComments(t *testing.T) {
	cases := []struct{ engine, in, want string }{
		{EngineMySQL, "SET PASSWORD FOR 'u'@'%' /* note */ = 'pw'", "SET PASSWORD FOR 'u'@'%'   = '***'"},
		{EngineMySQL, "ALTER USER u IDENTIFIED /*x*/ BY 'pw'", "ALTER USER u IDENTIFIED   BY '***'"},
		{EnginePostgres, "ALTER ROLE r PASSWORD /*x*/ 'pw'", "ALTER ROLE r PASSWORD   '***'"},
		{EngineMySQL, "CREATE USER u IDENTIFIED BY # x\n 'pw'", "CREATE USER u IDENTIFIED BY   '***'"},
		{EnginePostgres, "ALTER ROLE r PASSWORD -- note\n 'pw'", "ALTER ROLE r PASSWORD   '***'"},
		{EnginePostgres, "UPDATE t SET note = '-- not a comment'", "UPDATE t SET note = '-- not a comment'"},
		// Postgres nests block comments; MySQL does not.
		{EnginePostgres, "/* a /* b */ ' */ ALTER ROLE r PASSWORD 'pw'", "  ALTER ROLE r PASSWORD '***'"},
		{EngineMySQL, "/* a /* b */ SET PASSWORD = 'pw'", "  SET PASSWORD = '***'"},
	}
	for _, c := range cases {
		if got := RedactSecrets(c.engine, c.in); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}
