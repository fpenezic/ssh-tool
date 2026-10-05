// Who uses a saved connection as a jump host (JumpHostSpec.connection_id).
// Pure, so it is testable without the stores module.

interface WithJump {
  jump_host?: { kind: string; chain?: any } | null;
}

// chainUses reports whether a jump-host override references connectionId on
// any of its hops.
export function chainUses(jh: WithJump["jump_host"], connectionId: string): boolean {
  let cur = jh?.kind === "chain" ? jh.chain : null;
  let guard = 0;
  while (cur && guard++ < 100) {
    if (cur.connection_id === connectionId) return true;
    cur = cur.via;
  }
  return false;
}

// jumpReferrers names the folders and connections whose own jump chain
// goes through connectionId.
export function jumpReferrers(
  connectionId: string,
  folders: Array<{ name: string; settings?: WithJump | null }>,
  connections: Array<{ id: string; name: string; overrides?: WithJump | null }>,
): string[] {
  const out: string[] = [];
  for (const f of folders) if (chainUses(f.settings?.jump_host, connectionId)) out.push(`folder ${f.name}`);
  for (const c of connections) {
    if (c.id !== connectionId && chainUses(c.overrides?.jump_host, connectionId)) out.push(c.name);
  }
  return out;
}
