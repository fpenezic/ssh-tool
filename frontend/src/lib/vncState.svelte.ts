// VncPane connection info, keyed by sessionId. Populated when a VNC tab
// is opened (VncOpenProxmox / VncOpenConnection) and by vncSessionList()
// in a detached window after a tab tear-off. VncPane reads its ws_url +
// password from here on mount.
//
// Not reactive state - VncPane reads it once on mount and drives noVNC
// imperatively. A plain Map keeps it simple and avoids Svelte deep-track
// surprises.

import { api, type VncSession } from "./api";

const byId = new Map<string, VncSession>();

// RETRACE (test8): the reactive vncControls registry that fed the pane
// HEADER was reverted to VncPane's own toolbar to isolate the VNC freeze.
// If the freeze is gone in test8, this registry's $effect republish path
// was the cause and we re-add it more carefully; if not, the cause is
// elsewhere (ws relay / lazy connect) and this comes back as-is.

export const vncSessions = {
  set(s: VncSession) {
    byId.set(s.session_id, s);
  },
  get(sessionId: string): VncSession | undefined {
    return byId.get(sessionId);
  },
  delete(sessionId: string) {
    byId.delete(sessionId);
  },
  // Refresh from the backend - used by a detached window to learn the
  // ws_url + password for VNC tabs it received via the layout. Returns
  // the list so callers can register SessionTabs too.
  async refresh(): Promise<VncSession[]> {
    const list = (await api.vncSessionList()) ?? [];
    for (const s of list) byId.set(s.session_id, s);
    return list;
  },
};
