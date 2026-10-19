import { describe, it, expect } from "vitest";
import { splitHostInput } from "./hostInput";

describe("splitHostInput", () => {
  it("leaves a plain host alone", () => {
    expect(splitHostInput(" db.example.com ")).toEqual({ host: "db.example.com", port: undefined, user: undefined });
  });
  it("splits host:port", () => {
    expect(splitHostInput("db.example.com:2222")).toEqual({ host: "db.example.com", port: 2222, user: undefined });
    expect(splitHostInput("10.0.0.5:22")).toMatchObject({ host: "10.0.0.5", port: 22 });
  });
  it("takes the user", () => {
    expect(splitHostInput("deploy@10.0.0.5:2200")).toEqual({ host: "10.0.0.5", port: 2200, user: "deploy" });
    expect(splitHostInput("deploy@host")).toEqual({ host: "host", port: undefined, user: "deploy" });
  });
  it("knows IPv6", () => {
    expect(splitHostInput("2001:db8::1")).toMatchObject({ host: "2001:db8::1", port: undefined });
    expect(splitHostInput("[2001:db8::1]:2222")).toMatchObject({ host: "2001:db8::1", port: 2222 });
    expect(splitHostInput("[2001:db8::1]")).toMatchObject({ host: "2001:db8::1", port: undefined });
  });
  it("keeps text that has no valid port as typed", () => {
    expect(splitHostInput("host:99999")).toEqual({ host: "host:99999" });
    expect(splitHostInput("host:abc")).toMatchObject({ host: "host:abc", port: undefined });
  });
});
