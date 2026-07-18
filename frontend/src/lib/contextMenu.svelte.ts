// Tiny global popover menu store. One menu visible at a time; TreeNode
// and Sidebar both push onto it. Rendered by App.svelte so it can sit
// above the rest of the UI without z-index gymnastics inside the tree.

import type { Component } from "svelte";

export interface MenuItem {
  label: string;
  // A lucide icon component (preferred, renders identically on every
  // platform). `icon` (a text glyph) is the legacy fallback.
  iconComponent?: Component<{ size?: number }>;
  icon?: string;
  danger?: boolean;
  disabled?: boolean;
  onSelect: () => void;
}

class ContextMenuStore {
  open = $state(false);
  x = $state(0);
  y = $state(0);
  items = $state<MenuItem[]>([]);

  show(e: MouseEvent, items: MenuItem[]) {
    e.preventDefault();
    this.x = e.clientX;
    this.y = e.clientY;
    this.items = items;
    this.open = true;
  }
  close() {
    this.open = false;
    this.items = [];
  }
  pick(item: MenuItem) {
    this.close();
    if (!item.disabled) item.onSelect();
  }
}

export const contextMenu = new ContextMenuStore();
