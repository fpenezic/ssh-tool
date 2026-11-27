// modalKeys: a Svelte action for a modal that must own the keyboard while it
// is open (the MCP approval dialogs). Without it focus stays where it was -
// usually the terminal behind the overlay - and Escape (or any key) went to
// the session instead of answering the dialog.
//
// While mounted: the keyboard is claimed (terminals do not grab focus back),
// focus moves to the element marked data-autofocus (the safe choice, Deny),
// Escape answers onEscape wherever focus is, Tab stays inside the node.
// On destroy focus returns to where it was.
import { claimKeyboard } from "./paneFocus";

export interface ModalKeysOptions {
  onEscape: () => void;
}

export function modalKeys(node: HTMLElement, opts: ModalKeysOptions) {
  let current = opts;
  const release = claimKeyboard();
  const before = document.activeElement as HTMLElement | null;

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      current.onEscape();
      return;
    }
    if (e.key !== "Tab") return;
    const items = [...node.querySelectorAll<HTMLElement>("button, input, select, textarea, [tabindex]:not([tabindex='-1'])")]
      .filter((el) => !(el as HTMLButtonElement).disabled);
    if (!items.length) return;
    const first = items[0], last = items[items.length - 1];
    const at = document.activeElement;
    if (e.shiftKey && (at === first || !node.contains(at))) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && (at === last || !node.contains(at))) { e.preventDefault(); first.focus(); }
  }
  window.addEventListener("keydown", onKey, true);
  const t = setTimeout(() => {
    (node.querySelector<HTMLElement>("[data-autofocus]") ?? node.querySelector<HTMLElement>("button"))?.focus();
  }, 0);

  return {
    update(next: ModalKeysOptions) { current = next; },
    destroy() {
      clearTimeout(t);
      window.removeEventListener("keydown", onKey, true);
      release();
      before?.focus?.();
    },
  };
}
