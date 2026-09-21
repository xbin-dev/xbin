/**
 * bx-agent.js — the Agent tab in the terminal window (<bx-agent
 * session=<id> component=<cwd>>). An agent session is a terminal session
 * whose sandbox runs a coding agent over the Agent Client Protocol (D74,
 * docs/overview/09-terminals.md §Agent sessions); this element drives it
 * through the agent API and renders its typed event log.
 *
 * Source of truth is the session's event log: on mount the element replays
 * it (`GET …/events?since=0`), then applies live `session` events for its
 * topic. Because the events hub drops a slow subscriber and reconnects
 * silently, the element never trusts the live stream alone — it re-fetches
 * `?since=<last>` whenever a live seq skips, when the tab becomes visible,
 * and every 2 s while a turn runs. Any attached client (this tab, another
 * browser, `bx agent`) sees the same stream, and the first to answer a
 * permission request wins.
 *
 * Events: 'bx-session' (detail {id, kind:'agent'}) when it creates the
 * session, so the frame records the id; 'bx-exit' when the session ends.
 */
import { LitElement, html, css, nothing } from 'lit';
import { repeat } from 'lit';
import { onEvent } from '/vendor/events-socket.js';
import { esc } from '/vendor/bx-kit.js';
import { md } from '/vendor/bx-md.js';

const KIND_ICON = { read: '📖', edit: '✏️', delete: '🗑️', move: '↪', search: '🔎', execute: '⚙', think: '💭', fetch: '🌐', other: '•' };

export class BxAgent extends LitElement {
  static properties = {
    session: { type: String },
    component: { type: String },
    _events: { state: true },
    _providers: { state: true },
    _provider: { state: true },
    _mode: { state: true },
    _draft: { state: true },
    _truncated: { state: true },
    _error: { state: true },
  };

  static styles = css`
    :host { display: flex; flex-direction: column; height: 100%; min-height: 0;
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      font: 13px/1.5 var(--bx-sans, system-ui, sans-serif); }
    .scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 10px 12px; }
    .row { margin: 0 0 10px; }
    .who { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase;
      letter-spacing: .04em; color: var(--bx-muted, #868f9a); margin-bottom: 2px; }
    .user .bubble { background: var(--bx-panel-2, #2b3038); border-radius: 8px; padding: 6px 10px; white-space: pre-wrap; }
    .agent .bubble > :first-child { margin-top: 0; }
    .agent .bubble > :last-child { margin-bottom: 0; }
    .bubble :is(pre, code) { font-family: var(--bx-mono, ui-monospace, monospace); }
    .bubble pre { background: var(--bx-term-bg, #262c36); padding: 8px 10px; border-radius: 6px; overflow-x: auto; }
    .bubble :not(pre) > code { background: var(--bx-term-bg, #262c36); padding: .1em .3em; border-radius: 3px; }
    .bubble a { color: var(--bx-accent, #f5a623); }
    .md-img { color: var(--bx-muted, #868f9a); font-style: italic; }
    .thought { color: var(--bx-muted, #868f9a); font-style: italic; white-space: pre-wrap;
      border-left: 2px solid var(--bx-border, #363c45); padding-left: 8px; }
    .tool { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; margin: 0 0 8px; overflow: hidden; }
    .tool > summary { list-style: none; cursor: pointer; padding: 5px 9px; display: flex; align-items: center; gap: 6px;
      font: 11px var(--bx-mono, ui-monospace, monospace); }
    .tool > summary::-webkit-details-marker { display: none; }
    .tool .title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .chip { font: 9.5px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; letter-spacing: .03em;
      padding: 1px 5px; border-radius: 3px; background: var(--bx-panel-2, #2b3038); color: var(--bx-muted, #868f9a); }
    .chip.completed { color: var(--bx-green, #4caf50); }
    .chip.failed, .chip.cancelled { color: var(--bx-red, #ef5350); }
    .chip.in_progress, .chip.pending { color: var(--bx-amber, #f2a71b); }
    .tool .body { padding: 6px 9px; border-top: 1px solid var(--bx-border, #363c45); }
    .tool pre { margin: 0; font: 11px var(--bx-mono, ui-monospace, monospace); white-space: pre-wrap; overflow-x: auto; }
    .diff .add { color: var(--bx-green, #4caf50); }
    .diff .del { color: var(--bx-red, #ef5350); }
    .plan { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 10px; margin: 0 0 8px; }
    .plan .h { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; color: var(--bx-muted, #868f9a); margin-bottom: 4px; }
    .plan li { list-style: none; margin: 1px 0; }
    .plan .done { color: var(--bx-green, #4caf50); text-decoration: line-through; opacity: .7; }
    .perm { border: 1px solid var(--bx-amber, #f2a71b); border-radius: 6px; padding: 8px 10px; margin: 0 0 10px;
      background: color-mix(in srgb, var(--bx-amber, #f2a71b) 8%, var(--bx-panel, #23272e)); }
    .perm .q { margin-bottom: 6px; }
    .perm .btns { display: flex; gap: 6px; flex-wrap: wrap; }
    .perm button { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      border-radius: 5px; padding: 3px 10px; cursor: pointer; font: 12px var(--bx-sans, system-ui); }
    .perm button.allow { border-color: var(--bx-green, #4caf50); }
    .perm button.deny { border-color: var(--bx-red, #ef5350); }
    .perm .settled { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); }
    .gap { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); text-align: center; margin: 4px 0; }
    .turn { border-top: 1px dashed var(--bx-border, #363c45); margin: 10px 0; padding-top: 4px;
      font: 10px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); text-align: center; }
    .foot { flex: none; border-top: 1px solid var(--bx-border, #363c45); padding: 6px 8px; }
    .status { font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a);
      display: flex; align-items: center; gap: 8px; margin-bottom: 5px; min-height: 14px; }
    .status .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--bx-muted, #868f9a); flex: none; }
    .status .dot.running, .status .dot.waiting_permission { background: var(--bx-amber, #f2a71b); }
    .status .dot.idle { background: var(--bx-green, #4caf50); }
    .status .dot.error, .status .dot.exited { background: var(--bx-red, #ef5350); }
    .status .err { color: var(--bx-red, #ef5350); }
    .compose { display: flex; gap: 6px; align-items: flex-end; }
    .compose textarea { flex: 1; resize: none; background: var(--bx-term-bg, #262c36); color: var(--bx-text, #d4d9e0);
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 8px;
      font: 13px var(--bx-sans, system-ui); max-height: 40vh; }
    .compose button { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0);
      border-radius: 6px; padding: 6px 12px; cursor: pointer; font: 12px var(--bx-sans, system-ui); }
    .compose button.cancel { border-color: var(--bx-red, #ef5350); }
    .chooser { display: flex; gap: 6px; margin-bottom: 6px; flex-wrap: wrap; }
    .chooser label { display: inline-flex; align-items: center; gap: 4px; font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); }
    .chooser select { background: var(--bx-term-bg, #262c36); color: var(--bx-text, #d4d9e0);
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px; padding: 3px 6px; font: 12px var(--bx-mono, ui-monospace, monospace); }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
  `;

  constructor() {
    super();
    this._events = [];
    this._providers = null;
    this._provider = '';
    this._mode = '';
    this._draft = '';
    this._truncated = false;
    this._error = '';
    this._lastSeq = 0;
    this._off = null;
    this._poll = null;
    this._creating = false;
    this._onVisible = () => { if (document.visibilityState === 'visible' && this.session) this._refetch(); };
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => this._live(e));
    document.addEventListener('visibilitychange', this._onVisible);
    if (this.session) this._load(0);
    else this._loadProviders();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    document.removeEventListener('visibilitychange', this._onVisible);
    clearInterval(this._poll); this._poll = null;
  }

  updated(ch) {
    // a reattach (the frame set our session after a listing): start replaying
    if (ch.has('session') && this.session && !this._lastSeq && !this._events.length) this._load(0);
    const sc = this.renderRoot?.querySelector('.scroll');
    if (sc && this._atBottom !== false) sc.scrollTop = sc.scrollHeight;
  }

  // ---- data ----

  async _loadProviders() {
    try {
      const r = await fetch('/api/xbin/agent/providers');
      if (!r.ok) return;
      const ps = await r.json();
      this._providers = ps;
      if (ps.length && !this._provider) { this._provider = ps[0].id; this._mode = ps[0].defaultMode || ''; }
    } catch { /* offline; the composer still shows */ }
  }

  async _load(since) {
    if (!this.session) return;
    try {
      const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/events?since=${since}`);
      if (!r.ok) { if (r.status === 404) this._end(); return; }
      const { events, next, truncated } = await r.json();
      this._merge(events || [], !!truncated);
      if (typeof next === 'number' && next > this._lastSeq) this._lastSeq = next;
      this._maybePoll();
    } catch { /* transient; the live stream or the next poll recovers */ }
  }

  _refetch() { this._load(this._lastSeq); }

  // merge new events by seq (dedup; ignore anything already applied)
  _merge(events, truncated) {
    if (truncated) this._truncated = true;
    const seen = new Set(this._events.map((e) => e.seq));
    let added = false;
    for (const e of events) {
      if (e.seq <= this._lastSeq && seen.has(e.seq)) continue;
      if (seen.has(e.seq)) continue;
      seen.add(e.seq);
      this._events = [...this._events, e];
      added = true;
      if (e.seq > this._lastSeq) this._lastSeq = e.seq;
    }
    if (added) { this._events = [...this._events].sort((a, b) => a.seq - b.seq); this.requestUpdate(); this._maybePoll(); }
  }

  // a live event for our session; a skipped seq means the hub dropped one,
  // so re-fetch from the last we have rather than trust the gap
  _live(e) {
    if (e.type !== 'session' || !this.session || e.topic !== 'session.' + this.session) return;
    const ev = e.data;
    if (!ev || typeof ev.seq !== 'number') return;
    if (ev.seq <= this._lastSeq) return;
    if (ev.seq === this._lastSeq + 1) this._merge([ev], false);
    else this._refetch(); // a gap: catch up from the log
  }

  _maybePoll() {
    const busy = this._status() === 'running' || this._status() === 'waiting_permission';
    if (busy && !this._poll) this._poll = setInterval(() => this._refetch(), 2000);
    else if (!busy && this._poll) { clearInterval(this._poll); this._poll = null; }
  }

  _end() {
    // the session is gone server-side (exited, or removed): let the frame close the tab
    this.dispatchEvent(new CustomEvent('bx-exit', { bubbles: true }));
  }

  // ---- actions ----

  async _submit() {
    const text = this._draft.trim();
    if (this._creating) return;
    if (!this.session) {
      if (!(await this._create())) return;
      if (!text) return; // started; the settings pickers show now, the first prompt can wait
    }
    if (!text) return;
    this._draft = '';
    try {
      const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/prompt`,
        { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text }) });
      if (!r.ok) this._error = (await r.json().catch(() => ({}))).error || `prompt failed (${r.status})`;
      else { this._error = ''; this._refetch(); }
    } catch (e) { this._error = String(e.message || e); }
  }

  async _create() {
    this._creating = true; this._error = '';
    try {
      const r = await fetch('/api/xbin/term/sessions', {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ cwd: this.component, kind: 'agent', provider: this._provider, mode: this._mode }),
      });
      const info = await r.json().catch(() => ({}));
      if (!r.ok) { this._error = info.error || `could not start (${r.status})`; return false; }
      this.session = info.id;
      this.setAttribute('session', info.id);
      this.dispatchEvent(new CustomEvent('bx-session', { detail: { id: info.id, kind: 'agent', provider: info.provider, name: info.name }, bubbles: true }));
      this._lastSeq = 0; this._events = [];
      this._load(0);
      return true;
    } catch (e) { this._error = String(e.message || e); return false; }
    finally { this._creating = false; }
  }

  _cancel() {
    if (!this.session) return;
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/cancel`, { method: 'POST' }).catch(() => { });
  }

  _permit(pid, decision) {
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/permissions/${encodeURIComponent(pid)}`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ decision }) })
      .then((r) => { if (r.ok) this._refetch(); });
  }

  _setOption(id, value) {
    if (!this.session) return;
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/options`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ id, value }) })
      .then(async (r) => { if (!r.ok) this._error = (await r.json().catch(() => ({}))).error || `could not set ${id}`; else { this._error = ''; this._refetch(); } });
  }

  // the agent's session settings (model, effort, …): the last status event's
  // options list, as the agent reported it
  _options() {
    let opts = [];
    for (const e of this._events) if (e.type === 'status' && Array.isArray(e.data?.options)) opts = e.data.options;
    return opts;
  }

  _key(ev) {
    if (ev.key === 'Enter' && !ev.shiftKey) { ev.preventDefault(); this._submit(); }
  }

  // ---- view model ----

  // fold the event log into ordered blocks (streaming-tolerant: deltas of one
  // run concatenate, tool updates land on their call, a plan replaces).
  _blocks() {
    const blocks = [];
    const tools = new Map();
    const perms = new Map();
    let plan = null, cur = null;
    for (const e of this._events) {
      const d = e.data || {};
      switch (e.type) {
        case 'message.delta': {
          const role = d.role || 'agent';
          if (cur && cur.kind === 'msg' && cur.role === role && cur.mid === (d.messageId || '')) cur.text += d.text || '';
          else { cur = { kind: 'msg', role, mid: d.messageId || '', text: d.text || '' }; blocks.push(cur); }
          break;
        }
        case 'thought.delta':
          if (cur && cur.kind === 'thought') cur.text += d.text || '';
          else { cur = { kind: 'thought', text: d.text || '' }; blocks.push(cur); }
          break;
        case 'tool.call': case 'tool.update': {
          cur = null;
          let t = tools.get(d.id);
          if (!t) { t = { kind: 'tool', id: d.id, title: '', tk: 'other', status: 'pending', content: null }; tools.set(d.id, t); blocks.push(t); }
          if (d.title != null) t.title = d.title;
          if (d.kind != null) t.tk = d.kind;
          if (d.status != null) t.status = d.status;
          if (d.content != null) t.content = d.content;
          break;
        }
        case 'plan':
          cur = null;
          if (!plan) { plan = { kind: 'plan', entries: [] }; blocks.push(plan); }
          plan.entries = d.entries || [];
          break;
        case 'permission.request': {
          cur = null;
          const p = { kind: 'perm', pid: d.pid, tool: d.toolCall || {}, options: d.options || [], by: null, optionId: null };
          perms.set(d.pid, p); blocks.push(p);
          break;
        }
        case 'permission.resolved': {
          const p = perms.get(d.pid);
          if (p) { p.by = d.by; p.optionId = d.optionId; }
          break;
        }
        case 'turn.end':
          cur = null;
          blocks.push({ kind: 'turn', turn: d.turn, stopReason: d.stopReason, usage: d.usage, error: d.error });
          break;
        case 'gap':
          cur = null;
          blocks.push({ kind: 'gap' });
          break;
      }
    }
    return blocks;
  }

  _status() {
    let s = this.session ? 'starting' : 'new';
    for (const e of this._events) if (e.type === 'status' && e.data?.status) s = e.data.status;
    return s;
  }

  _statusDetail() {
    let detail = '';
    for (const e of this._events) if (e.type === 'status') detail = e.data?.detail || detail;
    return detail;
  }

  _curMode() {
    let m = this._mode;
    for (const e of this._events) if (e.type === 'status' && e.data?.currentMode) m = e.data.currentMode;
    return m;
  }

  // ---- render ----

  render() {
    const status = this._status();
    const busy = status === 'running' || status === 'waiting_permission';
    return html`
      <div class="scroll" @scroll=${this._onScroll}>
        ${this._truncated ? html`<div class="gap">… earlier events dropped (log limit)</div>` : nothing}
        ${!this.session && !this._events.length ? html`<div class="hint">Start a coding agent in this tile's sandbox. Pick a provider, then send a message.</div>` : nothing}
        ${repeat(this._blocks(), (b, i) => b.pid || b.id || i, (b) => this._block(b))}
      </div>
      <div class="foot">
        <div class="status">
          <span class="dot ${status}"></span>
          <span>${this.session ? status.replace('_', ' ') : 'not started'}</span>
          ${this._curMode() && !this._options().length ? html`<span>· ${this._curMode()}</span>` : nothing}
          ${this._usage()}
          ${this._error || this._statusDetail() ? html`<span class="err">${this._error || this._statusDetail()}</span>` : nothing}
        </div>
        ${!this.session ? this._chooser() : this._settings()}
        <div class="compose">
          <textarea rows="1" placeholder=${busy ? 'A turn is running…' : 'Message the agent (Enter to send, Shift+Enter for a newline)'}
            .value=${this._draft} @input=${(e) => { this._draft = e.target.value; this._autosize(e.target); }}
            @keydown=${this._key}></textarea>
          ${busy
            ? html`<button class="cancel" @click=${this._cancel} title="interrupt the running turn">Stop</button>`
            : html`<button @click=${this._submit} ?disabled=${this._creating} title=${this.session ? 'send (Enter)' : 'start the agent — with a message it sends it too; without one you can pick the model first'}>${this.session ? 'Send' : 'Start'}</button>`}
        </div>
      </div>`;
  }

  _chooser() {
    const ps = this._providers || [];
    const prov = ps.find((p) => p.id === this._provider);
    return html`<div class="chooser">
      <select title="provider" @change=${(e) => { this._provider = e.target.value; const p = ps.find((x) => x.id === e.target.value); this._mode = p?.defaultMode || ''; }}>
        ${ps.length ? ps.map((p) => html`<option value=${p.id} ?selected=${p.id === this._provider}>${p.name}</option>`)
          : html`<option>loading…</option>`}
      </select>
      ${prov?.modes?.length ? html`<select title="mode" @change=${(e) => { this._mode = e.target.value; }}>
        ${prov.modes.map((m) => html`<option value=${m.id} ?selected=${m.id === this._curMode()}>${m.name}${m.explicit ? ' ⚠' : ''}</option>`)}
      </select>` : nothing}
    </div>`;
  }

  // _settings: one select per setting the agent advertised (model, effort,
  // mode, …), live — changing one calls set_config_option for the next turn.
  _settings() {
    const opts = this._options().filter((o) => o.type === 'select' && Array.isArray(o.options) && o.options.length);
    if (!opts.length) return nothing;
    return html`<div class="chooser settings">
      ${opts.map((o) => html`<label title=${o.description || o.name}><span class="lbl">${o.name}</span>
        <select @change=${(e) => this._setOption(o.id, e.target.value)}>
          ${o.options.map((v) => html`<option value=${v.value} ?selected=${v.value === o.currentValue} title=${v.description || ''}>${v.name || v.value}</option>`)}
        </select></label>`)}
    </div>`;
  }

  _usage() {
    let u = null;
    for (const e of this._events) if (e.type === 'turn.end' && e.data?.usage) u = e.data.usage;
    if (!u) return nothing;
    return html`<span>· ${fmtN(u.used)}/${fmtN(u.size)} tokens</span>`;
  }

  _block(b) {
    switch (b.kind) {
      case 'msg':
        return b.role === 'user'
          ? html`<div class="row user"><div class="who">you</div><div class="bubble">${b.text}</div></div>`
          : html`<div class="row agent"><div class="who">agent</div><div class="bubble" .innerHTML=${md(b.text)}></div></div>`;
      case 'thought':
        return html`<div class="row"><div class="thought">${b.text}</div></div>`;
      case 'tool':
        return html`<details class="tool" ?open=${b.status === 'failed'}>
          <summary><span>${KIND_ICON[b.tk] || KIND_ICON.other}</span>
            <span class="title">${b.title || b.id}</span>
            <span class="chip ${b.status}">${b.status}${this._diffStat(b.content)}</span></summary>
          ${this._toolBody(b)}
        </details>`;
      case 'plan':
        return html`<div class="plan"><div class="h">plan</div><ul>
          ${(b.entries || []).map((en) => html`<li class="${en.status === 'completed' ? 'done' : ''}">${en.status === 'completed' ? '✓' : en.status === 'in_progress' ? '▸' : '○'} ${en.content}</li>`)}
        </ul></div>`;
      case 'perm':
        return this._permCard(b);
      case 'turn':
        return html`<div class="turn">turn ${b.turn ?? ''} · ${b.stopReason || 'done'}${b.error ? html` — <span class="err">${b.error}</span>` : nothing}</div>`;
      case 'gap':
        return html`<div class="gap">… earlier events dropped (log limit)</div>`;
      default:
        return nothing;
    }
  }

  _toolBody(b) {
    const items = Array.isArray(b.content) ? b.content : null;
    if (!items || !items.length) return nothing;
    return html`<div class="body">${items.map((it) => {
      if (it.type === 'diff') {
        return html`<pre class="diff">${diffLines(it.oldText, it.newText, it.path)}</pre>`;
      }
      if (it.type === 'terminal') return html`<pre>[terminal ${esc(it.terminalId || '')}]</pre>`;
      const text = it.content?.text ?? it.text ?? '';
      return html`<pre>${String(text)}</pre>`;
    })}</div>`;
  }

  _diffStat(content) {
    const items = Array.isArray(content) ? content : [];
    let add = 0, del = 0;
    for (const it of items) if (it.type === 'diff') { add += count(it.newText); del += count(it.oldText); }
    return add || del ? html` +${add}/-${del}` : nothing;
  }

  _permCard(b) {
    const title = b.tool?.title || b.tool?.id || 'a tool call';
    if (b.by) {
      const who = b.by === 'auto' ? 'the session rule' : b.by === 'cancel' ? 'cancel' : b.by.replace('user:', '');
      const verb = (b.optionId && /allow/i.test(b.optionId)) || b.by === 'auto' ? 'allowed' : 'answered';
      return html`<div class="perm"><div class="q">${esc(title)}</div><div class="settled">${verb} by ${esc(who)}</div></div>`;
    }
    return html`<div class="perm">
      <div class="q"><b>Permission:</b> ${esc(title)}</div>
      <div class="btns">
        <button class="allow" @click=${() => this._permit(b.pid, 'allow_once')}>Allow once</button>
        <button class="allow" @click=${() => this._permit(b.pid, 'allow_always')}>Allow for session</button>
        <button class="deny" @click=${() => this._permit(b.pid, 'reject_once')}>Deny</button>
      </div>
    </div>`;
  }

  _onScroll(e) {
    const el = e.target;
    this._atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }

  _autosize(el) {
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, window.innerHeight * 0.4) + 'px';
  }

  // Test hook (the harness): the transcript as plain records, and the actions.
  testApi() {
    const a = this;
    return {
      get status() { return a._status(); },
      get sessionId() { return a.session || null; },
      get provider() { return a._provider; },
      get blocks() { return a._blocks().map((b) => ({ kind: b.kind, role: b.role, text: b.text, status: b.status, pid: b.pid, by: b.by, stopReason: b.stopReason })); },
      get pending() { return a._blocks().filter((b) => b.kind === 'perm' && !b.by).map((b) => b.pid); },
      setProvider(id) { a._provider = id; const p = (a._providers || []).find((x) => x.id === id); a._mode = p?.defaultMode || ''; },
      get options() { return a._options().map((o) => ({ id: o.id, current: o.currentValue, values: (o.options || []).map((v) => v.value) })); },
      setOption(id, value) { a._setOption(id, value); },
      start() { return a._create(); },
      send(text) { a._draft = text; return a._submit(); },
      permit(pid, decision) { a._permit(pid, decision); },
      cancel() { a._cancel(); },
    };
  }
}

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
const count = (s) => (s ? String(s).split('\n').length - (String(s).endsWith('\n') ? 1 : 0) : 0);

// diffLines(old, new, path): a compact unified-ish view — every old line with
// a '-', every new line with a '+', the path as a header. Not a real diff
// (the ACP block already is the change); enough to read.
function diffLines(oldText, newText, path) {
  const out = [];
  if (path) out.push(html`<span class="del">--- ${esc(path)}</span>\n`);
  for (const l of String(oldText || '').split('\n')) if (l) out.push(html`<span class="del">- ${esc(l)}</span>\n`);
  for (const l of String(newText || '').split('\n')) if (l) out.push(html`<span class="add">+ ${esc(l)}</span>\n`);
  return out;
}

customElements.define('bx-agent', BxAgent);
