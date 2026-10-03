// sidebar.js — how the conversation list draws (lit, keyed by id): the views
// switch (Mine · Shared · Archived), Pinned, then date groups
// (model/conv-groups.js), newest activity first — or, in Shared, what you
// shared and what was shared with you; a shared row says how (chips); search
// results with the matching line; a row menu (right-click or ⋯) to rename,
// pin, share, archive, delete or leave; "more" at the bottom as you scroll.
// The list's state is model/conv-list.js; which glyphs and menu items a row
// gets is model/rules.js; the row actions are model/actions.js.
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { groupRows } from './model/conv-groups.js';
import { rowGlyph, rowShared, rowMenu } from './model/rules.js';
import { sharing } from './model/partition.js'; // the Shared view (in a person's partition: the shared space's)
import * as actions from './model/actions.js';
import { hostedRowChip } from './hosted-ui.js'; // a non-secure conversation's warning chip
import { kindOf } from './model/harness-start.js';
import { kidsWords } from './model/harness-child.js';

/**
 * @param list  ConvList (conv-list.js)
 * @param ui    {sel, renaming, menu: {id,x,y}|null, select(id), openMenu(id, ev),
 *               closeMenu(), act(action, row), rename(id, title), cancelRename(), more(),
 *               view(scope, archived)}
 */
export function sidebarTpl(list, ui) {
  const results = list.results;
  const shared = list.scope === 'shared' && !list.archived;
  const byMe = (r) => (rowShared(r) || {}).byMe;
  const body = results
    ? (results.length ? rowsTpl(results, ui, true) : html`<div class="empty">nothing found</div>`)
    : html`
      ${list.pinned.length ? html`<div class="grp">Pinned</div>${rowsTpl(list.pinned, ui)}` : nothing}
      ${shared
        ? [['Shared by you', list.items.filter(byMe)], ['Shared with you', list.items.filter((r) => !byMe(r))]]
          .filter(([, rows]) => rows.length).map(([label, rows]) => html`<div class="grp">${label}</div>${rowsTpl(rows, ui)}`)
        : groupRows(list.items).map((g) => html`<div class="grp">${g.label}</div>${rowsTpl(g.rows, ui)}`)}
      ${!list.pinned.length && !list.items.length && !list.loading
        ? html`<div class="empty">${list.archived ? 'nothing archived' : shared ? emptyShared : 'no conversations yet — ask below'}</div>` : nothing}
      ${list.next ? html`<button class="more btn ghost btnsm" @click=${() => ui.more()}>${list.loading ? 'loading…' : 'more'}</button>` : nothing}`;
  return html`${body}${menuTpl(list, ui)}`;
}

const emptyShared = 'nothing shared yet — share a conversation from its row menu or its top bar, and whatever others share with you shows here too';

function rowsTpl(rows, ui, withMatch = false) {
  return repeat(rows, (r) => r.id, (r) => rowTpl(r, ui, withMatch));
}

function rowTpl(r, ui, withMatch) {
  const g = rowGlyph(r);
  // the row's state (D184): a glyph with its word in the title, the live square while it works
  const glyph = g === 'ask' ? html`<span class="gl ask" title="waiting for you"><bx-icon name="question" label="waiting for you"></bx-icon></span>`
    : g === 'error' ? html`<span class="gl err" title="failed"><bx-icon name="error" label="failed"></bx-icon></span>`
    : g === 'spin' ? html`<span class="spin" title="working"></span>` : nothing;
  const shared = rowShared(r);
  const kind = kindOf(r); // a coding agent answers it (D147): its monogram
  const kids = kidsWords(r); // coding agents at work below it (D147 §4.3.8)
  if (ui.renaming === r.id) {
    return html`<div class="run on" data-id=${r.id}>
      <input class="ren" .value=${r.title || ''} @keydown=${(e) => {
        if (e.key === 'Enter') ui.rename(r.id, e.target.value);
        if (e.key === 'Escape') ui.cancelRename();
      }} @blur=${(e) => ui.rename(r.id, e.target.value)}>
    </div>`;
  }
  return html`<div class="run ${r.id === ui.sel ? 'on' : ''} ${r.unread ? 'unread' : ''}" data-id=${r.id}
      @click=${() => ui.select(r.id)} @contextmenu=${(e) => { e.preventDefault(); ui.openMenu(r.id, e); }}>
    ${kind ? html`<span class="kind" data-kind=${kind.provider} title=${kind.title}>${kind.mono}</span>` : nothing}
    <div class="t">${r.title || 'run ' + r.id}</div>
    ${hostedRowChip(r)}
    ${kids ? html`<span class="kids" title=${kids.title}>${kids.text}</span>` : nothing}
    ${glyph}
    <button class="rmenu" title="more" aria-label="more" @click=${(e) => { e.stopPropagation(); ui.openMenu(r.id, e); }}><bx-icon name="ellipsis"></bx-icon></button>
    ${shared ? html`<div class="chips" title=${shared.title}>${shared.chips.map((c) => html`<span class="chip ${c.kind}">${c.label}</span>`)}</div>` : nothing}
    ${withMatch && r.match ? html`<div class="snip">${r.match.snippet}</div>` : nothing}
  </div>`;
}

function menuTpl(list, ui) {
  const m = ui.menu;
  if (!m) return nothing;
  const r = list.find(m.id) || (list.results || []).find((x) => x.id === m.id);
  if (!r) return nothing;
  const item = (label, action, cls = '') => html`<div class="mi ${cls}" @click=${() => { ui.closeMenu(); ui.act(action, r); }}>${label}</div>`;
  return html`<div class="mback" @click=${() => ui.closeMenu()} @contextmenu=${(e) => { e.preventDefault(); ui.closeMenu(); }}></div>
    <div class="rowmenu" style="left:${m.x}px;top:${m.y}px">
      ${rowMenu(r, { publish: true }).map((i) => item(i.label, i.action, i.cls))}
    </div>`;
}

// viewsTpl is the list's views switch, above it: your conversations, the
// shared ones (both ways), your archive — the one you're in stays marked.
export function viewsTpl(list, ui) {
  const at = (scope, archived) => list.scope === scope && list.archived === archived && !list.results;
  const seg = (label, scope, archived, title) => html`<button class=${'seg' + (at(scope, archived) ? ' on' : '')} title=${title}
      aria-pressed=${at(scope, archived) ? 'true' : 'false'} @click=${() => ui.view(scope, archived)}>${label}</button>`;
  return html`${seg('Mine', 'mine', false, 'your conversations')}${sharing() ? seg('Shared', 'shared', false, 'what you shared, and what others shared with you') : nothing}${seg('Archived', 'mine', true, 'your archive')}`;
}

// makeSideUI is the list's behaviour: selection, the row menu, inline rename
// and the row actions (confirmed here, done by model/actions.js). d: {convs,
// api, selectRun, goHome, paint, current, search() → the search input,
// share(row), me() → GET /me}.
export function makeSideUI(d) {
  let menu = null, renaming = null;
  const ui = {
    get sel() { const c = d.current(); return c ? (c.run.rootId || c.run.id) : null; },
    get menu() { return menu; },
    get renaming() { return renaming; },
    select: (id) => d.selectRun(id),
    openMenu: (id, e) => { menu = { id, x: Math.min(e.clientX, innerWidth - 150), y: Math.min(e.clientY, innerHeight - 150) }; d.paint(); },
    closeMenu: () => { menu = null; d.paint(); },
    cancelRename: () => { renaming = null; d.paint(); },
    rename: async (id, title) => {
      if (renaming !== id) return;
      renaming = null;
      await actions.rename(d.convs, id, title).catch((e) => alert(e.message));
      d.paint();
    },
    more: () => d.convs.more(),
    view: (scope, archived) => { d.search().value = ''; d.convs.view(scope, archived); },
    act: async (action, r) => {
      try {
        if (action === 'rename') { renaming = r.id; d.paint(); document.querySelector('#runs .ren')?.focus(); return; }
        if (action === 'pin') await actions.pin(d.convs, r);
        if (action === 'archive') await actions.archive(d.convs, r);
        if (action === 'share') d.share(r);
        if (action === 'leave' && confirm(`Leave "${r.title}"?`)) {
          await actions.leave(d.convs, r, d.me().user);
          if (ui.sel === r.id) d.goHome();
        }
        if (action === 'delete' && confirm(`Delete "${r.title}" and its history?`)) {
          await actions.deleteRun(r.id);
          if (ui.sel === r.id) d.goHome();
        }
      } catch (e) { alert(e.message); }
    },
  };
  return ui;
}

