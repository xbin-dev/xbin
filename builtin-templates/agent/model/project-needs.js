// model/project-needs.js — what waits on you in one project (#56), for both
// views: the "Waiting for you" strip atop a project's page — your tasks'
// questions, approvals and sign-ins (red), their failures (yellow) and
// your coordinator's own question — from GET /projects/{pid}/needs, read
// when the page shows it (each opening) and again on the project's
// `project` events (coalesced). A pull request ready for review stays a count, never a row
// (the conversation list's split: model/project-pins.js needSplit). No lit,
// no DOM.
//
//   const nd = projectNeeds(app);   // one per app, made on first use
//   nd.ensure(pid) → {items, loaded, loading, err}: read at each opening of
//                    its page (app.projects.opens); nd.load(pid)
//   needRows(items, now) → [{key, reason, tone ('bad' | 'warn'), n (the task's;
//                    0 the coordinator), who ('#3' | 'Coordinator'), title, words,
//                    text, age, run}] — red first,
//                    then the longest waiting
import { projectApi } from './project-api.js';
import { needWords } from './home.js';
import { plain } from './project-feed.js';
import { ago } from './project-overview.js';

const COALESCE = 400;

// RED: a person's answer holds it up — a question, an approval (a task's:
// action), a coding agent's sign-in. A workspace's park is red too unless
// it failed; failures and a turn that stopped part-way are yellow.
const RED = new Set(['question', 'approval', 'action', 'login']);
const WS_WORDS = { signin: 'its workspace needs you to sign in to the scm provider', failed: 'its workspace failed',
  blocked: 'its cleanup needs you (work that isn\'t pushed)' };

/** toneOf(item): 'bad' (red) or 'warn' (yellow). */
export const toneOf = (n) => (n.reason === 'project' ? (n.ws === 'failed' ? 'warn' : 'bad') : RED.has(n.reason) ? 'bad' : 'warn');

/** ageWords(since, now): "just now", "5 m ago" — '' unknown. */
export function ageWords(since, now = Date.now()) {
  const a = ago(since, now);
  return !a ? '' : a === 'now' ? 'just now' : `${a} ago`;
}

/** needRows(items, now): the strip's rows (a review is a count, not a row). */
export function needRows(items, now = Date.now()) {
  const rows = (items || []).filter((n) => n && n.run && n.reason !== 'review').map((n) => {
    const k = n.project && +n.project.n;
    const tone = toneOf(n);
    return {
      key: `${n.reason}:${n.run.id}:${n.subRun || ''}`, reason: n.reason, tone, n: k || 0,
      who: k ? `#${k}` : 'Coordinator', title: k ? plain(n.run.title, 80) : '',
      words: n.reason === 'project' ? WS_WORDS[n.ws] || 'its workspace needs you' : needWords(n),
      text: plain(n.text || '', 240), age: ageWords(n.since, now), since: +n.since || 0,
      run: n.subRun || n.run.id,
    };
  });
  return rows.sort((a, b) => (a.tone === 'bad' ? 0 : 1) - (b.tone === 'bad' ? 0 : 1) || (a.since || Infinity) - (b.since || Infinity));
}

class Needs {
  constructor(app) {
    this.app = app;
    this.items = new Map(); // pid → {items, loaded, loading, err, seq}
    this.timer = null;
    const pj = app.projects;
    if (pj && pj.take) {
      const take = pj.take.bind(pj);
      pj.take = (ev) => { take(ev); this.take(ev); };
    }
  }

  changed() { this.app.emit('projects'); }

  item(pid) {
    let it = this.items.get(+pid);
    if (!it) { it = { items: [], loaded: false, loading: false, err: '', seq: 0 }; this.items.set(+pid, it); }
    return it;
  }

  // ensure: read the first time a page draws it, and again at each opening of the page (pj.opens) —
  // what was answered meanwhile in its conversation goes
  ensure(pid) {
    const it = this.item(pid);
    const opens = (this.app.projects && this.app.projects.opens) || 0;
    if ((!it.loaded || it.opens !== opens) && !it.loading) { it.opens = opens; this.load(pid); }
    return it;
  }

  async load(pid) {
    pid = +pid;
    const it = this.item(pid);
    if (it.loading) { it.again = true; return; }
    it.loading = true;
    const seq = ++it.seq;
    try {
      const r = await projectApi(this.app, pid).needs();
      if (seq !== it.seq) return;
      it.items = (r && r.items) || []; it.err = '';
    } catch (e) {
      if (seq !== it.seq) return;
      it.err = e.message; // the strip stays as it was: Needs you at home still lists them
    }
    it.loading = false; it.loaded = true;
    this.changed();
    if (it.again) { it.again = false; this.load(pid); }
  }

  // take: a `project` event of the open project reads its needs again (coalesced) —
  // a task or the coordinator started or stopped waiting
  take(ev) {
    if (!ev || ev.type !== 'project' || !ev.data) return;
    const pid = +ev.data.id;
    if (ev.data.change === 'deleted') { this.items.delete(pid); return; }
    if (pid !== (this.app.projects && this.app.projects.opened) || !this.items.has(pid)) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.load(pid), COALESCE);
  }
}

/** projectNeeds(app): the app's store of projects' needs, made on first use. */
export function projectNeeds(app) {
  if (!app.projNeeds) app.projNeeds = new Needs(app);
  return app.projNeeds;
}
