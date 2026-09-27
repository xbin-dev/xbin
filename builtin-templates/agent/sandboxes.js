// sandboxes.js — coding sandboxes (D115) on the web: the composer's sandbox
// picker (#ssel, beside the model picker — only where the class has the
// sandbox toolset: the open conversation's, or the next new chat's), the top
// bar's ▣ badge (#sbxbadge) with its popover (#sbxpop: the working
// directory, switching among the attached sandboxes, Detach, Manage…), and
// the Sandboxes dialog (#sbxdlg: every sandbox you may see, with the
// lifecycle actions your rights allow, and the create form). What they say
// is model/sandboxes.js, what they do model/sandbox-store.js (app.sbx); the
// native view draws the same. "Open terminal" waits for phase 3 (a
// bx-terminal src): not offered.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import * as S from './model/sandboxes.js';

const NEW = '+new';
const MANAGE = '+manage';

/**
 * makeSandboxUI wires the picker (sel: the composer's <select id="ssel">) and
 * the dialog (dlg: <dialog id="sbxdlg">); repaint() redraws the page (the top
 * bar carries the badge). The page calls paint(v) with its own paint,
 * badgeTpl(v) in its top bar, and closePop() on Escape.
 */
export function makeSandboxUI(app, { sel, dlg, repaint }) {
  const pop = { open: false, cwd: '', err: '' };
  const dl = { form: null, err: '', msg: '', busy: '', bind: true };
  const draw = () => { if (dlg.open) render(dlgTpl(), dlg); };
  app.on('sandboxes', () => { repaint(); draw(); });
  app.on('class', () => paint()); // the class for new chats: the picker follows it at home

  // --- the composer's picker ------------------------------------------------------

  sel.addEventListener('focus', () => app.sbx.refresh());
  sel.addEventListener('change', async () => {
    const to = sel.value;
    if (to === NEW || to === MANAGE) { paint(); open({ create: to === NEW }); return; }
    try { await app.sbx.choose(to); } catch (e) { alert(e.message); }
    paint();
  });

  function paint() {
    const p = app.sbx.picker();
    sel.hidden = !p.shown;
    if (!p.shown) return;
    app.sbx.ensure();
    sel.disabled = p.disabled;
    sel.title = p.notes.length ? `${p.title}\n${p.notes.join('\n')}` : p.title;
    render(optionsTpl(p), sel);
    if (sel.value !== p.value) sel.value = p.value;
  }

  // --- the top bar's badge and its popover ------------------------------------------------

  function badgeTpl(v) {
    const b = app.sbx.badge(v);
    if (!b) { pop.open = false; return nothing; }
    app.sbx.ensure();
    return html`<span class="sbxwrap"><span class="badge sbxbadge ${b.broken ? 'broken' : ''}" id="sbxbadge" role="button" tabindex="0"
        title=${b.title} @click=${() => toggle(b)}>${b.label}${b.broken ? ' ⚠' : ''}</span>${pop.open ? popTpl(b) : nothing}</span>`;
  }
  function toggle(b) {
    pop.open = !pop.open;
    pop.cwd = b.cwd;
    pop.err = '';
    if (pop.open) app.sbx.refresh();
    repaint();
  }
  function closePop() {
    if (!pop.open) return false;
    pop.open = false;
    repaint();
    return true;
  }
  // run: a popover action; close it when it is done (the badge says the rest).
  const run = async (fn, done = false) => {
    pop.err = '';
    try { await fn(); if (done) pop.open = false; } catch (e) { pop.err = e.message; }
    repaint();
  };
  const setCwd = (b) => run(() => app.sbx.setCwd(pop.cwd).then(() => { pop.cwd = S.bindingOf(app.session.current())?.cwd || pop.cwd; }));

  function popTpl(b) {
    return html`<div class="mback" @click=${closePop}></div>
      <div class="sbxpop" id="sbxpop" role="dialog" aria-label="This conversation's sandbox">
        <div class="sbxhd"><b>${S.ICON} ${b.name}</b><span class="muted">${b.detail}</span></div>
        ${b.broken ? html`<div class="err" id="sbx-broken">⚠ ${b.broken} — pick another, or detach it</div>` : nothing}
        <div class="field"><label>Working directory</label>
          <div class="sbxcwd"><input id="sbx-cwd" class="mono" .value=${pop.cwd} placeholder="the sandbox's workdir" ?disabled=${!b.canChange}
              @input=${(e) => { pop.cwd = e.target.value; }} @keydown=${(e) => { if (e.key === 'Enter') setCwd(b); }}>
            <button class="btn btnsm" id="sbx-cwd-set" ?disabled=${!b.canChange} @click=${() => setCwd(b)}>Set</button></div>
          <div class="hint">The tools work there from the agent's next turn.</div></div>
        ${b.attached.length > 1 ? html`<div class="field"><label>Attached — the agent works in one at a time</label>
          ${b.attached.map((a) => html`<div class="sbxatt ${a.on ? 'on' : ''}" data-ref=${a.ref} title=${a.broken || (a.on ? 'the active one' : 'make it the active one')}
              @click=${() => { if (!a.on && b.canChange) run(() => app.sbx.choose(a.ref, a.cwd)); }}>
            ${a.on ? '●' : '○'} ${a.name}${a.cwd ? html` <span class="mono muted">${a.cwd}</span>` : nothing}${a.broken ? ' ⚠' : ''}</div>`)}</div>` : nothing}
        ${pop.err ? html`<div class="err" id="sbx-err">${pop.err}</div>` : nothing}
        <div class="sbxacts">
          <button class="btn rm btnsm" id="sbx-detach" ?disabled=${!b.canChange} title="Take it off this conversation (the sandbox stays)"
            @click=${() => run(() => app.sbx.detach(b.ref), true)}>Detach</button>
          <span style="flex:1"></span>
          <button class="btn ghost btnsm" id="sbx-manage" @click=${() => { pop.open = false; open(); }}>Manage…</button>
        </div>
      </div>`;
  }

  // --- the Sandboxes dialog ---------------------------------------------------------------

  // open shows it — with the create form open when create is set — and reads
  // the list afresh from the managers.
  function open({ create = false } = {}) {
    dl.form = create ? {} : null;
    dl.err = ''; dl.msg = ''; dl.busy = ''; dl.bind = true;
    if (!dlg.open) dlg.showModal();
    draw();
    app.sbx.load(true).catch(() => {});
    repaint();
  }

  async function act(r, a) {
    if (a.confirm && !confirm(a.confirm)) return;
    dl.busy = r.ref; dl.err = ''; dl.msg = '';
    draw();
    try {
      if (a.id === 'use') {
        await app.sbx.choose(r.ref);
        dl.msg = app.session.current() ? `${r.name} is this conversation's sandbox from its next turn ✓` : `${r.name} is your next new chat's sandbox ✓`;
      } else if (a.id === 'delete') {
        await app.sbx.remove(r.ref);
        dl.msg = `deleted ${r.name}`;
      } else if (a.id === 'team' || a.id === 'private') {
        await app.sbx.share(r.ref, a.id);
      } else {
        await app.sbx.act(r.ref, a.id);
      }
    } catch (e) { dl.err = `${r.name}: ${e.message}`; }
    dl.busy = '';
    draw();
  }

  async function create() {
    const vm = app.sbx.form(dl.form);
    dl.err = vm.error;
    if (dl.err) return draw();
    dl.busy = 'create';
    draw();
    try {
      const here = !!app.session.current();
      const s = await app.sbx.create(vm.f, { bind: dl.bind });
      dl.form = null;
      dl.msg = `created ${s.name}${dl.bind ? (here ? ' — this conversation works in it from its next turn' : ' — your next new chat starts in it') : ''} ✓`;
    } catch (e) { dl.err = e.message; }
    dl.busy = '';
    draw();
  }

  function dlgTpl() {
    const L = app.sbx.list;
    const rows = app.sbx.rows();
    const usable = L.managers.some((m) => m.ok !== false);
    return html`<div class="dlg-hd sbxdhd">${S.ICON} Sandboxes<span style="flex:1"></span>
        <button class="btn ghost btnsm" id="sbx-refresh" title="Read them again from their managers" @click=${() => app.sbx.load(true)}>↻</button>
        <button class="btn ghost btnsm" id="sbx-close" title="Close" @click=${() => dlg.close()}>✕</button></div>
      <div class="dlg-bd sbxbd">
        ${app.sbx.error ? html`<div class="err">${app.sbx.error}</div>` : nothing}
        ${L.managers.filter((m) => m.ok === false).map((m) => html`<div class="err">${m.title || m.provider}: ${m.error || 'unavailable'}</div>`)}
        ${!L.loaded && !app.sbx.error ? html`<div class="muted">loading…</div>` : nothing}
        ${L.loaded && !L.managers.length ? html`<div class="hint" id="sbx-nomgr">No sandbox manager is bound — bind one to this agent's
          <span class="mono">sandboxes</span> slot (the binding panel, or <span class="mono">bx bind</span>).</div>` : nothing}
        <div class="sbxlist" id="sbx-list">${rows.map((r) => rowTpl(r))}</div>
        ${L.loaded && !rows.length ? html`<div class="muted" id="sbx-empty">No sandboxes yet.</div>` : nothing}
        ${dl.msg ? html`<div class="muted" id="sbx-msg">${dl.msg}</div>` : nothing}
        ${dl.err && !dl.form ? html`<div class="err" id="sbx-err">${dl.err}</div>` : nothing}
        ${dl.form ? formTpl() : html`<div><button class="btn btnsm" id="sbx-new" ?disabled=${!usable}
          @click=${() => { dl.form = {}; dl.err = ''; dl.msg = ''; draw(); }}>＋ New sandbox</button></div>`}
      </div>`;
  }

  function rowTpl(r) {
    const facts = [r.manager, r.image, r.size, r.egressLabel, `owner: ${r.owner}`, r.lastLabel && `active ${r.lastLabel}`,
      r.bound ? `in ${r.bound} conversation${r.bound === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ');
    return html`<div class="sbxrow ${r.active ? 'on' : ''}" data-ref=${r.ref}>
      <div class="l1"><b class="nm">${r.name}</b>
        <span class="badge sbxst ${r.state}" title=${r.stateDetail}>${r.stateLabel}</span>
        <span class="badge">${r.visLabel}</span>
        ${r.active ? html`<span class="badge sbxon">active here</span>` : r.here ? html`<span class="badge">attached here</span>` : nothing}
        <span style="flex:1"></span>
        ${r.actions.map((a) => html`<button class="btn btnsm ${a.danger ? 'rm' : 'ghost'}" data-act=${a.id} ?disabled=${!!dl.busy}
          @click=${() => act(r, a)}>${a.label}</button>`)}</div>
      <div class="l2 muted">${facts}</div>
    </div>`;
  }

  function formTpl() {
    const vm = app.sbx.form(dl.form);
    const f = vm.f;
    const here = !!app.session.current();
    const set = (k, redraw = true) => (e) => { dl.form = { ...vm.f, ...dl.form, [k]: e.target.value }; if (redraw) draw(); };
    const opts = (list, value) => list.map((o) => html`<option value=${o.value} ?selected=${o.value === value} ?disabled=${o.disabled}
      title=${o.why || ''}>${o.label}${o.disabled ? ' — not in this class' : ''}</option>`);
    return html`<div class="sec sbxform" id="sbx-form"><h4>New sandbox${here ? ' — for this conversation' : ''}</h4>
      ${vm.managers.length > 1 ? html`<div class="field"><label>Manager</label>
        <select id="sbxf-provider" @change=${set('provider')}>${opts(vm.managers, f.provider)}</select></div>` : nothing}
      <div class="row2">
        <div class="field"><label>Name</label><input id="sbxf-name" .value=${f.name} placeholder="api-dev" @input=${set('name', false)}></div>
        <div class="field"><label>Working directory — optional</label>
          <input id="sbxf-cwd" class="mono" .value=${f.cwd} placeholder="its workdir" @input=${set('cwd', false)}></div>
      </div>
      <div class="grid4">
        ${vm.images.length ? html`<div class="field"><label>Image</label><select id="sbxf-image" @change=${set('image')}>${opts(vm.images, f.image)}</select></div>` : nothing}
        ${vm.sizes.length ? html`<div class="field"><label>Size</label><select id="sbxf-size" @change=${set('size')}>${opts(vm.sizes, f.size)}</select></div>` : nothing}
        <div class="field"><label>Network</label><select id="sbxf-egress" @change=${set('egress')}>${opts(vm.egress, f.egress)}</select></div>
        <div class="field"><label>Who may use it</label><select id="sbxf-vis" @change=${set('visibility')}>
          <option value="private" ?selected=${f.visibility !== 'team'}>you (and people you add)</option>
          <option value="team" ?selected=${f.visibility === 'team'}>the team</option></select></div>
      </div>
      <label class="chk"><input type="checkbox" id="sbxf-bind" .checked=${dl.bind} @change=${(e) => { dl.bind = e.target.checked; }}>
        ${here ? 'Work in it in this conversation, from its next turn' : 'Start your next new chat in it'}</label>
      ${dl.err ? html`<div class="err" id="sbxf-err">${dl.err}</div>` : nothing}
      <div><button class="btn" id="sbxf-create" ?disabled=${dl.busy === 'create'} @click=${create}>${dl.busy === 'create' ? 'Creating…' : 'Create'}</button>
        <button class="btn ghost" id="sbxf-cancel" @click=${() => { dl.form = null; dl.err = ''; draw(); }}>Cancel</button></div>
    </div>`;
  }

  return { paint, badgeTpl, closePop, open, get popOpen() { return pop.open; } };
}

// optionsTpl: the picker's <option>s — none, the groups, then the actions.
function optionsTpl(p) {
  return html`<option value="" ?selected=${!p.value}>${S.ICON} ${p.none.label}</option>
    ${p.groups.map((g) => html`<optgroup label=${g.label}>${g.rows.map((r) => html`<option value=${r.value} ?selected=${r.on}
      ?disabled=${r.disabled} title=${r.why || r.detail}>${S.ICON} ${r.label}</option>`)}</optgroup>`)}
    ${p.loading ? html`<option disabled value="+loading">loading…</option>` : nothing}
    <optgroup label="Sandboxes">${p.actions.map((a) => html`<option value=${a.id === 'new' ? NEW : MANAGE} ?disabled=${a.disabled}
      title=${a.why}>${a.label}</option>`)}</optgroup>`;
}
