/**
 * bx-first-run.js — <bx-first-run>, the first-run sheet (/vendor/bx-first-run.js,
 * D236; docs/auth.md §Owner login mechanics): xbin's own chrome over the shell, shown
 * only in the bootstrap state (GET /api/xbin/first-run — the owner token's
 * browser session, no accounts yet), where the owner's first visit becomes
 * their account, the vault and their workspace agent in one sheet:
 *
 *   your name, a password                         POST /users {role: admin}, then
 *                                                 POST /first-run/session: this browser signs in as it
 *   the vault's passphrase, twice                 POST /vault-unseal, as the account — only when the
 *                                                 vault is unconfigured (once, to unlock, when sealed;
 *                                                 none when the host unseals it)
 *   "Create my account and turn on my workspace   PUT /delegate/workspace {enabled: true, who: "me"},
 *    agent" (the default action)                  as the account — only with a model and the switch
 *                                                 never set; the quiet button leaves it off
 *
 * then the page reloads as the account (sessionStorage DONE_KEY: the
 * shell's hook opens the dock and gives the agent's composer the keys).
 * A step after the account that fails is said as what is left — the
 * person is signed in by then, never locked out. "Not now" is remembered
 * in this browser (localStorage SKIP_KEY) and the element removes itself
 * (event bx-first-run-skip). Rendered into the light DOM, in
 * document.body, so a password manager sees an ordinary sign-up form:
 * autocomplete name, username and new-password on the account's form;
 * the vault's fields belong to a form of their own and say they aren't a
 * sign-in.
 *
 * Keyboard-first: focus starts in the name, Enter moves on (a field that
 * isn't right yet says why in place), Enter in the last field is the
 * primary button. Errors sit under their field (aria-describedby).
 *
 *   testApi()   {plan, errors, busy, left, made, editingId, fields: {name, id}}
 */
import { LitElement, html, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { WORDS, SKIP_KEY, DONE_KEY, signInName, plan, check, fieldOf } from '/vendor/first-run-words.js';

async function call(path, { method = 'GET', body } = {}) {
  try {
    const r = await fetch('/api/xbin' + path, {
      method, credentials: 'same-origin', cache: 'no-store',
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    let data = null;
    try { data = await r.json(); } catch { /* not JSON */ }
    return { ok: r.ok, status: r.status, data, error: (data && data.error) || (r.ok ? '' : `xbin answered ${r.status}`) };
  } catch {
    return { ok: false, status: 0, data: null, error: WORDS.errors.offline };
  }
}

// one stylesheet for the light DOM, scoped by the element's name; theme tokens only
const CSS = `
bx-first-run { position: fixed; inset: 0; z-index: 1000; display: flex; overflow: auto; padding: 24px 16px; box-sizing: border-box;
  background: var(--bx-scrim); color: var(--bx-text); font: var(--bx-font); }
bx-first-run * { box-sizing: border-box; }
bx-first-run .plate { margin: auto; width: min(480px, 100%); background: var(--bx-panel); border: 1px solid var(--bx-border-strong);
  border-top: 3px solid var(--bx-text); border-radius: var(--bx-radius); box-shadow: var(--bx-shadow-pop); }
bx-first-run .xbar { display: flex; align-items: center; gap: 8px; padding: 4px 16px; background: var(--bx-text); color: var(--bx-bg);
  font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; }
bx-first-run .main { padding: 16px 20px 20px; display: flex; flex-direction: column; gap: 4px; }
bx-first-run h1 { margin: 0; font: var(--bx-font-heading); overflow-wrap: anywhere; }
bx-first-run p { margin: 0; overflow-wrap: anywhere; }
bx-first-run .lead { margin: 4px 0 8px; color: var(--bx-muted); }
bx-first-run form { display: contents; }
bx-first-run label { display: block; margin: 10px 0 4px; font: var(--bx-font-ui); font-weight: 600; }
bx-first-run input { width: 100%; min-height: var(--bx-control-h); padding: 4px 8px; background: var(--bx-panel); color: var(--bx-text);
  border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); font: var(--bx-font); }
bx-first-run input[aria-invalid="true"] { border-color: var(--bx-danger); }
bx-first-run input:disabled { opacity: .6; }
bx-first-run :focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
bx-first-run .hint { margin-top: 4px; color: var(--bx-muted); font: var(--bx-font-meta); overflow-wrap: anywhere; }
bx-first-run .hint b { color: var(--bx-text); font-weight: 600; }
bx-first-run .err { display: flex; gap: 6px; align-items: flex-start; margin-top: 4px; color: var(--bx-danger); font: var(--bx-font-meta); overflow-wrap: anywhere; }
bx-first-run .err bx-icon { flex: none; }
bx-first-run fieldset { margin: 14px 0 0; padding: 10px 12px 12px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); min-width: 0; }
bx-first-run legend { padding: 0 4px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
bx-first-run fieldset > label:first-of-type { margin-top: 6px; }
bx-first-run .agent { display: flex; gap: 8px; align-items: flex-start; margin: 14px 0 0; padding: 10px 12px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); }
bx-first-run .agent bx-icon { flex: none; margin-top: 1px; }
bx-first-run .agent .more { margin-top: 4px; color: var(--bx-muted); }
bx-first-run .agent a { color: var(--bx-link); }
bx-first-run .btns { display: flex; flex-direction: column; gap: 8px; margin-top: 16px; }
bx-first-run button, bx-first-run a.btn { display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-height: var(--bx-control-h); width: 100%;
  padding: 6px 12px; border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); background: var(--bx-panel); color: var(--bx-text);
  font: var(--bx-font); font-weight: 600; text-align: center; text-decoration: none; cursor: pointer; white-space: normal; }
bx-first-run button:hover:not(:disabled), bx-first-run a.btn:hover { background: var(--bx-hover); }
bx-first-run button.primary, bx-first-run a.btn.primary { background: var(--bx-accent); border-color: var(--bx-accent); color: var(--bx-accent-ink); }
bx-first-run button.primary:hover:not(:disabled), bx-first-run a.btn.primary:hover { background: var(--bx-accent-hover); border-color: var(--bx-accent-hover); }
bx-first-run button.quiet { background: transparent; border-color: transparent; }
bx-first-run button.quiet:hover:not(:disabled) { background: transparent; color: var(--bx-accent); }
bx-first-run button.link { display: inline; width: auto; min-height: 0; padding: 0; border: 0; background: none; color: var(--bx-link); font: inherit; font-weight: 400; text-decoration: underline; }
bx-first-run button.link:hover:not(:disabled) { background: none; }
bx-first-run button:disabled { opacity: .55; cursor: default; }
bx-first-run .skip { margin-top: 12px; text-align: center; }
bx-first-run .token { margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--bx-border); }
bx-first-run .status { margin-top: 10px; color: var(--bx-muted); font: var(--bx-font-meta); min-height: 16px; }
bx-first-run ul.left { margin: 8px 0 0; padding-left: 20px; display: flex; flex-direction: column; gap: 6px; }
bx-first-run .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 520px) {
  bx-first-run { padding: 0; }
  bx-first-run .plate { width: 100%; min-height: 100%; border-left: 0; border-right: 0; border-bottom: 0; border-radius: 0; box-shadow: none; }
  bx-first-run .main { padding: 14px 16px 24px; }
}
`;
function style() {
  if (document.getElementById('bx-first-run-css')) return;
  const s = document.createElement('style');
  s.id = 'bx-first-run-css';
  s.textContent = CSS;
  document.head.append(s);
}

// the field keys, with each one's input id
const IDS = { name: 'bxfr-name', id: 'bxfr-id', password: 'bxfr-pw', vault: 'bxfr-vault', repeat: 'bxfr-repeat', form: 'bxfr-form' };

export class BxFirstRun extends LitElement {
  static properties = {
    plan: { attribute: false },
    _f: { state: true }, _err: { state: true }, _busy: { state: true }, _editId: { state: true }, _left: { state: true }, _made: { state: true },
  };

  createRenderRoot() { return this; }

  constructor() {
    super();
    this.plan = null;
    this._f = { name: '', id: '', password: '', vault: '', repeat: '' };
    this._err = {}; this._busy = ''; this._editId = false; this._left = null; this._made = null;
    this._idTouched = false; this._on = false; this._focused = false;
  }

  connectedCallback() {
    super.connectedCallback();
    style();
    if (!this.plan) this.load();
  }

  /** load(): what the sheet asks, read from xbind (the vault, the model, the workspace agent's switch). */
  async load() {
    const [v, l, w] = await Promise.all([call('/vault-status'), call('/llm'), call('/delegate/workspace')]);
    this.plan = plan({ vault: v.ok ? v.data : null, llm: l.ok ? l.data : null, ws: w.ok ? w.data : null });
  }

  updated() {
    if (this.plan && !this._focused) { this._focused = true; this._focus('name'); }
  }

  testApi() {
    return { plan: this.plan, errors: { ...this._err }, busy: this._busy, left: this._left, made: this._made, editingId: this._editId, fields: { name: this._f.name, id: this._f.id } };
  }

  _focus(k) { this.updateComplete.then(() => { const el = this.querySelector(`#${IDS[k] || IDS.name}`); if (el) el.focus(); }); }

  _order() {
    const v = this.plan && this.plan.vault;
    return ['name', ...(this._editId ? ['id'] : []), 'password', ...(v === 'new' ? ['vault', 'repeat'] : v === 'unlock' ? ['vault'] : [])];
  }

  _set(k, value) {
    const f = { ...this._f, [k]: value };
    if (k === 'name' && !this._idTouched) f.id = signInName(value);
    if (k === 'id') { this._idTouched = true; f.id = value.trim().toLowerCase(); }
    this._f = f;
    if (this._err[k] || this._err.form) { const e = { ...this._err }; delete e[k]; delete e.form; this._err = e; }
  }

  _key(e) {
    if (e.key !== 'Enter' || e.isComposing || this._busy || e.target.localName !== 'input') return;
    e.preventDefault();
    const k = e.target.dataset.k;
    const err = check(this._f, this.plan, k);
    if (k === 'name' && !err.name && !this._f.id) { this._editId = true; this._err = { ...this._err, id: WORDS.noName }; this._focus('id'); return; }
    if (err[k]) { this._err = { ...this._err, [k]: err[k] }; return; }
    const order = this._order(), next = order[order.indexOf(k) + 1];
    if (next) this._focus(next); else this._submit(true);
  }

  _fail(k, msg) {
    this._busy = '';
    if (k === 'id') this._editId = true;
    this._err = { [k]: msg };
    this._focus(k);
  }

  async _submit(withAgent) {
    const p = this.plan;
    if (this._busy || !p) return;
    const errs = check(this._f, p);
    if (errs.id) this._editId = true; // a name that gives no sign-in name: the field to choose one
    const first = this._order().find((k) => errs[k]);
    if (first) {
      this._err = errs;
      this._focus(first);
      return;
    }
    this._err = {};
    const f = this._f, id = f.id;
    // a locked vault first: its passphrase is the one thing here that can be mistyped and typed again
    if (p.vault === 'unlock') {
      this._busy = WORDS.busy.vault;
      const r = await call('/vault-unseal', { method: 'POST', body: { passphrase: f.vault } });
      if (!r.ok) return this._fail('vault', r.status === 403 ? WORDS.errors.wrongUnlock : r.error);
      this.plan = { ...p, vault: '' }; // open now: not asked again if the account needs another try
    }
    this._busy = WORDS.busy.account;
    let r = await call('/users', { method: 'POST', body: { id, name: f.name.trim(), role: 'admin', password: f.password } });
    if (!r.ok) return this._fail(fieldOf('account', r.status, r.error), r.error);
    this._busy = WORDS.busy.signin;
    r = await call('/first-run/session', { method: 'POST', body: { id, password: f.password } });
    if (!r.ok) { this._busy = ''; this._made = { id, why: r.error }; return; }
    // signed in as the account from here: what fails now is said as what is left
    const left = [];
    if (p.vault === 'new') {
      this._busy = WORDS.busy.vault;
      const v = await call('/vault-unseal', { method: 'POST', body: { passphrase: f.vault } });
      if (!v.ok) left.push(WORDS.vaultLeft(v.error));
    }
    this._on = p.agent.kind === 'host-on';
    if (withAgent && p.agent.kind === 'offer') {
      this._busy = WORDS.busy.agent;
      const a = await call('/delegate/workspace', { method: 'PUT', body: p.agent.body });
      if (a.ok) this._on = true; else left.push(WORDS.agentLeft(a.error));
    }
    this.dispatchEvent(new CustomEvent('bx-first-run-made', { bubbles: true, detail: { user: id, agent: this._on, left: left.length } }));
    if (left.length) { this._busy = ''; this._left = left; this.updateComplete.then(() => this.querySelector('[data-continue]')?.focus()); return; }
    this._finish();
  }

  _finish() {
    try { sessionStorage.setItem(DONE_KEY, this._on ? 'agent' : 'done'); } catch { /* no storage: the dock stays closed */ }
    this._busy = WORDS.busy.done;
    location.replace('/'); // the shell again, as the account — not at the token's screen, which isn't theirs
  }

  _skip() {
    try { localStorage.setItem(SKIP_KEY, '1'); } catch { /* no storage: asked again next time */ }
    this.dispatchEvent(new CustomEvent('bx-first-run-skip', { bubbles: true }));
    this.remove();
  }

  _field(k, label, input, hint = nothing) {
    const err = this._err[k];
    return html`<label for=${IDS[k]}>${label}</label>${input(err)}${hint}
      ${err ? html`<div class="err" id=${`${IDS[k]}-err`} data-err=${k}><bx-icon name="error"></bx-icon><span>${err}</span></div>` : nothing}`;
  }

  _input(k, { type = 'text', ac, form, extra = {} } = {}) {
    const err = this._err[k], hint = extra.hint ? `${IDS[k]}-hint` : '';
    return html`<input id=${IDS[k]} data-k=${k} name=${extra.name || k} type=${type} autocomplete=${ac} form=${form || nothing}
      .value=${this._f[k]} ?disabled=${!!this._busy} aria-invalid=${err ? 'true' : 'false'}
      aria-describedby=${[hint, err ? `${IDS[k]}-err` : ''].filter(Boolean).join(' ') || nothing}
      autocapitalize=${extra.cap || nothing} spellcheck=${extra.spell === false ? 'false' : nothing}
      data-1p-ignore=${extra.ignore ? '' : nothing} data-lpignore=${extra.ignore ? 'true' : nothing}
      @input=${(e) => this._set(k, e.target.value)}>`;
  }

  render() {
    const p = this.plan;
    if (!p) return html`<div class="plate" role="dialog" aria-modal="true" aria-busy="true" aria-label=${WORDS.title}><div class="xbar"><bx-icon name="shield"></bx-icon><span>${WORDS.bar}</span></div></div>`;
    return html`<div class="plate" role="dialog" aria-modal="true" aria-labelledby="bxfr-h" data-state=${this._made ? 'made' : this._left ? 'left' : 'form'}>
      <div class="xbar"><bx-icon name="shield"></bx-icon><span>${WORDS.bar}</span></div>
      <div class="main">${this._made ? this._madeView() : this._left ? this._leftView() : this._form(p)}</div>
    </div>`;
  }

  _form(p) {
    const f = this._f, busy = !!this._busy;
    const vault = p.vault === 'new' ? html`<fieldset data-vault="new"><legend>${WORDS.vault}</legend>
        <p class="hint" id="bxfr-vault-hint">${WORDS.vaultLine}</p>
        ${this._field('vault', WORDS.vaultNew, () => this._input('vault', { type: 'password', ac: 'off', form: 'bxfr-vaultform', extra: { name: 'vault-passphrase', ignore: true, hint: true } }))}
        ${this._field('repeat', WORDS.vaultRepeat, () => this._input('repeat', { type: 'password', ac: 'off', form: 'bxfr-vaultform', extra: { name: 'vault-passphrase-again', ignore: true } }))}
      </fieldset>`
      : p.vault === 'unlock' ? html`<fieldset data-vault="unlock"><legend>${WORDS.vault}</legend>
        <p class="hint" id="bxfr-vault-hint">${WORDS.vaultUnlockLine}</p>
        ${this._field('vault', WORDS.vaultUnlock, () => this._input('vault', { type: 'password', ac: 'off', form: 'bxfr-vaultform', extra: { name: 'vault-passphrase', ignore: true, hint: true } }))}
      </fieldset>` : nothing;
    const offer = p.agent.kind === 'offer';
    return html`
      <h1 id="bxfr-h">${WORDS.title}</h1>
      <p class="lead">${WORDS.lead}</p>
      <form id="bxfr-form" novalidate autocomplete="on" @submit=${(e) => { e.preventDefault(); this._submit(true); }} @keydown=${this._key}>
        ${this._field('name', WORDS.name, () => this._input('name', { ac: 'name', extra: { hint: !this._editId, cap: 'words' } }),
          this._editId ? nothing : html`<div class="hint" id="bxfr-name-hint" data-signin>${!f.name.trim() ? WORDS.nameHint : html`${f.id ? html`${WORDS.signsInAs} <b>${f.id}</b>. ` : ''}<button type="button" class="link" ?disabled=${busy}
            @click=${() => { this._editId = true; this._focus('id'); }}>${f.id ? WORDS.change : WORDS.noName}</button>`}</div>`)}
        ${this._editId ? this._field('id', WORDS.username, () => this._input('id', { ac: 'username', extra: { name: 'username', hint: true, cap: 'none', spell: false } }),
          html`<div class="hint" id="bxfr-id-hint">${WORDS.usernameHint}</div>`)
          : html`<input type="text" name="username" autocomplete="username" .value=${f.id} hidden tabindex="-1" aria-hidden="true">`}
        ${this._field('password', WORDS.password, () => this._input('password', { type: 'password', ac: 'new-password', extra: { hint: true } }),
          html`<div class="hint" id="bxfr-pw-hint">${WORDS.passwordHint}</div>`)}
      </form>
      <form id="bxfr-vaultform" novalidate autocomplete="off" @submit=${(e) => { e.preventDefault(); this._submit(true); }} @keydown=${this._key}></form>
      <div @keydown=${this._key}>${vault}</div>
      ${p.says ? html`<div class="agent" data-agent=${p.agent.kind}><bx-icon name="agent"></bx-icon><div><p>${WORDS.agentLine}</p>${p.line ? html`<p class="more">${p.line}${p.link
        ? html` <a href=${p.link.href} data-link>${p.link.text}</a>` : nothing}</p>` : nothing}</div></div>` : nothing}
      ${this._err.form ? html`<div class="err" data-err="form" role="alert"><bx-icon name="error"></bx-icon><span>${this._err.form}</span></div>` : nothing}
      <div class="btns">
        <button type="submit" form="bxfr-form" class="primary" data-primary ?disabled=${busy}>${p.primary}</button>
        ${offer ? html`<button type="button" class="quiet" data-quiet ?disabled=${busy} @click=${() => this._submit(false)}>${p.quiet}</button>` : nothing}
      </div>
      <div class="status" role="status" aria-live="polite">${this._busy}</div>
      <p class="skip"><button type="button" class="link" data-skip ?disabled=${busy} @click=${() => this._skip()}>${WORDS.skip}</button></p>
      <p class="hint token" data-token>${WORDS.token}</p>`;
  }

  _leftView() {
    return html`<h1 id="bxfr-h">${WORDS.leftTitle}</h1>
      <p class="lead">${WORDS.leftText(this._f.id)}</p>
      <ul class="left" data-left>${this._left.map((x) => html`<li>${x}</li>`)}</ul>
      <div class="btns"><button type="button" class="primary" data-continue @click=${() => this._finish()}>${WORDS.continue}</button></div>`;
  }

  _madeView() {
    return html`<h1 id="bxfr-h">${WORDS.madeTitle}</h1>
      <p class="lead" data-made>${WORDS.madeText(this._made.id, this._made.why)}</p>
      <div class="btns"><a class="btn primary" href="/login" data-signin-link>${WORDS.signIn}</a></div>`;
  }
}
if (!customElements.get('bx-first-run')) customElements.define('bx-first-run', BxFirstRun);
