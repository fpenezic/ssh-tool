package dbx

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// A small RESP2 client: enough for the Database tab, so go-redis (and its
// pool, retries and cluster logic) stays out. A connection is used by one
// caller at a time.

// rdReply is one reply. kind is the RESP type byte: '+' simple string,
// '-' error, ':' integer, '$' bulk string, '*' array. nil is a null bulk
// string or array.
type rdReply struct {
	kind  byte
	str   []byte // simple, error, bulk
	num   int64  // integer
	elems []*rdReply
	null  bool
}

func (r *rdReply) text() string { return string(r.str) }

// rdConn is one server connection.
type rdConn struct {
	c  net.Conn
	br *bufio.Reader
	bw *bufio.Writer
	db int
	// budget is what is left of MaxResultBytes for the replies of one do():
	// an array of millions of elements (KEYS *, HGETALL on a big hash) is
	// refused while it arrives, not after it is all in memory.
	budget int
}

func rdDial(ctx context.Context, cfg Config, db int) (*rdConn, error) {
	c, err := cfg.dialer()(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	if tc := cfg.tlsConfig(); tc != nil {
		t := tls.Client(c, tc)
		if err := t.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, err
		}
		c = t
	}
	rc := &rdConn{c: c, br: bufio.NewReaderSize(c, 64<<10), bw: bufio.NewWriter(c)}
	var cmds [][]string
	switch {
	case cfg.User != "":
		cmds = append(cmds, []string{"AUTH", cfg.User, cfg.Password})
	case cfg.Password != "":
		cmds = append(cmds, []string{"AUTH", cfg.Password})
	}
	cmds = append(cmds, []string{"CLIENT", "SETNAME", rdClientName(cfg.AppName)})
	if db != 0 {
		cmds = append(cmds, []string{"SELECT", strconv.Itoa(db)})
	}
	replies, err := rc.do(ctx, cmds...)
	if err != nil {
		rc.close()
		return nil, err
	}
	for i, r := range replies {
		// A server that does not know CLIENT SETNAME (or an ACL that
		// refuses it) is fine; a failed AUTH or SELECT is not.
		if r.kind == '-' && cmds[i][0] != "CLIENT" {
			rc.close()
			return nil, errors.New(r.text())
		}
	}
	rc.db = db
	return rc, nil
}

// rdClientName is a name CLIENT SETNAME takes: no spaces.
func rdClientName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c <= ' ' || c > '~' {
			b[i] = '-'
		}
	}
	if len(b) == 0 {
		return "ssh-tool"
	}
	return string(b)
}

// do sends commands as one pipeline and reads a reply for each. A server
// error is a reply ('-'), not an error; err is the connection failing, after
// which the connection is no use. Ending ctx closes the connection.
func (rc *rdConn) do(ctx context.Context, cmds ...[]string) ([]*rdReply, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = rc.c.SetDeadline(dl)
	} else {
		_ = rc.c.SetDeadline(time.Time{})
	}
	// Closing, not only a deadline: an SSH channel ignores deadlines. The
	// connection is no use after a broken-off reply anyway.
	stop := context.AfterFunc(ctx, func() { _ = rc.c.Close() })
	defer stop()
	for _, cmd := range cmds {
		fmt.Fprintf(rc.bw, "*%d\r\n", len(cmd))
		for _, a := range cmd {
			fmt.Fprintf(rc.bw, "$%d\r\n%s\r\n", len(a), a)
		}
	}
	if err := rc.bw.Flush(); err != nil {
		return nil, rdCtxErr(ctx, err)
	}
	out := make([]*rdReply, 0, len(cmds))
	rc.budget = MaxResultBytes
	for range cmds {
		r, err := rc.read(0)
		if err != nil {
			return nil, rdCtxErr(ctx, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// one sends one command.
func (rc *rdConn) one(ctx context.Context, cmd ...string) (*rdReply, error) {
	rs, err := rc.do(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}

func rdCtxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (rc *rdConn) read(depth int) (*rdReply, error) {
	if depth > 32 {
		return nil, errors.New("reply nested too deep")
	}
	line, err := rc.line()
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, errors.New("empty reply line")
	}
	r := &rdReply{kind: line[0]}
	body := line[1:]
	switch r.kind {
	case '+', '-':
		r.str = append([]byte(nil), body...)
	case ':':
		if r.num, err = strconv.ParseInt(string(body), 10, 64); err != nil {
			return nil, fmt.Errorf("bad integer reply")
		}
	case '$':
		n, err := strconv.Atoi(string(body))
		if err != nil || n < -1 {
			return nil, fmt.Errorf("bad bulk length")
		}
		if n == -1 {
			r.null = true
			return r, nil
		}
		if rc.budget -= n; rc.budget < 0 {
			return nil, errReplyTooBig
		}
		r.str = make([]byte, n+2)
		if _, err := io.ReadFull(rc.br, r.str); err != nil {
			return nil, err
		}
		r.str = r.str[:n]
	case '*':
		n, err := strconv.Atoi(string(body))
		if err != nil || n < -1 {
			return nil, fmt.Errorf("bad array length")
		}
		if n == -1 {
			r.null = true
			return r, nil
		}
		// Each element costs its struct even when empty.
		if rc.budget -= n * 64; rc.budget < 0 {
			return nil, errReplyTooBig
		}
		r.elems = make([]*rdReply, 0, min(n, 1024))
		for i := 0; i < n; i++ {
			e, err := rc.read(depth + 1)
			if err != nil {
				return nil, err
			}
			r.elems = append(r.elems, e)
		}
	default:
		return nil, fmt.Errorf("unexpected reply type %q", r.kind)
	}
	return r, nil
}

var errReplyTooBig = fmt.Errorf("the reply is over %d MB: narrow it (SCAN, LRANGE with a range, HSCAN)", MaxResultBytes>>20)

func (rc *rdConn) line() ([]byte, error) {
	var buf []byte
	for {
		part, err := rc.br.ReadSlice('\n')
		buf = append(buf, part...)
		if err == bufio.ErrBufferFull {
			if len(buf) > 64<<10 {
				return nil, errors.New("reply line too long")
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if n := len(buf); n >= 2 && buf[n-2] == '\r' {
			return buf[:n-2], nil
		}
		return buf[:len(buf)-1], nil
	}
}

func (rc *rdConn) close() { _ = rc.c.Close() }
