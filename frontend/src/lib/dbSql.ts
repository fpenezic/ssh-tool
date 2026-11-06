// SQL text for rows of a table's Data view: "Copy as SQL" and the UPDATE
// script that cell edits collect. Values become literals in the engine's
// own syntax (they go into a query tab for the user to read and run, not
// to the server from here). Pure, unit-tested.

export type SqlEngine = "postgres" | "mysql" | "oracle" | "mssql" | "db2";

export interface SqlTable {
  engine: SqlEngine;
  database: string; // the schema on MySQL, Oracle and Db2
  schema: string;
  name: string;
}

export interface SqlColumn {
  name: string;
  type: string;
}

// A column the server fills itself, from the described column's extra:
// an identity that refuses a value (PostgreSQL, SQL Server, a GENERATED
// ALWAYS one on Oracle / Db2) or a generated / computed column. A plain
// auto_increment takes a value and is not one, nor is MySQL's
// DEFAULT_GENERATED (only its default is an expression).
export function serverFilled(extra: string | undefined): boolean {
  return /GENERATED ALWAYS|(VIRTUAL|STORED) GENERATED|^generated$|^identity$|^computed/i.test(extra ?? "");
}

export function ident(engine: SqlEngine, name: string): string {
  if (engine === "mysql") return "`" + name.replace(/`/g, "``") + "`";
  if (engine === "mssql") return "[" + name.replace(/]/g, "]]") + "]";
  return '"' + name.replace(/"/g, '""') + '"';
}

export function tableName(t: SqlTable): string {
  const id = (n: string) => ident(t.engine, n);
  switch (t.engine) {
    case "mysql":
    case "oracle":
    case "db2":
      return t.database ? `${id(t.database)}.${id(t.name)}` : id(t.name);
    case "mssql":
      return `${id(t.schema || "dbo")}.${id(t.name)}`;
    default:
      // Always qualified: the session's search_path may not start with public.
      return t.schema ? `${id(t.schema)}.${id(t.name)}` : id(t.name);
  }
}

const BINARY = /binary|blob|bytea|\braw\b|bit data|image/i;

// A column whose cells the grid shows as hex (0x... or \x...): bound and
// written as bytes. MySQL / MariaDB BIT too; PostgreSQL bit strings are
// 0s and 1s, not hex.
export function isBinaryType(engine: string, type: string): boolean {
  return BINARY.test(type) || (engine === "mysql" && /^bit\b/i.test(type));
}
const NUMERIC = /int|numeric|decimal|number|float|double|real|money|serial/i;
const NUMBER = /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/;
const TEMPORAL = /date|timestamp/i;

// A value as a literal. The grid's text of a binary cell is hex (0x... or
// \x...); a number stays bare for a numeric column.
export function literal(engine: SqlEngine, v: string | null, type: string): string {
  if (v === null) return "NULL";
  if (isBinaryType(engine, type) && /^(0x|\\x)[0-9a-fA-F]*$/.test(v)) {
    const hex = v.slice(2);
    switch (engine) {
      case "postgres":
        return `'\\x${hex}'::bytea`;
      case "mysql":
        return `X'${hex}'`;
      case "mssql":
        return `0x${hex}`;
      case "oracle":
        return `HEXTORAW('${hex}')`;
      case "db2":
        return `BX'${hex}'`;
    }
  }
  if (NUMERIC.test(type) && !/interval|point/i.test(type) && NUMBER.test(v)) return v;
  // MySQL reads a backslash in a string as an escape or not depending on
  // sql_mode (NO_BACKSLASH_ESCAPES), and a NUL can cut a pasted statement:
  // such a value goes as hex instead.
  if (engine === "mysql" && (v.includes("\\") || v.includes("\0"))) {
    const hex = [...new TextEncoder().encode(v)].map((b) => b.toString(16).padStart(2, "0")).join("");
    return `_utf8mb4 X'${hex}'`;
  }
  const q = v.replace(/'/g, "''");
  // Oracle reads a bare date string with the session's NLS format; a
  // TIMESTAMP literal takes the offset a zoned value prints with too.
  if (engine === "oracle" && TEMPORAL.test(type)) return `TIMESTAMP '${q}'`;
  if (engine === "mssql" && /^n?(var)?char|^n?text/i.test(type)) return `N'${q}'`;
  return `'${q}'`;
}

// The condition that picks one row: its key columns, or every column when
// the table has no primary key (then it can match more than one row, and
// the script says so).
// Types the engine cannot compare with = (json, LOBs, xml): a keyless row
// is picked without them.
function noCompare(engine: SqlEngine, type: string): boolean {
  switch (engine) {
    case "postgres":
      return /^json$|^xml$/i.test(type);
    case "oracle":
      return /lob|^long|xmltype/i.test(type);
    case "mssql":
      return /^n?text$|^image$|^xml$/i.test(type);
    case "db2":
      return /lob|^xml$/i.test(type);
    default:
      return false;
  }
}

// A key column the rows do not have is an error, not a dropped condition:
// a WHERE missing part of the key would touch more rows than the one meant.
export function rowWhere(engine: SqlEngine, cols: SqlColumn[], row: (string | null)[], key: string[]): string {
  const use = key.length ? key : cols.filter((c) => !noCompare(engine, c.type)).map((c) => c.name);
  return use
    .map((k) => {
      const i = cols.findIndex((c) => c.name === k);
      if (i < 0) throw new Error(`key column ${k} is not among the columns shown`);
      const v = row[i];
      return v === null ? `${ident(engine, k)} IS NULL` : `${ident(engine, k)} = ${literal(engine, v, cols[i].type)}`;
    })
    .join(" AND ");
}

// The note a keyless table's statements start with.
function keylessNote(engine: SqlEngine, cols: SqlColumn[], key: string[]): string {
  if (key.length) return "";
  const left = cols.filter((c) => noCompare(engine, c.type)).map((c) => c.name);
  return (
    "-- no primary key: each condition names every column and can match more than one row\n" +
    (left.length ? `-- left out of the conditions (cannot be compared with =): ${left.join(", ")}\n` : "")
  );
}

// filled: the columns the server fills (serverFilled), left out.
export function insertSql(t: SqlTable, cols: SqlColumn[], rows: (string | null)[][], filled: Set<string> = new Set()): string {
  const keep = cols.map((c, i) => ({ c, i })).filter(({ c }) => !filled.has(c.name));
  const names = keep.map(({ c }) => ident(t.engine, c.name)).join(", ");
  const note = keep.length < cols.length ? `-- left out (the server fills them): ${cols.filter((c) => filled.has(c.name)).map((c) => c.name).join(", ")}\n` : "";
  return (
    note +
    rows.map((r) => `INSERT INTO ${tableName(t)} (${names}) VALUES (${keep.map(({ c, i }) => literal(t.engine, r[i], c.type)).join(", ")});`).join("\n")
  );
}

export function deleteSql(t: SqlTable, cols: SqlColumn[], rows: (string | null)[][], key: string[]): string {
  const note = keylessNote(t.engine, cols, key);
  return note + rows.map((r) => `DELETE FROM ${tableName(t)} WHERE ${rowWhere(t.engine, cols, r, key)};`).join("\n");
}

// UPDATE statements for edited cells: changes maps a row (as the server
// sent it) to its column index -> new value.
export function updateSql(
  t: SqlTable,
  cols: SqlColumn[],
  changes: { row: (string | null)[]; set: Map<number, string | null> }[],
  key: string[],
  filled: Set<string> = new Set(),
): string {
  for (const c of changes) {
    for (const ci of c.set.keys()) {
      if (filled.has(cols[ci].name)) throw new Error(`${cols[ci].name} is filled by the server (identity or generated): it cannot be set`);
    }
  }
  const note = keylessNote(t.engine, cols, key);
  return (
    note +
    changes
      .filter((c) => c.set.size > 0)
      .map((c) => {
        const set = [...c.set.entries()].map(([ci, v]) => `${ident(t.engine, cols[ci].name)} = ${literal(t.engine, v, cols[ci].type)}`).join(", ");
        return `UPDATE ${tableName(t)} SET ${set} WHERE ${rowWhere(t.engine, cols, c.row, key)};`;
      })
      .join("\n")
  );
}

// A table's primary key columns: the columns described with key PRI (the
// index labels can carry " DESC" or " (included)", which a column's own
// name could too).
export function primaryKey(columns: { name: string; key?: string }[] | null | undefined): string[] {
  return (columns ?? []).filter((c) => c.key === "PRI").map((c) => c.name);
}

// "Copy as WHERE" of grid cells: the condition that picks those values.
// One column gives col = v or col IN (...) (a NULL among them adds
// IS NULL); several give each row's cells ANDed, the rows ORed.
export function cellsWhere(engine: SqlEngine, cols: SqlColumn[], rows: (string | null)[][]): string {
  if (!cols.length || !rows.length) return "";
  if (cols.length === 1) {
    const id = ident(engine, cols[0].name);
    const seen = new Set<string>();
    const vals: string[] = [];
    let hasNull = false;
    for (const r of rows) {
      const v = r[0];
      if (v === null) hasNull = true;
      else if (!seen.has(v)) {
        seen.add(v);
        vals.push(literal(engine, v, cols[0].type));
      }
    }
    const parts = [
      ...(vals.length === 1 ? [`${id} = ${vals[0]}`] : vals.length ? [`${id} IN (${vals.join(", ")})`] : []),
      ...(hasNull ? [`${id} IS NULL`] : []),
    ];
    return parts.join(" OR ");
  }
  const names = cols.map((c) => c.name);
  const each = rows.map((r) => rowWhere(engine, cols, r, names));
  return each.length === 1 ? each[0] : each.map((w) => `(${w})`).join("\nOR ");
}

// A table's name as a query on the session's database names it, for any
// engine of the Database tab (ClickHouse quotes like MySQL; PostgreSQL
// leaves public unqualified).
export function objectName(engine: string, schema: string, name: string): string {
  if (engine === "mysql" || engine === "clickhouse") return "`" + name.replace(/`/g, "``") + "`";
  if (engine === "mssql") return `[${(schema || "dbo").replace(/]/g, "]]")}].[${name.replace(/]/g, "]]")}]`;
  return (schema && schema !== "public" ? `"${schema.replace(/"/g, '""')}".` : "") + `"${name.replace(/"/g, '""')}"`;
}

// The query a table's "Query" starts with, in the engine's language.
export function selectText(engine: string, schema: string, name: string): string {
  if (engine === "opensearch") return `GET /${name}/_search\n{\n  "query": { "match_all": {} },\n  "size": 100\n}`;
  if (engine === "redis") {
    const glob = name.endsWith(":*") ? name.slice(0, -1).replace(/[*?[\]\\]/g, "\\$&") + "*" : "*";
    return `SCAN 0 MATCH "${glob.replace(/["\\]/g, "\\$&")}" COUNT 1000`;
  }
  const q = objectName(engine, schema, name);
  if (engine === "oracle" || engine === "db2") return `SELECT *\nFROM ${q}\nFETCH FIRST 100 ROWS ONLY;`;
  if (engine === "mssql") return `SELECT TOP 100 *\nFROM ${q};`;
  return `SELECT *\nFROM ${q}\nLIMIT 100;`;
}

// The exact row count, for the engines with SQL; OpenSearch counts with
// _count, Redis has no count of a key group.
export function countText(engine: string, schema: string, name: string): string | null {
  if (engine === "opensearch") return `GET /${name}/_count`;
  if (engine === "redis") return null;
  return `SELECT COUNT(*)\nFROM ${objectName(engine, schema, name)};`;
}
