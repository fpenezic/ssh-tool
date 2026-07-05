# Presence + remote disconnect for network profiles (design)

Status: **design only, not implemented.** Ships after the cheap
"single identity" warning. Written before any code because it touches
the sync layer, which is delicate.

## Problem

A WireGuard profile carries ONE identity (private key + interface IP),
and the whole profile - config in `store.db`, secrets in `vault.enc` -
is synced across the user's machines. That is deliberate: "configure
once, use everywhere". The cost: the same identity can be brought up on
two machines at once.

Unlike a clean "latest handshake wins" story, two peers actively
sending from the same key make the server's endpoint for that key
**flap** between them: neither side gets a stable path, so BOTH degrade.
Our 2-minute idle-stop shrinks the window but does not close it - a
live SSH session keeps the tunnel up indefinitely.

Real-world trigger (author): tunnel left up on the office PC, now needs
to connect from the laptop. Wants: "turn it off there, take it over
here" without maintaining per-machine configs (which would break the
sync-one-profile model).

NetBird does not have this problem - each machine registers as its own
peer with its own overlay IP. So this whole mechanism is a
**WireGuard** concern first; it is still useful for NetBird as a
"see/stop my tunnels elsewhere" convenience.

## Non-goals

- Not an instant kill. There is no direct machine-to-machine channel;
  coordination goes through the existing synced state, so latency is
  bounded by the sync cadence (seconds with live-sync, minutes with
  interval polling).
- Not a lock/lease that PREVENTS a second machine from connecting. The
  user stays in control - we inform and offer, we do not forbid.
- No new server. Everything rides the WebDAV sync artefact the app
  already uses.

## Key invariant that makes this tractable

Our tunnels are **userspace, in-process**. A tunnel cannot outlive the
app: if the app is closed or crashes, the tunnel is gone. Therefore a
FRESH presence record (recent heartbeat) implies the owning app is
alive and WILL see a kill-request. No zombie-tunnel problem.

## State shape

A new synced artefact, separate from `store.db`/`vault.enc` so it can
be written frequently without churning the profile snapshot. Small JSON
blob (or a tiny sealed file next to the snapshot), one entry per
(profile, machine):

```
presence[profileID][machineID] = {
  machine_name:  "work-pc",       // human label (hostname)
  pid:           12345,
  since:         1699999999,      // unix, tunnel-up time
  heartbeat:     1700000030,      // unix, refreshed every ~30s while up
  kind:          "wireguard",
}
```

Kill requests, same file:

```
kill[profileID] = {
  target_machine: "work-pc",      // whose tunnel to stop
  by_machine:     "laptop",
  ts:             1700000040,
  nonce:          "…",            // so a stale request isn't re-honoured
}
```

`machineID` is a stable per-install id (generate once, store in
settings). Distinct from the sidecar machine-bound key - this is just a
presence identity, not a secret.

## Freshness

A presence record is LIVE when `now - heartbeat < staleAfter`
(propose 90s = 3 missed 30s beats). Stale records are ignored and
garbage-collected by any machine that notices them. This absorbs
crashes, hard power-off, and network drops without a dedicated
"goodbye" (though a clean tunnel-stop SHOULD delete its own record
promptly).

## Flow

Bring-up on machine B (laptop), profile already live on A (PC):

1. Before starting the tunnel, B reads presence for the profile.
2. Sees a LIVE record from A → dialog:
   > Profile "office" is active on **work-pc** (seen 20s ago).
   > Connecting here will fight for the same identity.
   > [Request office-pc to disconnect and take over]  [Connect anyway]  [Cancel]
3. "Take over" → B writes a `kill[profileID]` entry targeting A, with a
   fresh nonce, and waits (bounded, ~sync-interval + margin) for A's
   presence to disappear.
4. A, on its next sync read (or live-sync push), sees a kill-request
   naming it with an unseen nonce → stops its tunnel, deletes its
   presence record, records the nonce as handled.
5. B sees A's presence gone → starts its tunnel, writes its own
   presence.
6. If the wait times out (A offline / not syncing) → B tells the user
   "office-pc did not respond; connect anyway?" and lets them force it.

"Connect anyway" at step 2 skips the handshake and just brings B up -
the informed-flapping path, for when the user knows A is actually dead.

## Edge cases

- **Both bring up simultaneously.** Each sees the other's fresh (or
  absent) record depending on timing. Worst case both connect and flap
  - same as today, no regression; the dialog reduces the odds. A
  lease/lock could serialise this but is out of scope (see non-goals).
- **A offline when B requests kill.** Kill-request lingers in the file;
  A honours it whenever it next comes online and reads sync BEFORE
  bringing the profile up again. B, meanwhile, force-connected or gave
  up. GC drops the request after a TTL.
- **Kill-request replay.** Nonce + "handled nonces" set on A prevents
  re-honouring an old request after A legitimately reconnects later.
- **Sealed vs plaintext.** Presence carries hostnames + pids, not
  secrets. Can live in a plaintext sibling file to avoid re-sealing the
  vault on every heartbeat; still, prefer the same WebDAV auth. Decide
  at implementation.
- **Clock skew** between machines. All comparisons are "age since
  heartbeat" on each reader's own clock against a remote absolute ts -
  skew shifts the staleness boundary but never makes a dead record look
  alive by more than the skew. 90s window tolerates typical skew;
  consider a skew guard if it bites.

## Scope of work when built

- `internal/presence` (new): read/write/GC the artefact, machineID.
- Sync layer: carry the presence file alongside the snapshot; a
  lightweight "read presence" that does NOT pull the whole store.
- Tunnel bring-up (`ensureTunnel` / `wgDialerFor`): presence check +
  the dialog hook (returns a decision the caller acts on).
- A sync-read tick (or reuse live-sync) that spots kill-requests
  targeting this machine and stops the named tunnel.
- Frontend: the take-over dialog; optionally a "profile active on X"
  hint in the network segment / profile card.

## For now

Ship only the **warning**: a synced WireGuard profile shows a note that
it uses one identity across machines and a live tunnel elsewhere will
degrade both sides - with a pointer that NetBird gives each machine its
own peer. No presence, no kill. This document is the plan for the real
fix.
