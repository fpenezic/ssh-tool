<script lang="ts">
  import { onMount } from "svelte";
  import "@xterm/xterm/css/xterm.css";
  import { GuestClient, type Phase, type ManifestSession } from "./ws";
  import GuestPane from "./GuestPane.svelte";

  let phase = $state<Phase>({ kind: "connecting" });
  let activeTab = $state(0);
  // While true the guest mirrors the host's active tab. Clicking a tab drops
  // out of follow mode so the guest can look around independently.
  let following = $state(true);

  // The websocket URL is this page's own origin with /ws appended: the guest
  // was served from https://<bind>/s/<token>, so /s/<token>/ws is the socket.
  const wsURL = (() => {
    const u = new URL(window.location.href);
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
    if (!u.pathname.endsWith("/ws")) {
      u.pathname = u.pathname.replace(/\/$/, "") + "/ws";
    }
    return u.toString();
  })();

  const client = new GuestClient(wsURL);

  // Session metadata by slot, for the pane renderer.
  let sessionMap = $state(new Map<string, ManifestSession>());

  onMount(() => {
    client.onPhase = (p) => {
      phase = p;
      if (p.kind === "live") {
        const m = new Map<string, ManifestSession>();
        for (const s of p.manifest.sessions) m.set(s.id, s);
        sessionMap = m;
        activeTab = p.manifest.active_tab ?? 0;
      }
    };
    // Follow the host's active tab, unless the guest has taken manual control
    // by clicking a tab themselves.
    client.onActiveTab = (index) => {
      if (following) activeTab = index;
    };
    client.connect();
    return () => client.close();
  });
</script>

<div class="guest-root">
  {#if phase.kind === "connecting"}
    <div class="center">
      <div class="spinner"></div>
      <div class="big">Connecting…</div>
    </div>
  {:else if phase.kind === "pending"}
    <div class="center">
      <div class="big">Waiting for {phase.info.host} to let you in…</div>
      <div class="fp-label">Confirm this code matches what they see:</div>
      <div class="fp-words">{phase.info.fp_words}</div>
      <div class="fp-hint">
        Ask the host (by phone or chat) to read out their fingerprint. If it
        doesn't match, do not continue.
      </div>
    </div>
  {:else if phase.kind === "denied"}
    <div class="center">
      <div class="big">Access denied</div>
      <div class="sub">The host didn't allow the connection.</div>
    </div>
  {:else if phase.kind === "closed"}
    <div class="center">
      <div class="big">Session ended</div>
      <div class="sub">{phase.reason}</div>
    </div>
  {:else if phase.kind === "error"}
    <div class="center">
      <div class="big">Couldn't connect</div>
      <div class="sub">{phase.message}</div>
    </div>
  {:else if phase.kind === "live"}
    {@const manifest = phase.manifest}
    <div class="topbar">
      <div class="tabs">
        {#each manifest.tabs as tab, i (i)}
          <button
            class="tab"
            class:active={i === activeTab}
            style={tab.groupColor ? `border-bottom-color:${tab.groupColor}` : ""}
            onclick={() => { activeTab = i; following = false; }}
          >
            {tab.title}
          </button>
        {/each}
      </div>
      <div class="topbar-right">
        {#if !following}
          <button class="follow-btn" onclick={() => (following = true)}>Follow host</button>
        {/if}
        <div class="badge" class:control={manifest.level === "control"}>
          {manifest.level === "control" ? "can type" : "read-only"}
        </div>
      </div>
    </div>
    <div class="stage">
      {#each manifest.tabs as tab, i (i)}
        <div class="tab-view" class:hidden={i !== activeTab}>
          <GuestPane node={tab.root} sessions={sessionMap} level={manifest.level} {client} />
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  :global(html),
  :global(body) {
    margin: 0;
    height: 100%;
    background: #181825;
    color: #cdd6f4;
    font-family: system-ui, -apple-system, sans-serif;
  }
  .guest-root {
    display: flex;
    flex-direction: column;
    height: 100vh;
    width: 100vw;
    overflow: hidden;
  }
  .center {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.7rem;
    padding: 2rem;
    text-align: center;
  }
  .big {
    font-size: 1.3rem;
    font-weight: 600;
  }
  .sub {
    color: #a6adc8;
  }
  .fp-label {
    margin-top: 1rem;
    color: #a6adc8;
    font-size: 0.9rem;
  }
  .fp-words {
    font-size: 1.5rem;
    font-weight: 700;
    letter-spacing: 0.02em;
    color: #89b4fa;
    font-family: "JetBrains Mono", monospace;
  }
  .fp-hint {
    max-width: 26rem;
    color: #7f849c;
    font-size: 0.8rem;
  }
  .spinner {
    width: 28px;
    height: 28px;
    border: 3px solid #313244;
    border-top-color: #89b4fa;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .topbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    background: #11111b;
    border-bottom: 1px solid #313244;
    padding: 0 0.5rem;
    flex: 0 0 auto;
  }
  .tabs {
    display: flex;
    overflow-x: auto;
  }
  .tab {
    background: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    color: #a6adc8;
    padding: 0.55rem 0.9rem;
    cursor: pointer;
    font-size: 0.85rem;
    white-space: nowrap;
  }
  .tab.active {
    color: #cdd6f4;
    border-bottom-color: #89b4fa;
  }
  .topbar-right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .follow-btn {
    background: #89b4fa;
    color: #11111b;
    border: none;
    border-radius: 999px;
    padding: 0.15rem 0.7rem;
    font-size: 0.72rem;
    cursor: pointer;
    font-weight: 600;
  }
  .badge {
    font-size: 0.72rem;
    padding: 0.12rem 0.55rem;
    border-radius: 999px;
    background: #313244;
    color: #a6adc8;
  }
  .badge.control {
    background: #f38ba8;
    color: #11111b;
    font-weight: 600;
  }
  .stage {
    flex: 1;
    position: relative;
    min-height: 0;
  }
  .tab-view {
    position: absolute;
    inset: 0;
  }
  .tab-view.hidden {
    display: none;
  }
</style>
