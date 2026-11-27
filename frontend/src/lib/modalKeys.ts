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
// and traps Tab, and it is also drawn in front (its overlay's z-index
// follows the stack), so one key press never answers a request the user
// cannot see. Closing it hands the keyboard back to the one below.
//
// A different dialog above them (the Ctrl+K palette) keeps its own keys:
// a key whose target sits in an overlay outside this stack is left alone.
import { claimKeyboard } from "./paneFocus";

export interface ModalKeysOptions {
  onEscape: () => void;
}

interface Owner {
  node: HTMLElement;
  opts: ModalKeysOptions;
}

const stack: Owner[] = [];
const BASE_Z = 100;
// Where focus was before the first dialog opened; it goes back there when
// the last one closes (dialogs replaced in between never become the target).
let restoreTo: HTMLElement | null = null;

function overlayOf(node: HTMLElement): HTMLElement {
  return (node.closest(".overlay") as HTMLElement | null) ?? node;
}

// The key belongs to another dialog (e.g. the palette opened over an
// approval) when its target is inside an overlay none of ours owns.
function foreign(target: EventTarget | null): boolean {
  const el = target instanceof Element ? target.closest(".overlay, [role='dialog']") : null;
  return !!el && !stack.some((o) => overlayOf(o.node) === el || o.node.contains(el));
}

function onKey(e: KeyboardEvent) {
  const top = stack[stack.length - 1];
  if (!top || foreign(e.target)) return;
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
  if (stack.length === 0) {
    restoreTo = document.activeElement as HTMLElement | null;
    window.addEventListener("keydown", onKey, true);
  }
  stack.push(owner);
  overlayOf(node).style.zIndex = String(BASE_Z + stack.length);
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
      else if (stack.length === 0) {
        if (restoreTo?.isConnected) restoreTo.focus?.();
        restoreTo = null;
      }
    },
  };
}
