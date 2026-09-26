/**
 * xb/render-structure.js — structure and navigation primitives of the
 * reference renderer: fragment, nav, screen, toolbar, section, stack, list,
 * row, actions, disclosure, tabs, tab, sheet, split, spacer, divider.
 *
 * Phone conventions, as the app draws them with SwiftUI: a list/form screen
 * is an inset grouped list (sections are rounded cards of cells with inset
 * separators, uppercase footnote headers, footers below); the navigation bar
 * shows a large title on a stack's root screen and an inline title on pushed
 * ones; a nav shows its last screen with a back button; a sheet slides over
 * everything with a grabber. Helpers and the `cx` contract: xb/render-base.js.
 */
import { css, repeat } from '/vendor/lit-all.min.js';
import { html, nothing, own, P, cls, tone, icon, str } from '/vendor/xb/render-base.js';

const backTitle = (s) => { const t = str(P(s).title); return t && t.length <= 14 ? t : 'Back'; };

function fragment(n, cx) {
  return html`<xb-fragment data-k=${n.k}>${cx.kids(n)}</xb-fragment>`;
}

// nav: every screen stays mounted (scroll positions survive a push); only the
// last is shown. Back hides the top screen at once and reports `pop` with
// the depth that remains; the tile then drops it from the tree.
function nav(n, cx) {
  const screens = n.c || [];
  const u = cx.ui(n.k);
  const sig = screens.map((s) => s.k).join('\n');
  if (u.sig !== sig) { u.sig = sig; u.popped = 0; }
  const depth = Math.max(1, screens.length - (u.popped || 0));
  const go = () => {
    u.popped = screens.length - depth + 1;
    cx.emit(n, 'pop', { depth: depth - 1 });
  };
  return html`<xb-nav data-k=${n.k} class="nav">${repeat(screens, (s) => s.k, (s, i) => html`
    <div class="nav-page" ?hidden=${i !== depth - 1}>${cx.in('free', {
      pushed: i > 0, offstage: i !== depth - 1, sheet: false,
      back: i > 0 && i === depth - 1 ? { title: backTitle(screens[i - 1]), go } : null,
    }).node(s)}</div>`)}</xb-nav>`;
}

function searchField(n, cx) {
  const v = str(cx.val(n, 'search', ''));
  return html`<div class="search">${icon('search')}<input type="search" placeholder="Search"
    .value=${v} @input=${(e) => cx.emit(n, 'search', { value: e.target.value })}></div>`;
}

function screen(n, cx) {
  const p = P(n);
  const style = p.style || 'scroll';
  const grouped = style === 'list' || style === 'form';
  const all = n.c || [];
  const tb = all.find((c) => c.t === 'toolbar');
  const docked = all.filter((c) => c.t === 'composer' || (c.t === 'tabs' && P(c).style === 'bar'));
  const body = all.filter((c) => c !== tb && !docked.includes(c));
  const chat = body.some((c) => c.t === 'transcript');
  const large = p.large === true || (p.large !== false && !cx.x.pushed && !cx.x.sheet && style !== 'scroll');
  const u = cx.ui(n.k);
  if (!cx.x.offstage) cx.v.shown(n, cx);
  const onScroll = (e) => {
    const s = e.target.scrollTop > (large ? 40 : 1);
    if (s !== !!u.scrolled) { u.scrolled = s; cx.update(); }
  };
  const back = cx.x.back;
  const refresh = p.refreshable && cx.on(n, 'refresh');
  const title = str(p.title);
  const inner = cx.in(grouped ? 'screen' : 'free', { pushed: false, back: null, offstage: false });
  const showBar = !cx.x.sheet || back || title;
  return html`<xb-screen data-k=${n.k} class=${cls('screen', `style-${style}`, u.scrolled && 'scrolled', large && 'has-large')}>
    ${showBar ? html`<div class=${cls('bar', (u.scrolled || !large) && 'solid')}>
      <div class="bar-lead">${back ? html`<button class="back" @click=${back.go}>${icon('back')}<span>${back.title}</span></button>` : nothing}</div>
      <div class=${cls('bar-title', large && !u.scrolled && 'away')}>
        <div class="bt">${title}</div>${p.subtitle && !large ? html`<div class="bs">${p.subtitle}</div>` : nothing}
      </div>
      <div class="bar-trail">
        ${refresh ? html`<button class="tb-btn" aria-label="Refresh" @click=${() => cx.emit(n, 'refresh', {})}>${icon('refresh')}</button>` : nothing}
        ${tb ? cx.in('toolbar').node(tb) : nothing}
      </div>
    </div>` : nothing}
    <div class=${cls('body', grouped ? 'grouped' : 'free', chat && 'chat')} @scroll=${onScroll}>
      ${large ? html`<div class="large"><h1 class="lt">${title}</h1>${p.subtitle ? html`<div class="ls">${p.subtitle}</div>` : nothing}</div>` : nothing}
      ${own(p, 'search') ? searchField(n, cx) : nothing}
      ${repeat(body, (c) => c.k, (c) => inner.node(c))}
    </div>
    ${repeat(docked, (c) => c.k, (c) => cx.in('dock').node(c))}
  </xb-screen>`;
}

function toolbar(n, cx) {
  return html`<xb-toolbar data-k=${n.k} class="toolbar">${cx.kids(n, 'toolbar')}</xb-toolbar>`;
}

function section(n, cx) {
  const p = P(n);
  const collapsible = !!p.collapsible;
  const collapsed = collapsible && !!cx.val(n, 'collapsed', false);
  const toggle = () => cx.emit(n, 'toggle', { collapsed: !collapsed });
  const head = p.title || p.badge || collapsible;
  const title = html`<span class="sec-t">${str(p.title)}</span>`;
  const has = (n.c || []).length > 0;
  return html`<xb-section data-k=${n.k} class=${cls('section', cx.place === 'group' && 'cell nested')}>
    ${head ? html`<div class="sec-head">
      ${collapsible
        ? html`<button class="sec-title" aria-expanded=${!collapsed} @click=${toggle}>${title}${icon(collapsed ? 'forward' : 'expand', 'sec-chev')}</button>`
        : html`<div class="sec-title">${title}</div>`}
      ${p.badge ? html`<span class="sec-badge">${p.badge}</span>` : nothing}
    </div>` : nothing}
    ${!collapsed && has ? html`<div class="group">${cx.kids(n, 'group')}</div>` : nothing}
    ${p.footer && !collapsed ? html`<div class="sec-foot">${p.footer}</div>` : nothing}
  </xb-section>`;
}

function stack(n, cx) {
  const p = P(n);
  const h = p.axis === 'h';
  const gap = p.gap ? `var(--xb-gap-${p.gap})` : h ? '8px' : '12px';
  const align = { start: 'flex-start', center: 'center', end: 'flex-end' }[p.align] || (h ? 'center' : 'stretch');
  return html`<xb-stack data-k=${n.k} class=${cls('stack', h ? 'h' : 'v', p.wrap && 'wrap', cx.place === 'group' && 'cell')}
    style=${`gap:${gap};align-items:${align}`}>${cx.kids(n, 'free')}</xb-stack>`;
}

// list: runs of rows share one card (inset/grouped) or a full-bleed plain
// list; each section is its own card.
function list(n, cx) {
  const style = P(n).style || 'inset';
  const runs = [];
  for (const c of n.c || []) {
    if (c.t === 'section') runs.push(c);
    else if (Array.isArray(runs[runs.length - 1])) runs[runs.length - 1].push(c);
    else runs.push([c]);
  }
  const more = cx.on(n, 'more') ? html`<xb-more @more=${() => cx.emit(n, 'more', {})}></xb-more>` : nothing;
  return html`<xb-list data-k=${n.k} class=${cls('list', `list-${style}`)}>${runs.map((r) => (Array.isArray(r)
    ? html`<div class=${style === 'plain' ? 'plain' : 'group'}>${repeat(r, (c) => c.k, (c) => cx.in('group').node(c))}</div>`
    : cx.in('screen').node(r)))}${more}</xb-list>`;
}

function row(n, cx) {
  const p = P(n);
  const all = n.c || [];
  const acts = all.filter((c) => c.t === 'actions');
  const content = all.filter((c) => c.t !== 'actions');
  const tap = cx.on(n, 'tap') && !p.disabled;
  const m = (f) => p.mono === f || p.mono === 'all';
  const t = p.tone || '';
  const lead = p.icon ? html`<span class=${cls('row-ic', tone(t || 'accent'))}>${icon(p.icon)}</span>`
    : t && !p.badge ? html`<span class=${cls('row-dot', `bg-${t}`)}></span>` : nothing;
  const inset = p.icon ? 'calc(16px + var(--xb-icon) + 14px)' : t && !p.badge ? '36px' : '16px';
  const act = tap ? () => cx.emit(n, 'tap', {}) : null;
  const key = tap ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); act(); } } : null;
  return html`<xb-row data-k=${n.k} style=${`--sep:${inset}`}
    class=${cls('row', cx.place === 'group' ? 'cell' : 'lone', tap && 'tap', p.disabled && 'disabled')}>
    <div class="row-main" role=${tap ? 'button' : nothing} tabindex=${tap ? '0' : nothing} @click=${act} @keydown=${key}>
      ${lead}
      <div class="row-text">
        <div class=${cls('row-title', m('title') && 'mono')}>${str(p.title)}</div>
        ${p.subtitle ? html`<div class=${cls('row-sub', m('subtitle') && 'mono')}>${p.subtitle}</div>` : nothing}
      </div>
      ${p.detail ? html`<div class=${cls('row-detail', m('detail') && 'mono')}>${p.detail}</div>` : nothing}
      ${p.badge ? html`<span class=${cls('pill', t && `pill-${t}`)}>${p.badge}</span>` : nothing}
      ${p.selected ? html`<span class="row-check">${icon('check')}</span>` : nothing}
      ${p.nav ? html`<span class="row-chev">${icon('forward')}</span>` : nothing}
    </div>
    ${content.length ? html`<div class="row-content">${repeat(content, (c) => c.k, (c) => cx.in('free').node(c))}</div>` : nothing}
    ${repeat(acts, (c) => c.k, (c) => cx.in('actions').node(c))}
  </xb-row>`;
}

function actions(n, cx) {
  return html`<xb-actions data-k=${n.k} class="actions">${cx.kids(n, 'actions')}</xb-actions>`;
}

function disclosure(n, cx) {
  const open = !!cx.val(n, 'open', false);
  const toggle = () => cx.emit(n, 'toggle', { open: !open });
  const inGroup = cx.place === 'group';
  return html`<xb-disclosure data-k=${n.k} class=${cls('disc', inGroup ? 'cell' : 'free', open && 'open')}>
    <button class="disc-head" aria-expanded=${open} @click=${toggle}>
      <span class="disc-t">${str(P(n).title)}</span>${icon('forward', 'disc-chev')}
    </button>
    ${open ? html`<div class="disc-body">${cx.kids(n, inGroup ? 'group' : 'free')}</div>` : nothing}
  </xb-disclosure>`;
}

function tabs(n, cx) {
  const p = P(n);
  const list = (n.c || []).filter((c) => c.t === 'tab');
  const first = list[0] ? str(P(list[0]).key) : '';
  const sel = str(cx.val(n, 'selected', first));
  const cur = list.find((t) => str(P(t).key) === sel) || list[0];
  const choose = (key) => { if (key !== sel) cx.emit(n, 'change', { key }); };
  const bar = p.style === 'bar';
  const items = list.map((t) => { const q = P(t); return html`<button role="tab" aria-selected=${t === cur}
    class=${cls(bar ? 'tb-item' : 'seg-item', t === cur && 'on')} @click=${() => choose(str(q.key))}>
    ${bar ? icon(q.icon || 'ui-circle') : nothing}<span>${str(q.title || q.key)}</span>${q.badge ? html`<span class="tab-badge">${q.badge}</span>` : nothing}
  </button>`; });
  const panes = repeat(list, (t) => t.k, (t) => html`<xb-tab data-k=${t.k} class="tab" ?hidden=${t !== cur}>${cx.kids(t)}</xb-tab>`);
  if (bar) return html`<xb-tabs data-k=${n.k} class="tabs bar"><div class="tabs-body">${panes}</div><div class="tabbar" role="tablist">${items}</div></xb-tabs>`;
  return html`<xb-tabs data-k=${n.k} class=${cls('tabs', 'segmented', cx.place === 'group' && 'cell')}>
    <div class="seg" role="tablist">${items}</div>${panes}</xb-tabs>`;
}

// tab outside tabs (a child-rule violation the runtime already reported)
const tab = (n, cx) => html`<xb-tab data-k=${n.k} class="tab">${cx.kids(n)}</xb-tab>`;

function sheet(n, cx) {
  const p = P(n);
  if (!cx.val(n, 'open', true)) return html`<xb-sheet data-k=${n.k} hidden></xb-sheet>`;
  const dismiss = () => cx.emit(n, 'dismiss', {});
  const det = Array.isArray(p.detents) ? p.detents : p.detents ? [p.detents] : ['large'];
  const all = n.c || [];
  const tb = all.find((c) => c.t === 'toolbar');
  const body = all.filter((c) => c !== tb);
  const whole = body.length === 1 && (body[0].t === 'screen' || body[0].t === 'nav');
  const esc = (e) => { if (e.key === 'Escape') dismiss(); };
  return html`<xb-sheet data-k=${n.k} class="sheet-layer" @keydown=${esc}>
    <div class="scrim" @click=${dismiss}></div>
    <div class=${cls('sheet', det[0] === 'medium' ? 'medium' : 'large')} role="dialog" aria-modal="true" aria-label=${str(p.title) || nothing}>
      <div class="grabber"></div>
      ${whole ? nothing : html`<div class="sheet-bar">
        <div class="bar-lead"><button class="sheet-x" aria-label="Close" @click=${dismiss}>${icon('xmark')}</button></div>
        <div class="bar-title"><div class="bt">${str(p.title)}</div></div>
        <div class="bar-trail">${tb ? cx.in('toolbar').node(tb) : nothing}</div>
      </div>`}
      <div class=${cls('sheet-body', whole && 'whole')}>${repeat(body, (c) => c.k, (c) => cx.in('free', { sheet: true }).node(c))}</div>
    </div>
  </xb-sheet>`;
}

function split(n, cx) {
  const prefer = P(n).prefer === 'single' ? 'single' : 'auto';
  return html`<xb-split data-k=${n.k} class=${cls('split', prefer)}>${repeat(n.c || [], (c) => c.k, (c, i) => html`
    <div class=${i === 0 ? 'split-a' : 'split-b'}>${cx.in('free').node(c)}</div>`)}</xb-split>`;
}

const spacer = (n) => html`<xb-spacer data-k=${n.k} class="spacer"></xb-spacer>`;
const divider = (n, cx) => html`<xb-divider data-k=${n.k} class=${cls('divider', cx.place === 'group' && 'cell')}></xb-divider>`;

export const STRUCTURE = { fragment, nav, screen, toolbar, section, stack, list, row, actions, disclosure, tabs, tab, sheet, split, spacer, divider };

export const STRUCTURE_CSS = css`
  xb-nav, .nav-page { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .nav-page > * { flex: 1 1 auto; min-height: 0; }

  xb-screen { display: flex; flex-direction: column; height: 100%; min-height: 0; position: relative; background: var(--xb-bg); }
  .bar {
    position: absolute; top: 0; left: 0; right: 0; z-index: 3; min-height: 44px;
    display: flex; align-items: center; gap: 4px; padding: 0 8px; transition: background 0.15s;
  }
  .bar-lead, .bar-trail { flex: 1 1 0; }
  .bar.solid { background: color-mix(in srgb, var(--xb-bg) 82%, transparent); backdrop-filter: saturate(1.6) blur(18px); -webkit-backdrop-filter: saturate(1.6) blur(18px); }
  .scrolled .bar { box-shadow: 0 0.5px 0 var(--xb-separator); }
  .bar-lead { display: flex; align-items: center; min-width: 0; }
  .bar-trail { display: flex; align-items: center; justify-content: flex-end; gap: 4px; }
  .bar-title { flex: 0 1 auto; text-align: center; min-width: 0; padding: 4px 0; transition: opacity 0.15s; }
  .bar-title.away { opacity: 0; }
  .bt { font: var(--xb-font-headline); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bs { font: var(--xb-font-caption); color: var(--xb-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .back { display: flex; align-items: center; gap: 2px; color: var(--xb-accent-text); padding: 8px 8px 8px 0; font: var(--xb-font-body); min-width: 0; }
  .back .ic { width: calc(var(--xb-icon) + 6px); height: calc(var(--xb-icon) + 6px); stroke-width: 2.4; margin-right: -2px; }
  .back span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tb-btn { display: flex; align-items: center; justify-content: center; min-width: 36px; height: 36px; color: var(--xb-accent-text); border-radius: 18px; }

  .body { flex: 1 1 auto; min-height: 0; overflow-y: auto; overflow-x: hidden; padding-top: 44px; display: flex; flex-direction: column; overscroll-behavior: contain; }
  /* scroll containers never squeeze their children (an overflow:hidden child
     would otherwise shrink to nothing) */
  .body > *, .sheet-body > *, .disc-body > *, xb-list > * { flex-shrink: 0; }
  .body.grouped { gap: 22px; padding: 62px var(--xb-margin) 40px; }
  .body.free { gap: 12px; padding: 56px var(--xb-margin) 32px; }
  .has-large .body.grouped, .has-large .body.free { padding-top: 44px; }
  .body.chat { overflow: hidden; padding: 44px 0 0; gap: 0; }
  .body.chat > xb-transcript { flex: 1 1 auto; min-height: 0; }
  .large { padding: 2px 4px 0; margin-bottom: -6px; }
  .lt { margin: 0; font: var(--xb-font-large-title); font-weight: 700; letter-spacing: 0.3px; overflow-wrap: anywhere; }
  .ls { font: var(--xb-font-subheadline); color: var(--xb-muted); margin-top: 2px; }
  .search { display: flex; align-items: center; gap: 6px; height: 36px; padding: 0 10px; border-radius: 10px; background: var(--xb-fill); color: var(--xb-muted); flex: none; }
  .search input { flex: 1; min-width: 0; border: 0; outline: 0; background: none; color: var(--xb-text); font: var(--xb-font-body); }
  .search .ic { width: 18px; height: 18px; }

  xb-toolbar { display: flex; align-items: center; gap: 2px; }
  xb-toolbar > xb-badge { align-self: center; margin: 0 4px; }
  .toolbar > * { flex: none; }

  xb-section { display: block; }
  .sec-head { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; padding: 0 16px 7px; }
  .sec-title { display: flex; align-items: center; gap: 4px; min-width: 0; font: var(--xb-font-footnote); text-transform: uppercase; letter-spacing: 0.2px; color: var(--xb-muted); text-align: left; }
  button.sec-title { padding: 2px 0; }
  .sec-chev { width: 14px; height: 14px; stroke-width: 2.6; }
  .sec-badge { font: var(--xb-font-caption); font-weight: 600; color: var(--xb-muted); background: var(--xb-fill); border-radius: 999px; padding: 1px 8px; font-variant-numeric: tabular-nums; }
  .sec-foot { padding: 7px 16px 0; font: var(--xb-font-footnote); color: var(--xb-muted); }
  xb-section.nested { padding-top: 12px; padding-bottom: 12px; }
  xb-section.nested .sec-head, xb-section.nested .sec-foot { padding-left: 0; padding-right: 0; }

  .group { background: var(--xb-surface); border-radius: var(--xb-radius-group); overflow: hidden; }
  .cell { position: relative; min-height: 44px; padding: 11px 16px; }
  .cell + .cell::before, .disc-body > .cell:first-child::before, .plain > * + *::before {
    content: ''; position: absolute; top: 0; left: var(--sep, 16px); right: 0; border-top: 0.5px solid var(--xb-separator);
  }
  .plain { margin: 0 calc(-1 * var(--xb-margin)); background: var(--xb-surface); }
  .plain > * { position: relative; }

  xb-stack { display: flex; min-width: 0; }
  xb-stack.v { flex-direction: column; }
  xb-stack.h { flex-direction: row; }
  xb-stack.wrap { flex-wrap: wrap; }
  xb-list { display: flex; flex-direction: column; gap: 22px; }

  xb-row { display: block; }
  xb-row.cell { padding: 0; }
  xb-row.lone { background: var(--xb-surface); border-radius: var(--xb-radius-group); }
  /* a row: lead | title+subtitle | detail | badge | check | chevron. With
     large text the detail and badge stack under the title (as iOS does at
     accessibility sizes) instead of squeezing it. */
  .row-main { display: grid; grid-template-columns: auto minmax(0, 1fr) fit-content(45%) auto auto auto;
    grid-template-areas: "lead text detail badge check chev"; align-items: center;
    min-height: 44px; padding: 10px 16px; outline-offset: -2px; }
  .row-main > .row-ic, .row-main > .row-dot { grid-area: lead; margin-right: 12px; }
  .row-main > .row-text { grid-area: text; }
  .row-main > .row-detail { grid-area: detail; margin-left: 12px; }
  .row-main > .pill { grid-area: badge; margin-left: 10px; justify-self: end; }
  .row-main > .row-check { grid-area: check; margin-left: 10px; }
  .row-main > .row-chev { grid-area: chev; margin-left: 8px; }
  :host([text="large"]) .row-main { grid-template-columns: auto minmax(0, 1fr) auto auto;
    grid-template-areas: "lead text check chev" "lead detail check chev" "lead badge check chev"; }
  :host([text="large"]) .row-main > .row-dot { align-self: start; margin-top: calc((var(--xb-line-body) - 8px) / 2); }
  :host([text="large"]) .row-main > .row-ic { align-self: start; margin-top: calc((var(--xb-line-body) - var(--xb-icon)) / 2); }
  :host([text="large"]) .row-main > .row-detail { margin: 2px 0 0; text-align: left; max-width: none; }
  :host([text="large"]) .row-main > .pill { margin: 6px 0 0; justify-self: start; }
  .tap > .row-main { cursor: pointer; }
  .tap > .row-main:active { background: var(--xb-fill); }
  .disabled { opacity: 0.45; }
  .row-ic { display: flex; flex: none; }
  .row-dot { width: 8px; height: 8px; border-radius: 4px; flex: none; margin-left: -2px; }
  .bg-muted { background: var(--xb-muted); } .bg-accent { background: var(--xb-accent); } .bg-ok { background: var(--xb-ok); }
  .bg-warn { background: var(--xb-warn); } .bg-danger { background: var(--xb-danger); }
  .row-text { min-width: 0; }
  .row-title { overflow-wrap: anywhere; }
  .row-sub { font: var(--xb-font-subheadline); color: var(--xb-muted); margin-top: 1px; overflow-wrap: anywhere; }
  .row-sub.mono { font-size: calc(var(--xb-size-subheadline) * 0.94); }
  .row-detail { color: var(--xb-muted); text-align: right; min-width: 0; overflow-wrap: anywhere; }
  .row-check { color: var(--xb-accent-text); display: flex; }
  .row-chev { color: var(--xb-muted); opacity: 0.6; display: flex; margin-right: -6px; }
  .row-chev .ic { width: calc(var(--xb-icon) - 2px); height: calc(var(--xb-icon) - 2px); stroke-width: 2.6; }
  .row-content { padding: 0 16px 12px; display: flex; flex-direction: column; gap: 8px; }
  .pill { flex: none; font: var(--xb-font-footnote); font-weight: 600; padding: 2px 9px; border-radius: 999px; background: var(--xb-fill); color: var(--xb-muted); white-space: nowrap; font-variant-numeric: tabular-nums; }
  .pill-accent { background: color-mix(in srgb, var(--xb-accent) 20%, transparent); color: var(--xb-accent-text); }
  .pill-ok { background: color-mix(in srgb, var(--xb-ok) 16%, transparent); color: var(--xb-ok); }
  .pill-warn { background: color-mix(in srgb, var(--xb-warn) 18%, transparent); color: var(--xb-warn); }
  .pill-danger { background: color-mix(in srgb, var(--xb-danger) 16%, transparent); color: var(--xb-danger); }

  xb-actions { display: flex; flex-wrap: wrap; gap: 8px; padding: 0 16px 12px; }
  xb-row:has(.row-ic) > xb-actions, xb-row:has(.row-dot) > xb-actions { padding-left: var(--sep); }

  xb-disclosure { display: block; }
  xb-disclosure.cell { padding: 0; }
  .disc-head { display: flex; align-items: center; gap: 8px; width: 100%; min-height: 44px; padding: 10px 16px; text-align: left; }
  .disc-t { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .disc-chev { color: var(--xb-accent-text); transition: transform 0.15s; width: calc(var(--xb-icon) - 4px); height: calc(var(--xb-icon) - 4px); stroke-width: 2.6; }
  .open > .disc-head .disc-chev { transform: rotate(90deg); }
  .disc-body { display: block; }
  xb-disclosure.cell > .disc-body { padding-left: 12px; }
  xb-disclosure.free > .disc-head { padding: 6px 0; font: var(--xb-font-headline); }
  xb-disclosure.free > .disc-body { display: flex; flex-direction: column; gap: 12px; padding-top: 4px; }

  xb-tabs.segmented { display: contents; }
  xb-tabs.segmented.cell { display: block; }
  xb-tab { display: contents; }
  .seg { display: flex; padding: 2px; border-radius: 9px; background: var(--xb-fill); flex: none; }
  .seg-item { flex: 1 1 0; min-width: 0; display: flex; align-items: center; justify-content: center; gap: 6px; padding: 5px 8px; border-radius: 7px; font: var(--xb-font-subheadline); font-weight: 500; color: var(--xb-text); white-space: nowrap; }
  .seg-item.on { background: var(--xb-control-on); box-shadow: 0 1px 3px rgba(0, 0, 0, 0.14), 0 0 0 0.5px rgba(0, 0, 0, 0.04); font-weight: 600; }
  .seg-item span { overflow: hidden; text-overflow: ellipsis; }
  .tab-badge { font: var(--xb-font-caption2); font-weight: 700; background: var(--xb-danger); color: #fff; border-radius: 999px; padding: 1px 6px; }
  xb-tabs.bar { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  xb-tabs.bar > .tabs-body { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; }
  xb-tabs.bar > .tabs-body > xb-tab:not([hidden]) { display: flex; flex-direction: column; flex: 1 1 auto; min-height: 0; }
  .tabbar { display: flex; flex: none; padding: 6px 8px 22px; border-top: 0.5px solid var(--xb-separator);
    background: color-mix(in srgb, var(--xb-surface) 88%, transparent); backdrop-filter: blur(18px); }
  .tb-item { flex: 1; display: flex; flex-direction: column; align-items: center; gap: 2px; font: var(--xb-font-caption2); font-weight: 500; color: var(--xb-muted); position: relative; }
  .tb-item.on { color: var(--xb-accent-text); }
  .tb-item .ic { width: 24px; height: 24px; }
  .tb-item .tab-badge { position: absolute; top: -4px; left: calc(50% + 6px); }

  .sheet-layer { position: fixed; inset: 0; z-index: 20; display: flex; flex-direction: column; justify-content: flex-end; }
  .scrim { position: absolute; inset: 0; background: var(--xb-scrim); animation: xb-fade 0.2s ease-out; }
  .sheet { position: relative; display: flex; flex-direction: column; background: var(--xb-bg); border-radius: 14px 14px 0 0;
    box-shadow: var(--xb-shadow); animation: xb-rise 0.25s cubic-bezier(0.2, 0.9, 0.3, 1); min-height: 0; }
  .sheet.large { height: calc(100% - 12px); }
  .sheet.medium { height: 52%; }
  .grabber { width: 36px; height: 5px; border-radius: 3px; background: var(--xb-border); margin: 6px auto 0; flex: none; }
  .sheet-bar { display: flex; align-items: center; gap: 4px; padding: 4px 12px 6px; flex: none; min-height: 48px; }
  .sheet-x { width: 32px; height: 32px; border-radius: 16px; background: var(--xb-fill); color: var(--xb-muted); display: flex; align-items: center; justify-content: center; }
  .sheet-x .ic { width: 16px; height: 16px; stroke-width: 2.6; }
  .sheet-body { flex: 1 1 auto; min-height: 0; overflow-y: auto; padding: 8px var(--xb-margin) 32px; display: flex; flex-direction: column; gap: 14px; }
  .sheet-body.whole { padding: 0; }
  .sheet-body.whole > * { flex: 1 1 auto; min-height: 0; }
  @keyframes xb-fade { from { opacity: 0; } }
  @keyframes xb-rise { from { transform: translateY(40%); opacity: 0.4; } }

  .root { container: xbroot / inline-size; }
  xb-split { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  xb-split > div { flex: 1 1 50%; min-height: 0; display: flex; flex-direction: column; }
  xb-split > div > * { flex: 1 1 auto; min-height: 0; }
  xb-split > .split-a { border-bottom: 0.5px solid var(--xb-separator); }
  @container xbroot (min-width: 700px) {
    xb-split.auto { flex-direction: row; }
    xb-split.auto > .split-a { flex: 0 0 clamp(280px, 36%, 400px); border-bottom: 0; border-right: 0.5px solid var(--xb-separator); }
    xb-split.auto > .split-b { flex: 1 1 auto; }
  }

  xb-spacer { display: block; flex: 1 1 auto; min-width: 8px; min-height: 8px; }
  xb-divider { display: block; flex: none; align-self: stretch; border-top: 0.5px solid var(--xb-separator); }
  xb-stack.h > xb-divider { border-top: 0; border-left: 0.5px solid var(--xb-separator); }
  xb-divider.cell { min-height: 0; padding: 0; border: 0; height: 8px; background: var(--xb-bg); }
`;

