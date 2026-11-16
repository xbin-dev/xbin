/**
 * <bx-wsagent-mcp-servers> — the MCP servers outside the workspace that
 * people's workspace agents may use, as a workspace admin lists them (D240;
 * /docs/protocol/delegation.md §Outside MCP servers): inside the workspace
 * agent's card on /xbin/system (<bx-wsagent-policy>), whose Save sends the
 * whole list (PUT /api/xbin/delegate/workspace {mcp: {servers}}).
 *
 * A secret is write-only: the field shows that one is set, never what it
 * is, and only a value typed here is sent ("" when the admin removes it).
 * The three flags that widen what xbin does for a server — on a private
 * network, trust its read-only marks, plain http — are armed like Allow
 * (WidenGuard: a deliberate click, a moment after they show) and each says
 * what it means beside it. Test runs initialize and tools/list on a saved
 * server (POST /api/xbin/delegate/mcp/test) and shows its tools or the exact
 * error. A list the host's file names is shown, and locked.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import { WidenGuard } from '/vendor/bx-agent-approve.js';
import { FLAGS, AUTHS, slug, draftOf, serverBody, serverWords, testWords } from './wsagent-mcp-words.js';

export { FLAGS, AUTHS, draftOf, serverBody, serverWords, testWords };


export class BxWsagentMcpServers extends LitElement {
  static properties = { servers: { attribute: false }, locked: { type: Boolean }, _rows: { state: true }, _edit: { state: true }, _tests: { state: true }, _dirty: { state: true } };
  static styles = css`
    :host { display: block; margin: 6px 0 0 24px; }
    .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .muted { color: var(--bx-muted, #A3A6B6); }
    .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
    li.srv { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 6px 8px; background: var(--bx-panel, #1F2028); min-width: 0; }
    .head { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    .head b { overflow-wrap: anywhere; }
    .tag { display: inline-block; padding: 0 6px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6); }
    .tag.warn { border-color: var(--bx-warn, #F2994A); color: var(--bx-warn, #F2994A); }
    .btns { display: flex; gap: 6px; margin-left: auto; }
    button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); font: inherit; padding: 0 10px; cursor: pointer;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
    button:hover { background: var(--bx-hover, #2A2B34); }
    button[disabled] { opacity: .5; cursor: default; }
    form { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; margin-top: 6px; }
    label.f { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
    input[type=text], input[type=url], input[type=password], select, textarea { box-sizing: border-box; width: 100%; min-width: 0; font: inherit; min-height: var(--bx-control-h, 28px);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); background: var(--bx-bg, #0B0C12); color: var(--bx-text, #E9EAF0); padding: 2px 8px; }
    textarea { min-height: 44px; }
    label.chk { display: flex; gap: 8px; align-items: flex-start; }
    label.chk input { margin-top: 3px; }
    .warnbox { border-left: 3px solid var(--bx-warn, #F2994A); padding: 2px 8px; background: var(--bx-warn-bg, #382F2C); }
    .test { margin-top: 4px; overflow-wrap: anywhere; }
    .test.bad { color: var(--bx-danger, #FF7A7A); }
    .test.good { color: var(--bx-ok, #A3CF5E); }
    .test ul { margin-top: 2px; gap: 0; }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); }
  `;

  constructor() { super(); this.servers = []; this.locked = false; this._rows = null; this._edit = -1; this._tests = {}; this._dirty = false; this._widen = new WidenGuard(this); }

  willUpdate(ch) {
    if (ch.has('servers') && !this._dirty) this._rows = (this.servers || []).map(draftOf);
  }

  /** The test surface (hack/ui-harness): the drafts, an edit, the body Save would send. */
  testApi() {
    return { rows: () => this._rows, set: (i, d) => this._set(i, d), add: () => this._add(), remove: (i) => this._remove(i), changes: () => this.changes(), test: (i) => this._test(i), edit: (i) => { this._edit = i; } };
  }

  /** changes(): the list for PUT …/workspace {mcp: {servers}}, or null when nothing changed. */
  changes() { return this._dirty && this._rows ? this._rows.map(serverBody) : null; }

  /** saved(): the list as it now reads (after a Save). */
  saved() { this._dirty = false; this._edit = -1; this._rows = (this.servers || []).map(draftOf); }

  _set(i, patch) {
    const rows = [...this._rows];
    rows[i] = { ...rows[i], ...patch };
    if (patch.name != null && !rows[i].saved && !rows[i].idTouched) rows[i].id = slug(patch.name);
    this._rows = rows;
    this._dirty = true;
    this.dispatchEvent(new CustomEvent('change', { bubbles: true, composed: true }));
  }

  _add() {
    this._rows = [...this._rows, draftOf({ auth: 'bearer' })];
    this._edit = this._rows.length - 1;
    this._dirty = true;
    this.dispatchEvent(new CustomEvent('change', { bubbles: true, composed: true }));
  }

  _remove(i) {
    this._rows = this._rows.filter((_, j) => j !== i);
    this._edit = -1;
    this._dirty = true;
    this.dispatchEvent(new CustomEvent('change', { bubbles: true, composed: true }));
  }

  async _test(i) {
    const d = this._rows[i];
    this._tests = { ...this._tests, [d.id]: { busy: true } };
    const r = await fetch('/api/xbin/delegate/mcp/test', { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' }, body: JSON.stringify({ id: d.id }) });
    const b = (await r.json().catch(() => ({}))) || {};
    this._tests = { ...this._tests, [d.id]: r.ok ? b : { ok: false, error: b.error || `error ${r.status}` } };
  }

  _flag(i, d, f) {
    return html`<label class="chk"><input type="checkbox" data-flag=${f.id} data-widen="on" .checked=${d[f.id]} ?disabled=${this.locked}
        @click=${this._widen.click} @keydown=${this._widen.key} @change=${(e) => this._set(i, { [f.id]: e.target.checked })}>
      <span>${f.label}${d[f.id] ? html`<div class="small warnbox" role="note">${f.warn}</div>` : html`<div class="small muted">${f.warn}</div>`}</span></label>`;
  }

  _form(i, d) {
    const ro = this.locked;
    return html`<form @submit=${(e) => e.preventDefault()} data-edit=${i}>
      <label class="f small">Name <span class="muted">— what people and their agents read</span>
        <input type="text" data-field="name" maxlength="80" .value=${d.name} ?disabled=${ro} @input=${(e) => this._set(i, { name: e.target.value })}></label>
      ${d.saved ? nothing : html`<label class="f small">Id <span class="muted">— the agent names it ext:<span class="mono">${d.id || '…'}</span>; it can’t change once saved</span>
        <input type="text" class="mono" data-field="id" maxlength="32" .value=${d.id} @input=${(e) => this._set(i, { id: slug(e.target.value), idTouched: true })}></label>`}
      <label class="f small">Address <span class="muted">— the server’s MCP endpoint (streamable HTTP), https</span>
        <input type="url" class="mono" data-field="url" placeholder="https://mcp.example.com/mcp" .value=${d.url} ?disabled=${ro} @input=${(e) => this._set(i, { url: e.target.value })}></label>
      <label class="f small">Sign-in
        <select data-field="auth" ?disabled=${ro} @change=${(e) => this._set(i, { auth: e.target.value })}>${AUTHS.map((a) => html`<option value=${a.id} ?selected=${d.auth === a.id}>${a.label}</option>`)}</select></label>
      ${d.auth === 'header' ? html`<label class="f small">Header name <input type="text" class="mono" data-field="header" placeholder="X-Api-Key" .value=${d.header} ?disabled=${ro}
        @input=${(e) => this._set(i, { header: e.target.value })}></label>` : nothing}
      ${d.auth !== 'none' && !d.byHost ? html`<label class="f small">Secret <span class="muted">— ${d.hasSecret && !d.dropSecret ? 'set; type here to replace it — it is never shown again' : 'kept sealed in the workspace vault, never shown again'}</span>
        <input type="password" class="mono" data-field="secret" autocomplete="new-password" placeholder=${d.hasSecret && !d.dropSecret ? '•••••••• (set)' : ''} .value=${d.secret} ?disabled=${ro}
          @input=${(e) => this._set(i, { secret: e.target.value, dropSecret: false })}></label>
        ${d.hasSecret ? html`<label class="chk small"><input type="checkbox" data-field="dropSecret" .checked=${d.dropSecret} @change=${(e) => this._set(i, { dropSecret: e.target.checked, secret: '' })}><span>Remove the secret</span></label>` : nothing}` : nothing}
      <label class="f small">Only for these organisations <span class="muted">(ids, comma-separated; empty: everyone given an agent — the others don’t see it at all)</span>
        <input type="text" class="mono" data-field="orgs" .value=${d.orgs} ?disabled=${ro} @input=${(e) => this._set(i, { orgs: e.target.value })}></label>
      <label class="f small">Note <span class="muted">— shown to people and their agents</span>
        <textarea data-field="note" maxlength="500" .value=${d.note} ?disabled=${ro} @input=${(e) => this._set(i, { note: e.target.value })}></textarea></label>
      ${FLAGS.map((f) => this._flag(i, d, f))}
    </form>`;
  }

  render() {
    if (!this._rows) return nothing;
    return html`<div class="small muted" data-outside-note>Outside the workspace: what an agent sends a server here leaves the workspace, so a conversation that holds an organisation’s data asks for each call. Each tool asks its person the first time.</div>
      ${this.locked ? html`<div class="small" data-host-list><span class="tag">Set by your host</span> Your host’s file names these servers: the list is the host’s.</div>` : nothing}
      <ul data-servers>${this._rows.map((d, i) => {
        const w = serverWords(d), t = testWords(this._tests[d.id]), busy = this._tests[d.id] && this._tests[d.id].busy;
        return html`<li class="srv" data-server=${d.id || `new-${i}`}>
          <div class="head"><b>${d.name || '(a new server)'}</b><span class="mono small muted">${w.host}</span>
            <span class="small muted">${w.sign}</span>${w.flags.map((f) => html`<span class="tag small ${FLAGS.some((x) => x.label === f) ? 'warn' : ''}">${f}</span>`)}
            <span class="btns">
              <button type="button" data-test ?disabled=${!d.saved || this._dirty || busy} title=${!d.saved || this._dirty ? 'Save first, then test' : 'Run initialize and tools/list now'} @click=${() => this._test(i)}>${busy ? 'Testing…' : 'Test'}</button>
              ${this.locked ? nothing : html`<button type="button" data-edit-btn @click=${() => { this._edit = this._edit === i ? -1 : i; }}>${this._edit === i ? 'Done' : 'Edit'}</button>
                <button type="button" data-remove @click=${() => this._remove(i)}>Remove</button>`}</span></div>
          ${t && !busy ? html`<div class="test small ${t.ok ? 'good' : 'bad'}" role="status" data-test-result>${t.line}
            ${t.tools.length ? html`<ul>${t.tools.map((x) => html`<li><span class="mono">${x.name}</span>${x.readOnly ? html` <span class="muted">(marked read-only)</span>` : nothing}${x.description ? html` — <span class="muted">${x.description.slice(0, 160)}</span>` : nothing}</li>`)}</ul>` : nothing}</div>` : nothing}
          ${this._edit === i ? this._form(i, d) : nothing}</li>`;
      })}</ul>
      ${this._rows.length ? nothing : html`<div class="small muted" data-empty>No outside server is listed yet.</div>`}
      ${this.locked ? nothing : html`<div style="margin-top:6px"><button type="button" data-add @click=${() => this._add()}>Add a server</button></div>`}`;
  }
}
if (!customElements.get('bx-wsagent-mcp-servers')) customElements.define('bx-wsagent-mcp-servers', BxWsagentMcpServers);
