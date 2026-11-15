// native/ws-panels.js — what a workspace agent's native view shows beside
// its conversation (D233), each drawn with the vocabulary's own lists and
// sheets over the store the web page shares (ws/store.js): Sessions; the
// scope panel — totals, the orgs unfolding to tiles with their four states
// and the org's reason, ticks staged until Apply, a model's what-if before
// anything changes, a change the conversation's data refuses; "What can it
// do?"; the notebook; one tool call in full. The words are ws/words.js and
// ws/scope-model.js.
//
// Scope is focus, not permission: a blocked tile can't be ticked, and the
// panel says so. Nothing here grants, pauses or resumes.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ui, ctx, fmtN } from './ui.js';
import { has, compact, ico, tone, starter } from './ws-chat.js';
import { W, short, sessionRow, day, pageTag, pageClosed } from '../ws/words.js';
import { STATES, tree, staged } from '../ws/scope-model.js';

let st = null;
export function bind(store) { st = store; }

const close = () => { st.S.pop = ''; st.S.ses.renaming = null; ctx.paint(); };
// a sheet's two buttons where the thumb is (toolbar rev 2: the bottom bar), else in its bar
const barTpl = (cancel, primary) => (has('toolbar', 2)
  ? html`<toolbar place="bottom">${cancel}${primary}</toolbar>`
  : html`<toolbar>${cancel}${primary}</toolbar>`);

// ---- sessions -----------------------------------------------------------------------------------

/** sessionsSheet(): Main first and always there, the others with their scope, model and status; New session…, Rename, Archive; archived ones under Older. */
export function sessionsSheet() {
  const S = st.S;
  if (S.pop !== 'sessions') return nothing;
  const now = Date.now();
  const live = S.sessions.filter((x) => !x.archivedAt || x.id === S.sel);
  const old = S.sessions.filter((x) => x.archivedAt && x.id !== S.sel);
  const act = st.sessions;
  const bag = st.bag('rename');
  const row = (x) => {
    const w = sessionRow(x, now);
    if (S.ses.renaming === x.id) {
      return html`<field label="Name" value=${bag.name ?? w.name} submit="done" @input=${(e) => { bag.name = e.value; }} @submit=${() => act.rename(x.id, bag.name ?? w.name)}/>
        <button role="primary" @tap=${() => act.rename(x.id, bag.name ?? w.name)}>Save</button>
        <button role="plain" @tap=${() => act.renaming(null)}>Cancel</button>`;
    }
    // (the status leads the second line — a row's trailing `detail` is squeezed to a letter a line beside a long scope
    // on a phone; Main's "always here" is its badge)
    return html`<row title=${w.name} subtitle=${[w.status.word, w.scope].filter(Boolean).join(' · ')} badge=${w.main ? 'always here' : nothing}
        icon=${w.status.glyph ? ico(w.status.glyph) : nothing} tone=${tone(w.status.tone)} ?selected=${x.id === S.sel} @tap=${() => act.pick(x.id)}>
      <actions>
        <button icon="pencil" @tap=${() => { bag.name = undefined; act.renaming(x.id); }}>Rename</button>
        ${x.main ? nothing : x.archivedAt ? html`<button icon="archive" @tap=${() => act.archive(x.id, false)}>Restore</button>`
          : html`<button icon="archive" role="destructive" @tap=${() => act.archive(x.id, true)}>Archive</button>`}
      </actions>
    </row>`;
  };
  return html`<sheet open title="Sessions" detents=${['medium', 'large']} @dismiss=${close}>
    <screen title="Sessions" style="list">
      <toolbar><button icon="plus" @tap=${() => act.create()}>New session…</button></toolbar>
      ${S.ses.err ? html`<notice tone="danger" text=${S.ses.err}/>` : nothing}
      <section>${repeat(live, (x) => x.id, row)}</section>
      ${old.length ? html`<section title=${`Older (${old.length})`} collapsible collapsed=${!bag.older} @toggle=${(e) => { bag.older = !e.collapsed; ctx.paint(); }}>${repeat(old, (x) => x.id, row)}</section>` : nothing}
      <section><text style="footnote" tone="muted">${W.sessionsFoot}</text></section>
    </screen>
  </sheet>`;
}

// ---- the scope panel -----------------------------------------------------------------------------

// a tick box's three states as icons (D233's; an app from before them: a check, a minus, nothing)
const BOX = { on: ['check-square', 'check'], mixed: ['minus-square', 'minus'], off: ['square', ''], disabled: ['lock', 'lock'] };
const box = (state) => { const [now, before] = BOX[state] || BOX.off; return (has('card') || has('connection') ? now : before) || nothing; };

/** scopeSheet(): "what this session is connected to" — the tree, the models with a what-if, or a refused change. */
export function scopeSheet() {
  const S = st.S;
  if (S.pop !== 'scope') return nothing;
  const sc = S.scope;
  const view = sc.view === 'refused' && sc.refused ? refusedScreen(sc.refused) : sc.view === 'models' || sc.view === 'whatif' ? modelsScreen() : treeScreen();
  return html`<sheet open title="Connected to" detents="large" @dismiss=${close}>${view}</sheet>`;
}

function treeScreen() {
  const S = st.S, sc = S.scope, s = st.current(), act = st.scope;
  const c = st.counts();
  const rows = S.full ? tree(S.full, sc.ticks, sc.filter) : [];
  const stg = S.full ? staged(S.full, sc.ticks) : { any: false };
  const err = sc.err || (S.full ? '' : S.fullErr);
  // (a phone's three segments are narrow: the totals above already count them)
  const n = (x) => (compact() ? '' : ` ${x}`);
  const filters = [{ value: 'all', label: `All${n((c.tiles || 0) + (c.notConnected || 0))}` }, { value: 'connected', label: 'Connected' }, { value: 'not', label: `Not connected${n(c.notConnected || 0)}` }];
  const priv = st.isPrivate();
  return html`<screen title="Connected to" style="list">
    ${barTpl(html`<button role="plain" @tap=${close}>Cancel</button>`, html`<button role="primary" ?disabled=${!stg.any || sc.busy} ?busy=${sc.busy} @tap=${() => act.apply()}>Apply</button>`)}
    <section>
      <row icon="check" tone="ok" title="Connected" detail=${String(c.tiles || 0)}/>
      <row icon="eye-slash" title="Hidden by you" detail=${String(c.hidden || 0)}/>
      <row icon="lock" tone=${c.blocked ? 'warn' : nothing} title="Blocked" detail=${String(c.blocked || 0)}/>
      <row title="No access" detail=${String(c.noAccess || 0)}/>
    </section>
    <section title="Model">
      <row icon="cpu" title=${short(s && s.model) || 'none'} subtitle=${priv ? 'private' : S.full && S.full.model ? 'not private' : nothing} detail="Change…" nav @tap=${() => act.models()}/>
    </section>
    ${err ? html`<notice tone="danger" text=${err}/>` : nothing}
    ${S.full ? html`<section>
        <picker label="Show" style="segmented" value=${sc.filter} options=${filters} @change=${(e) => act.filter(String(e.value))}/>
        <button role="plain" @tap=${() => act.selectAll()}>Select all allowed</button>
      </section>
      ${repeat(rows, (o) => o.id, orgTpl)}`
    : err ? nothing : html`<progress label="Reading what this session is connected to…"/>`}
    <section><text style="footnote" tone="muted">${W.scopeFoot}</text></section>
  </screen>`;
}

// an org: its own tick (all its allowed tiles, or none; a mixed box means some), its reason once, its tiles
function orgTpl(o) {
  const sc = st.S.scope, act = st.scope;
  const open = sc.expanded.has(o.id);
  const word = o.state && STATES[o.state] ? STATES[o.state] : null;
  const blocked = o.tiles.some((t) => t.state === 'blocked');
  // (the tiles in no org: xbind's own name for them says so)
  return html`<section title=${o.name} badge=${`${o.ticked} of ${o.of}`}
      collapsible collapsed=${!open} @toggle=${(e) => { if (!!e.collapsed === open) act.expand(o.id); }}>
    <row icon=${box(o.box)} title=${`All of ${o.name}`} subtitle=${o.box === 'mixed' ? 'partly selected' : nothing}
      detail=${word && word.word ? word.word : `${o.ticked} of ${o.of}`} tone=${word ? tone(word.tone) : nothing}
      ?disabled=${o.box === 'disabled'} @tap=${() => act.tickOrg(o.id)}/>
    ${o.reason && blocked ? html`<notice tone="warn" text=${`${o.reason}${o.pinned && !/pinned/.test(o.reason) ? ' (pinned by a workspace admin)' : ''}.`}/>
      ${o.rule === 'models' ? html`<button role="plain" @tap=${() => act.models()}>See what a private model would connect…</button>` : nothing}` : nothing}
    ${repeat(o.tiles, (t) => t.tile, tileTpl)}
  </section>`;
}

// a tile: connected, hidden by you, blocked (its box locked: ticking would not connect it) or no access
function tileTpl(t) {
  const w = STATES[t.state] || { word: t.state, tone: '' };
  return html`<row icon=${box(t.tickable ? (t.ticked ? 'on' : 'off') : t.state === 'blocked' ? 'disabled' : 'off')} title=${t.name}
    subtitle=${t.via ? `Under ${t.by || 'this org'} by its share ${t.via}` : nothing} detail=${w.word} tone=${tone(w.tone)}
    ?disabled=${!t.tickable} @tap=${() => st.scope.tickTile(t.tile)}/>`;
}

// the models, and what a switch would change before anything changes
function modelsScreen() {
  const S = st.S, sc = S.scope, s = st.current(), act = st.scope;
  const model = s && s.model;
  const list = st.models();
  const w = sc.what;
  const picked = sc.pick && sc.pick !== model;
  return html`<screen title="Change this session’s model" style="list">
    ${barTpl(html`<button role="plain" @tap=${() => act.back()}>Back</button>`,
      html`<button role="primary" ?disabled=${!w || !picked || sc.busy} ?busy=${sc.busy} @tap=${() => act.switchModel()}>${w && w.allowed ? w.button : 'Switch model'}</button>`)}
    <section title="Models">
      ${repeat(list, (m) => m.id, (m) => html`<row title=${m.name || short(m.id)} subtitle=${[m.where || m.upstream, m.private ? 'private' : ''].filter(Boolean).join(' · ') || nothing}
        detail=${m.id === model ? 'current' : nothing} icon=${sc.pick === m.id ? 'check' : m.private ? 'lock' : nothing} ?selected=${sc.pick === m.id} @tap=${() => act.pick(m.id)}/>`)}
      ${list.length ? nothing : html`<text tone="muted">No other model is open to you.</text>`}
    </section>
    ${picked ? (w ? html`<section title=${`If you switch to ${short(w.model || sc.pick)}${w.private ? ' · private' : ''}`}>
        <row icon="plus" tone="ok" title=${w.connectsWords} subtitle=${w.connects.length ? w.connects.join(', ') : nothing}/>
        <row icon="xmark" tone=${w.disconnects.length ? 'warn' : nothing} title=${w.disconnectsWords}/>
        ${w.disconnects.map((d) => html`<row title=${d.name} subtitle=${d.reason || nothing}/>`)}
        ${w.summary ? html`<text style="headline">${w.summary}</text>` : nothing}
      </section>` : sc.err ? html`<notice tone="danger" text=${sc.err}/>` : html`<progress label="Asking xbin what would change…"/>`) : nothing}
  </screen>`;
}

// a change the conversation's data doesn't allow: a new session on that model is the way forward
function refusedScreen(r) {
  return html`<screen title=${r.title} style="scroll">
    <notice tone="warn" title=${r.title} text=${r.body}/>
    <text>${r.after}</text>
    ${st.S.scope.err ? html`<notice tone="danger" text=${st.S.scope.err}/>` : nothing}
    <button role="primary" @tap=${() => st.scope.newOn(r.model)}>${r.start}</button>
    <button @tap=${close}>${r.stay}</button>
  </screen>`;
}

// ---- "What can it do?" ---------------------------------------------------------------------------

/** whatCanSheet(): the empty state's lists and starters again, on request; a starter fills the composer and closes it. With the agent's switch off (`off`): its sentence only. */
export function whatCanSheet() {
  const S = st.S;
  if (!S.whatCan) return nothing;
  const e = S.empty;
  const done = () => { S.whatCan = false; ctx.paint(); };
  return html`<sheet open title=${W.whatCanItDo} detents=${['medium', 'large']} @dismiss=${done}>
    <screen title=${W.whatCanItDo} style="list">
      ${!e ? html`<progress label="Loading…"/>` : e.off ? html`<section title=${e.heading || W.whatCanItDo} footer=${e.line || nothing}></section>` : html`
        <section title=${e.heading || W.whatCanItDo} footer=${e.line || nothing}></section>
        <section title=${W.itCan}>${(e.can || []).map((x) => html`<row icon="check" tone="ok" title=${x}/>`)}</section>
        <section title=${W.itCant}>${(e.cant || []).map((x) => html`<row icon="xmark" title=${x}/>`)}</section>
        ${(e.starters || []).length ? html`<section title=${e.startersTitle || 'Try one'}>
          ${e.starters.map((s, i) => html`<row title=${s.text} badge=${s.kind || nothing} @tap=${() => { S.whatCan = false; starter(i); }}/>`)}</section>` : nothing}`}
    </screen>
  </sheet>`;
}

// ---- the notebook ("what the agent remembers") -----------------------------------------------------

/** notebookScreens(): the screens pushed for the notebook — its list, then the open page — each {key, tpl, back}. */
export function notebookScreens() {
  const S = st.S;
  if (S.view !== 'notebook') return [];
  const n = S.nb;
  const out = [{ key: 'nb', tpl: listTpl, back: () => st.notebook.close() }];
  if (n.open) out.push({ key: 'nb:' + n.open, tpl: pageTpl, back: () => st.notebook.list() });
  return out;
}

function listTpl() {
  const n = st.S.nb, act = st.notebook;
  const pages = n.pages || [];
  return html`<screen title="Notebook" subtitle=${`what the agent remembers · ${pages.length} page${pages.length === 1 ? '' : 's'}`} style="list">
    <toolbar><button icon="plus" @tap=${() => act.add()}>Add a note</button></toolbar>
    ${n.err ? html`<notice tone="danger" text=${n.err}/>` : nothing}
    ${pages.length ? html`<section>${repeat(pages, (p) => p.slug, (p) => {
      const c = pageClosed(p);
      const meta = [p.summary, [p.updatedBy ? `by ${p.updatedBy}` : '', day(p.updatedMs)].filter(Boolean).join(' ')].filter(Boolean).join(' · ');
      return html`<row icon=${c.closed ? 'lock' : 'doc'} title=${p.title || p.slug} subtitle=${(c.closed ? c.why : meta) || nothing} badge=${pageTag(p)}
        tone=${c.closed ? 'warn' : nothing} ?disabled=${c.closed} ?nav=${!c.closed} @tap=${() => act.read(p.slug)}/>`;
    })}</section>` : html`<empty icon="doc" title="Nothing yet" text="The agent writes here what should outlast a session; so can you."/>`}
    <section><text style="footnote" tone="muted">${W.notebookFoot}</text></section>
  </screen>`;
}

function pageTpl() {
  const n = st.S.nb, act = st.notebook, p = n.page;
  const fresh = n.open === '+';
  const editing = n.editing || fresh;
  const d = n.draft || (n.draft = {});
  const title = fresh ? 'Add a note' : p ? p.title || p.slug : 'Notebook';
  if (editing) {
    return html`<screen title=${title} style="form">
      <toolbar><button role="plain" ?disabled=${n.busy} @tap=${() => act.cancel()}>Cancel</button><button role="primary" ?disabled=${n.busy} ?busy=${n.busy} @tap=${() => act.save()}>Save</button></toolbar>
      ${n.err ? html`<notice tone="danger" text=${n.err}/>` : nothing}
      <section>
        <field label="Title" value=${d.title ?? (p ? p.title : '')} @input=${(e) => { d.title = e.value; }}/>
        <field kind="multiline" label="Text" value=${d.body ?? (p ? p.body || '' : '')} @input=${(e) => { d.body = e.value; }}/>
      </section>
    </screen>`;
  }
  if (!p) return html`<screen title=${title} style="scroll">${n.err ? html`<notice tone="danger" text=${n.err}/>` : html`<progress label="Loading…"/>`}</screen>`;
  const orgs = p.orgs || [];
  const meta = [p.updatedBy ? `edited by ${p.updatedBy}` : '', day(p.updatedMs), p.tile ? `about ${p.tile}${p.rev ? ' at ' + p.rev : ''}` : '', pageTag(p)].filter(Boolean).join(' · ');
  return html`<screen title=${title} style="scroll">
    <toolbar>
      <button icon="pencil" @tap=${() => act.edit()}>Edit</button>
      <menu icon="ellipsis" label="More">
        <button icon="trash" role="destructive" confirm=${{ title: 'Delete this page?', message: 'The agent forgets it; its history is kept.', label: 'Delete', destructive: true }}
          @tap=${() => act.drop()}>Delete</button>
      </menu>
    </toolbar>
    ${n.err ? html`<notice tone="danger" text=${n.err}/>` : nothing}
    <text style="footnote" tone="muted">${meta}</text>
    ${orgs.length ? html`<text style="footnote" tone="muted">${W.marksNote(orgs)}</text>` : nothing}
    <markdown source=${p.body || ''}/>
  </screen>`;
}

// ---- one tool call in full (a card's ↗: native/blocks.js pushes {kind: 'call'}) --------------------------

const findBlock = (blocks, id) => {
  for (const b of blocks || []) {
    if (b.id === id) return b;
    const hit = findBlock(b.kids, id) || findBlock(b.blocks, id);
    if (hit) return hit;
  }
  return null;
};

/** callScreens(): the calls opened in full over the conversation (ui.stack), each {key, tpl, back}. */
export function callScreens() {
  return ui.stack.filter((s) => s.kind === 'call').map((s, i) => ({
    key: `call:${i}:${s.id}`,
    back: () => { const j = ui.stack.indexOf(s); if (j >= 0) ui.stack.splice(j, 1); },
    tpl: () => {
      const b = findBlock(st.session.shown().blocks, s.id) || s.last;
      if (b) s.last = b;
      if (!b) return html`<screen title="Tool call" style="scroll"><empty title="gone"/></screen>`;
      return html`<screen title=${b.headline} subtitle=${b.name} style="scroll">
        <text style="caption" tone="muted">arguments</text>
        <code text=${b.args || '{}'} copy wrap/>
        <text style="caption" tone="muted">${`result · ${fmtN((b.result || '').length)} characters`}</text>
        <code text=${b.result || ''} copy wrap/>
      </screen>`;
    },
  }));
}
