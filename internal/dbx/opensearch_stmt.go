package dbx

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// A query tab script on OpenSearch / Elasticsearch mixes Kibana console
// blocks and SQL:
//
//	GET /logs/_search
//	{ "query": { "match": { "msg": "timeout" } } }
//
//	SELECT host, count(*) FROM logs GROUP BY host;
//
// A block starts at a line that is a method and one path ("DELETE FROM t"
// is SQL) and runs to the next block, or to a line that starts with a word
// outside a JSON body (true, false and null aside), which starts SQL. SQL
// splits on semicolons as on the other engines. "#" and "//" start line
// comments.

var osMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "HEAD": true, "PATCH": true}

// osMethodLine reports a console request line at s[i:]: a method, blanks
// and one path token to the end of the line.
func osMethodLine(s string, i int) bool {
	end := strings.IndexByte(s[i:], '\n')
	line := s[i:]
	if end >= 0 {
		line = s[i : i+end]
	}
	f := strings.Fields(line)
	return len(f) == 2 && osMethods[strings.ToUpper(f[0])]
}

// osWordLine reports a line at s[i:] that starts with a word a JSON body
// cannot hold.
func osWordLine(s string, i int) bool {
	if i >= len(s) || !unicode.IsLetter(rune(s[i])) {
		return false
	}
	j := i
	for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j]))) {
		j++
	}
	switch s[i:j] {
	case "true", "false", "null":
		return false
	}
	return true
}

func osSplit(script string) []Stmt {
	var out []Stmt
	start := 0
	emit := func(end int) {
		text := strings.TrimSpace(script[start:end])
		if text != "" && leadKeyword(EngineOpenSearch, text) != "" {
			off := start + strings.Index(script[start:end], text)
			out = append(out, Stmt{Text: text, Start: off, End: off + len(text)})
		}
	}
	rest, triple := false, false
	for i := 0; i < len(script); {
		if (i == 0 || script[i-1] == '\n') && !triple {
			j := i
			for j < len(script) && (script[j] == ' ' || script[j] == '\t') {
				j++
			}
			if osMethodLine(script, j) {
				emit(i)
				start, rest = i, true
				i = lineEnd(script, i)
				continue
			}
			if rest && osWordLine(script, j) {
				emit(i)
				start, rest = i, false
			}
		}
		if rest {
			switch {
			case strings.HasPrefix(script[i:], `"""`):
				triple = !triple
				i += 3
			case triple:
				i++
			case script[i] == '"':
				// A JSON string: one line at most.
				j := quoted(script, i, '"', true)
				if k := strings.IndexByte(script[i:j], '\n'); k >= 0 {
					j = i + k
				}
				i = j
			case script[i] == '#' || strings.HasPrefix(script[i:], "//"):
				i = lineEnd(script, i)
			default:
				i++
			}
			continue
		}
		if n := skipNonCode(EngineOpenSearch, script, i); n > i {
			i = n
			continue
		}
		if script[i] == ';' {
			emit(i)
			start = i + 1
		}
		i++
	}
	emit(len(script))
	return out
}

// osParseREST reads a console block; ok is false for SQL.
func osParseREST(stmt string) (osREST, bool) {
	stmt = strings.TrimSpace(stmt)
	first, body, _ := strings.Cut(stmt, "\n")
	if !osMethodLine(first, 0) {
		return osREST{}, false
	}
	f := strings.Fields(first)
	path := f[1]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return osREST{method: strings.ToUpper(f[0]), path: path, body: osBody(body)}, true
}

// osBody drops comment lines and turns """ strings (raw text, as the
// console writes a SQL query or a script) into JSON strings.
func osBody(body string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		b.WriteString(l)
	}
	s := b.String()
	if !strings.Contains(s, `"""`) {
		return s
	}
	var out strings.Builder
	for {
		i := strings.Index(s, `"""`)
		if i < 0 {
			break
		}
		j := strings.Index(s[i+3:], `"""`)
		if j < 0 {
			break
		}
		out.WriteString(s[:i])
		q, _ := json.Marshal(s[i+3 : i+3+j])
		out.Write(q)
		s = s[i+3+j+3:]
	}
	out.WriteString(s)
	return out.String()
}

// osReadEndpoints answer a POST without changing anything.
var osReadEndpoints = map[string]bool{
	"_search": true, "_count": true, "_msearch": true, "_mget": true,
	"_field_caps": true, "_validate": true, "_explain": true, "_analyze": true,
	"_termvectors": true, "_mtermvectors": true, "_rank_eval": true,
	"_search_shards": true, "_render": true,
}

// osRESTRead reports a block that only reads, or why it does not.
func osRESTRead(req osREST) (bool, string) {
	ep, next := req.endpoint()
	sql := ep == "_sql" || ep == "_ppl" || (ep == "_plugins" || ep == "_opendistro") && (next == "_sql" || next == "_ppl")
	switch req.method {
	case "GET", "HEAD":
		if !sql || strings.TrimSpace(req.body) == "" {
			return true, ""
		}
		// Elasticsearch takes SQL on GET too: the body is checked the same.
	case "POST":
	default:
		return false, req.method + " requests"
	}
	if sql {
		if next == "close" || next == "translate" || strings.HasSuffix(strings.SplitN(req.path, "?", 2)[0], "/close") ||
			strings.HasSuffix(strings.SplitN(req.path, "?", 2)[0], "/_explain") {
			return true, ""
		}
		v, ok := osSQLBody(req.body)
		if !ok {
			return false, "a SQL request it cannot read"
		}
		if v.Query == "" && v.Cursor != "" {
			return true, ""
		}
		if ep == "_ppl" || next == "_ppl" {
			// PPL only queries.
			return v.Query != "", "an empty PPL request"
		}
		if v.Query == "" || !IsRead(EngineOpenSearch, v.Query) {
			return false, "this SQL"
		}
		return true, ""
	}
	if osReadEndpoints[ep] {
		return true, ""
	}
	if ep == "" {
		return false, "POST " + req.path
	}
	return false, "POST " + ep
}

// osSQLBody reads the query and cursor of a SQL request the way the server
// does. Keys match exactly (encoding/json would take "QUERY" for "query"
// and let a second key overwrite the one checked), and a key given twice,
// in any case, is refused.
func osSQLBody(body string) (struct{ Query, Cursor string }, bool) {
	var v struct{ Query, Cursor string }
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &fields) != nil {
		return v, false
	}
	dec := json.NewDecoder(strings.NewReader(body))
	if _, err := dec.Token(); err != nil {
		return v, false
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return v, false
		}
		k := strings.ToLower(tok.(string))
		if seen[k] {
			return v, false
		}
		seen[k] = true
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return v, false
		}
	}
	if raw, ok := fields["query"]; ok && json.Unmarshal(raw, &v.Query) != nil {
		return v, false
	}
	if raw, ok := fields["cursor"]; ok && json.Unmarshal(raw, &v.Cursor) != nil {
		return v, false
	}
	return v, true
}

// osIsRead reports whether a block or SQL statement only reads.
func osIsRead(stmt string) bool {
	if req, ok := osParseREST(stmt); ok {
		ok, _ := osRESTRead(req)
		return ok
	}
	return sqlIsRead(EngineOpenSearch, stmt)
}

func osCheckReadOnly(stmt string) error {
	if req, ok := osParseREST(stmt); ok {
		if ok, what := osRESTRead(req); !ok {
			return fmt.Errorf("read-only connection: %s are not allowed", what)
		}
		return nil
	}
	return sqlCheckReadOnly(EngineOpenSearch, stmt)
}

// osSecretRE matches a JSON string value under a key that names a secret
// (password, hash, token, api key, access / secret / private key).
var osSecretRE = regexp.MustCompile(`(?i)("[^"]*(?:password|passwd|secret|token|api_?key|access_?key|private_?key|credentials)[^"]*"\s*:\s*|"hash"\s*:\s*)"(?:[^"\\]|\\.)*"`)

func osRedact(stmt string) string {
	if _, ok := osParseREST(stmt); ok {
		return osSecretRE.ReplaceAllString(stmt, `${1}"***"`)
	}
	return secretRE.ReplaceAllString(stripComments(EngineOpenSearch, stmt), "${1}'***'")
}
