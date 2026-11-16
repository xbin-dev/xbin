/**
 * bx-agent-dock.js — <bx-agent-dock>, xbin's own chrome around a person's
 * workspace agent (/vendor/bx-agent-dock.js, D232): a side panel beside the
 * canvas (360–720 px, its width remembered per person; the whole page when
 * the window is narrow, and on /xbin/agent), with
 *
 *   - a header xbin draws: the title, Needs you with its count, Pause /
 *     Resume, a menu (Approvals, What my agent did, Preferences), close;
 *   - below it, the agent's own page in a sandboxed frame — or, in its
 *     place, the state that explains why there is none (bx-agent-setup);
 *   - and every trusted overlay: the Needs-you tray, the approval sheet
 *     (it hangs from the top of the dock, over the header: a region the
 *     frame cannot draw in), the lists, and the change-set dialog, which it
 *     puts over the whole workspace.
 *
 * THE RULE: a grant is drawn and sent only here, in a document that holds
 * the person's own session. The agent's page can say that something waits
 * and ask the dock to show it — a message carrying an id and nothing else,
 * taken only from this dock's own frame (agent-chrome-words.js pageMessage:
 * xbin:agent-open-request {id}, xbin:agent-open-changeset {id},
 * xbin:open-session {session, tile}, and {session: "new", tile, draft} for a
 * new session of a tile's own agent with a draft the page suggests — shown as the page's, for the person to read
 * and edit, never as their words — unsent; xbin:agent-open {view} for a view that changes nothing — Needs you,
 * Approvals, What my agent did: Preferences opens from this dock's own menu only). The dock then reads the request from
 * xbind itself and draws xbind's reading; what the page sent is never
 * shown, and nothing the page does reaches a button: it is another origin
 * (sandboxed, never chrome), it cannot draw outside its frame, and Allow
 * takes only the person's own click (bx-agent-approve.js).
 *
 *   properties  open      shown (reflected)
 *               full      fill the page: no resize, no close (/xbin/agent, a narrow window)
 *               context   {tile} | {screen}: what the person was looking at when they opened it — told to the page
 *                         (xbin:agent-context {tile | screen})
 *               page      the agent's page, when it is not the holder's from the state (a test's)
 *   methods     toggle(), show(view), review(kind, id), focusComposer()
 *   events      bx-agent-dock {open, width}   the shell makes room
 *               bx-agent-open-session {tile, session, page?}   a waiting tile agent: the shell opens it (page: the agent's page asked)
 *               bx-agent-new-session {tile, draft}   (cancelable) a new session of the tile's own agent, the draft in its
 *                         composer, unsent: a shell that can opens the tile's window on it and takes the event
 *                         (preventDefault); untaken — an older shell, /xbin/agent — the dock shows a plain link to the tile
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { chromeCss } from '/vendor/agent-chrome-css.js';
import { agentStore, call } from '/vendor/agent-chrome-store.js';
import { infoFor, refreshFrameInfo, frameSource, SANDBOX } from '/vendor/frame-info.js';
import { CHOICES, DRAFT_FROM, SIGNINS_MESSAGE, clampDock, contextMessage, dockState, handoffLink, needs, pageMessage } from '/vendor/agent-chrome-words.js';
import '/vendor/agent-signin.js';
import '/vendor/bx-agent-approve.js';
import '/vendor/bx-agent-changes.js';
import '/vendor/bx-agent-panels.js';

const PREF = '/prefs/agent-dock'; // {w}: the person's width for the dock

export class BxAgentDock extends LitElement {
  static properties = {
    open: { type: Boolean, reflect: true }, full: { type: Boolean, reflect: true }, context: { attribute: false }, page: { attribute: false },
    _w: { state: true }, _view: { state: true }, _tray: { state: true }, _menu: { state: true }, _sheet: { state: true }, _frame: { state: true }, _narrow: { state: true }, _err: { state: true },
    _handoff: { state: true }, // {tile, draft}: a new session the shell couldn't open — the plain link instead
    _signinFor: { state: true }, // a coding agent's id: the sign-in card the page asked for (D230)
  };

  static styles = [chromeCss, css`
    :host { display: none; position: relative; flex: none; height: 100%; min-height: 0; background: var(--bx-bg, #0B0C12); border-left: 1px solid var(--bx-border-strong, #666A7E); }
    :host([open]) { display: flex; flex-direction: column; }
    :host([full]) { border-left: 0; width: 100% !important; }
    .grip { position: absolute; left: -4px; top: 0; bottom: 0; width: 8px; cursor: col-resize; z-index: 4; touch-action: none; }
    :host([full]) .grip { display: none; }
    header { display: flex; align-items: center; gap: 6px; padding: 6px 8px 6px 12px; min-height: 40px; border-bottom: 1px solid var(--bx-border, #33353F); background: var(--bx-panel, #1F2028); }
    header h2 { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    /* a narrow dock keeps its title whole: Pause / Resume is its icon there, still named for a screen reader */
    header { container-type: inline-size; }
    @container (max-width: 460px) { header:has([data-close]) [data-pause] .lbl { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
      header:has([data-close]) > bx-icon:first-child { display: none; }
      header:has([data-close]) button { padding-left: 7px; padding-right: 7px; } }
    .body { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
    iframe { flex: 1; min-height: 0; width: 100%; border: 0; background: var(--bx-bg, #0B0C12); }
    .tray { position: absolute; left: 0; right: 0; top: 0; z-index: 2; }
    .menu { position: absolute; right: 8px; top: 40px; z-index: 3; display: flex; flex-direction: column; min-width: 200px; background: var(--bx-panel-2, #262730);
      border: 1px solid var(--bx-border-strong, #666A7E); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
    .menu button { border: 0; justify-content: flex-start; border-radius: 0; background: none; }
    .over { position: absolute; inset: 0; z-index: 5; display: flex; flex-direction: column; padding: 8px; }
    .over .scrim { position: absolute; inset: 0; background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }
    .over bx-agent-approve { position: relative; max-height: 100%; min-height: 0; display: flex; flex-direction: column; }
    .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  `];

  constructor() {
    super();
    this.open = false; this.full = false; this.context = null; this.page = '';
    this._w = CHOICES.dockWidth; this._view = 'chat'; this._tray = false; this._menu = false; this._sheet = null; this._frame = null; this._narrow = false; this._err = ''; this._handoff = null; this._signinFor = '';
    this._framed = ''; this._dialog = null; this._said = '';
    this.store = agentStore();
    this._onMsg = (e) => this._message(e);
    this._onResize = () => { this._narrow = window.innerWidth < CHOICES.narrow; };
  }

  connectedCallback() {
    super.connectedCallback();
    this.setAttribute('role', 'complementary');
    this.setAttribute('aria-label', 'Workspace agent');
    this._off = this.store.subscribe(() => { this._resolveFrame(); this.requestUpdate(); });
    window.addEventListener('message', this._onMsg);
    window.addEventListener('resize', this._onResize);
    this._onResize();
    call(PREF).then((r) => { if (r.ok && r.data && Number(r.data.w)) this._w = clampDock(r.data.w, window.innerWidth); });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    window.removeEventListener('message', this._onMsg);
    window.removeEventListener('resize', this._onResize);
    this._dialog?.remove(); this._dialog = null;
  }

  updated(ch) {
    if (ch.has('open') || ch.has('_w') || ch.has('_narrow')) {
      this.style.width = this.full || this._narrow ? '' : `${this._w}px`;
      this.toggleAttribute('data-narrow', this._narrow);
      this.dispatchEvent(new CustomEvent('bx-agent-dock', { detail: { open: this.open, width: this.open ? this._w : 0 }, bubbles: true, composed: true }));
    }
    if (ch.has('open') && this.open) { this._resolveFrame(); this.store.refresh(); }
    if (ch.has('page')) this._resolveFrame();
    if (ch.has('context') || ch.has('_frame')) this._tell();
  }

  toggle() { this.open = !this.open; }

  /**
   * focusComposer(): open, and give the agent's page the keys once it is
   * framed — the frame focused, and a cleared xbin:agent-context, which the
   * page answers by focusing its composer (the first-run sheet's hand-over,
   * D237). Nothing when no page is framed (setup, a state in its place).
   */
  focusComposer() {
    this.open = true;
    this._wantFocus = true;
    this._giveFocus();
  }

  _giveFocus() {
    const frame = this.renderRoot && this.renderRoot.querySelector('iframe');
    if (!this._wantFocus || !frame || !this._frameLoaded || !frame.contentWindow) return;
    this._wantFocus = false;
    frame.focus();
    frame.contentWindow.postMessage({ type: 'xbin:agent-context' }, '*');
  }

  /** show(view): open on 'chat', 'approvals', 'activity', 'prefs' or 'needs' (the tray). */
  show(view = 'chat') {
    this.open = true; this._menu = false;
    if (view === 'needs') { this._tray = true; return; }
    this._tray = false;
    this._view = ['approvals', 'activity', 'prefs'].includes(view) ? view : 'chat';
  }

  /**
   * review(kind, id): show the approval for a request, or the review of a
   * change set — read from xbind here, by id; one that is not the person's,
   * or is gone, shows nothing.
   */
  async review(kind, id) {
    this.open = true; this._tray = false; this._menu = false;
    if (kind === 'changeset') {
      const set = await this.store.changeset(id);
      if (set) this._openDialog(set);
      return;
    }
    const req = await this.store.request(id);
    if (!req) return;
    const pending = this.store.requests.filter((r) => r.state === 'pending');
    const i = pending.findIndex((r) => r.id === req.id);
    this._sheet = { req, position: pending.length > 1 && i >= 0 ? `${i + 1} of ${pending.length}` : '' };
  }

  _person() { const me = this.store.me || {}; return me.name || me.display || me.user || ''; }

  _openDialog(set) {
    // the shell's: over the whole workspace, not inside the dock
    if (!this._dialog) {
      this._dialog = document.createElement('bx-agent-changes');
      this._dialog.addEventListener('bx-agent-done', () => { this._dialog?.remove(); this._dialog = null; this.renderRoot.querySelector('[data-needs]')?.focus(); });
      this._dialog.addEventListener('bx-agent-review', (e) => { e.stopPropagation(); this.review(e.detail.kind, e.detail.id); });
      document.body.append(this._dialog);
    }
    this._dialog.person = this._person();
    this._dialog.set = set;
  }

  _sheetDone(e) {
    const v = e.detail.verdict;
    this._sheet = null;
    // one at a time: after a decision the next one that waits is shown
    const next = v === 'later' ? null : this.store.requests.find((r) => r.state === 'pending' && r.id !== e.detail.id);
    if (next) this.review('request', next.id); else this.updateComplete.then(() => this.renderRoot.querySelector('[data-needs]')?.focus());
  }

  // ---- the agent's page ----

  // where the page comes from: a test's, else the person's holder as the state names it; never a chrome component
  async _resolveFrame() {
    const st = dockState(this.store);
    const src = this.page || (st === 'page' ? this.store.state.agent : '');
    if (src === this._framed) return;
    this._framed = src;
    this._frameLoaded = false;
    if (!src) { this._frame = null; return; }
    if (this.page) { this._frame = { url: this.page, sandbox: SANDBOX, credentialless: false }; return; }
    let info = await infoFor(src);
    // a sealed component is left out of the plain listing (docs/protocol/sealed.md §Listings): its one row is asked by name
    if (!info) { await refreshFrameInfo(src); info = await infoFor(src); }
    if (!info || info.chrome) { this._frame = null; return; } // a holder's page holds a frame token, never the person's session
    const f = await frameSource(src, info);
    if (this._framed === src) this._frame = f.sandboxed ? f : null;
  }

  // what the page may ask: to open a request or a change set by id, one of
  // the person's sessions, a view that changes nothing (pageMessage: never
  // Preferences). Only from this dock's own frame.
  _message(e) {
    const frame = this.renderRoot.querySelector('iframe');
    if (!frame || e.source !== frame.contentWindow) return;
    const m = pageMessage(e.data);
    if (!m) return;
    if (m.type === 'review') this.review(m.kind, m.id);
    else if (m.type === 'session') this.dispatchEvent(new CustomEvent('bx-agent-open-session', { detail: { tile: m.tile, session: m.session, page: true }, bubbles: true, composed: true }));
    else if (m.type === 'new-session') this._newSession(m.tile, m.draft);
    else if (m.type === 'signin') this._signinFor = m.harness; // the card is the chrome's: the page only named the coding agent (D230)
    else this.show(m.view);
  }

  // the person's sign-ins changed here: the page reads what a start would run on again
  _signinsChanged() {
    this.renderRoot.querySelector('iframe')?.contentWindow?.postMessage(SIGNINS_MESSAGE, '*');
  }

  // a new session of a tile's own agent, the person's words in its composer:
  // the shell's to open; one that doesn't take it gets the plain link here
  _newSession(tile, draft) {
    this._handoff = null;
    const ev = new CustomEvent('bx-agent-new-session', { detail: { tile, draft }, bubbles: true, composed: true, cancelable: true });
    if (this.dispatchEvent(ev)) this._handoff = { tile, draft };
  }

  _handoffView() {
    const h = this._handoff;
    if (!h) return nothing;
    const copy = (e) => { navigator.clipboard?.writeText(h.draft).then(() => { e.target.textContent = 'Copied'; }, () => { }); };
    return html`<div class="banner" role="status" data-handoff><bx-icon name="info"></bx-icon><span>
      <b>Start a session of ${h.tile}’s own agent there.</b> This workspace can’t open one for you from here: <a href=${handoffLink(h.tile)} target="_top">open ${h.tile}</a>, start a coding agent in its window${h.draft ? html`, and ${DRAFT_FROM.paste}` : ''}.
      ${h.draft ? html`<div class="small" style="margin-top:4px" data-draft-from>${DRAFT_FROM.label}</div>
        <div class="small muted mono" style="white-space:pre-wrap;overflow-wrap:anywhere;max-height:120px;overflow:auto" data-draft>${h.draft}</div>` : nothing}
      <div class="btns" style="justify-content:flex-start;margin-top:4px">${h.draft ? html`<button data-copy @click=${copy}>${DRAFT_FROM.copy}</button>` : nothing}<button class="quiet" @click=${() => { this._handoff = null; }}>Dismiss</button></div></span></div>`;
  }

  // what the person was looking at, for the page ("about <tile>")
  _tell() {
    const frame = this.renderRoot.querySelector('iframe');
    const m = contextMessage(this.context);
    if (frame && frame.contentWindow && m) frame.contentWindow.postMessage(m, '*');
  }

  // ---- the person ----

  async _pause(e, paused) {
    if (!e.isTrusted) return;
    this._err = '';
    const r = await this.store.setPaused(paused);
    if (!r.ok) this._err = r.error;
  }

  _grip(e) {
    if (this.full || this._narrow) return;
    e.preventDefault();
    const grip = e.currentTarget, right = this.getBoundingClientRect().right;
    grip.setPointerCapture(e.pointerId);
    const move = (ev) => { this._w = clampDock(right - ev.clientX, window.innerWidth); };
    const up = () => {
      grip.removeEventListener('pointermove', move); grip.removeEventListener('pointerup', up); grip.removeEventListener('pointercancel', up);
      call(PREF, { method: 'PUT', body: { w: this._w } });
    };
    grip.addEventListener('pointermove', move); grip.addEventListener('pointerup', up); grip.addEventListener('pointercancel', up);
  }

  _gripKey(e) {
    const d = e.key === 'ArrowLeft' ? 16 : e.key === 'ArrowRight' ? -16 : 0;
    if (!d) return;
    e.preventDefault();
    this._w = clampDock(this._w + d, window.innerWidth);
    call(PREF, { method: 'PUT', body: { w: this._w } });
  }

  _key(e) {
    if (e.key !== 'Escape') return;
    if (this._menu || this._tray) { this._menu = false; this._tray = false; e.stopPropagation(); }
  }

  render() {
    const s = this.store, st = dockState(s);
    const waiting = needs(s).length;
    const said = waiting ? `${waiting} need${waiting === 1 ? 's' : ''} you` : '';
    const live = said !== this._said ? (this._said = said) : this._said;
    const paused = !!(s.state && s.state.paused);
    const usable = st !== 'hidden' && st !== 'setup' && st !== 'offline';
    return html`
      <div class="grip" role="separator" aria-orientation="vertical" aria-label="Resize the workspace agent" tabindex="0" aria-valuemin=${CHOICES.dockMin} aria-valuemax=${CHOICES.dockMax} aria-valuenow=${this._w}
        @pointerdown=${this._grip} @keydown=${this._gripKey}></div>
      <header @keydown=${this._key}>
        <bx-icon name="agent"></bx-icon><h2>Workspace agent</h2>
        ${usable ? html`<button data-needs aria-expanded=${this._tray ? 'true' : 'false'} aria-label=${`Needs you${waiting ? `, ${waiting}` : ''}`} @click=${() => { this._tray = !this._tray; this._menu = false; }}>
            Needs you${waiting ? html`<span class="count" data-count>${waiting}</span>` : nothing}</button>
          <button data-pause aria-pressed=${paused ? 'true' : 'false'} @click=${(e) => this._pause(e, !paused)}><bx-icon name=${paused ? 'play' : 'pause'}></bx-icon><span class="lbl">${paused ? 'Resume' : 'Pause'}</span></button>
          <button class="quiet" data-menu aria-label="More" aria-haspopup="menu" aria-expanded=${this._menu ? 'true' : 'false'} @click=${() => { this._menu = !this._menu; this._tray = false; }}><bx-icon name="ellipsis"></bx-icon></button>` : nothing}
        ${this.full ? nothing : html`<button class="quiet" data-close aria-label="Close the workspace agent" @click=${() => { this.open = false; }}><bx-icon name="xmark"></bx-icon></button>`}
      </header>
      ${this._menu ? html`<div class="menu" role="menu" @keydown=${this._key}>
        ${[['approvals', 'Approvals'], ['activity', 'What my agent did'], ['prefs', 'Preferences']].map(([v, label]) => html`<button role="menuitem" data-view=${v} @click=${() => this.show(v)}>${label}</button>`)}</div>` : nothing}
      <div class="body" @bx-agent-review=${(e) => { e.stopPropagation(); this.review(e.detail.kind, e.detail.id); }} @bx-agent-back=${() => { this._view = 'chat'; }}>
        ${this._tray ? html`<div class="tray" @keydown=${this._key}><bx-agent-needs></bx-agent-needs></div>` : nothing}
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
        ${this._handoffView()}
        ${this._signinFor ? html`<div class="banner" data-signin role="group" aria-label="Sign in to a coding agent"><span>
          <bx-agent-signin harness=${this._signinFor} @bx-signins-changed=${() => this._signinsChanged()}></bx-agent-signin>
          <button class="quiet" data-signin-close @click=${() => { this._signinFor = ''; }}>Close</button></span></div>` : nothing}
        ${this._view === 'approvals' ? html`<bx-agent-approvals></bx-agent-approvals>` : this._view === 'activity' ? html`<bx-agent-activity></bx-agent-activity>`
          : this._view === 'prefs' ? html`<bx-agent-prefs></bx-agent-prefs>`
            : this._frame && (st === 'page' || (this.page && st !== 'paused' && st !== 'hidden')) ? html`<iframe src=${this._frame.url} title="Your workspace agent" sandbox=${this._frame.sandbox}
                credentialless=${this._frame.credentialless ? '' : nothing} @load=${() => { this._frameLoaded = true; this._tell(); this._giveFocus(); }}></iframe>`
              : html`<bx-agent-setup></bx-agent-setup>`}
      </div>
      ${this._sheet ? html`<div class="over"><div class="scrim"></div>
        <bx-agent-approve .request=${this._sheet.req} .person=${this._person()} .position=${this._sheet.position} @bx-agent-done=${this._sheetDone}></bx-agent-approve></div>` : nothing}
      <div class="sr" aria-live="polite">${live}</div>`;
  }
}
if (!customElements.get('bx-agent-dock')) customElements.define('bx-agent-dock', BxAgentDock);
