// sidebar.js — how the conversation list draws (lit, keyed by id): Pinned,
// then date groups (conv-groups.js), newest activity first; search results
// with the matching line; a row menu (right-click or ⋯) to rename, pin,
// share, archive, delete or leave; "more" at the bottom as you scroll.
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { groupRows } from './conv-groups.js';

const ACTIVE = new Set(['running', 'awaiting', 'sleeping', 'queued', 'blocked']);

/**
 * @param list  ConvList (conv-list.js)
 * @param ui    {sel, renaming, menu: {id,x,y}|null, select(id), openMenu(id, ev),
 *               closeMenu(), act(action, row), rename(id, title), cancelRename(), more(),
 *               view(scope, archived)}
 */
export function sidebarTpl(list, ui) {
  const results = list.results;
  const body = results
    ? (results.length ? rowsTpl(results, ui, true) : html`<div class="empty">nothing found</div>`)
    : html`
      ${list.pinned.length ? html`<div class="grp">Pinned</div>${rowsTpl(list.pinned, ui)}` : nothing}
      ${groupRows(list.items).map((g) => html`<div class="grp">${g.label}</div>${rowsTpl(g.rows, ui)}`)}
      ${!list.pinned.length && !list.items.length && !list.loading
        ? html`<div class="empty">${list.archived ? 'nothing archived' : list.scope === 'team' ? 'nothing shared with the team yet' : 'no conversations yet — ask below'}</div>` : nothing}
      ${list.next ? html`<button class="more btn ghost btnsm" @click=${() => ui.more()}>${list.loading ? 'loading…' : 'more'}</button>` : nothing}`;
  return html`${body}${menuTpl(list, ui)}`;
}

function rowsTpl(rows, ui, withMatch = false) {
  return repeat(rows, (r) => r.id, (r) => rowTpl(r, ui, withMatch));
}

function rowTpl(r, ui, withMatch) {
  const glyph = r.status === 'waiting_input' ? html`<span class="gl ask" title="waiting for you">?</span>`
    : r.status === 'error' ? html`<span class="gl err" title="failed">!</span>`
    : ACTIVE.has(r.status) ? html`<span class="spin"></span>` : nothing;
  const shared = r.visibility === 'team' || (r.members || 0) > 0;
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
    <div class="t">${r.title || 'run ' + r.id}</div>
    ${shared ? html`<span class="gl" title=${r.mine ? 'shared' : `shared by ${r.owner || 'the team'}`}>⇆</span>` : nothing}${glyph}
    <button class="rmenu" title="more" @click=${(e) => { e.stopPropagation(); ui.openMenu(r.id, e); }}>⋯</button>
    ${withMatch && r.match ? html`<div class="snip">${r.match.snippet}</div>` : nothing}
  </div>`;
}

function menuTpl(list, ui) {
  const m = ui.menu;
  if (!m) return nothing;
  const r = list.find(m.id) || (list.results || []).find((x) => x.id === m.id);
  if (!r) return nothing;
  const own = r.access === 'owner' || r.access === 'system';
  const item = (label, action, cls = '') => html`<div class="mi ${cls}" @click=${() => { ui.closeMenu(); ui.act(action, r); }}>${label}</div>`;
  return html`<div class="mback" @click=${() => ui.closeMenu()} @contextmenu=${(e) => { e.preventDefault(); ui.closeMenu(); }}></div>
    <div class="rowmenu" style="left:${m.x}px;top:${m.y}px">
      ${own ? item('Rename', 'rename') : nothing}
      ${item(r.pinnedAt ? 'Unpin' : 'Pin', 'pin')}
      ${own ? item('Share…', 'share') : nothing}
      ${item(r.archivedAt ? 'Unarchive' : 'Archive', 'archive')}
      ${own ? item('Delete', 'delete', 'rm') : r.mine ? item('Leave', 'leave', 'rm') : nothing}
    </div>`;
}

// footTpl is the list's footer: switch between your conversations, the ones
// shared with the team, and your archive.
export function footTpl(list, ui) {
  const at = (scope, archived) => list.scope === scope && list.archived === archived && !list.results;
  const link = (label, scope, archived) => html`<a class=${at(scope, archived) ? 'on' : ''} @click=${() => ui.view(scope, archived)}>${label}</a>`;
  return html`${link('Mine', 'mine', false)} · ${link('Shared with team', 'team', false)} · ${link('Archived', 'mine', true)}`;
}

// makeSideUI is the list's behaviour: selection, the row menu, inline rename
// and the row actions. d: {convs, api, selectRun, goHome, paint, current,
// search() → the search input}.
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
      const r = d.convs.find(id);
      if (title.trim() && (!r || title.trim() !== r.title)) await d.convs.patch(id, { title: title.trim() }).catch((e) => alert(e.message));
      d.paint();
    },
    more: () => d.convs.more(),
    view: (scope, archived) => { d.search().value = ''; d.convs.view(scope, archived); },
    act: async (action, r) => {
      try {
        if (action === 'rename') { renaming = r.id; d.paint(); document.querySelector('#runs .ren')?.focus(); return; }
        if (action === 'pin') await d.convs.patch(r.id, { pinned: !r.pinnedAt });
        if (action === 'archive') await d.convs.patch(r.id, { archived: !r.archivedAt });
        if (action === 'share') await d.convs.patch(r.id, r.visibility === 'team' ? { visibility: 'private' } : { visibility: 'team', teamRole: 'viewer' });
        if (action === 'delete' && confirm(`Delete "${r.title}" and its history?`)) {
          await d.api(`/runs/${r.id}`, { method: 'DELETE' });
          if (ui.sel === r.id) d.goHome();
        }
      } catch (e) { alert(e.message); }
    },
  };
  return ui;
}

