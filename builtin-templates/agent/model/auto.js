// model/auto.js — the Automations page's state (D83): the agents that work
// without anyone typing. Schedules and watchers are built in; chat channels
// (auto-channels.js) and triggers (auto-triggers.js) register their own kind
// (registerKind). What is open (the list, one automation with its runs, a
// form), the summary the sidebar entry shows, and the schedule actions. No
// lit, no DOM, no dialogs: automations.js draws it on the web (and asks
// "are you sure?" before a delete), a native view draws the same.
import { selfApi as api, jbody } from '/vendor/bx-kit.js';

// KINDS: kind → spec. A view adds its drawing to a kind (extendKind).
export const KINDS = new Map();

/**
 * registerKind adds a kind of automation to the page.
 * @param kind  the API's kind (GET /automations items[].kind)
 * @param spec  {label, order, empty?: text when there are none,
 *   card?(page, item), head?(item, page) — the detail's header actions,
 *   detail?(item, page) — above its runs, runsLabel?: what its runs are called,
 *   open?(item id, page) — load what the detail shows (on open and on every
 *   refresh), create?: {label, start(page)} — a header button (start may set
 *   page.custom, a page of its own: custom(page) → template), load?(page) —
 *   extra data on every refresh, listExtra?(page) — below its cards}
 */
export function registerKind(kind, spec) { KINDS.set(kind, spec); }

// extendKind adds to a registered kind (a view's card/head/detail over the
// model's label/order/open/load).
export function extendKind(kind, spec) { KINDS.set(kind, { ...(KINDS.get(kind) || {}), ...spec }); }

// kinds: the registered kinds in page order.
export const kinds = () => [...KINDS.entries()].sort((a, b) => a[1].order - b[1].order);

export const CADENCES = [
  ['0 9 * * *', 'every day at 9:00'],
  ['0 9 * * 1-5', 'every weekday at 9:00'],
  ['0 9 * * 1', 'every Monday at 9:00'],
  ['@every 1h', 'every hour'],
  ['@every 15m', 'every 15 minutes'],
];
export const MODES = { isolated: 'a new run each time', persistent: 'one ongoing thread', conversation: 'into a conversation' };

export class AutoPage {
  /** @param on {change(), select(runId), me() → GET /me, route(kind, id) — what is open, for the address} */
  constructor(on) {
    this.on = on;
    this.items = [];
    this.summary = { count: 0, unread: 0, failing: 0 };
    this.open = null;   // {kind, id}
    this.runs = [];
    this.next = '';
    this.form = null;   // the schedule being created or edited
    this.custom = null; // a kind's own page (its form): custom(page) → template
    this.err = '';
  }

  changed() { this.on.change?.(); }

  async loadSummary() {
    try { this.summary = await api('/automations?summary=1'); } catch { /* keep */ }
    this.changed();
  }

  async load() {
    try { this.items = (await api('/automations')).items || []; this.err = ''; } catch (e) { this.err = e.message; }
    await Promise.all([...KINDS.values()].map((s) => s.load?.(this)));
    if (this.open) await Promise.all([this.loadRuns(), KINDS.get(this.open.kind)?.open?.(this.open.id, this)]);
    this.changed();
  }

  item() { return this.open && this.items.find((i) => i.kind === this.open.kind && i.id === this.open.id); }

  async show(kind, id) {
    this.open = kind ? { kind, id } : null;
    this.form = null;
    this.custom = null;
    this.on.route?.(kind, id); // a reload (or a copied link) comes back here
    this.runs = [];
    this.next = '';
    if (this.open) {
      await Promise.all([this.loadRuns(), KINDS.get(kind)?.open?.(id, this)]);
      api(`/automations/${kind}/${id}/read`, { method: 'POST' }).then(() => this.loadSummary()).catch(() => {});
    }
    this.changed();
  }

  async loadRuns(more = false) {
    const { kind, id } = this.open;
    const q = more && this.next ? `?cursor=${encodeURIComponent(this.next)}` : '';
    try {
      const p = await api(`/automations/${kind}/${id}/runs${q}`);
      this.runs = more ? [...this.runs, ...(p.items || [])] : p.items || [];
      this.next = p.next || '';
    } catch (e) { this.err = e.message; }
    this.changed();
  }

  newSchedule(watcher = false) {
    this.open = null;
    this.form = { name: '', cron: CADENCES[0][0], goal: '', mode: 'isolated', toolset: 'private', visibility: 'private', watcher };
    this.changed();
  }

  editSchedule(it) {
    const c = it.config || {};
    this.form = { id: it.id, name: it.name, cron: c.cron, goal: c.goal, system: c.system || '', mode: it.mode || 'isolated',
      toolset: c.toolset || 'private', visibility: it.visibility, watcher: it.kind === 'watcher', targetRun: it.targetRun };
    this.changed();
  }

  closeForm() { this.form = null; this.changed(); }

  async save() {
    const f = this.form;
    if (!f.cron.trim() || !f.goal.trim()) { this.err = 'a cadence and what to do are needed'; this.changed(); return; }
    const body = { name: f.name.trim(), cron: f.cron.trim(), goal: f.goal.trim(), mode: f.watcher ? '' : f.mode,
      visibility: f.visibility, watcher: f.watcher, toolset: f.toolset, targetRun: f.targetRun || 0 };
    try {
      const s = f.id ? await api(`/schedules/${f.id}`, jbody(body, 'PUT')) : await api('/schedules', jbody(body, 'POST'));
      this.form = null;
      this.err = '';
      await this.load();
      await this.show(s.watcher ? 'watcher' : 'schedule', s.id);
    } catch (e) { this.err = e.message; this.changed(); }
  }

  async act(fn) {
    this.err = '';
    try { await fn(); await this.load(); } catch (e) { this.err = e.message; this.changed(); }
  }
  toggle(it) { return this.act(() => api(`/schedules/${it.id}`, jbody({ enabled: !it.enabled }, 'PUT'))); }
  runNow(it) { return this.act(() => api(`/schedules/${it.id}/trigger`, { method: 'POST' })); }
  reset(it) { return this.act(() => api(`/automations/${it.kind}/${it.id}/reset`, { method: 'POST' })); }
  // del removes a schedule or watcher (its runs stay). The view confirms first.
  async del(it) {
    await this.act(() => api(`/schedules/${it.id}`, { method: 'DELETE' }));
    this.open = null;
    this.changed();
  }
}

registerKind('schedule', { label: 'Schedules', order: 1 });
registerKind('watcher', { label: 'Watchers', order: 2 });

// scheduleCan: what you may do with a schedule or watcher — its owner runs,
// edits and restarts it; a manager overseeing it may switch it off or delete it.
export function scheduleCan(it) {
  const mine = it.access === 'owner';
  const oversee = it.access === 'oversee';
  return {
    mine,
    runNow: mine,
    toggle: mine || oversee,
    edit: mine && !!it.config,
    // a thread (a watcher, or a persistent schedule) can start afresh
    reset: mine && (it.kind === 'watcher' || it.mode === 'persistent'),
    del: mine || oversee,
  };
}

// summaryCount: what the sidebar entry's badge counts (new runs + what waits on you).
export const summaryCount = (s) => (s.unread || 0) + (s.attention || 0);

export const ago = (sec) => {
  if (!sec) return '';
  const s = Math.max(0, Date.now() / 1000 - sec);
  return s < 90 ? 'just now' : s < 5400 ? `${Math.round(s / 60)} min ago` : s < 129600 ? `${Math.round(s / 3600)} h ago` : `${Math.round(s / 86400)} d ago`;
};
export const cadence = (cron) => (CADENCES.find(([c]) => c === cron) || [cron, cron])[1];
