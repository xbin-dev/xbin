/**
 * bx-agent-approve.js — <bx-agent-approve>, the sheet in which a person
 * approves or denies what their workspace agent asked for
 * (/vendor/bx-agent-approve.js, D232; docs/protocol/delegation.md §Approval
 * requests and tickets). Trusted chrome: it runs in a document that holds
 * the person's own session (the shell's dock, /xbin/agent) and is the only
 * thing on the web that sends an approval.
 *
 * It is drawn from xbind's own reading of the request and from nothing
 * else: its host gives it a request it fetched itself by id (`request`),
 * never text from the agent's page. The agent's own sentence (`summary`)
 * is shown once, in a quote block labelled as unchecked — plain text, like
 * everything here: no markdown, no links, no images.
 *
 *   properties  request   the request, as GET /delegate/requests/<id> answered
 *               person    who is asked ("asking Ana Ruiz")
 *               position  "1 of 3" when more wait ('' none)
 *   events      bx-agent-done {id, verdict: 'allow' | 'deny' | 'later' | 'gone'}
 *
 * While the agent is stopped — workspace agents off, paused, held, no agent
 * (GET /delegate/state, or a 409 {refusal} to an Allow sent from a sheet
 * that was open before) — the sheet says why in the place of Allow and
 * sends nothing more; Deny stays.
 *
 * Against a misclick and against a page that tries to make the click for
 * the person: Allow is not armed for 600 ms after the sheet (or its second
 * step) appears, and only while nothing covers it and the window has the
 * person's attention; a click must be the person's own (isTrusted); Enter
 * never allows — Deny and Escape ("not now") are always there. The wider
 * standing choice is held the same way (WidenGuard). Focus lands on the
 * heading and stays in the sheet.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { chromeCss } from '/vendor/agent-chrome-css.js';
import { agentStore } from '/vendor/agent-chrome-store.js';
import {
  CHOICES, DURATIONS, heading, tile, levelWords, sends, undoWords, grantChoices, allowLabel, allowSpoken, approveBody, widerWords, sessionRows, asked, settled,
  stopRefusal, stopWords, reveal, HIDDEN_NOTE, clipText, clipNote,
} from '/vendor/agent-chrome-words.js';

/** shown(text): the agent's text as parts — each hidden character (bidi, zero-width, control) a chip, ⟨U+202E⟩, never interpreted. */
export const shown = (text) => reveal(text).parts.map((p) => (p.hidden ? html`<span class="hid" title=${`hidden character ${p.hidden}`}>⟨${p.hidden}⟩</span>` : p.text));
/** hiddenLine(text): above text that holds a hidden character, the line that says so (nothing for text that holds none). */
export const hiddenLine = (text) => (reveal(text).hidden ? html`<div class="small hidnote" data-hidden><bx-icon name="warning"></bx-icon><span>${HIDDEN_NOTE}</span></div>` : nothing);

/** uncovered(el): the person can see el and nothing is drawn over its middle — through every shadow root on the way. */
export function uncovered(el) {
  if (!el || !el.isConnected || document.visibilityState !== 'visible') return false;
  const r = el.getBoundingClientRect();
  if (r.width < 4 || r.height < 4) return false;
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  let at = document.elementFromPoint(x, y);
  for (let i = 0; at && at.shadowRoot && i < 16; i++) {
    const inner = at.shadowRoot.elementFromPoint(x, y);
    if (!inner || inner === at) break;
    at = inner;
  }
  return !!at && (at === el || el.contains(at));
}

/**
 * WidenGuard: a control that widens what the agent may do — start tile
 * agents without asking, a wider network, the admin opt-in, a standing
 * approval's wider scope — answers as Allow does, so a page that baits a
 * double-click or scripts a click can't land one on it: not for
 * CHOICES.armMs after it appears (or after rearm()), only the person's own
 * click (isTrusted) while nothing covers it and the window has their
 * attention, and never by Enter. Narrowing — back to asking, no internet,
 * the opt-in off — is never held up. A host marks each such control
 * data-widen ("on": checking it widens; "off": unchecking it does — a
 * radio or a checkbox) and binds @click=${guard.click} @keydown=${guard.key};
 * the control carries aria-disabled until it is armed.
 */
export class WidenGuard {
  constructor(host) { this.host = host; this.seen = new WeakMap(); host.addController(this); }
  hostUpdated() {
    for (const el of this.host.renderRoot.querySelectorAll('[data-widen]')) {
      if (this.seen.has(el)) continue;
      this.seen.set(el, performance.now());
      el.setAttribute('aria-disabled', 'true');
      setTimeout(() => { if (this.seen.has(el) && this.armed(el)) el.setAttribute('aria-disabled', 'false'); }, CHOICES.armMs + 10);
    }
  }
  /** rearm(): every control waits again — the host shows something new (another request, another step). */
  rearm() { this.seen = new WeakMap(); this.host.requestUpdate(); }
  armed(el) { const t = this.seen.get(el); return t != null && performance.now() - t >= CHOICES.armMs; }
  click = (e) => {
    const el = e.currentTarget;
    if (!(el.dataset.widen === 'off' ? !el.checked : el.checked)) return; // (the click's toggle is applied already: this is where it would land)
    if (e.isTrusted && this.armed(el) && document.hasFocus() && uncovered(el)) return;
    e.preventDefault(); // the control stays as it was, and no change follows
    e.stopImmediatePropagation();
  };
  key = (e) => { if (e.key === 'Enter') e.preventDefault(); };
}

export class BxAgentApprove extends LitElement {
  static properties = {
    request: { attribute: false }, person: { type: String }, position: { type: String },
    _choice: { state: true }, _duration: { state: true }, _step: { state: true }, _armed: { state: true }, _busy: { state: true }, _err: { state: true }, _all: { state: true },
    _stopped: { state: true }, _allBrief: { state: true },
  };

  static styles = [chromeCss, css`
    :host { display: block; }
    .sheet { display: flex; flex-direction: column; max-height: 100%; background: var(--bx-panel-2, #262730); border: 2px solid var(--bx-text, #E9EAF0);
      box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
    .in { display: flex; flex-direction: column; gap: 12px; padding: 12px; overflow: auto; min-height: 0; }
    h2[tabindex]:focus, h2[tabindex]:focus-visible { outline: none; } /* where focus lands: read, not acted on */
    h2 b { font-weight: 700; }
    .choices { display: flex; flex-direction: column; gap: 4px; border: 0; margin: 0; padding: 0; min-width: 0; }
    .choices legend { padding: 0; margin-bottom: 4px; }
    .choice { display: flex; gap: 8px; align-items: flex-start; padding: 6px 8px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); cursor: pointer; }
    .choice.on { border-color: var(--bx-accent, #8C9BFF); background: var(--bx-selection, #262C5C); }
    .choice input { margin-top: 3px; }
    .choice .ct { flex: 1; min-width: 0; }
    .choice .ct b { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
    .dur { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .foot { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 8px 12px; border-top: 1px solid var(--bx-border, #33353F); }
    .foot .when { flex: 1; min-width: 120px; }
    .brief { max-height: 220px; }
    @media (prefers-reduced-motion: no-preference) { button.arming { transition: opacity .15s; } }
  `];

  constructor() {
    super();
    this.request = null; this.person = ''; this.position = '';
    this._choice = 'once'; this._duration = ''; this._step = 'main'; this._armed = false; this._busy = false; this._err = ''; this._all = false;
    this._stopped = ''; // a 409's refusal: the agent stopped while the sheet was open
    this._allBrief = false;
    this._timer = 0;
    this.store = agentStore();
    this._widen = new WidenGuard(this); // the wider standing choices
  }

  willUpdate(ch) {
    if (ch.has('request')) { this._choice = 'once'; this._duration = ''; this._step = 'main'; this._err = ''; this._all = false; this._allBrief = false; this._stopped = ''; this._arm(); }
  }

  updated(ch) {
    if (ch.has('request') || ch.has('_step')) this.renderRoot.querySelector('h2')?.focus();
  }

  connectedCallback() { super.connectedCallback(); this._unsub = this.store.subscribe(() => this.requestUpdate()); } // the switches, as they turn
  disconnectedCallback() { super.disconnectedCallback(); clearTimeout(this._timer); if (this._unsub) this._unsub(); }

  /** stopped: why nothing can be allowed now ('' when it can) — xbind's 409, else the state. */
  get stopped() { return this._stopped || stopRefusal(this.store.state); }

  // Allow answers only after the sheet has been in front of the person for a moment
  _arm() {
    this._armed = false;
    this._widen.rearm();
    clearTimeout(this._timer);
    this._timer = setTimeout(() => { this._armed = true; }, CHOICES.armMs);
  }

  get armed() { return this._armed; }

  _done(verdict) { this.dispatchEvent(new CustomEvent('bx-agent-done', { detail: { id: this.request && this.request.id, verdict }, bubbles: true, composed: true })); }

  // the person's own click on an armed, visible button — nothing else allows
  _may(e) {
    return !!e && e.isTrusted && this._armed && !this._busy && document.hasFocus() && uncovered(e.currentTarget);
  }

  async _allow(e) {
    if (!this._may(e) || this.stopped) return;
    const req = this.request;
    if (req.kind === 'call' && this._choice === 'tile' && this._step === 'main') { this._step = 'wider'; this._arm(); return; }
    this._busy = true; this._err = '';
    const r = await this.store.approve(req.id, approveBody(req, this._choice, this._duration || CHOICES.duration[this._choice]));
    this._busy = false;
    // stopped meanwhile: say why where Allow was — never send it again
    if (r.ok) this._done('allow'); else if (r.stopped) this._stopAt(r.stopped); else if (r.gone) this._done('gone'); else { this._err = r.error; this._arm(); }
  }

  async _deny(e) {
    if (!e || !e.isTrusted || this._busy) return;
    this._busy = true; this._err = '';
    const r = await this.store.deny(this.request.id);
    this._busy = false;
    if (r.ok) this._done('deny'); else if (r.gone) this._done('gone'); else this._err = r.error;
  }

  _key(e) {
    if (e.key === 'Escape') { e.preventDefault(); this._done('later'); return; }
    // Enter never allows: on an Allow button it does nothing (Space, the person's deliberate press, does)
    if (e.key === 'Enter' && e.target instanceof HTMLElement && e.target.matches('button[data-allow]')) { e.preventDefault(); return; }
    if (e.key !== 'Tab') return;
    const f = [...this.renderRoot.querySelectorAll('button:not([disabled]), input:not([disabled]), [tabindex="0"]')].filter((x) => x.offsetParent !== null);
    if (!f.length) return;
    const at = this.renderRoot.activeElement, first = f[0], last = f[f.length - 1];
    if (e.shiftKey && (at === first || !f.includes(at))) { e.preventDefault(); last.focus(); } else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus(); }
  }

  render() {
    const req = this.request;
    if (!req) return nothing;
    const pending = req.state === 'pending';
    const h = heading(req);
    return html`<div class="sheet" role="dialog" aria-modal="true" aria-labelledby="h" aria-roledescription="xbin approval" data-kind=${req.kind} data-step=${this._step} @keydown=${this._key}>
      <div class="xbar"><bx-icon name="shield"></bx-icon><span>xbin · approval</span>${this.position ? html`<span>${this.position}</span>` : nothing}
        ${this.person ? html`<span class="who">asking ${this.person}</span>` : nothing}</div>
      ${!pending ? html`<div class="in"><h2 id="h" tabindex="-1">${h.before}<b>${h.strong}</b>${h.after}</h2><p>${settled(req)}</p></div>
          <div class="foot"><span class="when"></span><button class="primary" @click=${() => this._done('gone')}>Close</button></div>`
        : this._step === 'wider' ? this._wider(req) : html`<div class="in">
          <h2 id="h" tabindex="-1">${h.before}<b>${h.strong}</b>${h.after}</h2>
          ${req.kind === 'session' ? this._session(req) : req.kind === 'template' ? this._template(req) : this._call(req)}
          ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
        </div>${this._foot(req)}`}
    </div>`;
  }

  _quote(text, label = "The agent's own words — not checked by xbin") {
    return text ? html`<div class="quote"><span class="ql">${label}</span>${hiddenLine(text)}<span class="qt">${shown(text)}</span></div>` : nothing;
  }

  _tileRow(req) {
    const t = tile(req), p = req.preview || {};
    return html`<dt>Tile</dt><dd><b>${t.name}</b> <span class="mono muted">${t.path}</span>${t.org ? html` <span class="muted">· ${t.org}</span>` : nothing}
      ${levelWords(p.level) ? html`<div class="small muted">${levelWords(p.level)}</div>` : nothing}</dd>`;
  }

  _call(req) {
    const s = sends(req, this._all); // its first CLIP characters until Show all; the note says which
    const choices = grantChoices(req);
    return html`<dl class="rows">
        ${this._tileRow(req)}
        <dt>Action</dt><dd class="mono" data-row="action">${req.method} ${req.target}</dd>
        <dt>Sends</dt><dd data-row="sends">${s.text ? html`${hiddenLine(req.body)}<pre class="body" tabindex="-1">${shown(s.text)}${s.clipped ? '…' : ''}</pre>` : nothing}
          <div class="small muted" data-note>${s.note}${s.clipped ? html` — <button class="link" data-show-all @click=${() => this._showAll('_all')}>Show all</button>` : nothing}</div>
          ${s.binary ? html`<div class="small err">Nobody can read this before it is sent: allowing it approves bytes you have not seen.</div>` : nothing}</dd>
        <dt>Undo</dt><dd>${undoWords(req)}</dd>
      </dl>
      ${this._quote(req.summary)}
      <fieldset class="choices" role="radiogroup" aria-label="Allow">
        <legend><h3>Allow</h3></legend>
        ${choices.map((c) => html`<label class="choice ${this._choice === c.id ? 'on' : ''}" data-choice=${c.id}>
          <input type="radio" name="grant" .checked=${this._choice === c.id} data-widen=${c.wider ? 'on' : nothing} @click=${c.wider ? this._widen.click : nothing}
            @keydown=${c.wider ? this._widen.key : nothing} @change=${() => { this._choice = c.id; this._duration = ''; }}
            aria-label=${`${c.label}${c.wider ? ', wider' : ''}. ${c.text}`}>
          <span class="ct"><b>${c.label}${c.wider ? html`<span class="tag warn"><bx-icon name="warning"></bx-icon>wider</span>` : nothing}</b><span class="small muted">${c.text}</span></span></label>`)}
      </fieldset>
      ${this._choice !== 'once' ? this._durations() : nothing}`;
  }

  _durations() {
    const cur = this._duration || CHOICES.duration[this._choice];
    return html`<div class="dur" role="radiogroup" aria-label="For how long">${DURATIONS.map((d) => html`<label><input type="radio" name="dur" .checked=${cur === d.id}
      @change=${() => { this._duration = d.id; }}> ${d.label}</label>`)}</div>`;
  }

  _wider(req) {
    const w = widerWords(req);
    return html`<div class="in">
        <h2 id="h" tabindex="-1">${w.heading}</h2>
        <div class="banner warn"><bx-icon name="warning"></bx-icon><span>${w.means}</span></div>
        <p class="mono small" data-row="standing">${(req.preview && req.preview.standing && req.preview.standing.tile) || ''}</p>
        ${this._durations()}
        <p class="small muted">${w.ends}</p>
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      </div>
      <div class="foot"><span class="when small muted">${asked(req)}</span>
        ${this.stopped ? this._stop() : html`<button class="danger arming" data-allow="tile" aria-disabled=${this._armed && !this._busy ? 'false' : 'true'} @click=${this._allow}>${w.allow}</button>`}
        <button class="primary" data-back @click=${() => { this._step = 'main'; this._arm(); }}>${w.back}</button></div>`;
  }

  _session(req) {
    const r = sessionRows(req);
    const brief = clipText(r.brief, this._allBrief);
    return html`${r.danger ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${r.danger}</span></div>` : nothing}
      <dl class="rows">
        <dt>Mode</dt><dd data-row="mode"><span class="tag ${r.mode.tone}">${r.mode.tag}</span>${r.mode.name ? html` <span class="mono muted">${r.mode.name}</span>` : nothing}<div>${r.mode.text}</div></dd>
        ${this._tileRow(req)}
        <dt>Coding agent</dt><dd>${r.agent}</dd>
        <dt>Network</dt><dd>${r.network}</dd>
        <dt>Other tiles</dt><dd>${r.others}</dd>
        <dt>What it will be told</dt><dd>${hiddenLine(r.brief)}<pre class="body brief" tabindex="-1" data-row="brief">${r.brief ? html`${shown(brief.text)}${brief.clipped ? '…' : ''}` : '(nothing: it starts without a message)'}</pre>
          <div class="small muted">${brief.clipped ? html`${clipNote(brief)} — <button class="link" data-show-all @click=${() => this._showAll('_allBrief')}>Show all</button> · ` : nothing}sent exactly like this</div></dd>
      </dl>
      ${this._quote(req.summary)}
      <p class="small muted">${r.comes}</p>`;
  }

  _template(req) {
    let b = {};
    try { b = JSON.parse(req.body || '{}') || {}; } catch { /* shown raw below */ }
    const p = req.preview || {};
    const s = sends(req, this._all);
    return html`<dl class="rows">
        <dt>Template</dt><dd class="mono">${b.source || b.template || '(the default)'}</dd>
        <dt>Name and place</dt><dd class="mono">${b.path || b.name || req.target}</dd>
        <dt>Owner</dt><dd>${p.owner || 'you'}</dd>
        <dt>Sends</dt><dd>${hiddenLine(req.body)}<pre class="body" tabindex="-1">${shown(s.text)}${s.clipped ? '…' : ''}</pre>
          <div class="small muted" data-note>${s.note}${s.clipped ? html` — <button class="link" data-show-all @click=${() => this._showAll('_all')}>Show all</button>` : nothing}</div></dd>
      </dl>
      ${this._quote(req.summary)}`;
  }

  _foot(req) {
    const never = req.kind === 'session' && !(req.preview && req.preview.session && req.preview.session.asks === true);
    const on = this._armed && !this._busy;
    return html`<div class="foot">
      <span class="when small muted">${req.kind === 'call' ? asked(req) : `asked every time${req.kind === 'session' ? ' · change in Preferences' : ''}`}</span>
      <button class=${never || this.stopped ? 'primary' : ''} data-deny ?disabled=${this._busy} @click=${this._deny}>Deny</button>
      ${this.stopped ? this._stop() : html`<button class="${never ? 'danger' : 'primary'} arming" data-allow=${this._choice} aria-disabled=${on ? 'false' : 'true'} aria-label=${allowSpoken(req, this._choice)}
        @click=${this._allow}>${allowLabel(req, this._choice)}</button>`}
    </div>`;
  }

  // Show all: the rest of the text, and focus — which was on the button that goes — on the text itself (Escape still closes)
  async _showAll(which) {
    const at = this.renderRoot.activeElement && this.renderRoot.activeElement.closest('dd');
    this[which] = true;
    await this.updateComplete;
    (at && at.querySelector('pre') || this.renderRoot.querySelector('h2'))?.focus();
  }

  // a 409 {refusal}: the reason replaces Allow, and focus — which was on Allow — moves to Deny (Escape still closes)
  async _stopAt(refusal) {
    this._stopped = refusal;
    await this.updateComplete;
    this.renderRoot.querySelector('button[data-deny]')?.focus();
  }

  // in the place of Allow while the agent is stopped: why, and that Deny still works
  _stop() {
    const w = stopWords(this.stopped, 'request');
    return html`<div class="banner warn stopped" role="status" data-stopped=${this.stopped}><bx-icon name="pause"></bx-icon>
      <span><b>${w.title}.</b> ${w.text} ${w.still}</span></div>`;
  }
}
if (!customElements.get('bx-agent-approve')) customElements.define('bx-agent-approve', BxAgentApprove);
