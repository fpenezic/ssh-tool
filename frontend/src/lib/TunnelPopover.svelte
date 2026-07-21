<script lang="ts">
  // Compact tunnel/bookmark popover anchored under a pane's toolbar
  // button. Lists every port-forward configured on the active pane's
  // connection. Per-forward: a Start/Stop toggle that operates on the
  // session this pane belongs to. Dynamic (SOCKS5) forwards expand to
  // show their bookmarks; each bookmark gets a one-click browser
  // launcher that routes through the running tunnel.
  //
  // Disconnected sessions render every action disabled - the
  // "tunnels in this pane" view is read-only until the user
  // reconnects. We don't auto-start the SSH session on toggle
  // because that hides intent (user may be looking at a dead tab
  // intentionally before deciding what to do).

  import { onMount, onDestroy } from "svelte";
  import {
    api,
    type PortForward,
    type ForwardStatus,
    type ProxyBookmark,
  } from "./api";
  import { clickOutside } from "./clickOutside";
  import { writeClipboard } from "./clipboard";
  import { IconPlay, IconStop, IconExternalLink, IconGlobe, IconClipboardCopy } from "./iconMap";

  interface Props {
    connectionId: string;
    // Empty when the pane has no live session. All controls disable
    // because there's no session to start a forward against.
    sessionId: string;
    onClose: () => void;
  }
  let { connectionId, sessionId, onClose }: Props = $props();

  let specs = $state<PortForward[]>([]);
  let active = $state<ForwardStatus[]>([]);
  let err = $state<string | null>(null);
  let pollHandle: ReturnType<typeof setInterval> | null = null;
  // Per-forward "operation in flight" lock so the user can't spam
  // the toggle while we're already starting/stopping. Keyed by
  // forward id.
  let busy = $state<Record<string, boolean>>({});

  // "Give internet": ad-hoc reverse HTTP proxy so a server with no outbound
  // net borrows this machine's connectivity. Not persisted - lives only as a
  // running forward (kind "reverse-proxy") in `active`.
  let giPort = $state(3182);
  let giBusy = $state(false);
  let copied = $state(false);
  // Off by default: the proxy dials from THIS machine's network, so allowing
  // internal targets lets a process on the borrowing server reach our own
  // localhost / LAN. Only the public internet is proxied unless the user opts
  // in here.
  let giAllowInternal = $state(false);

  // Running reverse-proxy forwards for this session, pulled from the active
  // list (they have no persisted spec, so the specs loop below skips them).
  const reverseProxies = $derived(active.filter((a) => a.kind === "reverse-proxy"));

  // Export block is derived from the running proxy's port, so it shows even
  // when the popover is reopened on a proxy started earlier (state is fresh
  // per popover mount). Built in the frontend to match the backend block.
  const giExport = $derived(
    reverseProxies.length > 0
      ? exportBlockFor(reverseProxies[0].local_port)
      : null,
  );

  function exportBlockFor(port: number): string {
    const p = `http://127.0.0.1:${port}`;
    return `export http_proxy=${p} https_proxy=${p} HTTP_PROXY=${p} HTTPS_PROXY=${p} no_proxy=localhost,127.0.0.1,::1`;
  }

  async function giveInternet() {
    if (!sessionId || giBusy) return;
    giBusy = true;
    err = null;
    try {
      const res = await api.sshGiveInternet(sessionId, giPort, giAllowInternal);
      giPort = res.remote_port;
      active = (await api.forwardsActive(sessionId)) ?? [];
    } catch (e) {
      err = String((e as any)?.message ?? e);
    } finally {
      giBusy = false;
    }
  }

  async function stopReverseProxy(id: string) {
    if (busy[id]) return;
    busy = { ...busy, [id]: true };
    try {
      await api.forwardsStop(id);
      active = sessionId ? (await api.forwardsActive(sessionId)) ?? [] : [];
    } catch (e) {
      err = String((e as any)?.message ?? e);
    } finally {
      busy = { ...busy, [id]: false };
    }
  }

  async function copyExport() {
    if (!giExport) return;
    try {
      await writeClipboard(giExport);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      // Clipboard denied - leave the block visible for manual copy.
    }
  }

  async function reload() {
    err = null;
    try {
      specs = (await api.forwardsList(connectionId)) ?? [];
      if (sessionId) {
        active = (await api.forwardsActive(sessionId)) ?? [];
      } else {
        active = [];
      }
    } catch (e) {
      err = String((e as any)?.message ?? e);
    }
  }

  onMount(() => {
    reload();
    // PortForwards.svelte uses 2s polling; same here.
    pollHandle = setInterval(async () => {
      if (!sessionId) return;
      try { active = (await api.forwardsActive(sessionId)) ?? []; } catch {}
    }, 2000);
  });
  onDestroy(() => { if (pollHandle) clearInterval(pollHandle); });

  function statusOf(spec: PortForward): ForwardStatus | undefined {
    return active.find((a) => a.id === spec.id);
  }

  function isRunning(spec: PortForward): boolean {
    const s = statusOf(spec);
    return !!s && s.state === "listening";
  }

  async function toggle(spec: PortForward) {
    if (!sessionId) return;
    if (busy[spec.id]) return;
    busy = { ...busy, [spec.id]: true };
    try {
      if (isRunning(spec)) {
        await api.forwardsStop(spec.id);
      } else {
        await api.forwardsStart(spec.id, sessionId);
      }
      // Refresh active list immediately so the row's icon flips
      // without waiting for the 2s poll.
      active = (await api.forwardsActive(sessionId)) ?? [];
    } catch (e) {
      err = String((e as any)?.message ?? e);
    } finally {
      busy = { ...busy, [spec.id]: false };
    }
  }

  async function openBookmark(spec: PortForward, bm: ProxyBookmark) {
    if (!isRunning(spec)) {
      // Try to start the forward first so the bookmark click is
      // a one-step flow rather than "start, then click again".
      await toggle(spec);
      if (!isRunning(spec)) return; // start failed; err already set
    }
    try {
      await api.sshLaunchBrowser(spec.id, bm.url);
    } catch (e) {
      err = String((e as any)?.message ?? e);
    }
  }

  function label(spec: PortForward): string {
    if (spec.description) return spec.description;
    if (spec.kind === "dynamic") {
      return `SOCKS5 :${spec.local_port ?? "?"}`;
    }
    const dir = spec.kind === "local" ? "L" : "R";
    return `${dir} ${spec.local_port ?? "?"} → ${spec.remote_host ?? "?"}:${spec.remote_port ?? "?"}`;
  }
</script>

<div class="pop" use:clickOutside={{ onOutside: onClose }}>
  {#if err}<div class="err">{err}</div>{/if}

  <!-- Give internet: reverse HTTP proxy for an offline server -->
  <div class="gi">
    <div class="gi-head">
      <IconGlobe size={13} />
      <span class="gi-title">Give internet</span>
    </div>
    <div class="gi-sub">
      Lets a server with no outbound net use this machine's connection via a
      reverse HTTP proxy.
    </div>
    {#if reverseProxies.length === 0}
      <div class="gi-row">
        <label class="gi-port">
          port
          <input type="number" min="1" max="65535" bind:value={giPort} disabled={!sessionId || giBusy} />
        </label>
        <button class="gi-btn" disabled={!sessionId || giBusy} onclick={giveInternet}>
          {giBusy ? "Starting…" : "Give internet"}
        </button>
      </div>
      <label class="gi-allow" title="By default the proxy only reaches the public internet. Enable to also let the server reach this machine's own localhost and private LAN through the proxy.">
        <input type="checkbox" bind:checked={giAllowInternal} disabled={!sessionId || giBusy} />
        Allow reaching my local/private network
      </label>
      {#if giAllowInternal}
        <div class="gi-warn">The server will be able to reach your localhost and LAN through the proxy.</div>
      {/if}
      {#if !sessionId}
        <div class="gi-note">Connect the session first.</div>
      {/if}
    {:else}
      {#each reverseProxies as rp (rp.id)}
        <div class="gi-active">
          <div class="gi-active-head">
            <span class="gi-dot"></span>
            <span>Proxy on server 127.0.0.1:{rp.local_port}</span>
            <button
              class="gi-stop"
              disabled={!!busy[rp.id]}
              title="Stop proxy"
              onclick={() => stopReverseProxy(rp.id)}
            ><IconStop size={11} /> Stop</button>
          </div>
          <div class="gi-bytes">↓ {rp.bytes_in} B · ↑ {rp.bytes_out} B</div>
        </div>
      {/each}
      {#if giExport}
        <div class="gi-export-wrap">
          <div class="gi-export-label">Run this on the server:</div>
          <pre class="gi-export">{giExport}</pre>
          <button class="gi-copy" onclick={copyExport}>
            <IconClipboardCopy size={11} /> {copied ? "Copied" : "Copy"}
          </button>
        </div>
      {/if}
    {/if}
  </div>

  {#if specs.length === 0}
    <div class="empty">
      No forwards configured on this connection.
      Add them under Connection → Forwards.
    </div>
  {:else}
    {#if !sessionId}
      <div class="warn">Session is not connected - controls disabled.</div>
    {/if}
    <ul class="list">
      {#each specs as spec (spec.id)}
        {@const running = isRunning(spec)}
        {@const st = statusOf(spec)}
        <li class="row">
          <button
            class="toggle"
            class:running
            disabled={!sessionId || !!busy[spec.id]}
            title={running ? "Stop tunnel" : "Start tunnel"}
            onclick={() => toggle(spec)}
          >
            {#if running}<IconStop size={12} />{:else}<IconPlay size={12} />{/if}
          </button>
          <div class="meta">
            <div class="label">{label(spec)}</div>
            {#if st?.state === "error" && st.error}
              <div class="sub err-line">{st.error}</div>
            {:else if running}
              <div class="sub">listening on {st!.local_addr}:{st!.local_port}</div>
            {/if}
          </div>
        </li>
        {#if spec.kind === "dynamic" && spec.bookmarks?.length > 0}
          {#each spec.bookmarks as bm (bm.url)}
            <li class="bm">
              <button
                class="bm-btn"
                disabled={!sessionId || !!busy[spec.id]}
                title={running ? `Open ${bm.url}` : `Start tunnel and open ${bm.url}`}
                onclick={() => openBookmark(spec, bm)}
              >
                <IconExternalLink size={11} />
                <span class="bm-name">{bm.name || bm.url}</span>
              </button>
            </li>
          {/each}
        {/if}
      {/each}
    </ul>
  {/if}
</div>

<style>
  .pop {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    z-index: 200;
    min-width: 280px;
    max-width: 380px;
    max-height: 60vh;
    overflow-y: auto;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.45);
    padding: 0.3rem;
    font-size: 0.8rem;
  }
  .err, .err-line { color: var(--red); }
  .err {
    padding: 0.4rem 0.5rem;
    background: var(--surface0);
    border-radius: 4px;
    margin-bottom: 0.3rem;
    word-break: break-word;
  }
  .empty, .warn {
    padding: 0.5rem 0.6rem;
    color: var(--overlay0);
  }
  .warn {
    background: var(--surface0);
    border-radius: 4px;
    margin-bottom: 0.3rem;
    color: var(--yellow);
    font-size: 0.75rem;
  }
  .list {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .row {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    padding: 0.35rem 0.4rem;
    border-radius: 4px;
  }
  .row:hover { background: var(--surface0); }
  .toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 3px;
    border: 1px solid var(--surface1);
    background: var(--mantle);
    color: var(--green);
    cursor: pointer;
    flex-shrink: 0;
  }
  .toggle:hover:not(:disabled) { background: var(--surface1); }
  .toggle:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .toggle.running { color: var(--red); }
  .meta { flex: 1; min-width: 0; }
  .label {
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 0.78rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    color: var(--overlay0);
    font-size: 0.7rem;
    margin-top: 0.1rem;
  }
  .bm {
    padding: 0 0.4rem 0.2rem 2.2rem;
  }
  .bm-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    background: transparent;
    border: 0;
    color: var(--blue);
    cursor: pointer;
    font: inherit;
    font-size: 0.75rem;
    padding: 0.15rem 0.3rem;
    border-radius: 3px;
    max-width: 100%;
  }
  .bm-btn:hover:not(:disabled) {
    background: var(--surface0);
    color: var(--lavender);
  }
  .bm-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .bm-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Give internet section */
  .gi {
    padding: 0.4rem 0.5rem;
    margin-bottom: 0.3rem;
    background: var(--surface0);
    border-radius: 5px;
  }
  .gi-head {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--text);
    font-weight: 600;
    font-size: 0.8rem;
  }
  .gi-sub {
    color: var(--overlay0);
    font-size: 0.7rem;
    margin: 0.2rem 0 0.4rem;
    line-height: 1.3;
  }
  .gi-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .gi-port {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.72rem;
    color: var(--overlay1);
  }
  .gi-port input {
    width: 4.5rem;
    background: var(--mantle);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    color: var(--text);
    padding: 0.15rem 0.3rem;
    font: inherit;
    font-size: 0.75rem;
  }
  .gi-btn {
    background: var(--blue);
    color: var(--base);
    border: 0;
    border-radius: 3px;
    padding: 0.25rem 0.55rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.75rem;
    font-weight: 600;
  }
  .gi-btn:hover:not(:disabled) { filter: brightness(1.1); }
  .gi-btn:disabled { opacity: 0.4; cursor: not-allowed; }
  .gi-note { color: var(--yellow); font-size: 0.7rem; margin-top: 0.3rem; }
  .gi-allow {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.7rem;
    color: var(--overlay1);
    margin-top: 0.4rem;
    cursor: pointer;
  }
  .gi-allow input { margin: 0; cursor: pointer; }
  .gi-warn {
    color: var(--yellow);
    font-size: 0.68rem;
    margin-top: 0.25rem;
    line-height: 1.3;
  }
  .gi-active-head {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.75rem;
    color: var(--text);
  }
  .gi-dot {
    width: 7px; height: 7px; border-radius: 50%;
    background: var(--green);
    flex-shrink: 0;
  }
  .gi-stop {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    background: transparent;
    border: 1px solid var(--surface1);
    color: var(--red);
    border-radius: 3px;
    padding: 0.1rem 0.35rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.7rem;
  }
  .gi-stop:hover:not(:disabled) { background: var(--surface1); }
  .gi-stop:disabled { opacity: 0.4; cursor: not-allowed; }
  .gi-bytes { color: var(--overlay0); font-size: 0.68rem; margin: 0.15rem 0 0.3rem; }
  .gi-export-wrap { margin-top: 0.3rem; }
  .gi-export-label { color: var(--overlay1); font-size: 0.7rem; margin-bottom: 0.2rem; }
  .gi-export {
    background: var(--crust);
    border: 1px solid var(--surface1);
    border-radius: 3px;
    padding: 0.35rem 0.45rem;
    margin: 0;
    font-family: ui-monospace, monospace;
    font-size: 0.68rem;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
  }
  .gi-copy {
    margin-top: 0.25rem;
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    background: var(--surface1);
    border: 0;
    color: var(--text);
    border-radius: 3px;
    padding: 0.15rem 0.45rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.7rem;
  }
  .gi-copy:hover { background: var(--overlay0); }
</style>
