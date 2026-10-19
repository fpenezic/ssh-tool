import { describe, it, expect, beforeEach } from "vitest";
import { kafkaTabs, kafkaListenSummary } from "./kafkaState.svelte";

describe("kafkaListenSummary", () => {
  beforeEach(() => {
    kafkaTabs.listening = {};
  });

  it("is empty when nothing runs", () => {
    kafkaTabs.setListening("s1", "a", { state: "done", received: 5, scanned: 5, filtering: false });
    expect(kafkaListenSummary(["s1"])).toEqual({ running: 0, failed: 0, tip: "" });
  });

  it("counts running consumers across sessions and lists them", () => {
    kafkaTabs.setListening("s1", "orders", { state: "running", received: 12400, scanned: 12400, filtering: false });
    kafkaTabs.setListening("s1", "audit", { state: "running", received: 3, scanned: 900, filtering: true });
    kafkaTabs.setListening("s2", "other", { state: "running", received: 1, scanned: 1, filtering: false });
    const s = kafkaListenSummary(["s1"]);
    expect(s.running).toBe(2);
    expect(s.failed).toBe(0);
    expect(s.tip).toBe("listening: orders (12k), audit (3 of 900)");
  });

  it("marks a consumer with an error as failed", () => {
    kafkaTabs.setListening("s1", "a", { state: "running", received: 1, scanned: 1, filtering: false, error: "leader unreachable" });
    kafkaTabs.setListening("s1", "b", { state: "error", received: 0, scanned: 0, filtering: false });
    const s = kafkaListenSummary(["s1"]);
    expect(s.failed).toBe(2);
    expect(s.running).toBe(0);
    expect(s.tip).toContain("a: failed - leader unreachable");
  });

  it("forgets a consumer that is gone", () => {
    kafkaTabs.setListening("s1", "a", { state: "running", received: 1, scanned: 1, filtering: false });
    kafkaTabs.setListening("s1", "a", null);
    expect(kafkaListenSummary(["s1"]).running).toBe(0);
  });
});
