// Which network profile a session ACTUALLY dialed through. Filled by
// the api.ts connect wrappers from SshConnectResult.network_via
// (profile name, only when the tunnel was used - auto-mode direct
// dials stay empty); read by SessionStore.add, which always runs
// AFTER the connect promise resolves (the resolve IS the connected
// signal, gotcha 4). Kept in a leaf module so api.ts and
// stores.svelte.ts don't gain an import cycle.

const bySession = new Map<string, string>();

export function recordNetworkVia(sessionId: string, via: string | undefined) {
  if (via) bySession.set(sessionId, via);
}

export function takeNetworkVia(sessionId: string): string | undefined {
  const v = bySession.get(sessionId);
  bySession.delete(sessionId);
  return v;
}
