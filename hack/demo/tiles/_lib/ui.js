// ui.js — small shared bits of the demo tiles' pages (copied next to each
// tile's index.html by hack/demo/seed.sh): number and time formatting,
// people's initials and colours, and the base styles the pages share. The
// colours come from the workspace theme tokens (/vendor/theme.css).
import { css } from 'lit';

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
const HUES = [14, 32, 152, 168, 190, 210, 232, 262, 290, 330, 350, 46];
export function hue(id) {
  let h = 0;
  for (const c of String(id)) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return HUES[h % HUES.length];
}

export const baseCss = css`
  :host { display: block; height: 100%; font: var(--bx-font); color: var(--bx-text); background: var(--bx-panel);
          --ok: var(--bx-green); --warn: var(--bx-amber); --bad: var(--bx-red);
          --blue: #6aa7e8; --teal: #3fbfae; --violet: #a68cf0; }
  * { box-sizing: border-box; }
  button { font: inherit; color: inherit; cursor: pointer; }
  .muted { color: var(--bx-muted); }
  .mono { font-family: var(--bx-mono); }
  .num { font-variant-numeric: tabular-nums; }
  .av { display: inline-grid; place-items: center; width: 22px; height: 22px; border-radius: 50%; flex: none;
        font-size: 9.5px; font-weight: 700; letter-spacing: .02em; color: #fff;
        background: hsl(var(--h, 210) 45% 42%); box-shadow: 0 0 0 1.5px var(--bx-panel); }
  .av.lg { width: 30px; height: 30px; font-size: 11.5px; }
  .chip { display: inline-flex; align-items: center; gap: 4px; padding: 1px 7px; border-radius: 999px; font-size: 10.5px;
          border: 1px solid var(--bx-border); color: var(--bx-muted); white-space: nowrap; }
  .dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; flex: none; }
  .ok { --c: var(--ok); } .warn { --c: var(--warn); } .bad { --c: var(--bad); }
  .chip.ok, .chip.warn, .chip.bad { color: var(--c); border-color: color-mix(in srgb, var(--c) 40%, transparent);
          background: color-mix(in srgb, var(--c) 10%, transparent); }
  .dot.ok, .dot.warn, .dot.bad { background: var(--c); }
  .label { font-size: 10px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--bx-muted); }
  .btn { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 4px 11px; font-size: 12px; }
  .btn:hover { border-color: color-mix(in srgb, var(--bx-accent) 50%, var(--bx-border)); }
  .btn.primary { background: var(--bx-accent); border-color: var(--bx-accent); color: #1b1e24; font-weight: 600; }
  .empty { color: var(--bx-muted); padding: 18px; text-align: center; }
  .err { color: var(--bad); padding: 10px 14px; }
`;
