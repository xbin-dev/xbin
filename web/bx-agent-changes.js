/**
 * bx-agent-changes.js — <bx-agent-changes>, the dialog in which an admin
 * reviews a batch of admin changes their workspace agent proposed
 * (/vendor/bx-agent-changes.js, D232; docs/protocol/delegation.md §Change
 * sets). Trusted chrome, owned by the shell: a wide dialog over the
 * workspace (the whole screen when it is narrow), never inside the dock's
 * frame.
 *
 * Every line, its before and after, its effects and its warnings are
 * xbind's own (written from the change itself); the agent's title and its
 * reason for an item are quoted, labelled as unchecked. An item with a
 * warning starts unticked; one xbind blocked is shown with its reason and
 * cannot be ticked. Nothing is applied until the person presses Apply —
 * their own click, on a button armed like the approval sheet's — and xbind
 * may first ask for a recent sign-in (the password, or signing in again:
 * the ticks are kept either way).
 *
 *   properties  set      the change set, as GET /delegate/changesets/<id> answered
 *               person   who is asked
 * While the agent is stopped (off, paused, held, no agent: the state, or a
 * 409 {refusal} to an Apply from a dialog opened before) Apply is replaced
 * by why; Reject all stays.
 *
 * A PLAN (set.plan; docs/protocol/delegation.md §Plans) is reviewed here
 * too: its lines may create tiles and start a tile agent — drawn with the
 * coding agent, the mode and the brief "sent exactly like this", as the
 * single start's sheet draws them. A line says which lines it needs and
 * which need it; unticking one unticks the lines that need it, and a line
 * can be ticked only with the lines it needs. A line that takes an admin's
 * rights is marked: for an admin it starts unticked and reads "applied as
 * you" — their tick and Apply (on a recent sign-in) are the consent for
 * that line; for anyone else it reads "needs an admin", can't be ticked,
 * and Ask an admin sends it to the workspace's admins as a request from
 * them (set.from on the admin's side: the same dialog, saying who asks). A
 * line of a kind this page doesn't know is shown and can't be ticked.
 * After Apply the results show in place; when every chosen line was
 * applied and nothing is left to do, the review closes itself (the plan's
 * card in the conversation keeps each line's outcome) — a failure stays
 * open until the person closes it.
 *
 *   events      bx-agent-done {id, verdict: 'applied' | 'rejected' | 'later', self?: true}
 *               bx-agent-review {kind: 'changeset', id}   (Undo made a new set: review it)
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { chromeCss } from '/vendor/agent-chrome-css.js';
import { agentStore } from '/vendor/agent-chrome-store.js';
import { uncovered, shown, hiddenLine, signinRow, WidenGuard } from '/vendor/bx-agent-approve.js';
import { CHOICES, kindTag, warning, stateText, tickable, initialTicks, reviewWords, resultOf, resultsWords, stepUp, stopRefusal, stopWords, endedWords, knownState, UNKNOWN_STATE, clipText, clipNote } from '/vendor/agent-chrome-words.js';
import { lineState, canTick, planTicks, setTick, keepTicks, lineNotes, planWords, startRows, askedWords, waitingWords, closesItself, closingWords, PLAN_CLOSE_MS } from '/vendor/agent-plan-words.js';

const KEEP = 'xbin-agent-ticks:'; // sessionStorage: a set's ticks, kept across signing in again

export class BxAgentChanges extends LitElement {
  static properties = {
    set: { attribute: false }, person: { type: String },
    _ticks: { state: true }, _signins: { state: true }, _view: { state: true }, _step: { state: true }, _busy: { state: true }, _err: { state: true }, _armed: { state: true },
    _stopped: { state: true }, _briefs: { state: true }, _closing: { state: true },
  };

  static styles = [chromeCss, css`
    :host { position: fixed; inset: 0; z-index: 2147483000; display: flex; align-items: center; justify-content: center; }
    .scrim { position: absolute; inset: 0; background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }
    .dlg { position: relative; display: flex; flex-direction: column; width: min(860px, calc(100vw - 32px)); max-height: calc(100vh - 32px);
      background: var(--bx-panel-2, #262730); border: 2px solid var(--bx-text, #E9EAF0); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
    @media (max-width: 640px) { .dlg { width: 100vw; height: 100vh; max-height: none; border-width: 0; } }
    .in { display: flex; flex-direction: column; gap: 12px; padding: 12px 16px; overflow: auto; min-height: 0; flex: 1; }
    h2[tabindex]:focus, h2[tabindex]:focus-visible { outline: none; } /* where focus lands: read, not acted on */
    .items { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
    .item { display: flex; gap: 12px; align-items: flex-start; padding: 8px 12px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); }
    .item.blocked { opacity: .85; border-style: dashed; }
    .item .ix { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
    .item .line { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; font-weight: 600; }
    .ba { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
    @media (max-width: 640px) { .ba { grid-template-columns: 1fr; } }
    .ba > div { min-width: 0; padding: 4px 8px; border-radius: var(--bx-radius, 2px); white-space: pre-wrap; }
    .ba .b { background: var(--bx-diff-del-bg, #342429); } .ba .a { background: var(--bx-diff-add-bg, #1D302D); }
    .ba .lb { display: block; font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
      text-transform: uppercase; color: var(--bx-muted, #A3A6B6); }
    .warning { display: flex; gap: 8px; align-items: flex-start; }
    .warning .tag { flex: none; }
    .res { display: inline-flex; gap: 6px; align-items: center; font-weight: 600; }
    .res.ok { color: var(--bx-ok, #A3CF5E); } .res.bad { color: var(--bx-danger, #FF7A7A); } .res.warn { color: var(--bx-warn, #F2994A); }
    .foot { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 8px 16px; border-top: 1px solid var(--bx-border, #33353F); }
    .foot .fw { flex: 1; min-width: 160px; }
    .step { display: flex; flex-direction: column; gap: 8px; max-width: 460px; }
    .item.off { border-style: dashed; }
    .item .tag.adm { border-color: var(--bx-warn, #F2994A); color: var(--bx-warn, #F2994A); }
    .item dl.rows { margin: 0; display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; }
    .item dl.rows dt { color: var(--bx-muted, #A3A6B6); } .item dl.rows dd { margin: 0; min-width: 0; }
    .item pre.brief { margin: 4px 0 0; padding: 6px 8px; max-height: 220px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere;
      background: var(--bx-code-bg, #16171D); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); }
    .ask { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); }
    .ask { align-items: center; flex-wrap: wrap; border-color: var(--bx-border, #33353F); }
    .ask .fw { flex: 1; min-width: 200px; }
  `];

  constructor() {
    super();
    this.set = null; this.person = '';
    this._ticks = []; this._view = 'review'; this._step = null; this._busy = false; this._err = ''; this._armed = false;
    this._stopped = ''; this._briefs = []; this._closing = false;
    this._signins = {}; // n → {signin, trust, all}: a start line's own sign-in lines, ticked ("Use my … sign-in", "I trust this tile's source")
    this._widen = new WidenGuard(this); // …a tick held like Apply: the person's own click, a moment after it shows
    this._timer = 0; this._closeTimer = 0;
    this.store = agentStore();
  }

  /** asked: the set is a request from another person (this is an admin's review of it). */
  get asked() { return !!(this.set && this.set.from); }

  willUpdate(ch) {
    if (!ch.has('set') || !this.set) return;
    const cs = this.set;
    if ((ch.get('set') || {}).id !== cs.id) { this._closing = false; clearTimeout(this._closeTimer); } // another set: nothing of the last one's closing
    this._view = cs.state === 'proposed' ? 'review' : 'results';
    this._step = null; this._err = ''; this._stopped = '';
    let kept = null;
    try { kept = JSON.parse(sessionStorage.getItem(KEEP + cs.id) || 'null'); } catch { /* none kept */ }
    const ok = new Set((cs.items || []).filter(tickable).map((it) => it.n));
    if (cs.plan) this._ticks = Array.isArray(kept) ? keepTicks(cs, kept) : planTicks(cs);
    else this._ticks = Array.isArray(kept) ? kept.filter((n) => ok.has(n)) : initialTicks(cs);
    this._briefs = [];
    this._arm();
  }

  updated(ch) { if (ch.has('set') || ch.has('_view') || ch.has('_step')) this.renderRoot.querySelector('h2')?.focus(); }

  connectedCallback() { super.connectedCallback(); this._unsub = this.store.subscribe(() => this.requestUpdate()); }
  disconnectedCallback() { super.disconnectedCallback(); clearTimeout(this._timer); clearTimeout(this._closeTimer); if (this._unsub) this._unsub(); }

  /** stopped: why nothing can be applied now ('' when it can) — xbind's 409, else the state (the asker's agent's, for a request from someone else: only xbind knows it). */
  get stopped() { return this._stopped || (this.asked ? '' : stopRefusal(this.store.state)); }

  _stop() {
    const w = stopWords(this.stopped, 'changeset');
    return html`<div class="banner warn stopped" role="status" data-stopped=${this.stopped}><bx-icon name="pause"></bx-icon>
      <span><b>${w.title}.</b> ${w.text} ${w.still}</span></div>`;
  }

  _arm() { this._armed = false; clearTimeout(this._timer); this._timer = setTimeout(() => { this._armed = true; }, CHOICES.armMs); }

  _done(verdict, self = false) {
    clearTimeout(this._closeTimer);
    this.dispatchEvent(new CustomEvent('bx-agent-done', { detail: { id: this.set && this.set.id, verdict, ...(self ? { self: true } : {}) }, bubbles: true, composed: true }));
  }

  _tick(n, on) {
    if (this.set.plan) this._ticks = on && !canTick(this.set, this._ticks, this.set.items[n - 1]) ? this._ticks : setTick(this.set, this._ticks, n, on);
    else this._ticks = on ? [...new Set([...this._ticks, n])].sort((a, b) => a - b) : this._ticks.filter((x) => x !== n);
    try { sessionStorage.setItem(KEEP + this.set.id, JSON.stringify(this._ticks)); } catch { /* not kept */ }
  }

  // the start lines whose sign-in lines are ticked, among the lines to apply: {signin: [n…], trust: [n…]} (trust only with its sign-in)
  _signinTicks() {
    const on = (k) => this._ticks.filter((n) => this._signins[n] && this._signins[n].signin && this._signins[n][k]);
    return { signin: on('signin'), trust: on('trust') };
  }

  async _apply(e, password = '') {
    if (!e || !e.isTrusted || !this._armed || this._busy || !document.hasFocus() || !uncovered(e.currentTarget)) return;
    if (!this._ticks.length) return this._reject(e);
    if (this.stopped) return; // "Apply none" ends the set rejected
    this._busy = true; this._err = '';
    const r = await this.store.apply(this.set.id, this._ticks, password, this._signinTicks());
    this._busy = false;
    if (r.ok) {
      try { sessionStorage.removeItem(KEEP + this.set.id); } catch { /* nothing kept */ }
      const chosen = this._ticks;
      this.set = r.set; this._view = 'results'; this._step = null;
      // every chosen line applied, nothing left to do here: the results show for a moment and the review closes
      // itself — the plan's card in the conversation keeps them. A failure stays open until the person closes it
      if (closesItself(r.set, chosen)) { this._closing = true; this._closeTimer = setTimeout(() => this._done('applied', true), PLAN_CLOSE_MS); }
      return;
    }
    if (r.stopped) { // never sent again: the reason shows where Apply was, and focus moves to Reject all
      this._stopped = r.stopped; this._step = null;
      await this.updateComplete;
      this.renderRoot.querySelector('button[data-reject]')?.focus();
      return;
    }
    if (r.stepUp) { this._step = { kind: r.stepUp, error: r.error || '' }; this._arm(); return; }
    this._err = r.error; this._arm();
  }

  async _ask(e) {
    if (!e || !e.isTrusted || !this._armed || this._busy || !document.hasFocus() || !uncovered(e.currentTarget)) return;
    this._busy = true; this._err = '';
    const r = await this.store.ask(this.set.id);
    this._busy = false;
    if (r.ok) { this.set = r.set; return; }
    if (r.stopped) this._stopped = r.stopped; else this._err = r.error;
  }

  async _reject(e) {
    if (!e || !e.isTrusted || this._busy) return;
    this._busy = true;
    const r = await this.store.reject(this.set.id);
    this._busy = false;
    if (r.ok) { try { sessionStorage.removeItem(KEEP + this.set.id); } catch { /* nothing kept */ } this._done('rejected'); } else this._err = r.error;
  }

  // a decided plan's line that still waits for xbin: the person takes it back (POST …/reject on a set with a waiting line cancels it)
  async _cancelWaiting(e) {
    if (!e || !e.isTrusted || this._busy) return;
    this._busy = true; this._err = '';
    const r = await this.store.reject(this.set.id);
    this._busy = false;
    if (r.ok && r.set) this.set = r.set; else if (!r.ok) this._err = r.error;
  }

  async _undo(e) {
    if (!e || !e.isTrusted || this._busy) return;
    this._busy = true; this._err = '';
    const r = await this.store.undo(this.set.id);
    this._busy = false;
    if (!r.ok) { this._err = r.error; return; }
    // a new proposal: it opens for review like any other, and does nothing by itself
    this.dispatchEvent(new CustomEvent('bx-agent-review', { detail: { kind: 'changeset', id: r.set.id }, bubbles: true, composed: true }));
  }

  _signIn() {
    // the ticks are in sessionStorage: the page comes back to this set with them
    location.href = `/login?next=${encodeURIComponent(`/xbin/agent?changes=${encodeURIComponent(this.set.id)}`)}`;
  }

  _key(e) {
    if (e.key === 'Escape') { e.preventDefault(); this._done('later'); return; }
    if (e.key === 'Enter' && e.target instanceof HTMLElement && e.target.matches('button[data-apply]')) { e.preventDefault(); return; }
    if (e.key !== 'Tab') return;
    const f = [...this.renderRoot.querySelectorAll('button:not([disabled]), input:not([disabled])')].filter((x) => x.offsetParent !== null);
    if (!f.length) return;
    const at = this.renderRoot.activeElement, first = f[0], last = f[f.length - 1];
    if (e.shiftKey && (at === first || !f.includes(at))) { e.preventDefault(); last.focus(); } else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus(); }
  }

  render() {
    const cs = this.set;
    if (!cs) return nothing;
    return html`<div class="scrim"></div>
      <div class="dlg" role="dialog" aria-modal="true" aria-labelledby="h" data-view=${this._step ? 'stepup' : this._view} @keydown=${this._key}>
        <div class="xbar"><bx-icon name="shield"></bx-icon><span data-bar>${cs.plan ? planWords(cs, this._ticks).bar : 'xbin · admin changes'}</span>${this.person ? html`<span class="who">asking ${this.person}</span>` : nothing}</div>
        ${this._step ? this._stepUp(cs) : this._view === 'results' ? this._results(cs) : this._review(cs)}
      </div>`;
  }

  _item(it, i, total, review) {
    const cs = this.set, plan = !!cs.plan;
    const on = this._ticks.includes(it.n);
    const res = review ? null : resultOf(it);
    const st = plan ? lineState(it) : (it.blocked ? 'blocked' : 'ok');
    const notes = plan ? lineNotes(cs, it) : { tag: '', needs: '', neededBy: '', why: '' };
    const can = plan ? canTick(cs, this._ticks, it) : tickable(it);
    return html`<li class="item ${it.blocked ? 'blocked' : ''} ${st !== 'ok' && st !== 'blocked' ? 'off' : ''}" data-n=${it.n} data-kind=${it.kind} data-line=${st} role="group"
        aria-label=${`Change ${i + 1} of ${total}${review ? (on ? ', selected' : ', not selected') : ''}`}>
      ${review ? html`<input type="checkbox" aria-label=${`Apply: ${it.line || it.kind}`} .checked=${on} ?disabled=${!can && !on} @change=${(e) => { this._tick(it.n, e.target.checked); e.target.checked = this._ticks.includes(it.n); }}>` : nothing}
      <div class="ix">
        <div class="line"><span>${it.line || it.kind}</span><span class="tag">${kindTag(it.kind)}</span>${notes.tag ? html`<span class="tag adm" data-admin=${notes.tag}>${notes.tag}</span>` : nothing}
          ${res && res.word ? html`<span class="res ${res.tone}" data-result=${it.result || ''}>${res.icon ? html`<bx-icon name=${res.icon}></bx-icon>` : nothing}${res.word}</span>` : nothing}</div>
        ${(it.warnings || []).map((w) => { const x = warning(w); return html`<div class="warning" data-warning=${x.tag}><span class="tag bad"><bx-icon name=${x.icon}></bx-icon>${x.tag}</span><span>${x.text}</span></div>`; })}
        ${it.blocked && review ? html`<div class="banner warn" data-blocked><bx-icon name="lock"></bx-icon><span><b>Can't be applied.</b> ${it.blocked}</span></div>` : nothing}
        ${review && notes.why ? html`<div class="small muted" data-why>${notes.why}</div>` : nothing}
        ${res && res.text ? html`<div class="small ${res.tone === 'bad' ? 'err' : 'muted'}">${res.text}</div>` : nothing}
        ${it.kind === 'agent.start' && it.start ? this._start(it) : nothing}
        ${it.before != null || it.after != null ? html`<div class="ba mono small"><div class="b" data-before><span class="lb">Before</span>${stateText(it, 'before') ?? '—'}</div><div class="a" data-after><span class="lb">After</span>${stateText(it, 'after') ?? '—'}</div></div>` : nothing}
        ${(it.effects || []).length ? html`<div class="small" data-effects>${[].concat(it.effects).join(' · ')}</div>` : nothing}
        ${review && (notes.needs || notes.neededBy) ? html`<div class="small muted" data-needs>${[notes.needs, notes.neededBy].filter(Boolean).join(' · ')}</div>` : nothing}
        ${!review && it.result === 'applied' && it.undoByHand ? html`<div class="small muted" data-byhand>${it.undoByHand}.</div>` : nothing}
        ${it.why ? html`<div class="quote"><span class="ql">Agent's reason — not checked by xbin</span>${hiddenLine(it.why)}<span class="qt">${shown(it.why)}</span></div>` : nothing}
      </div></li>`;
  }

  // an agent.start line: the coding agent, the mode and the brief, as the single start's sheet draws them
  _start(it) {
    const r = startRows(it);
    const all = this._briefs.includes(it.n);
    const brief = clipText(r.brief, all);
    return html`<dl class="rows small" data-start>
        <dt>Mode</dt><dd data-row="mode"><span class="tag ${r.mode.tone}">${r.mode.tag}</span>${r.mode.name ? html` <span class="mono muted">${r.mode.name}</span>` : nothing}<div>${r.mode.text}</div></dd>
        <dt>Coding agent</dt><dd data-row="agent">${r.agent}</dd>
        ${signinRow(r, this._view === 'review' && !this._busy ? { ...(this._signins[it.n] || {}), guard: this._widen,
    set: (patch) => { this._signins = { ...this._signins, [it.n]: { ...(this._signins[it.n] || {}), ...patch } }; } } : null)}
        <dt>Network</dt><dd>${r.network}</dd>
        <dt>What it will be told</dt><dd>${r.briefBy ? html`<div class="small" data-row="brief-by">${r.briefBy}</div>` : nothing}${hiddenLine(r.brief)}<pre class="brief mono" tabindex="-1" data-row="brief">${shown(brief.text)}${brief.clipped ? '…' : ''}</pre>
          <div class="small muted">${brief.clipped ? html`${clipNote(brief)} — <button class="link" data-show-all @click=${() => { this._briefs = [...this._briefs, it.n]; }}>Show all</button> · ` : nothing}sent exactly like this</div></dd>
      </dl>`;
  }

  _review(cs) {
    const w = cs.plan ? planWords(cs, this._ticks) : { ...reviewWords(cs, this._ticks), reject: 'Reject all', ask: null, byHand: [] };
    const items = cs.items || [];
    return html`<div class="in">
        <h2 id="h" tabindex="-1">${w.heading}</h2>
        ${cs.title ? html`<div class="quote"><span class="ql">${this.asked ? `Their agent's title — not checked by xbin` : `The agent's title — not checked by xbin`}</span>${hiddenLine(cs.title)}<span class="qt">${shown(cs.title)}</span></div>` : nothing}
        <p class="muted">${w.intro}</p>
        <ul class="items">${items.map((it, i) => this._item(it, i, items.length, true))}</ul>
        ${w.byHand.length ? html`<div class="banner" data-byhand><bx-icon name="info"></bx-icon><span><b>Not undone by this.</b> ${w.byHand.join('. ')}.</span></div>` : nothing}
        ${w.ask ? this._askRow(w.ask) : nothing}
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      </div>
      <div class="foot"><span class="fw small muted" aria-live="polite">${w.footer}</span>
        <button data-reject ?disabled=${this._busy} @click=${this._reject}>${w.reject}</button>
        ${this.stopped ? this._stop() : html`<button class="primary" data-apply aria-disabled=${this._armed && !this._busy ? 'false' : 'true'} @click=${this._apply}>${w.apply}</button>`}</div>`;
  }

  _askRow(a) {
    return html`<div class="ask" data-ask><span class="fw"><b>${a.text}</b> <span class="small muted">${a.why}</span></span>
      ${a.can && !this.stopped ? html`<button data-ask-admin aria-disabled=${this._armed && !this._busy ? 'false' : 'true'} @click=${this._ask}>${a.button}</button>` : nothing}</div>`;
  }

  _stepUp(cs) {
    const s = stepUp(this._step.kind);
    return html`<div class="in"><div class="step" data-stepup=${this._step.kind}>
        <h2 id="h" tabindex="-1">${s.heading}</h2>
        <p>${s.text}</p>
        ${s.password ? html`<label>Password <input type="password" autocomplete="current-password" id="pw" @keydown=${(e) => { if (e.key === 'Enter') e.preventDefault(); }}></label>
          <p class="small muted">${s.sso} <button class="link" @click=${() => this._signIn()}>Sign in again</button></p>` : nothing}
        ${this._step.error ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._step.error}</span></div>` : nothing}
        <p class="small muted">${reviewWords(cs, this._ticks).footer}</p>
      </div></div>
      <div class="foot"><span class="fw"></span>
        <button data-notnow @click=${() => { this._step = null; this._arm(); }}>Not now</button>
        ${this.stopped ? this._stop() : s.password ? html`<button class="primary" data-apply aria-disabled=${this._armed && !this._busy ? 'false' : 'true'}
            @click=${(e) => this._apply(e, this.renderRoot.getElementById('pw').value)}>${s.action}</button>`
          : html`<button class="primary" data-signin @click=${() => this._signIn()}>${s.action}</button>`}</div>`;
  }

  _results(cs) {
    const w = resultsWords(cs);
    const items = cs.items || [];
    const state = endedWords(cs);
    return html`<div class="in">
        <h2 id="h" tabindex="-1">${!knownState(cs, 'changeset') ? UNKNOWN_STATE.pageTitle : state ? 'Nothing was applied' : w.title}</h2>
        ${state ? html`<p class="muted">${state}</p>` : nothing}
        ${w.banner ? html`<div class="banner warn" role="status"><bx-icon name="warning"></bx-icon><span>${w.banner}</span></div>` : nothing}
        ${cs.plan && askedWords(cs) ? html`<p class="muted" data-asked>${askedWords(cs)}</p>` : nothing}
        ${cs.plan && waitingWords(cs) ? html`<p class="muted" data-waiting>${waitingWords(cs)}</p>` : nothing}
        <ul class="items">${items.map((it, i) => this._item(it, i, items.length, false))}</ul>
        ${cs.plan && !state && planWords(cs, []).ask ? this._askRow(planWords(cs, []).ask) : nothing}
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      </div>
      ${this._closing ? html`<div class="foot" data-closing><span class="fw small muted" role="status">${closingWords}</span></div>`
        : html`<div class="foot"><span class="fw"></span>
        ${cs.plan && waitingWords(cs) ? html`<button data-cancel-waiting ?disabled=${this._busy} @click=${this._cancelWaiting}>Cancel what waits</button>` : nothing}
        ${w.undo && !state ? html`<button data-undo ?disabled=${this._busy} @click=${this._undo}>${w.undo}</button>` : nothing}
        <button class="primary" data-done @click=${() => this._done(cs.state === 'rejected' ? 'rejected' : 'applied')}>${cs.plan && w.banner ? 'Close' : 'Done'}</button></div>`}`;
  }
}
if (!customElements.get('bx-agent-changes')) customElements.define('bx-agent-changes', BxAgentChanges);
