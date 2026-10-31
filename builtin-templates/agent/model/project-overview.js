// model/project-overview.js — a project's overview (D214, plans/project-overview.md):
// the status strip, the agent words on board cards, the page's fold and the
// coordinator chat's height, for both views. No lit, no DOM.
//
//   const o = projectOverview(app);     // one per app, made on first use
//   o.ensure(pid) → {data, loading, err, loaded, range, supported}: the overview
//                   (GET /projects/{pid}/overview?range=), read the first time
//                   a view asks; data null until read
//   o.load(pid), o.setRange(pid, 'day' | 'week')
//   o.show(pid) / o.hide()  while a project's page shows: re-read every
//                   REFRESH ms (the sandbox chart moves) and on its `project`
//                   events (coalesced); nothing is read while none shows
//   o.folded, o.setFolded(on); o.chatHeight, o.setChatHeight(px | null)
//                   — kept in the person's prefs (xbind's per-person store)
//
// Words (pure, for both views): stripWords(data) — one entry per cell, in
// the strip's order; foldLine(data); cardAgent(data, n); tokenChart(data);
// sparkline(series, key); fmtTokens, fmtBytes, ago. TIPS: each part's
// tooltip text (the web shows them on hover, the native view as footers).
//
// A backend from before the overview answers 404 (the mux's, plain) or 405:
// `supported` false, and the page draws as it did. Text that came from the
// scm provider (labels, repo names) is untrusted: plain() here, drawn as text.
import { projCall, qs, projectHome } from './project-api.js';
import { plain, httpsUrl } from './project-feed.js';

export const REFRESH = 60000;
const COALESCE = 500;
const FOLDED = '/api/xbin/prefs/project-status-folded';
const CHAT_H = '/api/xbin/prefs/project-chat-height';

/** The chat window's height (px): the default, on a short tile, and its bounds. */
export const CHAT = { def: 300, short: 200, min: 100, boardMin: 120, tall: 600 };

/** TIPS: what each part is, for its tooltip (plain text; the first sentence is its title). */
export const TIPS = {
  coordinator: 'Coordinator. Your agent for this project: it turns issues and your requests into tasks, starts them, follows CI and reviews, and tells you what needs you. It never merges and never answers a task\'s question for you. Working: a turn runs now. Idle: it waits for the next event — a CI result, a review, a new issue, your message.',
  workers: 'Worker agents. One per task, each in its own conversation and worktree: the agent itself or a coding agent it drives. Working: a turn runs. Needs you: an answer, a sign-in or a review. Idle: between turns (waiting on CI or a review). Slots: how many may work at once (the project\'s policy).',
  subagents: 'Subagents. Helpers a worker agent or the coordinator started for part of its work (research, a second opinion, a parallel fix); they report back to the agent that started them. Finished ones count over the last 24 hours.',
  tokens: 'Tokens. Model tokens this project\'s agents used — the coordinator, the worker agents and their subagents — prompt and completion, as the model provider reported them. Coding agents report a running cost instead of tokens; it is added on its own line.',
  scm: 'Source control. The project\'s repositories at their provider: open pull requests and issues, what merged in the last 7 days, and the default branch\'s checks.',
  sandbox: 'Sandbox. The project\'s coding sandbox, where every task\'s worktree and commands run. CPU and memory over the last hour; disk is what its files take; commands: those running in it now.',
  board: 'Tasks. A column per state; a card opens its task\'s conversation, and says its agent and its running subagents.',
  split: 'Drag to resize. Shares the height between the tasks and the coordinator\'s conversation. Arrow keys move it too; double-click puts it back. Kept for you.',
  goal: 'Current goal. What the coordinator works towards, in its own words; it updates it as your requests change. Before it says one, your latest message to it.',
  idle: 'Idle. The coordinator has no turn running. It wakes for the events listed, or when you write to it.',
  chat: 'The coordinator\'s newest messages. Its whole conversation, with every tool call, is behind Open chat.',
  fold: 'Fold the status. The status strip as one line, to give the board and the coordinator more room. Kept for you.',
};

const ENGINE_TITLE = { agent: 'agent', 'claude-code': 'Claude Code', codex: 'Codex' };

// --- formatting ----------------------------------------------------------------------------

/** fmtTokens(n): 1234 → "1.2 k", 1840000 → "1.84 M". */
export function fmtTokens(n) {
  n = +n || 0;
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} G`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 1 : 2)} M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)} k`;
  return String(Math.round(n));
}

/** fmtBytes(n): bytes as GiB/MiB, one decimal. */
export function fmtBytes(n) {
  n = +n || 0;
  if (n >= 2 ** 30) return `${(n / 2 ** 30).toFixed(1)} GiB`;
  if (n >= 2 ** 20) return `${Math.round(n / 2 ** 20)} MiB`;
  return `${Math.round(n / 1024)} KiB`;
}

/** ago(ms, now): "3 m", "2 h", "4 d" — '' for none. */
export function ago(ms, now = Date.now()) {
  if (!ms) return '';
  const s = Math.max(0, (now - ms) / 1000);
  if (s < 60) return 'now';
  if (s < 3600) return `${Math.round(s / 60)} m`;
  if (s < 86400) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} d`;
}

const money = (c) => (c && c.amount > 0 ? `${c.currency === 'USD' || !c.currency ? '$' : ''}${(+c.amount).toFixed(2)}${c.currency && c.currency !== 'USD' ? ` ${plain(c.currency, 8)}` : ''}` : '');
const sum = (o) => (o ? (o.workers || 0) + (o.coordinator || 0) + (o.subagents || 0) : 0);

// --- words ---------------------------------------------------------------------------------

const COORD_STATE = {
  working: ['Working', 'run'], waiting: ['Waiting for you', 'warn'], idle: ['Idle', 'idle'], failed: ['Failed', 'bad'], none: ['Not started', 'idle'],
};

/** coordWords(data, now): the coordinator cell — {state, word, tone, since, line, last, counts}. */
export function coordWords(d, now = Date.now()) {
  const c = (d && d.coordinator) || {};
  const [word, tone] = COORD_STATE[c.state] || COORD_STATE.none;
  const run = c.run || null;
  const goal = c.goal || null;
  return {
    state: c.state || 'none', word, tone,
    since: c.state === 'working' && run && run.turnStartedMs ? `turn ${ago(run.turnStartedMs, now)}` : '',
    line: c.state === 'working' || c.state === 'waiting' ? plain((goal && goal.now) || '', 120) : (c.wakesOn || []).length ? `waiting for: ${(c.wakesOn || []).map((w) => plain(w.label, 40)).join(', ')}` : '',
    last: c.lastEvent ? { label: plain(c.lastEvent.label, 60), ago: ago(c.lastEvent.atMs, now) } : null,
    turnsToday: c.turnsToday || 0, tasksMade7d: c.tasksMade7d || 0,
    model: plain(c.model || '', 60), tokensToday: c.tokensToday || 0,
  };
}

/** goalWords(data): the goal line — {kind: 'goal' | 'idle' | 'none', text, now, by, wakes: [{kind, label}]}. */
export function goalWords(d) {
  const c = (d && d.coordinator) || {};
  const goal = c.goal ? { text: plain(c.goal.text, 300), now: plain(c.goal.now || '', 300), by: c.goal.by === 'you' ? 'you' : 'coordinator' } : null;
  const wakes = (c.wakesOn || []).map((w) => ({ kind: w.kind, label: plain(w.label, 40), n: w.n || 0 }));
  if (c.state === 'idle' || c.state === 'none') return { kind: c.state === 'none' && !goal ? 'none' : 'idle', text: goal ? goal.text : '', now: '', by: goal ? goal.by : '', wakes };
  return { kind: goal ? 'goal' : 'none', text: goal ? goal.text : '', now: goal ? goal.now : '', by: goal ? goal.by : '', wakes };
}

/** stripWords(data, now): the strip's cells in order — [{key, title, tip, …the cell's own words}]. */
export function stripWords(d, now = Date.now()) {
  if (!d) return [];
  const w = d.workers || {};
  const s = d.subagents || {};
  const t = d.tokens || {};
  const scm = d.scm || {};
  const sb = d.sandbox;
  const total = t.total || {};
  return [
    { key: 'coordinator', title: 'Coordinator', tip: TIPS.coordinator, ...coordWords(d, now) },
    { key: 'workers', title: 'Worker agents', tip: TIPS.workers, total: w.total || 0, working: w.working || 0, needsYou: w.needsYou || 0, idle: w.idle || 0,
      queued: w.queued || 0, slots: w.slots || null, engines: (w.byEngine || []).map((e) => ({ engine: e.engine, title: plain(e.title || ENGINE_TITLE[e.engine] || e.engine, 30), count: e.count || 0 })) },
    { key: 'subagents', title: 'Subagents', tip: TIPS.subagents, running: s.running || 0, waiting: s.waiting || 0, finished24h: s.finished24h || 0, failed24h: s.failed24h || 0,
      by: (s.by || []).map((x) => ({ label: x.coordinator ? 'coordinator' : `#${x.n}`, count: x.count || 0 })) },
    { key: 'tokens', title: 'Tokens', tip: TIPS.tokens, range: d.range === 'week' ? 'week' : 'day', total: sum(total), parts: total,
      big: fmtTokens(sum(total)), span: d.range === 'week' ? 'last 7 days' : 'last 24 h', cost: money(t.cost) },
    { key: 'scm', title: (scm.provider && plain(scm.provider.title, 30)) || 'Source control', tip: TIPS.scm, stats: !!scm.stats,
      repos: (scm.repos || []).map((r) => ({ repo: plain(r.repo, 80), url: httpsUrl(r.url), branch: plain(r.defaultBranch || '', 60),
        checks: r.checks ? { state: r.checks.state || 'none', url: httpsUrl(r.checks.url) } : null,
        openPulls: r.openPulls ?? null, openIssues: r.openIssues ?? null, merged7d: r.merged7d ?? null, err: plain(r.err || '', 120) })),
      pulls: scm.pulls || { open: 0, failing: 0, review: 0 },
      openIssues: (scm.repos || []).reduce((a, r) => a + (r.openIssues || 0), 0), merged7d: (scm.repos || []).reduce((a, r) => a + (r.merged7d || 0), 0) },
    { key: 'sandbox', title: 'Sandbox', tip: TIPS.sandbox, none: !sb,
      name: sb ? plain(sb.name, 60) : '', state: sb ? sb.state || '' : '', running: !!sb && sb.state === 'running', vcpus: sb ? sb.vcpus || 0 : 0,
      mem: sb && sb.memMiB ? fmtBytes(sb.memMiB * 2 ** 20) : '', disk: sb && sb.diskBytes != null ? fmtBytes(sb.diskBytes) : '', diskMax: sb && sb.diskGiB ? `${sb.diskGiB} GiB` : '',
      commands: sb ? sb.commands || 0 : 0,
      cpu: sb && sb.usage && sb.usage.cpuPct != null ? `${Math.round(sb.usage.cpuPct)}%` : '',
      memUsed: sb && sb.usage && sb.usage.memBytes != null ? fmtBytes(sb.usage.memBytes) : '',
      series: (sb && sb.usage && sb.usage.series) || [] },
  ];
}

/** foldLine(data): the strip folded — [{key, text, tone?}]. */
export function foldLine(d) {
  const cells = stripWords(d);
  if (!cells.length) return [];
  const [c, w, s, t, scm, sb] = cells;
  return [
    { key: 'coordinator', text: `coordinator ${c.word.toLowerCase()}`, tone: c.tone },
    { key: 'workers', text: `workers ${w.working} working${w.needsYou ? ` · ${w.needsYou} need you` : ''}`, tone: w.needsYou ? 'warn' : '' },
    { key: 'subagents', text: `subagents ${s.running}` },
    { key: 'tokens', text: `tokens ${t.big}/${t.range === 'week' ? '7 d' : '24 h'}` },
    { key: 'scm', text: `PRs ${scm.pulls.open || 0}${scm.pulls.failing ? ` · ${scm.pulls.failing} failing` : ''}`, tone: scm.pulls.failing ? 'bad' : '' },
    { key: 'sandbox', text: sb.none ? 'no sandbox' : `sandbox ${sb.state || 'unknown'}${sb.cpu ? ` · CPU ${sb.cpu}` : ''}` },
  ];
}

/** cardAgent(data, n): a board card's agent line — {engine, title, state, tone, subagents, tokens} | null. */
export function cardAgent(d, n) {
  const x = d && d.tasks && d.tasks[n];
  if (!x) return null;
  const tone = x.state === 'working' ? 'run' : x.state === 'waiting' ? 'warn' : x.state === 'failed' ? 'bad' : 'idle';
  return { engine: x.engine || 'agent', title: plain(x.title || ENGINE_TITLE[x.engine] || x.engine || 'agent', 30), state: x.state || 'idle', tone,
    subagents: x.subagents || 0, tokens: x.tokens ? fmtTokens(x.tokens) : '' };
}

/** tokenChart(data): the stacked chart's series — {labels, workers, coordinator, subagents, max}. */
export function tokenChart(d) {
  const b = (d && d.tokens && d.tokens.buckets) || [];
  const workers = b.map((x) => x.workers || 0), coordinator = b.map((x) => x.coordinator || 0), subagents = b.map((x) => x.subagents || 0);
  const max = Math.max(1, ...b.map((x, i) => workers[i] + coordinator[i] + subagents[i]));
  return { t: b.map((x) => x.t), workers, coordinator, subagents, max };
}

/** sparkline(series, key): one value per point, for a small line chart. */
export const sparkline = (series, key) => (series || []).map((p) => +p[key] || 0);

// --- the store -----------------------------------------------------------------------------

class Overview {
  constructor(app) {
    this.app = app;
    this.items = new Map(); // pid → {data, loading, err, loaded, range, supported, seq}
    this.shown = null;      // the pid whose page shows
    this.timer = null;
    this.coalesce = null;
    this.folded = false; this.foldSet = false;
    this.chatHeight = null; this.chatSet = false;
    this.prefsRead = false;
    const pj = app.projects;
    if (pj && pj.take) {
      const take = pj.take.bind(pj);
      pj.take = (ev) => { take(ev); this.take(ev); };
    }
  }

  changed() { this.app.emit('projects'); }

  item(pid) {
    let it = this.items.get(+pid);
    if (!it) { it = { data: null, loading: false, err: '', loaded: false, range: 'day', supported: true, seq: 0 }; this.items.set(+pid, it); }
    return it;
  }

  ensure(pid) {
    const it = this.item(pid);
    if (!it.loaded && !it.loading && !it.err) this.load(pid);
    if (!this.prefsRead) this.readPrefs();
    return it;
  }

  async load(pid) {
    pid = +pid;
    const it = this.item(pid);
    if (it.loading) { it.again = true; return; }
    it.loading = true;
    const seq = ++it.seq;
    try {
      const d = await projCall(projectHome(pid), `/projects/${pid}/overview${qs({ range: it.range })}`);
      if (seq !== it.seq) return;
      it.data = d; it.err = '';
    } catch (e) {
      if (seq !== it.seq) return;
      if (e.status === 405 || (e.status === 404 && !(e.data && typeof e.data === 'object'))) it.supported = false; // an older backend
      else it.err = e.message;
    }
    it.loading = false; it.loaded = true;
    this.changed();
    if (it.again) { it.again = false; this.load(pid); }
  }

  setRange(pid, range) {
    const it = this.item(pid);
    it.range = range === 'week' ? 'week' : 'day';
    this.load(pid);
    this.changed();
  }

  // show / hide: the page of pid shows — re-read every REFRESH ms meanwhile
  show(pid) {
    pid = +pid;
    if (this.shown === pid) return;
    this.hide();
    this.shown = pid;
    const tick = () => { this.timer = setTimeout(() => { if (this.shown === pid) { this.load(pid); tick(); } }, REFRESH); };
    tick();
  }
  hide() { this.shown = null; clearTimeout(this.timer); this.timer = null; }

  // take: a `project` event of the shown project reads the overview again (coalesced)
  take(ev) {
    if (!ev || ev.type !== 'project' || !ev.data || +ev.data.id !== this.shown) return;
    if (ev.data.change === 'deleted') { this.items.delete(+ev.data.id); return; }
    clearTimeout(this.coalesce);
    const pid = this.shown;
    this.coalesce = setTimeout(() => { if (this.shown === pid) this.load(pid); }, COALESCE);
  }

  // --- prefs: the fold and the chat's height, per person ----------------------------------
  async readPrefs() {
    this.prefsRead = true;
    const x = globalThis.xbin;
    if (!x || !x.fetch) return;
    try {
      const [f, h] = await Promise.all([x.fetch(FOLDED), x.fetch(CHAT_H)]);
      const fv = f.ok ? await f.json() : null;
      const hv = h.ok ? await h.json() : null;
      if (!this.foldSet) this.folded = fv === true;
      if (!this.chatSet && typeof hv === 'number' && hv >= CHAT.min) this.chatHeight = Math.round(hv);
      this.changed();
    } catch { /* no prefs: the defaults */ }
  }
  setFolded(on) {
    this.folded = !!on; this.foldSet = true; this.changed();
    this.writePref(FOLDED, this.folded);
  }
  setChatHeight(px) {
    this.chatHeight = px == null ? null : Math.max(CHAT.min, Math.round(px)); this.chatSet = true; this.changed();
    this.writePref(CHAT_H, this.chatHeight);
  }
  writePref(path, v) {
    const x = globalThis.xbin;
    if (!x || !x.fetch) return;
    Promise.resolve().then(() => (v == null ? x.fetch(path, { method: 'DELETE' })
      : x.fetch(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(v) }))).catch(() => {});
  }
}

/** projectOverview(app): the app's overview store, made on first use. */
export function projectOverview(app) {
  if (!app.projOverview) app.projOverview = new Overview(app);
  return app.projOverview;
}
