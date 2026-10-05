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

// ----- Tree marks: which hosts are bastions -----

interface MarkFolder { id: string; name: string; settings?: WithJump | null }
interface MarkConn {
  id: string;
  name: string;
  hostname: string;
  overrides?: (WithJump & { port?: number | null }) | null;
}
interface MarkEntry { external_id: string; name: string; hostname: string }

export function dynRef(folderId: string, externalId: string): string {
  return `dyn:${folderId}/${externalId}`;
}

// One folder or connection that jumps through a host.
export interface JumpUser {
  kind: "folder" | "connection";
  id: string;
  label: string; // "folder X", the connection name, "inventory folder X"
}

// bastionUsage maps every host that something jumps through to who does:
// key = saved connection id, or dynRef() for an inventory host; value =
// "folder X" / connection names / "inventory folder X". Hops count when
// they reference the host, or when a typed-in hop's address matches it
// (hostname + port for saved connections, hostname for inventory hosts).
// An inventory folder's own bastion route counts too.
export function bastionUsage(
  folders: MarkFolder[],
  connections: MarkConn[],
  dynEntries: Record<string, MarkEntry[] | undefined>,
  dynFolders: Record<string, { config?: Record<string, any> } | undefined>,
): Map<string, JumpUser[]> {
  const out = new Map<string, JumpUser[]>();
  const add = (key: string, who: JumpUser) => {
    const list = out.get(key);
    if (!list) out.set(key, [who]);
    else if (!list.some((w) => w.kind === who.kind && w.id === who.id)) list.push(who);
  };
  const byAddr = new Map<string, string[]>();
  for (const c of connections) {
    if (!c.hostname) continue;
    const k = `${c.hostname.toLowerCase()}:${c.overrides?.port ?? 22}`;
    byAddr.set(k, [...(byAddr.get(k) ?? []), c.id]);
  }
  const dynByHost = new Map<string, string[]>();
  for (const [fid, list] of Object.entries(dynEntries)) {
    for (const e of list ?? []) {
      if (!e.hostname || !e.external_id) continue;
      const k = e.hostname.toLowerCase();
      dynByHost.set(k, [...(dynByHost.get(k) ?? []), dynRef(fid, e.external_id)]);
    }
  }
  const walk = (jh: WithJump["jump_host"], who: JumpUser, selfId?: string) => {
    let cur = jh?.kind === "chain" ? jh.chain : null;
    let guard = 0;
    while (cur && guard++ < 100) {
      if (cur.connection_id) {
        if (cur.connection_id !== selfId) add(cur.connection_id, who);
      } else if (cur.hostname) {
        const host = String(cur.hostname).toLowerCase();
        for (const id of byAddr.get(`${host}:${cur.port ?? 22}`) ?? []) if (id !== selfId) add(id, who);
        for (const ref of dynByHost.get(host) ?? []) add(ref, who);
      }
      cur = cur.via;
    }
  };
  const folderName = new Map(folders.map((f) => [f.id, f.name]));
  for (const f of folders) walk(f.settings?.jump_host, { kind: "folder", id: f.id, label: `folder ${f.name}` });
  for (const c of connections) walk(c.overrides?.jump_host, { kind: "connection", id: c.id, label: c.name }, c.id);
  for (const [fid, df] of Object.entries(dynFolders)) {
    const ext = df?.config?.bastion_external_id;
    const name = df?.config?.bastion_name;
    if (!ext && !name) continue;
    const e = (dynEntries[fid] ?? []).find((x) => (ext && x.external_id === ext) || (!ext && x.name === name));
    if (e) add(dynRef(fid, e.external_id), { kind: "folder", id: fid, label: `inventory folder ${folderName.get(fid) ?? fid}` });
  }
  return out;
}
