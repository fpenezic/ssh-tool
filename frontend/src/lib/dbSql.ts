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
      return t.schema && t.schema !== "public" ? `${id(t.schema)}.${id(t.name)}` : id(t.name);
  }
}

const BINARY = /binary|blob|bytea|\braw\b|bit data|image/i;
const NUMERIC = /int|numeric|decimal|number|float|double|real|money|serial/i;
const NUMBER = /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/;
const TEMPORAL = /date|timestamp/i;

// A value as a literal. The grid's text of a binary cell is hex (0x... or
// \x...); a number stays bare for a numeric column.
export function literal(engine: SqlEngine, v: string | null, type: string): string {
  if (v === null) return "NULL";
  if (BINARY.test(type) && /^(0x|\\x)[0-9a-fA-F]*$/.test(v)) {
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
  let q = v.replace(/'/g, "''");
  if (engine === "mysql") q = q.replace(/\\/g, "\\\\");
  // Oracle reads a bare date string with the session's NLS format.
  if (engine === "oracle" && TEMPORAL.test(type) && !/zone/i.test(type)) return `TIMESTAMP '${q}'`;
  if (engine === "mssql" && /^n?(var)?char|^n?text/i.test(type)) return `N'${q}'`;
  return `'${q}'`;
}

// The condition that picks one row: its key columns, or every column when
// the table has no primary key (then it can match more than one row, and
// the script says so).
export function rowWhere(engine: SqlEngine, cols: SqlColumn[], row: (string | null)[], key: string[]): string {
  const use = key.length ? key : cols.map((c) => c.name);
  return use
    .map((k) => {
      const i = cols.findIndex((c) => c.name === k);
      if (i < 0) return null;
      const v = row[i];
      return v === null ? `${ident(engine, k)} IS NULL` : `${ident(engine, k)} = ${literal(engine, v, cols[i].type)}`;
    })
    .filter((x): x is string => x !== null)
    .join(" AND ");
}

export function insertSql(t: SqlTable, cols: SqlColumn[], rows: (string | null)[][]): string {
  const names = cols.map((c) => ident(t.engine, c.name)).join(", ");
  return rows
    .map((r) => `INSERT INTO ${tableName(t)} (${names}) VALUES (${cols.map((c, i) => literal(t.engine, r[i], c.type)).join(", ")});`)
    .join("\n");
}

export function deleteSql(t: SqlTable, cols: SqlColumn[], rows: (string | null)[][], key: string[]): string {
  const note = key.length ? "" : "-- no primary key: each condition names every column and can match more than one row\n";
  return note + rows.map((r) => `DELETE FROM ${tableName(t)} WHERE ${rowWhere(t.engine, cols, r, key)};`).join("\n");
}

// UPDATE statements for edited cells: changes maps a row (as the server
// sent it) to its column index -> new value.
export function updateSql(t: SqlTable, cols: SqlColumn[], changes: { row: (string | null)[]; set: Map<number, string | null> }[], key: string[]): string {
  const note = key.length ? "" : "-- no primary key: each condition names every column and can match more than one row\n";
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

// A table's primary key columns from its described indexes; index column
// labels can carry " DESC" (Db2) or " (included)" (SQL Server).
export function primaryKey(indexes: { primary: boolean; columns: string[] }[] | null | undefined): string[] {
  const pk = (indexes ?? []).find((i) => i.primary);
  return (pk?.columns ?? []).filter((c) => !c.endsWith(" (included)")).map((c) => c.replace(/ (ASC|DESC)$/, ""));
}
