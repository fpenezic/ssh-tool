// Color palette used by the connection/folder color-tag picker. Kept
// in one place so the resolver (tree.resolveColor*) can map raw
// names → palette hex before handing to CSS - otherwise typing "red"
// in the tag input yields raw CSS red (#ff0000), bypassing the
// Catppuccin-pastel scheme everyone expects.

export interface PaletteEntry {
  name: string;
  hex: string;
}

export const palette: PaletteEntry[] = [
  { name: "red",    hex: "var(--red)" },
  { name: "orange", hex: "var(--peach)" },
  { name: "yellow", hex: "var(--yellow)" },
  { name: "green",  hex: "var(--green)" },
  { name: "teal",   hex: "var(--teal)" },
  { name: "blue",   hex: "var(--blue)" },
  { name: "mauve",  hex: "var(--mauve)" },
  { name: "pink",   hex: "var(--pink)" },
];

const byName = new Map(palette.map((p) => [p.name.toLowerCase(), p.hex]));

/**
 * Resolve a color_tag value to a renderable CSS color.
 * - "" / undefined  -> ""
 * - "red", "blue", ...  -> palette hex
 * - "#ff0000", "rgb(...)" -> passed through unchanged
 * - any other string -> passed through (assume user knows it's CSS)
 */
export function resolveColorTag(raw: string | null | undefined): string {
  if (!raw) return "";
  const lower = raw.toLowerCase().trim();
  return byName.get(lower) ?? raw;
}
