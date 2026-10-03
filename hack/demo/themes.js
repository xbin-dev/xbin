// hack/demo/themes.js — the film set's two looks (D184): Concrete Night
// ("dark": the stills and the footage as they always were) and Concrete Day
// ("light"). Which themes a run shoots, and what a light take's files are
// called. The stills (stills.js) and the camera (cam/shot.js,
// cam/site-stills.sh) share it; a capture's theme is its browser's
// prefers-color-scheme, which every document that opts in
// (<html data-bx-theme="auto">) follows for a person who chose none.
'use strict';

const THEMES = Object.freeze({ dark: ['dark'], light: ['light'], both: ['dark', 'light'] });

// themes(v): the themes v names (DEMO_THEME: dark, light or both; unset is
// dark), in shooting order
function themes(v) {
  const t = THEMES[v || 'dark'];
  if (!t) throw new Error(`DEMO_THEME=${v}: dark, light or both`);
  return t;
}

// suffix(theme): what a take in that theme adds to its file names — nothing
// for dark, so the dark stills keep the names they always had
const suffix = (theme) => (!theme || theme === 'dark' ? '' : `-${theme}`);

module.exports = { THEMES, themes, suffix };
