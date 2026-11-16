/**
 * <bx-wsagent-policy> — what people's workspace agents may reach of the web
 * and of MCP servers, as a workspace admin sets it (D236;
 * /docs/protocol/delegation.md §Web and MCP): the workspace agent's card
 * on /xbin/system shows it. Read with GET /api/xbin/delegate/workspace and
 * written with PUT (`web: {mode, allow, deny}`, `mcp: {mode}`), with the
 * admin's own session. What the host's file (XBIN_WSAGENT_CONFIG) holds is
 * shown as "Set by your host": a mode it caps can be narrowed and never
 * widened, its lists always hold, and under its lockAll nothing here can be
 * changed. Each person still turns each one on for their own agent, in
 * their agent's Preferences.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';

const req = async (path, init = {}) => {
  const r = await fetch(`/api/xbin${path}`, { ...init, headers: { Accept: 'application/json', ...(init.body ? { 'Content-Type': 'application/json' } : {}) } });
  return { status: r.status, ok: r.ok, body: (await r.json().catch(() => ({}))) ?? {} };
};

const RANK = { off: 0, read: 1, full: 2, tiles: 1, listed: 2 };
const lines = (s) => [...new Set(String(s || '').split(/[\s,]+/).map((x) => x.trim()).filter(Boolean))];

/** policyWords(view): what the card says of the policy in force — pure, for the harness and the node test. */
export function policyWords(v) {
  const pol = (v && v.policy) || {}, locks = (v && v.locks) || {}, host = (v && v.host) || {};
  const web = pol.web || {}, mcp = pol.mcp || {};
  const all = !!locks['*'];
  const ceiling = (k) => (locks[k] && locks[k].ceiling != null ? String(locks[k].ceiling) : '');
  return {
    web: web.mode === 'read' ? 'People’s agents may read web pages (each person turns it on for their own).' : 'People’s agents can’t read the web.',
    mcp: mcp.mode === 'tiles' ? 'People’s agents may use the MCP tiles of this workspace they can reach (each person turns it on).' : 'People’s agents can’t use MCP tools.',
    lockAll: all, hostError: host.error || '',
    webCeiling: ceiling('web.mode'), mcpCeiling: ceiling('mcp.mode'),
    hostAllow: web.hostAllow || [], hostDeny: web.hostDeny || [],
    byHost: { web: !!web.byHost, mcp: !!mcp.byHost },
  };
}

export class BxWsagentPolicy extends LitElement {
  static properties = { _v: { state: true }, _draft: { state: true }, _busy: { state: true }, _err: { state: true }, _saved: { state: true } };
  static styles = css`
    :host { display: block; }
    h3 { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6);
      text-transform: uppercase; letter-spacing: 0.04em; margin: 16px 0 4px; }
    .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .muted { color: var(--bx-muted, #A3A6B6); }
    .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    fieldset { border: 0; margin: 0 0 8px; padding: 0; display: flex; flex-direction: column; gap: 4px; min-width: 0; }
    legend { padding: 0; margin-bottom: 4px; font-weight: 600; }
    label { display: flex; gap: 8px; align-items: flex-start; }
    label input { margin-top: 3px; }
    label.off { opacity: .55; }
    textarea { box-sizing: border-box; width: 100%; min-height: 56px; font: inherit; font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); padding: 4px 8px; }
    .tag { display: inline-block; padding: 0 6px; margin-left: 6px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
    button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); font: inherit; padding: 0 12px; font-weight: 600; cursor: pointer;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
    button:hover { background: var(--bx-hover, #2A2B34); }
    .banner { border: 1px solid var(--bx-danger, #FF7A7A); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 8px 0; background: var(--bx-danger-bg, #3A2B32); overflow-wrap: anywhere; }
    .ok { color: var(--bx-ok, #A3CF5E); }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); }
  `;

  constructor() { super(); this._v = null; this._draft = null; this._busy = false; this._err = ''; this._saved = false; }
  connectedCallback() { super.connectedCallback(); this._load(); }

  /** The test surface (hack/ui-harness): what the card shows and sets. */
  testApi() {
    return { view: () => this._v, draft: () => this._draft, set: (d) => { this._draft = { ...this._draft, ...d }; }, save: () => this._save(), reload: () => this._load() };
  }

  async _load() {
    const r = await req('/delegate/workspace');
    if (!r.ok) { this._err = r.body.error || `error ${r.status}`; return; }
    this._v = r.body;
    const w = r.body.web || {}, m = r.body.mcp || {};
    this._draft = { web: w.mode || 'off', allow: (w.allow || []).join('\n'), deny: (w.deny || []).join('\n'), mcp: m.mode || 'off' };
  }

  async _save() {
    if (this._busy || !this._draft) return;
    this._busy = true; this._err = ''; this._saved = false;
    const d = this._draft;
    const r = await req('/delegate/workspace', { method: 'PUT', body: JSON.stringify({ web: { mode: d.web, allow: lines(d.allow), deny: lines(d.deny) }, mcp: { mode: d.mcp } }) });
    this._busy = false;
    if (!r.ok) { this._err = r.body.error || `error ${r.status}`; return; }
    this._saved = true;
    await this._load();
  }

  _mode(name, value, label, note, { ceiling = '', notYet = false, locked = false } = {}) {
    const past = ceiling && RANK[value] > RANK[ceiling];
    const off = notYet || locked || past;
    return html`<label class=${off ? 'off' : ''}><input type="radio" name=${name} .value=${value} .checked=${this._draft[name] === value} ?disabled=${off}
        @change=${() => { this._draft = { ...this._draft, [name]: value }; }}>
      <span>${label}${notYet ? html`<span class="tag">not available yet</span>` : past ? html`<span class="tag">past what your host allows</span>` : nothing}
        ${note ? html`<div class="small muted">${note}</div>` : nothing}</span></label>`;
  }

  render() {
    if (!this._v || !this._draft) return this._err ? html`<div class="banner" role="alert">${this._err}</div>` : html`<div class="small muted">Reading…</div>`;
    const w = policyWords(this._v);
    const locked = w.lockAll;
    const hostMark = (k) => (this._v.locks && this._v.locks[k] ? html`<span class="tag" data-host=${k}>Set by your host</span>` : nothing);
    return html`
      <h3>The web and MCP tools</h3>
      <div class="small muted">Off until you turn them on here, and then each person turns them on for their own agent in its Preferences. An organisation can keep its data off the web, or from MCP servers outside it, in its agent rules.</div>
      ${w.hostError ? html`<div class="banner" role="alert">${w.hostError}</div>` : nothing}
      ${locked ? html`<div class="small" data-locked>Set by your host: whoever runs this xbin decides these settings (XBIN_WSAGENT_CONFIG).</div>` : nothing}
      <fieldset role="radiogroup" data-field="web"><legend>Reading the web ${hostMark('web.mode')}</legend>
        ${this._mode('web', 'off', 'Off', '', { locked })}
        ${this._mode('web', 'read', 'Read web pages', 'A GET of an address, through xbin: nothing sent but the address, no sign-in, public addresses only. A conversation that holds an organisation’s data asks its person for each page.', { ceiling: w.webCeiling, locked })}
        ${this._mode('web', 'full', 'Send to the web', 'Forms, sign-ins and requests with a body.', { notYet: true })}
      </fieldset>
      <label class="small" style="flex-direction:column;gap:2px">Only these sites <span class="muted">(one per line — example.com is it and the names under it; empty: any public site)</span> ${hostMark('web.allow')}
        <textarea data-field="allow" ?disabled=${locked} .value=${this._draft.allow} @input=${(e) => { this._draft = { ...this._draft, allow: e.target.value }; }}></textarea></label>
      ${w.hostAllow.length ? html`<div class="small muted">Your host allows only: <span class="mono">${w.hostAllow.join(', ')}</span></div>` : nothing}
      <label class="small" style="flex-direction:column;gap:2px;margin-top:6px">Never these sites ${hostMark('web.deny')}
        <textarea data-field="deny" ?disabled=${locked} .value=${this._draft.deny} @input=${(e) => { this._draft = { ...this._draft, deny: e.target.value }; }}></textarea></label>
      ${w.hostDeny.length ? html`<div class="small muted">Your host never allows: <span class="mono">${w.hostDeny.join(', ')}</span></div>` : nothing}
      <fieldset role="radiogroup" data-field="mcp" style="margin-top:10px"><legend>MCP tools ${hostMark('mcp.mode')}</legend>
        ${this._mode('mcp', 'off', 'Off', '', { locked })}
        ${this._mode('mcp', 'tiles', 'The MCP tiles of this workspace', 'Tiles that serve MCP, as each person, on the tiles they can reach. Each tool asks its person the first time.', { ceiling: w.mcpCeiling, locked })}
        ${this._mode('mcp', 'listed', 'MCP servers listed by URL', 'Outside the workspace, with credentials xbin keeps.', { notYet: true })}
      </fieldset>
      <div class="small muted" data-in-force>${w.web} ${w.mcp}</div>
      ${this._err ? html`<div class="banner" role="alert">${this._err}</div>` : nothing}
      <div class="row"><button data-save ?disabled=${locked || this._busy} @click=${() => this._save()}>Save</button>${this._saved ? html`<span class="small ok" role="status">Saved</span>` : nothing}</div>`;
  }
}
if (!customElements.get('bx-wsagent-policy')) customElements.define('bx-wsagent-policy', BxWsagentPolicy);
