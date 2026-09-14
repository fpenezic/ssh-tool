// Bridge for the tab strip's "+" button.
//
// The two things it offers - open the quick palette, spawn a local shell -
// both live in App.svelte: the palette is local $state there, and
// openLocalShell closes over localShellPrefs and the session store. The "+"
// itself belongs in TerminalArea, which takes no props and talks to the
// world through stores.
//
// Rather than thread props through or duplicate the logic, App registers
// its two callbacks here on mount. Anything that cannot reach them (a
// detached window renders TerminalArea without App) simply gets a "+" that
// does nothing, so registration is checked before the button is shown.
class NewTabActions {
  openPalette = $state<(() => void) | null>(null);
  openLocalShell = $state<((kind?: string) => void) | null>(null);
  /** Short label for the local-shell entry, e.g. "PowerShell" - reflects
   *  the user's saved preference so the menu names what will actually
   *  open rather than a generic word. */
  localShellLabel = $state("Local shell");

  register(opts: {
    openPalette: () => void;
    openLocalShell: (kind?: string) => void;
  }) {
    this.openPalette = opts.openPalette;
    this.openLocalShell = opts.openLocalShell;
  }

  unregister() {
    this.openPalette = null;
    this.openLocalShell = null;
  }

  get available(): boolean {
    return this.openPalette !== null || this.openLocalShell !== null;
  }
}

export const newTabActions = new NewTabActions();
