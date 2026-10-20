package dbx

import (
	"fmt"
	"regexp"
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
	switch engine {
	case EngineOpenSearch:
		return osSplit(script)
	case EngineRedis:
		return rdSplit(script)
	}
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
		// MySQL takes "--" as a comment only before whitespace: "--1" is
		// minus minus one, and what follows is code.
		if engine == EngineMySQL && i+2 < len(s) && !unicode.IsSpace(rune(s[i+2])) {
			return i
		}
		return lineEnd(s, i)
	case c == '#' && (engine == EngineMySQL || engine == EngineClickHouse || engine == EngineOpenSearch):
		return lineEnd(s, i)
	case c == '/' && strings.HasPrefix(s[i:], "//") && engine == EngineOpenSearch:
		return lineEnd(s, i)
	case c == '/' && strings.HasPrefix(s[i:], "/*!") && engine == EngineMySQL:
		// An executable comment is code to MySQL ("SELECT 1 /*!INTO
		// OUTFILE ...*/"), so it is not skipped like a comment.
		return i
	case c == '/' && strings.HasPrefix(s[i:], "/*"):
		// Postgres and ClickHouse nest block comments: "/* a /* b */ c */"
		// is one comment, and ending it at the first "*/" would read c as
		// code.
		depth := 1
		for j := i + 2; j+1 < len(s); j++ {
			switch {
			case s[j] == '*' && s[j+1] == '/':
				if depth--; depth == 0 {
					return j + 2
				}
				j++
			case s[j] == '/' && s[j+1] == '*' && (engine == EnginePostgres || engine == EngineClickHouse):
				depth++
				j++
			}
		}
		return len(s)
	case c == '\'':
		// MySQL and ClickHouse strings take backslash escapes; Postgres
		// only in E'...'.
		backslash := engine == EngineMySQL || engine == EngineClickHouse || (i > 0 && (s[i-1] == 'E' || s[i-1] == 'e'))
		return quoted(s, i, '\'', backslash)
	case c == '"':
		return quoted(s, i, '"', engine == EngineMySQL || engine == EngineClickHouse)
	case c == '`' && (engine == EngineMySQL || engine == EngineClickHouse || engine == EngineOpenSearch):
		return quoted(s, i, '`', engine == EngineClickHouse)
	case c == '$' && (engine == EnginePostgres || engine == EngineClickHouse):
		// Postgres dollar quotes; ClickHouse heredocs ($$...$$, $tag$...$tag$).
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

// readLead reports a read keyword for engine: ClickHouse adds EXISTS
// (EXISTS TABLE t).
func readLead(engine, k string) bool {
	return readKeywords[k] || (engine == EngineClickHouse && k == "EXISTS")
}

// IsRead reports whether a statement only reads: it starts with a read
// keyword and has none of the forms that write under one - a data-changing
// CTE (WITH d AS (DELETE ...)), EXPLAIN ANALYZE (which runs the statement),
// or SELECT ... INTO (a new table on Postgres, a file or variables on
// MySQL). On OpenSearch a console block reads when its method and endpoint
// do (osRESTRead).
func IsRead(engine, stmt string) bool {
	switch engine {
	case EngineOpenSearch:
		return osIsRead(stmt)
	case EngineRedis:
		return rdIsRead(stmt)
	}
	return sqlIsRead(engine, stmt)
}

func sqlIsRead(engine, stmt string) bool {
	lead := leadKeyword(engine, stmt)
	if !readLead(engine, lead) {
		return false
	}
	words := codeWords(engine, stmt)
	switch {
	case words["INTO"]:
		return false
	case lead == "WITH" && (words["INSERT"] || words["UPDATE"] || words["DELETE"] || words["MERGE"]):
		return false
	case lead == "EXPLAIN" && words["ANALYZE"]:
		return false
	}
	return true
}

// codeWords is the set of upper-cased words in a statement's code (strings,
// quoted identifiers and comments left out). An identifier like
// update_time is one word, not UPDATE.
func codeWords(engine, stmt string) map[string]bool {
	out := map[string]bool{}
	code := strings.ToUpper(codeOnly(engine, stmt))
	start := -1
	for i, r := range code {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out[code[start:i]] = true
			start = -1
		}
	}
	if start >= 0 {
		out[code[start:]] = true
	}
	return out
}

// returnsRows: statements a MySQL session runs as a query (rows) rather
// than an exec (affected count). CALL may return rows; its affected count
// is lost, which is what the mysql CLI shows for it too.
func returnsRows(engine, stmt string) bool {
	k := leadKeyword(engine, stmt)
	return readLead(engine, k) || k == "CALL" || k == "CHECK" || k == "ANALYZE" ||
		k == "OPTIMIZE" || k == "REPAIR" || k == "HELP"
}

// CheckReadOnly refuses a statement read-only mode must not run. It is one
// of three guards: the session is also opened read-only and every
// statement runs in a READ ONLY transaction that is rolled back. The
// keyword check is what stops MySQL DDL, which commits implicitly and so
// escapes the transaction. OpenSearch has neither session nor
// transaction: this check is all there is.
func CheckReadOnly(engine, stmt string) error {
	switch engine {
	case EngineOpenSearch:
		return osCheckReadOnly(stmt)
	case EngineRedis:
		return rdCheckReadOnly(stmt)
	}
	return sqlCheckReadOnly(engine, stmt)
}

func sqlCheckReadOnly(engine, stmt string) error {
	k := leadKeyword(engine, stmt)
	if !readLead(engine, k) {
		return fmt.Errorf("read-only connection: %s statements are not allowed", strings.ToLower(orWord(k)))
	}
	if !sqlIsRead(engine, stmt) {
		return fmt.Errorf("read-only connection: this %s writes (a data-changing WITH, EXPLAIN ANALYZE or SELECT ... INTO)", strings.ToLower(k))
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

// IsTxControl reports a statement that opens or ends a transaction
// (BEGIN, START, COMMIT, END, ROLLBACK, SAVEPOINT, RELEASE, ABORT).
func IsTxControl(engine, stmt string) bool {
	switch leadKeyword(engine, stmt) {
	case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE", "ABORT":
		return true
	}
	return false
}

// secretRE matches a quoted secret after PASSWORD (CREATE / ALTER ROLE,
// SET PASSWORD [FOR user] =), IDENTIFIED [WITH plugin] BY and REPLACE (the
// old password of a MySQL ALTER USER). Quotes may be doubled or, on MySQL,
// backslash-escaped.
var secretRE = regexp.MustCompile(`(?i)(\bPASSWORD\s+FOR\s+\S+?\s*=\s*|\bPASSWORD\s*(?:=\s*)?|\bIDENTIFIED\s+(?:WITH\s+\S+\s+)?BY\s+|\bREPLACE\s+)('(?:[^'\\]|\\.|'')*'|"(?:[^"\\]|\\.|"")*")`)

// RedactSecrets hides quoted passwords in a statement before it is stored
// (audit log, LLM activity). Comments go first - "IDENTIFIED /*x*/ BY
// 'pw'" would otherwise slip past the patterns - and are worth nothing in
// a stored statement anyway.
func RedactSecrets(engine, stmt string) string {
	switch engine {
	case EngineOpenSearch:
		return osRedact(stmt)
	case EngineRedis:
		return rdRedact(stmt)
	}
	return secretRE.ReplaceAllString(stripComments(engine, stmt), "${1}'***'")
}

// stripComments replaces each comment with one space, leaving strings and
// quoted identifiers (and MySQL executable comments, which are code) as
// they are.
func stripComments(engine, s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if n := skipNonCode(engine, s, i); n > i {
			if c := s[i]; c == '-' || c == '#' || c == '/' {
				b.WriteByte(' ')
			} else {
				b.WriteString(s[i:n])
			}
			i = n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
