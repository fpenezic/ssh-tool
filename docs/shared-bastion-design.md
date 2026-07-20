# Shared bastion (jump) connection multiplexing - design

Status: PROPOSED (awaiting review before implementation).

## Problem

A bulk Connect-all onto N connections that all sit behind the same jump
host opens N independent SSH connections TO that jump host - one per
target. Each is a full TCP + SSH handshake against the bastion in the
same instant. The bastion's `MaxStartups` (default `10:30:100`) then
resets some of them: the log shows

```
ssh: jump vpn-... handshake failed: ssh: handshake failed: EOF
```

and the batch stalls on retries. Observed with 13 targets behind one
bastion: 2-3 EOFs and an ~11s wall time.

This is exactly what OpenSSH `ControlMaster`/`ControlPersist` solves:
ONE connection to the bastion, then a `direct-tcpip` channel per target.
`golang.org/x/crypto/ssh` already gives us this - subsequent hops in a
chain use `prev.Dial("tcp", addr)`, which opens a direct-tcpip channel.
Today each connection just builds its own `prev` from scratch.

## Goal

When several connections share the same resolved jump prefix, dial that
prefix ONCE and reuse the resulting `*ssh.Client` for every target's
first-hop dial. Only the FINAL hop (the target) is per-connection.

Non-goals: SSH-level session multiplexing on the target itself; a
user-visible "connection sharing" setting; sharing across app restarts.

## Key: resolved jump prefix identity (NOT folder)

Two connections share a bastion iff their jump chains resolve to the
same prefix. The share key is derived from the resolved settings, not
the tree:

- For each jump hop (everything in `buildHopChain` EXCEPT the final
  target hop), include `hostname:port|user|authRef`.
- Prefix the whole key with the `NetworkProfileID` (nil-safe), because a
  WG profile changes the transport used to reach hop 0 - a bastion
  reached direct and the same bastion reached through WG must not share
  a client (different underlying path, possibly different reachability).
- Join hops with a separator. Empty chain prefix (no jump host) => no
  key => never pooled (a direct target connection is not a bastion).

Folder inheritance is how a user ends up with an identical jump prefix
(the JumpHostSpec is inherited from a common parent), but the pool keys
on the RESOLVED prefix so it also catches two connections in different
folders that happen to share a bastion, and it never wrongly shares two
connections that merely sit in the same folder but override the jump.

## Component: a jump-client pool (app layer, like the WG tunnel manager)

A new small manager, owned by App (mirrors `wgman`'s
acquire/release/idle-stop shape):

```
type jumpPool struct {
    mu      sync.Mutex
    entries map[string]*jumpEntry // key -> shared prefix client
}
type jumpEntry struct {
    key      string
    client   *ssh.Client   // the LAST client of the shared prefix (the
                           // bastion the target dials through)
    stack    []*ssh.Client // full prefix chain, for teardown
    refs     int           // live target sessions using this prefix
    idleStop *time.Timer
}
```

- `acquire(key, build) (*ssh.Client, release func(), error)`:
  under the lock, reuse an existing entry (`refs++`, cancel idle timer)
  or build a new prefix chain via `build()` and insert it. Returns the
  bastion client plus a `release` the session calls on teardown.
- `release`: `refs--`; when it hits 0, start an idle linger timer
  (reuse the `wgLinger` 2-minute value) that tears the prefix down if
  no one re-acquires. Matches the WG idle-stop UX so a quick
  disconnect/reconnect doesn't pay the bastion handshake again.
- `build(key)` runs the jump-prefix dial ONCE: it is the current
  `Connect` loop for hops `0..len(chain)-2`, factored out so both the
  pooled path and a cold single connect share it. Host-key check for
  the bastion happens here, once per shared client.

Concurrency: two connections racing to acquire the same missing key must
build the prefix ONCE. Simplest correct form: hold the pool lock while
building (bastion handshake is ~100-300ms; the batch is staggered anyway,
and blocking the second acquirer until the first bastion is up is
exactly the serialization we want). If that proves too coarse we switch
to a per-key `sync.Once`/in-flight promise, but start simple.

## Wiring into Connect

`Connect` gains an optional hook, set by the app layer (same pattern as
`FirstHopDialerHook`):

```
// JumpPrefixHook, when non-nil, returns a shared *ssh.Client for the
// jump prefix of settings (nil if the chain has no jump hops), plus a
// release to call on session teardown. When it returns a client,
// Connect skips building hops 0..n-2 and dials the target through it.
var JumpPrefixHook func(settings, ctx, buildDeps) (client *ssh.Client, release func(), err error)
```

- If the chain has jump hops AND the hook returns a client: the target
  (final hop) is dialed with `sharedClient.Dial("tcp", targetAddr)`,
  then the target SSH handshake + PTY proceed as today. `networkVia`
  comes from the shared prefix (recorded when it was built).
- If the hook is nil, returns nil, or the chain has no jump: the current
  full-chain path runs unchanged (single connections, tests, VNC-jump,
  batch-exec keep working with zero behavior change).
- The session stores the `release` and calls it from `Disconnect`
  alongside the existing `cleanup(clients)` - but ONLY closes the clients
  it OWNS (the target, and the prefix only when unpooled). A pooled
  session must NOT `cleanup` the shared prefix; the pool's refcount owns
  that.

## Teardown correctness (the trap)

`cleanup(clients)` today closes every client in the stack, target-first.
For a pooled session the stack's prefix clients are SHARED - closing them
would drop every sibling session. So:

- Pooled session's `Session.stack` holds ONLY the target client (+ any
  unshared hops, which for the single-bastion case is none). Its
  `Disconnect` closes the target and calls `release()`.
- The pool's idle-stop is the ONLY path that closes prefix clients.
- App shutdown: pool.StopAll() closes every prefix after sessions are
  torn down (order: sessions first, then pool, then WG - a prefix may
  itself ride a WG tunnel).

## Host key handling

- Bastion (prefix) host keys: verified once, when the shared client is
  built. Reusing the client reuses that trust. A bulk connect where the
  bastion is unknown prompts ONCE for the bastion (good - pairs with the
  accept-all host-key work), then per-target as their keys appear.
- Target host keys: unchanged, per-connection, verified on the target
  handshake through the shared channel.

## Interaction with the frontend concurrency limit + stagger

Shared-bastion removes the root cause (N bastion handshakes -> 1). The
`runPooled` concurrency cap + stagger in connectionActions stays as cheap
defense-in-depth for the DIFFERENT-bastion case (a batch spanning several
bastions still shouldn't fire all target handshakes in one tick) and for
the very first acquire of each key. We can raise the cap since the
bastion is no longer the bottleneck, but keep a limit.

## Rollout / testing

- Unit: pool acquire/release/refcount/idle-stop with a fake build fn
  (no real SSH) - two acquires of one key build once; release to zero
  arms the timer; re-acquire cancels it; StopAll closes.
- Unit: share-key derivation - same jump prefix => same key; different
  target => same key (target is not in the key); different
  NetworkProfileID => different key; no jump => empty key.
- Live: 13 targets behind one bastion via Connect-all -> ONE
  `jump ... authenticated` for hop 0 in the log (not 13), no EOF, fast.
  Close all 13 -> bastion lingers 2 min then stops (log line). Reconnect
  within the linger -> no new bastion handshake.
- Live: mixed batch (some behind bastion A, some behind B, some direct)
  each group shares its own prefix; direct ones unaffected.
- Live regression: single connection behind a jump still works; VNC
  through a jump still works (BuildJumpChain path is separate and
  unchanged unless we opt it in later); batch-exec unchanged.
- Gates: go build ./..., go test ./internal/ssh/ ./internal/resolver/,
  android parity build for internal/ssh, npm run check.

## Decisions (reviewed)

1. Idle linger: SHORT - ~10s (`bastionLinger`), not the WG 2-minute
   value. Just enough to cover a quick disconnect/reconnect without
   holding the bastion open; a new connect + jump comes up fast anyway.
2. VNC-through-jump (`BuildJumpChain`) stays INDEPENDENT - it is always a
   single console open, never N at once, so it has nothing to pool.
   Untouched this pass.
3. Frontend concurrency cap stays at 4.
