// model/project-upgrade.js — "Make this a project…" in both views (API.md
// §Projects in the UI): a conversation with a sandbox and git repos in it
// becomes a project, the conversation its first task (API.md §Big tasks,
// upgrades and pull requests: GET /runs/{id}/project/detect, then
// POST /runs/{id}/project).
//
//   upgradeOffer(view) → {shown, why}      where the action is offered
//   const u = projectUpgrade(app);         one per app, made on first use
//   u.open(runId) → the form, read from the sandbox (detect);
//   u.toggle(runId, path), u.set(runId, k, v), u.submit(runId)
//   forkBase(pid)                          big tasks' fork base, now (P2's route)
//
// A candidate's remote comes without its userinfo (the backend drops it);
// one that held credentials, or an ssh remote, may be switched to https so
// the project's own credential helper serves it. Emits `projects`. No lit,
// no DOM.
import { projCall, projectHome } from './project-api.js';
import { homeOf } from './homes.js';
import { bindingOf } from './sandboxes.js';
import { hostingOf } from './hosted.js';
import { partitionState } from './partition.js';
import { access } from './rules.js';
import { plain } from './project-feed.js';

/**
 * upgradeOffer(view): may this conversation be made a project? A root
 * conversation of yours (its owner), with a sandbox bound, that is no
 * project's already and isn't hosted — and not at a partitioned agent's
 * shared space, which holds team definitions only (no tasks).
 */
export function upgradeOffer(v) {
  if (!v || !v.run) return { shown: false };
  const r = v.run;
  if (v.project || r.origin === 'project') return { shown: false };
  if ((r.parentId && r.parentId !== r.id) || (r.rootId && r.rootId !== r.id)) return { shown: false };
  if (!bindingOf(v) || hostingOf(v) || !access(v).own) return { shown: false };
  if (partitionState() === 'global') return { shown: false };
  return { shown: true, title: 'turn this conversation into a project — it becomes the project\'s first task, its sandbox the project\'s' };
}

/** candidateWords(c): a repo found in the sandbox, as a row — {path, repo, title, detail, usable, why, https}. */
export function candidateWords(c) {
  const branch = c.branch ? `on ${plain(c.branch, 120)}` : 'no branch checked out';
  const dirty = c.dirty ? `${c.dirty} uncommitted change${c.dirty === 1 ? '' : 's'}` : '';
  const why = !c.repo ? 'its remote names no repo' : !c.scm ? `no scm provider serves ${plain(c.host || 'its host', 80)}` : '';
  return {
    path: c.path, repo: plain(c.repo, 200), title: c.repo ? plain(c.repo, 200) : plain(c.path, 200),
    detail: [plain(c.path, 200), branch, dirty, c.ssh ? 'ssh remote' : '', c.hasCredentials ? 'its remote held credentials' : ''].filter(Boolean).join(' · '),
    usable: !why, why, scm: c.scm || '',
    https: !!(c.ssh || c.hasCredentials), // offer switching its remote to https (the project's credential helper)
  };
}

const basename = (p) => String(p || '').replace(/\/+$/, '').split('/').pop();

class Upgrade {
  constructor(app) {
    this.app = app;
    this.forms = new Map(); // runId → the form
  }
  changed() { this.app.emit('projects'); }
  form(runId) { return this.forms.get(+runId) || null; }
  close(runId) { this.forms.delete(+runId); this.changed(); }

  // open reads the sandbox's repos (detect) into a fresh form: every usable
  // candidate picked, its branch kept, an ssh or credentialed remote switched
  // to https.
  async open(runId) {
    runId = +runId;
    const f = { runId, loading: true, err: '', busy: false, sandbox: '', cwd: '', candidates: [], picked: new Set(), https: new Set(), name: '', branch: 'keep', done: null };
    this.forms.set(runId, f);
    this.changed();
    try {
      const d = await projCall(homeOf(runId), `/runs/${runId}/project/detect`);
      if (this.form(runId) !== f) return f;
      f.sandbox = plain((d && d.sandbox) || '', 200); f.cwd = plain((d && d.cwd) || '', 400);
      f.candidates = ((d && d.candidates) || []).map(candidateWords);
      const first = f.candidates.find((c) => c.usable);
      for (const c of f.candidates) if (c.usable && (!first || c.scm === first.scm)) { f.picked.add(c.path); if (c.https) f.https.add(c.path); }
      f.name = first ? basename(first.repo) : '';
    } catch (e) {
      f.err = e.status === 404 && !(e.data && typeof e.data === 'object') ? 'This agent can\'t make a project from a conversation yet.' : e.message;
    }
    f.loading = false;
    this.changed();
    return f;
  }

  set(runId, k, v) { const f = this.form(runId); if (f) { f[k] = v; this.changed(); } }

  // toggle picks or leaves out a candidate; the repos of one project share a provider.
  toggle(runId, path) {
    const f = this.form(runId);
    if (!f) return;
    const c = f.candidates.find((x) => x.path === path);
    if (!c || !c.usable) return;
    if (f.picked.has(path)) { f.picked.delete(path); f.https.delete(path); } else {
      for (const p of [...f.picked]) if ((f.candidates.find((x) => x.path === p) || {}).scm !== c.scm) { f.picked.delete(p); f.https.delete(p); }
      f.picked.add(path);
      if (c.https) f.https.add(path);
    }
    if (!f.name) f.name = basename(c.repo);
    this.changed();
  }
  toggleHttps(runId, path) { const f = this.form(runId); if (!f) return; if (f.https.has(path)) f.https.delete(path); else f.https.add(path); this.changed(); }

  /** body(form): POST /runs/{id}/project's body, or {error}. */
  body(f) {
    const picked = f.candidates.filter((c) => f.picked.has(c.path));
    if (!picked.length) return { error: 'Pick a repo of the sandbox.' };
    if (!String(f.name || '').trim()) return { error: 'Name the project.' };
    const switchHttps = picked.filter((c) => c.https && f.https.has(c.path)).map((c) => c.path);
    return { body: { name: f.name.trim(), scm: picked[0].scm, repos: picked.map((c) => ({ path: c.path, repo: c.repo })),
      branch: f.branch === 'new' ? 'new' : 'keep', ...(switchHttps.length ? { switchHttps } : {}) } };
  }

  // submit makes the project; the conversation is read again (it is now task 1, with its crumb and chips).
  async submit(runId) {
    const f = this.form(runId);
    if (!f) return null;
    const { body, error } = this.body(f);
    if (error) { f.err = error; this.changed(); return null; }
    f.busy = true; f.err = '';
    this.changed();
    try {
      const r = await projCall(homeOf(runId), `/runs/${runId}/project`, 'POST', body);
      f.busy = false;
      f.done = { project: (r && r.project) || null, task: (r && r.task) || null };
      this.app.projects.load().catch(() => {});
      if (this.app.session && this.app.session.refresh) this.app.session.refresh(+runId).catch(() => {});
      this.changed();
      return r;
    } catch (e) { f.err = e.message; f.busy = false; this.changed(); return null; }
  }
}

/** forkBaseOffer(pv): may its owner take the big tasks' fork base now? A
 * project of theirs whose big tasks fork its sandbox (policy.bigTasks.mode). */
export const forkBaseOffer = (pv) => !!pv && pv.level === 'owner' && pv.kind !== 'team' && pv.state === 'active'
  && ((pv.policy && pv.policy.bigTasks && pv.policy.bigTasks.mode) || 'fork') === 'fork';

/** forkBase(pid): POST /projects/{pid}/fork-base {now: true} — stops the
 * project's sandbox to snapshot it (the view confirms first); {job}. */
export const forkBase = (pid) => projCall(projectHome(pid), `/projects/${pid}/fork-base`, 'POST', { now: true });

/** projectUpgrade(app): the app's upgrade forms, made on first use. */
export function projectUpgrade(app) {
  if (!app.projUpgrade) app.projUpgrade = new Upgrade(app);
  return app.projUpgrade;
}
