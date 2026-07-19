<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "./api";
  import { vncSessions } from "./vncState.svelte.ts";
  import { sessions } from "./stores.svelte";
  import PasswordInput from "./PasswordInput.svelte";

  type VncStatus = "connecting" | "connected" | "error" | "disconnected" | "needpass";

  interface Props {
    sessionId: string;
    // Console state surfaced to the pane header (PaneNode) via $bindable.
    // VncPane owns and assigns these; PaneNode reads them one-directionally
    // to render the status pill + control buttons inline in the header.
    // This replaces the old vncControls global $state registry, whose
    // $effect-republish + $derived-readback formed a reactive cycle that
    // froze the whole WebView (test7). Bindings flow VncPane -> PaneNode
    // only; PaneNode invokes the handlers, it never writes the state back.
    status?: VncStatus;
    scaled?: boolean;
    dotCursor?: boolean;
    onControls?: (c: {
      toggleScale: () => void;
      toggleDotCursor: () => void;
      sendCAD: () => void;
      reconnect: () => void;
    }) => void;
  }
  let {
    sessionId,
    status = $bindable("connecting"),
    scaled = $bindable(true),
    dotCursor = $bindable(true),
    onControls,
  }: Props = $props();

  let hostEl = $state<HTMLDivElement | null>(null);
  let connectFallback: ReturnType<typeof setTimeout> | null = null;
  let connectDeadline: ReturnType<typeof setTimeout> | null = null;
  let errorMsg = $state<string | null>(null);
  // A human description of what we're waiting on, shown under the spinner.
  // Set from the session's transport so a slow jump/tunnel connect tells
  // the user WHERE it's stuck instead of a bare "Connecting...".
  let connectMsg = $state("Connecting to console...");

  // Hard ceiling on the connect phase. The bridge dials the upstream (a
  // jump-chain SSH handshake, an SSH tunnel, or a plain TCP dial) only when
  // the websocket connects, off the IPC path - but a dead bastion or a
  // firewalled RFB port would otherwise leave the spinner up forever. If
  // we're still "connecting" after this, fail with a transport-specific
  // message. Jump/tunnel get a longer budget (an SSH handshake through a
  // bastion is slower than a LAN TCP dial).
  function connectTimeoutMs(transport: string): number {
    return transport === "direct" ? 12000 : 25000;
  }
  // Credential prompt. noVNC tells us which fields a security type needs
  // (VNC auth: password; Apple ARD: username + password + target), so we
  // prompt for exactly those - sending only password to ARD loops
  // forever because username/target stay undefined.
  let needTypes = $state<string[]>([]);
  let credUser = $state("");
  let credPass = $state("");
  let credTarget = $state("");

  // noVNC RFB instance. Loaded lazily so the (sizeable) RFB core stays
  // out of the main bundle until a console is actually opened.
  let rfb: any = null;
  let destroyed = false;

  function setTabStatus(s: "connecting" | "connected" | "disconnected" | "error") {
    sessions.setStatus(sessionId, s);
  }

  async function connect() {
    if (!hostEl) return;
    status = "connecting";
    errorMsg = null;
    setTabStatus("connecting");

    let info = vncSessions.get(sessionId);
    if (!info) {
      // Detached window, or a reload: re-fetch from the backend.
      try {
        await vncSessions.refresh();
        info = vncSessions.get(sessionId);
      } catch (e: any) {
        fail(`Could not load VNC session: ${e?.message ?? String(e)}`);
        return;
      }
    }
    if (!info) {
      fail("VNC session not found (it may have been closed).");
      return;
    }

    // Describe what we're waiting on from the transport label the backend
    // attached (direct / jump:<host> / tunnel / proxmox).
    const transport = info.transport || "direct";
    if (transport.startsWith("jump:")) {
      connectMsg = `Connecting via jump host ${transport.slice(5)}...`;
    } else if (transport === "tunnel") {
      connectMsg = "Opening SSH tunnel to console...";
    } else if (transport === "proxmox") {
      connectMsg = "Connecting to Proxmox console...";
    } else {
      connectMsg = "Connecting to console (direct)...";
    }

    // Arm the hard connect deadline. Cleared by onConnect / fail / destroy.
    if (connectDeadline) clearTimeout(connectDeadline);
    connectDeadline = setTimeout(() => {
      if (destroyed || status !== "connecting") return;
      const where =
        transport.startsWith("jump:") ? ` via jump host ${transport.slice(5)}` :
        transport === "tunnel"        ? " over the SSH tunnel" : "";
      fail(`Timed out connecting to the console${where}. The RFB port may be firewalled, the host down, or the bastion unreachable.`);
    }, connectTimeoutMs(transport));

    try {
      const { default: RFB } = await import("@novnc/novnc");
      // Seed credentials so the common case (VNC auth with a saved
      // password, or a Mac whose ARD login matches the SSH login) auths
      // without ever prompting. Missing fields trigger credentialsrequired.
      const creds: { username?: string; password?: string } = {};
      if (info.username) creds.username = info.username;
      if (info.password) creds.password = info.password;
      rfb = new RFB(hostEl, info.ws_url, { credentials: creds });
      rfb.scaleViewport = scaled;
      rfb.resizeSession = false;
      rfb.background = "var(--bg, #111)";
      // macOS Screen Sharing (and some servers) send no client cursor
      // shape until something changes (a window border, an edit field),
      // leaving the pointer invisible at first. Render a dot so the
      // cursor never disappears; user can toggle it off.
      rfb.showDotCursor = dotCursor;
      rfb.addEventListener("connect", onConnect);
      rfb.addEventListener("disconnect", onDisconnect);
      rfb.addEventListener("securityfailure", onSecurityFailure);
      rfb.addEventListener("credentialsrequired", onCredsRequired);
      rfb.addEventListener("clipboard", onRemoteClipboard);
      // Safety net: if the 'connect' event doesn't arrive but the RFB is in
      // fact connected (observed: desktop frames render yet status stays
      // "connecting", so the spinner overlay keeps blocking all interaction),
      // flip to connected once noVNC reports the connected state. Without
      // this the user is stuck behind an overlay they can't dismiss.
      if (connectFallback) clearTimeout(connectFallback);
      connectFallback = setTimeout(() => {
        if (destroyed || status !== "connecting") return;
        const st = (rfb as any)?._rfbConnectionState;
        if (st === "connected") onConnect();
      }, 2500);
    } catch (e: any) {
      fail(`noVNC init failed: ${e?.message ?? String(e)}`);
    }
  }

  function clearConnectTimers() {
    if (connectFallback) { clearTimeout(connectFallback); connectFallback = null; }
    if (connectDeadline) { clearTimeout(connectDeadline); connectDeadline = null; }
  }

  function onConnect() {
    clearConnectTimers();
    if (destroyed) return;
    status = "connected";
    setTabStatus("connected");
  }

  async function onDisconnect(e: any) {
    if (destroyed) return;
    if (e?.detail?.clean) {
      status = "disconnected";
      setTabStatus("disconnected");
      return;
    }
    // Unclean close. noVNC only tells us "clean: false"; the real reason
    // (e.g. a jump-host auth failure) is on the backend - the ws relay
    // closed because the upstream open failed. Pull it so the user sees
    // WHY instead of a generic "connection closed".
    let reason = "";
    try { reason = await api.vncLastError(sessionId); } catch { /* ignore */ }
    if (destroyed) return;
    fail(reason ? prettyVncError(reason) : "Connection closed unexpectedly.");
  }

  // Turn a backend upstream-open error into a short, user-facing message.
  // The raw strings are like "vnc jump connect: jump dns.bipe.onl: ssh
  // handshake: ssh: handshake failed: ssh: unable to authenticate, ...".
  function prettyVncError(raw: string): string {
    const low = raw.toLowerCase();
    if (low.includes("unable to authenticate") || low.includes("handshake failed")) {
      const m = raw.match(/jump (\S+?):/);
      const host = m ? m[1] : "the jump host";
      return `Jump host ${host} rejected the login (check its credentials). [${raw}]`;
    }
    if (low.includes("no such host") || low.includes("lookup")) {
      return `Host not found - DNS lookup failed. [${raw}]`;
    }
    if (low.includes("connection refused")) {
      return `Connection refused - the RFB port isn't open or nothing is listening. [${raw}]`;
    }
    if (low.includes("i/o timeout") || low.includes("deadline")) {
      return `Timed out reaching the console (host down or firewalled). [${raw}]`;
    }
    return raw;
  }

  function onSecurityFailure(e: any) {
    const reason = e?.detail?.reason || "authentication failed";
    fail(`VNC security failure: ${reason}`);
  }

  // The server wants credentials noVNC doesn't have. detail.types lists
  // the fields the negotiated security type needs.
  function onCredsRequired(e: any) {
    if (destroyed) return;
    // We reached the RFB auth phase, so the upstream dial succeeded - cancel
    // the connect deadline (the user may take a while to type credentials).
    clearConnectTimers();
    const types: string[] = e?.detail?.types ?? ["password"];
    needTypes = types;
    // Pre-fill from the connection like the SSH login does, so a Mac
    // console fills username + password automatically. The user only
    // edits what's wrong (or types the target, which Macs leave blank).
    const info = vncSessions.get(sessionId);
    if (types.includes("username") && info?.username && !credUser) credUser = info.username;
    if (types.includes("password") && info?.password && !credPass) credPass = info.password;
    status = "needpass";
    setTabStatus("connecting");
  }

  function submitCreds() {
    if (!rfb) return;
    const creds: { username?: string; password?: string; target?: string } = {};
    if (needTypes.includes("username")) creds.username = credUser;
    if (needTypes.includes("password")) creds.password = credPass;
    if (needTypes.includes("target")) creds.target = credTarget;
    rfb.sendCredentials(creds);
    credPass = "";
    status = "connecting";
  }

  function fail(msg: string) {
    clearConnectTimers();
    status = "error";
    errorMsg = msg;
    setTabStatus("error");
  }

  function reconnect() {
    teardownRfb();
    connect();
  }

  function teardownRfb() {
    if (rfb) {
      try {
        rfb.removeEventListener("connect", onConnect);
        rfb.removeEventListener("disconnect", onDisconnect);
        rfb.removeEventListener("securityfailure", onSecurityFailure);
        rfb.removeEventListener("credentialsrequired", onCredsRequired);
        rfb.removeEventListener("clipboard", onRemoteClipboard);
        rfb.disconnect();
      } catch {}
      rfb = null;
    }
  }

  function toggleScale() {
    scaled = !scaled;
    if (rfb) rfb.scaleViewport = scaled;
  }

  function toggleDotCursor() {
    dotCursor = !dotCursor;
    if (rfb) rfb.showDotCursor = dotCursor;
  }

  function sendCAD() {
    if (rfb && status === "connected") rfb.sendCtrlAltDel();
  }

  function isVisible() {
    return !!hostEl && hostEl.offsetParent !== null;
  }

  // Local -> remote paste. The webview blocks navigator.clipboard
  // .readText() over a canvas, so we read the OS clipboard through the
  // native Wails clipboard IPC instead. Bound to Ctrl+V / Cmd+V.
  let lastSentClip = "";
  // Local -> remote: put the host clipboard onto the remote's clipboard
  // via RFB ClientCutText. The user then pastes with the remote's native
  // shortcut. We swallow the Ctrl/Cmd+V chord so noVNC doesn't also
  // forward the raw keystroke (which showed up as "^V" in a terminal).
  //
  // NOTE: whether the remote ADOPTS the cut-text is server-side. x11vnc,
  // TigerVNC and the Proxmox guest consoles honour it; macOS Screen
  // Sharing largely ignores incoming clipboard, so paste-from-host into a
  // Mac is unreliable regardless of what we send.
  async function onKeydown(e: KeyboardEvent) {
    if (!rfb || status !== "connected" || !isVisible()) return;

    // Numpad Enter: noVNC sends XK_KP_Enter, which Proxmox's vncterm (LXC
    // / serial consoles) doesn't treat as a carriage return - so the
    // numpad Enter does nothing. Remap it to a plain Return, which every
    // shell and desktop accepts.
    if (e.code === "NumpadEnter") {
      e.preventDefault();
      e.stopImmediatePropagation();
      const XK_Return = 0xff0d;
      rfb.sendKey(XK_Return, "Enter", true);
      rfb.sendKey(XK_Return, "Enter", false);
      return;
    }

    const paste = (e.ctrlKey || e.metaKey) && (e.key === "v" || e.key === "V");
    if (!paste) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    try {
      const text = await api.clipboardGetText();
      if (!text) return;
      lastSentClip = text;
      rfb.clipboardPasteFrom(text);
    } catch (err) {
      console.log("[vnc] paste failed", err);
    }
  }

  // Remote -> local. noVNC fires "clipboard" when the remote desktop
  // copies (RFB cut-text). Mirror it into the local OS clipboard so a
  // copy in the VM is immediately pasteable on the host. Guard against
  // echoing back the text we just sent.
  function onRemoteClipboard(e: any) {
    const text: string = e?.detail?.text ?? "";
    if (!text || text === lastSentClip) return;
    api.clipboardSetText(text).catch(() => {});
  }

  onMount(() => {
    // Hand the (stable) control handlers up to the pane header ONCE. No
    // $effect, no reactive republish - the header just calls these; status /
    // scaled / dotCursor flow up via $bindable. One-directional, so there is
    // no read-back cycle to spin the WebView (the test7 freeze).
    onControls?.({ toggleScale, toggleDotCursor, sendCAD, reconnect });
    connect();
    // Window-level so Ctrl+V works regardless of focus inside the canvas.
    // Capture phase: noVNC's canvas keydown handler runs first and would
    // otherwise swallow Ctrl+V before it reaches us.
    window.addEventListener("keydown", onKeydown, true);
  });

  onDestroy(() => {
    destroyed = true;
    clearConnectTimers();
    window.removeEventListener("keydown", onKeydown, true);
    teardownRfb();
    // Tell the backend to tear down the bridge upstream + any owned SSH
    // tunnel. Safe to call even if already closed.
    api.vncClose(sessionId);
    vncSessions.delete(sessionId);
  });
</script>

<div class="vnc-pane">
  <!-- Console controls (status + Fit / Dot cursor / Ctrl+Alt+Del / Reconnect)
       render in the pane HEADER (PaneNode), fed by $bindable status/scaled/
       dotCursor + the onControls handlers. VncPane keeps the full area for
       the screen. -->
  <div class="vnc-screen-wrap">
    <div class="vnc-screen" bind:this={hostEl}></div>
    {#if status === "connecting"}
      <div class="vnc-overlay"><div class="spinner"></div><span>{connectMsg}</span></div>
    {:else if status === "needpass"}
      <div class="vnc-overlay">
        <span>This VNC server requires {needTypes.includes("username") ? "credentials" : "a password"}.</span>
        <form class="vnc-passform" onsubmit={(e) => { e.preventDefault(); submitCreds(); }}>
          {#if needTypes.includes("username")}
            <!-- svelte-ignore a11y_autofocus -->
            <input type="text" bind:value={credUser} placeholder="Username" autofocus />
          {/if}
          {#if needTypes.includes("password")}
            <PasswordInput bind:value={credPass} placeholder="Password" />
          {/if}
          {#if needTypes.includes("target")}
            <input type="text" bind:value={credTarget} placeholder="Target (Mac: usually blank)" />
          {/if}
          <button type="submit">Connect</button>
        </form>
      </div>
    {:else if status === "error"}
      <div class="vnc-overlay error">
        <span>{errorMsg}</span>
        <button onclick={reconnect}>Retry</button>
      </div>
    {:else if status === "disconnected"}
      <div class="vnc-overlay">
        <span>Console disconnected.</span>
        <button onclick={reconnect}>Reconnect</button>
      </div>
    {/if}
  </div>
</div>

<style>
  .vnc-pane {
    display: flex;
    flex-direction: column;
    height: 100%;
    width: 100%;
    background: var(--bg, #111);
  }
  .vnc-screen-wrap {
    position: relative;
    flex: 1 1 auto;
    overflow: hidden;
    min-height: 0;
  }
  .vnc-screen {
    width: 100%;
    height: 100%;
  }
  .vnc-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.8rem;
    background: rgba(0, 0, 0, 0.62);
    /* The overlay sits on a fixed dark scrim regardless of theme, so the
       text must stay light in both themes. --text1 was undefined (fell
       back to the inherited body colour, which is DARK under Latte -
       dark text on a dark scrim, unreadable). Pin a light colour. */
    color: #dce0e8;
    font-size: 0.9rem;
    text-align: center;
    padding: 1rem;
  }
  .vnc-overlay.error { color: #f5a3a3; }
  .vnc-overlay button {
    padding: 0.3rem 0.8rem;
    background: rgba(255, 255, 255, 0.14);
    color: #dce0e8;
    border: 1px solid rgba(255, 255, 255, 0.24);
    border-radius: 4px;
    cursor: pointer;
  }
  .vnc-overlay button:hover { background: rgba(255, 255, 255, 0.22); }
  .spinner {
    width: 26px;
    height: 26px;
    border: 3px solid rgba(255, 255, 255, 0.25);
    border-top-color: var(--accent, #58a);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
