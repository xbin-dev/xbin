// model/projects.js — the Projects page's state and calls for both views
// (API.md §Projects in the UI), as app.projects — the AutoPage pattern
// (model/auto.js): what is open (the list, one project's board or its
// settings, the new-project form), the list the sidebar entry counts, and
// every action a view takes. No lit, no DOM, no dialogs: a view asks "are
// you sure?" itself, then calls these.
//
//   const pj = createProjects(app, {route});   // app.js does this
//   pj.load(); pj.open(pid); pj.board(pid) → columns with their tasks
//   pj.take(ev)                                 // every stream event
//
// Where things live: a project's id says its home (model/project-api.js) —
// in a person's partition their own projects are in their partition, a team
// project's definition at the global instance, and the list reads both. The
// `project` stream event ({id, change, n}) says what changed: the list, the
// open project and an open task conversation are read again (coalesced,
// 250 ms) — the event carries no data of its own.
//
// Events on the app: 'projects' (the list, the page, a form, a project's
// data changed — the views repaint). Calls throw as the backend refuses
// (e.message, e.status, e.refusal, e.data); a page action's refusal is kept
// in pj.err instead, for the page to say.
import { listHomes, twoHomes } from './homes.js';
import { partitionState } from './partition.js';
import { projectApi, taskApi, scmApi, listProjects, createProject, projectHome } from './project-api.js';
import { columns, columnOf } from './project-task.js';
import { choices, find, reach } from './classes.js';

// The policy's keys (ProjectPolicy, API.md §Projects and tasks), grouped as
// the settings show them. type: text | area | lines (a list, one per line) |
// number | bool | select (options) | class (a class you may use) | harness
// (a coding agent of the catalog). A key the build doesn't know is kept as
// it is stored and never shown.
export const POLICY = [
  { key: 'tasks', title: 'Tasks', fields: [
    { path: 'taskClass', label: 'Class of new tasks', type: 'class', hint: 'never one with internal reach' },
    { path: 'engine', label: 'Who answers', type: 'select', options: [['auto', 'auto — your pick'], ['builtin', 'the built-in agent'], ['harness', 'a coding agent']] },
    { path: 'harness', label: 'Coding agent', type: 'harness', hint: 'empty: the one you used last' },
    { path: 'maxTasks', label: 'Tasks at work at once', type: 'number', min: 1, max: 16 },
    { path: 'maxOpenTasks', label: 'Open tasks a coordinator may make', type: 'number', min: 0 },
    { path: 'maxTaskCreatesPerDay', label: 'Tasks a coordinator may make a day', type: 'number', min: 0 },
    { path: 'instructions', label: 'Instructions for every task', type: 'area', hint: 'after the repos\' own AGENTS.md' },
    { path: 'checks', label: 'Checks before a push', type: 'lines', hint: 'one command per line' },
  ] },
  { key: 'prs', title: 'Branches and pull requests', fields: [
    { path: 'branchPrefix', label: 'Branch prefix', type: 'text', hint: 'empty: xbin/<the project\'s id>' },
    { path: 'autoPR', label: 'Open a pull request when a task rests', type: 'select', options: [['off', 'off — Open PR by hand'], ['draft', 'as a draft'], ['ready', 'ready for review']] },
    { path: 'prConventions', label: 'Pull request conventions', type: 'area' },
    { path: 'as', label: 'Work as', type: 'select', options: [['', 'the default (you in your own space, else the bot)'], ['person', 'you'], ['bot', 'the provider\'s bot']] },
    { path: 'membersAsBot', label: 'Team members may work as the bot', type: 'bool' },
    { path: 'protection', label: 'A base branch without protection', type: 'select', options: [['warn', 'warn'], ['refuse', 'refuse']] },
    { path: 'workflows', label: 'Tasks may change .github/workflows', type: 'bool' },
  ] },
  { key: 'ci', title: 'CI and reviews', fields: [
    { path: 'ci.autoFix', label: 'Tell a task when its CI fails', type: 'bool' },
    { path: 'ci.maxPerDay', label: 'CI fixes a task gets a day', type: 'number', min: 0 },
    { path: 'ci.delaySec', label: 'Wait for the other checks (s)', type: 'number', min: 0 },
    { path: 'ci.logBytes', label: 'Log a fix sees (bytes)', type: 'number', min: 0 },
    { path: 'reviews.forward', label: 'Review comments that reach the task', type: 'select', options: [['trusted', 'from people with access'], ['all', 'all'], ['off', 'none']] },
    { path: 'reviews.allow', label: 'Also forward from', type: 'lines', hint: 'logins, one per line' },
    { path: 'reviews.batchSec', label: 'Gather review comments for (s)', type: 'number', min: 0 },
    { path: 'autoLabel', label: 'An issue with this label wakes the coordinator', type: 'text' },
  ] },
  { key: 'setup', title: 'Workspace, ports and setup', fields: [
    { path: 'checkout', label: 'A task\'s checkout', type: 'select', options: [['worktree', 'a git worktree'], ['clone', 'a clone sharing the objects']] },
    { path: 'setupTimeoutSec', label: 'Setup timeout (s)', type: 'number', min: 1 },
    { path: 'setupBlocking', label: 'A task waits for its setup', type: 'bool' },
    { path: 'ports.base', label: 'First port', type: 'number', min: 1024, max: 65535 },
    { path: 'ports.span', label: 'Ports per task', type: 'number', min: 1 },
    { path: 'ports.slots', label: 'Port ranges', type: 'number', min: 1 },
    { path: 'fetchEveryMin', label: 'Fetch every (min)', type: 'number', min: 1 },
  ] },
  { key: 'big', title: 'Big tasks', fields: [
    { path: 'bigTasks.mode', label: 'A big task\'s sandbox', type: 'select', options: [['fork', 'forked from the project\'s'], ['fresh', 'a fresh one']] },
    { path: 'bigTasks.keepFork', label: 'Keep its sandbox after cleanup', type: 'bool' },
  ] },
  { key: 'cleanup', title: 'Cleanup', fields: [
    { path: 'cleanup.onMerge', label: 'Clean up when its pull request merges', type: 'bool' },
    { path: 'cleanup.onClose', label: 'Clean up when it closes', type: 'bool' },
  ] },
  { key: 'coord', title: 'The coordinator', fields: [
    { path: 'coordinator.web', label: 'May search and read the web', type: 'bool' },
    { path: 'coordinator.model', label: 'Its model', type: 'text', hint: 'empty: the agent\'s default' },
  ] },
];

/** policyGet(policy, 'ci.autoFix'): a key's value. */
export function policyGet(p, path) {
  let v = p;
  for (const k of String(path).split('.')) v = v == null ? undefined : v[k];
  return v;
}

/** policySet(policy, path, value): a copy with the key set (every other key kept, unknown ones too). */
export function policySet(p, path, value) {
  const keys = String(path).split('.');
  const out = { ...(p || {}) };
  let o = out;
  for (const k of keys.slice(0, -1)) { o[k] = { ...(o[k] && typeof o[k] === 'object' ? o[k] : {}) }; o = o[k]; }
  o[keys[keys.length - 1]] = value;
  return out;
}

/** fieldValue(field, raw): what a field's input means (a number, a bool, a list). */
export function fieldValue(f, raw) {
  if (f.type === 'bool') return !!raw;
  if (f.type === 'number') { const n = Number(raw); return Number.isFinite(n) ? Math.trunc(n) : 0; }
  if (f.type === 'lines') return String(raw ?? '').split('\n').map((s) => s.trim()).filter(Boolean);
  return String(raw ?? '');
}

/** fieldText(field, value): a field's value as its input shows it. */
export const fieldText = (f, v) => (f.type === 'lines' ? (Array.isArray(v) ? v.join('\n') : '') : v == null ? '' : String(v));

/** can(view): what the caller may do with a project (its level): settings, act (tasks), read. */
export function can(pv) {
  const lv = (pv && pv.level) || 'viewer';
  const owner = lv === 'owner';
  const act = owner || lv === 'participant';
  const live = !pv || pv.state === 'active';
  return { owner, act: act && live, read: true, settings: owner, archived: !!pv && pv.state === 'archived' };
}

/** sharable(view): may this project have members and team visibility? Not
 * a person's own in their partition (it is private), nor a membership. */
export const sharable = (pv, state = partitionState()) => !!pv && pv.kind !== 'membership' && !(state === 'user' && pv.kind === 'personal');

/** canSignin(provider): may this page offer "Sign in to ‹provider›"? Only in
 * a person's own partition (the sign-in routes answer 409 anywhere else),
 * and only when the provider lets *you* work as yourself (`you.identities`,
 * not what it hands out to anyone). */
export const canSignin = (prov, state = partitionState()) => !!prov && state === 'user' && ((prov.you && prov.you.identities) || []).includes('person');

/** taskClassChoices(state, cur): the policy's "Class of new tasks" — the
 * classes you may use without internal reach (one with it is refused,
 * `class-internal`); the stored one stays shown, said, when it has. */
export function taskClassChoices(state, cur) {
  const internal = (r) => !r.gone && reach(find(state, r.value)).internal;
  return choices(state, cur).filter((r) => r.on || !internal(r))
    .map((r) => (internal(r) ? { ...r, label: `${r.label} (has internal reach: pick another)` } : r));
}

/**
 * agentChoices(view, harnesses): who may answer a new task, as the New task
 * form and the issue picker offer it — [{value, label, disabled}]. A task
 * names a coding agent or nothing, and nothing is the project's
 * `policy.engine`: `builtin` the built-in agent; `auto` and `harness` the
 * policy's `harness`, else the coding agent you last started a
 * conversation with, else the built-in agent. So the built-in agent is
 * offered by name only where nothing means it (the policy says `builtin`,
 * or no coding agent is available); elsewhere the first choice says what
 * the project's default does.
 */
export function agentChoices(pv, harnesses = []) {
  const pol = (pv && pv.policy) || {};
  const hs = harnesses || [];
  const named = pol.harness ? (hs.find((h) => h.id === pol.harness) || { name: pol.harness }).name : '';
  const label = pol.engine === 'builtin' ? 'the built-in agent (the project\'s default)'
    : !hs.some((h) => h.available) ? 'the built-in agent (no coding agent is available)'
    : `the project's default: ${named || 'the coding agent you used last, else the built-in agent'}`;
  return [{ value: '', label, disabled: false },
    ...hs.map((h) => ({ value: h.id, label: `${h.name}${h.available ? '' : ' (not available)'}`, disabled: !h.available }))];
}

// slug: what the backend makes of a repo's name (a hint in the form; the backend decides).
export const repoSlug = (repo) => String(repo || '').split('/').pop().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);

// listAll: GET /projects at a home, page after page (a page is the
// backend's; at most 40 pages are followed).
async function listAll(home) {
  const items = [];
  let cursor = '';
  for (let i = 0; i < 40; i++) {
    const r = await listProjects(home, cursor ? { cursor } : {});
    items.push(...((r && r.items) || []));
    cursor = (r && r.next) || '';
    if (!cursor) break;
  }
  return items;
}

const byActivity = (a, b) => (b.updatedMs || 0) - (a.updatedMs || 0) || (b.id || 0) - (a.id || 0);

/** createProjects(app, {route, now}): the Projects store (app.projects). */
export function createProjects(app, opts = {}) {
  return new Projects(app, opts);
}

export class Projects {
  constructor(app, opts = {}) {
    this.app = app;
    this.route = opts.route || (() => {});
    this.list = [];          // ProjectView rows from every home, newest activity first
    this.loaded = false;
    this.supported = true;   // false: this backend has no Projects (GET /projects 404)
    this.err = '';
    this.opened = null;      // the open project's id (null: the list)
    this.tab = 'board';      // the open project's tab: board | settings
    this.views = new Map();  // pid → ProjectView
    this.taskRows = new Map(); // pid → {items, next, loading, err}
    this.statusOf = new Map(); // pid → ProjectStatus
    this.membersOf = new Map(); // pid → {owner, members}
    this.filter = { q: '', mine: false };
    this.form = null;        // the new-project form
    this.taskForm = null;    // the open project's new-task form
    this.picker = null;      // the open project's issue picker
    this.draft = null;       // the open project's policy as being edited ({pid, policy, dirty})
    this.scm = null;         // GET /projects/scm: {providers, error}
    this.signins = new Map(); // scm → scmSigninState (+ err)
    this.prRoute = new Map(); // home → true | false: has the backend "Open PR"?
    this.flash = '';         // a page's one-line news ("2 tasks made")
    this.dirty = new Set();
    this.timer = null;
    this.polls = new Map();  // scm → a sign-in poll's timer
    this.pollGen = new Map(); // scm → the latest poll's number (stopPoll)
    this.following = new Map(); // scm → the pollId being polled
    this.reading = new Set(); // pids being read by ensure()
    this.failedAt = new Map(); // pid → when ensure()'s read of it failed
    this.listSeq = 0;        // load()'s reads, numbered: only the latest one's answer is kept
    this.gone = new Set();   // pids deleted here: a list read in flight doesn't bring them back
  }

  changed() { this.app.emit('projects'); }
  fail(e) { this.err = e && e.message ? e.message : String(e); this.changed(); }

  // --- the list -----------------------------------------------------------------------

  // load reads GET /projects at every home this page has ('' and, in a
  // person's partition, the shared space's team definitions), every page of
  // it (`next`), so the needs-you count covers them all. Only the latest
  // read's answer is kept: an older one that arrives after it is dropped.
  async load() {
    const seq = ++this.listSeq;
    const homes = listHomes('mine');
    const got = await Promise.all(homes.map((h) => listAll(h).then((items) => ({ h, items }), (e) => ({ h, e }))));
    if (seq !== this.listSeq) return;
    const items = [];
    let err = '';
    for (const g of got) {
      if (g.e) {
        if (g.h === '' && g.e.status === 404) this.supported = false;
        else if (g.e.status !== 404) err = g.e.message;
        continue;
      }
      if (g.h === '') this.supported = true;
      for (const p of g.items) if (!this.gone.has(p.id)) items.push({ ...p, home: g.h });
    }
    this.list = items.sort(byActivity);
    for (const p of this.list) if (!this.views.has(p.id) || (this.views.get(p.id).version || 0) <= (p.version || 0)) this.views.set(p.id, p);
    this.loaded = true;
    this.err = err;
    this.changed();
  }

  find(pid) { return this.views.get(+pid) || this.list.find((p) => p.id === +pid) || null; }

  // ensure: a project's view, read once when something shows it (a task
  // conversation's chips need its repos and provider); null until then.
  ensure(pid) {
    pid = +pid;
    if (!pid) return null;
    const have = this.find(pid);
    if (have || this.reading.has(pid) || Date.now() - (this.failedAt.get(pid) || 0) < 30e3) return have;
    this.reading.add(pid);
    projectApi(this.app, pid).get().then((r) => {
      this.views.set(pid, { ...(r && r.project ? r.project : r), home: projectHome(pid) });
      this.changed();
      this.app.session.changed();
    }, () => { this.failedAt.set(pid, Date.now()); }).finally(() => this.reading.delete(pid)); // a refused read isn't asked again at every paint
    return null;
  }
  view(pid = this.opened) { return pid == null ? null : this.find(pid); }

  // sections: the list as the page shows it — yours (your own space, or
  // an unpartitioned agent's), then team projects, archived ones last.
  sections() {
    const live = this.list.filter((p) => p.state !== 'archived');
    const team = live.filter((p) => p.kind === 'team' || p.kind === 'membership');
    return [
      { key: 'yours', title: twoHomes() ? 'Your projects' : 'Projects', items: live.filter((p) => !team.includes(p)) },
      { key: 'team', title: 'Team projects', items: team },
      { key: 'archived', title: 'Archived', items: this.list.filter((p) => p.state === 'archived') },
    ].filter((s) => s.items.length || s.key === 'yours');
  }

  // needsYou: tasks that need you across your projects (the sidebar entry's count).
  needsYou() { return this.list.reduce((n, p) => n + (p.state === 'active' ? (p.counts && p.counts['needs-you']) || 0 : 0), 0); }

  // --- one project --------------------------------------------------------------------

  // open shows a project's page (pid null: the list) and reads it.
  async open(pid, tab = 'board') {
    this.opened = pid == null ? null : +pid;
    this.tab = tab;
    this.form = null;
    this.taskForm = null;
    this.picker = null;
    this.draft = null;
    this.err = '';
    this.flash = '';
    this.route(this.opened);
    this.changed();
    if (this.opened != null) await this.refresh(this.opened);
  }

  // refresh reads the open project again: its view, tasks, and the tab's own data.
  async refresh(pid = this.opened) {
    if (pid == null) return;
    const api = projectApi(this.app, pid);
    try {
      const r = await api.get();
      const pv = { ...(r && r.project ? r.project : r), home: api.home };
      this.views.set(pid, pv);
      this.err = '';
    } catch (e) {
      if (e.status === 404 && this.opened === pid) { this.opened = null; this.route(null); this.err = 'That project is gone, or not yours to see.'; this.changed(); return; }
      this.err = e.message;
    }
    const def = (this.views.get(pid) || {}).kind === 'team'; // a team project's definition has no tasks (they run in each member's space)
    await Promise.all([def ? null : this.tasks(pid), this.tab === 'settings' ? this.loadSettings(pid) : null]);
    this.changed();
  }

  // tasks reads a project's tasks (the board's; filter: q, mine). Only the
  // latest read's answer is kept: an older one (an event's read, a word
  // typed before) that arrives after it is dropped.
  async tasks(pid, filter = this.filter, more = false) {
    const cur = this.taskRows.get(pid) || { items: [], next: '', loading: false, err: '', seq: 0 };
    const seq = cur.seq = (cur.seq || 0) + 1;
    cur.loading = true;
    this.taskRows.set(pid, cur);
    try {
      const r = await projectApi(this.app, pid).tasks({ q: filter.q, mine: filter.mine, limit: 200, cursor: more ? cur.next : '' });
      if (seq !== cur.seq) return cur.items;
      cur.items = more ? [...cur.items, ...((r && r.items) || [])] : (r && r.items) || [];
      cur.next = (r && r.next) || '';
      cur.err = '';
    } catch (e) { if (seq !== cur.seq) return cur.items; cur.err = e.message; }
    cur.loading = false;
    this.changed();
    return cur.items;
  }

  // board(pid): the columns with their tasks.
  board(pid = this.opened) { return columns((this.taskRows.get(pid) || {}).items || []); }
  taskList(pid = this.opened) { return this.taskRows.get(pid) || { items: [], next: '', loading: false, err: '' }; }

  setFilter(f) { this.filter = { ...this.filter, ...f }; if (this.opened != null) this.tasks(this.opened); this.changed(); }

  showTab(tab) {
    this.tab = tab;
    this.draft = null;
    this.changed();
    if (tab === 'settings' && this.opened != null) this.loadSettings(this.opened);
  }

  async loadSettings(pid) {
    const api = projectApi(this.app, pid);
    await Promise.all([
      this.status(pid).catch(() => {}),
      sharable(this.find(pid)) ? api.members().then((m) => { this.membersOf.set(pid, m); }, () => {}) : null,
    ]);
    this.changed();
  }

  async status(pid) {
    const s = await projectApi(this.app, pid).status();
    this.statusOf.set(pid, s);
    this.changed();
    return s;
  }

  // act runs a page action: its refusal is said on the page (pj.err), and
  // the project is read again after it.
  async act(fn, pid = this.opened) {
    this.err = '';
    try {
      const r = await fn();
      if (pid != null) await this.refresh(pid);
      return r;
    } catch (e) { this.fail(e); return null; }
  }

  warm(pid = this.opened) { return this.act(() => projectApi(this.app, pid).warm(), pid); }

  // patch: the owner's PATCH with the version the change was made at
  // (opts.version: a draft's or an input's, taken when the editing began;
  // else the version last read) — a stale one (412) reads the project again
  // and says so; nothing is overwritten.
  async patch(pid, body, opts = {}) {
    const pv = this.find(pid);
    try {
      const r = await projectApi(this.app, pid).patch({ version: opts.version ?? (pv ? pv.version : 0), ...body });
      if (r && r.project) this.views.set(pid, { ...r.project, home: projectHome(pid) });
      await this.load();
      return r;
    } catch (e) {
      if (e.status === 412) {
        await this.refresh(pid);
        const x = new Error('Someone changed this project meanwhile: it was read again — check it and save again.');
        x.status = 412;
        throw x;
      }
      throw e;
    }
  }

  archive(pid, on = true) { return this.act(() => this.patch(pid, { state: on ? 'archived' : 'active' }), pid); }

  // remove deletes a project (the view confirmed): sandbox 'keep' | 'delete'.
  async remove(pid, sandbox = 'keep') {
    this.err = '';
    try {
      await projectApi(this.app, pid).remove(sandbox);
      this.gone.add(pid);
      this.list = this.list.filter((p) => p.id !== pid);
      this.views.delete(pid);
      if (this.opened === pid) { this.opened = null; this.route(null); }
      this.flash = 'The project is being deleted.';
      this.changed();
      this.load().catch(() => {});
      return true;
    } catch (e) { this.fail(e); return false; }
  }

  // --- the policy (settings) ----------------------------------------------------------

  // The draft keeps the version it was read at and the keys the owner
  // changed: a save sends that version, so a change someone made meanwhile
  // (read in by a `project` event) is never saved over. On a 412 the draft
  // is made again from what was read, with the owner's keys set on it —
  // both changes shown, saved by the next Save.
  editPolicy(pid = this.opened) {
    const pv = this.find(pid);
    this.draft = { pid, version: pv ? pv.version : 0, policy: JSON.parse(JSON.stringify((pv && pv.policy) || {})), touched: new Map(), dirty: false, err: '', saved: false };
    this.changed();
  }
  setPolicy(path, value) {
    if (!this.draft) this.editPolicy();
    this.draft.policy = policySet(this.draft.policy, path, value);
    this.draft.touched.set(path, value);
    this.draft.dirty = true;
    this.draft.saved = false;
    this.changed();
  }
  async savePolicy() {
    const d = this.draft;
    if (!d) return;
    try {
      const r = await this.patch(d.pid, { policy: d.policy }, { version: d.version });
      const pv = (r && r.project) || this.find(d.pid);
      this.draft = { ...d, version: pv ? pv.version : d.version, touched: new Map(), dirty: false, err: '', saved: true };
    } catch (e) {
      if (e.status === 412 && this.draft === d) {
        const pv = this.find(d.pid);
        let policy = JSON.parse(JSON.stringify((pv && pv.policy) || {}));
        for (const [path, v] of d.touched) policy = policySet(policy, path, v);
        this.draft = { ...d, version: pv ? pv.version : d.version, policy, err: 'Someone changed this policy meanwhile: their changes are shown with yours — check it and save again.' };
      } else d.err = e.message;
    }
    this.changed();
  }

  // rename, share: version — the one the person's input began at.
  rename(pid, name, version) { return this.act(() => this.patch(pid, { name }, { version }), pid); }
  share(pid, visibility, teamRole, version) { return this.act(() => this.patch(pid, { visibility, teamRole }, { version }), pid); }

  // --- members --------------------------------------------------------------------------

  async addMember(pid, user, role = 'participant') {
    if (!String(user || '').trim()) return;
    await this.act(async () => { this.membersOf.set(pid, await projectApi(this.app, pid).addMember(user.trim(), role)); }, pid);
  }
  async removeMember(pid, user) {
    await this.act(async () => {
      await projectApi(this.app, pid).removeMember(user);
      this.membersOf.set(pid, await projectApi(this.app, pid).members());
    }, pid);
  }

  // --- repos ------------------------------------------------------------------------------

  addRepo(pid, repo, setup = '') { return this.act(() => projectApi(this.app, pid).addRepo({ repo, ...(setup ? { setup } : {}) }), pid); }
  setSetup(pid, slug, setup) { return this.act(() => projectApi(this.app, pid).patchRepo(slug, { setup }), pid); }
  setCheckout(pid, slug, checkout) { return this.act(() => projectApi(this.app, pid).patchRepo(slug, { checkout }), pid); }
  // removeRepo: 409 `busy` while open tasks use it — the view asks, then
  // force; any other refusal is said.
  async removeRepo(pid, slug, force = false) {
    try {
      await projectApi(this.app, pid).removeRepo(slug, force);
      await this.refresh(pid);
      return { ok: true };
    } catch (e) {
      if (e.status === 409 && e.refusal === 'busy' && !force) return { busy: e.message };
      this.fail(e);
      return { ok: false };
    }
  }

  // --- tasks ------------------------------------------------------------------------------

  newTask() {
    const pv = this.view();
    const all = ((pv && pv.repos) || []).map((r) => r.slug);
    this.taskForm = { text: '', title: '', size: 'small', agent: '', repos: [...all], err: '', busy: false, all };
    this.picker = null;
    this.changed();
  }
  setTask(k, v) { if (this.taskForm) { this.taskForm[k] = v; this.changed(); } }
  closeTask() { this.taskForm = null; this.changed(); }

  // specOf: the form as a TaskSpec — the agent a coding agent's id ('' the
  // policy's engine: agentChoices), repos only when some are left out.
  specOf(f) {
    const spec = { text: f.text.trim() };
    if (f.title.trim()) spec.title = f.title.trim();
    if (f.size === 'big') spec.size = 'big';
    if (f.agent) spec.agent = { provider: f.agent };
    if (f.repos.length && f.repos.length < (f.all || []).length) spec.repos = [...f.repos];
    return spec;
  }

  // createTask: POST /projects/{pid}/tasks — {task, run}.
  async createTask(pid, spec) {
    const r = await projectApi(this.app, pid).createTask(spec);
    await this.tasks(pid);
    this.load().catch(() => {});
    return r;
  }

  async submitTask() {
    const f = this.taskForm, pid = this.opened;
    if (!f || pid == null) return null;
    if (!f.text.trim()) { f.err = 'Say what to do.'; this.changed(); return null; }
    if (f.all.length && !f.repos.length) { f.err = 'Pick a repo to work in.'; this.changed(); return null; }
    f.busy = true; f.err = '';
    this.changed();
    try {
      const r = await this.createTask(pid, this.specOf(f));
      this.taskForm = null;
      this.flash = `Task #${r && r.task ? r.task.n : '?'} made.`;
      this.changed();
      return r;
    } catch (e) { f.err = e.message; f.busy = false; this.changed(); return null; }
  }

  cancelTask(pid, n, reason = '') { return this.act(() => projectApi(this.app, pid).cancel(n, reason), pid); }

  // --- issues (the batch picker) -------------------------------------------------------------

  openPicker() {
    const pv = this.view();
    const repos = (pv && pv.repos) || [];
    this.picker = { repo: repos[0] ? repos[0].repo : '', q: '', state: 'open', items: [], next: '', picked: new Map(), loading: false, err: '', size: 'small', agent: '', result: null };
    this.taskForm = null;
    this.changed();
    return this.searchIssues();
  }
  closePicker() { this.picker = null; this.changed(); }
  setPicker(k, v) { if (this.picker) { this.picker[k] = v; this.changed(); } }

  // issues(pid, q): one page of a project repo's issues (bodies clipped and untrusted).
  issues(pid, q) { return projectApi(this.app, pid).issues(q); }

  async searchIssues(more = false) {
    const p = this.picker;
    if (!p || this.opened == null) return;
    const seq = p.seq = (p.seq || 0) + 1; // only the latest search's answer is kept
    p.loading = true; p.err = '';
    this.changed();
    try {
      const r = await this.issues(this.opened, { repo: p.repo, q: p.q, state: p.state, cursor: more ? p.next : '' });
      if (seq !== p.seq) return;
      const items = ((r && r.items) || []).map((i) => ({ ...i, repo: p.repo }));
      p.items = more ? [...p.items, ...items] : items;
      p.next = (r && r.next) || '';
    } catch (e) { if (seq !== p.seq) return; p.err = e.message; }
    p.loading = false;
    this.changed();
  }

  togglePick(issue) {
    const p = this.picker;
    if (!p) return;
    const k = `${issue.repo}#${issue.number}`;
    if (p.picked.has(k)) p.picked.delete(k);
    else if (p.picked.size < 20) p.picked.set(k, { repo: issue.repo, number: issue.number });
    this.changed();
  }

  // batch: POST /projects/{pid}/tasks/batch — a task per issue (≤ 20).
  async batch(pid, issues, opts = {}) {
    const body = { issues };
    if (opts.size === 'big') body.size = 'big';
    if (opts.agent) body.agent = { provider: opts.agent };
    if (opts.text) body.text = opts.text;
    const r = await projectApi(this.app, pid).batch(body);
    await this.tasks(pid);
    this.load().catch(() => {});
    return r;
  }

  async submitBatch() {
    const p = this.picker, pid = this.opened;
    if (!p || pid == null || !p.picked.size) return null;
    p.loading = true; p.err = '';
    this.changed();
    try {
      const r = await this.batch(pid, [...p.picked.values()], { size: p.size, agent: p.agent });
      const made = (r && r.tasks) || [], errs = (r && r.errors) || [];
      if (!errs.length) this.picker = null;
      else { p.result = { made: made.length, errors: errs }; p.loading = false; p.picked = new Map(); }
      this.flash = `${made.length} task${made.length === 1 ? '' : 's'} made${errs.length ? `, ${errs.length} refused` : ''}.`;
      this.changed();
      return r;
    } catch (e) { p.err = e.message; p.loading = false; this.changed(); return null; }
  }

  // --- a task conversation ----------------------------------------------------------------------

  // taskView: the open conversation's TaskView read again (a `project`
  // event about its task), into the held view.
  async rereadTask(runId) {
    try {
      const t = await taskApi(runId).get();
      const v = this.app.session.views.get(runId);
      if (v) { v.projectTask = t; this.app.session.changed(); }
    } catch (e) {
      const v = this.app.session.views.get(runId);
      if (v && e.status === 404) { v.projectTask = null; this.app.session.changed(); }
    }
  }

  // prRouteAt(home): does that backend have "Open PR" (P2's route)? null
  // until known; asked once per home, without opening anything.
  prRouteAt(home, runId) {
    if (this.prRoute.has(home)) return this.prRoute.get(home);
    this.prRoute.set(home, null);
    taskApi(runId).prProbe().then((yes) => { this.prRoute.set(home, yes); this.changed(); this.app.session.changed(); });
    return null;
  }

  async taskAction(runId, what, opts) {
    const api = taskApi(runId);
    try {
      const r = await api[what](opts);
      this.rereadTask(runId);
      return r;
    } catch (e) {
      // a route this backend lacks answers the mux's plain 404, a refusal of the route a JSON one
      if (what === 'pr' && e.status === 404 && !(e.data && typeof e.data === 'object')) { this.prRoute.set(api.home, false); this.app.session.changed(); }
      throw e;
    }
  }

  // --- the new-project form ------------------------------------------------------------------------

  // At a partitioned agent's global instance the form makes a team
  // project's definition: shared with the team at once (or with the members
  // added next), its seed sandbox optional.
  async newProject() {
    this.opened = null;
    this.route(null);
    const team = partitionState() === 'global';
    this.form = { name: '', scm: '', repos: [], q: '', results: [], next: '', loading: false, err: '', busy: false,
      sandbox: { mode: team ? 'none' : 'new', ref: '', provider: '', image: '', size: '', egress: '' },
      policy: { maxTasks: 3, autoPR: 'off', engine: 'auto', harness: '', as: '' },
      share: { visibility: team ? 'team' : 'private', teamRole: 'viewer' } };
    this.changed();
    await this.providers();
    const f = this.form;
    if (f && !f.scm) { const p = this.usableProviders()[0]; if (p) f.scm = p.scm; }
    this.changed();
    if (f && f.scm) this.searchRepos();
  }
  closeForm() { this.form = null; this.changed(); }
  setForm(k, v) { if (this.form) { this.form[k] = v; this.changed(); } }
  setFormPart(part, k, v) { if (this.form) { this.form[part] = { ...this.form[part], [k]: v }; this.changed(); } }

  // providers: GET /projects/scm, as this page's home sees them (cached; fresh to read again).
  async providers(fresh = false) {
    if (this.scm && !fresh) return this.scm;
    try { const r = await scmApi('').providers(); this.scm = { providers: (r && r.providers) || [], error: '' }; } catch (e) {
      this.scm = { providers: [], error: e.status === 404 ? 'This agent has no scm slot yet.' : e.message };
    }
    this.changed();
    return this.scm;
  }
  usableProviders() { return ((this.scm && this.scm.providers) || []).filter((p) => !p.error); }
  provider(scm) { return ((this.scm && this.scm.providers) || []).find((p) => p.scm === scm) || null; }

  // searchRepos: the repos you can name through the form's provider (GET /projects/scm/repos).
  async searchRepos(more = false) {
    const f = this.form;
    if (!f || !f.scm) return;
    const seq = f.seq = (f.seq || 0) + 1; // only the latest search's answer is kept
    f.loading = true; f.err = '';
    this.changed();
    try {
      const r = await scmApi('').repos(f.scm, f.q, more ? f.next : '');
      if (seq !== f.seq) return;
      const items = (r && r.items) || [];
      f.results = more ? [...f.results, ...items] : items;
      f.next = (r && r.next) || '';
    } catch (e) {
      if (seq !== f.seq) return;
      f.err = e.refusal === 'signin' ? 'Sign in to the provider to see your repos.' : e.message; f.refusal = e.refusal || ''; }
    f.loading = false;
    this.changed();
  }
  addFormRepo(repo) {
    const f = this.form;
    if (!f || !repo || f.repos.some((r) => r.repo === repo)) return;
    f.repos = [...f.repos, { repo, setup: '' }];
    if (!f.name) f.name = repo.split('/').pop();
    this.changed();
  }
  dropFormRepo(repo) { if (this.form) { this.form.repos = this.form.repos.filter((r) => r.repo !== repo); this.changed(); } }
  setFormSetup(repo, setup) { if (this.form) { this.form.repos = this.form.repos.map((r) => (r.repo === repo ? { ...r, setup } : r)); this.changed(); } }

  // formBody: the form as POST /projects' body, or {error} saying what is missing.
  formBody(f = this.form) {
    if (!f) return { error: 'no form' };
    if (!f.name.trim()) return { error: 'Name the project.' };
    if (!f.scm) return { error: 'Pick a provider.' };
    if (!f.repos.length) return { error: 'Add a repo.' };
    const state = partitionState();
    const team = state === 'global'; // a partitioned agent's global instance holds team definitions only
    const sb = f.sandbox;
    let sandbox;
    if (sb.mode === 'pick') {
      if (!sb.ref) return { error: 'Pick a sandbox, or make a new one.' };
      sandbox = { ref: sb.ref };
    } else if (sb.mode === 'none' && team) {
      sandbox = undefined; // a team definition's seed sandbox is optional
    } else {
      if (!sb.provider) return { error: 'No sandbox manager to make a sandbox with.' };
      sandbox = { new: { provider: sb.provider, ...(sb.image ? { image: sb.image } : {}), ...(sb.size ? { size: sb.size } : {}), ...(sb.egress ? { egress: sb.egress } : {}) } };
    }
    const policy = { maxTasks: Number(f.policy.maxTasks) || 3, autoPR: f.policy.autoPR || 'off', engine: f.policy.engine || 'auto' };
    if (f.policy.harness) policy.harness = f.policy.harness;
    if (f.policy.as) policy.as = f.policy.as;
    const body = { name: f.name.trim(), scm: f.scm, repos: f.repos.map((r) => ({ repo: r.repo, ...(r.setup.trim() ? { setup: r.setup } : {}) })), ...(sandbox ? { sandbox } : {}), policy };
    const shared = { visibility: f.share.visibility === 'team' ? 'team' : 'private', teamRole: f.share.teamRole === 'participant' ? 'participant' : 'viewer', members: [] };
    if (team) { body.kind = 'team'; body.share = shared; } // a person there must send share; private: only the members added next
    else if (state === 'legacy' && f.share.visibility === 'team') body.share = shared;
    return { body };
  }

  // create: POST /projects, then its page.
  async create(body) {
    const r = await createProject(body.kind === 'team' && twoHomes() ? 'global' : '', body);
    await this.load();
    return r;
  }

  async saveProject() {
    const f = this.form;
    const { body, error } = this.formBody(f);
    if (error) { f.err = error; this.changed(); return null; }
    f.busy = true; f.err = '';
    this.changed();
    try {
      const r = await this.create(body);
      this.form = null;
      if (r && r.project) await this.open(r.project.id);
      return r;
    } catch (e) { f.err = e.message; f.busy = false; this.changed(); return null; }
  }

  // --- signing in to a provider (the person's own, in their partition) ---------------------------

  signinOf(scm) { return this.signins.get(scm) || null; }

  // signinState: GET /projects/scm/signin. A pending one (a parked task's,
  // one started elsewhere) is followed; an answer that comes after a sign-in
  // was started or forgotten meanwhile changes nothing.
  async signinState(scm) {
    const gen = this.pollGen.get(scm) || 0;
    const s = await scmApi('').signin(scm).catch((e) => ({ state: 'none', err: e.message }));
    if ((this.pollGen.get(scm) || 0) !== gen) return this.signins.get(scm);
    if (s && s.state === 'pending' && s.signin && s.signin.pollId) {
      if (this.following.get(scm) === s.signin.pollId) this.signins.set(scm, { ...s, pollId: s.signin.pollId });
      else this.pollSignin(scm, s.signin);
    } else this.signins.set(scm, s);
    this.changed();
    return this.signins.get(scm);
  }

  // signin starts the device flow ({state: pending, signin: {url, userCode, pollId, intervalMs}})
  // and polls until it is done, denied or expired.
  async signin(scm) {
    const gen = this.stopPoll(scm);
    const s = await scmApi('').startSignin(scm).catch((e) => ({ state: 'error', err: e.message }));
    if (this.pollGen.get(scm) !== gen) return s; // another sign-in, or Forget, meanwhile
    this.signins.set(scm, s && s.signin ? { ...s, pollId: s.signin.pollId } : s);
    this.changed();
    if (s.state === 'pending' && s.signin) this.pollSignin(scm, s.signin);
    return s;
  }

  // stopPoll ends scm's sign-in poll — its timer, and an answer still on its
  // way (polls are numbered: an earlier one's tick neither writes nor goes
  // on) — and returns the new number.
  stopPoll(scm) {
    clearTimeout(this.polls.get(scm));
    this.polls.delete(scm); this.following.delete(scm);
    this.pollGen.set(scm, (this.pollGen.get(scm) || 0) + 1);
    return this.pollGen.get(scm);
  }

  // pollSignin follows one sign-in (si.pollId) until it is done, denied or
  // expired. The state it keeps names that pollId — a `done` of an earlier
  // sign-in never counts for this one — and an error of the poll itself is
  // tried again, later each time (5 s doubling to a minute; after 8 in a
  // row it stops, state 'error', and the card offers Check again).
  pollSignin(scm, si) {
    const gen = this.stopPoll(scm);
    const pollId = si.pollId; this.following.set(scm, pollId);
    const cur = this.signins.get(scm);
    if (!cur || cur.pollId !== pollId) this.signins.set(scm, { state: 'pending', signin: si, pollId });
    let fails = 0;
    const every = () => Math.max(1000, si.intervalMs || 5000);
    const live = () => this.pollGen.get(scm) === gen;
    const end = () => { this.polls.delete(scm); this.following.delete(scm); };
    const tick = async () => {
      let s;
      try { s = await scmApi('').pollSignin(scm, pollId); fails = 0; } catch (e) {
        if (!live()) return;
        fails++;
        const giveUp = fails >= 8;
        this.signins.set(scm, { state: giveUp ? 'error' : 'pending', signin: si, pollId, err: e.message });
        this.changed();
        if (giveUp) end();
        else this.polls.set(scm, setTimeout(tick, Math.min(60e3, 5000 * 2 ** (fails - 1))));
        return;
      }
      if (!live()) return;
      const keep = s.state === 'pending' ? { ...s, signin: s.signin || si, pollId } : { ...s, pollId };
      this.signins.set(scm, keep);
      this.changed();
      if (s.state === 'pending') this.polls.set(scm, setTimeout(tick, Math.max(1000, s.retryAfterMs || every())));
      else {
        end();
        if (s.state === 'done') { this.providers(true); if (this.form && this.form.scm === scm) this.searchRepos(); }
      }
    };
    this.polls.set(scm, setTimeout(tick, every()));
  }

  // forget: DELETE /projects/scm/signin — every credential of your projects is scrubbed first.
  async forget(scm) {
    const gen = this.stopPoll(scm);
    try { await scmApi('').forget(scm); if (this.pollGen.get(scm) === gen) this.signins.set(scm, { state: 'none' }); this.providers(true); } catch (e) { this.fail(e); }
    this.changed();
  }

  // pending, accept: a membership's changed definition (team projects, U2's surface).
  pending(pid) { return projectApi(this.app, pid).pending(); }
  async accept(pid, hash) { const r = await projectApi(this.app, pid).accept(hash); await this.refresh(pid); return r; }

  // --- the stream ---------------------------------------------------------------------------------------

  // take sees every stream event: a `project` event marks its project
  // dirty, and what shows it is read again 250 ms later (coalesced).
  take(ev) {
    if (!ev || ev.type !== 'project') return;
    const d = ev.data || {};
    const pid = +d.id;
    if (!pid) return;
    if (d.change === 'deleted') {
      this.gone.add(pid);
      this.list = this.list.filter((p) => p.id !== pid);
      this.views.delete(pid);
      this.taskRows.delete(pid);
      if (this.opened === pid) { this.opened = null; this.route(null); this.flash = 'That project was deleted.'; }
      this.changed();
      return;
    }
    const v = this.app.session && this.app.session.current && this.app.session.current();
    if (v && v.project && +v.project.id === pid && v.projectTask && (d.n == null || +d.n === +v.projectTask.n)) this.dirty.add(`task:${v.run.id}`);
    this.dirty.add(pid);
    if (!this.timer) this.timer = setTimeout(() => this.flush(), 250);
  }

  async flush() {
    this.timer = null;
    const dirty = [...this.dirty];
    this.dirty.clear();
    const jobs = [this.load().catch(() => {})];
    for (const k of dirty) {
      if (typeof k === 'string' && k.startsWith('task:')) jobs.push(this.rereadTask(+k.slice(5)));
      else if (k === this.opened && this.app.page === 'projects') jobs.push(this.refresh(k));
    }
    await Promise.all(jobs);
  }

  // column of a task, for a view that holds one (re-exported for both views).
  columnOf(t) { return columnOf(t); }
}
