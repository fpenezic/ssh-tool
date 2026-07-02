// Shared latch state for the on-screen terminal key bar (mobile). When the
// user taps "Ctrl" (or "Alt") on the bar and then types a letter on the
// soft keyboard, that letter flows through xterm's onData - not the bar's
// own buttons - so the bar can't transform it locally. The Terminal's
// onData consults this latch and applies the modifier to the next typed
// character, then clears it. Per-terminal isn't needed: only the focused
// terminal receives keystrokes, and the bar belongs to it.

class KeyBarMods {
  ctrl = $state(false);
  alt = $state(false);

  clear() {
    this.ctrl = false;
    this.alt = false;
  }

  // Apply latched modifiers to a raw typed string (one onData chunk),
  // returning the transformed bytes and clearing the latch. Ctrl maps a
  // letter to its control byte (a->1 .. z->26); Alt prefixes ESC.
  apply(data: string): string {
    if (!this.ctrl && !this.alt) return data;
    let out = data;
    if (this.ctrl && data.length === 1) {
      const c = data.toLowerCase().charCodeAt(0);
      if (c >= 97 && c <= 122) out = String.fromCharCode(c - 96);
      else if (data === " ") out = "\x00";
    }
    if (this.alt) out = "\x1b" + out;
    this.clear();
    return out;
  }
}

export const keyBarMods = new KeyBarMods();
