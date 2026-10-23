package dbx

import (
	"fmt"
	"regexp"
	"strconv"
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
//
// Oracle and SQL Server also end a statement at a line holding only "/"
// (SQL*Plus) or GO (sqlcmd), and a block - PL/SQL, a stored routine, a
// T-SQL DECLARE / BEGIN / IF batch - runs to that line or the end of the
// script, semicolons and all (see blockStmt).
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
	blockFrom, block := -1, false
	batch := 0 // where the statements since the last GO start in out
	for i := 0; i < len(script); {
		if i == 0 || script[i-1] == '\n' {
			if n, count, ok := terminatorLine(engine, script, i); ok && goFits(len(out)+pending(engine, script[start:i]), len(out)-batch+pending(engine, script[start:i]), count) {
				emit(i)
				// GO n runs the batch n times, as sqlcmd does.
				one := out[batch:]
				for k := 1; k < count; k++ {
					out = append(out, one...)
				}
				start, i, batch = n, n, len(out)
				continue
			}
		}
		if n := skipNonCode(engine, script, i); n > i {
			i = n
			continue
		}
		if script[i] == ';' {
			if blockFrom != start {
				blockFrom, block = start, blockStmt(engine, script[start:i])
			}
			if !block {
				emit(i)
				start = i + 1
			}
		}
		i++
	}
	emit(len(script))
	return out
}

// maxGoStatements bounds the whole script once GO's repeat counts are
// expanded: each batch is copied that many times into the statement list.
// A count that would go past it is not cut short: the line is then no
// separator, and the server refuses the GO in the batch.
const maxGoStatements = 100000

// goFits: the list holds total statements once the one before the GO line
// is in, the last batch of them; repeating it count times stays within the
// bound. Divided,
// not multiplied, so nothing overflows on 32-bit.
func goFits(total, batch, count int) bool {
	if count <= 1 || batch == 0 {
		return true
	}
	return total <= maxGoStatements && batch <= (maxGoStatements-total)/(count-1)
}

// pending is 1 when text still holds a statement to emit.
func pending(engine, text string) int {
	if t := strings.TrimSpace(text); t != "" && leadKeyword(engine, t) != "" {
		return 1
	}
	return 0
}

// terminatorLine reports a line at i that ends a statement or batch: "/"
// on Oracle, GO on SQL Server, which may carry a repeat count (count, at
// least 1) and a -- comment. n is where the next line starts.
func terminatorLine(engine, s string, i int) (n, count int, ok bool) {
	if engine != EngineOracle && engine != EngineMSSQL {
		return i, 0, false
	}
	n = lineEnd(s, i)
	line := strings.TrimSpace(s[i:n])
	if engine == EngineOracle {
		return n, 1, line == "/"
	}
	if j := strings.Index(line, "--"); j >= 0 {
		line = line[:j]
	}
	f := strings.Fields(line)
	if len(f) == 0 || len(f) > 2 || !strings.EqualFold(f[0], "GO") {
		return n, 0, false
	}
	count = 1
	if len(f) == 2 {
		c, err := strconv.Atoi(f[1])
		if err != nil || c < 1 || c > maxGoStatements {
			return n, 0, false
		}
		count = c
	}
	return n, count, true
}

// blockStmt reports a statement whose own semicolons do not end it: a
// PL/SQL block, stored unit or WITH FUNCTION query on Oracle; on SQL Server a routine or
// trigger body, a BEGIN ... END block, an IF / WHILE, and a DECLARE (its
// variables live as long as the batch, so what follows has to go with
// it). stmt is the statement so far, from its start.
func blockStmt(engine, stmt string) bool {
	w := leadWords(engine, stmt, 5)
	at := func(i int) string {
		if i < len(w) {
			return w[i]
		}
		return ""
	}
	switch engine {
	case EngineOracle:
		switch at(0) {
		case "DECLARE", "BEGIN":
			return true
		case "WITH":
			// WITH FUNCTION / PROCEDURE: PL/SQL in a query, ended by "/"
			// like any block (SQL*Plus does the same).
			return at(1) == "FUNCTION" || at(1) == "PROCEDURE"
		case "CREATE":
			i := 1
			if at(i) == "OR" && at(i+1) == "REPLACE" {
				i += 2
			}
			if at(i) == "EDITIONABLE" || at(i) == "NONEDITIONABLE" {
				i++
			}
			switch at(i) {
			case "PROCEDURE", "FUNCTION", "PACKAGE", "TRIGGER", "TYPE":
				return true
			}
		}
	case EngineMSSQL:
		switch at(0) {
		case "DECLARE", "IF", "WHILE":
			return true
		case "BEGIN":
			switch at(1) {
			case "TRAN", "TRANSACTION", "DISTRIBUTED":
				return false
			}
			return true
		case "CREATE", "ALTER":
			i := 1
			if at(0) == "CREATE" && at(i) == "OR" && at(i+1) == "ALTER" {
				i += 2
			}
			switch at(i) {
			case "PROCEDURE", "PROC", "FUNCTION", "TRIGGER":
				return true
			}
		}
	}
	return false
}

// msVarSigil: on SQL Server a word may start with @ (a variable, @@ a
// function) or # (a temp table); it stays on the word, so @rollback is not
// ROLLBACK.
func msVarSigil(engine string, r rune) bool {
	return engine == EngineMSSQL && (r == '@' || r == '#')
}

// msRoutine reports a CREATE / ALTER of a procedure, function or trigger:
// its body is stored, not run.
func msRoutine(engine, stmt string) bool {
	w := leadWords(engine, stmt, 4)
	if len(w) < 2 || (w[0] != "CREATE" && w[0] != "ALTER") {
		return false
	}
	i := 1
	if w[0] == "CREATE" && len(w) > 3 && w[1] == "OR" && w[2] == "ALTER" {
		i = 3
	}
	switch w[i] {
	case "PROCEDURE", "PROC", "FUNCTION", "TRIGGER":
		return true
	}
	return false
}

// leadWords is a statement's first n words of code, upper-cased; n < 0
// takes them all.
func leadWords(engine, stmt string, n int) []string {
	var out []string
	code := codeOnly(engine, stmt)
	start := -1
	for i := 0; i <= len(code) && (n < 0 || len(out) < n); i++ {
		if i < len(code) && (code[i] == '_' || unicode.IsLetter(rune(code[i])) || unicode.IsDigit(rune(code[i])) || (start < 0 && msVarSigil(engine, rune(code[i])))) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, strings.ToUpper(code[start:i]))
			start = -1
		}
	}
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
		// Postgres, ClickHouse and SQL Server nest block comments: "/* a /*
		// b */ c */" is one comment, and ending it at the first "*/" would
		// read c as code.
		depth := 1
		for j := i + 2; j+1 < len(s); j++ {
			switch {
			case s[j] == '*' && s[j+1] == '/':
				if depth--; depth == 0 {
					return j + 2
				}
				j++
			case s[j] == '/' && s[j+1] == '*' && (engine == EnginePostgres || engine == EngineClickHouse || engine == EngineMSSQL):
				depth++
				j++
			}
		}
		return len(s)
	case c == '\'':
		if engine == EngineOracle {
			if n, ok := oraQQuote(s, i); ok {
				return n
			}
		}
		// MySQL and ClickHouse strings take backslash escapes; Postgres
		// only in E'...'. Oracle and SQL Server never.
		sqlStd := engine == EngineOracle || engine == EngineMSSQL
		backslash := engine == EngineMySQL || engine == EngineClickHouse || (!sqlStd && i > 0 && (s[i-1] == 'E' || s[i-1] == 'e'))
		return quoted(s, i, '\'', backslash)
	case c == '[' && engine == EngineMSSQL:
		return quoted(s, i, ']', false)
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

// oraQQuote reads Oracle's alternative quoting at the quote i: q'[...]',
// q'{...}', q'<...>', q'(...)' or q'X...X' for another character, also
// after N (nq'...'). n is the index after the closing quote.
func oraQQuote(s string, i int) (n int, ok bool) {
	if i == 0 || (s[i-1] != 'q' && s[i-1] != 'Q') || i+1 >= len(s) {
		return i, false
	}
	p := i - 2 // what is before the q
	if p >= 0 && (s[p] == 'n' || s[p] == 'N') {
		p--
	}
	if p >= 0 && isIdent(rune(s[p])) {
		return i, false
	}
	closer := s[i+1]
	switch closer {
	case '[':
		closer = ']'
	case '{':
		closer = '}'
	case '<':
		closer = '>'
	case '(':
		closer = ')'
	case ' ', '\t', '\n', '\r':
		return i, false
	}
	if j := strings.Index(s[i+2:], string(closer)+"'"); j >= 0 {
		return i + 2 + j + 2, true
	}
	return len(s), true
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
// (EXISTS TABLE t). Oracle and SQL Server read with SELECT and WITH only
// (their EXPLAIN PLAN / SHOWPLAN forms write a plan table or change the
// session).
func readLead(engine, k string) bool {
	if engine == EngineOracle || engine == EngineMSSQL {
		return k == "SELECT" || k == "WITH"
	}
	return readKeywords[k] || (engine == EngineClickHouse && k == "EXISTS")
}

// msWriteWords can not appear in a read on SQL Server. T-SQL needs no
// semicolon between statements, so "SELECT 1 DELETE FROM t" is two
// statements in one; the transaction a read-only run is rolled back in
// does not stop EXEC of a procedure that commits, or OPENQUERY running on
// a linked server.
var msWriteWords = []string{
	"INSERT", "UPDATE", "DELETE", "MERGE", "TRUNCATE", "DROP", "CREATE", "ALTER",
	"EXEC", "EXECUTE", "GRANT", "REVOKE", "DENY", "BACKUP", "RESTORE", "DBCC",
	"KILL", "SHUTDOWN", "RECONFIGURE", "BULK", "INTO", "OPENQUERY", "OPENROWSET",
	"OPENDATASOURCE", "WRITETEXT", "UPDATETEXT", "BEGIN", "COMMIT", "ROLLBACK",
	"SAVE", "USE", "SET",
}

// msWriteWord is the first of msWriteWords in a SQL Server statement's
// code, or "".
func msWriteWord(words map[string]bool) string {
	for _, w := range msWriteWords {
		if words[w] {
			return w
		}
	}
	return ""
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
	case engine == EngineOracle && lead == "WITH" && (words["FUNCTION"] || words["PROCEDURE"]):
		// WITH FUNCTION: PL/SQL in the query, which may be an autonomous
		// transaction.
		return false
	case engine == EngineMSSQL && msWriteWord(words) != "":
		return false
	case engine == EngineMSSQL && words["NEXT"] && words["VALUE"],
		engine == EngineOracle && words["NEXTVAL"]:
		// A sequence moves on in a read, and no rollback takes it back.
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
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || (start < 0 && msVarSigil(engine, r)) {
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
	if engine == EngineMSSQL {
		if w := msWriteWord(codeWords(engine, stmt)); w != "" {
			return fmt.Errorf("read-only connection: %s is not allowed (T-SQL runs a second statement without a semicolon)", w)
		}
	}
	if w := codeWords(engine, stmt); (engine == EngineMSSQL && w["NEXT"] && w["VALUE"]) || (engine == EngineOracle && w["NEXTVAL"]) {
		return fmt.Errorf("read-only connection: taking a sequence's next value is a write no rollback undoes")
	}
	if engine == EngineOracle && k == "WITH" && !sqlIsRead(engine, stmt) {
		if w := codeWords(engine, stmt); w["FUNCTION"] || w["PROCEDURE"] {
			return fmt.Errorf("read-only connection: WITH FUNCTION / PROCEDURE (PL/SQL in a query) is not allowed")
		}
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
// (BEGIN, START, COMMIT, END, ROLLBACK, SAVEPOINT, RELEASE, ABORT). On
// Oracle BEGIN opens a PL/SQL block. On SQL Server only BEGIN TRAN
// counts, and anywhere in the statement: a DECLARE batch runs as one and
// can open a transaction half way (a routine's body only defines one).
func IsTxControl(engine, stmt string) bool {
	w := leadWords(engine, stmt, 2)
	second := ""
	if len(w) > 1 {
		second = w[1]
	}
	switch engine {
	case EngineOracle:
		switch leadKeyword(engine, stmt) {
		case "COMMIT", "ROLLBACK", "SAVEPOINT":
			return true
		case "SET":
			return second == "TRANSACTION"
		}
		return false
	case EngineMSSQL:
		words := w
		if blockStmt(engine, stmt) && !msRoutine(engine, stmt) {
			words = leadWords(engine, stmt, -1)
		}
		for i, k := range words {
			switch k {
			case "COMMIT", "ROLLBACK", "SAVE":
				return true
			case "BEGIN":
				if i+1 < len(words) {
					switch words[i+1] {
					case "TRAN", "TRANSACTION", "DISTRIBUTED":
						return true
					}
				}
			}
		}
		return false
	}
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
var secretRE = regexp.MustCompile(`(?i)(\bPASSWORD\s+FOR\s+\S+?\s*=\s*|\b(?:OLD_)?PASSWORD\s*(?:=\s*)?|\bIDENTIFIED\s+(?:WITH\s+\S+\s+)?BY\s+|\bREPLACE\s+)(N?'(?:[^'\\]|\\.|'')*'|"(?:[^"\\]|\\.|"")*")`)

// Oracle passwords are usually unquoted: IDENTIFIED BY secret [REPLACE
// old], IDENTIFIED BY VALUES 'hash'.
var (
	oraSecretRE  = regexp.MustCompile(`(?i)(\bIDENTIFIED\s+BY\s+(?:VALUES\s+)?)('(?:[^']|'')*'|"(?:[^"]|"")*"|[^\s;'"]+)`)
	oraReplaceRE = regexp.MustCompile(`(?i)(\bIDENTIFIED\s+BY\s+(?:VALUES\s+)?'\*\*\*'\s+REPLACE\s+)('(?:[^']|'')*'|"(?:[^"]|"")*"|[^\s;'"]+)`)
)

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
	out := secretRE.ReplaceAllString(stripComments(engine, stmt), "${1}'***'")
	if engine == EngineOracle {
		out = oraSecretRE.ReplaceAllString(out, "${1}'***'")
		out = oraReplaceRE.ReplaceAllString(out, "${1}'***'")
	}
	return out
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
