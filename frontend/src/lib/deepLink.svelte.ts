// Cross-component channel for `ssh-tool://import?source=URL`
// triggers. Backend emits `deep_link_import` (handled in App.svelte)
// which sets pendingImportURL here; Settings.svelte's import-archive
// section watches it via $effect, switches to the right section, and
// runs FetchArchiveURL automatically.

class DeepLinkStore {
  pendingImportURL = $state<string | null>(null);

  setImportURL(url: string) {
    this.pendingImportURL = url;
  }

  clearImportURL() {
    this.pendingImportURL = null;
  }
}

export const deepLink = new DeepLinkStore();

/**
 * The import source from what the deep-link event carries. Desktop sends
 * the source URL itself; Android hands over the whole
 * ssh-tool://import?source=... link, which must be unpacked (it was used
 * as the source as it was, so the import fetched a URL it cannot load).
 * null for an ssh-tool:// link that is not an import.
 */
export function importSourceFromLink(raw: string): string | null {
  if (!/^ssh-tool:\/\//i.test(raw)) return raw;
  try {
    const u = new URL("http://" + raw.slice("ssh-tool://".length));
    if (u.hostname.toLowerCase() !== "import" && u.pathname.replace(/\/+$/, "") !== "/import") return null;
    return u.searchParams.get("source") || null;
  } catch {
    return null;
  }
}
