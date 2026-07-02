// Thin client around the backend-owned broadcast group.
//
// The set lives on the Go side so that every window (main + detached)
// observes the same membership. A "broadcast_changed" event from the
// backend pushes the new list whenever the set mutates; this module
// owns local mirror state ($state) so Svelte components react.
//
// fanOut() is also server-side: we hand the encoded payload to
// BroadcastFanOut and Go iterates the pool. Avoids N round-trips
// per keystroke and works even when the originating window doesn't
// have references to the other sessions in its local store.

import { api } from "./api";
import { EventsOn } from "./wailsRuntime";

function encodeB64(s: string): string {
  // Inline TextEncoder->btoa so emoji/non-ASCII keystrokes survive.
  const enc = new TextEncoder();
  const bytes = enc.encode(s);
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin);
}

class BroadcastStore {
  // members mirrors the default broadcast group ("") so single-group
  // call sites keep working. groups holds the full snapshot for the
  // multi-group manager UI; updated by the broadcast_groups_changed
  // event the backend now emits alongside broadcast_changed.
  members = $state<Set<string>>(new Set());
  groups = $state<Record<string, Set<string>>>({});
  // version is bumped on every groups mutation so $derived consumers
  // re-evaluate even when the active key was already present (the
  // Set reference inside `groups[X]` changes but Svelte's deep
  // tracking via $state can miss it on a single-key replace).
  groupsVersion = $state(0);
  lastError = $state<string | null>(null);

  private wired = false;

  async init() {
    if (this.wired) return;
    this.wired = true;
    EventsOn("broadcast_changed", (ids: string[]) => {
      this.members = new Set(ids ?? []);
    });
    EventsOn("broadcast_groups_changed", (map: Record<string, string[]>) => {
      const next: Record<string, Set<string>> = {};
      for (const [g, ids] of Object.entries(map ?? {})) {
        next[g] = new Set(ids);
      }
      this.groups = next;
      this.groupsVersion++;
    });
    try {
      const initial = await api.broadcastList();
      this.members = new Set(initial ?? []);
    } catch (e) {
      console.warn("broadcast init:", e);
    }
    try {
      const full = (await api.broadcastListGroups()) ?? {};
      const next: Record<string, Set<string>> = {};
      for (const [g, ids] of Object.entries(full)) {
        next[g] = new Set(ids);
      }
      this.groups = next;
      this.groupsVersion++;
    } catch (e) {
      console.warn("broadcast groups init:", e);
    }
  }

  // has() reports default-group membership only (legacy API). For
  // the pane badge - "is this session in ANY broadcast group?" -
  // use hasInAnyGroup so the highlight survives sessions that live
  // only in a user-created group.
  has(sessionId: string): boolean {
    return this.members.has(sessionId);
  }

  hasInAnyGroup(sessionId: string): boolean {
    void this.groupsVersion;
    for (const set of Object.values(this.groups)) {
      if (set.has(sessionId)) return true;
    }
    return false;
  }

  // Total unique members across every group - used by the status
  // bar pill so 'Broadcast: 5' reflects the real fan-out scope,
  // not just the default group's size.
  totalMembers(): number {
    void this.groupsVersion;
    const u = new Set<string>();
    for (const set of Object.values(this.groups)) {
      for (const id of set) u.add(id);
    }
    return u.size;
  }

  // Sessions a given session shares any group with. Used by panel
  // chips to colour-code per-group membership.
  groupsOf(sessionId: string): string[] {
    const out: string[] = [];
    for (const [gid, set] of Object.entries(this.groups)) {
      if (set.has(sessionId)) out.push(gid);
    }
    return out;
  }

  groupNames(): string[] {
    return Object.keys(this.groups).sort((a, b) => {
      // Default group ("") comes first.
      if (a === "") return -1;
      if (b === "") return 1;
      return a.localeCompare(b);
    });
  }

  groupSize(groupId: string): number {
    return this.groups[groupId]?.size ?? 0;
  }

  hasInGroup(groupId: string, sessionId: string): boolean {
    return this.groups[groupId]?.has(sessionId) ?? false;
  }

  get size(): number {
    return this.members.size;
  }

  // Each action calls the backend; local state updates via the
  // "broadcast_changed" event, not optimistically. This keeps a
  // detached window honest - its mirror is updated by the same
  // event the main window receives.
  async add(sessionId: string) {
    try { await api.broadcastAdd(sessionId); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }
  async remove(sessionId: string) {
    try { await api.broadcastRemove(sessionId); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }
  async toggle(sessionId: string) {
    if (this.members.has(sessionId)) await this.remove(sessionId);
    else await this.add(sessionId);
  }
  async setAll(sessionIds: string[]) {
    try { await api.broadcastSetAll(sessionIds); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }
  async clear() {
    try { await api.broadcastClear(); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }

  // Multi-group actions. groupID "" is the default (legacy) group;
  // any non-empty string is a user-created named group.
  async addTo(groupId: string, sessionId: string) {
    try { await api.broadcastAddTo(groupId, sessionId); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }
  async removeFrom(groupId: string, sessionId: string) {
    try { await api.broadcastRemoveFrom(groupId, sessionId); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }
  async toggleIn(groupId: string, sessionId: string) {
    if (this.hasInGroup(groupId, sessionId)) await this.removeFrom(groupId, sessionId);
    else await this.addTo(groupId, sessionId);
  }
  async clearGroup(groupId: string) {
    try { await api.broadcastClearGroup(groupId); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }
  async deleteGroup(groupId: string) {
    if (!groupId) return;
    try { await api.broadcastGroupDelete(groupId); }
    catch (e: any) { this.lastError = e?.message ?? String(e); }
  }

  // Hand a typed keystroke off to the backend, which fans it out to
  // every member except the origin. The backend returns a non-empty
  // string when at least one target failed; we surface that into
  // lastError so the manager modal can show it.
  async fanOut(data: string, originSessionId: string) {
    if (this.members.size < 2) return;
    try {
      const errs = await api.broadcastFanOut(originSessionId, encodeB64(data));
      if (errs) this.lastError = errs;
    } catch (e: any) {
      this.lastError = e?.message ?? String(e);
    }
  }

  dismissError() { this.lastError = null; }
}

export const broadcast = new BroadcastStore();
