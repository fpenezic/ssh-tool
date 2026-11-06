import { afterEach, describe, expect, it, vi } from "vitest";

const setText = vi.fn();
vi.mock("./api", () => ({ api: { clipboardSetText: (t: string) => setText(t) } }));

import { copyTable, tableHtml, tableMarkdown, tableTsv } from "./clipboardTable";
import { readAll, rowsCsv, rowsJson, type PageRead } from "./dbExport";
import { cellsWhere, countText, objectName, selectText, type SqlColumn } from "./dbSql";

describe("clipboardTable", () => {
  it("writes TSV with and without a header, flattening tabs and newlines", () => {
    expect(tableTsv(["a", "b"], [["1", null], ["x\ty", "l1\nl2"]])).toBe("a\tb\n1\t\nx y\tl1 l2\n");
    expect(tableTsv(null, [["1", "2"]])).toBe("1\t2\n");
  });
  it("escapes HTML and leaves the head out when there is none", () => {
    const h = tableHtml(null, [["<b>&", null]]);
    expect(h).not.toContain("<thead>");
    expect(h).toContain("&lt;b&gt;&amp;");
    expect(tableHtml(["c"], [])).toContain("<thead>");
  });
  it("keeps line breaks and space runs in HTML", () => {
    expect(tableHtml(null, [["a\nb  c"]])).toContain(">a<br>b &nbsp;c<");
  });
  it("writes Markdown with NULL and escaped pipes", () => {
    expect(tableMarkdown(["a", "b"], [["x|y", null]])).toBe("| a | b |\n| --- | --- |\n| x\\|y | NULL |\n");
    expect(tableMarkdown(["a"], [["x\\"], ["p\\|q"]])).toBe("| a |\n| --- |\n| x\\\\ |\n| p\\\\\\|q |\n");
  });
});

describe("copyTable", () => {
  afterEach(() => {
    setText.mockReset();
    vi.unstubAllGlobals();
  });
  function stubClipboard(write: (items: unknown[]) => Promise<void>) {
    class Item {
      constructor(public data: Record<string, Blob>) {}
    }
    vi.stubGlobal("ClipboardItem", Item);
    vi.stubGlobal("navigator", { clipboard: { write } });
  }
  it("writes HTML and text in one rich write", async () => {
    const write = vi.fn(async () => {});
    stubClipboard(write);
    await copyTable(["a"], [["1"]]);
    expect(write).toHaveBeenCalledTimes(1);
    const item = (write.mock.calls[0] as unknown as [{ data: Record<string, Blob> }[]])[0][0];
    expect(Object.keys(item.data).sort()).toEqual(["text/html", "text/plain"]);
    expect(await item.data["text/plain"].text()).toBe("a\n1\n");
    expect(setText).not.toHaveBeenCalled();
  });
  it("falls back to the plain write when the rich one is refused or missing", async () => {
    stubClipboard(async () => {
      throw new Error("denied");
    });
    setText.mockResolvedValue(true);
    await copyTable(null, [["1", "2"]]);
    expect(setText).toHaveBeenCalledWith("1\t2\n");
    vi.unstubAllGlobals();
    vi.stubGlobal("navigator", {});
    await copyTable(null, [["3"]]);
    expect(setText).toHaveBeenLastCalledWith("3\n");
  });
  it("throws when the plain write fails too", async () => {
    vi.stubGlobal("navigator", {});
    setText.mockResolvedValue(false);
    await expect(copyTable(null, [["1"]])).rejects.toThrow();
    setText.mockRejectedValue(new Error("gone"));
    await expect(copyTable(null, [["1"]])).rejects.toThrow("gone");
  });
});

describe("dbExport", () => {
  const cols = [{ name: "id", type: "int" }, { name: "t", type: "text" }];
  it("writes CSV and JSON", () => {
    expect(rowsCsv(cols, [["1", 'a,"b"'], ["2", null]])).toBe('id,t\r\n1,"a,""b"""\r\n2,\r\n');
    expect(JSON.parse(rowsJson(cols, [["1", null]]))).toEqual([{ id: "1", t: null }]);
  });
  function pages(total: number, cutAt = Infinity) {
    const calls: [number, number][] = [];
    const read = async (offset: number, limit: number): Promise<PageRead> => {
      calls.push([offset, limit]);
      const n = Math.max(0, Math.min(limit, cutAt, total - offset));
      return {
        columns: cols,
        rows: Array.from({ length: n }, (_, i) => [String(offset + i), "x"]),
        truncated: n === cutAt && offset + n < total,
        cut_cells: 0,
      };
    };
    return { read, calls };
  }
  it("reads every page, a short page is the last", async () => {
    const { read, calls } = pages(25);
    const r = await readAll(read, 10, () => false, () => {});
    expect(r?.rows.length).toBe(25);
    expect(calls).toEqual([[0, 10], [10, 10], [20, 10]]);
  });
  it("asks once more after a full last page", async () => {
    const { read, calls } = pages(20);
    const r = await readAll(read, 10, () => false, () => {});
    expect(r?.rows.length).toBe(20);
    expect(calls.length).toBe(3);
  });
  it("goes on after a page the server cut short", async () => {
    const { read } = pages(25, 7);
    const r = await readAll(read, 10, () => false, () => {});
    expect(r?.rows.map((x) => x[0])).toEqual(Array.from({ length: 25 }, (_, i) => String(i)));
  });
  it("a Stop during the last page still stops", async () => {
    const { read } = pages(15);
    let n = 0;
    const r = await readAll(read, 10, () => n >= 15, (got) => (n = got));
    expect(r).toBeNull();
  });
  it("stops when asked", async () => {
    const { read } = pages(100);
    let n = 0;
    const r = await readAll(read, 10, () => n >= 20, (got) => (n = got));
    expect(r).toBeNull();
  });
});

describe("cellsWhere", () => {
  const c: SqlColumn[] = [{ name: "id", type: "int4" }, { name: "name", type: "text" }];
  it("one cell is an equality, a NULL is IS NULL", () => {
    expect(cellsWhere("postgres", [c[1]], [["a'b"]])).toBe(`"name" = 'a''b'`);
    expect(cellsWhere("postgres", [c[1]], [[null]])).toBe(`"name" IS NULL`);
  });
  it("one column of several values is IN, duplicates once", () => {
    expect(cellsWhere("mysql", [c[0]], [["1"], ["2"], ["1"], [null]])).toBe("`id` IN (1, 2) OR `id` IS NULL");
  });
  it("several columns AND each row, rows ORed", () => {
    expect(cellsWhere("postgres", c, [["1", "a"]])).toBe(`"id" = 1 AND "name" = 'a'`);
    expect(cellsWhere("postgres", c, [["1", "a"], ["2", null]])).toBe(`("id" = 1 AND "name" = 'a')\nOR ("id" = 2 AND "name" IS NULL)`);
  });
  it("takes columns by position, so two of one name keep their own values", () => {
    expect(cellsWhere("postgres", [c[0], c[0]], [["1", "2"]])).toBe(`"id" = 1 AND "id" = 2`);
  });
  it("refuses a value it cannot compare with =, keeps IS NULL", () => {
    const j: SqlColumn = { name: "doc", type: "json" };
    expect(() => cellsWhere("postgres", [j], [["{}"]])).toThrow(/cannot be compared/);
    expect(cellsWhere("postgres", [j], [[null]])).toBe(`"doc" IS NULL`);
  });
});

describe("object names and starter queries", () => {
  it("quotes per engine", () => {
    expect(objectName("postgres", "public", "t")).toBe('"public"."t"');
    expect(objectName("postgres", "app", "t")).toBe('"app"."t"');
    expect(objectName("clickhouse", "", "t`x")).toBe("`t``x`");
    expect(objectName("mssql", "", "t")).toBe("[dbo].[t]");
  });
  it("selects and counts in the engine's language", () => {
    expect(selectText("mssql", "dbo", "t")).toBe("SELECT TOP 100 *\nFROM [dbo].[t];");
    expect(countText("oracle", "", "T")).toBe('SELECT COUNT(*)\nFROM "T";');
    expect(countText("opensearch", "", "logs")).toBe("GET /logs/_count");
    expect(countText("redis", "", "k:*")).toBeNull();
  });
});
