/**
 * agent-signin.js — <bx-agent-signin>, the Agent tab's guided sign-in
 * (D178). When a coding agent says it is signed out and its provider has a
 * `signin` (GET /api/xbin/agent/providers, docs/protocol.md), the tab shows
 * this strip instead of a terminal. Sign in runs the CLI's own sign-in
 * (`signin.command`) in a terminal session of its own on the tile — the
 * $HOME the agent signs in from — opened with ?purpose=signin, so no tab
 * shows it and xbind ends it after 15 minutes (term-sessions.js
 * visibleRows), at 1000 columns so nothing wraps, and reads its output
 * (signin-scan.js) for what matters: Open sign-in page, Copy link, a field
 * for the code the page shows (Finish types it and Enter into the CLI), and
 * a status line. The CLI's own words decide: signed in → bx-signin-done;
 * failed → its reason and Try again; a code refused as malformed → paste it
 * again; a run that ends without a link is a CLI too old for the command →
 * its fallback, in a terminal. "Use a terminal instead" asks for today's
 * shell tab (bx-signin-terminal). Nothing is kept but what the CLI saves in
 * $HOME; the code goes to the CLI and nowhere else.
 *
 * Properties: spec (the provider's signin), provider (its name), component
 * (the tile). Events, bubbling and composed: bx-signin-done;
 * bx-signin-terminal {fallback} — true when the CLI is too old for the
 * guided command.
 */
import { LitElement, html, css, nothing } from 'lit';
import { SigninReader, typedLine, cleanCode } from '/vendor/signin-scan.js';
import { SIGNIN_PURPOSE } from '/vendor/term-sessions.js';

const enc = new TextEncoder();
const COLS = 1000; // the CLI's terminal: wide enough that no URL wraps
const REATTACH = 3; // a dropped socket reattaches this many times

export class BxAgentSignin extends LitElement {
  static properties = {
    spec: { attribute: false },
    provider: { type: String },
    component: { type: String },
    // idle → starting → waiting (the link is up) → checking (a code sent) →
    // done | failed | ended | old (no link: an old CLI) | error (no terminal)
    _phase: { state: true },
    _st: { state: true }, // what the CLI said (signin-scan.js)
    _msg: { state: true },
    _copied: { state: true },
  };

  static styles = css`
    :host { display: block; margin-bottom: 6px; }
    .box { border: 1px solid var(--bx-amber, #f2a71b); border-radius: 5px; background: var(--bx-panel-2, #2b3038); padding: 6px 8px;
      font: 11px var(--bx-mono, ui-monospace, monospace); color: var(--bx-text, #d4d9e0); display: flex; flex-direction: column; gap: 6px; }
    .row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
    .row form { display: contents; }
    .msg { flex: 1; min-width: 0; }
    .go, button, a.go { border: 1px solid var(--bx-amber, #f2a71b); border-radius: 5px; padding: 3px 10px; font: inherit; white-space: nowrap; cursor: pointer; }
    .go { background: var(--bx-amber, #f2a71b); color: #1b1e24; font-weight: 700; text-decoration: none; }
    button { background: transparent; color: var(--bx-text, #d4d9e0); }
    button[disabled] { opacity: .5; cursor: default; }
    button.link { border: 0; padding: 0; color: var(--bx-muted, #868f9a); text-decoration: underline; }
    input { flex: 1; min-width: 12em; background: var(--bx-bg, #1b1e24); color: var(--bx-text, #d4d9e0); border: 1px solid var(--bx-border, #363c45);
      border-radius: 5px; padding: 3px 6px; font: inherit; }
    input.url { color: var(--bx-muted, #868f9a); }
    .status { color: var(--bx-muted, #868f9a); white-space: pre-wrap; overflow-wrap: anywhere; }
    .status.ok { color: var(--bx-green, #4caf50); }
    .status.bad { color: var(--bx-red, #ef5350); }
  `;

  constructor() {
    super();
    this._phase = 'idle';
    this._st = null;
    this._msg = '';
    this._copied = false;
    this._ws = null;
    this._id = '';
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._stop();
  }

  // start (or start over): a fresh session, a fresh sign-in
  start() {
    this._stop();
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
        this.dispatchEvent(new CustomEvent('bx-signin-done', { bubbles: true, composed: true }));
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

  // stop: close the socket, and end the session unless the CLI is ending it
  _stop() {
    const ws = this._ws, id = this._id;
    this._ws = null;
    if (ws) { ws.onclose = null; ws.close(); }
    if (id && !this._exited && this._phase !== 'done') fetch(`/ws/term?session=${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => { });
    this._exited = true;
  }

  _cancel() { this._stop(); this._phase = 'idle'; this._msg = ''; }

  _finish(e) {
    e?.preventDefault();
    const input = this.renderRoot?.querySelector('input.code');
    const code = cleanCode(input?.value);
    if (!code || !this._ws || this._ws.readyState !== WebSocket.OPEN) return;
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

  _status() {
    const name = this.provider || 'the agent', m = this._msg;
    switch (this._phase) {
      case 'idle': return ['', `Not signed in to ${name}.`];
      case 'starting': return ['', `Starting ${name}'s sign-in…`];
      case 'waiting': return [m ? 'bad' : '', m || 'Open the sign-in page and sign in there, then paste the code it shows and press Finish.'];
      case 'checking': return ['', 'Checking the code…'];
      case 'done': return ['ok', `Signed in to ${name}.`];
      case 'failed': return ['bad', `The sign-in failed: ${m}`];
      case 'ended': return ['bad', `The sign-in ended before it finished${m ? `: ${m}` : '.'}`];
      case 'old': return ['bad', `This ${name} can't sign in here without a terminal${m ? ` (${m})` : ''}.`];
      default: return ['bad', m || 'The sign-in could not start.'];
    }
  }

  render() {
    const p = this._phase, st = this._st || {}, name = this.provider || 'the agent';
    const live = p === 'waiting' || p === 'checking';
    const [cls, line] = this._status();
    return html`<div class="box" data-phase=${p}>
      <div class="row">
        <span class="msg"><b>Sign in to ${name}</b></span>
        ${p === 'idle' ? html`<button class="go start" @click=${() => this.start()}>Sign in</button>` : nothing}
        ${p === 'failed' || p === 'ended' || p === 'error' ? html`<button class="go retry" @click=${() => this.start()}>Try again</button>` : nothing}
        ${p === 'old' ? html`<button class="go fallback" @click=${() => this._terminal(true)}>Sign in in a terminal</button>` : nothing}
      </div>
      ${live ? html`
        <div class="row">
          <a class="go open" href=${st.url} target="_blank" rel="noopener noreferrer">Open sign-in page ↗</a>
          <button class="copy" @click=${() => this._copy()}>${this._copied ? 'Copied' : 'Copy link'}</button>
          <input class="url" readonly aria-label="the sign-in link" .value=${st.url} @focus=${(e) => e.target.select()}>
        </div>
        <div class="row"><form @submit=${(e) => this._finish(e)}>
          <input class="code" autocomplete="off" spellcheck="false" aria-label="the code from the sign-in page"
            placeholder=${st.code ? 'Paste the code the page shows' : 'Waiting for the CLI to ask for the code…'} ?disabled=${!st.code || p === 'checking'}>
          <button class="finish" type="submit" ?disabled=${!st.code || p === 'checking'}>Finish</button>
        </form></div>` : nothing}
      <div class="status ${cls}" role="status">${line}</div>
      ${p === 'done' ? nothing : html`<div class="row">
        <button class="link terminal" @click=${() => this._terminal(false)}>Use a terminal instead</button>
        ${p === 'starting' || live ? html`<button class="link cancel" @click=${() => this._cancel()}>Cancel</button>` : nothing}
      </div>`}
    </div>`;
  }

  // the harness's handle (hack/ui-harness): read what the strip shows, act as
  // a person would
  testApi() {
    const s = this;
    return {
      get phase() { return s._phase; },
      get url() { return s._st?.url || ''; },
      get code() { return !!s._st?.code; },
      get status() { return s._status()[1]; },
      get session() { return s._id; },
      start: () => s.start(),
      finish(code) { const i = s.renderRoot?.querySelector('input.code'); if (i) i.value = code; s._finish(); },
    };
  }
}

customElements.define('bx-agent-signin', BxAgentSignin);
