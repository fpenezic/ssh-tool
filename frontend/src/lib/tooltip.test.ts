import { describe, it, expect } from "vitest";
import { __test } from "./tooltip.svelte";

const { tipTextOf, parkTitle, restoreTitle, tipTargetOf, SHOW_DELAY, WARM_MS } = __test;

// The suite runs without a DOM, so these stand in for the few element bits the
// pure helpers touch. Anything needing layout or real event dispatch is left
// to manual verification.
class FakeEl {
  attrs = new Map<string, string>();
  parentElement: FakeEl | null = null;
  getAttribute(k: string) { return this.attrs.has(k) ? this.attrs.get(k)! : null; }
  setAttribute(k: string, v: string) { this.attrs.set(k, v); }
  removeAttribute(k: string) { this.attrs.delete(k); }
  hasAttribute(k: string) { return this.attrs.has(k); }
}

function el(attrs: Record<string, string> = {}): any {
  const e = new FakeEl();
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

describe("tipTextOf", () => {
  it("reads a live title", () => {
    expect(tipTextOf(el({ title: "Copy host" }))).toBe("Copy host");
  });

  it("reads a parked title", () => {
    expect(tipTextOf(el({ "data-tooltip": "Copy password" }))).toBe("Copy password");
  });

  it("prefers the live title when both are present", () => {
    expect(tipTextOf(el({ title: "new", "data-tooltip": "stale" }))).toBe("new");
  });

  it("returns empty for an element with neither", () => {
    expect(tipTextOf(el())).toBe("");
  });
});

describe("park / restore", () => {
  it("moves title out of the way and back", () => {
    const e = el({ title: "Copy ssh command" });
    parkTitle(e);
    expect(e.getAttribute("title")).toBeNull();
    expect(e.getAttribute("data-tooltip")).toBe("Copy ssh command");
    restoreTitle(e);
    expect(e.getAttribute("title")).toBe("Copy ssh command");
    expect(e.getAttribute("data-tooltip")).toBeNull();
  });

  it("parking twice does not lose the text to an empty second park", () => {
    const e = el({ title: "Snippets" });
    parkTitle(e);
    parkTitle(e); // no title to move now - must not clobber data-tooltip
    expect(e.getAttribute("data-tooltip")).toBe("Snippets");
  });

  it("restoring an unparked element is a no-op", () => {
    const e = el({ title: "unchanged" });
    restoreTitle(e);
    expect(e.getAttribute("title")).toBe("unchanged");
  });

  it("survives a park/restore round trip on a reactive title change", () => {
    // tcpdump's title changes while it runs; the parked copy is what the
    // tooltip reads, and the restore must put the parked text back.
    const e = el({ title: "Live packet capture" });
    parkTitle(e);
    expect(tipTextOf(e)).toBe("Live packet capture");
    restoreTitle(e);
    parkTitle(e);
    expect(tipTextOf(e)).toBe("Live packet capture");
  });
});

describe("tipTargetOf", () => {
  it("finds the element itself", () => {
    const root = el();
    const btn = el({ title: "Copy host" });
    btn.parentElement = root;
    expect(tipTargetOf(btn, root)).toBe(btn);
  });

  it("walks up from an icon inside the button", () => {
    // The pointer lands on the <svg>, not the <button> that carries the title.
    const root = el();
    const btn = el({ title: "Copy username" });
    const svg = el();
    btn.parentElement = root;
    svg.parentElement = btn;
    expect(tipTargetOf(svg, root)).toBe(btn);
  });

  it("returns null when nothing in the chain has a title", () => {
    const root = el();
    const plain = el();
    plain.parentElement = root;
    expect(tipTargetOf(plain, root)).toBeNull();
  });

  it("stops at the group root and does not escape to an outer title", () => {
    // The pane header itself carries a title in some states; hovering dead
    // space inside the toolbar must not pick it up.
    const outer = el({ title: "outer pane title" });
    const root = el();
    const inner = el();
    root.parentElement = outer;
    inner.parentElement = root;
    expect(tipTargetOf(inner, root)).toBeNull();
  });

  it("matches a parked element on a repeat hover", () => {
    // Second hover: the title is already parked as data-tooltip.
    const root = el();
    const btn = el({ "data-tooltip": "Copy password" });
    btn.parentElement = root;
    expect(tipTargetOf(btn, root)).toBe(btn);
  });

  it("returns null for a non-element target", () => {
    expect(tipTargetOf(null, el())).toBeNull();
  });
});

describe("timing constants", () => {
  it("shows well inside the native delay it replaces", () => {
    // The whole point: the OS tooltip is around a second.
    expect(SHOW_DELAY).toBeLessThan(400);
    expect(SHOW_DELAY).toBeGreaterThan(0);
  });

  it("stays warm longer than it takes to cross between icons", () => {
    expect(WARM_MS).toBeGreaterThan(SHOW_DELAY);
  });
});
