// web/term-palettes.js — the terminal's palettes (D184): the xterm theme
// objects <bx-terminal>'s settings menu offers, and how "Workspace (follows
// the theme)" is built from the --bx-term-* tokens. Data and pure functions:
// no DOM, no imports, so hack/term-palettes.test.mjs runs it under node and
// checks Concrete Night and Concrete Day against web/theme.css.
//
// The stored choice (localStorage['bx-term-theme'], per browser) is one of
// TERM_THEMES' keys. 'default' — every terminal that never picked — is the
// workspace's palette: built at the element from the tokens (theme.css), so
// it follows the person's light/dark choice live; a bare document without
// theme.css gets Concrete Night, as every token's fallback does. A palette
// the person picked stays theirs: Concrete Night or Day (a dark terminal in a
// light shell is one click) or one of the well-known ones. Programs choose
// ANSI colours, so these are a tool palette, not brand colour (allowlisted
// for literals in hack/theme-allow.txt).

// TERM_TOKENS: each xterm theme key and the token it comes from.
export const TERM_TOKENS = Object.freeze({
  background: '--bx-term-bg', foreground: '--bx-term-fg', cursor: '--bx-term-cursor', cursorAccent: '--bx-term-cursor-ink',
  selectionBackground: '--bx-term-selection',
  black: '--bx-term-black', red: '--bx-term-red', green: '--bx-term-green', yellow: '--bx-term-yellow',
  blue: '--bx-term-blue', magenta: '--bx-term-magenta', cyan: '--bx-term-cyan', white: '--bx-term-white',
  brightBlack: '--bx-term-bright-black', brightRed: '--bx-term-bright-red', brightGreen: '--bx-term-bright-green', brightYellow: '--bx-term-bright-yellow',
  brightBlue: '--bx-term-bright-blue', brightMagenta: '--bx-term-bright-magenta', brightCyan: '--bx-term-bright-cyan', brightWhite: '--bx-term-bright-white',
});

// Concrete Night and Concrete Day: theme.css's --bx-term-* values (product-ui §7).
export const NIGHT = Object.freeze({
  background: '#0B0C12', foreground: '#E6E7EE', cursor: '#8C9BFF', cursorAccent: '#0B0C12', selectionBackground: '#262C5C',
  black: '#1C1D26', red: '#FF6B6B', green: '#4CD69B', yellow: '#FFD54A', blue: '#6F86FF', magenta: '#FF5FB0', cyan: '#4FC3DC', white: '#C9CBD6',
  brightBlack: '#5C5F70', brightRed: '#FF8F8F', brightGreen: '#7BE6B6', brightYellow: '#FFE27A', brightBlue: '#96A6FF', brightMagenta: '#FF8CC8', brightCyan: '#85DCEC', brightWhite: '#FFFFFF',
});
export const DAY = Object.freeze({
  background: '#FFFFFF', foreground: '#1C1D24', cursor: '#1F3DFF', cursorAccent: '#FFFFFF', selectionBackground: '#DDE2FF',
  black: '#1C1D24', red: '#C81E1E', green: '#00794A', yellow: '#8A6100', blue: '#1F3DFF', magenta: '#B0005C', cyan: '#0E7490', white: '#7A7D8C',
  brightBlack: '#5A5D6C', brightRed: '#E0352B', brightGreen: '#00965C', brightYellow: '#A87800', brightBlue: '#4A63FF', brightMagenta: '#D4007A', brightCyan: '#0891B2', brightWhite: '#A6A9B8',
});

// The menu's palettes, in its order. 'default' is null: workspaceTheme().
export const TERM_THEMES = Object.freeze({
  'default': null,
  'concrete-night': NIGHT,
  'concrete-day': DAY,
  'dracula': { background: '#282a36', foreground: '#f8f8f2', cursor: '#f8f8f2', selectionBackground: '#44475a', black: '#21222c', red: '#ff5555', green: '#50fa7b', yellow: '#f1fa8c', blue: '#bd93f9', magenta: '#ff79c6', cyan: '#8be9fd', white: '#f8f8f2', brightBlack: '#6272a4', brightRed: '#ff6e6e', brightGreen: '#69ff94', brightYellow: '#ffffa5', brightBlue: '#d6acff', brightMagenta: '#ff92df', brightCyan: '#a4ffff', brightWhite: '#ffffff' },
  'nord': { background: '#2e3440', foreground: '#d8dee9', cursor: '#d8dee9', selectionBackground: '#434c5e', black: '#3b4252', red: '#bf616a', green: '#a3be8c', yellow: '#ebcb8b', blue: '#81a1c1', magenta: '#b48ead', cyan: '#88c0d0', white: '#e5e9f0', brightBlack: '#4c566a', brightRed: '#bf616a', brightGreen: '#a3be8c', brightYellow: '#ebcb8b', brightBlue: '#81a1c1', brightMagenta: '#b48ead', brightCyan: '#8fbcbb', brightWhite: '#eceff4' },
  'solarized-dark': { background: '#002b36', foreground: '#839496', cursor: '#93a1a1', selectionBackground: '#073642', black: '#073642', red: '#dc322f', green: '#859900', yellow: '#b58900', blue: '#268bd2', magenta: '#d33682', cyan: '#2aa198', white: '#eee8d5', brightBlack: '#586e75', brightRed: '#cb4b16', brightGreen: '#657b83', brightYellow: '#839496', brightBlue: '#657b83', brightMagenta: '#6c71c4', brightCyan: '#93a1a1', brightWhite: '#fdf6e3' },
  'solarized-light': { background: '#fdf6e3', foreground: '#657b83', cursor: '#586e75', selectionBackground: '#eee8d5', black: '#073642', red: '#dc322f', green: '#859900', yellow: '#b58900', blue: '#268bd2', magenta: '#d33682', cyan: '#2aa198', white: '#eee8d5', brightBlack: '#586e75', brightRed: '#cb4b16', brightGreen: '#657b83', brightYellow: '#839496', brightBlue: '#657b83', brightMagenta: '#6c71c4', brightCyan: '#93a1a1', brightWhite: '#fdf6e3' },
  'monokai': { background: '#272822', foreground: '#f8f8f2', cursor: '#f8f8f0', selectionBackground: '#49483e', black: '#272822', red: '#f92672', green: '#a6e22e', yellow: '#f4bf75', blue: '#66d9ef', magenta: '#ae81ff', cyan: '#a1efe4', white: '#f8f8f2', brightBlack: '#75715e', brightRed: '#f92672', brightGreen: '#a6e22e', brightYellow: '#f4bf75', brightBlue: '#66d9ef', brightMagenta: '#ae81ff', brightCyan: '#a1efe4', brightWhite: '#f9f8f5' },
  'gruvbox-dark': { background: '#282828', foreground: '#ebdbb2', cursor: '#ebdbb2', selectionBackground: '#504945', black: '#282828', red: '#cc241d', green: '#98971a', yellow: '#d79921', blue: '#458588', magenta: '#b16286', cyan: '#689d6a', white: '#a89984', brightBlack: '#928374', brightRed: '#fb4934', brightGreen: '#b8bb26', brightYellow: '#fabd2f', brightBlue: '#83a598', brightMagenta: '#d3869b', brightCyan: '#8ec07c', brightWhite: '#ebdbb2' },
  'one-dark': { background: '#282c34', foreground: '#abb2bf', cursor: '#528bff', selectionBackground: '#3e4451', black: '#282c34', red: '#e06c75', green: '#98c379', yellow: '#e5c07b', blue: '#61afef', magenta: '#c678dd', cyan: '#56b6c2', white: '#abb2bf', brightBlack: '#5c6370', brightRed: '#e06c75', brightGreen: '#98c379', brightYellow: '#e5c07b', brightBlue: '#61afef', brightMagenta: '#c678dd', brightCyan: '#56b6c2', brightWhite: '#ffffff' },
  'tango-dark': { background: '#2e3436', foreground: '#d3d7cf', cursor: '#d3d7cf', selectionBackground: '#555753', black: '#2e3436', red: '#cc0000', green: '#4e9a06', yellow: '#c4a000', blue: '#3465a4', magenta: '#75507b', cyan: '#06989a', white: '#d3d7cf', brightBlack: '#555753', brightRed: '#ef2929', brightGreen: '#8ae234', brightYellow: '#fce94f', brightBlue: '#729fcf', brightMagenta: '#ad7fa8', brightCyan: '#34e2e2', brightWhite: '#eeeeec' },
  'github-light': { background: '#ffffff', foreground: '#24292e', cursor: '#24292e', selectionBackground: '#c8e1ff', black: '#24292e', red: '#d73a49', green: '#28a745', yellow: '#dbab09', blue: '#0366d6', magenta: '#5a32a3', cyan: '#0598bc', white: '#6a737d', brightBlack: '#959da5', brightRed: '#cb2431', brightGreen: '#22863a', brightYellow: '#b08800', brightBlue: '#005cc5', brightMagenta: '#5a32a3', brightCyan: '#3192aa', brightWhite: '#d1d5da' },
});

export const THEME_LABELS = Object.freeze({
  'default': 'Workspace (follows the theme)', 'concrete-night': 'Concrete Night', 'concrete-day': 'Concrete Day',
  'dracula': 'Dracula', 'nord': 'Nord', 'solarized-dark': 'Solarized Dark', 'solarized-light': 'Solarized Light',
  'monokai': 'Monokai', 'gruvbox-dark': 'Gruvbox Dark', 'one-dark': 'One Dark', 'tango-dark': 'Tango Dark', 'github-light': 'GitHub Light',
});

// themeName(stored) → a palette's key: the stored one when it is a palette,
// else 'default' (nothing picked, or a name this version doesn't know).
export const themeName = (v) => (typeof v === 'string' && Object.hasOwn(TERM_THEMES, v) ? v : 'default');

// workspaceTheme(read) → the xterm theme of "Workspace (follows the theme)":
// each key from its token, read(name) → the token's value at the terminal
// element ('' when unset: a document without theme.css gets the Night value).
export function workspaceTheme(read) {
  const out = {};
  for (const [key, name] of Object.entries(TERM_TOKENS)) out[key] = String(read(name) || '').trim() || NIGHT[key];
  return out;
}

// paletteFor(name, read) → the xterm theme a stored choice names.
export const paletteFor = (name, read) => TERM_THEMES[themeName(name)] || workspaceTheme(read);
