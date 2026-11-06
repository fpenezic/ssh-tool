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
    // A last flag with no value does not fall back to an earlier one.
    expect(parseNetbirdKey("netbird up -k K --management-url https://old.example.com -m=")).toEqual({ key: "K" });
    // A shell comment is not part of the command.
    expect(parseNetbirdKey("netbird up --setup-key K --management-url https://vpn.example.com # --management-url https://backup.example.com")).toEqual({
      key: "K",
      management: "https://vpn.example.com",
    });
    expect(parseNetbirdKey(`netbird up -k "K#1"`)).toEqual({ key: "K#1" });
  });
  it("leaves a plain key alone", () => {
    expect(parseNetbirdKey("A1B2C3D4-0000-4000-8000-000000000000")).toBeNull();
    expect(parseNetbirdKey("nbp_abc")).toBeNull();
  });
});
