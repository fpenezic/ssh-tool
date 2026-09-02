// Minimal syntax highlighter for the SFTP quick-view.
//
// Deliberately not a real parser and deliberately not a dependency: the
// files people open over SFTP are configs and logs, not source trees, and
// a grammar-driven highlighter (shiki, highlight.js) would add more to the
// bundle than the whole feature is worth. Five line-oriented lexers cover
// what actually shows up: yaml, ini/conf, json, shell, and log output.
//
// Everything here works line by line and emits escaped HTML. Nothing is
// ever interpolated raw - see esc() and the token contract below.

export type Lang = "yaml" | "ini" | "json" | "shell" | "python" | "dockerfile" | "log" | "text";

/** esc escapes text for HTML. Every code path that emits file content
 *  must run through this: the input is a remote file we do not trust. */
function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** span wraps already-escaped content in a token class. */
function span(cls: string, escaped: string): string {
  return `<span class="tok-${cls}">${escaped}</span>`;
}

/** outsideTags applies fn to the parts of html that are NOT inside a tag we
 *  already emitted. Decorating passes run one after another over the same
 *  string, so a later pattern can otherwise match the markup of an earlier
 *  one - `class` is a Python keyword, and without this the keyword pass
 *  rewrote `<span class="tok-str">` into broken nested markup. */
function outsideTags(html: string, fn: (chunk: string) => string): string {
  let out = "";
  let i = 0;
  while (i < html.length) {
    const lt = html.indexOf("<", i);
    if (lt < 0) {
      out += fn(html.slice(i));
      break;
    }
    out += fn(html.slice(i, lt));
    const gt = html.indexOf(">", lt);
    if (gt < 0) {
      // Unterminated tag: emit the rest verbatim rather than risk mangling.
      out += html.slice(lt);
      break;
    }
    out += html.slice(lt, gt + 1);
    i = gt + 1;
  }
  return out;
}

// Extensions that map to a lexer directly. Basenames are checked first
// (dotfiles and extensionless configs are the common case over SFTP).
const EXT_LANG: Record<string, Lang> = {
  yml: "yaml", yaml: "yaml",
  json: "json",
  ini: "ini", conf: "ini", cfg: "ini", toml: "ini", properties: "ini",
  sh: "shell", bash: "shell", zsh: "shell", ksh: "shell",
  py: "python", pyw: "python",
  dockerfile: "dockerfile",
  log: "log", out: "log", err: "log",
};

const BASE_LANG: Record<string, Lang> = {
  ".env": "ini",
  ".gitconfig": "ini",
  ".npmrc": "ini",
  "dockerfile": "dockerfile",
  "containerfile": "dockerfile",
  "makefile": "shell",
  "nginx.conf": "ini",
  "sshd_config": "ini",
  "ssh_config": "ini",
  "fstab": "ini",
  "hosts": "ini",
  "crontab": "shell",
  "authorized_keys": "text",
  "known_hosts": "text",
};

/** detectLang picks a lexer from the filename, falling back to a content
 *  sniff for extensionless files (very common in /etc). */
export function detectLang(name: string, content: string): Lang {
  const base = name.toLowerCase().replace(/^.*\//, "");
  if (BASE_LANG[base]) return BASE_LANG[base];

  const dot = base.lastIndexOf(".");
  const ext = dot > 0 ? base.slice(dot + 1) : "";
  const byExt = EXT_LANG[ext];
  // A .log / .out holding one JSON document is common (structured logging,
  // captured tool output), so let the content win over those two
  // extensions. Every other extension is trusted as-is.
  if (byExt && byExt !== "log") return byExt;
  if (base.startsWith(".env")) return "ini";
  if (isJsonish(content)) return "json";
  if (byExt) return byExt;

  // Content sniff, cheap and only on the head of the file.
  const head = content.slice(0, 2048);
  if (/^#!.*\bpython[\d.]*\b/m.test(head)) return "python";
  if (/^#!.*\b(sh|bash|zsh|ksh)\b/m.test(head)) return "shell";
  // Timestamped lines at the start of most lines = log.
  const lines = head.split("\n").slice(0, 12).filter((l) => l.trim());
  if (lines.length >= 3) {
    const stamped = lines.filter((l) =>
      /^\s*(\[?\d{4}-\d{2}-\d{2}|\w{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2})/.test(l),
    ).length;
    if (stamped >= lines.length * 0.6) return "log";
  }
  if (/^\s*[\w.-]+\s*[:=]\s*\S/m.test(head)) return "yaml";
  return "text";
}

// ---- per-language line lexers ----
// Each takes a RAW line and returns ESCAPED html. They must escape every
// character they emit; helpers esc()/span() are the only way content gets out.

function hlYaml(line: string): string {
  const m = /^(\s*(?:-\s+)?)([\w.$-]+)(\s*:\s*)(.*)$/.exec(line);
  if (m && !line.trimStart().startsWith("#")) {
    const [, indent, key, sep, rest] = m;
    return esc(indent) + span("key", esc(key)) + esc(sep) + hlValue(rest);
  }
  const c = line.indexOf("#");
  if (c >= 0 && !line.slice(0, c).trim()) return span("comment", esc(line));
  const dash = /^(\s*-\s)([\s\S]*)$/.exec(line);
  if (dash) {
    return esc(dash[1]) + hlValue(dash[2]);
  }
  return hlValue(line);
}

/** hlValue colours a scalar plus any trailing comment. Shared by yaml/ini. */
function hlValue(v: string): string {
  // Split off a trailing comment, but only when it is not inside quotes.
  let inS = false, inD = false, cut = -1;
  for (let i = 0; i < v.length; i++) {
    const ch = v[i];
    if (ch === "'" && !inD) inS = !inS;
    else if (ch === '"' && !inS) inD = !inD;
    else if (ch === "#" && !inS && !inD && (i === 0 || /\s/.test(v[i - 1]))) { cut = i; break; }
  }
  const body = cut >= 0 ? v.slice(0, cut) : v;
  const tail = cut >= 0 ? span("comment", esc(v.slice(cut))) : "";
  const t = body.trim();

  if (!t) return esc(body) + tail;
  if (/^(['"]).*\1$/s.test(t)) return span("str", esc(body)) + tail;
  if (/^-?\d+(\.\d+)?([eE][+-]?\d+)?$/.test(t)) return span("num", esc(body)) + tail;
  if (/^(true|false|yes|no|on|off|null|~|none)$/i.test(t)) return span("bool", esc(body)) + tail;
  return esc(body) + tail;
}

function hlIni(line: string): string {
  const t = line.trimStart();
  if (t.startsWith("#") || t.startsWith(";") || t.startsWith("//")) {
    return span("comment", esc(line));
  }
  if (/^\s*\[.*\]\s*$/.test(line)) return span("section", esc(line));
  const m = /^(\s*(?:export\s+)?)([\w.$-]+)(\s*[:=]\s*)(.*)$/.exec(line);
  if (m) {
    const [, indent, key, sep, rest] = m;
    return esc(indent) + span("key", esc(key)) + esc(sep) + hlValue(rest);
  }
  // Directive style: "listen 80;" (nginx) - first word is the key.
  const d = /^(\s*)([\w.$-]+)(\s+)(.*)$/.exec(line);
  if (d) return esc(d[1]) + span("key", esc(d[2])) + esc(d[3]) + hlValue(d[4]);
  return esc(line);
}

function hlJson(line: string): string {
  let out = "";
  let i = 0;
  while (i < line.length) {
    const ch = line[i];
    if (ch === '"') {
      // Consume a full string, honouring backslash escapes.
      let j = i + 1;
      while (j < line.length) {
        if (line[j] === "\\") { j += 2; continue; }
        if (line[j] === '"') break;
        j++;
      }
      const str = line.slice(i, Math.min(j + 1, line.length));
      // A string followed by a colon is a key.
      const after = line.slice(j + 1);
      out += span(/^\s*:/.test(after) ? "key" : "str", esc(str));
      i = j + 1;
      continue;
    }
    const rest = line.slice(i);
    const num = /^-?\d+(\.\d+)?([eE][+-]?\d+)?/.exec(rest);
    if (num && !/[\w.]/.test(line[i - 1] || "")) {
      out += span("num", esc(num[0]));
      i += num[0].length;
      continue;
    }
    const lit = /^(true|false|null)\b/.exec(rest);
    if (lit) {
      out += span("bool", esc(lit[0]));
      i += lit[0].length;
      continue;
    }
    out += esc(ch);
    i++;
  }
  return out;
}

const SHELL_KW = new RegExp(
  "\\b(if|then|else|elif|fi|for|while|do|done|case|esac|in|function|return" +
  "|export|local|readonly|source|set|unset|echo|exit|shift|trap|eval)\\b",
  "g",
);

function hlShell(line: string): string {
  const t = line.trimStart();
  if (t.startsWith("#")) return span("comment", esc(line));

  // Escape first, then decorate - the patterns below only match ASCII word
  // characters and quotes, which esc() leaves intact apart from quotes.
  let out = esc(line);
  out = out.replace(
    /(&quot;(?:(?!&quot;).)*&quot;|'[^']*')/g,
    (m0) => span("str", m0),
  );
  out = outsideTags(out, (c) =>
    c.replace(/(\$\{[^}]*\}|\$[\w@*#?-]+)/g, (m0) => span("var", m0)),
  );
  out = outsideTags(out, (c) => c.replace(SHELL_KW, (m0) => span("kw", m0)));
  return out;
}

const PY_KW = new RegExp(
  "\\b(and|as|assert|async|await|break|class|continue|def|del|elif|else" +
  "|except|finally|for|from|global|if|import|in|is|lambda|nonlocal|not|or" +
  "|pass|raise|return|try|while|with|yield|None|True|False|self|match|case)\\b",
  "g",
);

// Python is line-oriented enough for this to hold up on the kind of file
// that gets opened over SFTP (a script, a settings module). Triple-quoted
// strings spanning lines are the known gap: each line is lexed on its own,
// so a docstring's middle lines are not coloured as a string. Tracking that
// needs cross-line state the per-line contract here does not carry, and
// getting it wrong looks worse than leaving those lines plain.
function hlPython(line: string): string {
  const t = line.trimStart();
  if (t.startsWith("#")) return span("comment", esc(line));

  let out = esc(line);
  // Strings first, so a keyword inside one is not recoloured afterwards.
  // esc() has already turned " into &quot;, hence the entity in the pattern.
  // A lone triple quote opens or closes a docstring: colour the rest of the
  // line as string so the delimiter line reads correctly. Whole docstring
  // bodies still need cross-line state we do not carry (see the note above).
  // t is the RAW trimmed line, so the test uses real quote characters.
  if (t.startsWith('"""') || t.startsWith("'''")) {
    return span("str", esc(line));
  }
  out = out.replace(
    /(&quot;&quot;&quot;.*?&quot;&quot;&quot;|'''.*?'''|&quot;(?:(?!&quot;).)*&quot;|'[^']*')/g,
    (m0) => span("str", m0),
  );
  // Every later pass skips the spans emitted above - see outsideTags.
  out = outsideTags(out, (c) => c.replace(/#.*$/, (m0) => span("comment", m0)));
  out = outsideTags(out, (c) =>
    c.replace(/\b\d+\.?\d*([eE][+-]?\d+)?\b/g, (m0) => span("num", m0)),
  );
  out = outsideTags(out, (c) => c.replace(PY_KW, (m0) => span("kw", m0)));
  out = outsideTags(out, (c) =>
    c.replace(/(^|\s)(@[\w.]+)/, (_m, sp, dec) => sp + span("section", dec)),
  );
  return out;
}

// Dockerfile instructions. Only the leading keyword on a line is one, so
// this is anchored rather than a global word match - ADD or RUN appearing
// inside an argument is not an instruction.
const DOCKER_INSTR = new RegExp(
  "^(\\s*)(FROM|RUN|CMD|LABEL|MAINTAINER|EXPOSE|ENV|ADD|COPY|ENTRYPOINT" +
  "|VOLUME|USER|WORKDIR|ARG|ONBUILD|STOPSIGNAL|HEALTHCHECK|SHELL|AS)\\b",
  "i",
);

function hlDockerfile(line: string): string {
  const t = line.trimStart();
  if (t.startsWith("#")) return span("comment", esc(line));

  let out = esc(line);
  // Instruction keyword first, then decorate the arguments around it.
  out = out.replace(DOCKER_INSTR, (_m, sp, kw) => sp + span("kw", kw));
  out = outsideTags(out, (c) =>
    c.replace(/(&quot;(?:(?!&quot;).)*&quot;|'[^']*')/g, (m0) => span("str", m0)),
  );
  // $VAR / ${VAR} expansions read the same way as in shell.
  out = outsideTags(out, (c) =>
    c.replace(/(\$\{[^}]*\}|\$[\w@*#?-]+)/g, (m0) => span("var", m0)),
  );
  // A trailing --flag=value on RUN/COPY (--from=builder, --chown=...).
  out = outsideTags(out, (c) =>
    c.replace(/(^|\s)(--[\w-]+)/g, (_m, pre, flag) => pre + span("section", flag)),
  );
  return out;
}

const LOG_LEVEL = /\b(TRACE|DEBUG|INFO|NOTICE|WARN(?:ING)?|ERROR|ERR|CRIT(?:ICAL)?|FATAL|ALERT|EMERG)\b/;

function hlLog(line: string): string {
  let out = esc(line);
  // Leading timestamp: ISO, syslog, or bracketed.
  // `out` is already escaped; pre is whitespace plus an optional bracket
  // and ts is digits/punctuation, so neither needs escaping again here.
  out = out.replace(
    /^(\s*\[?)(\d{4}-\d{2}-\d{2}[T ][\d:.,+-]+Z?|\w{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2})/,
    (_m, pre, ts) => pre + span("time", ts),
  );
  out = outsideTags(out, (c) => c.replace(LOG_LEVEL, (m0) => {
    const cls =
      /^(ERR|ERROR|CRIT|CRITICAL|FATAL|ALERT|EMERG)$/.test(m0) ? "lvl-err"
      : /^(WARN|WARNING|NOTICE)$/.test(m0) ? "lvl-warn"
      : /^(DEBUG|TRACE)$/.test(m0) ? "lvl-dbg"
      : "lvl-info";
    return span(cls, m0);
  }));
  return out;
}

const LEXERS: Record<Lang, (line: string) => string> = {
  yaml: hlYaml,
  ini: hlIni,
  json: hlJson,
  shell: hlShell,
  python: hlPython,
  dockerfile: hlDockerfile,
  log: hlLog,
  text: (l) => esc(l),
};

/** highlight returns one escaped HTML string per input line. The caller
 *  renders these with {@html}, which is safe only because every lexer
 *  escapes its output - do not bypass them. */
export function highlight(content: string, lang: Lang): string[] {
  const fn = LEXERS[lang] ?? LEXERS.text;
  // Split on either terminator: a CRLF file would otherwise leave a trailing
  // \r on every line, which sits between the text and the `$` anchor in most
  // of these patterns and quietly changes what they match.
  return content.split(/\r\n|\n|\r/).map((line) => {
    // A lexer must never take the whole view down with it. Any bug in a
    // pattern degrades that one line to plain escaped text instead.
    try {
      return fn(line);
    } catch {
      return esc(line);
    }
  });
}

/** isJsonish reports whether content is a single parseable JSON document.
 *  Cheap guard first so we don't hand a 256 KB log to JSON.parse. */
export function isJsonish(content: string): boolean {
  const t = content.trim();
  if (t.length < 2) return false;
  const open = t[0], close = t[t.length - 1];
  if (!((open === "{" && close === "}") || (open === "[" && close === "]"))) {
    return false;
  }
  try {
    JSON.parse(t);
    return true;
  } catch {
    return false;
  }
}

/** prettyJson re-indents a JSON document. Returns null when the content is
 *  not a single JSON value, so the caller can leave the file untouched. */
export function prettyJson(content: string, indent = 2): string | null {
  const t = content.trim();
  if (!t) return null;
  try {
    return JSON.stringify(JSON.parse(t), null, indent);
  } catch {
    return null;
  }
}

/** eolFlags returns, per line, whether that line ended with CRLF. Used to
 *  mark the odd ones out in a mixed file - a summary badge tells you the
 *  file is inconsistent, but not where, and "where" is the actionable part.
 *  The array has one entry per line produced by highlight(); the last line
 *  has no terminator, so it is always false. */
export function eolFlags(content: string): boolean[] {
  const out: boolean[] = [];
  let i = 0;
  while (i <= content.length) {
    const nl = content.indexOf("\n", i);
    if (nl < 0) {
      out.push(false);
      break;
    }
    out.push(nl > 0 && content[nl - 1] === "\r");
    i = nl + 1;
  }
  return out;
}

/** detectEol reports which line terminator a file uses. "mixed" means both
 *  appear, which usually means something appended to the file with the wrong
 *  convention - worth surfacing, since it breaks shebangs and here-docs. */
export function detectEol(content: string): "lf" | "crlf" | "mixed" | "none" {
  const crlf = (content.match(/\r\n/g) || []).length;
  // Bare \n not preceded by \r.
  const lf = (content.match(/(^|[^\r])\n/g) || []).length;
  if (crlf && lf) return "mixed";
  if (crlf) return "crlf";
  if (lf) return "lf";
  return "none";
}
