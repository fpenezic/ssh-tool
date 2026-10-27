// A switch over a tri-state setting ("" inherits, "on", "off"). It flips
// what the switch shows, and lands back on "" when the new state is the
// inherited one and the stored value was "" too: on and off again is no
// change, so Save does not light up for it.
export function flipTri(cur: string, saved: string, inherited: boolean): string {
  const want = !(cur === "" ? inherited : cur === "on");
  if (saved === "" && want === inherited) return "";
  return want ? "on" : "off";
}
