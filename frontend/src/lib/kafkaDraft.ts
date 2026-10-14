// The Kafka settings of a connection or folder as the editor holds them:
// every field a string, "" = inherit. Tri-state booleans are "on" / "off";
// NONE is an explicit empty override (stops an inherited SASL mechanism or
// credential). Pure, unit-tested.

import type { InheritableSettings } from "./api";

export const KAFKA_NONE = "__none__";

export interface KafkaDraft {
  enabled: string;
  bootstrap: string;
  tunnel: string;
  def: string;
  readOnly: string;
  tls: string;
  tlsSkip: string;
  sasl: string;
  cred: string;
}

const enc = (v: boolean | undefined) => (v === true ? "on" : v === false ? "off" : "");
const dec = (s: string) => (s === "on" ? true : s === "off" ? false : undefined);
const encRef = (v: string | undefined) => (v === "" ? KAFKA_NONE : v ?? "");
const decRef = (s: string) => (s === KAFKA_NONE ? "" : s || undefined);

export function kafkaDraftFrom(s: InheritableSettings | undefined | null): KafkaDraft {
  const o = s ?? {};
  return {
    enabled: enc(o.kafka_enabled),
    bootstrap: o.kafka_bootstrap ?? "",
    tunnel: enc(o.kafka_use_tunnel),
    def: enc(o.kafka_default),
    readOnly: enc(o.kafka_read_only),
    tls: enc(o.kafka_tls),
    tlsSkip: enc(o.kafka_tls_skip_verify),
    sasl: encRef(o.kafka_sasl),
    cred: encRef(o.kafka_credential_ref),
  };
}

// Writes the draft into settings (in place) and returns them.
export function applyKafkaDraft<T extends InheritableSettings>(s: T, d: KafkaDraft): T {
  s.kafka_enabled = dec(d.enabled);
  s.kafka_bootstrap = d.bootstrap.trim() || undefined;
  s.kafka_use_tunnel = dec(d.tunnel);
  s.kafka_default = dec(d.def);
  s.kafka_read_only = dec(d.readOnly);
  s.kafka_tls = dec(d.tls);
  s.kafka_tls_skip_verify = dec(d.tlsSkip);
  s.kafka_sasl = decRef(d.sasl);
  s.kafka_credential_ref = decRef(d.cred);
  return s;
}

export function kafkaDraftSame(d: KafkaDraft, s: InheritableSettings | undefined | null): boolean {
  const o = kafkaDraftFrom(s);
  return (Object.keys(o) as (keyof KafkaDraft)[]).every((k) =>
    k === "bootstrap" ? d.bootstrap.trim() === o.bootstrap.trim() : d[k] === o[k],
  );
}
