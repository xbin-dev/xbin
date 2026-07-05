/**
 * <bx-apidocs> — renders xbind's built-in API from its OpenAPI 3.1 spec
 * (GET /api/xbin/openapi.json), grouped by tag, with the RBAC capability each
 * endpoint requires (the x-xbin-capability extension) shown as a badge. The
 * spec is standard OpenAPI, so the "spec" link also feeds Swagger UI / Postman.
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import { marked } from '/vendor/marked.esm.js';

const md = (s) => unsafeHTML(marked.parse(s || '', { async: false }));

// Capability → colour hint.
const CAP_COLOR = (c) => {
  if (!c) return 'var(--bx-muted)';
  if (c.includes('admin') || c === 'owner') return 'var(--bx-red, #e5484d)';
  if (c.includes('writer') || c.includes('users')) return 'var(--bx-amber, #f2a71b)';
  if (c.includes('reader')) return 'var(--bx-green, #43a047)';
  return 'var(--bx-muted, #8794a1)';
};
const METHOD_COLOR = {
  get: 'var(--bx-green, #43a047)', post: 'var(--bx-amber, #f2a71b)',
  put: 'var(--bx-accent, #f5a623)', patch: '#8957e5', delete: 'var(--bx-red, #e5484d)',
};

export class BxApiDocs extends LitElement {
  static properties = { _spec: { state: true }, _q: { state: true }, _open: { state: true }, _err: { state: true } };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.5 system-ui, sans-serif); color: var(--bx-text, #33414e);
            background: var(--bx-panel, #fff); }
    .top { position: sticky; top: 0; z-index: 1; display: flex; gap: 10px; align-items: center; flex-wrap: wrap;
           padding: 10px 14px; border-bottom: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel-2, #f7f8fa); }
    .top h2 { margin: 0; font-size: 15px; }
    .top .spacer { flex: 1; }
    .top input { flex: 0 1 240px; background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 6px; padding: 4px 9px; font: inherit; font-size: 12px; color: var(--bx-text); }
    .top a { font-size: 12px; color: var(--bx-accent, #f5a623); text-decoration: none; }
    .top a:hover { text-decoration: underline; }
    .body { padding: 8px 14px 24px; }
    .intro { font-size: 13px; color: var(--bx-text); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 8px; padding: 4px 14px; margin: 10px 0 14px; background: var(--bx-panel-2, #f7f8fa); }
    .intro h2 { font-size: 13px; margin: 12px 0 4px; }
    .intro code { font: 11.5px var(--bx-mono, monospace); background: var(--bx-panel, #fff);
      border: 1px solid var(--bx-border); border-radius: 4px; padding: 0 4px; }
    .intro ul { margin: 4px 0; padding-left: 18px; }
    h3.tag { font-size: 11px; text-transform: uppercase; letter-spacing: .08em; color: var(--bx-muted, #8794a1);
      margin: 18px 0 6px; border-bottom: 1px solid var(--bx-border, #e4e8ed); padding-bottom: 3px; }
    .op { border: 1px solid var(--bx-border, #e4e8ed); border-radius: 7px; margin-bottom: 6px; overflow: hidden; }
    .op .row { display: flex; align-items: center; gap: 10px; padding: 6px 10px; cursor: pointer; }
    .op .row:hover { background: var(--bx-panel-2, #f7f8fa); }
    .m { font: 700 10.5px var(--bx-mono, monospace); text-transform: uppercase; color: #fff; padding: 1px 7px;
      border-radius: 4px; min-width: 46px; text-align: center; }
    .path { font: 12.5px var(--bx-mono, monospace); }
    .op .sum { color: var(--bx-muted, #8794a1); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .op .spacer { flex: 1; }
    .cap { font-size: 10px; padding: 1px 8px; border-radius: 999px; border: 1px solid currentColor; white-space: nowrap; }
    .detail { border-top: 1px solid var(--bx-border, #e4e8ed); padding: 8px 12px; background: var(--bx-panel-2, #f7f8fa); }
    .detail .desc :first-child { margin-top: 0; }
    .detail code { font: 11.5px var(--bx-mono, monospace); }
    .detail h5 { margin: 12px 0 4px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--bx-muted); }
    table { border-collapse: collapse; width: 100%; font-size: 12px; }
    th { text-align: left; font-size: 10px; text-transform: uppercase; color: var(--bx-muted); padding: 2px 8px 2px 0; }
    td { padding: 2px 8px 2px 0; border-top: 1px solid var(--bx-border, #e4e8ed); vertical-align: top; }
    .mono { font-family: var(--bx-mono, monospace); }
    .muted { color: var(--bx-muted, #8794a1); }
    .req { color: var(--bx-red, #e5484d); font-size: 10px; }
    .err { color: var(--bx-red, #e5484d); padding: 20px 14px; }
  `;

  constructor() { super(); this._q = ''; this._open = new Set(); }

  connectedCallback() {
    super.connectedCallback();
    (async () => {
      try {
        const r = await (window.xbin?.fetch ?? fetch)('/api/xbin/openapi.json');
        if (!r.ok) throw new Error('HTTP ' + r.status);
        this._spec = await r.json();
      } catch (e) { this._err = String(e.message ?? e); }
    })();
  }

  _toggle(id) { const s = new Set(this._open); s.has(id) ? s.delete(id) : s.add(id); this._open = s; }

  // [{tag, ops:[{method, path, op}]}] filtered by the search box.
  get _byTag() {
    const spec = this._spec; if (!spec) return [];
    const q = this._q.trim().toLowerCase();
    const groups = new Map();
    for (const [path, item] of Object.entries(spec.paths || {})) {
      for (const [method, op] of Object.entries(item)) {
        const hay = (method + ' ' + path + ' ' + (op.summary || '') + ' ' + (op['x-xbin-capability'] || '')).toLowerCase();
        if (q && !hay.includes(q)) continue;
        const tag = (op.tags && op.tags[0]) || 'Other';
        if (!groups.has(tag)) groups.set(tag, []);
        groups.get(tag).push({ method, path, op });
      }
    }
    const order = (spec.tags || []).map((t) => t.name);
    return [...groups.entries()]
      .sort((a, b) => (order.indexOf(a[0]) + 1 || 99) - (order.indexOf(b[0]) + 1 || 99))
      .map(([tag, ops]) => ({ tag, ops: ops.sort((a, b) => a.path.localeCompare(b.path)) }));
  }

  render() {
    if (this._err) return html`<div class="err"><b>Couldn't load the API spec.</b> ${this._err}</div>`;
    const spec = this._spec;
    if (!spec) return html`<div class="body muted">loading…</div>`;
    const base = (spec.servers && spec.servers[0] && spec.servers[0].url) || '';
    return html`
      <div class="top">
        <h2>${spec.info?.title || 'API'}</h2>
        <span class="muted" style="font-size:11px">v${spec.info?.version} · base <span class="mono">${base}</span></span>
        <span class="spacer"></span>
        <input placeholder="filter…" .value=${this._q} @input=${(e) => { this._q = e.target.value; }}>
        <a href="/api/xbin/openapi.json" target="_blank" title="raw OpenAPI 3.1 spec — import into Swagger UI / Postman">spec ↗</a>
      </div>
      <div class="body">
        <div class="intro desc">${md(spec.info?.description)}</div>
        ${this._byTag.map(({ tag, ops }) => html`
          <h3 class="tag">${tag}</h3>
          ${ops.map(({ method, path, op }) => this._op(method, path, op, base))}
        `)}
      </div>`;
  }

  _op(method, path, op, base) {
    const id = method + ' ' + path;
    const open = this._open.has(id);
    const cap = op['x-xbin-capability'];
    return html`
      <div class="op">
        <div class="row" @click=${() => this._toggle(id)}>
          <span class="m" style="background:${METHOD_COLOR[method] || 'var(--bx-muted)'}">${method}</span>
          <span class="path">${base}${path}</span>
          <span class="spacer"></span>
          <span class="sum">${op.summary || ''}</span>
          ${cap ? html`<span class="cap" style="color:${CAP_COLOR(cap)}">${cap}</span>` : nothing}
        </div>
        ${open ? this._detail(op) : nothing}
      </div>`;
  }

  _detail(op) {
    const params = op.parameters || [];
    const bodyProps = op.requestBody?.content?.['application/json']?.schema?.properties;
    const bodyReq = op.requestBody?.content?.['application/json']?.schema?.required || [];
    const responses = op.responses || {};
    return html`<div class="detail">
      <div class="desc">${md(op.description)}</div>
      ${params.length ? html`
        <h5>parameters</h5>
        <table><tr><th>name</th><th>in</th><th>req</th><th>description</th></tr>
        ${params.map((p) => html`<tr>
          <td class="mono">${p.name}</td><td class="muted">${p.in}</td>
          <td>${p.required ? html`<span class="req">yes</span>` : html`<span class="muted">—</span>`}</td>
          <td>${p.description || ''}</td></tr>`)}
        </table>` : nothing}
      ${op.requestBody ? html`
        <h5>request body${op.requestBody.required ? html` <span class="req">(required)</span>` : nothing}</h5>
        ${bodyProps ? html`<table><tr><th>field</th><th>type</th><th>description</th></tr>
          ${Object.entries(bodyProps).map(([k, v]) => html`<tr>
            <td class="mono">${k}${bodyReq.includes(k) ? html` <span class="req">*</span>` : nothing}</td>
            <td class="muted">${v.type || 'any'}</td><td>${v.description || ''}</td></tr>`)}
        </table>` : html`<div class="muted">${op.requestBody.description || 'raw body'}</div>`}` : nothing}
      <h5>responses</h5>
      <table>${Object.entries(responses).map(([code, r]) => html`<tr>
        <td class="mono">${code}</td><td>${r.description || ''}</td></tr>`)}
      </table>
    </div>`;
  }
}
customElements.define('bx-apidocs', BxApiDocs);
