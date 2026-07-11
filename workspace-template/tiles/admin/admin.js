/**
 * <bx-admin> — the workspace admin console (tiles/admin). Full owner-view
 * into the running system, powered by xbind's admin-capable endpoints via
 * the xbin:admin capability (see xbin.json / API.md).
 *
 * Tabs: overview · code & history · users · orgs & teams · vault ·
 * roles & grants · cron.
 * All reads go through xbin.fetch (admin identity attributed by frame token)
 * and refresh on the grants/reload event stream. The code & history tab browses
 * a component's files and git log/diffs (syntax-highlighted via vendored
 * highlight.js), scoped to its path in the single workspace repo.
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import hljs from '/vendor/highlight.min.js';
import '/vendor/bx-multiselect.js';

const api = async (path, opts) => {
  const r = await xbin.fetch('/api/xbin' + path, opts);
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
    _vaults: { state: true },      // [{component, keys}] (null while sealed)
    _vaultStatus: { state: true }, // {initialized, sealed, mode, insecure}
    _cron: { state: true },
    _users: { state: true },
    _orgs: { state: true },     // orgs & teams (docs/auth.md)
    _wsPolicy: { state: true }, // workspace policy-ceiling rows
    _polEdit: { state: true },  // policy-editor drafts, keyed '' (workspace) / org id
    _drafts: { state: true },   // click-through editor drafts, keyed by context
    _matrix: { state: true },   // /access-matrix payload (access-map tab)
    _mapSel: { state: true },   // selected matrix cell {user, tile} → derivation panel
    _authSettings: { state: true },
    _alerts: { state: true }, // {tokenLoginDisabled, hasAdminUser, canDisable}
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
    .alertbar { display: flex; flex-direction: column; gap: 2px; margin: 0 0 10px; }
    .al { padding: 6px 10px; border-radius: 6px; font-size: 12px; color: #fff; }
    .al b { margin-right: 4px; }
    .al.warn { background: #b7791f; }
    .al.crit { background: #c53030; }
    .denied { padding: 20px 14px; }
    .denied code { background: var(--bx-panel-2); border: 1px solid var(--bx-border);
      border-radius: 4px; padding: 0 4px; font: 11.5px var(--bx-mono); }

    h4 { margin: 14px 0 6px; font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
         text-transform: uppercase; color: var(--bx-muted, #8794a1); }
    h4:first-child { margin-top: 0; }
    .cards { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 4px; }
    .stat { background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 6px; padding: 6px 12px; min-width: 74px; }
    .stat .n { font: 700 18px var(--bx-mono, monospace); color: var(--bx-accent, #f5a623); }
    .stat .l { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
               color: var(--bx-muted, #8794a1); }
    .stat.warn .n { color: var(--bx-amber, #f2a71b); }
    .vault-banner { border-radius: 6px; padding: 8px 12px; margin-bottom: 12px;
      font-size: 12.5px; font-weight: 600; }
    .vault-banner.sealed {
      background: var(--bx-red, #e5484d); color: #fff; cursor: pointer;
      font-size: 13.5px; letter-spacing: .02em;
      box-shadow: 0 0 0 1px color-mix(in srgb, var(--bx-red, #e5484d) 60%, #000),
                  0 2px 10px color-mix(in srgb, var(--bx-red, #e5484d) 50%, transparent);
      animation: vault-pulse 1.6s ease-in-out infinite;
    }
    @keyframes vault-pulse { 50% { filter: brightness(1.18); } }
    @media (prefers-reduced-motion: reduce) { .vault-banner.sealed { animation: none; } }
    .vault-banner.warn { background: color-mix(in srgb, var(--bx-amber, #f2a71b) 18%, transparent);
      color: var(--bx-amber, #f2a71b); cursor: pointer;
      border: 1px solid color-mix(in srgb, var(--bx-amber, #f2a71b) 45%, transparent); }
    .vault-banner.ok { background: none; border: 0; padding: 0 2px;
      color: var(--bx-green, #43a047); font-weight: 500; font-size: 11px; }

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
    a.link { color: var(--bx-accent, #f5a623); cursor: pointer; text-decoration: none; }
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
    .code pre.diff > .h { color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 8%, transparent); }
    .code pre.diff > .fh { color: var(--bx-muted, #8794a1); }
    .grouphd { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
      color: var(--bx-muted, #8794a1); padding: 4px 8px; background: var(--bx-panel-2, #f7f8fa); }

    /* highlight.js — dark palette (Atom-One-Dark-ish) scoped to this shadow */
    .hljs-comment, .hljs-quote { color: #7f8896; font-style: italic; }
    .hljs-keyword, .hljs-selector-tag, .hljs-doctag, .hljs-formula { color: #c678dd; }
    .hljs-name, .hljs-section, .hljs-tag, .hljs-deletion { color: #e06c75; }
    .hljs-string, .hljs-regexp, .hljs-addition, .hljs-meta .hljs-string { color: #98c379; }
    .hljs-number, .hljs-literal, .hljs-type, .hljs-attr, .hljs-attribute,
    .hljs-variable, .hljs-template-variable, .hljs-selector-attr,
    .hljs-selector-pseudo, .hljs-selector-class { color: #d19a66; }
    .hljs-title, .hljs-title.function_, .hljs-built_in, .hljs-title.class_ { color: #61afef; }
    .hljs-symbol, .hljs-bullet, .hljs-link, .hljs-meta, .hljs-selector-id { color: #56b6c2; }
    .hljs-emphasis { font-style: italic; }
    .hljs-strong { font-weight: 600; }

    /* ---- runtime ---- */
    .hostcard { display: flex; flex-wrap: wrap; gap: 8px 18px; background: var(--bx-panel-2, #f7f8fa);
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 8px; padding: 10px 14px; margin-bottom: 12px; }
    .hostcard .kv { font-size: 12px; }
    .hostcard .kv b { font-family: var(--bx-mono, monospace); color: var(--bx-accent, #f5a623); }
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
    .state.building { color: var(--bx-accent, #f5a623); }
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

    /* ---- access map (structure + effective-access matrix) ---- */
    .lv { display: inline-flex; align-items: center; justify-content: center;
      width: 16px; height: 16px; border-radius: 4px; font-size: 10px; font-weight: 700;
      font-family: var(--bx-mono, monospace); border: 1px solid transparent; }
    .lv-read { color: #3577c8; background: color-mix(in srgb, #3577c8 14%, transparent);
      border-color: color-mix(in srgb, #3577c8 40%, transparent); }
    .lv-write { color: var(--bx-green, #43a047);
      background: color-mix(in srgb, var(--bx-green, #43a047) 14%, transparent);
      border-color: color-mix(in srgb, var(--bx-green, #43a047) 40%, transparent); }
    .lv-terminal { color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 16%, transparent);
      border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 45%, transparent); }
    .lv-none { color: var(--bx-muted, #8794a1); opacity: .5; }
    .pill.crown { border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      color: var(--bx-accent, #f5a623); }
    .pill.pol { border-color: color-mix(in srgb, var(--bx-red, #e5484d) 45%, transparent);
      color: var(--bx-red, #e5484d); cursor: help; }
    .pill.lv-read, .pill.lv-write, .pill.lv-terminal { width: auto; height: auto; }
    .snode { border: 1px solid var(--bx-border, #e4e8ed); border-left: 3px solid var(--bx-border, #e4e8ed);
      border-radius: 6px; padding: 6px 10px; margin: 6px 0; }
    .snode .shead { font-weight: 600; font-size: 12px; margin-bottom: 3px;
      display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; }
    .snode.ws { border-left-color: var(--bx-muted, #8794a1); }
    .snode.org { border-left-color: var(--bx-accent, #f5a623); }
    .snode.team { border-left-color: var(--bx-green, #43a047); margin-left: 18px; position: relative; }
    .snode.team::before { content: ''; position: absolute; left: -12px; top: 14px;
      width: 9px; border-top: 1px solid var(--bx-border, #e4e8ed); }
    .matrix { border-collapse: collapse; font-size: 11px; }
    .matrix th { padding: 3px 6px; font-size: 10.5px; color: var(--bx-muted, #8794a1);
      font-weight: 600; text-align: center; }
    .matrix .mgrp { padding: 6px 4px 2px; font-size: 10px; font-weight: 700;
      letter-spacing: .07em; text-transform: uppercase; color: var(--bx-muted, #8794a1);
      border-bottom: 1px solid var(--bx-border, #e4e8ed); }
    .matrix .mtile { padding: 2px 10px 2px 4px; font-size: 11px; white-space: nowrap; }
    .matrix .mcell { text-align: center; padding: 2px 5px; border-radius: 4px; }
    .matrix .mcell.has { cursor: pointer; }
    .matrix .mcell.has:hover { background: var(--bx-panel-2, #f7f8fa); }
    .matrix .mcell.msel { outline: 2px solid color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      outline-offset: -2px; }
    .flow-deny { color: var(--bx-red, #e5484d); }
    .flow-allow { color: var(--bx-green, #43a047); }
    .err-pill { color: var(--bx-red, #e5484d); font-size: 11px; }
  `;

  // {id, label} — ids stay URL-safe (no spaces/&) for hash deep-links.
  static TABS = [
    { id: 'overview', label: 'overview' },
    { id: 'runtime', label: 'runtime' },
    { id: 'users', label: 'users' },
    { id: 'orgs', label: 'orgs & teams' },
    { id: 'map', label: 'access map' },
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
    this._err = '';
    this._alerts = [];
    this._denied = false;
    this._rtOpen = new Set();
    this._versions = {};
    this._verOpen = new Set();
    this._schedules = [];
  }

  _setTab(t) {
    this._tab = t;
    try { history.replaceState(null, '', '#' + t); } catch { /* sandboxed */ }
    // Leaving/clicking overview drops any code drill-in (back to the list);
    // _openCode re-sets _codeComp right after calling this to drill in.
    this._codeComp = null;
    if (t === 'runtime') this._loadRuntime();
    if (t === 'map') this._loadMap();
    if (t === 'interfaces') this._loadIfaces();
    if (t === 'backup') this._loadBackup();
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'grants' || e.type === 'reload' || e.type === 'build-ok' || e.type === 'users') this._refresh();
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
      <polyline points=${pts} fill="none" stroke="var(--bx-accent,#f5a623)" stroke-width="1.2"></polyline></svg>`;
  }

  async _refresh() {
    try {
      const [ov, vaults, cron, users, authSettings, vaultStatus, alerts, orgs, wsPolicy] = await Promise.all([
        api('/auth-overview'),
        api('/vaults').catch(() => null), // 503 while the barrier is sealed
        api('/cron/jobs'),
        api('/users').catch(() => ({ users: [] })),
        api('/auth-settings').catch(() => null),
        api('/vault-status').catch(() => null),
        api('/alerts').catch(() => ({ alerts: [] })),
        api('/orgs').catch(() => ({ orgs: [] })),
        api('/policy').catch(() => ({ policy: [] })),
      ]);
      this._ov = ov; this._vaults = vaults; this._cron = cron.jobs ?? [];
      this._alerts = alerts.alerts ?? [];
      this._users = users.users ?? [];
      this._orgs = orgs.orgs ?? [];
      this._wsPolicy = wsPolicy.policy ?? [];
      this._authSettings = authSettings; this._vaultStatus = vaultStatus;
      this._err = ''; this._denied = false;
      if (this._tab === 'map') this._loadMap(true); // keep the matrix current
    } catch (e) {
      if (String(e.message).includes('admin')) this._denied = true;
      else this._err = String(e.message ?? e);
    }
  }

  // ---- vault ----
  // The admin console never reads secret values back — they're private to the
  // owning element (the vault lockdown). It can only list keys and set/rotate.
  async _setSecret(comp, key, value) {
    await api(`/vault/${comp}/${encodeURIComponent(key)}`,
      { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) });
    this._refresh();
  }
  async _delSecret(comp, key) {
    if (!confirm(`Delete secret ${comp} / ${key}?`)) return;
    await api(`/vault/${comp}/${encodeURIComponent(key)}`, { method: 'DELETE' });
    this._refresh();
  }

  // ---- barrier (seal state / unseal / passphrase) ----
  async _unseal(pass) {
    try {
      await api('/vault-unseal', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ passphrase: pass }) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }
  async _sealVault() {
    if (!confirm('Seal the vault? Encrypted resources unmount and stateful components stop until an admin unseals again.')) return;
    try { await api('/vault-seal', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }); this._err = ''; }
    catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }
  async _rekeyVault(current, nw) {
    try {
      await api('/vault-rekey', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current, new: nw }) });
      this._err = '';
      alert('Passphrase changed (data key unchanged — nothing re-encrypted).');
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }

  _barrierView() {
    const st = this._vaultStatus;
    if (!st) return nothing;
    const badge = {
      unsealed:     ['unsealed — encryption at rest active', 'var(--bx-green, #43a047)'],
      sealed:       ['sealed — encrypted and locked', 'var(--bx-amber, #f2a71b)'],
      unconfigured: ['unconfigured — no passphrase set, secret storage refused', 'var(--bx-red, #e5484d)'],
      plaintext:    ['plaintext — NO encryption at rest (dev mode)', 'var(--bx-red, #e5484d)'],
    }[st.mode] ?? [st.mode, 'var(--bx-muted, #8794a1)'];
    const firstTime = st.mode === 'unconfigured' || st.mode === 'plaintext';
    return html`
      <h4>encryption barrier</h4>
      <p style="margin:0 0 8px"><span class="dot" style="background:${badge[1]}"></span>${badge[0]}</p>

      ${st.mode === 'sealed' ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.pass.value) this._unseal(f.pass.value); f.reset(); }}>
          <input name="pass" type="password" placeholder="vault passphrase" size="24"
            autocomplete="off" required>
          <button class="act go">unseal</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Encrypted resources and secrets
          come back once unsealed. Also works from a terminal: <span class="mono">bx vault unseal</span>.</p>` : nothing}

      ${firstTime ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.pass.value !== f.confirm.value) { this._err = 'passphrases do not match'; return; }
            this._unseal(f.pass.value); f.reset(); }}>
          <input name="pass" type="password" placeholder="new vault passphrase" size="20"
            autocomplete="new-password" required>
          <input name="confirm" type="password" placeholder="repeat" size="12"
            autocomplete="new-password" required>
          <button class="act go">${st.mode === 'plaintext' ? 'encrypt now' : 'set passphrase & unseal'}</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Creates the barrier and encrypts
          existing secrets. <b>The passphrase cannot be recovered</b> — losing it loses the data.
          To have xbind unseal itself on boot, put <span class="mono">XBIN_VAULT_PASSPHRASE</span>
          in <span class="mono">/etc/xbin/xbin.env</span> (mode 600).</p>` : nothing}

      ${st.mode === 'unsealed' ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.nw.value !== f.confirm.value) { this._err = 'new passphrases do not match'; return; }
            this._rekeyVault(f.cur.value, f.nw.value); f.reset(); }}>
          <input name="cur" type="password" placeholder="current passphrase" size="17"
            autocomplete="off" required>
          <input name="nw" type="password" placeholder="new passphrase" size="15"
            autocomplete="new-password" required>
          <input name="confirm" type="password" placeholder="repeat" size="10"
            autocomplete="new-password" required>
          <button class="act">change passphrase</button>
          <button class="act rm" type="button" @click=${() => this._sealVault()}>seal now</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Changing the passphrase re-wraps the
          data key — nothing is re-encrypted. If auto-unseal is configured, update
          <span class="mono">/etc/xbin/xbin.env</span> to match.</p>` : nothing}`;
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

  // ---- code & history (a drill-in from the overview; no separate tab) ----
  async _openCode(comp) {
    this._setTab('overview');
    this._codeComp = comp; this._codeFile = null; this._codeDiff = null; this._codeMode = 'file';
    await this._loadCode();
    const files = this._codeTree?.files ?? [];
    const def = files.find((f) => f.path === 'index.html') || files.find((f) => f.path === 'xbin.json') || files[0];
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
    if (!this._codeComp) return this._overview(); // reached only defensively; the overview is the picker
    const tree = this._codeTree?.files ?? [];
    const log = this._codeLog?.commits ?? [];
    const noRepo = this._codeLog?.repo === false;
    return html`
      <div class="hd">
        <a class="link" @click=${() => { this._codeComp = null; }}>← components</a>
        <span class="path">${this._codeComp}</span>
        ${this._codeLog?.remote ? html`<span class="muted" style="font-size:11px" title="git remote (origin)">${this._codeLog.remote.replace(/^https:\/\/|\.git$/g, '')}</span>` : nothing}
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
      <b>No admin access.</b> This tile needs the <code>xbin:admin</code> grant.
      Approve <span class="mono">tiles/admin → xbin : admin</span> in the grants
      panel, or run <code>bx grant tiles/admin xbin:admin</code>.
      See <a href="/docs/auth.md" target="_blank">docs/auth.md</a>.</div>`;
    const tab = this._tab;
    return html`
      ${(this._alerts || []).length ? html`<div class="alertbar">
        ${this._alerts.map((a) => html`<div class="al ${a.level}">
          <b>${a.level === 'crit' ? '\u26A0' : '\u26A1'}</b> ${a.message}</div>`)}
      </div>` : nothing}
      <div class="tabs">
        ${BxAdmin.TABS.map((t) => html`
          <button class=${t.id === tab ? 'on' : ''} @click=${() => this._setTab(t.id)}>${t.label}</button>`)}
      </div>
      <div class="body">
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
        ${tab === 'users' ? this._usersView()
          : tab === 'orgs' ? this._orgsView()
          : tab === 'map' ? this._mapView()
          : tab === 'overview' ? (this._codeComp ? this._codeView() : this._overview())
          : tab === 'runtime' ? this._runtimeView()
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

  // Terminal secret-mask guards (docs/isolation.md): mount guard = seccomp
  // (masks can't be umounted), read guard = Landlock (secret files can't be
  // read even if a mask is peeled). Green when the kernel supports each.
  _guardStatus(p) {
    p = p || {};
    const mark = (on) => (on ? '✓' : '✗');
    const land = p.landlock ? `✓ (ABI ${p.landlockAbi})` : '✗';
    return html`<span title="seccomp mount guard · Landlock read guard"
      >mount ${mark(p.seccomp)} · read ${land}</span>`;
  }

  _runtimeView() {
    const rt = this._rt; if (!rt) return html`<span class="muted">loading…</span>`;
    const h = rt.host || {};
    const kv = (label, val) => html`<div class="kv"><span>${label}</span> <b>${val}</b></div>`;
    return html`
      <div class="hostcard">
        ${kv('xbind', h.version)}
        ${kv('kernel', h.kernel || '—')}
        ${kv('pid', h.pid)}
        ${kv('euid', h.uid)}
        ${kv('cpus', h.numCPU)}
        ${kv('goroutines', h.goroutines)}
        ${kv('heap', this._fmtBytes((h.heapMB || 0) * 1e6))}
        ${kv('uptime', this._fmtDur(h.uptimeSec))}
        ${kv('isolation', h.isolate ? 'on (tier 3)' : (h.scopeUids ? 'uids (tier 2)' : 'off (tier 1)'))}
        ${h.isolate ? kv('rootfs', h.rootfs) : nothing}
        ${h.isolate ? kv('terminal guard', this._guardStatus(h.protections)) : nothing}
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

  // Vault banner at the top of the overview: unmissable when the barrier is
  // sealed/unconfigured (stateful components are held), quiet when healthy.
  _vaultBanner() {
    const st = this._vaultStatus;
    if (!st) return nothing;
    const goVault = () => this._setTab('vault');
    if (st.mode === 'sealed') {
      return html`<div class="vault-banner sealed" @click=${goVault}
        title="open the vault tab to unseal">
        🔒 VAULT SEALED — encrypted resources are unmounted and stateful components are HELD.
        Click to unseal.</div>`;
    }
    if (st.mode === 'unconfigured') {
      return html`<div class="vault-banner sealed" @click=${goVault}
        title="open the vault tab to set a passphrase">
        🔒 VAULT UNCONFIGURED — secret & resource storage is refused until a passphrase is set.
        Click to set one.</div>`;
    }
    if (st.mode === 'plaintext') {
      return html`<div class="vault-banner warn" @click=${goVault}>
        ⚠ vault: plaintext at rest (dev mode) — click to encrypt.</div>`;
    }
    return html`<div class="vault-banner ok">vault unsealed — encryption at rest active</div>`;
  }

  _overview() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const c = ov.counts;
    return html`
      ${this._vaultBanner()}
      <div class="cards">
        <div class="stat"><div class="n">${c.components}</div><div class="l">components</div></div>
        <div class="stat"><div class="n">${c.exposed}</div><div class="l">expose APIs</div></div>
        <div class="stat"><div class="n">${c.grants}</div><div class="l">grants</div></div>
        <div class="stat ${c.pending ? 'warn' : ''}"><div class="n">${c.pending}</div><div class="l">pending</div></div>
      </div>
      <h4>principals</h4>
      <table>
        <tr><th>component</th><th>runtime</th><th>exposes</th><th>uses</th><th>vault</th><th>lifecycle</th></tr>
        ${ov.components.filter((k) => !this._isOffloaded(k)).map((k) => html`<tr>
          <td class="mono"><a class="link" @click=${() => this._openCode(k.path)} title="view code & history">${k.path}</a>${k.manifestError ? html` <span class="st-failed" title=${k.manifestError}>⚠</span>` : nothing}</td>
          <td class="muted">${k.runtime || 'static'}</td>
          <td>${k.roles ? Object.keys(k.roles).map((r) => html`<span class="pill">${r}</span>`) : html`<span class="muted">—</span>`}</td>
          <td>${(k.uses ?? []).map((u) => html`<span class="pill">${u.target}:${u.role}</span>`)}</td>
          <td>${k.hasVault ? '🔑' : ''}</td>
          <td>${this._lifecycleCell(k)}</td>
        </tr>`)}
      </table>
      ${this._offloadedSection(ov)}`;
  }

  _isOffloaded(k) { return k.state === 'offloaded' || k.state === 'offloaded-full'; }

  // Offloaded (archived) components live in their own section — they're not
  // running, so they'd only clutter the principals table. Restore from Backup.
  _offloadedSection(ov) {
    const off = (ov.components ?? []).filter((k) => this._isOffloaded(k));
    if (!off.length) return nothing;
    return html`
      <h4>offloaded <span class="muted" style="font-weight:400;text-transform:none;letter-spacing:0">— archived, not running</span></h4>
      <table>
        <tr><th>component</th><th>state</th><th></th></tr>
        ${off.map((k) => html`<tr>
          <td class="mono">${k.path}</td>
          <td><span class="pill st-failed">${k.state}</span></td>
          <td style="text-align:right"><a class="link" @click=${() => { this._tab = 'backup'; }}>restore in Backup →</a></td>
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
    const sealedOff = this._vaults == null && !!this._vaultStatus?.sealed;
    const vs = this._vaults ?? [];
    return html`
      ${this._barrierView()}
      ${sealedOff ? html`<h4>secrets</h4><span class="muted">unavailable while sealed — unseal above to browse and edit.</span>` : nothing}
      ${!sealedOff && vs.length === 0 ? html`<h4>secrets</h4><span class="muted">no vaults hold secrets yet — set one with
        <span class="mono">bx vault set &lt;component&gt; &lt;key&gt;</span> or below.</span>` : nothing}
      ${vs.length && !sealedOff ? html`<p class="muted" style="font-size:11px">
        Secret <b>values are private to the element that owns them</b> — the admin
        console can list and set/rotate secrets but can't read them back.</p>` : nothing}
      ${vs.map((v) => html`
        <h4>${v.component}</h4>
        <table>
          ${v.keys.map((k) => html`<tr>
              <td class="mono" style="width:30%">${k}</td>
              <td class="secret">••••••••</td>
              <td style="text-align:right; white-space:nowrap">
                <button class="act" @click=${() => { const nv = prompt(`Set a new value for ${v.component} / ${k} (can't read the current one)`); if (nv) this._setSecret(v.component, k, nv); }}>set</button>
                <button class="act rm" @click=${() => this._delSecret(v.component, k)}>del</button>
              </td></tr>`)}
        </table>`)}
      ${sealedOff ? nothing : html`<form class="inline" @submit=${(e) => { e.preventDefault();
          const f = e.target;
          if (f.comp.value && f.key.value) this._setSecret(f.comp.value.trim(), f.key.value.trim(), f.val.value);
          f.reset(); }}>
        <input name="comp" placeholder="component" size="16" list="admin-comps">
        <input name="key" placeholder="key" size="12">
        <input name="val" placeholder="value" size="18" type="password">
        <button class="act go">set secret</button>
      </form>`}
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
        <input name="target" placeholder="apps/other or res:…/… or xbin" size="20">
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
  // Replace a multi slot's whole set (bx-multiselect emits the full selection).
  async _bindSetMulti(component, slot, providers) {
    try {
      await api('/bindings', {
        method: providers.length ? 'POST' : 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(providers.length ? { component, slot, providers } : { component, slot }),
      });
      await this._loadIfaces();
    } catch (e) { this._err = String(e.message ?? e); }
  }
  _ifacesView() {
    const d = this._ifaces;
    if (!d) return html`<div class="muted">loading…</div>`;
    const comps = d.components || [];
    const instances = d.instances || {};
    // providers of each kind, carrying their service contract — an
    // instances-provide expands to one option per registered instance
    // (provider#instance; a subslot presents itself like any other provider,
    // so non-subslot-aware tiles bind to it the same way).
    const providersByKind = {};
    for (const c of comps)
      for (const def of Object.values(c.provides || {})) {
        const list = (providersByKind[def.kind] ||= []);
        if (def.instances) {
          for (const id of Object.keys(instances[c.component] || {}).sort())
            list.push({ ref: `${c.component}#${id}`, service: def.service });
        } else {
          list.push({ ref: c.component, service: def.service });
        }
      }
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
        <tr><th>tile</th><th>slot</th><th>kind</th><th>instances</th></tr>
        ${comps.flatMap((c) => Object.entries(c.provides || {}).map(([slot, def]) => html`<tr>
          <td class="mono">${c.component}</td><td>${slot}</td>
          <td><span class="pill">${def.kind}${def.service ? ':' + def.service : ''}</span></td>
          <td class="mono">${def.instances
            ? (Object.keys(instances[c.component] || {}).sort().map((id) => html`<span class="pill">#${id}</span>`) || nothing)
            : html`<span class="muted">—</span>`}</td></tr>`))}
        ${Object.keys(providersByKind).length === 0 ? html`<tr><td class="muted" colspan="4">no provider tiles</td></tr>` : nothing}
      </table>
      <h3>Requests → binding</h3>
      <table class="tbl">
        <tr><th>component</th><th>slot</th><th>kind</th><th>bound to</th></tr>
        ${requests.map((r) => {
          const raw = d.bindings?.[r.comp]?.[r.slot];
          const bound = [].concat(raw ?? []);           // string | array → array
          const own = (p) => p === r.comp || p.startsWith(r.comp + '#');
          // http slots bind within their service contract (same filter the
          // backend's bindOptions + bind validation apply) — an s3 slot must
          // not offer an openai provider.
          const opts = [...(builtins[r.def.kind] || []),
            ...(providersByKind[r.def.kind] || [])
              .filter((e) => !own(e.ref) &&
                (r.def.kind !== 'http' || !r.def.service || e.service === r.def.service))
              .map((e) => e.ref)];
          const kind = html`<span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}${r.def.multi ? ' ×N' : ''}</span>`;
          if (r.def.multi) {
            // Multi-input slot: a dropdown checklist over the same options.
            return html`<tr>
              <td class="mono">${r.comp}</td><td>${r.slot}</td><td>${kind}</td>
              <td><bx-multiselect .options=${opts} .selected=${bound} placeholder="— unbound —"
                  @change=${(e) => this._bindSetMulti(r.comp, r.slot, e.detail.selected)}></bx-multiselect></td></tr>`;
          }
          return html`<tr>
            <td class="mono">${r.comp}</td><td>${r.slot}</td><td>${kind}</td>
            <td><select @change=${(e) => this._bindSet(r.comp, r.slot, e.target.value)}>
              <option value="" ?selected=${bound.length === 0}>— unbound —</option>
              ${opts.map((p) => html`<option value=${p} ?selected=${bound[0] === p}>${p}</option>`)}
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
      const r = await xbin.fetch('/api/xbin/restore', {
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
  // Tile access is per-path levels (read < write < terminal, D16), edited
  // with the click-through row editors (_tilesEditor/_patternsEditor).
  async _createUser(f) {
    try {
      await api('/users', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: f.id.value.trim(), name: f.name.value.trim(), role: f.role.value,
          termApi: f.termApi.checked, termNet: f.termNet.checked,
          password: f.password.value }) });
      f.reset(); this._err = ''; this._refresh(); // only clear the form on success
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _patchUser(id, patch) {
    await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch) });
    this._refresh();
  }
  async _resetPw(id) {
    const pw = prompt(`New password for ${id} (min 8 chars):`);
    if (!pw) return;
    if (pw.length < 8) { this._err = 'password too short (min 8 characters)'; return; }
    try {
      await this._patchUser(id, { password: pw });
      this._err = `password reset for ${id}`;
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _delUser(id) {
    if (!confirm(`Delete user ${id}? Their sessions are revoked immediately.`)) return;
    await api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
    this._refresh();
  }

  async _setTokenLogin(disabled) {
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tokenLoginDisabled: disabled }) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
    this.requestUpdate();
  }

  _signInSecurityView() {
    const s = this._authSettings;
    if (!s) return nothing;
    const off = !!s.tokenLoginDisabled;      // token login currently OFF
    // canDisable is computed server-side (same predicate as the PATCH guard):
    // an admin user exists AND this session is a signed-in admin user — the
    // human behind the frame token, not the tile's own grants.
    const canDisable = !!s.canDisable;       // re-enabling is always allowed
    return html`
      <h4>sign-in security</h4>
      <label style="display:flex; gap:8px; align-items:flex-start; font-size:12px; max-width:52ch">
        <input type="checkbox" .checked=${off} ?disabled=${!off && !canDisable}
          @change=${(e) => this._setTokenLogin(e.target.checked)}>
        <span>
          <b>Disable token-URL login.</b> Turns off the bootstrap
          <span class="mono">/login?token=…</span> URL and the owner-token cookie —
          everyone signs in with an account. The <span class="mono">bx</span> CLI
          token (<span class="mono">Authorization: Bearer</span>) is unaffected.
          ${off ? html`<br><span class="muted">Token login is off. Uncheck to allow it again.</span>`
            : !s.hasAdminUser ? html`<br><span class="muted">Create an admin user first.</span>`
            : !canDisable ? html`<br><span class="muted">Sign in as an admin user (not the root token) to enable this.</span>`
            : nothing}
        </span>
      </label>
      <div style="margin-top:10px; font-size:12px; max-width:52ch">
        <button class="act" @click=${() => this._rotateToken()}>rotate owner token</button>
        <span class="muted"> Replaces <span class="mono">.xbin/token</span> — the old
        token (and any leaked copy, e.g. in pre-2026-07-09 agent transcripts)
        stops working immediately. Update host-side
        <span class="mono">XBIN_TOKEN</span> afterwards.</span>
      </div>`;
  }

  async _rotateToken() {
    if (!confirm('Rotate the owner token? The current token stops working immediately (bearer + cookie). Host-side bx/automation must switch to the new one.')) return;
    try {
      const d = await api('/auth-rotate-token', { method: 'POST' });
      prompt('New owner token — copy it now (also written to <workspace>/.xbin/token):', d.token);
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // _userOrgPills summarizes a user's org/team memberships for the users table.
  _userOrgPills(uid) {
    const out = [];
    for (const o of (this._orgs ?? [])) {
      if ((o.admins ?? []).includes(uid)) out.push(`${o.id}·admin`);
      else if ((o.members ?? []).includes(uid)) out.push(o.id);
      for (const t of (o.teams ?? [])) {
        if ((t.members ?? []).includes(uid)) out.push(`${o.id}/${t.id}`);
      }
    }
    return out;
  }

  _usersView() {
    const users = this._users ?? [];
    return html`
      ${this._targetDatalist()}
      <h4>users</h4>
      <table>
        <tr><th>id</th><th>name</th><th>role</th><th>access</th><th></th></tr>
        ${users.length ? users.map((u) => {
          const tilesKey = `user:${u.id}:tiles`;
          const createKey = `user:${u.id}:create`;
          return html`<tr>
          <td class="mono">${u.id}</td>
          <td>${u.name}</td>
          <td><span class="pill">${u.role}</span></td>
          <td>${u.role === 'admin' ? html`<span class="muted">all</span>`
            : html`${Object.entries(u.tiles || {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
              ${(u.canCreate || []).map((c) => html`<span class="pill">create·${c}</span>`)}
              ${u.termApi ? html`<span class="pill">term-api</span>` : nothing}
              ${u.termNet ? html`<span class="pill">term-net</span>` : nothing}
              ${!Object.keys(u.tiles || {}).length && !(u.canCreate || []).length
                ? html`<span class="muted">none</span>` : nothing}`}
            ${this._userOrgPills(u.id).map((p) => html`<span class="pill" style="opacity:.75">${p}</span>`)}</td>
          <td style="text-align:right; white-space:nowrap">
            <button class="act" @click=${() => this._patchUser(u.id, { role: u.role === 'admin' ? 'user' : 'admin' })}>${u.role === 'admin' ? 'demote' : 'make admin'}</button>
            ${u.role === 'admin' ? nothing : html`
              <button class="act" @click=${() => this._toggleDraft(tilesKey,
                () => Object.entries(u.tiles ?? {}).map(([target, level]) => ({ target, level })))}>tiles…</button>
              <button class="act" @click=${() => this._toggleDraft(createKey, () => [...(u.canCreate ?? [])])}>create…</button>
              <button class="act" @click=${() => this._patchUser(u.id, { termApi: !u.termApi })}>${u.termApi ? '− api' : '+ api'}</button>
              <button class="act" @click=${() => this._patchUser(u.id, { termNet: !u.termNet })}>${u.termNet ? '− net' : '+ net'}</button>`}
            <button class="act" @click=${() => this._resetPw(u.id)}>pw</button>
            <button class="act rm" @click=${() => this._delUser(u.id)}>del</button>
          </td>
        </tr>
        ${this._draft(tilesKey) ? html`<tr><td colspan="5">
          ${this._tilesEditor(tilesKey, (tiles) => this._orgAPI('PATCH', `/users/${encodeURIComponent(u.id)}`, { tiles }))}
        </td></tr>` : nothing}
        ${this._draft(createKey) ? html`<tr><td colspan="5">
          ${this._patternsEditor(createKey, (canCreate) => this._orgAPI('PATCH', `/users/${encodeURIComponent(u.id)}`, { canCreate }))}
        </td></tr>` : nothing}`;
        }) : html`<tr><td class="muted" colspan="5">no users — the root token is the only admin. Add one below.</td></tr>`}
      </table>

      <h4>add user</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createUser(e.target); }}>
        <input name="id" placeholder="username" size="12" required>
        <input name="name" placeholder="display name" size="14">
        <select name="role"><option value="user">user</option><option value="admin">admin</option></select>
        <label class="muted" style="font-size:11px"><input type="checkbox" name="termApi"> term-api</label>
        <label class="muted" style="font-size:11px"><input type="checkbox" name="termNet"> term-net</label>
        <input name="password" type="password" placeholder="password (min 8)" size="12" minlength="8" required>
        <button class="act go">create</button>
      </form>
      <p class="muted" style="font-size:11px;margin-top:6px">
        Grant tile access with the <b>tiles…</b> editor after creating (levels:
        <b>read</b> = see the tile + its source · <b>write</b> = edit/drive it ·
        <b>terminal</b> = a root shell in its directory — trusted users only).
        <b>create…</b> sets namespaces the user may scaffold tiles under (they get
        terminal on what they create). A non-admin's terminals run restricted:
        no live tile-API token without <b>term-api</b>, no internet egress
        without <b>term-net</b>.</p>

      ${this._signInSecurityView()}`;
  }

  // ---- click-through editors (shared by the users + orgs tabs) ----
  // Workspaces are small, so everything about permissions is enumerable and
  // clickable: people are picked from chip dropdowns, tile targets from a
  // datalist of real paths + patterns — no free-text specs to mistype.

  // _peoplePicker: chip dropdown over the workspace's users. onChange fires
  // with the full selection on every toggle (immediate save — click-through).
  _peoplePicker(selected, onChange, only = null) {
    const ids = only ?? (this._users ?? []).map((u) => u.id);
    const opts = ids.map((id) => {
      const u = (this._users ?? []).find((x) => x.id === id);
      return { value: id, label: u?.name && u.name !== id ? `${id} — ${u.name}` : id };
    });
    return html`<bx-multiselect style="min-width:120px" .options=${opts} .selected=${selected ?? []}
      placeholder="— nobody —" @change=${(e) => onChange(e.detail.selected)}></bx-multiselect>`;
  }

  // Draft plumbing for the row editors, keyed by a context id
  // ("user:bob:tiles", "team:sales/backend:create", …).
  _draft(k) { return this._drafts?.[k]; }
  _setDraft(k, v) { this._drafts = { ...(this._drafts ?? {}), [k]: v }; }
  _dropDraft(k) { const d = { ...(this._drafts ?? {}) }; delete d[k]; this._drafts = d; }
  _toggleDraft(k, seed) { this._draft(k) ? this._dropDraft(k) : this._setDraft(k, seed()); }

  // Target suggestions: every real component path, a pattern per group
  // (top dirs + org namespaces), and * — rendered once per view as a
  // <datalist> the row editors' inputs attach to.
  _targetOptions() {
    const comps = (this._ov?.components ?? []).map((c) => c.path)
      .filter((p) => p !== 'root' && p !== 'shell');
    const pats = new Set(['*']);
    for (const p of comps) {
      const s = p.split('/');
      if (s[0] === 'o' && s[1]) pats.add(`o/${s[1]}/*`);
      else if (s.length > 2 && s[1] === 'o' && s[2]) pats.add(`${s[0]}/o/${s[2]}/*`);
      else if (s.length > 1) pats.add(`${s[0]}/*`);
    }
    for (const o of (this._orgs ?? [])) { pats.add(`apps/o/${o.id}/*`); pats.add(`o/${o.id}/*`); }
    return [...comps.sort(), ...[...pats].sort()];
  }
  _targetDatalist() {
    return html`<datalist id="tile-targets">
      ${this._targetOptions().map((t) => html`<option value=${t}></option>`)}
    </datalist>`;
  }

  // _orgOfJS mirrors the server's positional org binding (o/<org>/… or
  // <dir>/o/<org>/…) for display grouping and inert-pattern warnings.
  _orgOfJS(path) {
    const s = String(path).split('/');
    if (s.length >= 2 && s[0] === 'o' && s[1]) return s[1];
    if (s.length >= 3 && s[1] === 'o' && s[2]) return s[2];
    return null;
  }

  // A team pattern that can never match its own org's paths is inert (the
  // evaluation clamp) — warn while editing, same rule as bx doctor.
  _teamPatInert(org, pat) {
    if (!pat || pat === '*') return false;
    const p = pat.endsWith('/*') ? pat.slice(0, -2) : pat;
    const pOrg = this._orgOfJS(p);
    if (pOrg) return pOrg !== org;
    if (p === pat) return true; // exact non-org path
    const segs = p.split('/');
    return !(segs.length === 1 || (segs.length === 2 && segs[1] === 'o'));
  }

  // _tilesEditor: rows of [target (datalist)] [level] [×] editing a
  // pattern→level map; save calls onSave(map). orgID adds the inert warning.
  _tilesEditor(ctx, onSave, orgID = null) {
    const d = this._draft(ctx) ?? [];
    const upd = (i, patch) => this._setDraft(ctx, d.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    return html`
      <div style="padding:6px 8px; background:var(--bx-panel-2,#f7f8fa); border-radius:6px">
        ${d.map((r, i) => html`<div style="display:flex; gap:5px; align-items:center; margin-bottom:4px">
          <input list="tile-targets" size="26" placeholder="path, prefix/* or *" .value=${r.target}
            @input=${(e) => upd(i, { target: e.target.value })}>
          <select @change=${(e) => upd(i, { level: e.target.value })}>
            ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${r.level === l}>${l}</option>`)}
          </select>
          ${orgID && this._teamPatInert(orgID, r.target)
            ? html`<span style="color:var(--bx-red,#e5484d); font-size:10.5px"
                title="team grants only apply inside org ${orgID} — this entry can never match one of its paths">⚠ inert</span>` : nothing}
          <button class="act rm" title="remove entry" @click=${() => this._setDraft(ctx, d.filter((_, j) => j !== i))}>✕</button>
        </div>`)}
        <div style="display:flex; gap:5px; align-items:center">
          <button class="act" @click=${() => this._setDraft(ctx, [...d, { target: '', level: 'write' }])}>+ entry</button>
          <button class="act go" @click=${async () => {
            const tiles = {};
            for (const r of d) if (r.target.trim()) tiles[r.target.trim()] = r.level;
            await onSave(tiles);
            if (!this._err) this._dropDraft(ctx);
          }}>save</button>
          <button class="act" @click=${() => this._dropDraft(ctx)}>cancel</button>
          <span class="muted" style="font-size:10.5px">read = see it · write = use/edit · terminal = root shell on it</span>
        </div>
      </div>`;
  }

  // _patternsEditor: same, for plain pattern lists (canCreate).
  _patternsEditor(ctx, onSave, orgID = null) {
    const d = this._draft(ctx) ?? [];
    const upd = (i, v) => this._setDraft(ctx, d.map((r, j) => (j === i ? v : r)));
    return html`
      <div style="padding:6px 8px; background:var(--bx-panel-2,#f7f8fa); border-radius:6px">
        ${d.map((r, i) => html`<div style="display:flex; gap:5px; align-items:center; margin-bottom:4px">
          <input list="tile-targets" size="26" placeholder="prefix/* (create namespace)" .value=${r}
            @input=${(e) => upd(i, e.target.value)}>
          ${orgID && this._teamPatInert(orgID, r)
            ? html`<span style="color:var(--bx-red,#e5484d); font-size:10.5px"
                title="create grants only apply inside org ${orgID}">⚠ inert</span>` : nothing}
          <button class="act rm" @click=${() => this._setDraft(ctx, d.filter((_, j) => j !== i))}>✕</button>
        </div>`)}
        <div style="display:flex; gap:5px; align-items:center">
          <button class="act" @click=${() => this._setDraft(ctx, [...d, ''])}>+ pattern</button>
          <button class="act go" @click=${async () => {
            await onSave(d.map((s) => s.trim()).filter(Boolean));
            if (!this._err) this._dropDraft(ctx);
          }}>save</button>
          <button class="act" @click=${() => this._dropDraft(ctx)}>cancel</button>
          <span class="muted" style="font-size:10.5px">creating a tile auto-grants the creator terminal on it</span>
        </div>
      </div>`;
  }

  // ---- access map: how permissions actually apply (visual) ----
  async _loadMap(force = false) {
    if (this._matrix && !force) return;
    try { this._matrix = await api('/access-matrix'); } catch (e) { this._err = String(e.message ?? e); }
  }

  _lvChip(level) {
    const s = { read: 'r', write: 'w', terminal: 't' }[level] ?? '·';
    return html`<span class="lv lv-${level || 'none'}" title=${level || 'no access'}>${s}</span>`;
  }

  _srcLabel(src) {
    const [kind, ...rest] = String(src).split(':');
    switch (kind) {
      case 'admin': return 'workspace admin';
      case 'org-admin': return `org admin of ${rest[0]}`;
      case 'direct': return html`own entry <span class="mono">${rest.join(':')}</span>`;
      case 'team': return html`team <span class="mono">${rest[0]}</span> · <span class="mono">${rest.slice(1).join(':')}</span>`;
      case 'base': return html`org <span class="mono">${rest[0]}</span> base permission`;
      default: return src;
    }
  }

  _structureView() {
    const users = this._users ?? [];
    const orgs = this._orgs ?? [];
    const inOrg = new Set(orgs.flatMap((o) => [...(o.admins ?? []), ...(o.members ?? [])]));
    const wsAdmins = users.filter((u) => u.role === 'admin').map((u) => u.id);
    const outside = users.filter((u) => u.role !== 'admin' && !inOrg.has(u.id)).map((u) => u.id);
    const person = (id, crown) => html`<span class="pill ${crown ? 'crown' : ''}">${crown ? '★ ' : ''}${id}</span>`;
    return html`
      <div class="snode ws">
        <div class="shead">workspace</div>
        <div>admins: ${wsAdmins.length ? wsAdmins.map((a) => person(a, true)) : html`<span class="muted">root token only</span>`}
          ${this._wsPolicy?.length ? html`<span class="pill pol" title=${this._wsPolicy.map((r) => `tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}`).join('\n')}>⛔ ${this._wsPolicy.length} policy row(s)</span>` : nothing}
        </div>
        ${outside.length ? html`<div style="margin-top:3px"><span class="muted" style="font-size:11px">in no org:</span> ${outside.map((u) => person(u, false))}</div>` : nothing}
      </div>
      ${orgs.map((o) => html`
        <div class="snode org">
          <div class="shead"><span class="mono">o/${o.id}</span>${o.name !== o.id ? html` <span class="muted">${o.name}</span>` : nothing}
            ${o.basePermission ? html`<span class="pill lv-${o.basePermission}" title="every member's floor on org tiles">base: ${o.basePermission}</span>` : nothing}
            ${o.policy?.length ? html`<span class="pill pol" title=${o.policy.map((r) => `tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}`).join('\n')}>⛔ ${o.policy.length} policy row(s)</span>` : nothing}
          </div>
          <div>${(o.admins ?? []).map((a) => person(a, true))}
               ${(o.members ?? []).filter((m) => !(o.admins ?? []).includes(m)).map((m) => person(m, false))}
               ${!(o.admins ?? []).length && !(o.members ?? []).length ? html`<span class="muted">no people</span>` : nothing}</div>
          ${(o.teams ?? []).map((t) => html`
            <div class="snode team">
              <div class="shead">${t.id}
                <span class="muted" style="font-weight:400">· created tiles → ${t.newTiles}</span>
                ${t.termApi ? html`<span class="pill">term-api</span>` : nothing}
                ${t.termNet ? html`<span class="pill">term-net</span>` : nothing}</div>
              <div>${(t.members ?? []).length ? (t.members ?? []).map((m) => person(m, false)) : html`<span class="muted">no members</span>`}</div>
              <div>${Object.entries(t.tiles ?? {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
                   ${(t.canCreate ?? []).map((c) => html`<span class="pill">create ${c}</span>`)}</div>
            </div>`)}
        </div>`)}`;
  }

  _mapDetail() {
    const s = this._mapSel;
    const c = s && this._matrix?.cells?.[s.user]?.[s.tile];
    if (!c) return nothing;
    return html`
      <div style="margin-top:8px; padding:8px 10px; border:1px solid var(--bx-border,#e4e8ed); border-radius:6px">
        <span class="mono">${s.user}</span> on <span class="mono">${s.tile}</span> →
        ${this._lvChip(c.level)} <b>${c.level}</b>
        <table style="margin-top:5px">
          ${c.via.map((v, i) => html`<tr style=${i === 0 ? '' : 'opacity:.65'}>
            <td style="white-space:nowrap">${this._lvChip(v.level)} ${v.level}</td>
            <td>${this._srcLabel(v.source)}</td>
            <td class="muted" style="font-size:10.5px">${i === 0 ? '← effective (highest wins)' : 'unioned'}</td>
          </tr>`)}
        </table>
      </div>`;
  }

  _mapView() {
    const m = this._matrix;
    const cols = (m?.users ?? []).filter((u) => u.role !== 'admin');
    const admins = (m?.users ?? []).filter((u) => u.role === 'admin').map((u) => u.id);
    // Tiles grouped like the shell sidebar: per-org, then top-level dirs.
    const groups = new Map();
    for (const tile of (m?.tiles ?? [])) {
      const org = this._orgOfJS(tile);
      const key = org ? `o/${org}` : (tile.includes('/') ? tile.split('/')[0] : 'workspace');
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(tile);
    }
    const grouped = [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
    return html`
      <h4>structure</h4>
      <p class="muted" style="font-size:11px; max-width:64ch; margin-top:2px">
        Who is where: ★ = admin of that box. Level pills on teams are their
        grants (union — the highest matching source wins per tile); ⛔ marks a
        policy ceiling on what those tiles may be granted (hover for rows).</p>
      ${this._structureView()}

      <h4 style="margin-top:14px">effective access</h4>
      <p class="muted" style="font-size:11px; max-width:64ch; margin-top:2px">
        The resolved model, straight from the server: what each user can do on
        each tile. <span class="lv lv-read">r</span> read ·
        <span class="lv lv-write">w</span> write ·
        <span class="lv lv-terminal">t</span> terminal (root shell) ·
        <span class="lv lv-none">·</span> none. Click a cell to see WHY.
        Workspace admins (${admins.length ? admins.join(', ') : 'root token only'})
        hold terminal everywhere and are omitted; chrome (root/shell) is always
        viewable and outside the model.</p>
      ${!m ? html`<p class="muted">loading…</p>` : !cols.length
        ? html`<p class="muted">No regular users yet — add some in the users tab.</p>`
        : html`
        <div style="overflow-x:auto">
          <table class="matrix">
            <tr><th style="text-align:left"></th>${cols.map((u) => html`<th class="mono" title=${u.name}>${u.id}</th>`)}</tr>
            ${grouped.map(([grp, tiles]) => html`
              <tr><td class="mgrp" colspan=${cols.length + 1}>${grp}</td></tr>
              ${tiles.map((tile) => html`<tr>
                <td class="mono mtile">${tile}</td>
                ${cols.map((u) => {
                  const c = m.cells?.[u.id]?.[tile];
                  const sel = this._mapSel?.user === u.id && this._mapSel?.tile === tile;
                  return html`<td class="mcell ${sel ? 'msel' : ''} ${c ? 'has' : ''}"
                    @click=${() => { this._mapSel = c ? { user: u.id, tile } : null; }}>${this._lvChip(c?.level)}</td>`;
                })}
              </tr>`)}`)}
          </table>
        </div>
        ${this._mapDetail()}`}
    `;
  }

  // ---- orgs & teams (docs/auth.md; plans/orgs.md) ----
  // Orgs own the o/<org> path namespace (apps/o/<org>/…); teams grant access
  // to org members by union; policy rows are the runtime ceiling on what the
  // org's tiles may be granted. This tab is the workspace-admin console —
  // org admins manage their org from the shell's per-tile ⚙ access panel.
  async _orgAPI(method, path, body) {
    try {
      await api(path, body === undefined ? { method }
        : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }

  // ---- policy-row editor (workspace + per-org ceilings, D20) ----
  // Draft-based: "edit" copies the live rows into _polEdit[key] (mayCall kept
  // as raw text while typing), save PUTs the parsed rows, cancel drops the
  // draft. key = '' for the workspace rows, else the org id.
  _polDraft(key) { return this._polEdit?.[key]; }
  _polSet(key, rows) { this._polEdit = { ...(this._polEdit ?? {}), [key]: rows }; }
  _polStop(key) { const e = { ...(this._polEdit ?? {}) }; delete e[key]; this._polEdit = e; }

  async _polSave(key) {
    const rows = (this._polDraft(key) ?? [])
      .map((r) => {
        const mayCall = r.mayCallText.split(',').map((s) => s.trim()).filter(Boolean);
        const row = { tiles: r.tiles.trim() };
        if (r.deny.length) row.deny = r.deny;
        if (mayCall.length) row.mayCall = mayCall;
        return row;
      })
      .filter((r) => r.tiles);
    await this._orgAPI('PUT', key ? `/orgs/${encodeURIComponent(key)}/policy` : '/policy', { policy: rows });
    if (!this._err) this._polStop(key);
  }

  _policyEditor(key, rows) {
    const draft = this._polDraft(key);
    if (!draft) {
      return html`
        ${(rows ?? []).map((r) => html`<div class="mono" style="font-size:11px">
          tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}</div>`)}
        ${!(rows ?? []).length ? html`<div class="muted" style="font-size:11px">no rows (no ceiling)</div>` : nothing}
        <button class="act" style="margin-top:3px" @click=${() => this._polSet(key,
          (rows ?? []).map((r) => ({ tiles: r.tiles, deny: [...(r.deny ?? [])], mayCallText: (r.mayCall ?? []).join(', ') })))}>edit</button>`;
    }
    const upd = (i, patch) => this._polSet(key, draft.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    return html`
      <table class="flowtab" style="margin-top:4px">
        ${draft.map((r, i) => html`<tr>
          <td><input size="14" placeholder=${key ? '* (all org tiles)' : 'apps/o/x/*'} .value=${r.tiles}
                @input=${(e) => upd(i, { tiles: e.target.value })}></td>
          <td style="white-space:nowrap">${['net', 'gpu', 'xbin-caps'].map((k) => html`
            <label class="muted" style="font-size:10.5px; margin-right:5px">
              <input type="checkbox" .checked=${r.deny.includes(k)}
                @change=${(e) => upd(i, { deny: e.target.checked ? [...r.deny, k] : r.deny.filter((d) => d !== k) })}>deny ${k}</label>`)}</td>
          <td><input size="20" placeholder="mayCall: a/*, res:a/* (empty = any)" .value=${r.mayCallText}
                @input=${(e) => upd(i, { mayCallText: e.target.value })}></td>
          <td><button class="act rm" title="remove row" @click=${() => this._polSet(key, draft.filter((_, j) => j !== i))}>✕</button></td>
        </tr>`)}
      </table>
      <div style="margin-top:4px">
        <button class="act" @click=${() => this._polSet(key, [...draft, { tiles: key ? '*' : '', deny: [], mayCallText: '' }])}>+ row</button>
        <button class="act go" @click=${() => this._polSave(key)}>save</button>
        <button class="act" @click=${() => this._polStop(key)}>cancel</button>
        <span class="muted" style="font-size:10.5px; margin-left:6px">
          deny strips the capability; mayCall allow-lists external call targets
          (a tile's own scope is always exempt)</span>
      </div>`;
  }

  async _createOrg(f) {
    await this._orgAPI('POST', '/orgs', {
      id: f.id.value.trim(), name: f.orgname.value.trim(),
      admins: f.admins.value.split(',').map((s) => s.trim()).filter(Boolean),
      members: f.members.value.split(',').map((s) => s.trim()).filter(Boolean),
    });
    if (!this._err) f.reset();
  }

  async _createTeam(orgID, f) {
    await this._orgAPI('POST', `/orgs/${encodeURIComponent(orgID)}/teams`, { id: f.id.value.trim() });
    if (!this._err) f.reset();
  }

  _teamRow(o, t) {
    const tpath = `/orgs/${encodeURIComponent(o.id)}/teams/${encodeURIComponent(t.id)}`;
    const orgPeople = [...new Set([...(o.admins ?? []), ...(o.members ?? [])])];
    const tilesKey = `team:${o.id}/${t.id}:tiles`;
    const createKey = `team:${o.id}/${t.id}:create`;
    return html`<tr>
      <td class="mono">${t.id}</td>
      <td>${this._peoplePicker(t.members, (m) => this._orgAPI('PATCH', tpath, { members: m }), orgPeople)}</td>
      <td>${Object.entries(t.tiles || {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
          ${(t.canCreate || []).map((c) => html`<span class="pill">create·${c}</span>`)}
          ${t.termApi ? html`<span class="pill">term-api</span>` : nothing}
          ${t.termNet ? html`<span class="pill">term-net</span>` : nothing}</td>
      <td><select title="level the team gets on tiles created in it"
            @change=${(e) => this._orgAPI('PATCH', tpath, { newTiles: e.target.value })}>
          ${['read', 'write', 'terminal'].map((l) => html`<option value=${l} ?selected=${t.newTiles === l}>new: ${l}</option>`)}
        </select></td>
      <td style="text-align:right; white-space:nowrap">
        <button class="act" @click=${() => this._toggleDraft(tilesKey,
          () => Object.entries(t.tiles ?? {}).map(([target, level]) => ({ target, level })))}>tiles…</button>
        <button class="act" @click=${() => this._toggleDraft(createKey, () => [...(t.canCreate ?? [])])}>create…</button>
        <button class="act" @click=${() => this._orgAPI('PATCH', tpath, { termApi: !t.termApi })}>${t.termApi ? '− api' : '+ api'}</button>
        <button class="act" @click=${() => this._orgAPI('PATCH', tpath, { termNet: !t.termNet })}>${t.termNet ? '− net' : '+ net'}</button>
        <button class="act rm" @click=${() => confirm(`Delete team ${o.id}/${t.id}? Its access grants vanish.`) &&
          this._orgAPI('DELETE', tpath)}>del</button>
      </td>
    </tr>
    ${this._draft(tilesKey) ? html`<tr><td colspan="5">
      ${this._tilesEditor(tilesKey, (tiles) => this._orgAPI('PATCH', tpath, { tiles }), o.id)}
    </td></tr>` : nothing}
    ${this._draft(createKey) ? html`<tr><td colspan="5">
      ${this._patternsEditor(createKey, (canCreate) => this._orgAPI('PATCH', tpath, { canCreate }), o.id)}
    </td></tr>` : nothing}`;
  }

  _orgCard(o) {
    const opath = `/orgs/${encodeURIComponent(o.id)}`;
    return html`
      <div style="border:1px solid var(--bx-border,#e4e8ed); border-radius:6px; padding:8px 10px; margin:8px 0">
        <div style="display:flex; align-items:baseline; gap:8px; flex-wrap:wrap">
          <b class="mono">${o.id}</b>
          <span class="muted">${o.name !== o.id ? o.name : ''}</span>
          <span class="pill" title="tiles live at apps/o/${o.id}/… (and o/${o.id}/…)">o/${o.id}</span>
          <span style="flex:1"></span>
          <label class="muted" style="font-size:11px">base
            <select title="floor every member gets on org tiles (terminal is never implicit)"
              @change=${(e) => this._orgAPI('PATCH', opath, { basePermission: e.target.value })}>
              ${[['', 'none'], ['read', 'read'], ['write', 'write']].map(([v, l]) =>
                html`<option value=${v} ?selected=${(o.basePermission ?? '') === v}>${l}</option>`)}
            </select></label>
          <button class="act rm" @click=${() => confirm(`Delete org ${o.id}? Team grants and its policy vanish; tiles stay on disk.`) &&
            this._orgAPI('DELETE', opath)}>del</button>
        </div>
        <div style="margin-top:5px; font-size:12px; display:flex; gap:10px; align-items:center; flex-wrap:wrap">
          <label class="muted" style="font-size:11px">admins
            ${this._peoplePicker(o.admins, (a) => this._orgAPI('PATCH', opath, { admins: a }))}</label>
          <label class="muted" style="font-size:11px" title="team members must be org members; admins count">members
            ${this._peoplePicker(o.members, (m) => this._orgAPI('PATCH', opath, { members: m }))}</label>
        </div>
        <table style="margin-top:6px">
          ${(o.teams || []).length ? html`<tr><th>team</th><th>members</th><th>grants</th><th></th><th></th></tr>` : nothing}
          ${(o.teams || []).map((t) => this._teamRow(o, t))}
        </table>
        <form class="inline" style="margin-top:6px" @submit=${(e) => { e.preventDefault(); this._createTeam(o.id, e.target); }}>
          <input name="id" placeholder="new team id" size="12" required>
          <button class="act go">add team</button>
          <span class="muted" style="font-size:10.5px">then pick members / grant tiles on its row</span>
        </form>
        <div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">org policy ceiling</span>
          ${this._policyEditor(o.id, o.policy)}
        </div>
      </div>`;
  }

  _orgsView() {
    const orgs = this._orgs ?? [];
    return html`
      ${this._targetDatalist()}
      <h4>organizations</h4>
      ${orgs.length ? orgs.map((o) => this._orgCard(o))
        : html`<p class="muted">No orgs. An org owns the <span class="mono">o/&lt;org&gt;</span> path
          namespace (tiles at <span class="mono">apps/o/&lt;org&gt;/…</span>), groups users into teams
          with shared access, and can carry a policy ceiling on what its tiles may reach.</p>`}

      <h4>add org</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createOrg(e.target); }}>
        <input name="id" placeholder="org id (path-safe)" size="14" required>
        <input name="orgname" placeholder="display name" size="14">
        <input name="admins" placeholder="admins: alice" size="14">
        <input name="members" placeholder="members: bob, carol" size="18">
        <button class="act go">create</button>
      </form>

      <h4>workspace policy</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        Pattern-keyed ceiling on what tiles may be granted, applied to EVERY tile (org rows add on top;
        any deny wins, mayCall allow-lists intersect). Enforced at approval and at every evaluation —
        a hand-edited xbin.json can't bypass it.</p>
      ${this._policyEditor('', this._wsPolicy)}

      <p class="muted" style="font-size:11px; margin-top:10px; max-width:60ch">
        Teams grant by union: a member's level is the highest of their own entries, their teams'
        entries (inside the team's org only), and the org base permission. Creating a tile in a team
        (manager → create) grants the team its "new" level; the creator gets terminal. Org admins
        manage members/teams/access from the shell's per-tile ⚙ panel — term-api/term-net, policy,
        and org create/delete stay here.</p>`;
  }
}

customElements.define('bx-admin', BxAdmin);
