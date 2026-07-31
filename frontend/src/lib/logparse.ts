// Structured parsing + a tiny filter query language for the live log tail.
// Pure TypeScript (no Svelte) so it is unit-testable and shared by the log-tail
// modal and its detached window. The Go backend streams raw text unchanged;
// everything here runs on the ~2000-line client ring.

export type LogLevel = "error" | "warn" | "info" | "debug" | "trace" | "none";

export interface ParsedLine {
  raw: string; // the original line, untouched (copy / dedupe / grouping source)
  ts?: string; // timestamp string as it appeared, if a format exposed one
  level: LogLevel;
  source?: string; // unit / host / component when a format exposes it
  msg: string; // the human message (== raw when unstructured)
  fields?: Record<string, string>; // extra key/values (JSON logs, access status)
}

// levelRank orders severities for `level>=x` comparisons. "none" sorts below
// everything so `level>=trace` still excludes truly unclassified lines.
const LEVEL_RANK: Record<LogLevel, number> = {
  none: 0,
  trace: 1,
  debug: 2,
  info: 3,
  warn: 4,
  error: 5,
};

export function levelRank(level: LogLevel): number {
  return LEVEL_RANK[level] ?? 0;
}

// normalizeLevel maps the many spellings log producers use onto our set.
function normalizeLevel(s: string | undefined): LogLevel {
  if (!s) return "none";
  const v = s.trim().toLowerCase();
  switch (v) {
    case "error":
    case "err":
    case "fatal":
    case "crit":
    case "critical":
    case "emerg":
    case "alert":
    case "panic":
      return "error";
    case "warn":
    case "warning":
      return "warn";
    case "info":
    case "notice":
    case "information":
      return "info";
    case "debug":
      return "debug";
    case "trace":
    case "verbose":
      return "trace";
    default:
      return "none";
  }
}

// levelFromStatus derives a severity from an HTTP status code (access logs).
function levelFromStatus(status: number): LogLevel {
  if (status >= 500) return "error";
  if (status >= 400) return "warn";
  return "info";
}

const RE_SYSLOG =
  /^(\w{3}\s+\d+\s+\d{2}:\d{2}:\d{2})\s+(\S+)\s+([^:[]+)(?:\[\d+\])?:\s?(.*)$/;
const RE_ACCESS =
  /^(\S+)\s+\S+\s+\S+\s+\[([^\]]+)\]\s+"(\S+)\s+[^"]*"\s+(\d{3})\b(.*)$/;
// A word-boundary keyword scan for unstructured app logs. Ordered by severity
// so the highest present wins.
const KEYWORD_LEVELS: [RegExp, LogLevel][] = [
  [/\b(?:FATAL|CRIT(?:ICAL)?|EMERG|PANIC|ERROR|ERR)\b/i, "error"],
  [/\b(?:WARN(?:ING)?)\b/i, "warn"],
  [/\b(?:INFO|NOTICE)\b/i, "info"],
  [/\bDEBUG\b/i, "debug"],
  [/\bTRACE\b/i, "trace"],
];

// A `docker compose logs` line is prefixed "service  | actual log line". Match
// the service and the remainder so we can classify the inner line normally.
const RE_COMPOSE = /^([A-Za-z0-9._-]+)\s*\|\s?(.*)$/;

// parseLine classifies one raw log line. Ordered probes, first match wins, and
// an always-safe fallback so an unrecognised line is returned verbatim.
export function parseLine(raw: string): ParsedLine {
  // 0. Compose prefix ("service | ..."): peel it off, classify the inner line,
  //    and record the service as the source. Keep the original raw for copy.
  const cm = RE_COMPOSE.exec(raw);
  if (cm) {
    const [, service, inner] = cm;
    const p = parseLine(inner);
    return { ...p, raw, source: service, msg: p.msg === inner ? inner : p.msg };
  }

  // 1. JSON logs (structured logging - pino, zap, bunyan, ...).
  const trimmed = raw.trimStart();
  if (trimmed.startsWith("{")) {
    try {
      const obj = JSON.parse(trimmed);
      if (obj && typeof obj === "object" && !Array.isArray(obj)) {
        const o = obj as Record<string, unknown>;
        const level = normalizeLevel(
          str(o.level) ?? str(o.severity) ?? str(o.lvl) ?? str(o.loglevel),
        );
        const msg = str(o.msg) ?? str(o.message) ?? str(o.text) ?? raw;
        const ts = str(o.time) ?? str(o.ts) ?? str(o.timestamp) ?? str(o["@timestamp"]);
        const source = str(o.logger) ?? str(o.source) ?? str(o.component) ?? str(o.unit);
        const fields: Record<string, string> = {};
        for (const [k, val] of Object.entries(o)) {
          if (["level", "severity", "lvl", "loglevel", "msg", "message", "text", "time", "ts", "timestamp", "@timestamp", "logger", "source", "component", "unit"].includes(k)) {
            continue;
          }
          const sv = scalar(val);
          if (sv !== undefined) fields[k] = sv;
        }
        return { raw, level, msg, ts, source, fields: Object.keys(fields).length ? fields : undefined };
      }
    } catch {
      // not valid JSON - fall through to the text probes
    }
  }

  // 2. nginx / apache combined access log.
  const am = RE_ACCESS.exec(raw);
  if (am) {
    const [, ip, ts, method, statusStr] = am;
    const status = parseInt(statusStr, 10);
    return {
      raw,
      ts,
      level: levelFromStatus(status),
      source: ip,
      msg: raw,
      fields: { ip, method, status: statusStr },
    };
  }

  // 3. journald / syslog: "Mon DD HH:MM:SS host unit[pid]: message".
  const sm = RE_SYSLOG.exec(raw);
  if (sm) {
    const [, ts, host, comp, msg] = sm;
    const source = comp.trim();
    // The message body may still name its own level (e.g. "error: ...").
    const level = scanKeywords(msg) ?? scanKeywords(raw) ?? "none";
    return { raw, ts, level, source, msg, fields: { host } };
  }

  // 4. Unstructured: scan for a level keyword, keep the whole line as message.
  const level = scanKeywords(raw) ?? "none";
  return { raw, level, msg: raw };
}

function scanKeywords(s: string): LogLevel | undefined {
  for (const [re, level] of KEYWORD_LEVELS) {
    if (re.test(s)) return level;
  }
  return undefined;
}

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : typeof v === "number" ? String(v) : undefined;
}

function scalar(v: unknown): string | undefined {
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  return undefined;
}

// --- Filter query language ------------------------------------------------

// A compiled predicate over parsed lines. parseQuery compiles a query string
// once; the result runs per line. The grammar is deliberately tiny: space-
// separated terms, implicit AND. Any parse trouble degrades to a plain
// substring match over the raw line, so typing mid-query never blanks the view.

export type LinePredicate = (l: ParsedLine) => boolean;

const ALWAYS: LinePredicate = () => true;

export function parseQuery(query: string): LinePredicate {
  const q = query.trim();
  if (q === "") return ALWAYS;

  let terms: string[];
  try {
    terms = tokenize(q);
  } catch {
    // Unbalanced quote etc. - fall back to a whole-string substring match.
    return substringPredicate(q);
  }
  if (terms.length === 0) return ALWAYS;

  const predicates = terms.map(compileTerm);
  return (l) => predicates.every((p) => p(l));
}

// tokenize splits on whitespace but keeps "quoted phrases" together. Throws on
// an unbalanced quote so the caller can fall back.
function tokenize(q: string): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < q.length) {
    while (i < q.length && /\s/.test(q[i])) i++;
    if (i >= q.length) break;
    // A leading -/! before a quote stays attached to the token.
    let prefix = "";
    if (q[i] === "-" || q[i] === "!") {
      prefix = q[i];
      i++;
    }
    if (q[i] === '"') {
      i++;
      const start = i;
      while (i < q.length && q[i] !== '"') i++;
      if (i >= q.length) throw new Error("unbalanced quote");
      out.push(prefix + '"' + q.slice(start, i) + '"');
      i++;
    } else {
      const start = i - prefix.length;
      while (i < q.length && !/\s/.test(q[i])) i++;
      out.push(q.slice(start, i));
    }
  }
  return out;
}

const CMP_RE = /^([A-Za-z_][\w.]*)(>=|<=|=)(.*)$/;

function compileTerm(term: string): LinePredicate {
  // Negation: -foo / !foo -> NOT substring(foo). (Quotes handled below.)
  if ((term.startsWith("-") || term.startsWith("!")) && term.length > 1) {
    const inner = unquote(term.slice(1));
    const sub = inner.toLowerCase();
    return (l) => !l.raw.toLowerCase().includes(sub);
  }

  // field / level comparison: key=value, key>=value, key<=value.
  const m = CMP_RE.exec(term);
  if (m) {
    const [, key, op, rawVal] = m;
    const val = unquote(rawVal);
    if (key.toLowerCase() === "level") {
      const want = levelRank(normalizeLevel(val));
      return (l) => {
        const have = levelRank(l.level);
        if (op === ">=") return have >= want;
        if (op === "<=") return have <= want;
        return l.level === normalizeLevel(val);
      };
    }
    // Generic field match (exact, case-insensitive). >= / <= on non-level
    // fields degrade to numeric compare when both sides are numbers, else
    // fall back to equality.
    const lc = val.toLowerCase();
    return (l) => {
      const fv = fieldValue(l, key);
      if (fv === undefined) return false;
      if (op === "=") return fv.toLowerCase() === lc;
      const a = Number(fv);
      const b = Number(val);
      if (Number.isFinite(a) && Number.isFinite(b)) {
        return op === ">=" ? a >= b : a <= b;
      }
      return fv.toLowerCase() === lc;
    };
  }

  // Bare term (possibly quoted) -> case-insensitive substring over raw.
  return substringPredicate(unquote(term));
}

function substringPredicate(s: string): LinePredicate {
  const needle = s.toLowerCase();
  if (needle === "") return ALWAYS;
  return (l) => l.raw.toLowerCase().includes(needle);
}

// fieldValue resolves a query key against a parsed line's known columns and
// its extra fields.
function fieldValue(l: ParsedLine, key: string): string | undefined {
  const k = key.toLowerCase();
  if (k === "level") return l.level;
  if (k === "source") return l.source;
  if (k === "ts" || k === "time") return l.ts;
  if (k === "msg" || k === "message") return l.msg;
  if (l.fields) {
    for (const [fk, fv] of Object.entries(l.fields)) {
      if (fk.toLowerCase() === k) return fv;
    }
  }
  return undefined;
}

function unquote(s: string): string {
  if (s.length >= 2 && s.startsWith('"') && s.endsWith('"')) {
    return s.slice(1, -1);
  }
  return s;
}

// --- Autocomplete ---------------------------------------------------------

// A single completion for the filter box: the full query string to set, plus a
// short label/hint for the dropdown.
export interface QuerySuggestion {
  insert: string; // the whole query text after accepting this suggestion
  label: string; // what the dropdown shows (the completed token)
  hint?: string; // a short description
}

// Query keys the language understands (level + the columns/fields worth
// completing). Values for `level` are fixed; values for other keys come from
// whatever the current lines actually contain.
const QUERY_KEYS: { key: string; hint: string }[] = [
  { key: "level", hint: "severity: error|warn|info|debug|trace" },
  { key: "status", hint: "HTTP status (access logs)" },
  { key: "method", hint: "HTTP method (access logs)" },
  { key: "source", hint: "unit / component" },
  { key: "msg", hint: "message text" },
];

const LEVEL_VALUES = ["error", "warn", "info", "debug", "trace"];

// suggestQuery returns completions for the term under the cursor. query is the
// whole box text, cursor the caret index, sample a slice of current parsed
// lines used to harvest real field values. Earlier terms are preserved verbatim
// so accepting a suggestion only rewrites the token being typed.
export function suggestQuery(
  query: string,
  cursor: number,
  sample: ParsedLine[],
): QuerySuggestion[] {
  const upto = query.slice(0, cursor);
  // The current term is the run of non-space chars ending at the cursor.
  const termStart = Math.max(upto.lastIndexOf(" ") + 1, 0);
  const prefix = query.slice(0, termStart); // earlier terms + trailing space
  const term = upto.slice(termStart);
  const rest = query.slice(cursor); // text after the cursor, kept as-is

  const build = (token: string): string => prefix + token + rest;

  // If the term is a `key=partial`, complete the VALUE.
  const eq = term.indexOf("=");
  if (eq > 0 && !term.includes(">") && !term.includes("<")) {
    const key = term.slice(0, eq).toLowerCase();
    const partial = term.slice(eq + 1).toLowerCase();
    let values: string[];
    if (key === "level") {
      values = LEVEL_VALUES;
    } else {
      values = observedValues(sample, key);
    }
    return values
      .filter((v) => v.toLowerCase().startsWith(partial))
      .slice(0, 8)
      .map((v) => ({ insert: build(`${key}=${v}`), label: `${key}=${v}` }));
  }

  // Otherwise complete a KEY (offer key= / key>= for level).
  const p = term.toLowerCase();
  const out: QuerySuggestion[] = [];
  for (const { key, hint } of QUERY_KEYS) {
    if (!key.startsWith(p)) continue;
    if (key === "level") {
      out.push({ insert: build("level>="), label: "level>=", hint });
      out.push({ insert: build("level="), label: "level=", hint });
    } else {
      out.push({ insert: build(`${key}=`), label: `${key}=`, hint });
    }
  }
  return out.slice(0, 8);
}

// observedValues collects distinct values a field takes across the sample, so
// the dropdown offers real data (status=500, method=POST, ...).
function observedValues(sample: ParsedLine[], key: string): string[] {
  const seen = new Set<string>();
  for (const l of sample) {
    const v = fieldValue(l, key);
    if (v !== undefined) seen.add(v);
    if (seen.size > 20) break;
  }
  return [...seen].sort();
}

// --- Pattern grouping -----------------------------------------------------

// templateKey normalizes a line's message by replacing volatile tokens with
// placeholders, so lines that differ only by variable data (ids, timestamps,
// counters, IPs) collapse to the same key. Used by the "Group similar" toggle.
export function templateKey(l: ParsedLine): string {
  let s = l.msg || l.raw;
  // Quoted strings -> a single placeholder (paths, values).
  s = s.replace(/"[^"]*"/g, '"…"').replace(/'[^']*'/g, "'…'");
  // IPv4 addresses.
  s = s.replace(/\b\d{1,3}(?:\.\d{1,3}){3}\b/g, "#.#.#.#");
  // Hex / uuid-ish runs.
  s = s.replace(/\b[0-9a-fA-F]{8,}\b/g, "#");
  // Any remaining number run.
  s = s.replace(/\b\d+\b/g, "#");
  // Collapse whitespace so spacing differences don't split a group.
  s = s.replace(/\s+/g, " ").trim();
  return l.level + "|" + s;
}

// A grouped view row: the first line seen for a key, how many collapsed into
// it, and the timestamp last seen.
export interface LineGroup {
  line: ParsedLine;
  count: number;
  lastTs?: string;
}

// groupLines collapses parsed lines by templateKey, preserving first-seen text
// and ordering by last occurrence (so the most recently active group sinks to
// the bottom, matching the streaming view's "newest at the end").
export function groupLines(lines: ParsedLine[]): LineGroup[] {
  const map = new Map<string, LineGroup & { lastIdx: number }>();
  lines.forEach((l, idx) => {
    const key = templateKey(l);
    const existing = map.get(key);
    if (existing) {
      existing.count++;
      existing.lastTs = l.ts ?? existing.lastTs;
      existing.lastIdx = idx;
    } else {
      map.set(key, { line: l, count: 1, lastTs: l.ts, lastIdx: idx });
    }
  });
  return [...map.values()]
    .sort((a, b) => a.lastIdx - b.lastIdx)
    .map(({ line, count, lastTs }) => ({ line, count, lastTs }));
}
