// Standing problems with the desktop integrations, surfaced outside
// Settings.
//
// The staleness warnings only helped someone who already opened
// Settings > Desktop integration - which is nobody, until an
// ssh-tool:// link silently opens the wrong binary and they go looking.
// This collects what is currently wrong so the status bar can say so.
//
// Deliberately narrow: these are conditions that persist until fixed,
// not events. Anything transient belongs in a toast.

import { api } from "./api";

export type DesktopAlert = {
  // Stable id, so a resolved alert can be told apart from a new one.
  id: string;
  // One line, the headline.
  message: string;
  // What it means and what fixing it takes, for the issues panel.
  detail: string;
  // Settings section to open when the badge is clicked; "section#setting"
  // also highlights that setting (see flashSetting in Settings.svelte).
  section: string;
};

class DesktopAlertsStore {
  alerts = $state<DesktopAlert[]>([]);

  get count() {
    return this.alerts.length;
  }

  // The section to open when there is exactly one place to go. With
  // several, the desktop section is the one that explains them all.
  get primarySection() {
    return this.alerts.length === 1 ? this.alerts[0].section : "desktop";
  }

  async refresh() {
    const found: DesktopAlert[] = [];
    try {
      const [url, menu] = await Promise.all([
        api.urlSchemeIntegration(),
        api.explorerMenuIntegration(),
      ]);
      if (url.stale) {
        found.push({
          id: "url-scheme-stale",
          message: "ssh-tool:// links open a different copy of ssh-tool",
          detail: "The link handler is registered to another install or an older path. Register it again from this copy.",
          section: "desktop#url-scheme",
        });
      }
      if (menu.stale) {
        found.push({
          id: "context-menu-stale",
          message: "The file manager's right-click menu opens a different copy",
          detail: "The menu entry points at another install or an older path. Register it again from this copy.",
          section: "desktop#file-manager",
        });
      }
    } catch {
      // Not worth reporting: the integrations query failing is itself
      // not a problem the user can act on.
    }

    try {
      const mcp = await api.claudeDesktopStatus();
      if (mcp?.stale) {
        found.push({
          id: "mcp-stale",
          message: "Claude Desktop's MCP entry points at an older ssh-tool",
          detail: "Claude Desktop will start the old binary for MCP. Update the entry so it runs this one.",
          section: "llm#mcp-register|mcp-enable",
        });
      }
    } catch { /* same */ }

    this.alerts = found;
  }
}

export const desktopAlerts = new DesktopAlertsStore();
