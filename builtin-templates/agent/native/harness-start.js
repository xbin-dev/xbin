// native/harness-start.js — starting a conversation with a coding agent in
// the native view (D-harness §2.2 "Conversation start", §4.2.3): "Who
// answers" in the home toolbar (the built-in agent or a coding agent of GET
// /harnesses — a picker can't disable an option, so one that isn't
// available is marked and picking it says why), the home's setup notice (no
// sandbox fits: Create, prefilled; not signed in there: say so), the
// new-chat sheet's section (who answers, and a coding agent's sandbox), and
// the open conversation's badge in its toolbar. Picking a coding agent
// hides the class and model pickers (the class resolves: model/app.js
// newClassId); the Sandbox picker keeps what it fits (model/sandbox-store.js).
// What they say is model/harness-start.js; the web draws the same from
// harness-start.js. Registered on the seams (native/ext.js) by
// native/harness-all.js; native/home.js draws homeSetupTpl, native/convs.js
// a row's kind (kindOf).
import { html, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, fail, guard, push } from './ui.js';
import * as HS from '../model/harness-start.js';
import { AGENT } from '../model/harness-start.js';

const TONE = { run: 'accent', ok: 'ok', warn: 'warn', bad: 'danger' };

// options: a picker's rows — one that isn't available says so (it can't be disabled).
const options = (rows) => rows.map((r) => ({ value: r.value, icon: r.disabled ? 'lock' : r.icon,
  label: `${r.value === AGENT ? r.name : `${r.mono} · ${r.name}`}${r.disabled ? ' — unavailable' : ''}` }));

// homeSetupTpl: at home, what a new chat with the coding agent picked needs first.
export function homeSetupTpl() {
  const app = ctx.app;
  if (!app || app.sel != null) return nothing;
  const c = HS.startOf(app).setup;
  if (!c) return nothing;
  if (c.kind === 'create') {
    return html`<section title=${c.title} footer=${c.create ? 'It becomes your next chat\'s sandbox.' : c.why}>
      <notice tone="info" text=${c.text}/>
      ${c.create ? html`<button role="primary" icon="plus" @tap=${() => { push({ kind: 'sandboxes' }); push({ kind: 'sandboxNew', f: { ...c.create.form }, bind: true }); }}>${c.create.label}</button>` : nothing}
    </section>`;
  }
  return html`<section title=${c.title}>
    <notice tone="warn" text=${c.text}/>
    ${c.use ? html`<button icon="box" @tap=${guard(() => app.sbx.choose(c.use.ref))}>${c.use.label}</button>` : nothing}
  </section>`;
}

ext.register({
  // toolbar: at home "Who answers"; in a conversation a coding agent answers, its badge
  toolbar: (v) => {
    const app = ctx.app;
    if (!v) {
      if (app.sel != null || app.page) return null;
      app.harness.ensure();
      const p = HS.startOf(app).picker;
      if (!p.shown) return null;
      const change = (e) => {
        const r = p.rows.find((x) => x.value === e.value);
        if (r && r.disabled) return fail(`${r.name}: ${r.why}`);
        HS.chooseAgent(app, e.value);
        ctx.paint();
      };
      return html`<picker label="Who answers" style="menu" value=${p.value} options=${options(p.rows)} @change=${change}/>`;
    }
    const t = HS.topChip(v);
    return t ? html`<badge text=${`${t.mono} ${t.label}${t.shared ? ' · shared sandbox' : ''}`} tone=${TONE[t.tone] || 'muted'}/>` : null;
  },
  // composer: at home, who answers (and where) in its placeholder
  composer: (v) => {
    if (v) return null;
    const st = HS.startOf(ctx.app);
    return st.placeholder ? { placeholder: st.placeholder } : null;
  },
  // newChat: who answers this one (f.agent) and a coding agent's sandbox (f.hsbx)
  newChat: (f) => {
    const app = ctx.app;
    app.harness.ensure();
    app.sbx.ensure();
    if (f.agent == null) f.agent = app.harness.picked() ? app.harness.pick : AGENT;
    const h = f.agent !== AGENT ? app.harness.find(f.agent) : null;
    const coding = h && h.available ? h : null;
    if (coding) {
      const opts = HS.sandboxOptions(coding, app.sbx.list);
      if (!opts.some((o) => o.value === f.hsbx && !o.disabled)) {
        const to = HS.preferredSandbox(coding, app.sbx.list, app.harness.sandboxes[coding.id], app.sbx.pick && app.sbx.pick.ref);
        f.hsbx = to ? to.value : '';
      }
    }
    return {
      tpl() {
        const p = HS.agentPicker(app.harness.catalog, f.agent, { classes: app.classes, classId: app.classId, list: app.sbx.list, manager: !!app.me.manager,
          remembered: coding && f.hsbx ? { ...app.harness.sandboxes, [coding.id]: f.hsbx } : app.harness.sandboxes }); // its sign-in where it would start
        if (!p.shown && !coding) return nothing;
        const pick = (e) => {
          const r = p.rows.find((x) => x.value === e.value);
          if (r && r.disabled) return fail(`${r.name}: ${r.why}`);
          f.agent = e.value;
          f.hsbx = '';
          ctx.paint();
        };
        const row = coding && p.rows.find((r) => r.value === coding.id);
        const opts = coding ? HS.sandboxOptions(coding, app.sbx.list) : [];
        const sbx = (e) => {
          const o = opts.find((x) => x.value === e.value);
          if (o && o.disabled) return fail(`${o.name}: ${o.why}`);
          f.hsbx = e.value;
          ctx.paint();
        };
        return html`<section title="Who answers" footer=${coding ? `${row && row.detail ? row.detail[0].toUpperCase() + row.detail.slice(1) + '. ' : ''}A coding agent keeps its own instructions — Instructions are for the built-in agent. Fixed once it starts.` : 'Fixed once it starts.'}>
          <picker label="Who answers" style="menu" value=${coding ? coding.id : AGENT} options=${options(p.rows)} @change=${pick}/>
          ${coding ? (opts.some((o) => !o.disabled)
            ? html`<picker label="Sandbox" style="menu" value=${f.hsbx} options=${opts.map((o) => ({ value: o.value, label: o.disabled ? `${o.label} — unavailable` : o.label, icon: o.disabled ? 'lock' : 'box' }))} @change=${sbx}/>`
            : html`<notice tone="warn" text=${`No sandbox you may use has ${row ? row.name : 'it'} with internet — create one from the home's Sandbox picker.`}/>`) : nothing}
        </section>`;
      },
      body() {
        if (coding && f.hsbx) app.harness.rememberSandbox(coding.id, f.hsbx);
        return HS.newChatPick(app, f.agent, coding ? f.hsbx : '');
      },
    };
  },
});
