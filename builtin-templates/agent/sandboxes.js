// sandboxes.js — coding sandboxes (D115) on the web: the composer's sandbox
// picker (#ssel, beside the model picker — only where the class has the
// sandbox toolset: the open conversation's, or the next new chat's), the top
// bar's sandbox badge (#sbxbadge) with its popover (#sbxpop: the working
// directory, switching among the attached sandboxes, Detach, Manage… — a
// coding agent's conversation keeps its sandbox: its cwd read-only, no
// switch, no Detach), and
// the Sandboxes dialog (#sbxdlg: every sandbox you may see, with the
// lifecycle actions your rights allow, the create form, and sharing one with
// a terminal tile: #sbx-share), and terminals (<bx-terminal src> on the
// sandbox's manager, opened from the popover or a dialog row where the
// manager offers `tty`, as tabs of the terminal dock: terminals.js). What
// they say is model/sandboxes.js, what they do model/sandbox-store.js
// (app.sbx); the native view draws the same (its terminals through the
// tile's relay).
import { html, render, nothing } from '/vendor/lit-all.min.js';
import * as S from './model/sandboxes.js';
import { termDock } from './terminals.js';

const NEW = '+new';
const MANAGE = '+manage';

/**
 * makeSandboxUI wires the picker (sel: the composer's <select id="ssel">) and
 * the dialog (dlg: <dialog id="sbxdlg">); repaint() redraws the page (the top
 * bar carries the badge); popExtra(b, close) adds a section to the popover
 * (ports.js). The page calls paint(v) with its own paint,
 * badgeTpl(v) in its top bar, and closePop() on Escape.
 */
export function makeSandboxUI(app, { sel, dlg, repaint, popExtra }) {
  const pop = { open: false, ref: '', cwd: '', err: '' }; // cwd: the field, for the sandbox ref
  const dl = { form: null, share: null, err: '', msg: '', busy: '', bind: true, order: null }; // order: the rows as shown, kept while open; share: {ref, f}
  const draw = () => { if (dlg.open) render(dlgTpl(), dlg); };
  // The terminals: the page dials the managers itself (its frame token — the
  // manager sees the verified person), at the endpoints its `sandboxes` slot
  // has (multi: {endpoints}).
  const slot = globalThis.xbin && globalThis.xbin.iface ? globalThis.xbin.iface('sandboxes') : null;
  app.sbx.tty = (slot && slot.endpoints) || [];
  app.on('sandboxes', () => { repaint(); draw(); });
  app.on('class', () => paint()); // the class for new chats: the picker follows it at home

  // --- the composer's picker ------------------------------------------------------

  sel.addEventListener('focus', () => app.sbx.refresh());
  sel.addEventListener('change', async () => {
    const to = sel.value;
    if (to === NEW || to === MANAGE) { paint(); open({ create: to === NEW }); return; }
    const ask = app.sbx.confirmBind(to);
    if (ask && !confirm(ask)) { paint(); return; }
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
    // another sandbox became the active one: the field is its directory
    if (pop.open && pop.ref !== b.ref) { pop.ref = b.ref; pop.cwd = b.cwd; }
    return html`<span class="sbxwrap"><span class="badge sbxbadge ${b.broken ? 'broken' : ''}" id="sbxbadge" role="button" tabindex="0"
        aria-expanded=${pop.open ? 'true' : 'false'} title=${b.title} @click=${() => toggle(b)}
        @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(b); } }}>${b.label}${b.broken ? html`<bx-icon name="warning" label="broken"></bx-icon>` : nothing}</span>${pop.open ? popTpl(b) : nothing}</span>`;
  }
  function toggle(b) {
    pop.open = !pop.open;
    pop.ref = b.ref;
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
    const tt = app.sbx.terminal(b.ref, b.cwd);
    return html`<div class="mback" @click=${closePop}></div>
      <div class="sbxpop" id="sbxpop" role="dialog" aria-label="This conversation's sandbox">
        <div class="sbxhd"><b><bx-icon name="box"></bx-icon>${b.name}</b><span class="muted">${b.detail}</span></div>
        ${b.broken ? html`<div class="err" id="sbx-broken"><bx-icon name="warning"></bx-icon><span>${b.broken} — ${b.advice}</span></div>` : nothing}
        ${b.fixed ? html`<div class="field"><label>Working directory</label>
          <div class="mono" id="sbx-cwd-fixed">${b.cwd || 'its workdir'}</div>
          <div class="hint">Fixed for this conversation: a coding agent keeps the sandbox and directory it started in.</div></div>`
        : html`<div class="field"><label>Working directory</label>
          <div class="sbxcwd"><input id="sbx-cwd" class="mono" .value=${pop.cwd} placeholder="the sandbox's workdir" ?disabled=${!b.canChange}
              @input=${(e) => { pop.cwd = e.target.value; }} @keydown=${(e) => { if (e.key === 'Enter') setCwd(b); }}>
            <button class="btn btnsm" id="sbx-cwd-set" ?disabled=${!b.canChange} @click=${() => setCwd(b)}>Set</button></div>
          <div class="hint">The tools work there from the agent's next turn.</div></div>`}
        ${b.attached.length > 1 && !b.fixed ? html`<div class="field"><label>Attached — the agent works in one at a time</label>
          ${b.attached.map((a) => html`<div class="sbxatt ${a.on ? 'on' : ''}" data-ref=${a.ref} title=${a.broken || (a.on ? 'the active one' : 'make it the active one')}
              @click=${() => { if (!a.on && b.canChange) run(() => app.sbx.choose(a.ref, a.cwd)); }}>
            <span class="dot ${a.on ? 'on' : ''}"></span>${a.name}${a.cwd ? html` <span class="mono muted">${a.cwd}</span>` : nothing}${a.broken
              ? html`<bx-icon class="gone" name="warning" label=${a.broken}></bx-icon>` : nothing}</div>`)}</div>` : nothing}
        ${pop.err ? html`<div class="err" id="sbx-err">${pop.err}</div>` : nothing}
        ${popExtra ? popExtra(b, closePop) : nothing}
        ${tt.shown && tt.why ? html`<div class="hint" id="sbx-term-why">No terminal: ${tt.why}.</div>` : nothing}
        <div class="sbxacts">
          ${b.fixed ? nothing : html`<button class="btn rm btnsm" id="sbx-detach" ?disabled=${!b.canChange} title="Take it off this conversation (the sandbox stays)"
            @click=${() => run(() => app.sbx.detach(b.ref), true)}>Detach</button>`}
          ${tt.shown ? html`<button class="btn btnsm" id="sbx-term" ?disabled=${!!tt.why}
            title=${tt.why || `a shell in ${b.name} at ${b.cwd || 'its workdir'}, as you`}
            @click=${() => { pop.open = false; repaint(); openTerm(tt); }}>Open terminal</button>` : nothing}
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
    dl.share = null;
    dl.err = ''; dl.msg = ''; dl.busy = ''; dl.bind = true;
    if (!dlg.open) { dl.order = null; dlg.showModal(); } // its rows sorted afresh when it opens, then kept
    draw();
    app.sbx.load(true).catch(() => {});
    repaint();
  }

  async function act(r, a) {
    if (a.id === 'terminal') { dlg.close(); openTerm(app.sbx.terminal(r.ref, a.cwd)); return; }
    if (a.id === 'shareTerm') { dl.share = { ref: r.ref, f: {} }; dl.form = null; dl.err = ''; dl.msg = ''; draw(); return; }
    if (a.confirm && !confirm(a.confirm)) return;
    dl.busy = r.ref; dl.err = ''; dl.msg = '';
    draw();
    try { dl.msg = await app.sbx.perform(r.ref, a.id, r.name); } catch (e) { dl.err = `${r.name}: ${e.message}`; }
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
      const s = await app.sbx.create(vm.f, { bind: dl.bind });
      dl.form = null;
      dl.msg = app.sbx.created(s, dl.bind);
    } catch (e) { dl.err = e.message; }
    dl.busy = '';
    draw();
  }

  function dlgTpl() {
    const L = app.sbx.list;
    const rows = app.sbx.rows(dl.order);
    dl.order = rows.map((r) => r.ref);
    const cant = app.sbx.createWhy();
    return html`<div class="dlg-hd sbxdhd"><bx-icon name="box"></bx-icon>Sandboxes<span style="flex:1"></span>
        <button class="btn ghost btnsm icon" id="sbx-refresh" title="Read them again from their managers" aria-label="Read them again" @click=${() => app.sbx.load(true)}><bx-icon name="refresh"></bx-icon></button>
        <button class="btn ghost btnsm icon" id="sbx-close" title="Close" aria-label="Close" @click=${() => dlg.close()}><bx-icon name="xmark"></bx-icon></button></div>
      <div class="dlg-bd sbxbd">
        ${app.sbx.error ? html`<div class="err">${app.sbx.error}</div>` : nothing}
        ${L.managers.filter((m) => m.ok === false).map((m) => html`<div class="err">${m.title || m.provider}: ${m.error || 'unavailable'}</div>`)}
        ${!L.loaded && !app.sbx.error ? html`<div class="muted">loading…</div>` : nothing}
        ${L.loaded && !L.managers.length ? html`<div class="hint" id="sbx-nomgr">No sandbox manager is bound — bind one to this agent's
          <span class="mono">sandboxes</span> slot (the binding panel, or <span class="mono">bx bind</span>).</div>` : nothing}
        <div class="sbxlist" id="sbx-list">${rows.map((r) => rowTpl(r))}</div>
        ${L.loaded && !rows.length ? html`<div class="muted" id="sbx-empty">No sandboxes yet.</div>` : nothing}
        ${dl.msg ? html`<div class="muted" id="sbx-msg">${dl.msg}</div>` : nothing}
        ${dl.err && !dl.form && !dl.share ? html`<div class="err" id="sbx-err">${dl.err}</div>` : nothing}
        ${dl.share ? shareTpl() : dl.form ? formTpl() : html`<div><button class="btn btnsm" id="sbx-new" ?disabled=${!!cant} title=${cant}
          @click=${() => { dl.form = {}; dl.err = ''; dl.msg = ''; draw(); }}><bx-icon name="plus"></bx-icon>New sandbox</button>
          ${cant === S.VIEW_ONLY ? html`<span class="hint" id="sbx-new-why">${S.sentence(cant)}</span>` : nothing}</div>`}
      </div>`;
  }

  function rowTpl(r) {
    const facts = [r.manager, r.image, r.size, r.egressLabel, `owner: ${r.owner}`, r.lastLabel && `active ${r.lastLabel}`,
      r.bound ? `in ${r.bound} conversation${r.bound === 1 ? '' : 's'}` : '', r.sharedWith.length ? `shared with ${r.sharedWith.join(', ')}` : '']
      .filter(Boolean).join(' · ');
    // the name and its badges on the left; the actions one group on the
    // right, wrapping within itself (under it on a phone); the facts below
    return html`<div class="sbxrow ${r.active ? 'on' : ''}" data-ref=${r.ref}>
      <div class="l1">
        <div class="hd"><b class="nm">${r.name}</b>
          <span class="badge sbxst ${r.state}" title=${r.stateDetail}>${r.stateLabel}</span>
          <span class="badge">${r.visLabel}</span>
          ${r.where ? html`<span class="badge ${r.active ? 'sbxon' : ''}">${r.where}</span>` : nothing}</div>
        ${r.actions.length ? html`<div class="acts">${r.actions.map((a) => html`<button class="btn btnsm ${a.danger ? 'rm' : 'ghost'}"
          data-act=${a.id} ?disabled=${!!dl.busy} @click=${() => act(r, a)}>${a.label}</button>`)}</div>` : nothing}
      </div>
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

  // "Share with a terminal tile…": the tile's path, who it is for, the shares
  // it has now (each can be stopped) — model/sandboxes.js shareForm.
  function shareTpl() {
    const { ref } = dl.share;
    const row = app.sbx.list.sandboxes.find((x) => x.ref === ref);
    const vm = app.sbx.shareForm(ref, dl.share.f);
    const go = async () => {
      dl.busy = 'share'; dl.err = ''; draw();
      try { dl.msg = await app.sbx.shareTerminal(ref, dl.share.f); dl.share = null; } catch (e) { dl.err = e.message; }
      dl.busy = ''; draw();
    };
    const stop = async (c) => {
      if (!confirm(`Stop sharing “${(row && row.name) || ref}” with ${c.consumer}? Its terminals there can't be opened again.`)) return;
      dl.busy = 'share'; dl.err = ''; draw();
      try { await app.sbx.unshare(ref, c.consumer); } catch (e) { dl.err = e.message; }
      dl.busy = ''; draw();
    };
    return html`<div class="sec sbxform" id="sbx-share" data-ref=${ref}><h4>Share “${(row && row.name) || S.splitRef(ref).id}” with a terminal tile</h4>
      <div class="hint">A terminal tile — the builtin <span class="mono">sandbox-terminal</span> — gives people terminals onto it in the
        browser and over SSH. It checks who may use the sandbox as well: a share never widens that.</div>
      <div class="row2">
        <div class="field"><label>The terminal tile's path</label><input id="sbxs-tile" class="mono" .value=${vm.tile}
          placeholder=${S.TERMINAL_TILE} @input=${(e) => { dl.share.f = { tile: e.target.value }; draw(); }}></div>
        <div class="field"><label>For</label><div class="muted" id="sbxs-who">${vm.usersLabel}</div></div>
      </div>
      ${vm.current.length ? html`<div class="field"><label>Shared with now</label>${vm.current.map((c) => html`<div class="sbxshr" data-consumer=${c.consumer}>
        <span class="mono">${c.consumer}</span> <span class="muted">${c.usersLabel}</span>
        <button class="btn ghost btnsm rm" data-unshare=${c.consumer} ?disabled=${!!dl.busy} @click=${() => stop(c)}>Stop sharing</button></div>`)}</div>` : nothing}
      ${dl.err || (vm.error && vm.tile) ? html`<div class="err" id="sbxs-err">${dl.err || vm.error}</div>` : nothing}
      <div><button class="btn" id="sbxs-share" ?disabled=${!vm.ok || !!dl.busy} @click=${go}>${dl.busy === 'share' ? 'Sharing…' : 'Share'}</button>
        <button class="btn ghost" id="sbxs-cancel" @click=${() => { dl.share = null; dl.err = ''; draw(); }}>Cancel</button></div>
    </div>`;
  }

  // --- terminals: a tab of the dock (terminals.js) --------------------------------------

  // openTerm shows a terminal for t (app.sbx.terminal(): its src) in a new tab.
  const openTerm = (t) => termDock(app).open(t);

  return { paint, badgeTpl, closePop, open, openTerm, get popOpen() { return pop.open; } };
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
