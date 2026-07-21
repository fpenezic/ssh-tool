<script lang="ts">
  // Small popover to share this pane's session with an external LLM (MCP
  // bridge). Only meaningful when the bridge is enabled in Settings; if it's
  // off we say so and link the user to where to turn it on. Mirrors the
  // grant states the backend tracks: not shared -> read-only / read+run.
  import { onMount } from "svelte";
  import { api, type McpGrantLevel } from "./api";
  import { clickOutside } from "./clickOutside";
  import { view } from "./stores.svelte";
  import { IconStop } from "./iconMap";
  import { MCP_SYSTEM_PROMPT, MCP_SYSTEM_PROMPT_HINT } from "./mcpSystemPrompt";
  import { toast } from "./toast.svelte.ts";
  import { writeClipboard } from "./clipboard";
  import { EventsOn } from "./wailsRuntime";

  interface Props {
    // Empty when the pane has no live session (controls disable).
    sessionId: string;
    onClose: () => void;
    onViewActivity?: () => void;
  }
  let { sessionId, onClose, onViewActivity }: Props = $props();

  let bridgeOn = $state(false);
  let level = $state<"" | McpGrantLevel>("");
  let manageOn = $state(false);
  let err = $state<string | null>(null);
  let loading = $state(true);

  onMount(() => {
    load();
    // Reflect manage-grant changes made elsewhere (or auto-dropped on process
    // exit / off toggle).
    const off = EventsOn("mcp_grants_changed", () => {
      api.mcpGetManageStore().then((v) => (manageOn = v)).catch(() => {});
    });
    return off;
  });

  async function load() {
    loading = true;
    try {
      const v = await api.settingsGet("mcp_bridge_enabled");
      bridgeOn = v === "1" || v === "true";
    } catch { bridgeOn = false; }
    if (bridgeOn) {
      try { manageOn = await api.mcpGetManageStore(); } catch { /* ignore */ }
    }
    if (bridgeOn && sessionId) {
      try {
        const grants = (await api.mcpListGrants()) ?? [];
        const g = grants.find((x) => x.session_id === sessionId);
        level = g ? g.level : "";
      } catch { /* ignore */ }
    }
    loading = false;
  }

  async function toggleManage() {
    try {
      await api.mcpSetManageStore(!manageOn);
      manageOn = !manageOn;
    } catch (e) { err = String((e as any)?.message ?? e); }
  }

  async function share(lvl: McpGrantLevel) {
    if (!sessionId) return;
    try {
      await api.mcpShareSession(sessionId, lvl);
      level = lvl;
    } catch (e) { err = String((e as any)?.message ?? e); }
  }

  async function unshare() {
    if (!sessionId) return;
    try {
      await api.mcpUnshareSession(sessionId);
      level = "";
    } catch (e) { err = String((e as any)?.message ?? e); }
  }

  function openSettings() {
    onClose();
    view.setTabSettingsSection("llm");
  }

  async function copyPrompt() {
    // writeClipboard tries the native Go clipboard first (the mac WKWebView
    // refuses navigator.clipboard here), then falls back to navigator.
    try {
      await writeClipboard(MCP_SYSTEM_PROMPT);
      toast.ok("System prompt copied. " + MCP_SYSTEM_PROMPT_HINT);
    } catch {
      toast.err("Copy failed - clipboard unavailable");
    }
  }

  function levelLabel(l: "" | McpGrantLevel): string {
    if (l === "read-run-yolo") return "auto-run (YOLO)";
    if (l === "read-run") return "read + run";
    return "read only";
  }
</script>

<div class="pop" use:clickOutside={{ onOutside: onClose }}>
  <div class="head">Share with LLM</div>
  {#if err}<div class="err">{err}</div>{/if}

  {#if loading}
    <div class="muted">…</div>
  {:else if !bridgeOn}
    <div class="muted">
      LLM access is off. Turn on <strong>Allow LLM (MCP) access</strong> in
      Settings first.
    </div>
    <button class="link" onclick={openSettings}>Open Settings</button>
  {:else if !sessionId}
    <div class="muted">Connect the session first.</div>
  {:else if level === ""}
    <div class="sub">
      Let a connected LLM inspect this session. Reads are safe; commands that
      change state ask you first.
    </div>
    <div class="row">
      <button class="btn" onclick={() => share("read")}>Read only</button>
      <button class="btn primary" onclick={() => share("read-run")}>Read + run</button>
    </div>
    <button class="btn yolo" onclick={() => share("read-run-yolo")}>Auto-run (YOLO)</button>
    <div class="yolo-warn">
      Auto-runs commands without asking. Catastrophic commands still prompt.
    </div>
  {:else}
    <div class="active" class:yolo-active={level === "read-run-yolo"}>
      <span class="dot"></span>
      <span>Shared - {levelLabel(level)}</span>
    </div>
    {#if level === "read-run-yolo"}
      <div class="yolo-warn">
        Commands run without asking (catastrophic ones still prompt).
      </div>
    {/if}
    <div class="row">
      {#if level === "read"}
        <button class="btn" onclick={() => share("read-run")}>Upgrade to read + run</button>
        <button class="btn yolo" onclick={() => share("read-run-yolo")}>Auto-run (YOLO)</button>
      {:else if level === "read-run"}
        <button class="btn" onclick={() => share("read")}>Downgrade to read only</button>
        <button class="btn yolo" onclick={() => share("read-run-yolo")}>Auto-run (YOLO)</button>
      {:else}
        <button class="btn" onclick={() => share("read-run")}>Back to read + run</button>
        <button class="btn" onclick={() => share("read")}>Read only</button>
      {/if}
      <button class="btn stop" onclick={unshare}><IconStop size={11} /> Stop</button>
    </div>
  {/if}

  {#if bridgeOn && !loading}
    <div class="manage">
      <label class="manage-row">
        <input type="checkbox" checked={manageOn} onchange={toggleManage} />
        <span>Allow manage (create connections)</span>
      </label>
      <div class="manage-sub">
        Lets the LLM propose new folders, connections and forwards for your
        approval. It never sets passwords - only references existing vault
        credentials.
      </div>
    </div>
  {/if}

  {#if bridgeOn}
    <button class="link prompt" onclick={copyPrompt}>Copy LLM system prompt</button>
  {/if}
  {#if bridgeOn && onViewActivity}
    <button class="link activity" onclick={onViewActivity}>View LLM activity ›</button>
  {/if}
</div>

<style>
  .pop {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    z-index: 200;
    min-width: 260px;
    max-width: 340px;
    background: var(--base);
    border: 1px solid var(--surface0);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.45);
    padding: 0.5rem 0.6rem;
    font-size: 0.8rem;
  }
  .head { font-weight: 600; color: var(--text); margin-bottom: 0.3rem; }
  .err { color: var(--red); margin-bottom: 0.3rem; word-break: break-word; }
  .muted { color: var(--overlay0); line-height: 1.4; }
  .sub { color: var(--overlay0); font-size: 0.75rem; margin-bottom: 0.5rem; line-height: 1.35; }
  .row { display: flex; gap: 0.4rem; margin-top: 0.4rem; flex-wrap: wrap; }
  .btn {
    background: var(--mantle); border: 1px solid var(--surface1);
    color: var(--text); border-radius: 3px; padding: 0.25rem 0.55rem;
    cursor: pointer; font: inherit; font-size: 0.75rem;
    display: inline-flex; align-items: center; gap: 0.2rem;
  }
  .btn:hover { background: var(--surface1); }
  .btn.primary { background: var(--blue); color: var(--base); border-color: var(--blue); font-weight: 600; }
  .btn.primary:hover { filter: brightness(1.1); }
  .btn.stop { color: var(--red); }
  .btn.yolo {
    margin-top: 0.4rem; width: 100%; justify-content: center;
    background: transparent; color: var(--red); border-color: var(--red);
    font-weight: 600;
  }
  .btn.yolo:hover { background: color-mix(in srgb, var(--red) 15%, transparent); }
  .yolo-warn {
    margin-top: 0.35rem; font-size: 0.7rem; line-height: 1.3;
    color: var(--red); opacity: 0.9;
  }
  .active { display: flex; align-items: center; gap: 0.4rem; color: var(--text); }
  .active.yolo-active .dot { background: var(--red); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--green); flex-shrink: 0; }
  .link {
    background: transparent; border: 0; color: var(--blue);
    cursor: pointer; font: inherit; font-size: 0.78rem;
    padding: 0.25rem 0; text-decoration: underline;
  }
  .link.prompt {
    display: block; margin-top: 0.5rem; padding-top: 0.4rem;
    border-top: 1px solid var(--surface0); text-decoration: none; width: 100%; text-align: left;
  }
  .link.prompt:hover { color: var(--lavender); }
  .link.activity {
    display: block; margin-top: 0.25rem; text-decoration: none; width: 100%; text-align: left;
  }
  .link.activity:hover { color: var(--lavender); }
  .manage {
    margin-top: 0.5rem; padding-top: 0.45rem;
    border-top: 1px solid var(--surface0);
  }
  .manage-row {
    display: flex; align-items: center; gap: 0.4rem;
    color: var(--text); cursor: pointer; user-select: none;
  }
  .manage-row input { cursor: pointer; }
  .manage-sub {
    margin-top: 0.3rem; font-size: 0.7rem; line-height: 1.35;
    color: var(--overlay0);
  }
</style>
