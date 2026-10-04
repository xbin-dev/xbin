// model/project-team.js — team projects in both views (API.md §Projects in
// the UI): the team board of a team project's definition (its members'
// tasks, never their transcripts), "Work on this" — your own half of it in
// your own space, after you have seen the definition's security part —
// reviewing the team's changes before they apply to your half, and making
// a team project's definition from your own space.
//
//   const t = projectTeam(app);   // one per app, made on first use
//   t.board(gpid) → {items, loading, err}; t.load(gpid); t.rows(gpid, me)
//   t.work(gpid) → the "Work on this" state; t.startWork(gpid), t.submitWork(gpid)
//   t.review(pid) → the pending changes; t.ensureReview(pv) (it re-reads the
//   definition once each time the membership's page opens), t.acceptReview(pid)
//
// A board row's text (title, waiting, branch, CI's current step) comes
// from each member's partition: clipped and drawn as plain text, never
// markdown or HTML; its links only https ones. A row's conversation opens
// only for its own member (the run lives in their space). Kept current by
// the `project` stream event (change `board`): app.projects.take is wrapped
// once. Emits `projects`. No lit, no DOM.
import { projCall, qs, projectHome } from './project-api.js';
import { COLUMNS, stateWords, prChip, WAITING } from './project-task.js';
import { partitionState } from './partition.js';
import { PARTITION_BASE } from './homes.js';
import { plain, httpsUrl } from './project-feed.js';

const CI_TONE = { pending: 'run', queued: 'run', running: 'run', success: 'ok', failure: 'bad', error: 'bad', cancelled: 'idle', neutral: 'idle' };

/** boardWords(row, me, owner): a team board row as a view draws it. */
export function boardWords(r, me, owner = false) {
  const who = me && typeof me === 'object' ? me.user : me;
  const waiting = r.waiting ? (WAITING[r.waiting] || plain(r.waiting, 200)) : '';
  const state = stateWords({ state: r.state });
  const prs = (r.prs || []).map((p) => prChip({ ...p, url: httpsUrl(p.url) }, r.ci || null));
  const ci = r.ci ? { text: `CI ${plain(r.ci.state, 20)}${r.ci.current ? ' · ' + plain(r.ci.current, 200) : ''}`, tone: CI_TONE[r.ci.state] || 'idle', url: httpsUrl(r.ci.url) } : null;
  const mine = !!who && r.member === who;
  return {
    key: `${r.member}:${r.n}`, who: String(r.member || ''), num: Number(r.n) || 0, member: plain(r.member, 80), n: `#${r.n}`, title: plain(r.title, 200) || `task ${r.n}`,
    col: COLUMNS.some((c) => c.key === r.col) ? r.col : 'working',
    state: { text: waiting && !state.text.includes(waiting) ? `${state.text} · ${waiting}` : state.text, tone: state.tone },
    branch: plain(r.branch, 200), prs, ci, stale: !!r.stale,
    // the member's own run, in their own space: only they open it (and only from it)
    open: mine && !r.stale && Number(r.run) >= PARTITION_BASE && partitionState() === 'user' ? Number(r.run) : 0,
    hide: !!owner,
  };
}

/** boardColumns(rows): the board's columns, each with its rows (stale ones last). */
export function boardColumns(words) {
  return COLUMNS.map((c) => ({ ...c, rows: words.filter((w) => w.col === c.key).sort((a, b) => Number(a.stale) - Number(b.stale)) }));
}

// The security part of a definition (repos' setup scripts and the policy
// keys that follow only on acceptance), as rows a view compares side by side.
const show = (v) => (v == null || v === '' ? '' : typeof v === 'string' ? v : Array.isArray(v) && v.every((x) => typeof x === 'string') ? v.join('\n') : JSON.stringify(v, null, 1));
const policyOf = (part) => {
  if (!part || typeof part !== 'object') return {};
  if (part.policy && typeof part.policy === 'object') return part.policy;
  const { repos: _r, ...rest } = part;
  return rest;
};
const setups = (part) => new Map(((part && part.repos) || []).map((r) => [r.repo, r.setup || '']));

/**
 * securityDiff(accepted, pending): {repos: [{repo, accepted, pending, kind}],
 * keys: [{key, accepted, pending, changed}]} — kind same | changed | added |
 * removed; the text plain (setup scripts and instructions are shown in
 * full, as the member must read what they accept). pending null: the
 * accepted part alone (a definition about to be accepted for the first time
 * is `pending` with nothing accepted).
 */
export function securityDiff(accepted, pending) {
  const a = setups(accepted), p = setups(pending);
  const repos = [...new Set([...a.keys(), ...p.keys()])].sort().map((repo) => {
    const kind = !pending ? 'same' : !a.has(repo) ? (accepted ? 'added' : 'same') : !p.has(repo) ? 'removed' : a.get(repo) !== p.get(repo) ? 'changed' : 'same';
    return { repo, accepted: a.has(repo) ? a.get(repo) : null, pending: p.has(repo) ? p.get(repo) : null, kind };
  });
  const pa = policyOf(accepted), pp = policyOf(pending);
  const keys = [...new Set([...Object.keys(pa), ...Object.keys(pp)])].sort().map((key) => {
    const x = show(pa[key]), y = show(pp[key]);
    return { key, accepted: x, pending: y, changed: !!pending && !!accepted && x !== y };
  });
  return { repos, keys };
}

class Team {
  constructor(app) {
    this.app = app;
    this.boards = new Map();  // gpid → {items, next, loading, err, seq}
    this.works = new Map();   // gpid → "Work on this": {sandbox, defHash, definition, err, busy}
    this.reviews = new Map(); // membership pid → {hash, accepted, pending, loading, err, busy, note}
    this.seeds = new Map();   // definition pid → the seed being set: {ref, busy, err, note}
    this.boardT = new Map();  // gpid → a coalesced board read's timer
    this.due = new Set();     // membership pids whose page opened and whose definition isn't re-read yet
    this.syncing = new Set(); // … being re-read now
    const pj = app.projects;
    const take = pj.take.bind(pj);
    pj.take = (ev) => { take(ev); this.take(ev); };
    // a membership re-reads its definition when its page opens (§Team
    // projects): each open marks it due, its first draw reads it
    const open = pj.open.bind(pj);
    pj.open = (pid, ...rest) => { if (pid != null) this.due.add(+pid); return open(pid, ...rest); };
    if (pj.opened != null) this.due.add(+pj.opened);
  }

  changed() { this.app.emit('projects'); }

  // --- the board (a team project's definition) ---------------------------------------------

  board(gpid) { return this.boards.get(+gpid) || { items: [], next: '', loading: false, err: '', loaded: false }; }

  // load reads the board — every page (at most 20). Only the latest read's answer is kept.
  async load(gpid) {
    gpid = +gpid;
    const b = { ...this.board(gpid) };
    const seq = b.seq = (this.board(gpid).seq || 0) + 1;
    b.loading = true;
    this.boards.set(gpid, b);
    this.changed();
    const items = [];
    let cursor = '', err = '';
    try {
      for (let i = 0; i < 20; i++) {
        const r = await projCall(projectHome(gpid), `/projects/${gpid}/board${qs({ cursor })}`);
        items.push(...((r && r.items) || []));
        cursor = (r && r.next) || '';
        if (!cursor) break;
      }
    } catch (e) { err = e.status === 404 && !(e.data && typeof e.data === 'object') ? 'This agent has no team board yet.' : e.message; }
    if (this.board(gpid).seq !== seq) return;
    this.boards.set(gpid, { items: err ? b.items : items.filter((r) => !r.hidden && r.state !== 'deleted'), next: '', loading: false, err, loaded: true, seq });
    this.changed();
  }

  ensureBoard(gpid) { const b = this.board(gpid); if (!b.loaded && !b.loading) this.load(gpid); return b; }

  /** rows(gpid): the board's rows as words, for whoever looks (me, and whether they own the definition). */
  rows(gpid) {
    const pv = this.app.projects.find(gpid);
    const owner = !!pv && pv.level === 'owner';
    return this.board(gpid).items.map((r) => boardWords(r, this.app.me, owner));
  }

  // hide: the owner hides a row (a member who left, or one not worth showing).
  async hide(gpid, member, n) {
    const b = this.board(gpid);
    try {
      await projCall(projectHome(gpid), `/projects/${gpid}/board/${encodeURIComponent(member)}/${n}/hide`, 'POST');
      this.boards.set(+gpid, { ...b, items: b.items.filter((r) => !(r.member === member && +r.n === +n)), err: '' });
    } catch (e) { this.boards.set(+gpid, { ...b, err: e.message }); }
    this.changed();
  }

  // --- the seed sandbox (the definition's owner) -------------------------------------------

  /** seedChoices(pv): a definition owner's picks for its seed — your sandboxes
   * where the definition lives that the team can see (members' sandboxes
   * fork only from one they can see; it holds no sign-in). null when there
   * is nothing to pick: not the owner, or a seed is set already (the
   * backend keeps the first one: the view shows it, read-only). */
  seedChoices(pv) {
    if (!pv || pv.kind !== 'team' || pv.level !== 'owner' || pv.sandboxRef) return null;
    const home = projectHome(pv.id);
    this.app.sbx.ensure('', home);
    return ((this.app.sbx.listAt(home) || {}).sandboxes || []).filter((x) => x.mine && x.visibility === 'team' && !['deleting', 'archived', 'error'].includes(x.state));
  }
  seed(pid) { return this.seeds.get(+pid) || { ref: '', busy: false, err: '', note: '' }; }
  setSeedRef(pid, ref) { this.seeds.set(+pid, { ...this.seed(pid), ref }); this.changed(); }

  // setSeed: POST /projects/{pid}/seed {sandbox: {ref}} — the seed's jobs start (no credential ever goes in).
  async setSeed(pid) {
    const cur = this.seed(pid);
    if (!cur.ref) { this.seeds.set(+pid, { ...cur, err: 'Pick a sandbox shared with the team.' }); this.changed(); return null; }
    this.seeds.set(+pid, { ...cur, busy: true, err: '', note: '' });
    this.changed();
    try {
      const r = await projCall(projectHome(pid), `/projects/${pid}/seed`, 'POST', { sandbox: { ref: cur.ref } });
      this.seeds.set(+pid, { ref: '', busy: false, err: '', note: 'The seed is being prepared — members\' new sandboxes fork from it once it is ready.' });
      await this.app.projects.refresh(+pid);
      return r;
    } catch (e) {
      this.seeds.set(+pid, { ...cur, busy: false, err: e.status === 404 && !(e.data && typeof e.data === 'object') ? 'This agent can\'t set a team project\'s seed yet.' : e.message });
      this.changed();
      return null;
    }
  }

  // --- your half of it ---------------------------------------------------------------------

  /** membershipOf(gpid): your current membership of a team definition (a project of yours, kind membership), or null. */
  membershipOf(gpid) {
    return this.app.projects.list.find((p) => p.kind === 'membership' && +p.teamRef === +gpid && !['deleting', 'archived'].includes(p.state)) || null;
  }
  /** formerOf(gpid): the membership you left or were removed from (archived): Work on this takes it up again. */
  formerOf(gpid) {
    return this.app.projects.list.find((p) => p.kind === 'membership' && +p.teamRef === +gpid && p.state === 'archived') || null;
  }
  /** definitionOf(pv): a membership's definition, as the list has it, or null. */
  definitionOf(pv) { return pv && pv.teamRef ? this.app.projects.find(pv.teamRef) : null; }

  /** canWork(pv): may "Work on this" be offered — a team definition seen from your own space, no current membership (an archived one is taken up again). */
  canWork(pv) { return !!pv && pv.kind === 'team' && pv.state === 'active' && partitionState() === 'user' && !this.membershipOf(pv.id); }

  /** workManagers(): the sandbox managers bound in your own space (a new sandbox's). */
  workManagers() {
    this.app.sbx.ensure('', '');
    return ((this.app.sbx.listAt('') || {}).managers || []).filter((m) => m.ok !== false);
  }
  /** seedProvider(pv): the provider of the definition's seed when a manager of
   * yours serves it — a new sandbox may then be left to the backend, which
   * forks the seed where that works for you — else ''. */
  seedProvider(pv) {
    const ref = (pv && pv.sandboxRef) || '';
    const i = ref.lastIndexOf('|');
    const prov = i > 0 ? ref.slice(0, i) : '';
    return prov && this.workManagers().some((m) => m.provider === prov) ? prov : '';
  }

  work(gpid) { return this.works.get(+gpid) || null; }
  startWork(gpid) {
    this.works.set(+gpid, { sandbox: { mode: 'auto', ref: '', provider: '' }, defHash: '', definition: null, err: '', busy: false, note: '' });
    this.changed();
  }
  setWork(gpid, k, v) { const w = this.work(gpid); if (w) { w.sandbox = { ...w.sandbox, [k]: v }; this.changed(); } }
  closeWork(gpid) { this.works.delete(+gpid); this.changed(); }

  // workSandbox(gpid): the body's sandbox — {ref} one of yours; nothing
  // when the definition's seed is served here (the backend makes a new one
  // from the seed's manager, forking the seed where that works for you);
  // else {new: {provider}}, the manager picked (the backend refuses a
  // membership with neither). {error} when there is no way to one.
  workSandbox(gpid) {
    const w = this.work(gpid);
    if (w.sandbox.mode === 'pick') return w.sandbox.ref ? { sandbox: { ref: w.sandbox.ref } } : { error: 'Pick a sandbox, or let one be made.' };
    if (this.seedProvider(this.app.projects.find(gpid))) return {};
    const ms = this.workManagers();
    const prov = ms.some((m) => m.provider === w.sandbox.provider) ? w.sandbox.provider : (ms[0] || {}).provider;
    if (prov) return { sandbox: { new: { provider: prov } } };
    if (this.formerOf(gpid)) return {}; // taken up again: it keeps its own sandbox
    return { error: 'No sandbox manager is bound in your space to make one: pick one of your own sandboxes.' };
  }

  // submitWork: POST /memberships in your own space. The first try sends no
  // accepted hash: the backend answers 409 with the definition's security
  // part and its hash, which the view shows — the member accepts exactly
  // that (a second 409: it changed meanwhile, shown again).
  async submitWork(gpid) {
    const w = this.work(gpid);
    if (!w) return null;
    const sb = this.workSandbox(gpid);
    if (sb.error) { w.err = sb.error; this.changed(); return null; }
    w.busy = true; w.err = ''; w.note = '';
    this.changed();
    const body = { team: +gpid, accept: w.defHash || '', ...sb };
    try {
      const r = await projCall('', '/memberships', 'POST', body);
      this.works.delete(+gpid);
      await this.app.projects.load();
      if (r && r.project) await this.app.projects.open(r.project.id);
      return r;
    } catch (e) {
      const d = e.data && typeof e.data === 'object' ? e.data : null;
      if (e.status === 409 && d && d.defHash) {
        const moved = !!w.defHash && w.defHash !== d.defHash;
        w.defHash = d.defHash; w.definition = d.definition || null;
        w.note = moved ? 'The team project\'s definition changed meanwhile — read it again before you accept it.' : '';
      } else w.err = e.message;
      w.busy = false;
      this.changed();
      return null;
    }
  }

  // --- the team's changes waiting for you ---------------------------------------------------

  review(pid) { return this.reviews.get(+pid) || null; }

  // loadReview: GET /memberships/{pid}/pending — what you accepted and what
  // the definition has now ({pending: null} when nothing waits); `forHash`
  // the membership's defPending it was read for (read once per hash).
  async loadReview(pid, forHash = '') {
    pid = +pid;
    const cur = this.review(pid) || {};
    this.reviews.set(pid, { ...cur, loading: true, for: forHash });
    this.changed();
    try {
      const r = await this.app.projects.pending(pid);
      this.reviews.set(pid, { hash: (r && r.hash) || '', accepted: (r && r.accepted) || null, pending: (r && r.pending) || null,
        loading: false, err: '', busy: false, note: cur.note || '', for: forHash });
    } catch (e) { this.reviews.set(pid, { ...cur, loading: false, busy: false, err: e.message, for: forHash }); }
    this.changed();
  }

  /** ensureReview(pv): a membership's pending changes, read once per pending
   * hash — and once each time its page opens, which re-reads the definition
   * (the backend's GET …/pending does) — null when none wait. */
  ensureReview(pv) {
    if (!pv || pv.kind !== 'membership') return null;
    const pid = +pv.id;
    if (this.due.has(pid)) { this.due.delete(pid); if (pv.state === 'active') this.syncOpen(pid, pv.defPending || ''); }
    const r = this.review(pid);
    if (pv.defPending && !this.syncing.has(pid) && (!r || (r.for !== pv.defPending && !r.loading && !r.busy))) this.loadReview(pid, pv.defPending);
    const cur = this.review(pid);
    return cur && (pv.defPending || cur.pending) ? cur : null;
  }

  // syncOpen: the definition re-read as the page opens; when what waits is
  // not what the page had, the membership is read again (its defPending).
  async syncOpen(pid, had) {
    this.syncing.add(pid);
    try {
      await this.loadReview(pid, had);
      const r = this.review(pid);
      if (r && !r.err && (r.hash || '') !== had) { r.for = r.hash || ''; await this.app.projects.refresh(pid); }
    } finally { this.syncing.delete(pid); this.changed(); }
  }

  /** acceptReview(pid): adopt exactly the pending part shown (its hash); a 409 reads it again. */
  async acceptReview(pid) {
    const r = this.review(pid);
    if (!r || !r.hash) return null;
    r.busy = true; r.err = '';
    this.changed();
    try {
      const out = await this.app.projects.accept(pid, r.hash);
      this.reviews.delete(+pid);
      this.app.projects.flash = 'Accepted — your half of the team project runs the team\'s changes from now on.';
      this.changed();
      return out;
    } catch (e) {
      r.busy = false;
      if (e.status === 409) { r.note = 'The team project\'s definition changed again — here it is as it is now.'; await this.loadReview(pid, r.for); } else { r.err = e.message; this.changed(); }
      return null;
    }
  }

  // --- a team project's definition from your own space -------------------------------------------

  /** teamBody(p, f): the new-project form as a team project's definition (made in the shared space). */
  teamBody(p, f = p.form) {
    if (!f) return { error: 'no form' };
    // the sandbox part is the form's own business: a definition made here has none
    const { body, error } = p.formBody({ ...f, sandbox: { mode: 'new', provider: 'none' } });
    if (error) return { error };
    const { sandbox: _s, share: _sh, ...rest } = body; // its seed sandbox is set at the shared space, if ever: a sandbox of your own space is not there
    return { body: { ...rest, kind: 'team', share: { visibility: f.share.visibility === 'team' ? 'team' : 'private', teamRole: f.share.teamRole === 'participant' ? 'participant' : 'viewer', members: [] } } };
  }

  async saveTeam(p) {
    const f = p.form;
    const { body, error } = this.teamBody(p, f);
    if (error) { f.err = error; p.changed(); return null; }
    f.busy = true; f.err = '';
    p.changed();
    try {
      const r = await p.create(body);
      p.form = null;
      if (r && r.project) await p.open(r.project.id);
      return r;
    } catch (e) { f.err = e.message; f.busy = false; p.changed(); return null; }
  }

  // --- the stream -----------------------------------------------------------------------------------

  take(ev) {
    if (!ev || ev.type !== 'project') return;
    const d = ev.data || {};
    const pid = +d.id;
    if (!pid) return;
    if (d.change === 'deleted') { this.boards.delete(pid); this.reviews.delete(pid); return; }
    if (d.change === 'board' && this.boards.has(pid) && !this.boardT.has(pid)) { // coalesced per definition
      this.boardT.set(pid, setTimeout(() => { this.boardT.delete(pid); if (this.boards.has(pid)) this.load(pid); }, 300));
    }
    if (d.change === 'project' && this.reviews.has(pid)) this.reviews.delete(pid); // its pending part is read again when shown
  }
}

/** projectTeam(app): the app's team-project store, made on first use. */
export function projectTeam(app) {
  if (!app.projTeam) app.projTeam = new Team(app);
  return app.projTeam;
}
