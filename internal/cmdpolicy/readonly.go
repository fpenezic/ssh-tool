// Package cmdpolicy decides whether a shell command line only reads state.
//
// It parses the line with a real shell parser (mvdan.cc/sh) instead of
// splitting on operator characters, so the things a read-only command line
// routinely contains - pipes, `2>/dev/null`, `2>&1`, `$(...)`, `cd dir &&`,
// a `for` loop over hosts - are understood rather than rejected, and the
// things that write are found wherever they hide: `sed -i`, `find -delete`,
// `sort -o`, `awk '{system(...)}'`, `git -c core.pager=...`, a redirect into
// a file inside a command substitution.
//
// Every simple command anywhere in the tree (pipelines, lists, loops,
// substitutions, xargs/sudo/timeout wrappers) must pass. Anything the
// classifier does not recognise is NOT read-only; the caller decides what that
// means (prompt, refuse).
package cmdpolicy

import (
	"fmt"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Options widens or narrows the built-in rules.
type Options struct {
	// Extra names commands the caller trusts as read-only with any arguments.
	// They never override the structural checks (redirects, background, ...).
	Extra []string
	// AllowSudo lets `sudo` / `doas` wrap a command that is itself read-only.
	AllowSudo bool
}

// Verdict is the classification of one command line.
type Verdict struct {
	ReadOnly bool
	// Reason names the first construct that made the line not read-only.
	// Empty when ReadOnly.
	Reason string
}

// Classify parses command and reports whether every part of it only reads.
func Classify(command string, opt Options) Verdict {
	command = strings.TrimSpace(command)
	if command == "" {
		return Verdict{Reason: "empty command"}
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return Verdict{Reason: "does not parse as a shell command: " + err.Error()}
	}
	c := checker{opt: opt, extra: map[string]bool{}, vars: map[string]string{}, safeVars: map[string]bool{}}
	for _, e := range opt.Extra {
		if e = strings.TrimSpace(e); e != "" {
			c.extra[e] = true
		}
	}
	syntax.Walk(f, c.visit)
	if c.err != nil {
		return Verdict{Reason: c.err.Error()}
	}
	return Verdict{ReadOnly: true}
}

type checker struct {
	opt   Options
	extra map[string]bool
	err   error
	// vars holds variables assigned a fixed value earlier in the same line
	// (`f=/var/log/x; tail $f`), substituted when classifying later words.
	vars map[string]string
	// safeVars are variables whose value is unknown but never starts with
	// '-' (a for-loop over fixed words or absolute globs), so they cannot
	// smuggle a flag in.
	safeVars map[string]bool
}

// systemVars are set by login/sshd and hold paths or names, never flags.
var systemVars = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "PWD": true, "HOSTNAME": true,
	"SHELL": true, "TMPDIR": true,
}

func (c *checker) fail(format string, a ...any) bool {
	if c.err == nil {
		c.err = fmt.Errorf(format, a...)
	}
	return false
}

// visit is the syntax.Walk callback. Returning false stops descent into the
// node; once an error is recorded the walk stops everywhere.
func (c *checker) visit(n syntax.Node) bool {
	if c.err != nil {
		return false
	}
	switch x := n.(type) {
	case *syntax.Stmt:
		if x.Background {
			return c.fail("runs a command in the background")
		}
		if x.Coprocess {
			return c.fail("starts a coprocess")
		}
	case *syntax.Redirect:
		return c.checkRedirect(x)
	case *syntax.ProcSubst:
		if x.Op != syntax.CmdIn {
			return c.fail("output process substitution >(...) feeds a command")
		}
	case *syntax.FuncDecl:
		return c.fail("defines a function")
	case *syntax.CoprocClause:
		return c.fail("starts a coprocess")
	case *syntax.TestDecl:
		return c.fail("declares a test")
	case *syntax.DeclClause:
		for _, a := range x.Args {
			if a.Name != nil && dangerousVar(a.Name.Value) {
				return c.fail("sets %s, which changes what later commands run", a.Name.Value)
			}
		}
	case *syntax.ForClause:
		if it, ok := x.Loop.(*syntax.WordIter); ok && it.Name != nil {
			delete(c.vars, it.Name.Value)
			safe := len(it.Items) > 0
			for _, w := range it.Items {
				if !c.word(w).nonFlag {
					safe = false
				}
			}
			c.safeVars[it.Name.Value] = safe
		}
	case *syntax.CallExpr:
		return c.checkCall(x)
	}
	return true
}

// devNull lists the only files a redirect may write to.
var devNull = map[string]bool{"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true}

func (c *checker) checkRedirect(r *syntax.Redirect) bool {
	switch r.Op {
	case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return true
	case syntax.DplIn, syntax.DplOut:
		// 2>&1, >&2, 3<&0, 2>&- : fd duplication. `>&file` is bash for
		// "stdout and stderr into file", so the target must be an fd.
		t := c.word(r.Word)
		if t.ok && (t.s == "-" || isDigits(t.s)) {
			return true
		}
		return c.fail("redirect %s%s writes a file", r.Op, wordText(r.Word))
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
		t := c.word(r.Word)
		if t.ok && devNull[t.s] {
			return true
		}
		return c.fail("redirect %s %s writes a file", r.Op, wordText(r.Word))
	}
	return c.fail("redirect %s opens a file for writing", r.Op)
}

// safeEnv are the variable names a `VAR=value cmd` prefix may set. PATH,
// LD_PRELOAD, PAGER-alikes and friends change WHAT runs, so they are out.
var safeEnv = map[string]bool{
	"LANG": true, "LANGUAGE": true, "TZ": true, "COLUMNS": true, "LINES": true,
	"TERM": true, "NO_COLOR": true, "SYSTEMD_COLORS": true, "SYSTEMD_PAGER": true,
	"SYSTEMD_LESS": true,
}

func safeEnvName(name string) bool {
	return safeEnv[name] || strings.HasPrefix(name, "LC_")
}

// dangerousVar names variables that make a later, otherwise read-only command
// run something else: the search path, loader, shell startup and trace hooks,
// and the pager/editor/helper variables tools exec.
func dangerousVar(name string) bool {
	switch name {
	case "PATH", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "PS1", "PS2", "PS3", "PS4",
		"PROMPT_COMMAND", "EDITOR", "VISUAL", "LESSOPEN", "LESSCLOSE", "BROWSER",
		"SSH_ASKPASS", "NODE_OPTIONS":
		return true
	}
	for _, p := range []string{"LD_", "DYLD_", "GIT_", "BASH_FUNC", "PYTHON", "PERL5", "RUBY"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.HasSuffix(name, "PAGER")
}

func (c *checker) checkCall(x *syntax.CallExpr) bool {
	if len(x.Args) == 0 {
		// Plain `f=/var/log/x` in a one-shot shell only sets a variable that
		// dies with the process - unless it steers what later commands run.
		for _, a := range x.Assigns {
			if a.Name == nil {
				continue
			}
			if dangerousVar(a.Name.Value) {
				return c.fail("sets %s, which changes what later commands run", a.Name.Value)
			}
			name := a.Name.Value
			delete(c.vars, name)
			delete(c.safeVars, name)
			if a.Array != nil || a.Index != nil || a.Append || a.Value == nil {
				continue
			}
			if v := c.word(a.Value); v.ok {
				c.vars[name] = v.s
			} else if v.nonFlag {
				c.safeVars[name] = true
			}
		}
		return true
	}
	for _, a := range x.Assigns {
		if a.Name == nil || !safeEnvName(a.Name.Value) {
			name := "?"
			if a.Name != nil {
				name = a.Name.Value
			}
			return c.fail("sets %s for the command", name)
		}
	}
	argv := make([]arg, len(x.Args))
	for i, w := range x.Args {
		argv[i] = c.word(w)
	}
	if err := c.checkArgv(argv); err != nil {
		return c.fail("%s", err.Error())
	}
	// Keep walking: arguments can hold $(...) whose own commands must pass.
	return true
}

// arg is one word of a command line. ok is false when the word contains an
// expansion whose value is unknown ($(...), an unset $VAR; globs are fine).
// nonFlag is true when the word certainly does not start with '-', known or
// not - `$HOME/x`, `/var/$f`, a loop variable over fixed words.
type arg struct {
	s       string
	ok      bool
	nonFlag bool
}

// systemBinDirs are the directories a command may be named from by full path.
// /tmp/x/cat is somebody's program, not cat.
var systemBinDirs = map[string]bool{
	"/bin": true, "/usr/bin": true, "/sbin": true, "/usr/sbin": true,
	"/usr/local/bin": true, "/usr/local/sbin": true,
}

// checkArgv classifies one simple command given as words. It is also the
// entry point for wrappers (sudo, timeout, xargs, env), which hand it the
// command they would run.
func (c *checker) checkArgv(argv []arg) error {
	if len(argv) == 0 {
		return nil
	}
	if !argv[0].ok || argv[0].s == "" {
		return fmt.Errorf("command name is not a fixed word")
	}
	name := argv[0].s
	if strings.Contains(name, "/") {
		if !systemBinDirs[path.Dir(name)] {
			return fmt.Errorf("%s runs a program outside the system directories", name)
		}
		name = path.Base(name)
	}
	args := argv[1:]
	if c.extra[name] {
		return nil
	}
	if plain[name] {
		return nil
	}
	if r, ok := rules[name]; ok {
		return r(c, name, args)
	}
	if wrap, ok := wrappers[name]; ok {
		return wrap(c, name, args)
	}
	if why, ok := notReadOnly[name]; ok {
		return fmt.Errorf("%s %s", name, why)
	}
	return fmt.Errorf("%s is not a known read-only command", name)
}

// ---- word helpers ----

// word resolves a shell word to its literal value where it can: quotes are
// removed and variables assigned a fixed value earlier in the line are
// substituted.
func (c *checker) word(w *syntax.Word) arg {
	if w == nil {
		return arg{}
	}
	var b strings.Builder
	ok, nonFlag, first := true, false, true
	// lead settles nonFlag from the first thing the word expands to.
	lead := func(text string, known bool) {
		if !first {
			return
		}
		switch {
		case text != "":
			nonFlag, first = text[0] != '-', false
		case !known:
			first = false
		}
	}
	var parts func(ps []syntax.WordPart)
	parts = func(ps []syntax.WordPart) {
		for _, p := range ps {
			switch x := p.(type) {
			case *syntax.Lit:
				lead(x.Value, true)
				b.WriteString(x.Value)
			case *syntax.SglQuoted:
				lead(x.Value, true)
				b.WriteString(x.Value)
			case *syntax.DblQuoted:
				parts(x.Parts)
			case *syntax.ParamExp:
				name, simple := simpleParam(x)
				if v, known := c.vars[name]; simple && known {
					lead(v, true)
					b.WriteString(v)
					continue
				}
				if simple && first && (c.safeVars[name] || systemVars[name]) {
					nonFlag, first = true, false
				}
				ok = false
				lead("", false)
			default:
				ok = false
				lead("", false)
			}
		}
	}
	parts(w.Parts)
	if ok {
		s := b.String()
		return arg{s: s, ok: true, nonFlag: s == "" || s[0] != '-'}
	}
	return arg{nonFlag: nonFlag}
}

// simpleParam reports the name of a plain $x / ${x} expansion.
func simpleParam(x *syntax.ParamExp) (string, bool) {
	if x.Param == nil || x.Excl || x.Length || x.Width || x.Index != nil ||
		x.Slice != nil || x.Repl != nil || x.Names != 0 || x.Exp != nil {
		return "", false
	}
	return x.Param.Value, true
}

func wordText(w *syntax.Word) string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	syntax.NewPrinter().Print(&b, w)
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
