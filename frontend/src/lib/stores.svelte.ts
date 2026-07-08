import { api, type Folder, type Connection, type CredentialRef, type CredentialFolder, type InheritableSettings } from "./api";
import { expandedConnections, expandedCredentials } from "./treeState.svelte";
import { tagFilter } from "./tagFilter.svelte.ts";
import { resolveColorTag } from "./palette";
import { terminalPrefs } from "./terminalPrefs.svelte.ts";
import { focusActiveTerminal } from "./terminalFocus";
import { takeNetworkVia } from "./networkVia";

class TreeStore {
  folders = $state<Folder[]>([]);
  connections = $state<Connection[]>([]);
  // Dynamic-inventory folder side-data: provider + refresh state.
  // Indexed by folder_id. Only folders that have an entry here are
  // "dynamic". Renderers check membership to swap the icon / show
  // refresh button / disable manual children CRUD.
  dynamicFolders = $state<Record<string, {
    provider: string;
    refresh_seconds: number;
    last_pulled_at: number | null;
    last_error: string;
    config: Record<string, any>;
  }>>({});
  // Cached entries per dynamic folder - populated lazily when a
  // dynamic folder expands, refreshed via the dynamic_folder_refreshed
  // event from the backend.
  dynamicEntries = $state<Record<string, Array<{
    id: string;
    external_id: string;
    name: string;
    hostname: string;
    kind: string;
    status: string;
    tags: string[];
    raw?: any;
  }>>>({});
  loading = $state(false);
  error = $state<string | null>(null);
  // Bumped every load() - derived consumers that want a guaranteed
  // re-run after a refresh can read this and Svelte will register the
  // dependency. Belt-and-braces over the implicit $state array deps,
  // since class-method-wrapped reads sometimes get optimised away.
  version = $state(0);

  async load() {
    this.loading = true;
    this.error = null;
    try {
      const [folders, conns] = await Promise.all([
        api.foldersList(),
        api.connectionsList(),
      ]);
      this.folders = folders ?? [];
      this.connections = conns ?? [];
      // Pull the dynamic-folder side data in parallel. Errors swallowed
      // because the feature is optional: a fresh DB has no
      // dynamic_folders rows and the IPC simply returns [].
      try {
        const dyn = (await api.dynamicFoldersList()) ?? [];
        const m: typeof this.dynamicFolders = {};
        for (const d of dyn) {
          m[d.folder_id] = {
            provider: d.provider,
            refresh_seconds: d.refresh_seconds,
            last_pulled_at: d.last_pulled_at ?? null,
            last_error: d.last_error ?? "",
            config: d.config ?? {},
          };
        }
        this.dynamicFolders = m;
      } catch (e) {
        console.warn("dynamic folders load:", e);
        this.dynamicFolders = {};
      }
      this.version++;
      await expandedConnections.load();
      expandedConnections.prune(new Set(this.folders.map((f) => f.id)));
      await tagFilter.load();
    } catch (e) {
      this.error = String(e);
    } finally {
      this.loading = false;
    }
  }

  childrenOf(parentId: string | null): Folder[] {
    return this.folders
      .filter((f) => (f.parent_id ?? null) === parentId)
      .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
  }

  isDynamic(folderId: string): boolean {
    return !!this.dynamicFolders[folderId];
  }

  // Fetch entries for a dynamic folder on demand and cache them.
  // Idempotent: existing entries are replaced. Also refreshes the
  // folder's metadata (last_error, last_pulled_at) so the red `!`
  // dot disappears after a successful refresh - previously only
  // the entries array updated and the stale error sat there until
  // the next full tree.load().
  async loadDynamicEntries(folderId: string) {
    try {
      const list = (await api.dynamicEntriesList(folderId)) ?? [];
      this.dynamicEntries = { ...this.dynamicEntries, [folderId]: list };
      // Re-pull folder meta so last_error clears after a successful
      // refresh and last_pulled_at moves forward.
      try {
        const dyn = (await api.dynamicFoldersList()) ?? [];
        const m: typeof this.dynamicFolders = {};
        for (const d of dyn) {
          m[d.folder_id] = {
            provider: d.provider,
            refresh_seconds: d.refresh_seconds,
            last_pulled_at: d.last_pulled_at ?? null,
            last_error: d.last_error ?? "",
            config: d.config ?? {},
          };
        }
        this.dynamicFolders = m;
      } catch { /* keep stale meta if reload fails */ }
      this.version++;
    } catch (e) {
      console.warn("dynamic entries load:", folderId, e);
    }
  }

  connectionsIn(folderId: string | null): Connection[] {
    return this.connections
      .filter((c) => (c.folder_id ?? null) === folderId)
      .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
  }

  folderById(id: string | null): Folder | null {
    if (!id) return null;
    return this.folders.find((f) => f.id === id) ?? null;
  }

  connectionById(id: string | null): Connection | null {
    if (!id) return null;
    return this.connections.find((c) => c.id === id) ?? null;
  }

  // All distinct tags in the tree, with a count of how many connections
  // wear each. Sorted by count desc so the most-useful filter chips
  // surface first.
  allTags(): Array<{ tag: string; count: number }> {
    const m = new Map<string, number>();
    for (const c of this.connections) {
      for (const t of c.tags) m.set(t, (m.get(t) ?? 0) + 1);
    }
    return [...m.entries()]
      .map(([tag, count]) => ({ tag, count }))
      .sort((a, b) => (b.count - a.count) || a.tag.localeCompare(b.tag));
  }

  // Connections flagged favorite=true, sorted by name. Used by the
  // Quick access panel.
  favorites(): Connection[] {
    return this.connections
      .filter((c) => c.favorite)
      .sort((a, b) => a.name.localeCompare(b.name));
  }

  // Top N most-recently used connections (by last_used_at desc, ties by
  // name). Skips connections without a last_used_at so the panel
  // doesn't show "never-touched" entries.
  recent(limit: number): Connection[] {
    return this.connections
      .filter((c) => c.last_used_at != null)
      .sort((a, b) => {
        const at = a.last_used_at ?? 0;
        const bt = b.last_used_at ?? 0;
        if (at !== bt) return bt - at;
        return a.name.localeCompare(b.name);
      })
      .slice(0, limit);
  }

  // Flat list of every connection currently visible in the tree, in
  // render order (depth-first traversal honouring folder sort_order
  // and the expand state). Used by Shift+click to compute a range
  // that spans folders.
  //
  // Collapsed folders contribute nothing - selecting a connection
  // inside a hidden subtree would surprise the user.
  flatVisibleConnectionIds(predicate?: (connectionID: string) => boolean): string[] {
    const out: string[] = [];
    // Order must match TreeNode.svelte's render: at every level,
    // subfolders are rendered FIRST (their contents are rendered when
    // expanded), THEN sibling connections. A flat order mismatched
    // with rendering breaks Shift+click ranges across parent and
    // grandchild because indexOf(anchor) and indexOf(target) end up
    // swapped relative to the visual flow.
    //
    // Optional predicate lets the caller skip rows the active name /
    // tag filter has hidden - shift-click range should only walk
    // entries the user can actually see, not invisible ones between
    // anchor and target.
    const visit = (parentId: string | null) => {
      for (const f of this.childrenOf(parentId)) {
        if (expandedConnections.isExpanded(f.id)) visit(f.id);
      }
      for (const c of this.connectionsIn(parentId)) {
        if (predicate && !predicate(c.id)) continue;
        out.push(c.id);
      }
    };
    visit(null);
    return out;
  }

  // Same shape as flatVisibleConnectionIds but for folders - used by
  // Shift+click on a folder row. Folder rows are emitted in render
  // order: parent first, then descended subfolders (only if expanded).
  // Optional predicate filters by active name / tag filter so the
  // range walker doesn't pick up hidden folders.
  flatVisibleFolderIds(predicate?: (folderID: string) => boolean): string[] {
    const out: string[] = [];
    const visit = (parentId: string | null) => {
      for (const f of this.childrenOf(parentId)) {
        if (!predicate || predicate(f.id)) out.push(f.id);
        if (expandedConnections.isExpanded(f.id)) visit(f.id);
      }
    };
    visit(null);
    return out;
  }

  // Resolve color_tag through the folder ancestor chain. Connection
  // override wins; otherwise walk up parents until one has color_tag.
  // Returns empty string when nothing in the chain set a color.
  resolveColorForConnection(id: string): string {
    const c = this.connectionById(id);
    if (!c) return "";
    if (c.overrides?.color_tag) return resolveColorTag(c.overrides.color_tag);
    return this.resolveColorForFolder(c.folder_id ?? null);
  }

  resolveColorForFolder(id: string | null): string {
    let cur = this.folderById(id);
    let guard = 0;
    while (cur && guard++ < 10000) {
      if (cur.settings?.color_tag) return resolveColorTag(cur.settings.color_tag);
      cur = this.folderById(cur.parent_id ?? null);
    }
    return "";
  }

  // Frontend mirror of the backend's jump-host inheritance walk.
  // Used only for hover tooltips so the user can preview the chain
  // ("bastion1 -> bastion2 -> target") without opening the editor.
  // The connection.overrides.jump_host wins outright if set; clear
  // ("none") wins too; "inherit" falls back to ancestor folders.
  // Returns the resolved chain as a list of hostnames in order
  // bastion-first, target-last. Empty array = direct connection.
  // Inheritance source: for a per-field connection lookup, return
  // {value, fromFolder} where fromFolder is the folder whose
  // settings supplied the value (null when no ancestor set it).
  // Used by the detail editor to render hints like
  // "inherited from <folder>" right under each field.
  inheritedFieldForConnection<K extends keyof InheritableSettings>(
    connectionID: string,
    field: K,
  ): { value: InheritableSettings[K] | undefined; from: Folder | null } {
    const c = this.connectionById(connectionID);
    if (!c) return { value: undefined, from: null };
    let cur = this.folderById(c.folder_id ?? null);
    let guard = 0;
    while (cur && guard++ < 10000) {
      const v = cur.settings?.[field];
      if (v !== undefined && v !== null && v !== "") {
        return { value: v as InheritableSettings[K], from: cur };
      }
      cur = this.folderById(cur.parent_id ?? null);
    }
    return { value: undefined, from: null };
  }

  resolveJumpChainForConnection(id: string): string[] {
    const c = this.connectionById(id);
    if (!c) return [];
    const override = c.overrides?.jump_host;
    if (override) {
      if (override.kind === "none") return [];
      if (override.kind === "chain" && override.chain) {
        return jumpChainHostnames(override.chain);
      }
    }
    let cur = this.folderById(c.folder_id ?? null);
    let guard = 0;
    while (cur && guard++ < 10000) {
      const fjh = cur.settings?.jump_host;
      if (fjh) {
        if (fjh.kind === "none") return [];
        if (fjh.kind === "chain" && fjh.chain) {
          return jumpChainHostnames(fjh.chain);
        }
      }
      cur = this.folderById(cur.parent_id ?? null);
    }
    return [];
  }
}

// jumpChainHostnames flattens a JumpHostSpec linked list into a
// bastion-first array. The on-wire shape nests `via` for each
// additional hop, so a 2-bastion chain looks like:
//   { hostname: "b2", via: { hostname: "b1" } }
// which we render as ["b1", "b2"] (closest-to-target last).
function jumpChainHostnames(spec: { hostname?: string; via?: any } | undefined | null): string[] {
  if (!spec) return [];
  const out: string[] = [];
  // Walk via-chain first so the outermost bastion lands at the
  // start of the list.
  let cur: any = spec.via;
  while (cur) {
    if (cur.hostname) out.unshift(cur.hostname);
    cur = cur.via;
  }
  if (spec.hostname) out.push(spec.hostname);
  return out;
}

export const tree = new TreeStore();

class CredentialStore {
  folders = $state<CredentialFolder[]>([]);
  list = $state<CredentialRef[]>([]);
  loading = $state(false);
  error = $state<string | null>(null);

  async load() {
    this.loading = true;
    this.error = null;
    try {
      const [folders, creds] = await Promise.all([
        api.credentialFoldersList(),
        api.credentialsList(),
      ]);
      this.folders = folders ?? [];
      this.list = creds ?? [];
      await expandedCredentials.load();
      expandedCredentials.prune(new Set(this.folders.map((f) => f.id)));
    } catch (e) {
      this.error = String(e);
    } finally {
      this.loading = false;
    }
  }

  byId(id: string | null): CredentialRef | null {
    if (!id) return null;
    return this.list.find((c) => c.id === id) ?? null;
  }

  folderById(id: string | null): CredentialFolder | null {
    if (!id) return null;
    return this.folders.find((f) => f.id === id) ?? null;
  }

  foldersIn(parentId: string | null): CredentialFolder[] {
    return this.folders
      .filter((f) => (f.parent_id ?? null) === parentId)
      .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
  }

  credsIn(folderId: string | null): CredentialRef[] {
    return this.list
      .filter((c) => (c.folder_id ?? null) === folderId)
      .sort((a, b) => a.name.localeCompare(b.name));
  }

  // Depth-first list of every credential id currently visible in the
  // tree, honouring expand state. Used by Shift+click to compute a
  // range that crosses folder boundaries. Root credentials lead, then
  // each folder's contents in render order.
  flatVisibleCredentialIds(): string[] {
    const out: string[] = [];
    const visit = (parentId: string | null) => {
      for (const c of this.credsIn(parentId)) out.push(c.id);
      for (const f of this.foldersIn(parentId)) {
        if (expandedCredentials.isExpanded(f.id)) visit(f.id);
      }
    };
    visit(null);
    return out;
  }

  // Depth-first list of every credential FOLDER id visible in the
  // tree, honouring expand state. Shift+click range source for
  // folder multi-select.
  flatVisibleCredentialFolderIds(): string[] {
    const out: string[] = [];
    const visit = (parentId: string | null) => {
      for (const f of this.foldersIn(parentId)) {
        out.push(f.id);
        if (expandedCredentials.isExpanded(f.id)) visit(f.id);
      }
    };
    visit(null);
    return out;
  }

  // Full folder ancestry path like "DBs/Prod/MySQL" for a credential.
  // Empty string if the credential is at the root.
  folderPath(folderId: string | null): string {
    if (!folderId) return "";
    const parts: string[] = [];
    let cur = this.folderById(folderId);
    let guard = 0;
    while (cur && guard++ < 10000) {
      parts.unshift(cur.name);
      cur = this.folderById(cur.parent_id ?? null);
    }
    return parts.join("/");
  }

  // Flatten into a sorted list grouped by folder for dropdown rendering.
  // Each item has the cred + a display label that mimics the tree:
  //   "rootcred - password"
  //   "  DBs/Prod/MySQL - password"
  // The "  " indent helps the eye anchor to siblings under the same
  // folder; <optgroup> would be cleaner but doesn't nest.
  flatGrouped(): Array<{ cred: CredentialRef; label: string }> {
    // Sort: root first; then by folder path; within a folder by name.
    const decorated = this.list.map((c) => ({
      cred: c,
      path: this.folderPath(c.folder_id ?? null),
    }));
    decorated.sort((a, b) => {
      if (a.path !== b.path) return a.path.localeCompare(b.path);
      return a.cred.name.localeCompare(b.cred.name);
    });
    return decorated.map(({ cred, path }) => ({
      cred,
      label: path
        ? `${path} / ${cred.name} - ${cred.kind}`
        : `${cred.name} - ${cred.kind}`,
    }));
  }
}

export const credentials = new CredentialStore();

type Selection =
  | { kind: "none" }
  | { kind: "folder"; id: string }
  | { kind: "connection"; id: string }
  | { kind: "credential"; id: string }
  | { kind: "credentialFolder"; id: string }
  | { kind: "dynamicEntry"; folderId: string; entryId: string };

class SelectionStore {
  current = $state<Selection>({ kind: "none" });
  // Extras live in two separate buckets so we can keep folder vs.
  // connection multi-selects homogeneous: when the anchor switches type,
  // we clear the buckets to avoid mixed batch operations. (Folders can
  // host connections; mass-deleting a "folders + some loose conns"
  // selection has unobvious semantics, so we just refuse.)
  extras = $state<Set<string>>(new Set());         // connection ids
  folderExtras = $state<Set<string>>(new Set());   // folder ids
  credentialExtras = $state<Set<string>>(new Set());       // credential ids
  credentialFolderExtras = $state<Set<string>>(new Set()); // credential folder ids
  // Multi-select for dynamic-inventory entries. Keyed as
  // "<folderId>:<entryId>" so the set can hold entries from a
  // single folder cleanly (we don't currently mix multi-folder
  // dynamic selection; if it ever happens, the bulk actions need
  // to look up the right (folderId, entryId) pair from the key).
  dynamicExtras = $state<Set<string>>(new Set());

  select(s: Selection) {
    this.current = s;
    this.extras = new Set();
    this.folderExtras = new Set();
    this.credentialExtras = new Set();
    this.credentialFolderExtras = new Set();
    this.dynamicExtras = new Set();
  }

  static dynKey(folderId: string, entryId: string): string {
    return folderId + ":" + entryId;
  }
  static parseDynKey(key: string): { folderId: string; entryId: string } {
    const i = key.indexOf(":");
    return { folderId: key.slice(0, i), entryId: key.slice(i + 1) };
  }

  // Ctrl/Cmd+click on a dynamic entry: toggle it in/out of the set.
  toggleDynamic(folderId: string, entryId: string) {
    const key = SelectionStore.dynKey(folderId, entryId);
    if (this.current.kind === "dynamicEntry" &&
        this.current.folderId === folderId && this.current.entryId === entryId) {
      // Removing the anchor: promote first extra.
      const first = this.dynamicExtras.values().next().value;
      if (first) {
        const next = new Set(this.dynamicExtras);
        next.delete(first);
        this.dynamicExtras = next;
        const p = SelectionStore.parseDynKey(first);
        this.current = { kind: "dynamicEntry", folderId: p.folderId, entryId: p.entryId };
      } else {
        this.current = { kind: "none" };
      }
      return;
    }
    const next = new Set(this.dynamicExtras);
    if (next.has(key)) {
      next.delete(key);
    } else {
      if (this.current.kind !== "dynamicEntry") {
        this.current = { kind: "dynamicEntry", folderId, entryId };
        return;
      }
      // Anchor stays; this becomes an extra.
      next.add(key);
    }
    this.dynamicExtras = next;
  }

  // Shift+click on a dynamic entry: select a range within the same
  // folder, using orderedEntryIds as the visual order. If the anchor
  // isn't a dynamic entry of the same folder, fall back to single-
  // click semantics.
  rangeDynamic(folderId: string, entryId: string, orderedEntryIds: string[]) {
    if (this.current.kind !== "dynamicEntry" || this.current.folderId !== folderId) {
      this.select({ kind: "dynamicEntry", folderId, entryId });
      return;
    }
    const anchor = this.current.entryId;
    const i = orderedEntryIds.indexOf(anchor);
    const j = orderedEntryIds.indexOf(entryId);
    if (i < 0 || j < 0) {
      this.select({ kind: "dynamicEntry", folderId, entryId });
      return;
    }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set<string>();
    for (let k = lo; k <= hi; k++) {
      if (k === i) continue;
      next.add(SelectionStore.dynKey(folderId, orderedEntryIds[k]));
    }
    this.dynamicExtras = next;
  }

  // All currently-selected dynamic entries (anchor + extras), as
  // (folderId, entryId) pairs. Used by bulk-connect / batch-exec.
  selectedDynamicEntries(): Array<{ folderId: string; entryId: string }> {
    const out: Array<{ folderId: string; entryId: string }> = [];
    if (this.current.kind === "dynamicEntry") {
      out.push({ folderId: this.current.folderId, entryId: this.current.entryId });
    }
    for (const key of this.dynamicExtras) {
      const p = SelectionStore.parseDynKey(key);
      out.push(p);
    }
    return out;
  }

  // Single-click on a connection: clear multi, anchor here.
  selectConnection(id: string) {
    this.select({ kind: "connection", id });
  }

  // Ctrl/Cmd+click: toggle one connection in/out of the multi-set.
  toggleConnection(id: string) {
    if (this.current.kind === "connection" && this.current.id === id) {
      // Removing the anchor: promote first extra (if any) to anchor.
      const first = this.extras.values().next().value;
      if (first) {
        const next = new Set(this.extras);
        next.delete(first);
        this.extras = next;
        this.current = { kind: "connection", id: first };
      } else {
        this.current = { kind: "none" };
      }
      return;
    }
    const next = new Set(this.extras);
    if (next.has(id)) {
      next.delete(id);
    } else {
      // First multi-click without an anchor: become anchor.
      if (this.current.kind !== "connection") {
        this.current = { kind: "connection", id };
        return;
      }
      next.add(id);
    }
    this.extras = next;
  }

  // Shift+click: select range between anchor and `id` within the same folder.
  // If anchor isn't a connection, behave like single-click.
  rangeConnection(id: string, orderedIds: string[]) {
    if (this.current.kind !== "connection") {
      this.select({ kind: "connection", id });
      return;
    }
    const anchor = this.current.id;
    const i = orderedIds.indexOf(anchor);
    const j = orderedIds.indexOf(id);
    if (i < 0 || j < 0) {
      // Fallback: cross-folder range not supported, just toggle.
      this.toggleConnection(id);
      return;
    }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set<string>();
    for (let k = lo; k <= hi; k++) {
      if (orderedIds[k] !== anchor) next.add(orderedIds[k]);
    }
    this.extras = next;
  }

  // All selected connection IDs (anchor + extras), de-duplicated.
  selectedConnectionIds(): string[] {
    const out: string[] = [];
    if (this.current.kind === "connection") out.push(this.current.id);
    for (const id of this.extras) {
      if (!out.includes(id)) out.push(id);
    }
    return out;
  }

  isConnectionSelected(id: string): boolean {
    if (this.current.kind === "connection" && this.current.id === id) return true;
    return this.extras.has(id);
  }

  multiCount(): number {
    return this.selectedConnectionIds().length;
  }

  // ----- folder multi-select (mirrors the connection helpers) -----

  selectFolderById(id: string) {
    this.select({ kind: "folder", id });
  }

  toggleFolder(id: string) {
    if (this.current.kind === "folder" && this.current.id === id) {
      const first = this.folderExtras.values().next().value;
      if (first) {
        const next = new Set(this.folderExtras);
        next.delete(first);
        this.folderExtras = next;
        this.current = { kind: "folder", id: first };
      } else {
        this.current = { kind: "none" };
      }
      return;
    }
    const next = new Set(this.folderExtras);
    if (next.has(id)) {
      next.delete(id);
    } else {
      if (this.current.kind !== "folder") {
        this.current = { kind: "folder", id };
        // Clear connection extras since we're switching modes.
        this.extras = new Set();
        return;
      }
      next.add(id);
    }
    this.folderExtras = next;
  }

  selectedFolderIds(): string[] {
    const out: string[] = [];
    if (this.current.kind === "folder") out.push(this.current.id);
    for (const id of this.folderExtras) {
      if (!out.includes(id)) out.push(id);
    }
    return out;
  }

  isFolderSelected(id: string): boolean {
    if (this.current.kind === "folder" && this.current.id === id) return true;
    return this.folderExtras.has(id);
  }

  folderMultiCount(): number {
    return this.selectedFolderIds().length;
  }

  // Shift+click range across the visible folder list. Mirrors
  // rangeConnection: anchor is the current folder selection, target
  // is `id`, and orderedIds is the depth-first flat list from
  // tree.flatVisibleFolderIds(). Falls back to plain select when the
  // anchor isn't a folder.
  rangeFolder(id: string, orderedIds: string[]) {
    if (this.current.kind !== "folder") {
      this.select({ kind: "folder", id });
      return;
    }
    const anchor = this.current.id;
    const i = orderedIds.indexOf(anchor);
    const j = orderedIds.indexOf(id);
    if (i < 0 || j < 0) {
      this.toggleFolder(id);
      return;
    }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set<string>();
    for (let k = lo; k <= hi; k++) {
      if (orderedIds[k] !== anchor) next.add(orderedIds[k]);
    }
    this.folderExtras = next;
  }

  // ----- credential multi-select -----

  selectCredentialById(id: string) { this.select({ kind: "credential", id }); }

  toggleCredential(id: string) {
    if (this.current.kind === "credential" && this.current.id === id) {
      const first = this.credentialExtras.values().next().value;
      if (first) {
        const next = new Set(this.credentialExtras);
        next.delete(first);
        this.credentialExtras = next;
        this.current = { kind: "credential", id: first };
      } else {
        this.current = { kind: "none" };
      }
      return;
    }
    const next = new Set(this.credentialExtras);
    if (next.has(id)) next.delete(id);
    else {
      if (this.current.kind !== "credential") {
        this.current = { kind: "credential", id };
        this.credentialFolderExtras = new Set();
        return;
      }
      next.add(id);
    }
    this.credentialExtras = next;
  }

  selectedCredentialIds(): string[] {
    const out: string[] = [];
    if (this.current.kind === "credential") out.push(this.current.id);
    for (const id of this.credentialExtras) if (!out.includes(id)) out.push(id);
    return out;
  }

  isCredentialSelected(id: string): boolean {
    if (this.current.kind === "credential" && this.current.id === id) return true;
    return this.credentialExtras.has(id);
  }

  credentialMultiCount(): number {
    return this.selectedCredentialIds().length;
  }

  rangeCredential(id: string, orderedIds: string[]) {
    if (this.current.kind !== "credential") {
      this.select({ kind: "credential", id });
      return;
    }
    const anchor = this.current.id;
    const i = orderedIds.indexOf(anchor);
    const j = orderedIds.indexOf(id);
    if (i < 0 || j < 0) {
      this.toggleCredential(id);
      return;
    }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set<string>();
    for (let k = lo; k <= hi; k++) {
      if (orderedIds[k] !== anchor) next.add(orderedIds[k]);
    }
    this.credentialExtras = next;
  }

  // ----- credential-folder multi-select (mirrors the folder helpers) -----

  toggleCredentialFolder(id: string) {
    if (this.current.kind === "credentialFolder" && this.current.id === id) {
      const first = this.credentialFolderExtras.values().next().value;
      if (first) {
        const next = new Set(this.credentialFolderExtras);
        next.delete(first);
        this.credentialFolderExtras = next;
        this.current = { kind: "credentialFolder", id: first };
      } else {
        this.current = { kind: "none" };
      }
      return;
    }
    const next = new Set(this.credentialFolderExtras);
    if (next.has(id)) {
      next.delete(id);
    } else {
      if (this.current.kind !== "credentialFolder") {
        this.current = { kind: "credentialFolder", id };
        // Keep the multi-select homogeneous - switching to folder
        // mode drops any credential extras.
        this.credentialExtras = new Set();
        return;
      }
      next.add(id);
    }
    this.credentialFolderExtras = next;
  }

  selectedCredentialFolderIds(): string[] {
    const out: string[] = [];
    if (this.current.kind === "credentialFolder") out.push(this.current.id);
    for (const id of this.credentialFolderExtras) if (!out.includes(id)) out.push(id);
    return out;
  }

  isCredentialFolderSelected(id: string): boolean {
    if (this.current.kind === "credentialFolder" && this.current.id === id) return true;
    return this.credentialFolderExtras.has(id);
  }

  credentialFolderMultiCount(): number {
    return this.selectedCredentialFolderIds().length;
  }

  rangeCredentialFolder(id: string, orderedIds: string[]) {
    if (this.current.kind !== "credentialFolder") {
      this.select({ kind: "credentialFolder", id });
      return;
    }
    const anchor = this.current.id;
    const i = orderedIds.indexOf(anchor);
    const j = orderedIds.indexOf(id);
    if (i < 0 || j < 0) {
      this.toggleCredentialFolder(id);
      return;
    }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set<string>();
    for (let k = lo; k <= hi; k++) {
      if (orderedIds[k] !== anchor) next.add(orderedIds[k]);
    }
    this.credentialFolderExtras = next;
  }

  selectedFolder(): Folder | null {
    if (this.current.kind !== "folder") return null;
    return tree.folderById(this.current.id);
  }

  selectedConnection(): Connection | null {
    if (this.current.kind !== "connection") return null;
    return tree.connectionById(this.current.id);
  }

  selectedCredential(): CredentialRef | null {
    if (this.current.kind !== "credential") return null;
    return credentials.byId(this.current.id);
  }

  selectedCredentialFolder(): CredentialFolder | null {
    if (this.current.kind !== "credentialFolder") return null;
    return credentials.folderById(this.current.id);
  }

  selectDynamicEntry(folderId: string, entryId: string) {
    this.select({ kind: "dynamicEntry", folderId, entryId });
  }

  isDynamicEntrySelected(folderId: string, entryId: string): boolean {
    if (this.current.kind === "dynamicEntry"
        && this.current.folderId === folderId
        && this.current.entryId === entryId) {
      return true;
    }
    return this.dynamicExtras.has(SelectionStore.dynKey(folderId, entryId));
  }
}

export const selection = new SelectionStore();

type ViewTab = "connections" | "credentials" | "terminal" | "settings";

class ViewStore {
  tab = $state<ViewTab>("connections");
  // pendingSettingsSection is consumed by Settings.svelte to jump to
  // a specific section when the user enters Settings from a deep
  // link (e.g. the status-bar version pill → About). Settings picks
  // it up via $effect, applies it, then nulls it back out so the
  // next plain "open Settings" lands on the user's last section.
  pendingSettingsSection = $state<string | null>(null);

  // Set by reveal() to ask the connections Sidebar to scroll a row
  // into view once the tree has re-rendered with the ancestors
  // expanded. Sidebar consumes it in an $effect and nulls it back out.
  pendingTreeReveal = $state<{ kind: "folder" | "connection"; id: string } | null>(null);

  // Jump from elsewhere (e.g. a credential's "Used by" list) to a
  // connection or folder in the connections tree: switch to that
  // view, expand the ancestor folders so the row is rendered, select
  // it, and queue a scroll-into-view. Lazy imports avoid a circular
  // module ref at the top of the file.
  reveal(kind: "folder" | "connection", id: string) {
    this.tab = "connections";
    // Expand every ancestor folder so the target row exists in the DOM.
    let folderId: string | null;
    if (kind === "connection") {
      const c = tree.connectionById(id);
      folderId = c?.folder_id ?? null;
      if (c) selection.select({ kind: "connection", id });
    } else {
      const f = tree.folderById(id);
      folderId = f?.parent_id ?? null;
      if (f) selection.select({ kind: "folder", id });
    }
    for (let i = 0; i < 10000 && folderId; i++) {
      expandedConnections.set(folderId, true);
      folderId = tree.folderById(folderId)?.parent_id ?? null;
    }
    this.pendingTreeReveal = { kind, id };
  }

  setTab(t: ViewTab) {
    this.tab = t;
    if (t === "connections" && selection.current.kind === "credential") {
      selection.select({ kind: "none" });
    } else if (
      t === "credentials" &&
      (selection.current.kind === "folder" ||
        selection.current.kind === "connection" ||
        selection.current.kind === "dynamicEntry")
    ) {
      selection.select({ kind: "none" });
    }
  }

  // Deep-link helper: jump to Settings and request a specific
  // section. Settings restores the section from settings_active_section
  // on its own mount; this stays valid through subsequent re-mounts
  // because we also persist the pin to that setting.
  setTabSettingsSection(section: string) {
    this.pendingSettingsSection = section;
    this.tab = "settings";
  }
}

export const view = new ViewStore();

export interface SessionTab {
  sessionId: string;
  connectionId: string;
  name: string;
  hostname: string;
  // Differentiates SSH and local-shell sessions so the Terminal
  // component knows which IPCs to call for write / resize / scrollback
  // and so panel toolbars hide SSH-only controls (broadcast, port
  // forwards, SFTP, opkssh refresh) on local tabs. Defaults to "ssh"
  // for backward compatibility.
  kind?: "ssh" | "local" | "vnc";
  status: "connecting" | "connected" | "disconnected" | "error" | "reconnecting";
  statusHint?: string;
  // While status === "reconnecting", the most recent retry attempt and
  // the delay before the *next* attempt fires. Used to render the
  // pane-title countdown.
  reconnectAttempt?: number;
  reconnectMaxAttempts?: number;
  reconnectDelay?: number;
  // Wall-clock timestamp (ms) when the session first reported
  // "connected". Used by the optional tab uptime indicator. Cleared
  // on disconnect.
  connectedAt?: number;
  // Name of the WireGuard network profile the first hop ACTUALLY
  // dialed through. Unset for plain dials and when an auto/paused
  // policy went direct. Drives the pane VPN badge.
  networkVia?: string;
}

// PaneNode is a binary tree: each tab has a root node which is either a
// leaf (one terminal) or a split (two children oriented horizontally or
// vertically with a resize ratio in [0,1]).
export type PaneNode = PaneLeaf | PaneSplit;

export interface PaneLeaf {
  kind: "pane";
  id: string;       // unique within the tab; used as a focus key
  sessionId: string;
  // What's rendered in this leaf. terminal/sftp share one SSH session
  // (the SFTP client layers on top of the existing ssh.Client), so
  // toggling doesn't reconnect. "vnc" renders a noVNC console - it never
  // splits or toggles (the tab is locked to a single full leaf).
  view?: "terminal" | "sftp" | "vnc";
}

export interface PaneSplit {
  kind: "split";
  id: string;
  direction: "horizontal" | "vertical"; // horizontal = side-by-side
  ratio: number;    // 0..1, fraction taken by `a`
  a: PaneNode;
  b: PaneNode;
}

export interface PaneTab {
  tabId: string;          // stable per tab so the bar doesn't lose track
  title: string;          // displayed in the tab bar
  rootPaneId: string;     // initial leaf id, kept for activate-on-click
  root: PaneNode;
  activePaneId: string;
  // Optional grouping metadata - set when a tab belongs to a saved
  // workspace, or when the user manually groups tabs together. groupName
  // shows as a small chip in the tab bar; groupColor tints the row.
  groupName?: string;
  groupColor?: string;
  // locked tabs refuse splits and view toggles. Set for VNC consoles:
  // a remote desktop wants the whole tab, never an SFTP-style side pane.
  locked?: boolean;
}

import { EventsOn } from "./wailsRuntime";

function genId(prefix: string): string {
  return prefix + "_" + Math.random().toString(36).slice(2, 10);
}

// PaneTreeStore tracks per-tab pane layouts. Lives alongside SessionStore;
// when a tab is added we create a single-leaf tree. Splits and replacements
// happen in-place.
class PaneTreeStore {
  tabs = $state<PaneTab[]>([]);
  activeTabId = $state<string | null>(null);

  addTab(sessionId: string, title: string): PaneTab {
    const leafId = genId("pane");
    const tab: PaneTab = {
      tabId: genId("tab"),
      title,
      rootPaneId: leafId,
      root: { kind: "pane", id: leafId, sessionId },
      activePaneId: leafId,
    };
    this.tabs.push(tab);
    this.activeTabId = tab.tabId;
    return tab;
  }

  // addVncTab adds a locked single-leaf VNC console tab. Locked = no
  // splits, no SFTP toggle; the leaf renders VncPane full-bleed.
  addVncTab(sessionId: string, title: string): PaneTab {
    const leafId = genId("pane");
    const tab: PaneTab = {
      tabId: genId("tab"),
      title,
      rootPaneId: leafId,
      root: { kind: "pane", id: leafId, sessionId, view: "vnc" },
      activePaneId: leafId,
      locked: true,
    };
    this.tabs.push(tab);
    this.activeTabId = tab.tabId;
    return tab;
  }

  // addTabFromLayout restores a previously serialized pane tree (e.g.
  // from a detach/redock round-trip). Regenerates tabId + every internal
  // pane/split id so the restored tab can't collide with existing ids
  // in this window. Sessions referenced in the layout must already
  // exist in SessionStore before this is called.
  addTabFromLayout(layout: SerializedPaneTab): PaneTab {
    const root = cloneWithFreshIds(layout.root);
    const firstLeaf = firstLeafIn(root);
    const tab: PaneTab = {
      tabId: genId("tab"),
      title: layout.title,
      rootPaneId: firstLeaf?.id ?? genId("pane"),
      root,
      activePaneId: firstLeaf?.id ?? "",
      groupName: layout.groupName,
      groupColor: layout.groupColor,
      locked: layout.locked,
    };
    this.tabs.push(tab);
    this.activeTabId = tab.tabId;
    return tab;
  }

  // moveTabBefore re-inserts tabId immediately before beforeTabId.
  // When beforeTabId is null, append to the end. No-op if the
  // result wouldn't change the order. Used by the tab-bar reorder
  // drag in TerminalArea.svelte.
  moveTabBefore(tabId: string, beforeTabId: string | null) {
    const cur = this.tabs;
    const fromIdx = cur.findIndex((t) => t.tabId === tabId);
    if (fromIdx < 0) return;
    const moving = cur[fromIdx];
    const without = cur.filter((_, i) => i !== fromIdx);
    let insertAt: number;
    if (beforeTabId === null) {
      insertAt = without.length;
    } else {
      insertAt = without.findIndex((t) => t.tabId === beforeTabId);
      if (insertAt < 0) insertAt = without.length;
    }
    const next = [...without.slice(0, insertAt), moving, ...without.slice(insertAt)];
    this.tabs = next;
  }

  removeTab(tabId: string) {
    this.tabs = this.tabs.filter((t) => t.tabId !== tabId);
    if (this.activeTabId === tabId) {
      this.activeTabId = this.tabs[this.tabs.length - 1]?.tabId ?? null;
    }
  }

  activateTab(tabId: string) {
    this.activeTabId = tabId;
  }

  // cycleActive moves the active tab pointer forward (delta=+1) or
  // backward (delta=-1) with wrap-around. No-op when zero or one tab.
  // Used by Ctrl+Tab / Ctrl+Shift+Tab.
  cycleActive(delta: 1 | -1): string | null {
    if (this.tabs.length <= 1) return this.activeTabId;
    const idx = this.tabs.findIndex((t) => t.tabId === this.activeTabId);
    if (idx < 0) {
      this.activeTabId = this.tabs[0].tabId;
      return this.activeTabId;
    }
    const next = (idx + delta + this.tabs.length) % this.tabs.length;
    this.activeTabId = this.tabs[next].tabId;
    return this.activeTabId;
  }

  // activateIndex jumps to the tab at position `idx` (0-based). Out-of-
  // range indices are ignored. Used by Ctrl+1..8 (and Ctrl+9 for the
  // last tab, handled by the caller).
  activateIndex(idx: number): boolean {
    if (idx < 0 || idx >= this.tabs.length) return false;
    this.activeTabId = this.tabs[idx].tabId;
    return true;
  }

  setTitle(tabId: string, title: string) {
    this.tabs = this.tabs.map((t) =>
      t.tabId === tabId ? { ...t, title } : t
    );
  }

  // Title of the currently active tab, or null when no tab is active.
  // Used to reflect the active connection in the OS window/taskbar title.
  activeTitle(): string | null {
    const t = this.tabs.find((t) => t.tabId === this.activeTabId);
    return t?.title ?? null;
  }

  setGroup(tabId: string, name: string | undefined, color: string | undefined) {
    this.tabs = this.tabs.map((t) =>
      t.tabId === tabId ? { ...t, groupName: name, groupColor: color } : t
    );
  }

  activePane(tabId: string): PaneLeaf | null {
    const t = this.tabs.find((x) => x.tabId === tabId);
    if (!t) return null;
    return findLeaf(t.root, t.activePaneId);
  }

  setPaneView(tabId: string, paneId: string, view: "terminal" | "sftp") {
    this.tabs = this.tabs.map((t) => {
      if (t.tabId !== tabId) return t;
      const newRoot = replaceLeaf(t.root, paneId, (leaf) => ({ ...leaf, view }));
      return { ...t, root: newRoot };
    });
  }

  // Split an existing leaf so the new sibling reuses the SAME session.
  // Used when we want a second view (e.g. SFTP) on the same connection
  // without firing a fresh sshConnect - the SFTP client layers on top of
  // the existing ssh.Client. The new leaf can have a different `view`.
  splitPaneShareSession(
    tabId: string,
    paneId: string,
    direction: "horizontal" | "vertical",
    view: "terminal" | "sftp",
    side: "a" | "b" = "b",
  ) {
    this.tabs = this.tabs.map((t) => {
      if (t.tabId !== tabId) return t;
      const newLeafId = genId("pane");
      const newRoot = replaceLeaf(t.root, paneId, (leaf) => {
        const newLeaf: PaneLeaf = {
          kind: "pane",
          id: newLeafId,
          sessionId: leaf.sessionId,
          view,
        };
        return {
          kind: "split",
          id: genId("split"),
          direction,
          ratio: 0.5,
          a: side === "a" ? newLeaf : leaf,
          b: side === "b" ? newLeaf : leaf,
        };
      });
      return { ...t, root: newRoot, activePaneId: newLeafId };
    });
  }

  setActivePane(tabId: string, paneId: string) {
    this.tabs = this.tabs.map((t) =>
      t.tabId === tabId ? { ...t, activePaneId: paneId } : t
    );
  }

  // Split the given pane along `direction`. The new pane starts with
  // `newSessionId`. `side` controls whether the new pane is placed as `a`
  // (left/top) or `b` (right/bottom, default).
  splitPane(tabId: string, paneId: string, direction: "horizontal" | "vertical", newSessionId: string, side: "a" | "b" = "b") {
    this.tabs = this.tabs.map((t) => {
      if (t.tabId !== tabId) return t;
      const newLeafId = genId("pane");
      const newLeaf: PaneLeaf = { kind: "pane", id: newLeafId, sessionId: newSessionId };
      const newRoot = replaceLeaf(t.root, paneId, (leaf) => ({
        kind: "split",
        id: genId("split"),
        direction,
        ratio: 0.5,
        a: side === "a" ? newLeaf : leaf,
        b: side === "b" ? newLeaf : leaf,
      }));
      return { ...t, root: newRoot, activePaneId: newLeafId };
    });
  }

  // Remove a pane from the tree. If the pane is the last one in the tab,
  // remove the tab. If the pane's sibling becomes the only child, collapse
  // the parent split.
  closePane(tabId: string, paneId: string): { tabRemoved: boolean } {
    const tab = this.tabs.find((t) => t.tabId === tabId);
    if (!tab) return { tabRemoved: false };
    if (tab.root.kind === "pane" && tab.root.id === paneId) {
      this.removeTab(tabId);
      return { tabRemoved: true };
    }
    const newRoot = removePane(tab.root, paneId);
    if (!newRoot) {
      this.removeTab(tabId);
      return { tabRemoved: true };
    }
    // Pick a new active pane: the first leaf we find.
    const firstLeaf = firstLeafIn(newRoot);
    this.tabs = this.tabs.map((t) =>
      t.tabId === tabId ? { ...t, root: newRoot, activePaneId: firstLeaf?.id ?? "" } : t
    );
    return { tabRemoved: false };
  }

  // Pop ONE pane out of a split tab into its own new tab, leaving the rest of
  // the split intact. Unlike ungroupTab (which splits every pane into its own
  // tab) this moves a single connection out. The session stays live - it just
  // moves to a new tab. No-op if the pane isn't in a split.
  movePaneToOwnTab(tabId: string, paneId: string) {
    const tab = this.tabs.find((t) => t.tabId === tabId);
    if (!tab || tab.root.kind !== "split") return;
    const leaf = findLeaf(tab.root, paneId);
    if (!leaf) return;
    const remaining = removePane(tab.root, paneId);
    if (!remaining) return; // shouldn't happen for a split with >1 leaf
    const firstLeaf = firstLeafIn(remaining);
    const newTab: PaneTab = {
      tabId: genId("tab"),
      title: leaf.sessionId,
      rootPaneId: leaf.id,
      root: leaf,
      activePaneId: leaf.id,
      // Carry the group label so a popped pane keeps its group.
      groupName: tab.groupName,
      groupColor: tab.groupColor,
    };
    this.tabs = [
      ...this.tabs.map((t) =>
        t.tabId === tabId ? { ...t, root: remaining, activePaneId: firstLeaf?.id ?? "" } : t
      ),
      newTab,
    ];
  }

  // Adjust the ratio of a split node.
  setSplitRatio(tabId: string, splitId: string, ratio: number) {
    const clamped = Math.max(0.1, Math.min(0.9, ratio));
    this.tabs = this.tabs.map((t) => {
      if (t.tabId !== tabId) return t;
      return { ...t, root: adjustRatio(t.root, splitId, clamped) };
    });
  }

  // Split a multi-pane tab into one tab per leaf pane.
  ungroupTab(tabId: string) {
    const tab = this.tabs.find((t) => t.tabId === tabId);
    if (!tab || tab.root.kind !== "split") return;

    const leaves: PaneLeaf[] = [];
    function collect(node: PaneNode) {
      if (node.kind === "pane") leaves.push(node);
      else { collect(node.a); collect(node.b); }
    }
    collect(tab.root);
    if (leaves.length <= 1) return;

    const [first, ...rest] = leaves;
    const newTabs: PaneTab[] = rest.map((leaf) => ({
      tabId: genId("tab"),
      title: leaf.sessionId,
      rootPaneId: leaf.id,
      root: leaf,
      activePaneId: leaf.id,
    }));

    this.tabs = [
      ...this.tabs.map((t) =>
        t.tabId === tabId
          ? { ...t, root: first, rootPaneId: first.id, activePaneId: first.id }
          : t
      ),
      ...newTabs,
    ];
  }

  // Remove the tab and return all sessionIds it contained.
  // Caller must call api.sshDisconnect + sessions.remove for each returned id.
  popTab(tabId: string): string[] {
    const tab = this.tabs.find((t) => t.tabId === tabId);
    if (!tab) return [];
    const sids: string[] = [];
    function collect(node: PaneNode) {
      if (node.kind === "pane") sids.push(node.sessionId);
      else { collect(node.a); collect(node.b); }
    }
    collect(tab.root);
    this.removeTab(tabId);
    return sids;
  }

  // Rewrite every leaf that references oldId to reference newId. Used
  // when a session is reconnected under a fresh backend id and the
  // pane tree should keep pointing at "the same logical pane".
  swapSessionId(oldId: string, newId: string) {
    function rewrite(node: PaneNode): PaneNode {
      if (node.kind === "pane") {
        return node.sessionId === oldId ? { ...node, sessionId: newId } : node;
      }
      return { ...node, a: rewrite(node.a), b: rewrite(node.b) };
    }
    this.tabs = this.tabs.map((t) => ({ ...t, root: rewrite(t.root) }));
  }

  // Replace the session referenced by one specific leaf node (by
  // leaf id, not session id) without touching any other leaf that
  // happens to share the same sessionId. Used by the "center drop"
  // gesture so that swapping into a pane doesn't accidentally
  // rewrite an unrelated SFTP-share split that points at the same
  // session.
  replaceLeafSession(tabId: string, paneId: string, newSessionId: string) {
    function rewrite(node: PaneNode): PaneNode {
      if (node.kind === "pane") {
        return node.id === paneId ? { ...node, sessionId: newSessionId } : node;
      }
      return { ...node, a: rewrite(node.a), b: rewrite(node.b) };
    }
    this.tabs = this.tabs.map((t) =>
      t.tabId === tabId ? { ...t, root: rewrite(t.root) } : t
    );
  }

  // How many leaves across all tabs currently reference this sessionId.
  // SFTP can split-share a session with the terminal pane, so closing one
  // shouldn't disconnect the underlying SSH session if any other leaf
  // still uses it.
  countLeavesForSession(sessionId: string): number {
    let n = 0;
    const walk = (node: PaneNode) => {
      if (node.kind === "pane") {
        if (node.sessionId === sessionId) n++;
      } else {
        walk(node.a); walk(node.b);
      }
    };
    for (const t of this.tabs) walk(t.root);
    return n;
  }

  // Find the tab that owns the given sessionId. Used by SessionStore to
  // route session_state events back to the right pane tab.
  findTabForSession(sessionId: string): PaneTab | null {
    for (const t of this.tabs) {
      if (containsSession(t.root, sessionId)) return t;
    }
    return null;
  }

  // Jump to a session: activate the tab holding it and focus its pane.
  // Returns false if the session isn't in this window's tree (e.g. it
  // lives in a detached window).
  revealSession(sessionId: string): boolean {
    const tab = this.findTabForSession(sessionId);
    if (!tab) return false;
    this.activateTab(tab.tabId);
    const leaf = leafForSession(tab.root, sessionId);
    if (leaf) this.setActivePane(tab.tabId, leaf.id);
    return true;
  }
}
export const paneTabs = new PaneTreeStore();

// ClosedTabEntry remembers just enough about a closed tab to let
// Ctrl+Shift+T reopen it: which SSH connections it carried (we drop
// local-shell entries because their identity is the PID, gone after
// disconnect), the tab title, and group meta. We do not preserve the
// pane tree - reopen creates flat one-session-per-pane layout. Refining
// to full layout restore would require keeping the connectionId on
// every leaf, which is a bigger change.
export interface ClosedTabEntry {
  title: string;
  connectionIds: string[];
  groupName?: string;
  groupColor?: string;
  closedAt: number;
}

class ClosedTabStore {
  // Cap the stack so a long-running session with hundreds of opens
  // doesn't grow this unbounded. 32 is plenty for "oops, reopen".
  private static CAP = 32;
  stack = $state<ClosedTabEntry[]>([]);

  push(e: ClosedTabEntry) {
    if (e.connectionIds.length === 0) return; // nothing reopenable
    this.stack = [e, ...this.stack].slice(0, ClosedTabStore.CAP);
  }

  pop(): ClosedTabEntry | null {
    if (this.stack.length === 0) return null;
    const [top, ...rest] = this.stack;
    this.stack = rest;
    return top;
  }

  clear() { this.stack = []; }
}
export const closedTabs = new ClosedTabStore();

// SerializedPaneTab is the wire format used to ship a tab across
// windows (detach → detached window URL, detached → main on redock).
// Only the structural state lives here; sessions themselves are
// global (backend pool) and looked up by id on restore.
export interface SerializedPaneTab {
  title: string;
  root: PaneNode;
  groupName?: string;
  groupColor?: string;
  locked?: boolean;
}

export function serializePaneTab(t: PaneTab): SerializedPaneTab {
  return {
    title: t.title,
    root: t.root,
    groupName: t.groupName,
    groupColor: t.groupColor,
    locked: t.locked,
  };
}

// encodePaneLayout / decodePaneLayout wrap the SerializedPaneTab in
// URL-safe base64 so it survives the ?layout=... query string and the
// opaque-string TabDragPayload field. We use the URL-safe base64
// alphabet to avoid '+' / '/' getting percent-encoded into noise.
export function encodePaneLayout(t: PaneTab): string {
  const json = JSON.stringify(serializePaneTab(t));
  // btoa needs latin-1; round-trip via TextEncoder for utf-8 safety.
  const bytes = new TextEncoder().encode(json);
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function decodeLayoutJSON(raw: string): any | null {
  if (!raw) return null;
  try {
    let b64 = raw.replace(/-/g, "+").replace(/_/g, "/");
    while (b64.length % 4) b64 += "=";
    const binStr = atob(b64);
    const bytes = new Uint8Array(binStr.length);
    for (let i = 0; i < binStr.length; i++) bytes[i] = binStr.charCodeAt(i);
    return JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    return null;
  }
}

export function decodePaneLayout(raw: string): SerializedPaneTab | null {
  const parsed = decodeLayoutJSON(raw);
  // A multi-tab blob ({tabs:[...]}) decodes to just its first tab here so old
  // single-tab callers keep working.
  const single = parsed && Array.isArray(parsed.tabs) ? parsed.tabs[0] : parsed;
  if (!single || !single.root || typeof single.title !== "string") return null;
  return single as SerializedPaneTab;
}

// encodePaneLayouts / decodePaneLayoutsMulti carry MULTIPLE tabs (a whole
// window's worth) in one blob so redocking a detached window with several
// tabs doesn't lose all but the first. Backward compatible: the decoder also
// accepts a legacy single-tab blob.
export function encodePaneLayouts(tabs: PaneTab[]): string {
  const payload = { tabs: tabs.map(serializePaneTab) };
  const json = JSON.stringify(payload);
  const bytes = new TextEncoder().encode(json);
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function decodePaneLayoutsMulti(raw: string): SerializedPaneTab[] {
  const parsed = decodeLayoutJSON(raw);
  if (!parsed) return [];
  const arr: any[] = Array.isArray(parsed.tabs) ? parsed.tabs : [parsed];
  return arr.filter((t) => t && t.root && typeof t.title === "string") as SerializedPaneTab[];
}

// cloneWithFreshIds deep-copies a pane tree and replaces every
// internal id (pane.id, split.id) with a freshly generated one. The
// sessionId on each leaf is preserved - that's the link back to the
// SessionStore entry. Used when restoring a tab into a window where
// the original ids might collide with an existing tab.
function cloneWithFreshIds(node: PaneNode): PaneNode {
  if (node.kind === "pane") {
    return { kind: "pane", id: genId("pane"), sessionId: node.sessionId, view: node.view };
  }
  return {
    kind: "split",
    id: genId("split"),
    direction: node.direction,
    ratio: node.ratio,
    a: cloneWithFreshIds(node.a),
    b: cloneWithFreshIds(node.b),
  };
}

// ----- pure tree helpers -----

function findLeaf(node: PaneNode, id: string): PaneLeaf | null {
  if (node.kind === "pane") return node.id === id ? node : null;
  return findLeaf(node.a, id) ?? findLeaf(node.b, id);
}

function firstLeafIn(node: PaneNode): PaneLeaf | null {
  if (node.kind === "pane") return node;
  return firstLeafIn(node.a) ?? firstLeafIn(node.b);
}

function containsSession(node: PaneNode, sessionId: string): boolean {
  if (node.kind === "pane") return node.sessionId === sessionId;
  return containsSession(node.a, sessionId) || containsSession(node.b, sessionId);
}

function leafForSession(node: PaneNode, sessionId: string): PaneLeaf | null {
  if (node.kind === "pane") return node.sessionId === sessionId ? node : null;
  return leafForSession(node.a, sessionId) ?? leafForSession(node.b, sessionId);
}

function replaceLeaf(
  node: PaneNode,
  id: string,
  replacer: (leaf: PaneLeaf) => PaneNode
): PaneNode {
  if (node.kind === "pane") {
    return node.id === id ? replacer(node) : node;
  }
  return {
    ...node,
    a: replaceLeaf(node.a, id, replacer),
    b: replaceLeaf(node.b, id, replacer),
  };
}

// Remove a pane from the tree. Returns the new root (collapsing the split
// if its other child becomes the only survivor) or null if the entire tree
// is gone.
function removePane(node: PaneNode, id: string): PaneNode | null {
  if (node.kind === "pane") {
    return node.id === id ? null : node;
  }
  const newA = removePane(node.a, id);
  const newB = removePane(node.b, id);
  if (newA === null && newB === null) return null;
  if (newA === null) return newB;
  if (newB === null) return newA;
  return { ...node, a: newA, b: newB };
}

function adjustRatio(node: PaneNode, splitId: string, ratio: number): PaneNode {
  if (node.kind === "pane") return node;
  if (node.id === splitId) return { ...node, ratio };
  return {
    ...node,
    a: adjustRatio(node.a, splitId, ratio),
    b: adjustRatio(node.b, splitId, ratio),
  };
}

class DragStore {
  // Source identity. Exactly one of these is set while a drag is in flight.
  connectionId       = $state<string | null>(null);
  folderId           = $state<string | null>(null);
  tabId              = $state<string | null>(null);
  credentialId       = $state<string | null>(null);
  credentialFolderId = $state<string | null>(null);
  // For multi-drag: when the user grabbed something that's in the current
  // multi-selection, we record every id the gesture should move. The
  // primary connectionId/folderId stays set to the row the user actually
  // grabbed (so existing single-drag code paths still work for the
  // "compute target / validity" step), and applyDrop iterates this list
  // when present.
  multiConnIds   = $state<string[]>([]);
  multiFolderIds = $state<string[]>([]);
  // Same idea for the credentials tree: every selected credential id
  // the gesture should move. credentialId stays the grabbed row.
  multiCredIds   = $state<string[]>([]);

  // Tree drop intent: while hovering over a tree row, where will the drop
  // land? 'before' / 'after' / 'inside' (folders only). Drives the row's
  // visual drop indicator.
  overTreeKind   = $state<"folder" | "connection" | null>(null);
  overTreeId     = $state<string | null>(null);
  overTreeIntent = $state<"before" | "after" | "inside" | null>(null);

  // Credential-tree hover: which folder is highlighted as the drop target,
  // or null for "root". Separate from overTreeKind/Id because credentials
  // use simple drop-inside (no reorder).
  overCredFolderId = $state<string | null | "ROOT">(null);

  get active() {
    return (
      this.connectionId !== null ||
      this.folderId !== null ||
      this.tabId !== null ||
      this.credentialId !== null ||
      this.credentialFolderId !== null
    );
  }

  startConnection(connectionId: string, alsoMoving: string[] = []) {
    this.clearSources();
    this.connectionId = connectionId;
    this.multiConnIds = alsoMoving;
  }
  startFolder(folderId: string, alsoMoving: string[] = []) {
    this.clearSources();
    this.folderId = folderId;
    this.multiFolderIds = alsoMoving;
  }
  startTab(tabId: string) {
    this.clearSources();
    this.tabId = tabId;
  }
  startCredential(id: string, alsoMoving: string[] = []) {
    this.clearSources();
    this.credentialId = id;
    this.multiCredIds = alsoMoving;
  }
  startCredentialFolder(id: string) {
    this.clearSources();
    this.credentialFolderId = id;
  }
  private clearSources() {
    this.connectionId = null;
    this.folderId = null;
    this.tabId = null;
    this.credentialId = null;
    this.credentialFolderId = null;
    this.multiConnIds = [];
    this.multiFolderIds = [];
    this.multiCredIds = [];
  }
  hoverCredFolder(id: string | null | "ROOT") {
    this.overCredFolderId = id;
  }
  hoverTree(
    kind: "folder" | "connection" | null,
    id: string | null,
    intent: "before" | "after" | "inside" | null,
  ) {
    this.overTreeKind = kind;
    this.overTreeId = id;
    this.overTreeIntent = intent;
  }
  end() {
    this.clearSources();
    this.overTreeKind = null; this.overTreeId = null; this.overTreeIntent = null;
    this.overCredFolderId = null;
  }
}
export const drag = new DragStore();

class SessionStore {
  tabs = $state<SessionTab[]>([]);
  activeId = $state<string | null>(null);

  private unsubs = new Map<string, () => void>();

  add(t: SessionTab) {
    if (t.status === "connected" && !t.connectedAt) {
      t = { ...t, connectedAt: Date.now() };
    }
    // Connect wrappers stash which WG profile the dial went through;
    // the add always runs after the connect promise resolved.
    if (!t.networkVia) {
      const via = takeNetworkVia(t.sessionId);
      if (via) t = { ...t, networkVia: via };
    }
    this.tabs.push(t);
    this.activeId = t.sessionId;
    this.subscribe(t.sessionId);
  }

  // subscribe wires the three event streams for a given sessionId. We
  // separate it from add() so the reconnect-success path can resubscribe
  // under the new id after a swap. Old subs are released first.
  private subscribe(sessionId: string) {
    const old = this.unsubs.get(sessionId);
    if (old) old();
    const unsubs: Array<() => void> = [];
    unsubs.push(
      EventsOn(`session_state:${sessionId}`, (p: any) => {
        const state = p?.state;
        console.log(`[session_state ${sessionId.slice(0, 8)}] ${state}`, p);
        if (state === "connecting" || state === "auth_in_progress") {
          this.setStatus(sessionId, "connecting", p.hint);
        } else if (state === "connected") {
          this.setStatus(sessionId, "connected");
        } else if (state === "disconnected") {
          // Only the active reconnect-attempt path should hold the
          // "reconnecting" status. A naked disconnected event always
          // wins after that: the previous behaviour swallowed
          // legitimate disconnects whenever reconnect was running
          // briefly and then failed, leaving the UI green forever.
          const cur = this.tabs.find((x) => x.sessionId === sessionId);
          const inFlightReconnect =
            cur?.status === "reconnecting" &&
            cur.reconnectAttempt !== undefined &&
            (cur.reconnectMaxAttempts ?? 0) > 0 &&
            cur.reconnectAttempt < (cur.reconnectMaxAttempts ?? 0);
          if (!inFlightReconnect) {
            this.setStatus(sessionId, "disconnected", p.reason);
            // Auto-close on clean exit (Ctrl+D, `exit 0`). Backend
            // sets p.clean=true only for normal shell exits; abnormal
            // closes leave the tab open so the user sees the reason.
            if (p.clean && terminalPrefs.closeOnCleanExit) {
              // Defer so the disconnect message renders briefly first.
              setTimeout(() => this.autoCloseSession(sessionId), 250);
            }
          }
        } else if (state === "error") {
          this.setStatus(sessionId, "error", p.message);
        }
      })
    );
    unsubs.push(
      EventsOn(`session_reconnect_attempt:${sessionId}`, (p: any) => {
        this.tabs = this.tabs.map((t) =>
          t.sessionId === sessionId
            ? {
                ...t,
                status: "reconnecting",
                statusHint: `attempt ${p.attempt}/${p.max_attempts}`,
                reconnectAttempt: p.attempt,
                reconnectMaxAttempts: p.max_attempts,
                reconnectDelay: p.delay_seconds,
              }
            : t
        );
      })
    );
    unsubs.push(
      EventsOn(`session_reconnect_success:${sessionId}`, (p: any) => {
        const newId: string = p.new_session_id;
        // Swap the session id across SessionStore and the pane tree.
        this.swapSessionId(sessionId, newId);
        paneTabs.swapSessionId(sessionId, newId);
        // The fresh session may have taken a different path (auto
        // mode: on-site direct vs remote tunnel) - refresh the badge.
        this.setNetworkVia(newId, p.network_via || undefined);
      })
    );
    unsubs.push(
      EventsOn(`session_reconnect_failed:${sessionId}`, (p: any) => {
        this.tabs = this.tabs.map((t) =>
          t.sessionId === sessionId
            ? { ...t, status: "disconnected", statusHint: p.reason,
                reconnectAttempt: undefined, reconnectDelay: undefined,
                reconnectMaxAttempts: undefined }
            : t
        );
      })
    );
    this.unsubs.set(sessionId, () => unsubs.forEach((fn) => fn()));
  }

  // swapSessionId rebinds the SessionTab on the old id to use a new id.
  // Releases the old subscriptions and resubscribes under the new id so
  // future session_state events route correctly.
  swapSessionId(oldId: string, newId: string) {
    this.tabs = this.tabs.map((t) =>
      t.sessionId === oldId
        ? {
            ...t,
            sessionId: newId,
            status: "connected",
            statusHint: "reconnected",
            reconnectAttempt: undefined,
            reconnectDelay: undefined,
            reconnectMaxAttempts: undefined,
          }
        : t
    );
    if (this.activeId === oldId) this.activeId = newId;
    const oldUnsub = this.unsubs.get(oldId);
    if (oldUnsub) {
      oldUnsub();
      this.unsubs.delete(oldId);
    }
    this.subscribe(newId);
  }
  setNetworkVia(sessionId: string, via: string | undefined) {
    this.tabs = this.tabs.map((t) =>
      t.sessionId === sessionId ? { ...t, networkVia: via } : t
    );
  }
  setStatus(sessionId: string, status: SessionTab["status"], hint?: string) {
    // Reassign the whole array so $derived consumers re-run. In Svelte 5
    // mutating a single $state array element doesn't notify derived state
    // that filters on a property of the element.
    this.tabs = this.tabs.map((t) => {
      if (t.sessionId !== sessionId) return t;
      const next: SessionTab = { ...t, status, statusHint: hint };
      // Stamp connectedAt on the first transition into "connected";
      // clear it on disconnect / error so the uptime restarts on the
      // next connect attempt.
      if (status === "connected" && t.status !== "connected") {
        next.connectedAt = Date.now();
      } else if (status === "disconnected" || status === "error") {
        next.connectedAt = undefined;
      }
      return next;
    });
  }
  remove(sessionId: string) {
    const un = this.unsubs.get(sessionId);
    if (un) {
      un();
      this.unsubs.delete(sessionId);
    }
    this.tabs = this.tabs.filter((t) => t.sessionId !== sessionId);
    if (this.activeId === sessionId) {
      this.activeId = this.tabs[this.tabs.length - 1]?.sessionId ?? null;
    }
  }
  activate(sessionId: string) {
    this.activeId = sessionId;
  }

  // Wired from the disconnected handler when terminalPrefs.closeOnCleanExit
  // is on and the backend reported a clean shell exit. Drops the leaf
  // (or whole tab if it was alone) and removes the session entry.
  autoCloseSession(sessionId: string) {
    // Find every pane that pointed at this session and close them.
    // countLeavesForSession returns 0 once all leaves are gone, at
    // which point the tab is already removed by closePane.
    for (const tab of paneTabs.tabs) {
      const leaves = findLeavesForSession(tab.root, sessionId);
      for (const leafId of leaves) {
        paneTabs.closePane(tab.tabId, leafId);
      }
    }
    api.sshDisconnect(sessionId).catch(() => {});
    this.remove(sessionId);
    if (paneTabs.tabs.length === 0) {
      // Match closeTab() behaviour: bounce to Connections if nothing
      // left to look at.
      view.setTab("connections");
    } else {
      // The close promoted another tab/pane to active, but keyboard
      // focus stayed on the now-unmounted xterm. Punt it into the
      // newly visible terminal so the user can type immediately.
      focusActiveTerminal();
    }
  }
}
export const sessions = new SessionStore();

// Walk a pane tree and collect every leaf id whose sessionId matches.
function findLeavesForSession(node: PaneNode, sessionId: string): string[] {
  const out: string[] = [];
  function walk(n: PaneNode) {
    if (n.kind === "pane") {
      if (n.sessionId === sessionId) out.push(n.id);
    } else {
      walk(n.a); walk(n.b);
    }
  }
  walk(node);
  return out;
}

export interface HostKeyChallenge {
  challengeId: string;
  hostname: string;
  port: number;
  keyType: string;
  fingerprint: string;
  status: "unknown" | "changed";
  oldFingerprint?: string;
  keyB64: string;
}

// FIFO queue. Batch connects (Connect-all on N selected) fire N
// host-key callbacks in parallel from the backend; each emits a
// host_key_challenge event with its own challenge_id. We surface them
// one at a time - the modal shows the head and the user responds;
// the next one slides up. Backend keeps the goroutines blocked on
// their per-challenge channels until SshRespondHostKey unblocks
// exactly the one with the matching id, so order doesn't have to
// match emission order.
class HostKeyStore {
  queue = $state<HostKeyChallenge[]>([]);

  // The legacy `pending` API is preserved as a getter so existing
  // call sites (App.svelte's `hostKeyStore.pending`) keep working
  // while we migrate. Returns the head of the queue or null.
  get pending(): HostKeyChallenge | null {
    return this.queue[0] ?? null;
  }

  enqueue(c: HostKeyChallenge) {
    // Defensive: drop duplicate challenge ids (unlikely but cheap).
    if (this.queue.some((q) => q.challengeId === c.challengeId)) return;
    this.queue = [...this.queue, c];
  }
  // Legacy alias.
  set(c: HostKeyChallenge) { this.enqueue(c); }

  shift() {
    if (this.queue.length === 0) return;
    this.queue = this.queue.slice(1);
  }
  // Legacy alias - clears just the head, not the whole queue.
  clear() { this.shift(); }

  clearAll() { this.queue = []; }
}
export const hostKeyStore = new HostKeyStore();

// Pending MCP command-approval requests. When the bridge is active and an LLM
// asks to run a non-allowlisted command or type into the terminal, the backend
// emits mcp_approval_request and blocks on a channel until McpApprovalRespond.
// Same FIFO surfacing as host-key challenges.
export interface McpApproval {
  approvalId: string;
  sessionId: string;
  sessionName: string;
  kind: "run" | "type" | "connect";
  command: string;
}

class McpApprovalStore {
  queue = $state<McpApproval[]>([]);
  get pending(): McpApproval | null {
    return this.queue[0] ?? null;
  }
  enqueue(a: McpApproval) {
    if (this.queue.some((q) => q.approvalId === a.approvalId)) return;
    this.queue = [...this.queue, a];
  }
  shift() {
    if (this.queue.length === 0) return;
    this.queue = this.queue.slice(1);
  }
  clearAll() { this.queue = []; }
}
export const mcpApprovalStore = new McpApprovalStore();

// Tracks which session ids are currently shared with the LLM (MCP bridge), so
// the terminal tabs can show a "shared" marker. Kept live off the
// mcp_grants_changed event; also seeded on demand.
class McpSharedStore {
  private ids = $state<Set<string>>(new Set());

  has(sessionId: string): boolean {
    return this.ids.has(sessionId);
  }
  // Replace the whole set (from mcp_grants_changed payload or a fetch).
  setFrom(grants: { session_id: string }[]) {
    this.ids = new Set(grants.map((g) => g.session_id));
  }
  get size(): number {
    return this.ids.size;
  }
}
export const mcpShared = new McpSharedStore();

// Whether the MCP bridge is enabled (mcp_bridge_enabled setting). Drives
// whether the robot affordances (pane Share-with-LLM button, status-bar robot)
// are shown at all - no point offering LLM sharing when the bridge is off.
// Seeded on demand and kept live via the mcp_bridge_toggled event.
class McpBridgeStore {
  enabled = $state(false);
  setEnabled(v: boolean) { this.enabled = v; }
}
export const mcpBridge = new McpBridgeStore();

class ExpandedFoldersStore {
  // Explicit per-ID override. Absent = fall back to depth default (depth 0 → expanded).
  private map = $state<Map<string, boolean>>(new Map());

  constructor() {
    try {
      const raw = localStorage.getItem("ssh-tool:expanded-folders");
      if (raw) {
        const entries = JSON.parse(raw) as [string, boolean][];
        this.map = new Map(entries);
      }
    } catch {}
  }

  isExpanded(id: string, depth: number): boolean {
    if (this.map.has(id)) return this.map.get(id)!;
    return depth === 0;
  }

  toggle(id: string, depth: number) {
    this.set(id, !this.isExpanded(id, depth));
  }

  set(id: string, expanded: boolean) {
    const next = new Map(this.map);
    next.set(id, expanded);
    this.map = next;
    try {
      localStorage.setItem("ssh-tool:expanded-folders", JSON.stringify([...next.entries()]));
    } catch {}
  }
}
export const expandedFolders = new ExpandedFoldersStore();
