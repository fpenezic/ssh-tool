// Everything the status bar's issues badge reports, in one list.
//
// The badge used to show a count and put the details in a tooltip, and a
// click went to a Settings section without saying which issue it was
// about. Each issue now carries its own description and its own way to
// the place that fixes it, rendered by IssuesPanel.
//
// Sources: desktop integration problems (desktopAlerts), credentials
// with a user-set expiry inside the warning window, and opkssh certs
// running out of their lifetime. Conditions that persist until fixed -
// anything transient belongs in a toast.

import { api, type OpksshCertLifetime } from "./api";
import { credentials, view, selection } from "./stores.svelte";
import { desktopAlerts } from "./desktopAlerts.svelte";
import { expiringCredentials, opksshExpiry } from "./credExpiry";

export type IssueSeverity = "warn" | "error";

export interface Issue {
  id: string;
  severity: IssueSeverity;
  title: string;
  detail: string;
  actionLabel: string;
  action: () => void;
}

function openCredential(id: string) {
  view.setTab("credentials");
  selection.selectCredentialById(id);
}

class IssuesStore {
  // Minute clock: opkssh windows can be hours long, so the level has to
  // move while the app sits open. The cert list is refreshed on the same
  // beat - a connect that signed in again replaces the cert behind it.
  now = $state(Date.now());
  opkssh = $state<OpksshCertLifetime[]>([]);
  private timer: ReturnType<typeof setInterval> | null = null;

  start() {
    if (this.timer) return;
    void this.refreshOpkssh();
    this.timer = setInterval(() => {
      this.now = Date.now();
      void this.refreshOpkssh();
    }, 60_000);
  }

  stop() {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  async refreshOpkssh() {
    try {
      this.opkssh = (await api.opksshCertLifetimes()) ?? [];
    } catch {
      // A locked vault answers with an empty list, not an error; a real
      // failure here is not something the user can act on from a badge.
    }
  }

  get items(): Issue[] {
    const out: Issue[] = [];
    for (const a of desktopAlerts.alerts) {
      out.push({
        id: a.id, severity: "warn", title: a.message, detail: a.detail,
        actionLabel: "Open Settings", action: () => view.setTabSettingsSection(a.section),
      });
    }
    for (const c of expiringCredentials(credentials.list, this.now)) {
      const expired = c.info.level === "expired";
      out.push({
        id: `cred-exp:${c.id}`,
        severity: expired ? "error" : "warn",
        title: `${c.name}: ${c.info.label}`,
        detail: expired
          ? "The secret has passed the expiry date set on it. Rotate it, then update or clear the date."
          : "The secret expires soon. Rotate it before then, then update the date.",
        actionLabel: "Open credential", action: () => openCredential(c.id),
      });
    }
    for (const o of this.opkssh) {
      const x = opksshExpiry(o.start, o.end, this.now);
      if (x.level === "ok") continue;
      const left = Math.round(x.fraction * 100);
      out.push({
        id: `opkssh:${o.credential_id}`,
        severity: x.level === "soon" ? "warn" : "error",
        title: `${o.name}: ${x.label}`,
        detail: x.level === "expired"
          ? "The opkssh certificate has run out. The next connect with it opens the browser to sign in."
          : `The opkssh certificate has ${left}% of its lifetime left. Once it runs out, the next connect opens the browser to sign in.`,
        actionLabel: "Open credential", action: () => openCredential(o.credential_id),
      });
    }
    // Errors first; the order inside each group is the sources' own.
    return out.sort((a, b) => (a.severity === b.severity ? 0 : a.severity === "error" ? -1 : 1));
  }

  get severity(): IssueSeverity | null {
    const items = this.items;
    if (items.length === 0) return null;
    return items.some((i) => i.severity === "error") ? "error" : "warn";
  }
}

export const issues = new IssuesStore();
