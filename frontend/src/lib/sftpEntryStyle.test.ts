import { describe, expect, it } from "vitest";
import { entryStyle } from "./sftpEntryStyle";

const f = (name: string, owner = "alice", is_dir = false) => ({ name, owner, is_dir });

describe("entryStyle", () => {
  it("marks an interrupted transfer as partial and editor leftovers as temp", () => {
    expect(entryStyle(f("Downloads.rar.part"), "alice").tempLabel).toBe("partial");
    expect(entryStyle(f(".bashrc.swp"), "alice").tempLabel).toBe("temp");
    expect(entryStyle(f("notes.txt~"), "alice").tempLabel).toBe("temp");
    expect(entryStyle(f("report.txt"), "alice").temp).toBe(false);
  });

  it("never marks a directory as temp or typed", () => {
    const st = entryStyle(f("cache.tmp", "alice", true), "alice");
    expect(st.temp).toBe(false);
    expect(st.kind).toBe("");
  });

  it("gives a temp file no type colour, so .tar.gz.part reads as partial", () => {
    expect(entryStyle(f("logs.tar.gz.part"), "alice").kind).toBe("");
    expect(entryStyle(f("logs.tar.gz"), "alice").kind).toBe("arch");
  });

  it("classifies by extension and known dotfiles", () => {
    expect(entryStyle(f("cs.py"), "alice").kind).toBe("code");
    expect(entryStyle(f("dump.pcapng"), "alice").kind).toBe("cap");
    expect(entryStyle(f(".bashrc"), "alice").kind).toBe("cfg");
    expect(entryStyle(f("compose.yaml"), "alice").kind).toBe("cfg");
    expect(entryStyle(f("README"), "alice").kind).toBe("");
  });

  it("dims dotfiles", () => {
    expect(entryStyle(f(".ssh", "alice", true), "alice").hidden).toBe(true);
    expect(entryStyle(f("ssh"), "alice").hidden).toBe(false);
  });

  it("marks another owner only when both names are known", () => {
    expect(entryStyle(f("hldm.pcap", "tcpdump"), "alice").foreign).toBe(true);
    expect(entryStyle(f("a", "alice"), "alice").foreign).toBe(false);
    expect(entryStyle(f("a", ""), "alice").foreign).toBe(false);
    expect(entryStyle(f("a", "bob"), "").foreign).toBe(false);
  });
});
