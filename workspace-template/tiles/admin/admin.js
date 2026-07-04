/**
 * <bx-admin> — the workspace admin console (tiles/admin). Full owner-view
 * into the running system, powered by buxond's admin-capable endpoints via
 * the buxon:admin capability (see buxon.json / API.md).
 *
 * Tabs: overview · code & history · users · vault · roles & grants · cron.
 * All reads go through buxon.fetch (admin identity attributed by frame token)
 * and refresh on the grants/reload event stream. The code & history tab browses
 * a component's files and git log/diffs (syntax-highlighted via vendored
 * highlight.js), scoped to its path in the single workspace repo.
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import hljs from '/vendor/highlight.min.js';

const api = async (path, opts) => {
  const r = await buxon.fetch('/api/buxon' + path, opts);
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error(data?.error ?? r.status);
  return data;
};

// Map a file path to a highlight.js language (empty = let hljs auto-detect).
const LANG_BY_EXT = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript', ts: 'typescript',
  jsx: 'javascript', json: 'json', jsonc: 'json', go: 'go', mod: 'go',
  py: 'python', rb: 'ruby', rs: 'rust', sh: 'bash', bash: 'bash', zsh: 'bash',
  css: 'css', scss: 'scss', less: 'less', html: 'xml', xml: 'xml', svg: 'xml',
  md: 'markdown', markdown: 'markdown', yml: 'yaml', yaml: 'yaml',
  toml: 'ini', ini: 'ini', sql: 'sql', java: 'java', c: 'c', h: 'c',
  cpp: 'cpp', cs: 'csharp', php: 'php', lua: 'lua', swift: 'swift', kt: 'kotlin',
};
function langFor(path) {
  const base = path.split('/').pop();
  if (base === 'go.mod' || base === 'go.sum') return 'go';
  if (base === 'Makefile' || base === 'makefile') return 'makefile';
  if (base === 'Dockerfile') return 'dockerfile';
  const ext = base.includes('.') ? base.split('.').pop().toLowerCase() : '';
  return LANG_BY_EXT[ext] || '';
}
const escHTML = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
// Highlight one chunk of code to an HTML string (escaped + tokenized), falling
// back to plain escaped text for unknown languages.
function hl(code, lang) {
  try {
    if (lang && hljs.getLanguage(lang)) return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch { /* fall through */ }
  return escHTML(code);
}

export class BxAdmin extends LitElement {
  static properties = {
    _tab: { state: true },
    _ov: { state: true },       // auth-overview
    _vaults: { state: true },   // [{component, keys}]
    _reveal: { state: true },   // "comp\0key" -> value (revealed secrets)
    _cron: { state: true },
    _users: { state: true },
    _ifaces: { state: true },   // {bindings, components} — interface wiring
    _schedules: { state: true }, // [{component, schedule, retention}]
    _versions: { state: true },  // comp -> [{version,time,size}] (lazy)
    _verOpen: { state: true },   // set of comps whose version list is expanded
    _busy: { state: true },      // comp path mid heavy op (offload/restore/backup)
    _err: { state: true },
    _denied: { state: true },
    _codeComp: { state: true }, // component being browsed in the code tab
    _codeTree: { state: true }, // its files
    _codeFile: { state: true }, // {path, content|binary|truncated}
    _codeLog: { state: true },  // its commits
    _codeDiff: { state: true }, // {rev, diff}
    _codeMode: { state: true }, // 'file' | 'diff'
    _rt: { state: true },       // /runtime snapshot {host, backends}
    _rtOpen: { state: true },   // set of expanded backend paths
  };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
            color: var(--bx-text, #33414e); background: var(--bx-panel, #fff); }
    .tabs { display: flex; gap: 2px; padding: 6px 8px 0;
            border-bottom: 1px solid var(--bx-border, #e4e8ed);
            background: var(--bx-panel-2, #f7f8fa); position: sticky; top: 0; z-index: 1; }
    .tabs button { border: 1px solid transparent; border-bottom: none; background: none;
      font: inherit; font-size: 12px; padding: 4px 12px; cursor: pointer;
      color: var(--bx-muted, #8794a1); border-radius: 5px 5px 0 0; }
    .tabs button.on { background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      border-color: var(--bx-border, #e4e8ed); margin-bottom: -1px; }
    .body { padding: 12px 14px; }
    .err { color: var(--bx-red, #e5484d); font-size: 12px; margin: 4px 0; }
    .denied { padding: 20px 14px; }
    .denied code { background: var(--bx-panel-2); border: 1px solid var(--bx-border);
      border-radius: 4px; padding: 0 4px; font: 11.5px var(--bx-mono); }

    h4 { margin: 14px 0 6px; font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
         text-transform: uppercase; color: var(--bx-muted, #8794a1); }
    h4:first-child { margin-top: 0; }
    .cards { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 4px; }
    .stat { background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 6px; padding: 6px 12px; min-width: 74px; }
    .stat .n { font: 700 18px var(--bx-mono, monospace); color: var(--bx-accent, #1e88e5); }
    .stat .l { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
               color: var(--bx-muted, #8794a1); }
    .stat.warn .n { color: var(--bx-amber, #f2a71b); }

    table { border-collapse: collapse; width: 100%; font-size: 12px; }
    th { text-align: left; font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
         color: var(--bx-muted, #8794a1); font-weight: 600; padding: 3px 8px 3px 0; }
    td { padding: 3px 8px 3px 0; border-top: 1px solid var(--bx-border, #e4e8ed);
         vertical-align: top; }
    .mono { font-family: var(--bx-mono, monospace); }
    .pill { display: inline-block; font-size: 11px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      margin: 1px 2px 1px 0; }
    .dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; margin-right: 5px; }
    .st-healthy { color: var(--bx-green, #43a047); }
    .st-failed  { color: var(--bx-red, #e5484d); }
    .st-idle    { color: var(--bx-muted, #8794a1); }

    button.act { border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff);
      color: var(--bx-text, #33414e); border-radius: 5px; font: inherit; font-size: 11px;
      padding: 1px 8px; cursor: pointer; }
    button.act:hover { background: var(--bx-panel-2, #f7f8fa); }
    button.go { background: var(--bx-green, #43a047); color: #fff; border-color: transparent; }
    button.rm { color: var(--bx-red, #e5484d); border-color: color-mix(in srgb, var(--bx-red) 40%, transparent); }
    input, select { font: inherit; font-size: 12px; padding: 2px 6px;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 5px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    .secret { font-family: var(--bx-mono, monospace); }
    .muted { color: var(--bx-muted, #8794a1); }
    form.inline { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
    a.link { color: var(--bx-accent, #1e88e5); cursor: pointer; text-decoration: none; }
    a.link:hover { text-decoration: underline; }
    a.link.gated { color: var(--bx-muted, #8794a1); cursor: not-allowed; opacity: .55; }
    a.link.gated:hover { text-decoration: none; }

    /* ---- code & history ---- */
    .code { display: grid; grid-template-columns: 240px 1fr; gap: 12px; align-items: start; }
    .code .side { min-width: 0; }
    .code .main { min-width: 0; }
    .code .files, .code .hist { border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      overflow: hidden; margin-bottom: 10px; }
    .code .files .row, .code .hist .row { padding: 3px 8px; cursor: pointer; font-size: 12px;
      border-top: 1px solid var(--bx-border, #e4e8ed); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .code .files .row:first-child, .code .hist .row:first-child { border-top: 0; }
    .code .files .row.on, .code .hist .row.on { background: var(--bx-panel-2, #f7f8fa); }
    .code .files .row:hover, .code .hist .row:hover { background: var(--bx-panel-2, #f7f8fa); }
    .code .hist .row .s { font-family: var(--bx-mono, monospace); color: var(--bx-muted, #8794a1); font-size: 10.5px; }
    .code .hd { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
    .code .hd .path { font-family: var(--bx-mono, monospace); font-size: 12px; }
    .code pre { margin: 0; padding: 10px 12px; background: var(--bx-panel-2, #f7f8fa);
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px; overflow: auto; max-height: 70vh;
      font: 11.5px/1.5 var(--bx-mono, monospace); white-space: pre;
      color: var(--bx-text, #383a42); tab-size: 4; }
    /* diff: tint add/del lines (a line is a direct-child span), keep syntax colors */
    .code pre.diff > span { display: block; }
    .code pre.diff > .d { background: color-mix(in srgb, var(--bx-green, #43a047) 14%, transparent); }
    .code pre.diff > .a { background: color-mix(in srgb, var(--bx-red, #e5484d) 14%, transparent); }
    .code pre.diff > .h { color: var(--bx-accent, #1e88e5);
      background: color-mix(in srgb, var(--bx-accent, #1e88e5) 8%, transparent); }
    .code pre.diff > .fh { color: var(--bx-muted, #8794a1); }
    .grouphd { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
      color: var(--bx-muted, #8794a1); padding: 4px 8px; background: var(--bx-panel-2, #f7f8fa); }

    /* highlight.js — light palette (Atom-One-Light-ish) scoped to this shadow */
    .hljs-comment, .hljs-quote { color: #a0a1a7; font-style: italic; }
    .hljs-keyword, .hljs-selector-tag, .hljs-doctag, .hljs-formula { color: #a626a4; }
    .hljs-name, .hljs-section, .hljs-tag, .hljs-deletion { color: #e45649; }
    .hljs-string, .hljs-regexp, .hljs-addition, .hljs-meta .hljs-string { color: #50a14f; }
    .hljs-number, .hljs-literal, .hljs-type, .hljs-attr, .hljs-attribute,
    .hljs-variable, .hljs-template-variable, .hljs-selector-attr,
    .hljs-selector-pseudo, .hljs-selector-class { color: #986801; }
    .hljs-title, .hljs-title.function_, .hljs-built_in, .hljs-title.class_ { color: #4078f2; }
    .hljs-symbol, .hljs-bullet, .hljs-link, .hljs-meta, .hljs-selector-id { color: #0184bb; }
    .hljs-emphasis { font-style: italic; }
    .hljs-strong { font-weight: 600; }

    /* ---- runtime ---- */
    .hostcard { display: flex; flex-wrap: wrap; gap: 8px 18px; background: var(--bx-panel-2, #f7f8fa);
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 8px; padding: 10px 14px; margin-bottom: 12px; }
    .hostcard .kv { font-size: 12px; }
    .hostcard .kv b { font-family: var(--bx-mono, monospace); color: var(--bx-accent, #1e88e5); }
    .hostcard .kv span { color: var(--bx-muted, #8794a1); }
    .bk { border: 1px solid var(--bx-border, #e4e8ed); border-radius: 7px; margin-bottom: 7px; overflow: hidden; }
    .bk .row { display: grid; grid-template-columns: 16px minmax(110px,1.3fr) 70px repeat(5, minmax(44px, .7fr)) 84px 1.1fr;
      gap: 8px; align-items: center; padding: 6px 10px; cursor: pointer; font-size: 12px; }
    .bk .row.rrow { grid-template-columns: minmax(150px, 2fr) 64px 78px 1fr; }
    .bk .row:hover { background: var(--bx-panel-2, #f7f8fa); }
    .bk .row .caret { color: var(--bx-muted, #8794a1); transition: transform .1s; }
    .bk.open .row .caret { transform: rotate(90deg); }
    .bk .p { font-family: var(--bx-mono, monospace); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .bk .num { font-family: var(--bx-mono, monospace); text-align: right; }
    .bk .hdr { text-transform: uppercase; font-size: 9.5px; letter-spacing: .05em; color: var(--bx-muted, #8794a1);
      cursor: default; background: var(--bx-panel-2, #f7f8fa); }
    .bk .hdr:hover { background: var(--bx-panel-2, #f7f8fa); }
    .state { font-size: 10px; padding: 0 6px; border-radius: 999px; border: 1px solid var(--bx-border); text-align: center; }
    .state.healthy { color: var(--bx-green, #43a047); border-color: color-mix(in srgb, var(--bx-green) 45%, var(--bx-border)); }
    .state.building { color: var(--bx-accent, #1e88e5); }
    .state.failed  { color: var(--bx-red, #e5484d); }
    .state.idle    { color: var(--bx-muted, #8794a1); }
    .lock { font-size: 11px; }
    .detail { border-top: 1px solid var(--bx-border, #e4e8ed); padding: 8px 12px; background: var(--bx-panel-2, #f7f8fa);
      display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
    .detail h5 { margin: 0 0 4px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--bx-muted); }
    .detail .mono { font-family: var(--bx-mono, monospace); font-size: 11px; }
    .nsrow { font-size: 11px; }
    .nsrow .iso { color: var(--bx-green, #43a047); }
    .nsrow .shared { color: var(--bx-muted, #8794a1); }
    .flowtab { width: 100%; font-size: 11px; }
    .flowtab td { padding: 1px 6px 1px 0; }
    .flow-deny { color: var(--bx-red, #e5484d); }
    .flow-allow { color: var(--bx-green, #43a047); }
    .err-pill { color: var(--bx-red, #e5484d); font-size: 11px; }
  `;

  // {id, label} — ids stay URL-safe (no spaces/&) for hash deep-links.
  static TABS = [
    { id: 'overview', label: 'overview' },
    { id: 'runtime', label: 'runtime' },
    { id: 'code', label: 'code & history' },
    { id: 'users', label: 'users' },
    { id: 'vault', label: 'vault' },
    { id: 'grants', label: 'roles & grants' },
    { id: 'interfaces', label: 'interfaces' },
    { id: 'backup', label: 'backup' },
    { id: 'cron', label: 'cron' },
  ];

  constructor() {
    super();
    const h = location.hash.replace(/^#/, '');
    this._tab = BxAdmin.TABS.some((t) => t.id === h) ? h : 'overview';
    this._reveal = {};
    this._err = '';
    this._denied = false;
    this._rtOpen = new Set();
    this._versions = {};
    this._verOpen = new Set();
    this._schedules = [];
  }

  _setTab(t) {
    this._tab = t;
    try { history.replaceState(null, '', '#' + t); } catch { /* sandboxed */ }
    if (t === 'runtime') this._loadRuntime();
    if (t === 'interfaces') this._loadIfaces();
    if (t === 'backup') this._loadBackup();
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.buxon?.events.on((e) => {
      if (e.type === 'grants' || e.type === 'reload' || e.type === 'build-ok') this._refresh();
    });
    this._refresh();
    // The runtime tab is live: poll while it's the active tab.
    this._rtTimer = setInterval(() => { if (this._tab === 'runtime') this._loadRuntime(); }, 2000);
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); clearInterval(this._rtTimer); }

  async _loadRuntime() {
    try {
      const rt = await api('/runtime');
      // Sample per-backend network totals for live rate sparklines.
      this._hist = this._hist || {};
      const now = Date.now();
      for (const b of rt.backends || []) {
        const a = b.activity;
        const h = (this._hist[b.path] = this._hist[b.path] || []);
        h.push({ t: now, tx: a ? a.txBytes : 0, rx: a ? a.rxBytes : 0 });
        if (h.length > 40) h.shift();
      }
      this._rt = rt;
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // rateSeries turns cumulative byte samples into a bytes/sec series.
  _rateSeries(path) {
    const h = (this._hist || {})[path] || [];
    const out = [];
    for (let i = 1; i < h.length; i++) {
      const dt = (h[i].t - h[i - 1].t) / 1000 || 1;
      out.push(Math.max(0, (h[i].tx + h[i].rx - h[i - 1].tx - h[i - 1].rx) / dt));
    }
    return out;
  }
  _spark(path, w = 76, ht = 16) {
    const s = this._rateSeries(path);
    if (s.length < 2 || Math.max(...s) === 0) return html`<span class="muted" style="font-size:10px">idle</span>`;
    const max = Math.max(...s);
    const step = w / (s.length - 1);
    const pts = s.map((v, i) => `${(i * step).toFixed(1)},${(ht - (v / max) * (ht - 2) - 1).toFixed(1)}`).join(' ');
    const peak = this._fmtBytes(max) + '/s';
    return html`<svg width=${w} height=${ht} viewBox="0 0 ${w} ${ht}" title=${'peak ' + peak}>
      <polyline points=${pts} fill="none" stroke="var(--bx-accent,#1e88e5)" stroke-width="1.2"></polyline></svg>`;
  }

  async _refresh() {
    try {
      const [ov, vaults, cron, users] = await Promise.all([
        api('/auth-overview'), api('/vaults'), api('/cron/jobs'),
        api('/users').catch(() => ({ users: [] })),
      ]);
      this._ov = ov; this._vaults = vaults; this._cron = cron.jobs ?? [];
      this._users = users.users ?? [];
      this._err = ''; this._denied = false;
    } catch (e) {
      if (String(e.message).includes('admin')) this._denied = true;
      else this._err = String(e.message ?? e);
    }
  }

  // ---- vault ----
  async _revealKey(comp, key) {
    try {
      const d = await api(`/vault/${comp}/${encodeURIComponent(key)}`);
      this._reveal = { ...this._reveal, [comp + '\0' + key]: d.value };
    } catch (e) { this._err = String(e.message); }
  }
  _hideKey(comp, key) {
    const r = { ...this._reveal }; delete r[comp + '\0' + key]; this._reveal = r;
  }
  async _setSecret(comp, key, value) {
    await api(`/vault/${comp}/${encodeURIComponent(key)}`,
      { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) });
    this._hideKey(comp, key); this._refresh();
  }
  async _delSecret(comp, key) {
    if (!confirm(`Delete secret ${comp} / ${key}?`)) return;
    await api(`/vault/${comp}/${encodeURIComponent(key)}`, { method: 'DELETE' });
    this._refresh();
  }

  // ---- grants ----
  async _grant(from, target, role) {
    await api('/grants', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from, target, role }) });
    this._refresh();
  }
  async _revoke(g) {
    await api('/grants', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(g) });
    this._refresh();
  }

  // ---- cron ----
  async _delCron(j) {
    if (!confirm(`Delete cron job ${j.name} (${j.component})?`)) return;
    await api(`/cron/jobs/${encodeURIComponent(j.name)}?component=${encodeURIComponent(j.component)}`,
      { method: 'DELETE' });
    this._refresh();
  }

  // ---- code & history ----
  async _openCode(comp) {
    this._setTab('code');
    this._codeComp = comp; this._codeFile = null; this._codeDiff = null; this._codeMode = 'file';
    await this._loadCode();
    const files = this._codeTree?.files ?? [];
    const def = files.find((f) => f.path === 'index.html') || files.find((f) => f.path === 'buxon.json') || files[0];
    if (def) this._loadFile(def.path);
  }
  async _loadCode() {
    const c = encodeURIComponent(this._codeComp);
    try {
      const [tree, log] = await Promise.all([api(`/code/tree?component=${c}`), api(`/git/log?component=${c}`)]);
      this._codeTree = tree; this._codeLog = log;
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _loadFile(path) {
    try {
      this._codeFile = await api(`/code/file?component=${encodeURIComponent(this._codeComp)}&file=${encodeURIComponent(path)}`);
      this._codeMode = 'file';
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _loadDiff(rev) {
    try {
      const d = await api(`/git/diff?component=${encodeURIComponent(this._codeComp)}&rev=${encodeURIComponent(rev)}`);
      this._codeDiff = { rev, diff: d.diff ?? '' }; this._codeMode = 'diff';
    } catch (e) { this._err = String(e.message ?? e); }
  }
  _fmtDate(iso) { try { return new Date(iso).toLocaleDateString(); } catch { return iso; } }

  // Render a unified diff to syntax-highlighted HTML: add/del lines tinted,
  // their code tokenized by the current file's language (tracked from headers).
  _diffHTML(diff) {
    if (!diff) return '<span class="muted">no changes</span>';
    const hdr = /^(--- |\+\+\+ )(a\/|b\/|\/dev\/null)/;
    let lang = '';
    const out = [];
    for (const raw of diff.split('\n')) {
      if (raw.startsWith('diff --git')) {
        const m = raw.match(/ b\/(\S+)/); if (m) lang = langFor(m[1]);
        out.push(`<span class="fh">${escHTML(raw)}</span>`); continue;
      }
      if (raw.startsWith('+++ ')) {
        const m = raw.match(/\+\+\+ b\/(.+)/); if (m) lang = langFor(m[1]);
      }
      if (hdr.test(raw) || raw.startsWith('index ') || raw.startsWith('new file')
        || raw.startsWith('deleted file') || raw.startsWith('similarity ') || raw.startsWith('rename ')) {
        out.push(`<span class="fh">${escHTML(raw)}</span>`); continue;
      }
      if (raw.startsWith('@@')) { out.push(`<span class="h">${escHTML(raw)}</span>`); continue; }
      const sign = raw[0];
      if (sign === '+') out.push(`<span class="d">+${hl(raw.slice(1), lang)}</span>`);
      else if (sign === '-') out.push(`<span class="a">-${hl(raw.slice(1), lang)}</span>`);
      else if (sign === ' ') out.push(`<span class="ctx"> ${hl(raw.slice(1), lang)}</span>`);
      else out.push(`<span class="fh">${escHTML(raw)}</span>`); // '\ No newline', blanks
    }
    return out.join('');
  }

  _codeView() {
    if (!this._codeComp) {
      const comps = this._ov?.components ?? [];
      return html`
        <p class="muted" style="margin-top:0">Pick a component to browse its files and git history.</p>
        <div class="files" style="max-width:360px">
          ${comps.map((k) => html`<div class="row" @click=${() => this._openCode(k.path)}>${k.path}</div>`)}
        </div>`;
    }
    const tree = this._codeTree?.files ?? [];
    const log = this._codeLog?.commits ?? [];
    const noRepo = this._codeLog?.repo === false;
    return html`
      <div class="hd">
        <a class="link" @click=${() => { this._codeComp = null; }}>← components</a>
        <span class="path">${this._codeComp}</span>
      </div>
      <div class="code">
        <div class="side">
          <div class="files">
            <div class="grouphd">files</div>
            ${tree.length ? tree.map((f) => html`
              <div class="row ${this._codeMode === 'file' && this._codeFile?.path === f.path ? 'on' : ''}"
                   @click=${() => this._loadFile(f.path)}>${f.path}</div>`)
              : html`<div class="row muted">—</div>`}
          </div>
          <div class="hist">
            <div class="grouphd">history</div>
            <div class="row ${this._codeMode === 'diff' && this._codeDiff?.rev === '' ? 'on' : ''}"
                 @click=${() => this._loadDiff('')}>● uncommitted changes</div>
            ${noRepo ? html`<div class="row muted">not a git repo</div>`
              : log.length ? log.map((c) => html`
                <div class="row ${this._codeMode === 'diff' && this._codeDiff?.rev === c.hash ? 'on' : ''}"
                     @click=${() => this._loadDiff(c.hash)}>
                  <div>${c.subject}</div><div class="s">${c.short} · ${c.author} · ${this._fmtDate(c.date)}</div>
                </div>`)
              : html`<div class="row muted">no commits touch this component</div>`}
          </div>
        </div>
        <div class="main">
          ${this._codeMode === 'diff'
            ? html`<pre class="diff hljs">${unsafeHTML(this._diffHTML(this._codeDiff?.diff))}</pre>`
            : this._codeFile
              ? (this._codeFile.binary ? html`<span class="muted">binary file (${this._codeFile.size} bytes)</span>`
                : this._codeFile.truncated ? html`<span class="muted">file too large to display (${this._codeFile.size} bytes)</span>`
                : html`<pre class="hljs"><code>${unsafeHTML(hl(this._codeFile.content ?? '', langFor(this._codeFile.path ?? '')))}</code></pre>`)
              : html`<span class="muted">select a file or a commit</span>`}
        </div>
      </div>`;
  }

  render() {
    if (this._denied) return html`<div class="denied">
      <b>No admin access.</b> This tile needs the <code>buxon:admin</code> grant.
      Approve <span class="mono">tiles/admin → buxon : admin</span> in the grants
      panel, or run <code>bx grant tiles/admin buxon:admin</code>.
      See <a href="/docs/auth.md" target="_blank">docs/auth.md</a>.</div>`;
    const tab = this._tab;
    return html`
      <div class="tabs">
        ${BxAdmin.TABS.map((t) => html`
          <button class=${t.id === tab ? 'on' : ''} @click=${() => this._setTab(t.id)}>${t.label}</button>`)}
      </div>
      <div class="body">
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
        ${tab === 'users' ? this._usersView()
          : tab === 'overview' ? this._overview()
          : tab === 'runtime' ? this._runtimeView()
          : tab === 'code' ? this._codeView()
          : tab === 'vault' ? this._vaultView()
          : tab === 'grants' ? this._rolesView()
          : tab === 'interfaces' ? this._ifacesView()
          : tab === 'backup' ? this._backupView()
          : this._cronView()}
      </div>`;
  }

  // ---- runtime ----
  _fmtBytes(n) {
    n = n || 0; const u = ['B', 'K', 'M', 'G', 'T']; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(1)) + u[i];
  }
  _fmtDur(s) {
    s = Math.max(0, s | 0);
    if (s < 60) return s + 's';
    if (s < 3600) return (s / 60 | 0) + 'm' + (s % 60) + 's';
    if (s < 86400) return (s / 3600 | 0) + 'h' + ((s % 3600) / 60 | 0) + 'm';
    return (s / 86400 | 0) + 'd' + ((s % 86400) / 3600 | 0) + 'h';
  }
  _toggleBk(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
  }
  _mem(b) {
    if (b.cgroup && b.cgroup.memCurrent) return this._fmtBytes(b.cgroup.memCurrent);
    if (b.rssKb) return this._fmtBytes(b.rssKb * 1024);
    return '—';
  }
  _flowTime(f) {
    const ageS = Math.max(0, (Date.now() - f.start) / 1000);
    const age = ageS < 60 ? (ageS | 0) + 's ago' : this._fmtDur(ageS) + ' ago';
    if (!f.end) return age + ' · open';
    const dur = (f.end - f.start) / 1000;
    return age + (dur >= 0.05 ? ' · ' + dur.toFixed(1) + 's' : '');
  }

  _runtimeView() {
    const rt = this._rt; if (!rt) return html`<span class="muted">loading…</span>`;
    const h = rt.host || {};
    const kv = (label, val) => html`<div class="kv"><span>${label}</span> <b>${val}</b></div>`;
    return html`
      <div class="hostcard">
        ${kv('buxond', h.version)}
        ${kv('kernel', h.kernel || '—')}
        ${kv('pid', h.pid)}
        ${kv('euid', h.uid)}
        ${kv('cpus', h.numCPU)}
        ${kv('goroutines', h.goroutines)}
        ${kv('heap', this._fmtBytes((h.heapMB || 0) * 1e6))}
        ${kv('uptime', this._fmtDur(h.uptimeSec))}
        ${kv('isolation', h.isolate ? 'on (tier 3)' : (h.scopeUids ? 'uids (tier 2)' : 'off (tier 1)'))}
        ${h.isolate ? kv('rootfs', h.rootfs) : nothing}
      </div>
      <div class="bk"><div class="row hdr">
        <span></span><span>component</span><span>state</span>
        <span class="num">pid</span><span class="num">cpu·s</span><span class="num">mem</span>
        <span class="num">fds</span><span class="num">conns</span><span>net</span><span>egress</span>
      </div></div>
      ${(rt.backends || []).map((b) => this._bkRow(b))}
      ${(rt.backends || []).length === 0 ? html`<span class="muted">no backends running</span>` : nothing}
      ${this._resourcesSection(rt.resources)}`;
  }

  _resourcesSection(resources) {
    if (!resources || !resources.length) return nothing;
    return html`
      <h4>resources</h4>
      <div class="bk"><div class="row hdr rrow"><span>id</span><span>type</span><span class="num">size</span><span>detail</span></div></div>
      ${resources.map((r) => html`<div class="bk"><div class="row rrow">
        <span class="p" title=${r.id}>${r.id}</span>
        <span>${r.type}</span>
        <span class="num">${r.size ? this._fmtBytes(r.size) : '—'}</span>
        <span class="muted">${r.detail || ''}</span>
      </div></div>`)}`;
  }

  _bkRow(b) {
    const open = this._rtOpen.has(b.path);
    const act = b.activity;
    const egress = b.isolated
      ? (b.egress && b.egress.length
          ? html`${b.egress.length} rule(s)${act ? html` · <span class="flow-allow">${act.allowed}↑</span>/<span class="flow-deny">${act.denied}⛔</span>` : nothing}`
          : html`<span class="muted">deny-all</span>`)
      : html`<span class="muted">host net</span>`;
    return html`
      <div class="bk ${open ? 'open' : ''}">
        <div class="row" @click=${() => this._toggleBk(b.path)}>
          <span class="caret">▶</span>
          <span class="p" title=${b.path}>${b.path} ${b.isolated ? html`<span class="lock" title="sandboxed">🔒</span>` : nothing}</span>
          <span class="state ${b.state}">${b.state}</span>
          <span class="num">${b.pid || '—'}</span>
          <span class="num">${b.cpuSec ? b.cpuSec.toFixed(1) : '—'}</span>
          <span class="num" title=${b.cgroup ? 'cgroup memory.current' : 'RSS'}>${this._mem(b)}</span>
          <span class="num">${b.fds || '—'}</span>
          <span class="num">${b.activeConns}</span>
          <span>${b.isolated ? this._spark(b.path) : html`<span class="muted" style="font-size:10px">—</span>`}</span>
          <span>${egress}</span>
        </div>
        ${open ? this._bkDetail(b) : nothing}
      </div>`;
  }

  _bkDetail(b) {
    const act = b.activity;
    return html`<div class="detail">
      <div>
        <h5>process</h5>
        <div class="mono">runtime ${b.runtime || 'static'} · gen ${b.gen} · up ${this._fmtDur(b.uptimeSec)}</div>
        <div class="mono">threads ${b.threads || '—'} · restarts ${b.restarts} · last req ${b.lastReqSec < 0 ? 'never' : this._fmtDur(b.lastReqSec) + ' ago'}</div>
        ${b.cgroup ? html`<div class="mono">cgroup: ${this._fmtBytes(b.cgroup.memCurrent)}${b.cgroup.memMax > 0 ? ' / ' + this._fmtBytes(b.cgroup.memMax) : ''} · cpu ${(b.cgroup.cpuUsec / 1e6).toFixed(1)}s · ${b.cgroup.pidsCurrent} pid(s)</div>` : nothing}
        ${b.error ? html`<div class="err-pill">${b.error}</div>` : nothing}
      </div>
      <div>
        <h5>namespaces</h5>
        ${b.namespaces
          ? Object.entries(b.namespaces).map(([k, v]) => html`<div class="nsrow">${k}: <span class=${v.isolated ? 'iso' : 'shared'}>${v.isolated ? 'isolated' : 'shared'}</span> <span class="muted mono">${v.id}</span></div>`)
          : html`<span class="muted">shared with host (not sandboxed)</span>`}
      </div>
      <div>
        <h5>egress ${act ? html`· ${this._fmtBytes(act.txBytes)}↑ ${this._fmtBytes(act.rxBytes)}↓ · ${act.active} active` : nothing}</h5>
        ${(b.egress && b.egress.length) ? html`<div class="mono">${b.egress.join(', ')}</div>` : html`<span class="muted">${b.isolated ? 'no egress granted (deny-all)' : 'unrestricted (host network)'}</span>`}
        ${act && act.recent && act.recent.length ? html`
          <table class="flowtab"><tbody>
            ${act.recent.slice(0, 12).map((f) => html`<tr>
              <td class=${f.allowed ? 'flow-allow' : 'flow-deny'}>${f.allowed ? '✓' : '⛔'}</td>
              <td class="mono">${f.proto} ${f.dst}:${f.port}</td>
              <td class="mono">${this._fmtBytes(f.txBytes)}↑ ${this._fmtBytes(f.rxBytes)}↓</td>
              <td class="muted">${this._flowTime(f)}</td>
            </tr>`)}
          </tbody></table>` : nothing}
      </div>
    </div>`;
  }

  _overview() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const c = ov.counts;
    return html`
      <div class="cards">
        <div class="stat"><div class="n">${c.components}</div><div class="l">components</div></div>
        <div class="stat"><div class="n">${c.exposed}</div><div class="l">expose APIs</div></div>
        <div class="stat"><div class="n">${c.grants}</div><div class="l">grants</div></div>
        <div class="stat ${c.pending ? 'warn' : ''}"><div class="n">${c.pending}</div><div class="l">pending</div></div>
      </div>
      <h4>principals</h4>
      <table>
        <tr><th>component</th><th>runtime</th><th>exposes</th><th>uses</th><th>vault</th><th>lifecycle</th></tr>
        ${ov.components.map((k) => html`<tr>
          <td class="mono"><a class="link" @click=${() => this._openCode(k.path)} title="view code & history">${k.path}</a>${k.manifestError ? html` <span class="st-failed" title=${k.manifestError}>⚠</span>` : nothing}</td>
          <td class="muted">${k.runtime || 'static'}</td>
          <td>${k.roles ? Object.keys(k.roles).map((r) => html`<span class="pill">${r}</span>`) : html`<span class="muted">—</span>`}</td>
          <td>${(k.uses ?? []).map((u) => html`<span class="pill">${u.target}:${u.role}</span>`)}</td>
          <td>${k.hasVault ? '🔑' : ''}</td>
          <td>${this._lifecycleCell(k)}</td>
        </tr>`)}
      </table>`;
  }

  // Lifecycle toggle (plans/lifecycle.md). Static/CGI components with no backend
  // still list, but only a running-backend runtime benefits — offer the toggle
  // for any runtime the owner may want paused.
  _lifecycleCell(k) {
    const st = k.state || 'enabled';
    const disabled = st !== 'enabled';
    return html`${disabled ? html`<span class="pill st-failed" title="not running">${st}</span> ` : nothing}
      <a class="link" @click=${() => this._setLifecycle(k.path, disabled ? 'enabled' : 'disabled')}>${disabled ? 'enable' : 'disable'}</a>`;
  }

  async _setLifecycle(path, state) {
    // Offload removes local bytes (after archiving) — confirm before the flip.
    if ((state === 'offloaded' || state === 'offloaded-full') &&
        !confirm(`Offload ${path}? Its ${state === 'offloaded-full' ? 'data + source' : 'data'} will be archived, then removed locally.`)) {
      this._refresh(); // revert the <select>
      return;
    }
    this._busy = path;
    try {
      await api('/lifecycle', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: path, state }) });
      await this._refresh();
      if (this._tab === 'backup') await this._loadBackup();
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  _vaultView() {
    const vs = this._vaults ?? [];
    return html`
      ${vs.length === 0 ? html`<span class="muted">no vaults hold secrets yet — set one with
        <span class="mono">bx vault set &lt;component&gt; &lt;key&gt;</span> or below.</span>` : nothing}
      ${vs.map((v) => html`
        <h4>${v.component}</h4>
        <table>
          ${v.keys.map((k) => {
            const shown = this._reveal[v.component + '\0' + k];
            return html`<tr>
              <td class="mono" style="width:30%">${k}</td>
              <td class="secret">${shown !== undefined ? shown : '••••••••'}</td>
              <td style="text-align:right; white-space:nowrap">
                ${shown !== undefined
                  ? html`<button class="act" @click=${() => navigator.clipboard?.writeText(shown)}>copy</button>
                         <button class="act" @click=${() => this._hideKey(v.component, k)}>hide</button>
                         <button class="act" @click=${() => { const nv = prompt(`New value for ${k}`, shown); if (nv != null) this._setSecret(v.component, k, nv); }}>edit</button>`
                  : html`<button class="act" @click=${() => this._revealKey(v.component, k)}>reveal</button>`}
                <button class="act rm" @click=${() => this._delSecret(v.component, k)}>del</button>
              </td></tr>`;
          })}
        </table>`)}
      <form class="inline" @submit=${(e) => { e.preventDefault();
          const f = e.target;
          if (f.comp.value && f.key.value) this._setSecret(f.comp.value.trim(), f.key.value.trim(), f.val.value);
          f.reset(); }}>
        <input name="comp" placeholder="component" size="16" list="admin-comps">
        <input name="key" placeholder="key" size="12">
        <input name="val" placeholder="value" size="18" type="password">
        <button class="act go">set secret</button>
      </form>
      <datalist id="admin-comps">
        ${(this._ov?.components ?? []).map((k) => html`<option value=${k.path}></option>`)}
      </datalist>`;
  }

  _rolesView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    // Empty slices arrive as null over JSON — coalesce before touching them.
    const comps = ov.components ?? [];
    const pending = ov.pending ?? [];
    const grants = ov.grants ?? [];
    return html`
      ${pending.length ? html`<h4>pending requests</h4>
        <table>${pending.map((g) => html`<tr>
          <td class="mono">${g.from} → ${g.target}</td>
          <td><span class="pill">${g.role}</span></td>
          <td style="text-align:right"><button class="act go" @click=${() => this._grant(g.from, g.target, g.role)}>approve</button></td>
        </tr>`)}</table>` : nothing}

      <h4>active grants</h4>
      <table>${grants.length ? grants.map((g) => html`<tr>
        <td class="mono">${g.from} → ${g.target}</td>
        <td><span class="pill">${g.role}</span></td>
        <td style="text-align:right"><button class="act rm" @click=${() => this._revoke(g)}>revoke</button></td>
      </tr>`) : html`<tr><td class="muted">none</td></tr>`}</table>

      <h4>add grant</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
          if (f.from.value && f.target.value && f.role.value) this._grant(f.from.value, f.target.value.trim(), f.role.value.trim());
          f.reset(); }}>
        <select name="from">${comps.map((k) => html`<option>${k.path}</option>`)}</select>
        <span class="muted">→</span>
        <input name="target" placeholder="apps/other or res:…/… or buxon" size="20">
        <span class="muted">:</span>
        <input name="role" placeholder="reader" size="8" value="reader">
        <button class="act go">grant</button>
      </form>

      <h4>exposed roles</h4>
      <table>
        <tr><th>component</th><th>role</th><th>description</th></tr>
        ${comps.filter((k) => k.roles).flatMap((k) =>
          Object.entries(k.roles).map(([role, desc]) => html`<tr>
            <td class="mono">${k.path}</td><td><span class="pill">${role}</span></td>
            <td class="muted">${desc}</td></tr>`))}
      </table>`;
  }

  // ---- interfaces (typed capability wiring; plans/interfaces.md) ----
  async _loadIfaces() {
    try { this._ifaces = await api('/bindings'); this._err = ''; }
    catch (e) { this._err = String(e.message ?? e); }
  }
  async _bindSet(component, slot, provider) {
    try {
      await api('/bindings', {
        method: provider ? 'POST' : 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component, slot, provider }),
      });
      await this._loadIfaces();
    } catch (e) { this._err = String(e.message ?? e); }
  }
  _ifacesView() {
    const d = this._ifaces;
    if (!d) return html`<div class="muted">loading…</div>`;
    const comps = d.components || [];
    // providers of each kind, for the request dropdowns
    const providersByKind = {};
    for (const c of comps)
      for (const def of Object.values(c.provides || {}))
        (providersByKind[def.kind] ||= []).push(c.component);
    // builtin net providers
    const builtins = { net: ['internet', 'host'] };
    const requests = comps.flatMap((c) =>
      Object.entries(c.interfaces || {}).map(([slot, def]) => ({ comp: c.component, slot, def })));
    return html`
      <p class="muted">Each component <b>requests</b> typed interface slots; you <b>bind</b> each to a
        provider (a builtin or a tile that <b>provides</b> it). The binding is the authorization —
        unbound means no capability. See <a href="/docs/protocol.md" target="_blank">plans/interfaces.md</a>.</p>
      <h3>Providers</h3>
      <table class="tbl">
        <tr><th>tile</th><th>slot</th><th>kind</th></tr>
        ${comps.flatMap((c) => Object.entries(c.provides || {}).map(([slot, def]) => html`<tr>
          <td class="mono">${c.component}</td><td>${slot}</td><td><span class="pill">${def.kind}</span></td></tr>`))}
        ${Object.keys(providersByKind).length === 0 ? html`<tr><td class="muted" colspan="3">no provider tiles</td></tr>` : nothing}
      </table>
      <h3>Requests → binding</h3>
      <table class="tbl">
        <tr><th>component</th><th>slot</th><th>kind</th><th>bound to</th></tr>
        ${requests.map((r) => {
          const bound = d.bindings?.[r.comp]?.[r.slot] || '';
          const opts = [...(builtins[r.def.kind] || []), ...(providersByKind[r.def.kind] || []).filter((p) => p !== r.comp)];
          return html`<tr>
            <td class="mono">${r.comp}</td><td>${r.slot}</td><td><span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}</span></td>
            <td><select @change=${(e) => this._bindSet(r.comp, r.slot, e.target.value)}>
              <option value="" ?selected=${!bound}>— unbound —</option>
              ${opts.map((p) => html`<option value=${p} ?selected=${bound === p}>${p}</option>`)}
            </select></td></tr>`;
        })}
        ${requests.length === 0 ? html`<tr><td class="muted" colspan="4">no components request interfaces</td></tr>` : nothing}
      </table>`;
  }

  // ---- backup (plans/lifecycle.md) ----
  async _loadBackup() {
    try {
      const [ifaces, sched] = await Promise.all([api('/bindings'), api('/backup-schedule')]);
      this._ifaces = ifaces;
      this._schedules = sched.schedules || [];
      this._err = '';
      // Load versions for disabled components so the offload gate is computable
      // (does a post-disable backup exist?) without expanding each one.
      const disabled = (this._ov?.components || []).filter((c) => c.state === 'disabled');
      await Promise.all(disabled.map((c) => this._loadVersions(c.path)));
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // Components that provide an `archive` interface (candidate archivers).
  _archivers() {
    const out = [];
    for (const c of (this._ifaces?.components || []))
      for (const def of Object.values(c.provides || {}))
        if (def.kind === 'archive') out.push(c.component);
    return out;
  }

  // '*' sets the workspace default; provider '' clears an override.
  async _setArchiver(comp, provider) {
    try {
      const body = JSON.stringify(provider ? { component: comp, slot: '@archive', provider } : { component: comp, slot: '@archive' });
      await api('/bindings', { method: provider ? 'POST' : 'DELETE', headers: { 'Content-Type': 'application/json' }, body });
      await this._loadBackup();
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _toggleVersions(comp) {
    const s = new Set(this._verOpen);
    if (s.has(comp)) { s.delete(comp); this._verOpen = s; return; }
    s.add(comp); this._verOpen = s;
    await this._loadVersions(comp);
  }
  async _loadVersions(comp) {
    try {
      const d = await api('/backups?component=' + encodeURIComponent(comp));
      this._versions = { ...this._versions, [comp]: d.versions || [] };
    } catch (e) { this._versions = { ...this._versions, [comp]: [] }; this._err = String(e.message ?? e); }
  }

  async _backupNow(comp) {
    this._busy = comp;
    try {
      await api('/backup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: comp }) });
      this._verOpen = new Set(this._verOpen).add(comp);
      await this._loadVersions(comp);
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  async _restoreVersion(comp, version) {
    if (!confirm(`Restore ${comp} from ${version}? This replaces its current data/source.`)) return;
    this._busy = comp;
    try {
      await api('/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: comp, version }) });
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  // Restore one file from a version — streamed back and offered as a download.
  async _restoreFile(comp, version) {
    const path = prompt('File path within the archive (e.g. source/index.html or data/kv.json):');
    if (!path) return;
    try {
      const r = await buxon.fetch('/api/buxon/restore', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: comp, version, file: path }),
      });
      if (!r.ok) throw new Error((await r.json()).error || r.status);
      const blob = await r.blob();
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = path.split('/').pop() || 'file';
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _setSchedule(comp, every, keep) {
    if (!every.trim()) return;
    try {
      await api('/backup-schedule', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: comp, schedule: '@every ' + every.trim(), retention: parseInt(keep, 10) || 0 }) });
      await this._loadBackup();
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _clearSchedule(comp) {
    try { await api('/backup-schedule?component=' + encodeURIComponent(comp), { method: 'DELETE' }); await this._loadBackup(); }
    catch (e) { this._err = String(e.message ?? e); }
  }

  _backupView() {
    const ov = this._ov, ifaces = this._ifaces;
    if (!ov || !ifaces) return html`<div class="muted">loading…</div>`;
    const archivers = this._archivers();
    if (archivers.length === 0)
      return html`<p class="muted">No archiver installed. Import the <b>S3 Archiver</b> tile (or another
        <code>archive</code> provider) from the Tile Manager, then pick it as the default below.</p>`;
    const defArch = ifaces.bindings?.['*']?.['@archive'] || '';
    const comps = (ov.components || []).filter((c) => !archivers.includes(c.path)); // an archiver isn't its own target
    const schedFor = (p) => this._schedules.find((s) => s.component === p);
    return html`
      <p class="muted">Back up a component (its source + data + terminal layer) to an archiver, offload to
        free disk, or restore a version/file. Vault is not backed up. See
        <a href="/docs/protocol.md" target="_blank">plans/lifecycle.md</a>.</p>
      <h3>Default archiver</h3>
      <select @change=${(e) => this._setArchiver('*', e.target.value)}>
        <option value="" ?selected=${!defArch}>— none —</option>
        ${archivers.map((a) => html`<option value=${a} ?selected=${defArch === a}>${a}</option>`)}
      </select>
      <span class="muted" style="margin-left:8px">used unless a component overrides it</span>

      <h3>Components</h3>
      <table class="tbl">
        <tr><th>component</th><th>lifecycle</th><th>archiver</th><th>schedule</th><th></th></tr>
        ${comps.map((c) => this._backupRow(c, archivers, defArch, schedFor(c.path)))}
      </table>`;
  }

  _backupRow(c, archivers, defArch, sched) {
    const override = this._ifaces.bindings?.[c.path]?.['@archive'] || '';
    const busy = this._busy === c.path;
    const open = this._verOpen.has(c.path);
    return html`
      <tr>
        <td class="mono">${c.path}</td>
        <td>${this._lifecycleControls(c)}</td>
        <td><select @change=${(e) => this._setArchiver(c.path, e.target.value)}>
          <option value="" ?selected=${!override}>default${defArch ? ' (' + defArch + ')' : ''}</option>
          ${archivers.map((a) => html`<option value=${a} ?selected=${override === a}>${a}</option>`)}
        </select></td>
        <td class="mono">${sched
          ? html`${sched.schedule}${sched.retention ? ' ·keep ' + sched.retention : ''}
              <a class="link" title="remove schedule" @click=${() => this._clearSchedule(c.path)}>✕</a>`
          : this._scheduleForm(c.path)}</td>
        <td style="white-space:nowrap">
          <a class="link" @click=${() => !busy && this._backupNow(c.path)}>${busy ? 'working…' : 'back up'}</a>
          · <a class="link" @click=${() => this._toggleVersions(c.path)}>versions${open ? ' ▾' : ''}</a>
        </td>
      </tr>
      ${open ? html`<tr><td colspan="5">${this._versionsList(c.path)}</td></tr>` : nothing}`;
  }

  // Guided lifecycle controls (plans/lifecycle.md). Offload is deliberately a
  // two-step, safe flow: you must DISABLE first (stops the backend → a consistent
  // db), then take a backup, and only then does offload un-gray — so you never
  // free local data without a verified, stopped-state snapshot.
  _lifecycleControls(c) {
    const st = c.state || 'enabled';
    const busy = this._busy === c.path;
    const act = (label, state, opts = {}) => html`<a
      class="link ${opts.gated ? 'gated' : ''}" title=${opts.title || ''}
      @click=${() => !busy && !opts.gated && this._setLifecycle(c.path, state)}>${busy ? '…' : label}</a>`;

    if (st === 'enabled') return html`enabled · ${act('disable', 'disabled')}`;
    if (st === 'disabled') {
      const ready = this._hasPostDisableBackup(c);
      const why = ready ? 'Archive + remove local data (source kept).'
        : 'Back up first — offload needs a backup taken while disabled (a consistent snapshot).';
      const whyFull = ready ? 'Archive + remove data AND source.' : why;
      return html`disabled · ${act('enable', 'enabled')}
        · ${act('offload', 'offloaded', { gated: !ready, title: why })}
        · ${act('offload+src', 'offloaded-full', { gated: !ready, title: whyFull })}
        ${ready ? nothing : html`<span class="muted" style="font-size:11px"> (back up to enable offload)</span>`}`;
    }
    // offloaded / offloaded-full
    return html`${st} · ${act('restore', 'enabled')}`;
  }

  // Offload is allowed only once a backup exists that was taken AFTER the tile was
  // disabled (its snapshot is consistent because the backend is stopped).
  _hasPostDisableBackup(c) {
    if ((c.state || 'enabled') !== 'disabled' || !c.stateAt) return false;
    const since = Date.parse(c.stateAt);
    const vers = this._versions[c.path];
    return Array.isArray(vers) && vers.some((v) => Date.parse(v.time) >= since);
  }

  _scheduleForm(comp) {
    return html`<span>
      <input class="ev" placeholder="24h" style="width:48px">
      <input class="kp" placeholder="keep" style="width:44px">
      <a class="link" @click=${(e) => { const s = e.target.parentElement; this._setSchedule(comp, s.querySelector('.ev').value, s.querySelector('.kp').value); }}>set</a>
    </span>`;
  }

  _versionsList(comp) {
    const vers = this._versions[comp];
    if (!vers) return html`<span class="muted">loading…</span>`;
    if (vers.length === 0) return html`<span class="muted">no backups yet</span>`;
    return html`<table class="tbl" style="margin:2px 0 4px 16px">
      ${vers.map((v) => html`<tr>
        <td class="mono">${v.version}</td>
        <td class="muted">${v.time}</td>
        <td class="mono">${this._fmtBytes(v.size)}</td>
        <td style="white-space:nowrap">
          <a class="link" @click=${() => this._restoreVersion(comp, v.version)}>restore</a>
          · <a class="link" @click=${() => this._restoreFile(comp, v.version)}>file…</a>
        </td>
      </tr>`)}
    </table>`;
  }

  _cronView() {
    const jobs = this._cron ?? [];
    return html`
      ${jobs.length === 0 ? html`<span class="muted">no scheduled jobs.</span>` : html`
        <table>
          <tr><th>name</th><th>component</th><th>schedule</th><th>path</th><th>role</th><th></th></tr>
          ${jobs.map((j) => html`<tr>
            <td class="mono">${j.name}</td>
            <td class="mono">${j.component}</td>
            <td class="mono">${j.schedule}</td>
            <td class="mono">${j.path}</td>
            <td><span class="pill">${j.role}</span></td>
            <td style="text-align:right"><button class="act rm" @click=${() => this._delCron(j)}>delete</button></td>
          </tr>`)}
        </table>`}`;
  }

  // ---- users ----
  async _createUser(f) {
    const tiles = f.tiles.value.split(',').map((s) => s.trim()).filter(Boolean);
    await api('/users', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: f.id.value.trim(), name: f.name.value.trim(), role: f.role.value,
        tiles, terminal: f.terminal.checked, password: f.password.value }) });
    f.reset(); this._refresh();
  }
  async _patchUser(id, patch) {
    await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch) });
    this._refresh();
  }
  async _resetPw(id) {
    const pw = prompt(`New password for ${id}:`);
    if (!pw) return;
    await this._patchUser(id, { password: pw });
    this._err = `password reset for ${id}`;
  }
  async _editTiles(u) {
    const v = prompt(`Allowed tiles for ${u.id} (comma-separated paths or prefix/*; * = all):`,
      (u.tiles || []).join(', '));
    if (v == null) return;
    this._patchUser(u.id, { tiles: v.split(',').map((s) => s.trim()).filter(Boolean) });
  }
  async _delUser(id) {
    if (!confirm(`Delete user ${id}? Their sessions are revoked immediately.`)) return;
    await api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
    this._refresh();
  }

  _usersView() {
    const users = this._users ?? [];
    return html`
      <h4>users</h4>
      <table>
        <tr><th>id</th><th>name</th><th>role</th><th>tiles</th><th>terminal</th><th></th></tr>
        ${users.length ? users.map((u) => html`<tr>
          <td class="mono">${u.id}</td>
          <td>${u.name}</td>
          <td><span class="pill">${u.role}</span></td>
          <td>${u.role === 'admin' ? html`<span class="muted">all</span>`
            : (u.tiles || []).length ? (u.tiles || []).map((t) => html`<span class="pill">${t}</span>`)
            : html`<span class="muted">none</span>`}</td>
          <td>${u.terminal || u.role === 'admin' ? '✓' : ''}</td>
          <td style="text-align:right; white-space:nowrap">
            <button class="act" @click=${() => this._patchUser(u.id, { role: u.role === 'admin' ? 'user' : 'admin' })}>${u.role === 'admin' ? 'demote' : 'make admin'}</button>
            ${u.role === 'admin' ? nothing : html`<button class="act" @click=${() => this._editTiles(u)}>tiles</button>
              <button class="act" @click=${() => this._patchUser(u.id, { terminal: !u.terminal })}>${u.terminal ? '− term' : '+ term'}</button>`}
            <button class="act" @click=${() => this._resetPw(u.id)}>pw</button>
            <button class="act rm" @click=${() => this._delUser(u.id)}>del</button>
          </td>
        </tr>`) : html`<tr><td class="muted" colspan="6">no users — the root token is the only admin. Add one below.</td></tr>`}
      </table>

      <h4>add user</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createUser(e.target); }}>
        <input name="id" placeholder="username" size="12" required>
        <input name="name" placeholder="display name" size="14">
        <select name="role"><option value="user">user</option><option value="admin">admin</option></select>
        <input name="tiles" placeholder="apps/chat, lib/*  (blank = none)" size="22">
        <label class="muted" style="font-size:11px"><input type="checkbox" name="terminal"> terminal</label>
        <input name="password" type="password" placeholder="password" size="12" required>
        <button class="act go">create</button>
      </form>
      <p class="muted" style="font-size:11px;margin-top:6px">
        Terminal = a <b>root shell</b> in a tile's directory; grant it only to trusted users.</p>`;
  }
}

customElements.define('bx-admin', BxAdmin);
