/**
 * xb/render-theme.js — the reference renderer's theme: the native vocabulary's
 * tokens as --xb-* CSS custom properties, for light and dark, default and
 * large text. Tiles name roles (`tone="ok"`, `style="footnote"`,
 * `gap="m"`); every renderer maps them — this is the web mapping, the one
 * previews and fixture screenshots use (the app maps the same roles to iOS
 * system colours and Dynamic Type).
 *
 * Colour roles (D184): Base Two's product tokens, Concrete Day (light) and
 * Concrete Night (dark) — web/theme.css's values; the renderer keeps its own
 * table, as the app draws a tile's view the same whatever page is around it:
 *   --xb-bg --xb-surface --xb-surface2 --xb-border --xb-text --xb-muted
 *   --xb-accent (fills: cobalt, periwinkle in dark) --xb-accent-text (text
 *   and icons in the accent) --xb-on-accent (the accent ink)
 *   --xb-ok --xb-warn --xb-danger
 * Derived: --xb-separator (the border) --xb-fill --xb-control-on (a
 *   selected segment) --xb-bubble (the user's chat turns: panel-2)
 *   --xb-scrim --xb-shadow --xb-chart-1…6 (the terminal's ANSI order: blue,
 *   magenta, cyan, green, yellow, red)
 *   --xb-term-bg --xb-term-fg --xb-term-cursor (the terminal primitive)
 *   --xb-on-danger (a badge's text) --xb-knob --xb-knob-shadow (a switch's
 *   thumb) --xb-control-shadow (a selected segment's lift) --xb-lightbox
 *   (behind a full-screen image)
 * Type roles (iOS default point sizes as px, body 17; `text="large"` = iOS
 * xxxLarge): --xb-font-<role> as a `font` shorthand, roles largeTitle title
 * title2 title3 headline body callout subheadline footnote caption caption2
 * mono (kebab-cased: --xb-font-large-title, --xb-font-title-2 …), with
 * --xb-size-<role> and --xb-line-<role> (px) beside each. Body and controls
 * stay the system font (Dynamic Type, product-ui §10); large titles are
 * Bricolage Grotesque 800 (--xb-display), mono JetBrains Mono — the faces
 * theme.css serves (a page that doesn't load it falls back down the stacks).
 * Gaps (stack gap only): --xb-gap-none|xs|s|m|l|xl|xxl = 0 4 8 12 16 24 32.
 * Heights (image/chart/canvas): --xb-h-xs|s|m|l|xl = 48 96 160 240 360.
 * Shape (the app's own, iOS): --xb-radius-group --xb-radius-control — the
 * app keeps its corners until it adopts Base Two's (plan §6).
 *
 * The scheme follows prefers-color-scheme unless the <xb-view> element says
 * theme="light" | theme="dark"; text="large" switches the type scale.
 */
import { css, unsafeCSS } from '/vendor/lit-all.min.js';

const LIGHT = {
  bg: '#E8E9EE', surface: '#FFFFFF', surface2: '#F7F8FA', border: '#CDD0D8',
  text: '#0B0C12', muted: '#4B4D5C', accent: '#1F3DFF', 'accent-text': '#1F3DFF',
  'on-accent': '#FFFFFF', ok: '#436C0C', warn: '#9A4A06', danger: '#C81E1E',
  separator: '#CDD0D8', fill: '#EEF0F4', 'control-on': '#FFFFFF', bubble: '#F7F8FA',
  scrim: 'rgba(11, 12, 18, 0.32)', shadow: '0 1px 0 rgba(11, 12, 18, 0.06), 0 12px 32px rgba(11, 12, 18, 0.18)',
  'chart-1': '#1F3DFF', 'chart-2': '#B0005C', 'chart-3': '#0E7490', 'chart-4': '#00794A',
  'chart-5': '#8A6100', 'chart-6': '#C81E1E',
  'term-bg': '#FFFFFF', 'term-fg': '#1C1D24', 'term-cursor': '#1F3DFF',
  'on-danger': '#FFFFFF', knob: '#FFFFFF', 'knob-shadow': '0 2px 6px rgba(11, 12, 18, 0.2), 0 0 0 0.5px rgba(11, 12, 18, 0.06)',
  'control-shadow': '0 1px 3px rgba(11, 12, 18, 0.14), 0 0 0 0.5px rgba(11, 12, 18, 0.04)', lightbox: 'rgba(11, 12, 18, 0.92)',
};
const DARK = {
  bg: '#0B0C12', surface: '#1F2028', surface2: '#262730', border: '#33353F',
  text: '#E9EAF0', muted: '#A3A6B6', accent: '#8C9BFF', 'accent-text': '#8C9BFF',
  'on-accent': '#0B0C12', ok: '#A3CF5E', warn: '#F2994A', danger: '#FF7A7A',
  separator: '#33353F', fill: '#2A2B34', 'control-on': '#5C5F70', bubble: '#262730',
  scrim: 'rgba(0, 0, 0, 0.55)', shadow: '0 12px 32px rgba(0, 0, 0, 0.6)',
  'chart-1': '#6F86FF', 'chart-2': '#FF5FB0', 'chart-3': '#4FC3DC', 'chart-4': '#4CD69B',
  'chart-5': '#FFD54A', 'chart-6': '#FF6B6B',
  'term-bg': '#0B0C12', 'term-fg': '#E6E7EE', 'term-cursor': '#8C9BFF',
  'on-danger': '#0B0C12', knob: '#FFFFFF', 'knob-shadow': '0 2px 6px rgba(0, 0, 0, 0.4), 0 0 0 0.5px rgba(0, 0, 0, 0.12)',
  'control-shadow': '0 1px 3px rgba(0, 0, 0, 0.4), 0 0 0 0.5px rgba(0, 0, 0, 0.12)', lightbox: 'rgba(11, 12, 18, 0.92)',
};

// [weight, size, line-height] per type role — iOS "Large" (the default) and
// "xxxLarge" (the large-text screenshots). The large title is the one brand
// flourish: Bricolage Grotesque 800.
const TYPE = {
  'large-title': [[800, 34, 41], [800, 40, 48]],
  title: [[400, 28, 34], [400, 34, 41]],
  'title-2': [[400, 22, 28], [400, 28, 34]],
  'title-3': [[400, 20, 25], [400, 26, 32]],
  headline: [[600, 17, 22], [600, 23, 29]],
  body: [[400, 17, 22], [400, 23, 29]],
  callout: [[400, 16, 21], [400, 22, 28]],
  subheadline: [[400, 15, 20], [400, 21, 28]],
  footnote: [[400, 13, 18], [400, 19, 24]],
  caption: [[400, 12, 16], [400, 18, 23]],
  'caption-2': [[400, 11, 13], [400, 17, 22]],
  mono: [[400, 16, 22], [400, 22, 29]],
};

const colors = (o) => Object.entries(o).map(([k, v]) => `--xb-${k}: ${v};`).join('\n');
const FAMILY = { mono: '--xb-mono', 'large-title': '--xb-display' };
const types = (i) => Object.entries(TYPE).map(([k, t]) => {
  const [w, s, l] = t[i];
  return `--xb-font-${k}: ${w} ${s}px/${l}px var(${FAMILY[k] || '--xb-family'});
--xb-size-${k}: ${s}px; --xb-line-${k}: ${l}px;`;
}).join('\n');

export const TOKENS = css`
  :host {
    --xb-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter", system-ui, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    --xb-display: "Bricolage Grotesque", "Arial Black", "Helvetica Neue", Arial, system-ui, sans-serif;
    --xb-mono: "JetBrains Mono", ui-monospace, "SF Mono", SFMono-Regular, Menlo, "DejaVu Sans Mono", Consolas, monospace;
    --xb-gap-none: 0px; --xb-gap-xs: 4px; --xb-gap-s: 8px; --xb-gap-m: 12px;
    --xb-gap-l: 16px; --xb-gap-xl: 24px; --xb-gap-xxl: 32px;
    --xb-h-xs: 48px; --xb-h-s: 96px; --xb-h-m: 160px; --xb-h-l: 240px; --xb-h-xl: 360px;
    --xb-radius-group: 12px; --xb-radius-control: 10px;
    --xb-margin: 16px;
    --xb-icon: 20px;
    ${unsafeCSS(colors(LIGHT))}
    ${unsafeCSS(types(0))}
    color-scheme: light;
  }
  @media (prefers-color-scheme: dark) {
    :host(:not([theme="light"])) {
      ${unsafeCSS(colors(DARK))}
      color-scheme: dark;
    }
  }
  :host([theme="dark"]) {
    ${unsafeCSS(colors(DARK))}
    color-scheme: dark;
  }
  :host([text="large"]) {
    ${unsafeCSS(types(1))}
    --xb-icon: 26px;
  }
`;

// Type-role classes (`text style=…`, and the renderer's own labels).
export const TYPE_CLASSES = css`
  .t-largeTitle { font: var(--xb-font-large-title); letter-spacing: -0.02em; }
  .t-title { font: var(--xb-font-title); letter-spacing: 0.2px; }
  .t-title2 { font: var(--xb-font-title-2); letter-spacing: 0.1px; }
  .t-title3 { font: var(--xb-font-title-3); letter-spacing: 0.1px; }
  .t-headline { font: var(--xb-font-headline); letter-spacing: -0.2px; }
  .t-body { font: var(--xb-font-body); letter-spacing: -0.2px; }
  .t-callout { font: var(--xb-font-callout); letter-spacing: -0.15px; }
  .t-subheadline { font: var(--xb-font-subheadline); letter-spacing: -0.1px; }
  .t-footnote { font: var(--xb-font-footnote); }
  .t-caption { font: var(--xb-font-caption); }
  .t-caption2 { font: var(--xb-font-caption-2); letter-spacing: 0.05px; }
  .t-mono { font: var(--xb-font-mono); }
  .tone-muted { color: var(--xb-muted); }
  .tone-accent { color: var(--xb-accent-text); }
  .tone-ok { color: var(--xb-ok); }
  .tone-warn { color: var(--xb-warn); }
  .tone-danger { color: var(--xb-danger); }
  .mono { font-family: var(--xb-mono); font-size: 0.94em; }
`;

// The scheme values, for hosts that paint outside <xb-view> (the page
// background behind it) and for tests.
export const SCHEMES = { light: LIGHT, dark: DARK };
export const TYPE_SCALE = TYPE;
