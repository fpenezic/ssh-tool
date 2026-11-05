import { describe, expect, it } from "vitest";
import { deleteSql, ident, insertSql, literal, primaryKey, rowWhere, tableName, updateSql, type SqlColumn } from "./dbSql";

const cols: SqlColumn[] = [
  { name: "id", type: "int4" },
  { name: "name", type: "text" },
  { name: "data", type: "bytea" },
];

describe("dbSql", () => {
  it("quotes identifiers per engine", () => {
    expect(ident("postgres", 'a"b')).toBe('"a""b"');
    expect(ident("mysql", "a`b")).toBe("`a``b`");
    expect(ident("mssql", "a]b")).toBe("[a]]b]");
    expect(tableName({ engine: "postgres", database: "app", schema: "public", name: "t" })).toBe('"public"."t"');
    expect(tableName({ engine: "postgres", database: "app", schema: "sales", name: "t" })).toBe('"sales"."t"');
    expect(tableName({ engine: "mysql", database: "app", schema: "", name: "t" })).toBe("`app`.`t`");
    expect(tableName({ engine: "mssql", database: "app", schema: "", name: "t" })).toBe("[dbo].[t]");
  });

  it("writes literals in the engine's syntax", () => {
    expect(literal("postgres", null, "text")).toBe("NULL");
    expect(literal("postgres", "42", "int4")).toBe("42");
    expect(literal("postgres", "42", "text")).toBe("'42'");
    expect(literal("postgres", "it's", "text")).toBe("'it''s'");
    expect(literal("mysql", "a\\b", "varchar")).toBe("_utf8mb4 X'615c62'");
    expect(literal("mysql", "it's", "varchar")).toBe("'it''s'");
    expect(literal("mysql", "a\0b", "varchar")).toBe("_utf8mb4 X'610062'");
    expect(literal("postgres", "\\xdead", "bytea")).toBe("'\\xdead'::bytea");
    expect(literal("mysql", "0xdead", "varbinary")).toBe("X'dead'");
    expect(literal("oracle", "0xdead", "raw")).toBe("HEXTORAW('dead')");
    expect(literal("db2", "0xdead", "varbinary")).toBe("BX'dead'");
    expect(literal("oracle", "2024-01-02 10:11:12", "date")).toBe("TIMESTAMP '2024-01-02 10:11:12'");
    expect(literal("mssql", "x", "nvarchar")).toBe("N'x'");
    expect(literal("postgres", "1 day", "interval")).toBe("'1 day'");
  });

  it("picks a row by its key, or by every column without one", () => {
    const row = ["7", "a", null];
    expect(rowWhere("postgres", cols, row, ["id"])).toBe('"id" = 7');
    expect(rowWhere("postgres", cols, row, [])).toBe(`"id" = 7 AND "name" = 'a' AND "data" IS NULL`);
    expect(() => rowWhere("postgres", cols, row, ["missing"])).toThrow(/missing/);
  });

  it("builds INSERT, UPDATE and DELETE", () => {
    const t = { engine: "postgres" as const, database: "app", schema: "", name: "t" };
    expect(insertSql(t, cols, [["1", "x", null]])).toBe(`INSERT INTO "t" ("id", "name", "data") VALUES (1, 'x', NULL);`);
    expect(updateSql(t, cols, [{ row: ["1", "x", null], set: new Map([[1, "y"]]) }], ["id"])).toBe(`UPDATE "t" SET "name" = 'y' WHERE "id" = 1;`);
    expect(deleteSql(t, cols, [["1", "x", null]], ["id"])).toBe(`DELETE FROM "t" WHERE "id" = 1;`);
    expect(deleteSql(t, cols, [["1", "x", null]], [])).toMatch(/^-- no primary key/);
  });

  it("reads the primary key from the described columns", () => {
    expect(primaryKey([{ name: "a DESC", key: "PRI" }, { name: "b" }, { name: "c", key: "PRI" }])).toEqual(["a DESC", "c"]);
    expect(primaryKey(null)).toEqual([]);
  });
});
