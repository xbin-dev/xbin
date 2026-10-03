/**
 * <bx-apidocs> — renders xbind's built-in API from its OpenAPI 3.1 spec
 * (GET /api/xbin/openapi.json), grouped by tag, with the RBAC capability each
 * endpoint requires (the x-xbin-capability extension) shown as a badge. The
 * spec is standard OpenAPI, so the "spec" link also feeds Swagger UI / Postman.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { unsafeHTML } from 'lit';
import { marked } from '/vendor/marked.esm.js';
import '/vendor/bx-icons.js'; // <bx-icon name>: drawn glyphs (D184)

const md = (s) => unsafeHTML(marked.parse(s || '', { async: false }));

// Capability → its weight, as a badge class (a status colour beside the
// capability's own words; D184: colour is never the only cue).
const CAP_CLASS = (c) => {
  if (!c) return '';
  if (c.includes('admin') || c === 'owner') return 'danger';
  if (c.includes('writer') || c.includes('users')) return 'warn';
  if (c.includes('reader')) return 'ok';
  return '';
};

export class BxApiDocs extends LitElement {
  static properties = { _spec: { state: true }, _q: { state: true }, _open: { state: true }, _err: { state: true } };

  // Base Two (D184): theme.css's tokens only (the page links it and opts in),
  // so the tile is right in light and dark; methods and capabilities are
  // square badges in words (a capability's weight in a status colour), paths
  // in ink and the code font, links in prose in the link colour.
  static styles = [scrollCss, css`
    :host { display: block; font: var(--bx-font); color: var(--bx-text); background: var(--bx-panel); }
    input { font: inherit; }
    ::placeholder { color: var(--bx-subtle); opacity: 1; }
    :focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
    .top { position: sticky; top: 0; z-index: 1; display: flex; gap: 12px; align-items: center; flex-wrap: wrap;
           padding: 8px var(--bx-pad); border-bottom: 1px solid var(--bx-border); background: var(--bx-panel); }
    .top h2 { margin: 0; font: var(--bx-font-title); }
    .top .spacer { flex: 1; }
    .top input { flex: 0 1 240px; box-sizing: border-box; min-height: var(--bx-control-h); background: var(--bx-panel);
      border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); padding: 4px 8px; color: var(--bx-text); }
    a { color: var(--bx-link); text-decoration: none; }
    a:hover { text-decoration: underline; }
    .body { padding: 8px var(--bx-pad) 24px; }
    .intro { font: var(--bx-font-body); color: var(--bx-text); border: 1px solid var(--bx-border);
      border-radius: var(--bx-radius); padding: 4px var(--bx-pad); margin: 12px 0 16px; background: var(--bx-panel-2); }
    .intro h2 { font: var(--bx-font-title); margin: 12px 0 4px; }
    .intro code { font: var(--bx-font-code); background: var(--bx-code-bg);
      border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 0 4px; }
    .intro ul { margin: 4px 0; padding-left: 18px; }
    h3.tag { font: var(--bx-font-micro); text-transform: uppercase; letter-spacing: var(--bx-tracking-micro); color: var(--bx-muted);
      margin: 16px 0 8px; border-bottom: 2px solid var(--bx-text); padding-bottom: 4px; }
    .op { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); margin-bottom: 4px; overflow: hidden; }
    .op .row { display: flex; align-items: center; gap: 12px; min-height: 32px; box-sizing: border-box; padding: 4px 12px; cursor: pointer; }
    .op .row:hover { background: var(--bx-hover); }
    /* the method: a square badge, mono, in ink (DELETE in the danger colour) */
    .m { box-sizing: border-box; display: inline-flex; align-items: center; justify-content: center; height: 20px; min-width: 56px;
      padding: 0 6px; font: var(--bx-font-code); font-weight: 700; text-transform: uppercase;
      border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); color: var(--bx-text); }
    .m.delete { color: var(--bx-danger); border-color: var(--bx-danger); }
    .path { font: var(--bx-font-code); }
    .op .sum { color: var(--bx-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .op .spacer { flex: 1; }
    .cap { box-sizing: border-box; display: inline-flex; align-items: center; height: 20px; padding: 0 6px;
      font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; white-space: nowrap;
      border-radius: var(--bx-radius); border: 1px solid var(--bx-border-strong); color: var(--bx-muted); }
    .cap.ok { color: var(--bx-ok); border-color: var(--bx-ok); }
    .cap.warn { color: var(--bx-warn); border-color: var(--bx-warn); }
    .cap.danger { color: var(--bx-danger); border-color: var(--bx-danger); }
    .detail { border-top: 1px solid var(--bx-border); padding: 8px 12px; background: var(--bx-panel-2); }
    .detail .desc :first-child { margin-top: 0; }
    .detail code { font: var(--bx-font-code); }
    .detail h5 { margin: 12px 0 4px; font: var(--bx-font-micro); text-transform: uppercase; letter-spacing: var(--bx-tracking-micro); color: var(--bx-muted); }
    table { border-collapse: collapse; width: 100%; font-variant-numeric: tabular-nums; }
    th { text-align: left; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
      color: var(--bx-muted); padding: 4px 8px 4px 0; border-bottom: 2px solid var(--bx-text); }
    td { padding: 4px 8px 4px 0; border-top: 1px solid var(--bx-border); vertical-align: top; }
    .mono { font: var(--bx-font-code); }
    .muted { color: var(--bx-muted); }
    .hint { font: var(--bx-font-meta); }
    .req { color: var(--bx-text); font-weight: 600; } /* required is a fact, not a danger */
    .err { display: flex; gap: 6px; align-items: baseline; color: var(--bx-danger); padding: 20px var(--bx-pad); }
  `];

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
    if (this._err) return html`<div class="err"><bx-icon name="error"></bx-icon><span><b>Couldn't load the API spec.</b> ${this._err}</span></div>`;
    const spec = this._spec;
    if (!spec) return html`<div class="body muted">loading…</div>`;
    const base = (spec.servers && spec.servers[0] && spec.servers[0].url) || '';
    return html`
      <div class="top">
        <h2>${spec.info?.title || 'API'}</h2>
        <span class="muted hint">v${spec.info?.version} · base <span class="mono">${base}</span></span>
        <span class="spacer"></span>
        <input placeholder="filter…" .value=${this._q} @input=${(e) => { this._q = e.target.value; }}>
        <a href="/api/xbin/openapi.json" target="_blank" title="raw OpenAPI 3.1 spec — import into Swagger UI / Postman">spec <bx-icon name="popout"></bx-icon></a>
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
          <span class="m ${method}">${method}</span>
          <span class="path">${base}${path}</span>
          <span class="spacer"></span>
          <span class="sum">${op.summary || ''}</span>
          ${cap ? html`<span class="cap ${CAP_CLASS(cap)}">${cap}</span>` : nothing}
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
