/**
 * xb/render-theme.js — the reference renderer's theme: the native vocabulary's
 * tokens as --xb-* CSS custom properties, for light and dark, default and
 * large text. Tiles name roles (`tone="ok"`, `style="footnote"`,
 * `gap="m"`); every renderer maps them — this is the web mapping, the one
 * previews and fixture screenshots use (the app maps the same roles to iOS
 * system colours and Dynamic Type).
 *
 * Colour roles (dark = the web shell's theme.css; light is new):
 *   --xb-bg --xb-surface --xb-surface2 --xb-border --xb-text --xb-muted
 *   --xb-accent (fills) --xb-accent-text (text/icons in accent) --xb-on-accent
 *   --xb-ok --xb-warn --xb-danger
 * Derived: --xb-separator --xb-fill --xb-control-on (a selected segment)
 *   --xb-bubble (the user's chat turns)
 *   --xb-scrim --xb-shadow --xb-chart-1…6
 * Type roles (iOS default point sizes as px, body 17; `text="large"` = iOS
 * xxxLarge): --xb-font-<role> as a `font` shorthand, roles largeTitle title
 * title2 title3 headline body callout subheadline footnote caption caption2
 * mono (kebab-cased: --xb-font-large-title, --xb-font-title-2 …), with
 * --xb-size-<role> and --xb-line-<role> (px) beside each.
 * Gaps (stack gap only): --xb-gap-none|xs|s|m|l|xl|xxl = 0 4 8 12 16 24 32.
 * Heights (image/chart/canvas): --xb-h-xs|s|m|l|xl = 48 96 160 240 360.
 * Shape (the renderer's own): --xb-radius-group --xb-radius-control.
 *
 * The scheme follows prefers-color-scheme unless the <xb-view> element says
 * theme="light" | theme="dark"; text="large" switches the type scale.
 */
import { css, unsafeCSS } from '/vendor/lit-all.min.js';

const LIGHT = {
  bg: '#f6f7f9', surface: '#ffffff', surface2: '#eef0f3', border: '#d5d9df',
  text: '#1b1e24', muted: '#5f6873', accent: '#f5a623', 'accent-text': '#a86400',
  'on-accent': '#1b1e24', ok: '#2e7d32', warn: '#9a6700', danger: '#c62828',
  separator: 'rgba(60, 64, 72, 0.18)', fill: 'rgba(118, 118, 128, 0.12)', 'control-on': '#ffffff', bubble: '#fde8c2',
  scrim: 'rgba(0, 0, 0, 0.32)', shadow: '0 1px 3px rgba(0, 0, 0, 0.12), 0 6px 20px rgba(0, 0, 0, 0.08)',
  'chart-1': '#e08e0b', 'chart-2': '#2f6fd6', 'chart-3': '#2e7d32', 'chart-4': '#8e44ad',
  'chart-5': '#00838f', 'chart-6': '#c2185b',
};
const DARK = {
  bg: '#1b1e24', surface: '#23272e', surface2: '#2b3038', border: '#363c45',
  text: '#d4d9e0', muted: '#868f9a', accent: '#f5a623', 'accent-text': '#f5a623',
  'on-accent': '#1b1e24', ok: '#4caf50', warn: '#f2a71b', danger: '#ef5350',
  separator: 'rgba(160, 170, 185, 0.16)', fill: 'rgba(118, 118, 128, 0.24)', 'control-on': '#5a616c', bubble: '#343a44',
  scrim: 'rgba(0, 0, 0, 0.55)', shadow: '0 1px 3px rgba(0, 0, 0, 0.5), 0 8px 24px rgba(0, 0, 0, 0.35)',
  'chart-1': '#f5a623', 'chart-2': '#5b9cf6', 'chart-3': '#4caf50', 'chart-4': '#b27ee0',
  'chart-5': '#26c6da', 'chart-6': '#f06292',
};

// [weight, size, line-height] per type role — iOS "Large" (the default) and
// "xxxLarge" (the large-text screenshots).
const TYPE = {
  'large-title': [[400, 34, 41], [400, 40, 48]],
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
const types = (i) => Object.entries(TYPE).map(([k, t]) => {
  const [w, s, l] = t[i];
  return `--xb-font-${k}: ${w} ${s}px/${l}px var(${k === 'mono' ? '--xb-mono' : '--xb-family'});
--xb-size-${k}: ${s}px; --xb-line-${k}: ${l}px;`;
}).join('\n');

export const TOKENS = css`
  :host {
    --xb-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter", system-ui, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    --xb-mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, "DejaVu Sans Mono", Consolas, monospace;
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
  .t-largeTitle { font: var(--xb-font-large-title); letter-spacing: 0.2px; }
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
