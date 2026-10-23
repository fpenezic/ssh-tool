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
		// ClickHouse: backslash escapes in every quote, # comments, nested
		// block comments, heredocs, and "--1" is a comment as on Postgres.
		{EngineClickHouse, "SELECT 'a\\';b', `c\\`;d`, \"e\\\";f\"; SELECT 2", []string{"SELECT 'a\\';b', `c\\`;d`, \"e\\\";f\"", "SELECT 2"}},
		{EngineClickHouse, "# x; y\nSELECT 1", []string{"# x; y\nSELECT 1"}},
		{EngineClickHouse, "/* a /* b; */ c; */ SELECT 1; SELECT 2", []string{"/* a /* b; */ c; */ SELECT 1", "SELECT 2"}},
		{EngineClickHouse, "SELECT $$x;y$$; SELECT $t$a;b$t$", []string{"SELECT $$x;y$$", "SELECT $t$a;b$t$"}},
		{EngineClickHouse, "SELECT 3 --1; DROP TABLE t\nSELECT 4", []string{"SELECT 3 --1; DROP TABLE t\nSELECT 4"}},
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
		got, err := ParseAddr(EnginePostgres, "", in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	if got, _ := ParseAddr(EngineMySQL, "", "db"); got != "db:3306" {
		t.Errorf("mysql default port: %q", got)
	}
	if _, err := ParseAddr(EnginePostgres, "", "db:x"); err == nil {
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

func TestCheckReadOnlyClickHouse(t *testing.T) {
	for _, s := range []string{
		"SELECT 1", "EXISTS TABLE t", "SHOW TABLES", "DESCRIBE TABLE t", "EXPLAIN SELECT 1",
		"WITH 1 AS x SELECT x", "SELECT 1 FORMAT JSONEachRow", "SELECT 'INSERT' # INSERT\n",
	} {
		if err := CheckReadOnly(EngineClickHouse, s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	for _, s := range []string{
		"INSERT INTO t VALUES (1)", "ALTER TABLE t DELETE WHERE 1", "OPTIMIZE TABLE t FINAL", "SYSTEM FLUSH LOGS",
		"SET readonly = 0", "KILL QUERY WHERE 1", "SELECT * FROM t INTO OUTFILE 'x'", "TRUNCATE t", "USE db",
		"/* c */ DROP TABLE t", "RENAME TABLE a TO b", "EXCHANGE TABLES a AND b",
	} {
		if err := CheckReadOnly(EngineClickHouse, s); err == nil {
			t.Errorf("%q allowed", s)
		}
	}
	if CheckReadOnly(EnginePostgres, "EXISTS TABLE t") == nil {
		t.Error("EXISTS is a read only on ClickHouse")
	}
}

func TestSplitOracle(t *testing.T) {
	cases := []struct {
		script string
		want   []string
	}{
		{"SELECT 1 FROM DUAL; SELECT 2 FROM DUAL;", []string{"SELECT 1 FROM DUAL", "SELECT 2 FROM DUAL"}},
		// SQL*Plus "/" ends a statement too.
		{"SELECT 1 FROM DUAL\n/\nSELECT 2 FROM DUAL", []string{"SELECT 1 FROM DUAL", "SELECT 2 FROM DUAL"}},
		// A block keeps its semicolons (END; included) up to the "/" line.
		{"BEGIN\n  x;\n  y;\nEND;\n/\nSELECT 1 FROM DUAL", []string{"BEGIN\n  x;\n  y;\nEND;", "SELECT 1 FROM DUAL"}},
		{"DECLARE n NUMBER; BEGIN n := 1; END;", []string{"DECLARE n NUMBER; BEGIN n := 1; END;"}},
		{"CREATE OR REPLACE EDITIONABLE PROCEDURE p IS BEGIN NULL; END;\n  /  \nSELECT 1 FROM DUAL",
			[]string{"CREATE OR REPLACE EDITIONABLE PROCEDURE p IS BEGIN NULL; END;", "SELECT 1 FROM DUAL"}},
		// Plain DDL is no block.
		{"CREATE TABLE t (a NUMBER); CREATE OR REPLACE VIEW v AS SELECT 1 x FROM DUAL;", []string{"CREATE TABLE t (a NUMBER)", "CREATE OR REPLACE VIEW v AS SELECT 1 x FROM DUAL"}},
		// q-quotes, and no backslash escape.
		{"SELECT q'[a;'b]' FROM DUAL; SELECT Nq'{;}' FROM DUAL; SELECT 'a\\'; SELECT 2 FROM DUAL",
			[]string{"SELECT q'[a;'b]' FROM DUAL", "SELECT Nq'{;}' FROM DUAL", "SELECT 'a\\'", "SELECT 2 FROM DUAL"}},
		// WITH FUNCTION carries PL/SQL: a block to the "/" line.
		{"WITH FUNCTION f RETURN NUMBER IS BEGIN RETURN 1; END;\nSELECT f FROM DUAL\n/\nSELECT 2 FROM DUAL",
			[]string{"WITH FUNCTION f RETURN NUMBER IS BEGIN RETURN 1; END;\nSELECT f FROM DUAL", "SELECT 2 FROM DUAL"}},
		// A "/" in an expression is division.
		{"SELECT 4\n/ 2 FROM DUAL", []string{"SELECT 4\n/ 2 FROM DUAL"}},
	}
	for _, c := range cases {
		if got := texts(Split(EngineOracle, c.script)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q:\n got %q\nwant %q", c.script, got, c.want)
		}
	}
}

func TestSplitMSSQL(t *testing.T) {
	cases := []struct {
		script string
		want   []string
	}{
		{"SELECT 1; SELECT [a;b] FROM t; SELECT N'it''s;'", []string{"SELECT 1", "SELECT [a;b] FROM t", "SELECT N'it''s;'"}},
		{"SELECT 1\nGO\nSELECT 2\n  go\nSELECT 3", []string{"SELECT 1", "SELECT 2", "SELECT 3"}},
		// GO n repeats the batch; GO takes a trailing comment.
		{"SELECT 0\nGO\nINSERT t VALUES (1); SELECT 1\nGO 3 -- three rows\nSELECT 2", []string{"SELECT 0", "INSERT t VALUES (1)", "SELECT 1", "INSERT t VALUES (1)", "SELECT 1", "INSERT t VALUES (1)", "SELECT 1", "SELECT 2"}},
		{"SELECT 1\nGO -- done\nSELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"SELECT 1\nGO x\nSELECT 2", []string{"SELECT 1\nGO x\nSELECT 2"}},
		{"SELECT 1\nGO 100001\nSELECT 2", []string{"SELECT 1\nGO 100001\nSELECT 2"}},
		// The bound is on what the batch expands to.
		{"SELECT 1; SELECT 2\nGO 50001\nSELECT 3", []string{"SELECT 1", "SELECT 2\nGO 50001\nSELECT 3"}},
		// A DECLARE batch, a routine and a BEGIN block run to GO.
		{"DECLARE @x int; SET @x = 1; SELECT @x\nGO\nSELECT 2", []string{"DECLARE @x int; SET @x = 1; SELECT @x", "SELECT 2"}},
		{"CREATE OR ALTER PROC p AS BEGIN SELECT 1; SELECT 2; END\nGO", []string{"CREATE OR ALTER PROC p AS BEGIN SELECT 1; SELECT 2; END"}},
		{"BEGIN TRY SELECT 1; END TRY BEGIN CATCH SELECT 2; END CATCH", []string{"BEGIN TRY SELECT 1; END TRY BEGIN CATCH SELECT 2; END CATCH"}},
		// BEGIN TRAN is a statement of its own.
		{"BEGIN TRAN; DELETE FROM t; COMMIT", []string{"BEGIN TRAN", "DELETE FROM t", "COMMIT"}},
		// GO inside a string or a name is not a separator.
		{"SELECT '\nGO\n'; SELECT 2", []string{"SELECT '\nGO\n'", "SELECT 2"}},
		{"/* a /* b; */ c; */ SELECT 1; SELECT 2", []string{"/* a /* b; */ c; */ SELECT 1", "SELECT 2"}},
	}
	for _, c := range cases {
		if got := texts(Split(EngineMSSQL, c.script)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q:\n got %q\nwant %q", c.script, got, c.want)
		}
	}
}

func TestCheckReadOnlyOracleMSSQL(t *testing.T) {
	ok := map[string][]string{
		EngineOracle: {"SELECT * FROM t", "WITH x AS (SELECT 1 a FROM DUAL) SELECT a FROM x", "SELECT 'delete' FROM DUAL"},
		EngineMSSQL: {"SELECT * FROM t", "SELECT [update], 'drop' FROM t", "WITH x AS (SELECT 1 AS a) SELECT a FROM x",
			"SELECT update_time FROM t", "SELECT @@ROWCOUNT, @delete FROM #exec",
			"SELECT a FROM t ORDER BY a OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY"},
	}
	bad := map[string][]string{
		EngineOracle: {"DELETE FROM t", "BEGIN NULL; END;", "DECLARE x NUMBER; BEGIN NULL; END;", "EXPLAIN PLAN FOR SELECT 1 FROM DUAL",
			"SELECT * INTO x FROM t", "WITH FUNCTION f RETURN NUMBER IS BEGIN RETURN 1; END; SELECT f FROM DUAL",
			"WITH d AS (SELECT 1 FROM DUAL) DELETE FROM t", "CALL p()", "LOCK TABLE t IN EXCLUSIVE MODE",
			"SELECT s.NEXTVAL FROM DUAL"},
		EngineMSSQL: {"DELETE FROM t", "EXEC sp_who", "sp_who", "SELECT 1 DELETE FROM t", "SELECT * INTO #x FROM t",
			"SELECT 1 EXEC('DROP TABLE t')", "SELECT * FROM OPENQUERY(srv, 'DELETE x')", "SELECT 1 COMMIT",
			"SELECT * FROM OPENROWSET(BULK 'x', SINGLE_CLOB) b", "WITH x AS (SELECT 1 AS a) MERGE t USING x ON 1=1 WHEN MATCHED THEN DELETE;",
			"DECLARE @x int", "SHOWPLAN_ALL ON", "SELECT 1 USE master", "SELECT 1 SET IMPLICIT_TRANSACTIONS ON",
			"SELECT NEXT VALUE FOR dbo.s"},
	}
	for eng, list := range ok {
		for _, s := range list {
			if err := CheckReadOnly(eng, s); err != nil {
				t.Errorf("%s %q refused: %v", eng, s, err)
			}
			if !IsRead(eng, s) {
				t.Errorf("%s %q not a read", eng, s)
			}
		}
	}
	for eng, list := range bad {
		for _, s := range list {
			if err := CheckReadOnly(eng, s); err == nil {
				t.Errorf("%s %q allowed", eng, s)
			}
			if IsRead(eng, s) {
				t.Errorf("%s %q a read", eng, s)
			}
		}
	}
}

func TestIsTxControlOracleMSSQL(t *testing.T) {
	cases := []struct {
		engine, stmt string
		want         bool
	}{
		{EngineOracle, "COMMIT", true},
		{EngineOracle, "SAVEPOINT a", true},
		{EngineOracle, "SET TRANSACTION READ ONLY", true},
		{EngineOracle, "BEGIN NULL; END;", false},
		{EngineMSSQL, "BEGIN TRAN", true},
		{EngineMSSQL, "begin transaction x", true},
		{EngineMSSQL, "SAVE TRAN a", true},
		{EngineMSSQL, "BEGIN SELECT 1 END", false},
		{EngineMSSQL, "ROLLBACK", true},
		// A batch that opens one half way; a string or a routine body does not.
		{EngineMSSQL, "DECLARE @x int; BEGIN TRAN; UPDATE t SET a = 1", true},
		{EngineMSSQL, "IF 1 = 1 BEGIN COMMIT END", true},
		{EngineMSSQL, "DECLARE @s nvarchar(20) = 'BEGIN TRAN'; SELECT @s", false},
		{EngineMSSQL, "CREATE OR ALTER PROC p AS BEGIN BEGIN TRAN; COMMIT; END", false},
		{EngineMSSQL, "UPDATE t SET note = 'rollback'", false},
		{EngineMSSQL, "DECLARE @x int; SELECT @rollback", false},
	}
	for _, c := range cases {
		if got := IsTxControl(c.engine, c.stmt); got != c.want {
			t.Errorf("%s %q = %v", c.engine, c.stmt, got)
		}
	}
}

func TestRedactSecretsOracleMSSQL(t *testing.T) {
	cases := []struct {
		engine, in, want string
	}{
		{EngineOracle, "CREATE USER app IDENTIFIED BY s3cret DEFAULT TABLESPACE users", "CREATE USER app IDENTIFIED BY '***' DEFAULT TABLESPACE users"},
		{EngineOracle, `ALTER USER app IDENTIFIED BY "n#w" REPLACE old1`, `ALTER USER app IDENTIFIED BY '***' REPLACE '***'`},
		{EngineOracle, "ALTER USER app IDENTIFIED BY VALUES 'S:ABC'", "ALTER USER app IDENTIFIED BY VALUES '***'"},
		{EngineOracle, "CREATE OR REPLACE VIEW v AS SELECT 1 x FROM DUAL", "CREATE OR REPLACE VIEW v AS SELECT 1 x FROM DUAL"},
		{EngineMSSQL, "CREATE LOGIN app WITH PASSWORD = N'pa''ss', CHECK_POLICY = OFF", "CREATE LOGIN app WITH PASSWORD = '***', CHECK_POLICY = OFF"},
		{EngineMSSQL, "ALTER LOGIN app WITH PASSWORD = 'new' OLD_PASSWORD = 'old'", "ALTER LOGIN app WITH PASSWORD = '***' OLD_PASSWORD = '***'"},
	}
	for _, c := range cases {
		if got := RedactSecrets(c.engine, c.in); got != c.want {
			t.Errorf("%s %q:\n got %q\nwant %q", c.engine, c.in, got, c.want)
		}
	}
}
