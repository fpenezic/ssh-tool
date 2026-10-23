// Moving tabs of this window into one new window, the way a tab is torn
// off: the pane trees go along (splits, titles, group), the sessions stay
// connected and change window. Shared by the tab context menu and drag-out
// (a multi-selection goes as one window), a workspace opened in a new
// window and the last-session restore of detached windows.

import { api } from "./api";
import { paneTabs, encodePaneLayout, encodePaneLayouts, type PaneNode, type PaneTab } from "./stores.svelte";

export function paneSessionIds(node: PaneNode, out: string[] = []): string[] {
  if (node.kind === "pane") out.push(node.sessionId);
  else {
    paneSessionIds(node.a, out);
    paneSessionIds(node.b, out);
  }
  return out;
}

// screenX / screenY place the window where a drag ended; 0, 0 centres it.
export async function detachTabsToWindow(tabs: PaneTab[], screenX = 0, screenY = 0) {
  if (tabs.length === 0) return;
  const sids = [...new Set(tabs.flatMap((t) => paneSessionIds(t.root)))].join(",");
  // A single tab keeps the single-tab blob older windows understand.
  const layout = tabs.length === 1 ? encodePaneLayout(tabs[0]) : encodePaneLayouts(tabs);
  for (const t of tabs) paneTabs.removeTab(t.tabId);
  if (screenX !== 0 || screenY !== 0) await api.windowDetachTabAt(tabs[0].tabId, screenX, screenY, sids, layout);
  else await api.windowDetachTab(tabs[0].tabId, sids, layout);
}
