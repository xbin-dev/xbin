/**
 * <bx-deployments component="apps/crm"> — the terminal window's Deployments
 * panel (docs/tile-deployments.md): live reload and a tile's deployments in
 * bx-prs's list-and-detail shape. A header with the live reload sentence and
 * its actions (and Undo after a code move onto the live reload target); a
 * side list ("tile-wide", one row per deployment, "+ Add deployment…"); a
 * main pane for the selected row — overview, deploy log, logs, registrations
 * (a non-primary's cron jobs and bus subscriptions fire for it unless its
 * deliveries switch is off; its interface instances and ingress hosts stay
 * dormant; Run now and "would notify"), and the view tab, which embeds a
 * non-primary deployment's frontend as <bx-frame src="<tile>+<name>">; the
 * tile-wide page holds the primary (reassign, protect) and the outbound
 * edges with their per-edge refusal and clamp counts. A zero-state tile gets
 * the one entry point: pause live reload, or add a deployment.
 *
 * The server decides; this renders: web/deploy-state.js and
 * web/deploy-panel.js turn the state
 * (GET /api/xbin/deployments?tile=, in the viewer's view: a reader gets the
 * primary only) and a dry run's impact into every word shown. Every change
 * confirms from a dry run of the exact request through the host frame's
 * dialog (`frame._ask`, set as a property by bx-frame), sends the state's
 * seq and the reviewed checkpoint, and is sent only when its `can` says yes.
 * A 409 because the record moved runs the dry run again; one because the
 * reviewed code moved refreshes the diff. Refusals show the server's text
 * inline. The state reloads on this tile's `deployments` events (250 ms
 * debounce) and after the events socket reconnects.
 *
 * Harness: testApi() (rows, select, tab, header, actions, act, diff, log,
 * edges, setEdge, registrations, runNow, wouldNotify, gitLine, view, text,
 * error). act(id) takes an action id: the header's (pause, resume,
 * resume/<name>, reloadNow, attach/<name>, undo), add, the selected row's
 * or the tile-wide page's (deploy, promote, remove, seed, reset, vaultCopy,
 * deliveries, alwaysOn, limits, open, reassign, protect, unprotect,
 * blockEdges), rollback/<checkpoint> and diff/<checkpoint> from the deploy
 * log, and confirm (the open review's Promote or Deploy).
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { unsafeHTML } from 'lit';
import { diffHTML, diffStats } from '/vendor/bx-code.js';
import { onEvent, onReconnect } from '/vendor/events-socket.js';
import { qualifiedSrc } from '/vendor/frame-info.js';
import * as dsState from '/vendor/deploy-state.js';
import * as dsPanel from '/vendor/deploy-panel.js';

const ds = { ...dsState, ...dsPanel };

const SHEET = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : { matches: false }; // the phone sheet: menus open as sheets
const ROUTE = {
  pause: 'live-reload/pause', resume: 'live-reload/resume', reloadNow: 'live-reload/now', attach: 'live-reload/attach',
  add: 'add', remove: 'remove', deploy: 'deploy', promote: 'promote', rollback: 'rollback', undo: 'rollback',
  primary: 'primary', protect: 'protect', unprotect: 'protect', edge: 'edge', deliveries: 'deliveries', alwaysOn: 'always-on',
  limits: 'limits', seed: 'seed', reset: 'reset', vaultCopy: 'vault-copy', runNow: 'run-now',
};
const HEADER_OPS = new Set(['pause', 'resume', 'reloadNow', 'attach', 'undo']);
const NO_DEPLOYMENT = new Set(['pause', 'reloadNow', 'promote', 'protect', 'unprotect', 'edge']);
const TABS = [['overview', 'overview'], ['log', 'deploy log'], ['logs', 'logs'], ['registrations', 'registrations'], ['view', 'view']];

// dryConfirm(op, body): the confirm token of a data-guarded operation
// (11-contract §1.2's table), which its dry run is judged with; {} otherwise.
const CONFIRM = { remove: 'erase', reset: 'erase-data', seed: 'copy-data', primary: 'data-stays' };
const dryConfirm = (op, body) => {
  const t = op === 'add' ? (body.data === 'seed' ? 'copy-data' : '') : CONFIRM[op];
  return t ? { confirm: t } : {};
};

async function post(path, body) {
  try {
    const r = await fetch(`/api/xbin/deployments/${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    return r.ok ? { status: r.status, body: j } : { status: r.status, error: j.error || `${r.status} ${r.statusText}` };
  } catch (e) {
    return { status: 0, error: String(e?.message || e) };
  }
}

async function getJSON(url) {
  try {
    const r = await fetch(url, { cache: 'no-store' });
    const j = await r.json().catch(() => null);
    return r.ok ? { body: j } : { status: r.status, error: j?.error || '' };
  } catch (e) {
    return { status: 0, error: String(e?.message || e) };
  }
}

const dep = (s, name) => s?.deployments?.find((d) => d.name === name) || null;
const glyphed = (text) => {
  const m = /^([●📌⇡⇈🛡↗]\S*)\s(.*)$/u.exec(text || '');
  return m ? html`<span aria-hidden="true">${m[1]}</span> ${m[2]}` : text;
};

export class BxDeployments extends LitElement {
  static properties = {
    component: { type: String },
    frame: { attribute: false }, // the host <bx-frame>: its dialog asks every question
    // the active tab's target — 'primary', a deployment's name, 'off', or
    // null (no tab): its row carries the Dev API tag. bx-frame sets it on
    // every render, so it follows tab switches; unset, it is read off the frame.
    target: { type: String },
    _state: { state: true },
    _loaded: { state: true },
    _loadError: { state: true },
    _sel: { state: true },    // the selected row: a deployment's name, '' = tile-wide
    _tab: { state: true },
    _log: { state: true },    // the selected deployment's deploy log (newest first), null: not loaded
    _undo: { state: true },   // the live reload target's newest entry, when a code move paused it
    _review: { state: true }, // the open diff: {op, from, to, base, head, patch, cpFrom, cpTo, stats, stale, note, error}
    _busy: { state: true },
    _error: { state: true },  // the inline refusal
    _said: { state: true },   // the last result, in the polite live region
    _drill: { state: true },  // narrow: the main pane is shown instead of the side list
  };

  static styles = [scrollCss, css`
    :host { display: flex; flex-direction: column; height: 100%; min-height: 0; font: 12px/1.5 var(--bx-mono, ui-monospace, monospace);
      color: var(--bx-text, #d4d9e0); background: var(--bx-panel, #23272e); }
    button { font: inherit; color: inherit; cursor: pointer; }
    button[disabled] { opacity: .5; cursor: default; }
    .head { flex: none; padding: 7px 12px; border-bottom: 1px solid var(--bx-border, #363c45); display: flex; flex-direction: column; gap: 5px; }
    .sentence { color: var(--bx-text, #d4d9e0); }
    .btns { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
    .btn { background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45); border-radius: 4px; padding: 3px 9px; white-space: nowrap; }
    .btn:hover:not([disabled]) { border-color: var(--bx-accent, #f5a623); }
    .btn.danger { color: var(--bx-red, #ef5350); }
    .btn[role=switch][aria-checked=true] { border-color: var(--bx-green, #4caf50); color: var(--bx-green, #4caf50); }
    .why { color: var(--bx-muted, #868f9a); font-size: 10.5px; }
    .said { color: var(--bx-muted, #868f9a); font-size: 11px; min-height: 0; }
    .err { color: var(--bx-red, #ef5350); white-space: pre-wrap; }
    /* the panel's own width decides the narrow layouts (container queries,
       below), so it works in a pane beside the terminal as on a phone */
    .body { flex: 1; display: flex; min-height: 0; container: dbody / inline-size; }
    .side { width: 230px; flex: none; display: flex; flex-direction: column; overflow: auto; border-right: 1px solid var(--bx-border, #363c45); }
    .row { display: block; width: 100%; text-align: left; background: none; border: 0; padding: 5px 8px;
      border-bottom: 1px solid color-mix(in srgb, var(--bx-border, #363c45) 40%, transparent); }
    .row:hover { background: var(--bx-panel-2, #2b3038); }
    .row.on { background: color-mix(in srgb, var(--bx-accent, #f5a623) 22%, transparent); }
    /* a row's name and tags: each tag one compact line; a tag that doesn't
       fit moves to the next line whole */
    .row .t { display: flex; flex-wrap: wrap; gap: 2px 6px; align-items: baseline; }
    .row .t .nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .row .m { color: var(--bx-muted, #868f9a); font-size: 10.5px; display: block; }
    .row.add { color: var(--bx-accent, #f5a623); }
    .pill { font-size: 9.5px; letter-spacing: .04em; padding: 0 5px; border-radius: 3px; border: 1px solid var(--bx-border, #363c45); color: var(--bx-muted, #868f9a);
      white-space: nowrap; flex: none; }
    .pill.primary { color: var(--bx-accent, #f5a623); }
    .pill.target { color: var(--bx-green, #4caf50); }
    .pill.lr { color: var(--bx-text, #d4d9e0); }
    .bad { color: var(--bx-red, #ef5350); }
    .st-healthy { color: var(--bx-green, #4caf50); } .st-building { color: var(--bx-accent, #f5a623); } .st-failed { color: var(--bx-red, #ef5350); }
    .main { flex: 1; min-width: 0; display: flex; flex-direction: column; min-height: 0; container: dmain / inline-size; }
    .title { padding: 7px 12px 0; font-weight: 600; display: flex; gap: 8px; align-items: baseline; }
    .tabs { display: flex; flex: none; border-bottom: 1px solid var(--bx-border, #363c45); padding: 0 8px; overflow-x: auto; }
    .tabs button { background: none; border: 0; color: var(--bx-muted, #868f9a); padding: 6px 8px; border-bottom: 2px solid transparent; white-space: nowrap; }
    .tabs button.on { color: var(--bx-text, #d4d9e0); border-bottom-color: var(--bx-accent, #f5a623); }
    .pane { flex: 1; overflow: auto; min-height: 0; padding: 8px 12px; }
    .pane.flush { padding: 0; display: flex; flex-direction: column; }
    .kv { display: grid; grid-template-columns: max-content 1fr; gap: 3px 14px; }
    .kv .k { color: var(--bx-muted, #868f9a); }
    .muted { color: var(--bx-muted, #868f9a); }
    .git { margin-top: 10px; padding: 6px 9px; background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45); border-radius: 5px; }
    .tbl { display: grid; gap: 0; }
    .tr { display: grid; grid-template-columns: var(--cols); gap: 10px; align-items: center; padding: 4px 0;
      border-bottom: 1px solid color-mix(in srgb, var(--bx-border, #363c45) 40%, transparent); }
    .tr.hd { color: var(--bx-muted, #868f9a); font-size: 10px; text-transform: uppercase; letter-spacing: .05em; }
    .tr .lb { display: none; color: var(--bx-muted, #868f9a); font-size: 10px; }
    select { font: inherit; color: inherit; background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45); border-radius: 4px; padding: 2px 4px; max-width: 100%; }
    h4 { margin: 12px 0 4px; font-size: 11px; color: var(--bx-muted, #868f9a); font-weight: 600; }
    .actions { flex: none; position: sticky; bottom: 0; padding: 7px 12px; border-top: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel, #23272e); display: flex; flex-direction: column; gap: 4px;
      /* a short or narrow pane: the buttons and their reasons scroll, never cover the title and tabs */
      max-height: 45%; overflow: auto; }
    .zero { padding: 12px 16px; display: flex; flex-direction: column; gap: 12px; max-width: 640px; }
    .zero b { color: var(--bx-text, #d4d9e0); }
    .view { flex: 1; min-height: 0; display: flex; flex-direction: column; }
    .view .vlabel { flex: none; padding: 2px 10px; font-size: 10.5px; color: var(--bx-accent, #f5a623); border-bottom: 1px solid var(--bx-border, #363c45); }
    .view bx-frame { flex: 1; min-height: 0; }
    pre.diff { margin: 0; padding: 8px 0; white-space: pre; tab-size: 4; overflow: auto; }
    .diff .fh { color: var(--bx-muted, #868f9a); display: block; }
    .diff .h { color: #61afef; display: block; }
    .diff .d { color: #98c379; display: block; background: color-mix(in srgb, #98c379 10%, transparent); }
    .diff .a { color: #e06c75; display: block; background: color-mix(in srgb, #e06c75 10%, transparent); }
    .diff .ctx { display: block; color: #abb2bf; }
    .back { display: none; }
    /* narrow (a phone, a pane beside the terminal): the side list, or the
       selected row's page with a way back */
    @container dbody (max-width: 720px) {
      .side { width: auto; flex: 1; border-right: 0; }
      .body.drill .side, .body:not(.drill) .main { display: none; }
      .back { display: block; }
    }
    @container dmain (max-width: 560px) {
      .tr { grid-template-columns: 1fr; gap: 2px; }
      .tr.hd { display: none; }
      .tr .lb { display: block; }
    }
    @media (max-width: 820px) {
      .btn, .row, .tabs button, select { min-height: 44px; }
    }
    @media (prefers-reduced-motion: reduce) { * { transition: none !important; } }
  `];

  constructor() {
    super();
    this._state = null; this._loaded = false; this._loadError = ''; this._sel = undefined; this._tab = 'overview';
    this._log = null; this._undo = null; this._review = null; this._busy = false; this._error = ''; this._said = ''; this._drill = false;
    this._gen = 0; this._rgen = 0; this._viewing = undefined;
  }

  connectedCallback() {
    super.connectedCallback();
    this._offEvents = onEvent((e) => this._event(e));
    this._offReconnect = onReconnect?.(() => this._load());
    if (this.component) this._load();
  }
  disconnectedCallback() {
    this._offEvents?.(); this._offReconnect?.(); clearTimeout(this._timer);
    super.disconnectedCallback();
  }
  updated(ch) {
    if (ch.has('component') && this.component && this.component !== this._for) {
      Object.assign(this, { _state: null, _sel: undefined, _log: null, _undo: null, _review: null, _error: '', _said: '' });
      this._load();
    }
  }

  // ---- loading ----

  _opts() {
    const f = this.frame, t = f?._sessions?.[f._active];
    const target = this.target !== undefined ? this.target || undefined : !t ? undefined : t.api === false ? 'off' : t.deployment || 'primary';
    return { now: Date.now(), viewing: this._viewing || '', target, undo: this._undo, entry: this._log?.find((e) => e.result === 'ok') };
  }

  async _load() {
    const tile = this.component;
    if (!tile) return null;
    const gen = ++this._gen;
    this._for = tile;
    // a deployment's frame (apps/crm+dev) has no panel state, and its ref never
    // goes into a query (D127j)
    const r = await qualifiedSrc(tile) ? { body: null } : await getJSON(`/api/xbin/deployments?tile=${encodeURIComponent(tile)}`);
    if (gen !== this._gen || tile !== this.component) return this._state;
    const s = r.body?.tile === tile ? r.body : null;
    this._state = s; this._loaded = true; this._loadError = s ? '' : r.error || '';
    if (s?.caller?.readOnly && this._viewing === undefined) this._learnViewing();
    const names = (s?.record ? s.deployments : []).map((d) => d.name);
    if (this._sel === undefined || (this._sel && !names.includes(this._sel))) {
      this._sel = names.includes(s?.liveReload) ? s.liveReload : names.includes(s?.primary) ? s.primary : undefined;
      if (this._tab === 'view') this._tab = 'overview';
    }
    if (s?.view === 'reader') this._said = ''; // a result may name what this viewer no longer sees
    this._loadLog();
    this._loadUndo();
    return s;
  }

  _learnViewing() {
    this._viewing = '';
    getJSON('/api/xbin/whoami').then((w) => { this._viewing = w.body?.readOnly ? String(w.body.id || '') : ''; this.requestUpdate(); });
  }

  async _loadLog() {
    const s = this._state, name = this._sel;
    if (!s?.record || !name || s.view === 'reader') { this._log = null; return; }
    const r = await getJSON(`/api/xbin/deployments/log?${new URLSearchParams({ tile: this.component, deployment: name, limit: '50' })}`);
    if (this._sel === name) this._log = r.body?.entries || [];
  }

  // Undo needs what the move replaced: the live reload target's newest entry.
  async _loadUndo() {
    const s = this._state, L = s?.lastLiveReload, how = dep(s, L)?.lastDeploy?.how;
    if (!s?.record || s.liveReload !== '' || s.view === 'reader' || !['deploy', 'promote', 'rollback'].includes(how)) { this._undo = null; return; }
    const r = await getJSON(`/api/xbin/deployments/log?${new URLSearchParams({ tile: this.component, deployment: L, limit: '1' })}`);
    this._undo = r.body?.entries?.[0] || null;
  }

  _event(e) {
    if (e?.type !== 'deployments' || e.component !== this.component) return;
    const d = e.data || {};
    if (d.op === 'work-tree') {
      const a = ds.applyEvent(this._state, d);
      if (a.state !== this._state) this._state = a.state;
      const r = this._review;
      if (r && !r.stale && (r.head === 'work-tree' || (r.op === 'promote' && dep(this._state, r.from)?.liveReload))) this._review = { ...r, stale: true };
      return;
    }
    if (d.op === 'deploy' && ['ok', 'failed', 'cancelled'].includes(d.result)) this._said = ds.deployText(d, this._state) || this._said;
    clearTimeout(this._timer);
    this._timer = setTimeout(() => this._load(), 250);
  }

  // ---- operations ----

  _body(op, x) {
    const b = { tile: this.component };
    if (x.deployment && !NO_DEPLOYMENT.has(op)) b.deployment = x.deployment;
    if (op === 'promote') Object.assign(b, { from: x.from, to: x.to }, x.reviewed ? { expect: x.reviewed } : {});
    if (op === 'protect' || op === 'unprotect') b.on = op === 'protect';
    if (op === 'deliveries' || op === 'alwaysOn') b.on = !!x.on;
    if (op === 'edge') Object.assign(b, { edge: x.edge, policy: x.policy });
    if (op === 'add') Object.assign(b, { from: x.add.from, data: x.add.data, attach: !!x.add.attach });
    if (op === 'rollback' || op === 'undo' || (op === 'deploy' && (x.checkpoint || x.reviewed))) b.checkpoint = x.checkpoint || x.reviewed;
    if (op === 'runNow') b.job = x.job;
    if (op === 'limits') b.limits = {};
    return b;
  }

  // _ask(spec): the host frame's dialog; focus returns to the control that opened it.
  async _ask(spec) {
    const back = this.shadowRoot?.activeElement;
    try { return await this.frame._ask(spec); } finally { back?.focus?.(); }
  }

  // _run(op, x): a dry run of the exact request renders the confirmation
  // (x.quiet: the safe direction, no dialog); the confirmed request carries
  // the dry run's seq and what the dialog showed. x.onRefuse(error) takes a
  // refusal instead of the inline error (the add form re-opens with it).
  async _run(op, x = {}) {
    const f = this.frame;
    if (this._busy || !this._state || (!x.quiet && !f?._ask)) return null;
    this._busy = true; this._error = ''; this._said = '';
    try {
      for (let attempt = 0; attempt < 3; attempt++) {
        const s = this._state, body = this._body(op, x), seq = (st) => { if (op !== 'runNow' && Number.isInteger(st?.seq)) body.seq = st.seq; };
        seq(s);
        if (!x.quiet) {
          // judged as for real (11-contract §1.2): a guarded op's dry run
          // carries the confirm token its confirmed request will send
          const dry = await post(ROUTE[op], { ...body, ...(op === 'vaultCopy' ? { all: true } : {}), ...dryConfirm(op, body), dryRun: true });
          if (dry.error) {
            if (ds.conflict(dry.status, dry.error) === 'seq' && attempt < 2) { await this._load(); continue; }
            return this._refused(op, dry.error, x, dry.status);
          }
          const cur = dry.body.state?.tile === this.component ? dry.body.state : s;
          this._state = cur;
          const c = ds.confirmation(op, { state: cur, impact: dry.body.impact, deployment: x.deployment, ...x }, this._opts());
          let values, err;
          for (;;) {
            const a = await this._ask(err ? { ...c.spec, error: err } : c.spec);
            if (a?.button !== 'ok') return null;
            values = a.values || {};
            if (c.required.every((n) => values[n])) break;
            err = ds.REASON.tick;
          }
          const extra = c.send(values);
          if (!extra) return null;
          seq(cur);
          Object.assign(body, extra);
        }
        const res = await post(ROUTE[op], body);
        if (res.error) {
          if (ds.conflict(res.status, res.error) === 'seq' && attempt < 2) { await this._load(); continue; }
          return this._refused(op, res.error, x, res.status);
        }
        if (res.body.state?.tile === this.component) this._state = res.body.state;
        this._said = ds.result(op, res.body, { deployment: x.deployment || x.to, prev: s }) || '';
        if (op === 'add') this._sel = x.deployment;
        if (op === 'promote' || op === 'deploy') this._review = null;
        this._load();
        return res.body;
      }
      return null;
    } finally {
      this._busy = false;
    }
  }

  _refused(op, error, x, status) {
    if (ds.conflict(status, error) === 'expect' && this._review) {
      this._openReview({ ...this._review, note: ds.REASON.reviewAgain });
      return null;
    }
    if (x.onRefuse) { x.onRefuse(error); return null; }
    this._error = error;
    this._load();
    return null;
  }

  async _add(error) {
    const s = this._state;
    if (!s || !this.frame?._ask) return null;
    const recent = [...new Set((this._log || []).map((e) => e.checkpoint).filter(Boolean))].slice(0, 5);
    const a = await this._ask(ds.addDialog(s, error, recent));
    if (a?.button !== 'ok') return null;
    const v = a.values || {}, name = String(v.name || '').trim();
    return this._run('add', { deployment: name, add: { from: v.from, data: v.data, attach: !!v.attach }, onRefuse: (err) => this._add(err) });
  }

  async _openReview(r) {
    const base = `deployment:${r.to}`, head = r.op === 'promote' ? `deployment:${r.from}` : r.op === 'deploy' ? 'work-tree' : r.checkpoint;
    const gen = ++this._rgen;
    this._review = { op: r.op, from: r.from, to: r.to, checkpoint: r.checkpoint, base, head, patch: null, stale: false, note: r.note || '' };
    try {
      const res = await fetch(`/api/xbin/deployments/diff?${new URLSearchParams({ tile: this.component, from: base, to: head })}`);
      const text = await res.text();
      if (gen !== this._rgen) return;
      if (!res.ok) {
        let msg = '';
        try { msg = JSON.parse(text).error; } catch { /* not JSON */ }
        this._review = { ...this._review, error: msg || `${res.status} ${res.statusText}` };
        return;
      }
      const h = (k) => res.headers.get(k) || '';
      this._review = { ...this._review, patch: text, cpFrom: h('X-XBin-Checkpoint-From'), cpTo: h('X-XBin-Checkpoint-To'), stats: diffStats(text) };
    } catch (e) {
      if (gen === this._rgen) this._review = { ...this._review, error: String(e?.message || e) };
    }
  }

  _reviewOk() {
    const r = this._review;
    if (!r || r.op === 'diff' || r.patch == null) return { enabled: false, why: '' };
    if (!r.stats.files) return { enabled: false, why: ds.REASON.emptyDiff(r.to) };
    return r.op === 'promote' ? ds.control(this._state, 'promoteTo', r.to, this._opts()) : ds.control(this._state, 'deploy', r.to, this._opts());
  }

  _confirmReview() {
    const r = this._review;
    if (!this._reviewOk().enabled) return false;
    return r.op === 'promote' ? this._run('promote', { from: r.from, to: r.to, reviewed: r.cpTo }) : this._run('deploy', { deployment: r.to, reviewed: r.cpTo });
  }

  async _reassign() {
    const s = this._state;
    if (!this.frame?._ask) return null;
    const ys = dsPanel.reassignable(s);
    let Y = ys[0];
    if (ys.length > 1) {
      const a = await this._ask({ title: 'Reassign the primary…', fields: [{ name: 'to', label: 'Deployment', type: 'select', value: Y, options: ys }],
        buttons: [{ label: 'Cancel', value: null }, { label: 'Reassign the primary…', value: 'ok', primary: true }] });
      if (a?.button !== 'ok') return null;
      Y = a.values?.to || Y;
    }
    return Y ? this._run('primary', { deployment: Y }) : null;
  }

  async _vaultCopy(name) {
    const r = await getJSON(`/api/xbin/vault/${this.component}`);
    const keys = (r.body?.keys || []).map((k) => (typeof k === 'string' ? k : k?.name)).filter(Boolean);
    return this._run('vaultCopy', { deployment: name, keys });
  }

  async _blockEdges() {
    for (const e of ds.edgeRows(this._state, this._opts())) {
      if (e.enabled && e.value !== 'block' && e.values.some((v) => v.value === 'block')) await this._run('edge', { edge: e.id, policy: 'block', quiet: true });
    }
    return true;
  }

  _setEdge(id, value) {
    const s = this._state, row = ds.edgeRows(s, this._opts()).find((e) => e.id === id);
    if (!row?.enabled || !row.values.some((v) => v.value === value) || value === row.value) return false;
    return this._run('edge', { edge: id, policy: value, quiet: !ds.widens(s, id, value) });
  }

  _runNow(job) {
    const s = this._state, name = this._sel, r = ds.registrationRows(s, name, this._opts()).find((x) => x.kind === 'cron' && x.name === job);
    if (!r?.runNow?.enabled) return false;
    return this._run('runNow', { deployment: name, job, quiet: !ds.asksToRun(s, name) });
  }

  // act(id): see the file comment. false: no such usable action.
  _act(id) {
    const s = this._state, o = this._opts(), [op, arg] = String(id).split(/\/(.*)/s), X = this._sel || '';
    if (!s) return false;
    if (HEADER_OPS.has(op)) {
      const acts = ds.panelHeader(s, o)?.actions || [], a = acts.find((x) => x.id === id) || acts.flatMap((x) => x.items || []).find((x) => x.id === id);
      const it = a?.items ? a.items[0] : a;
      if (!it?.enabled) return false;
      if (op === 'undo') return this._run('undo', { deployment: this._undo.deployment, checkpoint: this._undo.previous, entry: this._undo });
      return this._run(op, { deployment: it.id.split('/')[1] });
    }
    if (op === 'add') return ds.control(s, 'add', null, o).enabled ? this._add() : false;
    if (op === 'confirm') return this._confirmReview();
    if (op === 'rollback' || op === 'diff') {
      const e = (this._log || []).find((x) => x.checkpoint === arg), row = ds.logRows(s, X, this._log, o).find((x) => x.checkpoint === arg);
      if (!e) return false;
      if (op === 'diff') return this._openReview({ op: 'diff', to: X, checkpoint: arg });
      return row?.rollback?.enabled ? this._run('rollback', { deployment: X, checkpoint: arg, entry: e }) : false;
    }
    const a = ds.panelActions(s, X, o).find((x) => x.id === op);
    if (!a?.enabled) return false;
    switch (op) {
      case 'deploy': return dep(s, X)?.primary ? this._openReview({ op: 'deploy', to: X }) : this._run('deploy', { deployment: X });
      case 'promote': return this._openReview({ op: 'promote', from: X, to: a.to });
      case 'deliveries': case 'alwaysOn': return this._run(op, { deployment: X, on: !a.on, quiet: a.on });
      case 'open': window.open(ds.overview(s, X, o).url, '_blank'); return true;
      case 'reassign': return this._reassign();
      case 'protect': case 'unprotect': return this._run(op);
      case 'blockEdges': return this._blockEdges();
      case 'vaultCopy': return this._vaultCopy(X);
      default: return this._run(op, { deployment: X }); // remove, seed, reset, limits
    }
  }

  _select(name) {
    this._sel = name; this._review = null; this._error = ''; this._log = null; this._drill = true;
    if (name === '' || (this._tab === 'view' && dep(this._state, name)?.primary)) this._tab = 'overview';
    this._loadLog();
  }

  _openMenu(e, items) {
    const f = this.frame;
    if (!f) return;
    const menu = items.map((it) => ({ label: it.label, disabled: !it.enabled, ...(it.why ? { hint: it.why } : {}), ...(it.title ? { title: it.title } : {}),
      ...(it.enabled ? { action: () => this._act(it.id) } : {}) }));
    f._menu = { items: menu, anchor: e.currentTarget.getBoundingClientRect(), sheet: SHEET.matches };
  }

  // ---- rendering ----

  _button(a, onClick) {
    const click = onClick || ((e) => (a.items ? this._openMenu(e, a.items) : this._act(a.id)));
    const sw = typeof a.on === 'boolean';
    return html`<button class=${'btn' + (/^(remove|reset)$/.test(a.id) ? ' danger' : '')} ?disabled=${!a.enabled || this._busy}
        title=${a.why || a.title || ''} aria-label=${a.label.replace(/^[⇡↗]\s*|\s*↗$/u, '')} role=${sw ? 'switch' : nothing} aria-checked=${sw ? String(a.on) : nothing}
        aria-haspopup=${a.items ? 'menu' : nothing} @click=${click}>${glyphed(a.label)}</button>`;
  }

  // the reasons of disabled controls, visible (touch has no tooltips)
  _whys(list) {
    const w = [...new Set(list.filter((a) => !a.enabled && a.why).map((a) => a.why))];
    return w.length ? html`<div class="why">${w.map((t) => html`<div>${t}</div>`)}</div>` : nothing;
  }

  _header(s, o) {
    const h = ds.panelHeader(s, o);
    return html`<div class="head">
      ${s.record ? html`<div class="sentence">${glyphed(h.text)}</div>` : nothing}
      ${!s.record || !h.actions.length ? nothing : html`<div class="btns">${h.actions.map((a) => this._button(a))}</div>${this._whys(h.actions)}`}
      ${this._error ? html`<div class="err" role="alert">${this._error}</div>` : nothing}
      <div class="said" aria-live="polite">${this._said}</div>
    </div>`;
  }

  _zero(s, o) {
    return html`<div class="zero">${ds.zeroPanel(s, o).map((p) => html`<div>
      <div><b>${p.lead}</b> — ${p.text}</div>
      ${p.id ? html`<div class="btns" style="margin-top:6px">${this._button({ ...p, label: p.lead })}</div>${p.enabled || !p.why ? nothing : html`<div class="why">${p.why}</div>`}` : nothing}
    </div>`)}</div>`;
  }

  _side(s, o) {
    const add = ds.control(s, 'add', null, o);
    return html`<nav class="side" aria-label="Deployments">
      <button class=${'row' + (this._sel === '' ? ' on' : '')} @click=${() => this._select('')}><span class="t">tile-wide</span></button>
      ${ds.panelRows(s, o).map((r) => html`<button class=${'row' + (this._sel === r.name ? ' on' : '')} @click=${() => this._select(r.name)}>
        <span class="t"><span class="nm">${r.name}</span>${r.primary ? html`<span class="pill primary">primary</span>` : nothing}${r.protected ? html`<span aria-label="protected">🛡</span>` : nothing}
          ${r.target ? html`<span class="pill target" title=${ds.TAG.devApiTitle(s, r.name)}>${ds.TAG.devApi}</span>` : nothing}
          ${r.liveReload ? html`<span class="pill lr" title=${ds.TAG.liveReloadTitle(s, r.name)}>${ds.TAG.liveReload}</span>` : nothing}</span>
        <span class="m">${glyphed(r.code)} · <span class=${'st-' + r.status.split(' ')[0]}>${r.status}</span>${r.lastDeployFailed ? html` · <span class="bad">last deploy failed</span>` : nothing}</span>
        ${r.data || r.deliveries ? html`<span class="m">${[r.data, r.deliveries].filter(Boolean).join(' · ')}</span>` : nothing}
      </button>`)}
      <button class="row add" ?disabled=${!add.enabled || this._busy} title=${add.why || nothing}
        @click=${() => this._act('add')}>+ Add deployment…</button>
      ${add.enabled || !add.why ? nothing : html`<div class="why" style="padding:0 8px">${add.why}</div>`}
    </nav>`;
  }

  _tileWide(s, o) {
    const acts = ds.panelActions(s, '', o), rows = ds.edgeRows(s, o), P = s.primary || 'main', c = s.caps;
    return html`<div class="title">Primary: ${P}${s.protectedPrimary ? html` · <span aria-hidden="true">🛡</span> protected` : nothing}</div>
      <div class="pane">
        ${c ? html`<div class="muted">${ds.REASON.caps(c, s.tile)}</div>` : nothing}
        <h4>Non-primary access</h4>
        ${s.view === 'reader' ? html`<div class="muted">${ds.REASON.needsWrite(s.tile)}</div>` : html`
          <p class="muted">${ds.REASON.edgeRule(s.tile)}</p>
          <div class="tbl" style="--cols: minmax(0, 1.4fr) minmax(0, .7fr) minmax(0, 1.6fr) minmax(0, 1fr)">
            <div class="tr hd"><span>Edge</span><span>The primary</span><span>Non-primary access</span><span>Refused</span></div>
            ${rows.map((r) => html`<div class="tr">
              <span><span class="lb">Edge</span>${r.label}</span><span><span class="lb">The primary</span>${r.primary}</span>
              <span><span class="lb">Non-primary access</span>${r.values.length ? html`<select aria-label=${'Non-primary access: ' + r.label} ?disabled=${!r.enabled || this._busy}
                  title=${r.why} @change=${(e) => { const v = e.target.value; e.target.value = r.value; this._setEdge(r.id, v); }}>
                  ${r.values.map((v) => html`<option value=${v.value} ?selected=${v.value === r.value}>${v.label}</option>`)}</select>` : r.text}</span>
              <span><span class="lb">Refused</span>${r.refused}</span>
            </div>`)}
          </div>`}
      </div>
      ${acts.length ? html`<div class="actions"><div class="btns">${acts.map((a) => this._button(a))}</div>${this._whys(acts)}</div>` : nothing}`;
  }

  _overview(s, o, name) {
    const v = ds.overview(s, name, o);
    if (!v) return nothing;
    return html`<div class="kv">${v.lines.map(([k, t]) => html`<span class="k">${k}</span><span>${glyphed(t)}</span>`)}
        <span class="k">url</span><span><a href=${v.url} target="_blank" rel="noopener">${v.url}</a></span></div>
      ${v.gitLine ? html`<div class="git">${v.gitLine} <button class="btn" @click=${() => navigator.clipboard?.writeText('git fetch xbin-deploy')}
          aria-label="copy git fetch xbin-deploy">copy</button><div class="why">${v.gitNote}</div></div>` : nothing}`;
  }

  _logTab(s, o, name) {
    if (s.view === 'reader') return html`<div class="muted">${ds.REASON.needsWrite(s.tile)}</div>`;
    if (!this._log) return html`<div class="muted">…</div>`;
    return html`<div class="tbl" style="--cols: 110px minmax(0, 1.2fr) minmax(0, 1fr) minmax(0, 1fr) 70px 270px">
      <div class="tr hd"><span>code</span><span>how</span><span>result</span><span>who</span><span>feed</span><span></span></div>
      ${ds.logRows(s, name, this._log, o).map((r) => html`<div class="tr">
        <span>${glyphed(r.code)}</span><span>${r.how}</span><span class=${r.result.startsWith('failed') ? 'bad' : ''}>${r.result}</span><span>${r.who}</span><span>${r.feed}</span>
        <span class="btns">${r.state ? html`<span class="pill target">${r.state}</span>` : nothing}${r.rollback ? html`
          ${this._button({ id: `rollback/${r.checkpoint}`, ...r.rollback })}${this._button({ id: `diff/${r.checkpoint}`, label: 'diff', enabled: true })}` : nothing}</span>
      </div>`)}
    </div>`;
  }

  _logsTab(s, name) {
    if (!['terminal', 'admin'].includes(s.caller?.level)) return html`<div class="pane"><div class="muted">${ds.REASON.logs(s.tile)}</div></div>`;
    return html`<bx-logs component=${s.tile} deployment=${dep(s, name)?.primary ? nothing : name} style="flex:1; min-height:0"></bx-logs>`;
  }

  _registrations(s, o, name) {
    if (s.view === 'reader') return html`<div class="muted">${ds.REASON.needsWrite(s.tile)}</div>`;
    const regs = ds.registrationRows(s, name, o), wn = ds.wouldNotifyRows(s, name, o), note = ds.registrationsNote(s, name);
    return html`${note ? html`<div class="muted" style="margin-bottom:6px">${note}</div>` : nothing}${regs.length ? html`<div class="tbl" style="--cols: 90px minmax(0, 1.5fr) minmax(0, 1.4fr) 90px">${regs.map((r) => html`<div class="tr">
        <span class="muted">${r.kind}</span><span>${r.label}</span><span class="pill">${r.pill}</span>
        <span>${r.runNow ? this._button({ id: 'run', label: 'Run now', ...r.runNow }, () => this._runNow(r.name)) : nothing}</span></div>`)}</div>` : nothing}
      ${dep(s, name)?.primary ? nothing : html`<h4>${ds.REASON.wouldNotify}</h4>${wn.map((t) => html`<div>${t}</div>`)}`}`;
  }

  _view(s, name) {
    const c = dep(s, name)?.can?.open || {}; // viewing is a read: view-as views too
    if (!c.ok) return html`<div class="pane"><div class="muted">${c.why || ds.REASON.needsWrite(s.tile)}</div></div>`;
    return html`<div class="view"><span class="vlabel">${name}</span><bx-frame src=${`${s.tile}+${name}`} no-edit></bx-frame></div>`;
  }

  _reviewPane(s, o) {
    const r = this._review, names = (s.deployments || []).map((d) => d.name), ok = this._reviewOk();
    const pick = (key) => html`<select aria-label=${key} @change=${(e) => this._openReview({ ...r, [key]: e.target.value, note: '' })}>
      ${names.map((n) => html`<option value=${n} ?selected=${r[key] === n}>${n}</option>`)}</select>`;
    const label = r.op === 'promote' ? `Promote ${r.from} → ${r.to}…` : `Deploy to ${r.to}`;
    return html`<div class="pane">
        <div class="btns">${r.op === 'promote' ? html`From ${pick('from')} To ${pick('to')}` : nothing}
          <button class="btn" @click=${() => { this._review = null; }}>‹ ${this._sel}</button></div>
        ${r.note ? html`<div class="err">${r.note}</div>` : nothing}
        ${r.stale ? html`<div class="why">${ds.REASON.stale} <button class="btn" @click=${() => this._openReview(r)}>Refresh</button></div>` : nothing}
        ${r.error ? html`<div class="err">${r.error}</div>` : r.patch == null ? html`<div class="muted">…</div>` : html`
          <div class="muted">${ds.diffLine(r.cpFrom, r.cpTo, r.stats)}</div>
          <pre class="diff">${unsafeHTML(diffHTML(r.patch))}</pre>`}
      </div>
      ${r.op === 'diff' ? nothing : html`<div class="actions"><div class="btns">${this._button({ id: 'confirm', label, ...ok }, () => this._confirmReview())}</div>${this._whys([ok])}</div>`}`;
  }

  _deployment(s, o) {
    const name = this._sel, d = dep(s, name);
    if (!d) return html`<div class="pane muted">…</div>`;
    const tabs = TABS.filter(([id]) => id !== 'view' || !d.primary), acts = ds.panelActions(s, name, o);
    const body = this._review ? this._reviewPane(s, o)
      : this._tab === 'logs' ? this._logsTab(s, name)
        : this._tab === 'view' ? this._view(s, name)
          : html`<div class="pane">${this._tab === 'log' ? this._logTab(s, o, name) : this._tab === 'registrations' ? this._registrations(s, o, name) : this._overview(s, o, name)}</div>
            <div class="actions"><div class="btns">${acts.map((a) => this._button(a))}</div>${this._whys(acts)}</div>`;
    return html`<div class="title">${name}${d.primary ? html`<span class="pill primary">primary</span>` : nothing}</div>
      <div class="tabs" role="tablist">${tabs.map(([id, label]) => html`<button role="tab" aria-selected=${String(this._tab === id)} class=${this._tab === id ? 'on' : ''}
        @click=${() => { this._tab = id; this._review = null; }}>${label}</button>`)}</div>
      ${body}`;
  }

  render() {
    const s = this._state, o = this._opts();
    if (!s) return html`<div class="pane muted">${this._loaded ? this._loadError : '…'}</div>`;
    if (!s.record) return html`${this._header(s, o)}${this._zero(s, o)}`;
    const drill = this._drill; // the drill-down applies only while the panel is narrow (@container dbody)
    return html`${this._header(s, o)}
      <div class=${'body' + (drill ? ' drill' : '')}>
        ${this._side(s, o)}
        <section class="main">
          <button class="btn back" @click=${() => { this._drill = false; }}>‹ Deployments</button>
          ${this._sel === '' ? this._tileWide(s, o) : this._deployment(s, o)}
        </section>
      </div>`;
  }

  // ---- the harness's names ----

  testApi() {
    const el = this, o = () => el._opts(), s = () => el._state;
    const plain = (a) => ({ id: a.id, label: a.label, enabled: !!a.enabled, why: a.why || '' });
    return {
      get state() { return s(); },
      refresh: () => el._load(),
      get rows() { return ds.panelRows(s(), o()); },
      select(name) { el._select(name); },
      get selected() { return el._sel ?? null; },
      tab(name) { el._tab = name; el._review = null; },
      get header() { const h = s() && ds.panelHeader(s(), o()); return h ? { text: h.text, actions: h.actions.map((a) => a.id) } : null; },
      actions() {
        if (el._review) return [plain({ id: 'confirm', label: el._review.op === 'promote' ? `Promote ${el._review.from} → ${el._review.to}…` : `Deploy to ${el._review.to}`, ...el._reviewOk() })];
        return ds.panelActions(s(), el._sel || '', o()).map(plain);
      },
      act: (id) => el._act(id),
      get diff() {
        const r = el._review;
        return r ? { from: r.from ?? null, to: r.to, checkpoint: r.cpTo || null, files: r.stats?.files ?? null, add: r.stats?.add ?? null, del: r.stats?.del ?? null, stale: !!r.stale } : null;
      },
      get log() { return el._log ? ds.logRows(s(), el._sel, el._log, o()) : null; },
      get edges() { return ds.edgeRows(s(), o()).map((e) => ({ ...e, values: e.values.map((v) => v.value) })); },
      setEdge: (edge, value) => el._setEdge(edge, value),
      get registrations() { return ds.registrationRows(s(), el._sel, o()).map((r) => ({ ...r, runNow: r.runNow ? { ...r.runNow } : null })); },
      runNow: (name) => el._runNow(name),
      get wouldNotify() { return ds.wouldNotifyRows(s(), el._sel, o()); },
      get gitLine() { return el._sel ? ds.overview(s(), el._sel, o())?.gitLine ?? null : null; },
      view() { return el.renderRoot?.querySelector('.view bx-frame')?.testApi?.() ?? null; },
      text() { return (el.renderRoot?.textContent || '').replace(/\s+/g, ' ').trim(); },
      get error() { return el._error || null; },
      get busy() { return !!el._busy; },
    };
  }
}
customElements.define('bx-deployments', BxDeployments);
