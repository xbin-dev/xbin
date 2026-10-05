// harness-board.js — the Coding agents board on the web (D147 §4.3.6,
// §8 U7): the coding agents in the open conversation's tree — at home, every
// one of yours that runs or needs you — in one place.
//
//   top   the chip "⌨ 3 coding agents · 1 needs you" (a conversation with
//         coding agents below it; at home yours at work) — opens the board
//   dock  #hboard, a right dock (.wrap's third column from 1100 px; over the
//         chat below that): a row per coding agent in the order they started —
//         never re-sorted as they change, so nothing moves under the pointer —
//         each a child card (harness-child.js: its park answered in place,
//         Open ↗, Stop, Cancel, Message), at home under its conversation's
//         name; the "N need you" filter; ✕ closes it (it stays open across
//         conversations and follows the one open)
//   task  the unfolded pinned task's Delegated section: each coding agent
//         below the run — its state, its task, a link to its chat
//   host  the dock hosts other modules' sections (ext.dock(v): {key, title,
//         badge?, tpl()}, CI's): a tab strip "Coding agents · CI" when any
//         answers; openDock(key) opens it on one — even with no coding agents
//
// The rows are app.board's (model/harness-board.js): the tree, the links held
// and the stream. A parked row whose summary has only the compact park (a
// grandchild, home) reads the child's newest page once (loadTail), so its
// card can answer it here.
import { html, nothing, render, repeat } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { cardTpl } from './harness-child.js';
import { loadTail } from './model/harness-child.js';
import { filterWords, emptyWords, delegatedWords } from './model/harness-board.js';

const st = { open: false, needs: false, tab: 'agents' }; // the dock is open; only the ones that need you; its tab ('agents' or a section's key)
let dock = null;

const rootOf = (v) => (v ? v.run.rootId || v.run.id : null);
const toggle = () => { st.open = !(st.open && st.tab === 'agents'); st.tab = 'agents'; ctx.paint(); };
/** openDock(key): the right dock open on a tab ('agents', or a section ext.dock answers); dockTab(): the one shown ('' closed). */
export function openDock(key = 'agents') { st.open = true; st.tab = key; ctx.paint(); }
export const dockTab = () => (st.open ? st.tab : '');
const onKey = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } };

ext.register({
  top: (v) => chipTpl(v),
  paint: (v) => paintDock(v),
  task: (v) => delegatedTpl(v),
});

// --- the chip -------------------------------------------------------------------------

function chipTpl(v) {
  const app = ctx.app;
  if (!app) return null;
  const c = app.board.chip(rootOf(v));
  if (!c) return null;
  return html`<span class="badge hbchip" id="hbchip" role="button" tabindex="0" data-tone=${c.tone} aria-pressed=${st.open && st.tab === 'agents' ? 'true' : 'false'}
    title=${c.title} @click=${toggle} @keydown=${onKey}>${c.text}</span>`;
}

// --- the dock -------------------------------------------------------------------------

function paintDock(v) {
  const wrap = document.querySelector('.wrap');
  if (!dock) {
    if (!st.open || !wrap) return;
    dock = document.createElement('aside');
    dock.id = 'hboard';
    dock.className = 'hboard';
    dock.setAttribute('aria-label', 'Coding agents');
    wrap.append(dock);
  }
  dock.hidden = !st.open;
  wrap.classList.toggle('dockon', st.open);
  render(st.open ? boardTpl(v) : nothing, dock);
}

function boardTpl(v) {
  const app = ctx.app;
  const root = rootOf(v);
  const secs = ext.dock(v) || [], sec = secs.find((x) => x.key === st.tab);
  const tabs = secs.length ? html`<div class="hbtabs" role="tablist">${[{ key: 'agents', title: 'Coding agents' }, ...secs].map((x) => html`<button class="hbtab" role="tab"
    data-tab=${x.key} aria-selected=${(sec ? sec.key : 'agents') === x.key ? 'true' : 'false'} @click=${() => { st.tab = x.key; ctx.paint(); }}>${x.title}${x.badge ? html` <span class="hbbadge">${x.badge}</span>` : nothing}</button>`)}</div>` : html`<b>Coding agents</b>`;
  const close = html`<button class="btn ghost btnsm icon" data-act="close" title="close the dock" aria-label="close the dock" @click=${() => { st.open = false; ctx.paint(); }}><bx-icon name="xmark"></bx-icon></button>`;
  if (sec) return html`<div class="hbhd">${tabs}<span class="hbsp"></span>${close}</div><div class="hbbody hbsec" data-sec=${sec.key}>${sec.tpl()}</div>`;
  const all = app.board.rows(root);
  const f = filterWords(all, st.needs);
  const rows = st.needs ? all.filter((r) => r.section === 'needs') : all;
  for (const r of rows) if (r.card.park && !r.card.park.harness) loadTail(app.session, r.id); // its park in full, to answer here
  const title = (id) => (app.convs.find(id) || {}).title || '#' + id;
  const scope = root == null ? 'yours — running or waiting for you' : `in ${title(root)}`;
  return html`<div class="hbhd">
      ${tabs}<span class="muted hbscope" title=${scope}>${secs.length ? '' : scope}</span>
      ${f ? html`<button class="badge hbfilter" data-on=${f.on ? '1' : ''} aria-pressed=${f.on ? 'true' : 'false'} title=${f.title}
        @click=${() => { st.needs = !st.needs; ctx.paint(); }}>${f.text}</button>` : nothing}
      ${close}
    </div>
    <div class="hbbody">
      ${rows.length ? repeat(rows, (r) => r.id, (r) => html`<div class="hbrow" data-row=${r.id} data-section=${r.section}>
          ${root == null && r.root !== r.id ? html`<div class="hbin muted" @click=${() => app.select(r.root)}>in ${title(r.root)}</div>` : nothing}
          ${cardTpl(r.b, app.session.ui, 0, { run: r.run, view: r.view })}
        </div>`)
      : html`<div class="muted hbempty">${app.board.loading(root) ? 'loading…' : emptyWords(root == null, st.needs)}</div>`}
    </div>`;
}

// --- the pinned task's Delegated section -------------------------------------------------

function delegatedTpl(v) {
  const app = ctx.app;
  const rows = app && v ? app.board.delegated(v) : [];
  if (!rows.length) return null;
  return html`<div class="taskdel">
    <div class="askhead">Delegated — the coding agents it started, each with its task</div>
    ${rows.map((r) => {
      const d = delegatedWords(r);
      return html`<div class="deleg" data-child=${d.id}>
        <span class="kind" title=${d.name}>${d.mono}</span>
        <a class="lnk" title="its own chat" @click=${() => app.select(d.id)}>#${d.id} ${d.title}</a>
        <span class="hkst" data-tone=${d.state.tone}>${d.state.word}</span>
        ${d.task && d.task !== d.title ? html`<div class="deltask">${d.task}</div>` : nothing}
      </div>`;
    })}
  </div>`;
}

// the chip's, the dock's and the Delegated section's look (the tile's own sheet stays as it is):
// theme.css's tokens (D184); a chip that needs you says so in its colour and words, still
const style = document.createElement('style');
style.textContent = `
  .badge.hbchip { cursor: pointer; text-transform: none; letter-spacing: 0; color: var(--bx-text); border-color: var(--bx-border-strong); }
  .badge.hbchip[data-tone="warn"] { color: var(--bx-warn); border-color: var(--bx-warn); }
  .badge.hbchip[aria-pressed="true"] { background: var(--bx-selection); color: var(--bx-selection-text); }
  .wrap.dockon { grid-template-columns: 220px minmax(0, 1fr) 340px; }
  .hboard { display: flex; flex-direction: column; min-width: 0; min-height: 0; border-left: 1px solid var(--bx-border); background: var(--bx-panel); }
  @media (max-width: 1099px) {
    .wrap.dockon { grid-template-columns: 220px minmax(0, 1fr); }
    .hboard { position: fixed; top: 0; right: 0; bottom: 0; z-index: 35; width: min(340px, 100vw); box-sizing: border-box;
      box-shadow: var(--bx-shadow-pop); }
  }
  .hbhd { flex: none; display: flex; align-items: center; gap: 8px; padding: 4px 8px 4px 12px; min-height: var(--bx-topbar-h); box-sizing: border-box;
    border-bottom: 1px solid var(--bx-border); min-width: 0; }
  .hbhd .hbscope { flex: 1; min-width: 0; font: var(--bx-font-meta); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bx .badge.hbfilter { cursor: pointer; text-transform: none; letter-spacing: 0; background: none; min-height: 0;
    color: var(--bx-warn); border-color: var(--bx-warn); }
  .bx .badge.hbfilter[data-on="1"] { background: var(--bx-warn-bg); }
  .hbbody { flex: 1; min-height: 0; overflow: auto; padding: 4px 8px 12px; }
  .hbbody .hbrow { min-width: 0; }
  .hbbody .hbin { font: var(--bx-font-meta); margin: 8px 2px -2px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hbbody .hkid .hkline, .hbbody .hkid .hkmeta, .hbbody .hkid .hkpark, .hbbody .hkid .hkmsg { padding-left: 12px; }
  .hbbody .hkid .hkact { padding-left: 10px; }
  .hbbody .hkid .hkn { display: none; } /* the monogram says who (its title the name): the title gets the room */
  .hbempty { padding: 16px 8px; color: var(--bx-muted); }
  .hbtabs { display: flex; gap: 4px; min-width: 0; } .hbhd .hbsp { flex: 1; }
  .hbtab { font: inherit; background: none; border: 0; border-bottom: 2px solid transparent; padding: 2px 8px; cursor: pointer; color: var(--bx-muted); white-space: nowrap; }
  .hbtab[aria-selected="true"] { color: var(--bx-text); font-weight: 600; border-bottom-color: var(--bx-accent); } .hbtab .hbbadge { font-weight: 400; }
  .top .taskpin .taskdel { display: flex; flex-direction: column; gap: 4px; padding-top: 8px; border-top: 1px dashed var(--bx-border); }
  .taskdel .deleg { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; min-width: 0; }
  .taskdel .deleg .lnk { cursor: pointer; overflow-wrap: anywhere; }
  .taskdel .deleg .hkst { font: var(--bx-font-meta); }
  .taskdel .deleg .hkst[data-tone="run"] { color: var(--bx-text); } .taskdel .deleg .hkst[data-tone="warn"] { color: var(--bx-warn); }
  .taskdel .deleg .hkst[data-tone="bad"] { color: var(--bx-danger); } .taskdel .deleg .hkst[data-tone="ok"] { color: var(--bx-ok); }
  .taskdel .deltask { flex-basis: 100%; padding-left: 26px; color: var(--bx-muted); white-space: pre-wrap; overflow-wrap: anywhere; }
`;
document.head.append(style);
