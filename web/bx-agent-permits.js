/**
 * bx-agent-permits.js — <bx-agent-permits>: a tile agent's permission
 * requests, answered where the person already is (D243). A session the
 * person's workspace agent started asks before it edits or runs (its mode
 * asks); the dock's Needs you draws each request here — xbin's own chrome,
 * with the chat kit's permission card (/vendor/chat/asks.js: what the tile
 * window's Agent tab draws): the call, what exactly would run, and the
 * coding agent's own options ("Allow once", "Allow always" — offered
 * only where xbind can scope it to a session rule —, "Reject").
 *
 * The answer is the person's own: POST
 * /api/xbin/term/sessions/<id>/permissions/<pid> with this document's
 * session — the route the Agent tab uses. The workspace agent can't answer
 * (xbind refuses its credential and its page), and nothing here comes from
 * the agent's page: the element reads the session from xbind itself.
 *
 *   properties  session (xbind's session id), agentName
 *   events      bx-agent-permitted {session, pid}   (an answer went)
 */
import { LitElement, html, css, nothing, unsafeCSS } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { permView } from '/vendor/chat/asks.js';
import { chatCssText } from '/vendor/chat/css.js';

const POLL_MS = 1500; // the next request of the same turn follows an answer; nothing pushes it here

export class BxAgentPermits extends LitElement {
  static properties = { session: { type: String }, agentName: { type: String, attribute: 'agent-name' }, _perms: { state: true }, _err: { state: true } };
  static styles = [unsafeCSS(chatCssText), css`:host { display: block; } .none { font: var(--bx-font-meta); color: var(--bx-muted, #A3A6B6); padding: 4px 0; }
    .err { font: var(--bx-font-meta); color: var(--bx-danger, #FF7A7A); } .bxc-perm { margin-top: 6px; }`];

  constructor() {
    super();
    this.session = ''; this.agentName = ''; this._perms = null; this._err = ''; this._timer = 0; this._bags = new Map(); this._open = new Map();
  }

  connectedCallback() { super.connectedCallback(); this._read(); this._timer = setInterval(() => this._read(), POLL_MS); }
  disconnectedCallback() { super.disconnectedCallback(); clearInterval(this._timer); }
  updated(ch) { if (ch.has('session')) this._read(); }

  async _read() {
    if (!this.session) return;
    try {
      const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}`, { headers: { accept: 'application/json' } });
      if (!r.ok) { if (r.status === 404) this._perms = []; return; }
      const j = await r.json();
      const next = Array.isArray(j.permissions) ? j.permissions : [];
      if (JSON.stringify(next.map((p) => p.pid)) !== JSON.stringify((this._perms || []).map((p) => p.pid))) this._perms = next;
      else if (this._perms === null) this._perms = next;
    } catch { /* offline: the next poll */ }
  }

  // the kit's ChatCtx: what its permission card asks of a host
  get _ctx() {
    const self = this;
    return {
      agentName: this.agentName || 'the tile agent', role: 'owner', sessionRules: true,
      repaint: () => self.requestUpdate(),
      // the card's folds ("show all N lines"), kept here
      isOpen: (b, slot, dflt) => (self._open.has(`${b.k}:${slot}`) ? self._open.get(`${b.k}:${slot}`) : !!dflt),
      setOpen: (b, slot, open) => { self._open.set(`${b.k}:${slot}`, !!open); self.requestUpdate(); },
      state: (b) => { let bag = self._bags.get(b.k); if (!bag) self._bags.set(b.k, (bag = {})); return bag; },
      note: () => self._err,
      async permit(b, answer) {
        self._err = '';
        const body = answer.optionId ? { optionId: answer.optionId } : { decision: answer.decision };
        const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(self.session)}/permissions/${encodeURIComponent(b.pid)}`,
          { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null);
        if (!r || !r.ok) { self._err = (r && (await r.json().catch(() => ({}))).error) || 'the answer could not be sent'; return false; }
        self.dispatchEvent(new CustomEvent('bx-agent-permitted', { detail: { session: self.session, pid: b.pid }, bubbles: true, composed: true }));
        await self._read();
        return true;
      },
    };
  }

  render() {
    const perms = this._perms;
    if (perms === null) return nothing;
    if (!perms.length) return html`<div class="none" data-none>Nothing waits for you in this session right now.</div>`;
    const ctx = this._ctx;
    return html`${perms.map((p) => {
      const tool = p.toolCall || {};
      const b = { k: `perm:${p.pid}`, kind: 'perm', pid: p.pid, callId: tool.id || '', title: tool.title || '', tool: { kind: tool.kind, title: tool.title, name: tool.name, rawInput: tool.rawInput, content: tool.content }, options: p.options || [],
        // xbind's "always" is its own session rule — later calls of this kind with this title —, offered only when the request can be scoped to one (sdk/acp ToolCallRef.Rule)
        rule: tool.kind !== 'switch_mode' && (tool.kind || tool.title) ? { kind: tool.kind || '', title: tool.title || '' } : null };
      try { return permView(b, ctx); } catch (e) { // a card the kit can't draw is still a request: its title, and the way to the tile's window
        return html`<div class="err" data-undrawn>${tool.title || 'A permission request'} — open the tile's window to answer it.</div>`;
      }
    })}${this._err ? html`<div class="err" role="alert">${this._err}</div>` : nothing}`;
  }
}
if (!customElements.get('bx-agent-permits')) customElements.define('bx-agent-permits', BxAgentPermits);
