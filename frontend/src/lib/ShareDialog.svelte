<script lang="ts">
  import { paneTabs, sessions, shareShared } from "./stores.svelte";
  import { api, type ShareInterface, type ShareStartResult } from "./api";
  import { toast } from "./toast.svelte";
  import { errMsg } from "./connectErrors";
  import { projectTabs, realSessionIds } from "./shareProject";

  interface Props {
    onClose: () => void;
  }
  let { onClose }: Props = $props();

  // Tab selection: default to the active tab only (snapshot; least surprise).
  let selected = $state<Set<string>>(new Set(paneTabs.activeTabId ? [paneTabs.activeTabId] : []));
  let level = $state<"read" | "control">("read");
  let scrollback = $state(false); // safer default: only new output
  let ifaces = $state<ShareInterface[]>([]);
  let bindIp = $state("");
  let port = $state(8443);

  let result = $state<ShareStartResult | null>(null);
  let starting = $state(false);
  let copied = $state(false);

  $effect(() => {
    api.shareInterfaces()
      .then((list) => {
        ifaces = list ?? [];
        if (!bindIp && ifaces.length > 0) bindIp = ifaces[0].ip;
      })
      .catch(() => {});
  });

  function toggle(tabId: string) {
    const next = new Set(selected);
    if (next.has(tabId)) next.delete(tabId);
    else next.add(tabId);
    selected = next;
  }

  function tabPaneCount(tabId: string): number {
    const t = paneTabs.tabs.find((x) => x.tabId === tabId);
    if (!t) return 0;
    let n = 0;
    const walk = (node: any) => {
      if (node.kind === "pane") n++;
      else {
        walk(node.a);
        walk(node.b);
      }
    };
    walk(t.root);
    return n;
  }

  async function start() {
    const chosen = paneTabs.tabs.filter((t) => selected.has(t.tabId));
    if (chosen.length === 0) {
      toast.err("Pick at least one tab to share");
      return;
    }
    if (!bindIp) {
      toast.err("Pick a network interface");
      return;
    }
    starting = true;
    try {
      const proj = projectTabs(chosen, (sid) => {
        const s = sessions.tabs.find((x) => x.sessionId === sid);
        return s?.name ?? s?.hostname ?? sid;
      });
      // Where the host's active tab sits within the shared set (so the guest
      // opens on the same one). -1 -> 0 if the active tab wasn't shared.
      const activeIdx = Math.max(0, chosen.findIndex((t) => t.tabId === paneTabs.activeTabId));
      const res = await api.shareStart({
        bind_ip: bindIp,
        port,
        level,
        scrollback,
        active_tab: activeIdx,
        tabs_blob: proj.tabsBlob,
        sessions: proj.sessions,
      });
      result = res;
      // Remember which real sessions this share covers (badge attribution) and
      // the tab order (so a host tab switch maps to a guest tab index).
      shareShared.recordShare(res.share_id, realSessionIds(chosen), chosen.map((t) => t.tabId));
      if (res.regenerated) {
        toast.info(
          "Your certificate fingerprint changed because you're sharing on a new network. Guests who saved the old one will see a different fingerprint.",
          8000,
        );
      }
    } catch (e) {
      toast.err("Couldn't start sharing: " + errMsg(e));
    } finally {
      starting = false;
    }
  }

  async function copyLink() {
    if (!result) return;
    try {
      await api.clipboardSetText(result.url);
      copied = true;
      setTimeout(() => (copied = false), 1600);
    } catch (e) {
      toast.err("Copy failed: " + errMsg(e));
    }
  }
</script>

<div class="overlay" role="presentation">
  <div class="modal" role="dialog" aria-modal="true" aria-label="Share to browser">
    {#if !result}
      <h2>Share to browser</h2>

      <section>
        <div class="section-title">Tabs</div>
        <div class="tab-list">
          {#each paneTabs.tabs as t (t.tabId)}
            <label class="tab-row">
              <input type="checkbox" checked={selected.has(t.tabId)} onchange={() => toggle(t.tabId)} />
              <span class="tab-name">{t.title}</span>
              <span class="pane-count">{tabPaneCount(t.tabId)} pane{tabPaneCount(t.tabId) === 1 ? "" : "s"}</span>
            </label>
          {/each}
        </div>
      </section>

      <section>
        <div class="section-title">Access</div>
        <label class="radio"><input type="radio" bind:group={level} value="read" /> Read-only</label>
        <label class="radio"><input type="radio" bind:group={level} value="control" /> Full control</label>
        {#if level === "control"}
          <div class="warn">The guest types into the same terminal as you. Anything they type runs on your servers.</div>
        {/if}
      </section>

      <section>
        <div class="section-title">History</div>
        <label class="radio"><input type="radio" bind:group={scrollback} value={false} /> Guest sees only new output</label>
        <label class="radio"><input type="radio" bind:group={scrollback} value={true} /> Guest sees existing scrollback</label>
      </section>

      <section>
        <div class="section-title">Network</div>
        <div class="net-row">
          <select bind:value={bindIp}>
            {#each ifaces as i (i.ip)}
              <option value={i.ip}>{i.name} ({i.ip})</option>
            {/each}
          </select>
          <input class="port" type="number" min="1" max="65535" bind:value={port} />
        </div>
        {#if ifaces.length === 0}
          <div class="hint">No usable network interfaces found.</div>
        {/if}
      </section>

      <div class="actions">
        <button onclick={onClose}>Cancel</button>
        <button class="primary" disabled={starting} onclick={start}>
          {starting ? "Starting…" : "Start sharing"}
        </button>
      </div>
    {:else}
      <h2>Share started</h2>
      <div class="link-row">
        <input class="url" readonly value={result.url} />
        <button onclick={copyLink}>{copied ? "✓" : "Copy"}</button>
      </div>
      <div class="fp-block">
        <div class="fp-title">Send this fingerprint to your guest separately (not in the same message as the link):</div>
        <div class="fp-words">{result.fingerprint.Words}</div>
        <div class="fp-hint">When they open the link, they'll see the same words - and you'll compare them again when you allow them in.</div>
      </div>
      <div class="actions">
        <button class="primary" onclick={onClose}>Done</button>
      </div>
    {/if}
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 330;
  }
  .modal {
    background: var(--base, #1e1e2e);
    color: var(--text, #cdd6f4);
    border: 1px solid var(--surface1, #45475a);
    border-radius: 10px;
    padding: 1.3rem 1.5rem;
    width: min(32rem, 92vw);
    max-height: 90vh;
    overflow-y: auto;
  }
  h2 {
    margin: 0 0 1rem;
    font-size: 1.15rem;
  }
  section {
    margin-bottom: 1rem;
  }
  .section-title {
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--subtext0, #a6adc8);
    margin-bottom: 0.4rem;
  }
  .tab-list {
    max-height: 12rem;
    overflow-y: auto;
    border: 1px solid var(--surface0, #313244);
    border-radius: 6px;
  }
  .tab-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.35rem 0.6rem;
  }
  .tab-name {
    flex: 1;
  }
  .pane-count {
    font-size: 0.75rem;
    color: var(--overlay1, #7f849c);
  }
  .radio {
    display: block;
    padding: 0.2rem 0;
  }
  .warn {
    margin-top: 0.4rem;
    padding: 0.5rem 0.7rem;
    background: rgba(243, 139, 168, 0.12);
    border: 1px solid var(--red, #f38ba8);
    border-radius: 6px;
    color: var(--red, #f38ba8);
    font-size: 0.82rem;
  }
  .net-row {
    display: flex;
    gap: 0.5rem;
  }
  .net-row select {
    flex: 1;
  }
  .port {
    width: 6rem;
  }
  .hint {
    font-size: 0.78rem;
    color: var(--overlay1, #7f849c);
    margin-top: 0.3rem;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.6rem;
    margin-top: 0.6rem;
  }
  button {
    padding: 0.45rem 1.1rem;
    border-radius: 6px;
    border: 1px solid var(--surface1, #45475a);
    background: var(--surface0, #313244);
    color: var(--text, #cdd6f4);
    cursor: pointer;
  }
  button.primary {
    background: var(--blue, #89b4fa);
    color: var(--base, #1e1e2e);
    border-color: var(--blue, #89b4fa);
    font-weight: 600;
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .link-row {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }
  .url {
    flex: 1;
    font-family: monospace;
    font-size: 0.85rem;
  }
  input,
  select {
    padding: 0.4rem 0.5rem;
    border-radius: 6px;
    border: 1px solid var(--surface1, #45475a);
    background: var(--mantle, #181825);
    color: var(--text, #cdd6f4);
  }
  .fp-block {
    padding: 0.8rem;
    background: var(--mantle, #181825);
    border: 1px solid var(--surface0, #313244);
    border-radius: 8px;
    margin-bottom: 1rem;
  }
  .fp-title {
    font-size: 0.8rem;
    color: var(--subtext0, #a6adc8);
    margin-bottom: 0.4rem;
  }
  .fp-words {
    font-family: monospace;
    font-size: 1.3rem;
    font-weight: 700;
    color: var(--blue, #89b4fa);
  }
  .fp-hint {
    margin-top: 0.4rem;
    font-size: 0.76rem;
    color: var(--overlay1, #7f849c);
  }
</style>
