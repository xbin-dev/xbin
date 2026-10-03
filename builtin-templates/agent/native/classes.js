// native/classes.js — agent classes (D116) in the native view: the Class
// picker in the home toolbar (the app's composer holds buttons only; a
// conversation's class is fixed, its subtitle says it), the class section of
// the new-chat sheet, and the managers' Classes screens — the list (the
// default for new chats; a built-in resets, another deletes) and one class's
// form (saving one that can move internal data out is confirmed). The web
// draws the same from classes.js; what they say and send is model/classes.js.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import * as C from '../model/classes.js';
import * as actions from '../model/actions.js';
import { ctx, push, ui } from './ui.js';

// classPickerTpl: the class for your next new chats (model/classes.js classPicker).
export function classPickerTpl() {
  const app = ctx.app;
  const p = C.classPicker(null, app.classes, app.classId);
  if (!p.shown || app.harness.picked()) return nothing; // a coding agent resolves its class (D147; model/app.js newClassId)
  return html`<picker label="Class" style="menu" value=${p.value}
    options=${p.rows.map((r) => ({ value: r.value, label: r.mixed ? `${r.name} — ${C.MIXED}` : r.name, icon: r.nativeIcon }))}
    @change=${(e) => app.pickClass(e.value)}/>`;
}

// classSectionTpl: the new-chat sheet's class (f.class), with what it is for
// — none while a coding agent answers it (f.agent, native/harness-start.js):
// its class resolves.
export function classSectionTpl(f) {
  if (f.agent && f.agent !== 'agent' && ctx.app.harness.find(f.agent)?.available) return nothing;
  const rows = C.pickerRows(ctx.app.classes, f.class);
  const cur = rows.find((r) => r.on);
  const footer = [cur && cur.description, cur && cur.mixed ? `It ${C.MIXED}.` : '', 'Fixed for the conversation once it starts.'].filter(Boolean).join(' ');
  const short = rows.length <= 3 && rows.every((r) => r.name.length <= 10);
  return html`<section title="Class" footer=${footer}>
    <picker style=${short ? 'segmented' : 'menu'} label="Class" value=${f.class || ''}
      options=${rows.map((r) => ({ value: r.value, label: r.name, icon: r.nativeIcon }))} @change=${(e) => { f.class = e.value; ctx.paint(); }}/>
  </section>`;
}

// classRow: an automation's class (model/classes.js ofAutomation) — a class
// that can move internal data out says so.
export const classRow = (c) => html`<row title="Class" detail=${c.label} icon=${c.nativeIcon} subtitle=${c.warn || nothing}
  tone=${c.mixed ? 'warn' : nothing}/>`;

// classPicker: an automation form's class select (model/classes.js choices rows).
export const classPicker = (label, rows, value, pick) => html`<picker label=${label} style="menu" value=${value}
  options=${rows.map((r) => ({ value: r.value, label: r.mixed ? `${r.name} — ${C.MIXED}` : r.name, icon: r.nativeIcon }))} @change=${(e) => pick(e.value)}/>`;

// --- the managers' screens -----------------------------------------------------------

function load(s, fn) {
  if (s.loaded) return;
  s.loaded = true;
  s.err = '';
  Promise.resolve().then(() => fn(s)).catch((e) => { s.err = e.message; }).finally(() => ctx.paint());
}
const errTpl = (s) => (s.err ? html`<section><notice tone="danger" text=${s.err}/></section>` : nothing);
const act = (s, fn) => async (...a) => {
  s.err = '';
  try { await fn(...a); } catch (e) { s.err = e.message; }
  ctx.paint();
};
// the list screen under a form follows what a save answered
const listed = (state) => { for (const x of ui.stack) if (x.kind === 'classes') x.state = state; };

// classes: GET /classes afresh (a manager sees every class); saving goes
// through app.saveClasses, so the home picker follows at once.
function classesTpl(s) {
  const app = ctx.app;
  load(s, async () => { app.setClasses(await actions.classes()); s.state = app.classes; });
  app.harness.ensure(); // the coding agents' names (a class's checklist)
  const st = s.state;
  if (!st) return html`<screen title="Classes" style="list">${errTpl(s)}<section><progress label="loading…"/></section></screen>`;
  const save = (body) => act(s, async () => { s.state = await app.saveClasses(body); });
  return html`<screen title="Classes" style="list">
    <toolbar><button icon="plus" @tap=${() => push({ kind: 'classForm', form: C.blankForm() })}>New class</button></toolbar>
    ${errTpl(s)}
    <section title="New chats get" footer="…when the person has picked none — their pick is remembered.">
      <picker label="Default class" style="menu" value=${st.default}
        options=${st.classes.map((c) => ({ value: c.id, label: c.name || c.id, icon: C.nativeIcon(c) }))}
        @change=${(e) => save(C.defaultPlan(st, e.value).body)()}/>
    </section>
    <section footer=${`A conversation's class says which tools it gets, fixed when it starts. A class holding internal systems together with the web or a networked sandbox ${C.MIXED} — saving one asks first.`}>
      ${repeat(C.editorRows(st), (r) => r.id, (r) => html`<row title=${r.label} subtitle=${r.description || r.toolsets} detail=${r.tags.join(' · ') || nothing}
          icon=${r.nativeIcon} tone=${r.mixed ? 'warn' : nothing} nav
          @tap=${() => push({ kind: 'classForm', form: C.formOf(st.classes.find((c) => c.id === r.id)) })}>
        ${r.del ? html`<actions><button icon=${r.del.label === 'Delete' ? 'trash' : 'refresh'} role="destructive"
          confirm=${{ title: r.del.confirm, label: r.del.label, destructive: true }}
          @tap=${save(C.removePlan(st, r.id).body)}>${r.del.label}</button></actions>` : nothing}
      </row>`)}
    </section>
  </screen>`;
}

// classForm: one class — new, or as listed.
function classFormTpl(s) {
  const app = ctx.app;
  const f = s.form;
  const state = app.classes;
  const has = (t) => f.toolsets.includes(t);
  const mixed = C.reach(C.classOf(f)).mixed;
  const set = (k, repaint) => (e) => { f[k] = e.value; if (repaint) ctx.paint(); };
  const save = act(s, async () => {
    const err = C.check(f, state);
    if (err) throw new Error(err);
    const plan = C.savePlan(state, f);
    if (plan.mixed) plan.body.confirmMixed = true; // the Save button asked
    listed(await app.saveClasses(plan.body));
    ui.stack.pop();
  });
  const models = app.rules.modelPicker(null, f.model, app.catalog).options
    .map((o) => ({ value: o.value, label: o.value ? o.label : 'none — the person\'s pick or the default' }));
  const namesTpl = (key, modeKey, bound, title) => html`<section title=${title}>
    <picker label=${title} style="segmented" value=${f[modeKey]} options=${[{ value: 'all', label: 'all' }, { value: 'only', label: 'only these' }]}
      @change=${set(modeKey, true)}/>
    ${f[modeKey] === 'only' ? html`${repeat(C.names(f[key], bound), (n) => n.name, (n) => html`<toggle label=${n.name} value=${n.on}
        @change=${() => { f[key] = C.toggleName(f[key], n.name); ctx.paint(); }}/>`)}
      <field label="Names" placeholder="comma-separated" value=${f[key]} @change=${set(key, true)}/>` : nothing}
  </section>`;
  return html`<screen title=${f.orig ? f.name || f.orig : 'New class'} subtitle=${f.builtin ? 'built in' : nothing} style="form">
    <toolbar><button role="primary" confirm=${mixed ? { title: `Save a class that ${C.MIXED}?`, message: C.MIXED_WHY, label: 'Save', destructive: true } : nothing}
      @tap=${save}>Save</button></toolbar>
    ${errTpl(s)}
    ${mixed ? html`<section><notice tone="warn" title=${`This class ${C.MIXED}`} text=${C.MIXED_WHY}/></section>` : nothing}
    <section>
      ${f.orig ? html`<row title=${f.orig} mono="title" subtitle="id"/>` : html`<field label="Id" placeholder="research" value=${f.id} @input=${set('id')}/>`}
      <field label="Name" value=${f.name} @input=${set('name')}/>
      <field label="Icon" placeholder="an emoji" value=${f.icon} @input=${set('icon')}/>
      <field label="Description" hint="shown where a class is picked" value=${f.description} @input=${set('description')}/>
      <picker label="Who may use it" style="menu" value=${f.who}
        options=${[{ value: 'everyone', label: 'everyone' }, { value: 'managers', label: 'the agent\'s managers' }]} @change=${set('who', true)}/>
    </section>
    <section title="Toolsets" footer="The core tools (memory, notes, finish, asking you) are in every class.">
      ${repeat(C.TOOLSETS, (t) => t.id, (t) => html`<toggle label=${`${t.label} — ${t.hint}`} value=${has(t.id)}
        @change=${(e) => { f.toolsets = C.toggle(f.toolsets, t.id, e.value); ctx.paint(); }}/>`)}
    </section>
    ${has('internal') ? namesTpl('mcp', 'mcpMode', C.ifaceNames(globalThis.xbin?.iface?.('mcp')), 'MCP servers') : nothing}
    ${has('sandbox') ? html`<section title="A bound sandbox may reach">
        ${repeat(C.EGRESS, (e) => e.id, (e) => html`<toggle label=${`${e.label} — ${e.hint}`} value=${f.egress.includes(e.id)}
          @change=${(ev) => { f.egress = C.toggle(f.egress, e.id, ev.value); ctx.paint(); }}/>`)}
      </section>
      ${namesTpl('managers', 'managersMode', C.ifaceNames(globalThis.xbin?.iface?.('sandboxes')), 'Sandbox managers')}` : nothing}
    ${has('harness') ? html`<section title="Coding agents it may start or spawn" footer=${C.harnessWhy(f) ? `${C.harnessWhy(f)}: untick Coding agents, or turn on Coding sandbox and an egress other than none.` : 'Which coding agents a conversation of this class may start, and its agent spawn.'}>
      <picker label="Coding agents" style="segmented" value=${f.harnessesMode === 'only' ? 'only' : 'all'}
        options=${[{ value: 'all', label: 'all' }, { value: 'only', label: 'only these' }]} @change=${set('harnessesMode', true)}/>
      ${f.harnessesMode === 'only' ? repeat(C.harnessNames(f.harnesses, app.harness.catalog), (n) => n.id, (n) => html`<toggle label=${n.name} value=${n.on}
        @change=${() => { f.harnesses = C.toggleName(f.harnesses, n.id); ctx.paint(); }}/>`) : nothing}
    </section>` : nothing}
    <section title="Model" footer="Used when the person picked no model for the conversation.">
      <picker label="Model" style="menu" value=${f.model} options=${models} @change=${set('model', true)}/>
    </section>
    <section title="System addendum" footer="Added to the system prompt when a conversation of this class starts without instructions of its own.">
      <field kind="multiline" value=${f.system} @input=${set('system')}/>
    </section>
  </screen>`;
}

export const classScreens = { classes: classesTpl, classForm: classFormTpl };
