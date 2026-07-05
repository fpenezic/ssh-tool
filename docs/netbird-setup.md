# NetBird setup guide

How to connect ssh-tool to a NetBird network so a connection's first
SSH hop rides the tunnel. This is the practical checklist; for the
concept and the WireGuard alternative see the "Network profiles"
section of `USER_GUIDE.md`.

NetBird gives each machine its **own peer** (unlike a shared WireGuard
key), which is why it's the better choice when you connect from more
than one machine or sync your profile across them.

> Desktop only. NetBird runs as a helper process, which Android can't
> spawn - Android uses WireGuard profiles instead.

## 1. Install the plugin

Settings -> Network profiles -> **Plugins** -> Download next to
NetBird. It fetches the helper for your OS from the matching ssh-tool
release and verifies the checksum before installing. One-time.

## 2. Create the RIGHT key in NetBird

This is where most first attempts go wrong. NetBird has two very
different credentials:

| Credential | Format | What it's for | Use here? |
|---|---|---|---|
| **Setup key** | UUID, e.g. `A1B2C3D4-E5F6-...` | enrolling a peer | **YES** |
| Personal access token (PAT) | starts `nbp_...` | calling the NetBird API | no |

A PAT is rejected at registration with `setup key is invalid` /
`no peer auth method provided`. Always use a **setup key**.

In the NetBird dashboard: **Setup Keys -> Create Setup Key**.

- **Name** - anything identifiable, e.g. `ssh-tool`.
- **Reusable** - turn this **on** if you'll run the profile on more
  than one machine, or you sync the profile across machines. Each
  machine registers as its own separate peer. A one-off key is
  consumed after a single registration and then fails.
- **Usage limit** - `Unlimited` (or enough for your machines) for a
  reusable key.
- **Expiration** - set a sane expiry. When it expires, no NEW peers
  can enroll with it; peers already registered keep working.
- **Ephemeral peers** - optional. An ephemeral peer is auto-removed
  after it's been offline for ~10 minutes. Handy for laptops that come
  and go, but the machine then re-enrolls as a fresh peer each time it
  reconnects, so pair ephemeral with a **reusable** key.
- **Auto-assigned groups** - **this is what grants access.** See the
  next section - registering a peer is not the same as letting it
  reach anything.

Copy the generated key (the UUID).

## 3. Put the peer in a group that has access

Registering a peer only puts it on the network. What it can actually
**reach** is decided by NetBird **access policies**, which act on
**groups**. A peer with no policy to your target hosts will connect to
the tunnel and then time out reaching the host - that's the classic
"tunnel is up but SSH hangs" symptom.

So:

1. Make (or pick) a NetBird **group** that a policy allows to reach the
   hosts you want to SSH into - e.g. a `ssh-clients` group with a
   policy `ssh-clients -> servers` on the SSH port (or all).
2. On the setup key, set **Auto-assigned groups** to that group, so
   every peer enrolled with this key lands in it automatically.
3. If you skip this, add the peer to the right group by hand in the
   Peers list after it registers - but auto-assign is the
   set-and-forget way.

Rule of thumb: **setup key = identity + group membership; policy =
what that group may reach.** You need both.

## 4. Create the profile in ssh-tool

Settings -> Network profiles -> Add profile -> **NetBird**:

- **Management URL** - your NetBird control plane. Blank = the
  `netbird.io` cloud. Self-hosted: enter the URL (a bare host like
  `vpn.example.com` is fine, it's normalised to `https://`). Use the
  **management** URL, which for most self-hosted setups is the same
  host as the dashboard.
- **Device name** - how this peer shows up in the Peers list, e.g.
  `ssh-tool-laptop`.
- **Setup key** - click **+ New** next to the picker, name it, paste
  the setup key (UUID) into the Setup key field, Create. It's stored
  as an API-token credential in the vault.

## 5. Route connections through it

On a folder or connection, set **Network** to the NetBird profile (in
the detail pane). Inheritance applies, so setting it on a `client-X`
folder sends every host under it through that tunnel.

- Mode **Always** = always tunnel; **Auto** = direct first, tunnel
  fallback; **Pause** = kill switch (dial direct, stop the tunnel).
- The pane shows a VPN badge with the profile name when the hop went
  through the tunnel; the status bar lists running tunnels.

## Troubleshooting

- **`setup key is invalid` / `no peer auth method provided`** - you
  used a PAT, or the key is expired / already consumed (one-off), or
  it belongs to a different NetBird instance than the Management URL.
  Create a fresh **reusable setup key** on the right management server.
- **Tunnel connects but SSH times out** - the peer isn't in a group
  with an access policy to the target host. Fix the group / policy
  (section 3), not the key.
- **Port bind / adapter error on start** - the machine also runs the
  full NetBird desktop client (or another tool) holding the default
  WireGuard port. ssh-tool's helper uses a random port, so this
  shouldn't happen; if it does, check for a conflicting client.
- **Peer count keeps growing in the dashboard** - a one-off key can't
  cause this, but re-enrolling (ephemeral, or wiping the profile) adds
  a new peer each time. Prune stale peers in NetBird, or use ephemeral
  peers so they auto-expire.

## Where state lives

- The setup key: vault (via the API-token credential).
- The profile config (management URL, device name, key reference):
  `network_profiles` row, synced with the rest of your profile.
- Per-machine peer registration (device keys): on disk under
  `DataDir/netbird/<profile-id>/`, **not** synced - that's what makes
  each machine its own peer. Deleting the profile removes it.
