<script lang="ts">
  // HTTP / SOAP request tool. Fires one request through the backend
  // (Go net/http), optionally routing via a SOCKS5 dynamic forward
  // that's already running on the active session - so endpoints
  // inside the remote network become reachable without curl
  // gymnastics. JSON / XML responses get pretty-printed automatically;
  // anything else is shown raw.
  //
  // Request body is sent verbatim - the user picks Content-Type via
  // the headers list. A "preset" Content-Type quick-pick is offered
  // to cover the common cases (JSON, XML/SOAP, form-urlencoded).

  import { onMount, onDestroy } from "svelte";
  import { errMsg } from "./connectErrors";
  import { api, type HttpRequest, type HttpResponse, type HttpHeader, type ForwardStatus } from "./api";
  import { clickOutside } from "./clickOutside";
  import { copyText } from "./clipboard";

  interface Props {
    sessionId: string;
    onClose: () => void;
  }
  let { sessionId, onClose }: Props = $props();

  // ---- form state ----
  let method = $state<string>("GET");
  let url = $state<string>("");
  let headers = $state<HttpHeader[]>([{ name: "", value: "" }]);
  let body = $state<string>("");
  let tlsSkipVerify = $state(false);
  let timeoutSeconds = $state(60);

  // Active SOCKS5 forwards on this session - populated on mount.
  // "" = direct (no proxy). Anything else is host:port.
  let socksOptions = $state<Array<{ label: string; addr: string }>>([{ label: "Direct (no proxy)", addr: "" }]);
  let socksAddr = $state<string>("");

  // ---- response state ----
  let loading = $state(false);
  let response = $state<HttpResponse | null>(null);
  let errorMsg = $state<string | null>(null);
  let bodyView = $state<"pretty" | "raw">("pretty");

  onMount(async () => {
    try {
      const all = (await api.forwardsActive(sessionId)) ?? [];
      const dyn = (all as ForwardStatus[]).filter(
        (f) => f.kind === "dynamic" && f.state === "listening" && f.local_port > 0,
      );
      for (const f of dyn) {
        const addr = `${f.local_addr || "127.0.0.1"}:${f.local_port}`;
        socksOptions = [...socksOptions, { label: `SOCKS5 → ${addr}`, addr }];
      }
      // Auto-pick the first SOCKS5 if one exists - the most common
      // workflow is "open this URL via the proxy", not "hit my local
      // box".
      const firstSocks = socksOptions.find((o) => o.addr !== "");
      if (firstSocks) socksAddr = firstSocks.addr;
    } catch {
      /* leave default */
    }
  });

  onDestroy(() => {
    // Nothing to clean up - HttpDo is one-shot.
  });

  function addHeader() {
    headers = [...headers, { name: "", value: "" }];
  }
  function removeHeader(i: number) {
    headers = headers.filter((_, idx) => idx !== i);
    if (headers.length === 0) headers = [{ name: "", value: "" }];
  }

  function setContentType(ct: string) {
    // Update an existing Content-Type header in place, or add one.
    const idx = headers.findIndex((h) => h.name.toLowerCase() === "content-type");
    if (idx >= 0) {
      headers = headers.map((h, i) => (i === idx ? { ...h, value: ct } : h));
    } else {
      // Prefer to replace the first blank row so we don't accumulate empties.
      const blank = headers.findIndex((h) => !h.name.trim());
      if (blank >= 0) {
        headers = headers.map((h, i) => (i === blank ? { name: "Content-Type", value: ct } : h));
      } else {
        headers = [...headers, { name: "Content-Type", value: ct }];
      }
    }
  }

  async function send() {
    if (!url.trim()) { errorMsg = "URL required"; return; }
    errorMsg = null;
    response = null;
    loading = true;
    const req: HttpRequest = {
      method,
      url: url.trim(),
      headers: headers.filter((h) => h.name.trim()),
      body,
      tls_skip_verify: tlsSkipVerify,
      socks_addr: socksAddr,
      timeout_seconds: timeoutSeconds || 60,
    };
    try {
      response = await api.httpDo(req);
    } catch (e: any) {
      errorMsg = errMsg(e);
    } finally {
      loading = false;
    }
  }

  // Pretty-print the response body based on Content-Type. JSON and
  // XML are reformatted; everything else is returned as-is. Errors
  // (e.g. malformed JSON) fall back to the raw body.
  const responseContentType = $derived.by(() => {
    if (!response) return "";
    const h = response.headers.find((h) => h.name.toLowerCase() === "content-type");
    return (h?.value ?? "").toLowerCase();
  });

  const isJson = $derived(responseContentType.includes("json"));
  const isXml = $derived(
    responseContentType.includes("xml") || responseContentType.includes("soap")
  );

  const prettyBody = $derived.by(() => {
    if (!response) return "";
    if (bodyView === "raw") return response.body;
    if (isJson) {
      try {
        return JSON.stringify(JSON.parse(response.body), null, 2);
      } catch {
        return response.body;
      }
    }
    if (isXml) {
      return prettyXml(response.body);
    }
    return response.body;
  });

  // Very small XML pretty-printer. Doesn't try to be clever about
  // CDATA / comments / namespaces; just indents based on open/close
  // tag balance. Good enough for SOAP envelopes.
  function prettyXml(src: string): string {
    let out = "";
    let depth = 0;
    const tokens = src
      .replace(/>\s*</g, "><")
      .split(/(<[^>]+>)/g)
      .filter(Boolean);
    for (const tok of tokens) {
      if (!tok) continue;
      if (tok.startsWith("</")) {
        depth = Math.max(0, depth - 1);
        out += "  ".repeat(depth) + tok + "\n";
      } else if (tok.startsWith("<?") || tok.startsWith("<!")) {
        out += "  ".repeat(depth) + tok + "\n";
      } else if (tok.startsWith("<") && tok.endsWith("/>")) {
        out += "  ".repeat(depth) + tok + "\n";
      } else if (tok.startsWith("<")) {
        out += "  ".repeat(depth) + tok + "\n";
        depth += 1;
      } else {
        // Text content between tags.
        const text = tok.trim();
        if (text) out += "  ".repeat(depth) + text + "\n";
      }
    }
    return out.trimEnd();
  }

  function statusColor(code: number): string {
    if (code >= 200 && code < 300) return "var(--green)";
    if (code >= 300 && code < 400) return "var(--blue)";
    if (code >= 400 && code < 500) return "var(--yellow)";
    return "var(--red)";
  }

  async function copyBody() {
    if (!response) return;
    try { await copyText(prettyBody, { label: "Response" }); } catch { /* ignore */ }
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" tabindex="-1"
     onkeydown={(e) => { if (e.key === "Escape") onClose(); }}>
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal" role="document"
       use:clickOutside={{ onOutside: onClose }}
       onkeydown={(e) => e.stopPropagation()}>
    <header>
      <strong>HTTP request</strong>
      <button class="close" onclick={onClose} title="Close">✕</button>
    </header>

    <div class="req-line">
      <select bind:value={method}>
        {#each ["GET","POST","PUT","PATCH","DELETE","HEAD","OPTIONS"] as m (m)}
          <option value={m}>{m}</option>
        {/each}
      </select>
      <input
        bind:value={url}
        placeholder="https://api.example.com/v1/things"
        onkeydown={(e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) send(); }}
      />
      <button class="primary" onclick={send} disabled={loading}>
        {loading ? "Sending…" : "Send"}
      </button>
    </div>

    <div class="opts">
      <label>
        <span>Route via</span>
        <select bind:value={socksAddr}>
          {#each socksOptions as opt (opt.addr || "direct")}
            <option value={opt.addr}>{opt.label}</option>
          {/each}
        </select>
      </label>
      <label class="chk">
        <input type="checkbox" bind:checked={tlsSkipVerify} />
        <span>Skip TLS verify</span>
      </label>
      <label class="num">
        <span>Timeout (s)</span>
        <input type="number" min="1" max="600" bind:value={timeoutSeconds} />
      </label>
    </div>

    <div class="hdr-section">
      <div class="hdr-head">
        <strong>Headers</strong>
        <div class="ct-presets">
          <button onclick={() => setContentType("application/json")}>JSON</button>
          <button onclick={() => setContentType("text/xml; charset=utf-8")}>SOAP/XML</button>
          <button onclick={() => setContentType("application/x-www-form-urlencoded")}>Form</button>
        </div>
      </div>
      <div class="hdr-list">
        {#each headers as h, i (i)}
          <div class="hdr-row">
            <input class="hname" placeholder="Header name" bind:value={headers[i].name} />
            <input class="hval" placeholder="value" bind:value={headers[i].value} />
            <button class="del" onclick={() => removeHeader(i)} title="Remove">✕</button>
          </div>
        {/each}
        <button class="add-hdr" onclick={addHeader}>+ Add header</button>
      </div>
    </div>

    <div class="body-section">
      <strong>Body</strong>
      <textarea bind:value={body} rows="6" placeholder="Request body - JSON / XML / form-encoded / raw"></textarea>
    </div>

    {#if errorMsg}
      <div class="err">⚠ {errorMsg}</div>
    {/if}

    {#if response}
      <div class="resp">
        <div class="resp-head">
          <span class="status" style="color: {statusColor(response.status_code)}">
            {response.status}
          </span>
          <span class="duration">{response.duration_ms} ms</span>
          {#if response.truncated}<span class="warn-pill">body truncated</span>{/if}
          <div class="body-toggle">
            <button class:active={bodyView === "pretty"} onclick={() => (bodyView = "pretty")}>Pretty</button>
            <button class:active={bodyView === "raw"} onclick={() => (bodyView = "raw")}>Raw</button>
            <button onclick={copyBody} title="Copy body">📋</button>
          </div>
        </div>
        <details class="resp-headers" open={false}>
          <summary>{response.headers.length} response headers</summary>
          <table>
            <tbody>
              {#each response.headers as h, i (i)}
                <tr><td class="hk">{h.name}</td><td class="hv">{h.value}</td></tr>
              {/each}
            </tbody>
          </table>
        </details>
        <pre class="resp-body">{prettyBody}</pre>
      </div>
    {/if}
  </div>
</div>

<style>
  .overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: flex-start; justify-content: center;
    z-index: 320;
    padding-top: 5vh;
  }
  .modal {
    background: var(--base); color: var(--text);
    border: 1px solid var(--surface0); border-radius: 8px;
    width: min(960px, 95vw);
    max-height: 90vh;
    display: flex; flex-direction: column;
    overflow: hidden;
    box-shadow: 0 20px 60px rgba(0,0,0,0.6);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.55rem 0.9rem;
    background: var(--mantle);
    border-bottom: 1px solid var(--surface0);
    font-size: 0.9rem;
  }
  .close {
    background: transparent; color: var(--subtext0); border: 0;
    cursor: pointer; font: inherit; padding: 0 0.4rem;
  }
  .close:hover { color: var(--red); }

  .req-line {
    display: flex;
    gap: 0.4rem;
    padding: 0.55rem 0.9rem;
    border-bottom: 1px solid var(--surface0);
  }
  .req-line select { width: 5.5rem; }
  .req-line input { flex: 1; }
  .req-line button { padding: 0.4rem 1rem; }

  .opts {
    display: flex;
    gap: 1rem;
    align-items: center;
    padding: 0.5rem 0.9rem;
    border-bottom: 1px solid var(--surface0);
    background: var(--crust);
    flex-wrap: wrap;
  }
  .opts label {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.78rem;
    color: var(--subtext0);
  }
  .opts label.chk { color: var(--text); }
  .opts label.num input { width: 4rem; }

  .hdr-section, .body-section {
    padding: 0.5rem 0.9rem;
    border-bottom: 1px solid var(--surface0);
  }
  .hdr-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.35rem;
    font-size: 0.85rem;
  }
  .ct-presets { display: flex; gap: 0.25rem; }
  .ct-presets button {
    font-size: 0.7rem;
    padding: 0.15rem 0.45rem;
    background: var(--surface0);
    color: var(--subtext0);
  }
  .ct-presets button:hover { background: var(--surface1); color: var(--text); }
  .hdr-row {
    display: flex;
    gap: 0.35rem;
    margin-bottom: 0.25rem;
  }
  .hdr-row .hname { width: 30%; }
  .hdr-row .hval { flex: 1; }
  .hdr-row .del {
    background: transparent;
    color: var(--overlay0);
    border: 0;
    padding: 0 0.45rem;
    cursor: pointer;
    font: inherit;
  }
  .hdr-row .del:hover { color: var(--red); }
  .add-hdr {
    background: transparent;
    color: var(--blue);
    border: 1px dashed var(--surface1);
    padding: 0.2rem 0.6rem;
    border-radius: 3px;
    font-size: 0.78rem;
    cursor: pointer;
  }
  .body-section strong { font-size: 0.85rem; display: block; margin-bottom: 0.3rem; }
  .body-section textarea {
    width: 100%;
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.45rem 0.6rem;
    font-family: ui-monospace, "JetBrains Mono", monospace;
    font-size: 0.82rem;
  }

  .err {
    padding: 0.5rem 0.9rem;
    background: color-mix(in oklab, var(--red) 14%, var(--bg-panel));
    color: var(--red);
    font-size: 0.85rem;
    border-bottom: 1px solid var(--surface0);
  }

  .resp {
    flex: 1;
    overflow-y: auto;
    padding: 0.5rem 0.9rem;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    min-height: 0;
  }
  .resp-head {
    display: flex;
    align-items: center;
    gap: 0.8rem;
    flex-wrap: wrap;
  }
  .status { font-weight: 600; font-size: 0.95rem; }
  .duration { color: var(--overlay0); font-size: 0.78rem; }
  .warn-pill {
    background: var(--yellow);
    color: var(--on-accent);
    border-radius: 999px;
    padding: 0.05rem 0.55rem;
    font-size: 0.7rem;
    font-weight: 600;
  }
  .body-toggle { margin-left: auto; display: flex; gap: 0.2rem; }
  .body-toggle button {
    background: transparent;
    color: var(--subtext0);
    border: 1px solid var(--surface0);
    padding: 0.1rem 0.55rem;
    font-size: 0.72rem;
    border-radius: 3px;
  }
  .body-toggle button.active {
    background: var(--surface0);
    color: var(--text);
    border-color: var(--surface1);
  }
  .resp-headers {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.4rem 0.6rem;
    font-size: 0.78rem;
  }
  .resp-headers summary {
    cursor: pointer;
    color: var(--subtext0);
  }
  .resp-headers table {
    margin-top: 0.4rem;
    border-collapse: collapse;
    width: 100%;
  }
  .resp-headers td { padding: 0.15rem 0.45rem; vertical-align: top; }
  .resp-headers td.hk { color: var(--yellow); white-space: nowrap; }
  .resp-headers td.hv {
    color: var(--text);
    font-family: ui-monospace, monospace;
    word-break: break-all;
  }
  .resp-body {
    background: var(--mantle);
    border: 1px solid var(--surface0);
    border-radius: 4px;
    padding: 0.55rem 0.7rem;
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    white-space: pre-wrap;
    word-break: break-word;
    margin: 0;
    max-height: 30rem;
    overflow-y: auto;
  }

  input, select, textarea {
    background: var(--mantle);
    color: var(--text);
    border: 1px solid var(--surface0);
    border-radius: 3px;
    padding: 0.3rem 0.5rem;
    font: inherit;
    font-size: 0.85rem;
  }
  button {
    background: var(--surface0);
    color: var(--text);
    border: 0;
    border-radius: 3px;
    padding: 0.3rem 0.7rem;
    cursor: pointer;
    font: inherit;
    font-size: 0.82rem;
  }
  button:hover:not(:disabled) { background: var(--surface1); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .primary { background: var(--blue); color: var(--on-accent); font-weight: 600; }
  .primary:hover:not(:disabled) { background: var(--lavender); }
</style>
