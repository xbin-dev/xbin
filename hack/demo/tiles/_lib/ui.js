// ui.js — small shared bits of the demo tiles' pages (copied next to each
// tile's index.html by hack/demo/seed.sh): number and time formatting,
// people's initials, and the base styles the pages share. Every colour,
// font, corner and shadow comes from the workspace theme's tokens
// (/vendor/theme.css: the pages opt in with <html data-bx-theme="auto">, so
// they follow the person's light or dark theme); status is an icon, a word
// and a colour (/vendor/bx-icons.js draws the icons).
import { css, html } from 'lit';
import '/vendor/bx-icons.js';

export const api = (p, opt) => globalThis.xbin.fetch(`/api/${globalThis.xbin.self}${p}`, opt);
export async function getJSON(p, opt) {
  const r = await api(p, opt);
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || `HTTP ${r.status}`);
  return body;
}
export const sendJSON = (p, method, body) => getJSON(p, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

const usd0 = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 });
export function money(n, compact = false) {
  if (n == null) return '—';
  if (!compact || Math.abs(n) < 10000) return usd0.format(n);
  if (Math.abs(n) >= 1e6) return '$' + (n / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M';
  return '$' + (n / 1e3).toFixed(Math.abs(n) >= 1e5 ? 0 : 1).replace(/\.0$/, '') + 'k';
}
export const num = (n) => (n == null ? '—' : new Intl.NumberFormat('en-US').format(n));
const usd2 = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' });
export const cents = (n) => (n == null ? '—' : usd2.format(n));

const DAY = 864e5;
const startOfDay = (t) => { const d = new Date(t); d.setHours(0, 0, 0, 0); return d.getTime(); };
export function ago(t, now = Date.now()) {
  if (!t) return '';
  const s = (now - t) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  const days = Math.round((startOfDay(now) - startOfDay(t)) / DAY);
  if (days === 0) return `${Math.round(s / 3600)} h ago`;
  if (days === 1) return 'yesterday';
  if (days < 7) return new Date(t).toLocaleDateString('en-US', { weekday: 'long' });
  return dateShort(t);
}
export const dateShort = (t) => (t ? new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' }) : '');
export const timeShort = (t) => (t ? new Date(t).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' }) : '');
export function inDays(t, now = Date.now()) {
  const d = Math.round((startOfDay(t) - startOfDay(now)) / DAY);
  if (d === 0) return 'today';
  if (d === 1) return 'tomorrow';
  if (d === -1) return 'yesterday';
  return d > 0 ? `in ${d} days` : `${-d} days ago`;
}

export const initials = (name) => String(name || '?').split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join('');
// avatar: a person as their initials on a square of concrete (size '', 'sm'
// or 'lg'), their name in its tooltip
export const avatar = (name, size = '') => html`<span class="av ${size}" title=${name}>${initials(name)}</span>`;

// a status's icon (bx-icons) by its class: .ok, .warn, .bad
export const STATUS_ICON = { ok: 'ok', warn: 'warning', bad: 'error', info: 'info' };
// badge(status, word): a status badge — its icon, its word, its colour
export const badge = (status, word) => html`<span class="badge ${status}"><bx-icon name=${STATUS_ICON[status]}></bx-icon>${word}</span>`;

export const baseCss = css`
  :host { display: block; height: 100%; font: var(--bx-font); color: var(--bx-text); background: var(--bx-panel); }
  * { box-sizing: border-box; }
  ::selection { background: var(--bx-selection); color: var(--bx-selection-text); }
  button, input, select, textarea { font: inherit; color: inherit; }
  button { cursor: pointer; }
  ::placeholder { color: var(--bx-subtle); opacity: 1; }
  :focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
  .muted { color: var(--bx-muted); }
  .mono { font-family: var(--bx-mono); }
  .num { font-variant-numeric: tabular-nums; }
  .meta { font: var(--bx-font-meta); font-variant-numeric: tabular-nums; }
  .label { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
  bx-icon { flex: none; }
  /* status: the colour and its tint, for whatever carries the class */
  .ok { --st: var(--bx-ok); --st-bg: var(--bx-ok-bg); }
  .warn { --st: var(--bx-warn); --st-bg: var(--bx-warn-bg); }
  .bad { --st: var(--bx-danger); --st-bg: var(--bx-danger-bg); }
  .info { --st: var(--bx-info); --st-bg: var(--bx-info-bg); }
  /* a person: initials on concrete, square */
  .av { display: inline-grid; place-items: center; flex: none; width: 24px; height: 24px; border-radius: var(--bx-radius);
        background: var(--bx-panel-2); border: 1px solid var(--bx-border); color: var(--bx-text);
        font: var(--bx-font-micro); letter-spacing: 0.02em; }
  .av.sm { width: 20px; height: 20px; }
  .av.lg { width: 32px; height: 32px; font: var(--bx-font-ui); font-weight: 600; }
  /* a badge: square, 20 px, micro caps, a hairline; a status badge adds its
     icon and colour */
  .badge { display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px; border: 1px solid var(--bx-border);
           border-radius: var(--bx-radius); font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro);
           text-transform: uppercase; color: var(--bx-muted); white-space: nowrap; }
  .badge bx-icon { --bx-icon-size: 14px; margin-left: -2px; }
  .badge.ok, .badge.warn, .badge.bad, .badge.info { color: var(--st); background: var(--st-bg); border-color: color-mix(in srgb, var(--st) 45%, transparent); }
  /* buttons: secondary by default, .primary the accent, .quiet text only */
  .btn { display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-height: var(--bx-control-h); padding: 4px 11px;
         background: var(--bx-panel); border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); color: var(--bx-text); font-weight: 600; }
  .btn:hover { background: var(--bx-hover); }
  .btn.primary { background: var(--bx-accent); border-color: var(--bx-accent); color: var(--bx-accent-ink); }
  .btn.primary:hover { background: var(--bx-accent-hover); border-color: var(--bx-accent-hover); }
  .btn.quiet { background: transparent; border-color: transparent; color: var(--bx-muted); }
  .btn.quiet:hover { color: var(--bx-accent); }
  .btn:disabled { opacity: 0.5; cursor: default; }
  .btn.icon { width: var(--bx-control-h); padding: 0; }
  input, select, textarea { min-height: var(--bx-control-h); padding: 4px 8px; background: var(--bx-panel); border: 1px solid var(--bx-border-strong);
                            border-radius: var(--bx-radius); color: var(--bx-text); }
  .empty { color: var(--bx-muted); padding: 18px; text-align: center; }
  .err { display: flex; align-items: center; gap: 6px; color: var(--bx-danger); padding: 10px 14px; }
`;
