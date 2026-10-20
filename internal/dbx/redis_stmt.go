package dbx

import (
	"fmt"
	"strconv"
	"strings"
)

// A Redis script is one command per line, quoted the way redis-cli quotes:
// "..." with backslash escapes (\n, \t, \", \\, \xHH), '...' with only \'
// escaped. Lines starting with # or // are comments.

func rdSplit(script string) []Stmt {
	var out []Stmt
	for off := 0; off < len(script); {
		end := strings.IndexByte(script[off:], '\n')
		if end < 0 {
			end = len(script)
		} else {
			end += off
		}
		line := script[off:end]
		text := strings.TrimSpace(line)
		if text != "" && !strings.HasPrefix(text, "#") && !strings.HasPrefix(text, "//") {
			s := off + strings.Index(line, text)
			out = append(out, Stmt{Text: text, Start: s, End: s + len(text)})
		}
		off = end + 1
	}
	return out
}

// rdArgs splits a command line into its arguments.
func rdArgs(line string) ([]string, error) {
	var out []string
	i := 0
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\r') {
			i++
		}
		if i >= len(line) {
			return out, nil
		}
		var b strings.Builder
		switch line[i] {
		case '"':
			i++
			for {
				if i >= len(line) {
					return nil, fmt.Errorf("unbalanced quotes")
				}
				c := line[i]
				if c == '"' {
					i++
					break
				}
				if c == '\\' && i+1 < len(line) {
					i++
					switch line[i] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					case 'b':
						b.WriteByte('\b')
					case 'a':
						b.WriteByte('\a')
					case 'x':
						if i+2 < len(line) {
							if v, err := strconv.ParseUint(line[i+1:i+3], 16, 8); err == nil {
								b.WriteByte(byte(v))
								i += 2
								break
							}
						}
						b.WriteByte('x')
					default:
						b.WriteByte(line[i])
					}
					i++
					continue
				}
				b.WriteByte(c)
				i++
			}
		case '\'':
			i++
			for {
				if i >= len(line) {
					return nil, fmt.Errorf("unbalanced quotes")
				}
				c := line[i]
				if c == '\'' {
					i++
					break
				}
				if c == '\\' && i+1 < len(line) && line[i+1] == '\'' {
					i++
					c = '\''
				}
				b.WriteByte(c)
				i++
			}
		default:
			for i < len(line) && line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
				b.WriteByte(line[i])
				i++
			}
			out = append(out, b.String())
			continue
		}
		// A closing quote must end the argument, as in redis-cli.
		if i < len(line) && line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
			return nil, fmt.Errorf("closing quote must be followed by a space")
		}
		out = append(out, b.String())
	}
}

// rdRefused are never sent: they take the connection over (MONITOR,
// SUBSCRIBE, SYNC, a RESP3 HELLO) or change it under the tab (RESET, QUIT,
// CLIENT REPLY).
var rdRefused = map[string]bool{
	"MONITOR": true, "SUBSCRIBE": true, "PSUBSCRIBE": true, "SSUBSCRIBE": true,
	"SYNC": true, "PSYNC": true, "HELLO": true, "RESET": true, "QUIT": true,
}

// rdRefuse says why a command is never sent, or "".
func rdRefuse(args []string) string {
	cmd := strings.ToUpper(args[0])
	if rdRefused[cmd] {
		return cmd + " takes the connection over; use redis-cli in a terminal"
	}
	if cmd == "CLIENT" && len(args) > 1 && strings.EqualFold(args[1], "REPLY") {
		return "CLIENT REPLY would leave the tab without replies"
	}
	return ""
}

// rdReadCmds only read. Commands with subcommands are in rdReadSubs.
var rdReadCmds = map[string]bool{}

// rdReadSubs: commands that read only with one of these subcommands.
var rdReadSubs = map[string]map[string]bool{}

func init() {
	for _, c := range strings.Fields(`
		GET MGET GETRANGE SUBSTR STRLEN GETBIT BITCOUNT BITPOS BITFIELD_RO LCS
		EXISTS TYPE TTL PTTL EXPIRETIME PEXPIRETIME KEYS SCAN RANDOMKEY DBSIZE DUMP SORT_RO
		HGET HMGET HGETALL HKEYS HVALS HLEN HEXISTS HSTRLEN HSCAN HRANDFIELD
		HTTL HPTTL HEXPIRETIME HPEXPIRETIME
		LRANGE LLEN LINDEX LPOS
		SMEMBERS SISMEMBER SMISMEMBER SCARD SRANDMEMBER SSCAN SINTER SUNION SDIFF SINTERCARD
		ZRANGE ZRANGEBYSCORE ZRANGEBYLEX ZREVRANGE ZREVRANGEBYSCORE ZREVRANGEBYLEX ZCARD ZCOUNT
		ZLEXCOUNT ZSCORE ZMSCORE ZRANK ZREVRANK ZSCAN ZRANDMEMBER ZINTER ZUNION ZDIFF ZINTERCARD
		XRANGE XREVRANGE XLEN XPENDING XREAD
		GEOPOS GEODIST GEOHASH GEORADIUS_RO GEORADIUSBYMEMBER_RO GEOSEARCH
		PFCOUNT
		INFO PING ECHO TIME LASTSAVE ROLE SELECT
		EVAL_RO EVALSHA_RO FCALL_RO
		JSON.GET JSON.MGET JSON.TYPE JSON.STRLEN JSON.OBJKEYS JSON.OBJLEN JSON.ARRLEN JSON.ARRINDEX JSON.RESP
		FT.SEARCH FT.INFO FT._LIST FT.EXPLAIN FT.PROFILE FT.TAGVALS FT.SPELLCHECK
		TS.GET TS.MGET TS.RANGE TS.REVRANGE TS.MRANGE TS.MREVRANGE TS.INFO TS.QUERYINDEX
		BF.EXISTS BF.MEXISTS BF.INFO CF.EXISTS CF.COUNT CF.INFO`) {
		rdReadCmds[c] = true
	}
	subs := map[string]string{
		"OBJECT":   "ENCODING FREQ IDLETIME REFCOUNT HELP",
		"MEMORY":   "USAGE STATS DOCTOR MALLOC-STATS HELP",
		"XINFO":    "STREAM GROUPS CONSUMERS HELP",
		"COMMAND":  "COUNT DOCS GETKEYS GETKEYSANDFLAGS INFO LIST HELP",
		"CONFIG":   "GET HELP",
		"CLIENT":   "LIST INFO GETNAME ID TRACKINGINFO GETREDIR HELP",
		"SLOWLOG":  "GET LEN HELP",
		"LATENCY":  "LATEST HISTORY DOCTOR GRAPH HISTOGRAM HELP",
		"MODULE":   "LIST HELP",
		"ACL":      "WHOAMI USERS CAT GETUSER LOG LIST DRYRUN HELP",
		"CLUSTER":  "INFO NODES SLOTS SHARDS KEYSLOT COUNTKEYSINSLOT GETKEYSINSLOT MYID MYSHARDID LINKS HELP",
		"FUNCTION": "LIST STATS DUMP HELP",
		"SCRIPT":   "EXISTS HELP",
		"PUBSUB":   "CHANNELS NUMSUB NUMPAT SHARDCHANNELS SHARDNUMSUB HELP",
	}
	for c, s := range subs {
		rdReadSubs[c] = map[string]bool{}
		for _, sub := range strings.Fields(s) {
			rdReadSubs[c][sub] = true
		}
	}
}

// rdReadArgs reports whether a command only reads, or names the command
// (and subcommand) that does not.
func rdReadArgs(args []string) (bool, string) {
	if len(args) == 0 {
		return true, ""
	}
	cmd := strings.ToUpper(args[0])
	if rdReadCmds[cmd] {
		return true, ""
	}
	if subs, ok := rdReadSubs[cmd]; ok {
		if len(args) > 1 && subs[strings.ToUpper(args[1])] {
			return true, ""
		}
		sub := ""
		if len(args) > 1 {
			sub = " " + strings.ToUpper(args[1])
		}
		return false, cmd + sub
	}
	return false, cmd
}

func rdIsRead(stmt string) bool {
	args, err := rdArgs(stmt)
	if err != nil {
		return false
	}
	ok, _ := rdReadArgs(args)
	return ok
}

func rdCheckReadOnly(stmt string) error {
	args, err := rdArgs(stmt)
	if err != nil {
		return fmt.Errorf("read-only connection: %v", err)
	}
	if ok, what := rdReadArgs(args); !ok {
		return fmt.Errorf("read-only connection: %s is not a read command", what)
	}
	return nil
}

// rdRedact hides passwords: AUTH, HELLO ... AUTH, CONFIG SET requirepass /
// masterauth, ACL SETUSER rules (>pw, <pw, #hash, !hash), MIGRATE ...
// AUTH / AUTH2. A line it cannot parse is kept to its command name.
func rdRedact(stmt string) string {
	args, err := rdArgs(stmt)
	if err != nil || len(args) == 0 {
		if f := strings.Fields(stmt); len(f) > 0 {
			return f[0] + " ***"
		}
		return stmt
	}
	cmd := strings.ToUpper(args[0])
	hide := map[int]bool{}
	switch cmd {
	case "AUTH":
		for i := 1; i < len(args); i++ {
			if i == len(args)-1 {
				hide[i] = true
			}
		}
	case "CONFIG":
		for i := 2; i+1 < len(args); i += 2 {
			switch strings.ToLower(args[i]) {
			case "requirepass", "masterauth", "masteruser", "tls-key-file-pass", "tls-client-key-file-pass":
				hide[i+1] = true
			}
		}
	case "ACL":
		for i := 2; i < len(args); i++ {
			if a := args[i]; a != "" && strings.ContainsRune("><#!", rune(a[0])) {
				hide[i] = true
			}
		}
	}
	// HELLO / MIGRATE / anything with AUTH user pass or AUTH2.
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "AUTH":
			if cmd == "HELLO" {
				hide[i+2] = true
			} else if cmd != "AUTH" {
				hide[i+1] = true
			}
		case "AUTH2":
			hide[i+2] = true
		}
	}
	if len(hide) == 0 {
		return stmt
	}
	parts := make([]string, len(args))
	for i, a := range args {
		if hide[i] {
			parts[i] = "***"
		} else {
			parts[i] = rdQuote(a)
		}
	}
	return strings.Join(parts, " ")
}

// rdQuote quotes an argument redis-cli style when it needs it.
func rdQuote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\r\n\"'\\") && isPrintableASCII(a) {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(a); i++ {
		switch c := a[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c > 0x7e {
				fmt.Fprintf(&b, `\x%02x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
