<script lang="ts">
  // Publish a local folder to a catalog. Opened only from the folder's
  // context menu; nothing is sent until the user clicks Publish. The
  // preview is built by the backend (CatalogPublishPreview) from the
  // choices here and rebuilt whenever one changes: target, a bastion's
  // include / slot choice, a new slot's name.
  import { api, type CatalogPublishInput, type CatalogPublishTargets, type PublishPlan, type CatalogPublishResult, type SeenApp, type CatalogPublishProblem } from "./api";
  import { catalogs } from "./catalog.svelte";
  import { tree } from "./stores.svelte";
  import { toast } from "./toast.svelte.ts";
  import { errMsg } from "./connectErrors";
  import { clickOutside } from "./clickOutside";
  import { IconX, IconFolder, IconHost, IconTunnel, IconKey, IconVpn, IconBastion, IconAlertTriangle, IconLock, IconUsers } from "./iconMap";

  interface Props { folderId: string; folderName: string; onClose: () => void }
  let { folderId, folderName, onClose }: Props = $props();

  // Catalogs this machine can publish to: signed in, vault open.
  const usable = $derived(catalogs.list.filter((c) => !["no_login", "signed_out", "locked", "signing_in"].includes(c.state)));
  let catalogId = $state("");
  let targets = $state<CatalogPublishTargets | null>(null);
  let targetUUID = $state<string | null>(null);
  let jumpInclude = $state<Record<string, boolean>>({});
  let slotNames = $state<Record<string, string>>({});
  let plan = $state<PublishPlan | null>(null);
  let planErr = $state("");
  let oldApps = $state<SeenApp[]>([]);
  let reviewed = $state(false);
  let busy = $state(false);
  let result = $state<CatalogPublishResult | null>(null);

  $effect(() => {
    if (!catalogId && usable.length > 0) catalogId = usable[0].id;
  });
  api.catalogPublishOldApps().then((l) => (oldApps = l ?? [])).catch(() => {});

  // Targets per catalog.
  $effect(() => {
    const id = catalogId;
    if (!id) return;
    targets = null;
    targetUUID = null;
    api.catalogPublishTargets(id).then((t) => {
      if (id !== catalogId) return;
      targets = t;
      targetUUID = t.targets.length > 0 ? t.targets[0].uuid : t.can_create_space ? "" : null;
    }).catch((e) => (planErr = errMsg(e)));
  });

  const input = $derived<CatalogPublishInput>({
    catalog_id: catalogId,
    folder_id: folderId,
    target_uuid: targetUUID ?? "",
    jump_include: Object.keys(jumpInclude).filter((k) => jumpInclude[k]),
    slot_names: { ...slotNames },
  });

  // Preview, rebuilt on every change (debounced: slot names are typed).
  let seq = 0;
  $effect(() => {
    const inp = input;
    if (!inp.catalog_id || targetUUID === null) return;
    const mine = ++seq;
    const t = setTimeout(() => {
      api.catalogPublishPreview(inp).then((p) => {
        if (mine !== seq) return;
        plan = p;
        planErr = "";
      }).catch((e) => {
        if (mine === seq) planErr = errMsg(e);
      });
    }, 250);
    return () => clearTimeout(t);
  });

  const targetPath = $derived(targetUUID === "" ? "a new space" : (targets?.targets.find((t) => t.uuid === targetUUID)?.path ?? ""));
  const catName = $derived(catalogs.byId(catalogId)?.name ?? "the catalog");
  const freeByRecord = $derived.by(() => {
    const groups = new Map<string, { name: string; kind: string; items: PublishPlan["free_text"] }>();
    for (const f of plan?.free_text ?? []) {
      const k = f.kind + "/" + f.uuid;
      if (!groups.has(k)) groups.set(k, { name: f.name, kind: f.kind, items: [] });
      groups.get(k)!.items.push(f);
    }
    return [...groups.values()];
  });
  const counts = $derived.by(() => {
    const c = { folders: 0, connections: 0, forwards: 0 };
    for (const r of plan?.records ?? []) c[r.kind]++;
    return c;
  });
  const problemsByLocal = $derived.by(() => {
    const m = new Map<string, CatalogPublishProblem[]>();
    for (const p of result?.problems ?? []) {
      const k = p.local_id || p.name;
      m.set(k, [...(m.get(k) ?? []), p]);
    }
    return m;
  });
  const refusals = $derived(result?.refusals?.length ? result.refusals : (plan?.refusals ?? []));
  const needsReview = $derived((plan?.free_text.length ?? 0) > 0 && !reviewed);
  const canPublish = $derived(!!plan && !busy && refusals.length === 0 && !needsReview && targetUUID !== null && !result?.unknown);

  function depth(path: string) {
    return Math.max(0, path.split("/").length - 1);
  }
  function daysAgo(ts: number) {
    const d = Math.floor((Date.now() / 1000 - ts) / 86400);
    return d <= 0 ? "today" : d === 1 ? "1 day ago" : `${d} days ago`;
  }
  const fieldNames: Record<string, string> = {
    notes: "Notes", tags: "Tags", initial_command: "Initial command", env_vars: "Environment variables",
    ssh_options: "SSH options", kafka_bootstrap: "Kafka bootstrap", db_addr: "Database address", db_name: "Database name",
    db_label: "Database label", description: "Description", remote_host: "Remote host", "jump_host.hostname": "Jump host",
    name: "Slot name", username: "Username", auth_ref: "Credential", jump_host: "Jump host", network_profile_id: "Network",
  };
  const fieldName = (f: string) => fieldNames[f] ?? (f.startsWith("db_targets.") ? "Database target" : f);

  async function publish() {
    if (!canPublish) return;
    busy = true;
    result = null;
    try {
      const r = await api.catalogPublish(input);
      result = r;
      if (r.published) {
        toast.ok(`${folderName} published to ${catName}${r.message && r.message !== "published" ? " - " + r.message : ""}`, 8000);
        await tree.load();
        onClose();
      }
    } catch (e) {
      result = { published: false, unknown: false, message: errMsg(e) };
    } finally {
      busy = false;
    }
  }
</script>

<div class="overlay" role="presentation">
  <div class="modal" role="dialog" aria-modal="true" tabindex="-1"
    use:clickOutside={{ onOutside: () => { if (!busy) onClose(); } }}
    onkeydown={(e) => { if (e.key === "Escape" && !busy) onClose(); }}>
    <header>
      <h2><IconUsers size={15} /> Publish "{folderName}" to a catalog</h2>
      <button class="close" onclick={onClose} title="Close (Esc)" disabled={busy}><IconX size={14} /></button>
    </header>

    {#if usable.length === 0}
      <p class="hint">Sign in to a catalog first (Settings - Catalogs).</p>
    {:else}
      <div class="row">
        {#if usable.length > 1}
          <label>Catalog
            <select bind:value={catalogId}>
              {#each usable as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
            </select>
          </label>
        {/if}
        <label>Into
          {#if targets && (targets.targets.length > 0 || targets.can_create_space)}
            <select bind:value={targetUUID}>
              {#each targets.targets as t (t.uuid)}<option value={t.uuid}>{t.path}</option>{/each}
              {#if targets.can_create_space}<option value="">New space (top level)</option>{/if}
            </select>
          {:else if targets}
            <span class="warn-text">You cannot add to any folder of {catName}.</span>
          {:else}
            <span class="dim">loading...</span>
          {/if}
        </label>
      </div>

      <div class="notes">
        <p><IconFolder size={12} /> "{folderName}" moves into {targetPath || "the catalog"} in your tree. Same connections and settings, same behaviour; they become catalog records that everyone with access to that folder sees.</p>
        <p><IconLock size={12} /> Stored passwords, keys and tokens are never sent. Each colleague binds the slots below to their own.</p>
        {#if oldApps.length > 0}
          {#each oldApps as o (o.device)}
            <p class="warn-text"><IconAlertTriangle size={12} /> {o.device} ran ssh-tool {o.version}, last seen {daysAgo(o.last_seen)} - it will stop showing this folder until it is updated.</p>
          {/each}
        {:else}
          <p class="dim">Machines on your sync profile that still run ssh-tool 1.2 or older stop showing this folder until they are updated.</p>
        {/if}
      </div>

      {#if planErr}<div class="err">{planErr}</div>{/if}

      {#if refusals.length > 0}
        <section class="box refuse">
          <h3><IconAlertTriangle size={13} /> Cannot publish yet</h3>
          <p class="dim">Move these out of the folder, or fix them, then try again.</p>
          <ul>{#each refusals as r, ri (ri)}<li><strong>{r.name}</strong>: {r.reason}</li>{/each}</ul>
        </section>
      {/if}

      {#if result && !result.published}
        <section class="box" class:refuse={!result.unknown} class:unknown={result.unknown}>
          <h3><IconAlertTriangle size={13} /> {result.unknown ? "Outcome unknown" : "Not published"}</h3>
          <p>{result.message}</p>
          {#if result.unknown}<p class="dim">Your folder stays as it is. The next sync shows whether the publish arrived and finishes it, or that it did not.</p>{/if}
        </section>
      {/if}

      {#if plan}
        <div class="scroll">
          <section>
            <h3>What goes <span class="dim">{counts.folders} folder{counts.folders === 1 ? "" : "s"}, {counts.connections} connection{counts.connections === 1 ? "" : "s"}, {counts.forwards} forward{counts.forwards === 1 ? "" : "s"}</span></h3>
            <ul class="tree">
              {#each plan.records as r (r.uuid)}
                {@const probs = problemsByLocal.get(r.local_id || r.name) ?? []}
                <li style="padding-left: {depth(r.path) * 14}px" class:bad={probs.length > 0}>
                  {#if r.kind === "folders"}<IconFolder size={12} />{:else if r.kind === "connections"}<IconHost size={12} />{:else}<IconTunnel size={12} />{/if}
                  {r.name}
                  {#if r.copy_of}<span class="tag">copy of your bastion</span>{/if}
                  {#each probs as p, pi (pi)}<span class="prob">{p.field ? fieldName(p.field) + ": " : ""}{p.message}</span>{/each}
                </li>
              {/each}
            </ul>
            {#each (result?.problems ?? []).filter((p) => !p.local_id && !plan?.records.some((r) => r.name === p.name)) as p, pi (pi)}
              <p class="prob">{p.name}: {p.field ? fieldName(p.field) + " - " : ""}{p.message}</p>
            {/each}
          </section>

          {#if plan.slots.length > 0}
            <section>
              <h3>Slots <span class="dim">placeholders each reader binds to their own; yours are bound to what you use today</span></h3>
              {#each plan.slots as s (s.key)}
                <div class="slot" class:reused={!s.new}>
                  {#if s.type === "cred"}<IconKey size={13} />{:else if s.type === "net"}<IconVpn size={13} />{:else}<IconBastion size={13} />{/if}
                  {#if s.new}
                    <input class="slot-name" value={slotNames[s.key] ?? s.name} oninput={(e) => (slotNames = { ...slotNames, [s.key]: (e.currentTarget as HTMLInputElement).value })} title="The name colleagues see. Do not put customer details or secrets here." />
                    <span class="tag">new</span>
                  {:else}
                    <strong>{s.name}</strong>
                    <span class="tag reused-tag">reused from {s.reused_from || "the catalog"}</span>
                  {/if}
                  {#if s.kind === "external"}<span class="tag">{s.backend} item</span>{/if}
                  <span class="dim">yours: {s.bind_name} - used by {s.used_by}</span>
                  {#if !s.new}<p class="reused-note">Already in the catalog: whoever has bound it there reaches these connections with it too.</p>{/if}
                </div>
              {/each}
            </section>
          {/if}

          {#if plan.outside_jumps.length > 0}
            <section>
              <h3>Jump hosts outside the folder</h3>
              {#each plan.outside_jumps as j (j.connection_id)}
                <div class="jump">
                  <div><IconBastion size={13} /> <strong>{j.name}</strong> <span class="dim">used by {j.used_by.join(", ")}</span></div>
                  <label><input type="radio" name={"j-" + j.connection_id} checked={!jumpInclude[j.connection_id]} onchange={() => (jumpInclude = { ...jumpInclude, [j.connection_id]: false })} /> A jump slot: each colleague uses their own bastion (yours stays bound to {j.name})</label>
                  <label><input type="radio" name={"j-" + j.connection_id} checked={!!jumpInclude[j.connection_id]} onchange={() => (jumpInclude = { ...jumpInclude, [j.connection_id]: true })} /> Publish a copy of {j.name} inside the folder: a copy, independent from now on; your own {j.name} stays where it is</label>
                </div>
              {/each}
            </section>
          {/if}

          {#if plan.free_text.length > 0}
            <section>
              <details ontoggle={(e) => { if ((e.currentTarget as HTMLDetailsElement).open) reviewed = true; }}>
                <summary><strong>Review free text before sending</strong> <span class="dim">{plan.free_text.length} value{plan.free_text.length === 1 ? "" : "s"}: a secret typed into one of these would reach the catalog</span></summary>
                {#each freeByRecord as g, gi (gi)}
                  <div class="ft-rec">
                    <div class="ft-head">{#if g.kind === "folders"}<IconFolder size={12} />{:else if g.kind === "connections"}<IconHost size={12} />{:else if g.kind === "slots"}<IconKey size={12} />{:else}<IconTunnel size={12} />{/if} {g.name}</div>
                    {#each g.items as f, fi (fi)}
                      <div class="ft-item">
                        <span class="ft-field">{fieldName(f.field)}</span>
                        {#if f.inherited_from}<span class="tag">inherited from {f.inherited_from}</span>{/if}
                        {#if f.folder_level}<span class="tag warn-tag" title="Set on a folder, it reaches everyone who can read any folder below it, also through the folder outline">folder level</span>{/if}
                        <pre>{f.value}</pre>
                      </div>
                    {/each}
                  </div>
                {/each}
              </details>
            </section>
          {/if}
        </div>
      {/if}

      <footer>
        {#if needsReview}<span class="dim">Open the free-text review first.</span>{/if}
        <button onclick={onClose} disabled={busy}>{result?.unknown ? "Close" : "Cancel"}</button>
        <button class="primary" onclick={publish} disabled={!canPublish}>{busy ? "Publishing..." : "Publish"}</button>
      </footer>
    {/if}
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(17, 17, 27, 0.6); z-index: 1000; display: flex; align-items: center; justify-content: center; }
  .modal { background: var(--base); color: var(--text); border: 1px solid var(--surface0); border-radius: var(--r-md); width: min(820px, 94vw); max-height: 90vh; display: flex; flex-direction: column; gap: 0.5rem; padding: 1rem 1.2rem; box-shadow: 0 8px 30px rgba(0,0,0,0.5); }
  header { display: flex; align-items: center; justify-content: space-between; }
  header h2 { margin: 0; font-size: var(--fs-md); display: flex; align-items: center; gap: 6px; }
  .close { background: transparent; border: 0; color: var(--subtext0); cursor: pointer; padding: 0.15rem 0.35rem; border-radius: var(--r-sm); }
  .close:hover { background: var(--surface0); color: var(--text); }
  .row { display: flex; gap: 1rem; flex-wrap: wrap; }
  .row label { display: flex; align-items: center; gap: 0.4rem; font-size: var(--fs-sm); }
  .notes p { margin: 0.15rem 0; font-size: var(--fs-sm); display: flex; align-items: flex-start; gap: 5px; line-height: 1.45; }
  .hint, .dim { color: var(--subtext0); font-size: var(--fs-sm); }
  .warn-text { color: var(--yellow); }
  .err { color: var(--red); font-size: var(--fs-sm); }
  .scroll { overflow: auto; display: flex; flex-direction: column; gap: 0.6rem; border-top: 1px solid var(--surface0); padding-top: 0.5rem; }
  h3 { margin: 0 0 0.3rem; font-size: var(--fs-sm); display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  h3 .dim { font-weight: 400; }
  .box { border: 1px solid var(--surface0); border-radius: var(--r-md); padding: 0.4rem 0.6rem; font-size: var(--fs-sm); }
  .box p { margin: 0.2rem 0; }
  .box ul { margin: 0.2rem 0 0; padding-left: 1.1rem; }
  .box.refuse { border-color: color-mix(in srgb, var(--red) 50%, var(--surface0)); }
  .box.refuse h3 { color: var(--red); }
  .box.unknown { border-color: color-mix(in srgb, var(--yellow) 55%, var(--surface0)); }
  .box.unknown h3 { color: var(--yellow); }
  .tree { list-style: none; margin: 0; padding: 0; font-size: var(--fs-sm); }
  .tree li { display: flex; align-items: center; gap: 5px; padding: 1px 0; flex-wrap: wrap; }
  .tree li.bad { color: var(--red); }
  .prob { color: var(--red); font-size: var(--fs-sm); margin-left: 6px; }
  .tag { font-size: 0.72rem; padding: 0 6px; border-radius: 999px; border: 1px solid var(--surface1); color: var(--subtext0); }
  .warn-tag { color: var(--yellow); border-color: color-mix(in srgb, var(--yellow) 45%, transparent); }
  .reused-tag { color: var(--peach); border-color: color-mix(in srgb, var(--peach) 55%, transparent); font-weight: 600; }
  .slot { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; padding: 3px 6px; border-radius: var(--r-sm); font-size: var(--fs-sm); }
  .slot.reused { border: 1px solid color-mix(in srgb, var(--peach) 45%, transparent); }
  .slot-name { width: 14rem; }
  .reused-note { flex-basis: 100%; margin: 0 0 0 20px; color: var(--peach); font-size: var(--fs-sm); }
  .jump { display: flex; flex-direction: column; gap: 2px; font-size: var(--fs-sm); padding: 2px 0 6px; }
  .jump label { display: flex; align-items: flex-start; gap: 5px; margin-left: 18px; }
  details summary { cursor: pointer; font-size: var(--fs-sm); }
  .ft-rec { margin: 0.4rem 0 0 0.4rem; }
  .ft-head { display: flex; align-items: center; gap: 5px; font-weight: 600; font-size: var(--fs-sm); }
  .ft-item { margin-left: 18px; display: flex; flex-wrap: wrap; align-items: center; gap: 5px; font-size: var(--fs-sm); }
  .ft-field { color: var(--subtext1); }
  .ft-item pre { flex-basis: 100%; margin: 1px 0 4px; padding: 3px 6px; background: var(--mantle); border-radius: var(--r-sm); white-space: pre-wrap; word-break: break-all; font-size: var(--fs-sm); }
  footer { display: flex; justify-content: flex-end; align-items: center; gap: 0.5rem; border-top: 1px solid var(--surface0); padding-top: 0.5rem; }
  footer .dim { margin-right: auto; }
  button.primary { background: var(--blue); color: var(--base); border: 0; }
  button.primary:disabled { opacity: 0.5; }
</style>
