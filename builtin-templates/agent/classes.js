// classes.js — agent classes (D116) on the web: the composer's class picker
// (icon + name; each class's description in its menu; only the classes you
// may use — GET /classes; your last pick is your default), the options of the
// "new chat with options" dialog, and the ⚙ Classes tab where the tile's
// managers list, add, edit and delete classes (a built-in resets to its
// default; saving one that can move internal data out is confirmed). What
// they say and send is model/classes.js; the native view draws the same from
// native/classes.js.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import * as C from './model/classes.js';
import * as actions from './model/actions.js';
import * as rules from './model/rules.js';

// --- the composer's picker -------------------------------------------------------------

/**
 * makeClassPicker draws the picker into host (the composer's #cpick): a
 * button (#tset — the old lane toggle's place) that opens the classes as a
 * menu. It shows at home only: a conversation's class is fixed, and the top
 * bar says it.
 */
export function makeClassPicker(app, host) {
  let open = false;
  let v = null;
  const act = {
    toggle: () => { open = !open; paint(v); },
    close: () => { open = false; paint(v); },
    pick: (id) => { open = false; app.pickClass(id); paint(v); },
  };
  function paint(view) {
    v = view;
    const p = C.classPicker(v, app.classes, app.classId);
    // a coding agent answering new chats resolves the class itself (D-harness; model/app.js newClassId)
    const shown = p.shown && !app.harness.picked();
    if (!shown) open = false;
    host.hidden = !shown;
    // the menu opens above the button, kept inside the window
    const w = Math.min(340, innerWidth - 16);
    const left = open ? Math.min(0, innerWidth - 8 - w - host.getBoundingClientRect().left) : 0;
    render(shown ? pickerTpl(p, open, act, `width:${w}px;left:${left}px`) : nothing, host);
  }
  return { paint, get open() { return open; }, close: () => { if (open) act.close(); } };
}

function pickerTpl(p, open, act, place) {
  return html`<button class="btn ghost clsbtn ${p.mixed ? 'mixed' : ''}" id="tset" title=${p.title}
      aria-haspopup="menu" aria-expanded=${open ? 'true' : 'false'} @click=${act.toggle}>
      <span class="ic">${p.icon || '◆'}</span><span class="nm">${p.name}</span><span class="car">▾</span></button>
    ${open ? html`<div class="mback" @click=${act.close}></div>
      <div class="clsmenu" role="menu" aria-label="Class for new chats" style=${place}>
        <div class="clshd">Class for new chats — fixed once a chat starts</div>
        ${p.rows.map((r) => html`<div class="mi ${r.on ? 'on' : ''}" role="menuitemradio" aria-checked=${r.on ? 'true' : 'false'}
            data-class=${r.value} @click=${() => act.pick(r.value)}>
          <span class="ic">${r.icon || '◆'}</span>
          <span class="tx"><b>${r.name}</b>${r.managers ? html` <span class="badge">managers</span>` : nothing}
            ${r.description ? html`<span class="ds">${r.description}</span>` : nothing}
            ${r.mixed ? html`<span class="ds warn">⚠ ${C.MIXED}</span>` : nothing}</span>
          <span class="ck">${r.on ? '✓' : ''}</span></div>`)}
      </div>` : nothing}`;
}

// classOptionsTpl: the classes as <option>s (the new-chat dialog's select).
export const classOptionsTpl = (app, value) => html`${C.pickerRows(app.classes, value).map((r) =>
  html`<option value=${r.value} ?selected=${r.on} title=${r.description}>${r.label}${r.mixed ? ` — ⚠ ${C.MIXED}` : ''}</option>`)}`;

// classFieldTpl: an automation form's class select (model/classes.js choices
// rows; name tags it data-cls) with what the picked class is for under it.
export function classFieldTpl(label, rows, pick, { disabled = false, name = 'class', title = '' } = {}) {
  const cur = rows.find((r) => r.on);
  return html`<div class="field"><label>${label}</label>
    <select data-cls=${name} ?disabled=${disabled} title=${title} @change=${(e) => pick(e.target.value)}>
      ${rows.map((r) => html`<option value=${r.value} ?selected=${r.on} title=${r.description}>${r.label}</option>`)}</select>
    ${cur && (cur.description || cur.mixed) ? html`<div class="muted small">${cur.description}
      ${cur.mixed ? html`<span class="badge clswarn" title=${C.MIXED_WHY}>⚠ ${C.MIXED}</span>` : nothing}</div>` : nothing}</div>`;
}

// clsBadgeTpl: an automation's class on its card and detail (model/classes.js ofAutomation).
export const clsBadgeTpl = (c) => html`<span class="badge" data-cls=${c.id} title=${c.known ? `runs in the ${c.name} class` : `runs in ${c.id} (${c.lane} lane)`}>${c.label}</span>${
  c.mixed ? html` <span class="badge clswarn" title=${C.MIXED_WHY}>${c.warn}</span>` : nothing}`;

// --- ⚙ Classes (managers) --------------------------------------------------------------

/**
 * tabClasses draws the Classes tab into the settings body. It reads GET
 * /classes afresh (a manager sees every class) and saves through
 * app.saveClasses, so the composer's picker follows at once.
 */
export async function tabClasses(bd, app) {
  const st = { state: null, form: null, err: '', msg: '' };
  const [list] = await Promise.all([actions.classes(), app.harness.catalog.harnesses.length ? null : app.harness.load()]); // the coding agents' names
  app.setClasses(list);
  st.state = app.classes;
  bd.textContent = '';
  const host = document.createElement('div');
  bd.append(host);
  const draw = () => render(tabTpl(st, app, draw), host);
  draw();
}

// put saves a PUT /classes body; a 409 (a mixed class it did not know was
// confirmed) asks, then sends it again confirmed.
async function put(st, app, body) {
  st.err = ''; st.msg = '';
  try {
    st.state = await app.saveClasses(body);
  } catch (e) {
    if (e.status !== 409 || !confirm(`${e.message}\n\nSave anyway?`)) throw e;
    st.state = await app.saveClasses({ ...body, confirmMixed: true });
  }
}

function tabTpl(st, app, draw) {
  const s = st.state;
  const run = (fn) => async () => { try { await fn(); } catch (e) { st.err = e.message; } draw(); };
  const edit = (f) => {
    st.form = f; st.err = ''; st.msg = ''; draw();
    document.querySelector('.clsform')?.scrollIntoView({ block: 'nearest' });
  };
  const rows = C.editorRows(s);
  return html`<div class="sec clstab"><h4>Classes</h4>
    <div class="hint">A conversation's class says which tools it gets, and it is fixed when the conversation starts. A class that
      holds internal systems together with the web or a networked sandbox can move internal data out — saving one asks first.</div>
    ${s.fallback ? html`<div class="err">This agent's backend lists no classes.</div>` : nothing}
    <div class="field clsdef"><label>New chats get</label>
      <select id="cl-default" @change=${(e) => run(() => put(st, app, C.defaultPlan(s, e.target.value).body))()}>
        ${s.classes.map((c) => html`<option value=${c.id} ?selected=${c.id === s.default}>${C.label(c)}</option>`)}</select>
      <div class="hint">…when the person has picked none (their pick in the composer is remembered).</div></div>
    <div class="clslist">${rows.map((r) => html`<div class="clsrow" data-cls=${r.id}>
      <span class="lb">${r.label}</span>
      ${r.tags.map((t) => html`<span class="badge ${t.startsWith('⚠') ? 'clswarn' : ''}">${t}</span>`)}
      <span style="flex:1"></span>
      <button class="btn ghost btnsm" data-edit=${r.id} @click=${() => edit(C.formOf(s.classes.find((c) => c.id === r.id)))}>Edit</button>
      ${r.del ? html`<button class="btn rm btnsm" data-del=${r.id}
        @click=${() => { if (confirm(r.del.confirm)) run(() => put(st, app, C.removePlan(s, r.id).body))(); }}>${r.del.label}</button>` : nothing}
      ${r.description ? html`<div class="ds">${r.description}</div>` : nothing}
      <div class="ds mono">${r.toolsets}</div></div>`)}</div>
    <div><button class="btn btnsm" id="cl-new" @click=${() => edit(C.blankForm())}>New class</button>
      <span class="muted" id="cl-msg">${st.msg}</span></div>
    ${st.err && !st.form ? html`<div class="err">${st.err}</div>` : nothing}
  </div>
  ${st.form ? formTpl(st, app, draw) : nothing}`;
}

function formTpl(st, app, draw) {
  const f = st.form;
  const set = (k) => (e) => { f[k] = e.target.value; draw(); };
  const cls = C.classOf(f);
  const has = (t) => f.toolsets.includes(t);
  const mixed = C.reach(cls).mixed;
  const models = rules.modelPicker(null, f.model, app.catalog).options;
  const save = async () => {
    st.err = C.check(f, st.state);
    if (st.err) return draw();
    const plan = C.savePlan(st.state, f);
    if (plan.mixed) {
      if (!confirm(C.confirmWords(f))) return;
      plan.body.confirmMixed = true;
    }
    try { await put(st, app, plan.body); st.form = null; st.msg = 'saved ✓'; } catch (e) { st.err = e.message; }
    draw();
  };
  const namesTpl = (key, modeKey, bound, what) => html`<div class="field"><label>${what}</label>
    <select id=${'clf-' + modeKey} @change=${set(modeKey)}>
      <option value="all" ?selected=${f[modeKey] === 'all'}>all of them</option>
      <option value="only" ?selected=${f[modeKey] === 'only'}>only these</option></select>
    ${f[modeKey] === 'only' ? html`<input id=${'clf-' + key} class="mono" .value=${f[key]} @input=${set(key)} placeholder="names, comma-separated">
      <div class="clsnames">${C.names(f[key], bound).map((n) => html`<label class="chk"><input type="checkbox" .checked=${n.on}
        @change=${() => { f[key] = C.toggleName(f[key], n.name); draw(); }}> <span class="mono">${n.name}</span></label>`)}</div>` : nothing}</div>`;
  return html`<div class="sec clsform"><h4>${f.orig ? `Edit ${f.orig}` : 'New class'}${f.builtin ? ' (built in)' : ''}</h4>
    <div class="grid4">
      <div class="field"><label>Id</label><input id="clf-id" class="mono" .value=${f.id} ?disabled=${!!f.orig} @input=${set('id')} placeholder="research"></div>
      <div class="field"><label>Name</label><input id="clf-name" .value=${f.name} @input=${set('name')} placeholder="Research"></div>
      <div class="field"><label>Icon</label><input id="clf-icon" .value=${f.icon} @input=${set('icon')} placeholder="🔬"></div>
      <div class="field"><label>Who may use it</label><select id="clf-who" @change=${set('who')}>
        <option value="everyone" ?selected=${f.who !== 'managers'}>everyone</option>
        <option value="managers" ?selected=${f.who === 'managers'}>the agent's managers</option></select></div>
    </div>
    <div class="field"><label>Description — shown in the composer's menu</label><input id="clf-desc" .value=${f.description} @input=${set('description')}></div>
    <div class="field"><label>Toolsets</label>${C.TOOLSETS.map((t) => html`<label class="chk"><input type="checkbox" data-ts=${t.id} .checked=${has(t.id)}
      @change=${(e) => { f.toolsets = C.toggle(f.toolsets, t.id, e.target.checked); draw(); }}> <b>${t.label}</b>
      <span class="muted">${t.hint}</span></label>`)}
      <div class="hint">The core tools (memory, notes, finish, asking you) are in every class.</div></div>
    ${mixed ? html`<div class="clsmixed">⚠ This class ${C.MIXED}. ${C.MIXED_WHY} Saving it asks you to confirm.</div>` : nothing}
    ${has('internal') ? namesTpl('mcp', 'mcpMode', C.ifaceNames(globalThis.xbin?.iface?.('mcp')), 'MCP servers') : nothing}
    ${has('sandbox') ? html`<div class="field"><label>A bound sandbox may reach</label>${C.EGRESS.map((e) => html`<label class="chk">
      <input type="checkbox" data-eg=${e.id} .checked=${f.egress.includes(e.id)}
        @change=${(ev) => { f.egress = C.toggle(f.egress, e.id, ev.target.checked); draw(); }}> <b>${e.label}</b> <span class="muted">${e.hint}</span></label>`)}</div>
      ${namesTpl('managers', 'managersMode', C.ifaceNames(globalThis.xbin?.iface?.('sandboxes')), 'Sandbox managers')}` : nothing}
    ${has('harness') ? html`<div class="field"><label>Coding agents it may start or spawn</label>
      <select id="clf-harnessesMode" @change=${set('harnessesMode')}>
        <option value="all" ?selected=${f.harnessesMode !== 'only'}>all of them</option>
        <option value="only" ?selected=${f.harnessesMode === 'only'}>only these</option></select>
      ${f.harnessesMode === 'only' ? html`<div class="clsnames">${C.harnessNames(f.harnesses, app.harness.catalog).map((n) => html`<label class="chk">
        <input type="checkbox" data-harness=${n.id} .checked=${n.on} @change=${() => { f.harnesses = C.toggleName(f.harnesses, n.id); draw(); }}> ${n.name}</label>`)}</div>` : nothing}
      ${C.harnessWhy(f) ? html`<div class="clsmixed" id="clf-harness-why">⚠ ${C.harnessWhy(f)}: untick Coding agents, or tick Coding sandbox and an egress other than none.</div>` : nothing}</div>` : nothing}
    <div class="field"><label>Model</label><select id="clf-model" @change=${set('model')}>
      ${models.map((o) => html`<option value=${o.value} ?selected=${o.value === f.model}>${o.value ? o.label : '— none: the person\'s pick or the agent\'s default —'}</option>`)}</select>
      <div class="hint">Used when the person picked no model for the conversation.</div></div>
    <div class="field"><label>System addendum</label><textarea id="clf-system" rows="3" .value=${f.system} @input=${set('system')}></textarea>
      <div class="hint">Added to the agent's system prompt when a conversation of this class starts without instructions of its own.</div></div>
    ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
    <div><button class="btn" id="clf-save" @click=${save}>Save class</button>
      <button class="btn ghost" id="clf-cancel" @click=${() => { st.form = null; st.err = ''; draw(); }}>Cancel</button></div>
  </div>`;
}
