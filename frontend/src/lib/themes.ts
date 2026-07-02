// Terminal color schemes. Each theme matches xterm's ITheme shape so we
// can hand it straight to term.options.theme = ... or new Terminal({theme}).
//
// Curated set - popular dark + light palettes that read well at 12-14px.
// To add one: copy an existing entry and replace the colors. Ordering
// here is the order shown in the Settings dropdown.

export interface TerminalTheme {
  id: string;
  name: string;
  isLight: boolean;
  background: string;
  foreground: string;
  cursor: string;
  selectionBackground: string;
  black: string;
  red: string;
  green: string;
  yellow: string;
  blue: string;
  magenta: string;
  cyan: string;
  white: string;
  brightBlack: string;
  brightRed: string;
  brightGreen: string;
  brightYellow: string;
  brightBlue: string;
  brightMagenta: string;
  brightCyan: string;
  brightWhite: string;
}

export const DEFAULT_THEME_ID = "catppuccin-mocha";

export const themes: TerminalTheme[] = [
  {
    id: "catppuccin-mocha",
    name: "Catppuccin Mocha",
    isLight: false,
    background: "#11111b",
    foreground: "#cdd6f4",
    cursor: "#cdd6f4",
    selectionBackground: "#45475a",
    black: "#45475a", red: "#f38ba8", green: "#a6e3a1", yellow: "#f9e2af",
    blue: "#89b4fa", magenta: "#cba6f7", cyan: "#94e2d5", white: "#bac2de",
    brightBlack: "#585b70", brightRed: "#f38ba8", brightGreen: "#a6e3a1",
    brightYellow: "#f9e2af", brightBlue: "#89b4fa", brightMagenta: "#cba6f7",
    brightCyan: "#94e2d5", brightWhite: "#a6adc8",
  },
  {
    id: "catppuccin-latte",
    name: "Catppuccin Latte",
    isLight: true,
    background: "#eff1f5",
    foreground: "#4c4f69",
    cursor: "#4c4f69",
    selectionBackground: "#bcc0cc",
    black: "#5c5f77", red: "#d20f39", green: "#40a02b", yellow: "#df8e1d",
    blue: "#1e66f5", magenta: "#8839ef", cyan: "#179299", white: "#acb0be",
    brightBlack: "#6c6f85", brightRed: "#d20f39", brightGreen: "#40a02b",
    brightYellow: "#df8e1d", brightBlue: "#1e66f5", brightMagenta: "#8839ef",
    brightCyan: "#179299", brightWhite: "#bcc0cc",
  },
  {
    id: "gruvbox-dark",
    name: "Gruvbox Dark",
    isLight: false,
    background: "#282828",
    foreground: "#ebdbb2",
    cursor: "#ebdbb2",
    selectionBackground: "#504945",
    black: "#282828", red: "#cc241d", green: "#98971a", yellow: "#d79921",
    blue: "#458588", magenta: "#b16286", cyan: "#689d6a", white: "#a89984",
    brightBlack: "#928374", brightRed: "#fb4934", brightGreen: "#b8bb26",
    brightYellow: "#fabd2f", brightBlue: "#83a598", brightMagenta: "#d3869b",
    brightCyan: "#8ec07c", brightWhite: "#ebdbb2",
  },
  {
    id: "gruvbox-light",
    name: "Gruvbox Light",
    isLight: true,
    background: "#fbf1c7",
    foreground: "#3c3836",
    cursor: "#3c3836",
    selectionBackground: "#d5c4a1",
    black: "#fbf1c7", red: "#cc241d", green: "#98971a", yellow: "#d79921",
    blue: "#458588", magenta: "#b16286", cyan: "#689d6a", white: "#7c6f64",
    brightBlack: "#928374", brightRed: "#9d0006", brightGreen: "#79740e",
    brightYellow: "#b57614", brightBlue: "#076678", brightMagenta: "#8f3f71",
    brightCyan: "#427b58", brightWhite: "#3c3836",
  },
  {
    id: "solarized-dark",
    name: "Solarized Dark",
    isLight: false,
    background: "#002b36",
    foreground: "#839496",
    cursor: "#93a1a1",
    selectionBackground: "#073642",
    black: "#073642", red: "#dc322f", green: "#859900", yellow: "#b58900",
    blue: "#268bd2", magenta: "#d33682", cyan: "#2aa198", white: "#eee8d5",
    brightBlack: "#586e75", brightRed: "#cb4b16", brightGreen: "#586e75",
    brightYellow: "#657b83", brightBlue: "#839496", brightMagenta: "#6c71c4",
    brightCyan: "#93a1a1", brightWhite: "#fdf6e3",
  },
  {
    id: "solarized-light",
    name: "Solarized Light",
    isLight: true,
    background: "#fdf6e3",
    foreground: "#657b83",
    cursor: "#586e75",
    selectionBackground: "#eee8d5",
    black: "#073642", red: "#dc322f", green: "#859900", yellow: "#b58900",
    blue: "#268bd2", magenta: "#d33682", cyan: "#2aa198", white: "#eee8d5",
    brightBlack: "#002b36", brightRed: "#cb4b16", brightGreen: "#586e75",
    brightYellow: "#657b83", brightBlue: "#839496", brightMagenta: "#6c71c4",
    brightCyan: "#93a1a1", brightWhite: "#fdf6e3",
  },
  {
    id: "one-dark",
    name: "One Dark",
    isLight: false,
    background: "#282c34",
    foreground: "#abb2bf",
    cursor: "#abb2bf",
    selectionBackground: "#3e4451",
    black: "#282c34", red: "#e06c75", green: "#98c379", yellow: "#e5c07b",
    blue: "#61afef", magenta: "#c678dd", cyan: "#56b6c2", white: "#abb2bf",
    brightBlack: "#5c6370", brightRed: "#e06c75", brightGreen: "#98c379",
    brightYellow: "#e5c07b", brightBlue: "#61afef", brightMagenta: "#c678dd",
    brightCyan: "#56b6c2", brightWhite: "#ffffff",
  },
  {
    id: "one-dark-pro",
    name: "One Dark Pro",
    isLight: false,
    background: "#282c34",
    foreground: "#abb2bf",
    cursor: "#abb2bf",
    selectionBackground: "#3e4451",
    black: "#3f4451", red: "#e05561", green: "#8cc265", yellow: "#d18f52",
    blue: "#4aa5f0", magenta: "#c162de", cyan: "#42b3c2", white: "#e6e6e6",
    brightBlack: "#4f5666", brightRed: "#ff616e", brightGreen: "#a5e075",
    brightYellow: "#f0a45d", brightBlue: "#4dc4ff", brightMagenta: "#de73ff",
    brightCyan: "#4cd1e0", brightWhite: "#d7dae0",
  },
  {
    id: "dracula",
    name: "Dracula",
    isLight: false,
    background: "#282a36",
    foreground: "#f8f8f2",
    cursor: "#f8f8f2",
    selectionBackground: "#44475a",
    black: "#21222c", red: "#ff5555", green: "#50fa7b", yellow: "#f1fa8c",
    blue: "#bd93f9", magenta: "#ff79c6", cyan: "#8be9fd", white: "#f8f8f2",
    brightBlack: "#6272a4", brightRed: "#ff6e6e", brightGreen: "#69ff94",
    brightYellow: "#ffffa5", brightBlue: "#d6acff", brightMagenta: "#ff92df",
    brightCyan: "#a4ffff", brightWhite: "#ffffff",
  },
  {
    id: "nord",
    name: "Nord",
    isLight: false,
    background: "#2e3440",
    foreground: "#d8dee9",
    cursor: "#d8dee9",
    selectionBackground: "#434c5e",
    black: "#3b4252", red: "#bf616a", green: "#a3be8c", yellow: "#ebcb8b",
    blue: "#81a1c1", magenta: "#b48ead", cyan: "#88c0d0", white: "#e5e9f0",
    brightBlack: "#4c566a", brightRed: "#bf616a", brightGreen: "#a3be8c",
    brightYellow: "#ebcb8b", brightBlue: "#81a1c1", brightMagenta: "#b48ead",
    brightCyan: "#8fbcbb", brightWhite: "#eceff4",
  },
  {
    id: "tomorrow-night",
    name: "Tomorrow Night",
    isLight: false,
    background: "#1d1f21",
    foreground: "#c5c8c6",
    cursor: "#c5c8c6",
    selectionBackground: "#373b41",
    black: "#1d1f21", red: "#cc6666", green: "#b5bd68", yellow: "#f0c674",
    blue: "#81a2be", magenta: "#b294bb", cyan: "#8abeb7", white: "#c5c8c6",
    brightBlack: "#969896", brightRed: "#cc6666", brightGreen: "#b5bd68",
    brightYellow: "#f0c674", brightBlue: "#81a2be", brightMagenta: "#b294bb",
    brightCyan: "#8abeb7", brightWhite: "#ffffff",
  },
];

export function findTheme(id: string): TerminalTheme {
  return themes.find((t) => t.id === id) ?? themes[0];
}
