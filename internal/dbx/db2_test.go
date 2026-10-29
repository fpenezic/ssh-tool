package dbx

import (
	"reflect"
	"testing"
)

func TestSplitDB2(t *testing.T) {
	cases := []struct {
		script string
		want   []string
	}{
		{"SELECT 1 FROM SYSIBM.SYSDUMMY1; VALUES 2;", []string{"SELECT 1 FROM SYSIBM.SYSDUMMY1", "VALUES 2"}},
		// SQL PL keeps its semicolons up to an "@" (db2 -td@) or "/" line.
		{"CREATE OR REPLACE PROCEDURE p()\nBEGIN\n  DECLARE n INT;\n  SET n = 1;\nEND\n@\nCALL p()",
			[]string{"CREATE OR REPLACE PROCEDURE p()\nBEGIN\n  DECLARE n INT;\n  SET n = 1;\nEND", "CALL p()"}},
		{"BEGIN ATOMIC\n  INSERT INTO t VALUES (1);\nEND\n/\nSELECT 1 FROM t", []string{"BEGIN ATOMIC\n  INSERT INTO t VALUES (1);\nEND", "SELECT 1 FROM t"}},
		// Plain DDL is no block.
		{"CREATE TABLE t (a INT); CREATE VIEW v AS SELECT a FROM t;", []string{"CREATE TABLE t (a INT)", "CREATE VIEW v AS SELECT a FROM t"}},
		// No backslash escape; nested comments.
		{"SELECT 'a\\'; /* x /* y; */ z; */ SELECT 2 FROM t", []string{"SELECT 'a\\'", "/* x /* y; */ z; */ SELECT 2 FROM t"}},
	}
	for _, c := range cases {
		if got := texts(Split(EngineDB2, c.script)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q:\n got %q\nwant %q", c.script, got, c.want)
		}
	}
}

func TestCheckReadOnlyDB2(t *testing.T) {
	ok := []string{
		"SELECT * FROM t",
		"VALUES CURRENT TIMESTAMP",
		"WITH x(a) AS (VALUES 1) SELECT a FROM x",
		"SELECT 'delete' FROM SYSIBM.SYSDUMMY1",
		"SELECT update_time FROM t",
	}
	bad := []string{
		"DELETE FROM t",
		"CALL p()",
		"SELECT * FROM FINAL TABLE (INSERT INTO t VALUES (1))",
		"SELECT * FROM OLD TABLE (DELETE FROM t)",
		"VALUES NEXT VALUE FOR seq",
		"SELECT seq.NEXTVAL FROM SYSIBM.SYSDUMMY1",
		"BEGIN DELETE FROM t; END",
	}
	for _, s := range ok {
		if err := CheckReadOnly(EngineDB2, s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := CheckReadOnly(EngineDB2, s); err == nil {
			t.Errorf("%q allowed", s)
		}
	}
}

func TestIsTxControlDB2(t *testing.T) {
	cases := map[string]bool{
		"COMMIT":                                 true,
		"ROLLBACK TO SAVEPOINT a":                true,
		"SAVEPOINT a ON ROLLBACK RETAIN CURSORS": true,
		"RELEASE SAVEPOINT a":                    true,
		"BEGIN ATOMIC SET x = 1; END":            false,
		"SELECT 1 FROM SYSIBM.SYSDUMMY1":         false,
	}
	for s, want := range cases {
		if got := IsTxControl(EngineDB2, s); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}

func TestRedactSecretsDB2(t *testing.T) {
	cases := map[string]string{
		"CONNECT TO sample USER db2inst1 USING s3cret": "CONNECT TO sample USER db2inst1 USING '***'",
		"CONNECT TO sample USER db2inst1 USING 'a b'":  "CONNECT TO sample USER db2inst1 USING '***'",
		"SET ENCRYPTION PASSWORD = 'k3y'":              "SET ENCRYPTION PASSWORD = '***'",
	}
	for in, want := range cases {
		if got := RedactSecrets(EngineDB2, in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestDB2Limit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT * FROM t", "SELECT * FROM t\nFETCH FIRST 501 ROWS ONLY"},
		{"SELECT * FROM t ORDER BY a;", "SELECT * FROM t ORDER BY a\nFETCH FIRST 501 ROWS ONLY"},
		{"SELECT * FROM t -- all\n", "SELECT * FROM t -- all\nFETCH FIRST 501 ROWS ONLY"},
		// The query's own bound, or a clause that has to stay last.
		{"SELECT * FROM t FETCH FIRST 5 ROWS ONLY", "SELECT * FROM t FETCH FIRST 5 ROWS ONLY"},
		{"SELECT * FROM t LIMIT 5", "SELECT * FROM t LIMIT 5"},
		{"SELECT * FROM t WITH UR", "SELECT * FROM t WITH UR"},
		{"SELECT * FROM t FOR READ ONLY", "SELECT * FROM t FOR READ ONLY"},
		// A subquery's own bound is not the outer query's.
		{"SELECT * FROM t WHERE a = (SELECT a FROM u FETCH FIRST 1 ROW ONLY)", "SELECT * FROM t WHERE a = (SELECT a FROM u FETCH FIRST 1 ROW ONLY)\nFETCH FIRST 501 ROWS ONLY"},
		// A word in a string is no clause.
		{"SELECT 'limit' FROM t", "SELECT 'limit' FROM t\nFETCH FIRST 501 ROWS ONLY"},
	}
	for _, c := range cases {
		if got := db2Limit(c.in, 500); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestDB2Type(t *testing.T) {
	cases := []struct {
		typ           string
		length, scale int64
		want          string
	}{
		{"VARCHAR", 40, 0, "VARCHAR(40)"},
		{"CHARACTER", 1, 0, "CHAR(1)"},
		{"DECIMAL", 9, 2, "DECIMAL(9,2)"},
		{"DECIMAL", 5, 0, "DECIMAL(5)"},
		{"TIMESTAMP", 10, 6, "TIMESTAMP"},
		{"TIMESTAMP", 13, 3, "TIMESTAMP(3)"},
		{"CLOB", 1 << 20, 0, "CLOB(1M)"},
		{"DECFLOAT", 16, 0, "DECFLOAT(34)"},
		{"INTEGER", 4, 0, "INTEGER"},
	}
	for _, c := range cases {
		if got := db2Type(c.typ, c.length, c.scale); got != c.want {
			t.Errorf("%s(%d,%d): got %q want %q", c.typ, c.length, c.scale, got, c.want)
		}
	}
}
