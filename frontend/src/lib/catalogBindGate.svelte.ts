// Bind catalog slots at the first connect.
//
// Before a connect the user clicked (terminal, database, Kafka, VNC, a
// tunnel, a bulk connect), catalogBindGate asks the backend which unbound
// slots those connections need. None: the connect goes on. Some: one
// dialog (CatalogBindModal) lists them in connect order; the user binds
// them all, or cancels and nothing connects. Connects without a click
// (workspace restore, tunnel autostart, MCP, the CI runner) never come
// here; they fail with the slot's error and the catalog root's chip.
import { api, type CatalogPendingSlot } from "./api";
import { catalogs } from "./catalog.svelte";

class BindGateStore {
  // ids: the connections being connected (never offered as their own
  // bastion).
  pending = $state<{ slots: CatalogPendingSlot[]; ids: string[]; resolve: (ok: boolean) => void } | null>(null);
  // One dialog at a time: a second gate waits for the first and asks the
  // backend again, so slots bound meanwhile are not asked twice.
  chain: Promise<unknown> = Promise.resolve();

  ask(slots: CatalogPendingSlot[], ids: string[]): Promise<boolean> {
    return new Promise((resolve) => (this.pending = { slots, ids, resolve }));
  }

  finish(ok: boolean) {
    const p = this.pending;
    this.pending = null;
    p?.resolve(ok);
  }
}

export const bindGate = new BindGateStore();

// catalogBindGate resolves true when the connect may go on: nothing to
// bind, or the user bound it. A failing check lets the connect go on; it
// then fails with the slot's own error. purpose: "ssh" (terminal, VNC,
// tunnel), "db:<target id>" (a Database tab) or "kafka".
export function catalogBindGate(ids: string[], purpose = "ssh"): Promise<boolean> {
  if (catalogs.list.length === 0 || ids.length === 0) return Promise.resolve(true);
  const run = bindGate.chain.then(async () => {
    let slots: CatalogPendingSlot[];
    try {
      slots = (await api.catalogUnboundFor(ids, purpose)) ?? [];
    } catch {
      return true;
    }
    if (slots.length === 0) return true;
    return bindGate.ask(slots, ids);
  });
  bindGate.chain = run.catch(() => {});
  return run;
}
