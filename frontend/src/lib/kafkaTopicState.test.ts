import { describe, it, expect, beforeEach, vi } from "vitest";

const calls: string[] = [];
let tabState: { topics: string[]; consumers: { id: string; request: any }[] } = { topics: [], consumers: [] };

vi.mock("./api", () => ({
  api: {
    kafkaConsumeStop: async (id: string) => void calls.push(`stop ${id}`),
    kafkaConsumeHalt: async (id: string) => void calls.push(`halt ${id}`),
    kafkaConsumeSnapshot: async () => ({
      state: "done", messages: [], received: 0, scanned: 0, dropped: 0, buffered: 0,
      buffer_messages: 10000, buffer_bytes: 256 << 20, filtering: false, partitions: [0],
    }),
    kafkaGetTabState: async () => tabState,
  },
}));

const { kafkaTabs } = await import("./kafkaState.svelte");
const { topicState, releaseTopic, adoptTabState } = await import("./kafkaTopicState.svelte");

const req = (topic: string, extra = {}) => ({
  topic, mode: "earliest", n: 0, timestamp: 0, partition: -1, limit: 0, stop_at_end: true, ...extra,
});

describe("TopicState", () => {
  beforeEach(() => {
    calls.length = 0;
    for (const sid of ["s1", "s2"]) kafkaTabs.forget(sid);
    tabState = { topics: [], consumers: [] };
  });

  it("is one object per session and topic", () => {
    expect(topicState("s1", "a")).toBe(topicState("s1", "a"));
    expect(topicState("s1", "a")).not.toBe(topicState("s2", "a"));
  });

  it("adopts a consumer with the form that started it", () => {
    const t = topicState("s1", "orders");
    t.adopt("kc:1", req("orders", { mode: "last", n: 7, key_contains: "abc", stop_at_end: false }) as any);
    expect(t.consumerId).toBe("kc:1");
    expect(t.mode).toBe("last");
    expect(t.n).toBe(7);
    expect(t.keyHas).toBe("abc");
    expect(t.stopAtEnd).toBe(false);
    // A second consumer for the same tab is stopped, the first one kept.
    t.adopt("kc:2", req("orders") as any);
    expect(t.consumerId).toBe("kc:1");
    expect(calls).toContain("stop kc:2");
  });

  it("closing the tab stops the backend consumer, forgetting does not", () => {
    topicState("s1", "a").adopt("kc:a", req("a") as any);
    topicState("s1", "b").adopt("kc:b", req("b") as any);
    releaseTopic("s1", "a");
    expect(calls).toEqual(["stop kc:a"]);
    kafkaTabs.forget("s1");
    expect(calls).toEqual(["stop kc:a"]);
    // forget let go of the state: a later use starts fresh.
    expect(topicState("s1", "b").consumerId).toBeNull();
  });

  it("adoptTabState merges the backend's tabs with ones opened meanwhile", async () => {
    tabState = { topics: ["a", "b"], consumers: [{ id: "kc:c", request: req("c") }] };
    kafkaTabs.openTopics["s1"] = ["x", "a"];
    await adoptTabState("s1");
    expect(kafkaTabs.openTopics["s1"]).toEqual(["a", "b", "c", "x"]);
    expect(topicState("s1", "c").consumerId).toBe("kc:c");
  });
});
