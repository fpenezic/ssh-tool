// macOS (and Windows) filesystems ignore case. A store "confirmModal.svelte.ts"
// next to a component "ConfirmModal.svelte" is fine on Linux, but an import of
// "./confirmModal.svelte" (no .ts) resolves to the COMPONENT on a Mac and the
// build fails there - CI builds on Linux and never sees it. Such stores must
// be imported with their full ".svelte.ts" name.
import { describe, it, expect } from "vitest";

// Every source file under src, as text, keyed "../lib/Foo.svelte" etc.
const sources = import.meta.glob("../**/*.{svelte,ts}", { query: "?raw", import: "default", eager: true }) as Record<string, string>;

describe("case-insensitive filesystem imports", () => {
  it("imports stores that share a name with a component by their full .svelte.ts name", () => {
    const paths = Object.keys(sources);
    const dirOf = (p: string) => p.slice(0, p.lastIndexOf("/"));
    const bad: string[] = [];
    for (const store of paths.filter((p) => p.endsWith(".svelte.ts"))) {
      const dir = dirOf(store);
      const base = store.slice(dir.length + 1, -".ts".length); // "confirmModal.svelte"
      const clash = paths.some((p) => dirOf(p) === dir && p !== store &&
        p.slice(dir.length + 1).toLowerCase() === base.toLowerCase());
      if (!clash) continue;
      const needle = new RegExp(`from\\s+["']\\./${base.replace(".", "\\.")}["']`);
      for (const p of paths) {
        if (dirOf(p) === dir && needle.test(sources[p])) bad.push(`${p} imports ./${base} without .ts`);
      }
    }
    expect(bad).toEqual([]);
  });
});
