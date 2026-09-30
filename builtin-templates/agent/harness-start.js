// harness-start.js — starting a conversation with a coding agent on the web
// (D147 §2.2 "Conversation start", §4.2.3): "Who answers" in
// the home composer (#apick — the built-in agent or a coding agent of GET
// /harnesses, each with its monogram; one that isn't available is there,
// disabled, with the reason), the home's setup card (no sandbox fits: Create,
// prefilled; not signed in there: say so), the new-chat dialog's field
// (#n-agent — fixed to the built-in agent, saying why, for a chat shared
// with others in a person's partition — and the sandbox it starts in:
// #n-sandbox) and the open conversation's chip in the top bar. Picking a
// coding agent hides the class picker (the class resolves to one that
// allows it — model/app.js newClassId) and the built-in model's; the
// sandbox picker keeps the sandboxes it fits (model/sandbox-store.js; in a
// person's partition, their own — model/harness-homes.js). What they say is
// model/harness-start.js; the native view draws the same from
// native/harness-start.js. Registered on the seams (web-ext.js) by
// harness-web.js; sidebar.js draws a row's kind chip (.kind, styled here).
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import * as HS from './model/harness-start.js';
import { AGENT } from './model/harness-start.js';
import { sharedNewChat } from './model/harness-homes.js';

const $ = (id) => document.getElementById(id);

// What these draw looks like — beside the class picker's (.clspick, .clsmenu in index.html).
const CSS = `
  .kind { font: 600 9.5px/1.5 var(--bx-mono, monospace); padding: 0 4px; border-radius: 4px; flex: none; letter-spacing: .02em;
          color: var(--bx-accent); border: 1px solid color-mix(in srgb, var(--bx-accent) 50%, var(--bx-border)); }
  .apick .clsbtn .kind, .clsmenu .mi .kind { font-size: 10px; }
  .clsmenu .mi.off { opacity: .55; cursor: default; }
  .clsmenu .mi.off:hover { background: none; }
  .clsmenu .clssec { padding: 8px 12px 4px; color: var(--bx-muted); font-size: 11px; border-top: 1px solid var(--bx-border); margin-top: 4px; }
  .clsmenu .clsempty { padding: 6px 12px 8px; color: var(--bx-muted); font-size: 11.5px; }
  .top .hsetup { flex-basis: 100%; display: flex; flex-wrap: wrap; gap: 6px 10px; align-items: center; font-size: 12px; padding: 7px 10px; border-radius: 6px;
                 background: color-mix(in srgb, var(--bx-accent) 10%, transparent); border: 1px solid color-mix(in srgb, var(--bx-accent) 40%, var(--bx-border)); }
  .top .hsetup .tx { flex: 1; min-width: 200px; }
  .top .hsetup .why { color: var(--bx-muted); font-size: 11.5px; }
  .badge.hchip { display: inline-flex; gap: 4px; align-items: center; text-transform: none; letter-spacing: 0; }
  .badge.hchip.warn { color: var(--bx-yellow, #d9a441); } .badge.hchip.bad { color: var(--bx-red); } .badge.hchip.run { color: var(--bx-accent); }
  /* a narrow composer (a 480px tile beside the list): the pickers keep their icons (their row is index.html's .cpicks) */
  .composer { container-type: inline-size; }
  @container (max-width: 420px) { .composer .clsbtn .nm { display: none; } .composer .msel { max-width: 50px; } }
`;
if (!document.getElementById('harness-start-css')) {
  document.head.append(Object.assign(document.createElement('style'), { id: 'harness-start-css', textContent: CSS }));
}

// --- "Who answers" (#apick) -------------------------------------------------------------------

let open = false;
function pickerTpl(app, p) {
  const w = Math.min(340, innerWidth - 16);
  const host = $('apick');
  const left = open && host ? Math.min(0, innerWidth - 8 - w - host.getBoundingClientRect().left) : 0;
  const close = () => { open = false; ctx.paint(); };
  const pick = (r) => { if (r.disabled) return; open = false; HS.chooseAgent(app, r.value); ctx.paint(); };
  const row = (r) => html`<div class="mi ${r.on ? 'on' : ''} ${r.disabled ? 'off' : ''}" role="menuitemradio" aria-checked=${r.on ? 'true' : 'false'}
      aria-disabled=${r.disabled ? 'true' : 'false'} data-agent=${r.value} title=${r.disabled ? r.why : r.detail} @click=${() => pick(r)}>
    <span class="ic">${r.value === AGENT ? r.mono : html`<span class="kind">${r.mono}</span>`}</span>
    <span class="tx"><b>${r.name}</b>${r.detail ? html`<span class="ds ${r.disabled ? 'warn' : ''}">${r.detail}</span>` : nothing}</span>
    <span class="ck">${r.on ? '✓' : ''}</span></div>`;
  return html`<button class="btn ghost clsbtn" id="abtn" title=${p.title} aria-haspopup="menu" aria-expanded=${open ? 'true' : 'false'}
      @click=${() => { open = !open; if (open) { app.harness.load().catch(() => {}); app.sbx.ensure(); } ctx.paint(); }}>
      <span class="ic">${p.harness ? html`<span class="kind">${p.mono}</span>` : p.mono}</span><span class="nm">${p.label}</span><span class="car">▾</span></button>
    ${open ? html`<div class="mback" @click=${close}></div>
      <div class="clsmenu" role="menu" aria-label="Who answers new chats" style=${`width:${w}px;left:${left}px`}>
        <div class="clshd">${p.header}</div>
        ${row(p.rows[0])}
        <div class="clssec">${p.section}</div>
        ${p.rows.slice(1).map(row)}
        ${p.empty ? html`<div class="clsempty" id="apick-empty">${p.empty}</div>` : nothing}
      </div>` : nothing}`;
}
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && open) { open = false; ctx.paint(); } });

// --- the home's setup card --------------------------------------------------------------------

function setupTpl(app, st) {
  const c = st.setup;
  if (!c) return nothing;
  if (c.kind === 'create') {
    return html`<div class="hsetup" id="hsetup" data-kind="create"><span class="tx">${c.text}</span>
      ${c.create ? html`<button class="btn btnsm" id="hsetup-create" title="the create form, filled in for it — it becomes your next chat's sandbox"
        @click=${() => ctx.sbxUI && ctx.sbxUI.open({ create: true })}>${c.create.label}</button>` : html`<span class="why">${c.why}</span>`}</div>`;
  }
  return html`<div class="hsetup" id="hsetup" data-kind="signin"><span class="tx"><b>${c.title}.</b> ${c.text}</span>
    ${c.use ? html`<button class="btn ghost btnsm" id="hsetup-use" @click=${() => app.sbx.choose(c.use.ref).catch((e) => alert(e.message))}>${c.use.label}</button>` : nothing}</div>`;
}

// --- the seams ----------------------------------------------------------------------------------

let redrawNew = null; // the open new-chat dialog's redraw

ext.register({
  // top: at home the setup card; in a conversation a coding agent answers, its chip
  top: (v) => {
    const app = ctx.app;
    if (!app) return null;
    if (!v) return app.page ? null : setupTpl(app, HS.startOf(app));
    const t = HS.topChip(v);
    return t ? html`<span class="badge hchip ${t.tone}" id="hchip" title=${t.title}><span class="kind">${t.mono}</span>${t.label}${t.shared ? ' 👥' : ''}</span>` : null;
  },
  // paint: the composer at home — "Who answers", and what a coding agent hides
  paint: (v) => {
    const app = ctx.app;
    const host = $('apick');
    if (!app || !host) return;
    const home = !v && !app.page;
    if (home) app.harness.ensure();
    const st = home ? HS.startOf(app) : null;
    const p = st && st.picker;
    if (!p || !p.shown) open = false;
    host.hidden = !(p && p.shown);
    render(p && p.shown ? pickerTpl(app, p) : nothing, host);
    if (st && st.harness) {
      $('msel').hidden = true; // a coding agent's model is its own (an option)
      if (st.placeholder && !$('msg').disabled) $('msg').placeholder = st.placeholder;
    }
  },
  // newChat: who answers this one (#n-agent) and, for a coding agent, the
  // sandbox it starts in (#n-sandbox). In a person's partition a chat
  // shared with others (#n-share, homes-ui.js) is the built-in agent's:
  // "Who answers" is fixed to it, saying why (model/harness-homes.js).
  newChat: (redraw) => {
    const app = ctx.app;
    if (!app) return null;
    if (!redrawNew) { // the catalog or the sandboxes landing while the dialog is open — or who can see it changing
      const again = () => { if ($('newdlg')?.open) redrawNew(); };
      app.on('harness', again);
      app.on('sandboxes', again);
      $('newdlg')?.addEventListener('change', (e) => { if (e.target && e.target.id === 'n-share') again(); });
    }
    redrawNew = redraw;
    app.harness.ensure();
    app.sbx.ensure();
    const f = { agent: app.harness.picked() ? app.harness.pick : AGENT, ref: '' };
    const shared = () => sharedNewChat($('n-share')?.value); // why the built-in agent answers ('' = anyone may)
    const agent = () => (shared() ? AGENT : f.agent);
    const coding = () => { const h = agent() !== AGENT ? app.harness.find(agent()) : null; return h && h.available ? h : null; };
    const fields = (on) => {
      for (const id of ['n-class', 'n-system']) { const el = $(id)?.closest('.field'); if (el) el.style.display = on ? 'none' : ''; }
    };
    const refOf = (h) => {
      const opts = HS.sandboxOptions(h, app.sbx.list, HS.startClass(app, h));
      if (f.ref && opts.some((o) => o.value === f.ref && !o.disabled)) return f.ref;
      const to = HS.preferredSandbox(h, app.sbx.list, app.harness.sandboxes[h.id], app.sbx.pick && app.sbx.pick.ref, HS.startClass(app, h));
      return (f.ref = to ? to.value : '');
    };
    return {
      tpl() {
        const h = coding();
        const why = shared();
        const ref = h ? refOf(h) : '';
        const p = HS.agentPicker(app.harness.catalog, f.agent, { classes: app.classes, classId: app.classId, list: app.sbx.list, manager: !!app.me.manager,
          remembered: h && ref ? { ...app.harness.sandboxes, [h.id]: ref } : app.harness.sandboxes }); // its sign-in where it would start
        fields(!!h);
        if (!p.shown && !h) return nothing;
        const opts = h ? HS.sandboxOptions(h, app.sbx.list, HS.startClass(app, h)) : [];
        const cls = h ? p.rows.find((r) => r.value === h.id)?.detail : '';
        // shared with others: a select of its own (a fresh one again after, so the pick shows as it was)
        const pick = why ? html`<select id="n-agent" disabled title=${why}><option value=${AGENT} selected>${p.rows[0].name}</option></select>
            <div class="hint" id="n-agent-shared">${why}.</div>`
          : html`<select id="n-agent" @change=${(e) => { f.agent = e.target.value; f.ref = ''; redraw(); }}>
              ${p.rows.map((r) => html`<option value=${r.value} ?selected=${r.value === f.agent} ?disabled=${r.disabled}
                title=${r.disabled ? r.why : r.detail}>${r.value === AGENT ? r.name : `${r.mono} · ${r.name}`}${r.disabled ? ` — ${r.why}` : ''}</option>`)}</select>`;
        return html`<div class="field"><label>Who answers — fixed once it starts</label>
            ${pick}
            ${h ? html`<div class="hint" id="n-agent-hint">${cls ? cls[0].toUpperCase() + cls.slice(1) + '. ' : ''}A coding agent keeps its own instructions.</div>` : nothing}</div>
          ${h ? html`<div class="field"><label>Its sandbox — fixed for the conversation</label>
            <select id="n-sandbox" @change=${(e) => { f.ref = e.target.value; redraw(); }}>
              ${ref ? nothing : html`<option value="" selected>— none it fits: create one from the composer's ▣ —</option>`}
              ${opts.map((o) => html`<option value=${o.value} ?selected=${o.value === ref} ?disabled=${o.disabled} title=${o.why}>${o.label}${o.disabled ? ` — ${o.why}` : ''}</option>`)}</select></div>` : nothing}`;
      },
      body() {
        const h = coding();
        if (h && f.ref) app.harness.rememberSandbox(h.id, f.ref);
        return HS.newChatPick(app, agent(), h ? f.ref : '');
      },
    };
  },
});
