// Unicode width provider that widens emoji carrying a variation selector.
//
// A group of emoji default to TEXT presentation and are only drawn as
// emoji when followed by U+FE0F (VS16): the warning sign, gear, heart,
// sun, recycle arrows, check mark and a dozen more, all in the U+2xxx
// block. Unicode's own width tables call the base character narrow, and
// the v11 addon reports width 1 for the pair.
//
// Every current terminal - Windows Terminal, WezTerm, iTerm2, kitty -
// draws that pair two cells wide instead, following UTS #51's emoji
// presentation rule, and so does every TUI that lays out tables: they
// count 2 and pad accordingly. Measured against the same output, Windows
// Terminal renders it correctly while we did not.
//
// The visible cost of the mismatch is one column per occurrence: table
// borders drift, and the character after the emoji is overwritten. A
// "treba (warning) restart" cell ended up a column short with its border
// pushed into the neighbouring column.
//
// So: delegate everything to the wrapped provider, and override only the
// VS16 join so the pair measures 2.

import type { Terminal, ITerminalAddon } from "@xterm/xterm";
import { Unicode11Addon } from "@xterm/addon-unicode11";

const VS16 = 0xfe0f;

// Bit layout of UnicodeCharProperties, from xterm's UnicodeService:
//   bit 0      shouldJoin
//   bits 1-2   width
//   bits 3+    provider-private state
// Reproduced rather than imported: the helpers are not part of the
// public API surface.
const shouldJoinOf = (v: number): boolean => (v & 1) !== 0;
const widthOf = (v: number): number => (v >> 1) & 0x3;
const kindOf = (v: number): number => v >> 3;
const pack = (kind: number, width: number, join: boolean): number =>
  ((kind & 0xffffff) << 3) | ((width & 3) << 1) | (join ? 1 : 0);

interface Provider {
  readonly version: string;
  wcwidth(codepoint: number): number;
  charProperties(codepoint: number, preceding: number): number;
}

/** wrapWithVS16 returns a provider that behaves like `inner` except that
 *  a variation selector following a narrow base widens the cluster to 2.
 *
 *  `version` is the name the provider registers under; it must differ
 *  from the wrapped one so both can coexist in the registry.
 */
export function wrapWithVS16(inner: Provider, version: string): Provider {
  return {
    version,
    wcwidth: (cp: number) => inner.wcwidth(cp),
    charProperties(codepoint: number, preceding: number): number {
      const base = inner.charProperties(codepoint, preceding);
      if (codepoint !== VS16) return base;

      // xterm computes the cluster width as
      //     width(current) - width(preceding)   [when shouldJoin]
      // added to the width already counted for the preceding cell. To
      // land on 2 for a narrow base we report width 2 and keep the join,
      // which yields 2 - 1 = 1 more column on top of the base's 1.
      //
      // A base that is already wide (U+274C, U+2B50) needs no help: the
      // pair is 2 and reporting 2 here leaves it at 2.
      if (!shouldJoinOf(base)) return base;
      const prevWidth = widthOf(preceding);
      if (prevWidth !== 1) return base;
      return pack(kindOf(base), 2, true);
    },
  };
}

/** VS16Addon registers a width provider that measures emoji-with-VS16 as
 *  two cells.
 *
 *  It builds its own Unicode 11 provider rather than reading xterm's
 *  provider registry, which is private. Load it and then set
 *  `term.unicode.activeVersion` to `VS16Addon.VERSION`.
 */
export class VS16Addon implements ITerminalAddon {
  /** The version name this addon registers under. */
  public static readonly VERSION = "11-vs16";

  public activate(terminal: Terminal): void {
    // Unicode11Addon has no exported provider, but activating it against
    // a stub registry hands us the provider object it would have
    // registered - a supported path (that is its whole public contract)
    // and one that does not depend on xterm internals.
    let inner: Provider | undefined;
    new Unicode11Addon().activate({
      unicode: { register: (p: unknown) => { inner = p as Provider; } },
    } as never);
    if (!inner) {
      throw new Error("VS16Addon: Unicode11Addon registered no provider");
    }
    terminal.unicode.register(wrapWithVS16(inner, VS16Addon.VERSION) as never);
  }

  public dispose(): void {
    // The provider stays registered for the terminal's lifetime; there is
    // no unregister in the API and nothing to release.
  }
}
