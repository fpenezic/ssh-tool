// Tag filter for the connections tree.
//
// Two independent axes:
//
//   1. **Plain tags** - free-form labels the user assigned in the
//      connection editor. OR semantics: a connection matches if any
//      of its tags is in the active set.
//
//   2. **Auto facets** - derived from the connection itself: auth
//      kind, username, first jump hop, port. The user doesn't type
//      these; they appear as pills automatically whenever the
//      underlying data is present. AND across distinct facet keys,
//      OR within a single facet (so `auth=opkssh OR auth=password`
//      AND `user=root` is expressible).
//
// Connection visibility = plain-tag check AND facet check. A folder
// is visible whenever at least one descendant connection passes.
//
// Active state persisted in the settings table - both axes survive
// restart.

import { api, type Connection, type Folder } from "./api";
import { tree, credentials } from "./stores.svelte";

const KEY = "tree_active_tags";
const FACETS_KEY = "tree_active_facets";

// Facets we derive from a connection. Ordered for stable rendering.
export const FACET_KEYS = ["auth", "user", "via", "port"] as const;
export type FacetKey = (typeof FACET_KEYS)[number];

class TagFilterStore {
  // Plain user-typed tags (OR within set).
  active = $state<Set<string>>(new Set());
  // Auto facets - map of facet key to set of accepted values
  // (OR within a key). The map is rebuilt on every persist so
  // $state's shallow reactivity picks up changes; entries are
  // never mutated in place.
  activeFacets = $state<Map<FacetKey, Set<string>>>(new Map());

  private loaded = false;

  async load() {
    if (this.loaded) return;
    try {
      const raw = await api.settingsGet(KEY);
      if (raw) this.active = new Set(raw.split(",").filter(Boolean));
    } catch { /* missing key - empty */ }
    try {
      const raw = await api.settingsGet(FACETS_KEY);
      if (raw) {
        // Format: "auth=opkssh,password;user=root;via=bastion1"
        const map = new Map<FacetKey, Set<string>>();
        for (const part of raw.split(";")) {
          if (!part) continue;
          const eq = part.indexOf("=");
          if (eq < 0) continue;
          const k = part.slice(0, eq) as FacetKey;
          if (!FACET_KEYS.includes(k)) continue;
          const vals = part.slice(eq + 1).split(",").filter(Boolean);
          if (vals.length) map.set(k, new Set(vals));
        }
        this.activeFacets = map;
      }
    } catch { /* missing key */ }
    this.loaded = true;
  }

  // ---------- Plain tags ----------

  toggle(tag: string) {
    const next = new Set(this.active);
    if (next.has(tag)) next.delete(tag);
    else next.add(tag);
    this.active = next;
    this.persist();
  }

  clear() {
    let dirty = false;
    if (this.active.size > 0) { this.active = new Set(); dirty = true; }
    if (this.activeFacets.size > 0) { this.activeFacets = new Map(); dirty = true; }
    if (dirty) this.persist();
  }

  // ---------- Facets ----------

  toggleFacet(key: FacetKey, value: string) {
    const next = new Map(this.activeFacets);
    const cur = new Set(next.get(key) ?? []);
    if (cur.has(value)) cur.delete(value);
    else cur.add(value);
    if (cur.size === 0) next.delete(key);
    else next.set(key, cur);
    this.activeFacets = next;
    this.persist();
  }

  isFacetActive(key: FacetKey, value: string): boolean {
    return this.activeFacets.get(key)?.has(value) ?? false;
  }

  // Derive facet values for one connection. Walks the folder chain
  // when the field isn't set on the connection's overrides so the
  // pill matches what the user sees in the editor / resolved
  // settings preview.
  connectionFacets(conn: Connection): Map<FacetKey, string[]> {
    const out = new Map<FacetKey, string[]>();
    const resolved = resolveLite(conn);

    // auth - derived from the resolved credential kind.
    if (resolved.authRef) {
      const cred = credentials.byId(resolved.authRef);
      if (cred?.kind) out.set("auth", [cred.kind]);
    }

    // user - resolved username (connection overrides win, else folder
    // chain, else credential's default_username).
    if (resolved.username) out.set("user", [resolved.username]);

    // via - first hop of the resolved jump chain. Inherited chains
    // bubble up too, so we look at folder ancestors.
    const firstHop = resolved.firstJumpHostname;
    if (firstHop) out.set("via", [firstHop]);

    // port - connection-level override (folder default skipped - too
    // noisy to facet every conn by its inherited port).
    if (conn.overrides?.port) out.set("port", [String(conn.overrides.port)]);

    return out;
  }

  // Catalog every facet present across the tree, plus the count of
  // connections wearing each value. Used to render the facet pill
  // row.
  allFacets(): Map<FacetKey, Array<{ value: string; count: number }>> {
    const acc = new Map<FacetKey, Map<string, number>>();
    for (const conn of tree.connections) {
      const f = this.connectionFacets(conn);
      for (const [k, vals] of f) {
        let inner = acc.get(k);
        if (!inner) { inner = new Map(); acc.set(k, inner); }
        for (const v of vals) inner.set(v, (inner.get(v) ?? 0) + 1);
      }
    }
    // Sort: count desc, then value asc.
    const out = new Map<FacetKey, Array<{ value: string; count: number }>>();
    for (const k of FACET_KEYS) {
      const m = acc.get(k);
      if (!m) continue;
      const arr = [...m.entries()]
        .map(([value, count]) => ({ value, count }))
        .sort((a, b) => (b.count - a.count) || a.value.localeCompare(b.value));
      out.set(k, arr);
    }
    return out;
  }

  // ---------- Filter predicates (used by Sidebar / tree) ----------

  isFilterActive(): boolean {
    return this.active.size > 0 || this.activeFacets.size > 0;
  }

  connectionMatches(connId: string): boolean {
    if (!this.isFilterActive()) return true;
    const c = tree.connectionById(connId);
    if (!c) return false;
    return this.passesAll(c);
  }

  // Folder is visible if any descendant matches. Same recursive
  // walk as before.
  folderHasMatch(folderId: string): boolean {
    if (!this.isFilterActive()) return true;
    for (const c of tree.connectionsIn(folderId)) {
      if (this.passesAll(c)) return true;
    }
    for (const sub of tree.childrenOf(folderId)) {
      if (this.folderHasMatch(sub.id)) return true;
    }
    return false;
  }

  // ---------- internals ----------

  private passesAll(c: Connection): boolean {
    if (this.active.size > 0) {
      const ok = c.tags.some((t) => this.active.has(t));
      if (!ok) return false;
    }
    if (this.activeFacets.size > 0) {
      const f = this.connectionFacets(c);
      // AND across keys, OR within key.
      for (const [k, accepted] of this.activeFacets) {
        const vals = f.get(k);
        if (!vals || !vals.some((v) => accepted.has(v))) return false;
      }
    }
    return true;
  }

  private persist() {
    const plain = [...this.active].join(",");
    const facets = [...this.activeFacets]
      .map(([k, vals]) => `${k}=${[...vals].join(",")}`)
      .join(";");
    const tasks: Promise<unknown>[] = [
      plain ? api.settingsSet(KEY, plain) : api.settingsDelete(KEY),
      facets ? api.settingsSet(FACETS_KEY, facets) : api.settingsDelete(FACETS_KEY),
    ];
    Promise.allSettled(tasks).catch(() => {});
  }
}

// Lightweight resolver - folder chain walk for username / port /
// auth_ref / first jump hop. Done client-side because we want a
// pill row that updates instantly when the user changes a folder
// default; round-tripping every conn to the Go resolver is
// overkill for facet display.
function resolveLite(c: Connection): {
  username?: string;
  authRef?: string;
  firstJumpHostname?: string;
} {
  let username = c.overrides?.username ?? undefined;
  let authRef = c.overrides?.auth_ref ?? undefined;
  let jumpChain = c.overrides?.jump_host ?? undefined;

  // Walk folder ancestors only for unset fields.
  let folder: Folder | null = c.folder_id ? tree.folderById(c.folder_id) : null;
  let guard = 0;
  while (folder && guard++ < 20) {
    const s = folder.settings ?? {};
    if (!username && s.username) username = s.username;
    if (!authRef && s.auth_ref) authRef = s.auth_ref;
    if (!jumpChain && s.jump_host) jumpChain = s.jump_host;
    folder = folder.parent_id ? tree.folderById(folder.parent_id) : null;
  }

  // Credential's default_username is the lowest-priority fallback
  // for user - mirrors the Go resolver and the SSH layer.
  if (!username && authRef) {
    const cred = credentials.byId(authRef);
    if (cred?.default_username) username = cred.default_username;
  }

  let firstJumpHostname: string | undefined;
  if (jumpChain?.kind === "chain" && jumpChain.chain?.hostname) {
    firstJumpHostname = jumpChain.chain.hostname;
  }

  return { username, authRef, firstJumpHostname };
}

export const tagFilter = new TagFilterStore();
