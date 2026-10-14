import { describe, expect, it } from "vitest";
import { applyKafkaDraft, kafkaDraftFrom, kafkaDraftSame, KAFKA_NONE } from "./kafkaDraft";

describe("kafkaDraft", () => {
  it("round-trips settings", () => {
    const s = {
      kafka_enabled: true,
      kafka_bootstrap: "a:9092",
      kafka_use_tunnel: false,
      kafka_read_only: true,
      kafka_sasl: "scram-sha-512",
      kafka_credential_ref: "",
    };
    const d = kafkaDraftFrom(s);
    expect(d).toEqual({
      enabled: "on", bootstrap: "a:9092", tunnel: "off", def: "", readOnly: "on",
      tls: "", tlsSkip: "", sasl: "scram-sha-512", cred: KAFKA_NONE,
    });
    expect(applyKafkaDraft({}, d)).toEqual({
      kafka_enabled: true,
      kafka_bootstrap: "a:9092",
      kafka_use_tunnel: false,
      kafka_default: undefined,
      kafka_read_only: true,
      kafka_tls: undefined,
      kafka_tls_skip_verify: undefined,
      kafka_sasl: "scram-sha-512",
      kafka_credential_ref: "",
    });
    expect(kafkaDraftSame(d, s)).toBe(true);
  });

  it("treats empty as inherit and tracks changes", () => {
    const d = kafkaDraftFrom(undefined);
    expect(Object.values(d).every((v) => v === "")).toBe(true);
    expect(applyKafkaDraft({ username: "x" }, d)).toMatchObject({ username: "x", kafka_enabled: undefined, kafka_bootstrap: undefined });
    expect(kafkaDraftSame({ ...d, bootstrap: "  " }, {})).toBe(true);
    expect(kafkaDraftSame({ ...d, enabled: "off" }, {})).toBe(false);
  });
});
