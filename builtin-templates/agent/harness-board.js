// harness-board.js — the Coding agents board on the web (D-harness §4.3.6,
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

const st = { open: false, needs: false }; // the dock is open; only the ones that need you
let dock = null;

const rootOf = (v) => (v ? v.run.rootId || v.run.id : null);
const toggle = () => { st.open = !st.open; ctx.paint(); };
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
  return html`<span class="badge hbchip" id="hbchip" role="button" tabindex="0" data-tone=${c.tone} aria-pressed=${st.open ? 'true' : 'false'}
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
  const all = app.board.rows(root);
  const f = filterWords(all, st.needs);
  const rows = st.needs ? all.filter((r) => r.section === 'needs') : all;
  for (const r of rows) if (r.card.park && !r.card.park.harness) loadTail(app.session, r.id); // its park in full, to answer here
  const title = (id) => (app.convs.find(id) || {}).title || '#' + id;
  const scope = root == null ? 'yours — running or waiting for you' : `in ${title(root)}`;
  return html`<div class="hbhd">
      <b>Coding agents</b><span class="muted hbscope" title=${scope}>${scope}</span>
      ${f ? html`<button class="badge hbfilter" data-on=${f.on ? '1' : ''} aria-pressed=${f.on ? 'true' : 'false'} title=${f.title}
        @click=${() => { st.needs = !st.needs; ctx.paint(); }}>${f.text}</button>` : nothing}
      <button class="btn ghost btnsm" data-act="close" title="close the board" @click=${toggle}>✕</button>
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

// the chip's, the dock's and the Delegated section's look (the tile's own sheet stays as it is)
const style = document.createElement('style');
style.textContent = `
  .badge.hbchip { cursor: pointer; text-transform: none; letter-spacing: 0; color: var(--bx-accent);
    border-color: color-mix(in srgb, var(--bx-accent) 50%, var(--bx-border)); }
  .badge.hbchip[data-tone="warn"] { color: var(--bx-yellow, #d9a441); border-color: color-mix(in srgb, var(--bx-yellow, #d9a441) 55%, var(--bx-border));
    animation: hbpulse 2.4s ease-in-out 3; }
  .badge.hbchip[aria-pressed="true"] { background: var(--bx-panel-2); }
  @keyframes hbpulse { 50% { box-shadow: 0 0 0 3px color-mix(in srgb, var(--bx-yellow, #d9a441) 30%, transparent); } }
  .wrap.dockon { grid-template-columns: 220px minmax(0, 1fr) 340px; }
  .hboard { display: flex; flex-direction: column; min-width: 0; min-height: 0; border-left: 1px solid var(--bx-border); background: var(--bx-panel); }
  .hboard[hidden] { display: none; }
  @media (max-width: 1099px) {
    .wrap.dockon { grid-template-columns: 220px minmax(0, 1fr); }
    .hboard { position: fixed; top: 0; right: 0; bottom: 0; z-index: 35; width: min(340px, 100vw); box-sizing: border-box;
      box-shadow: -8px 0 28px rgba(0,0,0,.35); }
  }
  .hbhd { flex: none; display: flex; align-items: center; gap: 6px; padding: 7px 8px 7px 12px; border-bottom: 1px solid var(--bx-border); min-width: 0; }
  .hbhd .hbscope { flex: 1; min-width: 0; font-size: 11.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .badge.hbfilter { cursor: pointer; text-transform: none; letter-spacing: 0; background: none; font: inherit; font-size: 11px;
    color: var(--bx-yellow, #d9a441); border-color: color-mix(in srgb, var(--bx-yellow, #d9a441) 55%, var(--bx-border)); }
  .badge.hbfilter[data-on="1"] { background: color-mix(in srgb, var(--bx-yellow, #d9a441) 18%, transparent); }
  .hbbody { flex: 1; min-height: 0; overflow: auto; padding: 4px 8px 10px; }
  .hbbody .hbrow { min-width: 0; }
  .hbbody .hbin { font-size: 11px; margin: 8px 2px -2px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hbbody .hkid .hkline, .hbbody .hkid .hkmeta, .hbbody .hkid .hkpark, .hbbody .hkid .hkmsg { padding-left: 12px; }
  .hbbody .hkid .hkact { padding-left: 10px; }
  .hbbody .hkid .hkn { display: none; } /* the monogram says who (its title the name): the title gets the room */
  .hbempty { padding: 16px 6px; font-size: 12px; }
  .top .taskpin .taskdel { display: flex; flex-direction: column; gap: 4px; padding-top: 6px; border-top: 1px dashed var(--bx-border); }
  .taskdel .deleg { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 6px; min-width: 0; }
  .taskdel .deleg .lnk { cursor: pointer; overflow-wrap: anywhere; }
  .taskdel .deleg .hkst { font-size: 11px; }
  .taskdel .deleg .hkst[data-tone="run"] { color: var(--bx-accent); } .taskdel .deleg .hkst[data-tone="warn"] { color: var(--bx-yellow, #d9a441); }
  .taskdel .deleg .hkst[data-tone="bad"] { color: var(--bx-red); } .taskdel .deleg .hkst[data-tone="ok"] { color: var(--bx-green); }
  .taskdel .deltask { flex-basis: 100%; padding-left: 26px; color: var(--bx-muted); white-space: pre-wrap; overflow-wrap: anywhere; }
`;
document.head.append(style);
