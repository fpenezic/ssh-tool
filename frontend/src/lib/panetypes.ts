// Pane-tree shape, extracted from stores.svelte.ts so it can be imported
// WITHOUT dragging in the store runtime (and, through it, the Wails runtime).
//
// The guest bundle (frontend/src/guest/*) renders the same pane tree a shared
// tab has, but runs in a plain browser with no Wails IPC, no api.ts, and no
// stores. It imports these types only; a type-only import compiles to nothing,
// so nothing from the app leaks into the guest chunk. stores.svelte.ts
// re-exports these names, so every existing import site is unaffected.

export type PaneNode = PaneLeaf | PaneSplit;

export interface PaneLeaf {
  kind: "pane";
  id: string; // unique within the tab; used as a focus key
  sessionId: string;
  // What's rendered in this leaf. terminal/sftp share one SSH session
  // (the SFTP client layers on top of the existing ssh.Client), so
  // toggling doesn't reconnect. "vnc" renders a noVNC console - it never
  // splits or toggles (the tab is locked to a single full leaf).
  // "unavailable" is guest-only: the share projection downgrades sftp/vnc
  // leaves to it, since a browser guest has neither.
  view?: "terminal" | "sftp" | "vnc" | "unavailable";
}

export interface PaneSplit {
  kind: "split";
  id: string;
  direction: "horizontal" | "vertical"; // horizontal = side-by-side
  ratio: number; // 0..1, fraction taken by `a`
  a: PaneNode;
  b: PaneNode;
}

// The wire shape of a single tab (detach/redock, and the share manifest).
// Deliberately omits tabId/pane ids - the consumer regenerates them.
export interface SerializedPaneTab {
  title: string;
  root: PaneNode;
  groupName?: string;
  groupColor?: string;
  locked?: boolean;
}
