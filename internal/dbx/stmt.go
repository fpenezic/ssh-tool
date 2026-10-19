package dbx

import (
	"fmt"
	"strings"
	"unicode"
)

// Stmt is one statement of a script: its text (no trailing semicolon) and
// its byte range in the script, so the editor can find the statement under
// the cursor.
type Stmt struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Split cuts a script into statements on top-level semicolons. Quotes,
// quoted identifiers, comments and (Postgres) dollar-quoted bodies are
// skipped, so a semicolon inside a function body or a string does not
// split. Empty statements (only whitespace / comments) are dropped. MySQL's
// DELIMITER client command is not supported: a stored routine body has to
// go in as one statement on its own.
func Split(engine, script string) []Stmt {
	var out []Stmt
	start := 0
	emit := func(end int) {
		text := strings.TrimSpace(script[start:end])
		if text != "" && leadKeyword(engine, text) != "" {
			off := start + strings.Index(script[start:end], text)
			out = append(out, Stmt{Text: text, Start: off, End: off + len(text)})
		}
	}
	for i := 0; i < len(script); {
		if n := skipNonCode(engine, script, i); n > i {
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

// StatementAt returns the statement whose range holds offset (a byte
// offset into script), or the nearest one before it when offset sits
// between statements. ok is false for a script with no statements.
func StatementAt(engine, script string, offset int) (Stmt, bool) {
	stmts := Split(engine, script)
	if len(stmts) == 0 {
		return Stmt{}, false
	}
	best := stmts[0]
	for _, s := range stmts {
		if s.Start <= offset {
			best = s
		}
		if offset >= s.Start && offset <= s.End {
			return s, true
		}
	}
	return best, true
}

// skipNonCode returns the index after a string, quoted identifier, comment
// or dollar-quoted body starting at i, or i when none starts there. An
// unterminated one runs to the end of the script.
func skipNonCode(engine, s string, i int) int {
	c := s[i]
	switch {
	case c == '-' && strings.HasPrefix(s[i:], "--"):
		return lineEnd(s, i)
	case c == '#' && engine == EngineMySQL:
		return lineEnd(s, i)
	case c == '/' && strings.HasPrefix(s[i:], "/*"):
		if j := strings.Index(s[i+2:], "*/"); j >= 0 {
			return i + 2 + j + 2
		}
		return len(s)
	case c == '\'':
		// MySQL strings take backslash escapes; Postgres only in E'...'.
		backslash := engine == EngineMySQL || (i > 0 && (s[i-1] == 'E' || s[i-1] == 'e'))
		return quoted(s, i, '\'', backslash)
	case c == '"':
		return quoted(s, i, '"', engine == EngineMySQL)
	case c == '`' && engine == EngineMySQL:
		return quoted(s, i, '`', false)
	case c == '$' && engine == EnginePostgres:
		if tag, ok := dollarTag(s, i); ok {
			if j := strings.Index(s[i+len(tag):], tag); j >= 0 {
				return i + len(tag) + j + len(tag)
			}
			return len(s)
		}
	}
	return i
}

func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// quoted skips a quoted run; a doubled quote is an escaped quote.
func quoted(s string, i int, q byte, backslash bool) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if backslash {
				j++
			}
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// dollarTag reads a Postgres dollar-quote opener ($$ or $tag$) at i. A $
// after an identifier character is a positional parameter or part of a
// name ($1, a$b), not a quote.
func dollarTag(s string, i int) (string, bool) {
	if i > 0 && (isIdent(rune(s[i-1]))) {
		return "", false
	}
	for j := i + 1; j < len(s); j++ {
		c := rune(s[j])
		if c == '$' {
			return s[i : j+1], true
		}
		if !isIdent(c) || (j == i+1 && unicode.IsDigit(c)) {
			return "", false
		}
	}
	return "", false
}

func isIdent(c rune) bool {
	return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c)
}

// leadKeyword is the first keyword of a statement, upper-cased, past
// comments, whitespace and opening parentheses ("(SELECT 1)"). "" for a
// statement that is only comments.
func leadKeyword(engine, stmt string) string {
	i := 0
	for i < len(stmt) {
		c := stmt[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '(' {
			i++
			continue
		}
		if c == '-' || c == '#' || c == '/' {
			if n := skipNonCode(engine, stmt, i); n > i {
				i = n
				continue
			}
		}
		break
	}
	j := i
	for j < len(stmt) && (stmt[j] == '_' || unicode.IsLetter(rune(stmt[j]))) {
		j++
	}
	return strings.ToUpper(stmt[i:j])
}

// readKeywords start statements that only read. Everything else is a
// write as far as read-only mode is concerned.
var readKeywords = map[string]bool{
	"SELECT": true, "WITH": true, "SHOW": true, "EXPLAIN": true,
	"VALUES": true, "TABLE": true, "DESCRIBE": true, "DESC": true,
}

// IsRead reports whether a statement starts with a read-only keyword.
func IsRead(engine, stmt string) bool {
	return readKeywords[leadKeyword(engine, stmt)]
}

// returnsRows: statements a MySQL session runs as a query (rows) rather
// than an exec (affected count). CALL may return rows; its affected count
// is lost, which is what the mysql CLI shows for it too.
func returnsRows(engine, stmt string) bool {
	k := leadKeyword(engine, stmt)
	return readKeywords[k] || k == "CALL" || k == "CHECK" || k == "ANALYZE" ||
		k == "OPTIMIZE" || k == "REPAIR" || k == "HELP"
}

// CheckReadOnly refuses a statement read-only mode must not run. It is one
// of three guards: the session is also opened read-only and every
// statement runs in a READ ONLY transaction that is rolled back. The
// keyword check is what stops MySQL DDL, which commits implicitly and so
// escapes the transaction.
func CheckReadOnly(engine, stmt string) error {
	k := leadKeyword(engine, stmt)
	if !readKeywords[k] {
		return fmt.Errorf("read-only connection: %s statements are not allowed", strings.ToLower(orWord(k)))
	}
	if engine == EngineMySQL {
		// SELECT ... INTO OUTFILE / DUMPFILE writes a file on the server,
		// which no transaction mode stops.
		code := strings.ToUpper(codeOnly(engine, stmt))
		if strings.Contains(code, "OUTFILE") || strings.Contains(code, "DUMPFILE") {
			return fmt.Errorf("read-only connection: SELECT ... INTO OUTFILE / DUMPFILE is not allowed")
		}
	}
	return nil
}

func orWord(k string) string {
	if k == "" {
		return "these"
	}
	return k
}

// codeOnly blanks out strings, quoted identifiers and comments, so a
// keyword search does not match inside them.
func codeOnly(engine, s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if n := skipNonCode(engine, s, i); n > i {
			b.WriteByte(' ')
			i = n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
