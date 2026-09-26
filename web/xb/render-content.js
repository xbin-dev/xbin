/**
 * xb/render-content.js — content primitives of the reference renderer (text,
 * markdown, image, icon, badge, notice, progress, chart, code, empty) and the
 * escape hatches (terminal, canvas). In an inset group each is one cell.
 *
 * `text` is verbatim (a text binding — never markup); markup only ever comes
 * as markdown tokens. Images load through the view (the preview host fetches
 * tile-relative sources with the tile's own credentials); `data:` rasters
 * draw directly. A terminal is a placeholder here — previews never connect.
 */
import { css } from '/vendor/lit-all.min.js';
import { html, nothing, P, cls, tone, icon, spinner, str } from '/vendor/xb/render-base.js';
import { mdBlocks, tokensOf } from '/vendor/xb/render-markdown.js';

const TYPES = new Set(['largeTitle', 'title', 'title2', 'title3', 'headline', 'body', 'callout',
  'subheadline', 'footnote', 'caption', 'caption2', 'mono']);
const HEIGHTS = new Set(['xs', 's', 'm', 'l', 'xl']);
const cell = (cx) => cx.place === 'group' && 'cell';

function text(n, cx) {
  const p = P(n);
  const lines = Number(p.lines) > 0 ? Math.floor(Number(p.lines)) : 0;
  return html`<xb-text data-k=${n.k} style=${lines ? `-webkit-line-clamp:${lines}` : ''}
    class=${cls('text', `t-${TYPES.has(p.style) ? p.style : 'body'}`, tone(p.tone), p.mono && 'mono',
      p.selectable && 'sel', lines && 'clamp', cell(cx))}>${str(p.text)}</xb-text>`;
}

const link = (n, cx) => (href) => { if (cx.on(n, 'link')) cx.emit(n, 'link', { href }); };

function markdown(n, cx) {
  const p = P(n);
  return html`<xb-markdown data-k=${n.k} class=${cls('md', p.streaming && 'streaming', cell(cx))}>${mdBlocks(tokensOf(p, 'source'), link(n, cx))}</xb-markdown>`;
}

// An image that fails to load shows the placeholder (as the native renderer
// does), not the browser's broken-image glyph with its alt text spilling out.
const failed = (e) => e.currentTarget.setAttribute('data-failed', '');
const loaded = (e) => e.currentTarget.removeAttribute('data-failed');

function image(n, cx) {
  const p = P(n);
  const url = cx.v.image(p.src);
  const tappable = (p.preview && url) || cx.on(n, 'tap');
  const tap = () => {
    if (p.preview && url) cx.v.showImage(url, str(p.alt));
    if (cx.on(n, 'tap')) cx.emit(n, 'tap', {});
  };
  const h = HEIGHTS.has(p.height) ? `height:var(--xb-h-${p.height})` : '';
  return html`<xb-image data-k=${n.k} class=${cls('image', p.height && 'fixed', tappable && 'tap', cell(cx))} style=${h}>
    ${url ? html`<img src=${url} alt=${str(p.alt)} style=${`object-fit:${p.aspect === 'fill' ? 'cover' : 'contain'}`} @click=${tap}
        @error=${failed} @load=${loaded}><div class="img-ph" aria-hidden="true">${icon('photo')}</div>`
      : html`<div class="img-ph" role="img" aria-label=${str(p.alt) || 'image'}>${icon('photo')}</div>`}
  </xb-image>`;
}

const iconPrim = (n, cx) => html`<xb-icon data-k=${n.k} class=${cls('icon', tone(P(n).tone), cell(cx))}>${icon(P(n).name)}</xb-icon>`;

function badge(n, cx) {
  const p = P(n);
  return html`<xb-badge data-k=${n.k} class=${cls('badge', cell(cx))}><span class=${cls('pill', p.tone && `pill-${p.tone}`, p.pulse && 'pulse')}>${
    p.pulse ? html`<span class="pulse-dot"></span>` : nothing}${str(p.text)}</span></xb-badge>`;
}

const NOTICE_ICON = { info: 'info', muted: 'info', accent: 'sparkles', ok: 'ui-circle-check', warn: 'warning', danger: 'ui-alert' };
function notice(n, cx) {
  const p = P(n);
  const t = NOTICE_ICON[p.tone] ? p.tone : 'info';
  return html`<xb-notice data-k=${n.k} class=${cls('notice', `n-${t}`, cx.place === 'group' ? 'cell' : 'box')} role=${t === 'danger' || t === 'warn' ? 'alert' : 'status'}>
    <span class="n-ic">${icon(NOTICE_ICON[t])}</span>
    <div class="n-text">${p.title ? html`<div class="n-title">${p.title}</div>` : nothing}${p.text ? html`<div class="n-body">${p.text}</div>` : nothing}</div>
  </xb-notice>`;
}

function progress(n, cx) {
  const p = P(n);
  const v = typeof p.value === 'number' && Number.isFinite(p.value) ? Math.min(1, Math.max(0, p.value)) : null;
  if (v === null) {
    return html`<xb-progress data-k=${n.k} class=${cls('progress', 'busy', cell(cx))} role="progressbar" aria-label=${str(p.label) || 'loading'}>
      ${spinner()}${p.label ? html`<span class="pg-label">${p.label}</span>` : nothing}</xb-progress>`;
  }
  return html`<xb-progress data-k=${n.k} class=${cls('progress', 'det', cell(cx))} role="progressbar"
    aria-valuemin="0" aria-valuemax="100" aria-valuenow=${Math.round(v * 100)}>
    ${p.label ? html`<div class="pg-row"><span class="pg-label">${p.label}</span><span class="pg-pct">${Math.round(v * 100)}%</span></div>` : nothing}
    <div class="pg-track"><div class="pg-fill" style=${`width:${(v * 100).toFixed(1)}%`}></div></div>
  </xb-progress>`;
}

const chart = (n, cx) => html`<xb-chart data-k=${n.k} class=${cls('chart', cell(cx))} .node=${n}></xb-chart>`;

function code(n, cx) {
  const p = P(n);
  const u = cx.ui(n.k);
  const copy = async () => {
    await cx.v.copy(str(p.text));
    u.copied = true; cx.update();
    setTimeout(() => { u.copied = false; cx.update(); }, 1200);
  };
  return html`<xb-code data-k=${n.k} class=${cls('code', cx.place === 'group' ? 'cell' : 'box', p.wrap && 'wrap', p.copy && 'has-copy')}>
    <pre>${str(p.text)}</pre>
    ${p.copy ? html`<button class="copy" aria-label="Copy" @click=${copy}>${icon(u.copied ? 'check' : 'copy')}</button>` : nothing}
  </xb-code>`;
}

function empty(n, cx) {
  const p = P(n);
  const big = cx.place !== 'group' && (p.icon || p.title);
  return html`<xb-empty data-k=${n.k} class=${cls('empty', big ? 'big' : 'small', cell(cx))}>
    ${big && p.icon ? html`<span class="e-ic">${icon(p.icon)}</span>` : nothing}
    ${p.title ? html`<div class="e-title">${p.title}</div>` : nothing}
    ${p.text ? html`<div class="e-text">${p.text}</div>` : nothing}
  </xb-empty>`;
}

function terminal(n, cx) {
  const p = P(n);
  return html`<xb-terminal data-k=${n.k} class=${cls('terminal', cell(cx))} role="img" aria-label="terminal">
    <div class="term-bar">${icon('terminal')}<span>${str(p.title) || 'terminal'}</span></div>
    <pre class="term-body">${`# ${str(p.src) || 'pty'}\n$ `}<span class="term-cur"></span></pre>
  </xb-terminal>`;
}

// canvas: a WebView island. `html` is static (sandbox="" — no scripts, no
// same-origin); `src` is a page of the tile's own (a relative URL — anything
// with a scheme or a host is not drawn), sandboxed like one.
const RELATIVE = (s) => s !== '' && !/^[a-z][a-z0-9+.-]*:/i.test(s) && !s.startsWith('//') && !s.startsWith('\\');
function canvas(n, cx) {
  const p = P(n);
  const h = HEIGHTS.has(p.height) ? `var(--xb-h-${p.height})` : 'var(--xb-h-m)';
  const src = str(p.src);
  return html`<xb-canvas data-k=${n.k} class=${cls('canvas', cell(cx))} style=${`height:${h}`}>${p.html != null
    ? html`<iframe sandbox="" srcdoc=${str(p.html)} title="canvas"></iframe>`
    : RELATIVE(src) ? html`<iframe sandbox="allow-scripts allow-forms" src=${src} title="canvas"></iframe>` : nothing}</xb-canvas>`;
}

export const CONTENT = { text, markdown, image, icon: iconPrim, badge, notice, progress, chart, code, empty, terminal, canvas };

export const CONTENT_CSS = css`
  xb-text { display: block; white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; }
  xb-text.sel { user-select: text; }
  xb-text.clamp { display: -webkit-box; -webkit-box-orient: vertical; overflow: hidden; }
  xb-text.cell { min-height: 0; }
  xb-stack.h > xb-text { flex: 0 1 auto; }

  xb-markdown { display: block; min-width: 0; overflow-wrap: anywhere; }
  .md > :first-child, .md-li > :first-child, blockquote > :first-child { margin-top: 0; }
  .md > :last-child, .md-li > :last-child, blockquote > :last-child { margin-bottom: 0; }
  .md p { margin: 0 0 10px; }
  .md-h { margin: 16px 0 6px; }
  .md-h1 { font: var(--xb-font-title-2); font-weight: 700; }
  .md-h2 { font: var(--xb-font-title-3); font-weight: 600; }
  .md-h3 { font: var(--xb-font-headline); }
  .md-h4, .md-h5, .md-h6 { font: var(--xb-font-subheadline); font-weight: 600; }
  .md-list { margin: 0 0 10px; padding-left: 24px; }
  .md-list li { margin: 2px 0; }
  .md-list.loose li { margin: 6px 0; }
  .md-list li.task { list-style: none; display: flex; gap: 6px; margin-left: -22px; }
  .md-li { min-width: 0; }
  .md-li > p { margin: 0; }
  .md-check { display: flex; color: var(--xb-muted); padding-top: 2px; }
  .md-check.on { color: var(--xb-ok); }
  .md-check .ic { width: 16px; height: 16px; }
  .md code { font-family: var(--xb-mono); font-size: 0.86em; background: var(--xb-fill); border-radius: 5px; padding: 1px 5px; }
  .md-code { margin: 0 0 10px; border-radius: 10px; background: var(--xb-surface2); overflow: hidden; }
  .md-lang { font: var(--xb-font-caption); color: var(--xb-muted); padding: 6px 12px 0; }
  .md-code pre { margin: 0; padding: 8px 12px 10px; overflow-x: auto; font-family: var(--xb-mono); font-size: calc(var(--xb-size-subheadline) * 0.93); line-height: 1.45; }
  .md blockquote { margin: 0 0 10px; padding: 2px 0 2px 12px; border-left: 3px solid var(--xb-border); color: var(--xb-muted); }
  .md hr { border: 0; border-top: 0.5px solid var(--xb-separator); margin: 14px 0; }
  .md a { color: var(--xb-accent-text); text-decoration: none; }
  .md del { color: var(--xb-muted); }
  .md-table { overflow-x: auto; margin: 0 0 10px; }
  .md table { border-collapse: collapse; font: var(--xb-font-subheadline); }
  .md th, .md td { border-bottom: 0.5px solid var(--xb-separator); padding: 6px 12px 6px 0; text-align: left; vertical-align: top; }
  .md th { font-weight: 600; }
  .md.streaming > :last-child::after { content: ''; display: inline-block; width: 8px; height: 1em; margin-left: 2px; vertical-align: -2px; border-radius: 2px; background: var(--xb-accent); animation: xb-blink 1s steps(2) infinite; }
  @keyframes xb-blink { 50% { opacity: 0; } }

  xb-image { display: block; overflow: hidden; border-radius: 10px; background: var(--xb-fill); }
  xb-image.cell { border-radius: 0; background: none; padding: 0; }
  xb-image img { display: block; width: 100%; height: auto; max-height: var(--xb-h-xl); }
  xb-image.fixed img { height: 100%; max-height: none; }
  xb-image.tap img { cursor: zoom-in; }
  .img-ph { display: flex; align-items: center; justify-content: center; min-height: var(--xb-h-s); height: 100%; color: var(--xb-muted); }
  /* images side by side share the row (thumbnails), as the native renderer's do */
  xb-stack.h > xb-image { flex: 1 1 0; min-width: 0; }
  xb-image img + .img-ph, xb-image img[data-failed] { display: none; }
  xb-image img[data-failed] + .img-ph { display: flex; }
  xb-image.fixed .img-ph { min-height: 0; }
  .img-ph .ic { width: 32px; height: 32px; opacity: 0.6; }

  xb-icon { display: inline-flex; color: inherit; }
  xb-icon.cell { display: flex; }

  xb-badge { display: inline-flex; align-self: flex-start; }
  xb-badge.cell { display: flex; }
  .pill.pulse { display: inline-flex; align-items: center; gap: 6px; }
  .pulse-dot { width: 7px; height: 7px; border-radius: 4px; background: currentColor; animation: xb-pulse 1.6s ease-in-out infinite; }
  @keyframes xb-pulse { 50% { opacity: 0.3; } }

  xb-notice { display: flex; align-items: flex-start; gap: 10px; --n: var(--xb-muted); }
  xb-notice.box { padding: 12px 14px; border-radius: 12px; background: color-mix(in srgb, var(--n) 13%, var(--xb-surface)); }
  xb-notice.cell { background: color-mix(in srgb, var(--n) 10%, var(--xb-surface)); }
  .n-accent { --n: var(--xb-accent); } .n-ok { --n: var(--xb-ok); } .n-warn { --n: var(--xb-warn); } .n-danger { --n: var(--xb-danger); }
  .n-info { --n: var(--xb-muted); }
  .n-ic { display: flex; color: var(--n); padding-top: 1px; }
  .n-accent .n-ic { color: var(--xb-accent-text); }
  .n-text { flex: 1; min-width: 0; font: var(--xb-font-subheadline); overflow-wrap: anywhere; }
  .n-title { font-weight: 600; }
  .n-title + .n-body { margin-top: 2px; }

  xb-progress { display: flex; flex-direction: column; gap: 8px; }
  xb-progress.busy { flex-direction: row; align-items: center; gap: 10px; color: var(--xb-muted); }
  xb-progress.busy:not(.cell) { justify-content: center; padding: 12px 0; }
  .pg-label { font: var(--xb-font-subheadline); color: var(--xb-muted); }
  .pg-row { display: flex; justify-content: space-between; gap: 8px; }
  .pg-pct { font: var(--xb-font-subheadline); color: var(--xb-muted); font-variant-numeric: tabular-nums; }
  .pg-track { height: 4px; border-radius: 2px; background: var(--xb-fill); overflow: hidden; }
  .pg-fill { height: 100%; border-radius: 2px; background: var(--xb-accent); }

  xb-chart { display: block; min-width: 0; }

  xb-code { display: flex; align-items: center; gap: 4px; min-width: 0; }
  xb-code.box { border-radius: 10px; background: var(--xb-surface2); }
  xb-code pre { flex: 1 1 auto; min-width: 0; margin: 0; padding: 10px 12px; overflow-x: auto; font-family: var(--xb-mono); font-size: calc(var(--xb-size-subheadline) * 0.93); line-height: 1.45; white-space: pre; }
  xb-code.cell { padding-right: 8px; }
  xb-code.cell pre { padding: 0; }
  xb-code.wrap pre { white-space: pre-wrap; overflow-wrap: anywhere; }
  xb-code.box.has-copy pre { padding-right: 0; }
  xb-code .copy { flex: none; align-self: flex-start; margin: 4px 4px 0 0; width: 34px; height: 34px; border-radius: 8px; display: flex; align-items: center; justify-content: center; color: var(--xb-accent-text); }
  xb-code.cell .copy { margin: -6px 0; align-self: center; }
  xb-code .copy .ic { width: 18px; height: 18px; }
  xb-code .copy:active { background: var(--xb-fill); }

  xb-empty { display: flex; flex-direction: column; }
  xb-empty.small { color: var(--xb-muted); }
  xb-empty.small .e-title { color: var(--xb-text); }
  xb-empty.big { align-items: center; text-align: center; padding: 48px 24px; gap: 6px; }
  .e-ic { color: var(--xb-muted); display: flex; margin-bottom: 8px; }
  .e-ic .ic { width: 52px; height: 52px; stroke-width: 1.5; }
  .big .e-title { font: var(--xb-font-title-2); font-weight: 700; }
  .big .e-text { font: var(--xb-font-callout); color: var(--xb-muted); max-width: 300px; }

  xb-terminal { display: block; border-radius: 10px; overflow: hidden; background: #11141a; color: #d4d9e0; min-height: var(--xb-h-m); }
  xb-terminal.cell { border-radius: 0; padding: 0; }
  .term-bar { display: flex; align-items: center; gap: 8px; padding: 8px 12px; font: var(--xb-font-caption); color: #868f9a; background: #1b1e24; }
  .term-bar .ic { width: 16px; height: 16px; }
  .term-body { margin: 0; padding: 10px 12px; font-family: var(--xb-mono); font-size: 13px; line-height: 1.4; white-space: pre-wrap; }
  .term-cur { display: inline-block; width: 8px; height: 15px; background: #d4d9e0; vertical-align: -3px; animation: xb-blink 1s steps(2) infinite; }

  xb-canvas { display: block; border-radius: 10px; overflow: hidden; background: var(--xb-surface); }
  xb-canvas.cell { border-radius: 0; padding: 0; }
  xb-canvas iframe { display: block; width: 100%; height: 100%; border: 0; }
`;
