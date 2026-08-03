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
    _permsets: { state: true }, // {sets, attachedTo} (D28)
    _invite: { state: true },   // last minted invite link {id, url} (D22)
    _token: { state: true },    // freshly rotated owner token (copy-field box)
    _pwEdit: { state: true },   // user id whose password is being reset inline
    _secretEdit: { state: true }, // {comp, key} vault secret being re-set inline
    _notice: { state: true },   // green success line (never the red .err slot)
    _reqs: { state: true },     // pending human access requests (D36)
    _defaults: { state: true }, // defaultTiles map (D27)
    _polEdit: { state: true },  // policy-editor drafts, keyed '' (workspace) / org id
    _drafts: { state: true },   // click-through editor drafts, keyed by context
    _matrix: { state: true },   // /access-matrix payload (access-map tab)
    _ownerEdit: { state: true }, // owner reassignment {tile, to, rep?, perr?} (D39)
    _mapSel: { state: true },   // selected matrix cell {user, tile} → derivation panel
    _mapTileQ: { state: true }, // access-map filter: tile path / owner substring
    _showHidden: { state: true }, // reveal hidden (state=hidden) tiles in lists (D42)
    _mapUserQ: { state: true }, // access-map filter: user id/name substring
    _mapLayout: { state: true }, // 'auto' (default) | 'matrix' | 'list'
    _mapOpen: { state: true },  // set of tiles expanded in the by-tile list view
    _authSettings: { state: true },
    _alerts: { state: true }, // {tokenLoginDisabled, hasAdminUser, canDisable}
    _ifaces: { state: true },   // {bindings, components} — interface wiring
    _ingress: { state: true },  // {exposes, routes, streams, …} — published endpoints
    _ingEdit: { state: true },  // per-row route edits before publish (comp\x00slot → {…})
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
    _stSort: { state: true },   // live-stats table sort {col, dir}
    _stTotOrg: { state: true }, // totals charts: split lines per org
    _resType: { state: true },  // resources table: active type tab
    _stFilter: { state: true }, // live-stats name-prefix filter
    _stGroup: { state: true },  // live-stats: group rows by org
    _stOpen: { state: true },   // live-stats: tile expanded into big charts
  };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
            color: var(--bx-text, #33414e); background: var(--bx-panel, #fff); }
    /* two-level nav: a primary group row + a sub-tab row under it */
    .groups { display: flex; gap: 4px; padding: 6px 8px 0; flex-wrap: wrap;
              background: var(--bx-panel-2, #f7f8fa); position: sticky; top: 0; z-index: 2; }
    .groups button { border: 0; background: none; font: inherit; font-size: 12px; font-weight: 600;
      padding: 5px 12px; cursor: pointer; color: var(--bx-muted, #8794a1); border-radius: 6px;
      letter-spacing: .01em; }
    .groups button.on { background: var(--bx-accent, #f5a623); color: #fff; }
    .groups button:not(.on):hover { background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    .tabs { display: flex; gap: 2px; padding: 4px 8px 0; flex-wrap: wrap;
            border-bottom: 1px solid var(--bx-border, #e4e8ed);
            background: var(--bx-panel-2, #f7f8fa); position: sticky; top: 33px; z-index: 1; }
    .tabs.sub { top: 33px; }
    .tabs button { border: 1px solid transparent; border-bottom: none; background: none;
      font: inherit; font-size: 12px; padding: 4px 12px; cursor: pointer;
      color: var(--bx-muted, #8794a1); border-radius: 5px 5px 0 0; }
    .tabs button.on { background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      border-color: var(--bx-border, #e4e8ed); margin-bottom: -1px; }
    /* filter bar (scales list views to 1000s of tiles) */
    .filterbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin: 2px 0 10px; }
    .filterbar input.q { flex: 1; min-width: 12em; font: inherit; font-size: 12px; padding: 4px 9px;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    .chips { display: flex; gap: 4px; flex-wrap: wrap; }
    .chip { font-size: 11px; padding: 2px 9px; border-radius: 999px; cursor: pointer;
      border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff);
      color: var(--bx-muted, #8794a1); }
    .chip.on { background: var(--bx-accent, #f5a623); border-color: transparent; color: #fff; }
    .count-note { font-size: 11px; color: var(--bx-muted, #8794a1); white-space: nowrap; }
    /* standalone expand caret (component rows aren't inside .bk) */
    .caret { display: inline-block; color: var(--bx-muted, #8794a1); font-size: 10px;
             transition: transform .1s; }
    .caret.o { transform: rotate(90deg); }
    .body { padding: 12px 14px; }
    .err { color: var(--bx-red, #e5484d); font-size: 12px; margin: 4px 0; }
    .notice { color: var(--bx-green, #43a047); font-size: 12px; margin: 4px 0; }
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

    /* ---- live tile stats (resources tab) ---- */
    .strip { display: flex; gap: 10px; align-items: center; margin: 4px 0 8px; flex-wrap: wrap; }
    table.stats th.sortable { cursor: pointer; user-select: none; white-space: nowrap; }
    table.stats th.sortable:hover { color: var(--bx-text, #33414e); }
    table.stats .strow { cursor: pointer; }
    table.stats .strow:hover td, table.stats .strow.on td { background: var(--bx-panel-2, #f7f8fa); }
    .stcell { display: flex; flex-direction: column; gap: 1px; min-width: 90px; }
    .stcell .stval { font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
    svg.spark { display: block; opacity: .85; }
    td.stbig { padding: 8px 0 10px; }
    td.stbig > .stchart { display: inline-block; margin: 0 18px 4px 0; vertical-align: top; }
    .stchart b { font-variant-numeric: tabular-nums; font-weight: 600; }
    table.stats .grouphd { padding: 4px 8px; }

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
    .matrix .mown { padding: 2px 10px 2px 4px; white-space: nowrap; }
    .maprow { display: flex; align-items: center; gap: 6px; padding: 3px 4px;
              border-bottom: 1px solid var(--bx-border, #e4e8ed); font-size: 11.5px; }
    .maprow .mono { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
    .maprow:hover { background: var(--bx-panel-2, #f7f8fa); }
    .mapsub { margin: 0 0 6px 18px; font-size: 11px; }
    .mapsub td { padding: 1px 8px 1px 0; }
    .matrix .mcell.msel { outline: 2px solid color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      outline-offset: -2px; }
    .flow-deny { color: var(--bx-red, #e5484d); }
    .flow-allow { color: var(--bx-green, #43a047); }
    .err-pill { color: var(--bx-red, #e5484d); font-size: 11px; }
  `;

  // Two-level nav (deployments run to thousands of tiles, so the flat tab row
  // no longer scales). Primary GROUPS, each with sub-tabs; sub-tab ids stay
  // URL-safe for hash deep-links and unique across groups.
  static GROUPS = [
    { id: 'runtime', label: 'runtime', tabs: [
      { id: 'components', label: 'components' },
      { id: 'resources', label: 'resources' },
      { id: 'backup', label: 'backup' },
      { id: 'cron', label: 'cron' },
    ] },
    { id: 'usermgmt', label: 'user management', tabs: [
      { id: 'users', label: 'users' },
      { id: 'orgs', label: 'organisations' },
      { id: 'permsets', label: 'permission sets' },
      { id: 'map', label: 'access map' },
    ] },
    { id: 'vaultgrp', label: 'vault', tabs: [{ id: 'vault', label: 'vault' }] },
    { id: 'binding', label: 'binding', tabs: [
      { id: 'roles', label: 'roles' },
      { id: 'grants', label: 'grants' },
      { id: 'providers', label: 'interface providers' },
      { id: 'wiring', label: 'binding' },
    ] },
    { id: 'ingressgrp', label: 'ingress', tabs: [
      { id: 'endpoints', label: 'endpoints' },
      { id: 'expose', label: 'services / expose' },
    ] },
  ];
  static tabsFlat() { return BxAdmin.GROUPS.flatMap((g) => g.tabs); }
  _grpOf(tab) { return BxAdmin.GROUPS.find((g) => g.tabs.some((t) => t.id === tab)) || BxAdmin.GROUPS[0]; }

  constructor() {
    super();
    const h = location.hash.replace(/^#/, '');
    // Back-compat: honor a couple of old hash ids so bookmarks don't 404.
    const alias = { overview: 'components', runtime: 'components', interfaces: 'providers' };
    const want = alias[h] || h;
    this._tab = BxAdmin.tabsFlat().some((t) => t.id === want) ? want : 'components';
    this._err = '';
    this._alerts = [];
    this._denied = false;
    this._rtOpen = new Set();
    this._versions = {};
    this._verOpen = new Set();
    this._schedules = [];
    this._q = '';          // per-view text filter (reset on tab change)
    this._cats = new Set(); // active category chips
    this._access = {};      // per-component access relations, lazily loaded
    this._accOpen = new Set();
    this._stSort = { col: 'cpu', dir: -1 };
    this._stFilter = '';
    this._stGroup = false;
    this._stOpen = null;
  }

  _setGroup(g) { this._setTab(g.tabs[0].id); }

  _setTab(t) {
    this._tab = t;
    this._q = ''; this._cats = new Set();
    try { history.replaceState(null, '', '#' + t); } catch { /* sandboxed */ }
    // Leaving/clicking the component list drops any code drill-in (back to the
    // list); _openCode re-sets _codeComp right after calling this to drill in.
    this._codeComp = null;
    if (t === 'components' || t === 'resources') this._loadRuntime();
    if (t === 'map') this._loadMap();
    if (t === 'providers' || t === 'wiring' || t === 'endpoints' || t === 'expose') this._loadIfaces();
    if (t === 'backup') this._loadBackup();
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'grants' || e.type === 'reload' || e.type === 'build-ok' || e.type === 'users') this._refresh();
    });
    this._refresh();
    // Prime the data the initial tab needs (constructor set _tab from the hash
    // but doesn't fetch; _setTab does that on later clicks).
    if (this._tab === 'components' || this._tab === 'resources') this._loadRuntime();
    if (this._tab === 'map') this._loadMap();
    if (['providers', 'wiring', 'endpoints', 'expose'].includes(this._tab)) this._loadIfaces();
    if (this._tab === 'backup') this._loadBackup();
    // Live backend/resource data: poll while a runtime-data tab is active.
    this._rtTimer = setInterval(() => {
      if (this._tab === 'components' || this._tab === 'resources') this._loadRuntime();
    }, 2000);
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); clearInterval(this._rtTimer); }

  async _loadRuntime() {
    try {
      const rt = await api('/runtime');
      // Sample per-backend network totals for live rate sparklines.
      this._hist = this._hist || {};
      const now = Date.now();
      // Bus resources: ring of cumulative event counts → events/min.
      this._busRing = this._busRing || new Map();
      for (const r of rt.resources || []) {
        if (r.type !== 'bus') continue;
        const ring = this._busRing.get(r.id) || [];
        ring.push({ t: now, n: r.events || 0 });
        while (ring.length > 40) ring.shift();
        this._busRing.set(r.id, ring);
      }
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
      const [ov, vaults, cron, users, authSettings, vaultStatus, alerts, orgs, wsPolicy, permsets, defaults, reqs] = await Promise.all([
        api('/auth-overview'),
        api('/vaults').catch(() => null), // 503 while the barrier is sealed
        api('/cron/jobs'),
        api('/users').catch(() => ({ users: [] })),
        api('/auth-settings').catch(() => null),
        api('/vault-status').catch(() => null),
        api('/alerts').catch(() => ({ alerts: [] })),
        api('/orgs').catch(() => ({ orgs: [] })),
        api('/policy').catch(() => ({ policy: [] })),
        api('/permission-sets').catch(() => ({ sets: {}, attachedTo: {} })),
        api('/defaults').catch(() => ({ defaultTiles: {} })),
        api('/access-requests').catch(() => ({ requests: [] })),
      ]);
      this._ov = ov; this._vaults = vaults; this._cron = cron.jobs ?? [];
      this._alerts = alerts.alerts ?? [];
      this._users = users.users ?? [];
      this._orgs = orgs.orgs ?? [];
      this._wsPolicy = wsPolicy.policy ?? [];
      this._permsets = permsets; this._defaults = defaults.defaultTiles ?? {};
      this._reqs = reqs.requests ?? [];
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
    if (!this._codeComp) return this._componentsView(); // reached only defensively; the list is the picker
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
    const grp = this._grpOf(tab);
    return html`
      ${(this._alerts || []).length ? html`<div class="alertbar">
        ${this._alerts.map((a) => html`<div class="al ${a.level}">
          <b>${a.level === 'crit' ? '\u26A0' : '\u26A1'}</b> ${a.message}</div>`)}
      </div>` : nothing}
      <div class="groups">
        ${BxAdmin.GROUPS.map((g) => html`
          <button class=${g.id === grp.id ? 'on' : ''} @click=${() => this._setGroup(g)}>${g.label}</button>`)}
      </div>
      ${grp.tabs.length > 1 ? html`<div class="tabs sub">
        ${grp.tabs.map((t) => html`
          <button class=${t.id === tab ? 'on' : ''} @click=${() => this._setTab(t.id)}>${t.label}</button>`)}
      </div>` : nothing}
      <div class="body">
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
        ${this._notice ? html`<div class="notice">${this._notice}</div>` : nothing}
        ${tab === 'users' ? this._usersView()
          : tab === 'orgs' ? this._orgsView()
          : tab === 'permsets' ? this._permSetsView()
          : tab === 'map' ? this._mapView()
          : tab === 'components' ? (this._codeComp ? this._codeView() : this._componentsView())
          : tab === 'resources' ? this._resourcesView()
          : tab === 'vault' ? this._vaultView()
          : tab === 'roles' ? this._rolesCatalogView()
          : tab === 'grants' ? this._grantsView()
          : tab === 'providers' ? this._providersView()
          : tab === 'wiring' ? this._bindingView()
          : tab === 'endpoints' ? this._ingressEndpointsView()
          : tab === 'expose' ? this._ingressExposeView()
          : tab === 'backup' ? this._backupView()
          : this._cronView()}
      </div>`;
  }

  // ---- shared filter primitive (list views scale to 1000s of tiles) ----
  // _match tests any of the given strings against the current query (case-
  // insensitive substring). _filterBar renders the query input + optional
  // category chips + a result count; chips are toggled in this._cats.
  _match(...parts) {
    const q = (this._q || '').trim().toLowerCase();
    if (!q) return true;
    return parts.some((p) => (p || '').toString().toLowerCase().includes(q));
  }
  _catActive(c) { return this._cats.size === 0 || this._cats.has(c); }
  _toggleCat(c) {
    const s = new Set(this._cats); s.has(c) ? s.delete(c) : s.add(c); this._cats = s;
  }
  _filterBar(placeholder, cats, shown, total) {
    return html`<div class="filterbar">
      <input class="q" type="search" placeholder=${placeholder} .value=${this._q}
             @input=${(e) => { this._q = e.target.value; }}>
      ${cats && cats.length ? html`<div class="chips">
        ${cats.map((c) => html`<span class="chip ${this._cats.has(c) ? 'on' : ''}"
          @click=${() => this._toggleCat(c)}>${c}</span>`)}
      </div>` : nothing}
      ${total != null ? html`<span class="count-note">${shown}/${total}</span>` : nothing}
    </div>`;
  }

  // The scope/org category a component path falls in (for chips): its org
  // (o/<org>), else its top-level segment ("apps", "lib", \u2026).
  _catOf(path) {
    const s = (path || '').split('/');
    if (s[0] === 'o' && s[1]) return 'org:' + s[1];
    if (s[1] === 'o' && s[2]) return 'org:' + s[2];
    return s[0] || '\u2014';
  }

  // ---- runtime ----
  _fmtBytes(n) {
    n = n || 0; const u = ['B', 'K', 'M', 'G', 'T']; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i === 0 ? Math.round(n) : n.toFixed(1)) + u[i];
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

  // One tab per resource type; per-type columns (bus: live events/min from
  // the cumulative counter sampled every poll — see _loadRuntime).
  static resTypeOrder = ['filesystem', 'sqlite', 'kv', 'blob', 'bus', 'cron'];

  _busRate(id) {
    const ring = this._busRing?.get(id);
    if (!ring || ring.length < 2) return null;
    // events in the trailing ≤60s window, scaled to a minute
    const last = ring[ring.length - 1];
    let first = ring[0];
    for (const s of ring) { if (last.t - s.t <= 65000) { first = s; break; } }
    const dtMin = (last.t - first.t) / 60000;
    if (dtMin <= 0) return null;
    return Math.max(0, (last.n - first.n) / dtMin);
  }

  _resourcesSection(resources) {
    if (!resources || !resources.length) return nothing;
    const types = BxAdmin.resTypeOrder.filter((t) => resources.some((r) => r.type === t))
      .concat([...new Set(resources.map((r) => r.type))].filter((t) => !BxAdmin.resTypeOrder.includes(t)));
    const active = types.includes(this._resType) ? this._resType : types[0];
    const rows = resources.filter((r) => r.type === active);
    const cols = active === 'bus' ? ['id', 'events/min', 'events total']
      : active === 'cron' ? ['id', 'jobs']
      : active === 'kv' ? ['id', 'size', 'keys']
      : ['id', 'size', 'detail'];
    const cell = (r, c) => {
      switch (c) {
        case 'id': return html`<span class="p" title=${r.id}>${r.id}</span>`;
        case 'size': return html`<span class="num">${r.size ? this._fmtBytes(r.size) : '—'}</span>`;
        case 'events/min': { const v = this._busRate(r.id);
          return html`<span class="num">${v == null ? '…' : v < 10 ? v.toFixed(1) : Math.round(v)}</span>`; }
        case 'events total': return html`<span class="num">${r.events || 0}</span>`;
        default: return html`<span class="muted">${r.detail || ''}</span>`;
      }
    };
    return html`
      <h4>resources</h4>
      <div class="strip" style="gap:2px">
        ${types.map((t) => html`<button class="act ${t === active ? 'on' : ''}"
          style=${t === active ? 'font-weight:600' : ''}
          @click=${() => { this._resType = t; }}>${t}
          <span class="muted">${resources.filter((r) => r.type === t).length}</span></button>`)}
      </div>
      <div class="bk"><div class="row hdr rrow" style="grid-template-columns: 1fr ${cols.slice(1).map(() => '110px').join(' ')}">
        ${cols.map((c) => html`<span class=${c === 'id' ? '' : 'num'}>${c}</span>`)}</div></div>
      ${rows.map((r) => html`<div class="bk"><div class="row rrow" style="grid-template-columns: 1fr ${cols.slice(1).map(() => '110px').join(' ')}">
        ${cols.map((c) => cell(r, c))}
      </div></div>`)}`;
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

  // ---- components (runtime → components): the tile roster ----
  // Merges the manifest/principal view (_ov) with live backend state (_rt) and
  // adds per-component access relations on expand. Filterable + category-chipped
  // so it scales to thousands of tiles.
  _bkByPath() {
    const m = {};
    for (const b of (this._rt?.backends ?? [])) m[b.path] = b;
    return m;
  }
  _toggleComp(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
    if (s.has(path) && this._access[path] === undefined) this._loadAccess(path);
  }
  async _loadAccess(path) {
    this._access = { ...this._access, [path]: null }; // mark loading
    try {
      const d = await api('/access?tile=' + encodeURIComponent(path));
      this._access = { ...this._access, [path]: d };
    } catch (e) { this._access = { ...this._access, [path]: { error: String(e.message ?? e) } }; }
  }

  _componentsView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const c = ov.counts;
    const bk = this._bkByPath();
    const all = ov.components ?? [];
    const cats = [...new Set(all.map((k) => this._catOf(k.path)))].sort();
    const rows = all.filter((k) => this._catActive(this._catOf(k.path)) &&
      this._match(k.path, k.runtime, (k.uses ?? []).map((u) => u.target).join(' ')));
    const hiddenN = rows.filter((k) => k.state === 'hidden').length;
    const live = rows.filter((k) => !this._isOffloaded(k) && (this._showHidden || k.state !== 'hidden'));
    const off = rows.filter((k) => this._isOffloaded(k));
    return html`
      ${this._vaultBanner()}
      <div class="cards">
        <div class="stat"><div class="n">${c.components}</div><div class="l">components</div></div>
        <div class="stat"><div class="n">${c.exposed}</div><div class="l">expose APIs</div></div>
        <div class="stat"><div class="n">${c.grants}</div><div class="l">grants</div></div>
        <div class="stat ${c.pending ? 'warn' : ''}"><div class="n">${c.pending}</div><div class="l">pending</div></div>
      </div>
      ${this._filterBar('filter tiles by path, runtime or use…', cats, rows.length, all.length)}
      ${hiddenN ? html`<label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center;margin:2px 0 6px">
        <input type="checkbox" .checked=${!!this._showHidden}
          @change=${(e) => { this._showHidden = e.target.checked; }}> show hidden (${hiddenN})</label>` : nothing}
      <table>
        <tr><th></th><th>component</th><th>runtime</th><th>state</th><th>exposes</th><th>uses</th><th>vault</th><th>lifecycle</th></tr>
        ${live.map((k) => this._compRow(k, bk[k.path]))}
        ${live.length === 0 ? html`<tr><td></td><td class="muted" colspan="7">no matching components</td></tr>` : nothing}
      </table>
      ${off.length ? html`<h4>offloaded <span class="muted" style="font-weight:400;text-transform:none;letter-spacing:0">— archived, not running</span></h4>
        <table>
          <tr><th>component</th><th>state</th><th></th></tr>
          ${off.map((k) => html`<tr>
            <td class="mono">${k.path}</td>
            <td><span class="pill st-failed">${k.state}</span></td>
            <td style="text-align:right"><a class="link" @click=${() => this._setTab('backup')}>restore in Backup →</a></td>
          </tr>`)}
        </table>` : nothing}`;
  }

  _compRow(k, b) {
    const open = this._rtOpen.has(k.path);
    const state = b ? html`<span class="state ${b.state}">${b.state}</span>${b.isolated ? html` <span class="lock" title="sandboxed">🔒</span>` : nothing}`
      : html`<span class="muted">${(k.runtime && k.runtime !== 'static') ? 'idle' : 'static'}</span>`;
    return html`
      <tr>
        <td><span class="caret ${open ? 'o' : ''}" style="cursor:pointer" @click=${() => this._toggleComp(k.path)}>▶</span></td>
        <td class="mono"><a class="link" @click=${() => this._openCode(k.path)} title="view code & history">${k.path}</a>${k.manifestError ? html` <span class="st-failed" title=${k.manifestError}>⚠</span>` : nothing}</td>
        <td class="muted">${k.runtime || 'static'}</td>
        <td>${state}</td>
        <td>${k.roles ? Object.keys(k.roles).map((r) => html`<span class="pill">${r}</span>`) : html`<span class="muted">—</span>`}</td>
        <td>${(k.uses ?? []).map((u) => html`<span class="pill">${u.target}:${u.role}</span>`)}</td>
        <td>${k.hasVault ? '🔑' : ''}</td>
        <td>${this._lifecycleCell(k)}</td>
      </tr>
      ${open ? html`<tr><td></td><td colspan="7">${this._compDetail(k, b)}</td></tr>` : nothing}`;
  }

  // Component detail (expanded): who can reach it (access relations) + live
  // backend runtime. The access relations are the "user relations" view — the
  // reverse of the access map, per tile.
  _compDetail(k, b) {
    const acc = this._access[k.path];
    return html`<div class="detail" style="grid-template-columns:1fr">
      <div>
        <h5>access — who can reach this tile</h5>
        ${acc === undefined || acc === null ? html`<span class="muted">loading…</span>`
          : acc.error ? html`<span class="err-pill">${acc.error}</span>`
          : html`
            ${acc.org ? html`<div class="mono" style="font-size:11px;margin-bottom:3px">org: ${acc.org}</div>` : nothing}
            ${(acc.entries ?? []).length === 0 ? html`<span class="muted">no users or teams have access (admins always do)</span>` : html`
            <table class="tbl"><tr><th>who</th><th>level</th><th>via</th></tr>
              ${acc.entries.map((e) => html`<tr>
                <td>${e.kind === 'team' ? '👥' : '👤'} <span class="mono">${e.id}</span></td>
                <td><span class="pill">${e.level}</span></td>
                <td class="muted">${e.source}</td></tr>`)}
            </table>`}
            <a class="link" @click=${() => this._setTab('map')}>full access map →</a>`}
      </div>
      ${b ? html`<div style="margin-top:8px"><h5>runtime</h5>${this._bkDetail(b)}</div>` : nothing}
    </div>`;
  }

  _isOffloaded(k) { return k.state === 'offloaded' || k.state === 'offloaded-full'; }

  // ---- live per-tile stats (runtime → resources) --------------------------
  // CPU / memory / I/O rates sampled by xbind (cgroup leaves, /proc fallback;
  // internal/runner/stats.go), polled with the rest of /runtime every 2s.

  // Multi-line SVG sparkline over a stats series. keys/colors pick up to two
  // fields of each point; scaled to the window max (shared across lines).
  _stSpark(series, keys, colors, w = 84, ht = 18) {
    if (!series || series.length < 2) return html`<span class="muted" style="font-size:10px">—</span>`;
    let max = 0;
    for (const p of series) for (const k of keys) max = Math.max(max, p[k] || 0);
    const step = w / (series.length - 1);
    const pts = (k) => series.map((p, i) =>
      `${(i * step).toFixed(1)},${(ht - (max ? ((p[k] || 0) / max) : 0) * (ht - 2) - 1).toFixed(1)}`).join(' ');
    const p1 = pts(keys[0]);
    const p2 = keys[1] ? pts(keys[1]) : '';
    return html`<svg class="spark" width=${w} height=${ht} viewBox="0 0 ${w} ${ht}">
      <polyline points=${p1} fill="none" stroke=${colors[0]} stroke-width="1.2"></polyline>
      <polyline points=${p2} fill="none" stroke=${colors[1] || 'none'} stroke-width="1.2"></polyline>
    </svg>`;
  }

  // One metric cell: current value over its sparkline.
  _stCell(t, keys, colors, fmt) {
    return html`<div class="stcell">
      <span class="stval">${fmt(t.cur)}</span>
      ${this._stSpark(t.series, keys, colors)}
    </div>`;
  }

  // Sort on a ~1-minute moving average (last 30 points at 2s), not the
  // instantaneous sample — otherwise rows reshuffle on every poll.
  _stAvg(t, keys) {
    const s = t.series || [];
    const tail = s.slice(-30);
    if (!tail.length) return 0;
    let sum = 0;
    for (const p of tail) for (const k of keys) sum += p[k] || 0;
    return sum / tail.length;
  }

  _stSortKey(t) {
    switch (this._stSort.col) {
      case 'tile': return t.path;
      case 'org': return t.owner || '';
      case 'mem': return this._stAvg(t, ['mem']);
      case 'io': return this._stAvg(t, ['rbps', 'wbps']);
      case 'iops': return this._stAvg(t, ['riops', 'wiops']);
      case 'pids': return this._stAvg(t, ['pids']);
      default: return this._stAvg(t, ['cpu']);
    }
  }

  _stTh(col, label, title) {
    const s = this._stSort;
    return html`<th class="sortable" title=${title || ''}
      @click=${() => { this._stSort = { col, dir: s.col === col ? -s.dir : (col === 'tile' || col === 'org' ? 1 : -1) }; }}>
      ${label}${s.col === col ? (s.dir > 0 ? ' ▲' : ' ▼') : ''}</th>`;
  }

  // The four chart specs shared by cells and the expanded view. I/O series
  // are syscall-level (all file activity incl. FUSE-backed resources).
  static stMetrics = [
    { label: 'cpu', keys: ['cpu'], colors: ['var(--bx-accent,#f5a623)'], fmt: (c) => `${(c.cpu || 0).toFixed(1)}%` },
    { label: 'mem', keys: ['mem'], colors: ['var(--bx-green,#43a047)'], fmt: (c, el) => el._fmtBytes(c.mem || 0) },
    { label: 'i/o r+w', keys: ['rbps', 'wbps'], colors: ['#5b8def', 'var(--bx-red,#e5484d)'], fmt: (c, el) => `${el._fmtBytes(c.rbps || 0)}/s · ${el._fmtBytes(c.wbps || 0)}/s` },
    { label: 'iops r+w', keys: ['riops', 'wiops'], colors: ['#5b8def', 'var(--bx-red,#e5484d)'], fmt: (c) => `${Math.round(c.riops || 0)} · ${Math.round(c.wiops || 0)}` },
  ];

  // N-line sparkline over precomputed numeric arrays (shared max). Used by
  // the totals charts, where one line per org can exceed _stSpark's two.
  _stSparkN(lines, w = 220, ht = 44) {
    const len = Math.max(0, ...lines.map((l) => l.vals.length));
    if (len < 2) return html`<span class="muted" style="font-size:10px">gathering…</span>`;
    let max = 0;
    for (const l of lines) for (const v of l.vals) max = Math.max(max, v);
    const step = w / (len - 1);
    return html`<svg class="spark" width=${w} height=${ht} viewBox="0 0 ${w} ${ht}">
      ${lines.map((l) => html`<polyline fill="none" stroke=${l.color} stroke-width="1.3"
        points=${l.vals.map((v, i) => `${((i + (len - l.vals.length)) * step).toFixed(1)},${(ht - (max ? v / max : 0) * (ht - 2) - 1).toFixed(1)}`).join(' ')}></polyline>`)}
    </svg>`;
  }

  static orgPalette = ['#5b8def', '#43a047', '#f5a623', '#e5484d', '#9c27b0',
    '#00acc1', '#8d6e63', '#7cb342'];

  // Workspace totals: each metric summed across tiles point-by-point
  // (series share the sampler's cadence; aligned on the tail). "by org"
  // splits the sum into one line per owner.
  _stTotals(stats) {
    const tiles = stats?.tiles ?? [];
    if (!tiles.length) return nothing;
    const M = BxAdmin.stMetrics;
    const val = (p, keys) => keys.reduce((s, k) => s + (p[k] || 0), 0);
    const sum = (list, keys) => {
      const len = Math.max(0, ...list.map((t) => (t.series || []).length));
      const out = new Array(len).fill(0);
      for (const t of list) {
        const ser = t.series || [];
        for (let i = 0; i < ser.length; i++) out[len - ser.length + i] += val(ser[i], keys);
      }
      return out;
    };
    let orgs = null;
    if (this._stTotOrg) {
      const buckets = new Map();
      for (const t of tiles) {
        const k = t.owner || 'workspace';
        if (!buckets.has(k)) buckets.set(k, []);
        buckets.get(k).push(t);
      }
      orgs = [...buckets.entries()];
    }
    const chart = (m) => {
      const totalNow = tiles.reduce((s, t) => s + val(t.cur || {}, m.keys), 0);
      const lines = orgs
        ? orgs.map(([org, list], i) => ({ org, color: BxAdmin.orgPalette[i % BxAdmin.orgPalette.length], vals: sum(list, m.keys) }))
        : [{ color: m.colors[0], vals: sum(tiles, m.keys) }];
      return html`<div class="stchart">
        <div class="muted" style="font-size:10px;text-transform:uppercase;letter-spacing:.06em">${m.label}
          <b style="text-transform:none;letter-spacing:0"> ${m.label === 'cpu' ? `${totalNow.toFixed(1)}%`
            : m.label.startsWith('iops') ? `${Math.round(totalNow)}/s`
            : `${this._fmtBytes(totalNow)}${m.label === 'mem' ? '' : '/s'}`}</b></div>
        ${this._stSparkN(lines)}
      </div>`;
    };
    return html`
      <h4 style="display:flex;align-items:center;gap:12px">workspace totals
        <label class="muted" style="font-size:11px;font-weight:400;display:inline-flex;gap:5px;align-items:center">
          <input type="checkbox" .checked=${this._stTotOrg}
            @change=${(e) => { this._stTotOrg = e.target.checked; }}>
          by org</label></h4>
      <div class="stbig" style="padding:4px 0 2px">${M.map(chart)}</div>
      ${orgs && orgs.length > 1 ? html`<div class="strip" style="gap:10px;flex-wrap:wrap">
        ${orgs.map(([org], i) => html`<span class="muted mono" style="font-size:10.5px">
          <span style="display:inline-block;width:9px;height:9px;border-radius:2px;background:${BxAdmin.orgPalette[i % BxAdmin.orgPalette.length]}"></span>
          ${org}</span>`)}
      </div>` : nothing}`;
  }

  _liveStatsSection(stats) {
    const tiles = stats?.tiles ?? [];
    const M = BxAdmin.stMetrics;
    const f = (this._stFilter || '').trim();
    let rows = f ? tiles.filter((t) => t.path.startsWith(f) || (t.owner || '').startsWith(f)) : tiles.slice();
    const dir = this._stSort.dir;
    rows.sort((a, b) => {
      const ka = this._stSortKey(a); const kb = this._stSortKey(b);
      const c = typeof ka === 'string' ? ka.localeCompare(kb) : ka - kb;
      return c ? c * dir : a.path.localeCompare(b.path);
    });

    const head = html`<tr>
      ${this._stTh('tile', 'tile')}
      ${this._stGroup ? nothing : this._stTh('org', 'owner')}
      ${this._stTh('cpu', 'cpu %')}
      ${this._stTh('mem', 'memory')}
      ${this._stTh('io', 'i/o', 'read + write, syscall-level (includes resource/FUSE I/O)')}
      ${this._stTh('iops', 'iops', 'read + write ops/s')}
      ${this._stTh('pids', 'pids')}
    </tr>`;

    const row = (t) => {
      const open = this._stOpen === t.path;
      return html`<tr class="strow ${open ? 'on' : ''}" @click=${() => { this._stOpen = open ? null : t.path; }}>
        <td class="mono">${t.path}</td>
        ${this._stGroup ? nothing : html`<td class="muted mono" style="font-size:11px">${t.owner || '—'}</td>`}
        <td>${this._stCell(t, M[0].keys, M[0].colors, (c) => M[0].fmt(c, this))}</td>
        <td>${this._stCell(t, M[1].keys, M[1].colors, (c) => M[1].fmt(c, this))}</td>
        <td>${this._stCell(t, M[2].keys, M[2].colors, (c) => M[2].fmt(c, this))}</td>
        <td>${this._stCell(t, M[3].keys, M[3].colors, (c) => M[3].fmt(c, this))}</td>
        <td>${t.cur?.pids || 0}</td>
      </tr>
      ${open ? html`<tr><td colspan=${this._stGroup ? 6 : 7} class="stbig">
        ${M.map((m) => html`<div class="stchart">
          <div class="muted" style="font-size:10px;text-transform:uppercase;letter-spacing:.06em">${m.label}
            <b style="text-transform:none;letter-spacing:0"> ${m.fmt(t.cur || {}, this)}</b></div>
          ${this._stSpark(t.series, m.keys, m.colors, 300, 56)}
        </div>`)}
      </td></tr>` : nothing}`;
    };

    // Group by org: bucket per owner ref ("org:…", "user:…", or workspace).
    let body;
    if (this._stGroup) {
      const buckets = new Map();
      for (const t of rows) {
        const k = t.owner || 'workspace';
        if (!buckets.has(k)) buckets.set(k, []);
        buckets.get(k).push(t);
      }
      const agg = (list, key) => list.reduce((s, t) => s + (this._aggVal(t, key)), 0);
      body = [...buckets.entries()].map(([org, list]) => html`
        <tr><td colspan="6" class="grouphd mono">${org} <span style="float:right;font-weight:400">
          ${list.length} tile${list.length === 1 ? '' : 's'} · ${agg(list, 'cpu').toFixed(1)}% ·
          ${this._fmtBytes(agg(list, 'mem'))} · ${this._fmtBytes(agg(list, 'io'))}/s</span></td></tr>
        ${list.map(row)}`);
    } else {
      body = rows.map(row);
    }

    return html`
      <h4>live tiles</h4>
      <div class="strip">
        <input placeholder="filter by name prefix…" .value=${this._stFilter}
          @input=${(e) => { this._stFilter = e.target.value; }} style="width:200px">
        <label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center">
          <input type="checkbox" .checked=${this._stGroup}
            @change=${(e) => { this._stGroup = e.target.checked; this._stSort = this._stGroup && this._stSort.col === 'org' ? { col: 'cpu', dir: -1 } : this._stSort; }}>
          group by org</label>
        ${stats && !stats.cgroup ? html`<span class="muted" style="font-size:10.5px" title="run under the installed service (systemd Delegate=yes) for exact whole-tree accounting">process-tree sampling</span>` : nothing}
      </div>
      ${rows.length === 0 ? html`<p class="muted">${tiles.length === 0
        ? 'no running backends — live stats appear when a tile’s backend runs.'
        : 'no tiles match the filter.'}</p>`
      : html`<table class="stats">${head}${body}</table>`}`;
  }

  _aggVal(t, key) {
    const c = t.cur || {};
    if (key === 'io') return (c.rbps || 0) + (c.wbps || 0);
    return c[key] || 0;
  }

  // ---- resources (runtime → resources): host health + brokered state ----
  _resourcesView() {
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
      ${this._stTotals(rt.stats)}
      ${this._liveStatsSection(rt.stats)}
      ${this._resourcesSection(rt.resources)}
      ${(!rt.resources || !rt.resources.length) ? html`<p class="muted">no brokered resources provisioned yet — declare them in a <span class="mono">scope.json</span> (kv, blob, bus, cron, sqlite, filesystem). See <a href="/docs/resources.md" target="_blank">docs/resources.md</a>.</p>` : nothing}`;
  }

  // Lifecycle toggle (plans/lifecycle.md). Static/CGI components with no backend
  // still list, but only a running-backend runtime benefits — offer the toggle
  // for any runtime the owner may want paused.
  _lifecycleCell(k) {
    const st = k.state || 'enabled';
    const disabled = st !== 'enabled';
    return html`${disabled ? html`<span class="pill st-failed" title="not running">${st}</span> ` : nothing}
      <a class="link" @click=${() => this._setLifecycle(k.path, disabled ? 'enabled' : 'disabled')}>${st === 'hidden' ? 'unhide' : disabled ? 'enable' : 'disable'}</a>
      ${st !== 'hidden' ? html` · <a class="link" title="disabled + removed from sidebars until unhidden (D42)"
        @click=${() => this._setLifecycle(k.path, 'hidden')}>hide</a>` : nothing}`;
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
              <td class="secret">${this._secretEdit?.comp === v.component && this._secretEdit?.key === k ? html`
                <form style="display:inline-flex; gap:4px" @submit=${(e) => { e.preventDefault();
                    const nv = e.target.nv.value; this._secretEdit = null;
                    if (nv) this._setSecret(v.component, k, nv); }}>
                  <input name="nv" type="password" size="16" placeholder="new value (can't read the old one)" autofocus>
                  <button class="act" type="submit">save</button>
                  <button class="act" type="button" @click=${() => { this._secretEdit = null; }}>cancel</button>
                </form>` : '••••••••'}</td>
              <td style="text-align:right; white-space:nowrap">
                ${this._secretEdit?.comp === v.component && this._secretEdit?.key === k ? nothing
                  : html`<button class="act" @click=${() => { this._secretEdit = { comp: v.component, key: k }; }}>set</button>`}
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

  // ---- binding → grants: the grant table + approvals ----
  _grantsView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const comps = ov.components ?? [];
    const pending = (ov.pending ?? []).filter((g) => this._match(g.from, g.target, g.role));
    const grants = (ov.grants ?? []).filter((g) => this._match(g.from, g.target, g.role));
    const total = (ov.grants ?? []).length + (ov.pending ?? []).length;
    return html`
      ${this._filterBar('filter grants by caller, target or role…', null, pending.length + grants.length, total)}
      ${pending.length ? html`<h4>pending requests</h4>
        <table>${pending.map((g) => html`<tr>
          <td class="mono">${g.from} → ${g.target}</td>
          <td><span class="pill">${g.role}</span></td>
          <td style="text-align:right">${g.blocked
            ? html`<span class="err-pill" title=${g.blocked}>⛔ blocked by policy</span>`
            : html`<button class="act go" @click=${() => this._grant(g.from, g.target, g.role)}>approve</button>`}</td>
        </tr>`)}</table>` : nothing}

      <h4>active grants</h4>
      <table>${grants.length ? grants.map((g) => html`<tr>
        <td class="mono">${g.from} → ${g.target}</td>
        <td><span class="pill">${g.role}</span></td>
        <td style="text-align:right"><button class="act rm" @click=${() => this._revoke(g)}>revoke</button></td>
      </tr>`) : html`<tr><td class="muted">${this._q ? 'no matching grants' : 'none'}</td></tr>`}</table>

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
      </form>`;
  }

  // ---- binding → roles: the exposed-role catalog ----
  _rolesCatalogView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const rows = (ov.components ?? []).filter((k) => k.roles)
      .flatMap((k) => Object.entries(k.roles).map(([role, desc]) => ({ path: k.path, role, desc })))
      .filter((r) => this._match(r.path, r.role, r.desc));
    return html`
      <p class="muted">Every role a component <b>exposes</b> for others to be granted (manifest
        <code>expose.roles</code>). Callers request them in <code>uses</code>; you approve in
        <a class="link" @click=${() => this._setTab('grants')}>grants</a>.</p>
      ${this._filterBar('filter roles by component, role or description…', null, rows.length, rows.length)}
      <table>
        <tr><th>component</th><th>role</th><th>description</th></tr>
        ${rows.map((r) => html`<tr>
          <td class="mono">${r.path}</td><td><span class="pill">${r.role}</span></td>
          <td class="muted">${r.desc}</td></tr>`)}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="3">no exposed roles${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- interfaces (typed capability wiring; plans/interfaces.md) ----
  async _loadIfaces() {
    try {
      const [b, ing] = await Promise.all([api('/bindings'), api('/ingress')]);
      this._ifaces = b; this._ingress = ing; this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
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
  // Shared iface model: the provider roster (by kind, service-aware, instances
  // expanded) and the request list — consumed by both providers + binding views.
  _ifaceModel() {
    const d = this._ifaces || {};
    const comps = d.components || [];
    const instances = d.instances || {};
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
    const builtins = { net: ['internet', 'host'] };
    const requests = comps.flatMap((c) =>
      Object.entries(c.interfaces || {}).map(([slot, def]) => ({ comp: c.component, slot, def })));
    return { d, comps, instances, providersByKind, builtins, requests };
  }

  // ---- binding → interface providers ----
  _providersView() {
    if (!this._ifaces) return html`<div class="muted">loading…</div>`;
    const { comps, instances, providersByKind } = this._ifaceModel();
    const rows = comps.flatMap((c) => Object.entries(c.provides || {})
      .map(([slot, def]) => ({ comp: c.component, slot, def })))
      .filter((r) => this._match(r.comp, r.slot, r.def.kind, r.def.service));
    return html`
      <p class="muted">Tiles that <b>provide</b> a typed interface others can bind to — net providers,
        service (<code>http</code>) endpoints, ingress terminators. See
        <a href="/docs/elements.md" target="_blank">docs/elements.md</a>.</p>
      ${this._filterBar('filter providers by tile, slot, kind or service…', null, rows.length, rows.length)}
      <table class="tbl">
        <tr><th>tile</th><th>slot</th><th>kind</th><th>instances</th></tr>
        ${rows.map((r) => html`<tr>
          <td class="mono">${r.comp}</td><td>${r.slot}</td>
          <td><span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}</span></td>
          <td class="mono">${r.def.instances
            ? (Object.keys(instances[r.comp] || {}).sort().map((id) => html`<span class="pill">#${id}</span>`) || nothing)
            : html`<span class="muted">—</span>`}</td></tr>`)}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="4">no provider tiles${this._q ? ' match' : ''}${!this._q && Object.keys(providersByKind).length === 0 ? '' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- binding → binding: wire each requested slot to a provider ----
  _bindingView() {
    if (!this._ifaces) return html`<div class="muted">loading…</div>`;
    const { d, providersByKind, builtins, requests } = this._ifaceModel();
    const rows = requests.filter((r) => this._match(r.comp, r.slot, r.def.kind, r.def.service));
    return html`
      <p class="muted">Each component <b>requests</b> typed interface slots; you <b>bind</b> each to a
        provider. The binding is the authorization — unbound means no capability. Public exposure is
        under <a class="link" @click=${() => this._setTab('expose')}>ingress → services / expose</a>.</p>
      ${this._filterBar('filter by component, slot, kind or service…', null, rows.length, requests.length)}
      <table class="tbl">
        <tr><th>component</th><th>slot</th><th>kind</th><th>bound to</th></tr>
        ${rows.map((r) => {
          const raw = d.bindings?.[r.comp]?.[r.slot];
          const bound = [].concat(raw ?? []).map((x) => (x && x.ref) ? x.ref : x); // string|{ref}|array → refs
          const own = (p) => p === r.comp || p.startsWith(r.comp + '#');
          const opts = [...(builtins[r.def.kind] || []),
            ...(providersByKind[r.def.kind] || [])
              .filter((e) => !own(e.ref) &&
                (r.def.kind !== 'http' || !r.def.service || e.service === r.def.service))
              .map((e) => e.ref)];
          const kind = html`<span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}${r.def.multi ? ' ×N' : ''}</span>`;
          if (r.def.multi) {
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
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="4">no components request interfaces${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- ingress (published endpoints; plans/ingress.md) ----
  _ingKey(comp, slot) { return comp + '\x00' + slot; }
  _ingEditFor(e) {
    // The working row state: pending edits over the current binding.
    return this._ingEdit?.[this._ingKey(e.component, e.slot)]
      ?? { source: e.source || '', host: e.host || '', zone: e.zone || '', listen: e.listen || '' };
  }
  _ingSetEdit(e, patch) {
    const k = this._ingKey(e.component, e.slot);
    this._ingEdit = { ...(this._ingEdit || {}), [k]: { ...this._ingEditFor(e), ...patch } };
  }
  async _ingPublish(e) {
    const ed = this._ingEditFor(e);
    if (!ed.source) return;
    const body = { component: e.component, slot: e.slot, provider: ed.source };
    if (e.kind === 'http') {
      // Exactly one of host/zone — send whichever is filled (server validates).
      if (ed.zone) body.zone = ed.zone; else body.host = ed.host;
    } else if (ed.listen) body.listen = ed.listen;
    try {
      await api('/bindings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const k = this._ingKey(e.component, e.slot);
      const { [k]: _, ...rest } = this._ingEdit || {}; this._ingEdit = rest;
      await this._loadIfaces();
    } catch (err) { this._err = String(err.message ?? err); }
  }
  async _ingUnpublish(e) {
    try {
      await api('/bindings', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: e.component, slot: e.slot }) });
      await this._loadIfaces();
    } catch (err) { this._err = String(err.message ?? err); }
  }
  // ---- ingress → services / expose: publish tiles to the outside ----
  _ingressExposeView() {
    const d = this._ingress;
    if (!d) return html`<span class="muted">loading…</span>`;
    const streams = d.streams || [];
    const sources = (kind) => kind === 'http' ? ['runtime', ...(d.terminators || [])] : ['runtime'];
    const all = d.exposes || [];
    const exposes = all.filter((e) => this._match(e.component, e.slot, e.kind, e.host, e.zone));
    return html`
      <p class="muted">Tiles declare <code>exposes</code> in their manifest; <b>binding a slot to an
        ingress source publishes it</b> to the outside — anonymous traffic, confined to the tile's
        declared public paths. Unbound = unreachable, exactly like interfaces.
        See <a href="/docs/ingress.md" target="_blank">docs/ingress.md</a>.</p>
      ${all.length === 0 ? html`<div class="muted">No tile declares <code>exposes</code> yet. Add an
        <span class="mono">exposes</span> block to a tile's <span class="mono">xbin.json</span>
        (http paths, or a tcp/udp port), or import the <b>Public HTTPS (Traefik)</b> tile.</div>` : html`
      ${this._filterBar('filter exposed endpoints…', null, exposes.length, all.length)}
      <table class="tbl">
        <tr><th>tile</th><th>endpoint</th><th>source</th><th>route</th><th></th><th>state</th></tr>
        ${exposes.map((e) => {
          const ed = this._ingEditFor(e);
          const bound = !!e.source;
          const dirty = ed.source !== (e.source || '') || ed.host !== (e.host || '')
            || ed.zone !== (e.zone || '') || ed.listen !== (e.listen || '');
          const endpoint = e.kind === 'http'
            ? html`<span class="pill">http</span> <span class="muted">${(e.paths || []).join(' ')}</span>`
            : html`<span class="pill">${e.proto}:${e.port}</span>`;
          const routeEd = e.kind === 'http'
            ? html`<input class="mono" style="width:12em" placeholder="host (blog.example.com)"
                     .value=${ed.zone ? '' : ed.host} ?disabled=${!!ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { host: ev.target.value.trim(), zone: '' })}>
                   <input class="mono" style="width:11em" placeholder="or zone (*.sites.…)"
                     .value=${ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { zone: ev.target.value.trim() })}>`
            : html`<input class="mono" style="width:8em" placeholder=":${e.port} (host port)"
                     .value=${ed.listen}
                     @input=${(ev) => this._ingSetEdit(e, { listen: ev.target.value.trim() })}>`;
          let state = html`<span class="muted">unbound — not reachable</span>`;
          if (e.blocked) state = html`<span class="st-failed">⛔ ${e.blocked}</span>`;
          else if (bound && e.kind === 'http') state = html`<span class="st-healthy">public: ${e.zone || e.host}</span>`;
          else if (bound) {
            const st = streams.find((s) => s.component === e.component && s.slot === e.slot);
            state = st?.error ? html`<span class="st-failed">⚠ ${st.error}</span>`
              : html`<span class="st-healthy">host ${e.listen || ':' + e.port} → :${e.port}${st ? ` (${st.active} active)` : ''}</span>`;
          }
          return html`<tr>
            <td class="mono">${e.component}</td><td>${e.slot} ${endpoint}</td>
            <td><select ?disabled=${!!e.blocked} @change=${(ev) => this._ingSetEdit(e, { source: ev.target.value })}>
              <option value="" ?selected=${!ed.source}>— unbound —</option>
              ${sources(e.kind).map((s) => html`<option value=${s} ?selected=${ed.source === s}>${s}</option>`)}
            </select></td>
            <td>${routeEd}</td>
            <td>
              ${ed.source && (dirty || !bound) ? html`<button class="act go" @click=${() => this._ingPublish(e)}>publish</button>` : nothing}
              ${bound ? html`<button class="act rm" @click=${() => this._ingUnpublish(e)}>unpublish</button>` : nothing}
            </td>
            <td>${state}</td></tr>`;
        })}
        ${exposes.length === 0 ? html`<tr><td class="muted" colspan="6">no matching endpoints</td></tr>` : nothing}
      </table>`}`;
  }

  // ---- ingress → endpoints: live routes, listeners, terminators ----
  _ingressEndpointsView() {
    const d = this._ingress;
    if (!d) return html`<span class="muted">loading…</span>`;
    const routes = (d.routes || []).filter((r) => this._match(r.host, r.component, r.slot, r.source));
    const streams = d.streams || [];
    const forwards = d.forwards || [];
    const lst = d.httpListener || {};
    return html`
      <p class="muted">The live public routing table — what the outside can reach right now. Publish
        or unpublish under <a class="link" @click=${() => this._setTab('expose')}>services / expose</a>.</p>
      <div class="muted" style="margin:2px 0 10px">
        builtin HTTP listener: ${lst.listen ? html`<b>${lst.listen}</b> (${lst.tls ? 'TLS' : 'no TLS — front it, or use the Traefik tile'})` : 'off — start xbind with --ingress-listen'}
      </div>
      <h4>HTTP routes</h4>
      ${this._filterBar('filter routes by host or tile…', null, routes.length, (d.routes || []).length)}
      <table class="tbl">
        <tr><th>public host</th><th>→ tile</th><th>via</th></tr>
        ${routes.map((r) => html`<tr>
          <td class="mono">${r.host}</td>
          <td class="mono">${r.component}.${r.slot}</td>
          <td>${r.source}${r.zone ? html` <span class="muted">(zone ${r.zone})</span>` : nothing}</td></tr>`)}
        ${routes.length === 0 ? html`<tr><td class="muted" colspan="3">no HTTP routes${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>
      ${streams.length ? html`<h4>stream listeners (tcp / udp)</h4>
        <table class="tbl">
          <tr><th>host listen</th><th>→ tile</th><th>proto</th><th>state</th></tr>
          ${streams.map((s) => html`<tr>
            <td class="mono">${s.listen}</td>
            <td class="mono">${s.component}.${s.slot} → :${s.port}</td>
            <td>${s.proto}</td>
            <td>${s.error ? html`<span class="st-failed">⚠ ${s.error}</span>` : html`<span class="st-healthy">${s.active} active</span>`}</td></tr>`)}
        </table>` : nothing}
      ${forwards.length ? html`<h4>terminator forward doors</h4>
        <table class="tbl">
          <tr><th>terminator tile</th><th>state</th></tr>
          ${forwards.map((f) => html`<tr>
            <td class="mono">${f.source}</td>
            <td>${f.error ? html`<span class="st-failed">⚠ ${f.error}</span>` : html`<span class="st-healthy">up</span>`}</td></tr>`)}
        </table>` : nothing}`;
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
      const d = await api('/users', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: f.id.value.trim(), name: f.name.value.trim(), role: f.role.value,
          termApi: f.termApi.checked, termNet: f.termNet.checked,
          password: f.password.value }) });
      if (d?.inviteUrl) this._invite = { id: f.id.value.trim(), url: location.origin + d.inviteUrl };
      f.reset(); this._err = ''; this._refresh(); // only clear the form on success
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _patchUser(id, patch) {
    await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch) });
    this._refresh();
  }
  async _resetPw(id, pw) {
    if (!pw) return;
    if (pw.length < 8) { this._err = 'password too short (min 8 characters)'; return; }
    try {
      await this._patchUser(id, { password: pw });
      this._err = ''; this._notice = `password reset for ${id}`;
      setTimeout(() => { this._notice = ''; }, 3000);
    } catch (e) { this._err = String(e.message ?? e); this._notice = ''; }
  }
  async _setDisabled(u) {
    if (!u.disabled && !confirm(`Disable ${u.id}? Their sessions and terminals stop working now; everything is kept for re-enable.`)) return;
    try {
      await this._patchUser(u.id, { disabled: !u.disabled });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); } // self/last-admin guards land here
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
      </div>
      ${this._tokenBox()}`;
  }

  async _rotateToken() {
    if (!confirm('Rotate the owner token? The current token stops working immediately (bearer + cookie). Host-side bx/automation must switch to the new one.')) return;
    try {
      const d = await api('/auth-rotate-token', { method: 'POST' });
      this._token = d.token; // rendered in a copy-field box (like invites)
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // The rotated-token box: a copy field that stays until dismissed — a
  // prompt() you can accidentally dismiss is no place for a credential.
  _tokenBox() {
    if (!this._token) return nothing;
    return html`<div style="margin:8px 0; padding:8px 10px; border:1px solid var(--bx-green,#43a047);
        border-radius:6px; display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <b style="font-size:12px">new owner token</b>
      <input class="mono" size="40" readonly .value=${this._token} @focus=${(e) => e.target.select()}>
      <button class="act" @click=${() => navigator.clipboard?.writeText(this._token)}>copy</button>
      <span class="muted" style="font-size:10.5px">also written to &lt;workspace&gt;/.xbin/token —
        update host-side XBIN_TOKEN</span>
      <button class="act" @click=${() => { this._token = null; }}>✕</button>
    </div>`;
  }

  // _userOrgPills summarizes a user's org memberships for the users table.
  _userOrgPills(uid) {
    const out = [];
    for (const o of (this._orgs ?? [])) {
      const m = (o.members ?? []).find((x) => x.id === uid);
      if (m) out.push(m.suspended ? `${o.id}·suspended` : m.admin ? `${o.id}·admin` : `${o.id}·${m.level}`);
    }
    return out;
  }

  async _mintInvite(id) {
    try {
      const d = await api(`/users/${encodeURIComponent(id)}/invite`, { method: 'POST' });
      this._invite = { id, url: d.inviteLink || location.origin + d.inviteUrl };
      this._err = ''; this._notice = `invite link minted for ${id}`;
      setTimeout(() => { this._notice = ''; }, 3000);
    } catch (e) { this._err = String(e.message ?? e); }
    this._refresh();
  }

  // The one-time invite link box: shown after creating a user without a
  // password or minting a re-invite — copy it and send it however you like
  // (no self-signup: links only ever come from an admin).
  _inviteBox() {
    const inv = this._invite;
    if (!inv) return nothing;
    return html`<div style="margin:8px 0; padding:8px 10px; border:1px solid var(--bx-green,#43a047);
        border-radius:6px; display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <b style="font-size:12px">invite link for ${inv.id}</b>
      <input class="mono" size="46" readonly .value=${inv.url} @focus=${(e) => e.target.select()}>
      <button class="act" @click=${() => navigator.clipboard?.writeText(inv.url)}>copy</button>
      <span class="muted" style="font-size:10.5px">single-use · expires in 72h · send it to them yourself</span>
      <button class="act" @click=${() => { this._invite = null; }}>✕</button>
    </div>`;
  }

  // Pending human access requests (D36) — approve writes an exact entry at
  // the chosen level (authoritative, D31) and clears the row.
  _requestsView() {
    const reqs = (this._reqs ?? []).filter((q) => q.manage);
    if (!reqs.length) return nothing;
    return html`
      <h4>access requests</h4>
      ${reqs.map((q) => html`<div style="display:flex; gap:6px; align-items:center; flex-wrap:wrap; margin:3px 0; font-size:12px">
        <span class="mono">${q.user}</span> wants
        <select id="rq-${q.user}-${q.tile}">
          ${['read', 'write', 'terminal'].map((l) => html`<option value=${l} ?selected=${q.level === l}>${l}</option>`)}
        </select>
        on <span class="mono">${q.tile}</span>
        ${q.note ? html`<span class="muted">— ${q.note}</span>` : nothing}
        <button class="act go" @click=${async () => {
          const sel = this.renderRoot.getElementById(`rq-${q.user}-${q.tile}`);
          try {
            await api('/access-requests/approve', { method: 'POST', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ user: q.user, tile: q.tile, level: sel?.value || q.level }) });
            this._err = '';
          } catch (e) { this._err = String(e.message ?? e); }
          this._refresh();
        }}>approve</button>
        <button class="act rm" @click=${async () => {
          try {
            await api('/access-requests', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ user: q.user, tile: q.tile }) });
            this._err = '';
          } catch (e) { this._err = String(e.message ?? e); }
          this._refresh();
        }}>dismiss</button>
      </div>`)}`;
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
          return html`<tr style=${u.disabled ? 'opacity:.55' : ''}>
          <td class="mono">${u.id}</td>
          <td>${u.name}</td>
          <td><span class="pill">${u.role}</span>${u.disabled ? html`<span class="pill" style="color:var(--bx-red,#e5484d); border-color:var(--bx-red,#e5484d)" title="account disabled — can't sign in; everything is kept for re-enable (D34)">disabled</span>` : nothing}${u.invitePending ? html`<span class="pill" title="an unredeemed invite link is out">invited</span>` : nothing}</td>
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
            <button class="act" title="mint a single-use set-password link (re-minting invalidates the old one)"
              @click=${() => this._mintInvite(u.id)}>invite</button>
            ${this._pwEdit === u.id ? html`
              <form style="display:inline-flex; gap:4px" @submit=${(e) => { e.preventDefault();
                  const pw = e.target.pw.value; this._pwEdit = null; this._resetPw(u.id, pw); }}>
                <input name="pw" type="password" size="12" placeholder="new password (min 8)" autofocus>
                <button class="act" type="submit">set</button>
                <button class="act" type="button" @click=${() => { this._pwEdit = null; }}>✕</button>
              </form>` : html`
              <button class="act" title="reset password inline (or use invite for reset-by-link)"
                @click=${() => { this._pwEdit = u.id; }}>pw</button>`}
            <button class="act ${u.disabled ? '' : 'rm'}" title=${u.disabled
              ? 'restore the account — same password, tiles, memberships'
              : 'pause the account: sign-in, sessions and invites all refuse; nothing is lost (D34)'}
              @click=${() => this._setDisabled(u)}>${u.disabled ? 'enable' : 'disable'}</button>
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

      ${this._requestsView()}

      <h4>add user</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createUser(e.target); }}>
        <input name="id" placeholder="username" size="12" required>
        <input name="name" placeholder="display name" size="14">
        <select name="role"><option value="user">user</option><option value="admin">admin</option></select>
        <label class="muted" style="font-size:11px"><input type="checkbox" name="termApi"> term-api</label>
        <label class="muted" style="font-size:11px"><input type="checkbox" name="termNet"> term-net</label>
        <input name="password" type="password" placeholder="password (empty → invite link)" size="16" minlength="8">
        <button class="act go">create</button>
      </form>
      ${this._inviteBox()}
      <p class="muted" style="font-size:11px;margin-top:6px">
        Leave the password empty to get a single-use <b>invite link</b> instead —
        the new user sets their own password when opening it (there is no
        self-signup; accounts only come from here).
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
      if (s.length > 1) pats.add(`${s[0]}/*`);
    }
    return [...comps.sort(), ...[...pats].sort()];
  }
  _targetDatalist() {
    return html`<datalist id="tile-targets">
      ${this._targetOptions().map((t) => html`<option value=${t}></option>`)}
    </datalist>`;
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
            ${['read', 'write', 'terminal', 'none'].map((l) => html`<option value=${l} ?selected=${r.level === l}
              title=${l === 'none' ? 'authoritative: overrides org membership, patterns and defaults (D31)' : ''}>${l === 'none' ? 'none (exclude)' : l}</option>`)}
          </select>
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
          <span class="muted" style="font-size:10.5px">read = see it · write = use/edit · terminal = root shell on it ·
            exact entries are authoritative (none = exclude, D31)</span>
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
    try {
      this._matrix = await api('/access-matrix');
      // Lifecycle states for the map's hidden-tile filtering (D42).
      const comps = await api('/components').catch(() => []);
      this._compState = Object.fromEntries((comps ?? []).map((c) => [c.path, c.state || 'enabled']));
    } catch (e) { this._err = String(e.message ?? e); }
  }

  _lvChip(level) {
    const s = { read: 'r', write: 'w', terminal: 't' }[level] ?? '·';
    return html`<span class="lv lv-${level || 'none'}" title=${level || 'no access'}>${s}</span>`;
  }

  _srcLabel(src) {
    const [kind, ...rest] = String(src).split(':');
    switch (kind) {
      case 'admin': return 'workspace admin';
      case 'owner': return 'owns the tile (D24)';
      case 'exact': return 'exact entry — authoritative (D31)';
      case 'org-admin': return `admin of owning org ${rest[0]}`;
      case 'org-member': return html`member level in owning org <span class="mono">${rest[0]}</span>`;
      case 'org-share': return html`shared to org <span class="mono">${rest[0]}</span> · <span class="mono">${rest.slice(1).join(':')}</span>`;
      case 'direct': return html`own entry <span class="mono">${rest.join(':')}</span>`;
      case 'default': return html`workspace default <span class="mono">${rest.join(':')}</span>`;
      default: return src;
    }
  }

  _structureView() {
    const users = this._users ?? [];
    const orgs = this._orgs ?? [];
    const inOrg = new Set(orgs.flatMap((o) => (o.members ?? []).map((m) => m.id)));
    const wsAdmins = users.filter((u) => u.role === 'admin').map((u) => u.id);
    const outside = users.filter((u) => u.role !== 'admin' && !inOrg.has(u.id)).map((u) => u.id);
    const person = (m) => html`<span class="pill ${m.admin ? 'crown' : ''}"
      title="level ${m.level}${m.create ? ' · may create org tiles' : ''}${m.admin ? ' · org admin' : ''}">
      ${m.admin ? '★ ' : ''}${m.id}·${(m.level || 'read')[0]}${m.create && !m.admin ? '+' : ''}</span>`;
    return html`
      <div class="snode ws">
        <div class="shead">workspace</div>
        <div>admins: ${wsAdmins.length ? wsAdmins.map((a) => html`<span class="pill crown">★ ${a}</span>`) : html`<span class="muted">root token only</span>`}
          ${this._wsPolicy?.length ? html`<span class="pill pol" title=${this._wsPolicy.map((r) => `tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}`).join('\n')}>⛔ ${this._wsPolicy.length} policy row(s)</span>` : nothing}
        </div>
        ${outside.length ? html`<div style="margin-top:3px"><span class="muted" style="font-size:11px">in no org:</span> ${outside.map((u) => html`<span class="pill">${u}</span>`)}</div>` : nothing}
      </div>
      ${orgs.map((o) => html`
        <div class="snode org">
          <div class="shead"><span class="mono">${o.id}</span>${o.name !== o.id ? html` <span class="muted">${o.name}</span>` : nothing}
            ${(o.sets ?? []).map((n) => html`<span class="pill" title="permission set">⛭ ${n}</span>`)}
            ${(o.resolvedAllow ?? []).length ? html`<span class="pill" title=${'org admins may self-approve:\n' + o.resolvedAllow.join('\n')}>✓ ${o.resolvedAllow.length} allowance(s)</span>` : nothing}
            ${o.policy?.length ? html`<span class="pill pol" title=${o.policy.map((r) => `tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}`).join('\n')}>⛔ ${o.policy.length} policy row(s)</span>` : nothing}
          </div>
          <div>${(o.members ?? []).length ? (o.members ?? []).map(person) : html`<span class="muted">no members</span>`}</div>
          ${(o.ownedTiles ?? []).length ? html`<div style="margin-top:3px">
            ${(o.ownedTiles ?? []).map((p) => html`<span class="pill mono" title="owned by ${o.id}">${p}</span>`)}</div>` : nothing}
        </div>`)}`;
  }

  _mapDetail() {
    const s = this._mapSel;
    const c = s && this._matrix?.matrix?.[s.user]?.[s.tile];
    if (!c) return nothing;
    return html`
      <div style="margin-top:8px; padding:8px 10px; border:1px solid var(--bx-border,#e4e8ed); border-radius:6px">
        <span class="mono">${s.user}</span> on <span class="mono">${s.tile}</span> →
        ${this._lvChip(c.level)} <b>${c.level}</b>
        <table style="margin-top:5px">
          ${(c.explain ?? []).map((v, i) => html`<tr style=${i === 0 ? '' : 'opacity:.65'}>
            <td style="white-space:nowrap">${this._lvChip(v.level)} ${v.level}</td>
            <td>${this._srcLabel(v.source)}</td>
            <td class="muted" style="font-size:10.5px">${i === 0 ? '← effective (highest wins)' : 'unioned'}</td>
          </tr>`)}
        </table>
      </div>`;
  }

  // _ownerCell: the owner pill + the D39 transfer entry point — a labeled
  // button, not an icon (review feedback: ⇄ alone wasn't discoverable).
  _ownerCell(m, tile) {
    const owner = m?.owners?.[tile] ?? '';
    const icon = owner.startsWith('user:') ? '👤 ' : owner.startsWith('org:') ? '🏢 ' : '';
    return html`<span class="pill mono" title="owner (D24)">${icon}${owner || 'workspace'}</span>
      <button class="act" title="reassign this tile's owner — previews impact first (D39)"
        @click=${() => {
          this._ownerEdit = this._ownerEdit?.tile === tile ? null
            : { tile, to: owner };
        }}>transfer</button>`;
  }

  // _mapGroups: tiles grouped by OWNER (D24), filtered by the tile/owner query.
  _mapHiddenCount(m) {
    return (m.components ?? []).filter((p) => this._compState?.[p] === 'hidden').length;
  }

  _mapGroups(m, tq) {
    const groups = new Map();
    for (const tile of (m?.components ?? [])) {
      if (!this._showHidden && this._compState?.[tile] === 'hidden') continue; // D42
      const owner = m?.owners?.[tile] ?? '';
      if (tq && !tile.toLowerCase().includes(tq) &&
          !(owner || 'workspace').toLowerCase().includes(tq)) continue;
      const key = owner.startsWith('org:') ? `org ${owner.slice(4)}`
        : owner.startsWith('user:') ? `user ${owner.slice(5)}`
        : (tile.includes('/') ? tile.split('/')[0] : 'workspace');
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(tile);
    }
    return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
  }

  // The wide users×tiles matrix — the classic view for small workspaces.
  _mapMatrix(m, grouped, cols) {
    return html`
      <div style="overflow-x:auto">
        <table class="matrix">
          <tr><th style="text-align:left"></th><th style="text-align:left">owner</th>
            ${cols.map((u) => html`<th class="mono" title=${u.name}>${u.id}</th>`)}</tr>
          ${grouped.map(([grp, tiles]) => html`
            <tr><td class="mgrp" colspan=${cols.length + 2}>${grp}</td></tr>
            ${tiles.map((tile) => html`<tr>
              <td class="mono mtile">${tile}</td>
              <td class="mown">${this._ownerCell(m, tile)}</td>
              ${cols.map((u) => {
                const c = m.matrix?.[u.id]?.[tile];
                const sel = this._mapSel?.user === u.id && this._mapSel?.tile === tile;
                return html`<td class="mcell ${sel ? 'msel' : ''} ${c ? 'has' : ''}"
                  @click=${() => { this._mapSel = c ? { user: u.id, tile } : null; }}>${this._lvChip(c?.level)}</td>`;
              })}
            </tr>
            ${this._ownerEdit?.tile === tile ? html`<tr>
              <td colspan=${cols.length + 2}>${this._ownerEditor(m)}</td>
            </tr>` : nothing}`)}`)}
        </table>
      </div>`;
  }

  // The by-tile list — the many-users layout: one row per tile (owner +
  // transfer + who-has-access summary), expandable to that tile's users with
  // levels + provenance. Cells still click through to the derivation panel.
  _mapList(m, grouped, cols) {
    const open = this._mapOpen ?? new Set();
    const toggle = (tile) => {
      const next = new Set(open);
      next.has(tile) ? next.delete(tile) : next.add(tile);
      this._mapOpen = next;
    };
    return html`${grouped.map(([grp, tiles]) => html`
      <div class="mgrp" style="padding:8px 4px 2px">${grp}</div>
      ${tiles.map((tile) => {
        const withAccess = cols.filter((u) => m.matrix?.[u.id]?.[tile]);
        const isOpen = open.has(tile);
        return html`
          <div class="maprow">
            <button class="act" style="width:20px" title=${isOpen ? 'collapse' : 'who has access'}
              @click=${() => toggle(tile)}>${isOpen ? '▾' : '▸'}</button>
            <span class="mono" style="flex:1">${tile}</span>
            ${this._ownerCell(m, tile)}
            <span class="muted" style="white-space:nowrap; cursor:pointer" @click=${() => toggle(tile)}>
              ${withAccess.length} user${withAccess.length === 1 ? '' : 's'}</span>
          </div>
          ${this._ownerEdit?.tile === tile ? this._ownerEditor(m) : nothing}
          ${isOpen ? html`<table class="mapsub">
            ${withAccess.length ? withAccess.map((u) => {
              const c = m.matrix[u.id][tile];
              const sel = this._mapSel?.user === u.id && this._mapSel?.tile === tile;
              return html`<tr class=${sel ? 'msel' : ''} style="cursor:pointer"
                  @click=${() => { this._mapSel = { user: u.id, tile }; }}>
                <td class="mono">${u.id}</td>
                <td>${this._lvChip(c.level)} ${c.level}</td>
                <td class="muted">${this._srcLabel(c.explain?.[0]?.source ?? '')}</td>
              </tr>`;
            }) : html`<tr><td class="muted">no regular users reach this tile</td></tr>`}
          </table>` : nothing}`;
      })}`)}`;
  }

  _mapView() {
    const m = this._matrix;
    const allCols = (m?.users ?? []).filter((u) => u.role !== 'admin');
    const admins = (m?.users ?? []).filter((u) => u.role === 'admin').map((u) => u.id);
    const tq = (this._mapTileQ ?? '').trim().toLowerCase();
    const uq = (this._mapUserQ ?? '').trim().toLowerCase();
    const cols = uq ? allCols.filter((u) =>
      u.id.toLowerCase().includes(uq) || (u.name ?? '').toLowerCase().includes(uq)) : allCols;
    const grouped = this._mapGroups(m, tq);
    const nTiles = grouped.reduce((n, [, ts]) => n + ts.length, 0);
    // The wide matrix stops scaling past ~10 user columns; big (or force-
    // toggled) workspaces get the by-tile list instead.
    const layout = this._mapLayout === 'matrix' || this._mapLayout === 'list'
      ? this._mapLayout : (cols.length > 10 ? 'list' : 'matrix');
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
        each tile, and who OWNS it (transfer = reassign, with an impact
        preview). <span class="lv lv-read">r</span> read ·
        <span class="lv lv-write">w</span> write ·
        <span class="lv lv-terminal">t</span> terminal (root shell) ·
        <span class="lv lv-none">·</span> none.
        ${layout === 'matrix' ? 'Click a cell to see WHY.'
          : 'Expand a tile to see who reaches it; click a row for the full derivation.'}
        Workspace admins (${admins.length ? admins.join(', ') : 'root token only'})
        hold terminal everywhere and are omitted; chrome (root/shell) is always
        viewable and outside the model.</p>
      ${!m ? html`<p class="muted">loading…</p>` : !allCols.length
        ? html`<p class="muted">No regular users yet — add some in the users tab.</p>`
        : html`
        <div class="row" style="margin:6px 0; flex-wrap:wrap">
          <input placeholder="filter tiles / owner…" .value=${this._mapTileQ ?? ''}
            @input=${(e) => { this._mapTileQ = e.target.value; }} style="width:170px">
          <input placeholder="filter users…" .value=${this._mapUserQ ?? ''}
            @input=${(e) => { this._mapUserQ = e.target.value; }} style="width:130px">
          <select title="layout" @change=${(e) => { this._mapLayout = e.target.value; }}>
            ${[['auto', `auto (${cols.length > 10 ? 'by-tile list' : 'matrix'})`],
               ['matrix', 'matrix'], ['list', 'by-tile list']].map(([v, l]) =>
              html`<option value=${v} ?selected=${(this._mapLayout ?? 'auto') === v}>${l}</option>`)}
          </select>
          <span class="muted" style="font-size:11px">${nTiles} tile${nTiles === 1 ? '' : 's'} ·
            ${cols.length}/${allCols.length} user${allCols.length === 1 ? '' : 's'}</span>
          ${this._mapHiddenCount(m) ? html`<label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center">
            <input type="checkbox" .checked=${!!this._showHidden}
              @change=${(e) => { this._showHidden = e.target.checked; }}> show hidden (${this._mapHiddenCount(m)})</label>` : nothing}
        </div>
        ${!cols.length ? html`<p class="muted">no users match the filter</p>`
          : layout === 'list' ? this._mapList(m, grouped, cols)
          : this._mapMatrix(m, grouped, cols)}
        ${this._mapDetail()}`}
    `;
  }

  // ---- owner reassignment (D39, plans/transfer.md): picker → preview → confirm ----
  _xferReport(rep) {
    if (!rep) return nothing;
    const lv = rep.callerLevel;
    return html`<div style="margin-top:5px; font-size:11.5px">
      ${lv && lv.before !== lv.after ? html`<div>your access: <b>${lv.before || 'none'}</b> → <b>${lv.after || 'none'}</b></div>` : nothing}
      ${(rep.deadBindings ?? []).map((b) => html`<div style="color:var(--bx-red,#e5484d)">
        binding <span class="mono">${b.slot}</span> will be <b>UNBOUND</b>: ${b.reason}</div>`)}
      ${(rep.deadGrants ?? []).map((g) => html`<div style="color:var(--bx-red,#e5484d)">
        grant <span class="mono">${g.target}:${g.role}</span> becomes inert: ${g.reason}</div>`)}
      ${(rep.planeChanges ?? []).map((s) => html`<div class="muted">${s}</div>`)}
      ${(rep.unbound ?? []).length ? html`<div>unbound: ${rep.unbound.map((s) => html`<span class="pill mono">${s}</span>`)}</div>` : nothing}
    </div>`;
  }

  _ownerEditor(m) {
    const oe = this._ownerEdit;
    if (!oe) return nothing;
    const cur = m?.owners?.[oe.tile] ?? '';
    const opts = [{ value: '', label: '— workspace —' },
      ...(this._users ?? []).map((u) => ({ value: 'user:' + u.id, label: 'user: ' + u.id })),
      ...(this._orgs ?? []).map((o) => ({ value: 'org:' + o.id, label: 'org: ' + o.id }))];
    const pick = (to) => { this._ownerEdit = { tile: oe.tile, to, rep: null, perr: null }; };
    return html`<div style="padding:6px 8px; border:1px solid var(--bx-accent,#f5a623); border-radius:6px; margin:2px 0">
      <div class="row">owner of <span class="mono">${oe.tile}</span>:
        <span class="mono">${cur || 'workspace'}</span> →
        <select @change=${(e) => pick(e.target.value)}>
          ${opts.map((o) => html`<option value=${o.value} ?selected=${oe.to === o.value}>${o.label}</option>`)}
        </select>
        <button class="act" ?disabled=${oe.to === cur} @click=${async () => {
          try {
            const rep = await api(`/owner/preview?tile=${encodeURIComponent(oe.tile)}&to=${encodeURIComponent(oe.to)}`);
            this._ownerEdit = { ...oe, rep, perr: null };
          } catch (e) { this._ownerEdit = { ...oe, rep: null, perr: String(e.message ?? e) }; }
        }}>preview</button>
        ${oe.rep ? html`<button class="go" @click=${async () => {
          try {
            const done = await api('/owner', { method: 'POST', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ tile: oe.tile, to: oe.to }) });
            this._ownerEdit = null;
            this._err = '';
            this._notice = `${oe.tile} → ${oe.to || 'workspace'}`
              + ((done?.unbound ?? []).length ? ` — unbound: ${done.unbound.join(', ')}` : '');
            setTimeout(() => { this._notice = ''; }, 6000);
            this._loadMap(true);
          } catch (e) { this._ownerEdit = { ...oe, perr: String(e.message ?? e) }; }
        }}>transfer</button>` : nothing}
        <button @click=${() => { this._ownerEdit = null; }}>cancel</button>
      </div>
      ${oe.perr ? html`<div class="err" style="margin-top:4px">${oe.perr}</div>` : nothing}
      ${this._xferReport(oe.rep)}
    </div>`;
  }

  // ---- ownership & organisations (docs/auth.md; plans/ownership.md D24–D28) ----
  // Orgs are flat member lists with org-wide roles on org-OWNED tiles; the
  // ws-admin delegates approval via allowances/permission sets. This tab is
  // the workspace-admin console — org admins use tiles/organisations.
  async _orgAPI(method, path, body) {
    try {
      await api(path, body === undefined ? { method }
        : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }

  // ---- policy-row editor (workspace + per-org ceilings, D20) ----
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
          <td><input size="14" placeholder="* (all covered tiles)" .value=${r.tiles}
                @input=${(e) => upd(i, { tiles: e.target.value })}></td>
          <td style="white-space:nowrap">${['net', 'gpu', 'xbin-caps', 'ingress'].map((k) => html`
            <label class="muted" style="font-size:10.5px; margin-right:5px">
              <input type="checkbox" .checked=${r.deny.includes(k)}
                @change=${(e) => upd(i, { deny: e.target.checked ? [...r.deny, k] : r.deny.filter((d) => d !== k) })}>deny ${k}</label>`)}</td>
          <td><input size="20" placeholder="mayCall: a/*, res:a/* (empty = any)" .value=${r.mayCallText}
                @input=${(e) => upd(i, { mayCallText: e.target.value })}></td>
          <td><button class="act rm" title="remove row" @click=${() => this._polSet(key, draft.filter((_, j) => j !== i))}>✕</button></td>
        </tr>`)}
      </table>
      <div style="margin-top:4px">
        <button class="act" @click=${() => this._polSet(key, [...draft, { tiles: '*', deny: [], mayCallText: '' }])}>+ row</button>
        <button class="act go" @click=${() => this._polSave(key)}>save</button>
        <button class="act" @click=${() => this._polStop(key)}>cancel</button>
        <span class="muted" style="font-size:10.5px; margin-left:6px">
          deny strips the capability; mayCall allow-lists external call targets
          (a tile's own scope is always exempt); deny beats every allowance</span>
      </div>`;
  }

  async _createOrg(f) {
    await this._orgAPI('POST', '/orgs', { id: f.id.value.trim(), name: f.orgname.value.trim() });
    if (!this._err) f.reset();
  }

  // Member presets (D25 UI): admin / developer / viewer over the three knobs.
  static PRESETS = {
    admin: { level: 'terminal', create: true, admin: true },
    developer: { level: 'terminal', create: true, admin: false },
    viewer: { level: 'read', create: false, admin: false },
  };
  _presetOf(m) {
    for (const [name, p] of Object.entries(BxAdmin.PRESETS)) {
      if (m.level === p.level && !!m.create === p.create && !!m.admin === p.admin) return name;
    }
    return 'custom';
  }

  _memberRow(o, m) {
    const save = (patch) => {
      const members = (o.members ?? []).map((x) => (x.id === m.id ? { ...x, ...patch } : x));
      return this._orgAPI('PATCH', `/orgs/${encodeURIComponent(o.id)}`, { members });
    };
    return html`<tr style=${m.suspended ? 'opacity:.55' : ''}>
      <td class="mono">${m.id}${m.suspended ? html` <span class="pill">suspended</span>` : nothing}</td>
      <td><select title="role preset" @change=${(e) => {
            const p = BxAdmin.PRESETS[e.target.value];
            if (p) save(p);
          }}>
          ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${this._presetOf(m) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
        </select></td>
      <td><select title="org-wide level on tiles the org OWNS" @change=${(e) => save({ level: e.target.value })}>
          ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${m.level === l}>${l}</option>`)}
        </select></td>
      <td><label class="muted" style="font-size:11px"><input type="checkbox" .checked=${!!m.create}
            @change=${(e) => save({ create: e.target.checked })} title="may create org-owned tiles"> create</label></td>
      <td><label class="muted" style="font-size:11px"><input type="checkbox" .checked=${!!m.admin}
            @change=${(e) => save({ admin: e.target.checked })} title="org management: members, ACLs, transfers, allowance approvals"> admin</label></td>
      <td><label class="muted" style="font-size:11px"><input type="checkbox" .checked=${!!m.suspended}
            @change=${(e) => save({ suspended: e.target.checked })}
            title="pause this membership — it confers nothing while suspended, but keeps its knobs (D34)"> susp</label></td>
      <td style="text-align:right"><button class="act rm" @click=${() => this._orgAPI('PATCH', `/orgs/${encodeURIComponent(o.id)}`,
        { members: (o.members ?? []).filter((x) => x.id !== m.id) })}>remove</button></td>
    </tr>`;
  }

  async _transferTile(tile) {
    const to = prompt(`Transfer ${tile} to (user:<id>, org:<id>, or "workspace"):`);
    if (to == null) return;
    await this._orgAPI('POST', '/owner', { tile, to: to.trim() === 'workspace' ? '' : to.trim() });
  }

  _orgCard(o) {
    const opath = `/orgs/${encodeURIComponent(o.id)}`;
    const memberIds = new Set((o.members ?? []).map((m) => m.id));
    const addable = (this._users ?? []).filter((u) => !memberIds.has(u.id));
    const setNames = Object.keys(this._permsets?.sets ?? {});
    const allowKey = `org:${o.id}:allow`;
    return html`
      <div style="border:1px solid var(--bx-border,#e4e8ed); border-radius:6px; padding:8px 10px; margin:8px 0">
        <div style="display:flex; align-items:baseline; gap:8px; flex-wrap:wrap">
          <b class="mono">${o.id}</b>
          <span class="muted">${o.name !== o.id ? o.name : ''}</span>
          <span style="flex:1"></span>
          <button class="act rm" title=${(o.ownedTiles ?? []).length ? 'transfer its owned tiles away first' : 'delete the org'}
            @click=${() => confirm(`Delete org ${o.id}?`) && this._orgAPI('DELETE', opath)}>del</button>
        </div>

        <table style="margin-top:6px">
          ${(o.members ?? []).length ? html`<tr><th>member</th><th>preset</th><th>level</th><th></th><th></th><th></th><th></th></tr>` : nothing}
          ${(o.members ?? []).map((m) => this._memberRow(o, m))}
        </table>
        <div style="margin-top:4px; display:flex; gap:6px; align-items:center">
          <select id="add-${o.id}">
            <option value="">add member…</option>
            ${addable.map((u) => html`<option value=${u.id}>${u.id}${u.name && u.name !== u.id ? ` — ${u.name}` : ''}</option>`)}
          </select>
          <button class="act go" @click=${(e) => {
            const sel = e.target.previousElementSibling;
            if (!sel.value) return;
            this._orgAPI('PATCH', opath, { members: [...(o.members ?? []), { id: sel.value, ...BxAdmin.PRESETS.developer }] });
          }}>add as developer</button>
        </div>

        <div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">delegation (ws-admin, D26/D28)</span>
          <div style="display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:3px">
            <label class="muted" style="font-size:11px">sets
              <bx-multiselect style="min-width:130px"
                .options=${setNames.map((n) => ({ value: n, label: n }))}
                .selected=${o.sets ?? []} placeholder="— none —"
                @change=${(e) => this._orgAPI('PATCH', opath, { sets: e.detail.selected })}></bx-multiselect></label>
            <label class="muted" style="font-size:11px">extra allow
              <input size="30" placeholder="net:internet, cap:containers, …" .value=${(o.allow ?? []).join(', ')}
                @change=${(e) => this._orgAPI('PATCH', opath, { allow: e.target.value.split(',').map((x) => x.trim()).filter(Boolean) })}></label>
          </div>
          ${(o.resolvedAllow ?? []).length ? html`<div style="margin-top:3px">
            <span class="muted" style="font-size:10.5px">org admins may self-approve:</span>
            ${o.resolvedAllow.map((a) => html`<span class="pill mono">${a}</span>`)}</div>`
            : html`<div class="muted" style="font-size:10.5px; margin-top:3px">no allowances — every grant/binding goes through a workspace admin</div>`}
        </div>

        ${(o.ownedTiles ?? []).length ? html`<div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">owned tiles</span>
          <div style="margin-top:3px">${o.ownedTiles.map((p) => html`
            <span class="pill mono">${p} <a class="link" title="transfer ownership"
              @click=${() => this._transferTile(p)}>⇄</a></span>`)}</div>
        </div>` : nothing}

        <div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">org policy ceiling (applies to owned tiles)</span>
          ${this._policyEditor(o.id, o.policy)}
        </div>
        <span class="muted" style="display:block; margin-top:4px; font-size:10.5px" data-k=${allowKey}>
          allowance grammar: res:/gpu:/cap:/net:internet|host|lan:…|provider:…/iface:&lt;svc&gt;/ingress:host|zone|listen:&lt;range&gt;/tile:&lt;pat&gt; — xbin is never delegable</span>
      </div>`;
  }

  // ---- permission sets (D28, ws-admin) ----
  _permSetsView() {
    const sets = this._permsets?.sets ?? {};
    const attached = this._permsets?.attachedTo ?? {};
    const editKey = (n) => `permset:${n}`;
    return html`
      <p class="muted" style="max-width:66ch">Reusable bundles of org permissions, attached to orgs
        <b>by reference</b> — edit a set once and every attached org follows. A set carries
        <b>allow</b> entries (what its orgs' admins may self-approve), <b>ceiling rows</b>
        (restrictive — a set can also impose fleet-wide denies), and member
        <b>term-api/term-net</b> flags.</p>
      ${Object.entries(sets).sort(([a], [b]) => a.localeCompare(b)).map(([name, ps]) => {
        const d = this._draft(editKey(name));
        return html`
        <div style="border:1px solid var(--bx-border,#e4e8ed); border-radius:6px; padding:8px 10px; margin:8px 0">
          <div style="display:flex; align-items:baseline; gap:8px; flex-wrap:wrap">
            <b class="mono">⛭ ${name}</b>
            ${(attached[name] ?? []).map((o) => html`<span class="pill">org ${o}</span>`)}
            ${ps.termApi ? html`<span class="pill">term-api</span>` : nothing}
            ${ps.termNet ? html`<span class="pill">term-net</span>` : nothing}
            <span style="flex:1"></span>
            <button class="act" @click=${() => this._toggleDraft(editKey(name), () => ({
              allow: (ps.allow ?? []).join(', '), termApi: !!ps.termApi, termNet: !!ps.termNet }))}>edit</button>
            <button class="act rm" title=${(attached[name] ?? []).length ? 'detach from its orgs first' : 'delete'}
              @click=${() => confirm(`Delete permission set ${name}?`) && this._orgAPI('DELETE', `/permission-sets/${encodeURIComponent(name)}`)}>del</button>
          </div>
          <div style="margin-top:3px">${(ps.allow ?? []).length
            ? ps.allow.map((a) => html`<span class="pill mono">${a}</span>`)
            : html`<span class="muted" style="font-size:11px">no allow entries</span>`}
            ${(ps.policy ?? []).length ? html`<span class="pill pol">⛔ ${ps.policy.length} ceiling row(s)</span>` : nothing}</div>
          ${d ? html`<div style="margin-top:6px; padding:6px 8px; background:var(--bx-panel-2,#f7f8fa); border-radius:6px">
            <label class="muted" style="font-size:11px">allow
              <input size="44" .value=${d.allow} @input=${(e) => this._setDraft(editKey(name), { ...d, allow: e.target.value })}></label>
            <label class="muted" style="font-size:11px"><input type="checkbox" .checked=${d.termApi}
              @change=${(e) => this._setDraft(editKey(name), { ...d, termApi: e.target.checked })}> term-api</label>
            <label class="muted" style="font-size:11px"><input type="checkbox" .checked=${d.termNet}
              @change=${(e) => this._setDraft(editKey(name), { ...d, termNet: e.target.checked })}> term-net</label>
            <button class="act go" @click=${async () => {
              await this._orgAPI('PUT', `/permission-sets/${encodeURIComponent(name)}`, {
                allow: d.allow.split(',').map((x) => x.trim()).filter(Boolean),
                policy: ps.policy ?? [], termApi: d.termApi, termNet: d.termNet,
              });
              if (!this._err) this._dropDraft(editKey(name));
            }}>save</button>
            <button class="act" @click=${() => this._dropDraft(editKey(name))}>cancel</button>
          </div>` : nothing}
        </div>`;
      })}
      ${!Object.keys(sets).length ? html`<p class="muted">No permission sets yet.</p>` : nothing}
      <h4>add set</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault();
        const f = e.target;
        this._orgAPI('PUT', `/permission-sets/${encodeURIComponent(f.name_.value.trim())}`, {
          allow: f.allow.value.split(',').map((x) => x.trim()).filter(Boolean) });
        if (!this._err) f.reset(); }}>
        <input name="name_" placeholder="set name" size="14" required>
        <input name="allow" placeholder="allow: net:internet, cap:containers" size="34">
        <button class="act go">create</button>
      </form>
      <p class="muted" style="font-size:10.5px; margin-top:6px">
        Allowance grammar: <span class="mono">res: gpu: cap: net:internet|host|lan:…|provider:…
        iface:&lt;service&gt; ingress:host:|zone:|listen:&lt;lo-hi&gt; tile:&lt;pattern&gt;</span> —
        the <span class="mono">xbin</span> capability family is never delegable.</p>`;
  }

  _orgsView() {
    const orgs = this._orgs ?? [];
    return html`
      ${this._targetDatalist()}
      <h4>organizations</h4>
      ${orgs.length ? orgs.map((o) => this._orgCard(o))
        : html`<p class="muted">No orgs. An org is a flat member list with org-wide roles on the
          tiles the org <b>owns</b> (ownership is assigned at create and transferable — D24/D25).
          Attach permission sets to delegate grant/binding approval to its admins.</p>`}

      <h4>add org</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createOrg(e.target); }}>
        <input name="id" placeholder="org id" size="14" required>
        <input name="orgname" placeholder="display name" size="14">
        <button class="act go">create</button>
      </form>

      <h4>workspace defaults</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        Baseline visibility every user gets (D27) — pattern → level.</p>
      ${this._defaultsEditor()}

      <h4>workspace policy</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        Pattern-keyed ceiling on what tiles may be granted, applied to EVERY tile (org and
        permission-set rows add on top; any deny wins; deny beats every allowance).</p>
      ${this._policyEditor('', this._wsPolicy)}

      <p class="muted" style="font-size:11px; margin-top:10px; max-width:60ch">
        Effective access is a union: workspace admin · tile OWNER (terminal) · org member level /
        org-admin terminal on org-owned tiles · org shares · a user's own entries · workspace
        defaults. Org admins manage members and org-tile ACLs in the
        <span class="mono">tiles/organisations</span> tile; permission sets, allowances, policy
        and org create/delete stay here.</p>`;
  }

  _defaultsEditor() {
    const key = 'ws:defaults';
    const d = this._draft(key);
    if (!d) {
      return html`
        ${Object.entries(this._defaults ?? {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
        ${!Object.keys(this._defaults ?? {}).length ? html`<span class="muted" style="font-size:11px">none</span>` : nothing}
        <button class="act" style="margin-left:4px" @click=${() => this._toggleDraft(key,
          () => Object.entries(this._defaults ?? {}).map(([target, level]) => ({ target, level })))}>edit</button>`;
    }
    return this._tilesEditor(key, (tiles) => this._orgAPI('PUT', '/defaults', { defaultTiles: tiles }));
  }
}

customElements.define('bx-admin', BxAdmin);
