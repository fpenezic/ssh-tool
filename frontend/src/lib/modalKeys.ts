// modalKeys: a Svelte action for a modal that must own the keyboard while it
// is open (the MCP approval dialogs). Without it focus stays where it was -
// usually the terminal behind the overlay - and Escape (or any key) went to
// the session instead of answering the dialog.
//
// While mounted: the keyboard is claimed (terminals do not grab focus back),
// focus moves to the element marked data-autofocus (the safe choice, Deny),
// Escape answers onEscape wherever focus is, Tab stays inside the node. On
// destroy focus returns to where it was.
//
// Several dialogs can be open at once (a run, a facts and a plan approval
// mount independently): only the most recently mounted one answers Escape
// and traps Tab, so one key press never denies requests the user did not
// look at. Closing it hands the keyboard back to the one below.
import { claimKeyboard } from "./paneFocus";

export interface ModalKeysOptions {
  onEscape: () => void;
}

interface Owner {
  node: HTMLElement;
  opts: ModalKeysOptions;
}

const stack: Owner[] = [];

function onKey(e: KeyboardEvent) {
  const top = stack[stack.length - 1];
  if (!top) return;
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    top.opts.onEscape();
    return;
  }
  if (e.key !== "Tab") return;
  const items = [...top.node.querySelectorAll<HTMLElement>("button, input, select, textarea, [tabindex]:not([tabindex='-1'])")]
    .filter((el) => !(el as HTMLButtonElement).disabled);
  if (!items.length) return;
  const first = items[0], last = items[items.length - 1];
  const at = document.activeElement;
  if (e.shiftKey && (at === first || !top.node.contains(at))) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && (at === last || !top.node.contains(at))) { e.preventDefault(); first.focus(); }
}

function focusSafe(node: HTMLElement) {
  (node.querySelector<HTMLElement>("[data-autofocus]") ?? node.querySelector<HTMLElement>("button"))?.focus();
}

export function modalKeys(node: HTMLElement, opts: ModalKeysOptions) {
  const owner: Owner = { node, opts };
  const release = claimKeyboard();
  const before = document.activeElement as HTMLElement | null;
  if (stack.length === 0) window.addEventListener("keydown", onKey, true);
  stack.push(owner);
  const t = setTimeout(() => focusSafe(node), 0);

  return {
    update(next: ModalKeysOptions) { owner.opts = next; },
    destroy() {
      clearTimeout(t);
      const i = stack.indexOf(owner);
      const wasTop = i === stack.length - 1;
      if (i >= 0) stack.splice(i, 1);
      if (stack.length === 0) window.removeEventListener("keydown", onKey, true);
      release();
      if (wasTop && stack.length > 0) focusSafe(stack[stack.length - 1].node);
      else if (stack.length === 0) before?.focus?.();
    },
  };
}
