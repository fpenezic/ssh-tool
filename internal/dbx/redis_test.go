package dbx

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
)

func TestRDArgs(t *testing.T) {
	cases := map[string][]string{
		`SET k v`:                      {"SET", "k", "v"},
		`  set   "a b"  'c d'  `:       {"set", "a b", "c d"},
		`SET k "line\nnext \x41\"q\\"`: {"SET", "k", "line\nnext A\"q\\"},
		`SET k 'it\'s'`:                {"SET", "k", "it's"},
		`ECHO ""`:                      {"ECHO", ""},
	}
	for in, want := range cases {
		got, err := rdArgs(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{`SET "k`, `SET 'k`, `SET "k"v`} {
		if _, err := rdArgs(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestRDSplit(t *testing.T) {
	script := "# keys\nSET a 1\n\n  GET a  \n// done\nDEL a"
	var texts []string
	for _, s := range Split(EngineRedis, script) {
		texts = append(texts, s.Text)
		if script[s.Start:s.End] != s.Text {
			t.Fatalf("range %+v", s)
		}
	}
	if strings.Join(texts, "|") != "SET a 1|GET a|DEL a" {
		t.Fatal(texts)
	}
}

func TestRDRead(t *testing.T) {
	for _, s := range []string{"GET k", "hgetall h", "SCAN 0 MATCH a:*", "CONFIG GET maxmemory", "CLIENT LIST",
		"INFO", "XINFO STREAM s", "EVAL_RO \"return 1\" 0", "SELECT 2", "MEMORY USAGE k", "JSON.GET j $", "ACL LOG 10"} {
		if !IsRead(EngineRedis, s) || CheckReadOnly(EngineRedis, s) != nil {
			t.Errorf("not read: %q", s)
		}
	}
	for _, s := range []string{"SET k v", "DEL k", "CONFIG SET maxmemory 1", "CONFIG", "CLIENT KILL ID 1",
		"EVAL \"return 1\" 0", "FLUSHALL", "XREADGROUP GROUP g c STREAMS s >", "SORT k", "OBJECT", "MULTI",
		"ACL SETUSER x on", "ACL LOG RESET", "SCRIPT FLUSH", "GET \"unterminated"} {
		if IsRead(EngineRedis, s) || CheckReadOnly(EngineRedis, s) == nil {
			t.Errorf("read: %q", s)
		}
	}
	if err := CheckReadOnly(EngineRedis, "config set x 1"); err == nil || !strings.Contains(err.Error(), "CONFIG SET") {
		t.Fatal(err)
	}
	for _, s := range []string{"MONITOR", "subscribe ch", "HELLO 3", "CLIENT REPLY OFF", "QUIT"} {
		args, _ := rdArgs(s)
		if rdRefuse(args) == "" {
			t.Errorf("not refused: %q", s)
		}
	}
}

func TestRDRedact(t *testing.T) {
	cases := map[string]string{
		"AUTH s3cret":                               "AUTH ***",
		"AUTH bob s3cret":                           "AUTH bob ***",
		"AUTH bob s3cret extra":                     "AUTH *** *** ***",
		"HELLO 2 AUTH bob s3cret":                   "HELLO 2 AUTH bob ***",
		"CONFIG SET requirepass s3cret maxmemory 1": "CONFIG SET requirepass *** maxmemory 1",
		"ACL SETUSER bob on >s3cret ~* +@read":      "ACL SETUSER bob on *** ~* +@read",
		"MIGRATE h 6379 k 0 5000 AUTH s3cret":       "MIGRATE h 6379 k 0 5000 AUTH ***",
		"MIGRATE h 6379 k 0 5000 AUTH2 bob s3cret":  "MIGRATE h 6379 k 0 5000 AUTH2 bob ***",
		"SET k v":            "SET k v",
		`AUTH "unterminated`: "AUTH ***",
	}
	for in, want := range cases {
		if got := RedactSecrets(EngineRedis, in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func rdParse(t *testing.T, raw string) *rdReply {
	t.Helper()
	rc := &rdConn{br: bufio.NewReader(strings.NewReader(raw)), budget: MaxResultBytes}
	r, err := rc.read(0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRDResult(t *testing.T) {
	r := rdResult([]string{"HGETALL", "h"}, rdParse(t, "*4\r\n$1\r\na\r\n$1\r\n1\r\n$1\r\nb\r\n$2\r\n\xff\x00\r\n"), 10)
	if len(r.Columns) != 2 || r.Columns[0].Name != "field" || len(r.Rows) != 2 || *r.Rows[1][1] != "0xff00" {
		t.Fatalf("%+v", r)
	}
	r = rdResult([]string{"SCAN", "0"}, rdParse(t, "*2\r\n$2\r\n17\r\n*2\r\n$1\r\nx\r\n$1\r\ny\r\n"), 10)
	if r.Note != "next cursor 17" || len(r.Rows) != 2 {
		t.Fatalf("%+v", r)
	}
	r = rdResult([]string{"ZRANGE", "z", "0", "-1", "withscores"}, rdParse(t, "*2\r\n$1\r\nm\r\n$3\r\n1.5\r\n"), 10)
	if r.Columns[1].Name != "score" || *r.Rows[0][1] != "1.5" {
		t.Fatalf("%+v", r)
	}
	r = rdResult([]string{"MGET", "a", "b"}, rdParse(t, "*3\r\n$1\r\n1\r\n$-1\r\n*1\r\n:5\r\n"), 2)
	if len(r.Rows) != 2 || r.Rows[1][0] != nil || !r.Truncated {
		t.Fatalf("%+v", r)
	}
	r = rdResult([]string{"INFO"}, rdParse(t, "$36\r\n# Server\r\nredis_version:7.4.1\r\nx:1\r\n\r\n"), 10)
	if len(r.Rows) != 2 || *r.Rows[0][0] != "Server" || *r.Rows[0][2] != "7.4.1" {
		t.Fatalf("%+v", r)
	}
	if r = rdResult([]string{"DEL", "k"}, rdParse(t, ":3\r\n"), 10); r.Command != "(integer) 3" || r.HasRows {
		t.Fatalf("%+v", r)
	}
	if r = rdResult([]string{"GET", "k"}, rdParse(t, "$-1\r\n"), 10); r.Command != "(nil)" {
		t.Fatalf("%+v", r)
	}
	if r = rdResult([]string{"SET", "k"}, rdParse(t, "-WRONGTYPE no\r\n"), 10); r.Error != "WRONGTYPE no" {
		t.Fatalf("%+v", r)
	}
}

// A reply past the budget is refused while it arrives.
func TestRDBudget(t *testing.T) {
	rc := &rdConn{br: bufio.NewReader(strings.NewReader("*2000000\r\n:1\r\n")), budget: 1 << 20}
	if _, err := rc.read(0); err != errReplyTooBig {
		t.Fatal(err)
	}
}

func TestRDGroups(t *testing.T) {
	if rdGroup("user:1:name") != "user:*" || rdGroup("plain") != rdNoPrefix {
		t.Fatal()
	}
	if m := rdMatch("a*[b:*"); m != `a\*\[b:*` {
		t.Fatal(m)
	}
	if rdMatch(rdNoPrefix) != "" {
		t.Fatal()
	}
	var b bytes.Buffer
	b.WriteString(rdPreviewText("list", rdParse(t, "*2\r\n$1\r\na\r\n$1\r\nb\r\n"), 5))
	if b.String() != `["a","b"]...` {
		t.Fatal(b.String())
	}
}

// One SCAN reply can carry far more keys than COUNT: the cap holds inside
// a reply, and a reply cut short is not reported as the end.
func TestRDScanCapsAReply(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	go func() {
		defer srv.Close()
		br := bufio.NewReader(srv)
		for {
			if _, err := br.ReadString('\n'); err != nil { // *N
				return
			}
			// Drain the rest of the command (SCAN 0 COUNT 1000: 4 bulk strings).
			for i := 0; i < 8; i++ {
				if _, err := br.ReadString('\n'); err != nil {
					return
				}
			}
			_, _ = srv.Write([]byte("*2\r\n$1\r\n0\r\n*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n"))
		}
	}()
	rc := &rdConn{c: cli, br: bufio.NewReader(cli), bw: bufio.NewWriter(cli)}
	keys, done, err := rdScan(context.Background(), rc, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || done {
		t.Fatalf("keys %v done %v, want 2 keys and not done", keys, done)
	}
}
