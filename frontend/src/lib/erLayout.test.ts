import { describe, expect, it } from "vitest";
import { boxes, edgePath, edgesOf, layout, shownColumns } from "./erLayout";
import type { DbForeignKey, DbGraphTable } from "./api";

const t = (name: string, ...cols: string[]): DbGraphTable => ({
  name,
  columns: [{ name: "id", type: "int", key: true }, ...cols.map((c) => ({ name: c, type: "int" }))],
});
const fk = (table: string, col: string, ref: string, refSchema = "public"): DbForeignKey => ({
  name: `${table}_${col}_fk`, def: "", database: "app", schema: "public", table, columns: [col],
  ref_database: "app", ref_schema: refSchema, ref_table: ref, ref_columns: ["id"],
});
const measure = (s: string) => s.length * 7;

describe("ER layout", () => {
  const tables = [t("customers"), t("orders", "customer_id", "note"), t("order_items", "order_id", "product_id"), t("products"), t("audit_log"), t("tree", "parent_id")];
  const fks = [fk("orders", "customer_id", "customers"), fk("order_items", "order_id", "orders"), fk("order_items", "product_id", "products"), fk("tree", "parent_id", "tree"), fk("orders", "note", "elsewhere", "other")];
  it("keeps the keys inside the schema", () => {
    expect(edgesOf(tables, fks).map((e) => `${e.from}>${e.to}`)).toEqual(["orders>customers", "order_items>orders", "order_items>products", "tree>tree"]);
  });
  it("puts referenced tables left of the ones pointing at them, unlinked ones under them", () => {
    const b = layout(boxes(tables, fks, false, measure), edgesOf(tables, fks));
    const at = (n: string) => b.find((x) => x.name === n)!;
    expect(at("customers").x).toBeLessThan(at("orders").x);
    expect(at("orders").x).toBeLessThan(at("order_items").x);
    expect(at("products").x).toBeLessThan(at("order_items").x);
    const graphBottom = Math.max(...["customers", "orders", "order_items", "products"].map((n) => at(n).y + at(n).h));
    expect(at("audit_log").y).toBeGreaterThan(graphBottom);
    expect(at("tree").y).toBeGreaterThan(graphBottom); // a self key only
    // No two boxes overlap.
    for (const a of b) for (const c of b) {
      if (a === c) continue;
      const apart = a.x + a.w <= c.x || c.x + c.w <= a.x || a.y + a.h <= c.y || c.y + c.h <= a.y;
      expect(apart, `${a.name} / ${c.name}`).toBe(true);
    }
  });
  it("survives a cycle", () => {
    const ts = [t("a", "b_id"), t("b", "a_id")];
    const ks = [fk("a", "b_id", "b"), fk("b", "a_id", "a")];
    const b = layout(boxes(ts, ks, false, measure), edgesOf(ts, ks));
    expect(b[0].x).not.toBe(b[1].x);
  });
  it("folds a box to its keys", () => {
    const fkc = new Set(["customer_id"]);
    expect(shownColumns(t("orders", "customer_id", "note"), fkc, true).map((c) => c.name)).toEqual(["id", "customer_id"]);
    expect(shownColumns({ name: "x", columns: [{ name: "a", type: "t" }, { name: "b", type: "t" }] }, new Set(), true).map((c) => c.name)).toEqual(["a"]);
  });
  it("draws an edge from the facing sides", () => {
    const [a, c] = boxes([t("a", "c_id"), t("c")], [], false, measure);
    a.x = 400; c.x = 0;
    // Out of a's left side at c_id's line (24 + 18 + 9), into c's right
    // side at id's line (24 + 9), the controls pulled half the gap apart.
    const x2 = c.w;
    const k = Math.max(40, (400 - x2) / 2);
    expect(edgePath(a, c, "c_id", "id")).toBe(`M 400 51 C ${400 - k} 51, ${x2 + k} 33, ${x2} 33`);
  });
});
