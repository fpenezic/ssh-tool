import { describe, expect, it } from "vitest";
import { parseNetbirdKey } from "./netbirdKey";

describe("parseNetbirdKey", () => {
  it("takes the key and management URL from the dashboard's command", () => {
    expect(parseNetbirdKey("netbird up --management-url https://vpn.example.com --setup-key A1B2C3D4-0000-4000-8000-000000000000")).toEqual({
      key: "A1B2C3D4-0000-4000-8000-000000000000",
      management: "https://vpn.example.com",
    });
    expect(parseNetbirdKey(`sudo netbird up -k "A1B2" -m=https://vpn.example.com`)).toEqual({ key: "A1B2", management: "https://vpn.example.com" });
    expect(parseNetbirdKey("netbird up --setup-key=K1")).toEqual({ key: "K1" });
    expect(parseNetbirdKey("netbird up --setup-key=OLD -k NEW")).toEqual({ key: "NEW" });
    expect(parseNetbirdKey("netbird up -k NEW --setup-key LAST")).toEqual({ key: "LAST" });
  });
  it("leaves a plain key alone", () => {
    expect(parseNetbirdKey("A1B2C3D4-0000-4000-8000-000000000000")).toBeNull();
    expect(parseNetbirdKey("nbp_abc")).toBeNull();
  });
});
