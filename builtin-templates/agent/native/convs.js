// native/convs.js — the conversations drawer (a sheet from the leading
// edge, over the chat: one surface at a time on every device) and the
// small sheets it opens: new chat with options, rename. The list is the
// model's ConvList (model/conv-list.js: paging, search, kept current by the
// stream); its groups are model/conv-groups.js; which glyphs and actions a
// row gets is model/rules.js — the web's sidebar.js draws the same.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ui, ctx, guard } from './ui.js';
import { groupRows } from '../model/conv-groups.js';
import { rowGlyph, rowShared, rowMenu } from '../model/rules.js';
import { sharing } from '../model/partition.js';
import { summaryCount } from '../model/auto.js';
import { mainMenu } from './home.js';
import { classSectionTpl } from './classes.js';
import { kindOf } from '../model/harness-start.js';
import { kidsWords } from '../model/harness-child.js';

const GLYPH = { ask: ['waiting for you', 'accent'], error: ['failed', 'danger'], spin: ['working', 'muted'] };
// the Shared view — in a person's partition, the shared space's (model/partition.js, model/homes.js)
const SCOPES = [{ value: 'mine', label: 'Mine' }, { value: 'shared', label: 'Shared' }, { value: 'archived', label: 'Archived' }]
  .filter((s) => s.value !== 'shared' || sharing());
const EMPTY_SHARED = 'nothing shared yet — share a conversation from its menu, and whatever others share with you shows here too';

const close = () => { ui.drawer = false; ctx.paint(); };

export function drawerSheet() {
  if (!ui.drawer) return nothing;
  const app = ctx.app;
  const list = app.convs;
  const results = list.results;
  const scope = list.archived ? 'archived' : list.scope;
  const shared = scope === 'shared';
  const byMe = (r) => (rowShared(r) || {}).byMe;
  const s = app.autos.summary || {};
  const n = summaryCount(s);
  return html`<sheet open edge="leading" title="Conversations" @dismiss=${close}>
    <screen title="Conversations" style="scroll" search=${ui.q} @search=${search}>
      <toolbar><menu icon="ellipsis" label="More">${mainMenu(close)}</menu></toolbar>
      <list style="inset" @more=${list.next && !results ? () => list.more().catch(() => {}) : nothing}>
        <section>
          <row title="New chat" icon="plus" @tap=${() => { app.home(); close(); }}/>
          <row title="New chat with options…" icon="pencil" @tap=${() => { ui.newChat = { text: '', title: '', system: '', class: app.classId }; close(); }}/>
          <row title="Automations" icon="clock" badge=${n ? String(n) : nothing} tone=${s.failing ? 'danger' : n ? 'accent' : nothing}
            nav @tap=${() => { app.openAutomations(); close(); }}/>
          ${ctx.ext.drawer(close) || nothing}
        </section>
        ${results ? nothing : html`<section><picker style="segmented" value=${scope} options=${SCOPES}
          @change=${(e) => { ui.q = ''; list.view(e.value === 'shared' ? 'shared' : 'mine', e.value === 'archived'); }}/></section>`}
        ${results
          ? (results.length ? html`<section title="Results">${rowsTpl(results, true)}</section>` : html`<empty icon="search" title="nothing found"/>`)
          : html`
            ${list.pinned.length ? html`<section title="Pinned">${rowsTpl(list.pinned)}</section>` : nothing}
            ${shared
              ? [['Shared by you', list.items.filter(byMe)], ['Shared with you', list.items.filter((r) => !byMe(r))]]
                .filter(([, rows]) => rows.length).map(([label, rows]) => html`<section title=${label}>${rowsTpl(rows)}</section>`)
              : repeat(groupRows(list.items), (g) => g.label, (g) => html`<section title=${g.label}>${rowsTpl(g.rows)}</section>`)}
            ${!list.pinned.length && !list.items.length && !list.loading ? html`<empty icon="chat"
              title=${list.archived ? 'nothing archived' : shared ? EMPTY_SHARED : 'no conversations yet — ask below'}/>` : nothing}
            ${list.next && list.loading ? html`<progress label="loading…"/>` : nothing}`}
      </list>
    </screen>
  </sheet>`;
}

// search: the list's ?q= (debounced); a pasted invite link joins instead.
let searchT = null;
function search(e) {
  const app = ctx.app;
  ui.q = e.value || '';
  clearTimeout(searchT);
  if (ui.q.includes('#join=')) {
    const text = ui.q;
    ui.q = '';
    close();
    app.join(text);
    return;
  }
  searchT = setTimeout(() => app.convs.search(ui.q).catch(() => {}), 200);
}

function rowsTpl(rows, withMatch = false) {
  return repeat(rows, (r) => r.id, (r) => rowTpl(r, withMatch));
}

// A row: its glyph (? waiting, ! failed, working) as the badge, unread as the
// accent dot, how it is shared (the chips of rowShared) and a search hit's
// line under the title; its menu as swipe actions and a context menu.
function rowTpl(r, withMatch) {
  const app = ctx.app;
  const g = GLYPH[rowGlyph(r)];
  const shared = rowShared(r);
  const kind = kindOf(r); // a coding agent answers it (D147): its name first
  const kids = kidsWords(r); // coding agents at work below it (D147 §4.3.8)
  const sub = [kind ? kind.name : '', kids ? kids.label : '',
    ...(withMatch && r.match ? [r.match.snippet] : [r.hosted ? 'not private' : '', // a non-secure conversation (native/hosted.js)
      shared ? shared.chips.map((c) => c.label).join(' · ') : ''])].filter(Boolean).join(' · ');
  const sel = app.root === r.id;
  return html`<row title=${r.title || 'run ' + r.id} subtitle=${sub || nothing}
      badge=${g ? g[0] : nothing} tone=${g ? g[1] : r.unread ? 'accent' : nothing} ?selected=${sel}
      @tap=${() => { close(); app.select(r.id); }}>
    <actions>${repeat(rowMenu(r), (i) => i.action, (i) => itemTpl(i, r))}</actions>
  </row>`;
}

function itemTpl(i, r) {
  const app = ctx.app;
  const title = r.title || 'run ' + r.id;
  switch (i.action) {
    case 'rename': return html`<button icon="pencil" @tap=${() => { ui.rename = { id: r.id, title: r.title || '' }; close(); }}>${i.label}</button>`;
    case 'pin': return html`<button icon="pin" @tap=${guard(() => app.actions.pin(app.convs, r))}>${i.label}</button>`;
    case 'share': return html`<button icon="people" @tap=${() => { ui.share = { run: { id: r.id, title: r.title, engine: r.engine, parentId: r.parentId } }; close(); }}>${i.label}</button>`;
    case 'archive': return html`<button icon="archive" @tap=${guard(() => app.actions.archive(app.convs, r))}>${i.label}</button>`;
    case 'delete': return html`<button icon="trash" role="destructive" confirm=${{ title: `Delete "${title}" and its history?`, label: 'Delete', destructive: true }}
      @tap=${guard(async () => { await app.actions.deleteRun(r.id); app.convs.remove(r.id); if (app.root === r.id) app.home(); })}>${i.label}</button>`;
    case 'leave': return html`<button icon="xmark" role="destructive" confirm=${{ title: `Leave "${title}"?`, label: 'Leave', destructive: true }}
      @tap=${guard(async () => { await app.actions.leave(app.convs, r, app.me.user); if (app.root === r.id) app.home(); })}>${i.label}</button>`;
  }
  return nothing;
}

// --- new chat with options ---------------------------------------------------------

export function newChatSheet() {
  const f = ui.newChat;
  if (!f) return nothing;
  const app = ctx.app;
  const done = () => { ui.newChat = null; ctx.paint(); };
  const set = (k) => (e) => { f[k] = e.value; };
  const more = ctx.ext.newChat(f) || []; // the seams' sections (native/ext.js)
  // a coding agent answering it (f.agent, native/harness-start.js) keeps its own instructions
  const coding = !!(f.agent && f.agent !== 'agent' && app.harness.find(f.agent)?.available);
  const start = guard(async () => {
    const text = f.text.trim();
    if (!text) return;
    await app.ask(Object.assign({ text, title: f.title.trim(), system: f.system.trim(), class: f.class }, ...more.map((x) => (x.body ? x.body() : {}))));
    ui.newChat = null;
  });
  return html`<sheet open title="New chat" @dismiss=${done}>
    <screen title="New chat" style="form">
      <toolbar><button role="primary" @tap=${start}>Start</button></toolbar>
      ${ui.err ? html`<section><notice tone="danger" text=${ui.err}/></section>` : nothing}
      <section title="First message">
        <field kind="multiline" placeholder="what should it do?" value=${f.text} @input=${set('text')}/>
      </section>
      ${classSectionTpl(f)}
      ${more.map((x) => (x.tpl ? x.tpl() : nothing))}
      <section title="Optional">
        <field label="Title" placeholder="from the first message" value=${f.title} @input=${set('title')}/>
        ${coding ? nothing : html`<field label="Instructions" kind="multiline" placeholder="extra system instructions" value=${f.system} @input=${set('system')}/>`}
      </section>
    </screen>
  </sheet>`;
}

// --- rename ------------------------------------------------------------------------------

export function renameSheet() {
  const f = ui.rename;
  if (!f) return nothing;
  const app = ctx.app;
  const done = () => { ui.rename = null; ctx.paint(); };
  const save = guard(async () => {
    await app.actions.rename(app.convs, f.id, f.title);
    // the open conversation's title follows at once (the stream says so too)
    const title = f.title.trim();
    const v = app.session.views.get(f.id);
    if (v && title) v.run = { ...v.run, title };
    if (title && app.session.runs.has(f.id)) app.session.runs.set(f.id, { ...app.session.runs.get(f.id), title });
    ui.rename = null;
  });
  return html`<sheet open title="Rename" detents="medium" @dismiss=${done}>
    <screen title="Rename" style="form">
      <toolbar><button role="primary" @tap=${save}>Save</button></toolbar>
      <section><field label="Title" value=${f.title} submit="done" @input=${(e) => { f.title = e.value; }} @submit=${save}/></section>
    </screen>
  </sheet>`;
}
