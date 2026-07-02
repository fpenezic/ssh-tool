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
