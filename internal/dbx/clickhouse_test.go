package dbx

import (
	"context"
	"strings"
	"testing"
)

func TestCHRead(t *testing.T) {
	body := "a\tb\nUInt8\tNullable(String)\n1\t\\N\n2\tx\\ty\\\\n\\'q\n"
	r := chRead(strings.NewReader(body), 10, true, nil)
	if r.Error != "" || len(r.Columns) != 2 || r.Columns[1].Type != "Nullable(String)" || len(r.Rows) != 2 || r.RowsAffected != 2 {
		t.Fatalf("%+v", r)
	}
	if r.Rows[0][1] != nil || *r.Rows[1][1] != "x\ty\\n'q" {
		t.Fatalf("values %v %q", r.Rows[0][1], *r.Rows[1][1])
	}
	// One column, an empty string row.
	r = chRead(strings.NewReader("s\nString\n\nz\n"), 10, true, nil)
	if len(r.Rows) != 2 || *r.Rows[0][0] != "" || *r.Rows[1][0] != "z" {
		t.Fatalf("empty string row: %+v", r)
	}
	// Bytes that are not UTF-8 come back as hex.
	r = chRead(strings.NewReader("b\nString\n\xff\x00\n"), 10, true, nil)
	if *r.Rows[0][0] != "0xff00" {
		t.Fatalf("binary %q", *r.Rows[0][0])
	}
}

func TestCHReadBounds(t *testing.T) {
	cut := 0
	r := chRead(strings.NewReader("n\nUInt64\n1\n2\n3\n4\n"), 2, true, func() { cut++ })
	if !r.Truncated || len(r.Rows) != 2 || cut != 1 || r.RowsAffected != -1 {
		t.Fatalf("%+v cut=%d", r, cut)
	}
	// With no onCut the rest is read and dropped.
	r = chRead(strings.NewReader("n\nUInt64\n1\n2\n3\n4\n"), 2, true, nil)
	if !r.Truncated || len(r.Rows) != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestCHException(t *testing.T) {
	// 24.x and later mark it.
	r := chRead(strings.NewReader("n\nUInt64\n1\n__exception__\nCode: 395. DB::Exception: boom. (FUNCTION_THROW_IF_VALUE_IS_NON_ZERO) (version 25.8.1.1)\n"), 10, true, nil)
	if r.Error != "Code: 395. DB::Exception: boom. (FUNCTION_THROW_IF_VALUE_IS_NON_ZERO)" {
		t.Fatalf("%q", r.Error)
	}
	// Older servers write it where a row was due.
	r = chRead(strings.NewReader("n\tm\nUInt64\tUInt8\n1\t0\nCode: 395. DB::Exception: boom\n"), 10, true, nil)
	if !strings.Contains(r.Error, "boom") {
		t.Fatalf("%+v", r)
	}
	// A value that merely looks like one is a value.
	r = chRead(strings.NewReader("s\tn\nString\tUInt8\nCode: 1. DB::Exception: x\t1\n"), 10, true, nil)
	if r.Error != "" || len(r.Rows) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestCHReadLines(t *testing.T) {
	// A logged error as data is a line, not the statement's error.
	logged := "Code: 60. DB::Exception: Table x does not exist. (UNKNOWN_TABLE)\n"
	if r := chReadLines(strings.NewReader(logged+"next\n"), 10, nil); r.Error != "" || len(r.Rows) != 2 {
		t.Fatalf("logged error: %+v", r)
	}
	// The same line last in the body is an old server's error.
	if r := chReadLines(strings.NewReader("a\n"+logged), 10, nil); !strings.Contains(r.Error, "UNKNOWN_TABLE") {
		t.Fatalf("trailing error: %+v", r)
	}
	r := chReadLines(strings.NewReader(`{"a":1}`+"\n"+`{"a":2}`+"\n"), 10, nil)
	if !r.HasRows || r.Columns[0].Name != "output" || len(r.Rows) != 2 || *r.Rows[1][0] != `{"a":2}` {
		t.Fatalf("%+v", r)
	}
	if r = chReadLines(strings.NewReader(""), 10, nil); r.HasRows || r.Error != "" {
		t.Fatalf("empty: %+v", r)
	}
}

func TestCHHelpers(t *testing.T) {
	if got := splitTop("kind, toDate(ts, 'UTC'), (a, b)"); len(got) != 3 || got[1] != "toDate(ts, 'UTC')" || got[2] != "(a, b)" {
		t.Fatalf("splitTop %q", got)
	}
	if chString(`a'b\c`) != `'a\'b\\c'` || chIdent("a`b") != "`a\\`b`" {
		t.Fatal("quoting")
	}
	if chWritten(`{"read_rows":"7","written_rows":"7"}`) != 7 || chWritten("") != -1 {
		t.Fatal("written_rows")
	}
	if DefaultPort(EngineClickHouse, TLSDisable) != 8123 || DefaultPort(EngineClickHouse, TLSRequire) != 8443 || DefaultPort(EngineMySQL, TLSRequire) != 3306 {
		t.Fatal("port")
	}
	if a, _ := ParseAddr(EngineClickHouse, TLSVerifyFull, "ch"); a != "ch:8443" {
		t.Fatalf("tls addr %q", a)
	}
	if a, _ := ParseAddr(EngineClickHouse, TLSVerifyFull, "ch:9443"); a != "ch:9443" {
		t.Fatalf("explicit port %q", a)
	}
}

func TestCHHasFormat(t *testing.T) {
	for q, want := range map[string]bool{
		"SELECT 1 FORMAT JSONEachRow":                    true,
		"select 1 format CSV settings max_threads = 1":   true,
		"SELECT 1 FORMAT Vertical -- why":                true,
		"SELECT format('{}', 1) AS format":               false,
		"SELECT format FROM t":                           false,
		"SELECT 'FORMAT CSV'":                            false,
		"SELECT 1 /* FORMAT CSV */":                      false,
		"SELECT 1 SETTINGS output_format_json_quote = 1": false,
	} {
		if chHasFormat(q) != want {
			t.Errorf("%q: want %v", q, want)
		}
	}
}

type cancelledReader struct{ data string }

func (c *cancelledReader) Read(p []byte) (int, error) {
	if c.data == "" {
		return 0, context.Canceled
	}
	n := copy(p, c.data)
	c.data = c.data[n:]
	return n, nil
}

// Stop while rows are arriving is an error, not a short result.
func TestCHReadStopped(t *testing.T) {
	r := chRead(&cancelledReader{data: "n\nUInt64\n1\n2\n"}, 10, true, nil)
	if r.Error != "query stopped" {
		t.Fatalf("%+v", r)
	}
	r = chReadLines(&cancelledReader{data: "a\n"}, 10, nil)
	if r.Error != "query stopped" {
		t.Fatalf("%+v", r)
	}
}
