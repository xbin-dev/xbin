/**
 * agent-signin.js — <bx-agent-signin>, xbin's one sign-in for coding
 * agents (D178, D230; docs/agent-signins.md). Three faces of one element:
 *
 *   the card (default) — where a coding agent says it is signed out, on a
 *     card that needs a sign-in, in a launcher. "Sign in" runs the guided
 *     sign-in xbind hosts (POST /api/xbin/agent/signins/guided): a link to
 *     open, the code the page shows, Finish — and the sign-in is saved with
 *     xbind, named, the default, used for your sessions on every tile where
 *     that is safe. Beside it: paste a key instead, and "sign in on this
 *     tile only" — the CLI's own sign-in in a terminal session of its own
 *     on the tile (D178: its login stays in your $HOME there), which is
 *     also what the card does where nothing can be saved.
 *   the list (view="manage") — your saved sign-ins per coding agent: what
 *     each is, when it was used, rename, make default, remove, add one;
 *     for workspace admins the switch and who has sign-ins.
 *   a button (view="button") — "Sign-ins…", which opens the list.
 *
 * No secret is ever shown: the guided sign-in's token never leaves xbind,
 * and a pasted key goes from its field into one request.
 *
 * Properties: harness (the coding agent's id), provider (its name),
 * component (the tile — for "this tile only" and for whether a saved
 * sign-in would be used there), spec (the provider's D178 `signin`, for
 * the tile's own flow), view. Events, bubbling and composed:
 * bx-signin-done {saved?} — signed in; `saved` is the sign-in xbind keeps,
 * absent after the tile's own flow; bx-signin-terminal {fallback} — the
 * person asked for a terminal; bx-signins-changed — the list changed.
 */
import { LitElement, html, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { SigninReader, typedLine, cleanCode } from '/vendor/signin-scan.js';
import { SIGNIN_PURPOSE } from '/vendor/term-sessions.js';
import { signinsApi, harnessOf, whyNot, nextStep } from '/vendor/agent-signins.js';
import { signinCss, manageView, manageInit } from '/vendor/agent-signin-list.js';

const enc = new TextEncoder();
const COLS = 1000; // the CLI's terminal: wide enough that no URL wraps
const REATTACH = 3; // a dropped socket reattaches this many times

export class BxAgentSignin extends LitElement {
  static properties = {
    spec: { attribute: false },
    provider: { type: String },
    harness: { type: String },
    component: { type: String },
    view: { type: String, reflect: true }, // '' (the card) | manage | button
    // idle → starting → waiting (the link is up) → checking (a code sent) →
    // done | failed | ended | old (no link: an old CLI) | error; paste: the key's form
    _phase: { state: true },
    _how: { state: true }, // which sign-in runs: 'saved' (xbind's) | 'tile' (the CLI's own, D178)
    _st: { state: true }, // what the CLI said: the tile flow's scan, or the saved flow's {url, code}
    _msg: { state: true },
    _copied: { state: true },
    _data: { state: true }, // GET /agent/signins (null until it answered; false when it can't be had)
    _saved: { state: true }, // the sign-in a flow just saved
    _m: { state: true }, // the list's own state (agent-signin-list.js)
    _open: { state: true }, // the list is open over the card or the button
  };

  static styles = signinCss;

  constructor() {
    super();
    this._phase = 'idle';
    this._how = '';
    this._st = null;
    this._msg = '';
    this._copied = false;
    this._data = null;
    this._saved = null;
    this._m = manageInit();
    this._open = false;
    this._ws = null;
    this._id = '';
    this._flow = '';
    this.api = signinsApi();
  }

  connectedCallback() {
    super.connectedCallback();
    if (this.view !== 'button') this.reload(); // a button reads when it is opened
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._stop();
    this._dialogKeys(false);
  }

  updated(changed) {
    if (changed.has('component') && changed.get('component') !== undefined) this.reload();
    // the code field takes the focus once the link is up: paste, Enter
    if (changed.has('_phase') && this._phase === 'waiting' && this._st?.code) this.renderRoot?.querySelector('input.code')?.focus();
    if (changed.has('_phase') && this._phase === 'paste') this.renderRoot?.querySelector('input.secret')?.focus();
    if (changed.has('_open')) this._dialogKeys(this._open);
  }

  /** reload(): read the saved sign-ins again (and a flow of yours under way). */
  async reload() {
    try {
      this._data = await this.api.list(this.component || '');
    } catch (e) {
      this._data = false; // an older xbind, or no store: the tile's own flow is all there is
      return;
    }
    const mine = (this._data.flows || []).find((f) => f.harness === this.harness);
    if (mine && this._phase === 'idle' && !this.view) { // a flow of yours is still open (a reload): pick it up
      this._how = 'saved';
      this._flow = mine.id;
      this._st = { url: mine.url, code: mine.paste };
      this._phase = 'waiting';
    }
  }

  get _harness() { return this._data ? harnessOf(this._data, this.harness) : null; }

  // can a sign-in be saved for this coding agent at all?
  get _canSave() { return !!this._harness && !whyNot(this._data); }

  // would a session on this tile get it (the rule's answer; null: not asked)
  get _here() { return this._harness?.here || null; }

  // ---- the saved sign-in: xbind runs the CLI ----

  /** startSaved(name?): start xbind's guided sign-in (or pick up yours). */
  async startSaved(name) {
    this._stop();
    this._how = 'saved';
    this._phase = 'starting';
    this._msg = '';
    this._copied = false;
    this._st = null;
    this._saved = null;
    try {
      const { flow } = await this.api.guided(this.harness, name || '', false);
      if (this._phase !== 'starting' || this._how !== 'saved') { this.api.cancel(flow.id).catch(() => { }); return; }
      this._flow = flow.id;
      this._st = { url: flow.url, code: flow.paste };
      this._phase = 'waiting';
    } catch (e) { this._fail(e); }
  }

  _fail(e) {
    this._flow = '';
    this._phase = 'failed';
    this._msg = [e.message || String(e), nextStep(e)].filter(Boolean).join(' ');
  }

  async _finishSaved(code) {
    this._phase = 'checking';
    this._msg = '';
    try {
      const { signin } = await this.api.code(this._flow, code);
      this._done(signin);
    } catch (e) {
      if (e.status === 409 && this._flow) { // malformed: the CLI asks again
        this._phase = 'waiting';
        this._msg = 'That is not the whole code: copy it again from the sign-in page.';
        return;
      }
      this._fail(e);
    }
  }

  _done(signin) {
    this._flow = '';
    this._saved = signin || null;
    this._phase = 'done';
    this.reload();
    this.dispatchEvent(new CustomEvent('bx-signin-done', { detail: { saved: signin || null, here: this._here }, bubbles: true, composed: true }));
    this.dispatchEvent(new CustomEvent('bx-signins-changed', { bubbles: true, composed: true }));
  }

  // ---- paste a key ----

  _pasteForm() { this._stop(); this._how = 'saved'; this._phase = 'paste'; this._msg = ''; }

  async _paste(e) {
    e?.preventDefault();
    const f = this.renderRoot?.querySelector('form.paste');
    const secret = (f?.querySelector('input.secret')?.value || '').trim();
    const name = (f?.querySelector('input.name')?.value || '').trim();
    if (!secret) { this._msg = 'Paste the key or token first.'; return; }
    this._phase = 'checking';
    this._msg = '';
    try {
      const { signin } = await this.api.paste(this.harness, secret, name, false);
      this._done(signin);
    } catch (err) {
      this._phase = 'paste';
      this._msg = [err.message, nextStep(err)].filter(Boolean).join(' ');
    }
  }

  // ---- the tile's own sign-in (D178): the CLI in a terminal session of its own ----

  /** start(): the tile's own sign-in — a fresh session, a fresh sign-in. */
  start() {
    this._stop();
    this._how = 'tile';
    this._reader = new SigninReader(this.spec || {});
    this._st = this._reader.state;
    this._phase = 'starting';
    this._msg = '';
    this._copied = false;
    this._id = '';
    this._exited = false;
    this._typed = false;
    this._tries = 0;
    this._sentAt = -1; // the invalid-code count when a code was sent
    this._dial('');
  }

  _dial(session) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const q = session ? `session=${encodeURIComponent(session)}` : `cwd=${encodeURIComponent(this.component || '')}&gpu=none&api=0&purpose=${SIGNIN_PURPOSE}`;
    let ws;
    try { ws = new WebSocket(`${proto}//${location.host}/ws/term?${q}`); } catch (e) { this._over(String(e.message || e)); return; }
    ws.binaryType = 'arraybuffer';
    this._ws = ws;
    let opened = false;
    ws.onopen = () => { opened = true; ws.send(JSON.stringify({ op: 'resize', cols: COLS, rows: 40 })); };
    ws.onmessage = (m) => { if (ws === this._ws) this._frame(m, ws); };
    ws.onclose = () => {
      if (ws !== this._ws) return;
      this._ws = null;
      if (this._exited) return;
      // a drop: the session lives on — reattach, and its scrollback replays
      if (this._id && opened && this._tries < REATTACH) {
        this._tries++;
        setTimeout(() => { if (this.isConnected && !this._ws && !this._exited && this._phase !== 'idle') { this._reader.reset(); this._dial(this._id); } }, 400 * this._tries);
        return;
      }
      this._over(opened ? 'The sign-in lost its terminal.' : 'Could not open a terminal on this tile for the sign-in.');
    };
  }

  _frame(m, ws) {
    if (typeof m.data !== 'string') { this._read(new Uint8Array(m.data)); return; }
    let ctl;
    try { ctl = JSON.parse(m.data); } catch { return; }
    if (ctl.op === 'session' && ctl.id) {
      if (!this._id) this._id = ctl.id;
      if (!this._typed) { this._typed = true; ws.send(enc.encode(typedLine(this.spec))); }
    } else if (ctl.op === 'exit') {
      this._exited = true;
      this._over('');
    }
  }

  _read(bytes) {
    const st = this._reader.push(bytes);
    this._st = st;
    if (st.done) {
      if (this._phase !== 'done') {
        this._phase = 'done';
        this.dispatchEvent(new CustomEvent('bx-signin-done', { detail: { saved: null }, bubbles: true, composed: true }));
      }
      return;
    }
    if (st.failed) { this._phase = 'failed'; this._msg = st.failed; return; }
    if (this._phase === 'checking' && st.invalid > this._sentAt) {
      this._phase = 'waiting';
      this._msg = 'That is not the whole code: copy it again from the sign-in page.';
    }
    // a link from the text shows once the CLI asks for the code (all of it is out then)
    if (this._phase === 'starting' && st.url && (st.link || st.code)) this._phase = 'waiting';
  }

  // the CLI is gone (an exit frame) or out of reach (why)
  _over(why) {
    const st = this._st || {};
    if (this._phase === 'done' || st.done) this._phase = 'done';
    else if (st.failed) { this._phase = 'failed'; this._msg = st.failed; }
    else if (why) { this._phase = 'error'; this._msg = why; }
    else if (!st.url) { this._phase = 'old'; this._msg = st.last || ''; }
    else { this._phase = 'ended'; this._msg = st.last || ''; }
    const ws = this._ws;
    this._ws = null;
    if (ws) { ws.onclose = null; ws.close(); }
  }

  // stop: whatever runs ends — the socket and its session (unless the CLI
  // is ending it), xbind's flow
  _stop() {
    const ws = this._ws, id = this._id, flow = this._flow;
    this._ws = null;
    this._flow = '';
    this._id = '';
    if (ws) { ws.onclose = null; ws.close(); }
    if (id && !this._exited && this._phase !== 'done') fetch(`/ws/term?session=${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => { });
    if (flow && this._phase !== 'done') this.api.cancel(flow).catch(() => { });
    this._exited = true;
  }

  _cancel() { this._stop(); this._phase = 'idle'; this._msg = ''; }

  _finish(e) {
    e?.preventDefault();
    const input = this.renderRoot?.querySelector('input.code');
    const code = cleanCode(input?.value);
    if (!code) return;
    if (this._how === 'saved') { if (this._flow) { input.value = ''; this._finishSaved(code); } return; }
    if (!this._ws || this._ws.readyState !== WebSocket.OPEN) return;
    this._sentAt = this._st.invalid;
    this._ws.send(enc.encode(code + '\r'));
    input.value = '';
    this._phase = 'checking';
    this._msg = '';
  }

  async _copy() {
    const url = this._st?.url;
    if (!url) return;
    try { await navigator.clipboard.writeText(url); this._copied = true; } catch {
      const el = this.renderRoot?.querySelector('input.url');
      el?.focus(); el?.select(); // the link, selected: copy it by hand
      this._msg = 'Copying was refused here: the link is selected, copy it with the keyboard.';
    }
  }

  // today's way: a shell tab that runs the login (the fallback for an old CLI)
  _terminal(fallback) {
    this._stop();
    this._phase = 'idle';
    this.dispatchEvent(new CustomEvent('bx-signin-terminal', { detail: { fallback: !!fallback }, bubbles: true, composed: true }));
  }

  // the card's main button: xbind's sign-in where one can be saved and
  // would be used on this tile, else the tile's own, else a pasted key
  get _primary() {
    const guided = this._canSave && this._harness?.guided;
    if (guided && (!this._here || this._here.inject)) return 'saved';
    if (this.spec) return 'tile';
    if (guided) return 'saved';
    return this._canSave ? 'paste' : '';
  }

  _go() { if (this._primary === 'saved') this.startSaved(); else if (this._primary === 'tile') this.start(); else if (this._primary === 'paste') this._pasteForm(); }

  _status() {
    const name = this.provider || this._harness?.name || 'the agent', m = this._msg;
    switch (this._phase) {
      case 'idle': return ['', `Not signed in to ${name}.`];
      case 'starting': return ['', `Starting ${name}'s sign-in…`];
      case 'waiting': return [m ? 'bad' : '', m || 'Open the sign-in page and sign in there, then paste the code it shows and press Finish.'];
      case 'paste': return [m ? 'bad' : '', m || `Paste ${this._keyWords()}. It is sealed with the workspace vault and never shown again.`];
      case 'checking': return ['', this._how === 'saved' && !this._flow ? 'Saving…' : 'Checking the code…'];
      case 'done': return ['ok', this._saved ? `Saved as ${this._saved.name}. ${this._usedWords()}` : `Signed in to ${name}.`];
      case 'failed': return ['bad', this._how === 'saved' ? m : `The sign-in failed: ${m}`];
      case 'ended': return ['bad', `The sign-in ended before it finished${m ? `: ${m}` : '.'}`];
      case 'old': return ['bad', `This ${name} can't sign in here without a terminal${m ? ` (${m})` : ''}.`];
      default: return ['bad', m || 'The sign-in could not start.'];
    }
  }

  _keyWords() {
    const labels = (this._harness?.keys || []).map((k) => k.label);
    return labels.length ? `your ${labels.join(', or ')}` : 'the key';
  }

  // where a saved sign-in is used, in one sentence — and whether here
  _usedWords() {
    const here = this._here;
    if (here && !here.inject) return `It is used on tiles only you can open a terminal on — not on this one: ${here.why}.`;
    return 'xbind uses it for your coding agents on every tile only you can open a terminal on.';
  }

  // the line under the status while idle: what the main button will do, or
  // why nothing can be saved
  _hint() {
    if (!this._data || !this._harness) return '';
    const not = whyNot(this._data);
    if (not) return not;
    const here = this._here;
    if (here && !here.inject) return `A sign-in saved with xbind isn't used on this tile: ${here.why}.`;
    if (this._primary === 'saved') return `xbind keeps the sign-in sealed and uses it for your coding agents on every tile only you can open a terminal on.${this._data.unisolated ? ' This workspace runs without sandboxes (a development workspace): everything it runs could read it.' : ''}`;
    return '';
  }

  _card() {
    const p = this._phase, st = this._st || {}, name = this.provider || this._harness?.name || 'the agent';
    const live = p === 'waiting' || (p === 'checking' && (this._how === 'tile' || !!this._flow));
    const pasting = p === 'paste' || (p === 'checking' && this._how === 'saved' && !this._flow);
    const [cls, line] = this._status();
    const primary = this._primary, hint = p === 'idle' ? this._hint() : '';
    const guided = this._canSave && this._harness?.guided;
    const canPaste = this._canSave && (this._harness?.keys || []).length > 0;
    const retry = () => (this._how === 'saved' ? this.startSaved() : this.start());
    const label = primary === 'tile' && this._canSave ? 'Sign in on this tile' : primary === 'paste' ? 'Paste a key' : 'Sign in';
    return html`<div class="box" data-phase=${p} data-how=${this._how}>
      <div class="row">
        <span class="msg">${p === 'done' ? nothing : html`<bx-icon name="warning"></bx-icon>`}<b>Sign in to ${name}</b></span>
        ${p === 'idle' && primary ? html`<button class="go start" @click=${() => this._go()}>${label}</button>` : nothing}
        ${p === 'failed' || p === 'ended' || p === 'error' ? html`<button class="go retry" @click=${retry}>Try again</button>` : nothing}
        ${p === 'old' ? html`<button class="go fallback" @click=${() => this._terminal(true)}>Sign in in a terminal</button>` : nothing}
      </div>
      ${live ? html`
        <div class="row">
          <a class="go open" href=${st.url} target="_blank" rel="noopener noreferrer">Open sign-in page<bx-icon name="popout"></bx-icon></a>
          <button class="copy" @click=${() => this._copy()}><bx-icon name=${this._copied ? 'check' : 'copy'}></bx-icon>${this._copied ? 'Copied' : 'Copy link'}</button>
          <input class="url" readonly aria-label="the sign-in link" .value=${st.url} @focus=${(e) => e.target.select()}>
        </div>
        <div class="row"><form @submit=${(e) => this._finish(e)}>
          <input class="code" autocomplete="off" spellcheck="false" aria-label="the code from the sign-in page"
            placeholder=${st.code ? 'Paste the code the page shows' : 'Waiting for the CLI to ask for the code…'} ?disabled=${!st.code || p === 'checking'}
            @keydown=${(e) => { if (e.key === 'Escape') { e.stopPropagation(); this._cancel(); } }}>
          <button class="finish" type="submit" ?disabled=${!st.code || p === 'checking'}>Finish</button>
        </form></div>` : nothing}
      ${pasting ? html`
        <form class="paste" @submit=${(e) => this._paste(e)} @keydown=${(e) => { if (e.key === 'Escape') { e.stopPropagation(); this._cancel(); } }}>
          <div class="row">
            <input class="secret" type="password" autocomplete="off" spellcheck="false" aria-label=${this._keyWords()} placeholder="Paste the key or token" ?disabled=${p === 'checking'}>
          </div>
          <div class="row">
            <input class="name" autocomplete="off" maxlength="40" aria-label="a name for this sign-in" placeholder="Name it (optional), e.g. Work" ?disabled=${p === 'checking'}>
            <button class="go save" type="submit" ?disabled=${p === 'checking'}>Save</button>
          </div>
        </form>` : nothing}
      <div class="status ${cls}" role="status">${cls === 'ok' ? html`<bx-icon name="ok"></bx-icon>` : cls === 'bad' ? html`<bx-icon name="error"></bx-icon>` : nothing}${line}</div>
      ${hint ? html`<div class="hint">${hint}</div>` : nothing}
      ${p === 'done' && this._saved && this._here && !this._here.inject && this.spec ? html`<div class="row"><button class="go tile-now" @click=${() => this.start()}>Sign in on this tile</button></div>` : nothing}
      ${p === 'done' ? nothing : html`<div class="row links">
        ${p === 'idle' && primary === 'tile' && guided ? html`<button class="link save-other" @click=${() => this.startSaved()}>Save a sign-in for my other tiles</button>` : nothing}
        ${p === 'idle' && canPaste && primary !== 'paste' ? html`<button class="link paste-instead" @click=${() => this._pasteForm()}>Paste a key instead</button>` : nothing}
        ${p === 'idle' && primary === 'saved' && this.spec ? html`<button class="link tile-only" @click=${() => this.start()} title="the CLI's own sign-in: its login stays in your home on this workspace">Sign in on this tile only</button>` : nothing}
        ${this.spec && (p === 'idle' || this._how === 'tile') ? html`<button class="link terminal" @click=${() => this._terminal(false)}>Use a terminal instead</button>` : nothing}
        ${p === 'starting' || live || pasting ? html`<button class="link cancel" @click=${() => this._cancel()}>Cancel</button>` : nothing}
        ${p === 'idle' && this._data && this._data.available ? html`<button class="link manage" @click=${() => { this._open = true; }}>Saved sign-ins…</button>` : nothing}
      </div>`}
    </div>`;
  }

  _dialog() {
    return html`<div class="scrim" @click=${(e) => { if (e.target === e.currentTarget) this._close(); }}>
      <div class="dialog" role="dialog" aria-modal="true" aria-label="Saved coding-agent sign-ins" tabindex="-1">${manageView(this, () => this._close())}</div>
    </div>`;
  }

  // the list over the page: Escape closes it wherever the focus is (a field
  // inside that takes Escape itself stops it there), and it takes the focus
  _dialogKeys(on) {
    if (!this._onKey) this._onKey = (e) => { if (e.key === 'Escape' && this._open) this._close(); };
    window.removeEventListener('keydown', this._onKey);
    if (on) { window.addEventListener('keydown', this._onKey); this.renderRoot?.querySelector('.dialog')?.focus(); }
  }

  _close() { this._open = false; this.updateComplete.then(() => this.renderRoot?.querySelector('button.manage, button.open-list')?.focus()); }

  render() {
    if (this.view === 'manage') return manageView(this, null);
    if (this.view === 'button') {
      return html`<button class="open-list" @click=${() => { this._open = true; this.reload(); }} title="the coding-agent sign-ins xbind keeps for you">Sign-ins…</button>${this._open ? this._dialog() : nothing}`;
    }
    return html`${this._card()}${this._open ? this._dialog() : nothing}`;
  }

  // the harness's handle (hack/ui-harness): read what the element shows, act
  // as a person would
  testApi() {
    const s = this;
    const q = (sel) => s.renderRoot?.querySelector(sel);
    return {
      get phase() { return s._phase; },
      get how() { return s._how; },
      get url() { return s._st?.url || ''; },
      get code() { return !!s._st?.code; },
      get status() { return s._status()[1]; },
      get hint() { return s._phase === 'idle' ? s._hint() : ''; },
      get primary() { return s._primary; },
      get session() { return s._id; },
      get saved() { return s._saved; },
      get data() { return s._data; },
      get open() { return s._open || s.view === 'manage'; },
      get list() { return s._m; },
      start: () => s.start(), // the tile's own sign-in (D178)
      startSaved: (name) => s.startSaved(name), // xbind's guided sign-in
      go: () => s._go(), // the card's main button
      finish(code) { const i = q('input.code'); if (i) i.value = code; s._finish(); },
      pasteForm: () => s._pasteForm(),
      paste(secret, name) { const f = q('form.paste'); if (!f) return; f.querySelector('input.secret').value = secret; f.querySelector('input.name').value = name || ''; s._paste(); },
      cancel: () => s._cancel(),
      openList: () => { s._open = true; return s.reload(); },
      closeList: () => s._close(),
      reload: () => s.reload(),
    };
  }
}

customElements.define('bx-agent-signin', BxAgentSignin);
