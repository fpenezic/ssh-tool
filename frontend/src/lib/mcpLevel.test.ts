import { describe, it, expect } from "vitest";
import { loudestMcpLevel, mcpCounterTitle, mcpLevelTitle } from "./mcpLevel";

describe("loudestMcpLevel", () => {
  it("ranks the levels by how much they let an LLM do", () => {
    expect(loudestMcpLevel("read", "read-run")).toBe("read-run");
    expect(loudestMcpLevel("read-run", "read-run-yolo")).toBe("read-run-yolo");
    expect(loudestMcpLevel("read", "read-run-yolo")).toBe("read-run-yolo");
  });

  it("is order-independent, so folding a map in any iteration order agrees", () => {
    expect(loudestMcpLevel("read-run-yolo", "read")).toBe("read-run-yolo");
    expect(loudestMcpLevel("read-run", "read")).toBe("read-run");
  });

  it("treats the empty level as the quietest", () => {
    expect(loudestMcpLevel("", "read")).toBe("read");
    expect(loudestMcpLevel("read", "")).toBe("read");
    expect(loudestMcpLevel("", "")).toBe("");
  });

  // A level from a newer build must never outrank one this build understands:
  // showing calm blue because of a string we could not read would be the one
  // failure mode that matters here.
  it("ranks an unknown level at the bottom", () => {
    expect(loudestMcpLevel("read-run-yolo", "read-run-telepathy")).toBe("read-run-yolo");
    expect(loudestMcpLevel("read", "read-run-telepathy")).toBe("read");
  });

  it("folds a set of sessions to the loudest one", () => {
    const fold = (levels: string[]) => levels.reduce((a, b) => loudestMcpLevel(a, b), "");
    expect(fold(["read", "read", "read"])).toBe("read");
    expect(fold(["read", "read-run-yolo", "read"])).toBe("read-run-yolo");
    expect(fold([])).toBe("");
  });
});

describe("mcpCounterTitle", () => {
  it("agrees with the singular/plural of the count", () => {
    expect(mcpCounterTitle(1, "read")).toContain("1 session shared");
    expect(mcpCounterTitle(3, "read")).toContain("3 sessions shared");
  });

  it("names the risk when the loudest grant can run commands", () => {
    expect(mcpCounterTitle(2, "read-run")).toContain("needs approval");
    expect(mcpCounterTitle(2, "read-run-yolo")).toContain("AUTO-RUN");
  });

  it("stays quiet for a read-only set", () => {
    const t = mcpCounterTitle(2, "read");
    expect(t).not.toContain("AUTO-RUN");
    expect(t).not.toContain("approval");
  });
});

describe("mcpLevelTitle", () => {
  // The counter and the single-session indicator describe different things
  // and must not be swapped by a later refactor.
  it("describes one session, not a count", () => {
    expect(mcpLevelTitle("read")).not.toContain("sessions");
    expect(mcpLevelTitle("read-run-yolo")).toContain("AUTO-RUN");
  });
});
