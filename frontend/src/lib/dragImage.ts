// Custom HTML5 drag image with a "N items" badge. Default browser drag
// image only shows the actual dragged element, which makes multi-drag
// look like single-drag. We render a small DOM node off-screen, hand
// it to dataTransfer.setDragImage, and remove it on the next tick.

export function setMultiDragImage(dt: DataTransfer | null, count: number, label: string) {
  if (!dt || count <= 1) return;
  const el = document.createElement("div");
  el.style.position = "fixed";
  el.style.top = "-1000px";
  el.style.left = "-1000px";
  el.style.padding = "6px 10px";
  el.style.borderRadius = "4px";
  el.style.background = "var(--base)";
  el.style.color = "var(--text)";
  el.style.border = "1px solid var(--blue)";
  el.style.fontFamily = "ui-sans-serif, system-ui, sans-serif";
  el.style.fontSize = "12px";
  el.style.fontWeight = "500";
  el.style.whiteSpace = "nowrap";
  el.style.boxShadow = "0 2px 8px rgba(0,0,0,0.4)";
  el.textContent = `${count} ${label}`;
  document.body.appendChild(el);
  dt.setDragImage(el, 12, 12);
  // Remove on next tick - by then the browser has snapshotted it.
  setTimeout(() => el.remove(), 0);
}

// Drag image for "detaching" a terminal tab - gesture-style, not a
// real drop target. Renders a small floating chip that says
// "Detach <name>" so Windows / macOS don't fall back to a generic
// "document" icon with a no-drop slash when the user crosses the
// taskbar / other apps.
export function setTabDetachDragImage(
  dt: DataTransfer | null,
  label: string,
) {
  if (!dt) return;
  const el = document.createElement("div");
  el.style.position = "fixed";
  el.style.top = "-1000px";
  el.style.left = "-1000px";
  el.style.padding = "5px 11px";
  el.style.borderRadius = "999px";
  el.style.background = "var(--base)";
  el.style.color = "var(--text)";
  el.style.border = "1px solid var(--blue)";
  el.style.fontFamily = "ui-sans-serif, system-ui, sans-serif";
  el.style.fontSize = "11px";
  el.style.fontWeight = "600";
  el.style.whiteSpace = "nowrap";
  el.style.boxShadow = "0 4px 12px rgba(0,0,0,0.5)";
  el.textContent = `⤴ Detach: ${label}`;
  document.body.appendChild(el);
  dt.setDragImage(el, 14, 12);
  setTimeout(() => el.remove(), 0);
  // "move" reads as a green cursor in most desktop environments
  // rather than the red no-entry one.
  dt.effectAllowed = "move";
}
