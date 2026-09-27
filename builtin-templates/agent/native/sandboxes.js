// native/sandboxes.js — coding sandboxes (D115) in the native view: the
// Sandbox picker in the chat and home toolbars (the app's composer holds
// buttons only; shown where the class — the open conversation's, or the
// next new chat's — has the sandbox toolset), the ▣ badge (the header's
// subtitle, a notice when the binding no longer resolves, and ⋯ → Sandbox:
// the working directory, switching among the attached ones, Detach,
// Manage…), and the pushed Sandboxes screens: the list with the lifecycle
// actions your rights allow, and the create form. What they say is
// model/sandboxes.js, what they do app.sbx (model/sandbox-store.js); the web
// draws the same from sandboxes.js. "Open terminal" waits for phase 3.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import * as S from '../model/sandboxes.js';
import { ctx, fail, guard, push, ui } from './ui.js';

const NEW = '+new';
const MANAGE = '+manage';
const GROUP = { shared: ' · shared', team: ' · team' };

// openSandboxes pushes the Sandboxes screen (read afresh from the managers),
// with the create form over it when create is set.
export function openSandboxes({ create = false } = {}) {
  push({ kind: 'sandboxes' });
  if (create) push({ kind: 'sandboxNew', f: {}, bind: true });
}

// sandboxPickerTpl: the open conversation's sandbox from its next turn, or at
// home the next new chat's (model/sandboxes.js sandboxPicker). A picker
// cannot disable an option: one you may not use is marked, and picking it
// says why instead of binding it.
export function sandboxPickerTpl() {
  const app = ctx.app;
  const p = app.sbx.picker();
  if (!p.shown || p.disabled) return nothing;
  app.sbx.ensure();
  // short labels: the bar shows the current one beside the model's
  const rows = p.groups.flatMap((g) => g.rows.map((r) => ({ ...r,
    label: r.name + (r.state && r.state !== 'running' ? ` (${S.STATES[r.state] || r.state})` : '') + (GROUP[g.id] || '') })));
  const options = [
    { value: '', label: p.none.label, icon: 'minus' },
    ...rows.map((r) => ({ value: r.value, label: r.disabled ? `${r.label} — unavailable` : r.label, icon: r.disabled ? 'lock' : 'box' })),
    ...p.actions.map((a) => ({ value: a.id === 'new' ? NEW : MANAGE, label: a.label, icon: a.id === 'new' ? 'plus' : 'list' })),
  ];
  const change = (e) => {
    const to = e.value;
    if (to === MANAGE) return openSandboxes();
    if (to === NEW) {
      const a = p.actions.find((x) => x.id === 'new');
      return a.disabled && a.why ? fail(a.why) : openSandboxes({ create: true });
    }
    const r = rows.find((x) => x.value === to);
    if (r && r.disabled) return fail(`${r.name}: ${r.why}`);
    return guard(() => app.sbx.choose(to))();
  };
  return html`<picker label="Sandbox" style="menu" value=${p.value} options=${options} @change=${change}/>`;
}

// badgeWords: the open conversation's ▣ for its header's subtitle ('' = none).
export function badgeWords(v) {
  const b = ctx.app.sbx.badge(v);
  if (!b) return '';
  ctx.app.sbx.ensure();
  return b.label + (b.broken ? ' ⚠' : '');
}

// brokenTpl: why the binding no longer resolves, said in the transcript.
export function brokenTpl(v) {
  const b = ctx.app.sbx.badge(v);
  if (!b || !b.broken) return nothing;
  return html`<notice tone="warn" title=${`${S.ICON} ${b.name}`} text=${`${b.broken} — pick another sandbox, or detach it (⋯ → Sandbox)`}/>`;
}

// sandboxMenuTpl: the run menu's way to the badge's screen.
export function sandboxMenuTpl(v) {
  const b = ctx.app.sbx.badge(v);
  if (!b) return nothing;
  return html`<button icon="box" @tap=${() => push({ kind: 'sandbox' })}>${`Sandbox: ${b.name}${b.broken ? ' ⚠' : ''}`}</button>`;
}

// --- the screens ---------------------------------------------------------------------

function load(s, fn) {
  if (s.loaded) return;
  s.loaded = true;
  Promise.resolve().then(fn).catch((e) => { s.err = e.message; }).finally(() => ctx.paint());
}
const errTpl = (s) => (s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing);
const drop = (s) => { const i = ui.stack.indexOf(s); if (i >= 0) ui.stack.splice(i); };

// sandbox: the badge's screen — the web's popover.
function boxTpl(s) {
  const app = ctx.app;
  const b = app.sbx.badge();
  if (!b) {
    return html`<screen title="Sandbox" style="form"><section>
      <empty icon="box" title="No sandbox" text="This conversation works in none — pick one with the Sandbox picker."/>
    </section></screen>`;
  }
  if (s.ref !== b.ref) { s.ref = b.ref; s.cwd = b.cwd; }
  const run = (fn, close = false) => async () => {
    s.err = '';
    try { await fn(); if (close) drop(s); } catch (e) { s.err = e.message; }
    ctx.paint();
  };
  const setCwd = run(async () => { await app.sbx.setCwd(s.cwd); s.cwd = S.bindingOf(app.session.current())?.cwd ?? s.cwd; });
  return html`<screen title=${b.name} subtitle=${`${S.ICON} ${b.detail}`} style="form">
    ${b.broken ? html`<section><notice tone="warn" title="The binding no longer resolves" text=${`${b.broken} — pick another, or detach it.`}/></section>` : nothing}
    ${errTpl(s)}
    <section footer="The tools work there from the agent's next turn; empty is the sandbox's workdir.">
      <field label="Working directory" placeholder="the sandbox's workdir" value=${s.cwd} ?disabled=${!b.canChange} submit="done"
        @input=${(e) => { s.cwd = e.value; }} @submit=${setCwd}/>
      <button ?disabled=${!b.canChange} @tap=${setCwd}>Set</button>
    </section>
    ${b.attached.length > 1 ? html`<section title="Attached" footer="The agent works in one at a time.">
      ${repeat(b.attached, (a) => a.ref, (a) => html`<row title=${a.name} subtitle=${a.broken || a.cwd || nothing} mono=${a.broken ? nothing : 'subtitle'}
        icon="box" ?selected=${a.on} detail=${a.on ? 'active' : nothing} tone=${a.broken ? 'warn' : nothing}
        @tap=${a.on || !b.canChange ? nothing : run(() => app.sbx.choose(a.ref, a.cwd))}/>`)}
    </section>` : nothing}
    <section footer="Detaching takes it off this conversation; the sandbox stays.">
      <button role="destructive" ?disabled=${!b.canChange} @tap=${run(() => app.sbx.detach(b.ref), true)}>Detach</button>
      <row title="Manage sandboxes…" icon="list" nav @tap=${() => push({ kind: 'sandboxes' })}/>
    </section>
  </screen>`;
}

const ACT_ICON = { use: 'check', start: 'play', stop: 'stop', thaw: 'sun', archive: 'archive', team: 'people', private: 'lock', delete: 'trash' };

// sandboxes: every sandbox you may see (model/sandboxes.js sandboxRows), each
// row's actions behind its swipe and ⋯ (archive and delete confirmed).
function listTpl(s) {
  const app = ctx.app;
  load(s, () => app.sbx.load(true));
  const L = app.sbx.list;
  const rows = app.sbx.rows();
  const usable = L.managers.some((m) => m.ok !== false);
  const act = (r, a) => async () => {
    s.busy = r.ref; s.err = ''; s.msg = '';
    ctx.paint();
    try { s.msg = await app.sbx.perform(r.ref, a.id, r.name); } catch (e) { s.err = `${r.name}: ${e.message}`; }
    s.busy = '';
    ctx.paint();
  };
  return html`<screen title="Sandboxes" subtitle=${app.session.current() ? 'for this conversation' : 'for your next new chat'} style="list"
      refreshable @refresh=${() => { s.loaded = false; ctx.paint(); }}>
    <toolbar><button icon="plus" ?disabled=${!usable} @tap=${() => push({ kind: 'sandboxNew', f: {}, bind: true })}>New sandbox</button></toolbar>
    ${app.sbx.error ? html`<section><notice tone="danger" text=${app.sbx.error}/></section>` : nothing}
    ${repeat(L.managers.filter((m) => m.ok === false), (m) => m.provider, (m) => html`<section><notice tone="warn"
      title=${m.title || m.provider} text=${m.error || 'unavailable'}/></section>`)}
    ${L.loaded && !L.managers.length ? html`<section><notice tone="info" title="No sandbox manager is bound"
      text="Bind one to this agent's sandboxes slot (the binding panel, or bx bind)."/></section>` : nothing}
    ${s.msg ? html`<section><notice tone="ok" text=${s.msg}/></section>` : nothing}
    ${errTpl(s)}
    ${!L.loaded && !app.sbx.error ? html`<section><progress label="loading…"/></section>` : rows.length ? html`<section
        footer="Swipe a sandbox, or its ⋯, for what you may do with it.">${repeat(rows, (r) => r.ref, (r) => rowTpl(r, s.busy, act, L.managers.length > 1))}</section>`
      : L.loaded ? html`<section><empty icon="box" title="No sandboxes yet"/></section>` : nothing}
  </screen>`;
}

// a row: its state and where it is used first, then what it is (the manager
// only when there are several)
function rowTpl(r, busy, act, managers) {
  const facts = [busy === r.ref ? `${r.stateLabel.replace(/…$/, '')}…` : r.stateLabel, r.stateDetail, r.where,
    r.visLabel, managers && r.manager, r.image, r.size, r.egressLabel, `owner: ${r.owner}`, r.lastLabel && `active ${r.lastLabel}`,
    r.bound ? `in ${r.bound} conversation${r.bound === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ');
  return html`<row title=${r.name} subtitle=${facts} icon="box" ?selected=${r.active} tone=${r.state === 'error' ? 'danger' : nothing}>
    ${r.actions.length ? html`<actions>${repeat(r.actions, (a) => a.id, (a) => html`<button icon=${ACT_ICON[a.id]}
      role=${a.danger ? 'destructive' : nothing} ?disabled=${!!busy}
      confirm=${a.confirm ? { title: a.confirm, label: a.label, destructive: !!a.danger } : nothing}
      @tap=${act(r, a)}>${a.label}</button>`)}</actions>` : nothing}
  </row>`;
}

// sandboxNew: the create form (model/sandboxes.js createForm) — for the open
// conversation's class, and bound there; at home the next new chat's.
function newTpl(s) {
  const app = ctx.app;
  app.sbx.ensure();
  const vm = app.sbx.form(s.f);
  const f = vm.f;
  const here = !!app.session.current();
  const set = (k) => (e) => { s.f = { ...vm.f, ...s.f, [k]: e.value }; ctx.paint(); };
  // a picker cannot disable an option: one the class does not allow says why
  const pick = (k, list) => (e) => {
    const o = list.find((x) => x.value === e.value);
    if (o && o.disabled) { s.err = o.why; ctx.paint(); return; }
    s.err = '';
    set(k)(e);
  };
  const opts = (list) => list.map((o) => ({ value: o.value, label: o.disabled ? `${o.label} — not in this class` : o.label }));
  const create = async () => {
    const now = app.sbx.form(s.f);
    s.err = now.error;
    if (s.err) return ctx.paint();
    s.busy = true;
    ctx.paint();
    try {
      const bind = s.bind !== false;
      const made = await app.sbx.create(now.f, { bind });
      const msg = app.sbx.created(made, bind);
      drop(s);
      for (const x of ui.stack) if (x.kind === 'sandboxes') x.msg = msg;
    } catch (e) { s.err = e.message; }
    s.busy = false;
    ctx.paint();
  };
  return html`<screen title="New sandbox" subtitle=${here ? 'for this conversation' : 'for your next new chat'} style="form">
    <toolbar><button role="primary" ?busy=${!!s.busy} @tap=${create}>Create</button></toolbar>
    ${errTpl(s)}
    <section>
      ${vm.many ? html`<picker label="Manager" style="menu" value=${f.provider} options=${opts(vm.managers)} @change=${pick('provider', vm.managers)}/>` : nothing}
      <field label="Name" placeholder="api-dev" value=${f.name} @input=${set('name')}/>
      <field label="Working directory" placeholder="its workdir" hint="optional — an absolute path" value=${f.cwd} @input=${set('cwd')}/>
    </section>
    <section title="The machine">
      ${vm.images.length ? html`<picker label="Image" style="menu" value=${f.image} options=${opts(vm.images)} @change=${pick('image', vm.images)}/>` : nothing}
      ${vm.sizes.length ? html`<picker label="Size" style="menu" value=${f.size} options=${opts(vm.sizes)} @change=${pick('size', vm.sizes)}/>` : nothing}
      <picker label="Network" style="menu" value=${f.egress} options=${opts(vm.egress)} @change=${pick('egress', vm.egress)}/>
    </section>
    <section title="Who may use it" footer="A private sandbox is yours and the people you add; a team one is everyone's on the team.">
      <picker label="Who may use it" style="segmented" value=${f.visibility}
        options=${[{ value: 'private', label: 'Private' }, { value: 'team', label: 'The team' }]} @change=${set('visibility')}/>
      <toggle label=${here ? 'Work in it in this conversation, from its next turn' : 'Start your next new chat in it'} value=${s.bind !== false}
        @change=${(e) => { s.bind = e.value; ctx.paint(); }}/>
    </section>
  </screen>`;
}

export const sandboxScreens = { sandbox: boxTpl, sandboxes: listTpl, sandboxNew: newTpl };
